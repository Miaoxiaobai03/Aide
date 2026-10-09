package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStructuredOutputFailuresNeverProducePartialResults(t *testing.T) {
	for _, tc := range []struct {
		name, reason, content string
		want                  error
	}{
		{"partial_limit", "length", `{"score":`, ErrOutputTruncated},
		{"valid_looking_limit", "length", `{"score":5}`, ErrOutputTruncated},
		{"invalid_stop", "stop", `{"score":`, ErrStructuredOutput},
		{"empty_stop", "stop", "", ErrStructuredOutput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": tc.reason, "message": map[string]any{"content": tc.content}}}, "usage": map[string]any{"completion_tokens": 8192, "completion_tokens_details": map[string]any{"reasoning_tokens": 7000}}})
			}))
			defer server.Close()
			client, _ := New(Config{APIKey: "test", BaseURL: server.URL, MaxTokens: 8192, PreferJSONObject: true, DisableStructuredRepair: true})
			out := struct {
				Score int `json:"score"`
			}{Score: 99}
			err := client.ChatJSON(context.Background(), []Message{{Role: RoleUser, Content: "evaluate"}}, &out)
			if !errors.Is(err, tc.want) || calls != 1 || out.Score != 99 {
				t.Fatalf("error=%v calls=%d output=%+v", err, calls, out)
			}
		})
	}
}
