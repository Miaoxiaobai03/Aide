package interview

import (
	"context"
	"encoding/json"
	"fmt"
	"aide/backend/internal/gradecheck"
	"strings"
	"time"
)

// Read-only projection of coach facts. Keeping the schema here avoids coupling
// the interview orchestrator to the chat/summary/model service or calling a
// model while opening training history. Assistant chat and summaries are never
// sources of ability statistics.
type practiceTraining struct {
	PageStorage      bool      `json:"pageStorage"`
	ID               string    `json:"id"`
	Profile          string    `json:"profileId"`
	Deleted          bool      `json:"deleted"`
	TrainingRetained bool      `json:"trainingRetained"`
	Created          time.Time `json:"createdAt"`
	Plan             *struct {
		Status        string       `json:"status"`
		Taught        map[int]bool `json:"taught"`
		ViewedAnswers map[int]bool `json:"viewedAnswers"`
		Questions     []struct {
			Reference string `json:"reference"`
		} `json:"questions"`
		Attempts []struct {
			Ordinal    int    `json:"ordinal"`
			Answer     string `json:"answer"`
			Status     string `json:"status"`
			Assisted   bool   `json:"assisted"`
			ReanswerOf string `json:"reanswerOf"`
			Model      string `json:"model"`
			Evaluation *struct {
				Version     string   `json:"version"`
				Status      string   `json:"status"`
				AnswerQuote string   `json:"answerQuote"`
				Advice      string   `json:"advice"`
				Strengths   []string `json:"strengths"`
				Correctness int      `json:"correctness"`
				Coverage    int      `json:"coverage"`
				Explanation int      `json:"explanation"`
				Gaps        []struct {
					Quote  string `json:"quote"`
					Point  string `json:"point"`
					Reason string `json:"reason"`
				} `json:"gaps"`
			} `json:"evaluation"`
		} `json:"attempts"`
	} `json:"plan"`
}

