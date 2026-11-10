package core

import (
	"context"
	"math"
	"math/rand"
	"time"

	"github.com/dynesshely/bot-harness/internal/affect"
	"github.com/dynesshely/bot-harness/internal/settings"
)

// 情绪状态的接入层：只读、只做软约束。所有函数在 d.affect == nil 或服务关闭时
// 都返回"没有变化"，这是默认配置下的零行为差异保证。

// affectObserveTimeout bounds the classification read/write. It is deliberately
// tiny: mood bookkeeping must never make a reply wait on the database.
const affectObserveTimeout = 150 * time.Millisecond

// observeAffect classifies one human message in the background of the turn.
func (d *Dispatcher) observeAffect(ctx context.Context, conversationID, text string) {
	if d.affect == nil || conversationID == "" {
		return
	}
	obsCtx, cancel := context.WithTimeout(ctx, affectObserveTimeout)
	defer cancel()
	if err := d.affect.Observe(obsCtx, conversationID, text, time.Now()); err != nil {
		d.log.Debug("affect observe failed", "component", "affect",
			"conversation", conversationID, "error", err.Error())
	}
}

// appendSystemBlock joins a prompt and an optional block with the same blank
// line the rest of the prompt builder uses. An empty block is a no-op.
func appendSystemBlock(prompt, block string) string {
	if block == "" {
		return prompt
	}
	if prompt == "" {
		return block
	}
	return prompt + "\n\n" + block
}

// affectBlock renders the prompt block, or "" when the feature is unavailable.
func (d *Dispatcher) affectBlock(ctx context.Context, conversationID string, s settings.Settings, cfg settings.GroupConfig, now time.Time) string {
	if d.affect == nil {
		return ""
	}
	return d.affect.Block(ctx, conversationID, s, cfg, now)
}

// affectShortLine is the polish-stage variant: at most one compact line.
func (d *Dispatcher) affectShortLine(ctx context.Context, conversationID string, s settings.Settings, cfg settings.GroupConfig, now time.Time) string {
	if d.affect == nil {
		return ""
	}
	return d.affect.ShortLine(ctx, conversationID, s, cfg, now)
}

// affectBehavior resolves the pacing factors. A nil service yields the identity
// behavior, so callers never need a branch.
func (d *Dispatcher) affectBehavior(ctx context.Context, conversationID string, now time.Time, cfg settings.GroupConfig) affect.Behavior {
	if d.affect == nil {
		return affect.DefaultBehavior()
	}
	// The global switch is the master kill switch; a group override can only
	// narrow it (off / another mode). Without this check, group_overrides[].affect
	// = false would stop injection but still re-shape length, segments, humanize
	// delay and proactive probability.
	if !cfg.AffectEnabled || cfg.AffectInjectMode == affect.ModeOff {
		return affect.DefaultBehavior()
	}
	// Behavior sits on the reply path too, so it gets the same short leash as
	// injection: a slow database degrades to "no adjustment".
	readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	return d.affect.Behavior(readCtx, conversationID, now)
}

// affectSilenceBoost is added to the un-@-ed silence probability.
func (d *Dispatcher) affectSilenceBoost(ctx context.Context, conversationID string, cfg settings.GroupConfig) float64 {
	if d.affect == nil {
		return 0
	}
	return d.affectBehavior(ctx, conversationID, time.Now(), cfg).SilenceBoost
}

// applyAffectBehavior folds the behavior into a **local copy** of the settings
// and humanize level. The live settings document is never touched.
func applyAffectBehavior(s settings.Settings, cfg settings.GroupConfig, b affect.Behavior) (settings.Settings, string) {
	level := humanizeLevel(s, cfg)
	if b.MaxCharsFactor > 0 && b.MaxCharsFactor != 1 {
		s.ReplyMaxChars = int(math.Round(float64(s.ReplyMaxChars) * b.MaxCharsFactor))
		if s.ReplyMaxChars < 40 {
			s.ReplyMaxChars = 40
		}
	}
	if b.MaxSegments > 0 {
		if s.ReplyMaxSegments <= 0 || b.MaxSegments < s.ReplyMaxSegments {
			s.ReplyMaxSegments = b.MaxSegments
		}
	}
	if s.ReplyMaxSegments < 1 {
		s.ReplyMaxSegments = 1
	}
	if b.HumanizeScale > 0 && b.HumanizeScale != 1 {
		scale := s.HumanizeDelayScale * b.HumanizeScale
		if scale < 0.5 {
			scale = 0.5
		}
		if scale > 3 {
			scale = 3
		}
		s.HumanizeDelayScale = scale
	}
	switch b.Tone {
	case "low_battery", "tired", "low":
		// A tired voice should not also carry the "strong" level's long pauses
		// and high silence rate: short and quick is the point.
		if level == HumanizeStrong {
			level = HumanizeMedium
		}
	}
	return s, level
}

// affectProactiveSkip checks the group's conversations. A group can have one
// conversation per member (C1), so any paused or very low state wins: talking
// unprompted while one member's mood is at the floor is exactly the kind of
// mismatch the gate is for.
func (d *Dispatcher) affectProactiveSkip(ctx context.Context, groupOpenID string, now time.Time) (bool, string) {
	if d.affect == nil || d.st == nil || d.settings == nil {
		return false, ""
	}
	cfg := d.settings.GroupConfig(groupOpenID)
	if !cfg.AffectEnabled || cfg.AffectInjectMode == affect.ModeOff {
		return false, ""
	}
	conversations, err := d.st.ConversationsForGroup(ctx, groupOpenID, 20)
	if err != nil {
		d.log.Debug("affect: list conversations for proactive", "component", "affect",
			"group_openid", groupOpenID, "error", err.Error())
		return false, ""
	}
	for _, conversationID := range conversations {
		if skip, why := d.affect.ProactiveSkip(ctx, conversationID, now); skip {
			return true, why
		}
	}
	return false, ""
}

// affectInitiativeFactor scales the proactive credits. It only ever reduces
// them, and 1 (the default) is a no-op.
func affectInitiativeFactor(b affect.Behavior) float64 {
	if b.InitiativeFactor <= 0 || b.InitiativeFactor >= 1 {
		return 1
	}
	return b.InitiativeFactor
}

// affectInitiativeSkip rolls the proactive decision down by the group's lowest
// initiative factor. It is a probability multiplier only: credits, interval and
// quiet hours are untouched.
func (d *Dispatcher) affectInitiativeSkip(ctx context.Context, groupOpenID string, now time.Time) bool {
	if d.affect == nil || d.st == nil || d.settings == nil {
		return false
	}
	cfg := d.settings.GroupConfig(groupOpenID)
	if !cfg.AffectEnabled || cfg.AffectInjectMode == affect.ModeOff {
		return false
	}
	conversations, err := d.st.ConversationsForGroup(ctx, groupOpenID, 20)
	if err != nil {
		d.log.Debug("affect: list conversations for initiative", "component", "affect",
			"group_openid", groupOpenID, "error", err.Error())
		return false
	}
	factor := 1.0
	for _, conversationID := range conversations {
		if f := affectInitiativeFactor(d.affectBehavior(ctx, conversationID, now, cfg)); f < factor {
			factor = f
		}
	}
	if factor >= 1 {
		return false
	}
	return rand.Float64() >= factor
}
