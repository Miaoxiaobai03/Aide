package coach

import (
	"context"
	"offerpilot/backend/internal/chat"
	"testing"
)

func TestGradingSourceMismatchBlocksEvenExactReference(t *testing.T) {
	s, m, _ := setup(t)
	ctx := context.Background()
	c, _ := s.StartPractice(ctx, 1)
	text := c.Plan.Questions[0].Reference
	c, _ = s.update(ctx, c.ID, func(c *Conversation) error {
		msg := appendMessage(c, "user", "answer", "被修改的消息", "complete", "mismatch", 1)
		c.Plan.Attempts = append(c.Plan.Attempts, Attempt{ID: "mismatch", Ordinal: 1, AnswerID: msg.ID, Answer: text, Status: "failed"})
		return nil
	})
	c, e := s.Answer(ctx, Input{ConversationID: c.ID, SubmissionID: "mismatch", Message: text})
	if e != nil {
		t.Fatal(e)
	}
	if c.Plan.Attempts[0].Status != "failed" || c.Plan.Attempts[0].ErrorCode != "assessment_source" || len(m.calls) != 0 || c.Completed() != 0 {
		t.Fatal("mismatched source invoked or counted grading")
	}
}

type finishGradeModel struct {
	finish, text string
	err          error
	calls        int
}

func (m *finishGradeModel) Stream(ctx context.Context, _ string, msgs []chat.Message, cb func(chat.Delta) error) (chat.Usage, error) {
	m.calls++
	if m.err != nil {
		return chat.Usage{}, m.err
	}
	err := cb(chat.Delta{Text: m.text})
	return chat.Usage{Known: true, FinishReason: m.finish, InputTokens: 10, OutputTokens: 10}, err
}
func TestGradingTimeoutTruncationAndEmptyAreNotScores(t *testing.T) {
	for _, tc := range []struct {
		name, finish, text, code string
		err                      error
	}{
		{"timeout", "", "", "assessment_timeout", context.DeadlineExceeded},
		{"cancel", "", "", "assessment_cancelled", context.Canceled},
		{"truncated", "length", gradeJSON("我的作答", 3), "assessment_truncated", nil},
		{"empty", "stop", "", "assessment_empty", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := setup(t)
			m := &finishGradeModel{finish: tc.finish, text: tc.text, err: tc.err}
			s.model = m
			c, _ := s.StartPractice(t.Context(), 1)
			c, e := s.Answer(t.Context(), Input{ConversationID: c.ID, SubmissionID: "boundary", Message: "我的作答"})
			if e != nil {
				t.Fatal(e)
			}
			a := c.Plan.Attempts[0]
			if a.Status != "failed" || a.ErrorCode != tc.code || a.Evaluation != nil || c.Completed() != 0 || m.calls != 1 {
				t.Fatalf("boundary failure: %+v", a)
			}
		})
	}
}
