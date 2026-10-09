package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aide/backend/internal/interview"
)

type workflowTrainingAgent struct{}

func workflowRefs(anchors []interview.SourceAnchor) []interview.EvidenceRef {
	result := []interview.EvidenceRef{}
	for _, a := range anchors {
		result = append(result, interview.EvidenceRef{SourceID: a.SourceID, Kind: a.Kind, AnchorID: a.ID, Locator: a.Locator, Quote: a.Text})
	}
	return result
}
func (workflowTrainingAgent) GenerateQuestion(_ context.Context, r interview.GenerateQuestionRequest) (interview.QuestionDraft, error) {
	return interview.QuestionDraft{Text: "你在事务项目中如何验证隔离边界？", EvidenceRefs: workflowRefs(r.Anchors)}, nil
}
func (workflowTrainingAgent) AssessAnswer(_ context.Context, r interview.AssessAnswerRequest) (interview.Assessment, error) {
	return interview.Assessment{Correctness: 2, Depth: 2, Specificity: 3, Ownership: 3, Metrics: 3, Tradeoffs: 2, Gaps: []string{"未解释隔离边界"}, EvidenceRefs: workflowRefs(r.Anchors)}, nil
}
func (workflowTrainingAgent) GenerateReport(_ context.Context, r interview.GenerateReportRequest) (interview.ReportDraft, error) {
	return interview.ReportDraft{Summary: "已经提交的训练报告", Gaps: []string{"未解释隔离边界"}, EvidenceRefs: workflowRefs(r.Anchors)}, nil
}
func (workflowTrainingAgent) GenerateRetest(_ context.Context, r interview.RetestQuestionRequest) (interview.RetestQuestionDraft, error) {
	ids := []string{}
	for _, t := range r.Targets {
		ids = append(ids, t.ID)
	}
	return interview.RetestQuestionDraft{Question: interview.QuestionDraft{Text: "事务隔离边界发生变化时，你如何验证项目行为？", EvidenceRefs: workflowRefs(r.Anchors)}, Difficulty: r.Original.Difficulty, TargetIDs: ids, Answerable: true, BindingReason: "相同项目范围的隔离边界验证"}, nil
}
func (workflowTrainingAgent) ValidateRetest(_ context.Context, r interview.RetestAssessmentRequest) (interview.RetestValidation, error) {
	ids := []string{}
	for _, t := range r.Targets {
		ids = append(ids, t.ID)
	}
	return interview.RetestValidation{Valid: true, TargetIDs: ids, Reason: "有源项目依据并覆盖目标", EvidenceRefs: workflowRefs(r.Anchors)}, nil
}
func (workflowTrainingAgent) AssessRetest(_ context.Context, r interview.RetestAssessmentRequest) (interview.RetestEvaluation, error) {
	e := interview.RetestEvaluation{Assessment: interview.Assessment{Correctness: 2, Depth: 2, Specificity: 3, Ownership: 3, Metrics: 3, Tradeoffs: 2, EvidenceRefs: workflowRefs(r.Anchors)}, Results: []interview.CriterionResult{}}
	for _, t := range r.Targets {
		for _, c := range t.Criteria {
			status, quote := "unmet", ""
			if strings.Contains(r.Answer.Text, "改善") {
				status, quote = "met", "改善"
				e.Assessment.Correctness = 4
			}
			e.Results = append(e.Results, interview.CriterionResult{TargetID: t.ID, Criterion: c, Status: status, Reason: "对照用户实际回答和标准", AnswerQuote: quote})
		}
	}
	return e, nil
}

