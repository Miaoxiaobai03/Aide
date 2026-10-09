package coach_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"aide/backend/internal/chat"
	"aide/backend/internal/coach"
	"aide/backend/internal/httpapi"
	"aide/backend/internal/interview"
	"aide/backend/internal/knowledge"
)

func TestGradingActualHTTPTransportAndRecovery(t *testing.T) {
	reference := "动态工具加载应按任务选择工具，压缩描述并按能力分层。延迟加载应在需要某类工具时触发，权限过滤不能省略。"
	original := "我会先路由当前意图，仅注入相关工具，再加载完整schema，并校验租户权限。"
	mode := "missing"
	calls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []chat.Message `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Error("provider request JSON")
			w.WriteHeader(400)
			return
		}
		var data struct {
			Answer       string `json:"student_answer"`
			Reference    string `json:"frozen_reference"`
			AnswerHash   string `json:"answer_hash"`
			SubmissionID string `json:"submission_id"`
			Ordinal      int    `json:"ordinal"`
		}
		if json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &data) != nil || data.Answer != original || data.Reference != reference || data.AnswerHash != coach.RawHash(original) || data.SubmissionID != "http-evidence" || data.Ordinal != 1 {
			t.Error("actual HTTP request lost original or source")
		}
		calls++
		e := coach.Evaluation{Version: coach.GradingVersion, Status: "assessed", AnswerQuote: original, Correctness: 5, Coverage: 5, Explanation: 5, Strengths: []string{"思路完整"}, Gaps: []coach.Gap{}, Advice: "继续独立练习"}
		if mode == "missing" {
			e.Correctness = 1
			e.Coverage = 1
			e.Explanation = 1
			e.Gaps = []coach.Gap{{Point: "未提供本次作答原文", Reason: "没有用户作答，无法评估", Quote: reference}}
		}
		b, _ := json.Marshal(e)
		content := string(b)
		if mode == "json" {
			content = "不是JSON"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 100, "completion_tokens": 80}})
	}))
	defer provider.Close()
	model, e := chat.New(chat.Config{APIKey: "local-fixture-not-a-secret", BaseURL: provider.URL, Model: "fixture"}, provider.Client())
	if e != nil {
		t.Fatal(e)
	}
	repo, e := interview.OpenSQLiteStore(filepath.Join(t.TempDir(), "transport.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer repo.Close()
	service := coach.New(repo, model, knowledge.NewIndex([]knowledge.Entry{{ID: "q", Question: "工具多如何处理？", Source: "fixture", Excerpt: reference}}), coach.ContextConfig{DefaultModel: "fixture"})
	api, e := httpapi.New(httpapi.Config{}, httpapi.Dependencies{Coach: service, Interview: interview.NewService(interview.Dependencies{Store: repo}), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if e != nil {
		t.Fatal(e)
	}
	call := func(body map[string]any) coach.Conversation {
		b, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", "/api/v1/coach", bytes.NewReader(b))
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
		var c coach.Conversation
		if json.Unmarshal(w.Body.Bytes(), &c) != nil {
			t.Fatal("response JSON")
		}
		return c
	}
	c := call(map[string]any{"action": "practice", "count": 1})
	input := map[string]any{"action": "answer", "practiceId": c.ID, "questionId": c.Plan.Questions[0].ID, "submissionId": "http-evidence", "message": original}
	for _, state := range []struct{ mode, code string }{{"missing", "assessment_evidence_conflict"}, {"json", "assessment_json"}, {"valid", ""}} {
		mode = state.mode
		c = call(input)
		a := c.Plan.Attempts[0]
		if state.code != "" {
			if a.Status != "failed" || a.ErrorCode != state.code || a.Evaluation != nil {
				t.Fatalf("failed gate %+v", a)
			}
		} else if a.Status != "succeeded" || a.Evaluation.Correctness != 5 {
			t.Fatalf("valid grade %+v", a)
		}
	}
	c = call(input)
	if calls != 3 || len(c.Plan.Attempts) != 1 || c.Plan.Attempts[0].Answer != original || len(c.Plan.Attempts[0].History) != 2 {
		t.Fatal("retry lost original, duplicated or bypassed evidence")
	}
	state, _ := service.Load(t.Context(), c.ID)
	if len(state.Runs) != 3 || !state.Runs[0].ModelCalled || state.Runs[0].AnswerHash != coach.RawHash(original) || state.Runs[0].ResponseHash == "" {
		t.Fatal("outgoing source trace missing")
	}
}
