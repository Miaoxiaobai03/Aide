// Package coach owns durable chat/practice facts and derived context projections.
// It deliberately does not depend on the legacy forty-message session window.
package coach

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"offerpilot/backend/internal/chat"
)

const DocumentKind = "coach-conversation-v1"
const ProfileID = "local-default" // Installation-local, not multi-user authentication.
const RubricVersion = "knowledge-v1"

type Repository interface {
	TrainingDocuments(context.Context, string) (map[string]json.RawMessage, error)
	LoadTrainingDocument(context.Context, string, string) (json.RawMessage, int64, error)
	SaveTrainingDocument(context.Context, string, string, any, int64) error
}
type Model interface {
	Stream(context.Context, string, []chat.Message, func(chat.Delta) error) (chat.Usage, error)
}
type Error struct {
	Code, Message string
	Status        int
}

func (e *Error) Error() string                 { return e.Message }
func fail(code, text string, status int) error { return &Error{code, text, status} }

var ErrBudget = &Error{"context_budget", "必须保留的输入或历史超过预算；请缩短输入、取消引用或选择片段后重试，原文已保留", 413}
var ErrConflict = &Error{"conflict", "会话状态已变化，请刷新后重试", 409}

type Message struct {
	ID           string    `json:"id"`
	Seq          int       `json:"seq"`
	Revision     int       `json:"revision"`
	Role         string    `json:"role"`
	Kind         string    `json:"kind"`
	Content      string    `json:"content"`
	Status       string    `json:"status"`
	Ordinal      int       `json:"ordinal,omitempty"`
	QuestionID   string    `json:"questionId,omitempty"`
	SubmissionID string    `json:"submissionId,omitempty"`
	Corrects     string    `json:"corrects,omitempty"`
	InputHash    string    `json:"inputHash,omitempty"`
	Deleted      bool      `json:"deleted,omitempty"`
	At           time.Time `json:"at"`
}
type Question struct {
	ID                string `json:"id"`
	Text              string `json:"text"`
	Source            string `json:"source"`
	Hash              string `json:"hash"`
	Reference         string `json:"reference,omitempty"`         // Stored privately; Public() removes until evaluated/learned.
	ReferenceMarkdown string `json:"referenceMarkdown,omitempty"` // Frozen display formatting, with the same disclosure gate.
	ReferenceVersion  string `json:"referenceVersion"`
	ReferenceHash     string `json:"referenceHash"`
}
type Evidence struct {
	SourceID string `json:"sourceId"`
	Quote    string `json:"quote"`
	Hash     string `json:"hash"`
	Revision int    `json:"revision"`
}
type Gap struct {
	Point  string `json:"point"`
	Quote  string `json:"quote"`
	Reason string `json:"reason"`
}
type Evaluation struct {
	Version     string   `json:"version,omitempty"`
	Status      string   `json:"status,omitempty"`
	Method      string   `json:"method,omitempty"`
	AnswerQuote string   `json:"answerQuote,omitempty"`
	Correctness int      `json:"correctness"`
	Coverage    int      `json:"coverage"`
	Explanation int      `json:"explanation"`
	Strengths   []string `json:"strengths"`
	Gaps        []Gap    `json:"gaps"`
	Advice      string   `json:"advice"`
}
type Attempt struct {
	ID         string          `json:"id"`
	Ordinal    int             `json:"ordinal"`
	AnswerID   string          `json:"answerId"`
	Answer     string          `json:"answer"`
	Status     string          `json:"status"`
	Assisted   bool            `json:"assisted"`
	ReanswerOf string          `json:"reanswerOf,omitempty"`
	Evaluation *Evaluation     `json:"evaluation,omitempty"`
	Error      string          `json:"error,omitempty"`
	ErrorCode  string          `json:"errorCode,omitempty"`
	Revision   int             `json:"gradingRevision,omitempty"`
	History    []GradeRevision `json:"gradingHistory,omitempty"`
	At         time.Time       `json:"at"`
	Model      string          `json:"model,omitempty"`
}
type GradeRevision struct {
	Revision   int         `json:"revision"`
	Evaluation *Evaluation `json:"evaluation,omitempty"`
	Error      string      `json:"error,omitempty"`
	ErrorCode  string      `json:"errorCode,omitempty"`
	Model      string      `json:"model,omitempty"`
	At         time.Time   `json:"at"`
}
type Plan struct {
	PageIsolated       bool         `json:"pageIsolated,omitempty"`
	Questions          []Question   `json:"questions"`
	Current            int          `json:"current"`                     // zero-based, advanced only by Next
	Discussion         int          `json:"discussionOrdinal,omitempty"` // one-based; browsing never advances Current
	Status             string       `json:"status"`
	Taught             map[int]bool `json:"taught"`
	ViewedAnswers      map[int]bool `json:"viewedAnswers"`     // Explicit reveal; nil denotes legacy records needing conservative inference.
	Skipped            map[int]bool `json:"skipped,omitempty"` // Viewed then advanced without a valid evaluation; never a grade.
	Attempts           []Attempt    `json:"attempts"`
	SourceConversation string       `json:"sourceConversation,omitempty"`
}
type Summary struct {
	ID           string     `json:"id"`
	Model        string     `json:"model"`
	Template     string     `json:"template"`
	SourceIDs    []string   `json:"sourceIds"`
	SourceHashes []string   `json:"sourceHashes"`
	Text         string     `json:"text"`
	Quotes       []Evidence `json:"quotes"`
	At           time.Time  `json:"at"`
}
type Run struct {
	PolicyVersion   string     `json:"contextPolicyVersion,omitempty"`
	SubmissionID    string     `json:"submissionId,omitempty"`
	AnswerHash      string     `json:"answerHash,omitempty"`
	ReferenceHash   string     `json:"referenceHash,omitempty"`
	Ordinal         int        `json:"ordinal,omitempty"`
	GradingVersion  string     `json:"gradingVersion,omitempty"`
	ResponseHash    string     `json:"responseHash,omitempty"`
	ErrorCode       string     `json:"errorCode,omitempty"`
	ID              string     `json:"id"`
	Purpose         string     `json:"purpose"`
	Model           string     `json:"model"`
	Counter         string     `json:"counter"`
	WindowKnown     bool       `json:"windowKnown"`
	Budget          int        `json:"budget"`
	Before          int        `json:"before"`
	InputEstimate   int        `json:"inputEstimate"`
	BodyEstimate    int        `json:"bodyEstimate,omitempty"` // content only; not provider usage
	SourceIDs       []string   `json:"sourceIds"`
	SummaryIDs      []string   `json:"summaryIds"`
	Usage           chat.Usage `json:"usage"`
	Millis          int64      `json:"millis"`
	Error           string     `json:"error,omitempty"`
	BudgetViolation bool       `json:"budgetViolation,omitempty"`
	ModelCalled     bool       `json:"modelCalled"`
}
type Lease struct {
	SourceIDs   []string  `json:"sourceIds,omitempty"`
	ID          string    `json:"id"`
	Until       time.Time `json:"until"`
	Fingerprint string    `json:"fingerprint"`
}
type Conversation struct {
	PageStorage       bool               `json:"pageStorage,omitempty"`
	PracticeID        string             `json:"practiceId,omitempty"`
	PageOrdinal       int                `json:"pageOrdinal,omitempty"`
	ID                string             `json:"id"`
	Profile           string             `json:"profileId"`
	Mode              string             `json:"mode"`
	Title             string             `json:"title"`
	Version           int64              `json:"version"`
	Created           time.Time          `json:"createdAt"`
	Updated           time.Time          `json:"updatedAt"`
	Deleted           bool               `json:"deleted,omitempty"`
	TrainingRetained  bool               `json:"trainingRetained,omitempty"`
	Messages          []Message          `json:"messages"`
	Plan              *Plan              `json:"plan,omitempty"`
	Summaries         []Summary          `json:"summaries"`
	Runs              []Run              `json:"runs"`
	Pending           *Lease             `json:"pending,omitempty"`
	Review            *PracticeReview    `json:"review,omitempty"`
	QuestionGroups    []QuestionMemory   `json:"questionGroups,omitempty"`
	CompressionChecks []CompressionCheck `json:"compressionChecks,omitempty"`
}
type PracticeReview struct {
	Completed     int          `json:"completed"`
	Independent   int          `json:"independent"`
	Assisted      int          `json:"assisted"`
	Failed        int          `json:"failed"`
	Gaps          []ReviewGap  `json:"gaps"`
	ViewedAnswers int          `json:"viewedAnswers"`
	Skipped       int          `json:"skipped"`
	NeedsReview   []ReviewNeed `json:"needsReview"`
}
type ReviewNeed struct {
	Ordinal  int    `json:"ordinal"`
	Question string `json:"question"`
	Reason   string `json:"reason"`
}

