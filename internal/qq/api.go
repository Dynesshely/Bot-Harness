package qq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Platform error codes we react to explicitly.
const (
	ErrMsgIDExpired    = 304103   // 消息 ID 已过期，不能回复
	ErrReplyExpired    = 40034005 // 回复消息 msg_id 已过期
	ErrPassiveExceeded = 40034128 // 被动回复时间或次数超限
	ErrDuplicateMsg    = 40054005 // 消息被去重（相同 msg_id + msg_seq）
	ErrActiveRateLimit = 40034100 // 主动消息发送超过频控限制
	ErrContentTooLong  = 40054007 // 消息长度超限
	ErrURLNotAllowed   = 40054010 // 不允许发送 URL
	ErrNotGroupMember  = 40034101 // 机器人非群成员
	ErrBotMuted        = 40054002 // 机器人被禁言
	ErrContentBlocked  = 40034006 // 消息内容违规
	// Markdown 相关：权限未申请，或 markdown 正文不合法。
	ErrMarkdownNoPerm         = 304036
	ErrMarkdownTemplateNoPerm = 40034127
	ErrMarkdownParam          = 40034008
	ErrMarkdownParamNewline   = 40034009
	ErrMarkdownParamMarkdown  = 40034010
	ErrMarkdownParamInvalid   = 40034011
	ErrMarkdownParamBad       = 40034124

	ErrRecallExpired = 40064004 // 已超出消息撤回时限（2 分钟）
	ErrRecallNoPerm  = 40062003 // 无操作权限
)

// APIError is a non-2xx or business-error response from the open platform.
type APIError struct {
	HTTPStatus int
	Code       int
	Message    string
	Path       string
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("qq api %s: HTTP %d code=%d message=%s", e.Path, e.HTTPStatus, e.Code, e.Message)
	}
	return fmt.Sprintf("qq api %s: HTTP %d message=%s", e.Path, e.HTTPStatus, e.Message)
}

// CodeIs reports whether the error carries any of the given platform codes.
func CodeIs(err error, codes ...int) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, c := range codes {
		if apiErr.Code == c {
			return true
		}
	}
	return false
}

// IsPassiveExpired reports that the passive reply window closed.
func IsPassiveExpired(err error) bool {
	return CodeIs(err, ErrMsgIDExpired, ErrReplyExpired, ErrPassiveExceeded)
}

// IsDuplicate reports the platform rejected a duplicate msg_id+msg_seq pair,
// which means the reply was already delivered.
func IsDuplicate(err error) bool {
	return CodeIs(err, ErrDuplicateMsg)
}

// IsRecallExpired reports that a message is too old to retract.
func IsRecallExpired(err error) bool {
	return CodeIs(err, ErrRecallExpired)
}

// IsMarkdownUnavailable reports that the platform would not take a markdown
// message: no template permission (304036 / 40034127) or a rejected markdown
// body (4003xxxx). Either way the answer should be resent as plain text.
func IsMarkdownUnavailable(err error) bool {
	return CodeIs(err, ErrMarkdownNoPerm, ErrMarkdownTemplateNoPerm,
		ErrMarkdownParam, ErrMarkdownParamNewline, ErrMarkdownParamMarkdown, ErrMarkdownParamInvalid)
}

// IsRateLimited reports that active-message quotas were exceeded.
func IsRateLimited(err error) bool {
	return CodeIs(err, ErrActiveRateLimit)
}

// Client is a thin QQ open-platform REST client.
type Client struct {
	baseURL string
	http    *http.Client
	tokens  *TokenManager
}

// NewClient builds a REST client sharing the given token manager.
func NewClient(apiBase string, tokens *TokenManager, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		baseURL: strings.TrimRight(apiBase, "/"),
		http:    hc,
		tokens:  tokens,
	}
}

func (c *Client) Tokens() *TokenManager { return c.tokens }

func (c *Client) BaseURL() string { return c.baseURL }

