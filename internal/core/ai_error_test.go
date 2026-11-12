package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/ai"
)

func TestUserFacingAIError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want []string
		deny []string
	}{
		{
			name: "http 400 with provider message",
			err:  fmt.Errorf("ai: HTTP 400: invalid model: gpt-unknown"),
			want: []string{"模型调用失败（HTTP 400）", "invalid model"},
		},
		{
			name: "http 401 hides key",
			err:  fmt.Errorf("ai: HTTP 401: invalid api key sk-live-secret123"),
			want: []string{"模型鉴权失败（HTTP 401）", "***"},
			deny: []string{"sk-live-secret123"},
		},
		{
			name: "http 429",
			err:  fmt.Errorf("ai: HTTP 429: rate limited"),
			want: []string{"模型服务限流（HTTP 429）", "rate limited"},
		},
		{
			name: "http 500 with base url",
			err:  fmt.Errorf("ai: HTTP 502: upstream https://api.internal.example/v1/chat failed"),
			want: []string{"模型服务异常（HTTP 502）"},
			deny: []string{"api.internal.example"},
		},
		{
			name: "timeout",
			err:  context.DeadlineExceeded,
			want: []string{"模型调用超时"},
		},
		{
			name: "reasoning only keeps its own copy",
			err:  ai.ErrReasoningOnlyCompletion,
			want: []string{"只输出了思考过程"},
		},
		{
			name: "empty completion",
			err:  ai.ErrEmptyCompletion,
			want: []string{"空回复"},
		},
		{
			name: "dsn redacted",
			err:  fmt.Errorf("ai: request failed: postgres://user:pass@db:5432/bot?sslmode=disable"),
			want: []string{"数据库"},
			deny: []string{"postgres://", "pass@"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := userFacingAIError(tc.err)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Fatalf("result %q missing %q", got, w)
				}
			}
			for _, d := range tc.deny {
				if strings.Contains(got, d) {
					t.Fatalf("result %q leaks %q", got, d)
				}
			}
		})
	}
}

func TestUserFacingAIErrorNil(t *testing.T) {
	if got := userFacingAIError(nil); got != "" {
		t.Fatalf("nil => %q", got)
	}
}

func TestUserFacingAIErrorTruncates(t *testing.T) {
	err := errors.New("ai: HTTP 400: " + strings.Repeat("原因", 500))
	got := userFacingAIError(err)
	if len([]rune(got)) > 260 {
		t.Fatalf("too long: %d runes", len([]rune(got)))
	}
}
