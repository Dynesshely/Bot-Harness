package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// 旧库升级：records 增加 delivery 列，并按「有没有 msg_id」回填历史数据。
//
// 主动消息与被动回复共用平台的主动消息配额，没有这一列就算不出「今天用掉多少」。
func TestLegacyDatabaseGainsRecordsDelivery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	// 用当前 schema 起旧库，只去掉 delivery 那一列，其余保持一致。
	legacySchema := regexp.MustCompile(`(?m)^\s*delivery\s+TEXT NOT NULL DEFAULT '',\n`).ReplaceAllString(schema, "")
	if legacySchema == schema {
		t.Fatal("没能从 schema 里去掉 delivery，测试前置条件失效了")
	}
	if _, err := legacy.Exec(legacySchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}

	today := time.Now().Format(time.RFC3339)
	stmts := []string{
		`INSERT INTO records (direction, status, msg_id, content, created_at) VALUES
		   ('out','replied','M1','被动回复','` + today + `'),
		   ('out','replied','','主动兜底','` + today + `'),
		   ('out','early_reply','M2','提前安抚','` + today + `'),
		   ('in','received','M3','收到的消息','` + today + `')`,
	}
	for _, q := range stmts {
		if _, err := legacy.Exec(q); err != nil {
			t.Fatalf("legacy setup %q: %v", q, err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("open upgraded: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	recs, _, err := s.ListRecords(ctx, RecordFilter{Page: 1, PageSize: 20})
	if err != nil {
		t.Fatalf("records: %v", err)
	}
	got := map[string]string{}
	for _, r := range recs {
		got[r.Content] = r.Delivery
	}
	want := map[string]string{
		"被动回复":  DeliveryPassive,
		"主动兜底":  DeliveryActive,
		"提前安抚":  DeliveryActive, // 早回复带 msg_id，但它是主动消息
		"收到的消息": "",
	}
	for content, delivery := range want {
		if got[content] != delivery {
			t.Fatalf("%q 的 delivery 应为 %q，实际 %q", content, delivery, got[content])
		}
	}

	// 计数：只有主动消息那一类算进去（被动回复与入站都不算）。
	n, err := s.CountActiveMessages(ctx, "G1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		// 这些历史行的 group_openid 是空的，按群统计自然为 0。
		t.Fatalf("没有 group_openid 的历史行不该计入某个群，实际 %d", n)
	}
}

// 计数只看指定群、指定时间之后的主动消息。
func TestCountActiveMessagesScopesByGroupAndTime(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()
	ctx := context.Background()

	rows := []Record{
		{Direction: "out", GroupOpenID: "G1", Status: StatusManual, Delivery: DeliveryActive},
		{Direction: "out", GroupOpenID: "G1", Status: "early_reply", Delivery: DeliveryActive},
		{Direction: "out", GroupOpenID: "G1", Status: "replied", Delivery: DeliveryPassive},
		{Direction: "out", GroupOpenID: "G2", Status: StatusManual, Delivery: DeliveryActive},
		{Direction: "in", GroupOpenID: "G1", Status: "received"},
	}
	for i := range rows {
		if _, err := s.AddRecord(ctx, &rows[i]); err != nil {
			t.Fatalf("add record: %v", err)
		}
	}

	n, err := s.CountActiveMessages(ctx, "G1", time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("G1 今天应有 2 条主动消息，实际 %d", n)
	}
	if n, _ := s.CountActiveMessages(ctx, "G2", time.Now().Add(-time.Hour)); n != 1 {
		t.Fatalf("G2 应有 1 条主动消息，实际 %d", n)
	}
	// 未来时间点 = 什么都没发生
	if n, _ := s.CountActiveMessages(ctx, "G1", time.Now().Add(time.Hour)); n != 0 {
		t.Fatalf("未来时间点应为 0，实际 %d", n)
	}
	// 空群号直接返回 0，不去查库
	if n, _ := s.CountActiveMessages(ctx, "", time.Now().Add(-time.Hour)); n != 0 {
		t.Fatalf("空群号应为 0，实际 %d", n)
	}
}
