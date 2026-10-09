package interview

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

type HistoryQuery struct {
	Limit                     int
	Cursor, State, Kind, Area string
}
type HistoryItem struct {
	ID             string    `json:"id"`
	Type           string    `json:"type"`
	State          string    `json:"state"`
	StartedAt      time.Time `json:"startedAt"`
	Answered       int       `json:"answered"`
	Evaluated      int       `json:"evaluated"`
	FeedbackHidden bool      `json:"feedbackHidden"`
	ReportStatus   string    `json:"reportStatus"`
	Area           string    `json:"area"`
	ScoringVersion string    `json:"scoringVersion"`
	NeedsReview    int       `json:"needsReview,omitempty"`
}
type HistoryPage struct {
	Items      []HistoryItem    `json:"items"`
	NextCursor string           `json:"nextCursor"`
	Warnings   []HistoryWarning `json:"warnings"`
	Unassigned []string         `json:"unassigned"`
	ProfileID  string           `json:"profileId"`
}
type HistoryDetail struct {
	Item     HistoryItem     `json:"item"`
	Snapshot SessionSnapshot `json:"snapshot"`
	Report   *Report         `json:"report,omitempty"`
}
type ScoreGroup struct {
	Key        string             `json:"key"`
	Version    string             `json:"version"`
	Kind       QuestionKind       `json:"kind"`
	Difficulty Difficulty         `json:"difficulty"`
	Count      int                `json:"count"`
	Scores     map[string]float64 `json:"scores"`
}
type TrainingStats struct {
	Sessions            int              `json:"sessions"`
	Completed           int              `json:"completed"`
	Answered            int              `json:"answered"`
	Evaluated           int              `json:"evaluated"`
	HiddenEvaluations   int              `json:"hiddenEvaluations"`
	InvalidEvaluations  int              `json:"invalidEvaluations"`
	RetestFailures      int              `json:"retestFailures"`
	PracticeNeedsReview int              `json:"practiceNeedsReview,omitempty"`
	Groups              []ScoreGroup     `json:"groups"`
	Areas               map[string]int   `json:"areas"`
	Warnings            []HistoryWarning `json:"warnings"`
	Unassigned          int              `json:"unassigned"`
}

func (s *Service) trainingRepo() (trainingRepository, error) {
	repo, ok := s.store.(trainingRepository)
	if !ok {
		return nil, unavailable("训练历史需要 SQLite 存储", nil)
	}
	return repo, nil
}
func (s *Service) ownedSessions(ctx context.Context) ([]InterviewSession, []HistoryWarning, []string, error) {
	repo, err := s.trainingRepo()
	if err != nil {
		return nil, nil, nil, err
	}
	all, warnings, err := repo.HistorySessions(ctx)
	if err != nil {
		return nil, nil, nil, unavailable("训练历史读取失败", err)
	}
	claims, err := repo.TrainingDocuments(ctx, "ownership")
	if err != nil {
		return nil, nil, nil, unavailable("档案归属读取失败", err)
	}
	owned := []InterviewSession{}
	unassigned := []string{}
	for _, record := range all {
		owner := record.LocalProfileID
		if owner == "" {
			if raw, ok := claims[record.ID]; ok {
				if err := json.Unmarshal(raw, &owner); err != nil {
					return nil, nil, nil, unavailable("档案归属损坏", err)
				}
			}
		}
		if owner == LocalProfileID {
			owned = append(owned, record)
		} else if owner == "" {
			unassigned = append(unassigned, record.ID)
		}
	}
	sort.Strings(unassigned)
	return owned, warnings, unassigned, nil
}
func (s *Service) ImportHistory(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return 0, validation("ids", "请选择 1—100 条旧记录")
	}
	repo, err := s.trainingRepo()
	if err != nil {
		return 0, err
	}
	// Validate every selection before writing anything. Explicit ownership is
	// separate from the immutable legacy source; repeated imports are harmless.
	for _, id := range ids {
		record, err := s.load(ctx, id)
		if err != nil {
			return 0, err
		}
		if record.LocalProfileID != "" && record.LocalProfileID != LocalProfileID {
			return 0, conflict("记录不属于本地档案", nil)
		}
	}
	n := 0
	for _, id := range ids {
		err := repo.SaveTrainingDocument(ctx, "ownership", id, LocalProfileID, 0)
		if errors.Is(err, ErrStoreConflict) {
			raw, _, e := repo.LoadTrainingDocument(ctx, "ownership", id)
			if e != nil || string(raw) != strconv.Quote(LocalProfileID) {
				return n, conflict("已有不同的档案归属", e)
			}
			continue
		}
		if err != nil {
			return n, unavailable("导入失败；已导入部分可安全重试", err)
		}
		n++
	}
	return n, nil
}
func feedbackVisible(record InterviewSession) bool {
	return record.Config.FeedbackMode != FeedbackDeferred || record.State == StateCompleted
}
func validStoredAssessment(a Assessment) bool {
	for _, score := range []int{a.Correctness, a.Depth, a.Specificity, a.Ownership, a.Metrics, a.Tradeoffs} {
		if score < 1 || score > 5 {
			return false
		}
	}
	return len(a.EvidenceRefs) > 0
}

