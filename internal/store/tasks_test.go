package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func taskStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/tasks.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDueTasksAreLeasedExactlyOnce(t *testing.T) {
	s := taskStore(t)
	ctx := context.Background()
	due := time.Now().Add(-time.Second).Format(time.RFC3339)
	if _, err := s.AddTask(ctx, TaskInput{GroupOpenID: "G1", Text: "该喝水了", DueAt: due}); err != nil {
		t.Fatalf("add: %v", err)
	}

	first, err := s.LeaseDueTasks(ctx, time.Now(), 10, time.Minute)
	if err != nil || len(first) != 1 {
		t.Fatalf("第一次应领到一条：%d err=%v", len(first), err)
	}
	second, err := s.LeaseDueTasks(ctx, time.Now(), 10, time.Minute)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("租约期内不该被再次领走：%+v", second)
	}
	if first[0].Status != TaskLeased || first[0].Attempts != 1 {
		t.Fatalf("租约状态不对：%+v", first[0])
	}
}

func TestFutureTasksAreNotLeased(t *testing.T) {
	s := taskStore(t)
	ctx := context.Background()
	_, _ = s.AddTask(ctx, TaskInput{GroupOpenID: "G1", Text: "还没到点", DueAt: time.Now().Add(time.Hour).Format(time.RFC3339)})
	got, err := s.LeaseDueTasks(ctx, time.Now(), 10, time.Minute)
	if err != nil || len(got) != 0 {
		t.Fatalf("未来任务不该被领走：%d err=%v", len(got), err)
	}
}

func TestFinishTaskRecordsOutcome(t *testing.T) {
	s := taskStore(t)
	ctx := context.Background()
	id, _ := s.AddTask(ctx, TaskInput{GroupOpenID: "G1", Text: "x", DueAt: time.Now().Add(-time.Minute).Format(time.RFC3339)})
	leased, _ := s.LeaseDueTasks(ctx, time.Now(), 10, time.Minute)
	if len(leased) != 1 {
		t.Fatal("应领到")
	}
	if err := s.FinishTask(ctx, id, TaskSent, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	items, _ := s.ListTasks(ctx, TaskSent, 10, 0)
	if len(items) != 1 || items[0].SentAt == "" {
		t.Fatalf("发送时间没记：%+v", items)
	}
	// 已发送的不会被再次领走
	if again, _ := s.LeaseDueTasks(ctx, time.Now(), 10, time.Minute); len(again) != 0 {
		t.Fatalf("已发送任务被重复领取：%+v", again)
	}
}

func TestCancelAndCounts(t *testing.T) {
	s := taskStore(t)
	ctx := context.Background()
	due := time.Now().Add(time.Hour).Format(time.RFC3339)
	a, _ := s.AddTask(ctx, TaskInput{GroupOpenID: "G1", MemberOpenID: "U1", CreatorOpenID: "U1", Text: "a", DueAt: due})
	_, _ = s.AddTask(ctx, TaskInput{GroupOpenID: "G1", MemberOpenID: "U1", CreatorOpenID: "U1", Text: "b", DueAt: due})

	if n, _ := s.PendingTasksOfCreator(ctx, "U1"); n != 2 {
		t.Fatalf("待发计数应为 2，实际 %d", n)
	}
	ok, err := s.CancelTaskAs(ctx, a, "U1")
	if err != nil || !ok {
		t.Fatalf("取消失败：ok=%v err=%v", ok, err)
	}
	if ok, _ := s.CancelTaskAs(ctx, a, "U1"); ok {
		t.Fatal("不能重复取消")
	}
	if n, _ := s.PendingTasksOfCreator(ctx, "U1"); n != 1 {
		t.Fatalf("取消后应剩 1，实际 %d", n)
	}
}

// 同一个群里两个人各自的提醒互不可见、互不可取消。
func TestTasksAreScopedToTheirCreator(t *testing.T) {
	s := taskStore(t)
	ctx := context.Background()
	due := time.Now().Add(time.Hour).Format(time.RFC3339)

	aID, _ := s.AddTask(ctx, TaskInput{
		GroupOpenID: "G1", CreatorOpenID: "U-A", Text: "A 的提醒", DueAt: due})
	bID, _ := s.AddTask(ctx, TaskInput{
		GroupOpenID: "G1", CreatorOpenID: "U-B", Text: "B 的提醒", DueAt: due})

	// 各自只看得到自己的
	aItems, err := s.ListPendingTasksOfCreator(ctx, "U-A", 20)
	if err != nil {
		t.Fatalf("list A: %v", err)
	}
	if len(aItems) != 1 || aItems[0].ID != aID {
		t.Fatalf("A 应只看到自己的：%+v", aItems)
	}
	bItems, _ := s.ListPendingTasksOfCreator(ctx, "U-B", 20)
	if len(bItems) != 1 || bItems[0].ID != bID {
		t.Fatalf("B 应只看到自己的：%+v", bItems)
	}

	// 限额也按人算
	if n, _ := s.PendingTasksOfCreator(ctx, "U-A"); n != 1 {
		t.Fatalf("A 名下应为 1，实际 %d", n)
	}

	// B 取消不了 A 的
	if ok, err := s.CancelTaskAs(ctx, aID, "U-B"); err != nil || ok {
		t.Fatalf("B 不该取消得了 A 的：ok=%v err=%v", ok, err)
	}
	if aItems, _ := s.ListPendingTasksOfCreator(ctx, "U-A", 20); len(aItems) != 1 {
		t.Fatal("A 的提醒被误删")
	}

	// 后台仍可取消任意一条（运营者视角）
	if ok, err := s.CancelTask(ctx, aID); err != nil || !ok {
		t.Fatalf("后台取消失败：ok=%v err=%v", ok, err)
	}
}

// 老库迁移：旧表没有 creator_openid，Open 时应补列并用 member_openid 回填。
func TestLegacyTasksGainCreatorColumn(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/legacy.db"
	ctx := context.Background()

	// 1) 造一个"当年"的库：scheduled_tasks 没有 creator_openid
	raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(3000)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `CREATE TABLE scheduled_tasks (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  conversation_id TEXT NOT NULL DEFAULT '',
  group_openid    TEXT NOT NULL DEFAULT '',
  member_openid   TEXT NOT NULL DEFAULT '',
  kind            TEXT NOT NULL DEFAULT 'reminder',
  text            TEXT NOT NULL,
  due_at          TEXT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending',
  attempts        INTEGER NOT NULL DEFAULT 0,
  lease_until     TEXT NOT NULL DEFAULT '',
  error           TEXT NOT NULL DEFAULT '',
  created_at      TEXT NOT NULL,
  sent_at         TEXT NOT NULL DEFAULT ''
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
INSERT INTO scheduled_tasks (conversation_id, group_openid, member_openid, kind, text, due_at, status, created_at)
VALUES ('c2c:U1', '', 'U1', 'reminder', '老任务', ?, 'pending', '2026-01-01T00:00:00+08:00')`,
		time.Now().Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatalf("insert legacy: %v", err)
	}
	_ = raw.Close()

	// 2) 正常打开：迁移应补列并回填
	s, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	items, err := s.ListPendingTasksOfCreator(ctx, "U1", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || items[0].Text != "老任务" {
		t.Fatalf("回填后应能按 U1 查到：%+v", items)
	}
	if items[0].CreatorOpenID != "U1" {
		t.Fatalf("creator_openid 没回填：%+v", items[0])
	}
}
