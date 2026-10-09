package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeeGuardReservationsUsageAndEarlyStop(t *testing.T) {
	g := &feeGuard{LimitCNY: 0.1, Dir: t.TempDir()}
	request := []byte(`{"model":"deepseek-flash","max_tokens":4096,"messages":[]}`)
	ticket, e := g.begin(request)
	if e != nil || g.Calls != 1 || g.SpentUpperCNY <= 0 {
		t.Fatal("request was not reserved", e)
	}
	ticket.responseFile.Close()
	g.finish(ticket, 200, `data: {"usage":{"prompt_tokens":100,"completion_tokens":50,"prompt_cache_hit_tokens":20}}`)
	if g.SpentUpperCNY <= 0 || g.SpentUpperCNY > 0.001 || g.UnknownUsageCalls != 0 {
		t.Fatal("actual usage did not refund reservation", g)
	}
	before := g.SpentUpperCNY
	g.finish(ticket, 200, "")
	if g.SpentUpperCNY != before {
		t.Fatal("double finalization charged twice")
	}
	if _, e = g.begin([]byte(`{"model":"deepseek-flash","max_tokens":4096,"data":"` + strings.Repeat("长", 20000) + `"}`)); e == nil || g.Calls != 1 || g.StopReason != "fee_limit" {
		t.Fatal("overspending request admitted", e)
	}
	if _, e = g.begin(request); e == nil {
		t.Fatal("stopped run continued")
	}
	if _, e = os.Stat(filepath.Join(g.Dir, "0002.request.json")); !os.IsNotExist(e) {
		t.Fatal("blocked request journalled as sent")
	}
}

func TestFeeGuardUnknownUsageAndProviderFailure(t *testing.T) {
	g := &feeGuard{LimitCNY: 1, Dir: t.TempDir()}
	ticket, e := g.begin([]byte(`{"model":"deepseek-flash","max_tokens":4096}`))
	if e != nil {
		t.Fatal(e)
	}
	ticket.responseFile.Close()
	g.finish(ticket, 402, `{"error":{"message":"Insufficient Balance"}}`)
	if g.SpentUpperCNY != ticket.reserve || g.UnknownUsageCalls != 1 || g.StopReason != "provider_http_402" {
		t.Fatal("unknown cost fabricated or provider error not latched", g)
	}
	if _, e = g.begin([]byte(`{"model":"deepseek-flash","max_tokens":4096}`)); e == nil || g.Calls != 1 {
		t.Fatal("provider error flooded with new requests", e)
	}
}
