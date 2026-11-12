package core

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/dynesshely/bot-harness/internal/ai"
)

// 推理模型会把 max_tokens 预算花在 reasoning_content 上：最终 content 可能
// 在句子中间停止，stream 以 finish_reason=length 结束。直接把这段发出去，
// 用户看到的就是「说一半不说了」。
//
// 这里只做两件事：识别「没写完」，以及给一次重试算更大的输出预算。识别必须保守，
// 正常短回复（「吓哭了」「ciallo」）不能因为没句号就被重试。

const (
	// truncatedRunes is the minimum length before an unpunctuated ending is
	// treated as suspicious. Short chat replies routinely end without 。
	truncatedRunes = 60
	// minCompletionRetryTokens / maxCompletionRetryTokens bound the one retry.
	minCompletionRetryTokens = 2048
	maxCompletionRetryTokens = 8192
)

// needsCompletion reports whether a model result should be retried with a
// larger output budget. finish_reason=length is authoritative; the text
// heuristics cover providers that omit it.
func needsCompletion(res *ai.ChatResult) bool {
	if res == nil || strings.TrimSpace(res.Text) == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(res.FinishReason), "length") {
		// A provider can report length even when the final line happened to end
		// cleanly; only retry when the text itself still looks unfinished.
		return !endsWithTerminal(res.Text) || strings.Count(res.Text, "```")%2 != 0
	}
	return looksCutOff(res.Text)
}

// looksCutOff catches the common shapes of a mid-sentence stop. It deliberately
// errs on the side of "complete": a missed retry only loses a nicer ending,
// while a false positive spends an extra model call on every chatty reply.
func looksCutOff(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	// An odd number of fences means the code block was cut open.
	if strings.Count(t, "```")%2 != 0 {
		return true
	}
	last, _ := utf8.DecodeLastRuneInString(t)
	if endsWithTerminal(t) {
		return false
	}
	if utf8.RuneCountInString(t) < truncatedRunes {
		return false
	}
	return unicode.Is(unicode.Han, last) || unicode.IsLetter(last) || unicode.IsDigit(last)
}

// endsWithTerminal reports whether text ends at a natural boundary. Emoji and
// closing quotes count: they are often how a chat reply ends.
func endsWithTerminal(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return true
	}
	last, _ := utf8.DecodeLastRuneInString(t)
	return strings.ContainsRune("。！？!?…～~.，,、；;：:）)]】」』”’\"'", last) ||
		unicode.Is(unicode.So, last)
}

// completionRetryTokens doubles the configured budget for the single retry,
// bounded so a bad provider response cannot turn into an unbounded bill.
func completionRetryTokens(current int) int {
	budget := current * 2
	if budget < minCompletionRetryTokens {
		budget = minCompletionRetryTokens
	}
	if budget > maxCompletionRetryTokens {
		budget = maxCompletionRetryTokens
	}
	return budget
}

// polishLooksCutOff is stricter than looksCutOff: stage 2 is only rewriting a
// draft that is already complete, so any unpunctuated ending that is shorter
// than the draft (or an odd fence) means the rewrite lost the tail.
func polishLooksCutOff(draft, polished string) bool {
	p := strings.TrimSpace(polished)
	if p == "" {
		return true
	}
	if strings.Count(p, "```")%2 != 0 {
		return true
	}
	if endsWithTerminal(p) {
		return false
	}
	// The draft ended cleanly but the rewrite did not: it cut the sentence.
	if endsWithTerminal(draft) && !endsWithTerminal(p) {
		return true
	}
	return utf8.RuneCountInString(p) < utf8.RuneCountInString(draft)/2
}
