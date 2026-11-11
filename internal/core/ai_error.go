package core

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/dynesshely/bot-harness/internal/ai"
)

// 模型错误透传到聊天时的脱敏与文案收敛。
//
// 原则：只告诉用户「哪一类失败 + 上游给的原因摘要」，绝不回显内部端点、
// 密钥、DSN、代理地址或完整堆栈；日志里仍保留原始 err。

var (
	httpStatusPattern = regexp.MustCompile(`(?i)\bHTTP\s+(\d{3})\b`)
	statusPrefix      = regexp.MustCompile(`(?i)^HTTP\s+\d{3}\s*:\s*`)
	urlPattern        = regexp.MustCompile(`(?i)\bhttps?://[^\s"'）)]+`)
	secretPattern     = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{4,}|bearer\s+[a-z0-9._-]+)`)
	keyValuePattern   = regexp.MustCompile(`(?i)\b(api[_-]?key|token|password|secret)\s*[=:]\s*[^\s,;]+`)
	dsnPattern        = regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|redis)://[^\s"'）)]+`)
	spacePattern      = regexp.MustCompile(`\s+`)
)

// userFacingAIError 把内部模型调用错误收敛成一句可直接发到群里的中文摘要。
// 结果已脱敏并截断，长度不超过约 300 个字符。
func userFacingAIError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ai.ErrReasoningOnlyCompletion) {
		return "模型只输出了思考过程，没有给出回答"
	}
	if errors.Is(err, ai.ErrEmptyCompletion) {
		return "模型返回了空回复"
	}
	if errors.Is(err, ai.ErrNotConfigured) {
		return "模型端点尚未配置"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "模型调用超时"
	}

	raw := err.Error()
	message := sanitizeAIError(raw)
	status := statusCode(raw)

	switch {
	case status == 401 || status == 403:
		return withDetail("模型鉴权失败（HTTP "+itoa(status)+"）", message)
	case status == 429:
		return withDetail("模型服务限流（HTTP 429）", message)
	case status >= 500:
		// 5xx 的响应体常是网关/代理的 HTML 或内部堆栈，只透出状态码，避免带出基础设施细节。
		return "模型服务异常（HTTP " + itoa(status) + "）"
	case status >= 400:
		return withDetail("模型调用失败（HTTP "+itoa(status)+"）", message)
	case strings.Contains(strings.ToLower(raw), "timeout") || strings.Contains(raw, "超时"):
		return "模型调用超时"
	case strings.Contains(raw, "connection refused") || strings.Contains(raw, "no such host") ||
		strings.Contains(raw, "dial tcp") || strings.Contains(raw, "network is unreachable"):
		return "模型服务连接失败"
	default:
		return withDetail("模型调用失败", message)
	}
}

func statusCode(raw string) int {
	m := httpStatusPattern.FindStringSubmatch(raw)
	if len(m) < 2 {
		return 0
	}
	n := 0
	for _, r := range m[1] {
		n = n*10 + int(r-'0')
	}
	return n
}

func sanitizeAIError(raw string) string {
	s := raw
	s = urlPattern.ReplaceAllString(s, "上游")
	s = dsnPattern.ReplaceAllString(s, "数据库")
	s = secretPattern.ReplaceAllString(s, "***")
	s = keyValuePattern.ReplaceAllString(s, "$1=***")
	// 去掉错误前缀与 HTTP 状态，只留下游原因；避免重复。
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "ai: ")
	s = statusPrefix.ReplaceAllString(s, "")
	s = strings.TrimPrefix(s, "request failed: ")
	s = strings.TrimPrefix(s, "ai: ")
	s = strings.TrimPrefix(s, "provider error: ")
	s = strings.TrimPrefix(s, "decode response: ")
	s = strings.TrimSpace(strings.TrimLeft(s, "0123456789: "))
	s = spacePattern.ReplaceAllString(s, " ")
	return truncateAIError(s, 220)
}

func withDetail(prefix, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" || detail == "上游" {
		return prefix
	}
	return prefix + "：" + detail
}

func truncateAIError(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [3]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
