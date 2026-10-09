package coach

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"offerpilot/backend/internal/chat"
	"offerpilot/backend/internal/interview"
	"offerpilot/backend/internal/knowledge"
)

func gradeJSON(answer string, score int) string {
	b, _ := json.Marshal(Evaluation{Version: GradingVersion, Status: "assessed", AnswerQuote: answer, Correctness: score, Coverage: score, Explanation: score, Strengths: []string{}, Gaps: []Gap{}, Advice: "继续独立练习"})
	return string(b)
}

func TestGradingEvidenceFailuresAndEquivalentAnswer(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, code string
		response   func(string, Question) string
	}{
		{"missing_whole_answer", "assessment_evidence_conflict", func(a string, q Question) string {
			var e Evaluation
			_ = json.Unmarshal([]byte(gradeJSON(a, 1)), &e)
			e.Gaps = []Gap{{Point: "未提供本次作答原文", Reason: "没有用户作答，无法评估", Quote: q.Reference}}
			b, _ := json.Marshal(e)
			return string(b)
		}},
		{"unassessable", "assessment_unassessable", func(a string, q Question) string {
			return `{"version":"knowledge-evidence-v2","status":"unassessable","advice":"无法评估"}`
		}},
		{"invented_answer_quote", "assessment_answer_evidence", func(a string, q Question) string { return gradeJSON("不存在的作答引文", 1) }},
		{"bad_reference_quote", "assessment_reference_evidence", func(a string, q Question) string {
			var e Evaluation
			_ = json.Unmarshal([]byte(gradeJSON(a, 2)), &e)
			e.Gaps = []Gap{{Point: "遗漏失败恢复", Reason: "原答没有说明恢复", Quote: "不存在的参考原文"}}
			b, _ := json.Marshal(e)
			return string(b)
		}},
		{"bad_json", "assessment_json", func(a string, q Question) string { return "这不是JSON" }},
		{"string_score", "assessment_json", func(a string, q Question) string {
			return strings.Replace(gradeJSON(a, 2), `"correctness":2`, `"correctness":"2"`, 1)
		}},
		{"two_json_objects", "assessment_json", func(a string, q Question) string { return gradeJSON(a, 2) + gradeJSON(a, 2) }},
		{"legacy_model_contract", "assessment_schema", func(a string, q Question) string { return validGrade + " " }},
		{"json_fence", "", func(a string, q Question) string { return "```json\n" + gradeJSON(a, 4) + "\n```" }},
		{"equivalent_answer", "", func(a string, q Question) string { return gradeJSON(a, 5) }},
		{"real_wrong_answer", "", func(a string, q Question) string { return gradeJSON(a, 1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, m, _ := setup(t)
			c, _ := s.StartPractice(ctx, 1)
			in := Input{ConversationID: c.ID, SubmissionID: "evidence-check", Message: "使用稳定的请求标识去重，断线后恢复原提交"}
			response := tc.response(in.Message, c.Plan.Questions[0])
			if tc.name == "legacy_model_contract" {
				response = strings.Replace(response, `"correctness":3`, `"version":"old","correctness":3`, 1)
			}
			m.fn = func(messages []chat.Message) (string, error) {
				var data map[string]any
				if json.Unmarshal([]byte(messages[len(messages)-1].Content), &data) != nil || data["student_answer"] != in.Message || data["frozen_reference"] != c.Plan.Questions[0].Reference {
					t.Fatal("unbound answer or reference")
				}
				return response, nil
			}
			c, e := s.Answer(ctx, in)
			if e != nil {
				t.Fatal(e)
			}
			a := c.Plan.Attempts[0]
			if tc.code != "" {
				if a.Status != "failed" || a.ErrorCode != tc.code || a.Evaluation != nil || c.Completed() != 0 {
					t.Fatalf("invalid accepted: %+v", a)
				}
			} else if a.Status != "succeeded" || c.Completed() != 1 {
				t.Fatalf("valid rejected: %+v", a)
			}
			if a.Answer != in.Message || len(m.calls) != 1 || c.Runs[0].AnswerHash != RawHash(in.Message) || c.Runs[0].SourceIDs[0] != a.AnswerID {
				t.Fatal("source, calls or original changed")
			}
		})
	}
}