// do performs an authenticated request and decodes the JSON envelope.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	token, err := c.tokens.Token(ctx, false)
	if err != nil {
		return err
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("qq api %s: encode body: %w", path, err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qq api %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("qq api %s: read body: %w", path, err)
	}

	if resp.StatusCode >= 300 {
		apiErr := &APIError{HTTPStatus: resp.StatusCode, Path: path}
		var env struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
			ErrCode int             `json:"err_code"`
		}
		if json.Unmarshal(raw, &env) == nil {
			apiErr.Code = flexIntFrom(env.Code)
			if apiErr.Code == 0 {
				apiErr.Code = env.ErrCode
			}
			apiErr.Message = env.Message
		}
		if apiErr.Message == "" {
			apiErr.Message = truncate(string(raw), 300)
		}
		return apiErr
	}

	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("qq api %s: decode response: %w", path, err)
	}
	return nil
}

func flexIntFrom(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var v flexInt
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0
	}
	return int(v)
}

// GatewayInfo is the response of GET /gateway/bot.
type GatewayInfo struct {
	URL    string `json:"url"`
	Shards int    `json:"shards"`
}

// GetGateway fetches the WebSocket gateway endpoint.
func (c *Client) GetGateway(ctx context.Context) (*GatewayInfo, error) {
	var out GatewayInfo
	if err := c.do(ctx, http.MethodGet, "/gateway/bot", nil, &out); err != nil {
		return nil, err
	}
	if out.URL == "" {
		return nil, errors.New("qq api: gateway response missing url")
	}
	return &out, nil
}

// BotInfo is the identity of the bot itself, from GET /users/@me.
type BotInfo struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

