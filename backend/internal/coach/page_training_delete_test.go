package coach

import (
	"errors"
	"offerpilot/backend/internal/chat"
	"offerpilot/backend/internal/interview"
	"strings"
	"testing"
)

func TestPagedStatsLatestIndependentAndRetestDelete(t *testing.T) {
	s, db, parent := pagedFixture(t, nil)
	v, _ := s.Page(t.Context(), parent.ID, "", true)
	q := parent.Plan.Questions[0]
	for _, submission := range []string{"first", "second"} {
		_, e := s.Answer(t.Context(), Input{ConversationID: v.Conversation.ID, QuestionID: q.ID, SubmissionID: submission, Message: q.Reference, Reanswer: submission == "second"})
		if e != nil {
			t.Fatal(e)
		}
	}
	training := interview.NewService(interview.Dependencies{Store: db})
	stats, e := training.TrainingStats(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if stats.Answered != 1 || stats.Evaluated != 1 || len(stats.Groups) != 1 || !strings.Contains(stats.Groups[0].Key, "/independent") || stats.Groups[0].Count != 1 {
		t.Fatalf("independent reanswer counted wrongly: %+v", stats)
	}
	s.Learn(t.Context(), v.Conversation.ID, 1)
	_, e = s.Answer(t.Context(), Input{ConversationID: v.Conversation.ID, QuestionID: q.ID, SubmissionID: "assisted", Message: q.Reference, Reanswer: true})
	if e != nil {
		t.Fatal(e)
	}
	ret, e := s.Retest(t.Context(), parent.ID, "", true)
	if e != nil {
		t.Fatal(e)
	}
	retPage, e := s.Page(t.Context(), ret.ID, "", true)
	if e != nil {
		t.Fatal(e)
	}
	privateRet, loadErr := s.Load(t.Context(), retPage.Conversation.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	snapshot := privateRet.Plan.Questions[0]
	if _, e = s.Answer(t.Context(), Input{ConversationID: retPage.Conversation.ID, QuestionID: snapshot.ID, SubmissionID: "ret", Message: snapshot.Reference}); e != nil {
		t.Fatal(e)
	}
	info, _ := s.Directory(t.Context(), parent.ID)
	ended, e := s.EndPages(t.Context(), parent.ID, info.Version)
	if e != nil {
		t.Fatal(e)
	}
	review, e := s.Training(t.Context(), parent.ID)
	if e != nil {
		t.Fatal(e)
	}
	public := s.Public(review)
	if public.Review.Independent != 1 || public.Review.Assisted != 1 || public.Review.Completed != 1 {
		t.Fatalf("review duplicate contributions: %+v", public.Review)
	}
	if e = s.DeletePages(t.Context(), parent.ID, ended.Practice.Version); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Directory(t.Context(), parent.ID); e == nil {
		t.Fatal("deleted directory visible")
	}
	if _, e = s.Page(t.Context(), parent.ID, q.ID, false); e == nil {
		t.Fatal("deleted reference retrievable")
	}
	kept, e := s.Training(t.Context(), ret.ID)
	if e != nil || kept.Plan.SourceConversation != "" || kept.Plan.Questions[0].Reference != snapshot.Reference || len(kept.Plan.Attempts) != 1 {
		t.Fatal("independent retest lost", e)
	}
	stats, e = training.TrainingStats(t.Context())
	if e != nil || stats.Sessions != 1 || stats.Evaluated != 1 {
		t.Fatal("deleted practice still contributed", e, stats)
	}
	history, e := training.History(t.Context(), interview.HistoryQuery{})
	if e != nil || len(history.Items) != 1 || history.Items[0].ID != ret.ID {
		t.Fatal("deleted practice in history", e)
	}
}
func TestPageDeleteDuringGradeCannotResurrect(t *testing.T) {
	m := &fixtureModel{}
	s, db, parent := pagedFixture(t, m)
	v, _ := s.Page(t.Context(), parent.ID, "", true)
	m.fn = func([]chat.Message) (string, error) {
		info, _ := db.PracticeInfo(t.Context(), parent.ID)
		if e := s.DeletePages(t.Context(), parent.ID, info.Version); e != nil {
			t.Fatal(e)
		}
		return validGrade, nil
	}
	if _, e := s.Answer(t.Context(), Input{ConversationID: v.Conversation.ID, QuestionID: parent.Plan.Questions[0].ID, SubmissionID: "late-grade", Message: "用户原答"}); e == nil {
		t.Fatal("deleted grade accepted")
	}
	if _, e := db.PracticeInfo(t.Context(), parent.ID); !errors.Is(e, interview.ErrStoreNotFound) {
		t.Fatal("grade resurrected parent", e)
	}
}
