package interview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrPracticeBusy = errors.New("practice already has an active operation")

// A practice owns lightweight navigation metadata. Each page owns its complete
// single-question conversation; navigating never loads the other transcripts.
type PracticePageInfo struct {
	ID         string `json:"id"`
	QuestionID string `json:"questionId"`
	Ordinal    int    `json:"ordinal"`
	Question   string `json:"question"`
	State      string `json:"state"`
	Viewed     bool   `json:"viewedAnswer"`
	Graded     bool   `json:"graded"`
	Visited    bool   `json:"visited"`
	Busy       bool   `json:"busy"`
}
type PracticeInfo struct {
	Runs      []json.RawMessage  `json:"runs,omitempty"`
	ID        string             `json:"id"`
	Profile   string             `json:"profileId"`
	Title     string             `json:"title"`
	Version   int64              `json:"version"`
	Created   time.Time          `json:"createdAt"`
	Updated   time.Time          `json:"updatedAt"`
	Ended     bool               `json:"ended"`
	Source    string             `json:"sourceConversation,omitempty"`
	Pages     []PracticePageInfo `json:"pages"`
	Completed int                `json:"completed"`
}
type storedPage struct {
	PageStorage       bool              `json:"pageStorage,omitempty"`
	ID                string            `json:"id"`
	PracticeID        string            `json:"practiceId"`
	PageOrdinal       int               `json:"pageOrdinal"`
	Profile           string            `json:"profileId"`
	Mode              string            `json:"mode"`
	Title             string            `json:"title"`
	Version           int64             `json:"version"`
	Created           time.Time         `json:"createdAt"`
	Updated           time.Time         `json:"updatedAt"`
	Messages          []json.RawMessage `json:"messages"`
	Summaries         []json.RawMessage `json:"summaries"`
	Runs              []json.RawMessage `json:"runs"`
	Pending           json.RawMessage   `json:"pending,omitempty"`
	CompressionChecks []json.RawMessage `json:"compressionChecks,omitempty"`
	Plan              *storedPagePlan   `json:"plan"`
}
type storedPagePlan struct {
	Questions    []json.RawMessage `json:"questions"`
	Current      int               `json:"current"`
	Discussion   int               `json:"discussionOrdinal,omitempty"`
	Status       string            `json:"status"`
	Taught       map[int]bool      `json:"taught"`
	Viewed       map[int]bool      `json:"viewedAnswers"`
	Skipped      map[int]bool      `json:"skipped,omitempty"`
	Attempts     []json.RawMessage `json:"attempts"`
	Source       string            `json:"sourceConversation,omitempty"`
	PageIsolated bool              `json:"pageIsolated,omitempty"`
}

