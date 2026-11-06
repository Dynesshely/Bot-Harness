package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// ErrAdminOnly is returned when a caller that is not in admin_openids tries to
// run a command. The dispatcher maps it to a "rejected" audit row.
var ErrAdminOnly = errors.New("只有管理员可以执行命令（admin_openids）")

// ErrNoAdminsConfigured is the explicit "empty allowlist" gate (T28). It exists
// in addition to ErrAdminOnly so that opening exec_enabled without filling in
// admin_openids can never enable run_shell, even if the identity check is ever
// changed to default-open: a dangerous capability needs a switch *and* an
// identity, and an empty list means nobody has that identity.
var ErrNoAdminsConfigured = errors.New("管理员名单为空，执行命令不可用（请在策略配置填写 admin_openids）")

// ExecConfig is read on every call so settings changes apply without a restart.
type ExecConfig struct {
	Enabled   bool
	Timeout   time.Duration
	MaxOutput int
	// Root is the parent of every per-conversation workspace (default /tmp).
	Root string
}

// NewShell runs commands inside an isolated workspace of this container.
//
// The isolation boundary is the container itself (see D-034): commands run as the
// same uid, so the mitigations here are the cheap ones that still matter —
// scrubbed environment (no QQ/AI credentials), a throwaway working directory,
// resource limits, a hard timeout and bounded output.
func NewShell(cfg func() ExecConfig) Tool { return &shellTool{cfg: cfg} }

type shellTool struct{ cfg func() ExecConfig }

func (t *shellTool) Name() string  { return "run_shell" }
func (t *shellTool) Group() string { return "exec" }
func (t *shellTool) Risk() Risk    { return Dangerous }

func (t *shellTool) Description() string {
	return "在隔离容器里执行一段 shell 命令，返回标准输出与退出码。没有网络隔离之外的特权，" +
		"文件只在临时工作目录里可见，命令结束后目录会被删除。只用于计算、文本处理这类小任务。"
}

func (t *shellTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"command":{"type":"string","description":"要执行的 shell 命令"}},"required":["command"]}`)
}

func (t *shellTool) Run(ctx context.Context, raw json.RawMessage, env *Env) (Result, error) {
	cfg := t.cfg()
	if !cfg.Enabled {
		return Result{}, errors.New("命令执行已关闭（exec_enabled=false）")
	}
	// Identity check comes second: an enabled capability is not a licence for
	// everyone. Only admin_openids may run commands, no matter what the model
	// was told.
	if env == nil || !env.IsAdmin {
		return Result{}, ErrAdminOnly
	}
	// Second, explicit gate: an empty admin_openids list is a closed door, not a
	// formality. Rejecting on AdminCount keeps that policy readable here even if
	// a future change to the identity computation lets an empty list produce
	// IsAdmin=true.
	if env.AdminCount == 0 {
		return Result{}, ErrNoAdminsConfigured
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, fmt.Errorf("参数需要 {\"command\": \"…\"}: %w", err)
	}
	command := strings.TrimSpace(args.Command)
	if command == "" {
		return Result{}, errors.New("command 不能为空")
	}

	root := cfg.Root
	if root == "" {
		root = os.TempDir()
	}
	dir := filepath.Join(root, "bot-harness-exec", workspaceName(env))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("准备工作目录失败: %w", err)
	}
	// The workspace is per-call: a later command must not see an earlier one.
	defer func() { _ = os.RemoveAll(dir) }()

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// busybox ash supports the common limits; unsupported ones are ignored.
	script := strings.Join([]string{
		"ulimit -t 10 2>/dev/null",
		"ulimit -f 1024 2>/dev/null",
		"ulimit -u 64 2>/dev/null",
		"ulimit -v 262144 2>/dev/null",
		command,
	}, "; ")

	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", script)
	cmd.Dir = dir
	// Deliberately minimal: the harness' own environment carries the QQ app
	// secret, and a model-issued `env` must not be able to print it.
	cmd.Env = []string{
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + dir,
		"TMPDIR=" + dir,
		"LANG=C.UTF-8",
		"TZ=UTC",
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the whole process group: a backgrounded child would otherwise
		// survive the timeout and keep the container busy.
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	maxOutput := cfg.MaxOutput
	if maxOutput <= 0 {
		maxOutput = 8192
	}
	out := &cappedBuffer{max: maxOutput}
	cmd.Stdout = out
	cmd.Stderr = out

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start).Round(time.Millisecond)

	text := strings.TrimSpace(out.String())
	if out.truncated {
		text += "\n…（输出过长已截断）"
	}

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return Result{Text: fmt.Sprintf("$ %s\n%s\n(退出码 0，耗时 %s)", command, text, elapsed), Truncated: out.truncated}, nil
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return Result{Text: fmt.Sprintf("$ %s\n%s\n(超时 %s，已终止；输出可能不完整)", command, text, timeout)}, nil
	case errors.As(err, &exitErr):
		return Result{Text: fmt.Sprintf("$ %s\n%s\n(退出码 %d，耗时 %s)", command, text, exitErr.ExitCode(), elapsed), Truncated: out.truncated}, nil
	default:
		return Result{}, fmt.Errorf("执行失败: %w", err)
	}
}

// workspaceName derives a filesystem-safe, per-conversation directory name.
func workspaceName(env *Env) string {
	seed := "global"
	if env != nil {
		switch {
		case env.ConversationID != "":
			seed = env.ConversationID
		case env.GroupOpenID != "":
			seed = env.GroupOpenID
		case env.MemberOpenID != "":
			seed = env.MemberOpenID
		}
	}
	var b strings.Builder
	for _, r := range seed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if len(name) > 64 {
		name = name[:64]
	}
	if name == "" {
		name = "session"
	}
	return name
}

// cappedBuffer stops collecting once the cap is reached, so a runaway command
// cannot exhaust memory before the timeout fires.
type cappedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	room := w.max - w.buf.Len()
	if room <= 0 {
		w.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		w.buf.Write(p[:room])
		w.truncated = true
		return len(p), nil
	}
	w.buf.Write(p)
	return len(p), nil
}

func (w *cappedBuffer) String() string { return w.buf.String() }
