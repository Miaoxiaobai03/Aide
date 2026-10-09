package coach

import (
	"context"
	"encoding/json"
	"fmt"
	"aide/backend/internal/chat"
	"aide/backend/internal/interview"
	"reflect"
	"strings"
	"testing"
	"time"
)

type pageSummaryModel struct {
	calls int
	fn    func(context.Context, []chat.Message, func(chat.Delta) error) (chat.Usage, error)
}

func (m *pageSummaryModel) Stream(ctx context.Context, _ string, msgs []chat.Message, delta func(chat.Delta) error) (chat.Usage, error) {
	m.calls++
	return m.fn(ctx, msgs, delta)
}
func noteOutput(msgs []chat.Message) string {
	var raw []Message
	json.Unmarshal([]byte(msgs[1].Content), &raw)
	b, _ := json.Marshal(map[string]any{"conclusions": []string{"工具边界隔离访问路径，权限检查在调用前完成。"}, "correctedUnderstanding": []string{}, "openQuestions": []string{"需要进一步讨论缓存失效"}, "nextSteps": []string{"解释缓存"}, "quotes": []map[string]string{{"sourceId": raw[0].ID, "quote": string([]rune(raw[0].Content)[:12])}}})
	return string(b)
}
func compactionFixture(t *testing.T, m Model) (*Service, Conversation, Input) {
	s, _, parent := pagedFixture(t, m)
	v, _ := s.Page(t.Context(), parent.ID, "", true)
	c, _ := s.Learn(t.Context(), v.Conversation.ID, 1)
	c, e := s.update(t.Context(), c.ID, func(c *Conversation) error {
		for i := 0; i < 32; i++ {
			role, kind := "user", "followup"
			if i%2 == 1 {
				role, kind = "assistant", "teaching"
			}
			appendMessage(c, role, kind, strings.Repeat("工具边界用于隔离访问路径。", 55), "complete", fmt.Sprint(i), 1)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	s.config.DefaultModel = "deepseek-flash"
	in := Input{ConversationID: c.ID, QuestionID: c.Plan.Questions[0].ID, Message: "解释权限路径", SubmissionID: "new"}
	_, _, e = s.projection(c, in, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	s.config.InputCap = 30000
	return s, c, in
}
func TestPageCompactionFailuresAndStickySources(t *testing.T) {
	for _, failure := range []string{"length", "empty", "timeout", "schema", "evidence", "no-gain"} {
		t.Run(failure, func(t *testing.T) {
			m := &pageSummaryModel{fn: func(ctx context.Context, msgs []chat.Message, d func(chat.Delta) error) (chat.Usage, error) {
				text := noteOutput(msgs)
				u := chat.Usage{FinishReason: "stop"}
				switch failure {
				case "length":
					u.FinishReason = "length"
				case "empty":
					text = ""
				case "timeout":
					return u, context.DeadlineExceeded
				case "schema":
					text = `{"conclusions":[]}`
				case "evidence":
					text = strings.ReplaceAll(text, "工具边界用于隔离访问路径", "虚构证据")
				}
				return u, d(chat.Delta{Text: text, Thinking: "此推理不写入上下文"})
			}}
			s, c, in := compactionFixture(t, m)
			if failure == "no-gain" {
				s.config.Counter = derivedPenaltyCounter{}
				s.config.DefaultModel = "fixture"
				s.config.InputCap = 30000
			}
			original := len(c.Messages)
			p, e := s.BuildContext(t.Context(), c.ID, in, "teaching")
			if e != nil {
				t.Fatal(e)
			}
			after, _ := s.Load(t.Context(), c.ID)
			if m.calls != 1 || len(after.Summaries) != 0 || len(after.CompressionChecks) != 1 || len(after.Messages) != original {
				t.Fatalf("failed summary attached or originals lost: calls=%d summaries=%d checks=%d", m.calls, len(after.Summaries), len(after.CompressionChecks))
			}
			if strings.Contains(fmt.Sprint(p.Messages), "此推理") {
				t.Fatal("reasoning replayed")
			}
			s.update(t.Context(), c.ID, func(c *Conversation) error {
				appendMessage(c, "user", "followup", "解释缓存", "complete", "later", 1)
				appendMessage(c, "assistant", "teaching", "缓存维护来源版本", "complete", "later", 1)
				c.CompressionChecks[0].Until = time.Now().Add(-time.Hour)
				return nil
			})
			s.BuildContext(t.Context(), c.ID, in, "teaching")
			if m.calls != 1 {
				t.Fatal("later messages or elapsed timeout bypassed failure control")
			}
			in.RetryCompression = true
			s.BuildContext(t.Context(), c.ID, in, "teaching")
			if m.calls != 2 {
				t.Fatal("explicit retry blocked")
			}
			in.RetryCompression = false
			current, _ := s.Load(t.Context(), c.ID)
			source := current.CompressionChecks[0].SourceIDs[0]
			for _, msg := range current.Messages {
				if msg.ID == source {
					_, e = s.Edit(t.Context(), c.ID, source, msg.Content+"实际修订内容", false, current.Version)
					break
				}
			}
			if e != nil {
				t.Fatal(e)
			}
			s.BuildContext(t.Context(), c.ID, in, "teaching")
			if m.calls != 3 {
				t.Fatal("changed sources still blocked")
			}
		})
	}
}

type derivedPenaltyCounter struct{}

func (derivedPenaltyCounter) Name() string { return "fixture-penalty" }
func (derivedPenaltyCounter) Count(model string, msgs []chat.Message, out int) (int, error) {
	n, e := (PracticeEstimateCounter{}).Count(model, msgs, out)
	for _, m := range msgs {
		if strings.Contains(m.Content, "派生历史摘要") {
			n += 100000
		}
	}
	return n, e
}
func TestPageCompactionSuccessPreservesAndBindsOriginals(t *testing.T) {
	m := &pageSummaryModel{fn: func(_ context.Context, msgs []chat.Message, d func(chat.Delta) error) (chat.Usage, error) {
		return chat.Usage{FinishReason: "stop"}, d(chat.Delta{Text: noteOutput(msgs), Thinking: "秘密推理"})
	}}
	s, c, in := compactionFixture(t, m)
	p, e := s.BuildContext(t.Context(), c.ID, in, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	after, _ := s.Load(t.Context(), c.ID)
	if len(after.Summaries) != 1 || !reflect.DeepEqual(after.Messages, c.Messages) || p.Run.InputEstimate >= p.Run.Before {
		t.Fatal("summary did not reduce input with original records intact")
	}
	t.Logf("synthetic same-page compaction input_estimate_before=%d after=%d relative_reduction=%.2f%% originals_equal=true", p.Run.Before, p.Run.InputEstimate, 100*float64(p.Run.Before-p.Run.InputEstimate)/float64(p.Run.Before))
	su := after.Summaries[0]
	for _, key := range su.SourceIDs {
		found := false
		for _, used := range p.Run.SourceIDs {
			if used == key {
				found = true
			}
		}
		if !found {
			t.Fatal("summary sources not bound to request")
		}
	}
	s.config.InputCap = 1000
	_, candidates, e := s.projection(after, in, "teaching")
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, m := range candidates {
		if m.ID == su.SourceIDs[0] {
			found = true
		}
	}
	if !found {
		t.Fatal("unloaded summary still excluded raw sources")
	}
	s.config.InputCap = 30000
	latest, _ := s.Load(t.Context(), c.ID)
	first := latest.Messages[2]
	s.Edit(t.Context(), c.ID, first.ID, first.Content+"变化", false, latest.Version)
	edited, _ := s.Load(t.Context(), c.ID)
	if len(edited.Summaries) != 0 {
		t.Fatal("edited source retained summary")
	}
}
func TestNormalTenTwentyPagesNoSummaryCalls(t *testing.T) {
	for _, count := range []int{10, 20} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			m := &pageSummaryModel{fn: func(context.Context, []chat.Message, func(chat.Delta) error) (chat.Usage, error) {
				t.Fatal("normal navigation/teaching used summary")
				return chat.Usage{}, nil
			}}
			s, _, _ := pagedFixture(t, m)
			s.config.DefaultModel = "deepseek-flash"
			qs := []Question{}
			for i := 0; i < count; i++ {
				q := Question{ID: fmt.Sprint(i), Text: fmt.Sprintf("专属问题%d", i), Reference: strings.Repeat("本题知识依据说明工具调用协议。", 15)}
				q.ReferenceHash = RawHash(q.Reference)
				q.Hash = Hash(q)
				qs = append(qs, q)
			}
			parent, e := s.createPlanStorage(t.Context(), qs, "", true)
			if e != nil {
				t.Fatal(e)
			}
			maxInput := 0
			for _, q := range qs {
				v, _ := s.Page(t.Context(), parent.ID, q.ID, true)
				c, _ := s.Answer(t.Context(), Input{ConversationID: v.Conversation.ID, QuestionID: q.ID, SubmissionID: "a", Message: q.Reference})
				p, e := s.BuildContext(t.Context(), c.ID, Input{ConversationID: c.ID, QuestionID: q.ID, Message: "解释本题"}, "teaching")
				if e != nil {
					t.Fatal(e)
				}
				maxInput = max(maxInput, p.Run.InputEstimate)
				s.Page(t.Context(), parent.ID, qs[0].ID, true)
			}
			if m.calls != 0 {
				t.Fatal("normal flow generated summary")
			}
			t.Logf("questions=%d max_single_page_input_estimate=%d navigation_summary_calls=0", count, maxInput)
		})
	}
}
func TestPageDeleteDuringSummaryCannotResurrect(t *testing.T) {
	var s *Service
	var parentID string
	m := &pageSummaryModel{fn: func(_ context.Context, msgs []chat.Message, d func(chat.Delta) error) (chat.Usage, error) {
		info, _ := s.repo.(interface {
			PracticeInfo(context.Context, string) (interview.PracticeInfo, error)
		}).PracticeInfo(t.Context(), parentID)
		if e := s.DeletePages(t.Context(), parentID, info.Version); e != nil {
			t.Fatal(e)
		}
		return chat.Usage{}, d(chat.Delta{Text: noteOutput(msgs)})
	}}
	var c Conversation
	var in Input
	s, c, in = compactionFixture(t, m)
	parentID = c.PracticeID
	if _, e := s.BuildContext(t.Context(), c.ID, in, "teaching"); e == nil {
		t.Fatal("deleted summary result saved")
	}
	if _, e := s.Load(t.Context(), c.ID); e == nil {
		t.Fatal("summary resurrected page")
	}
}
