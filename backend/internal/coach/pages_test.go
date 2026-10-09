package coach

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"aide/backend/internal/chat"
	"aide/backend/internal/interview"
	"aide/backend/internal/knowledge"
)

func pagedFixture(t *testing.T, model Model) (*Service, *interview.SQLiteStore, Conversation) {
	t.Helper()
	db, err := interview.OpenSQLiteStore(filepath.Join(t.TempDir(), "pages.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := New(db, model, nil, ContextConfig{InputCap: 30000})
	qs := []Question{}
	for _, name := range []string{"第一题专属资料", "第二题独立资料", "第三题独立资料"} {
		q := Question{ID: name, Text: name + "的原理是什么？", Reference: strings.Repeat(name+"：这份原始参考必须完整保存。", 8)}
		q.ReferenceHash = RawHash(q.Reference)
		q.ReferenceVersion = "frozen"
		q.Hash = Hash(q)
		qs = append(qs, q)
	}
	c, err := s.createPlanStorage(t.Context(), qs, "", true)
	if err != nil {
		t.Fatal(err)
	}
	return s, db, c
}
func TestPracticePagesIndependentStorageAndAnswers(t *testing.T) {
	s, db, parent := pagedFixture(t, nil)
	first, err := s.Page(t.Context(), parent.ID, parent.Plan.Questions[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Page(t.Context(), parent.ID, parent.Plan.Questions[1].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Conversation.Plan.Questions) != 1 || len(second.Conversation.Messages) != 1 {
		t.Fatal("page contains another question")
	}
	_, versionBefore, err := db.PracticePage(t.Context(), parent.ID, parent.Plan.Questions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.Answer(t.Context(), Input{ConversationID: second.Conversation.ID, QuestionID: parent.Plan.Questions[1].ID, SubmissionID: "answer-second", Message: parent.Plan.Questions[1].Reference})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Plan.Attempts) != 1 || c.Plan.Attempts[0].Status != "succeeded" || c.Plan.Attempts[0].Evaluation.Correctness != 5 {
		t.Fatalf("reference answer not graded: %+v", c.Plan.Attempts)
	}
	_, versionAfter, _ := db.PracticePage(t.Context(), parent.ID, parent.Plan.Questions[0].ID)
	if versionAfter != versionBefore {
		t.Fatal("other page version changed")
	}
	firstRestored, err := s.Page(t.Context(), parent.ID, parent.Plan.Questions[0].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstRestored.Conversation.Messages) != len(first.Conversation.Messages) {
		t.Fatal("answer saved to wrong page")
	}
	c, err = s.Answer(t.Context(), Input{ConversationID: c.ID, QuestionID: parent.Plan.Questions[1].ID, SubmissionID: "answer-second-again", Message: parent.Plan.Questions[1].Reference, Reanswer: true})
	if err != nil {
		t.Fatal(err)
	}
	if c.Plan.Attempts[1].Assisted {
		t.Fatal("ordinary feedback falsely marked answer viewed")
	}
	info, err := db.PracticeInfo(t.Context(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info.Completed != 1 {
		t.Fatalf("duplicate submissions counted as questions: %d", info.Completed)
	}
	_, err = s.Learn(t.Context(), c.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	c, err = s.Answer(t.Context(), Input{ConversationID: c.ID, QuestionID: parent.Plan.Questions[1].ID, SubmissionID: "after-view", Message: parent.Plan.Questions[1].Reference, Reanswer: true})
	if err != nil {
		t.Fatal(err)
	}
	if !c.Plan.Attempts[2].Assisted || !c.AnswerViewed(1) {
		t.Fatal("viewed answer event lost")
	}
	if _, err = s.EndPages(t.Context(), parent.ID, info.Version); err != nil {
		t.Fatal(err)
	}
	third, err := s.Page(t.Context(), parent.ID, parent.Plan.Questions[2].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !third.Practice.Ended || third.Practice.Completed != 1 {
		t.Fatal("ended is confused with scored")
	}
	// Ended practices remain readable and allow explicit per-question work.
	_, err = s.Answer(t.Context(), Input{ConversationID: third.Conversation.ID, QuestionID: parent.Plan.Questions[2].ID, SubmissionID: "after-end", Message: parent.Plan.Questions[2].Reference})
	if err != nil {
		t.Fatal(err)
	}
}

type pageWaitingModel struct {
	start    chan struct{}
	release  chan struct{}
	once     sync.Once
	captured []chat.Message
}

func (m *pageWaitingModel) Stream(ctx context.Context, _ string, msgs []chat.Message, delta func(chat.Delta) error) (chat.Usage, error) {
	m.captured = msgs
	m.once.Do(func() { close(m.start) })
	select {
	case <-ctx.Done():
		return chat.Usage{}, ctx.Err()
	case <-m.release:
	}
	return chat.Usage{}, delta(chat.Delta{Text: "本题教学回复完整保留"})
}
func TestPracticePagesNavigationDuringGenerationAndDelete(t *testing.T) {
	model := &pageWaitingModel{start: make(chan struct{}), release: make(chan struct{})}
	s, db, parent := pagedFixture(t, model)
	first, err := s.Page(t.Context(), parent.ID, parent.Plan.Questions[0].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Learn(t.Context(), first.Conversation.ID, 1); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := s.Generate(t.Context(), Input{ConversationID: first.Conversation.ID, QuestionID: parent.Plan.Questions[0].ID, SubmissionID: "teaching-first", Message: "进一步解释这个原理"}, nil)
		finished <- err
	}()
	<-model.start
	second, err := s.Page(t.Context(), parent.ID, parent.Plan.Questions[1].ID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range model.captured {
		if strings.Contains(msg.Content, "第二题独立资料") || strings.Contains(msg.Content, "第三题独立资料") {
			t.Fatal("other page entered model context")
		}
	}
	_, err = s.Answer(t.Context(), Input{ConversationID: second.Conversation.ID, QuestionID: parent.Plan.Questions[1].ID, SubmissionID: "concurrent", Message: parent.Plan.Questions[1].Reference})
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "operation_busy" {
		t.Fatalf("same practice concurrency not controlled: %v", err)
	}
	close(model.release)
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	first, err = s.Page(t.Context(), parent.ID, parent.Plan.Questions[0].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Conversation.Messages[len(first.Conversation.Messages)-1].Content != "本题教学回复完整保留" {
		t.Fatal("late answer not saved to original page")
	}
	second, err = s.Page(t.Context(), parent.ID, parent.Plan.Questions[1].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Conversation.Messages) != 1 {
		t.Fatal("late answer polluted next page")
	}
	info, _ := db.PracticeInfo(t.Context(), parent.ID)
	if err = s.DeletePages(t.Context(), parent.ID, info.Version); err != nil {
		t.Fatal(err)
	}
	for _, q := range parent.Plan.Questions {
		if _, _, err = db.PracticePage(t.Context(), parent.ID, q.ID); !errors.Is(err, interview.ErrStoreNotFound) {
			t.Fatal("deleted page remains")
		}
	}
	if err = s.DeletePages(t.Context(), parent.ID, info.Version); err != nil {
		t.Fatal("delete is not idempotent")
	}
	if err = s.save(t.Context(), &first.Conversation); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted page resurrected: %v", err)
	}
}
func TestPracticePagesDeleteCancelsInFlight(t *testing.T) {
	model := &pageWaitingModel{start: make(chan struct{}), release: make(chan struct{})}
	s, db, parent := pagedFixture(t, model)
	view, err := s.Page(t.Context(), parent.ID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Learn(t.Context(), view.Conversation.ID, 1); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := s.Generate(t.Context(), Input{ConversationID: view.Conversation.ID, QuestionID: parent.Plan.Questions[0].ID, SubmissionID: "delete-running", Message: "讲解"}, nil)
		finished <- err
	}()
	<-model.start
	info, _ := db.PracticeInfo(t.Context(), parent.ID)
	if err = s.DeletePages(t.Context(), parent.ID, info.Version); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err == nil {
		t.Fatal("deleted generation succeeded")
	}
	if _, err = db.PracticeInfo(t.Context(), parent.ID); !errors.Is(err, interview.ErrStoreNotFound) {
		t.Fatal("deleted practice reappeared")
	}
}

func TestPracticePageCreationReplayAndInvalidTargets(t *testing.T) {
	s, db, _ := pagedFixture(t, nil)
	s.knowledge = knowledge.NewIndex([]knowledge.Entry{
		{ID: "fresh1", Question: "第一题的独立题目", Excerpt: strings.Repeat("第一题完整参考独立保存。", 5)},
		{ID: "fresh2", Question: "第二题的独立题目", Excerpt: strings.Repeat("第二题完整参考独立保存。", 5)},
	})
	parent, err := s.StartPracticePagesWithID(t.Context(), 2, "fixed-create")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.StartPracticePagesWithID(t.Context(), 2, "fixed-create")
	if err != nil || replay.ID != parent.ID {
		t.Fatal("creation replay changed practice", err)
	}
	if _, err = s.StartPracticePagesWithID(t.Context(), 1, "fixed-create"); err == nil {
		t.Fatal("creation ID accepted different count")
	}
	dir, err := s.Directory(t.Context(), parent.ID)
	if err != nil || len(dir.Pages) != 2 || dir.Completed != 0 {
		t.Fatal("directory", err)
	}
	first, err := s.Page(t.Context(), parent.ID, dir.Pages[0].QuestionID, true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Page(t.Context(), parent.ID, dir.Pages[1].QuestionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if first.Conversation.ID == second.Conversation.ID || len(second.Conversation.Messages) != 1 || second.Conversation.Plan.Questions[0].Reference != "" {
		t.Fatal("page data or private reference leaked")
	}
	if _, err = s.Page(t.Context(), parent.ID, "missing", false); err == nil {
		t.Fatal("missing target accepted")
	}
	nextDir, err := db.PracticeInfo(t.Context(), parent.ID)
	if err != nil || nextDir.Completed != 0 || nextDir.Version != dir.Version {
		t.Fatal("navigation altered score or session content", err)
	}
	if _, err = s.EndPages(t.Context(), parent.ID, dir.Version); err != nil {
		t.Fatal(err)
	}
	ended, err := s.Directory(t.Context(), parent.ID)
	if err != nil || !ended.Ended || ended.Completed != 0 {
		t.Fatal("ending unanswered practice invented completions", err)
	}
}