func (c Conversation) AnswerViewed(ordinal int) bool {
	if c.Plan == nil {
		return false
	}
	if c.Plan.ViewedAnswers != nil {
		return c.Plan.ViewedAnswers[ordinal]
	}
	// Older versions used Taught for both revealing and grading. A successful
	// independent first answer distinguishes ordinary feedback from prior reveal.
	if !c.Plan.Taught[ordinal] {
		return false
	}
	for _, a := range c.Plan.Attempts {
		if a.Ordinal == ordinal && a.Status == "succeeded" && a.Evaluation != nil && !a.Assisted {
			return false
		}
	}
	return true
}

type ReviewGap struct {
	Ordinal  int    `json:"ordinal"`
	Point    string `json:"point"`
	Quote    string `json:"quote"`
	AnswerID string `json:"answerId"`
}
type Listing struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Mode      string    `json:"mode"`
	Updated   time.Time `json:"updatedAt"`
	Completed int       `json:"completed"`
	Target    int       `json:"target"`
}
type Input struct {
	QuestionID       string     `json:"questionId"`
	ConversationID   string     `json:"conversationId"`
	SubmissionID     string     `json:"submissionId"`
	Message          string     `json:"message"`
	Model            string     `json:"model"`
	References       []string   `json:"references"`
	Fragments        []Fragment `json:"fragments"`
	Corrects         string     `json:"corrects"`
	Reanswer         bool       `json:"reanswer"`
	Regrade          bool       `json:"regrade"`
	GradingRevision  int        `json:"gradingRevision"`
	RetryCompression bool       `json:"retryCompression"`
	assessmentBound  bool
}
type Fragment struct {
	MessageID string `json:"messageId"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Revision  int    `json:"revision"`
}

func id(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b[:])
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func appendMessage(c *Conversation, role, kind, text, status, submission string, ordinal int) Message {
	m := Message{ID: id("msg_"), Seq: len(c.Messages) + 1, Revision: 1, Role: role, Kind: kind, Content: text, Status: status, SubmissionID: submission, Ordinal: ordinal, At: time.Now().UTC()}
	if c.Plan != nil && ordinal > 0 && ordinal <= len(c.Plan.Questions) {
		m.QuestionID = c.Plan.Questions[ordinal-1].ID
	}
	c.Messages = append(c.Messages, m)
	return m
}
func (c Conversation) Completed() int {
	if c.Plan == nil {
		return 0
	}
	seen := map[int]bool{}
	for _, a := range c.Plan.Attempts {
		if a.Ordinal > 0 && a.Ordinal <= len(c.Plan.Questions) && validAttempt(a, c.Plan.Questions[a.Ordinal-1]) {
			seen[a.Ordinal] = true
		}
	}
	return len(seen)
}
func (c Conversation) Public() Conversation {
	b, _ := json.Marshal(c)
	var out Conversation
	_ = json.Unmarshal(b, &out)
	out.Pending = nil
	out.Runs = nil
	out.Summaries = nil
	out.CompressionChecks = nil
	if out.Plan != nil {
		// Reject known invalid historical observations on read, preserving their
		// stored data. Explicit regrading archives the old evaluation on write.
		for i := range out.Plan.Attempts {
			a := &out.Plan.Attempts[i]
			if a.Status == "succeeded" && (a.Ordinal < 1 || a.Ordinal > len(out.Plan.Questions) || !validAttempt(*a, out.Plan.Questions[a.Ordinal-1])) {
				a.Status = "failed"
				a.Evaluation = nil
				a.ErrorCode = "assessment_legacy_invalid"
				a.Error = "历史评分与作答证据矛盾，已排除统计；请重新评分原回答"
				if a.Ordinal == out.Plan.Current+1 && out.Plan.Status != "completed" {
					out.Plan.Status = "evaluation_failed"
				}
			}
		}
		for i := range out.Messages {
			m := &out.Messages[i]
			if m.Kind != "evaluation" {
				continue
			}
			for _, a := range out.Plan.Attempts {
				if a.ID == m.SubmissionID && (a.Status != "succeeded" || (a.Revision > 1 && m.Revision != a.Revision)) {
					m.Status = "superseded"
					break
				}
			}
		}
		if out.Plan.ViewedAnswers == nil {
			out.Plan.ViewedAnswers = map[int]bool{}
		}
		for i := range out.Plan.Questions {
			if c.AnswerViewed(i + 1) {
				out.Plan.ViewedAnswers[i+1] = true
			}
		}
		if out.Plan.Status == "completed" || out.TrainingRetained {
			out.Review = &PracticeReview{Completed: c.Completed(), Gaps: []ReviewGap{}}
			out.Review.NeedsReview = []ReviewNeed{}
			for i, q := range out.Plan.Questions {
				if c.AnswerViewed(i + 1) {
					out.Review.ViewedAnswers++
					out.Review.NeedsReview = append(out.Review.NeedsReview, ReviewNeed{i + 1, q.Text, "看过答案，待独立复测"})
				}
				if out.Plan.Skipped[i+1] {
					out.Review.Skipped++
				}
			}
			seen := map[string]bool{}
			reviewAttempts := out.Plan.Attempts
			if c.PageStorage || c.Plan.PageIsolated {
				latest := map[string]int{}
				for i, a := range reviewAttempts {
					if a.Ordinal < 1 || a.Ordinal > len(c.Plan.Questions) || !validAttempt(a, c.Plan.Questions[a.Ordinal-1]) {
						continue
					}
					latest[fmt.Sprintf("%d/%t", a.Ordinal, a.Assisted)] = i
				}
				filtered := []Attempt{}
				for i, a := range reviewAttempts {
					if a.Status != "succeeded" || latest[fmt.Sprintf("%d/%t", a.Ordinal, a.Assisted)] == i {
						filtered = append(filtered, a)
					}
				}
				reviewAttempts = filtered
			}
			for _, a := range reviewAttempts {
				if a.Status != "succeeded" || a.Evaluation == nil {
					out.Review.Failed++
					continue
				}
				if a.Assisted || !c.PageStorage && !c.Plan.PageIsolated && a.ReanswerOf != "" {
					out.Review.Assisted++
				} else {
					out.Review.Independent++
					for _, g := range a.Evaluation.Gaps {
						k := fmt.Sprint(a.Ordinal) + "/" + g.Point
						if !seen[k] {
							seen[k] = true
							out.Review.Gaps = append(out.Review.Gaps, ReviewGap{a.Ordinal, g.Point, g.Quote, a.AnswerID})
						}
					}
				}
			}
		}
		for i := range out.Plan.Questions {
			allowed := out.Plan.Taught[i+1]
			for _, a := range out.Plan.Attempts {
				if a.Ordinal == i+1 && a.Status == "succeeded" {
					allowed = true
				}
			}
			if !allowed {
				out.Plan.Questions[i].Reference = ""
				out.Plan.Questions[i].ReferenceMarkdown = ""
			}
		}
	}
	filtered := []Message{}
	for _, m := range out.Messages {
		if !m.Deleted {
			filtered = append(filtered, m)
		}
	}
	out.Messages = filtered
	return out
}

func RawHash(text string) string { h := sha256.Sum256([]byte(text)); return hex.EncodeToString(h[:]) }
func truncated(reason string) bool {
	return reason == "length" || reason == "max_tokens" || reason == "max_output_tokens"
}
func asError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{"storage_or_model", "保存或模型调用失败，请恢复会话后重试", 503}
}
