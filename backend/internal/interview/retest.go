package interview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const retestBatchSize = 20
const retestLease = 4 * time.Minute

func safeTrainingID(id string) bool {
	if len(id) == 0 || len(id) > 200 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._:-", r)) {
			return false
		}
	}
	return true
}
func (s *Service) loadPlan(ctx context.Context, id string) (RetestPlan, error) {
	repo, err := s.trainingRepo()
	if err != nil {
		return RetestPlan{}, err
	}
	raw, version, err := repo.LoadTrainingDocument(ctx, "retest", id)
	if errors.Is(err, ErrStoreNotFound) {
		return RetestPlan{}, &DomainError{Code: CodeNotFound, Message: "复测计划不存在"}
	}
	if err != nil {
		return RetestPlan{}, unavailable("复测计划读取失败", err)
	}
	var plan RetestPlan
	if json.Unmarshal(raw, &plan) != nil || plan.ID != id || plan.ProfileID != LocalProfileID {
		return plan, unavailable("复测计划损坏或归属不一致", nil)
	}
	plan.Version = version
	source, e := frozenSession(plan)
	if e != nil || source.ID != plan.SourceID || len(plan.Tasks) == 0 {
		return RetestPlan{}, unavailable("复测冻结材料或任务损坏", e)
	}
	return plan, nil
}
func (s *Service) savePlan(ctx context.Context, plan *RetestPlan) error {
	repo, err := s.trainingRepo()
	if err != nil {
		return err
	}
	err = repo.SaveTrainingDocument(ctx, "retest", plan.ID, plan, plan.Version)
	if errors.Is(err, ErrStoreConflict) {
		return conflict("计划已变化，请读取最新状态后重试", err)
	}
	if err != nil {
		return unavailable("复测计划保存失败", err)
	}
	plan.Version++
	return nil
}
func frozenSession(plan RetestPlan) (InterviewSession, error) {
	return unmarshalSQLiteSession(plan.FrozenSource)
}
func (s *Service) publicPlan(plan RetestPlan) RetestPlan {
	// Clone before redaction: returned slices must not alter the saved snapshot.
	raw, _ := json.Marshal(plan)
	var result RetestPlan
	_ = json.Unmarshal(raw, &result)
	source, err := frozenSession(plan)
	result.FrozenSource = nil
	if err != nil {
		return RetestPlan{ID: plan.ID, Status: "invalid", Error: "冻结材料损坏，未公开任务内容"}
	}
	for i := range result.Tasks {
		r := AnswerRecord{Question: result.Tasks[i].Question}
		publicRecordEvidence(source.Sources, &r)
		result.Tasks[i].Question = r.Question
		texts := publicGeneratedStrings(source.Sources, []string{result.Tasks[i].BindingReason})
		if len(texts) > 0 {
			result.Tasks[i].BindingReason = texts[0]
		} else {
			result.Tasks[i].BindingReason = ""
		}
	}
	for i := range result.Attempts {
		if result.Attempts[i].Evaluation != nil {
			result.Attempts[i].Evaluation.Assessment = publicAssessment(source.Sources, result.Attempts[i].Evaluation.Assessment)
			for j := range result.Attempts[i].Evaluation.Results {
				r := &result.Attempts[i].Evaluation.Results[j]
				safe := publicGeneratedStrings(source.Sources, []string{r.Reason})
				if len(safe) > 0 {
					r.Reason = safe[0]
				} else {
					r.Reason = "评价依据未能安全公开，请核对"
				}
			}
		}
	}
	return result
}
func (s *Service) RetestPlan(ctx context.Context, id string) (RetestPlan, error) {
	p, e := s.loadPlan(ctx, id)
	if e != nil {
		return p, e
	}
	return s.publicPlan(p), nil
}