func validStoredRecord(session InterviewSession, turn AnswerRecord) bool {
	return turn.Question.ID != "" && strings.TrimSpace(turn.Answer.Text) != "" && validStoredAssessment(turn.Assessment) && validateAssessment(session.Sources, turn.Assessment, evidenceIDSet(turn.Question.EvidenceRefs)) == nil
}
func historyItem(record InterviewSession) HistoryItem {
	item := HistoryItem{ID: record.ID, Type: "interview", State: string(record.State), StartedAt: record.StartedAt, Answered: len(record.Answers), FeedbackHidden: !feedbackVisible(record), ReportStatus: "not_generated", Area: "unclassified", ScoringVersion: record.ScoringVersion}
	if item.ScoringVersion == "" {
		item.ScoringVersion = "legacy-unknown"
	}
	if record.Config.Focus != "" {
		item.Area = string(record.Config.Focus)
	}
	if record.State != StateCompleted {
		item.ReportStatus = "not_ready"
	} else if record.Report != nil {
		item.ReportStatus = "available"
	}
	if feedbackVisible(record) {
		for _, a := range record.Answers {
			if validStoredRecord(record, a) {
				item.Evaluated++
			}
		}
	}
	return item
}

type historyCursor struct {
	Version int       `json:"version"`
	At      time.Time `json:"at"`
	ID      string    `json:"id"`
}

