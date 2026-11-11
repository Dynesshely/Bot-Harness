package store

import (
	"context"
	"strings"
	"testing"
)

func TestExpressionsCRUD(t *testing.T) {
	s, err := Open(t.TempDir() + "/expr.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	e, err := s.AddExpression(ctx, ExpressionInput{
		GroupOpenID: "G1", Situation: "有人问你吃了吗", Style: "刚吃完，你呢", Source: "learn", Enabled: true,
	})
	if err != nil || e.ID == 0 {
		t.Fatalf("add: %+v %v", e, err)
	}
	if !e.Enabled || e.UseCount != 1 {
		t.Fatalf("new row = %+v", e)
	}

	// 同一 group+situation+style 再来一次：累加 use_count，不新增行。
	again, err := s.AddExpression(ctx, ExpressionInput{GroupOpenID: "G1", Situation: "有人问你吃了吗", Style: "刚吃完，你呢", Enabled: true})
	if err != nil {
		t.Fatalf("add duplicate: %v", err)
	}
	if again.ID != e.ID || again.UseCount != 2 {
		t.Fatalf("duplicate should bump use_count: %+v", again)
	}

	for i := 0; i < 3; i++ {
		if _, err := s.AddExpression(ctx, ExpressionInput{GroupOpenID: "G1", Situation: "测试情境", Style: "测试说法" + string(rune('A'+i)), Enabled: true}); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	if _, err := s.AddExpression(ctx, ExpressionInput{GroupOpenID: "G2", Situation: "别的群", Style: "别的说法", Enabled: true}); err != nil {
		t.Fatalf("other group: %v", err)
	}
	if _, err := s.AddExpression(ctx, ExpressionInput{GroupOpenID: "G1", Situation: "关闭的", Style: "不该召回", Enabled: false}); err != nil {
		t.Fatalf("disabled add: %v", err)
	}

	items, total, err := s.ListExpressions(ctx, ExpressionFilter{GroupOpenID: "G1", Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 5 || len(items) != 5 {
		t.Fatalf("list G1 total=%d len=%d", total, len(items))
	}
	// 排序：use_count DESC。
	if items[0].UseCount < items[1].UseCount {
		t.Fatalf("not sorted by use_count: %+v", items)
	}

	onlyEnabled := true
	enabled, total, err := s.ListExpressions(ctx, ExpressionFilter{GroupOpenID: "G1", Enabled: &onlyEnabled, Query: "测试", Page: 1, PageSize: 20})
	if err != nil || total != 3 || len(enabled) != 3 {
		t.Fatalf("filtered list: total=%d len=%d err=%v", total, len(enabled), err)
	}

	groupId := "G1"
	group, err := s.ExpressionsForGroup(ctx, groupId, 50)
	if err != nil {
		t.Fatalf("for group: %v", err)
	}
	for _, row := range group {
		if row.GroupOpenID == "G2" {
			t.Fatalf("other group leaked: %+v", row)
		}
		if !row.Enabled {
			t.Fatalf("disabled leaked: %+v", row)
		}
	}

	if ok, err := s.SetExpressionEnabled(ctx, e.ID, false); err != nil || !ok {
		t.Fatalf("disable: %v %v", ok, err)
	}
	if ok, err := s.SetExpressionEnabled(ctx, 999999, false); err != nil || ok {
		t.Fatalf("missing id should report false: %v %v", ok, err)
	}
	if ok, err := s.ReinforceExpression(ctx, e.ID); err != nil || !ok {
		t.Fatalf("reinforce: %v %v", ok, err)
	}
	if err := s.UpdateExpressionEmbedding(ctx, e.ID, "[0.1,0.2]", 2); err != nil {
		t.Fatalf("embedding: %v", err)
	}
	if n, err := s.CountExpressions(ctx); err != nil || n != 6 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if ok, err := s.DeleteExpression(ctx, e.ID); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, err := s.DeleteExpression(ctx, e.ID); err != nil || ok {
		t.Fatalf("second delete should report false: %v %v", ok, err)
	}
}

func TestExpressionsValidation(t *testing.T) {
	s, err := Open(t.TempDir() + "/expr.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, in := range []ExpressionInput{
		{GroupOpenID: "G1", Situation: "", Style: "x"},
		{GroupOpenID: "G1", Situation: "x", Style: ""},
		{GroupOpenID: "G1", Situation: strings.Repeat("情", 201), Style: "x"},
		{GroupOpenID: "G1", Situation: "x", Style: strings.Repeat("说", 201)},
	} {
		if _, err := s.AddExpression(ctx, in); err == nil {
			t.Fatalf("expected validation error for %+v", in)
		}
	}
}
