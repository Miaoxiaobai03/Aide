package interview

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const coachDocumentKind = "coach-conversation-v1"

// DeleteTrainingConversation removes the owning JSON aggregate, including raw
// question groups, grades, summary/check caches and runs, in one transaction.
// Independent retest sessions survive with their old source pointers removed.
// Missing IDs are idempotent; a stale version on an existing ID is a conflict.
func (s *SQLiteStore) DeleteTrainingConversation(ctx context.Context, kind, id string, expected int64, profile string) error {
	if kind != coachDocumentKind {
		return ErrStoreConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw []byte
	var version int64
	err = tx.QueryRowContext(ctx, "SELECT payload,version FROM training_documents WHERE kind=? AND id=?", kind, id).Scan(&raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	var owner struct {
		Profile string `json:"profileId"`
	}
	if json.Unmarshal(raw, &owner) != nil || owner.Profile != profile {
		return ErrStoreNotFound
	}
	if version != expected {
		return ErrStoreConflict
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,payload,version FROM training_documents WHERE kind=? AND id<>?", kind, id)
	if err != nil {
		return err
	}
	type update struct {
		id      string
		data    []byte
		version int64
	}
	updates := []update{}
	for rows.Next() {
		var key string
		var data []byte
		var v int64
		if err = rows.Scan(&key, &data, &v); err != nil {
			rows.Close()
			return err
		}
		var doc map[string]any
		if json.Unmarshal(data, &doc) != nil {
			rows.Close()
			return ErrStoreConflict
		}
		plan, _ := doc["plan"].(map[string]any)
		if plan["sourceConversation"] == id {
			delete(plan, "sourceConversation")
			doc["version"] = v + 1
			doc["updatedAt"] = time.Now().UTC()
			encoded, e := json.Marshal(doc)
			if e != nil {
				rows.Close()
				return e
			}
			updates = append(updates, update{key, encoded, v})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, u := range updates {
		result, e := tx.ExecContext(ctx, "UPDATE training_documents SET payload=?,version=version+1 WHERE kind=? AND id=? AND version=?", u.data, kind, u.id, u.version)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrStoreConflict
		}
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM training_documents WHERE kind=? AND id=? AND version=?", kind, id, expected)
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
	return tx.Commit()
}

// A new derived practice cannot be inserted after its source was deleted.
// Using INSERT ... SELECT ties the existence check to the same SQL statement;
// SQLite serializes it against whole-session deletion without a load/save race.
func insertTrainingDocument(ctx context.Context, db *sql.DB, kind, id string, data []byte) (sql.Result, error) {
	var doc struct {
		Profile string `json:"profileId"`
		Plan    *struct {
			Source string `json:"sourceConversation"`
		} `json:"plan"`
	}
	if kind == coachDocumentKind && json.Unmarshal(data, &doc) == nil && doc.Plan != nil && doc.Plan.Source != "" {
		return db.ExecContext(ctx, `INSERT INTO training_documents(kind,id,version,payload)
		 SELECT ?,?,1,? WHERE EXISTS (SELECT 1 FROM training_documents WHERE kind=? AND id=? AND json_extract(payload,'$.profileId')=? AND coalesce(json_extract(payload,'$.deleted'),0)=0)
		 ON CONFLICT(kind,id) DO NOTHING`, kind, id, data, kind, doc.Plan.Source, doc.Profile)
	}
	return db.ExecContext(ctx, "INSERT INTO training_documents(kind,id,version,payload) VALUES(?,?,1,?) ON CONFLICT(kind,id) DO NOTHING", kind, id, data)
}
