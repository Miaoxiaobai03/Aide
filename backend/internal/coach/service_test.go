package coach

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"offerpilot/backend/internal/chat"
	"offerpilot/backend/internal/interview"
	"offerpilot/backend/internal/knowledge"
)

type fixtureModel struct {
	calls [][]chat.Message
	fn    func([]chat.Message) (string, error)
}

func (m *fixtureModel) Stream(ctx context.Context, _ string, msgs []chat.Message, delta func(chat.Delta) error) (chat.Usage, error) {
	m.calls = append(m.calls, append([]chat.Message{}, msgs...))
	text, e := m.fn(msgs)
	text = fixtureGradeEvidence(text, msgs)
	if e != nil {
		return chat.Usage{}, e
	}
	if e = delta(chat.Delta{Text: text}); e != nil {
		return chat.Usage{}, e
	}
	return chat.Usage{Known: true, InputTokens: 20, OutputTokens: 10, FinishReason: "stop"}, nil
}

const validGrade = `{"correctness":3,"coverage":3,"explanation":3,"strengths":["定义正确"],"gaps":[{"point":"需要说明失败处理","quote":"失败应保留原始回答","reason":"回答未说明失败处理"}],"advice":"补充恢复步骤"}`

// Upgrade only the local v1 fixture protocol, not frozen input or assertions.
// Dedicated grading regressions below emit v2 directly and are not rewritten.
func fixtureGradeEvidence(text string, msgs []chat.Message) string {
	var grade map[string]any
	if json.Unmarshal([]byte(text), &grade) != nil || grade["correctness"] == nil || grade["version"] != nil {
		return text
	}
	var data struct {
		Answer string `json:"student_answer"`
	}
	if len(msgs) == 0 || json.Unmarshal([]byte(msgs[len(msgs)-1].Content), &data) != nil || data.Answer == "" {
		return text
	}
	grade["version"] = GradingVersion
	grade["status"] = "assessed"
	grade["answerQuote"] = data.Answer
	b, _ := json.Marshal(grade)
	return string(b)
}

