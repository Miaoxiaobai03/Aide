// Local integration fixture ONLY. No dotenv, credentials, external model, or
// user database. The same product service/routes/storage run with fixed model
// responses; this command cannot establish real model quality or performance.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aide/backend/internal/chat"
	"aide/backend/internal/coach"
	"aide/backend/internal/httpapi"
	"aide/backend/internal/interview"
	"aide/backend/internal/knowledge"
)

type fixture struct{}

func (fixture) Stream(ctx context.Context, _ string, m []chat.Message, delta func(chat.Delta) error) (chat.Usage, error) {
	var data struct {
		Answer string `json:"student_answer"`
	}
	_ = json.Unmarshal([]byte(m[len(m)-1].Content), &data)
	text := "这是固定的集成测试教学回复，不能用于评价真实模型效果。"
	if strings.Contains(m[0].Content, "教学笔记") {
		text = `{"summary":"此前在讨论教学与恢复，尚未独立掌握。","quotes":[]}`
	}
	for _, x := range m {
		if strings.Contains(x.Content, "仅评价下列这次原始作答") {
			text = `{"correctness":3,"coverage":3,"explanation":3,"strengths":["描述了恢复"],"gaps":[{"point":"失败处理","quote":"失败应保留原始回答","reason":"未说明恢复步骤"}],"advice":"补充恢复步骤"}`
			var grade map[string]any
			_ = json.Unmarshal([]byte(text), &grade)
			grade["version"] = coach.GradingVersion
			grade["status"] = "assessed"
			grade["answerQuote"] = data.Answer
			if data.Answer == "FIXTURE_MISSING_ANSWER" {
				grade["correctness"] = 1
				grade["coverage"] = 1
				grade["explanation"] = 1
				grade["advice"] = "未提供本次作答原文，无法评估任何知识要点"
			}
			b, _ := json.Marshal(grade)
			text = string(b)
		}
	}
	if data.Answer == "FIXTURE_INVALID_GRADE" {
		text = "invalid-json"
	}
	if strings.Contains(m[0].Content, "同一知识能力") {
		text = `{"question":"如果请求断线，如何避免重放产生重复结果？","reference":"失败应保留原始回答，重试应使用提交ID去重，记录完整来源并检查状态。"}`
	}
	u := chat.Usage{Known: true, InputTokens: 100, OutputTokens: 50, FinishReason: "stop", Model: "fixture-model"}
	return u, delta(chat.Delta{Text: text})
}
func main() {
	temp, e := os.MkdirTemp("", "aide-coach-check-")
	if e != nil {
		panic(e)
	}
	repo, e := interview.OpenSQLiteStore(filepath.Join(temp, "check.db"))
	if e != nil {
		panic(e)
	}
	defer repo.Close()
	entries := []knowledge.Entry{}
	for i := 0; i < 12; i++ {
		entries = append(entries, knowledge.Entry{ID: fmt.Sprintf("fixture-q%d", i), Question: fmt.Sprintf("Q%d：如何设计可恢复的请求流程？", i), Source: "fixture.md", Excerpt: "失败应保留原始回答，重试应使用提交ID去重，记录完整来源并检查状态。"})
	}
	index := knowledge.NewIndex(entries)
	service := coach.New(repo, fixture{}, index, coach.ContextConfig{DefaultModel: "fixture-model"})
	api, e := httpapi.New(httpapi.Config{RequireAuth: true, APIKey: "fixture-only-key"}, httpapi.Dependencies{Interview: interview.NewService(interview.Dependencies{Store: repo}), Coach: service, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if e != nil {
		panic(e)
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		panic(e)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"url": "http://" + listener.Addr().String(), "fixture": true, "gradingVersion": coach.GradingVersion})
	server := &http.Server{Handler: api.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if e = server.Serve(listener); e != nil {
		panic(e)
	}
}
