package interview

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoachPhysicalDeleteReusesSQLitePagesAndChecksOwnership(t *testing.T) {
	s, e := OpenSQLiteStore(filepath.Join(t.TempDir(), "delete.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	value := map[string]any{"id": "owned", "profileId": LocalProfileID, "version": 1, "messages": strings.Repeat("deleted-original-data", 20000)}
	if e = s.SaveTrainingDocument(t.Context(), coachDocumentKind, "owned", value, 0); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteTrainingConversation(t.Context(), coachDocumentKind, "owned", 1, "another-profile"); !errors.Is(e, ErrStoreNotFound) {
		t.Fatal("foreign profile deleted", e)
	}
	if e = s.DeleteTrainingConversation(t.Context(), coachDocumentKind, "owned", 2, LocalProfileID); !errors.Is(e, ErrStoreConflict) {
		t.Fatal("stale transaction accepted", e)
	}
	var beforePages, afterPages, freePages int
	if e = s.db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&beforePages); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteTrainingConversation(t.Context(), coachDocumentKind, "owned", 1, LocalProfileID); e != nil {
		t.Fatal(e)
	}
	if e = s.db.QueryRowContext(t.Context(), "PRAGMA freelist_count").Scan(&freePages); e != nil || freePages == 0 {
		t.Fatal("large deleted record did not free reusable pages", e)
	}
	// Newly inserted sessions reuse free pages; no permanent tombstone rows.
	for i := 0; i < 10; i++ {
		if e = s.SaveTrainingDocument(t.Context(), coachDocumentKind, "owned", value, 0); e != nil {
			t.Fatal(e)
		}
		if e = s.DeleteTrainingConversation(t.Context(), coachDocumentKind, "owned", 1, LocalProfileID); e != nil {
			t.Fatal(e)
		}
	}
	if e = s.db.QueryRowContext(t.Context(), "PRAGMA page_count").Scan(&afterPages); e != nil {
		t.Fatal(e)
	}
	if afterPages > beforePages+2 {
		t.Fatal("same-size create/delete grew main DB", beforePages, afterPages)
	}
	docs, e := s.TrainingDocuments(t.Context(), coachDocumentKind)
	if e != nil || len(docs) != 0 {
		t.Fatal("deleted document or tombstone retained", e)
	}
	t.Logf("synthetic DB pages before=%d after_10_cycles=%d reusable_after_delete=%d; owned rows=0", beforePages, afterPages, freePages)
}
