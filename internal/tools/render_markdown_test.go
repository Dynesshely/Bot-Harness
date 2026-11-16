package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"testing"
)

func TestRenderMarkdownToolSendsPNG(t *testing.T) {
	var got []byte
	var alt string
	env := &Env{SendImage: func(_ context.Context, pngBytes []byte, a string) error {
		got = append([]byte(nil), pngBytes...)
		alt = a
		return nil
	}}
	args, _ := json.Marshal(map[string]string{"markdown": "# 你好\n\n这是**测试**。", "title": "标题"})
	res, err := NewRenderMarkdown().Run(context.Background(), args, env)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Text == "" || len(got) == 0 {
		t.Fatalf("empty result/image (text=%q bytes=%d)", res.Text, len(got))
	}
	if !bytes.HasPrefix(got, []byte("\x89PNG")) {
		t.Fatal("callback did not receive a png")
	}
	if _, err := png.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if alt != "[图片] 标题" {
		t.Fatalf("alt = %q", alt)
	}
}

func TestRenderMarkdownToolNeedsTransport(t *testing.T) {
	args, _ := json.Marshal(map[string]string{"markdown": "hi"})
	if _, err := NewRenderMarkdown().Run(context.Background(), args, &Env{}); err == nil {
		t.Fatal("expected missing transport error")
	}
}
