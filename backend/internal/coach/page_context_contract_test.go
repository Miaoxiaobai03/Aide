package coach

import (
	"context"
	"encoding/json"
	"errors"
	"offerpilot/backend/internal/chat"
	"strings"
	"testing"
	"time"
)

func TestPageSourceEditAndDisconnectedStream(t *testing.T) {
	for _, relevant := range []bool{false, true} {
		t.Run(map[bool]string{false: "unused-source", true: "used-source"}[relevant], func(t *testing.T) {
			m := &fixtureModel{}
			s, _, parent := pagedFixture(t, m)
			v, _ := s.Page(t.Context(), parent.ID, "", true)
			c, _ := s.Learn(t.Context(), v.Conversation.ID, 1)
			refID := c.Messages[len(c.Messages)-1].ID
			m.fn = func([]chat.Message) (string, error) {
				latest, _ := s.Load(t.Context(), c.ID)
				key := refID
				if relevant {
					for _, msg := range latest.Messages {
						if msg.Role == "user" && msg.SubmissionID == "stream" {
							key = msg.ID
						}
					}
				}
				if _, err := s.Edit(t.Context(), c.ID, key, "更新后的原文", false, latest.Version); err != nil {
					t.Fatal(err)
				}
				return "模型回答", nil
			}
			_, err := s.Generate(t.Context(), Input{ConversationID: c.ID, QuestionID: parent.Plan.Questions[0].ID, Message: "解释本题", SubmissionID: "stream"}, nil)
			if relevant && !errors.Is(err, ErrConflict) || !relevant && err != nil {
				t.Fatalf("source-based acceptance wrong: %v", err)
			}
		})
	}
	m := &fixtureModel{fn: func([]chat.Message) (string, error) { return "断线之前生成的原文", nil }}
	s, _, parent := pagedFixture(t, m)
	v, _ := s.Page(t.Context(), parent.ID, "", true)
	c, _ := s.Learn(t.Context(), v.Conversation.ID, 1)
	_, err := s.Generate(t.Context(), Input{ConversationID: c.ID, QuestionID: parent.Plan.Questions[0].ID, Message: "问题", SubmissionID: "disconnect"}, func(chat.Delta) error { return context.Canceled })
	if err == nil {
		t.Fatal("disconnect considered completed")
	}
	restored, _ := s.Load(t.Context(), c.ID)
	last := restored.Messages[len(restored.Messages)-1]
	if restored.Pending != nil || last.Status != "interrupted" || last.Content != "断线之前生成的原文" {
		t.Fatal("disconnected output not persisted to original page")
	}
}