func (s *Service) CreateRetest(ctx context.Context, input RetestMutation) (RetestPlan, error) {
	if input.Mode != "variant" && input.Mode != "original" {
		return RetestPlan{}, validation("mode", "请选择变式题或原题重答")
	}
	if len(input.IDs) == 0 || len(input.IDs) > 1000 {
		return RetestPlan{}, validation("ids", "请选择 1—1000 个薄弱点，超过20项自动分批")
	}
	if !safeTrainingID(input.PlanID) {
		return RetestPlan{}, validation("planId", "需要稳定的计划请求标识")
	}
	// A retried creation uses the existing frozen plan, never new model output.
	if existing, e := s.loadPlan(ctx, input.PlanID); e == nil {
		wanted := map[string]bool{}
		for _, id := range input.IDs {
			if wanted[id] {
				return existing, validation("ids", "不能重复选择同一目标")
			}
			wanted[id] = true
		}
		actual := map[string]bool{}
		for _, t := range existing.Tasks {
			for _, target := range t.Targets {
				actual[target.ID] = true
			}
		}
		if existing.SourceID != input.SourceID || existing.Mode != input.Mode || trainingHash(actual) != trainingHash(wanted) {
			return RetestPlan{}, conflict("同一计划标识对应不同请求", nil)
		}
		return s.publicPlan(existing), nil
	} else if !IsCode(e, CodeNotFound) {
		return RetestPlan{}, e
	}
	page, err := s.Weaknesses(ctx, input.SourceID)
	if err != nil {
		return RetestPlan{}, err
	}
	available := map[string]Weakness{}
	for _, w := range page.Items {
		available[w.ID] = w
	}
	source, err := s.ownedSession(ctx, input.SourceID)
	if err != nil {
		return RetestPlan{}, err
	}
	snapshot, err := marshalSQLiteSession(source)
	if err != nil {
		return RetestPlan{}, err
	}
	plan := RetestPlan{ID: input.PlanID, ProfileID: LocalProfileID, SourceID: source.ID, Mode: input.Mode, CreatedAt: s.clock.Now(), Status: "pending", MaterialVersion: trainingHash(source.Sources), ScoringVersion: "retest-criteria-v1", FrozenSource: snapshot, Tasks: []RetestTask{}, Attempts: []RetestAttempt{}}
	byOriginal := map[string]int{}
	seen := map[string]bool{}
	for _, id := range input.IDs {
		if seen[id] {
			return plan, validation("ids", "不能重复选择同一目标")
		}
		seen[id] = true
		w, ok := available[id]
		if !ok || w.Status == "candidate" || w.Status == "not_applicable" || len(w.Sources) == 0 {
			return plan, validation("ids", "目标失效、不适用或尚未确认")
		}
		originalID := w.Sources[0].QuestionID
		var question *Question
		for _, turn := range source.Answers {
			if turn.Question.ID == originalID {
				copy := turn.Question
				question = &copy
				break
			}
		}
		if question == nil || len(anchorsForEvidence(source.Sources, question.EvidenceRefs)) == 0 {
			return plan, validation("ids", "原题或证据缺失，请取消该目标后重试")
		}
		if err := validateQuestionDraft(source.Sources, QuestionDraft{Text: question.Text, EvidenceRefs: publicQuestionEvidence(source.Sources, question.EvidenceRefs)}, evidenceIDSet(question.EvidenceRefs), nil); err != nil {
			return plan, validation("ids", "原题或证据不能安全核对，请取消该目标")
		}
		target := RetestTarget{ID: w.ID, Label: w.Label, Criteria: append([]string{}, w.Criteria...)}
		if index, ok := byOriginal[originalID]; ok && input.Mode == "original" {
			plan.Tasks[index].Targets = append(plan.Tasks[index].Targets, target)
			continue
		}
		index := len(plan.Tasks)
		byOriginal[originalID] = index
		plan.Tasks = append(plan.Tasks, RetestTask{ID: s.ids.NewID("task"), OriginalQuestionID: originalID, Question: *question, Targets: []RetestTarget{target}, Status: "pending", Batch: index / retestBatchSize})
	}
	for i := range plan.Tasks {
		plan.Tasks[i].CriteriaVersion = trainingHash(plan.Tasks[i].Targets)
	}
	if err := s.savePlan(ctx, &plan); err != nil {
		return RetestPlan{}, err
	}
	return s.publicPlan(plan), nil
}

