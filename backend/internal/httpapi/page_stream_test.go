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

func TestPageStreamIdentitySequenceAndCrossQuestionRejection(t *testing.T) {
	db, err := interview.OpenSQLiteStore(filepath.Join(t.TempDir(), "stream.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	index := knowledge.NewIndex([]knowledge.Entry{{ID: "a", Question: "问题甲", Excerpt: strings.Repeat("参考甲", 5)}, {ID: "b", Question: "问题乙", Excerpt: strings.Repeat("参考乙", 5)}})
	m := &recordingChatClient{}
	service := coach.New(db, m, index, coach.ContextConfig{})
	parent, err := service.StartPracticePagesWithID(t.Context(), 2, "event-practice")
	if err != nil {
		t.Fatal(err)
	}
	v, _ := service.Page(t.Context(), parent.ID, "", true)
	_, err = service.Learn(t.Context(), v.Conversation.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	api, err := New(Config{}, Dependencies{Coach: service, Interview: interview.NewService(interview.Dependencies{Store: db})})
	if err != nil {
		t.Fatal(err)
	}
	call := func(qid, ref string, status int) []map[string]any {
		body := map[string]any{"sessionId": v.Conversation.ID, "practiceId": parent.ID, "questionId": qid, "submissionId": "request-" + ref, "message": "继续解释"}
		if ref != "" {
			body["references"] = []string{ref}
		}
		data, _ := json.Marshal(body)
		out := httptest.NewRecorder()
		api.Handler().ServeHTTP(out, httptest.NewRequest("POST", "/api/chat", bytes.NewReader(data)))
		if out.Code != status {
			t.Fatalf("HTTP %d: %s", out.Code, out.Body.String())
		}
		var events []map[string]any
		for _, line := range strings.Split(out.Body.String(), "\n") {
			if !strings.HasPrefix(line, "data: {") {
				continue
			}
			var e map[string]any
			if err := json.Unmarshal([]byte(line[6:]), &e); err != nil {
				t.Fatal(err)
			}
			if e["practiceId"] != parent.ID || e["questionId"] != qid || e["operationId"] != "request-"+ref || e["sequence"] != float64(len(events)+1) {
				t.Fatalf("event identity or ordering missing: %#v", e)
			}
			events = append(events, e)
		}
		return events
	}
	qid := v.Conversation.Plan.Questions[0].ID
	events := call(qid, "", 200)
	if len(events) != 3 || events[0]["type"] != "session" || events[2]["type"] != "done" {
		t.Fatal("completed stream missing boundaries")
	}
	before := len(m.calls)
	events = call(qid, "foreign-message", 200)
	if events[len(events)-1]["type"] != "error" || len(m.calls) != before {
		t.Fatal("scope error was paid or lacked identity")
	}
	other := parent.Plan.Questions[1].ID
	if other == qid {
		other = parent.Plan.Questions[0].ID
	}
	call(other, "wrong-question", 422)
	if len(m.calls) != before {
		t.Fatal("mismatched page called model")
	}
}