func TestPageTeachingContextScopeDedupAndReferences(t *testing.T) {
	s, _, parent := pagedFixture(t, nil)
	view, err := s.Page(t.Context(), parent.ID, parent.Plan.Questions[1].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Learn(t.Context(), view.Conversation.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	referenceID := c.Messages[len(c.Messages)-1].ID
	c, err = s.update(t.Context(), c.ID, func(c *Conversation) error {
		for i := 0; i < 30; i++ {
			appendMessage(c, "user", "followup", "如何理解本题的权限检查？", "complete", id("q_"), 1)
			appendMessage(c, "assistant", "teaching", "请在执行操作之前检查权限，并保存失败状态。", "complete", id("r_"), 1)
		}
		appendMessage(c, "user", "followup", "纠正：权限检查必须在执行之前。", "complete", "correction", 1)
		c.Messages[len(c.Messages)-1].Corrects = c.Messages[2].ID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Message: "解释这个过程", SubmissionID: "current", Model: "deepseek-flash", QuestionID: parent.Plan.Questions[1].ID, References: []string{referenceID}}
	p, _, err := s.projection(c, in, "teaching")
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, m := range p.Messages {
		text += m.Content
	}
	if strings.Count(text, c.Plan.Questions[0].Reference) != 1 || strings.Count(text, in.Message) != 1 || strings.Contains(text, parent.Plan.Questions[0].Reference) || strings.Contains(text, parent.Plan.Questions[2].Reference) || strings.Contains(text, "已归档题目目录") {
		t.Fatal("scope or dedup contract violated")
	}
	if !strings.Contains(text, "权限检查必须在执行之前") || len(p.Messages) < 60 || len(c.Messages) < 60 {
		t.Fatal("same-page history or correction was dropped")
	}
	if p.Run.BodyEstimate <= 0 || p.Run.InputEstimate <= p.Run.BodyEstimate || p.Run.Budget != 30000 || p.Run.WindowKnown {
		t.Fatal("counter telemetry or budget mislabeled")
	}
	for _, bad := range []Input{
		{Message: "引用", References: []string{parent.Plan.Questions[0].ID}},
		{Message: "片段", Fragments: []Fragment{{MessageID: parent.Plan.Questions[0].ID, Start: 0, End: 2, Revision: 1}}},
		{Message: "纠正", Corrects: parent.Plan.Questions[0].ID},
		{Message: "错误题目", QuestionID: parent.Plan.Questions[0].ID},
	} {
		_, _, err = s.projection(c, bad, "teaching")
		var ce *Error
		if !errors.As(err, &ce) || ce.Status != 422 {
			t.Fatalf("cross-page source not rejected: %v", err)
		}
	}
	fragment := Fragment{MessageID: c.Messages[2].ID, Start: 0, End: 1, Revision: c.Messages[2].Revision + 1}
	_, _, err = s.projection(c, Input{Message: "片段", Fragments: []Fragment{fragment}}, "teaching")
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "stale_reference" {
		t.Fatalf("stale fragment accepted: %v", err)
	}
	fragment.Revision--
	fragment.End = 9999
	_, _, err = s.projection(c, Input{Message: "片段", Fragments: []Fragment{fragment}}, "teaching")
	if !errors.As(err, &ce) || ce.Code != "invalid_fragment" {
		t.Fatalf("invalid range accepted: %v", err)
	}
	c.Messages = append(c.Messages, Message{ID: "foreign", Ordinal: 2})
	if _, _, err = s.projection(c, in, "teaching"); err == nil {
		t.Fatal("corrupted cross-page record entered model")
	}
}

func TestPageAssessmentPersistsOriginalRetriesAndReanswers(t *testing.T) {
	model := &fixtureModel{}
	s, _, parent := pagedFixture(t, model)
	view, _ := s.Page(t.Context(), parent.ID, parent.Plan.Questions[2].ID, true)
	answer := strings.Repeat("用户原始作答保持完整，不能只评价一小段。\n", 100)
	model.fn = func(msgs []chat.Message) (string, error) {
		saved, err := s.Load(t.Context(), view.Conversation.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(saved.Plan.Attempts) != 1 || saved.Plan.Attempts[0].Answer != answer || saved.Pending == nil {
			t.Fatal("model called before original saved")
		}
		var payload map[string]any
		_ = json.Unmarshal([]byte(msgs[len(msgs)-1].Content), &payload)
		if payload["student_answer"] != answer || payload["frozen_reference"] != parent.Plan.Questions[2].Reference {
			t.Fatal("wrong assessment source")
		}
		return `{}`, nil
	}
	in := Input{ConversationID: view.Conversation.ID, QuestionID: parent.Plan.Questions[2].ID, SubmissionID: "first", Message: answer}
	c, err := s.Answer(t.Context(), in)
	if err != nil || c.Plan.Attempts[0].Status != "failed" || c.Completed() != 0 {
		t.Fatalf("invalid grade counted: %v", err)
	}
	model.fn = func([]chat.Message) (string, error) {
		return `{"correctness":3,"coverage":3,"explanation":3,"strengths":["作答提供了明确的保护原则"],"gaps":[],"advice":"继续说明具体步骤"}`, nil
	}
	c, err = s.Answer(t.Context(), in)
	if err != nil || len(c.Plan.Attempts) != 1 || c.Completed() != 1 {
		t.Fatalf("same submission retry not idempotent: %v", err)
	}
	in.Message += "变更"
	_, err = s.Answer(t.Context(), in)
	var conflict *Error
	if !errors.As(err, &conflict) || conflict.Status != 409 {
		t.Fatalf("same ID different answer accepted: %v", err)
	}
	in.SubmissionID = "reanswer"
	in.Reanswer = true
	c, err = s.Answer(t.Context(), in)
	if err != nil || len(c.Plan.Attempts) != 2 || c.Completed() != 1 {
		t.Fatalf("reanswer count wrong: %v", err)
	}
	in.Reanswer = false
	in.Regrade = true
	in.GradingRevision = c.Plan.Attempts[1].Revision
	c, err = s.Answer(t.Context(), in)
	if err != nil || len(c.Plan.Attempts) != 2 || len(c.Plan.Attempts[1].History) != 1 || c.Plan.Attempts[1].Revision != in.GradingRevision+1 || c.Completed() != 1 {
		t.Fatalf("regrade replaced original or duplicate-counted: %v", err)
	}
}

func TestPageRestartRecoversUnexpiredOperationsWithoutModelRetry(t *testing.T) {
	model := &fixtureModel{fn: func([]chat.Message) (string, error) { t.Fatal("restart charged a model call"); return "", nil }}
	s, db, parent := pagedFixture(t, model)
	view, _ := s.Page(t.Context(), parent.ID, "", true)
	_, err := s.update(t.Context(), view.Conversation.ID, func(c *Conversation) error {
		appendMessage(c, "user", "followup", "已保存问题", "complete", "abandoned", 1)
		appendMessage(c, "assistant", "teaching", "已生成的一部分原文", "streaming", "abandoned", 1)
		c.Pending = &Lease{ID: "abandoned", Until: time.Now().Add(time.Hour)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(db, model, nil, ContextConfig{})
	n, err := restarted.RecoverAfterRestart(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("restart recovery failed %d %v", n, err)
	}
	c, err := restarted.Load(t.Context(), view.Conversation.ID)
	last := c.Messages[len(c.Messages)-1]
	if err != nil || c.Pending != nil || last.Status != "interrupted" || last.Content != "已生成的一部分原文" || len(model.calls) != 0 {
		t.Fatal("partial output lost or run automatically retried")
	}
	if n, err = restarted.RecoverAfterRestart(t.Context()); err != nil || n != 0 {
		t.Fatal("restart recovery is not idempotent")
	}
}

func TestPageBudgetKnownUnknownAndOversizedMandatoryInput(t *testing.T) {
	s, _, parent := pagedFixture(t, nil)
	view, _ := s.Page(t.Context(), parent.ID, "", true)
	c, _ := s.Learn(t.Context(), view.Conversation.ID, 1)
	s.config.Windows = map[string]int{"deepseek-flash": 35000, "tiny": 7000}
	if b, known := s.config.budget("deepseek-flash"); b != 29880 || !known {
		t.Fatalf("input/output window accounting wrong %d", b)
	}
	if b, known := s.config.budget("unknown"); b != 30000 || known {
		t.Fatal("unknown window invented")
	}
	if _, err := s.BuildContext(t.Context(), c.ID, Input{Message: strings.Repeat("必需原文", 30000), Model: "tiny"}, "teaching"); !errors.Is(err, ErrBudget) {
		t.Fatalf("oversized current input not protected: %v", err)
	}
	if s.counterFor(c, "my-deepseek-proxy").Name() == (PracticeEstimateCounter{}).Name() {
		t.Fatal("unvalidated model alias used calibrated counter")
	}
}