// GetMe returns the bot's own identity. Useful as a fallback for telling our
// own messages and mentions apart from other bots in the same group.
func (c *Client) GetMe(ctx context.Context) (*BotInfo, error) {
	var out BotInfo
	if err := c.do(ctx, http.MethodGet, "/users/@me", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BotState is the bot's own state inside one group, from
// GET /v2/groups/{group_openid}/bot_state.
//
// MemberOpenID is the identifier that appears in event `mentions` when someone
// @s this bot, which is the only reliable way to tell "the other bot in the
// group was mentioned" from "we were mentioned".
type BotState struct {
	MemberOpenID      string `json:"member_openid"`
	JoinedAt          string `json:"joined_at"`
	AllowProactiveMsg bool   `json:"allow_proactive_msg"`
	// RecvMsgSetting is the platform-side receive setting:
	// all | only_mention | mention_and_context.
	RecvMsgSetting string `json:"recv_msg_setting"`
	MemberRole     string `json:"member_role"`
}

// GetBotState fetches our own state inside a group.
func (c *Client) GetBotState(ctx context.Context, groupOpenID string) (*BotState, error) {
	var out BotState
	path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/bot_state"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GroupInfo is basic metadata about a group.
type GroupInfo struct {
	GroupOpenID string `json:"group_openid"`
	GroupName   string `json:"group_name"`
}

// GetGroupInfo fetches a group's display metadata.
func (c *Client) GetGroupInfo(ctx context.Context, groupOpenID string) (*GroupInfo, error) {
	var out GroupInfo
	path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/info"
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MessageType identifies the payload shape of an outgoing message.
const (
	MsgTypeText     = 0
	MsgTypeMarkdown = 2
	MsgTypeMedia    = 7
)

// SendMessageRequest is the body of the group/C2C send endpoints.
type SendMessageRequest struct {
	MsgType    int              `json:"msg_type"`
	Content    string           `json:"content,omitempty"`
	Markdown   *MessageMarkdown `json:"markdown,omitempty"`
	Media      *MediaInfo       `json:"media,omitempty"`
	MsgID      string           `json:"msg_id,omitempty"`
	EventID    string           `json:"event_id,omitempty"`
	MsgSeq     int              `json:"msg_seq,omitempty"`
	MessageRef *MessageRef      `json:"message_reference,omitempty"`
}

// MediaInfo points at a file previously uploaded to the platform. For
// msg_type=7 the rich-media message carries it in the "media" field.
type MediaInfo struct {
	FileInfo string `json:"file_info"`
}

// MessageMarkdown carries markdown content for msg_type=2.
type MessageMarkdown struct {
	Content string `json:"content"`
}

// MessageRef quotes an earlier message.
type MessageRef struct {
	MessageID string `json:"message_id"`
}

// SendMessageResponse is returned by the send endpoints.
type SendMessageResponse struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	ExtInfo   struct {
		RefIdx string `json:"ref_idx"`
	} `json:"ext_info"`
}

// SendGroupMessage posts a message into a group.
func (c *Client) SendGroupMessage(ctx context.Context, groupOpenID string, req SendMessageRequest) (*SendMessageResponse, error) {
	var out SendMessageResponse
	path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/messages"
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RecallGroupMessage retracts a message the bot sent to a group.
//
// The platform only allows recalling messages younger than 2 minutes; older
// ones fail with ErrRecallExpired.
func (c *Client) RecallGroupMessage(ctx context.Context, groupOpenID, messageID string) error {
	path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/messages/" + url.PathEscape(messageID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// RecallC2CMessage retracts a message the bot sent to a user.
func (c *Client) RecallC2CMessage(ctx context.Context, userOpenID, messageID string) error {
	path := "/v2/users/" + url.PathEscape(userOpenID) + "/messages/" + url.PathEscape(messageID)
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// maxUploadImageBytes is the platform's limit on one uploaded image. It is
// checked before base64 encoding so an oversized PNG fails locally with the same
// APIError shape the platform would produce.
const maxUploadImageBytes = 5 << 20

// uploadImageResponse is the reply of the group/C2C file upload endpoints.
type uploadImageResponse struct {
	FileInfo string `json:"file_info"`
	TTL      int    `json:"ttl"`
}

// UploadGroupImage uploads a PNG to a group and returns the file_info needed by
// a msg_type=7 message.
//
// The body shape is fixed by the platform: file_type=1 (image), a file name,
// the base64 bytes in file_data, and srv_send_msg=false so the upload does not
// emit a separate message — the caller sends the media message itself.
func (c *Client) UploadGroupImage(ctx context.Context, groupOpenID string, png []byte) (string, error) {
	path := "/v2/groups/" + url.PathEscape(groupOpenID) + "/files"
	return c.uploadImage(ctx, path, png)
}

// UploadC2CImage is the direct-message counterpart of UploadGroupImage.
func (c *Client) UploadC2CImage(ctx context.Context, userOpenID string, png []byte) (string, error) {
	path := "/v2/users/" + url.PathEscape(userOpenID) + "/files"
	return c.uploadImage(ctx, path, png)
}

func (c *Client) uploadImage(ctx context.Context, path string, png []byte) (string, error) {
	if len(png) == 0 {
		return "", &APIError{Path: path, Message: "empty image"}
	}
	if len(png) > maxUploadImageBytes {
		return "", &APIError{Path: path, Message: fmt.Sprintf("image too large: %d bytes (limit %d)", len(png), maxUploadImageBytes)}
	}
	body := struct {
		FileType   int    `json:"file_type"`
		FileName   string `json:"file_name"`
		FileData   string `json:"file_data"`
		SrvSendMsg bool   `json:"srv_send_msg"`
	}{
		FileType:   1,
		FileName:   "status.png",
		FileData:   base64.StdEncoding.EncodeToString(png),
		SrvSendMsg: false,
	}
	var out uploadImageResponse
	if err := c.do(ctx, http.MethodPost, path, body, &out); err != nil {
		return "", err
	}
	if out.FileInfo == "" {
		return "", &APIError{Path: path, Message: "upload response missing file_info"}
	}
	return out.FileInfo, nil
}

// SendC2CMessage posts a direct message to a user.
func (c *Client) SendC2CMessage(ctx context.Context, userOpenID string, req SendMessageRequest) (*SendMessageResponse, error) {
	var out SendMessageResponse
	path := "/v2/users/" + url.PathEscape(userOpenID) + "/messages"
	if err := c.do(ctx, http.MethodPost, path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
