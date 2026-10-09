package coach

import (
	"context"
	"testing"

	"aide/backend/internal/interview"
)

func TestViewedAnswerAdvancePersistenceAndIndependentRetest(t *testing.T) {
	s, _, path := setup(t)
	ctx := context.Background()
	c, err := s.StartPractice(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Next(ctx, c.ID, 1, false); err == nil {
		t.Fatal("unanswered/unviewed advanced")
	}
	c, err = s.Learn(ctx, c.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	messages := len(c.Messages)
	c, err = s.Learn(ctx, c.ID, 1)
	if err != nil || len(c.Messages) != messages || !c.AnswerViewed(1) {
		t.Fatal("reveal not idempotent", err)
	}
	c, err = s.Next(ctx, c.ID, 1, false)
	if err != nil || c.Plan.Current != 1 || !c.Plan.Skipped[1] || c.Completed() != 0 || len(c.Plan.Attempts) != 0 {
		t.Fatal("viewed advance fabricated a score", err)
	}
	c, err = s.Next(ctx, c.ID, 1, false)
	if err != nil || c.Plan.Current != 1 {
		t.Fatal("advance replayed twice", err)
	}
	if _, err = s.Next(ctx, c.ID, 2, true); err == nil {
		t.Fatal("unanswered last question finished")
	}
	c, err = s.Learn(ctx, c.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.Answer(ctx, Input{ConversationID: c.ID, SubmissionID: "viewed-own-answer", Message: "我用自己的话作答"})
	if err != nil || !c.Plan.Attempts[0].Assisted {
		t.Fatal("viewed answer scored as independent", err)
	}
	c, err = s.Next(ctx, c.ID, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	public := c.Public()
	if public.Review.ViewedAnswers != 2 || public.Review.Skipped != 1 || len(public.Review.NeedsReview) != 2 || public.Review.Independent != 0 || public.Review.Completed != 1 {
		t.Fatalf("incorrect review: %+v", public.Review)
	}
	repo, err := interview.OpenSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	reopened := New(repo, s.model, s.knowledge, s.config)
	c, err = reopened.Load(ctx, c.ID)
	if err != nil || !c.AnswerViewed(1) || !c.AnswerViewed(2) {
		t.Fatal("reveal flags lost on reopen", err)
	}
	retest, err := reopened.Retest(ctx, c.ID, "", true)
	if err != nil || len(retest.Plan.Questions) != 2 || retest.Plan.SourceConversation != c.ID {
		t.Fatal("viewed questions omitted from retest", err)
	}
	if retest.AnswerViewed(1) || retest.Plan.Taught[1] || retest.Public().Plan.Questions[0].Reference != "" {
		t.Fatal("new retest leaked or inherited reveal flag")
	}
	for i := range c.Plan.Questions {
		if retest.Plan.Questions[i].Reference != c.Plan.Questions[i].Reference {
			t.Fatal("retest reference changed")
		}
	}
}

func TestNormalFeedbackIsNotViewedAnswer(t *testing.T) {
	s, _, _ := setup(t)
	ctx := context.Background()
	c, _ := s.StartPractice(ctx, 1)
	c, err := s.Answer(ctx, Input{ConversationID: c.ID, SubmissionID: "independent-normal", Message: "独立回答"})
	if err != nil || c.AnswerViewed(1) || c.Public().Plan.ViewedAnswers[1] {
		t.Fatal("ordinary feedback marked as reveal", err)
	}
	// Preserve compatibility with old records where no separate field existed.
	c.Plan.ViewedAnswers = nil
	if c.AnswerViewed(1) {
		t.Fatal("legacy independent feedback misclassified")
	}
	c.Plan.Attempts = nil
	if !c.AnswerViewed(1) {
		t.Fatal("legacy explicit-learning state lost")
	}
}
