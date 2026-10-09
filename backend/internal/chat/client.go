// Package chat provides the OpenAI-compatible streaming boundary used by the
// legacy free-form chat endpoint while interview orchestration lives in the
// typed harness packages.
package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	APIKey                 string
	BaseURL                string
	Model                  string
	Timeout                time.Duration
	MaxTokens              int
	DisableThinking        bool
	SummaryMaxTokens       int
	SummaryTimeout         time.Duration
	DisableSummaryThinking *bool
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Usage struct {
	InputTokens     int    `json:"inputTokens"`
	OutputTokens    int    `json:"outputTokens"`
	Known           bool   `json:"known"`
	CacheHitTokens  int    `json:"cacheHitTokens,omitempty"`
	CacheMissTokens int    `json:"cacheMissTokens,omitempty"`
	ReasoningTokens int    `json:"reasoningTokens,omitempty"`
	FinishReason    string `json:"finishReason,omitempty"`
	Model           string `json:"model,omitempty"`
}

type Delta struct {
	Text     string
	Thinking string
}

type Client struct {
	config Config
	http   *http.Client
}

func New(config Config, httpClient *http.Client) (*Client, error) {
	config.APIKey = strings.TrimSpace(config.APIKey)
	if config.APIKey == "" {
		return nil, errors.New("chat: OPENAI_API_KEY is required")
	}
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if config.BaseURL == "" {
		config.BaseURL = "https://api.openai.com/v1"
	}
	if strings.TrimSpace(config.Model) == "" {
		config.Model = "gpt-4o-mini"
	}
	if config.Timeout <= 0 {
		config.Timeout = 90 * time.Second
	}
	if config.MaxTokens <= 0 {
		config.MaxTokens = 4096
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{config: config, http: httpClient}, nil
}

func (c *Client) Stream(ctx context.Context, model string, messages []Message, onDelta func(Delta) error) (Usage, error) {
	if len(messages) == 0 {
		return Usage{}, errors.New("chat: at least one message is required")
	}
	if strings.TrimSpace(model) == "" {
		model = c.config.Model
	}
	payload := struct {
		Model         string    `json:"model"`
		Messages      []Message `json:"messages"`
		MaxTokens     int       `json:"max_tokens"`
		Stream        bool      `json:"stream"`
		Thinking      any       `json:"thinking,omitempty"`
		StreamOptions any       `json:"stream_options,omitempty"`
	}{
		Model:         model,
		Messages:      messages,
		MaxTokens:     c.config.MaxTokens,
		Stream:        true,
		StreamOptions: map[string]bool{"include_usage": true},
	}
	if c.config.DisableThinking {
		payload.Thinking = map[string]string{"type": "disabled"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Usage{}, fmt.Errorf("chat: encode request: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Usage{}, fmt.Errorf("chat: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream, application/json")

	response, err := c.http.Do(req)
	if err != nil {
		return Usage{}, fmt.Errorf("chat: provider request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4097))
		return Usage{}, fmt.Errorf("chat: provider HTTP %d: %s", response.StatusCode, compact(string(data), 4000))
	}
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		return readEventStream(response.Body, onDelta)
	}
	return readJSONCompletion(response.Body, onDelta)
}

type completionChunk struct {
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Delta        struct {
			Content          json.RawMessage `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
		} `json:"delta"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		CacheHitTokens   int `json:"prompt_cache_hit_tokens"`
		CacheMissTokens  int `json:"prompt_cache_miss_tokens"`
		Details          struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func mergeUsage(usage *Usage, chunk completionChunk) {
	if chunk.Model != "" {
		usage.Model = chunk.Model
	}
	if len(chunk.Choices) > 0 && chunk.Choices[0].FinishReason != "" {
		usage.FinishReason = chunk.Choices[0].FinishReason
	}
	if chunk.Usage != nil {
		usage.Known = true
		usage.InputTokens = chunk.Usage.PromptTokens
		usage.OutputTokens = chunk.Usage.CompletionTokens
		usage.CacheHitTokens = chunk.Usage.CacheHitTokens
		usage.CacheMissTokens = chunk.Usage.CacheMissTokens
		usage.ReasoningTokens = chunk.Usage.Details.ReasoningTokens
	}
}

func readEventStream(reader io.Reader, onDelta func(Delta) error) (Usage, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	usage := Usage{}
	terminal := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			if data == "[DONE]" {
				terminal = true
			}
			continue
		}
		var chunk completionChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return usage, fmt.Errorf("chat: decode stream chunk: %w", err)
		}
		if chunk.Error != nil {
			return usage, errors.New("chat: provider stream error: " + chunk.Error.Message)
		}
		mergeUsage(&usage, chunk)
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := Delta{
			Text:     decodeText(chunk.Choices[0].Delta.Content),
			Thinking: strings.TrimSpace(chunk.Choices[0].Delta.ReasoningContent),
		}
		if (delta.Text != "" || delta.Thinking != "") && onDelta != nil {
			if err := onDelta(delta); err != nil {
				return usage, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("chat: read provider stream: %w", err)
	}
	if !terminal && usage.FinishReason == "" {
		return usage, fmt.Errorf("chat: unexpected end of provider stream")
	}
	return usage, nil
}

func readJSONCompletion(reader io.Reader, onDelta func(Delta) error) (Usage, error) {
	var response completionChunk
	decoder := json.NewDecoder(io.LimitReader(reader, 8<<20))
	if err := decoder.Decode(&response); err != nil {
		return Usage{}, fmt.Errorf("chat: decode completion: %w", err)
	}
	usage := Usage{}
	mergeUsage(&usage, response)
	if response.Error != nil {
		return usage, errors.New("chat: provider error: " + response.Error.Message)
	}
	if len(response.Choices) == 0 {
		return usage, errors.New("chat: provider response has no choices")
	}
	text := decodeText(response.Choices[0].Message.Content)
	if text == "" {
		return usage, errors.New("chat: provider returned empty content")
	}
	if onDelta != nil {
		if err := onDelta(Delta{Text: text}); err != nil {
			return usage, err
		}
	}
	return usage, nil
}

// RequestShape includes every transmitted field, without authorization headers.
// Context budget estimates use this same shape as Stream.
func RequestShape(model string, messages []Message, maxTokens int) []byte {
	b, _ := json.Marshal(struct {
		Model     string          `json:"model"`
		Messages  []Message       `json:"messages"`
		MaxTokens int             `json:"max_tokens"`
		Stream    bool            `json:"stream"`
		Options   map[string]bool `json:"stream_options"`
	}{model, messages, maxTokens, true, map[string]bool{"include_usage": true}})
	return b
}

func decodeText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var result strings.Builder
	for _, part := range parts {
		if part.Type == "" || part.Type == "text" || part.Type == "output_text" {
			result.WriteString(part.Text)
		}
	}
	return result.String()
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

// Summary requests have bounded independent generation settings. Only the
// documented DeepSeek endpoint receives its provider-specific thinking toggle.
func (c *Client) StreamSummary(ctx context.Context, model string, msgs []Message, delta func(Delta) error) (Usage, error) {
	clone := *c
	clone.config = c.config
	clone.config.MaxTokens = c.config.SummaryMaxTokens
	if clone.config.MaxTokens <= 0 {
		clone.config.MaxTokens = 8192
	}
	clone.config.Timeout = c.config.SummaryTimeout
	if clone.config.Timeout <= 0 {
		clone.config.Timeout = 45 * time.Second
	}
	if endpoint, err := url.Parse(c.config.BaseURL); err == nil && endpoint.Hostname() == "api.deepseek.com" {
		clone.config.DisableThinking = c.config.DisableSummaryThinking == nil || *c.config.DisableSummaryThinking
	}
	return clone.Stream(ctx, model, msgs, delta)
}
