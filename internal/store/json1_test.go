package store

import (
	"context"
	"testing"
)

func TestJSON1IsAvailable(t *testing.T) {
	s, err := Open(t.TempDir() + "/j.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.DB().ExecContext(context.Background(),
		`INSERT INTO media (id, content_type, size, sha256, data, created_at) VALUES ('a','image/png',1,'a',x'00','2026-01-01T00:00:00+08:00')`); err != nil {
		t.Fatalf("insert media: %v", err)
	}
	var n int
	if err := s.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM json_each('[{"media_id":"a"}]')`).Scan(&n); err != nil {
		t.Fatalf("json_each 不可用: %v", err)
	}
	if n != 1 {
		t.Fatalf("json_each 结果异常: %d", n)
	}
	t.Log("JSON1 可用")
}
