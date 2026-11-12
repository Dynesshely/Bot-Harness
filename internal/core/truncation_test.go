package core

import (
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/ai"
)

func TestNeedsCompletion(t *testing.T) {
	cases := []struct {
		name string
		res  *ai.ChatResult
		want bool
	}{
		{"length finish", &ai.ChatResult{Text: "说到一半", FinishReason: "length"}, true},
		{"stop but mid-sentence", &ai.ChatResult{Text: strings.Repeat("这是一句没有句号的话", 8) + "压根", FinishReason: "stop"}, true},
		{"long but terminal", &ai.ChatResult{Text: strings.Repeat("这是一句完整的话。", 8), FinishReason: "stop"}, false},
		{"short chat", &ai.ChatResult{Text: "吓哭了", FinishReason: "stop"}, false},
		{"emoji ending", &ai.ChatResult{Text: strings.Repeat("今天也很开心", 8) + "😄", FinishReason: "stop"}, false},
		{"unclosed fence", &ai.ChatResult{Text: "看代码\n```go\nfmt.Println(1)", FinishReason: "stop"}, true},
		{"closed fence", &ai.ChatResult{Text: "看代码\n```go\nfmt.Println(1)\n```\n就这样。", FinishReason: "stop"}, false},
		{"empty", &ai.ChatResult{Text: "  ", FinishReason: "length"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsCompletion(tc.res); got != tc.want {
				t.Fatalf("needsCompletion=%v want %v (%q)", got, tc.want, tc.res.Text)
			}
		})
	}
}

func TestCompletionRetryTokens(t *testing.T) {
	for _, tc := range []struct{ in, want int }{{0, 2048}, {512, 2048}, {1024, 2048}, {4096, 8192}, {10000, 8192}} {
		if got := completionRetryTokens(tc.in); got != tc.want {
			t.Fatalf("completionRetryTokens(%d)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestPolishLooksCutOff(t *testing.T) {
	draft := "这是一段完整的话，润色之后不应该少掉最后一句。"
	if !polishLooksCutOff(draft, "这是一段完整的话") {
		t.Fatal("shorter unpunctuated polish should be rejected")
	}
	if polishLooksCutOff(draft, "这是一段完整的话，润色之后不应该少掉最后一句。") {
		t.Fatal("complete polish should pass")
	}
	if !polishLooksCutOff(draft, "```go\nfmt.Println(1)") {
		t.Fatal("unclosed fence should be rejected")
	}
}