func (s *Service) PrepareRetest(ctx context.Context, id string, batch int) (RetestPlan, error) {
	plan, err := s.loadPlan(ctx, id)
	if err != nil {
		return plan, err
	}
	if batch < 0 || batch > (len(plan.Tasks)-1)/retestBatchSize {
		return plan, validation("batch", "复测批次不存在")
	}
	if plan.Status == "preparing" && plan.LeaseUntil.After(s.clock.Now()) {
		return s.publicPlan(plan), nil
	}
	if plan.Status == "paused" {
		return plan, conflict("请先继续已暂停的计划", nil)
	}
	agent, ok := s.agent.(RetestAgent)
	if !ok {
		return s.publicPlan(plan), unavailable("复测模型未配置；计划和历史已保留", nil)
	}
	source, err := frozenSession(plan)
	if err != nil {
		return plan, unavailable("冻结材料损坏", err)
	}
	allReady := true
	for _, task := range plan.Tasks {
		if task.Batch == batch && task.Status != "ready" {
			allReady = false
		}
	}
	if allReady {
		return s.publicPlan(plan), nil
	}
	plan.Status = "preparing"
	plan.LeaseUntil = s.clock.Now().Add(retestLease)
	plan.Error = ""
	if err := s.savePlan(ctx, &plan); err != nil {
		return plan, err
	}
	// Persist even when the HTTP/model request is cancelled. Failed inputs and
	// pending tasks survive, while model execution still obeys the request ctx.
	// Each checkpoint gets a fresh persistence deadline after model execution.
	// A deadline created before a slow model call would already be expired.
	saveCheckpoint := func() error {
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		return s.savePlan(persistCtx, &plan)
	}
	for i := range plan.Tasks {
		task := &plan.Tasks[i]
		if task.Batch != batch || task.Status == "ready" {
			continue
		}
		taskCtx, cancelTask := context.WithTimeout(ctx, 90*time.Second)
		if plan.Mode == "original" {
			if checkErr := s.checkRetestTask(taskCtx, agent, source, *task); checkErr != nil {
				cancelTask()
				task.Status = "failed"
				task.Error = "任务与标准核验失败，可重试"
				plan.Status = "preparation_failed"
				plan.Error = task.Error
				plan.LeaseUntil = time.Time{}
				if e := saveCheckpoint(); e != nil {
					return plan, e
				}
				return s.publicPlan(plan), nil
			}
			task.Status = "ready"
			task.BindingReason = "原题重答，逐项检查选中的验收标准"
		} else {
			original := task.Question
			original.EvidenceRefs = publicQuestionEvidence(source.Sources, original.EvidenceRefs)
			request := RetestQuestionRequest{Original: original, Targets: task.Targets, Anchors: publicQuestionAnchors(anchorsForEvidence(source.Sources, task.Question.EvidenceRefs))}
			var draft RetestQuestionDraft
			var generationErr error
			for attempt := 0; attempt < 2; attempt++ {
				draft, generationErr = agent.GenerateRetest(taskCtx, request)
				if generationErr != nil {
					break
				}
				generationErr = validateRetestDraft(source, *task, draft)
				if generationErr == nil {
					candidate := *task
					candidate.Question.Text = draft.Question.Text
					generationErr = s.checkRetestTask(taskCtx, agent, source, candidate)
					if generationErr == nil {
						break
					}
				}
				request.Repair = &RepairInstruction{Reason: generationErr.Error(), AllowedEvidence: original.EvidenceRefs}
			}
			if generationErr != nil {
				cancelTask()
				task.Status = "failed"
				task.Error = "出题或依据校验失败，可重试"
				plan.Error = task.Error
				plan.Status = "preparation_failed"
				plan.LeaseUntil = time.Time{}
				if e := saveCheckpoint(); e != nil {
					return plan, e
				}
				return s.publicPlan(plan), nil
			}
			task.Question.Text = strings.TrimSpace(draft.Question.Text)
			task.Question.ID = s.ids.NewID("retest-question")
			task.Status = "ready"
			task.Error = ""
			task.BindingReason = draft.BindingReason
		}
		cancelTask()
		plan.LeaseUntil = s.clock.Now().Add(retestLease)
		if err := saveCheckpoint(); err != nil {
			return s.publicPlan(plan), err
		}
	}
	plan.Status = "ready"
	plan.LeaseUntil = time.Time{}
	if err := saveCheckpoint(); err != nil {
		return plan, err
	}
	return s.publicPlan(plan), nil
}

