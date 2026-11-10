package affect

import "testing"

func eventIDs(events []*EventDef) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		out = append(out, ev.ID)
	}
	return out
}

func hasEvent(events []*EventDef, id string) bool {
	for _, ev := range events {
		if ev.ID == id {
			return true
		}
	}
	return false
}

func TestClassifyMatchesSafeEvents(t *testing.T) {
	p := DefaultPack()
	cases := map[string]string{
		"这也太厉害了吧": "praise",
		"谢谢你帮我":   "thanks",
		"哈哈哈哈笑死":  "joke",
		"今天好累啊":   "complaint",
	}
	for text, want := range cases {
		events := Classify(p, text)
		if !hasEvent(events, want) {
			t.Fatalf("%q -> %v, want event %q", text, eventIDs(events), want)
		}
	}
}

func TestClassifyGatesNegationHypothesisAndQuestion(t *testing.T) {
	p := DefaultPack()
	for _, text := range []string{
		"不厉害",
		"没有很厉害",
		"如果厉害的话",
		"要是真棒就好了",
		"厉害吗？",
		"这也算厉害？",
	} {
		if events := Classify(p, text); len(events) != 0 {
			t.Fatalf("%q should be gated, got %v", text, eventIDs(events))
		}
	}
}

func TestClassifyRejectsLongMessages(t *testing.T) {
	p := DefaultPack()
	long := ""
	for i := 0; i < MaxClassifyRunes+1; i++ {
		long += "厉害"
	}
	if events := Classify(p, long); len(events) != 0 {
		t.Fatalf("%d-rune message must not be classified, got %v", len([]rune(long)), eventIDs(events))
	}
}

func TestClassifyReturnsEachEventOnce(t *testing.T) {
	p := DefaultPack()
	events := Classify(p, "厉害厉害真棒真棒")
	seen := map[string]int{}
	for _, ev := range events {
		seen[ev.ID]++
	}
	for id, n := range seen {
		if n > 1 {
			t.Fatalf("event %q returned %d times", id, n)
		}
	}
}
