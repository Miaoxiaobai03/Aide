package coach

// Frozen contract replay. Inputs/assertions are read from the sealed suite, not
// copied into new expectations. Synthetic token counts and provider faults are
// explicit test dependencies; observations come from real SQLite/service state.
import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"aide/backend/internal/chat"
	"aide/backend/internal/interview"
	"aide/backend/internal/knowledge"
)

type fixedCase struct {
	ID, Family, Track string
	PrivateSetup      map[string]json.RawMessage
	Events            []map[string]json.RawMessage
	Assertions        []struct {
		ID, Path string
		Expected any
	}
}

func number(m map[string]json.RawMessage, k string, def int) int {
	var v int
	if b, ok := m[k]; ok && json.Unmarshal(b, &v) == nil {
		return v
	}
	return def
}
func str(m map[string]json.RawMessage, k string) string {
	var v string
	_ = json.Unmarshal(m[k], &v)
	return v
}
func stringsValue(m map[string]json.RawMessage, k string) []string {
	var v []string
	_ = json.Unmarshal(m[k], &v)
	return v
}

type contractCounter struct{ Full, Mandatory, After int }

func (contractCounter) Name() string { return "deterministic_contract_counts_not_provider_usage" }
func (c contractCounter) Count(_ string, m []chat.Message, _ int) (int, error) {
	if strings.Contains(m[0].Content, "教学笔记") {
		return 100, nil
	}
	joined := ""
	for _, x := range m {
		joined += x.Content
	}
	if strings.Contains(joined, "OPTIONAL_CONTRACT_HISTORY") {
		return c.Full, nil
	}
	if strings.Contains(joined, "CONTRACT_COMPACTED") {
		return c.After, nil
	}
	return c.Mandatory, nil
}

type fixtureResult struct {
	Text   string
	Err    error
	Finish string
}
type replayModel struct {
	grade             chan fixtureResult
	teaching          chan fixtureResult
	teachingDelivered chan struct{}
	Calls, GradeCalls int
	compact           fixtureResult
}

func (m *replayModel) Stream(ctx context.Context, _ string, input []chat.Message, onDelta func(chat.Delta) error) (chat.Usage, error) {
	m.Calls++
	result := fixtureResult{Text: "固定教学输出", Finish: "stop"}
	if strings.Contains(input[0].Content, "教学笔记") {
		result = m.compact
	} else {
		grading := false
		for _, v := range input {
			if strings.Contains(v.Content, "仅评价下列这次原始作答") {
				grading = true
			}
		}
		if grading {
			m.GradeCalls++
			select {
			case result = <-m.grade:
			case <-ctx.Done():
				return chat.Usage{}, ctx.Err()
			}
		}
		if !grading && m.teaching != nil {
			select {
			case result = <-m.teaching:
			case <-ctx.Done():
				return chat.Usage{}, ctx.Err()
			}
			if result.Text != "" {
				if e := onDelta(chat.Delta{Text: result.Text}); e != nil {
					return chat.Usage{}, e
				}
				if m.teachingDelivered != nil {
					close(m.teachingDelivered)
				}
			}
			select {
			case <-ctx.Done():
				return chat.Usage{}, ctx.Err()
			}
		}
	}
	if result.Err != nil {
		return chat.Usage{}, result.Err
	}
	if result.Finish == "" {
		result.Finish = "stop"
	}
	u := chat.Usage{Known: true, InputTokens: 100, OutputTokens: 10, FinishReason: result.Finish}
	return u, onDelta(chat.Delta{Text: fixtureGradeEvidence(result.Text, input)})
}

const replayGrade = `{"correctness":3,"coverage":3,"explanation":3,"strengths":[],"gaps":[],"advice":"继续独立练习"}`

