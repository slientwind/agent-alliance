package web

import (
	"net/http"
	"regexp"
	"strings"
)

const HiddenCredentials = "〔敏感字段已遮盖〕"

var credentialHeader = regexp.MustCompile(`(?i)(?:^|[-_])(?:authorization|api[-_]?key|subscription[-_]?key|auth|secret|token|signature|credentials?)(?:$|[-_])`)
var assignedCredential = regexp.MustCompile(`(?i)(?:authorization|api[-_ ]?key|access[-_ ]?token|client[-_ ]?secret|password|cookie|密钥|密码)["']?\s*[:=：]\s*\S+|bearer\s+\S+|-----BEGIN [^-]*PRIVATE KEY|\bsk-[A-Za-z0-9_-]{10,}`)
var sensitiveExcerpt = regexp.MustCompile(`(?i)(authorization|api[-_ ]?key|access[-_ ]?token|password|set-cookie|密码|密钥|口令)`)

func TraceText(value string) string {
	if assignedCredential.MatchString(value) || sensitiveExcerpt.MatchString(value) {
		return HiddenCredentials
	}
	return value
}

func safeProblemMessage(r *http.Request, message string) string {
	if assignedCredential.MatchString(message) {
		return HiddenCredentials
	}
	for name, values := range r.Header {
		if !credentialHeader.MatchString(name) {
			continue
		}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" && strings.Contains(message, value) {
				return HiddenCredentials
			}
			if fields := strings.Fields(value); len(fields) == 2 && strings.Contains(message, fields[1]) {
				return HiddenCredentials
			}
		}
	}
	for _, cookie := range r.Cookies() {
		if len(cookie.Value) >= 6 && strings.Contains(message, cookie.Value) {
			return HiddenCredentials
		}
	}
	return message
}