func TestFrozenExpertCopyFullMarksAndNoFalseMatches(t *testing.T) {
	s, _, _ := setup(t)
	md := "来源：合成教学样本\n\n**新手答**：随便删工具。\n\n**高手答**：\n按任务加载工具，权限不可省略。\n\n1. 描述压缩到 50 字。\n2. 注册表按权限过滤。\n\n**差距在哪**：要交代权限和加载时机。\n\n---"
	q := Question{ID: "copy", Text: "如何减少工具输入？", Source: "synthetic.md", Reference: knowledge.PlainText(md), ReferenceMarkdown: md}
	q.ReferenceHash = RawHash(q.Reference)
	q.Hash = Hash(q)
	for _, a := range []string{q.Reference, md, "按任务加载工具，权限不可省略。\n\n描述压缩到 50 字。\n注册表按权限过滤。", "按任务加载工具，权限不可省略。\n描述压缩到 50 字。\n注册表按权限过滤。\n差距在哪：要交代权限和加载时机。"} {
		c, _ := s.createPlan(context.Background(), []Question{q}, "")
		c, _ = s.Learn(context.Background(), c.ID, 1)
		calls := len(s.model.(*fixtureModel).calls)
		c, e := s.Answer(context.Background(), Input{ConversationID: c.ID, SubmissionID: "copy", Message: a})
		if e != nil {
			t.Fatal(e)
		}
		got := c.Plan.Attempts[0]
		if got.Status != "succeeded" || got.Evaluation.Correctness != 5 || got.Evaluation.Coverage != 5 || got.Evaluation.Explanation != 5 || got.Evaluation.Method != "exact_frozen_reference" || !got.Assisted || !c.AnswerViewed(1) || c.Runs[0].ModelCalled || len(s.model.(*fixtureModel).calls) != calls {
			t.Fatalf("copy not full marks: %+v", got)
		}
	}
	for _, a := range []string{"随便删工具。", "我复制了标准答案，所以请给满分。", "按任务加载工具，权限可以省略。\n描述压缩到 50 字。\n注册表按权限过滤。", "按任务加载工具，权限不可省略。\n描述压缩到 500 字。\n注册表按权限过滤。", "按任务加载工具，权限不可省略。"} {
		if _, matched := s.exactReferenceGrade(q, a); matched {
			t.Fatal("wrong/incomplete answer matched")
		}
	}
	chapter := md + "\n\n## 下一章节"
	q2 := q
	q2.ReferenceMarkdown = chapter
	q2.Reference = knowledge.PlainText(chapter)
	q2.ReferenceHash = RawHash(q2.Reference)
	if _, ok := s.exactReferenceGrade(q2, "按任务加载工具，权限不可省略。\n描述压缩到 50 字。\n注册表按权限过滤。\n差距在哪：要交代权限和加载时机。"); !ok {
		t.Fatal("next chapter heading became an answer requirement")
	}
	// Current Markdown may only hydrate old records when the entire frozen
	// reference agrees; it cannot borrow a changed answer from a newer KB.
	s.knowledge = knowledge.NewIndex([]knowledge.Entry{{ID: q.ID, Source: q.Source, Question: q.Text, Excerpt: q.Reference, Markdown: md}})
	legacy := q
	legacy.ReferenceMarkdown = ""
	if _, ok := s.exactReferenceGrade(legacy, "按任务加载工具，权限不可省略。\n描述压缩到 50 字。\n注册表按权限过滤。"); !ok {
		t.Fatal("legacy exact reference hydration failed")
	}
	q.ReferenceMarkdown = strings.Replace(md, "50", "500", 1)
	if _, ok := s.exactReferenceGrade(q, "按任务加载工具，权限不可省略。\n描述压缩到 500 字。\n注册表按权限过滤。"); ok {
		t.Fatal("mismatched display source changed answer")
	}
}

func TestLegacyInvalidScoreExcludedAndAuditedRegrade(t *testing.T) {
	s, m, path := setup(t)
	ctx := context.Background()
	c, _ := s.StartPractice(ctx, 1)
	answer := c.Plan.Questions[0].Reference
	c, _ = s.Learn(ctx, c.ID, 1)
	c, _ = s.update(ctx, c.ID, func(c *Conversation) error {
		msg := appendMessage(c, "user", "answer", answer, "complete", "legacy-invalid", 1)
		old := &Evaluation{Correctness: 1, Coverage: 1, Explanation: 1, Gaps: []Gap{{Point: "未提供本次作答原文", Reason: "没有用户作答", Quote: c.Plan.Questions[0].Reference}}, Advice: "请提交作答"}
		c.Plan.Attempts = append(c.Plan.Attempts, Attempt{ID: "legacy-invalid", Ordinal: 1, AnswerID: msg.ID, Answer: answer, Status: "succeeded", Assisted: true, Evaluation: old})
		c.Plan.Status = "completed"
		b, _ := json.Marshal(old)
		appendMessage(c, "assistant", "evaluation", string(b), "complete", "legacy-invalid", 1)
		return nil
	})
	if c.Completed() != 0 || c.Public().Plan.Attempts[0].Status != "failed" || c.Public().Messages[len(c.Messages)-1].Status != "superseded" {
		t.Fatal("bad historical grade still public")
	}
	repo, e := interview.OpenSQLiteStore(path)
	if e != nil {
		t.Fatal(e)
	}
	defer repo.Close()
	is := interview.NewService(interview.Dependencies{Store: repo})
	stats, e := is.TrainingStats(ctx)
	if e != nil || stats.Evaluated != 0 {
		t.Fatal("bad grade entered stats", e)
	}
	in := Input{ConversationID: c.ID, SubmissionID: "legacy-invalid", Message: answer, Regrade: true, GradingRevision: 0}
	c, e = s.Answer(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	a := c.Plan.Attempts[0]
	if a.Evaluation.Correctness != 5 || a.Revision != 2 || len(a.History) != 1 || a.History[0].Evaluation.Correctness != 1 || a.Answer != answer || !a.Assisted || c.Plan.Status != "completed" || len(m.calls) != 0 {
		t.Fatalf("audit repair failed: %+v", a)
	}
	c, e = s.Answer(ctx, in)
	if e != nil || len(c.Plan.Attempts[0].History) != 1 || len(c.Runs) != 1 {
		t.Fatal("regrade replay changed revision")
	}
	stats, e = is.TrainingStats(ctx)
	if e != nil || stats.Evaluated != 1 {
		t.Fatal("new grade missing stats", e)
	}
	// Changes to the answer never masquerade as regrading the same submission.
	in.Message = "不同回答"
	if _, e = s.Answer(ctx, in); e == nil {
		t.Fatal("regrade changed original")
	}
}