func (s *Service) History(ctx context.Context, q HistoryQuery) (HistoryPage, error) {
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 100 {
		return HistoryPage{}, validation("limit", "必须为 1—100")
	}
	if q.State != "" && q.State != string(StateAwaitingAnswer) && q.State != string(StateCompleted) {
		return HistoryPage{}, validation("state", "非法训练状态")
	}
	if q.Kind != "" && q.Kind != "interview" && q.Kind != "retest" && q.Kind != "practice" {
		return HistoryPage{}, validation("kind", "非法训练类型")
	}
	if q.Area != "" && q.Area != "unclassified" && q.Area != "mixed" && q.Area != "knowledge" && q.Area != "projects" {
		return HistoryPage{}, validation("area", "非法训练领域")
	}
	var cursor historyCursor
	if q.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.ID == "" || cursor.Version != 1 {
			return HistoryPage{}, validation("cursor", "非法历史游标")
		}
	}
	records, warnings, unassigned, err := s.ownedSessions(ctx)
	if err != nil {
		return HistoryPage{}, err
	}
	items := []HistoryItem{}
	for _, r := range records {
		items = append(items, historyItem(r))
	}
	// P1 appends persisted retest plans to the same history projection.
	extra, err := s.retestHistory(ctx)
	if err != nil {
		return HistoryPage{}, err
	}
	items = append(items, extra...)
	practice, err := s.practiceHistory(ctx)
	if err != nil {
		return HistoryPage{}, err
	}
	items = append(items, practice...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartedAt.Equal(items[j].StartedAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].StartedAt.After(items[j].StartedAt)
	})
	filtered := []HistoryItem{}
	for _, item := range items {
		if q.State != "" && item.State != q.State || q.Kind != "" && item.Type != q.Kind || q.Area != "" && item.Area != q.Area {
			continue
		}
		if q.Cursor != "" && (item.StartedAt.After(cursor.At) || item.StartedAt.Equal(cursor.At) && item.ID >= cursor.ID) {
			continue
		}
		filtered = append(filtered, item)
	}
	page := HistoryPage{Items: filtered, Warnings: warnings, Unassigned: unassigned, ProfileID: LocalProfileID}
	if len(filtered) > q.Limit {
		page.Items = filtered[:q.Limit]
		last := page.Items[len(page.Items)-1]
		raw, _ := json.Marshal(historyCursor{Version: 1, At: last.StartedAt, ID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
func (s *Service) ownedSession(ctx context.Context, id string) (InterviewSession, error) {
	record, err := s.load(ctx, id)
	if err != nil {
		return record, err
	}
	if record.LocalProfileID == LocalProfileID {
		return record, nil
	}
	repo, err := s.trainingRepo()
	if err != nil {
		return record, err
	}
	raw, _, err := repo.LoadTrainingDocument(ctx, "ownership", id)
	if err != nil || string(raw) != strconv.Quote(LocalProfileID) {
		return InterviewSession{}, &DomainError{Code: CodeNotFound, Message: "训练不存在或尚未导入本地档案"}
	}
	return record, nil
}
func (s *Service) HistoryDetail(ctx context.Context, id string) (HistoryDetail, error) {
	record, err := s.ownedSession(ctx, id)
	if err != nil {
		return HistoryDetail{}, err
	}
	snapshot, err := s.Snapshot(ctx, id)
	if err != nil {
		return HistoryDetail{}, err
	}
	detail := HistoryDetail{Item: historyItem(record), Snapshot: snapshot}
	if feedbackVisible(record) && record.Report != nil {
		copy := cloneSession(record)
		detail.Report = copy.Report
		for i := range detail.Report.Turns {
			publicRecordEvidence(record.Sources, &detail.Report.Turns[i])
		}
		publicProfileEvidence(record.Sources, &detail.Report.Profile)
	}
	return detail, nil
}
func (s *Service) TrainingStats(ctx context.Context) (TrainingStats, error) {
	records, warnings, unassigned, err := s.ownedSessions(ctx)
	if err != nil {
		return TrainingStats{}, err
	}
	stats := TrainingStats{Sessions: len(records), Areas: map[string]int{}, Groups: []ScoreGroup{}, Warnings: warnings, Unassigned: len(unassigned)}
	groups := map[string]*ScoreGroup{}
	for _, r := range records {
		if r.State == StateCompleted {
			stats.Completed++
		}
		stats.Answered += len(r.Answers)
		stats.Areas[historyItem(r).Area]++
		if !feedbackVisible(r) {
			stats.HiddenEvaluations += len(r.Answers)
			continue
		}
		for _, turn := range r.Answers {
			if !validStoredRecord(r, turn) {
				stats.InvalidEvaluations++
				continue
			}
			stats.Evaluated++
			v := historyItem(r).ScoringVersion
			key := strings.Join([]string{v, string(turn.Question.Kind), string(turn.Question.Difficulty), string(turn.Answer.InputMode)}, "/")
			if v == "legacy-unknown" {
				key += "/" + r.ID
			}
			if groups[key] == nil {
				groups[key] = &ScoreGroup{Key: key, Version: v, Kind: turn.Question.Kind, Difficulty: turn.Question.Difficulty, Scores: map[string]float64{}}
			}
			g := groups[key]
			g.Count++
			a := turn.Assessment
			values := map[string]int{"correctness": a.Correctness, "depth": a.Depth, "specificity": a.Specificity, "ownership": a.Ownership, "metrics": a.Metrics, "tradeoffs": a.Tradeoffs}
			for k, value := range values {
				g.Scores[k] += float64(value)
			}
		}
	}
	plans, err := s.allPlans(ctx)
	if err != nil {
		return stats, err
	}
	for _, p := range plans {
		stats.Sessions++
		if p.Status == "completed" {
			stats.Completed++
		}
		source, e := frozenSession(p)
		if e != nil {
			return stats, unavailable("复测材料损坏", e)
		}
		stats.Areas[historyItem(source).Area]++
		for _, attempt := range p.Attempts {
			stats.Answered++
			if attempt.Status == "failed" {
				stats.RetestFailures++
			}
			if attempt.Status != "succeeded" || attempt.Evaluation == nil {
				continue
			}
			stats.Evaluated++
			uncertain := false
			for _, r := range attempt.Evaluation.Results {
				if r.Status == "unassessed" {
					uncertain = true
				}
			}
			if uncertain {
				continue
			}
			var task *RetestTask
			for i := range p.Tasks {
				if p.Tasks[i].ID == attempt.TaskID {
					task = &p.Tasks[i]
				}
			}
			if task == nil {
				return stats, unavailable("复测任务关联损坏", nil)
			}
			key := p.ScoringVersion + "/" + trainingHash(task.CriteriaVersion)[:12] + "/" + trainingHash([]string{task.Question.Text, p.MaterialVersion, string(attempt.Answer.InputMode)})[:12]
			if groups[key] == nil {
				groups[key] = &ScoreGroup{Key: key, Version: p.ScoringVersion, Kind: task.Question.Kind, Difficulty: task.Question.Difficulty, Scores: map[string]float64{}}
			}
			g := groups[key]
			g.Count++
			a := attempt.Evaluation.Assessment
			for k, value := range map[string]int{"correctness": a.Correctness, "depth": a.Depth, "specificity": a.Specificity, "ownership": a.Ownership, "metrics": a.Metrics, "tradeoffs": a.Tradeoffs} {
				g.Scores[k] += float64(value)
			}
		}
	}
	for _, g := range groups {
		for k, v := range g.Scores {
			g.Scores[k] = v / float64(g.Count)
		}
		stats.Groups = append(stats.Groups, *g)
	}
	if err = s.appendPracticeStats(ctx, &stats); err != nil {
		return stats, err
	}
	sort.Slice(stats.Groups, func(i, j int) bool { return stats.Groups[i].Key < stats.Groups[j].Key })
	return stats, nil
}

// Implemented in P1; P0 deliberately has no fabricated retest records.
func (s *Service) retestHistory(ctx context.Context) ([]HistoryItem, error) {
	return s.retestItems(ctx)
}
