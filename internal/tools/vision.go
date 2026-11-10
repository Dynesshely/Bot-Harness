package tools

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
)

// VisionConfig is read on every call so enabling vision (or switching the
// vision model) in the admin panel applies without a restart.
type VisionConfig struct {
	// Enabled is the global vision_enabled switch. When false the tool reports
	// "vision not enabled" as text instead of failing the turn.
	Enabled bool
	// Model is the dedicated vision model (human_status_vision_model).
	Model string
	// FallbackModel is used when Model is empty (the main model).
	FallbackModel string
}

// ImageLoader loads one cached attachment. *media.Bundle implements it.
type ImageLoader interface {
	Load(ctx context.Context, mediaID string) (contentType string, data []byte, err error)
}

// VisionProvider performs one multimodal completion. *ai.Client implements it.
type VisionProvider interface {
	Chat(ctx context.Context, req ai.ChatRequest) (*ai.ChatResult, error)
}

const (
	// viewImageResultChars caps what the model receives from one image: the
	// agent loop already truncates every tool result, but an image description
	// has no reason to be long and a tight cap keeps the context small.
	viewImageResultChars = 500
	// viewImageQuestionChars caps the optional question so a quoted user
	// message cannot turn into an unbounded vision prompt.
	viewImageQuestionChars = 200
	viewImageDefaultPrompt = "请客观描述这张图片：画面主体、场景、可见文字与关键细节。不要推断年龄、性别、健康等敏感属性。"
)

// NewViewImage returns the view_image tool. It never returns a Go error for a
// missing/expired image, a disabled feature or a failed vision call: those are
// reported to the model as 「图片不可用：…」 text so one bad picture cannot make
// the whole reply fail.
func NewViewImage(loader ImageLoader, provider VisionProvider, cfg func() VisionConfig) Tool {
	return &viewImageTool{loader: loader, provider: provider, cfg: cfg}
}

type viewImageTool struct {
	loader   ImageLoader
	provider VisionProvider
	cfg      func() VisionConfig
}

func (t *viewImageTool) Name() string { return "view_image" }

func (t *viewImageTool) Description() string {
	return "查看消息里 [图片 #id] / [引用图片 #id] 对应的已缓存图片并返回描述。只能看本轮或历史消息的附件；没有图片 id 时不要调用，也不要逐张调用。"
}

func (t *viewImageTool) Parameters() json.RawMessage {
	return schema(`"media_id":{"type":"string","description":"消息里 [图片 #id] / [引用图片 #id] 给出的图片 id"},`+
		`"question":{"type":"string","description":"可选：想从图片里确认的具体问题"}`, "media_id")
}

func (t *viewImageTool) Risk() Risk { return ReadOnly }

func (t *viewImageTool) Group() string { return "vision" }

func (t *viewImageTool) Run(ctx context.Context, raw json.RawMessage, _ *Env) (Result, error) {
	cfg := VisionConfig{}
	if t.cfg != nil {
		cfg = t.cfg()
	}
	if !cfg.Enabled {
		return Result{Text: "图片不可用：视觉能力未启用。"}, nil
	}
	var args struct {
		MediaID  string `json:"media_id"`
		Question string `json:"question"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return Result{Text: "图片不可用：工具参数无法解析。"}, nil
		}
	}
	mediaID := strings.TrimSpace(args.MediaID)
	if mediaID == "" {
		return Result{Text: "图片不可用：缺少 media_id。"}, nil
	}
	if t.loader == nil || t.provider == nil {
		return Result{Text: "图片不可用：媒体存储或视觉模型不可用。"}, nil
	}
	contentType, data, err := t.loader.Load(ctx, mediaID)
	if err != nil || len(data) == 0 {
		return Result{Text: "图片不可用：图片不存在或已过期。"}, nil
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "image/") {
		return Result{Text: "图片不可用：该附件不是图片。"}, nil
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = strings.TrimSpace(cfg.FallbackModel)
	}
	if model == "" {
		return Result{Text: "图片不可用：未配置视觉模型。"}, nil
	}
	question := truncateRunes(strings.TrimSpace(args.Question), viewImageQuestionChars)
	if question == "" {
		question = viewImageDefaultPrompt
	}
	res, err := t.provider.Chat(ctx, ai.ChatRequest{
		Model: model,
		Messages: []ai.Message{{
			Role: ai.RoleUser,
			Parts: []ai.ContentPart{
				ai.TextPart(question),
				ai.ImagePart(ai.DataURI(contentType, data)),
			},
		}},
		Temperature: 0.2,
		MaxTokens:   500,
		Timeout:     45 * time.Second,
		Stream:      false,
	})
	if err != nil {
		return Result{Text: "图片不可用：视觉模型调用失败。"}, nil
	}
	description := strings.TrimSpace(res.Text)
	if description == "" {
		return Result{Text: "图片不可用：视觉模型没有返回描述。"}, nil
	}
	return Result{Text: truncateRunes(description, viewImageResultChars)}, nil
}

// truncateRunes bounds s to n runes without adding a marker: the caller decides
// how to phrase a truncated description.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
