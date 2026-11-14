package core

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/dynesshely/bot-harness/internal/ai"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// Conversation scenes as exposed by the admin API. The values live in the
// store package so merging and storage agree on them.
const (
	SceneGroupMember = store.SceneGroupMember
	SceneGroupShared = store.SceneGroupShared
	SceneC2C         = store.SceneC2C
)

// Inbound is a normalised inbound chat message.
type Inbound struct {
	EventType string
	MsgID     string // d.id, used as the passive reply target
	EventID   string // envelope id, used for event-based replies
	// RequestID is the stable per-event id used in logs and metrics. It is the
	// platform envelope id, falling back to the message id.
	RequestID    string
	Scene        string // "group" or "c2c"
	GroupOpenID  string
	MemberOpenID string
	UserOpenID   string
	MemberName   string
	Content      string
	// QuotedText is the quoted message's text, if the platform supplied it in
	// msg_elements. It is model input only; the stored user message stays clean.
	QuotedText string
	// AtBot reports that this message is addressed to the bot: either a
	// GROUP_AT_MESSAGE_CREATE, or a full-group message that @s it.
	AtBot bool
	// MentionedBot is the raw "the platform listed a bot in mentions" flag.
	MentionedBot bool
	// SelfMentioned reports that one of those mentions was this bot itself.
	SelfMentioned bool
	// MentionCount / MentionedBots are kept for diagnostics in the audit trail.
	MentionCount  int
	MentionedBots int
	// QuoteIndex is this message's platform reference index
	// (message_scene.ext[msg_idx], a REFIDX_… token). Replying with it set as
	// message_reference makes the answer a quoted reply in QQ.
	QuoteIndex string
	// RefMsgIndex is the message this one quotes (message_scene.ref_msg_idx).
	// The platform only hands over the REFIDX_… token, so it has to be matched
	// against stored quote indexes before it means anything.
	RefMsgIndex string
	// ReplyToBot reports that this message quotes one of the bot's own
	// messages in this group. It is derived, not platform-provided: a hit makes
	// the message as good as an @ (AtBot=true), but it is tracked separately so
	// the reason a reply happened stays visible in the logs.
	ReplyToBot bool
	// Attachments are the message's files; images are already cached locally so
	// the timeline keeps rendering after the platform URL expires. Quoted and
	// merged messages contribute their attachments too, so the timeline (and the
	// next turn's context) keeps them.
	Attachments []store.Attachment
	// MediaRefs records where each cached image came from and in which order it
	// appeared. It is deliberately separate from Attachments: the text the model
	// sees needs the current|quoted distinction, while the store keeps a flat
	// attachment list.
	MediaRefs []MediaRef
}

// Media sources for Inbound.MediaRefs.
const (
	MediaSourceCurrent = "current"
	MediaSourceQuoted  = "quoted"
)

// MediaRef is one model-visible cached image reference. The annotation in the
// user text is derived from these, never from the raw platform payload.
type MediaRef struct {
	MediaID string
	Source  string // MediaSourceCurrent | MediaSourceQuoted
	Kind    string // "image" when the cache holds a usable image
}

type convTarget struct {
	ID           string
	Scene        string // group_member | group_shared | c2c
	GroupOpenID  string
	MemberOpenID string
}

// resolveConversation maps an inbound message onto its context scope, applying
// decision D-003 (C1 per member by default, C2 shared when permitted).
func resolveConversation(in *Inbound, contextMode string) convTarget {
	if in.Scene == "c2c" {
		key := "c2c:" + in.UserOpenID
		// A C2C chat has no member_openid; the user_openid is the person (and the
		// address a manual send has to use), so it is stored as the member.
		return convTarget{ID: EncodeConversationID(key), Scene: SceneC2C, MemberOpenID: in.UserOpenID}
	}
	if contextMode == settings.ContextShared {
		return convTarget{
			ID:          EncodeConversationID("group-shared:" + in.GroupOpenID),
			Scene:       SceneGroupShared,
			GroupOpenID: in.GroupOpenID,
		}
	}
	return convTarget{
		ID:           EncodeConversationID("group:" + in.GroupOpenID + ":" + in.MemberOpenID),
		Scene:        SceneGroupMember,
		GroupOpenID:  in.GroupOpenID,
		MemberOpenID: in.MemberOpenID,
	}
}

