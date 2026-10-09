package resumediagnosis

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aide/backend/internal/harness"
	"aide/backend/internal/llm"
)

func TestDiagnosisConfigurationIsIndependentAndProviderScoped(t *testing.T) {
	base := llm.Config{APIKey: "test", BaseURL: "https://api.deepseek.com/v1", MaxTokens: 1024, MaxRetries: 2}
	got := ModelConfig(base, ModelOptions{DisableThinking: true})
	if got.MaxTokens != 8192 || !got.PreferJSONObject || !got.DisableThinking || !got.DisableStructuredRepair || got.MaxRetries != 0 {
		t.Fatalf("config=%+v", got)
	}
	if base.MaxTokens != 1024 || base.MaxRetries != 2 {
		t.Fatal("changed shared configuration")
	}
	got = ModelConfig(base, ModelOptions{MaxTokens: 6000, DisableThinking: false})
	if got.DisableThinking || got.MaxTokens != 6000 {
		t.Fatalf("explicit options ignored: %+v", got)
	}
	for _, host := range []string{"https://gateway.example/v1", "https://api.deepseek.com.attacker.example/v1"} {
		base.BaseURL = host
		got = ModelConfig(base, ModelOptions{DisableThinking: true})
		if got.PreferJSONObject || got.DisableThinking {
			t.Fatalf("provider extension sent to %s", host)
		}
	}
}

// Replay the exact error shape seen in production, then verify the dedicated
// request succeeds without a compatibility probe or losing the resume text.
func TestDiagnosisReplaysCompatibilityEOFAndUsesDirectJSON(t *testing.T) {
	for _, vision := range []bool{false, true} {
		t.Run(map[bool]string{false: "docx_text", true: "page_images"}[vision], func(t *testing.T) {
			result := validMultimodalResult()
			if !vision {
				result.Mode = "text_only"
				result.Layout = LayoutAssessment{Summary: "未提供页面图片，本次仅诊断文字内容。"}
			}
			resume := strings.Repeat("教育经历与项目成果。", 229) + "原文末尾标记"
			oldCalls, newCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					ResponseFormat struct {
						Type string `json:"type"`
					} `json:"response_format"`
					MaxTokens int `json:"max_tokens"`
					Thinking  struct {
						Type string `json:"type"`
					} `json:"thinking"`
					Messages json.RawMessage `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if body.Thinking.Type != "disabled" {
					oldCalls++
					if body.ResponseFormat.Type == "json_schema" {
						w.WriteHeader(400)
						_, _ = w.Write([]byte(`{"error":{"message":"json_schema response_format unsupported"}}`))
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": `{"overallScore":82`}}}})
					return
				}
				newCalls++
				if body.ResponseFormat.Type != "json_object" || body.MaxTokens != 8192 {
					t.Errorf("unexpected settings: %+v", body)
				}
				var decoded any
				_ = json.Unmarshal(body.Messages, &decoded)
				canonical, _ := json.Marshal(decoded)
				if !strings.Contains(string(canonical), resume) {
					t.Error("full resume missing from provider request")
				}
				if !strings.Contains(string(canonical), "expectedMode") {
					t.Error("expected mode missing")
				}
				if vision && !strings.Contains(string(canonical), "data:image/jpeg;base64,fixture") {
					t.Error("page image lost")
				}
				encoded, _ := json.Marshal(result)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(encoded)}}}})
			}))
			defer server.Close()
			base := llm.Config{APIKey: "test", BaseURL: server.URL, Model: "fixture", MaxTokens: 8192}
			old, _ := llm.New(base)
			var ignored Result
			err := old.ChatJSON(context.Background(), []llm.Message{{Role: llm.RoleUser, Content: resume}}, &ignored)
			if !errors.Is(err, llm.ErrStructuredOutput) || !strings.Contains(err.Error(), "unexpected EOF") || oldCalls != 2 {
				t.Fatalf("old replay calls=%d error=%v", oldCalls, err)
			}
			// The production factory enables these flags only for the official host.
			base.BaseURL = "https://api.deepseek.com"
			cfg := ModelConfig(base, ModelOptions{DisableThinking: true})
			cfg.BaseURL = server.URL
			client, _ := llm.New(cfg)
			runtime, _ := harness.NewRuntime(client, harness.Options{})
			agent, _ := NewAgent(runtime, AgentOptions{})
			request := Request{Content: resume}
			if vision {
				request.Images = []string{"data:image/jpeg;base64,fixture"}
			}
			actual, err := agent.Diagnose(context.Background(), request)
			if err != nil || actual.Mode != result.Mode || len(actual.Diagnosis) != 4 || newCalls != 1 {
				t.Fatalf("new replay calls=%d mode=%s error=%v", newCalls, actual.Mode, err)
			}
		})
	}
}
