package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"offerpilot/backend/internal/chat"
	"offerpilot/backend/internal/coach"
)

func writeCoachError(w http.ResponseWriter, err error) {
	var e *coach.Error
	if errors.As(err, &e) {
		writeAPIError(w, e.Status, e.Code, e.Message, e.Status >= 500 || e.Status == 409, "")
		return
	}
	writeAPIError(w, 503, "coach_unavailable", "保存或模型调用失败，请恢复会话后重试", true, "")
}
func (s *Server) coachAvailable(w http.ResponseWriter) bool {
	if s.coach == nil {
		writeAPIError(w, 503, "coach_unavailable", "持久化问答服务未配置", false, "")
		return false
	}
	return true
}
func (s *Server) handleCoachRead(w http.ResponseWriter, r *http.Request) {
	if !s.coachAvailable(w) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	if q.Get("resource") == "capabilities" {
		capabilities := s.coach.ContextLimits()
		capabilities["gradingVersion"] = coach.GradingVersion
		capabilities["contextPolicyVersion"] = coach.ContextPolicyVersion
		writeJSON(w, 200, capabilities)
		return
	}
	if q.Get("resource") == "list" || q.Get("resource") == "" && q.Get("id") == "" {
		list, e := s.coach.List(r.Context())
		if e != nil {
			writeCoachError(w, e)
			return
		}
		writeJSON(w, 200, map[string]any{"items": list})
		return
	}
	if q.Get("resource") == "directory" {
		info, err := s.coach.Directory(r.Context(), q.Get("id"))
		if err != nil {
			writeCoachError(w, err)
			return
		}
		writeJSON(w, 200, info)
		return
	}
	if q.Get("resource") == "page" {
		view, err := s.coach.Page(r.Context(), q.Get("id"), q.Get("questionId"), false)
		if err != nil {
			writeCoachError(w, err)
			return
		}
		writeJSON(w, 200, view)
		return
	}
	var c coach.Conversation
	var e error
	if q.Get("resource") == "training" {
		c, e = s.coach.Training(r.Context(), q.Get("id"))
	} else {
		c, e = s.coach.Recover(r.Context(), q.Get("id"))
	}
	if e != nil {
		writeCoachError(w, e)
		return
	}
	if q.Get("resource") == "runs" {
		writeJSON(w, 200, map[string]any{"runs": c.Runs})
		return
	}
	if q.Get("resource") == "messages" {
		after := 0
		limit := 50
		if v := q.Get("after"); v != "" {
			after, e = strconv.Atoi(v)
			if e != nil || after < 0 {
				writeAPIError(w, 400, "validation", "after 必须为非负序号", false, "")
				return
			}
		}
		if v := q.Get("limit"); v != "" {
			limit, e = strconv.Atoi(v)
			if e != nil || limit < 1 || limit > 100 {
				writeAPIError(w, 400, "validation", "limit 必须为1—100", false, "")
				return
			}
		}
		out := []coach.Message{}
		next := 0
		for _, m := range s.coach.Public(c).Messages {
			if m.Seq <= after {
				continue
			}
			if len(out) == limit {
				next = out[len(out)-1].Seq
				break
			}
			out = append(out, m)
		}
		writeJSON(w, 200, map[string]any{"messages": out, "next": next, "version": c.Version})
		return
	}
	writeJSON(w, 200, s.coach.Public(c))
}
func (s *Server) handleCoachMutation(w http.ResponseWriter, r *http.Request) {
	if !s.coachAvailable(w) {
		return
	}
	var in struct {
		Action         string          `json:"action"`
		ID             string          `json:"id"`
		Count          json.RawMessage `json:"count"`
		Ordinal        int             `json:"ordinal"`
		Version        int64           `json:"version"`
		MessageID      string          `json:"messageId"`
		Text           string          `json:"text"`
		Deleted        bool            `json:"deleted"`
		RemoveTraining bool            `json:"removeTraining"`
		PracticeID     string          `json:"practiceId"`
		Original       bool            `json:"original"`
		coach.Input
	}
	if e := readJSON(w, r, s.config.MaxJSONBodyBytes, &in); e != nil {
		writeReadError(w, e)
		return
	}
	if in.ID == "" {
		in.ID = in.PracticeID
	}
	if in.Action == "visitpage" || in.Action == "endpages" || in.Action == "deletepages" {
		var view coach.PageView
		var err error
		switch in.Action {
		case "visitpage":
			view, err = s.coach.Page(r.Context(), in.ID, in.QuestionID, true)
		case "endpages":
			view, err = s.coach.EndPages(r.Context(), in.ID, in.Version)
		case "deletepages":
			err = s.coach.DeletePages(r.Context(), in.ID, in.Version)
		}
		if err != nil {
			writeCoachError(w, err)
			return
		}
		if in.Action == "deletepages" {
			writeJSON(w, 200, map[string]any{"deleted": true, "id": in.ID})
			return
		}
		writeJSON(w, 200, view)
		return
	}
	if in.PracticeID != "" {
		if strings.TrimSpace(in.QuestionID) == "" {
			writeAPIError(w, 400, "validation", "本题操作需要questionId", false, "questionId")
			return
		}
		view, err := s.coach.Page(r.Context(), in.PracticeID, in.QuestionID, false)
		if err != nil {
			writeCoachError(w, err)
			return
		}
		in.ID = view.Conversation.ID
		in.ConversationID = in.ID
		in.Ordinal = 1
	}
	var c coach.Conversation
	var e error
	switch in.Action {
	case "create":
		c, e = s.coach.Create(r.Context())
	case "practice":
		count, err := coach.ParseRoundCount(in.Count)
		if err != nil {
			writeCoachError(w, err)
			return
		}
		c, e = s.coach.StartPracticePagesWithID(r.Context(), count, in.SubmissionID)
	case "answer":
		if in.ConversationID == "" {
			in.ConversationID = in.ID
		}
		c, e = s.coach.Answer(r.Context(), in.Input)
	case "next", "finish":
		c, e = s.coach.Next(r.Context(), in.ID, in.Ordinal, in.Action == "finish")
	case "learn":
		c, e = s.coach.Learn(r.Context(), in.ID, in.Ordinal)
	case "focus":
		c, e = s.coach.FocusQuestion(r.Context(), in.ID, in.QuestionID)
	case "retest":
		c, e = s.coach.Retest(r.Context(), in.ID, in.Model, in.Original)
	case "edit":
		c, e = s.coach.Edit(r.Context(), in.ID, in.MessageID, in.Text, in.Deleted, in.Version)
	case "delete":
		c, e = s.coach.Delete(r.Context(), in.ID, in.Version, in.RemoveTraining)
	default:
		writeAPIError(w, 400, "validation", "未知操作", false, "action")
		return
	}
	if e != nil {
		writeCoachError(w, e)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, s.coach.Public(c))
}
func (s *Server) handleDurableChat(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Message          string           `json:"message"`
		SessionID        string           `json:"sessionId"`
		Model            string           `json:"model"`
		SubmissionID     string           `json:"submissionId"`
		References       []string         `json:"references"`
		Fragments        []coach.Fragment `json:"fragments"`
		Corrects         string           `json:"corrects"`
		QuestionID       string           `json:"questionId"`
		PracticeID       string           `json:"practiceId"`
		RetryCompression bool             `json:"retryCompression"`
	}
	if e := readJSON(w, r, s.config.MaxJSONBodyBytes, &in); e != nil {
		writeReadError(w, e)
		return
	}
	if strings.TrimSpace(in.Message) == "" {
		writeAPIError(w, 400, "validation", "请输入问题", false, "message")
		return
	}
	loaded, loadErr := s.coach.Load(r.Context(), in.SessionID)
	if loadErr != nil {
		writeCoachError(w, loadErr)
		return
	}
	if loaded.PracticeID != "" {
		if in.PracticeID != "" && in.PracticeID != loaded.PracticeID || len(loaded.Plan.Questions) != 1 || in.QuestionID != loaded.Plan.Questions[0].ID {
			writeAPIError(w, 422, "question_scope", "请切换到对应题页再提问", false, "questionId")
			return
		}
		in.PracticeID = loaded.PracticeID
	}
	if in.SubmissionID == "" {
		in.SubmissionID = randomID()
	} // Older callers still work; new UI always sends an ID.
	stream := newSSEWriter(w)
	stream.start()
	sequence := 0
	send := func(event map[string]any) {
		sequence++
		event["practiceId"] = in.PracticeID
		event["questionId"] = in.QuestionID
		event["operationId"] = in.SubmissionID
		event["sequence"] = sequence
		stream.send(event)
	}
	send(map[string]any{"type": "session", "sessionId": in.SessionID, "questionId": in.QuestionID, "operationId": in.SubmissionID})
	c, e := s.coach.Generate(r.Context(), coach.Input{ConversationID: in.SessionID, SubmissionID: in.SubmissionID, Message: in.Message, Model: in.Model, References: in.References, Fragments: in.Fragments, Corrects: in.Corrects, QuestionID: in.QuestionID, RetryCompression: in.RetryCompression}, func(d chat.Delta) error {
		if d.Thinking != "" {
			send(map[string]any{"type": "thinking_delta", "content": d.Thinking, "sessionId": in.SessionID, "questionId": in.QuestionID, "operationId": in.SubmissionID})
		}
		if d.Text != "" {
			send(map[string]any{"type": "text_delta", "content": d.Text, "sessionId": in.SessionID, "questionId": in.QuestionID, "operationId": in.SubmissionID})
		}
		return r.Context().Err()
	})
	if e != nil {
		var ce *coach.Error
		code := "generation_failed"
		message := "模型输出未完成，原文已保留；请刷新后重试"
		if errors.As(e, &ce) {
			message = ce.Message
			code = ce.Code
		}
		send(map[string]any{"type": "error", "message": message, "code": code})
	} else {
		send(map[string]any{"type": "done", "conversation": s.coach.Public(c), "sessionId": in.SessionID, "questionId": in.QuestionID, "operationId": in.SubmissionID})
	}
	stream.done()
}
