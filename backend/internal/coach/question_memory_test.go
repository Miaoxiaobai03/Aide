package coach

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"aide/backend/internal/chat"
	"aide/backend/internal/interview"
)

func tenQuestionFixture(t *testing.T) (*Service, *fixtureModel, Conversation, string) {
	t.Helper()
	s, m, path := setup(t)
	s.config.InputCap = 30000
	s.config.Windows = map[string]int{"test": 100000}
	base, e := s.StartPractice(t.Context(), 1)
	if e != nil {
		t.Fatal(e)
	}
	questions := []Question{}
	for i := 1; i <= 10; i++ {
		q := base.Plan.Questions[0]
		q.ID = fmt.Sprintf("question-%d", i)
		q.Text = fmt.Sprintf("第%d项系统设计知识", i)
		q.Hash = ""
		q.Hash = Hash(q)
		questions = append(questions, q)
	}
	c, e := s.createPlan(t.Context(), questions, "")
	if e != nil {
		t.Fatal(e)
	}
	for n := 1; n <= 10; n++ {
		c, e = s.Learn(t.Context(), c.ID, n)
		if e != nil {
			t.Fatal(e)
		}
		c, e = s.update(t.Context(), c.ID, func(c *Conversation) error {
			appendMessage(c, "user", "followup", fmt.Sprintf("UNIQUE_Q%d_RAW_", n)+strings.Repeat("具体知识解释。", 2000), "complete", fmt.Sprintf("turn-%d", n), n)
			appendMessage(c, "assistant", "teaching", fmt.Sprintf("UNIQUE_Q%d_REPLY_", n)+strings.Repeat("本题知识边界。", 2000), "complete", fmt.Sprintf("turn-%d", n), n)
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
		if n < 10 {
			c, e = s.Next(t.Context(), c.ID, n, false)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	return s, m, c, path
}

// Q-A1/Q-A2: nine old originals are large, but only question ten plus capsules
// enter working context. Restart restores every raw question, including ten.
func TestQuestionMemoryTenArchivesAndRestart(t *testing.T) {
	s, m, c, path := tenQuestionFixture(t)
	if len(c.QuestionGroups) != 10 || c.QuestionGroups[9].Archived {
		t.Fatal("last question was not persisted as active")
	}
	for i, g := range c.QuestionGroups {
		if g.Index == "" || g.SessionID != c.ID || g.QuestionID != c.Plan.Questions[i].ID || len(g.MessageIDs) < 3 || len(g.Notes) != 2 {
			t.Fatalf("missing group %d: %+v", i, g)
		}
	}
	// Remove artificial long current exchange, keeping it fully archived in a
	// separate fixture below; this case verifies the normal ten-question scope.
	c, e := s.update(t.Context(), c.ID, func(c *Conversation) error {
		for i := range c.Messages {
			if c.Messages[i].Ordinal == 10 && c.Messages[i].SubmissionID == "turn-10" {
				c.Messages[i].Content = "CURRENT_TEN_EXCHANGE"
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.BuildContext(t.Context(), c.ID, Input{Message: "继续解释当前题", SubmissionID: "check"}, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	joined := ""
	for _, msg := range p.Messages {
		joined += msg.Content
	}
	if p.Run.Budget != 30000 || p.Run.InputEstimate > 30000 || len(m.calls) != 0 {
		t.Fatal("wrong cap or switch incurred model costs", p.Run)
	}
	if strings.Contains(joined, strings.Repeat("具体知识解释。", 50)) {
		t.Fatal("old raw history leaked into working context")
	}
	for n := 1; n <= 9; n++ {
		if !strings.Contains(joined, fmt.Sprintf(`"questionId":"question-%d"`, n)) {
			t.Fatalf("archive index %d absent", n)
		}
	}
	repo, e := interview.OpenSQLiteStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer repo.Close()
	restarted := New(repo, m, s.knowledge, s.config)
	restored, e := restarted.Load(t.Context(), c.ID)
	if e != nil || Hash(restored.Messages) != Hash(c.Messages) || len(restored.QuestionGroups) != 10 {
		t.Fatal("restart lost originals", e)
	}
	if restored.Messages[len(restored.Messages)-1].QuestionID != "question-10" {
		t.Fatal("last question binding missing")
	}
}

// Q-A3: discussing old raw data is independent of answer progression.
func TestQuestionMemoryRevisitNeverAdvancesOrMisgrades(t *testing.T) {
	s, m, c, _ := tenQuestionFixture(t)
	c, e := s.FocusQuestion(t.Context(), c.ID, "question-3")
	if e != nil {
		t.Fatal(e)
	}
	if c.Plan.Current != 9 || discussionOrdinal(c) != 3 {
		t.Fatal("focus changed progress")
	}
	m.fn = func(msgs []chat.Message) (string, error) {
		joined := ""
		for _, msg := range msgs {
			joined += msg.Content
		}
		if !strings.Contains(joined, "UNIQUE_Q3_RAW_") || strings.Contains(joined, "UNIQUE_Q4_RAW_") && strings.Contains(joined, strings.Repeat("具体知识解释。", 50)) {
			t.Error("wrong raw question loaded")
		}
		return "继续讨论第三题，不改变答题进度。", nil
	}
	// Explicitly quote an old full source to verify loading, without making the
	// enormous synthetic raw answer a required budget-overflow in this test.
	c, e = s.update(t.Context(), c.ID, func(c *Conversation) error {
		for i := range c.Messages {
			if c.Messages[i].Ordinal == 3 {
				c.Messages[i].Content = short(c.Messages[i].Content, 80)
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	c, e = s.Generate(t.Context(), Input{ConversationID: c.ID, QuestionID: "question-3", SubmissionID: "revisit", Message: "请进一步解释", Model: "test"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if c.Plan.Current != 9 || discussionOrdinal(c) != 3 || c.Messages[len(c.Messages)-1].Ordinal != 3 {
		t.Fatal("revisit was attached to current question")
	}
	if _, e = s.Next(t.Context(), c.ID, 10, true); e == nil {
		t.Fatal("old topic allowed advancing current progress")
	}
	if _, e = s.Answer(t.Context(), Input{ConversationID: c.ID, QuestionID: "question-3", SubmissionID: "bad-bind", Message: "旧题回答"}); e == nil {
		t.Fatal("old discussion graded as current answer")
	}
	c, e = s.FocusQuestion(t.Context(), c.ID, "question-10")
	if e != nil || c.Plan.Current != 9 {
		t.Fatal(e)
	}
	n, e := resolveQuestion(c, Input{QuestionID: "question-10", Message: "回到第3题"})
	if e != nil || n != 3 {
		t.Fatal("natural return ignored UI binding", n, e)
	}
	n, e = resolveQuestion(c, Input{QuestionID: "question-10", Message: "我们回到讨论第三题"})
	if e != nil || n != 3 {
		t.Fatal("Chinese return index not recognized", n, e)
	}
}

func TestQuestionMemoryFutureAndAmbiguousReferencesRejected(t *testing.T) {
	s, m, _ := setup(t)
	c, _ := s.StartPractice(t.Context(), 2)
	if _, e := s.FocusQuestion(t.Context(), c.ID, c.Plan.Questions[1].ID); e == nil {
		t.Fatal("future reference disclosed")
	}
	if _, e := s.Generate(t.Context(), Input{ConversationID: c.ID, SubmissionID: "future", QuestionID: c.Plan.Questions[1].ID, Message: "讲解", Model: "test"}, nil); e == nil {
		t.Fatal("future request accepted")
	}
	if _, e := resolveQuestion(c, Input{Message: "回到第1题和第2题"}); e == nil {
		t.Fatal("ambiguous target accepted")
	}
	if len(m.calls) != 0 || c.Public().Plan.Questions[1].Reference != "" {
		t.Fatal("future answer/model call leaked")
	}
}

// Q-A5: physical delete clears the entire owning aggregate, unlinks independent
// retests, is repeatable, and old CAS updates cannot resurrect a deleted ID.
func TestQuestionMemoryWholeDeleteAtomicAndNoResurrection(t *testing.T) {
	s, _, c, _ := tenQuestionFixture(t)
	child, e := s.createPlan(t.Context(), c.Plan.Questions[:1], c.ID)
	if e != nil {
		t.Fatal(e)
	}
	other, e := s.Create(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Delete(t.Context(), c.ID, c.Version-1, true); !errors.Is(e, ErrConflict) {
		t.Fatal("stale delete accepted", e)
	}
	if _, e = s.Load(t.Context(), c.ID); e != nil {
		t.Fatal("failed transaction removed owner", e)
	}
	if _, e = s.Delete(t.Context(), c.ID, c.Version, false); e != nil {
		t.Fatal(e)
	} // practice never retains results
	if _, _, e = s.repo.LoadTrainingDocument(t.Context(), DocumentKind, c.ID); !errors.Is(e, interview.ErrStoreNotFound) {
		t.Fatal("owning row retained", e)
	}
	childNow, e := s.Load(t.Context(), child.ID)
	if e != nil || childNow.Plan.SourceConversation != "" || childNow.Version != child.Version+1 {
		t.Fatal("source pointer retained or child lost", e)
	}
	if _, e = s.Load(t.Context(), other.ID); e != nil {
		t.Fatal("unrelated chat deleted", e)
	}
	if _, e = s.Delete(t.Context(), c.ID, c.Version, true); e != nil {
		t.Fatal("repeat delete failed", e)
	}
	if e = s.save(t.Context(), &c); !errors.Is(e, ErrConflict) {
		t.Fatal("late CAS recreated owner", e)
	}
	if _, e = s.createPlan(t.Context(), child.Plan.Questions, c.ID); !errors.Is(e, ErrConflict) {
		t.Fatal("retest was created after source deletion", e)
	}
	items, e := s.List(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	for _, item := range items {
		if item.ID == c.ID {
			t.Fatal("deleted practice remains listed")
		}
	}
}

type deletingModel struct {
	started  chan struct{}
	canceled chan struct{}
}

func (m *deletingModel) Stream(ctx context.Context, _ string, _ []chat.Message, delta func(chat.Delta) error) (chat.Usage, error) {
	close(m.started)
	<-ctx.Done()
	close(m.canceled)
	_ = delta(chat.Delta{Text: strings.Repeat("LATE_DELETED_RESPONSE", 80)})
	return chat.Usage{}, ctx.Err()
}

func TestQuestionMemoryDeleteCancelsGenerationAndLateCallback(t *testing.T) {
	s, _, _ := setup(t)
	c, _ := s.StartPractice(t.Context(), 1)
	c, _ = s.Learn(t.Context(), c.ID, 1)
	m := &deletingModel{make(chan struct{}), make(chan struct{})}
	s.model = m
	done := make(chan error, 1)
	go func() {
		_, e := s.Generate(context.Background(), Input{ConversationID: c.ID, SubmissionID: "inflight", Message: "请解释", Model: "test"}, nil)
		done <- e
	}()
	select {
	case <-m.started:
	case <-time.After(3 * time.Second):
		t.Fatal("model never started")
	}
	current, e := s.Load(t.Context(), c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Delete(t.Context(), c.ID, current.Version, true); e != nil {
		t.Fatal(e)
	}
	select {
	case <-m.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("inflight call not canceled")
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("deleted run succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("late callback hung")
	}
	if _, _, e = s.repo.LoadTrainingDocument(t.Context(), DocumentKind, c.ID); !errors.Is(e, interview.ErrStoreNotFound) {
		t.Fatal("callback resurrected deleted data", e)
	}
}

type noGainCounter struct{}

func (noGainCounter) Name() string { return "synthetic_no_gain_not_provider_tokens" }
func (noGainCounter) Count(_ string, m []chat.Message, _ int) (int, error) {
	if len(m) == 1 || strings.Contains(m[0].Content, "教学笔记") {
		return 100, nil
	}
	for _, msg := range m {
		if strings.Contains(msg.Content, "OPTIONAL_BLOCK") || strings.Contains(msg.Content, "NO_GAIN") {
			return 13000, nil
		}
	}
	return 1000, nil
}

func TestQuestionMemoryNoGainIsCachedAcrossRequests(t *testing.T) {
	s, m, _ := setup(t)
	s.config.Counter = noGainCounter{}
	s.config.InputCap = 12000
	c, _ := s.StartPractice(t.Context(), 1)
	c, _ = s.Learn(t.Context(), c.ID, 1)
	c, e := s.update(t.Context(), c.ID, func(c *Conversation) error {
		for i := 0; i < 12; i++ {
			user := "旧问题"
			if i == 0 {
				user = "OPTIONAL_BLOCK"
			}
			appendMessage(c, "user", "followup", user, "complete", fmt.Sprintf("u%d", i), 1)
			appendMessage(c, "assistant", "teaching", "旧解释", "complete", fmt.Sprintf("u%d", i), 1)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	m.fn = func([]chat.Message) (string, error) { return `{"summary":"NO_GAIN","quotes":[]}`, nil }
	in := Input{Message: "当前问题", SubmissionID: "no-gain"}
	if _, e = s.BuildContext(t.Context(), c.ID, in, "teaching"); !errors.Is(e, ErrBudget) {
		t.Fatal("no-gain bypassed budget", e)
	}
	first := len(m.calls)
	if first < 1 || first > 2 {
		t.Fatal("wrong bounded attempt count", first)
	}
	if _, e = s.BuildContext(t.Context(), c.ID, in, "teaching"); !errors.Is(e, ErrBudget) {
		t.Fatal(e)
	}
	if len(m.calls) != first {
		t.Fatal("identical failed source was charged again")
	}
	restored, _ := s.Load(t.Context(), c.ID)
	if Hash(restored.Messages) != Hash(c.Messages) || len(restored.CompressionChecks) != first {
		t.Fatal("cache or originals missing")
	}
}

func TestQuestionMemoryMandatoryOverflowPreservesRawInput(t *testing.T) {
	s, m, _ := setup(t)
	s.config.InputCap = 30000
	s.config.Windows = nil
	c, _ := s.StartPractice(t.Context(), 1)
	c, _ = s.Learn(t.Context(), c.ID, 1)
	raw := strings.Repeat("本题完整作答", 12000)
	c, e := s.update(t.Context(), c.ID, func(c *Conversation) error {
		appendMessage(c, "user", "followup", raw, "complete", "large", 1)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BuildContext(t.Context(), c.ID, Input{Message: raw, SubmissionID: "large"}, "teaching"); !errors.Is(e, ErrBudget) {
		t.Fatal("oversized input silently truncated", e)
	}
	restored, _ := s.Load(t.Context(), c.ID)
	if restored.Messages[len(restored.Messages)-1].Content != raw || len(m.calls) != 0 {
		t.Fatal("overflow erased raw data or called model")
	}
	if _, e := parseSummary(`{"summary":"`+strings.Repeat("长", 2001)+`","quotes":[]}`, c.Messages, "test"); e == nil {
		t.Fatal("unbounded summary accepted")
	}
}

// Q-A4: real local estimator, long single-question followups, successful
// compaction, exact-source recall and byte-for-byte durable raw preservation.
func TestQuestionMemoryLongActiveQuestionCompactsAndRecalls(t *testing.T) {
	s, m, _ := setup(t)
	s.config.InputCap = 30000
	s.config.Windows = map[string]int{"test": 100000}
	c, _ := s.StartPractice(t.Context(), 1)
	c, _ = s.Learn(t.Context(), c.ID, 1)
	c, e := s.update(t.Context(), c.ID, func(c *Conversation) error {
		for i := 0; i < 20; i++ {
			appendMessage(c, "user", "followup", strings.Repeat("请解释检索方法的具体适用场景。", 30), "complete", fmt.Sprintf("long-%d", i), 1)
			appendMessage(c, "assistant", "teaching", strings.Repeat("检索方法应结合当前任务与知识结构使用。", 30), "complete", fmt.Sprintf("long-%d", i), 1)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	in := Input{Message: "继续解释", SubmissionID: "long-current"}
	before, _, e := s.projection(c, in, "teaching")
	if e != nil || before.Run.InputEstimate < 24000 {
		t.Fatal("fixture did not reach trigger", before.Run.InputEstimate, e)
	}
	m.fn = func(msgs []chat.Message) (string, error) {
		return `{"summary":"已讨论检索方法的适用场景与知识结构，用户仍在请求具体使用条件，未验证独立掌握。","quotes":[]}`, nil
	}
	p, e := s.BuildContext(t.Context(), c.ID, in, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	restored, _ := s.Load(t.Context(), c.ID)
	if len(m.calls) < 1 || len(m.calls) > 2 || p.Run.InputEstimate >= before.Run.InputEstimate || p.Run.InputEstimate > 30000 || Hash(restored.Messages) != Hash(c.Messages) {
		t.Fatal("compaction failed or modified raw facts", before.Run.InputEstimate, p.Run.InputEstimate, len(m.calls))
	}
	if len(restored.Summaries) == 0 {
		t.Fatal("derived note was not durably saved")
	}
	afterEstimate := p.Run.InputEstimate
	key := restored.Summaries[0].SourceIDs[0]
	p, _, e = s.projection(restored, Input{Message: "引用原文", SubmissionID: "exact-recall", References: []string{key}}, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, id := range p.Run.SourceIDs {
		if id == key {
			found = true
		}
	}
	if !found {
		t.Fatal("compacted original cannot be recalled")
	}
	t.Logf("synthetic active-question estimate before=%d after_compaction=%d exact_recall=%d; fake-model calls=%d; raw hash unchanged", before.Run.InputEstimate, afterEstimate, p.Run.InputEstimate, len(m.calls))
}

func TestQuestionMemoryOptionalSummariesCannotRejectFittingInput(t *testing.T) {
	s, m, _ := setup(t)
	s.config.InputCap = 30000
	s.config.Windows = nil
	c, _ := s.StartPractice(t.Context(), 1)
	c, _ = s.Learn(t.Context(), c.ID, 1)
	c, e := s.update(t.Context(), c.ID, func(c *Conversation) error {
		for i := 0; i < 3; i++ {
			u := appendMessage(c, "user", "followup", "历史疑问", "complete", fmt.Sprintf("old-%d", i), 1)
			a := appendMessage(c, "assistant", "teaching", "历史讲解", "complete", fmt.Sprintf("old-%d", i), 1)
			su, err := parseSummary(`{"summary":"`+strings.Repeat("历史知识具体说明", 240)+`","quotes":[]}`, []Message{u, a}, "test")
			if err != nil {
				return err
			}
			c.Summaries = append(c.Summaries, su)
		}
		for i := 0; i < 4; i++ {
			appendMessage(c, "user", "followup", "当前问题", "complete", fmt.Sprintf("recent-%d", i), 1)
			appendMessage(c, "assistant", "teaching", "当前解释", "complete", fmt.Sprintf("recent-%d", i), 1)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	in := Input{Message: strings.Repeat("请具体解释当前概念", 1150), SubmissionID: "near-limit"}
	lower, _, e := s.project(c, in, "teaching", true)
	if e != nil || lower.Run.InputEstimate > 30000 || lower.Run.InputEstimate < 24000 {
		t.Fatal("fixture must have fitting but large mandatory input", lower.Run.InputEstimate, e)
	}
	p, e := s.BuildContext(t.Context(), c.ID, in, "teaching")
	if e != nil || p.Run.InputEstimate > 30000 || len(m.calls) != 0 {
		t.Fatal("optional summaries caused budget rejection or unnecessary API calls", p.Run, e)
	}
	restored, _ := s.Load(t.Context(), c.ID)
	if len(restored.Summaries) != 3 || Hash(restored.Messages) != Hash(c.Messages) {
		t.Fatal("optional selection deleted durable source")
	}
}

func TestQuestionMemoryDeleteCancelsAssessment(t *testing.T) {
	s, _, _ := setup(t)
	c, _ := s.StartPractice(t.Context(), 1)
	m := &deletingModel{make(chan struct{}), make(chan struct{})}
	s.model = m
	done := make(chan error, 1)
	go func() {
		_, e := s.Answer(context.Background(), Input{ConversationID: c.ID, SubmissionID: "grading-inflight", Message: "请评估这个独立回答", Model: "test"})
		done <- e
	}()
	select {
	case <-m.started:
	case <-time.After(3 * time.Second):
		t.Fatal("assessment never started")
	}
	current, e := s.Load(t.Context(), c.ID)
	if e != nil || current.Plan.Attempts[0].Status != "evaluating" {
		t.Fatal("pending answer not durable", e)
	}
	if _, e = s.Delete(t.Context(), c.ID, current.Version, true); e != nil {
		t.Fatal(e)
	}
	select {
	case <-m.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("assessment call not canceled")
	}
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("deleted grade succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deleted assessment hung")
	}
	if _, _, e = s.repo.LoadTrainingDocument(t.Context(), DocumentKind, c.ID); !errors.Is(e, interview.ErrStoreNotFound) {
		t.Fatal("grade resurrected data", e)
	}
}

type independentModel struct {
	started chan struct{}
	resume  chan struct{}
}

func (m *independentModel) Stream(ctx context.Context, _ string, _ []chat.Message, delta func(chat.Delta) error) (chat.Usage, error) {
	close(m.started)
	select {
	case <-m.resume:
		return chat.Usage{FinishReason: "stop"}, delta(chat.Delta{Text: "独立复测的教学回答仍然有效"})
	case <-ctx.Done():
		return chat.Usage{}, ctx.Err()
	}
}

func TestQuestionMemoryDeletingSourceKeepsIndependentRetestRunning(t *testing.T) {
	s, _, _ := setup(t)
	parent, _ := s.StartPractice(t.Context(), 1)
	child, e := s.createPlan(t.Context(), parent.Plan.Questions, parent.ID)
	if e != nil {
		t.Fatal(e)
	}
	child, e = s.Learn(t.Context(), child.ID, 1)
	if e != nil {
		t.Fatal(e)
	}
	m := &independentModel{make(chan struct{}), make(chan struct{})}
	s.model = m
	done := make(chan error, 1)
	go func() {
		_, e := s.Generate(context.Background(), Input{ConversationID: child.ID, SubmissionID: "independent", Message: "请解释本题", Model: "test"}, nil)
		done <- e
	}()
	select {
	case <-m.started:
	case <-time.After(3 * time.Second):
		t.Fatal("independent model never started")
	}
	if _, e = s.Delete(t.Context(), parent.ID, parent.Version, true); e != nil {
		t.Fatal(e)
	}
	close(m.resume)
	select {
	case e := <-done:
		if e != nil {
			t.Fatal("source deletion invalidated independent context", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("independent request stuck")
	}
	restored, e := s.Load(t.Context(), child.ID)
	if e != nil || restored.Pending != nil || restored.Plan.SourceConversation != "" || restored.Messages[len(restored.Messages)-1].Status != "complete" {
		t.Fatal("independent session was damaged", e)
	}
}
