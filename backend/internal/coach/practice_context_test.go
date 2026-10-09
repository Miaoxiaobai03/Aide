package coach

import (
	"context"
	"aide/backend/internal/chat"
	"strings"
	"testing"
)

func TestPracticeOldTechnicalAnswersStayArchivedWithoutModelCalls(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	c, _ := s.StartPractice(ctx, 2)
	c, _ = s.update(ctx, c.ID, func(c *Conversation) error {
		for i := 0; i < 10; i++ {
			appendMessage(c, "user", "answer", strings.Repeat("没有注册全部工具，延迟加载仍需完整说明与权限过滤。", 30), "complete", id("past_"), 1)
			appendMessage(c, "assistant", "teaching", "应分层管理工具，待独立复测。", "complete", id("reply_"), 1)
		}
		c.Plan.Current = 1
		c.Plan.Taught[2] = true
		appendMessage(c, "user", "followup", "我没有独立负责生产部署，请不要把练习讲解当作我的经历。", "complete", "personal", 1)
		return nil
	})
	in := Input{Message: "请解释当前题", SubmissionID: "context-check"}
	lower, _, e := s.project(c, in, "teaching", true)
	if e != nil {
		t.Fatal(e)
	}
	if lower.Run.InputEstimate > lower.Run.Budget {
		t.Fatal("old technical negations permanently pinned")
	}
	text := ""
	for _, msg := range lower.Messages {
		text += msg.Content
	}
	if !strings.Contains(text, "我没有独立负责生产部署") {
		t.Fatal("personal constraint lost")
	}
	m.fn = func([]chat.Message) (string, error) {
		return `{"summary":"之前讨论了工具加载，尚未独立复测。","quotes":[]}`, nil
	}
	built, e := s.BuildContext(ctx, c.ID, in, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	restored, _ := s.Load(ctx, c.ID)
	if len(restored.Summaries) != 0 || len(m.calls) != 0 || built.Run.InputEstimate > built.Run.Budget || Hash(restored.Messages) != Hash(c.Messages) {
		t.Fatal("archived questions incurred compression calls or raw messages changed")
	}
	explicit := Input{Message: "回查原作答", SubmissionID: "explicit", References: []string{c.Messages[1].ID}}
	p, _, e := s.project(c, explicit, "teaching", true)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, key := range p.Run.SourceIDs {
		if key == c.Messages[1].ID {
			found = true
		}
	}
	if !found {
		t.Fatal("explicit source weakened by scoped protection")
	}
}

func TestPracticeReferenceAndFailedRetriesNotDuplicated(t *testing.T) {
	s, _, _ := setup(t)
	c, _ := s.StartPractice(t.Context(), 1)
	c, _ = s.Learn(t.Context(), c.ID, 1)
	c, _ = s.update(t.Context(), c.ID, func(c *Conversation) error {
		for i := 0; i < 10; i++ {
			key := id("failed_")
			appendMessage(c, "user", "followup", strings.Repeat("没有收到回复，请解释之前的失败恢复过程。", 50), "complete", key, 1)
			appendMessage(c, "assistant", "teaching", "", "interrupted", key, 1)
		}
		return nil
	})
	p, _, e := s.projection(c, Input{Message: "当前问题", SubmissionID: "new"}, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	text := ""
	for _, msg := range p.Messages {
		text += msg.Content
	}
	if strings.Count(text, c.Plan.Questions[0].Reference) != 1 || strings.Contains(text, strings.Repeat("没有收到回复，请解释之前的失败恢复过程。", 50)) {
		t.Fatal("reference or failed requests duplicated")
	}
}

func TestQuotedNoviceDescriptionCannotGradeFullAnswer(t *testing.T) {
	s, _, _ := setup(t)
	c, _ := s.StartPractice(t.Context(), 1)
	answer := "应保留请求标识，失败后恢复原提交，不能把失败记零分。"
	e := Evaluation{Correctness: 3, Coverage: 1, Explanation: 1, Advice: "补充恢复机制", Gaps: []Gap{{Point: "回答过度简化", Reason: "本次作答只说“上下文太长了”，没有说明恢复。", Quote: c.Plan.Questions[0].Reference}}}
	a := Attempt{Status: "succeeded", Ordinal: 1, Answer: answer, Evaluation: &e}
	if validAttempt(a, c.Plan.Questions[0]) {
		t.Fatal("novice description accepted for different original")
	}
	e.Version = GradingVersion
	e.Status = "assessed"
	e.AnswerQuote = answer
	if err := validateAssessed(e, c.Plan.Questions[0], answer); err == nil {
		t.Fatal("v2 accepted conflicting answer description")
	}
}
