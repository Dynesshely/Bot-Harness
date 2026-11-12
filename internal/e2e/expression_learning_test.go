package e2e

import (
	"context"
	"testing"
	"time"
)

// 表达方式学习：一轮回复结束后按 learn_every 异步入库，不阻塞回复。
func TestExpressionLearningStoresCandidates(t *testing.T) {
	h := newHarness(t)
	h.mu.Lock()
	h.aiExpressionReply = `{"candidates":[{"situation":"有人问吃了吗","style":"刚吃完，你呢"},{"situation":"有人问吃了吗","style":"刚吃完，你呢"}]}`
	h.mu.Unlock()
	h.apply(t, `{"expression_enabled":true,"expression_mode":"fast",`+
		`"expression_learn_enabled":true,"expression_learn_every":1,"expression_review_enabled":false}`)

	h.dis.HandlePayload(context.Background(), groupAtMessage("EXP-L1", "G1", "U1", "小明", "你吃了吗"))
	h.waitForSent(t, 1)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := h.st.CountExpressions(context.Background())
		if err != nil {
			t.Fatalf("count: %v", err)
		}
		if n == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected one learned expression, got %d", countOrZero(t, h))
}

func countOrZero(t *testing.T, h *harness) int {
	n, _ := h.st.CountExpressions(context.Background())
	return n
}
