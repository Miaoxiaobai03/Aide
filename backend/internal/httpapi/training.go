package httpapi

import (
	"net/http"
	"strconv"

	"aide/backend/internal/interview"
)

func (s *Server) trainingService(response http.ResponseWriter) *interview.Service {
	service, ok := s.interview.(*interview.Service)
	if !ok {
		writeAPIError(response, 503, "service_unavailable", "训练服务未配置", true, "")
		return nil
	}
	return service
}
func (s *Server) handleTraining(response http.ResponseWriter, request *http.Request) {
	service := s.trainingService(response)
	if service == nil {
		return
	}
	q := request.URL.Query()
	var result any
	var err error
	switch q.Get("resource") {
	case "", "history":
		limit := 20
		if raw := q.Get("limit"); raw != "" {
			limit, err = strconv.Atoi(raw)
			if err != nil || limit < 1 || limit > 100 {
				writeAPIError(response, 400, "validation", "limit 必须为 1—100", false, "limit")
				return
			}
		}
		result, err = service.History(request.Context(), interview.HistoryQuery{Limit: limit, Cursor: q.Get("cursor"), State: q.Get("state"), Kind: q.Get("kind"), Area: q.Get("area")})
	case "stats":
		result, err = service.TrainingStats(request.Context())
	case "weaknesses":
		result, err = service.Weaknesses(request.Context(), q.Get("sourceId"))
	case "plan":
		result, err = service.RetestPlan(request.Context(), q.Get("id"))
	case "attempts":
		result, err = service.WeaknessAttempts(request.Context(), q.Get("sourceId"), q.Get("id"))
	case "compare":
		result, err = service.CompareAttempts(request.Context(), q.Get("sourceId"), q.Get("id"), q.Get("left"), q.Get("right"))
	case "detail":
		var detail interview.HistoryDetail
		detail, err = service.HistoryDetail(request.Context(), q.Get("id"))
		if err == nil {
			value := map[string]any{"item": detail.Item, "snapshot": mapSnapshot(detail.Snapshot)}
			if detail.Report != nil {
				value["report"] = mapReport(detail.Item.ID, *detail.Report)
			}
			result = value
		}
	default:
		writeAPIError(response, 400, "validation", "resource 非法", false, "resource")
		return
	}
	if err != nil {
		writeInterviewError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, 200, result)
}
func (s *Server) handleTrainingMutation(response http.ResponseWriter, request *http.Request) {
	service := s.trainingService(response)
	if service == nil {
		return
	}
	var input interview.RetestMutation
	if err := readJSON(response, request, s.config.MaxInterviewBytes, &input); err != nil {
		writeReadError(response, err)
		return
	}
	var result any
	var err error
	switch input.Action {
	case "import":
		var n int
		n, err = service.ImportHistory(request.Context(), input.IDs)
		result = map[string]any{"imported": n}
	case "edit_weakness":
		result, err = service.EditWeakness(request.Context(), input)
	case "create_retest":
		result, err = service.CreateRetest(request.Context(), input)
	case "prepare_retest":
		result, err = service.QueueRetest(request.Context(), input.PlanID, input.Batch)
	case "answer_retest":
		result, err = service.SubmitRetest(request.Context(), input)
	case "pause_retest":
		result, err = service.PauseRetest(request.Context(), input.PlanID, true)
	case "resume_retest":
		result, err = service.PauseRetest(request.Context(), input.PlanID, false)
	default:
		writeAPIError(response, 400, "validation", "action 非法", false, "action")
		return
	}
	if err != nil {
		writeInterviewError(response, err)
		return
	}
	writeJSON(response, 200, result)
}