func TestTrainingHTTPWorkflowFromNewInterviewThroughThreeRetests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := interview.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Config{ModelConfigured: true}, Dependencies{Interview: interview.NewService(interview.Dependencies{Store: store, Agent: workflowTrainingAgent{}})})
	if err != nil {
		t.Fatal(err)
	}
	call := func(url string, body any, output any) {
		t.Helper()
		method := "GET"
		var data []byte
		if body != nil {
			method = "POST"
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, url, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		api.Handler().ServeHTTP(res, req)
		if res.Code != 200 {
			t.Fatalf("%s returned %d: %s", url, res.Code, res.Body.String())
		}
		if strings.Contains(res.Body.String(), "frozenSource") || strings.Contains(res.Body.String(), "sourceContents") {
			t.Fatal("private snapshot exposed")
		}
		if output != nil {
			if err := json.Unmarshal(res.Body.Bytes(), output); err != nil {
				t.Fatal(err)
			}
		}
	}
	var started struct {
		InterviewID string `json:"interviewId"`
		Question    struct {
			ID string `json:"id"`
		} `json:"question"`
	}
	call("/api/interview", map[string]any{"action": "start", "clientSessionId": "workflow-client", "config": map[string]any{"focus": "project", "difficulty": "medium", "questionCount": 1, "feedbackMode": "after_each", "language": "zh-CN"}, "materials": map[string]any{"resume": map[string]any{"text": "项目：事务服务。我负责事务隔离设计与验证。"}}}, &started)
	answer := map[string]any{"action": "answer", "interviewId": started.InterviewID, "questionId": started.Question.ID, "clientAnswerId": "original-answer", "answer": map[string]any{"text": "我负责事务服务，但还没有解释边界", "inputMode": "text"}}
	call("/api/interview", answer, nil)
	call("/api/interview", answer, nil)
	call("/api/interview", map[string]any{"action": "report", "interviewId": started.InterviewID}, nil)
	var history interview.HistoryPage
	call("/api/v1/training", nil, &history)
	if len(history.Items) != 1 || history.Items[0].Evaluated != 1 || len(history.Unassigned) != 0 {
		t.Fatalf("new history=%+v", history)
	}
	var detail map[string]any
	call("/api/v1/training?resource=detail&id="+started.InterviewID, nil, &detail)
	if detail["report"] == nil {
		t.Fatal("existing report missing")
	}
	var weaknesses interview.WeaknessPage
	call("/api/v1/training?resource=weaknesses&sourceId="+started.InterviewID, nil, &weaknesses)
	if len(weaknesses.Items) != 1 {
		t.Fatalf("weaknesses=%+v", weaknesses)
	}
	w := weaknesses.Items[0]
	call("/api/v1/training", interview.RetestMutation{Action: "edit_weakness", SourceID: started.InterviewID, ID: w.ID, Label: w.Label, Criteria: w.Criteria, Status: "confirmed", Revision: w.Revision}, nil)
	for i, text := range []string{"第一次仍有缺口", "第二次改善", "第三次仍有缺口"} {
		var p interview.RetestPlan
		call("/api/v1/training", interview.RetestMutation{Action: "create_retest", PlanID: fmt.Sprintf("http-plan-%d", i), SourceID: started.InterviewID, IDs: []string{w.ID}, Mode: "original"}, &p)
		call("/api/v1/training", interview.RetestMutation{Action: "prepare_retest", PlanID: p.ID}, &p)
		deadline := time.Now().Add(3 * time.Second)
		for p.Status != "ready" {
			if time.Now().After(deadline) {
				t.Fatalf("preparation stuck=%+v", p)
			}
			time.Sleep(5 * time.Millisecond)
			call("/api/v1/training?resource=plan&id="+p.ID, nil, &p)
		}
		input := interview.RetestMutation{Action: "answer_retest", PlanID: p.ID, TaskID: p.Tasks[0].ID, AttemptID: fmt.Sprintf("http-attempt-%d", i), Answer: interview.AnswerPayload{Text: text, InputMode: interview.InputModeText}}
		call("/api/v1/training", input, &p)
		call("/api/v1/training", input, &p)
		if p.Status != "completed" || len(p.Attempts) != 1 {
			t.Fatalf("submit=%+v", p)
		}
	}
	var comparison interview.AttemptComparison
	call("/api/v1/training?resource=compare&sourceId="+started.InterviewID+"&id="+w.ID+"&left=http-attempt-0&right=http-attempt-1", nil, &comparison)
	if !comparison.ScoreComparable || comparison.ScoreDelta == nil {
		t.Fatalf("compare=%+v", comparison)
	}
	var stats interview.TrainingStats
	call("/api/v1/training?resource=stats", nil, &stats)
	if stats.Sessions != 4 || stats.Evaluated != 4 {
		t.Fatalf("stats=%+v", stats)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = interview.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	api, err = New(Config{}, Dependencies{Interview: interview.NewService(interview.Dependencies{Store: store})})
	if err != nil {
		t.Fatal(err)
	}
	var observations []interview.AttemptObservation
	call("/api/v1/training?resource=attempts&sourceId="+started.InterviewID+"&id="+w.ID, nil, &observations)
	if len(observations) != 4 {
		t.Fatalf("restart attempts=%d", len(observations))
	}
}

func TestTrainingRoutesAvailableWithoutModelAndAuth(t *testing.T) {
	store, err := interview.OpenSQLiteStore(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Create(context.Background(), interview.InterviewSession{ID: "new", LocalProfileID: interview.LocalProfileID, Version: 1, StartedAt: time.Now(), State: interview.StateAwaitingAnswer}); err != nil {
		t.Fatal(err)
	}
	api, err := New(Config{RequireAuth: true, APIKey: "test-key"}, Dependencies{Interview: interview.NewService(interview.Dependencies{Store: store})})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		url, body string
		status    int
	}{
		{"/api/v1/training", "", 200}, {"/api/v1/training?resource=stats", "", 200}, {"/api/v1/training?resource=detail&id=new", "", 200},
		{"/api/v1/training?resource=detail&id=missing", "", 404}, {"/api/v1/training?limit=0", "", 400}, {"/api/v1/training?cursor=bad", "", 400},
		{"/api/v1/training?resource=wrong", "", 400}, {"/api/v1/training", `{"action":"import","ids":[]}`, 400},
	} {
		method := "GET"
		if test.body != "" {
			method = "POST"
		}
		req := httptest.NewRequest(method, test.url, strings.NewReader(test.body))
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		api.Handler().ServeHTTP(res, req)
		if res.Code != test.status {
			t.Fatalf("%s: %d %s", test.url, res.Code, res.Body.String())
		}
	}
	res := httptest.NewRecorder()
	api.Handler().ServeHTTP(res, httptest.NewRequest("GET", "/api/v1/training", nil))
	if res.Code != 401 {
		t.Fatalf("auth=%d", res.Code)
	}
}
