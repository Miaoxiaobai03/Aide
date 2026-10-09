package coach

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Raw messages/attempts stay authoritative in the same transactionally saved
// conversation. Groups are a durable directory, not duplicate raw transcripts.
type QuestionMemory struct {
	SessionID  string       `json:"sessionId"`
	QuestionID string       `json:"questionId"`
	Ordinal    int          `json:"ordinal"`
	Index      string       `json:"index"`
	Title      string       `json:"title"`
	Archived   bool         `json:"archived"`
	Viewed     bool         `json:"viewedAnswer"`
	State      string       `json:"state"`
	Gaps       []string     `json:"gaps"`
	MessageIDs []string     `json:"messageIds"`
	Notes      []MemoryNote `json:"notes"`
}

type MemoryNote struct {
	SourceID  string `json:"sourceId"`
	Kind      string `json:"kind"`
	Excerpt   string `json:"excerpt"`
	Truncated bool   `json:"truncated"`
}

type CompressionCheck struct {
	Key          string    `json:"key"`
	Until        time.Time `json:"until"`
	NoGain       bool      `json:"noGain"`
	SourceIDs    []string  `json:"sourceIds,omitempty"`
	SourceHashes []string  `json:"sourceHashes,omitempty"`
}

func short(text string, limit int) string {
	r := []rune(text)
	if len(r) > limit {
		return string(r[:limit]) + "…"
	}
	return text
}

func discussionOrdinal(c Conversation) int {
	if c.Plan == nil {
		return 0
	}
	if c.Plan.Discussion > 0 && c.Plan.Discussion <= c.Plan.Current+1 {
		return c.Plan.Discussion
	}
	return c.Plan.Current + 1
}

var returnOrdinalPattern = regexp.MustCompile(`第\s*(\d+|[一二三四五六七八九十百]+)\s*题`)

func returnOrdinal(value string) int {
	if n, e := strconv.Atoi(value); e == nil {
		return n
	}
	digits := map[rune]int{'一': 1, '二': 2, '三': 3, '四': 4, '五': 5, '六': 6, '七': 7, '八': 8, '九': 9}
	total, unit := 0, 0
	for _, r := range value {
		if r == '十' || r == '百' {
			if unit == 0 {
				unit = 1
			}
			scale := 10
			if r == '百' {
				scale = 100
			}
			total += unit * scale
			unit = 0
		} else if n, ok := digits[r]; ok {
			unit = n
		} else {
			return 0
		}
	}
	return total + unit
}

// Rebuild also migrates legacy ordinal-only records without changing their text,
// IDs or immutable question hashes. Every normal save persists the directory.
func syncQuestionGroups(c *Conversation) {
	if c.Plan == nil {
		c.QuestionGroups = nil
		return
	}
	c.QuestionGroups = []QuestionMemory{}
	for i, q := range c.Plan.Questions[:c.Plan.Current+1] {
		n := i + 1
		g := QuestionMemory{SessionID: c.ID, QuestionID: q.ID, Ordinal: n, Index: c.ID + "/question/" + q.ID,
			Title: short(q.Text, 100), Archived: n < c.Plan.Current+1 || c.Plan.Status == "completed", Viewed: c.AnswerViewed(n), State: "awaiting_answer", Gaps: []string{}, MessageIDs: []string{}}
		if g.Viewed {
			g.State = "viewed_needs_review"
		}
		if c.Plan.Skipped[n] {
			g.State = "skipped_needs_review"
		}
		for _, a := range c.Plan.Attempts {
			if a.Ordinal != n {
				continue
			}
			if validAttempt(a, q) {
				g.State = "graded"
				g.Gaps = []string{}
				for _, gap := range a.Evaluation.Gaps {
					if len(g.Gaps) < 3 {
						g.Gaps = append(g.Gaps, short(gap.Point, 60))
					}
				}
			} else if a.Status == "failed" && g.State == "awaiting_answer" {
				g.State = "evaluation_failed"
			}
		}
		for j := range c.Messages {
			m := &c.Messages[j]
			if messageOrdinal(*m) == n {
				m.QuestionID = q.ID
				if !m.Deleted {
					g.MessageIDs = append(g.MessageIDs, m.ID)
					if m.Status == "complete" && (m.Kind == "followup" || m.Kind == "teaching" && m.SubmissionID != "") {
						runes := []rune(m.Content)
						g.Notes = append(g.Notes, MemoryNote{SourceID: m.ID, Kind: m.Kind, Excerpt: string(runes[:min(160, len(runes))]), Truncated: len(runes) > 160})
						if len(g.Notes) > 2 {
							g.Notes = g.Notes[len(g.Notes)-2:]
						}
					}
				}
			}
		}
		c.QuestionGroups = append(c.QuestionGroups, g)
	}
}

