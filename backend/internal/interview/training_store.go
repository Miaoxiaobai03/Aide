package interview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// LocalProfileID is an installation-local profile, not an online user identity.
const LocalProfileID = "local-default"

type HistoryWarning struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

type trainingRepository interface {
	NextTrainingSequence(context.Context) (int64, error)
	HistorySessions(context.Context) ([]InterviewSession, []HistoryWarning, error)
	TrainingDocuments(context.Context, string) (map[string]json.RawMessage, error)
	LoadTrainingDocument(context.Context, string, string) (json.RawMessage, int64, error)
	SaveTrainingDocument(context.Context, string, string, any, int64) error
}

func (s *SQLiteStore) NextTrainingSequence(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, "INSERT INTO training_sequence DEFAULT VALUES")
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// Source snapshots remain the only authoritative interview records. Rebuilding
// a history projection cannot lose committed answers or require model calls.
func (s *SQLiteStore) HistorySessions(ctx context.Context) ([]InterviewSession, []HistoryWarning, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, version, snapshot_json FROM interview_sessions")
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	result := []InterviewSession{}
	warnings := []HistoryWarning{}
	for rows.Next() {
		var id string
		var version int64
		var data []byte
		if err := rows.Scan(&id, &version, &data); err != nil {
			return nil, nil, err
		}
		record, err := unmarshalSQLiteSession(data)
		if err != nil || record.ID != id || record.Version != version {
			warnings = append(warnings, HistoryWarning{ID: id, Reason: "原始快照损坏或版本不一致，未纳入统计"})
			continue
		}
		result = append(result, record)
	}
	return result, warnings, rows.Err()
}

func (s *SQLiteStore) TrainingDocuments(ctx context.Context, kind string) (map[string]json.RawMessage, error) {
	return s.trainingDocuments(ctx, kind, true)
}

// Listing chat headers must not reconstruct every practice transcript.
func (s *SQLiteStore) ListTrainingDocuments(ctx context.Context, kind string) (map[string]json.RawMessage, error) {
	return s.trainingDocuments(ctx, kind, false)
}
func (s *SQLiteStore) trainingDocuments(ctx context.Context, kind string, includePractices bool) (map[string]json.RawMessage, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, payload FROM training_documents WHERE kind = ?", kind)
	if err != nil {
		return nil, err
	}
	result := map[string]json.RawMessage{}
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		if !json.Valid(data) {
			return nil, fmt.Errorf("corrupt training document: %s", id)
		}
		result[id] = data
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if includePractices && kind == coachDocumentKind {
		if err = s.addPracticeDocuments(ctx, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *SQLiteStore) LoadTrainingDocument(ctx context.Context, kind, id string) (json.RawMessage, int64, error) {
	var data []byte
	var version int64
	err := s.db.QueryRowContext(ctx, "SELECT payload, version FROM training_documents WHERE kind = ? AND id = ?", kind, id).Scan(&data, &version)
	if errors.Is(err, sql.ErrNoRows) {
		if kind == coachDocumentKind {
			err = s.db.QueryRowContext(ctx, "SELECT payload,version FROM practice_pages WHERE page_id=?", id).Scan(&data, &version)
			if err == nil {
				return data, version, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return nil, 0, err
			}
			return s.practiceAggregate(ctx, id)
		}
		return nil, 0, ErrStoreNotFound
	}
	return data, version, err
}

// expected=0 creates; later writes use CAS so concurrent tabs cannot silently
// overwrite plans, human corrections, or attempt records.
func (s *SQLiteStore) SaveTrainingDocument(ctx context.Context, kind, id string, value any, expected int64) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if kind == coachDocumentKind {
		var p struct {
			PracticeID  string `json:"practiceId"`
			PageStorage bool   `json:"pageStorage"`
		}
		if err = json.Unmarshal(data, &p); err != nil {
			return err
		}
		if p.PracticeID != "" {
			return s.savePracticePage(ctx, data, expected)
		}
		if p.PageStorage && expected == 0 {
			return s.createPracticePages(ctx, data)
		}
	}
	var result sql.Result
	if expected == 0 {
		result, err = insertTrainingDocument(ctx, s.db, kind, id, data)
	} else {
		result, err = s.db.ExecContext(ctx, "UPDATE training_documents SET version=version+1,payload=? WHERE kind=? AND id=? AND version=?", data, kind, id, expected)
	}
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrStoreConflict
	}
	return nil
}