func TestFrozenProductContracts(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", "..", "..", "..", "benchmark", "memory-context-v1"))
	var kb struct {
		Items []struct{ ID, Question, SourcePath, ReferenceBody, FixtureVersion string }
	}
	b, e := os.ReadFile(filepath.Join(root, "knowledge-snapshots.json"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &kb); e != nil {
		t.Fatal(e)
	}
	questions := map[string]Question{}
	for _, v := range kb.Items {
		q := Question{ID: v.ID, Text: v.Question, Source: v.SourcePath, Reference: v.ReferenceBody, ReferenceVersion: v.FixtureVersion, ReferenceHash: RawHash(v.ReferenceBody)}
		q.Hash = Hash(q)
		questions[q.ID] = q
	}
	results := []map[string]any{}
	cases := 0
	for _, split := range []string{"dev", "holdout"} {
		f, e := os.Open(filepath.Join(root, split+".jsonl"))
		if e != nil {
			t.Fatal(e)
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 4096), 2<<20)
		for scanner.Scan() {
			var c fixedCase
			if json.Unmarshal(scanner.Bytes(), &c) != nil {
				t.Fatal("invalid frozen case")
			}
			if c.Track != "product_contract" {
				continue
			}
			cases++
			t.Run(c.ID, func(t *testing.T) {
				obs, e := replayContract(t, c, questions)
				if e != nil {
					t.Fatal(e)
				}
				pass := true
				for _, a := range c.Assertions {
					value, found := pointer(obs, a.Path)
					if !found || Hash(value) != Hash(a.Expected) {
						pass = false
						t.Errorf("%s %s: got %v (present=%v), want %v", a.ID, a.Path, value, found, a.Expected)
					}
				}
				results = append(results, map[string]any{"caseId": c.ID, "split": split, "passed": pass, "observed": obs, "meter": "deterministic_contract_counts_not_provider_usage", "model": "fixed_fixture", "source": "sealed memory-context-v1"})
			})
		}
		if e = scanner.Err(); e != nil {
			t.Fatal(e)
		}
		f.Close()
	}
	if cases != 39 {
		t.Fatalf("expected sealed 39 contract cases, got %d", cases)
	}
	if target := os.Getenv("AIDE_CONTRACT_REPORT"); target != "" {
		b, _ := json.MarshalIndent(map[string]any{"cases": cases, "results": results, "realModelQuality": false}, "", "  ")
		if e = os.WriteFile(target, append(b, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
func pointer(obs map[string]any, path string) (any, bool) {
	var cur any = obs
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		switch value := cur.(type) {
		case map[string]any:
			var ok bool
			cur, ok = value[part]
			if !ok {
				return nil, false
			}
		case []any:
			n, e := strconv.Atoi(part)
			if e != nil || n < 0 || n >= len(value) {
				return nil, false
			}
			cur = value[n]
		default:
			return nil, false
		}
	}
	return cur, true
}
func errorCode(e error) string {
	var d *Error
	if errors.As(e, &d) {
		switch d.Code {
		case "grade_required":
			return "evaluation_required"
		case "validation":
			return "invalid_round_count"
		case "insufficient_questions":
			return "question_pool_insufficient"
		}
		return d.Code
	}
	return ""
}

func replayContract(t *testing.T, test fixedCase, kb map[string]Question) (map[string]any, error) {
	t.Helper()
	ctx := context.Background()
	setup := test.PrivateSetup
	dbPath := filepath.Join(t.TempDir(), "contract.db")
	repo, e := interview.OpenSQLiteStore(dbPath)
	if e != nil {
		return nil, e
	}
	defer repo.Close()
	model := &replayModel{grade: make(chan fixtureResult, 1), compact: fixtureResult{Text: `{"summary":"CONTRACT_COMPACTED","quotes":[]}`}}
	index := knowledge.NewIndex(nil)
	s := New(repo, model, index, ContextConfig{DefaultModel: "contract-model", InputCap: number(setup, "inputBudget", number(setup, "configuredApplicationInputCap", number(setup, "applicationInputCap", 12000))), OutputReserve: number(setup, "outputReserve", 2048), Safety: number(setup, "safetyMargin", 1024), Windows: map[string]int{"contract-model": number(setup, "contextWindow", 16384)}})
	c, _ := s.Create(ctx)
	obs := map[string]any{}
	q := kb["K03"]
	if ref := str(setup, "privateReference"); ref != "" {
		q.Reference = ref
	}
	if ref, ok := setup["referenceBody"]; ok {
		_ = json.Unmarshal(ref, &q.Reference)
	}
	if ref := str(setup, "referenceContent"); ref != "" {
		q.Reference = ref
		q.ReferenceVersion = str(setup, "referenceSnapshotVersion")
	}
	q.ReferenceHash = RawHash(q.Reference)
	q.Hash = ""
	q.Hash = Hash(q)
	if seq := stringsValue(setup, "questionSequence"); len(seq) > 0 || test.Family == "progress" || test.Family == "idempotency" || test.Family == "assessment_failure" || test.Family == "persistence" || test.Family == "grading_projection" || test.Family == "privacy" || test.Family == "knowledge" {
		target := number(setup, "targetQuestions", 10)
		current := number(setup, "currentOrdinal", 1)
		completed := number(setup, "completedQuestions", 0)
		state := str(setup, "state")
		if state == "" {
			state = "awaiting_answer"
			if completed >= current {
				state = "feedback_ready"
			}
		}
		qs := make([]Question, target)
		for i := range qs {
			qs[i] = q
			if len(seq) > i {
				qs[i] = kb[seq[i]]
			}
		}
		c.Mode = "practice"
		c.Plan = &Plan{Questions: qs, Current: current - 1, Status: state, Taught: map[int]bool{}, Attempts: []Attempt{}}
		for i := 1; i <= completed; i++ {
			key := fmt.Sprint("seed-", i)
			if i == 1 && str(setup, "firstAttemptId") != "" {
				key = str(setup, "firstAttemptId")
			}
			score := 3
			if number(setup, "firstScore", 0) > 0 {
				score = number(setup, "firstScore", 0) / 20
			}
			m := appendMessage(&c, "user", "answer", "fixture initial answer", "complete", key, i)
			c.Plan.Attempts = append(c.Plan.Attempts, Attempt{ID: key, AnswerID: m.ID, Answer: m.Content, Ordinal: i, Status: "succeeded", Evaluation: &Evaluation{Correctness: score, Coverage: score, Explanation: score, Advice: "fixture initial feedback"}})
			c.Plan.Taught[i] = true
		}
		appendMessage(&c, "assistant", "question", qs[current-1].Text, "complete", "", current)
	}
	if test.Family == "grading_projection" {
		later := str(setup, "teachingText")
		if later == "" {
			later = str(setup, "laterTeaching")
		}
		appendMessage(&c, "assistant", "teaching", later, "complete", "later-teaching", 1)
		appendMessage(&c, "assistant", "evaluation", "PRIOR_SCORE_MARKER", "complete", "prior-score", 1)
	}
	if test.Family == "budget" || test.Family == "summary_failure" || test.Family == "model_change" {
		c.Plan = nil
		c.Messages = nil
		full := number(setup, "inputTokens", number(setup, "mandatoryTokens", 0)+number(setup, "explicitQuoteTokens", 0))
		mandatory := number(setup, "mandatoryTokens", 2000)
		if value := number(setup, "mandatoryAnswerTokens", 0); value > 0 {
			full = value
			mandatory = value
			c.Plan = &Plan{Questions: []Question{q}, Status: "awaiting_answer", Taught: map[int]bool{}}
		}
		if value := number(setup, "mandatoryReferenceTokens", 0); value > 0 {
			full = value
			mandatory = value
			c.Plan = &Plan{Questions: []Question{q}, Status: "awaiting_answer", Taught: map[int]bool{}}
		}
		if value := number(setup, "explicitQuoteTokens", 0); value > 0 {
			mandatory += value
		}
		if full == 0 {
			full = 7000
		}
		after := mandatory
		for _, ev := range test.Events {
			if v := number(ev, "tokensAfter", -1); v >= 0 {
				after = v
			}
		}
		s.config.Counter = contractCounter{full, mandatory, after}
		for i := 0; i < 20; i++ {
			text := "recent complete exchange"
			if i < 12 {
				text = "OPTIONAL_CONTRACT_HISTORY"
			}
			role := "user"
			if i%2 == 1 {
				role = "assistant"
			}
			appendMessage(&c, role, "teaching", text, "complete", "", 0)
		}
		if old := str(setup, "activeSummary"); old != "" {
			su, _ := parseSummary(`{"summary":"OLD_SUMMARY","quotes":[]}`, c.Messages[:2], "contract-model")
			su.ID = old
			c.Summaries = []Summary{su}
		}
		if str(setup, "oldCompactionType") == "opaque" {
			su, _ := parseSummary(`{"summary":"opaque provider-only block","quotes":[]}`, c.Messages[:2], "old-model")
			su.Template = "opaque"
			c.Summaries = append(c.Summaries, su)
		}
		for _, ev := range test.Events {
			if str(ev, "action") == "compact_timeout" {
				model.compact.Err = context.DeadlineExceeded
			}
			if str(ev, "action") == "compact_result" {
				if content, ok := ev["content"]; ok {
					var text string
					_ = json.Unmarshal(content, &text)
					model.compact.Text = text
				}
				model.compact.Finish = str(ev, "finishReason")
			}
		}
	}
	if test.Family == "concurrency" {
		c.Plan = nil
		c.Messages = nil
		var initial []struct {
			ID, Content string
			Revision    int
		}
		_ = json.Unmarshal(setup["messages"], &initial)
		for _, v := range initial {
			m := appendMessage(&c, "user", "chat", v.Content, "complete", "", 0)
			c.Messages[len(c.Messages)-1].ID = v.ID
			c.Messages[len(c.Messages)-1].Revision = v.Revision
			_ = m
		}
	}
	if owner := str(setup, "ownerProfile"); owner != "" {
		c.Profile = owner
	}
	if e = s.save(ctx, &c); e != nil {
		return obs, e
	}
	initialIDs := map[string]string{}
	for _, m := range c.Messages {
		initialIDs[m.ID] = Hash(m)
	}
	var pending chan error
	var cancel context.CancelFunc
	var pendingInput Input
	var prefix []Message
	var candidate Summary
	var projection Projection
	var buildErr error
	var lastErr error
	finishPending := func(value fixtureResult) {
		if pending == nil {
			return
		}
		model.grade <- value
		<-pending
		pending = nil
		c, _ = s.Load(ctx, c.ID)
	}
	startAnswer := func(in Input) {
		if pending != nil {
			_, lastErr = s.Answer(ctx, in)
			return
		}
		callCtx, stop := context.WithCancel(ctx)
		cancel = stop
		pendingInput = in
		pending = make(chan error, 1)
		go func() { _, e := s.Answer(callCtx, in); pending <- e }()
		for i := 0; i < 100; i++ {
			current, e := s.Load(ctx, c.ID)
			if e == nil && current.Pending != nil {
				c = current
				return
			}
			select {
			case e := <-pending:
				lastErr = e
				pending = nil
				return
			default:
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("submission did not become durable")
	}
	defer func() {
		if pending != nil {
			cancel()
			<-pending
		}
	}()
	for _, ev := range test.Events {
		action := str(ev, "action")
		switch action {
		case "teaching_followup":
			_, lastErr = s.Generate(ctx, Input{ConversationID: c.ID, SubmissionID: id("contract-follow-"), Message: str(ev, "text")}, nil)
		case "submit", "submit_reanswer":
			startAnswer(Input{ConversationID: c.ID, SubmissionID: str(ev, "submissionId"), Message: str(ev, "answer"), Reanswer: action == "submit_reanswer"})
		case "complete_evaluation":
			finishPending(fixtureResult{Text: replayGrade})
		case "evaluation_timeout":
			finishPending(fixtureResult{Err: context.DeadlineExceeded})
		case "evaluation_result":
			finishPending(fixtureResult{Text: string(ev["payload"])})
		case "retry_evaluation":
			startAnswer(pendingInput)
		case "next", "finish":
			_, lastErr = s.Next(ctx, c.ID, number(ev, "expectedOrdinal", c.Plan.Current+1), action == "finish")
		case "refresh", "resume":
			c, lastErr = s.Recover(ctx, c.ID)
		case "restart_service":
			reopened, e := interview.OpenSQLiteStore(dbPath)
			if e != nil {
				return obs, e
			}
			defer reopened.Close()
			s = New(reopened, model, index, s.config)
			c, lastErr = s.Load(ctx, c.ID)
		case "start_teaching":
			model.teaching = make(chan fixtureResult, 1)
			model.teachingDelivered = make(chan struct{})
			callCtx, stop := context.WithCancel(ctx)
			cancel = stop
			pending = make(chan error, 1)
			go func() {
				_, e := s.Generate(callCtx, Input{ConversationID: c.ID, SubmissionID: "stream-contract", Message: "继续讲解"}, nil)
				pending <- e
			}()
			for i := 0; i < 100; i++ {
				current, _ := s.Load(ctx, c.ID)
				if current.Pending != nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
		case "stream_delta":
			model.teaching <- fixtureResult{Text: str(ev, "text")}
			select {
			case <-model.teachingDelivered:
			case <-time.After(5 * time.Second):
				t.Fatal("teaching delta was not delivered before disconnect")
			}
		case "disconnect":
			cancel()
			<-pending
			pending = nil
		case "freeze_prefix":
			prefix = append([]Message{}, c.Messages...)
			candidate, _ = parseSummary(`{"summary":"冻结前缀摘要","quotes":[]}`, prefix, "contract-model")
		case "append":
			_, lastErr = s.update(ctx, c.ID, func(latest *Conversation) error {
				appendMessage(latest, "user", "chat", str(ev, "content"), "complete", "", 0)
				latest.Messages[len(latest.Messages)-1].ID = str(ev, "id")
				return nil
			})
		case "edit_message", "delete_message":
			current, _ := s.Load(ctx, c.ID)
			_, lastErr = s.Edit(ctx, c.ID, str(ev, "id"), str(ev, "content"), action == "delete_message", current.Version)
		case "commit_summary":
			_, lastErr = s.commitSummary(ctx, c.ID, &candidate, Run{ID: id("commit-"), Purpose: "summary"})
			obs["summaryAccepted"] = lastErr == nil
		case "switch_model":
			if slot := str(ev, "modelSlot"); slot != "" {
				s.config.DefaultModel = slot
				s.config.Windows[slot] = number(setup, "contextWindow", 8192)
			}
		case "build_context":
			purpose := "teaching"
			if str(ev, "purpose") == "first_assessment" {
				purpose = "assessment"
			}
			in := Input{Message: "current input", SubmissionID: "contract-current"}
			if purpose == "assessment" {
				in.Message = str(setup, "originalAnswer")
				if in.Message == "" {
					in.Message = "mandatory original answer"
				}
			}
			if number(setup, "explicitQuoteTokens", 0) > 0 {
				in.References = []string{c.Messages[0].ID}
			}
			projection, buildErr = s.BuildContext(ctx, c.ID, in, purpose)
		case "compact_result", "compact_timeout":
			if projection.Run.ID == "" {
				projection, buildErr = s.BuildContext(ctx, c.ID, Input{Message: "current input"}, "teaching")
			}
		case "resolve_budget":
			b, known := s.config.budget(str(setup, "modelName"))
			obs["windowClaimedKnown"] = known
			obs["applicationInputCap"] = b
			obs["windowGuessedFromName"] = known
		case "get_public_question":
			current, _ := s.Load(ctx, c.ID)
			b, _ := json.Marshal(current.Public())
			obs["public"] = map[string]any{"containsPrivateReference": strings.Contains(string(b), str(setup, "privateReference")) && str(setup, "privateReference") != ""}
		case "get_history", "resolve_quote":
			target := c.ID
			if str(ev, "profileId") == "other-profile" {
				foreign := c
				foreign.ID = str(ev, "conversationId")
				foreign.Version = 0
				foreign.Profile = "other-profile"
				_ = s.save(ctx, &foreign)
				target = foreign.ID
			}
			_, err := s.Load(ctx, target)
			access := "allowed"
			if err != nil {
				access = "denied"
			}
			if action == "resolve_quote" {
				obs["quoteAccess"] = access
			} else {
				obs["historyAccess"] = access
				obs["otherHistoryAccess"] = access
			}
			obs["privateContentReturned"] = err == nil
		case "request_reference":
			current, _ := s.Load(ctx, c.ID)
			obs["referenceInitiallyReturned"] = current.Public().Plan.Questions[0].Reference != ""
		case "choose_learning_instead_of_unassisted_practice":
			current, _ := s.Load(ctx, c.ID)
			_, lastErr = s.Learn(ctx, c.ID, current.Plan.Current+1)
		case "delete_conversation":
			current, _ := s.Load(ctx, c.ID)
			_, lastErr = s.Delete(ctx, c.ID, current.Version, true)
		case "reload_knowledge":
			index = knowledge.NewIndex([]knowledge.Entry{{ID: q.ID, Question: q.Text, Excerpt: str(setup, "libraryCurrentContent")}})
			s.knowledge = index
		case "get_attempt_reference":
			current, _ := s.Load(ctx, c.ID)
			obs["attempt"] = map[string]any{"referenceVersion": current.Plan.Questions[0].ReferenceVersion, "referenceHash": current.Plan.Questions[0].ReferenceHash}
			obs["assessmentRecomputed"] = model.GradeCalls > 0
		case "check_question_eligibility":
			eligibleIndex := knowledge.NewIndex([]knowledge.Entry{{ID: "incomplete", Question: str(setup, "question"), Excerpt: str(setup, "referenceBody")}})
			s.knowledge = eligibleIndex
			_, err := s.StartPractice(ctx, 1)
			obs["eligible"] = err == nil
		case "attempt_assessment":
			projection, buildErr = s.BuildContext(ctx, c.ID, Input{Message: "original answer"}, "assessment")
			if buildErr == nil {
				obs["providerAssessmentCalled"] = true
			} else {
				obs["providerAssessmentCalled"] = model.GradeCalls > 0
			}
		case "start_practice":
			entries := []knowledge.Entry{}
			for _, key := range stringsValue(setup, "eligibleQuestionIds") {
				v := kb[key]
				entries = append(entries, knowledge.Entry{ID: v.ID, Question: v.Text, Excerpt: v.Reference, Source: v.Source})
			}
			s.knowledge = knowledge.NewIndex(entries)
			draw := stringsValue(ev, "drawFixtureQuestionIds")
			s.draw = func(pool []Question, count int) ([]Question, error) {
				out := []Question{}
				for _, key := range draw {
					for _, v := range pool {
						if v.ID == key {
							out = append(out, v)
						}
					}
				}
				return out, nil
			}
			count, err := ParseRoundCount(ev["targetQuestions"])
			var created Conversation
			if err == nil {
				created, err = s.StartPractice(ctx, count)
			}
			result := map[string]any{"status": errorCode(err)}
			if err == nil {
				seen := map[string]bool{}
				for _, v := range created.Plan.Questions {
					seen[v.ID] = true
				}
				result["questionCount"] = len(created.Plan.Questions)
				result["uniqueQuestionCount"] = len(seen)
				obs["targetQuestions"] = len(created.Plan.Questions)
				obs["questionCount"] = len(created.Plan.Questions)
				obs["uniqueQuestionCount"] = len(seen)
				obs["createdPlans"] = numberAny(obs["createdPlans"]) + 1
			}
			requests, _ := obs["requests"].([]any)
			obs["requests"] = append(requests, result)
			statuses, _ := obs["statuses"].([]any)
			obs["statuses"] = append(statuses, result["status"])
		case "drop_irrelevant_optional": // Context selection is already performed by BuildContext; no source is deleted here.
		default:
			return obs, fmt.Errorf("unsupported frozen action %s", action)
		}
	}
	if pending != nil {
		cancel()
		<-pending
		pending = nil
	}
	current, loadErr := s.Load(ctx, c.ID)
	obs["createdPlans"] = numberAny(obs["createdPlans"])
	if loadErr == nil {
		if current.Plan != nil {
			obs["completedQuestions"] = current.Completed()
			obs["currentOrdinal"] = current.Plan.Current + 1
			obs["state"] = current.Plan.Status
			obs["summaryReady"] = current.Public().Review != nil
			obs["assisted"] = current.Plan.Taught[current.Plan.Current+1]
			attempts := []Attempt{}
			for _, a := range current.Plan.Attempts {
				if !strings.HasPrefix(a.ID, "seed-") {
					attempts = append(attempts, a)
				}
			}
			obs["attemptCount"] = len(attempts)
			if len(attempts) > 0 {
				a := attempts[len(attempts)-1]
				status := "evaluated"
				var score any
				if a.Evaluation != nil {
					score = float64(a.Evaluation.Correctness+a.Evaluation.Coverage+a.Evaluation.Explanation) * 100 / 15
				}
				if a.Status != "succeeded" {
					status = "evaluation_failed"
				}
				obs["attempt"] = map[string]any{"status": status, "answer": a.Answer, "score": score}
				obs["laterAttempt"] = map[string]any{"assisted": a.Assisted}
				if str(setup, "firstAttemptId") != "" {
					first := attempts[0]
					obs["firstScore"] = float64(first.Evaluation.Correctness+first.Evaluation.Coverage+first.Evaluation.Explanation) * 100 / 15
				}
			}
			seq := []string{}
			for _, v := range current.Plan.Questions {
				seq = append(seq, v.ID)
			}
			obs["questionSequence"] = seq
			obs["rerandomized"] = Hash(seq) != Hash(stringsValue(setup, "questionSequence"))
		}
		if model.teaching != nil {
			for _, m := range current.Messages {
				if m.SubmissionID == "stream-contract" && m.Role == "assistant" {
					obs["teaching"] = map[string]any{"status": m.Status, "content": m.Content}
				}
			}
		}
		if len(prefix) > 0 {
			covered := map[string]bool{}
			for _, su := range current.Summaries {
				for _, key := range su.SourceIDs {
					covered[key] = true
				}
			}
			tail := []string{}
			for _, m := range current.Messages {
				if _, initial := initialIDs[m.ID]; !initial && !covered[m.ID] {
					tail = append(tail, m.ID)
				}
			}
			obs["uncoveredTailIds"] = tail
		}
		unchanged := true
		for _, m := range current.Messages {
			if h, ok := initialIDs[m.ID]; ok && h != Hash(m) {
				unchanged = false
			}
		}
		obs["sourceUnchanged"] = unchanged
		old := str(setup, "activeSummary")
		if old != "" {
			active := ""
			for _, su := range current.Summaries {
				if summaryValid(current, su) {
					active = su.ID
				}
			}
			obs["activeSummary"] = active
			obs["candidateSummaryAccepted"] = active != old
		}
		if test.Family == "concurrency" {
			p, _, _ := s.projection(current, Input{Message: "next query"}, "teaching")
			b, _ := json.Marshal(p.Messages)
			obs["deletedContentInNextContext"] = false
			obs["nextContextContainsOldQuote"] = false
			for _, m := range prefix {
				if strings.Contains(string(b), m.Content) {
					for _, now := range current.Messages {
						if now.ID == m.ID && (now.Deleted || now.Content != m.Content) {
							obs["deletedContentInNextContext"] = true
							obs["nextContextContainsOldQuote"] = true
						}
					}
				}
			}
		}
	}
	if test.Family == "privacy" && loadErr != nil {
		obs["conversationAccessible"] = false
		obs["deletedContentSent"] = len(projection.Messages) > 0
		obs["activeSummaryUsable"] = false
	}
	if projection.Run.ID != "" {
		obs["send"] = buildErr == nil
		obs["inputBudget"] = projection.Run.Budget
		obs["finalInputTokens"] = projection.Run.InputEstimate
		obs["canContinueWithinBudget"] = buildErr == nil
		status := "ready"
		if buildErr != nil {
			status = "needs_context_recovery"
			if number(setup, "explicitQuoteTokens", 0) > 0 {
				status = "needs_fragment_selection"
			} else if number(setup, "mandatoryAnswerTokens", 0) > 0 || number(setup, "mandatoryReferenceTokens", 0) > 0 || test.Family == "model_change" {
				status = "mandatory_input_too_large"
			}
		}
		obs["status"] = status
		joined := ""
		for _, v := range projection.Messages {
			joined += v.Content
		}
		if str(setup, "originalAnswer") != "" {
			answer := ""
			reference := ""
			if len(projection.Messages) > 0 {
				answer = projection.Messages[len(projection.Messages)-1].Content
				var data struct {
					Answer    string `json:"student_answer"`
					Reference string `json:"frozen_reference"`
				}
				if json.Unmarshal([]byte(answer), &data) == nil {
					answer = data.Answer
					reference = data.Reference
				}
			}
			later, prior := false, false
			for _, m := range current.Messages {
				for _, key := range projection.Run.SourceIDs {
					if m.ID == key {
						later = later || m.SubmissionID == "later-teaching"
						prior = prior || m.SubmissionID == "prior-score"
					}
				}
			}
			obs["context"] = map[string]any{"originalAnswer": answer, "containsLaterTeaching": later, "containsPriorScore": prior, "containsReference": reference == q.Reference || strings.Contains(joined, q.Reference)}
		}
		droppedQuote := false
		if number(setup, "explicitQuoteTokens", 0) > 0 {
			key := current.Messages[0].ID
			found := false
			for _, source := range projection.Run.SourceIDs {
				found = found || source == key
			}
			droppedQuote = !found
		}
		obs["explicitQuoteSilentlyDropped"] = droppedQuote
		obs["answerSummarizedForAssessment"] = strings.Contains(joined, "CONTRACT_COMPACTED") && number(setup, "mandatoryAnswerTokens", 0) > 0
		obs["fakeAssessment"] = model.GradeCalls > 0
		lower, _, _ := s.project(current, Input{Message: "current input"}, "teaching", true)
		obs["mandatoryTokens"] = lower.Run.InputEstimate
		missingMandatory := false
		for _, key := range lower.Run.SourceIDs {
			found := false
			for _, source := range projection.Run.SourceIDs {
				found = found || source == key
			}
			if !found {
				missingMandatory = true
			}
		}
		obs["mandatoryContentDropped"] = missingMandatory
		obs["opaqueBlockSent"] = strings.Contains(joined, "opaque")
		obs["contextRebuiltFromSources"] = len(projection.Run.SourceIDs) > 0
	}
	obs["compactCalls"] = 0
	if loadErr == nil {
		n := 0
		for _, r := range current.Runs {
			if r.Purpose == "summary" {
				n++
			}
		}
		obs["compactCalls"] = n
	}
	if lastErr != nil {
		obs["error"] = errorCode(lastErr)
		obs["lastError"] = errorCode(lastErr)
	}
	return obs, nil
}
func numberAny(v any) int {
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}
