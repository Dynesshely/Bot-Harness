package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/store"
)

func TestRenderMarkdownToolSendsImage(t *testing.T) {
	h := newHarness(t)
	h.apply(t, "{\"tools_enabled\":true,\"reply_footer_enabled\":false}")
	args, _ := json.Marshal(map[string]string{
		"markdown": "# 标题\n\n- 第一项",
		"title":    "测试图",
	})
	h.mu.Lock()
	h.aiToolScript = []string{"render_markdown|" + string(args)}
	h.mu.Unlock()

	h.dis.HandlePayload(context.Background(), groupAtMessage("MD1", "G1", "U1", "小明", "把这段画成图"))

	sent := h.waitForSent(t, 2)
	if len(sent) < 2 {
		t.Fatalf("expected image + final answer, got %d", len(sent))
	}
	uploads := h.sentUploads()
	if len(uploads) != 1 {
		t.Fatalf("expected one image upload, got %d", len(uploads))
	}
	if !strings.Contains(uploads[0].Path, "/files") {
		t.Fatalf("unexpected upload path %q", uploads[0].Path)
	}
	if sent[0].Body.MsgType != 7 {
		t.Fatalf("first send should be media, got msg_type=%d", sent[0].Body.MsgType)
	}
	rows, err := h.st.ListToolCalls(context.Background(), 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Tool != "render_markdown" || rows[0].Status != store.ToolCallOK {
		t.Fatalf("tool audit = %#v", rows)
	}
}
