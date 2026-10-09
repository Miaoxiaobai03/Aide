package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"aide/backend/internal/llm"
	"aide/backend/internal/resumediagnosis"
)

const (
	maxResumeImages   = 3
	maxResumeImageURL = 3 << 20
)

func (s *Server) handleResumeDiagnosis(response http.ResponseWriter, request *http.Request) {
	if s.resumeDiagnostician == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "resume_diagnostician_unavailable", "简历诊断模型尚未配置，请检查服务配置；简历原文已保留。", true, "")
		return
	}
	var input resumediagnosis.Request
	if err := readJSON(response, request, s.config.MaxResumeDiagnosisBytes, &input); err != nil {
		writeReadError(response, err)
		return
	}
	input.Content = strings.TrimSpace(input.Content)
	if input.Content == "" {
		writeAPIError(response, http.StatusBadRequest, "validation_error", "resume content is required", false, "content")
		return
	}
	if len(input.Images) > maxResumeImages {
		writeAPIError(response, http.StatusBadRequest, "too_many_images", "at most three resume page images are allowed", false, "images")
		return
	}
	for _, image := range input.Images {
		if len(image) > maxResumeImageURL ||
			(!strings.HasPrefix(image, "data:image/jpeg;base64,") && !strings.HasPrefix(image, "data:image/png;base64,")) {
			writeAPIError(response, http.StatusBadRequest, "invalid_image", "resume images must be bounded JPEG or PNG data URLs", false, "images")
			return
		}
	}
	result, err := s.resumeDiagnostician.Diagnose(request.Context(), input)
	if err != nil {
		s.logger.Warn("resume diagnostician failed", "error", err)
		status, code, message := resumeDiagnosisError(err)
		writeAPIError(response, status, code, message, true, "")
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func resumeDiagnosisError(err error) (int, string, string) {
	switch {
	case errors.Is(err, llm.ErrOutputTruncated):
		return http.StatusBadGateway, "resume_output_truncated", "模型诊断结果超过输出额度，未返回完整报告；简历原文已保留。请重试，若持续出现，请调整简历诊断专用输出额度。"
	case errors.Is(err, llm.ErrStructuredOutput), errors.Is(err, resumediagnosis.ErrInvalidResult):
		return http.StatusBadGateway, "resume_result_invalid", "模型未返回完整、合法的诊断报告；简历原文已保留，请重试。"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "resume_diagnosis_timeout", "简历诊断超时；简历原文已保留，请稍后重试。"
	}
	var providerErr *llm.HTTPError
	if errors.As(err, &providerErr) {
		switch providerErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return http.StatusServiceUnavailable, "resume_model_auth_failed", "简历诊断模型认证失败，请检查 API 配置；简历原文已保留。"
		case http.StatusPaymentRequired:
			return http.StatusServiceUnavailable, "resume_model_balance_insufficient", "模型服务余额不足；简历原文已保留，请补充余额后重试。"
		case http.StatusTooManyRequests:
			return http.StatusServiceUnavailable, "resume_model_rate_limited", "模型服务请求过于频繁；简历原文已保留，请稍后重试。"
		case http.StatusBadRequest:
			return http.StatusBadGateway, "resume_model_request_rejected", "模型拒绝了诊断请求，请检查模型是否支持当前文字／图片及输出配置；简历原文已保留。"
		}
	}
	return http.StatusServiceUnavailable, "resume_diagnosis_failed", "简历诊断服务暂时不可用；简历原文已保留，请稍后重试。"
}
