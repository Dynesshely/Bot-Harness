package settings

import "testing"

// 默认值必须真的能「像人」：打字上限若落到 0（不等待），分段气泡就没有节奏感，
// 而且后台看到的默认值与预期不符。
func TestHumanityDefaultsAreUsable(t *testing.T) {
	d := Defaults()
	if d.HumanizeSegmentSplit != "medium" {
		t.Fatalf("segment split default = %q, want medium", d.HumanizeSegmentSplit)
	}
	if d.HumanizeTypingSecondsPerChar < 0.2 || d.HumanizeTypingSecondsPerChar > 0.5 {
		t.Fatalf("typing seconds default = %v", d.HumanizeTypingSecondsPerChar)
	}
	if d.HumanizeTypingMaxSeconds <= 0 {
		t.Fatalf("typing max seconds default = %d, want > 0", d.HumanizeTypingMaxSeconds)
	}
	if d.ReplyDebounceMode != "natural" || d.ReplyDebounceQuietSeconds != 2 || d.ReplyDebounceActiveSeconds != 5 {
		t.Fatalf("debounce defaults = %q/%d/%d", d.ReplyDebounceMode, d.ReplyDebounceQuietSeconds, d.ReplyDebounceActiveSeconds)
	}
	if d.QuoteReplyMode != QuoteReplyOccasionally || !d.QuoteReply {
		t.Fatalf("quote defaults = %q/%v", d.QuoteReplyMode, d.QuoteReply)
	}
}
