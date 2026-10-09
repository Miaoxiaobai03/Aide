package coach

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"aide/backend/internal/interview"
	"aide/backend/internal/knowledge"
)

type Service struct {
	activeMu   sync.Mutex
	activeRuns map[string]map[string]context.CancelFunc
	repo       Repository
	model      Model
	knowledge  *knowledge.Index
	config     ContextConfig
	draw       func([]Question, int) ([]Question, error) // Optional deterministic dependency for isolated replay.
}

func New(repo Repository, model Model, index *knowledge.Index, config ContextConfig) *Service {
	return &Service{repo: repo, model: model, knowledge: index, config: config.defaults()}
}
func (s *Service) Load(ctx context.Context, key string) (Conversation, error) {
	return s.load(ctx, key, false)
}
func (s *Service) Training(ctx context.Context, key string) (Conversation, error) {
	return s.load(ctx, key, true)
}
func (s *Service) load(ctx context.Context, key string, allowRetained bool) (Conversation, error) {
	raw, v, err := s.repo.LoadTrainingDocument(ctx, DocumentKind, key)
	if errors.Is(err, interview.ErrStoreNotFound) {
		return Conversation{}, fail("not_found", "会话不存在或不属于当前档案", 404)
	}
	if err != nil {
		return Conversation{}, err
	}
	var c Conversation
	if json.Unmarshal(raw, &c) != nil || c.ID != key || c.Version != v {
		return c, fail("corrupt", "会话记录损坏，不能使用不完整记录", 503)
	}
	if c.Profile != ProfileID || c.Deleted && (!allowRetained || !c.TrainingRetained) {
		return Conversation{}, fail("not_found", "会话不存在或不属于当前档案", 404)
	}
	if c.Plan != nil {
		if len(c.Plan.Questions) == 0 || c.Plan.Current < 0 || c.Plan.Current >= len(c.Plan.Questions) || c.Plan.Discussion < 0 || c.Plan.Discussion > c.Plan.Current+1 {
			return c, fail("corrupt", "训练题序损坏", 503)
		}
		for _, q := range c.Plan.Questions {
			if c.Plan.PageIsolated {
				if q.ID == "" || q.Reference == "" {
					return c, fail("corrupt", "本题记录不完整", 503)
				}
				continue
			}
			if q.ReferenceHash != RawHash(q.Reference) {
				return c, fail("corrupt", "冻结知识依据的原文字节哈希不一致", 503)
			}
			expected := q.Hash
			q.Hash = ""
			if expected != Hash(q) {
				return c, fail("corrupt", "冻结题目或依据哈希不一致", 503)
			}
		}
	}
	syncQuestionGroups(&c)
	return c, nil
}

// Recover only expired leases. It does not race an active provider request.
func (s *Service) Recover(ctx context.Context, key string) (Conversation, error) {
	c, e := s.Load(ctx, key)
	if e != nil || c.Pending == nil || time.Now().Before(c.Pending.Until) {
		return c, e
	}
	return s.update(ctx, key, func(c *Conversation) error { return active(c) })
}
func (s *Service) save(ctx context.Context, c *Conversation) error {
	syncQuestionGroups(c)
	v := c.Version
	c.Version++
	c.Updated = time.Now().UTC()
	err := s.repo.SaveTrainingDocument(ctx, DocumentKind, c.ID, c, v)
	if err != nil {
		c.Version = v
		if errors.Is(err, interview.ErrPracticeBusy) {
			return fail("operation_busy", "本场已有请求正在处理，可以翻页浏览，稍后再提交", 409)
		}
		if errors.Is(err, interview.ErrStoreConflict) {
			return ErrConflict
		}
	}
	return err
}

