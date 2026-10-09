package interview

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestResetLegacyPracticesOnly(t *testing.T) {
	db, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "reset.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for id, raw := range map[string]string{"old-practice": `{"id":"old-practice","plan":{"questions":[]}}`, "ordinary-chat": `{"id":"ordinary-chat","plan":null}`, "other-kind": `{"id":"other-kind","plan":{}}`} {
		kind := coachDocumentKind
		if id == "other-kind" {
			kind = "other"
		}
		if _, err = db.db.ExecContext(t.Context(), "INSERT INTO training_documents(kind,id,version,payload) VALUES(?,?,1,?)", kind, id, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	count, err := db.ResetLegacyPractices(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("reset: %d %v", count, err)
	}
	if count, err = db.ResetLegacyPractices(t.Context()); err != nil || count != 0 {
		t.Fatal("repeated reset not idempotent")
	}
	var retained int
	if err = db.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM training_documents").Scan(&retained); err != nil || retained != 2 {
		t.Fatal("unrelated records removed")
	}
}

func newPageFixture(t *testing.T, db *SQLiteStore, id string, duplicate bool) {
	t.Helper()
	questions := []json.RawMessage{
		json.RawMessage(`{"id":"q1","text":"第一题","reference":"第一题完整依据"}`),
		json.RawMessage(`{"id":"q2","text":"第二题","reference":"第二题完整依据"}`),
	}
	if duplicate {
		questions[1] = questions[0]
	}
	original := storedPage{PageStorage: true, ID: id, Profile: LocalProfileID, Version: 1, Created: time.Now().UTC(), Plan: &storedPagePlan{Questions: questions}}
	data, _ := json.Marshal(original)
	err := db.createPracticePages(t.Context(), data)
	if duplicate {
		if err == nil {
			t.Fatal("duplicate question committed")
		}
		var n int
		if err = db.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM practice_sessions WHERE id=?", id).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial creation was retained", err)
		}
	} else if err != nil {
		t.Fatal(err)
	}
}
func TestPracticePagePersistenceIsolationAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "independent.db")
	db, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	newPageFixture(t, db, "round-a", false)
	first, v1, err := db.PracticePage(t.Context(), "round-a", "q1")
	if err != nil {
		t.Fatal(err)
	}
	second, v2, err := db.PracticePage(t.Context(), "round-a", "q2")
	if err != nil {
		t.Fatal(err)
	}
	var p storedPage
	if err = json.Unmarshal(second, &p); err != nil {
		t.Fatal(err)
	}
	p.Messages = append(p.Messages, json.RawMessage(`{"id":"saved-answer","role":"user","content":"第二题的完整作答","questionId":"q2"}`))
	p.Version++
	if err = db.SaveTrainingDocument(t.Context(), coachDocumentKind, p.ID, p, v2); err != nil {
		t.Fatal(err)
	}
	after, vAfter, err := db.PracticePage(t.Context(), "round-a", "q1")
	if err != nil || vAfter != v1 || !bytes.Equal(first, after) {
		t.Fatal("other page changed", err)
	}
	newPageFixture(t, db, "duplicate-round", true)
	// Neither a different profile nor a changed question binding can update a page.
	p.Profile = "foreign-profile"
	p.Version++
	if err = db.SaveTrainingDocument(t.Context(), coachDocumentKind, p.ID, p, v2+1); !errors.Is(err, ErrStoreConflict) {
		t.Fatal("foreign profile saved", err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	restored, _, err := db.PracticePage(t.Context(), "round-a", "q2")
	if err != nil || !bytes.Contains(restored, []byte("第二题的完整作答")) {
		t.Fatal("saved answer missing after reopen", err)
	}
}
func TestPracticePageForeignKeysOnReplacementConnections(t *testing.T) {
	db, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	newPageFixture(t, db, "round", false)
	db.db.SetMaxIdleConns(0)
	var enabled int
	if err = db.db.QueryRowContext(t.Context(), "PRAGMA foreign_keys").Scan(&enabled); err != nil || enabled != 1 {
		t.Fatal("replacement connection lacks foreign keys", err)
	}
	if err = db.DeletePractice(t.Context(), "round", 1); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM practice_pages WHERE practice_id='round'").Scan(&n); err != nil || n != 0 {
		t.Fatal("page deletion did not cascade", err)
	}
}
func TestLegacyResetDetachesSourcesAndRollsBack(t *testing.T) {
	db, err := OpenSQLiteStore(filepath.Join(t.TempDir(), "reset-links.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	old := json.RawMessage(`{"id":"legacy","profileId":"local-default","plan":{"questions":[]}}`)
	if _, err = db.db.ExecContext(t.Context(), "INSERT INTO training_documents(kind,id,version,payload) VALUES(?,?,1,?)", coachDocumentKind, "legacy", old); err != nil {
		t.Fatal(err)
	}
	newPageFixture(t, db, "new-round", false)
	if _, err = db.db.ExecContext(t.Context(), "UPDATE practice_sessions SET metadata=json_set(metadata,'$.sourceConversation','legacy') WHERE id='new-round'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.ExecContext(t.Context(), `CREATE TRIGGER prevent_reset BEFORE DELETE ON training_documents BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ResetLegacyPractices(t.Context()); err == nil {
		t.Fatal("failed delete committed")
	}
	info, err := db.PracticeInfo(t.Context(), "new-round")
	if err != nil || info.Source != "legacy" {
		t.Fatal("source detached despite rolled back delete", err)
	}
	if _, err = db.db.ExecContext(t.Context(), "DROP TRIGGER prevent_reset"); err != nil {
		t.Fatal(err)
	}
	n, err := db.ResetLegacyPractices(t.Context())
	if err != nil || n != 1 {
		t.Fatal("reset failed", n, err)
	}
	info, err = db.PracticeInfo(t.Context(), "new-round")
	if err != nil || info.Source != "" || len(info.Pages) != 2 {
		t.Fatal("new pages lost or stale source retained", err)
	}
}
