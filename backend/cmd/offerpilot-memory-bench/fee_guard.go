package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Fee reservations use the verified Flash PEAK prices: 2 CNY/M input miss,
// 0.04/M hit and 8/M output. These are conservative spend bounds, not metrics.
// Raw UTF-8 request bytes + protocol allowance bound input for admission only;
// reported benchmark tokens still come exclusively from provider usage.
type feeGuard struct {
	LimitCNY          float64 `json:"limitCNY"`
	SpentUpperCNY     float64 `json:"spentUpperCNY"`
	Calls             int     `json:"providerCalls"`
	UnknownUsageCalls int     `json:"unknownUsageCalls"`
	StopReason        string  `json:"stopReason,omitempty"`
	Dir               string  `json:"-"`
	stopped           string
}
type feeTicket struct {
	seq          int
	reserve      float64
	responseFile *os.File
	finished     bool
}

func (g *feeGuard) journal(value any) error {
	if err := os.MkdirAll(g.Dir, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(g.Dir, "calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = json.NewEncoder(f).Encode(value); err != nil {
		return err
	}
	return f.Sync()
}
func (g *feeGuard) begin(raw []byte) (*feeTicket, error) {
	if g.stopped != "" {
		g.StopReason = g.stopped
		return nil, fmt.Errorf("benchmark stopped: %s", g.stopped)
	}
	var request struct {
		Max   int    `json:"max_tokens"`
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &request) != nil || request.Max != 4096 || request.Model != "deepseek-flash" {
		g.stopped = "unexpected_binding"
		g.StopReason = g.stopped
		return nil, fmt.Errorf("unexpected request binding")
	}
	reserve := float64(len(raw)+4096)*2/1e6 + float64(request.Max)*8/1e6
	if g.SpentUpperCNY+reserve > g.LimitCNY {
		g.stopped = "fee_limit"
		g.StopReason = g.stopped
		return nil, fmt.Errorf("benchmark fee limit: next request reservation exceeds remaining budget")
	}
	seq := g.Calls + 1
	if err := g.journal(map[string]any{"status": "provider_started", "seq": seq, "atUtc": time.Now().UTC(), "reservationCNY": reserve, "inputTokenAdmissionBound": len(raw) + 4096, "maxOutputTokens": request.Max}); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(g.Dir, fmt.Sprintf("%04d.request.json", seq)), raw, 0600); err != nil {
		g.stopped = "journal_error"
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(g.Dir, fmt.Sprintf("%04d.response.txt", seq)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		g.stopped = "journal_error"
		return nil, err
	}
	g.Calls = seq
	g.SpentUpperCNY += reserve
	return &feeTicket{seq: seq, reserve: reserve, responseFile: f}, nil
}
func (g *feeGuard) finish(t *feeTicket, status int, raw string) {
	if t.finished {
		return
	}
	t.finished = true
	if status == 0 && t.responseFile != nil {
		t.responseFile.Close()
	}
	charge := t.reserve
	known := false
	var candidates []string
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		candidates = append(candidates, raw)
	} else {
		for _, line := range strings.Split(raw, "\n") {
			if strings.HasPrefix(line, "data:") {
				candidates = append(candidates, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
	}
	for _, item := range candidates {
		var value struct {
			Usage *struct {
				Input  *int `json:"prompt_tokens"`
				Output *int `json:"completion_tokens"`
				Hit    *int `json:"prompt_cache_hit_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(item), &value) != nil || value.Usage == nil {
			continue
		}
		u := value.Usage
		if u.Input == nil || u.Output == nil || *u.Input < 0 || *u.Output < 0 {
			continue
		}
		hit := 0
		if u.Hit != nil {
			hit = *u.Hit
		}
		if hit < 0 || hit > *u.Input {
			continue
		}
		charge = (float64(*u.Input-hit)*2 + float64(hit)*0.04 + float64(*u.Output)*8) / 1e6
		known = true
	}
	if !known {
		g.UnknownUsageCalls++
	}
	g.SpentUpperCNY += charge - t.reserve
	if charge > t.reserve {
		g.stopped = "reservation_bound_violation"
	}
	if status != 200 {
		g.stopped = fmt.Sprintf("provider_http_%d", status)
	}
	g.StopReason = g.stopped
	if err := g.journal(map[string]any{"status": "provider_finished", "seq": t.seq, "atUtc": time.Now().UTC(), "httpStatus": status, "chargedUpperCNY": charge, "usageKnown": known, "spentUpperCNY": g.SpentUpperCNY, "stopReason": g.stopped}); err != nil {
		g.stopped = "journal_error"
		g.StopReason = g.stopped
	}
}