func setup(t *testing.T) (*Service, *fixtureModel, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coach.db")
	repo, e := interview.OpenSQLiteStore(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { repo.Close() })
	model := &fixtureModel{fn: func([]chat.Message) (string, error) { return validGrade, nil }}
	idx := knowledge.NewIndex([]knowledge.Entry{{ID: "q1", Question: "如何恢复失败请求？", Source: "a.md", Excerpt: "失败应保留原始回答，不能记零分。重试需要提交ID去重，并校验服务端状态。"}, {ID: "q2", Question: "如何保证幂等？", Source: "b.md", Excerpt: "失败应保留原始回答，不能记零分。不同ID的作答保留历史，同一ID只对应一次提交。"}})
	return New(repo, model, idx, ContextConfig{DefaultModel: "test", Windows: map[string]int{"test": 20000}}), model, path
}
func TestPracticePersistenceIdempotencyAndIsolation(t *testing.T) {
	s, m, path := setup(t)
	ctx := context.Background()
	c, e := s.StartPractice(ctx, 2)
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range c.Public().Plan.Questions {
		if q.Reference != "" {
			t.Fatal("private reference leaked")
		}
	}
	if _, e = s.Next(ctx, c.ID, 1, false); e == nil {
		t.Fatal("ungraded question advanced")
	}
	in := Input{ConversationID: c.ID, SubmissionID: "submission-001", Message: "用提交ID恢复请求"}
	c, e = s.Answer(ctx, in)
	if e != nil || c.Completed() != 1 {
		t.Fatalf("grade %v %+v", e, c.Plan)
	}
	first := c.Plan.Attempts[0]
	c, e = s.Answer(ctx, in)
	if e != nil || len(m.calls) != 1 || len(c.Plan.Attempts) != 1 {
		t.Fatal("duplicate grade", e)
	}
	in.Message = "changed"
	if _, e = s.Answer(ctx, in); e == nil {
		t.Fatal("same id changed content")
	}
	for i := 0; i < 5; i++ {
		m.fn = func([]chat.Message) (string, error) { return "教学解释", nil }
		_, e = s.Generate(ctx, Input{ConversationID: c.ID, SubmissionID: id("followup-"), Message: "请解释"}, nil)
		if e != nil {
			t.Fatal(e)
		}
	}
	c, _ = s.Load(ctx, c.ID)
	if c.Completed() != 1 {
		t.Fatal("followups counted as questions")
	}
	c, e = s.Next(ctx, c.ID, 1, false)
	if e != nil {
		t.Fatal(e)
	}
	c, e = s.Next(ctx, c.ID, 1, false)
	if e != nil || c.Plan.Current != 1 {
		t.Fatal("double Next", e)
	}
	m.fn = func(msgs []chat.Message) (string, error) {
		b, _ := json.Marshal(msgs)
		if strings.Contains(string(b), "教学解释") || strings.Contains(string(b), "submission-001") || strings.Contains(string(b), "correctness") { /* correctness appears in rubric; test history separately below */
		}
		return validGrade, nil
	}
	p, _, e := s.projection(c, Input{Message: "独立回答", SubmissionID: "submission-002"}, "assessment")
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(p.Messages)
	if strings.Contains(string(b), "教学解释") || strings.Contains(string(b), "定义正确") || strings.Contains(string(b), "用提交ID恢复请求") {
		t.Fatal("first assessment contaminated")
	}
	reopened, e := interview.OpenSQLiteStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	restored, e := New(reopened, m, s.knowledge, s.config).Load(ctx, c.ID)
	if e != nil {
		t.Fatal(e)
	}
	if Hash(restored.Messages) != Hash(c.Messages) || Hash(restored.Plan.Questions) != Hash(c.Plan.Questions) || Hash(restored.Plan.Attempts[0]) != Hash(first) {
		t.Fatal("restart changed original messages, question order or first attempt")
	}
	if restored.Plan.Current != 1 {
		t.Fatal("progress lost")
	}
}
func TestFailedGradeRetryAndAssistedReanswer(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	c, _ := s.StartPractice(ctx, 1)
	in := Input{ConversationID: c.ID, SubmissionID: "answer-fail", Message: "原始作答"}
	m.fn = func([]chat.Message) (string, error) { return "", errors.New("timeout") }
	c, e := s.Answer(ctx, in)
	if e != nil || c.Completed() != 0 || c.Plan.Attempts[0].Status != "failed" || len(c.Messages) != 2 {
		t.Fatal("failed grade lost or counted", e)
	}
	m.fn = func([]chat.Message) (string, error) { return validGrade, nil }
	c, e = s.Answer(ctx, in)
	if e != nil || len(c.Plan.Attempts) != 1 || c.Plan.Attempts[0].Assisted {
		t.Fatal("retry duplicated", e)
	}
	in.SubmissionID = "answer-retry"
	in.Message = "看过讲解的重答"
	in.Reanswer = true
	c, e = s.Answer(ctx, in)
	if e != nil || len(c.Plan.Attempts) != 2 || !c.Plan.Attempts[1].Assisted || c.Completed() != 1 || c.Plan.Attempts[0].Answer != "原始作答" {
		t.Fatal("reanswer overwrote facts", e)
	}
	c, e = s.Next(ctx, c.ID, 1, true)
	if e != nil || c.Plan.Status != "completed" {
		t.Fatal(e)
	}
}
func TestPracticeLearnAndValidation(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	if _, e := s.StartPractice(ctx, -1); e == nil {
		t.Fatal("negative count")
	}
	if _, e := s.StartPractice(ctx, 3); e == nil {
		t.Fatal("insufficient pool")
	}
	c, _ := s.StartPractice(ctx, 1)
	if _, e := s.Generate(ctx, Input{ConversationID: c.ID, SubmissionID: "ask-answer-1", Message: "告诉我答案"}, nil); e == nil {
		t.Fatal("answer leaked before explicit learn")
	}
	c, e := s.Learn(ctx, c.ID, 1)
	if e != nil || c.Public().Plan.Questions[0].Reference == "" || c.Completed() != 0 {
		t.Fatal("learn counted", e)
	}
	c, e = s.Answer(ctx, Input{ConversationID: c.ID, SubmissionID: "learn-answer-1", Message: "学习后回答"})
	if e != nil || !c.Plan.Attempts[0].Assisted {
		t.Fatal("learned answer not marked", e)
	}
}
func seed(t *testing.T, s *Service, n int) Conversation {
	t.Helper()
	c, e := s.Create(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	appendMessage(&c, "user", "chat", "我参与调试，但没有独立负责部署。耗时是80毫秒。", "complete", "early-fact", 0)
	for i := 0; i < n; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		appendMessage(&c, role, "teaching", strings.Repeat("普通教学解释及未完成讨论", 30), "complete", id("seed-"), 0)
	}
	if e = s.save(context.Background(), &c); e != nil {
		t.Fatal(e)
	}
	return c
}
func TestCompactionPreservesSourcesAndInvalidatesOnEdit(t *testing.T) {
	s, m, _ := setup(t)
	s.config.InputCap = 12000
	c := seed(t, s, 14)
	original := Hash(c.Messages)
	m.fn = func(msgs []chat.Message) (string, error) {
		if !strings.Contains(msgs[0].Content, "教学笔记") {
			t.Fatal("unexpected call")
		}
		return `{"summary":"此前讨论答题表达，仍需要独立考核。","quotes":[]}`, nil
	}
	p, e := s.BuildContext(context.Background(), c.ID, Input{Message: "请继续", SubmissionID: "new-followup"}, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	stored, _ := s.Load(context.Background(), c.ID)
	if Hash(stored.Messages) != original || len(stored.Summaries) == 0 || p.Run.InputEstimate >= p.Run.Before {
		t.Fatal("no compaction/original changed")
	}
	b, _ := json.Marshal(p.Messages)
	if !strings.Contains(string(b), "没有独立负责部署") || !strings.Contains(string(b), "80毫秒") {
		t.Fatal("protected original lost")
	}
	old := stored.Summaries[0]
	changed, e := s.Edit(context.Background(), c.ID, old.SourceIDs[0], "修正：新的教学讨论", false, stored.Version)
	if e != nil || summaryValid(changed, old) || len(changed.Summaries) != 0 {
		t.Fatal("stale summary reused", e)
	}
	changed, e = s.Edit(context.Background(), c.ID, c.Messages[0].ID, "修正：不是80毫秒，是60毫秒", false, changed.Version)
	if e != nil {
		t.Fatal(e)
	}
	p, _, e = s.projection(changed, Input{Message: "什么耗时？"}, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	b, _ = json.Marshal(p.Messages)
	if strings.Contains(string(b), "耗时是80毫秒") {
		t.Fatal("old text revived")
	}
}
func TestSummaryFailureFallbackAndOversizedInput(t *testing.T) {
	s, m, _ := setup(t)
	s.config.InputCap = 12000
	c := seed(t, s, 8)
	m.fn = func([]chat.Message) (string, error) { return `{"summary":"","quotes":[]}`, nil }
	p, e := s.BuildContext(context.Background(), c.ID, Input{Message: "继续教学"}, "teaching")
	if e != nil {
		t.Fatal("budget-fit fallback should succeed", e)
	}
	if p.Run.InputEstimate > p.Run.Budget {
		t.Fatal("overbudget sent")
	}
	c, _ = s.StartPractice(context.Background(), 1)
	m.calls = nil
	c, e = s.Answer(context.Background(), Input{ConversationID: c.ID, SubmissionID: "too-long-answer", Message: strings.Repeat("必须保留原始作答", 3000)})
	if e != nil {
		t.Fatal(e)
	}
	if len(m.calls) != 0 || c.Plan.Attempts[0].Status != "failed" || c.Completed() != 0 || !strings.Contains(c.Plan.Attempts[0].Error, "预算") {
		t.Fatal("oversize graded/truncated")
	}
}
func TestDeletedReferenceAndOwnership(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	c := seed(t, s, 2)
	key := c.Messages[1].ID
	c, e := s.Edit(ctx, c.ID, key, "", true, c.Version)
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.projection(c, Input{Message: "解释它", References: []string{key}}, "teaching"); e == nil {
		t.Fatal("deleted source returned")
	}
	c.Profile = "other-profile"
	if e = s.save(ctx, &c); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Load(ctx, c.ID); e == nil {
		t.Fatal("foreign profile leaked")
	}
}
func TestConcurrentSourceEditRejectsModelResult(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	c := seed(t, s, 2)
	m.fn = func([]chat.Message) (string, error) {
		latest, _ := s.Load(ctx, c.ID)
		_, e := s.Edit(ctx, c.ID, c.Messages[0].ID, "纠正：没有独立部署", false, latest.Version)
		if e != nil {
			t.Fatal(e)
		}
		return "过时回答", nil
	}
	_, e := s.Generate(ctx, Input{ConversationID: c.ID, SubmissionID: "edit-concurrent", Message: "请解释"}, nil)
	if !errors.Is(e, ErrConflict) {
		t.Fatal("stale result committed", e)
	}
}
func TestSummaryAppendAndDeleteSourceValidation(t *testing.T) {
	s, _, _ := setup(t)
	c := seed(t, s, 2)
	su, e := parseSummary(`{"summary":"讨论表达","quotes":[]}`, c.Messages[:2], "test")
	if e != nil {
		t.Fatal(e)
	}
	appendMessage(&c, "user", "chat", "新增修正", "complete", "new-tail", 0)
	if !summaryValid(c, su) {
		t.Fatal("append invalidated immutable prefix")
	}
	c.Messages[0].Deleted = true
	if summaryValid(c, su) {
		t.Fatal("deleted prefix accepted")
	}
}
func TestTimeoutBoundAndUnknownWindow(t *testing.T) {
	s, m, _ := setup(t)
	s.config.SummaryTimeout = 5 * time.Millisecond
	s.config.InputCap = 12000
	c := seed(t, s, 14)
	m.fn = func([]chat.Message) (string, error) {
		time.Sleep(10 * time.Millisecond)
		return "", context.DeadlineExceeded
	}
	_, e := s.BuildContext(context.Background(), c.ID, Input{Message: "继续", Model: "unconfigured-model"}, "teaching")
	if e != nil && !errors.Is(e, ErrBudget) {
		t.Fatal(e)
	}
	if len(m.calls) > 2 {
		t.Fatal("unbounded retries")
	}
	stored, _ := s.Load(context.Background(), c.ID)
	if len(stored.Runs) == 0 || stored.Runs[0].WindowKnown {
		t.Fatal("unknown window claimed known")
	}
}

func TestFullHistoryBeyondFortyAndSubmissionReferenceConflict(t *testing.T) {
	s, m, _ := setup(t)
	m.fn = func([]chat.Message) (string, error) { return "固定教学解释", nil }
	ctx := context.Background()
	c, _ := s.Create(ctx)
	first := Input{ConversationID: c.ID, SubmissionID: "first-submission", Message: "我没有独立负责部署。"}
	c, e := s.Generate(ctx, first, nil)
	if e != nil {
		t.Fatal(e)
	}
	firstMessage := c.Messages[0]
	for n := 0; n < 25; n++ {
		c, e = s.Generate(ctx, Input{ConversationID: c.ID, SubmissionID: id("follow-"), Message: "继续解释答题表达"}, nil)
		if e != nil {
			t.Fatal(e)
		}
	}
	c, e = s.Load(ctx, c.ID)
	if e != nil || len(c.Messages) != 52 || Hash(c.Messages[0]) != Hash(firstMessage) {
		t.Fatal("full original history lost beyond forty", e)
	}
	first.References = []string{c.Messages[3].ID}
	if _, e = s.Generate(ctx, first, nil); errorCode(e) != "submission_conflict" {
		t.Fatal("same submission changed sources", e)
	}
}

func TestExpiredEvaluationLeaseRecoversToFailure(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	c, _ := s.StartPractice(ctx, 1)
	message := appendMessage(&c, "user", "answer", "原始作答", "complete", "expired-answer", 1)
	c.Plan.Attempts = append(c.Plan.Attempts, Attempt{ID: "expired-answer", AnswerID: message.ID, Answer: message.Content, Ordinal: 1, Status: "evaluating"})
	c.Plan.Status = "evaluating"
	c.Pending = &Lease{ID: "expired-answer", Until: time.Now().Add(-time.Minute)}
	if e := s.save(ctx, &c); e != nil {
		t.Fatal(e)
	}
	c, e := s.Recover(ctx, c.ID)
	if e != nil || c.Pending != nil || c.Plan.Attempts[0].Status != "failed" || c.Completed() != 0 || c.Plan.Attempts[0].Answer != "原始作答" {
		t.Fatal("expired lease became grade or lost answer", e)
	}
}

func TestLongExplicitFragmentAndStaleSource(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	c, _ := s.Create(ctx)
	msg := appendMessage(&c, "assistant", "teaching", strings.Repeat("这是可选的教学原文片段", 2000), "complete", "", 0)
	if e := s.save(ctx, &c); e != nil {
		t.Fatal(e)
	}
	in := Input{Message: "请解释所选片段", SubmissionID: "fragment-query", References: []string{msg.ID}}
	_, e := s.BuildContext(ctx, c.ID, in, "teaching")
	if !errors.Is(e, ErrBudget) || len(m.calls) != 0 {
		t.Fatal("whole oversize source silently sent", e)
	}
	in.References = nil
	in.Fragments = []Fragment{{MessageID: msg.ID, Start: 0, End: 22, Revision: 1}}
	p, e := s.BuildContext(ctx, c.ID, in, "teaching")
	if e != nil || p.Run.InputEstimate > p.Run.Budget {
		t.Fatal("chosen fragment did not recover", e)
	}
	stored, _ := s.Load(ctx, c.ID)
	if Hash(stored.Messages[0]) != Hash(msg) {
		t.Fatal("fragment rewrote original")
	}
	stored, e = s.Edit(ctx, c.ID, msg.ID, "修改过的教学原文", false, stored.Version)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.BuildContext(ctx, c.ID, in, "teaching"); errorCode(e) != "stale_reference" {
		t.Fatal("stale fragment returned", e)
	}
}

func TestAmbiguousRecallReturnsClarificationWithoutProvider(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	c, _ := s.Create(ctx)
	c, e := s.Generate(ctx, Input{ConversationID: c.ID, SubmissionID: "ambiguous-query", Message: "刚才那个例子指什么？"}, nil)
	if e != nil || len(m.calls) != 0 || len(c.Messages) != 2 || !strings.Contains(c.Messages[1].Content, "选择具体消息") {
		t.Fatal("ambiguous source invented", e)
	}
	if c.Runs[0].ModelCalled {
		t.Fatal("local recovery billed as provider call")
	}
}
