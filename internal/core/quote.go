package core

import "github.com/dynesshely/bot-harness/internal/settings"

// 引用回复的频率控制。
//
// 以前 quote_reply 是一个布尔：要么每条都引用，要么一条都不引用。每条都引用
// 会让回复显得机械（每条下面都挂一个引用块），于是按 MaiBot 的「引用频率分档」
// 改成三档概率：never / occasionally(~30%) / often(~70%)。
//
// 只作用于**正式答复**：即时回复（「让我多想会儿」）、错误兜底、手动发送时显式
// 选择的引用都不经过这里 —— 那些要么是占位语，要么是操作者的明确意图。

// shouldQuote decides whether this answer should quote the triggering message.
// The roll is injectable (Dispatcher.quoteRoll) so tests can pin a band.
func (d *Dispatcher) shouldQuote(mode string, legacy bool) bool {
	if mode == "" {
		return legacy
	}
	rate := settings.QuoteReplyRate(mode)
	switch {
	case rate <= 0:
		return false
	case rate >= 1:
		return true
	default:
		return d.rollQuote() < rate
	}
}
