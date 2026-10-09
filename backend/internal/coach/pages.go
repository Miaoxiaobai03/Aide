package coach

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"aide/backend/internal/interview"
)

type pageRepository interface {
	PracticeInfo(context.Context, string) (interview.PracticeInfo, error)
	PracticePage(context.Context, string, string) (json.RawMessage, int64, error)
	VisitPracticePage(context.Context, string, string) error
	EndPractice(context.Context, string, int64) error
	DeletePractice(context.Context, string, int64) error
}
type PageView struct {
	Practice     interview.PracticeInfo `json:"practice"`
	Conversation Conversation           `json:"conversation"`
}

func pageError(err error) error {
	if errors.Is(err, interview.ErrStoreNotFound) {
		return fail("not_found", "练习或题目不存在", 404)
	}
	if errors.Is(err, interview.ErrStoreConflict) {
		return ErrConflict
	}
	if errors.Is(err, interview.ErrPracticeBusy) {
		return fail("operation_busy", "本场已有请求正在处理，可以翻页浏览，稍后再提交", 409)
	}
	return err
}
func (s *Service) practiceRepository() (pageRepository, error) {
	repo, ok := s.repo.(pageRepository)
	if !ok {
		return nil, fail("pages_unavailable", "存储尚不支持独立题页", 503)
	}
	return repo, nil
}
func (s *Service) Directory(ctx context.Context, parent string) (interview.PracticeInfo, error) {
	repo, err := s.practiceRepository()
	if err != nil {
		return interview.PracticeInfo{}, err
	}
	info, err := repo.PracticeInfo(ctx, parent)
	info.Runs = nil
	return info, pageError(err)
}
func (s *Service) Page(ctx context.Context, parent, question string, visit bool) (PageView, error) {
	repo, err := s.practiceRepository()
	if err != nil {
		return PageView{}, err
	}
	info, err := repo.PracticeInfo(ctx, parent)
	if err != nil {
		return PageView{}, pageError(err)
	}
	if question == "" && len(info.Pages) > 0 {
		question = info.Pages[0].QuestionID
	}
	var target *interview.PracticePageInfo
	for i := range info.Pages {
		if info.Pages[i].QuestionID == question {
			target = &info.Pages[i]
			break
		}
	}
	if target == nil {
		return PageView{}, fail("not_found", "本场没有这道题", 404)
	}
	if visit {
		if err = repo.VisitPracticePage(ctx, parent, question); err != nil {
			return PageView{}, pageError(err)
		}
		target.Visited = true
	}
	raw, version, err := repo.PracticePage(ctx, parent, question)
	if err != nil {
		return PageView{}, pageError(err)
	}
	var c Conversation
	if err = json.Unmarshal(raw, &c); err != nil || c.ID != target.ID || c.Version != version || c.PracticeID != parent || c.Profile != ProfileID || c.PageOrdinal != target.Ordinal || c.Plan == nil || !c.Plan.PageIsolated || len(c.Plan.Questions) != 1 || c.Plan.Questions[0].ID != question {
		return PageView{}, fail("corrupt", "本题记录不完整，请恢复后重试", 503)
	}
	// Browsing itself never edits another question or global navigation progress.
	if c.Pending != nil && !time.Now().Before(c.Pending.Until) {
		c, err = s.Recover(ctx, target.ID)
	}
	if err != nil {
		return PageView{}, err
	}
	info.Runs = nil
	return PageView{Practice: info, Conversation: s.Public(c)}, nil
}
func (s *Service) EndPages(ctx context.Context, parent string, version int64) (PageView, error) {
	repo, err := s.practiceRepository()
	if err != nil {
		return PageView{}, err
	}
	info, err := repo.PracticeInfo(ctx, parent)
	if err != nil {
		return PageView{}, pageError(err)
	}
	if !info.Ended {
		if err = repo.EndPractice(ctx, parent, version); err != nil {
			return PageView{}, pageError(err)
		}
	}
	return s.Page(ctx, parent, "", false)
}
func (s *Service) DeletePages(ctx context.Context, parent string, version int64) error {
	repo, err := s.practiceRepository()
	if err != nil {
		return err
	}
	if err = repo.DeletePractice(ctx, parent, version); err != nil {
		return pageError(err)
	}
	s.cancelRuns(parent)
	s.activeMu.Lock()
	for key, runs := range s.activeRuns {
		if strings.HasPrefix(key, parent+"::") {
			for _, cancel := range runs {
				cancel()
			}
		}
	}
	s.activeMu.Unlock()
	return nil
}
