package affect

import (
	"strings"
	"testing"
	"time"
)

func TestBuildBlockModes(t *testing.T) {
	p := DefaultPack()
	dims := BaselineDims(p)
	dims["valence"] = 15
	dims["energy"] = 20
	now := time.Date(2026, 1, 1, 23, 30, 0, 0, time.Local)
	marks := []EventMark{{ID: "praise", At: now.Add(-10 * time.Minute).Format(time.RFC3339)}}
	b := BehaviorFor(p, dims)

	if got := BuildBlock(p, dims, marks, ModeOff, 120, b, now); got != "" {
		t.Fatalf("off mode must return empty, got %q", got)
	}
	line := BuildBlock(p, dims, marks, ModeLine, 120, b, now)
	if line == "" {
		t.Fatal("line mode should render")
	}
	if approxTokens(line) > lineMaxTokens {
		t.Fatalf("line is %d tokens, want <= %d: %q", approxTokens(line), lineMaxTokens, line)
	}
	if strings.Contains(line, "\n") {
		t.Fatalf("line must be one line: %q", line)
	}

	full := BuildBlock(p, dims, marks, ModeFull, 120, b, now)
	if full == "" {
		t.Fatal("full mode should render")
	}
	if approxTokens(full) > defaultFullTokens {
		t.Fatalf("full is %d tokens, want <= %d", approxTokens(full), defaultFullTokens)
	}
	if !strings.Contains(full, "自述") {
		t.Fatalf("full mode should carry the self-talk: %q", full)
	}
	if strings.ContainsAny(full, "0123456789") {
		t.Fatalf("injection must not contain numbers: %q", full)
	}
	// The residue has the lowest priority: it appears when the budget allows.
	roomy := BuildBlock(p, dims, marks, ModeFull, 300, b, now)
	if !strings.Contains(roomy, "被夸") {
		t.Fatalf("a roomy full block should mention the recent event residue: %q", roomy)
	}
}

func TestBuildBlockRespectsTokenBudget(t *testing.T) {
	p := DefaultPack()
	dims := BaselineDims(p)
	dims["valence"] = 10
	dims["energy"] = 10
	now := time.Now()
	got := BuildBlock(p, dims, nil, ModeFull, 40, BehaviorFor(p, dims), now)
	if approxTokens(got) > 40 {
		t.Fatalf("max tokens not respected: %d > 40 (%q)", approxTokens(got), got)
	}
}

func TestBuildBlockIgnoresOldEvents(t *testing.T) {
	p := DefaultPack()
	now := time.Now()
	old := now.Add(-2 * time.Hour).Format(time.RFC3339)
	got := BuildBlock(p, BaselineDims(p), []EventMark{{ID: "argue", At: old}}, ModeFull, 120, DefaultBehavior(), now)
	if strings.Contains(got, "争执") {
		t.Fatalf("an event older than 30 minutes must not colour the block: %q", got)
	}
}

func TestSanitizeStripsStructure(t *testing.T) {
	got := sanitize("第一行\n第二行 「引用」 \"双引号\" (括号) [方括号]")
	if strings.ContainsAny(got, "\n「」\"()[]") {
		t.Fatalf("sanitize left structure behind: %q", got)
	}
	long := sanitize(strings.Repeat("字", 100))
	if n := len([]rune(long)); n > 40 {
		t.Fatalf("sanitize should cap at 40 runes, got %d", n)
	}
	if approxTokens(long) > approxTokens(strings.Repeat("字", 40)) {
		t.Fatalf("sanitize grew the text: %q", long)
	}
}

func TestBehaviorConstraintAndTruncate(t *testing.T) {
	if got := behaviorConstraint(DefaultBehavior()); got != "" {
		t.Fatalf("default behavior should add no constraint, got %q", got)
	}
	b := Behavior{MaxCharsFactor: 0.6, MaxSegments: 1, HumanizeScale: 1.3, SilenceBoost: 0.1}
	if got := behaviorConstraint(b); got == "" {
		t.Fatal("a constrained behavior should render a constraint")
	}
	if got := truncateTokens("一二三四五六七八九十", 3); approxTokens(got) > 3 {
		t.Fatalf("truncateTokens exceeded the budget: %q", got)
	}
}
