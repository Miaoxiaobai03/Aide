package interview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
)

func trainingHash(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func normalizedGap(value string) string {
	value = strings.NewReplacer("没有解释", "未解释", "未说明", "未解释", "没有说明", "未解释", "缺少解释", "未解释").Replace(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}
func uncertainGap(value string) bool {
	for _, word := range []string{"无法核实", "待核实", "无法验证", "尚未核实", "未验证", "材料不足", "信息不足", "证据不足", "无法判断", "unverified", "not_in_material"} {
		if strings.Contains(strings.ToLower(value), word) {
			return true
		}
	}
	return false
}

func gapCriterion(label string) string {
	for _, prefix := range []string{"未解释", "没有解释", "未说明", "没有说明", "缺少解释"} {
		if strings.HasPrefix(label, prefix) {
			return "清楚解释" + strings.TrimSpace(strings.TrimPrefix(label, prefix))
		}
	}
	return "针对以下原始差距给出有依据的说明：" + label
}
func (s *Service) Weaknesses(ctx context.Context, sourceID string) (WeaknessPage, error) {
	record, err := s.ownedSession(ctx, sourceID)
	if err != nil {
		return WeaknessPage{}, err
	}
	page := WeaknessPage{Items: []Weakness{}, State: "ready"}
	if !feedbackVisible(record) {
		page.State = "deferred"
		page.Reason = "训练结束后才公开薄弱点"
		return page, nil
	}
	repo, err := s.trainingRepo()
	if err != nil {
		return page, err
	}
	edits, err := repo.TrainingDocuments(ctx, "weakness")
	if err != nil {
		return page, unavailable("薄弱点修订读取失败", err)
	}
	byID := map[string]*Weakness{}
	valid := 0
	for _, turn := range record.Answers {
		allowed := evidenceIDSet(turn.Question.EvidenceRefs)
		if validateAssessment(record.Sources, turn.Assessment, allowed) != nil {
			continue
		}
		valid++
		public := turn
		publicRecordEvidence(record.Sources, &public)
		for _, issue := range []struct {
			kind   string
			values []string
		}{{"gap", public.Assessment.Gaps}, {"factual_error", public.Assessment.FactualErrors}} {
			for _, label := range issue.values {
				area := coveragePointByID(record.Profile, turn.Question.CoveragePointID).Area
				irrelevant := (turn.Question.Kind == QuestionKnowledge || area == FocusKnowledge) && (strings.Contains(label, "个人职责") || strings.Contains(label, "个人贡献") || strings.Contains(strings.ToLower(label), "ownership"))
				if strings.TrimSpace(label) == "" || uncertainGap(label) || irrelevant {
					continue
				}
				// Coverage IDs remain scoped to this training and material. Text
				// normalization merges formatting duplicates, never unrelated projects.
				scopeKind := string(area)
				if scopeKind == "" {
					scopeKind = string(turn.Question.Kind)
				}
				scopePoint := turn.Question.CoveragePointID
				if scopePoint == "" {
					scopePoint = "unknown/" + turn.Question.ID
				}
				scope := record.ID + "/" + scopePoint + "/" + scopeKind
				id := "weakness-" + trainingHash([]string{scope, issue.kind, normalizedGap(label)})[:24]
				w := byID[id]
				if w == nil {
					w = &Weakness{ID: id, Label: label, Scope: scope, Issue: issue.kind, Status: "candidate", Criteria: []string{gapCriterion(label)}, Sources: []WeaknessSource{}}
					byID[id] = w
				}
				w.Sources = append(w.Sources, WeaknessSource{InterviewID: record.ID, QuestionID: turn.Question.ID, Question: public.Question.Text, Answer: turn.Answer.Text, Feedback: label, Evidence: public.Assessment.EvidenceRefs, At: turn.AnsweredAt})
			}
		}
	}
	observations, err := s.allObservations(ctx)
	if err != nil {
		return page, err
	}
	for _, w := range byID {
		if raw, ok := edits[w.ID]; ok {
			var edit weaknessEdit
			if json.Unmarshal(raw, &edit) != nil {
				return page, unavailable("薄弱点修订损坏", nil)
			}
			_, version, e := repo.LoadTrainingDocument(ctx, "weakness", w.ID)
			if e != nil {
				return page, unavailable("薄弱点修订读取失败", e)
			}
			w.Label = edit.Current.Label
			w.Criteria = edit.Current.Criteria
			w.Status = edit.Current.Status
			w.Revision = version
			w.UpdatedAt = edit.Current.At
			w.UpdateSequence = edit.Current.Sequence
		}
		if w.Status == "confirmed" {
			for _, o := range observations {
				matches := false
				for _, target := range o.Task.Targets {
					if target.ID == w.ID && target.Label == w.Label && trainingHash(target.Criteria) == trainingHash(w.Criteria) {
						matches = true
					}
				}
				if !matches {
					continue
				}
				if o.Attempt.Sequence > w.UpdateSequence || o.Attempt.Sequence == 0 && w.UpdateSequence == 0 && !o.Attempt.CreatedAt.Before(w.UpdatedAt) {
					if state := targetOutcome(o, w.ID); state != "" {
						w.Status = state
					}
				}
			}
		}
		page.Items = append(page.Items, *w)
	}
	if valid == 0 {
		page.State = "no_evaluation"
		page.Reason = "没有可用的完整评价，不能判断是否存在薄弱点"
	} else if len(page.Items) == 0 {
		page.State = "no_actionable_gap"
		page.Reason = "现有评价没有可确认的能力差距；待核实陈述不自动定为能力错误"
	}
	sort.Slice(page.Items, func(i, j int) bool { return page.Items[i].ID < page.Items[j].ID })
	return page, nil
}
func targetOutcome(o AttemptObservation, id string) string {
	if o.Attempt.Status != "succeeded" || o.Attempt.Evaluation == nil {
		return ""
	}
	seen := false
	unmet := false
	for _, r := range o.Attempt.Evaluation.Results {
		if r.TargetID == id {
			seen = true
			if r.Status == "unassessed" {
				return ""
			}
			if r.Status == "unmet" {
				unmet = true
			}
		}
	}
	if !seen {
		return ""
	}
	if unmet {
		return "needs_practice"
	}
	return "passed_once"
}
func (s *Service) EditWeakness(ctx context.Context, input RetestMutation) (WeaknessPage, error) {
	page, err := s.Weaknesses(ctx, input.SourceID)
	if err != nil {
		return page, err
	}
	var selected *Weakness
	for i := range page.Items {
		if page.Items[i].ID == input.ID {
			selected = &page.Items[i]
			break
		}
	}
	if selected == nil {
		return page, validation("id", "薄弱点不存在或不属于本场训练")
	}
	if input.Status != "confirmed" && input.Status != "candidate" && input.Status != "not_applicable" {
		return page, validation("status", "请选择确认、取消确认或不适用")
	}
	if len(strings.TrimSpace(input.Label)) == 0 || len(input.Label) > 1200 || len(input.Criteria) == 0 || len(input.Criteria) > 10 {
		return page, validation("criteria", "需要具体名称和 1—10 条验收标准")
	}
	seenCriteria := map[string]bool{}
	for i, criterion := range input.Criteria {
		criterion = strings.TrimSpace(criterion)
		input.Criteria[i] = criterion
		if strings.TrimSpace(criterion) == "" || len(criterion) > 2000 {
			return page, validation("criteria", "验收标准为空或过长")
		}
		if seenCriteria[criterion] {
			return page, validation("criteria", "验收标准不能重复")
		}
		seenCriteria[criterion] = true
	}
	record, err := s.ownedSession(ctx, input.SourceID)
	if err != nil {
		return page, err
	}
	if generatedTextLeaksKnowledgeReference(record.Sources, input.Label) {
		return page, validation("label", "不能把参考答案写入公开目标")
	}
	for _, c := range input.Criteria {
		if generatedTextLeaksKnowledgeReference(record.Sources, c) {
			return page, validation("criteria", "验收标准不能泄露参考答案")
		}
	}
	repo, _ := s.trainingRepo()
	edit := weaknessEdit{History: []WeaknessRevision{}}
	if raw, _, e := repo.LoadTrainingDocument(ctx, "weakness", input.ID); e == nil {
		if json.Unmarshal(raw, &edit) != nil {
			return page, unavailable("修订记录损坏", nil)
		}
	} else if !errors.Is(e, ErrStoreNotFound) {
		return page, unavailable("修订读取失败", e)
	}
	edit.Current = WeaknessRevision{Label: strings.TrimSpace(input.Label), Criteria: input.Criteria, Status: input.Status, At: s.clock.Now()}
	sequence, err := repo.NextTrainingSequence(ctx)
	if err != nil {
		return page, unavailable("修订序号保存失败", err)
	}
	edit.Current.Sequence = sequence
	edit.History = append(edit.History, edit.Current)
	if err := repo.SaveTrainingDocument(ctx, "weakness", input.ID, edit, input.Revision); err != nil {
		if errors.Is(err, ErrStoreConflict) {
			return page, conflict("薄弱点已被其他页面修改，请刷新", err)
		}
		return page, unavailable("修订保存失败", err)
	}
	return s.Weaknesses(ctx, input.SourceID)
}
