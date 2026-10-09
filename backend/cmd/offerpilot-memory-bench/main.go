// Measurement adapter for S0/S1. Fixed prefixes enter the real durable store;
// compaction uses the real builder/model, probes use the real HTTP chat route.
// Synthetic data only; never opens the installation's database.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"offerpilot/backend/internal/chat"
	"offerpilot/backend/internal/coach"
	"offerpilot/backend/internal/config"
	"offerpilot/backend/internal/httpapi"
	"offerpilot/backend/internal/interview"
	"offerpilot/backend/internal/knowledge"
)

type source struct {
	ID      string `json:"id"`
	Seq     int    `json:"seq"`
	Role    string `json:"role"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
	Ordinal int    `json:"questionOrdinal"`
}
type probe struct {
	ID       string `json:"id"`
	AfterSeq int    `json:"afterSeq"`
	Query    string `json:"query"`
}
type input struct {
	InputCap           int               `json:"inputCap"`
	BudgetRemainingCNY float64           `json:"budgetRemainingCNY"`
	CallJournalDir     string            `json:"callJournalDir"`
	Mode               string            `json:"mode"`
	Variant            string            `json:"variant"`
	History            []source          `json:"history"`
	Probes             []probe           `json:"probes"`
	Knowledge          []knowledge.Entry `json:"knowledge"`
	CaseHash           string            `json:"caseHash"`
	BindingHash        string            `json:"bindingHash"`
}
type call struct {
	Attempted bool            `json:"attempted"`
	Request   json.RawMessage `json:"request"`
	Response  string          `json:"response"`
	Status    int             `json:"status"`
	Started   string          `json:"startedAt"`
	Millis    float64         `json:"millis"`
}
type capture struct {
	guard     *feeGuard
	transport http.RoundTripper
	calls     []*call
}
type capturedBody struct {
	finish  func(string)
	archive io.WriteCloser
	io.ReadCloser
	buf    bytes.Buffer
	record *call
	start  time.Time
}

func (b *capturedBody) Read(p []byte) (int, error) {
	n, e := b.ReadCloser.Read(p)
	b.buf.Write(p[:n])
	if b.archive != nil {
		if _, err := b.archive.Write(p[:n]); err != nil {
			return n, err
		}
	}
	if e != nil {
		b.record.Response = b.buf.String()
		b.record.Millis = float64(time.Since(b.start).Microseconds()) / 1000
	}
	return n, e
}
func (b *capturedBody) Close() error {
	b.record.Response = b.buf.String()
	b.record.Millis = float64(time.Since(b.start).Microseconds()) / 1000
	if b.archive != nil {
		b.archive.Close()
		b.archive = nil
	}
	if b.finish != nil {
		b.finish(b.buf.String())
		b.finish = nil
	}
	return b.ReadCloser.Close()
}
func (c *capture) RoundTrip(r *http.Request) (*http.Response, error) {
	b, e := io.ReadAll(r.Body)
	if e != nil {
		return nil, e
	}
	r.Body = io.NopCloser(bytes.NewReader(b))
	start := time.Now()
	record := &call{Request: b, Started: start.UTC().Format(time.RFC3339Nano)}
	c.calls = append(c.calls, record)
	var ticket *feeTicket
	if c.guard != nil {
		ticket, e = c.guard.begin(b)
		if e != nil {
			return nil, e
		}
	}
	record.Attempted = true
	response, e := c.transport.RoundTrip(r)
	if e == nil {
		record.Status = response.StatusCode
		body := &capturedBody{ReadCloser: response.Body, record: record, start: start}
		if ticket != nil {
			body.archive = ticket.responseFile
			body.finish = func(raw string) { c.guard.finish(ticket, response.StatusCode, raw) }
			if response.StatusCode != 200 {
				c.guard.stopped = "provider_http_" + fmt.Sprint(response.StatusCode)
			}
		}
		response.Body = body
	} else {
		record.Millis = float64(time.Since(start).Microseconds()) / 1000
		if ticket != nil {
			c.guard.finish(ticket, 0, "")
			c.guard.stopped = "provider_transport_error"
		}
	}
	return response, e
}

type result struct {
	FeeGuard           *feeGuard        `json:"feeGuard,omitempty"`
	InputCap           int              `json:"inputCap"`
	PolicyVersion      string           `json:"contextPolicyVersion"`
	Variant            string           `json:"variant"`
	Mode               string           `json:"mode"`
	CaseHash           string           `json:"caseHash"`
	BindingHash        string           `json:"bindingHash"`
	Counter            string           `json:"counter"`
	PrefixRuns         []coach.Run      `json:"prefixRuns"`
	PrefixFailures     []map[string]any `json:"prefixFailures"`
	Probes             []map[string]any `json:"probes"`
	Calls              []*call          `json:"calls"`
	OriginalsUnchanged bool             `json:"originalsUnchanged"`
}

func run(in input) (result, error) {
	out := result{Variant: in.Variant, Mode: in.Mode, CaseHash: in.CaseHash, BindingHash: in.BindingHash, Counter: "unicode-request-estimate-v1+30%+256", PrefixRuns: []coach.Run{}, PrefixFailures: []map[string]any{}, Probes: []map[string]any{}, OriginalsUnchanged: true}
	if in.Variant != "S0" && in.Variant != "S1" {
		return out, fmt.Errorf("variant must be S0 or S1")
	}
	if in.Mode != "live" && in.Mode != "transport_fixture" {
		return out, fmt.Errorf("mode must be live or transport_fixture")
	}
	apiKey, base, model := os.Getenv("OPENAI_API_KEY"), os.Getenv("OPENAI_BASE_URL"), os.Getenv("OPENAI_MODEL")
	if in.Mode == "transport_fixture" {
		fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				Messages []chat.Message `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			text := "固定传输测试回复，不能视为真实模型效果。"
			if len(request.Messages) > 0 && strings.Contains(request.Messages[0].Content, "教学笔记") {
				text = `{"summary":"早期在讨论教学过程，不表示已经掌握。","quotes":[]}`
			}
			data, _ := json.Marshal(map[string]any{"model": "fixture-model", "choices": []any{map[string]any{"delta": map[string]string{"content": text}, "finish_reason": "stop"}}})
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprintf(w, "data: %s\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":123,\"completion_tokens\":7,\"prompt_cache_hit_tokens\":0,\"prompt_cache_miss_tokens\":123}}\n\ndata: [DONE]\n\n", data)
		}))
		defer fixture.Close()
		apiKey, base, model = "fixture-only", fixture.URL, "fixture-model"
	}
	if apiKey == "" || base == "" || model == "" {
		return out, fmt.Errorf("missing live configuration")
	}
	if in.Mode == "live" && (model != "deepseek-flash" || strings.TrimRight(base, "/") != "https://api.deepseek.com" || in.BindingHash == "" || in.CaseHash == "") {
		return out, fmt.Errorf("live adapter requires the frozen DeepSeek binding and case hashes")
	}
	wire := &capture{transport: http.DefaultTransport}
	if in.Mode == "live" && in.BudgetRemainingCNY > 0 {
		if in.CallJournalDir == "" {
			return out, fmt.Errorf("fee-limited capture requires durable call journal")
		}
		wire.guard = &feeGuard{LimitCNY: in.BudgetRemainingCNY, Dir: in.CallJournalDir}
		out.FeeGuard = wire.guard
	}
	cap := in.InputCap
	if cap == 0 {
		cap = 12000
	}
	if cap != 12000 && cap != 30000 {
		return out, fmt.Errorf("unsupported measured input cap")
	}
	out.InputCap = cap
	out.PolicyVersion = coach.ContextPolicyVersion
	client, e := chat.New(chat.Config{APIKey: apiKey, BaseURL: base, Model: model, Timeout: 90 * time.Second, MaxTokens: 4096}, &http.Client{Transport: wire})
	if e != nil {
		return out, e
	}
	file, e := os.CreateTemp("", "offerpilot-s1-bench-*.db")
	if e != nil {
		return out, e
	}
	path := file.Name()
	file.Close()
	defer os.Remove(path)
	repo, e := interview.OpenSQLiteStore(path)
	if e != nil {
		return out, e
	}
	defer repo.Close()
	svc := coach.New(repo, client, knowledge.NewIndex(in.Knowledge), coach.ContextConfig{DefaultModel: model, InputCap: cap, OutputReserve: 4096, Safety: 1024, Windows: map[string]int{model: 1000000}, DisableSummaries: in.Variant == "S0"})
	api, e := httpapi.New(httpapi.Config{}, httpapi.Dependencies{Interview: interview.NewService(interview.Dependencies{Store: repo}), Coach: svc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if e != nil {
		return out, e
	}
	server := httptest.NewServer(api.Handler())
	defer server.Close()
	ctx := context.Background()
	prefix, e := svc.Create(ctx)
	if e != nil {
		return out, e
	}
	next := 0
	for _, p := range in.Probes {
		if p.AfterSeq < next || p.AfterSeq > len(in.History) {
			return out, fmt.Errorf("invalid checkpoint order")
		}
		for next < p.AfterSeq {
			item := in.History[next]
			next++
			if item.Seq != next || item.ID == "" || (item.Role != "user" && item.Role != "assistant") {
				return out, fmt.Errorf("invalid fixed source metadata")
			}
			prefix, e = svc.Load(ctx, prefix.ID)
			if e != nil {
				return out, e
			}
			kind := item.Kind
			if kind == "original_answer" {
				kind = "answer"
			}
			if kind == "assessment" {
				kind = "evaluation"
			}
			prefix.Messages = append(prefix.Messages, coach.Message{ID: item.ID, Seq: item.Seq, Revision: 1, Role: item.Role, Kind: kind, Content: item.Content, Status: "complete", Ordinal: item.Ordinal, SubmissionID: item.ID, At: time.Unix(0, 0).UTC()})
			version := prefix.Version
			prefix.Version++
			e = repo.SaveTrainingDocument(ctx, coach.DocumentKind, prefix.ID, prefix, version)
			if e != nil {
				return out, e
			}
			if item.Role == "user" {
				before := coach.Hash(prefix.Messages)
				_, buildErr := svc.BuildContext(ctx, prefix.ID, coach.Input{SubmissionID: item.ID, Message: item.Content, Model: model}, "teaching")
				prefix, e = svc.Load(ctx, prefix.ID)
				if e != nil {
					return out, e
				}
				out.OriginalsUnchanged = out.OriginalsUnchanged && before == coach.Hash(prefix.Messages)
				if buildErr != nil {
					out.PrefixFailures = append(out.PrefixFailures, map[string]any{"afterSeq": next, "error": buildErr.Error()})
				}
				if wire.guard != nil && wire.guard.stopped != "" {
					out.PrefixRuns = prefix.Runs
					out.Calls = wire.calls
					return out, nil
				}
			}
		}
		prefix, e = svc.Load(ctx, prefix.ID)
		if e != nil {
			return out, e
		}
		// Probe branches inherit derived summaries but their query/reply/extra
		// summaries never contaminate the next fixed source checkpoint.
		branch := prefix
		branch.ID = prefix.ID + "-probe-" + fmt.Sprint(len(out.Probes))
		branch.Version = 1
		branch.Runs = nil
		branch.Pending = nil
		if e = repo.SaveTrainingDocument(ctx, coach.DocumentKind, branch.ID, branch, 0); e != nil {
			return out, e
		}
		body, _ := json.Marshal(map[string]any{"sessionId": branch.ID, "submissionId": "probe-" + p.ID, "message": p.Query, "model": model})
		start := time.Now()
		response, e := server.Client().Post(server.URL+"/api/chat", "application/json", bytes.NewReader(body))
		if e != nil {
			return out, e
		}
		var first *float64
		var text strings.Builder
		events := []json.RawMessage{}
		terminal := 0.0
		status := "failed"
		message := ""
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 4<<20)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				continue
			}
			var event struct{ Type, Content, Message string }
			if json.Unmarshal([]byte(data), &event) != nil {
				continue
			}
			events = append(events, append(json.RawMessage{}, data...))
			if event.Type == "text_delta" {
				if first == nil {
					n := float64(time.Since(start).Microseconds()) / 1000
					first = &n
				}
				text.WriteString(event.Content)
			}
			if event.Type == "done" {
				status = "completed"
				terminal = float64(time.Since(start).Microseconds()) / 1000
			}
			if event.Type == "error" {
				message = event.Message
				terminal = float64(time.Since(start).Microseconds()) / 1000
			}
		}
		readErr := scanner.Err()
		response.Body.Close()
		if readErr != nil {
			message = readErr.Error()
		}
		saved, _ := svc.Load(ctx, branch.ID)
		out.Probes = append(out.Probes, map[string]any{"id": p.ID, "afterSeq": p.AfterSeq, "status": status, "httpStatus": response.StatusCode, "text": text.String(), "error": message, "firstContentMs": first, "terminalMs": terminal, "responseEndMs": float64(time.Since(start).Microseconds()) / 1000, "events": events, "runs": saved.Runs})
		if wire.guard != nil && wire.guard.stopped != "" {
			out.PrefixRuns = prefix.Runs
			out.Calls = wire.calls
			return out, nil
		}
	}
	out.PrefixRuns = prefix.Runs
	out.Calls = wire.calls
	return out, nil
}
func main() {
	// Load dotenv only for an explicit live request. Configuration is not output.
	var in input
	if e := json.NewDecoder(os.Stdin).Decode(&in); e != nil {
		fmt.Fprintln(os.Stderr, "invalid measurement input")
		os.Exit(1)
	}
	if in.Mode == "live" {
		_, _ = config.LoadDotEnv()
	}
	out, e := run(in)
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(out)
}
