package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/ai"
)

type fakeImageLoader struct {
	contentType string
	data        []byte
	err         error
	loads       int
}

func (f *fakeImageLoader) Load(_ context.Context, _ string) (string, []byte, error) {
	f.loads++
	return f.contentType, f.data, f.err
}

type fakeVisionProvider struct {
	calls int
	req   ai.ChatRequest
	text  string
	err   error
}

func (f *fakeVisionProvider) Chat(_ context.Context, req ai.ChatRequest) (*ai.ChatResult, error) {
	f.calls++
	f.req = req
	if f.err != nil {
		return nil, f.err
	}
	return &ai.ChatResult{Text: f.text}, nil
}

func visionConfig(model, fallback string) func() VisionConfig {
	return func() VisionConfig {
		return VisionConfig{Enabled: true, Model: model, FallbackModel: fallback}
	}
}

func TestViewImageDescribesACachedImage(t *testing.T) {
	loader := &fakeImageLoader{contentType: "image/png", data: []byte("png-bytes")}
	provider := &fakeVisionProvider{text: " 画面里是一只橘猫，趴在键盘上。 "}
	tool := NewViewImage(loader, provider, visionConfig("vision-model", "main-model"))

	res, err := tool.Run(context.Background(), []byte(`{"media_id":"abc123","question":"画面里有什么？"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Text, "橘猫") {
		t.Fatalf("描述应原样返回：%q", res.Text)
	}
	if provider.calls != 1 {
		t.Fatalf("应只调用一次视觉模型，实际 %d", provider.calls)
	}
	if provider.req.Model != "vision-model" {
		t.Fatalf("应使用专用视觉模型，实际 %q", provider.req.Model)
	}
	if len(provider.req.Messages) != 1 || len(provider.req.Messages[0].Parts) != 2 {
		t.Fatalf("请求应包含文本 + 图片两部分：%#v", provider.req.Messages)
	}
	parts := provider.req.Messages[0].Parts
	if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "画面里有什么") {
		t.Fatalf("文本部分应携带 question：%#v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil ||
		!strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("图片部分应是 data URI：%#v", parts[1])
	}
}

func TestViewImageFallsBackToTheMainModel(t *testing.T) {
	provider := &fakeVisionProvider{text: "描述"}
	tool := NewViewImage(&fakeImageLoader{contentType: "image/jpeg", data: []byte("x")}, provider, visionConfig("", "main-model"))
	if _, err := tool.Run(context.Background(), []byte(`{"media_id":"id"}`), nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.req.Model != "main-model" {
		t.Fatalf("专用模型为空时应回退主模型，实际 %q", provider.req.Model)
	}
}

func TestViewImageUnavailableCasesAreTextNotErrors(t *testing.T) {
	visionErr := errors.New("upstream down")
	cases := []struct {
		name     string
		cfg      func() VisionConfig
		loader   ImageLoader
		provider VisionProvider
		args     string
		want     string
	}{
		{
			name:   "视觉未启用",
			cfg:    func() VisionConfig { return VisionConfig{Enabled: false, Model: "m"} },
			loader: &fakeImageLoader{}, provider: &fakeVisionProvider{text: "不该调用"},
			args: `{"media_id":"id"}`, want: "视觉能力未启用",
		},
		{
			name: "缺少 media_id", cfg: visionConfig("", "m"),
			loader: &fakeImageLoader{}, provider: &fakeVisionProvider{},
			args: `{}`, want: "缺少 media_id",
		},
		{
			name: "参数不是 JSON", cfg: visionConfig("", "m"),
			loader: &fakeImageLoader{}, provider: &fakeVisionProvider{},
			args: `not-json`, want: "参数无法解析",
		},
		{
			name: "图片不存在", cfg: visionConfig("", "m"),
			loader: &fakeImageLoader{err: errors.New("sql: no rows")}, provider: &fakeVisionProvider{},
			args: `{"media_id":"missing"}`, want: "图片不存在或已过期",
		},
		{
			name: "不是图片", cfg: visionConfig("", "m"),
			loader: &fakeImageLoader{contentType: "video/mp4", data: []byte("x")}, provider: &fakeVisionProvider{},
			args: `{"media_id":"id"}`, want: "不是图片",
		},
		{
			name: "未配置模型", cfg: visionConfig("", ""),
			loader: &fakeImageLoader{contentType: "image/png", data: []byte("x")}, provider: &fakeVisionProvider{},
			args: `{"media_id":"id"}`, want: "未配置视觉模型",
		},
		{
			name: "视觉调用失败", cfg: visionConfig("", "m"),
			loader: &fakeImageLoader{contentType: "image/png", data: []byte("x")}, provider: &fakeVisionProvider{err: visionErr},
			args: `{"media_id":"id"}`, want: "视觉模型调用失败",
		},
		{
			name: "视觉返回空", cfg: visionConfig("", "m"),
			loader: &fakeImageLoader{contentType: "image/png", data: []byte("x")}, provider: &fakeVisionProvider{text: "   "},
			args: `{"media_id":"id"}`, want: "没有返回描述",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewViewImage(tc.loader, tc.provider, tc.cfg)
			res, err := tool.Run(context.Background(), []byte(tc.args), nil)
			if err != nil {
				t.Fatalf("工具失败应作为文本返回而不是 error：%v", err)
			}
			if !strings.HasPrefix(res.Text, "图片不可用：") || !strings.Contains(res.Text, tc.want) {
				t.Fatalf("期望「图片不可用：…%s…」，实际 %q", tc.want, res.Text)
			}
		})
	}
}

func TestViewImageTruncatesTheDescription(t *testing.T) {
	loader := &fakeImageLoader{contentType: "image/png", data: []byte("x")}
	provider := &fakeVisionProvider{text: strings.Repeat("很长的描述", 200)}
	tool := NewViewImage(loader, provider, visionConfig("", "m"))
	res, err := tool.Run(context.Background(), []byte(`{"media_id":"id"}`), nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len([]rune(res.Text)); got > viewImageResultChars {
		t.Fatalf("描述应截断到 %d 字，实际 %d", viewImageResultChars, got)
	}
}