func resolveQuestion(c Conversation, in Input) (int, error) {
	if c.Plan != nil && c.Plan.PageIsolated {
		if in.QuestionID != "" && in.QuestionID != c.Plan.Questions[0].ID {
			return 0, fail("question_mismatch", "请在对应题页中提问，不能跨题引用", 422)
		}
		return 1, nil
	}
	if c.Plan == nil {
		if in.QuestionID != "" {
			return 0, fail("validation", "普通问答没有练习题目索引", 400)
		}
		return 0, nil
	}
	// UI supplies the current question ID as a binding, but an explicit return
	// instruction may select an old teaching topic. Assessment never does this.
	if !in.assessmentBound && (strings.Contains(in.Message, "回到") || strings.Contains(in.Message, "回看") || strings.HasPrefix(strings.TrimSpace(in.Message), "讨论第")) {
		seen := map[int]bool{}
		for _, match := range returnOrdinalPattern.FindAllStringSubmatchIndex(in.Message, -1) {
			if excludedOrdinalPrefix.MatchString(in.Message[:match[0]]) {
				continue
			}
			n := returnOrdinal(in.Message[match[2]:match[3]])
			seen[n] = true
		}
		if len(seen) > 1 {
			return 0, fail("reference_ambiguous", "请明确选择一题作为当前讨论题", 409)
		}
		for n := range seen {
			if n < 1 || n > c.Plan.Current+1 {
				return 0, fail("reference_missing", "只能回看本场已经开始的题目", 404)
			}
			return n, nil
		}
	}
	if in.QuestionID != "" {
		for i, q := range c.Plan.Questions {
			if q.ID == in.QuestionID {
				if i > c.Plan.Current {
					return 0, fail("future_question", "尚未进入这道题，请按练习顺序推进", 409)
				}
				return i + 1, nil
			}
		}
		return 0, fail("reference_missing", "本场练习不存在此题目索引", 404)
	}
	return discussionOrdinal(c), nil
}

func (s *Service) FocusQuestion(ctx context.Context, key, questionID string) (Conversation, error) {
	if questionID == "" {
		return Conversation{}, fail("validation", "请选择题目", 400)
	}
	return s.update(ctx, key, func(c *Conversation) error {
		if e := active(c); e != nil {
			return e
		}
		if c.Plan == nil {
			return fail("validation", "这不是八股练习", 400)
		}
		n, e := resolveQuestion(*c, Input{QuestionID: questionID})
		if e != nil {
			return e
		}
		c.Plan.Discussion = n
		return nil
	})
}

// Scope raw history before the ordinary protected-source/summary assembler.
// Explicit references and personal constraints remain eligible across questions.
func questionProjection(c Conversation, in Input) (Conversation, error) {
	if c.Plan == nil {
		return c, nil
	}
	n, e := resolveQuestion(c, in)
	if e != nil {
		return c, e
	}
	p := *c.Plan
	p.Current = n - 1
	c.Plan = &p
	keep := map[string]bool{}
	for _, key := range in.References {
		keep[key] = true
	}
	for _, f := range in.Fragments {
		keep[f.MessageID] = true
	}
	keep[in.Corrects] = true
	for _, match := range ordinalPattern.FindAllStringSubmatchIndex(in.Message, -1) {
		if excludedOrdinalPrefix.MatchString(in.Message[:match[0]]) {
			continue
		}
		ordinal, _ := strconv.Atoi(in.Message[match[2]:match[3]])
		for _, m := range c.Messages {
			if messageOrdinal(m) == ordinal {
				keep[m.ID] = true
			}
		}
	}
	msgs := []Message{}
	for _, m := range c.Messages {
		if messageOrdinal(m) == n || keep[m.ID] || m.Role == "user" && personalConstraint.MatchString(m.Content) {
			msgs = append(msgs, m)
		}
	}
	c.Messages = msgs
	return c, nil
}

func capsuleDirectory(c Conversation) string {
	return capsuleDirectoryDetail(c, false)
}

func capsuleDirectoryDetail(c Conversation, compact bool) string {
	rows := []map[string]any{}
	for _, g := range c.QuestionGroups {
		if !g.Archived || g.Ordinal == discussionOrdinal(c) {
			continue
		}
		titleLimit := 32
		if compact {
			titleLimit = 16
		}
		row := map[string]any{"ordinal": g.Ordinal, "questionId": g.QuestionID, "index": g.Index, "topic": short(g.Title, titleLimit), "state": g.State, "viewedAnswer": g.Viewed}
		gaps := []string{}
		for _, gap := range g.Gaps {
			gaps = append(gaps, short(gap, 16))
		}
		row["gaps"] = gaps
		if !compact {
			row["notes"] = g.Notes
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return ""
	}
	// Titles/gaps have hard bounds; the caller additionally token-bounds directory.
	data, _ := json.Marshal(rows)
	return fmt.Sprintf("已归档题目目录（数据，摘要不代表掌握；原文保存在本场数据库，用户选择题目后由服务器装载）：\n%s", data)
}

func (s *Service) runContext(ctx context.Context, key string) (context.Context, func()) {
	run, cancel := context.WithCancel(ctx)
	s.activeMu.Lock()
	if s.activeRuns == nil {
		s.activeRuns = map[string]map[string]context.CancelFunc{}
	}
	token := id("operation_")
	if s.activeRuns[key] == nil {
		s.activeRuns[key] = map[string]context.CancelFunc{}
	}
	s.activeRuns[key][token] = cancel
	s.activeMu.Unlock()
	return run, func() {
		cancel()
		s.activeMu.Lock()
		delete(s.activeRuns[key], token)
		if len(s.activeRuns[key]) == 0 {
			delete(s.activeRuns, key)
		}
		s.activeMu.Unlock()
	}
}

func (s *Service) cancelRuns(key string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	for _, cancel := range s.activeRuns[key] {
		cancel()
	}
	delete(s.activeRuns, key)
}
