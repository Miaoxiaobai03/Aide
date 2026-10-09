package chat

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type summaryTransport struct {
	t         *testing.T
	bodies    []map[string]any
	deadlines []time.Duration
}

func (tr *summaryTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body map[string]any
	if e := json.NewDecoder(r.Body).Decode(&body); e != nil {
		tr.t.Fatal(e)
	}
	tr.bodies = append(tr.bodies, body)
	deadline, ok := r.Context().Deadline()
	if !ok {
		tr.t.Fatal("missing bounded timeout")
	}
	tr.deadlines = append(tr.deadlines, time.Until(deadline))
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"正文"},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":8}}`))}, nil
}
func TestSummaryUsesIndependentProviderSettings(t *testing.T) {
	for _, host := range []string{"https://api.deepseek.com", "https://example.invalid/v1"} {
		tr := &summaryTransport{t: t}
		c, e := New(Config{APIKey: "local-fixture", BaseURL: host, MaxTokens: 2000, Timeout: time.Minute, SummaryMaxTokens: 9000, SummaryTimeout: 35 * time.Second}, &http.Client{Transport: tr})
		if e != nil {
			t.Fatal(e)
		}
		msgs := []Message{{Role: "user", Content: "本题教学"}}
		c.Stream(context.Background(), "deepseek-flash", msgs, nil)
		c.StreamSummary(context.Background(), "deepseek-flash", msgs, nil)
		if tr.bodies[0]["max_tokens"] != float64(2000) || tr.bodies[1]["max_tokens"] != float64(9000) {
			t.Fatal("summary and teaching output quotas not independent")
		}
		if tr.bodies[0]["thinking"] != nil {
			t.Fatal("teaching configuration modified")
		}
		thinking := tr.bodies[1]["thinking"]
		if strings.Contains(host, "deepseek.com") {
			if thinking.(map[string]any)["type"] != "disabled" {
				t.Fatal("DeepSeek summary thinking not disabled")
			}
		} else if thinking != nil {
			t.Fatal("unsupported provider received thinking flag")
		}
		if tr.deadlines[1] > 35*time.Second || tr.deadlines[1] < 30*time.Second {
			t.Fatal("summary timeout not independent")
		}
		enabled := false
		c.config.DisableSummaryThinking = &enabled
		c.StreamSummary(context.Background(), "deepseek-flash", msgs, nil)
		if tr.bodies[2]["thinking"] != nil {
			t.Fatal("explicit thinking configuration ignored")
		}
	}
}