func (s *Service) practiceRecords(ctx context.Context) ([]practiceTraining, error) {
	repo, e := s.trainingRepo()
	if e != nil {
		return nil, e
	}
	docs, e := repo.TrainingDocuments(ctx, "coach-conversation-v1")
	if e != nil {
		return nil, e
	}
	out := []practiceTraining{}
	for _, raw := range docs {
		var r practiceTraining
		if json.Unmarshal(raw, &r) != nil {
			return nil, unavailable("八股训练记录损坏", nil)
		}
		if r.Profile == LocalProfileID && (!r.Deleted || r.TrainingRetained) && r.Plan != nil {
			out = append(out, r)
		}
	}
	return out, nil
}
func validPracticeAttempt(r practiceTraining, index int) bool {
	a := r.Plan.Attempts[index]
	if a.Status != "succeeded" || a.Answer == "" || a.Evaluation == nil || a.Ordinal < 1 || a.Ordinal > len(r.Plan.Questions) {
		return false
	}
	for _, n := range []int{a.Evaluation.Correctness, a.Evaluation.Coverage, a.Evaluation.Explanation} {
		if n < 1 || n > 5 {
			return false
		}
	}
	if a.Evaluation.Version != "" && (a.Evaluation.Version != "knowledge-evidence-v2" || a.Evaluation.Status != "assessed" || strings.TrimSpace(a.Evaluation.AnswerQuote) == "" || !strings.Contains(a.Answer, a.Evaluation.AnswerQuote)) {
		return false
	}
	claim := a.Evaluation.Advice + " " + strings.Join(a.Evaluation.Strengths, " ")
	for _, g := range a.Evaluation.Gaps {
		if gradecheck.QuotedAnswerConflict(a.Answer, g.Reason) {
			return false
		}
		claim += " " + g.Point + " " + g.Reason
		if g.Quote == "" || !strings.Contains(r.Plan.Questions[a.Ordinal-1].Reference, g.Quote) {
			return false
		}
	}
	if gradecheck.MissingAnswerClaim(claim) {
		return false
	}
	return true
}
func practiceAnswerViewed(r practiceTraining, ordinal int) bool {
	if r.Plan.ViewedAnswers != nil {
		return r.Plan.ViewedAnswers[ordinal]
	}
	if !r.Plan.Taught[ordinal] {
		return false
	}
	for i, a := range r.Plan.Attempts {
		if a.Ordinal == ordinal && validPracticeAttempt(r, i) && !a.Assisted {
			return false
		}
	}
	return true
}
func (s *Service) practiceHistory(ctx context.Context) ([]HistoryItem, error) {
	records, e := s.practiceRecords(ctx)
	if e != nil {
		return nil, e
	}
	out := []HistoryItem{}
	for _, r := range records {
		state := "awaiting_answer"
		report := "not_ready"
		if r.Plan.Status == "completed" {
			state = "completed"
			report = "available"
		}
		item := HistoryItem{ID: r.ID, Type: "practice", State: state, StartedAt: r.Created, Answered: 0, Area: "knowledge", ScoringVersion: "knowledge-v1", ReportStatus: report}
		for i := range r.Plan.Questions {
			if practiceAnswerViewed(r, i+1) {
				item.NeedsReview++
			}
		}
		answered, evaluated := map[int]bool{}, map[int]bool{}
		for i, a := range r.Plan.Attempts {
			answered[a.Ordinal] = true
			if validPracticeAttempt(r, i) {
				evaluated[a.Ordinal] = true
			}
		}
		item.Answered = len(answered)
		item.Evaluated = len(evaluated)
		out = append(out, item)
	}
	return out, nil
}
func (s *Service) appendPracticeStats(ctx context.Context, stats *TrainingStats) error {
	records, e := s.practiceRecords(ctx)
	if e != nil {
		return e
	}
	groups := map[string]*ScoreGroup{}
	for _, r := range records {
		stats.Sessions++
		stats.Areas["knowledge"]++
		for i := range r.Plan.Questions {
			if practiceAnswerViewed(r, i+1) {
				stats.PracticeNeedsReview++
			}
		}
		if r.Plan.Status == "completed" {
			stats.Completed++
		}
		answered, evaluated := map[int]bool{}, map[int]bool{}
		latest := map[string]int{}
		for i, a := range r.Plan.Attempts {
			answered[a.Ordinal] = true
			if validPracticeAttempt(r, i) {
				evaluated[a.Ordinal] = true
				kind := "independent"
				if a.Assisted || !r.PageStorage && a.ReanswerOf != "" {
					kind = "assisted"
				}
				latest[fmt.Sprint(a.Ordinal)+"/"+kind] = i
			} else if a.Status == "failed" {
				stats.RetestFailures++
			}
		}
		stats.Answered += len(answered)
		stats.Evaluated += len(evaluated)
		for _, i := range latest {
			a := r.Plan.Attempts[i]
			if !validPracticeAttempt(r, i) {
				if a.Status == "failed" {
					stats.RetestFailures++
				}
				continue
			}
			key := "knowledge-v1/knowledge/unspecified/independent"
			if a.Assisted || !r.PageStorage && a.ReanswerOf != "" {
				key = "knowledge-v1/knowledge/unspecified/assisted"
			}
			if a.Model != "" {
				key += "/" + a.Model
			}
			g := groups[key]
			if g == nil {
				g = &ScoreGroup{Key: key, Version: "knowledge-v1", Kind: QuestionKnowledge, Difficulty: Difficulty("unspecified"), Scores: map[string]float64{}}
				groups[key] = g
			}
			g.Count++
			g.Scores["correctness"] += float64(a.Evaluation.Correctness)
			g.Scores["coverage"] += float64(a.Evaluation.Coverage)
			g.Scores["explanation"] += float64(a.Evaluation.Explanation)
		}
	}
	for _, g := range groups {
		for k, v := range g.Scores {
			g.Scores[k] = v / float64(g.Count)
		}
		stats.Groups = append(stats.Groups, *g)
	}
	return nil
}
