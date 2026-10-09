// Package gradecheck rejects explicit claims that an entire nonempty answer
// is absent. It does not reject ordinary missing-knowledge feedback.
package gradecheck

import (
	"regexp"
	"strings"
)

var quotedAnswer = regexp.MustCompile(`(?:本次作答|本次回答|你的回答|原回答|用户作答|回答)(?:中)?(?:只|仅)(?:说|提到|写了|回答)[“”"]([^“”"]+)[“”"]`)

// A claimed verbatim description of what the student "only said" must be
// present in that answer. Ordinary missing-point feedback is unaffected.
func QuotedAnswerConflict(answer, text string) bool {
	for _, m := range quotedAnswer.FindAllStringSubmatch(text, -1) {
		if !strings.Contains(answer, m[1]) {
			return true
		}
	}
	return false
}

func MissingAnswerClaim(text string) bool {
	for _, claim := range []string{"未提供本次作答", "未提供作答原文", "没有用户作答", "缺少用户作答", "未收到用户作答", "未提供用户回答", "未提供本次回答", "没有本次回答", "未提交作答", "没有提供回答", "缺少作答原文", "未提供答案文本", "没有用户回答", "本次作答为空", "未提供原始作答", "no student answer", "no user answer", "answer was not provided", "missing student answer"} {
		if strings.Contains(strings.ToLower(text), claim) {
			return true
		}
	}
	return false
}
