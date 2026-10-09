package httpapi

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"aide/backend/internal/jobextract"
)

const maxJDImageBytes = 8 << 20

func (s *Server) handleJDImage(response http.ResponseWriter, request *http.Request) {
	if s.jdExtractor == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "vision_unavailable", "图片解析服务未配置", true, "")
		return
	}
	var input struct {
		Image string `json:"image"`
	}
	if err := readJSON(response, request, s.config.MaxJDImageBodyBytes, &input); err != nil {
		writeReadError(response, err)
		return
	}
	if err := validateJDImage(input.Image); err != "" {
		writeAPIError(response, http.StatusBadRequest, "invalid_image", err, false, "image")
		return
	}
	text, err := s.jdExtractor.Extract(request.Context(), input.Image)
	if err != nil {
		s.logger.Warn("JD image transcription failed", "error", err)
		if errors.Is(err, jobextract.ErrNoReadableText) {
			writeAPIError(response, http.StatusUnprocessableEntity, "transcription_failed", "无法识别图片中的文字，请尝试更清晰的截图", false, "image")
			return
		}
		writeAPIError(response, http.StatusServiceUnavailable, "vision_unavailable", "图片解析服务暂时不可用，请稍后重试", true, "")
		return
	}
	writeJSON(response, http.StatusOK, map[string]string{"text": text})
}

func validateJDImage(dataURL string) string {
	var expected string
	switch {
	case strings.HasPrefix(dataURL, "data:image/png;base64,"):
		expected = "png"
	case strings.HasPrefix(dataURL, "data:image/jpeg;base64,"):
		expected = "jpeg"
	default:
		return "只支持 PNG 或 JPG 图片"
	}
	encoded := dataURL[strings.IndexByte(dataURL, ',')+1:]
	if len(encoded) > base64.StdEncoding.EncodedLen(maxJDImageBytes) {
		return "图片超过 8 MB"
	}
	imageBytes, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(imageBytes) == 0 {
		return "图片数据无效"
	}
	if len(imageBytes) > maxJDImageBytes {
		return "图片超过 8 MB"
	}
	if expected == "png" && !bytes.HasPrefix(imageBytes, []byte{137, 80, 78, 71, 13, 10, 26, 10}) {
		return "图片内容与 PNG 格式不符"
	}
	if expected == "jpeg" && (len(imageBytes) < 4 || imageBytes[0] != 0xff || imageBytes[1] != 0xd8 || imageBytes[len(imageBytes)-2] != 0xff || imageBytes[len(imageBytes)-1] != 0xd9) {
		return "图片内容与 JPG 格式不符"
	}
	return ""
}
