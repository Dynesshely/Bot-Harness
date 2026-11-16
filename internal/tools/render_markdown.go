package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dynesshely/bot-harness/internal/render"
)

// RenderMarkdownTool turns a markdown document into a PNG and asks the caller
// to deliver it to the current chat. The tool deliberately owns no transport:
// core injects Env.SendImage, so the same renderer can be tested in isolation.
type RenderMarkdownTool struct{}

// NewRenderMarkdown returns the render_markdown tool.
func NewRenderMarkdown() Tool { return RenderMarkdownTool{} }

func (RenderMarkdownTool) Name() string { return "render_markdown" }

func (RenderMarkdownTool) Description() string {
	return "把 Markdown 渲染成图片并发送到当前会话。支持标题、段落、列表、引用、代码块、分隔线和表格；" +
		"当用户要求把长文、表格、排版内容变成图片时调用。不要用它发送普通短句。"
}

func (RenderMarkdownTool) Parameters() json.RawMessage {
	return schema(
		"\"markdown\":{\"type\":\"string\",\"description\":\"要渲染的 Markdown 正文，最多 20000 字符\"},"+
			"\"title\":{\"type\":\"string\",\"description\":\"可选标题，最多 120 字符\"}",
		"markdown",
	)
}

func (RenderMarkdownTool) Risk() Risk { return SideEffect }

func (RenderMarkdownTool) Group() string { return "util" }

func (RenderMarkdownTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (Result, error) {
	var args struct {
		Markdown string `json:"markdown"`
		Title    string `json:"title"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, fmt.Errorf("参数需要形如 {\"markdown\": \"# 标题\"}: %w", err)
	}
	if len([]rune(args.Markdown)) == 0 {
		return Result{}, errors.New("markdown 不能为空")
	}
	if len([]rune(args.Markdown)) > 20000 {
		return Result{}, errors.New("markdown 不能超过 20000 字符")
	}
	if len([]rune(args.Title)) > 120 {
		return Result{}, errors.New("title 不能超过 120 字符")
	}
	if env == nil || env.SendImage == nil {
		return Result{}, errors.New("当前会话不支持发送图片")
	}
	png, err := render.RenderMarkdown(render.Markdown{Title: args.Title, Body: args.Markdown})
	if err != nil {
		return Result{}, err
	}
	if len(png) > 5*1024*1024 {
		return Result{}, errors.New("渲染结果超过 5 MB，请缩短内容")
	}
	alt := "[图片] Markdown"
	if args.Title != "" {
		alt = "[图片] " + args.Title
	}
	if err := env.SendImage(ctx, png, alt); err != nil {
		return Result{}, fmt.Errorf("发送渲染图片失败：%w", err)
	}
	return Result{Text: fmt.Sprintf("已渲染并发送 Markdown 图片（%d 字节）", len(png))}, nil
}
