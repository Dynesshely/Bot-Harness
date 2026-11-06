package qq

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSplitTextShortInputIsUnchanged(t *testing.T) {
	text := "你好，世界"
	got := SplitText(text, 100)
	if len(got) != 1 || got[0] != text {
		t.Fatalf("expected the text unchanged, got %#v", got)
	}
}

func TestSplitTextEmptyReturnsNil(t *testing.T) {
	if got := SplitText("   \n\n  ", 100); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestSplitTextRespectsMaxChars(t *testing.T) {
	var paragraphs []string
	for i := 0; i < 20; i++ {
		paragraphs = append(paragraphs, strings.Repeat("段落内容", 8))
	}
	text := strings.Join(paragraphs, "\n\n")

	const max = 100
	chunks := SplitText(text, max)
	if len(chunks) < 2 {
		t.Fatalf("expected the text to be split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if n := utf8.RuneCountInString(c); n > max {
			t.Fatalf("chunk %d has %d runes, limit is %d: %q", i, n, max, c)
		}
		if strings.TrimSpace(c) == "" {
			t.Fatalf("chunk %d is blank", i)
		}
	}
}

func TestSplitTextHardWrapsLongLine(t *testing.T) {
	line := strings.Repeat("a", 500)
	chunks := SplitText(line, 120)
	if len(chunks) < 4 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for _, c := range chunks {
		if utf8.RuneCountInString(c) > 120 {
			t.Fatalf("chunk too long: %d", utf8.RuneCountInString(c))
		}
	}
	if joined := strings.Join(chunks, ""); joined != line {
		t.Fatalf("hard wrap lost content: %d vs %d bytes", len(joined), len(line))
	}
}

func TestSplitTextBalancesCodeFences(t *testing.T) {
	body := strings.Repeat("代码行内容\n", 40)
	text := "说明\n\n```go\n" + body + "```\n\n结束"

	chunks := SplitText(text, 120)
	if len(chunks) < 2 {
		t.Fatalf("expected the fence block to be split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if strings.Count(c, "```")%2 != 0 {
			t.Fatalf("chunk %d has an unbalanced code fence:\n%s", i, c)
		}
	}
}

func TestSplitTextKeepsSentenceBoundaries(t *testing.T) {
	text := strings.Repeat("这是一个句子。", 40)
	chunks := SplitText(text, 60)
	for _, c := range chunks {
		if !strings.HasSuffix(c, "。") {
			t.Fatalf("expected the chunk to end on a sentence boundary: %q", c)
		}
	}
}

func TestStripMentionMarkup(t *testing.T) {
	cases := map[string]string{
		"<@07BDF821828D4CB952A249FF4BC3A17D> 你好，你是谁呀": "你好，你是谁呀",
		"<@!07BDF821828D4CB952A249FF4BC3A17D>你好":      "你好",
		"普通消息，没有 at":                                  "普通消息，没有 at",
		"@某人 <@ABC123> 你看这个 <@DEF456> 对不对":            "@某人 你看这个 对不对",
		"  前后有空白  ":                                   "前后有空白",
	}
	for in, want := range cases {
		if got := StripMentionMarkup(in); got != want {
			t.Fatalf("StripMentionMarkup(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMessageEventTextStripsMentions(t *testing.T) {
	ev := &MessageEvent{Content: "<@07BDF821828D4CB952A249FF4BC3A17D> 帮我看看这段代码"}
	if got := ev.Text(); got != "帮我看看这段代码" {
		t.Fatalf("Text() = %q", got)
	}
}
