package interview

import (
	"context"
	"encoding/json"
	"time"
)

type WeaknessSource struct {
	InterviewID string        `json:"interviewId"`
	QuestionID  string        `json:"questionId"`
	Question    string        `json:"question"`
	Answer      string        `json:"answer"`
	Feedback    string        `json:"feedback"`
	Evidence    []EvidenceRef `json:"evidence"`
	At          time.Time     `json:"at"`
}
type Weakness struct {
	UpdateSequence int64            `json:"updateSequence"`
	ID             string           `json:"id"`
	Label          string           `json:"label"`
	Scope          string           `json:"scope"`
	Issue          string           `json:"issue"`
	Status         string           `json:"status"`
	Criteria       []string         `json:"criteria"`
	Sources        []WeaknessSource `json:"sources"`
	Revision       int64            `json:"revision"`
	UpdatedAt      time.Time        `json:"updatedAt"`
}
type WeaknessRevision struct {
	Sequence int64     `json:"sequence"`
	Label    string    `json:"label"`
	Criteria []string  `json:"criteria"`
	Status   string    `json:"status"`
	At       time.Time `json:"at"`
}
type weaknessEdit struct {
	Current WeaknessRevision   `json:"current"`
	History []WeaknessRevision `json:"history"`
}
type WeaknessPage struct {
	Items  []Weakness `json:"items"`
	State  string     `json:"state"`
	Reason string     `json:"reason,omitempty"`
}
type RetestTarget struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Criteria []string `json:"criteria"`
}
type RetestTask struct {
	ID                 string         `json:"id"`
	Targets            []RetestTarget `json:"targets"`
	OriginalQuestionID string         `json:"originalQuestionId"`
	Question           Question       `json:"question"`
	Status             string         `json:"status"`
	Error              string         `json:"error,omitempty"`
	Batch              int            `json:"batch"`
	CriteriaVersion    string         `json:"criteriaVersion"`
	BindingReason      string         `json:"bindingReason,omitempty"`
}
type CriterionResult struct {
	TargetID    string `json:"targetId"`
	Criterion   string `json:"criterion"`
	Status      string `json:"status"` // met, unmet, unassessed
	Reason      string `json:"reason"`
	AnswerQuote string `json:"answerQuote"`
}
type RetestEvaluation struct {
	Assessment Assessment        `json:"assessment"`
	Results    []CriterionResult `json:"results"`
}
type RetestAttempt struct {
	Sequence   int64             `json:"sequence"`
	ID         string            `json:"id"`
	TaskID     string            `json:"taskId"`
	Answer     AnswerPayload     `json:"answer"`
	Status     string            `json:"status"`
	Error      string            `json:"error,omitempty"`
	CreatedAt  time.Time         `json:"createdAt"`
	LeaseUntil time.Time         `json:"leaseUntil"`
	Retries    int               `json:"retries"`
	Evaluation *RetestEvaluation `json:"evaluation,omitempty"`
}
type RetestPlan struct {
	ID              string          `json:"id"`
	ProfileID       string          `json:"profileId"`
	SourceID        string          `json:"sourceId"`
	Mode            string          `json:"mode"`
	CreatedAt       time.Time       `json:"createdAt"`
	Status          string          `json:"status"`
	Error           string          `json:"error,omitempty"`
	Version         int64           `json:"version"`
	MaterialVersion string          `json:"materialVersion"`
	ScoringVersion  string          `json:"scoringVersion"`
	FrozenSource    json.RawMessage `json:"frozenSource,omitempty"`
	Tasks           []RetestTask    `json:"tasks"`
	Attempts        []RetestAttempt `json:"attempts"`
	LeaseUntil      time.Time       `json:"leaseUntil"`
}
type RetestQuestionRequest struct {
	Original Question           `json:"original"`
	Targets  []RetestTarget     `json:"targets"`
	Anchors  []SourceAnchor     `json:"anchors"`
	Repair   *RepairInstruction `json:"repair,omitempty"`
}
type RetestQuestionDraft struct {
	Question      QuestionDraft `json:"question"`
	Difficulty    Difficulty    `json:"difficulty"`
	TargetIDs     []string      `json:"targetIds"`
	BindingReason string        `json:"bindingReason"`
	Answerable    bool          `json:"answerable"`
}
type RetestAssessmentRequest struct {
	Question Question           `json:"question"`
	Targets  []RetestTarget     `json:"targets"`
	Answer   AnswerPayload      `json:"answer"`
	Anchors  []SourceAnchor     `json:"anchors"`
	Repair   *RepairInstruction `json:"repair,omitempty"`
}
type RetestAgent interface {
	ValidateRetest(context.Context, RetestAssessmentRequest) (RetestValidation, error)
	GenerateRetest(context.Context, RetestQuestionRequest) (RetestQuestionDraft, error)
	AssessRetest(context.Context, RetestAssessmentRequest) (RetestEvaluation, error)
}

type RetestValidation struct {
	Valid        bool          `json:"valid"`
	TargetIDs    []string      `json:"targetIds"`
	Reason       string        `json:"reason"`
	EvidenceRefs []EvidenceRef `json:"evidenceRefs"`
}
type RetestMutation struct {
	Action    string        `json:"action"`
	IDs       []string      `json:"ids"`
	SourceID  string        `json:"sourceId"`
	ID        string        `json:"id"`
	Label     string        `json:"label"`
	Criteria  []string      `json:"criteria"`
	Status    string        `json:"status"`
	Revision  int64         `json:"revision"`
	Mode      string        `json:"mode"`
	PlanID    string        `json:"planId"`
	TaskID    string        `json:"taskId"`
	AttemptID string        `json:"attemptId"`
	Answer    AnswerPayload `json:"answer"`
	Batch     int           `json:"batch"`
	Retry     bool          `json:"retry"`
}
type AttemptObservation struct {
	PlanID          string        `json:"planId"`
	Task            RetestTask    `json:"task"`
	Attempt         RetestAttempt `json:"attempt"`
	MaterialVersion string        `json:"materialVersion"`
	ScoringVersion  string        `json:"scoringVersion"`
}
type AttemptComparison struct {
	Left            AttemptObservation `json:"left"`
	Right           AttemptObservation `json:"right"`
	ScoreComparable bool               `json:"scoreComparable"`
	ScoreDelta      *int               `json:"scoreDelta,omitempty"`
	Reason          string             `json:"reason"`
}
