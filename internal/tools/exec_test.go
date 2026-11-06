package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func staticExec(cfg ExecConfig) func() ExecConfig { return func() ExecConfig { return cfg } }

func runShell(t *testing.T, cfg ExecConfig, command string) Result {
	t.Helper()
	tool := NewShell(staticExec(cfg))
	args, _ := json.Marshal(map[string]string{"command": command})
	// 既有用例关注执行细节，身份放行为「名单里有管理员、调用者就是管理员」。
	res, err := tool.Run(context.Background(), args, &Env{ConversationID: "conv-1", IsAdmin: true, AdminCount: 1})
	if err != nil {
		t.Fatalf("run %q: %v", command, err)
	}
	return res
}

func TestShellRunsInsideAWorkspace(t *testing.T) {
	res := runShell(t, ExecConfig{Enabled: true, Timeout: 5 * time.Second, Root: t.TempDir()}, "pwd")
	if !strings.Contains(res.Text, "bot-harness-exec") {
		t.Fatalf("工作目录应在 bot-harness-exec 下：%q", res.Text)
	}
}

// 关键安全性质：模型发的 `env` 不能看到宿主的凭据。
func TestShellEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("QQ_APP_SECRET", "super-secret")
	t.Setenv("OPENAI_API_KEY", "sk-should-not-leak")

	res := runShell(t, ExecConfig{Enabled: true, Timeout: 5 * time.Second, Root: t.TempDir()}, "env")
	for _, leaked := range []string{"super-secret", "sk-should-not-leak", "QQ_APP_SECRET", "OPENAI_API_KEY"} {
		if strings.Contains(res.Text, leaked) {
			t.Fatalf("环境变量泄漏了 %q：%s", leaked, res.Text)
		}
	}
	if !strings.Contains(res.Text, "HOME=") {
		t.Fatalf("应当有最小环境：%s", res.Text)
	}
}

func TestShellTimeoutKillsTheCommand(t *testing.T) {
	start := time.Now()
	res := runShell(t, ExecConfig{Enabled: true, Timeout: 300 * time.Millisecond, Root: t.TempDir()}, "sleep 5")
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("超时没有生效，耗时 %s", elapsed)
	}
	if !strings.Contains(res.Text, "超时") {
		t.Fatalf("应报告超时：%q", res.Text)
	}
}

func TestShellOutputIsCapped(t *testing.T) {
	res := runShell(t, ExecConfig{Enabled: true, Timeout: 5 * time.Second, MaxOutput: 64, Root: t.TempDir()},
		"printf 'x%.0s' $(seq 1 5000)")
	if !res.Truncated || !strings.Contains(res.Text, "已截断") {
		t.Fatalf("输出应被截断：%+v", res)
	}
	if len(res.Text) > 500 {
		t.Fatalf("截断后仍然太长：%d", len(res.Text))
	}
}

func TestShellReportsExitCode(t *testing.T) {
	res := runShell(t, ExecConfig{Enabled: true, Timeout: 5 * time.Second, Root: t.TempDir()}, "exit 3")
	if !strings.Contains(res.Text, "退出码 3") {
		t.Fatalf("应报告退出码：%q", res.Text)
	}
}

// 身份校验：不在 admin_openids 里的人即使功能开着也执行不了。
func TestShellRequiresAdminIdentity(t *testing.T) {
	tool := NewShell(staticExec(ExecConfig{Enabled: true, Timeout: 5 * time.Second, Root: t.TempDir()}))
	args := json.RawMessage(`{"command":"echo hi"}`)

	for _, env := range []*Env{
		nil,
		{},
		{IsAdmin: false, MemberOpenID: "U-x"},
		{IsAdmin: false, AdminCount: 3, MemberOpenID: "U-x"},
	} {
		if _, err := tool.Run(context.Background(), args, env); !errors.Is(err, ErrAdminOnly) {
			t.Fatalf("非管理员应被拒绝，env=%+v err=%v", env, err)
		}
	}
	res, err := tool.Run(context.Background(), args, &Env{IsAdmin: true, AdminCount: 1})
	if err != nil {
		t.Fatalf("管理员应当可以执行：%v", err)
	}
	if !strings.Contains(res.Text, "hi") {
		t.Fatalf("执行结果不对：%q", res.Text)
	}
}

// T28 第二道门：即使身份判定算出 IsAdmin=true，只要 admin_openids 为空，
// run_shell 也必须拒绝。用写文件探测证明命令真的没执行，而不只是返回了错误。
func TestShellRequiresConfiguredAdminList(t *testing.T) {
	root := t.TempDir()
	probe := filepath.Join(t.TempDir(), "executed")
	tool := NewShell(staticExec(ExecConfig{Enabled: true, Timeout: 5 * time.Second, Root: root}))
	args, _ := json.Marshal(map[string]string{"command": "touch " + probe})

	res, err := tool.Run(context.Background(), args, &Env{ConversationID: "conv-1", IsAdmin: true, AdminCount: 0})
	if !errors.Is(err, ErrNoAdminsConfigured) {
		t.Fatalf("空管理员名单应返回 ErrNoAdminsConfigured，实际 res=%+v err=%v", res, err)
	}
	if msg := err.Error(); !strings.Contains(msg, "管理员名单为空") || !strings.Contains(msg, "admin_openids") {
		t.Fatalf("错误文案应指路 admin_openids：%q", msg)
	}
	if _, statErr := os.Stat(probe); !os.IsNotExist(statErr) {
		t.Fatalf("命令不该被执行，探测文件已存在：%v", statErr)
	}

	// 空名单 + 非管理员：第一道门（身份）优先，仍然是 ErrAdminOnly。
	if _, err := tool.Run(context.Background(), args, &Env{IsAdmin: false, AdminCount: 0}); !errors.Is(err, ErrAdminOnly) {
		t.Fatalf("非管理员应先在身份检查被拒绝，实际 %v", err)
	}
	if _, statErr := os.Stat(probe); !os.IsNotExist(statErr) {
		t.Fatalf("命令不该被执行，探测文件已存在：%v", statErr)
	}

	// 名单非空 + 管理员：正常执行并产生探测文件。
	res, err = tool.Run(context.Background(), args, &Env{IsAdmin: true, AdminCount: 1})
	if err != nil {
		t.Fatalf("管理员应当可以执行：%v", err)
	}
	if !strings.Contains(res.Text, "退出码 0") {
		t.Fatalf("执行结果不对：%q", res.Text)
	}
	if _, statErr := os.Stat(probe); statErr != nil {
		t.Fatalf("命令应已执行并创建探测文件：%v", statErr)
	}
}

func TestShellDisabled(t *testing.T) {
	tool := NewShell(staticExec(ExecConfig{Enabled: false}))
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"command":"echo hi"}`), &Env{}); err == nil {
		t.Fatal("关闭后必须拒绝执行")
	}
}

func TestShellWorkspaceIsWipedBetweenCalls(t *testing.T) {
	root := t.TempDir()
	cfg := ExecConfig{Enabled: true, Timeout: 5 * time.Second, Root: root}
	runShell(t, cfg, "echo leftover > file.txt")
	res := runShell(t, cfg, "ls")
	if strings.Contains(res.Text, "file.txt") {
		t.Fatalf("上一次的文件不该残留：%q", res.Text)
	}
}