func pageBrief(p storedPage) PracticePageInfo {
	var q struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(p.Plan.Questions[0], &q)
	info := PracticePageInfo{ID: p.ID, QuestionID: q.ID, Ordinal: p.PageOrdinal, Question: q.Text, State: p.Plan.Status, Viewed: p.Plan.Viewed[1], Busy: len(p.Pending) > 0 && string(p.Pending) != "null"}
	for _, raw := range p.Plan.Attempts {
		var a struct {
			Status     string          `json:"status"`
			Evaluation json.RawMessage `json:"evaluation"`
		}
		_ = json.Unmarshal(raw, &a)
		if a.Status == "succeeded" && len(a.Evaluation) > 0 && string(a.Evaluation) != "null" {
			info.Graded = true
		}
	}
	return info
}
func (s *SQLiteStore) createPracticePages(ctx context.Context, data []byte) error {
	var original storedPage
	if err := json.Unmarshal(data, &original); err != nil {
		return err
	}
	if original.Profile != LocalProfileID || original.Version != 1 || original.Plan == nil || len(original.Plan.Questions) == 0 {
		return ErrStoreConflict
	}
	info := PracticeInfo{ID: original.ID, Profile: original.Profile, Title: original.Title, Version: original.Version, Created: original.Created, Updated: original.Updated, Source: original.Plan.Source, Pages: []PracticePageInfo{}}
	metadata, _ := json.Marshal(info)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if info.Source != "" {
		var found int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM practice_sessions WHERE id=? AND profile=?", info.Source, info.Profile).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return ErrStoreNotFound
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO practice_sessions(id,profile,version,metadata) VALUES(?,?,?,?)", info.ID, info.Profile, info.Version, metadata); err != nil {
		return err
	}
	for i, q := range original.Plan.Questions {
		var question struct {
			ID   string `json:"id"`
			Text string `json:"text"`
		}
		if err = json.Unmarshal(q, &question); err != nil || question.ID == "" {
			return fmt.Errorf("invalid practice question")
		}
		page := storedPage{ID: info.ID + "::" + question.ID, PracticeID: info.ID, PageOrdinal: i + 1, Profile: info.Profile, Mode: "practice", Title: question.Text, Version: 1, Created: info.Created, Updated: info.Updated, Messages: []json.RawMessage{}, Summaries: []json.RawMessage{}, Runs: info.Runs, Plan: &storedPagePlan{Questions: []json.RawMessage{q}, Status: "awaiting_answer", Taught: map[int]bool{}, Viewed: map[int]bool{}, Attempts: []json.RawMessage{}, PageIsolated: true}}
		for _, raw := range original.Messages {
			var m map[string]any
			_ = json.Unmarshal(raw, &m)
			if ordinal, ok := m["ordinal"].(float64); ok && int(ordinal) == i+1 {
				m["ordinal"] = 1
				m["seq"] = len(page.Messages) + 1
				raw, _ = json.Marshal(m)
				page.Messages = append(page.Messages, raw)
			}
		}
		if len(page.Messages) == 0 {
			msg := map[string]any{"id": page.ID + "::question", "seq": 1, "revision": 1, "role": "assistant", "kind": "question", "content": question.Text, "status": "complete", "ordinal": 1, "questionId": question.ID, "at": info.Created}
			raw, _ := json.Marshal(msg)
			page.Messages = append(page.Messages, raw)
		}
		payload, _ := json.Marshal(page)
		summary, _ := json.Marshal(pageBrief(page))
		if _, err = tx.ExecContext(ctx, "INSERT INTO practice_pages(page_id,practice_id,question_id,ordinal,version,payload,summary,visited) VALUES(?,?,?,?,?,?,?,?)", page.ID, info.ID, question.ID, i+1, 1, payload, summary, i == 0); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *SQLiteStore) savePracticePage(ctx context.Context, data []byte, expected int64) error {
	var p storedPage
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if p.Profile != LocalProfileID || p.Version != expected+1 || p.Plan == nil || !p.Plan.PageIsolated || len(p.Plan.Questions) != 1 || p.PracticeID == "" || p.PageOrdinal < 1 || expected < 1 {
		return ErrStoreConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if len(p.Pending) > 0 && string(p.Pending) != "null" {
		var others int
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM practice_pages WHERE practice_id=? AND page_id<>? AND json_extract(payload,'$.pending.id') IS NOT NULL AND julianday(json_extract(payload,'$.pending.until'))>julianday(?)", p.PracticeID, p.ID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&others)
		if err != nil {
			return err
		}
		if others > 0 {
			return ErrPracticeBusy
		}
	}
	var question struct {
		ID string `json:"id"`
	}
	if err = json.Unmarshal(p.Plan.Questions[0], &question); err != nil || question.ID == "" {
		return ErrStoreConflict
	}
	summary, _ := json.Marshal(pageBrief(p))
	result, err := tx.ExecContext(ctx, "UPDATE practice_pages SET version=version+1,payload=?,summary=? WHERE page_id=? AND practice_id=? AND question_id=? AND ordinal=? AND version=? AND EXISTS(SELECT 1 FROM practice_sessions WHERE id=? AND profile=?)", data, summary, p.ID, p.PracticeID, question.ID, p.PageOrdinal, expected, p.PracticeID, p.Profile)
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
	if _, err = tx.ExecContext(ctx, "UPDATE practice_sessions SET metadata=json_set(metadata,'$.updatedAt',?) WHERE id=?", p.Updated.Format(time.RFC3339Nano), p.PracticeID); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *SQLiteStore) PracticeInfo(ctx context.Context, id string) (PracticeInfo, error) {
	var raw []byte
	var v int64
	err := s.db.QueryRowContext(ctx, "SELECT metadata,version FROM practice_sessions WHERE id=? AND profile=?", id, LocalProfileID).Scan(&raw, &v)
	if errors.Is(err, sql.ErrNoRows) {
		return PracticeInfo{}, ErrStoreNotFound
	}
	if err != nil {
		return PracticeInfo{}, err
	}
	var info PracticeInfo
	if err = json.Unmarshal(raw, &info); err != nil {
		return info, err
	}
	info.Version = v
	info.Pages = []PracticePageInfo{}
	rows, err := s.db.QueryContext(ctx, "SELECT summary,visited FROM practice_pages WHERE practice_id=? ORDER BY ordinal", id)
	if err != nil {
		return info, err
	}
	defer rows.Close()
	for rows.Next() {
		var brief []byte
		var visited bool
		if err = rows.Scan(&brief, &visited); err != nil {
			return info, err
		}
		var p PracticePageInfo
		if err = json.Unmarshal(brief, &p); err != nil {
			return info, err
		}
		p.Visited = visited
		info.Pages = append(info.Pages, p)
		if p.Graded {
			info.Completed++
		}
	}
	return info, rows.Err()
}
func (s *SQLiteStore) PracticePage(ctx context.Context, parent, question string) (json.RawMessage, int64, error) {
	var raw []byte
	var v int64
	err := s.db.QueryRowContext(ctx, "SELECT p.payload,p.version FROM practice_pages p JOIN practice_sessions s ON s.id=p.practice_id WHERE p.practice_id=? AND p.question_id=? AND s.profile=?", parent, question, LocalProfileID).Scan(&raw, &v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrStoreNotFound
	}
	return raw, v, err
}
func (s *SQLiteStore) VisitPracticePage(ctx context.Context, parent, question string) error {
	r, err := s.db.ExecContext(ctx, "UPDATE practice_pages SET visited=1 WHERE practice_id=? AND question_id=? AND EXISTS(SELECT 1 FROM practice_sessions WHERE id=? AND profile=?)", parent, question, parent, LocalProfileID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrStoreNotFound
	}
	return nil
}
func (s *SQLiteStore) EndPractice(ctx context.Context, id string, expected int64) error {
	if expected < 1 {
		return ErrStoreConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var busy int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM practice_pages WHERE practice_id=? AND json_extract(payload,'$.pending.id') IS NOT NULL AND julianday(json_extract(payload,'$.pending.until'))>julianday(?)", id, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&busy); err != nil {
		return err
	}
	if busy > 0 {
		return ErrPracticeBusy
	}
	r, err := tx.ExecContext(ctx, "UPDATE practice_sessions SET version=version+1,metadata=json_set(metadata,'$.ended',json('true'),'$.version',version+1) WHERE id=? AND profile=? AND version=?", id, LocalProfileID, expected)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return ErrStoreConflict
	}
	return tx.Commit()
}
func (s *SQLiteStore) DeletePractice(ctx context.Context, id string, expected int64) error {
	if expected < 1 {
		return ErrStoreConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var v int64
	err = tx.QueryRowContext(ctx, "SELECT version FROM practice_sessions WHERE id=? AND profile=?", id, LocalProfileID).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	if v != expected {
		return ErrStoreConflict
	}
	if _, err = tx.ExecContext(ctx, "UPDATE practice_sessions SET version=version+1, metadata=json_set(json_remove(metadata,'$.sourceConversation'),'$.version',version+1,'$.updatedAt',?) WHERE profile=? AND json_extract(metadata,'$.sourceConversation')=?", time.Now().UTC().Format(time.RFC3339Nano), LocalProfileID, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM practice_sessions WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetLegacyPractices is an explicit one-time user action, never a startup hook.
func (s *SQLiteStore) ResetLegacyPractices(ctx context.Context) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	// Detach surviving new practices before removing legacy source records.
	_, err = tx.ExecContext(ctx, `UPDATE practice_sessions
 SET metadata=json_remove(metadata,'$.sourceConversation')
 WHERE json_extract(metadata,'$.sourceConversation') IN
 (SELECT id FROM training_documents WHERE kind=? AND json_valid(payload) AND json_type(payload,'$.plan')='object')`, coachDocumentKind)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM training_documents WHERE kind=? AND json_valid(payload) AND json_type(payload,'$.plan')='object'", coachDocumentKind)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// Full aggregation is used only for reports/statistics/retest, not page chat.
func (s *SQLiteStore) practiceAggregate(ctx context.Context, id string) (json.RawMessage, int64, error) {
	info, err := s.PracticeInfo(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	whole := storedPage{PageStorage: true, ID: info.ID, Profile: info.Profile, Mode: "practice", Title: info.Title, Version: info.Version, Created: info.Created, Updated: info.Updated, Messages: []json.RawMessage{}, Summaries: []json.RawMessage{}, Runs: info.Runs, Plan: &storedPagePlan{Questions: []json.RawMessage{}, Status: "awaiting_answer", Taught: map[int]bool{}, Viewed: map[int]bool{}, Skipped: map[int]bool{}, Attempts: []json.RawMessage{}, Source: info.Source}}
	if info.Ended {
		whole.Plan.Status = "completed"
	}
	for _, brief := range info.Pages {
		raw, _, e := s.PracticePage(ctx, id, brief.QuestionID)
		if e != nil {
			return nil, 0, e
		}
		var p storedPage
		if e = json.Unmarshal(raw, &p); e != nil {
			return nil, 0, e
		}
		whole.Plan.Questions = append(whole.Plan.Questions, p.Plan.Questions[0])
		whole.Plan.Taught[brief.Ordinal] = p.Plan.Taught[1]
		whole.Plan.Viewed[brief.Ordinal] = p.Plan.Viewed[1]
		whole.Plan.Skipped[brief.Ordinal] = !brief.Graded
		for _, raw := range p.Plan.Attempts {
			var a map[string]any
			_ = json.Unmarshal(raw, &a)
			a["ordinal"] = brief.Ordinal
			raw, _ = json.Marshal(a)
			whole.Plan.Attempts = append(whole.Plan.Attempts, raw)
		}
	}
	raw, err := json.Marshal(whole)
	return raw, info.Version, err
}
func (s *SQLiteStore) addPracticeDocuments(ctx context.Context, documents map[string]json.RawMessage) error {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM practice_sessions WHERE profile=?", LocalProfileID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		raw, _, e := s.practiceAggregate(ctx, id)
		if e != nil {
			return e
		}
		documents[id] = raw
	}
	return nil
}

func (s *SQLiteStore) RecordPracticeRun(ctx context.Context, id string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE practice_sessions SET metadata=json_set(metadata,'$.runs',json_insert(coalesce(json_extract(metadata,'$.runs'),'[]'),'$[#]',json(?))) WHERE id=? AND profile=?", raw, id, LocalProfileID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrStoreNotFound
	}
	return nil
}

// PracticeDirectories loads metadata and lightweight page summaries only.
func (s *SQLiteStore) PracticeDirectories(ctx context.Context) ([]PracticeInfo, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM practice_sessions WHERE profile=?", LocalProfileID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []PracticeInfo{}
	for _, id := range ids {
		info, err := s.PracticeInfo(ctx, id)
		if errors.Is(err, ErrStoreNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		info.Runs = nil
		result = append(result, info)
	}
	return result, nil
}