// EncodeConversationID renders an opaque, URL-safe conversation id.
func EncodeConversationID(key string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(key))
}

// DecodeConversationID is the inverse of EncodeConversationID.
func DecodeConversationID(id string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(id)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// EstimateTokens approximates a token count: roughly one token per CJK rune and
// one per four ASCII characters. It only needs to be good enough for trimming.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	cjk, other := 0, 0
	for _, r := range s {
		switch {
		case r >= 0x2E80: // CJK, kana, hangul and friends
			cjk++
		case r == ' ' || r == '\n' || r == '\t' || r == '\r':
			other++
		default:
			other++
		}
	}
	return cjk + (other+3)/4
}

// sessionIdentityBlock tells the model which conversation it is in. The admin
// panel addresses groups by OpenID, so when an operator says "change this group's
// setting" the model must be able to hand settings_write the right id instead of
// guessing or asking for it.
//
// Group chats expose the group OpenID; c2c exposes the user OpenID. If neither is
// present (a malformed event) the block is omitted rather than printing an empty
// key.
func sessionIdentityBlock(in *Inbound) string {
	if in == nil {
		return ""
	}
	if in.Scene == "c2c" {
		id := firstNonEmpty(in.UserOpenID, in.MemberOpenID)
		if id == "" {
			return ""
		}
		return fmt.Sprintf("【当前会话】场景：私聊；用户 OpenID：%s。\n"+
			"管理员让你调整本会话相关的设置时，用这个 OpenID 作为标识。", id)
	}
	if in.GroupOpenID == "" {
		return ""
	}
	return fmt.Sprintf("【当前会话】场景：群聊；群 OpenID：%s。\n"+
		"管理员让你调整本群（或某个群）的设置时，用这个 OpenID 作为标识。", in.GroupOpenID)
}

// effectiveContextLimit is the smaller of the configured logical budget and
// the provider context window, so a 1M setting cannot be sent to a 128k model.
func effectiveContextLimit(s settings.Settings) int {
	limit := s.ModelContextTokens
	if limit <= 0 || (s.ContextMaxTokens > 0 && limit > s.ContextMaxTokens) {
		limit = s.ContextMaxTokens
	}
	if limit <= 0 {
		return 256000
	}
	return limit
}

// BuildChatMessages assembles the model input: a system prompt plus the most
// recent history that fits the token budget.
func BuildChatMessages(systemPrompt string, history []store.Message, userText string, budget int) []ai.Message {
	out := make([]ai.Message, 0, len(history)+2)
	if strings.TrimSpace(systemPrompt) != "" {
		out = append(out, ai.Message{Role: ai.RoleSystem, Content: systemPrompt})
	}

	spent := EstimateTokens(systemPrompt) + EstimateTokens(userText)
	if budget <= 0 {
		budget = 6000
	}

	kept := make([]ai.Message, 0, len(history))
	for i := len(history) - 1; i >= 0; i-- {
		m := history[i]
		content := m.Content
		// 旁观消息（未 @ 机器人的群聊 / 其他机器人发言）带昵称前缀，
		// 否则模型看到一串没有归属的 user 消息。
		if m.Kind == store.KindObserved || m.Kind == store.KindObservedBot {
			who := m.Speaker
			if who == "" {
				who = "群成员"
			}
			content = fmt.Sprintf("%s：%s", who, content)
		}
		cost := EstimateTokens(content) + 4
		if spent+cost > budget {
			break
		}
		spent += cost
		role := ai.RoleUser
		if m.Role == "assistant" {
			role = ai.RoleAssistant
		}
		kept = append(kept, ai.Message{Role: role, Content: content})
	}
	// kept is newest-first; restore chronological order.
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	out = append(out, kept...)
	out = append(out, ai.Message{Role: ai.RoleUser, Content: userText})
	return out
}