// update retries only pure storage transformations, never a provider call.
func (s *Service) update(ctx context.Context, key string, fn func(*Conversation) error) (Conversation, error) {
	for n := 0; n < 4; n++ {
		c, e := s.Load(ctx, key)
		if e != nil {
			return c, e
		}
		if e = fn(&c); e != nil {
			return c, e
		}
		e = s.save(ctx, &c)
		if !errors.Is(e, ErrConflict) {
			return c, e
		}
	}
	return Conversation{}, ErrConflict
}
func (s *Service) Create(ctx context.Context) (Conversation, error) {
	now := time.Now().UTC()
	c := Conversation{ID: id("chat_"), Profile: ProfileID, Mode: "chat", Title: "新对话", Created: now, Messages: []Message{}, Summaries: []Summary{}, Runs: []Run{}}
	err := s.save(ctx, &c)
	return c, err
}
func (s *Service) List(ctx context.Context) ([]Listing, error) {
	var raw map[string]json.RawMessage
	var err error
	listingRepo, paged := s.repo.(interface {
		ListTrainingDocuments(context.Context, string) (map[string]json.RawMessage, error)
		PracticeDirectories(context.Context) ([]interview.PracticeInfo, error)
	})
	if paged {
		raw, err = listingRepo.ListTrainingDocuments(ctx, DocumentKind)
	} else {
		raw, err = s.repo.TrainingDocuments(ctx, DocumentKind)
	}
	if err != nil {
		return nil, err
	}
	out := []Listing{}
	if paged {
		directories, e := listingRepo.PracticeDirectories(ctx)
		if e != nil {
			return nil, e
		}
		for _, info := range directories {
			out = append(out, Listing{ID: info.ID, Title: info.Title, Mode: "practice", Updated: info.Updated, Completed: info.Completed, Target: len(info.Pages)})
		}
	}
	for _, b := range raw {
		var c Conversation
		if json.Unmarshal(b, &c) != nil {
			return nil, fail("corrupt", "会话列表存在损坏记录", 503)
		}
		if c.Deleted || c.Profile != ProfileID {
			continue
		}
		item := Listing{ID: c.ID, Title: c.Title, Mode: c.Mode, Updated: c.Updated, Completed: c.Completed()}
		if c.Plan != nil {
			item.Target = len(c.Plan.Questions)
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}
func validInput(in Input) error {
	if strings.TrimSpace(in.SubmissionID) == "" || len(in.SubmissionID) > 120 {
		return fail("validation", "需要有效提交 ID", 400)
	}
	if strings.TrimSpace(in.Message) == "" {
		return fail("validation", "请输入回答或问题", 400)
	}
	if len(in.Message) > 120000 {
		return fail("validation", "输入过长，请分段提交", 413)
	}
	if len(in.References)+len(in.Fragments) > 5 {
		return fail("validation", "一次最多引用五条消息", 400)
	}
	return nil
}
func active(c *Conversation) error {
	if c.Pending != nil && time.Now().Before(c.Pending.Until) {
		return fail("busy", "上一次请求仍在处理，请稍后刷新", 409)
	}
	// Expired runs are recoverable facts, never successful grades.
	if c.Pending != nil {
		for i := range c.Messages {
			if c.Messages[i].SubmissionID == c.Pending.ID && c.Messages[i].Status == "streaming" {
				c.Messages[i].Status = "interrupted"
				c.Messages[i].Revision++
			}
		}
		if c.Plan != nil {
			for i := range c.Plan.Attempts {
				if c.Plan.Attempts[i].ID == c.Pending.ID && c.Plan.Attempts[i].Status == "evaluating" {
					c.Plan.Attempts[i].Status = "failed"
					c.Plan.Attempts[i].Error = "请求中断或运行租约到期，可重试原提交"
					c.Plan.Status = "evaluation_failed"
				}
			}
		}
		c.Pending = nil
	}
	return nil
}
func fingerprint(c Conversation) string {
	bound := c.Plan != nil && c.Plan.PageIsolated && c.Pending != nil && len(c.Pending.SourceIDs) > 0
	msgs := []Message{}
	for _, m := range c.Messages {
		if bound && !leaseReads(c.Pending, m.ID) {
			continue
		}
		if !m.Deleted {
			msgs = append(msgs, m)
		}
	}
	plan := c.Plan
	if plan != nil {
		copy := *plan
		if bound {
			copy.Attempts = nil
			for _, a := range plan.Attempts {
				if a.ID == c.Pending.ID {
					copy.Attempts = append(copy.Attempts, a)
				}
			}
		}
		// The source link is navigation metadata. Deleting another practice may
		// unlink it without invalidating this practice's independent model input.
		copy.SourceConversation = ""
		plan = &copy
	}
	return Hash(struct {
		Messages []Message
		Plan     *Plan
	}{msgs, plan})
}
func leaseReads(lease *Lease, key string) bool {
	for _, id := range lease.SourceIDs {
		if id == key {
			return true
		}
	}
	return false
}
func (s *Service) Edit(ctx context.Context, key, messageID, text string, deleted bool, expected int64) (Conversation, error) {
	return s.update(ctx, key, func(c *Conversation) error {
		if c.Version != expected {
			return ErrConflict
		}
		for i := range c.Messages {
			m := &c.Messages[i]
			if m.ID != messageID {
				continue
			}
			if m.Kind == "answer" || m.Kind == "evaluation" || m.Kind == "question" {
				return fail("immutable", "训练原始作答、题目和评价不可改写；请追加重答", 409)
			}
			if !deleted && strings.TrimSpace(text) == "" {
				return fail("validation", "消息不能为空", 400)
			}
			if len(text) > 120000 {
				return fail("validation", "消息过长", 413)
			}
			m.Content = text
			m.Deleted = deleted
			m.Revision++
			c.Summaries = nil
			affectsRun := c.Pending != nil && (c.Plan == nil || !c.Plan.PageIsolated || len(c.Pending.SourceIDs) == 0 || leaseReads(c.Pending, m.ID) || m.SubmissionID == c.Pending.ID)
			if affectsRun {
				for j := range c.Messages {
					if c.Messages[j].SubmissionID == c.Pending.ID && c.Messages[j].Role == "assistant" && c.Messages[j].Status == "streaming" {
						c.Messages[j].Status = "interrupted"
						c.Messages[j].Revision++
					}
				}
				if c.Plan != nil {
					for j := range c.Plan.Attempts {
						if c.Plan.Attempts[j].ID == c.Pending.ID && c.Plan.Attempts[j].Status == "evaluating" {
							c.Plan.Attempts[j].Status = "failed"
							c.Plan.Attempts[j].Error = "相关原文已修改或删除，评价已取消，可以重试原提交"
							c.Plan.Status = "evaluation_failed"
						}
					}
				}
			}
			if affectsRun {
				c.Pending = nil
			}
			return nil
		}
		return fail("not_found", "原消息未找到", 404)
	})
}
func (s *Service) Delete(ctx context.Context, key string, expected int64, removeTraining bool) (Conversation, error) {
	if strings.TrimSpace(key) == "" || expected < 1 {
		return Conversation{}, fail("validation", "删除需要有效会话ID和正版本号", 400)
	}
	c, e := s.Training(ctx, key)
	if e != nil {
		var ce *Error
		if errors.As(e, &ce) && ce.Code == "not_found" {
			return Conversation{ID: key, Profile: ProfileID, Deleted: true, Messages: []Message{}}, nil
		}
		return c, e
	}
	if c.Version != expected {
		return c, ErrConflict
	}
	if c.PracticeID != "" {
		return c, fail("whole_practice", "请删除整轮练习，而非单独删除题页", 409)
	}
	if c.Plan != nil {
		if repo, ok := s.repo.(pageRepository); ok {
			if _, err := repo.PracticeInfo(ctx, key); err == nil {
				if err = s.DeletePages(ctx, key, expected); err != nil {
					return c, err
				}
				return Conversation{ID: key, Profile: ProfileID, Deleted: true, Messages: []Message{}}, nil
			}
		}
	}
	if c.Plan != nil || removeTraining {
		repo, ok := s.repo.(interface {
			DeleteTrainingConversation(context.Context, string, string, int64, string) error
		})
		if !ok {
			return c, fail("delete_unavailable", "存储尚不支持事务删除，未删除任何数据", 503)
		}
		e = repo.DeleteTrainingConversation(ctx, DocumentKind, key, expected, ProfileID)
		if errors.Is(e, interview.ErrStoreConflict) {
			return c, ErrConflict
		}
		if e != nil {
			return c, e
		}
		s.cancelRuns(key)
		return Conversation{ID: key, Profile: ProfileID, Deleted: true, Messages: []Message{}}, nil
	}
	c.Deleted = true
	if c.Plan != nil {
		for i := range c.Plan.Attempts {
			if c.Plan.Attempts[i].Status == "evaluating" {
				c.Plan.Attempts[i].Status = "failed"
				c.Plan.Attempts[i].Error = "删除会话时评分尚未完成"
				c.Plan.Status = "evaluation_failed"
			}
		}
	}
	c.Messages = nil
	c.Summaries = nil
	c.Pending = nil
	c.TrainingRetained = c.Plan != nil && !removeTraining
	if removeTraining {
		c.Plan = nil
	}
	c.Runs = nil
	e = s.save(ctx, &c)
	return c, e
}

func (s *Service) recordRetainedRun(ctx context.Context, key string, r Run) (Conversation, error) {
	if repo, ok := s.repo.(interface {
		RecordPracticeRun(context.Context, string, any) error
	}); ok {
		if pages, ok := s.repo.(pageRepository); ok {
			if _, err := pages.PracticeInfo(ctx, key); err == nil {
				if err = repo.RecordPracticeRun(ctx, key, r); err != nil {
					return Conversation{}, pageError(err)
				}
				return s.Training(ctx, key)
			}
		}
	}
	for i := 0; i < 4; i++ {
		c, e := s.Training(ctx, key)
		if e != nil {
			return c, e
		}
		c.Runs = append(c.Runs, r)
		e = s.save(ctx, &c)
		if !errors.Is(e, ErrConflict) {
			return c, e
		}
	}
	return Conversation{}, ErrConflict
}
