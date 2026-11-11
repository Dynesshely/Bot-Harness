package core

import (
	"strings"
	"testing"
	"time"
)

func TestSplitBubblesShortTextStaysWhole(t *testing.T) {
	if got := splitBubbles("好的", "medium", 1200, 3, func() float64 { return 0 }); len(got) != 1 || got[0] != "好的" {
		t.Fatalf("short text should stay one bubble: %#v", got)
	}
}

func TestSplitBubblesOffStaysWhole(t *testing.T) {
	text := strings.Repeat("这是一句完整的话。", 10)
	if got := splitBubbles(text, "off", 1200, 3, func() float64 { return 0 }); len(got) != 1 {
		t.Fatalf("off should not split: %#v", got)
	}
}

func TestSplitBubblesLongTextSplits(t *testing.T) {
	text := "第一句话讲背景。第二句话讲原因。第三句话给结论。第四句话补充说明。"
	got := splitBubbles(text, "strong", 1200, 3, func() float64 { return 0 })
	if len(got) != 3 {
		t.Fatalf("expected 3 bubbles, got %d: %#v", len(got), got)
	}
	joined := strings.Join(got, "")
	if strings.ReplaceAll(joined, " ", "") != strings.ReplaceAll(text, " ", "") {
		t.Fatalf("bubbles lost content: %q vs %q", joined, text)
	}
}

func TestSplitBubblesKeepsCodeAndLinksWhole(t *testing.T) {
	for _, text := range []string{
		"看这个\n```go\nfmt.Println(1)\n```\n就明白了。",
		"参考 https://example.com/a/very/long/path 这里。",
		"> 引用一段很长的话。\n\n然后继续。",
	} {
		if got := splitBubbles(text, "strong", 1200, 3, func() float64 { return 0 }); len(got) != 1 {
			t.Fatalf("must not split %q: %#v", text, got)
		}
	}
}

func TestSplitBubblesRespectsMaxSegments(t *testing.T) {
	text := strings.Repeat("一句话。", 30)
	got := splitBubbles(text, "strong", 1200, 2, func() float64 { return 0 })
	if len(got) > 2 {
		t.Fatalf("maxSegments=2 violated: %d", len(got))
	}
}

func TestTypingPauseWeightsChinese(t *testing.T) {
	zh := typingPause("你好世界", 0.3, 1)
	en := typingPause("abcd", 0.3, 1)
	if zh <= en {
		t.Fatalf("Chinese should take longer: zh=%v en=%v", zh, en)
	}
	if one := typingPause("好", 0.3, 1); one != 900*time.Millisecond {
		t.Fatalf("single char should be tripled: %v", one)
	}
	if typingPause("你好", 0, 1) != 0 {
		t.Fatal("perChar=0 should mean no wait")
	}
}
