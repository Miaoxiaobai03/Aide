package interview

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func historyFixture(id string, legacy bool) InterviewSession {
	ref := EvidenceRef{SourceID: "kb", Kind: SourceKnowledge, AnchorID: "kb:1", Locator: "Q1", Quote: "解释事务隔离"}
	r := InterviewSession{ID: id, LocalProfileID: LocalProfileID, ScoringVersion: "assessment-v1", Version: 1, State: StateCompleted, StartedAt: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), Config: InterviewConfig{Focus: FocusKnowledge, FeedbackMode: FeedbackImmediate}, Sources: SourceIndex{Documents: map[string]SourceDocument{}, Anchors: map[string]SourceAnchor{"kb:1": {ID: "kb:1", SourceID: "kb", Kind: SourceKnowledge, Locator: "Q1", Text: ref.Quote}}, Order: []string{"kb:1"}}, Profile: Profile{Coverage: []CoveragePoint{{ID: "coverage1", Area: FocusKnowledge, Label: "事务", EvidenceRefs: []EvidenceRef{ref}}}}}
	r.Answers = []AnswerRecord{{Question: Question{ID: "q1", RootID: "root1", Text: "解释事务隔离", Kind: QuestionKnowledge, Difficulty: DifficultyMedium, CoveragePointID: "coverage1", EvidenceRefs: []EvidenceRef{ref}}, Answer: AnswerPayload{Text: "我的原始回答", InputMode: InputModeText}, Assessment: Assessment{Correctness: 2, Depth: 2, Specificity: 3, Ownership: 3, Metrics: 3, Tradeoffs: 2, Gaps: []string{"未解释隔离边界"}, EvidenceRefs: []EvidenceRef{ref}}, AnsweredAt: r.StartedAt}}
	r.Report = &Report{Summary: "原始报告", Turns: r.Answers}
	if legacy {
		r.LocalProfileID = ""
		r.ScoringVersion = ""
	}
	return r
}

func TestHistoryRestartImportPaginationAndStats(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []InterviewSession{historyFixture("a", false), historyFixture("b", true), historyFixture("c", false)} {
		if err := store.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	s := NewService(Dependencies{Store: store})
	page, err := s.History(ctx, HistoryQuery{Limit: 1})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "c" || len(page.Unassigned) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if n, err := s.ImportHistory(ctx, []string{"b"}); err != nil || n != 1 {
		t.Fatalf("import %d %v", n, err)
	}
	if n, err := s.ImportHistory(ctx, []string{"b", "b"}); err != nil || n != 0 {
		t.Fatalf("reimport %d %v", n, err)
	}
	page, err = s.History(ctx, HistoryQuery{Limit: 1, Cursor: page.NextCursor})
	if err != nil || page.Items[0].ID != "b" {
		t.Fatalf("second=%+v err=%v", page, err)
	}
	stats, err := s.TrainingStats(ctx)
	if err != nil || stats.Sessions != 3 || stats.Completed != 3 || stats.Evaluated != 3 || len(stats.Groups) != 2 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(Dependencies{Store: store})
	detail, err := s.HistoryDetail(ctx, "b")
	if err != nil || detail.Report.Summary != "原始报告" || detail.Snapshot.Turns[0].Answer.Text != "我的原始回答" {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	for _, q := range []HistoryQuery{{Limit: -1}, {Cursor: "bad"}, {State: "abandoned"}, {Kind: "invalid"}, {Area: "fake"}} {
		if _, err := s.History(ctx, q); !IsCode(err, CodeValidation) {
			t.Fatalf("query=%+v err=%v", q, err)
		}
	}
	if _, err := s.HistoryDetail(ctx, "missing"); !IsCode(err, CodeNotFound) {
		t.Fatalf("missing=%v", err)
	}
}

func TestHistoryDeferredCorruptionAndFailedWrite(t *testing.T) {
	ctx := context.Background()
	store := openTestSQLiteStore(t)
	r := historyFixture("deferred", false)
	r.State = StateAwaitingAnswer
	r.Config.FeedbackMode = FeedbackDeferred
	if err := store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("INSERT INTO interview_sessions VALUES('broken',1,'not json',0,0)"); err != nil {
		t.Fatal(err)
	}
	s := NewService(Dependencies{Store: store})
	page, err := s.History(ctx, HistoryQuery{})
	if err != nil || len(page.Warnings) != 1 || page.Items[0].Evaluated != 0 {
		t.Fatalf("page=%+v %v", page, err)
	}
	detail, err := s.HistoryDetail(ctx, r.ID)
	if err != nil || detail.Report != nil || !detail.Snapshot.Turns[0].Feedback.Deferred {
		t.Fatalf("deferred=%+v %v", detail, err)
	}
	review, err := s.Review(ctx, r.ID)
	if err != nil || !review.Turns[0].Feedback.Deferred || len(review.Turns[0].References) != 0 {
		t.Fatalf("review=%+v %v", review, err)
	}
	stats, err := s.TrainingStats(ctx)
	if err != nil || stats.Evaluated != 0 || stats.Completed != 0 || stats.HiddenEvaluations != 1 || len(stats.Groups) != 0 {
		t.Fatalf("stats=%+v %v", stats, err)
	}
	if _, err := store.db.Exec("PRAGMA query_only=ON"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTrainingDocument(ctx, "ownership", "x", LocalProfileID, 0); err == nil {
		t.Fatal("read-only write reported success")
	}
	if _, err := store.db.Exec("PRAGMA query_only=OFF"); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryInvalidEvaluationNotZeroAndUnknownTimePagination(t *testing.T) {
	ctx := context.Background()
	store := openTestSQLiteStore(t)
	r := historyFixture("invalid-evaluation", false)
	r.StartedAt = time.Time{}
	r.Report = nil
	r.Answers[0].Assessment.EvidenceRefs[0].AnchorID = "missing"
	if err := store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	other := historyFixture("another-unknown-time", false)
	other.StartedAt = time.Time{}
	if err := store.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	s := NewService(Dependencies{Store: store})
	detail, err := s.HistoryDetail(ctx, r.ID)
	if err != nil || !detail.Snapshot.Turns[0].Feedback.Unavailable || detail.Item.Evaluated != 0 {
		t.Fatalf("invalid=%+v %v", detail, err)
	}
	stats, err := s.TrainingStats(ctx)
	if err != nil || stats.InvalidEvaluations != 1 || stats.Evaluated != 1 {
		t.Fatalf("stats=%+v %v", stats, err)
	}
	first, err := s.History(ctx, HistoryQuery{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.History(ctx, HistoryQuery{Limit: 1, Cursor: first.NextCursor})
	if err != nil || len(second.Items) != 1 || first.Items[0].ID == second.Items[0].ID {
		t.Fatalf("unknown date pagination=%+v %v", second, err)
	}
}
