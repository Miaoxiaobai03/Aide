package interview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type retestTestAgent struct {
	*scriptedAgent
	mu                           sync.Mutex
	generated, assessed, checked int
	failGenerationAt             int
	malformed                    bool
	rejectTask                   bool
	uncertain                    bool
	started                      chan struct{}
	release                      chan struct{}
	generationStarted            chan struct{}
	generationRelease            chan struct{}
}

func (a *retestTestAgent) GenerateRetest(_ context.Context, r RetestQuestionRequest) (RetestQuestionDraft, error) {
	a.mu.Lock()
	a.generated++
	n := a.generated
	fail := a.failGenerationAt == n
	a.mu.Unlock()
	if a.generationStarted != nil && n == 1 {
		close(a.generationStarted)
		<-a.generationRelease
	}
	if fail {
		return RetestQuestionDraft{}, errors.New("timeout")
	}
	ids := []string{}
	for _, t := range r.Targets {
		ids = append(ids, t.ID)
	}
	return RetestQuestionDraft{Question: QuestionDraft{Text: fmt.Sprintf("情境%d中如何解释事务隔离与边界？", n), EvidenceRefs: evidenceForAnchors(r.Anchors)}, Difficulty: r.Original.Difficulty, TargetIDs: ids, BindingReason: "同一隔离考点的边界验证", Answerable: true}, nil
}
func (a *retestTestAgent) ValidateRetest(_ context.Context, r RetestAssessmentRequest) (RetestValidation, error) {
	a.mu.Lock()
	a.checked++
	reject := a.rejectTask
	a.mu.Unlock()
	ids := []string{}
	for _, t := range r.Targets {
		ids = append(ids, t.ID)
	}
	return RetestValidation{Valid: !reject, TargetIDs: ids, Reason: "任务与所选考点及依据一致", EvidenceRefs: evidenceForAnchors(r.Anchors)}, nil
}
func (a *retestTestAgent) AssessRetest(_ context.Context, r RetestAssessmentRequest) (RetestEvaluation, error) {
	a.mu.Lock()
	a.assessed++
	n := a.assessed
	malformed := a.malformed
	uncertain := a.uncertain
	started, release := a.started, a.release
	a.mu.Unlock()
	if started != nil && n == 1 {
		close(started)
		<-release
	}
	e := RetestEvaluation{Assessment: Assessment{Correctness: 2, Depth: 2, Specificity: 3, Ownership: 3, Metrics: 3, Tradeoffs: 2, EvidenceRefs: evidenceForAnchors(r.Anchors)}, Results: []CriterionResult{}}
	for _, target := range r.Targets {
		for _, c := range target.Criteria {
			status := "unmet"
			quote := ""
			if strings.Contains(r.Answer.Text, "改善") {
				status = "met"
				quote = "改善"
				e.Assessment.Correctness = 4
			}
			if uncertain {
				status = "unassessed"
			}
			e.Results = append(e.Results, CriterionResult{TargetID: target.ID, Criterion: c, Status: status, Reason: "对照该考点与回答原文进行检查", AnswerQuote: quote})
		}
	}
	if malformed {
		e.Results = nil
	}
	return e, nil
}
func retestSetup(t *testing.T) (*SQLiteStore, *Service, *retestTestAgent) {
	t.Helper()
	store := openTestSQLiteStore(t)
	if err := store.Create(context.Background(), historyFixture("source", false)); err != nil {
		t.Fatal(err)
	}
	agent := &retestTestAgent{scriptedAgent: groundedAgent()}
	s := NewService(Dependencies{Store: store, Agent: agent})
	return store, s, agent
}
func confirmTargets(t *testing.T, s *Service, source string) []string {
	t.Helper()
	page, err := s.Weaknesses(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, w := range page.Items {
		if _, err := s.EditWeakness(context.Background(), RetestMutation{SourceID: source, ID: w.ID, Label: w.Label, Criteria: w.Criteria, Status: "confirmed", Revision: w.Revision}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, w.ID)
	}
	return ids
}
func createPrepared(t *testing.T, s *Service, id, mode string, ids []string) RetestPlan {
	t.Helper()
	ctx := context.Background()
	p, err := s.CreateRetest(ctx, RetestMutation{PlanID: id, SourceID: "source", Mode: mode, IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.PrepareRetest(ctx, p.ID, 0)
	if err != nil || p.Status != "ready" {
		t.Fatalf("prepare=%+v %v", p, err)
	}
	return p
}
func submitFixture(plan RetestPlan, id, text string) RetestMutation {
	return RetestMutation{PlanID: plan.ID, TaskID: plan.Tasks[0].ID, AttemptID: id, Answer: AnswerPayload{Text: text, InputMode: InputModeText}}
}

func TestRetestThreeAttemptsRestartStateAndComparisons(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "retest.db")
	store, err := OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Create(ctx, historyFixture("source", false)); err != nil {
		t.Fatal(err)
	}
	agent := &retestTestAgent{scriptedAgent: groundedAgent()}
	s := NewService(Dependencies{Store: store, Agent: agent})
	ids := confirmTargets(t, s, "source")
	for i, text := range []string{"仍然答错", "已经改善", "再次答错"} {
		p := createPrepared(t, s, fmt.Sprintf("plan%d", i), "original", ids)
		p, err = s.SubmitRetest(ctx, submitFixture(p, fmt.Sprintf("attempt%d", i), text))
		if err != nil || p.Status != "completed" {
			t.Fatalf("submit=%+v %v", p, err)
		}
	}
	page, err := s.Weaknesses(ctx, "source")
	if err != nil || page.Items[0].Status != "needs_practice" {
		t.Fatalf("state=%+v %v", page, err)
	}
	observations, err := s.WeaknessAttempts(ctx, "source", ids[0])
	if err != nil || len(observations) != 4 {
		t.Fatalf("attempts=%+v %v", observations, err)
	}
	comparison, err := s.CompareAttempts(ctx, "source", ids[0], "attempt0", "attempt1")
	if err != nil || !comparison.ScoreComparable || comparison.ScoreDelta == nil {
		t.Fatalf("compare=%+v %v", comparison, err)
	}
	comparison, err = s.CompareAttempts(ctx, "source", ids[0], "initial-q1", "attempt1")
	if err != nil || comparison.ScoreComparable {
		t.Fatalf("original comparison=%+v %v", comparison, err)
	}
	stats, err := s.TrainingStats(ctx)
	if err != nil || stats.Sessions != 4 || stats.Evaluated != 4 {
		t.Fatalf("stats=%+v %v", stats, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s = NewService(Dependencies{Store: store})
	p, err := s.RetestPlan(ctx, "plan1")
	if err != nil || p.Attempts[0].Answer.Text != "已经改善" || p.Status != "completed" {
		t.Fatalf("restart=%+v %v", p, err)
	}
	history, err := s.History(ctx, HistoryQuery{Kind: "retest"})
	if err != nil || len(history.Items) != 3 {
		t.Fatalf("history=%+v %v", history, err)
	}
	if _, err := s.PrepareRetest(ctx, "plan1", 0); !IsCode(err, CodeUnavailable) && err != nil {
		t.Fatal(err)
	}
}
func TestRetestFailedEvaluationRetryAndIdempotency(t *testing.T) {
	_, s, agent := retestSetup(t)
	ctx := context.Background()
	ids := confirmTargets(t, s, "source")
	p := createPrepared(t, s, "retry-plan", "variant", ids)
	agent.malformed = true
	input := submitFixture(p, "retry-attempt", "输入保留")
	p, err := s.SubmitRetest(ctx, input)
	if err != nil || p.Attempts[0].Status != "failed" || p.Attempts[0].Evaluation != nil || p.Status == "completed" || agent.assessed != 2 {
		t.Fatalf("failure=%+v %v", p, err)
	}
	page, _ := s.Weaknesses(ctx, "source")
	if page.Items[0].Status != "confirmed" {
		t.Fatalf("failure altered mastery=%+v", page)
	}
	stats, _ := s.TrainingStats(ctx)
	if stats.Evaluated != 1 || stats.RetestFailures != 1 {
		t.Fatalf("failure stats=%+v", stats)
	}
	agent.malformed = false
	input.Retry = true
	p, err = s.SubmitRetest(ctx, input)
	if err != nil || p.Status != "completed" || len(p.Attempts) != 1 {
		t.Fatalf("retry=%+v %v", p, err)
	}
	n := agent.assessed
	p, err = s.SubmitRetest(ctx, input)
	if err != nil || agent.assessed != n || len(p.Attempts) != 1 {
		t.Fatal("duplicate reevaluated")
	}
	input.Answer.Text = "changed"
	if _, err := s.SubmitRetest(ctx, input); !IsCode(err, CodeConflict) {
		t.Fatalf("conflict=%v", err)
	}
	agent.uncertain = true
	p = createPrepared(t, s, "uncertain-plan", "original", ids)
	p, err = s.SubmitRetest(ctx, submitFixture(p, "uncertain-attempt", "改善"))
	if err != nil {
		t.Fatal(err)
	}
	page, _ = s.Weaknesses(ctx, "source")
	if page.Items[0].Status != "needs_practice" {
		t.Fatalf("uncertain overwrote mastery=%+v", page)
	}
}
func TestRetestBatchCoveragePartialPreparationAndOriginalMerge(t *testing.T) {
	store, s, agent := retestSetup(t)
	ctx := context.Background()
	r, _ := store.Load(ctx, "source")
	r.Answers[0].Assessment.Gaps = []string{}
	for i := 0; i < 21; i++ {
		r.Answers[0].Assessment.Gaps = append(r.Answers[0].Assessment.Gaps, fmt.Sprintf("隔离边界第%d项未解释", i))
	}
	r.Version++
	if err := store.Save(ctx, r, 1); err != nil {
		t.Fatal(err)
	}
	ids := confirmTargets(t, s, "source")
	p, err := s.CreateRetest(ctx, RetestMutation{PlanID: "many", SourceID: "source", Mode: "variant", IDs: ids})
	if err != nil || len(p.Tasks) != 21 || p.Tasks[20].Batch != 1 {
		t.Fatalf("batch=%+v %v", p, err)
	}
	agent.failGenerationAt = 6
	p, err = s.PrepareRetest(ctx, p.ID, 0)
	if err != nil || p.Status != "preparation_failed" || p.Tasks[0].Status != "ready" || p.Tasks[5].Status != "failed" {
		t.Fatalf("partial=%+v %v", p, err)
	}
	first := p.Tasks[0].Question.Text
	if _, err := s.SubmitRetest(ctx, submitFixture(p, "early", "答错")); !IsCode(err, CodeConflict) {
		t.Fatalf("early=%v", err)
	}
	agent.failGenerationAt = 0
	p, err = s.PrepareRetest(ctx, p.ID, 0)
	if err != nil || p.Status != "ready" || p.Tasks[0].Question.Text != first || p.Tasks[20].Status != "pending" {
		t.Fatalf("retry preparation=%+v %v", p, err)
	}
	p, err = s.PrepareRetest(ctx, p.ID, 1)
	if err != nil || p.Tasks[20].Status != "ready" {
		t.Fatalf("second batch=%+v %v", p, err)
	}
	merged, err := s.CreateRetest(ctx, RetestMutation{PlanID: "merged", SourceID: "source", Mode: "original", IDs: ids})
	if err != nil || len(merged.Tasks) != 1 || len(merged.Tasks[0].Targets) != 21 {
		t.Fatalf("merge=%+v %v", merged, err)
	}
	if _, err := s.CreateRetest(ctx, RetestMutation{PlanID: "empty", SourceID: "source", Mode: "variant"}); !IsCode(err, CodeValidation) {
		t.Fatalf("empty=%v", err)
	}
	if _, err := s.CreateRetest(ctx, RetestMutation{PlanID: "invalid", SourceID: "source", Mode: "variant", IDs: []string{"wrong"}}); !IsCode(err, CodeValidation) {
		t.Fatalf("invalid=%v", err)
	}
	agent.rejectTask = true
	p, err = s.CreateRetest(ctx, RetestMutation{PlanID: "unrelated", SourceID: "source", Mode: "variant", IDs: ids[:1]})
	if err != nil {
		t.Fatal(err)
	}
	p, err = s.PrepareRetest(ctx, p.ID, 0)
	if err != nil || p.Status != "preparation_failed" {
		t.Fatalf("unrelated=%+v %v", p, err)
	}
}
func TestRetestConcurrentDuplicatePauseAndLeaseRecovery(t *testing.T) {
	_, s, agent := retestSetup(t)
	ctx := context.Background()
	ids := confirmTargets(t, s, "source")
	p := createPrepared(t, s, "concurrent", "original", ids)
	agent.started = make(chan struct{})
	agent.release = make(chan struct{})
	input := submitFixture(p, "concurrent-attempt", "改善")
	done := make(chan error, 1)
	go func() { _, err := s.SubmitRetest(ctx, input); done <- err }()
	<-agent.started
	running, err := s.SubmitRetest(ctx, input)
	if err != nil || len(running.Attempts) != 1 || running.Attempts[0].Status != "running" {
		t.Fatalf("running=%+v %v", running, err)
	}
	if _, err := s.PauseRetest(ctx, p.ID, true); err != nil {
		t.Fatal(err)
	}
	close(agent.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	p, err = s.RetestPlan(ctx, p.ID)
	if err != nil || p.Status != "paused" || p.Attempts[0].Status != "succeeded" {
		t.Fatalf("late=%+v %v", p, err)
	}
	p, err = s.PauseRetest(ctx, p.ID, false)
	if err != nil || p.Status != "completed" {
		t.Fatalf("resume=%+v %v", p, err)
	}
	p = createPrepared(t, s, "expired", "original", ids)
	internal, _ := s.loadPlan(ctx, p.ID)
	internal.Attempts = []RetestAttempt{{ID: "expired-attempt", TaskID: p.Tasks[0].ID, Answer: AnswerPayload{Text: "改善", InputMode: InputModeText}, Status: "running", CreatedAt: s.clock.Now(), LeaseUntil: s.clock.Now().Add(-time.Minute)}}
	if err := s.savePlan(ctx, &internal); err != nil {
		t.Fatal(err)
	}
	input = submitFixture(p, "expired-attempt", "改善")
	input.Retry = true
	p, err = s.SubmitRetest(ctx, input)
	if err != nil || p.Attempts[0].Status != "succeeded" || len(p.Attempts) != 1 {
		t.Fatalf("lease=%+v %v", p, err)
	}
}
func TestRetestUnavailableModelRetainsInputAndDuplicateCriteriaRejected(t *testing.T) {
	store, s, _ := retestSetup(t)
	ctx := context.Background()
	ids := confirmTargets(t, s, "source")
	p := createPrepared(t, s, "no-model-plan", "original", ids)
	unconfigured := NewService(Dependencies{Store: store})
	input := submitFixture(p, "no-model-answer", "用户输入不能丢失")
	p, err := unconfigured.SubmitRetest(ctx, input)
	if err != nil || len(p.Attempts) != 1 || p.Attempts[0].Status != "failed" || p.Attempts[0].Answer.Text != input.Answer.Text || p.Attempts[0].Evaluation != nil {
		t.Fatalf("unconfigured=%+v %v", p, err)
	}
	page, err := s.Weaknesses(ctx, "source")
	if err != nil {
		t.Fatal(err)
	}
	w := page.Items[0]
	if _, err := s.EditWeakness(ctx, RetestMutation{SourceID: "source", ID: w.ID, Label: w.Label, Criteria: []string{"重复标准", "重复标准"}, Status: "confirmed", Revision: w.Revision}); !IsCode(err, CodeValidation) {
		t.Fatalf("duplicate criteria=%v", err)
	}
}

func TestRetestBackgroundQueueAndOlderResult(t *testing.T) {
	_, s, agent := retestSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ids := confirmTargets(t, s, "source")
	p, err := s.CreateRetest(ctx, RetestMutation{PlanID: "queued-plan", SourceID: "source", Mode: "variant", IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	agent.generationStarted = make(chan struct{})
	agent.generationRelease = make(chan struct{})
	p, err = s.QueueRetest(ctx, p.ID, 0)
	if err != nil || p.Status != "queued" {
		t.Fatalf("queue=%+v %v", p, err)
	}
	select {
	case <-agent.generationStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	duplicate, err := s.QueueRetest(ctx, p.ID, 0)
	if err != nil || duplicate.Status != "preparing" {
		t.Fatalf("duplicate queue=%+v %v", duplicate, err)
	}
	cancel()
	close(agent.generationRelease)
	deadline := time.Now().Add(3 * time.Second)
	for {
		p, err = s.RetestPlan(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker did not finish: %+v", p)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// An older plan's result may arrive after a newer passing observation.
	old := createPrepared(t, s, "old-result", "original", ids)
	agent.started = make(chan struct{})
	agent.release = make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, e := s.SubmitRetest(context.Background(), submitFixture(old, "older-attempt", "答错"))
		done <- e
	}()
	<-agent.started
	newer := createPrepared(t, s, "new-result", "original", ids)
	if _, err := s.SubmitRetest(context.Background(), submitFixture(newer, "newer-attempt", "改善")); err != nil {
		t.Fatal(err)
	}
	close(agent.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	page, err := s.Weaknesses(context.Background(), "source")
	if err != nil || page.Items[0].Status != "passed_once" {
		observations, _ := s.allObservations(context.Background())
		for _, o := range observations {
			t.Logf("observation %s %s %v", o.Attempt.ID, o.Attempt.CreatedAt, o.Attempt.Evaluation)
		}
		t.Fatalf("older result overwrote newer=%+v %v", page, err)
	}
}

func TestWeaknessDedupCorrectionsAndFrozenCriteria(t *testing.T) {
	store, s, _ := retestSetup(t)
	ctx := context.Background()
	r, _ := store.Load(ctx, "source")
	r.Answers[0].Assessment.Gaps = []string{"未解释隔离边界", "没有解释隔离边界", "个人职责不清", "经历无法核实"}
	r.Version++
	if err := store.Save(ctx, r, 1); err != nil {
		t.Fatal(err)
	}
	page, err := s.Weaknesses(ctx, "source")
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Sources) != 2 {
		t.Fatalf("dedup=%+v %v", page, err)
	}
	ids := confirmTargets(t, s, "source")
	p := createPrepared(t, s, "frozen", "original", ids)
	old := p.Tasks[0].CriteriaVersion
	page, _ = s.Weaknesses(ctx, "source")
	w := page.Items[0]
	input := RetestMutation{SourceID: "source", ID: w.ID, Label: "修正后的隔离问题", Criteria: []string{"解释隔离级别与具体异常"}, Status: "confirmed", Revision: w.Revision}
	if _, err := s.EditWeakness(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EditWeakness(ctx, input); !IsCode(err, CodeConflict) {
		t.Fatalf("revision conflict=%v", err)
	}
	p, err = s.RetestPlan(ctx, p.ID)
	if err != nil || p.Tasks[0].CriteriaVersion != old || p.Tasks[0].Targets[0].Label == input.Label {
		t.Fatalf("freeze=%+v %v", p, err)
	}
	newPlan := createPrepared(t, s, "changed-criteria", "original", ids)
	p, err = s.SubmitRetest(ctx, submitFixture(p, "before-change", "改善"))
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Weaknesses(ctx, "source")
	if err != nil || current.Items[0].Status != "confirmed" {
		t.Fatalf("old standard overwrote corrected target=%+v %v", current, err)
	}
	newPlan, err = s.SubmitRetest(ctx, submitFixture(newPlan, "after-change", "改善"))
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := s.CompareAttempts(ctx, "source", ids[0], "before-change", "after-change")
	if err != nil || comparison.ScoreComparable {
		t.Fatalf("different criteria=%+v %v", comparison, err)
	}
	page, _ = s.Weaknesses(ctx, "source")
	w = page.Items[0]
	if _, err := s.EditWeakness(ctx, RetestMutation{SourceID: "source", ID: w.ID, Label: w.Label, Criteria: w.Criteria, Status: "not_applicable", Revision: w.Revision}); err != nil {
		t.Fatal(err)
	}
	page, _ = s.Weaknesses(ctx, "source")
	if page.Items[0].Status != "not_applicable" {
		t.Fatal("rebuild lost correction")
	}
	public, _ := json.Marshal(p)
	if strings.Contains(string(public), "frozenSource") || strings.Contains(string(public), "sourceContents") {
		t.Fatalf("private snapshot exposed: %s", public)
	}
}
