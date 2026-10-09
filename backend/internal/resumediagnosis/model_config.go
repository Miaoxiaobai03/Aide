package resumediagnosis

import (
	"net/url"
	"strings"

	"aide/backend/internal/llm"
)

type ModelOptions struct {
	MaxTokens       int
	DisableThinking bool
}

// ModelConfig gives diagnosis its own output budget and avoids silent paid retries.
// Provider extensions are sent only to the documented DeepSeek endpoint.
func ModelConfig(base llm.Config, options ModelOptions) llm.Config {
	base.MaxTokens = options.MaxTokens
	if base.MaxTokens <= 0 {
		base.MaxTokens = 8192
	}
	base.MaxRetries = 0
	base.DisableStructuredRepair = true
	endpoint, err := url.Parse(base.BaseURL)
	if err == nil && strings.EqualFold(endpoint.Hostname(), "api.deepseek.com") {
		base.PreferJSONObject = true
		base.DisableThinking = options.DisableThinking
	}
	return base
}
