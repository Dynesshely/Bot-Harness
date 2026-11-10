package affect

import (
	"strings"
	"unicode/utf8"
)

// MaxClassifyRunes is the hard ceiling for event classification. Longer texts
// are articles or pasted logs, not a passing remark: classifying them would let
// one message rewrite the whole state.
const MaxClassifyRunes = 120

// Negation, hypothetical and question gates are applied in code rather than in
// every pattern, so a new pack cannot forget them. They are the difference
// between "真厉害" (praise) and "如果厉害的话" / "厉害吗" (not a compliment) /
// "不厉害" (the opposite).
var (
	negationMarkers = []string{
		"不", "没", "别", "无", "非", "未", "勿", "不是", "没有", "不要",
		"不用", "并不", "从不", "毫不", "谈不上", "算不上",
	}
	hypotheticalMarkers = []string{
		"如果", "假如", "假使", "要是", "假设", "万一", "若果", "倘若", "会不会", "是不是", "难道",
	}
)

// Classify returns the events whose patterns match the message and survive the
// gates. An event appears at most once per message. The returned pointers point
// into the pack and must be treated as read-only.
func Classify(p *Pack, message string) []*EventDef {
	text := strings.TrimSpace(message)
	if p == nil || text == "" {
		return nil
	}
	if utf8.RuneCountInString(text) > MaxClassifyRunes {
		return nil
	}
	runes := []rune(text)
	question := looksLikeQuestion(runes)

	var out []*EventDef
	for i := range p.Events {
		ev := &p.Events[i]
		matched := false
		for _, re := range ev.compiled {
			for _, loc := range re.FindAllStringIndex(text, -1) {
				start := utf8.RuneCountInString(text[:loc[0]])
				length := utf8.RuneCountInString(text[loc[0]:loc[1]])
				if gated(runes, start, length, question) {
					continue
				}
				matched = true
				break
			}
			if matched {
				break
			}
		}
		if matched {
			out = append(out, ev)
		}
	}
	return out
}

// gated reports whether the match sits inside a negated, hypothetical or
// question context.
func gated(runes []rune, start, length int, question bool) bool {
	if question {
		return true
	}
	left := start - 12
	if left < 0 {
		left = 0
	}
	right := start + length + 8
	if right > len(runes) {
		right = len(runes)
	}
	window := string(runes[left:right])
	for _, marker := range negationMarkers {
		if strings.Contains(window, marker) {
			return true
		}
	}
	for _, marker := range hypotheticalMarkers {
		if strings.Contains(window, marker) {
			return true
		}
	}
	return false
}

// looksLikeQuestion detects the common ways a Chinese chat message asks rather
// than states. It is intentionally conservative about "吗": a trailing "吗"
// really is a question, while the character in the middle of a word is not
// treated as one.
func looksLikeQuestion(runes []rune) bool {
	for len(runes) > 0 && (runes[len(runes)-1] == ' ' || runes[len(runes)-1] == '\n') {
		runes = runes[:len(runes)-1]
	}
	if len(runes) == 0 {
		return false
	}
	switch runes[len(runes)-1] {
	case '?', '？':
		return true
	}
	trimmed := string(runes)
	for _, suffix := range []string{"吗", "呢", "么"} {
		if strings.HasSuffix(trimmed, suffix) {
			return true
		}
	}
	return false
}
