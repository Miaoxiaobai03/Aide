package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"aide/backend/internal/coach"
	"aide/backend/internal/interview"
	"aide/backend/internal/knowledge"
	"path/filepath"
	"strings"
	"testing"
)

func TestPageBudgetErrorPreservesInputAndCarriesCode(t *testing.T) {
	db, e := interview.OpenSQLiteStore(filepath.Join(t.TempDir(), "budget-http.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	model := &recordingChatClient{}
	s := coach.New(db, model, knowledge.NewIndex([]knowledge.Entry{{ID: "q", Question: "本题预算边界", Excerpt: strings.Repeat("冻结依据保持完整。", 30)}}), coach.ContextConfig{InputCap: 100})
	parent, e := s.StartPracticePagesWithID(t.Context(), 1, "budget-http")
	if e != nil {
		t.Fatal(e)
	}
	v, _ := s.Page(t.Context(), parent.ID, "", true)
	s.Learn(t.Context(), v.Conversation.ID, 1)
	api, e := New(Config{}, Dependencies{Coach: s, Interview: interview.NewService(interview.Dependencies{Store: db})})
	if e != nil {
		t.Fatal(e)
	}
	original := "本次原始问题必须完整保留"
	data, _ := json.Marshal(map[string]any{"sessionId": v.Conversation.ID, "practiceId": parent.ID, "questionId": v.Conversation.Plan.Questions[0].ID, "submissionId": "budget-submit", "message": original, "retryCompression": true})
	out := httptest.NewRecorder()
	api.Handler().ServeHTTP(out, httptest.NewRequest("POST", "/api/chat", bytes.NewReader(data)))
	if out.Code != 200 || !strings.Contains(out.Body.String(), `"code":"context_budget"`) || len(model.calls) != 0 {
		t.Fatal("budget error contract incorrect", out.Code, out.Body.String())
	}
	saved, e := s.Load(t.Context(), v.Conversation.ID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, m := range saved.Messages {
		if m.SubmissionID == "budget-submit" && m.Role == "user" && m.Content == original {
			found = true
		}
	}
	if !found || saved.Pending != nil {
		t.Fatal("budget failure lost input or left lease busy")
	}
}
