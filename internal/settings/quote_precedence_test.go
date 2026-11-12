package settings

import (
	"context"
	"encoding/json"
	"testing"
)

// 新键与旧键同时出现在一个补丁里时，结果必须与 map 的迭代顺序无关：
// quote_reply_mode 优先，旧布尔被镜像后一起落库。
func TestQuoteReplyModeWinsInsideOnePatch(t *testing.T) {
	m, st := newManager(t, false)
	ctx := context.Background()

	// 旧布尔写 true、新键写 often：不管哪条先被 apply，最终都必须是 often。
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"quote_reply":true,"quote_reply_mode":"often"}`), &patch); err != nil {
		t.Fatalf("patch: %v", err)
	}
	updated, _, err := m.Apply(ctx, patch)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if updated.QuoteReplyMode != QuoteReplyOften || !updated.QuoteReply {
		t.Fatalf("同一补丁里新键优先：mode=%q bool=%v", updated.QuoteReplyMode, updated.QuoteReply)
	}

	// 反向组合：旧布尔 false、新键 occasionally → occasionally。
	if err := json.Unmarshal([]byte(`{"quote_reply":false,"quote_reply_mode":"occasionally"}`), &patch); err != nil {
		t.Fatalf("patch: %v", err)
	}
	updated, _, err = m.Apply(ctx, patch)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if updated.QuoteReplyMode != QuoteReplyOccasionally || !updated.QuoteReply {
		t.Fatalf("新键仍应优先：mode=%q bool=%v", updated.QuoteReplyMode, updated.QuoteReply)
	}

	// 落库：两个键都写了，重新加载仍以档位为准（New 里也要有同样的优先级）。
	reloaded, err := New(ctx, st, Defaults())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Get(); got.QuoteReplyMode != QuoteReplyOccasionally || !got.QuoteReply {
		t.Fatalf("重新加载后档位被旧布尔覆盖：mode=%q bool=%v", got.QuoteReplyMode, got.QuoteReply)
	}
}
