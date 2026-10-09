package coach

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"offerpilot/backend/internal/chat"
)

func chatFingerprint(c Conversation, submission string) string {
	copy := c
	copy.Messages = []Message{}
	for _, m := range c.Messages {
		if m.SubmissionID == submission && m.Role == "assistant" {
			continue
		}
		copy.Messages = append(copy.Messages, m)
	}
	return fingerprint(copy)
}

// Generate runs on the actual HTTP chat path. Idempotent retries replay saved
// output; interrupted attempts require a new explicit retry, not hidden calls.
func (s *Service) Generate(ctx context.Context, in Input, onDelta func(chat.Delta) error) (Conversation, error) {
	ctx, release := s.runContext(ctx, in.ConversationID)
	defer release()
	if e := validInput(in); e != nil {
		return Conversation{}, e
	}
	if s.model == nil {
		return Conversation{}, fail("model_unavailable", "文本模型尚未配置", 503)
	}
	replayed := false
	var cached string
	c, e := s.update(ctx, in.ConversationID, func(c *Conversation) error {
		replayed = false
		inputHash := Hash(struct {
			QuestionID string
			Message    string
			References []string
			Corrects   string
			Fragments  []Fragment
		}{in.QuestionID, in.Message, append([]string{}, in.References...), in.Corrects, append([]Fragment{}, in.Fragments...)})
		for _, m := range c.Messages {
			if m.SubmissionID == in.SubmissionID && m.Role == "user" && m.InputHash != "" && m.InputHash != inputHash {
				return fail("submission_conflict", "相同提交ID不能更换引用或纠正对象", 409)
			}
			if m.SubmissionID == in.SubmissionID && m.Role == "user" && m.Content != in.Message {
				return fail("submission_conflict", "提交 ID 与原问题不同", 409)
			}
		}
		for _, m := range c.Messages {
			if m.SubmissionID == in.SubmissionID && m.Role == "assistant" {
				if m.Status == "complete" {
					cached = m.Content
					replayed = true
					return nil
				}
				if m.Status == "interrupted" || m.Status == "failed" {
					return fail("retry_required", "原请求已中断，原文保留；请使用重试操作发起新请求", 409)
				}
			}
		}
		if e := active(c); e != nil {
			return e
		}
		for _, m := range c.Messages {
			if m.SubmissionID == in.SubmissionID {
				return fail("retry_required", "原提交已保存，请恢复结果或用新的提交ID重试", 409)
			}
		}
		n, resolveErr := resolveQuestion(*c, in)
		if resolveErr != nil {
			return resolveErr
		}
		if c.Plan != nil {
			c.Plan.Discussion = n
		}
		if c.Plan != nil && !c.Plan.Taught[n] {
			return fail("answer_first", "请先独立作答，或明确选择先学习", 409)
		}
		if in.Corrects != "" {
			ok := false
			for _, m := range c.Messages {
				if m.ID == in.Corrects && !m.Deleted && m.Role == "user" {
					ok = true
				}
			}
			if !ok {
				if c.Plan != nil && c.Plan.PageIsolated {
					return fail("question_scope", "纠正对象不属于本题，请切换到对应题页再提问", 422)
				}
				return fail("reference_missing", "被纠正的用户原消息未找到", 404)
			}
		}
		ordinal := 0
		if c.Plan != nil {
			ordinal = n
		}
		kind := "chat"
		if c.Plan != nil {
			kind = "followup"
		}
		m := appendMessage(c, "user", kind, in.Message, "complete", in.SubmissionID, ordinal)
		c.Messages[len(c.Messages)-1].Corrects = in.Corrects
		c.Messages[len(c.Messages)-1].InputHash = inputHash
		if c.Title == "新对话" {
			r := []rune(m.Content)
			c.Title = string(r[:min(30, len(r))])
		}
		appendMessage(c, "assistant", "teaching", "", "streaming", in.SubmissionID, ordinal)
		c.Pending = &Lease{ID: in.SubmissionID, Until: time.Now().Add(3 * time.Minute)}
		c.Pending.Fingerprint = chatFingerprint(*c, in.SubmissionID)
		return nil
	})
	if e != nil {
		return c, e
	}
	if replayed {
		if onDelta != nil {
			e = onDelta(chat.Delta{Text: cached})
		}
		return c, e
	}
	started := time.Now()
	p, callErr := s.BuildContext(ctx, c.ID, in, "teaching")
	// Freeze only sources actually assembled for this page. Updating another
	// page, or an unused source on this page, is not a reason to reject output.
	if callErr == nil && c.Plan != nil && c.Plan.PageIsolated {
		_, callErr = s.update(ctx, c.ID, func(latest *Conversation) error {
			if latest.Pending == nil || latest.Pending.ID != in.SubmissionID || latest.Pending.Fingerprint != chatFingerprint(*latest, in.SubmissionID) {
				return ErrConflict
			}
			latest.Pending.SourceIDs = append([]string{}, p.Run.SourceIDs...)
			latest.Pending.Fingerprint = chatFingerprint(*latest, in.SubmissionID)
			return nil
		})
	}
	var out strings.Builder
	var usage chat.Usage
	checkpoint := time.Now()
	lastBytes := 0
	var recovery *Error
	if errors.As(callErr, &recovery) && (recovery.Code == "reference_ambiguous" || recovery.Code == "reference_missing" && len(in.References)+len(in.Fragments) == 0) {
		out.WriteString(recovery.Message)
		p.Run.Purpose = "clarification"
		callErr = nil
		if onDelta != nil {
			callErr = onDelta(chat.Delta{Text: out.String()})
		}
	}
	if callErr == nil && p.Run.Purpose != "clarification" {
		p.Run.ModelCalled = true
		usage, callErr = s.model.Stream(ctx, p.Run.Model, p.Messages, func(d chat.Delta) error {
			out.WriteString(d.Text)
			if out.Len()-lastBytes >= 512 || time.Since(checkpoint) > 2*time.Second {
				_, e := s.update(ctx, c.ID, func(latest *Conversation) error {
					if latest.Pending == nil || latest.Pending.ID != in.SubmissionID || latest.Pending.Fingerprint != chatFingerprint(*latest, in.SubmissionID) {
						return ErrConflict
					}
					for i := range latest.Messages {
						if latest.Messages[i].Role == "assistant" && latest.Messages[i].SubmissionID == in.SubmissionID {
							latest.Messages[i].Content = out.String()
						}
					}
					return nil
				})
				if e != nil {
					return e
				}
				checkpoint = time.Now()
				lastBytes = out.Len()
			}
			if onDelta != nil {
				return onDelta(d)
			}
			return ctx.Err()
		})
	}
	if callErr == nil && strings.TrimSpace(out.String()) == "" {
		callErr = fmt.Errorf("模型未返回公开回答，原问题已保存，请重试")
	}
	if callErr == nil && truncated(usage.FinishReason) {
		callErr = fmt.Errorf("回答被输出额度截断，已保存片段，可以引用继续提问")
	}
	p.Run.Usage = usage
	p.Run.BudgetViolation = usage.Known && usage.InputTokens > p.Run.Budget
	p.Run.Millis = time.Since(started).Milliseconds()
	if callErr != nil {
		p.Run.Error = callErr.Error()
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	stale := false
	c, e = s.update(writeCtx, c.ID, func(latest *Conversation) error {
		stale = false
		if latest.Pending == nil || latest.Pending.ID != in.SubmissionID || latest.Pending.Fingerprint != chatFingerprint(*latest, in.SubmissionID) {
			stale = true
			p.Run.Error = "来源或运行版本改变，模型结果拒绝"
			latest.Runs = append(latest.Runs, p.Run)
			return nil
		}
		latest.Pending = nil
		latest.Runs = append(latest.Runs, p.Run)
		for i := range latest.Messages {
			m := &latest.Messages[i]
			if m.Role == "assistant" && m.SubmissionID == in.SubmissionID {
				m.Content = out.String()
				m.Status = "complete"
				if callErr != nil {
					m.Status = "interrupted"
					if strings.TrimSpace(out.String()) == "" {
						m.Status = "failed"
					}
				}
				m.Revision++
			}
		}
		return nil
	})
	if e != nil {
		return c, e
	}
	if stale {
		return c, ErrConflict
	}
	return c, callErr
}
