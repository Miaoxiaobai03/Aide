package interview

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestPagedDeleteRollbackCascadeAndSpaceReuse(t *testing.T) {
	db, e := OpenSQLiteStore(filepath.Join(t.TempDir(), "cascade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	newPageFixture(t, db, "source", false)
	newPageFixture(t, db, "independent", false)
	db.db.ExecContext(t.Context(), `UPDATE practice_sessions SET metadata=json_set(metadata,'$.sourceConversation','source') WHERE id='independent'`)
	raw, v, _ := db.PracticePage(t.Context(), "source", "q1")
	var p storedPage
	json.Unmarshal(raw, &p)
	p.Messages = append(p.Messages, json.RawMessage(`{"id":"raw","content":"`+strings.Repeat("完整原文", 40000)+`"}`))
	p.Summaries = []json.RawMessage{json.RawMessage(`{"id":"note","sourceIds":["raw"]}`)}
	p.Runs = []json.RawMessage{json.RawMessage(`{"id":"run"}`)}
	p.CompressionChecks = []json.RawMessage{json.RawMessage(`{"key":"failed"}`)}
	p.Plan.Attempts = []json.RawMessage{json.RawMessage(`{"id":"answer","gradingHistory":[{"revision":1}]}`)}
	p.Version++
	if e = db.SaveTrainingDocument(t.Context(), coachDocumentKind, p.ID, p, v); e != nil {
		t.Fatal(e)
	}
	if e = db.DeletePractice(t.Context(), "source", 99); !errors.Is(e, ErrStoreConflict) {
		t.Fatal("stale deletion accepted", e)
	}
	db.db.ExecContext(t.Context(), `CREATE TRIGGER block_delete BEFORE DELETE ON practice_sessions WHEN OLD.id='source' BEGIN SELECT RAISE(ABORT,'fixture rollback'); END`)
	if e = db.DeletePractice(t.Context(), "source", 1); e == nil {
		t.Fatal("failed transaction accepted")
	}
	info, _ := db.PracticeInfo(t.Context(), "independent")
	if info.Source != "source" || info.Version != 1 {
		t.Fatal("source unlink escaped rollback")
	}
	if _, _, e = db.PracticePage(t.Context(), "source", "q1"); e != nil {
		t.Fatal("failed deletion removed page")
	}
	db.db.ExecContext(t.Context(), `DROP TRIGGER block_delete`)
	var before, after, free int
	db.db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&before)
	if e = db.DeletePractice(t.Context(), "source", 1); e != nil {
		t.Fatal(e)
	}
	if e = db.DeletePractice(t.Context(), "source", 1); e != nil {
		t.Fatal("repeat not idempotent", e)
	}
	for _, table := range []string{"practice_sessions", "practice_pages"} {
		var n int
		col := "id"
		if table == "practice_pages" {
			col = "practice_id"
		}
		if e = db.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE "+col+"='source'").Scan(&n); e != nil || n != 0 {
			t.Fatal("delete residual", table, n, e)
		}
	}
	if e = db.SaveTrainingDocument(t.Context(), coachDocumentKind, p.ID, p, v+1); !errors.Is(e, ErrStoreConflict) {
		t.Fatal("late save resurrected page", e)
	}
	info, _ = db.PracticeInfo(t.Context(), "independent")
	if info.Source != "" || info.Version != 2 || len(info.Pages) != 2 {
		t.Fatal("independent retest damaged or still linked")
	}
	db.db.QueryRowContext(t.Context(), "PRAGMA freelist_count").Scan(&free)
	if free == 0 {
		t.Fatal("deleted pages not reusable")
	}
	for i := 0; i < 10; i++ {
		newPageFixture(t, db, "reused", false)
		raw, v, _ = db.PracticePage(t.Context(), "reused", "q1")
		var again storedPage
		json.Unmarshal(raw, &again)
		again.Messages = p.Messages
		again.Summaries = p.Summaries
		again.Runs = p.Runs
		again.CompressionChecks = p.CompressionChecks
		again.Plan.Attempts = p.Plan.Attempts
		again.Version++
		if e = db.SaveTrainingDocument(t.Context(), coachDocumentKind, again.ID, again, v); e != nil {
			t.Fatal(e)
		}
		if e = db.DeletePractice(t.Context(), "reused", 1); e != nil {
			t.Fatal(e)
		}
	}
	db.db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&after)
	if after > before+3 {
		t.Fatal("SQLite grew without reuse", before, after)
	}
	newPageFixture(t, db, "foreign", false)
	db.db.ExecContext(t.Context(), `UPDATE practice_sessions SET profile='foreign-profile' WHERE id='foreign'`)
	db.DeletePractice(t.Context(), "foreign", 1)
	var n int
	db.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM practice_sessions WHERE id='foreign'`).Scan(&n)
	if n != 1 {
		t.Fatal("foreign profile deleted")
	}
	t.Logf("source and children residual=0 late_write_recreated=0 SQLite before=%d after_10_cycles=%d free_pages=%d", before, after, free)
}