// HTTP preparation acknowledges a persisted queue entry immediately. The
// worker checkpoints each task; after a process stop the bounded lease permits
// a user-triggered retry without changing already generated questions.
func (s *Service) QueueRetest(ctx context.Context, id string, batch int) (RetestPlan, error) {
	plan, err := s.loadPlan(ctx, id)
	if err != nil {
		return plan, err
	}
	if batch < 0 || batch > (len(plan.Tasks)-1)/retestBatchSize {
		return plan, validation("batch", "复测批次不存在")
	}
	if plan.Status == "paused" {
		return plan, conflict("请先继续已暂停的计划", nil)
	}
	if (plan.Status == "preparing" || plan.Status == "queued") && plan.LeaseUntil.After(s.clock.Now()) {
		return s.publicPlan(plan), nil
	}
	ready := true
	for _, task := range plan.Tasks {
		if task.Batch == batch && task.Status != "ready" {
			ready = false
		}
	}
	if ready {
		return s.publicPlan(plan), nil
	}
	if _, ok := s.agent.(RetestAgent); !ok {
		return s.publicPlan(plan), unavailable("复测模型未配置", nil)
	}
	plan.Status = "queued"
	plan.LeaseUntil = s.clock.Now().Add(retestLease)
	plan.Error = ""
	if err := s.savePlan(ctx, &plan); err != nil {
		return plan, err
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
		defer cancel()
		_, _ = s.PrepareRetest(runCtx, id, batch)
	}()
	return s.publicPlan(plan), nil
}
func validateRetestDraft(source InterviewSession, task RetestTask, draft RetestQuestionDraft) error {
	if draft.Difficulty != task.Question.Difficulty || !draft.Answerable || strings.TrimSpace(draft.BindingReason) == "" {
		return errors.New("必须同难度、可回答，并说明与目标标准的对应")
	}
	wanted := map[string]bool{}
	for _, t := range task.Targets {
		wanted[t.ID] = true
	}
	actual := map[string]bool{}
	for _, id := range draft.TargetIDs {
		if actual[id] {
			return errors.New("重复目标")
		}
		actual[id] = true
	}
	if trainingHash(wanted) != trainingHash(actual) {
		return errors.New("任务必须准确覆盖全部目标")
	}
	if generatedTextLeaksKnowledgeReference(source.Sources, draft.BindingReason) {
		return errors.New("对应说明泄露参考答案")
	}
	return validateQuestionDraft(source.Sources, draft.Question, evidenceIDSet(task.Question.EvidenceRefs), []AnswerRecord{{Question: task.Question}})
}

func (s *Service) checkRetestTask(ctx context.Context, agent RetestAgent, source InterviewSession, task RetestTask) error {
	check, err := agent.ValidateRetest(ctx, RetestAssessmentRequest{Question: task.Question, Targets: task.Targets, Anchors: anchorsForEvidence(source.Sources, task.Question.EvidenceRefs)})
	if err != nil {
		return err
	}
	if !check.Valid || strings.TrimSpace(check.Reason) == "" {
		return errors.New("任务与标准的语义／可回答性未通过核验")
	}
	wanted := map[string]bool{}
	actual := map[string]bool{}
	for _, t := range task.Targets {
		wanted[t.ID] = true
	}
	for _, id := range check.TargetIDs {
		if actual[id] {
			return errors.New("核验目标重复")
		}
		actual[id] = true
	}
	if trainingHash(wanted) != trainingHash(actual) {
		return errors.New("语义核验未覆盖全部目标")
	}
	if generatedTextLeaksKnowledgeReference(source.Sources, check.Reason) {
		return errors.New("核验说明泄露参考内容")
	}
	return validateEvidenceRefs(source.Sources, check.EvidenceRefs, evidenceIDSet(task.Question.EvidenceRefs), true)
}

