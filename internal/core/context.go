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
	EventType    string
	MsgID        string // d.id, used as the passive reply target
	EventID      string // envelope id, used for event-based replies
	Scene        string // "group" or "c2c"
	GroupOpenID  string
	MemberOpenID string
	UserOpenID   string
	MemberName   string
	Content      string
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
	// Attachments are the message's files; images are already cached locally so
	// the timeline keeps rendering after the platform URL expires.
	Attachments []store.Attachment
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
