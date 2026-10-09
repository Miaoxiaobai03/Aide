package webcrawler

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"aide/backend/internal/config"
	"aide/backend/internal/harness"
	"aide/backend/internal/llm"
)

type forcedFallbackFetcher struct {
	*Fetcher
}

func (fetcher *forcedFallbackFetcher) Fetch(context.Context, Request) (Result, error) {
	return Result{}, ErrNoContent
}

type liveLoggingClient struct {
	t        *testing.T
	delegate llm.StructuredClient
}

func (client liveLoggingClient) ChatJSON(ctx context.Context, messages []llm.Message, out any) error {
	err := client.delegate.ChatJSON(ctx, messages, out)
	if decision, ok := out.(*crawlDecision); ok {
		client.t.Logf("crawler decision: action=%s tool=%s url=%s reason=%s error=%v", decision.Action, decision.Tool, decision.URL, decision.Reason, err)
	}
	return err
}

func TestLiveModelFallbackCrawlsDynamicJobPage(t *testing.T) {
	if os.Getenv("AIDE_RUN_LIVE_CRAWLER_FALLBACK") != "1" {
		t.Skip("set AIDE_RUN_LIVE_CRAWLER_FALLBACK=1 to run the live model/tool integration")
	}
	if _, err := config.LoadDotEnv(); err != nil {
		t.Fatal(err)
	}
	model, err := llm.NewFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := harness.NewRuntime(liveLoggingClient{t: t, delegate: model}, harness.Options{MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	fetcher := &forcedFallbackFetcher{Fetcher: NewFetcher(Options{
		RequestTimeout: 15 * time.Second, MaxResponseBytes: 2 << 20,
		MaxRedirects: 3, AllowBenchmarkTunnel: true,
	})}
	agent, err := NewAgentWithOptions(runtime, fetcher, AgentOptions{
		MaxFallbackIterations: 4, FallbackTimeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	target := os.Getenv("AIDE_LIVE_CRAWLER_URL")
	if strings.TrimSpace(target) == "" {
		target = "https://jobs.bytedance.com/campus/position/7628936427621927173/detail?spread=5YNTDRM"
	}
	result, err := agent.Crawl(context.Background(), Request{URL: target})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "agent-fallback" || result.Title == "" || !strings.Contains(result.Text, "岗位职责") || !strings.Contains(result.Text, "岗位要求与加分项") {
		t.Fatalf("incomplete fallback result: %#v", result)
	}
}
