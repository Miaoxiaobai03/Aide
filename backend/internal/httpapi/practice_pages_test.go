package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"offerpilot/backend/internal/coach"
	"offerpilot/backend/internal/interview"
	"offerpilot/backend/internal/knowledge"
)

// Exercise page contracts and data boundaries, not merely endpoint availability.
func TestPracticePageHTTPContracts(t *testing.T) {
	db, err := interview.OpenSQLiteStore(filepath.Join(t.TempDir(), "page-contract.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	index := knowledge.NewIndex([]knowledge.Entry{
		{ID: "qa", Question: "如何处理第一种问题？", Excerpt: strings.Repeat("第一题私有参考正文。", 5)},
		{ID: "qb", Question: "如何处理第二种问题？", Excerpt: strings.Repeat("第二题私有参考正文。", 5)},
	})
	service := coach.New(db, nil, index, coach.ContextConfig{})
	api, err := New(Config{}, Dependencies{Coach: service, Interview: interview.NewService(interview.Dependencies{Store: db}), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, url string, body any, status int) []byte {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req := httptest.NewRequest(method, url, bytes.NewReader(data))
		out := httptest.NewRecorder()
		api.Handler().ServeHTTP(out, req)
		if out.Code != status {
			t.Fatalf("%s %s: %d %s", method, url, out.Code, out.Body.String())
		}
		return out.Body.Bytes()
	}
	created := call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "practice", "count": 2, "submissionId": "api-creation"}, 200)
	var parent coach.Conversation
	if err = json.Unmarshal(created, &parent); err != nil {
		t.Fatal(err)
	}
	if !parent.PageStorage {
		t.Fatal("Web API created legacy aggregate")
	}
	replay := call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "practice", "count": 2, "submissionId": "api-creation"}, 200)
	var same coach.Conversation
	_ = json.Unmarshal(replay, &same)
	if same.ID != parent.ID {
		t.Fatal("create replay drew a new practice")
	}
	dirRaw := call(http.MethodGet, "/api/v1/coach?resource=directory&id="+parent.ID, nil, 200)
	var dir interview.PracticeInfo
	_ = json.Unmarshal(dirRaw, &dir)
	if len(dir.Pages) != 2 || dir.Completed != 0 || bytes.Contains(dirRaw, []byte("私有参考正文")) {
		t.Fatal("directory includes references or wrong progress")
	}
	listRaw := call(http.MethodGet, "/api/v1/coach?resource=list", nil, 200)
	if !bytes.Contains(listRaw, []byte(parent.ID)) || bytes.Contains(listRaw, []byte("私有参考正文")) {
		t.Fatal("practice listing missing or includes transcript")
	}
	second := dir.Pages[1].QuestionID
	var view coach.PageView
	data := call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "visitpage", "practiceId": parent.ID, "questionId": second}, 200)
	if err = json.Unmarshal(data, &view); err != nil {
		t.Fatal(err)
	}
	if view.Conversation.PageOrdinal != 2 || len(view.Conversation.Plan.Questions) != 1 || len(view.Conversation.Messages) != 1 || view.Practice.Completed != 0 {
		t.Fatal("unanswered navigation altered score or included another page")
	}
	call(http.MethodGet, "/api/v1/coach?resource=page&id="+parent.ID+"&questionId=missing", nil, 404)
	call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "answer", "practiceId": parent.ID, "submissionId": "missing-target", "message": "作答"}, 400)
	// Grade against the persisted reference, without a paid model.
	raw, _, err := db.PracticePage(t.Context(), parent.ID, second)
	if err != nil {
		t.Fatal(err)
	}
	var private coach.Conversation
	_ = json.Unmarshal(raw, &private)
	answer := private.Plan.Questions[0].Reference
	result := call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "answer", "practiceId": parent.ID, "questionId": second, "submissionId": "one-answer", "message": answer}, 200)
	var graded coach.Conversation
	_ = json.Unmarshal(result, &graded)
	if len(graded.Plan.Attempts) != 1 || graded.Plan.Attempts[0].Evaluation.Correctness != 5 {
		t.Fatal("answer bound to wrong question")
	}
	call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "answer", "practiceId": parent.ID, "questionId": second, "submissionId": "one-answer", "message": answer + "不同"}, 409)
	firstRaw := call(http.MethodGet, "/api/v1/coach?resource=page&id="+parent.ID+"&questionId="+dir.Pages[0].QuestionID, nil, 200)
	var first coach.PageView
	_ = json.Unmarshal(firstRaw, &first)
	if len(first.Conversation.Messages) != 1 || first.Practice.Completed != 1 {
		t.Fatal("scoring leaked to first page or lost directory progress")
	}
	call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "endpages", "practiceId": parent.ID, "version": dir.Version}, 200)
	if data = call(http.MethodGet, "/api/v1/coach?resource=directory&id="+parent.ID, nil, 200); !bytes.Contains(data, []byte(`"ended":true`)) {
		t.Fatal("end state missing")
	}
	call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "practice", "count": 0}, 400)
	// Ended practice deletion is a business transition, not a connectivity check.
	call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "deletepages", "practiceId": parent.ID, "version": 999}, 409)
	var ended interview.PracticeInfo
	json.Unmarshal(call(http.MethodGet, "/api/v1/coach?resource=directory&id="+parent.ID, nil, 200), &ended)
	call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "deletepages", "practiceId": parent.ID, "version": ended.Version}, 200)
	call(http.MethodGet, "/api/v1/coach?resource=directory&id="+parent.ID, nil, 404)
	call(http.MethodGet, "/api/v1/coach?resource=page&id="+parent.ID+"&questionId="+second, nil, 404)
	call(http.MethodPost, "/api/v1/coach", map[string]any{"action": "deletepages", "practiceId": parent.ID, "version": ended.Version}, 200)
	statsAfter := call(http.MethodGet, "/api/v1/training?resource=stats", nil, 200)
	var stats interview.TrainingStats
	if e := json.Unmarshal(statsAfter, &stats); e != nil || stats.Sessions != 0 || stats.Evaluated != 0 {
		t.Fatal("deleted HTTP practice remains in statistics", e, string(statsAfter))
	}

}
