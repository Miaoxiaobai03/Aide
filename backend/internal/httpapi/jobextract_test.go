package httpapi

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"offerpilot/backend/internal/jobextract"
)

type jdExtractorStub struct {
	called bool
	text   string
	err    error
}

func (stub *jdExtractorStub) Extract(_ context.Context, _ string) (string, error) {
	stub.called = true
	return stub.text, stub.err
}

func TestJDImageEndpoint(t *testing.T) {
	image := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte{137, 80, 78, 71, 13, 10, 26, 10})
	for _, test := range []struct {
		name, body, text string
		err              error
		status           int
		called           bool
	}{
		{name: "success", body: `{"image":"` + image + `"}`, text: "岗位职责", status: http.StatusOK, called: true},
		{name: "remote URL", body: `{"image":"https://example.com/a.png"}`, status: http.StatusBadRequest},
		{name: "bad base64", body: `{"image":"data:image/png;base64,!!"}`, status: http.StatusBadRequest},
		{name: "model error", body: `{"image":"` + image + `"}`, err: errors.New("SECRET_MODEL_ERROR"), status: http.StatusServiceUnavailable, called: true},
		{name: "no text", body: `{"image":"` + image + `"}`, err: jobextract.ErrNoReadableText, status: http.StatusUnprocessableEntity, called: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			extractor := &jdExtractorStub{text: test.text, err: test.err}
			server, err := New(Config{}, Dependencies{Interview: &interviewStub{}, JDExtractor: extractor})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/jobs/extract-image", strings.NewReader(test.body)))
			if response.Code != test.status || extractor.called != test.called || strings.Contains(response.Body.String(), "SECRET_MODEL_ERROR") {
				t.Fatalf("status=%d called=%v body=%s", response.Code, extractor.called, response.Body.String())
			}
		})
	}
}
