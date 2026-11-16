package render

import (
	"bytes"
	"image/png"
	"testing"
)

func TestRenderMarkdownProducesPNG(t *testing.T) {
	pngBytes, err := RenderMarkdown(Markdown{
		Title: "示例",
		Body:  "# 标题\n\n这是一段**加粗**文字。\n\n- 第一项\n- 第二项\n\n> 引用\n\n\x60\x60\x60go\nfmt.Println(\"hi\")\n\x60\x60\x60\n\n| A | B |\n|---|---|\n| 1 | 2 |",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.HasPrefix(pngBytes, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("not a png")
	}
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if img.Bounds().Dx() <= 0 || img.Bounds().Dy() <= 0 {
		t.Fatalf("bad bounds: %v", img.Bounds())
	}
}

func TestRenderMarkdownRejectsEmpty(t *testing.T) {
	if _, err := RenderMarkdown(Markdown{Body: "  \n\n"}); err == nil {
		t.Fatal("expected empty markdown error")
	}
}
