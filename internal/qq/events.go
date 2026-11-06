package qq

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
)

// Event types delivered through both the gateway and the webhook.
const (
	EventReady                = "READY"
	EventResumed              = "RESUMED"
	EventGroupAtMessageCreate = "GROUP_AT_MESSAGE_CREATE"
	EventGroupMessageCreate   = "GROUP_MESSAGE_CREATE"
	EventC2CMessageCreate     = "C2C_MESSAGE_CREATE"
	EventGroupAddRobot        = "GROUP_ADD_ROBOT"
	EventInteractionCreate    = "INTERACTION_CREATE"
)

// OpCode values of the gateway protocol.
const (
	OpDispatch       = 0
	OpHeartbeat      = 1
	OpIdentify       = 2
	OpResume         = 6
	OpReconnect      = 7
	OpInvalidSession = 9
	OpHello          = 10
	OpHeartbeatACK   = 11
	OpCallbackVerify = 13 // webhook URL validation
)

// IntentGroupAndC2C is GROUP_AND_C2C_EVENT: group @-messages and direct messages.
const IntentGroupAndC2C = uint64(1) << 25

// Payload is the outer envelope of every gateway and webhook event.
type Payload struct {
	Op int             `json:"op"`
	S  *int            `json:"s,omitempty"`
	T  string          `json:"t,omitempty"`
	ID string          `json:"id,omitempty"`
	D  json.RawMessage `json:"d,omitempty"`
}

// User is a message author or mention target.
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Bot          bool   `json:"bot"`
	UserOpenID   string `json:"user_openid"`
	MemberOpenID string `json:"member_openid"`
	MemberRole   string `json:"member_role"`
}

// DisplayName prefers the platform nickname, falling back to an openid slice.
func (u User) DisplayName() string {
	if strings.TrimSpace(u.Username) != "" {
		return u.Username
	}
	if u.MemberOpenID != "" {
		return shortID(u.MemberOpenID)
	}
	return shortID(u.ID)
}

// MessageScene carries the platform's per-message index list (`msg_idx=REFIDX_…`),
// which is what a quoted reply has to reference.
type MessageScene struct {
	Ext []string `json:"ext"`
}

// Get returns the value of a key=value entry in Ext.
func (m MessageScene) Get(key string) string {
	prefix := key + "="
	for _, e := range m.Ext {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

// MessageAttachment is an image, voice, video or file attachment.
type MessageAttachment struct {
	URL          string `json:"url"`
	Filename     string `json:"filename"`
	ContentType  string `json:"content_type"`
	Size         int64  `json:"size"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	ASRReferText string `json:"asr_refer_text"`
}

// ARKData is a structured card message.
type ARKData struct {
	Prompt string         `json:"prompt"`
	Fields map[string]any `json:"fields"`
}

// MsgElement is one element of a composite (quoted / merged) message.
type MsgElement struct {
	Author      User                `json:"author"`
	MessageType int                 `json:"message_type"`
	Content     string              `json:"content"`
	Attachments []MessageAttachment `json:"attachments"`
	ARKData     *ARKData            `json:"ark_data"`
	MsgElements []MsgElement        `json:"msg_elements"`
}

// MessageEvent is the body of a group/C2C message event.
type MessageEvent struct {
	ID           string              `json:"id"`
	Content      string              `json:"content"`
	GroupOpenID  string              `json:"group_openid"`
	Author       User                `json:"author"`
	Timestamp    string              `json:"timestamp"`
	MessageType  int                 `json:"message_type"`
	MessageScene MessageScene        `json:"message_scene"`
	Attachments  []MessageAttachment `json:"attachments"`
	Mentions     []User              `json:"mentions"`
	ARKData      *ARKData            `json:"ark_data"`
	MsgElements  []MsgElement        `json:"msg_elements"`
}

// mentionMarkup matches the "<@OPENID>"/"<@!OPENID>" tokens QQ leaves in the
// content of full-group messages. The platform only strips the @bot prefix for
// GROUP_AT_MESSAGE_CREATE, so these must be removed here or the model ends up
// reading raw openids.
var mentionMarkup = regexp.MustCompile(`<@!?[0-9A-Za-z_\-]+>`)

// StripMentionMarkup removes @ mention markup and tidies the spacing.
func StripMentionMarkup(s string) string {
	if !strings.Contains(s, "<@") {
		return strings.TrimSpace(s)
	}
	cleaned := mentionMarkup.ReplaceAllString(s, " ")
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	return strings.TrimSpace(cleaned)
}

// Text returns the best-effort plain text of the message.
func (e *MessageEvent) Text() string {
	if t := StripMentionMarkup(e.Content); t != "" {
		return t
	}
	// Quoted or merged messages carry their payload in msg_elements.
	var parts []string
	for _, el := range e.MsgElements {
		if s := strings.TrimSpace(el.Content); s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) > 0 {
		return StripMentionMarkup(strings.Join(parts, "\n"))
	}
	if e.ARKData != nil && strings.TrimSpace(e.ARKData.Prompt) != "" {
		return strings.TrimSpace(e.ARKData.Prompt)
	}
	if len(e.Attachments) > 0 {
		return "[附件消息]"
	}
	return ""
}

// ErrNotMessageEvent is returned when a payload is not a chat message.
var ErrNotMessageEvent = errors.New("payload is not a message event")

// DecodeMessage extracts a MessageEvent from an envelope whose T field is one
// of the message event types.
func DecodeMessage(p *Payload) (*MessageEvent, error) {
	switch p.T {
	case EventGroupAtMessageCreate, EventGroupMessageCreate, EventC2CMessageCreate:
	default:
		return nil, ErrNotMessageEvent
	}
	var ev MessageEvent
	if err := json.Unmarshal(p.D, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

// Scene classifies a message event into a conversation scene.
func Scene(p *Payload) string {
	switch p.T {
	case EventGroupAtMessageCreate, EventGroupMessageCreate:
		return "group"
	case EventC2CMessageCreate:
		return "c2c"
	default:
		return ""
	}
}

func shortID(s string) string {
	r := []rune(s)
	if len(r) <= 8 {
		return s
	}
	return string(r[:8])
}
