package main

import (
	"os"

	"github.com/dynesshely/bot-harness/internal/render"
)

func main() {
	png, err := render.RenderMarkdown(render.Markdown{
		Title: "周报",
		Body: "# 本周进展\n\n- 完成 **Markdown** 渲染\n- 修复引用上下文\n\n## 数据\n\n| 指标 | 数值 |\n|---|---|\n| 测试 | 220 |\n\n\x60\x60\x60go\nfmt.Println(\"hello\")\n\x60\x60\x60\n\n> 这是一段引用。",
	})
	if err != nil { panic(err) }
	if err := os.WriteFile("/tmp/markdown-sample.png", png, 0o644); err != nil { panic(err) }
}