func (s *Service) SubmitRetest(ctx context.Context, input RetestMutation) (RetestPlan, error) {
	if !safeTrainingID(input.AttemptID) {
		return RetestPlan{}, validation("attemptId", "需要稳定的提交标识")
	}
	if strings.TrimSpace(input.Answer.Text) == "" || len([]rune(input.Answer.Text)) > 20000 || input.Answer.DurationMS < 0 || input.Answer.DurationMS > 24*60*60*1000 {
		return RetestPlan{}, validation("answer", "回答为空、过长或时长非法")
	}
	if input.Answer.InputMode != InputModeText && input.Answer.InputMode != InputModeVoice {
		return RetestPlan{}, validation("inputMode", "回答模式非法")
	}
	plan, err := s.loadPlan(ctx, input.PlanID)
	if err != nil {
		return plan, err
	}
	source, err := frozenSession(plan)
	if err != nil {
		return plan, unavailable("冻结材料损坏", err)
	}
	var task *RetestTask
	for i := range plan.Tasks {
		if plan.Tasks[i].ID == input.TaskID {
			task = &plan.Tasks[i]
			break
		}
	}
	if task == nil {
		return plan, validation("taskId", "任务不属于本计划")
	}
	index := -1
	for i, a := range plan.Attempts {
		if a.ID == input.AttemptID {
			index = i
			if a.TaskID != input.TaskID || trainingHash(a.Answer) != trainingHash(input.Answer) {
				return plan, conflict("同一提交标识对应不同内容", nil)
			}
			if a.Status == "succeeded" || a.Status == "running" && a.LeaseUntil.After(s.clock.Now()) {
				return s.publicPlan(plan), nil
			}
			if !input.Retry {
				return s.publicPlan(plan), nil
			}
			break
		}
	}
	if plan.Status == "paused" || plan.Status == "preparing" || plan.Status == "queued" {
		return plan, conflict("计划暂停或题目准备中", nil)
	}
	for _, t := range plan.Tasks {
		if t.Batch == task.Batch && t.Status != "ready" {
			return plan, conflict("本批题目尚未全部准备好", nil)
		}
	}
	for i, a := range plan.Attempts {
		if a.TaskID == task.ID && i != index {
			if a.Status == "succeeded" {
				return plan, conflict("本任务已完成；再练请创建新计划", nil)
			}
			return plan, conflict("已有回答待处理，请使用其提交标识重试", nil)
		}
	}
	agent, ok := s.agent.(RetestAgent)
	if index < 0 {
		repo, _ := s.trainingRepo()
		sequence, err := repo.NextTrainingSequence(ctx)
		if err != nil {
			return plan, unavailable("回答序号保存失败", err)
		}
		plan.Attempts = append(plan.Attempts, RetestAttempt{ID: input.AttemptID, TaskID: task.ID, Answer: input.Answer, CreatedAt: s.clock.Now(), Sequence: sequence})
		index = len(plan.Attempts) - 1
	} else {
		plan.Attempts[index].Retries++
	}
	a := &plan.Attempts[index]
	a.Status = "running"
	a.Error = ""
	a.LeaseUntil = s.clock.Now().Add(retestLease)
	if err := s.savePlan(ctx, &plan); err != nil {
		return plan, err
	}
	if !ok {
		a.Status = "failed"
		a.Error = "评价模型未配置，回答已保存，请配置模型后重试"
		a.LeaseUntil = time.Time{}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := s.savePlan(persistCtx, &plan); err != nil {
			return plan, err
		}
		return s.publicPlan(plan), nil
	}
	request := RetestAssessmentRequest{Question: task.Question, Targets: task.Targets, Answer: input.Answer, Anchors: anchorsForEvidence(source.Sources, task.Question.EvidenceRefs)}
	var evaluation RetestEvaluation
	var evaluationErr error
	evaluationCtx, cancelEvaluation := context.WithTimeout(ctx, 180*time.Second)
	defer cancelEvaluation()
	for attempt := 0; attempt < 2; attempt++ {
		evaluation, evaluationErr = agent.AssessRetest(evaluationCtx, request)
		if evaluationErr != nil {
			break
		}
		evaluationErr = validateRetestEvaluation(source, *task, input.Answer, evaluation)
		if evaluationErr == nil {
			break
		}
		request.Repair = &RepairInstruction{Reason: evaluationErr.Error(), AllowedEvidence: task.Question.EvidenceRefs}
	}
	a.LeaseUntil = time.Time{}
	if evaluationErr != nil {
		a.Status = "failed"
		a.Error = "评价暂不可用或依据校验失败，回答已保存，请重试"
	} else {
		a.Status = "succeeded"
		a.Evaluation = &evaluation
	}
	// Every task needs one valid assessment, even an incorrect or explicitly
	// unassessed answer. Unknown conclusions do not alter weakness mastery.
	complete := true
	for _, t := range plan.Tasks {
		done := false
		for _, attempt := range plan.Attempts {
			if attempt.TaskID == t.ID && attempt.Status == "succeeded" {
				done = true
			}
		}
		if !done {
			complete = false
		}
	}
	if complete {
		plan.Status = "completed"
	} else {
		plan.Status = "ready"
	}
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	// Merge the result with concurrent task completions using CAS. A pause
	// remains a pause and cannot be undone by a late model response.
	finished := *a
	for retry := 0; retry < 5; retry++ {
		if err := s.savePlan(persistCtx, &plan); err == nil {
			return s.publicPlan(plan), nil
		} else if !IsCode(err, CodeConflict) {
			return plan, err
		}
		latest, e := s.loadPlan(persistCtx, plan.ID)
		if e != nil {
			return plan, e
		}
		found := false
		for i := range latest.Attempts {
			if latest.Attempts[i].ID == finished.ID {
				if latest.Attempts[i].Status == "succeeded" || latest.Attempts[i].Retries > finished.Retries {
					return s.publicPlan(latest), nil
				}
				latest.Attempts[i] = finished
				found = true
				break
			}
		}
		if !found {
			return plan, conflict("提交记录已变化，请刷新", nil)
		}
		plan = latest
		refreshPlanCompletion(&plan)
	}
	return s.publicPlan(plan), conflict("保存结果遇到并发修改，请读取提交状态", nil)
}
func refreshPlanCompletion(plan *RetestPlan) {
	if plan.Status == "paused" {
		return
	}
	complete := true
	for _, t := range plan.Tasks {
		done := false
		for _, a := range plan.Attempts {
			if a.TaskID == t.ID && a.Status == "succeeded" {
				done = true
			}
		}
		if !done {
			complete = false
		}
	}
	if complete {
		plan.Status = "completed"
	} else {
		plan.Status = "ready"
	}
}
func validateRetestEvaluation(source InterviewSession, task RetestTask, answer AnswerPayload, e RetestEvaluation) error {
	if err := validateAssessment(source.Sources, e.Assessment, evidenceIDSet(task.Question.EvidenceRefs)); err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, t := range task.Targets {
		for _, c := range t.Criteria {
			expected[t.ID+"\x00"+c] = true
		}
	}
	for _, r := range e.Results {
		key := r.TargetID + "\x00" + r.Criterion
		if !expected[key] {
			return errors.New("评价目标或标准重复／不匹配")
		}
		delete(expected, key)
		if r.Status != "met" && r.Status != "unmet" && r.Status != "unassessed" {
			return errors.New("评价状态非法")
		}
		if strings.TrimSpace(r.Reason) == "" {
			return errors.New("逐项评价需要原因")
		}
		if r.AnswerQuote != "" && !strings.Contains(answer.Text, r.AnswerQuote) {
			return errors.New("回答引用并非用户原文")
		}
		if r.Status == "met" && strings.TrimSpace(r.AnswerQuote) == "" {
			return errors.New("达标项需要原文依据")
		}
		if generatedTextLeaksKnowledgeReference(source.Sources, r.Reason) {
			return errors.New("反馈泄露私有参考答案")
		}
	}
	if len(expected) != 0 {
		return errors.New("评价遗漏部分验收标准")
	}
	return nil
}
func (s *Service) PauseRetest(ctx context.Context, id string, pause bool) (RetestPlan, error) {
	plan, err := s.loadPlan(ctx, id)
	if err != nil {
		return plan, err
	}
	if plan.Status == "completed" {
		return s.publicPlan(plan), nil
	}
	if plan.Status == "preparing" && plan.LeaseUntil.After(s.clock.Now()) {
		return plan, conflict("出题处理中，请稍后暂停", nil)
	}
	if pause {
		plan.Status = "paused"
	} else {
		plan.Status = "pending"
		refreshPlanCompletion(&plan)
	}
	if err := s.savePlan(ctx, &plan); err != nil {
		return plan, err
	}
	return s.publicPlan(plan), nil
}

func (s *Service) allPlans(ctx context.Context) ([]RetestPlan, error) {
	repo, err := s.trainingRepo()
	if err != nil {
		return nil, err
	}
	docs, err := repo.TrainingDocuments(ctx, "retest")
	if err != nil {
		return nil, unavailable("复测历史读取失败", err)
	}
	result := []RetestPlan{}
	for id, data := range docs {
		var p RetestPlan
		if json.Unmarshal(data, &p) != nil || p.ID != id {
			return nil, unavailable("复测历史损坏", nil)
		}
		if _, err := frozenSession(p); err != nil {
			return nil, unavailable("复测冻结材料损坏", err)
		}
		if p.ProfileID == LocalProfileID {
			result = append(result, p)
		}
	}
	return result, nil
}
func (s *Service) allObservations(ctx context.Context) ([]AttemptObservation, error) {
	plans, err := s.allPlans(ctx)
	if err != nil {
		return nil, err
	}
	result := []AttemptObservation{}
	for _, p := range plans {
		public := s.publicPlan(p)
		for _, a := range public.Attempts {
			for _, task := range public.Tasks {
				if task.ID == a.TaskID {
					result = append(result, AttemptObservation{PlanID: p.ID, Task: task, Attempt: a, MaterialVersion: p.MaterialVersion, ScoringVersion: p.ScoringVersion})
				}
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Attempt.Sequence > 0 && result[j].Attempt.Sequence > 0 {
			return result[i].Attempt.Sequence < result[j].Attempt.Sequence
		}
		if result[i].Attempt.CreatedAt.Equal(result[j].Attempt.CreatedAt) {
			return result[i].Attempt.ID < result[j].Attempt.ID
		}
		return result[i].Attempt.CreatedAt.Before(result[j].Attempt.CreatedAt)
	})
	return result, nil
}
func (s *Service) WeaknessAttempts(ctx context.Context, sourceID, id string) ([]AttemptObservation, error) {
	page, err := s.Weaknesses(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	exists := false
	var selected Weakness
	for _, w := range page.Items {
		if w.ID == id {
			exists = true
			selected = w
		}
	}
	if !exists {
		return nil, validation("id", "薄弱点不属于该训练")
	}
	all, err := s.allObservations(ctx)
	if err != nil {
		return nil, err
	}
	result := []AttemptObservation{}
	source, err := s.ownedSession(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, origin := range selected.Sources {
		if seen[origin.QuestionID] {
			continue
		}
		seen[origin.QuestionID] = true
		for _, turn := range source.Answers {
			if turn.Question.ID == origin.QuestionID {
				copy := turn
				publicRecordEvidence(source.Sources, &copy)
				result = append(result, AttemptObservation{PlanID: source.ID, MaterialVersion: trainingHash(source.Sources), ScoringVersion: historyItem(source).ScoringVersion, Task: RetestTask{ID: turn.Question.ID, Question: copy.Question, Targets: []RetestTarget{{ID: id, Label: selected.Label, Criteria: selected.Criteria}}, CriteriaVersion: "original-assessment"}, Attempt: RetestAttempt{ID: "initial-" + turn.Question.ID, TaskID: turn.Question.ID, Answer: turn.Answer, CreatedAt: turn.AnsweredAt, Status: "succeeded", Evaluation: &RetestEvaluation{Assessment: copy.Assessment, Results: []CriterionResult{}}}})
			}
		}
	}
	for _, o := range all {
		for _, t := range o.Task.Targets {
			if t.ID == id {
				result = append(result, o)
			}
		}
	}
	return result, nil
}
func (s *Service) CompareAttempts(ctx context.Context, sourceID, id, left, right string) (AttemptComparison, error) {
	all, err := s.WeaknessAttempts(ctx, sourceID, id)
	if err != nil {
		return AttemptComparison{}, err
	}
	var a, b *AttemptObservation
	for i := range all {
		if all[i].Attempt.ID == left {
			a = &all[i]
		}
		if all[i].Attempt.ID == right {
			b = &all[i]
		}
	}
	if a == nil || b == nil || left == right {
		return AttemptComparison{}, validation("attempts", "请选择同一薄弱点的两次不同尝试")
	}
	result := AttemptComparison{Left: *a, Right: *b, Reason: "题目、材料、标准或评价状态不同，仅并排比较原文与逐项反馈"}
	result.ScoreComparable = a.Attempt.Status == "succeeded" && b.Attempt.Status == "succeeded" && a.MaterialVersion == b.MaterialVersion && a.ScoringVersion == b.ScoringVersion && a.Task.CriteriaVersion == b.Task.CriteriaVersion && a.Task.Question.Text == b.Task.Question.Text && a.Task.Question.Difficulty == b.Task.Question.Difficulty && a.Attempt.Answer.InputMode == b.Attempt.Answer.InputMode
	for _, o := range []*AttemptObservation{a, b} {
		if o.Attempt.Evaluation != nil {
			for _, r := range o.Attempt.Evaluation.Results {
				if r.Status == "unassessed" {
					result.ScoreComparable = false
				}
			}
		}
	}
	if result.ScoreComparable {
		delta := overallRetestScore(b.Attempt.Evaluation.Assessment) - overallRetestScore(a.Attempt.Evaluation.Assessment)
		result.ScoreDelta = &delta
		result.Reason = "相同题目、材料版本、标准与输入条件；分数变化仅代表这两次回答"
	}
	return result, nil
}
func overallRetestScore(a Assessment) int {
	return (a.Correctness + a.Depth + a.Specificity + a.Ownership + a.Metrics + a.Tradeoffs) * 100 / 30
}
func (s *Service) retestItems(ctx context.Context) ([]HistoryItem, error) {
	plans, err := s.allPlans(ctx)
	if err != nil {
		return nil, err
	}
	items := []HistoryItem{}
	for _, p := range plans {
		state := string(StateAwaitingAnswer)
		if p.Status == "completed" {
			state = string(StateCompleted)
		}
		item := HistoryItem{ID: p.ID, Type: "retest", State: state, StartedAt: p.CreatedAt, ReportStatus: "not_provided", Area: "unclassified", ScoringVersion: p.ScoringVersion}
		source, e := frozenSession(p)
		if e != nil {
			return nil, fmt.Errorf("retest snapshot: %w", e)
		}
		item.Area = historyItem(source).Area
		for _, a := range p.Attempts {
			item.Answered++
			if a.Status == "succeeded" {
				item.Evaluated++
			}
		}
		items = append(items, item)
	}
	return items, nil
}
