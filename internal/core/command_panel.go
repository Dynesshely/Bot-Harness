package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
)

// QQ「指令面板」同步：把注册表里的命令投影成平台面板，用户在群里点开
// 就能看到命令列表并一键填入输入框。
//
// 面板只是展示层，命令的执行仍然由消息事件驱动；因此同步失败绝不能影响
// 启动或命令本身。自动同步在 main 里以 goroutine 运行，失败只记 WARN。

const (
	// panelRemark 是识别「我们自己的面板」的唯一标记。用户手工创建的面板
	// remark 不是它，同步永远不会更新或删除那些面板。
	panelRemark = "bot-harness:commands"

	// 平台限制：item.name ≤ 14 字符，desc ≤ 30 字符。
	commandPanelItemNameMax = 14
	commandPanelDescMax     = 30

	// commandPanelPageSize 是列表分页大小；平台允许最多 50，20 足够且省配额。
	commandPanelPageSize = 20

	// 一个机器人最多 20 个面板，翻页上限只是防御平台异常。
	commandPanelMaxPages = 100
)

// 同步动作，用于日志与后台响应。
const (
	PanelActionCreated   = "created"
	PanelActionUpdated   = "updated"
	PanelActionUnchanged = "unchanged"
)

// CommandPanelSyncResult describes what happened to one scope.
type CommandPanelSyncResult struct {
	Scope   string `json:"scope"`
	PanelID string `json:"panel_id"`
	Action  string `json:"action"`
	// Version is the platform version after the operation. A freshly created
	// panel has no version in the API response, so it is reported as 0.
	Version int `json:"version"`
}

// commandPanelItems projects the command registry onto panel items.
//
// Aliases are collapsed (the registry maps every alias to the same *command),
// names come from the first token of usage (what a tap should type), and the
// list is sorted by canonical command name so the panel is stable across syncs.
func (d *Dispatcher) commandPanelItems() []qq.PanelItem {
	commands := d.registeredCommands()
	items := make([]qq.PanelItem, 0, len(commands))
	for _, cmd := range commands {
		name := commandPanelItemName(cmd)
		if name == "" {
			continue
		}
		items = append(items, qq.PanelItem{
			Type:      qq.PanelItemCommand,
			Name:      name,
			Desc:      truncateRunes(cmd.desc, commandPanelDescMax),
			OnlyAdmin: cmd.adminOnly,
		})
	}
	return items
}

// truncateRunes bounds a string by runes, not bytes: the platform limits are
// character counts, and a multi-byte Chinese description must not be cut in the
// middle of a UTF-8 sequence.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// commandPanelItemName is the first whitespace-delimited token of usage with
// its leading slash removed, e.g. "/tokenstats [on|off|status]" -> "tokenstats".
//
// The slash is deliberately dropped: the platform normalises item names by
// stripping it (a write of "/help" reads back as "help"). Comparing the
// write shape against the read-back would make every sync see a difference and
// report "updated" on every run — the panel would never converge to unchanged.
func commandPanelItemName(cmd *command) string {
	if cmd == nil {
		return ""
	}
	if fields := strings.Fields(strings.TrimSpace(cmd.usage)); len(fields) > 0 {
		return truncateRunes(panelItemName(fields[0]), commandPanelItemNameMax)
	}
	if strings.TrimSpace(cmd.name) == "" {
		return ""
	}
	return truncateRunes(panelItemName("/"+cmd.name), commandPanelItemNameMax)
}

// panelItemName canonicalises an item name the same way the platform does, so a
// panel written before this rule (or by hand with a slash) still compares equal.
func panelItemName(name string) string {
	return strings.TrimPrefix(strings.TrimSpace(name), "/")
}

// SyncCommandPanel reconciles the group and c2c panels with the registry.
//
// Both scopes are attempted even when one fails, so a missing c2c permission
// does not block the group panel. A partial failure returns the results that
// did succeed together with the error text; the admin endpoint surfaces that
// text as upstream_error.
func (d *Dispatcher) SyncCommandPanel(ctx context.Context) ([]CommandPanelSyncResult, error) {
	if d == nil || d.qq == nil {
		return nil, errors.New("未配置 QQ 客户端，无法同步指令面板")
	}

	// 手动触发与自动同步可能同时发生：串行化，避免读到同一旧版本后各写一次。
	d.panelMu.Lock()
	defer d.panelMu.Unlock()

	items := d.commandPanelItems()
	results := make([]CommandPanelSyncResult, 0, 2)
	var failures []error
	for _, scope := range []string{qq.PanelScopeGroup, qq.PanelScopeC2C} {
		res, err := d.syncCommandPanelScope(ctx, scope, items)
		if err != nil {
			// Keep the platform error in the chain: the admin endpoint prints
			// its text, and CodeIs still works for callers that inspect it.
			failures = append(failures, fmt.Errorf("%s: %w", scope, err))
			continue
		}
		results = append(results, res)
	}
	if len(failures) > 0 {
		return results, errors.Join(failures...)
	}
	return results, nil
}

// syncCommandPanelScope creates, updates or leaves alone one scope's panel.
func (d *Dispatcher) syncCommandPanelScope(ctx context.Context, scope string, items []qq.PanelItem) (CommandPanelSyncResult, error) {
	existing, err := d.findCommandPanel(ctx, scope)
	if err != nil {
		return CommandPanelSyncResult{}, err
	}
	if existing == nil {
		panelID, err := d.qq.CreatePanel(ctx, qq.CreatePanelRequest{
			Scope:      scope,
			TargetType: qq.PanelTargetAll,
			Panel:      qq.Panel{Items: items, Remark: panelRemark},
		})
		if err != nil {
			return CommandPanelSyncResult{}, err
		}
		return CommandPanelSyncResult{
			Scope:   scope,
			PanelID: panelID,
			Action:  PanelActionCreated,
			// 创建接口只回 panel_id；首次版本未知，报 0。
			Version: 0,
		}, nil
	}

	if panelItemsEqual(existing.Panel.Items, items) {
		return CommandPanelSyncResult{
			Scope:   scope,
			PanelID: existing.PanelID,
			Action:  PanelActionUnchanged,
			Version: existing.Version,
		}, nil
	}

	version, err := d.qq.UpdatePanel(ctx, existing.PanelID, qq.Panel{
		Items:  items,
		Remark: panelRemark,
	})
	if err != nil {
		return CommandPanelSyncResult{}, err
	}
	return CommandPanelSyncResult{
		Scope:   scope,
		PanelID: existing.PanelID,
		Action:  PanelActionUpdated,
		Version: version,
	}, nil
}

// findCommandPanel pages through one scope looking for the panel carrying our
// marker. Panels with any other remark are left untouched.
func (d *Dispatcher) findCommandPanel(ctx context.Context, scope string) (*qq.PanelRecord, error) {
	cursor := ""
	for page := 0; page < commandPanelMaxPages; page++ {
		list, err := d.qq.ListPanels(ctx, scope, cursor, commandPanelPageSize)
		if err != nil {
			return nil, err
		}
		for i := range list.Records {
			record := &list.Records[i]
			// 服务端理论上已按 scope 过滤；再校验一次，避免把别的 scope
			// 里同名的面板改掉。
			if record.Scope != "" && record.Scope != scope {
				continue
			}
			if record.Panel.Remark == panelRemark {
				return record, nil
			}
		}
		if list.IsEnd || strings.TrimSpace(list.NextCursor) == "" {
			return nil, nil
		}
		cursor = list.NextCursor
	}
	return nil, errors.New("QQ 指令面板列表翻页次数过多")
}

// panelItemsEqual compares the platform view with the desired items. Order is
// significant: the sync emits a stable sorted list, and a reordered panel is
// exactly the kind of drift a refresh should fix.
func panelItemsEqual(got, want []qq.PanelItem) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		// Names are compared post-normalisation: the platform strips the leading
		// "/" on write, so a legacy panel holding "/help" must not look different
		// from a projection holding "help".
		if got[i].Type != want[i].Type ||
			panelItemName(got[i].Name) != panelItemName(want[i].Name) ||
			got[i].Desc != want[i].Desc ||
			got[i].OnlyAdmin != want[i].OnlyAdmin {
			return false
		}
	}
	return true
}

// Startup-sync timing. Kept as variables so tests can shorten them if needed;
// production values are fixed by the spec.
var (
	commandPanelInitialDelay = 8 * time.Second
	commandPanelBackoff      = []time.Duration{
		30 * time.Second,
		time.Minute,
		2 * time.Minute,
		5 * time.Minute,
		10 * time.Minute,
	}
)

// RunCommandPanelSync performs the startup sync: wait 8s (the platform token and
// network may not be ready yet), then retry failures with 30s/1m/2m/5m/10m
// backoff, at most 6 attempts in total.
//
// It never returns an error and never panics: every failure is a WARN, because
// a panel is cosmetic and a platform-side refusal (missing permission, content
// risk) must not keep the bot from starting. command_panel_enabled=false skips
// the automatic sync entirely; an admin can still trigger it manually.
func (d *Dispatcher) RunCommandPanelSync(ctx context.Context) {
	total := len(commandPanelBackoff) + 1
	delay := commandPanelInitialDelay
	log := d.log
	if log == nil {
		log = slogDefault()
	}

	for attempt := 1; attempt <= total; attempt++ {
		if !sleepContext(ctx, delay) {
			return
		}
		if d.settings != nil && !d.settings.Get().CommandPanelEnabled {
			log.Info("command panel auto sync skipped",
				"component", "command-panel", "reason", "command_panel_enabled=false")
			return
		}

		results, err := d.SyncCommandPanel(ctx)
		if err == nil {
			for _, res := range results {
				log.Info("command panel synced",
					"component", "command-panel",
					"scope", res.Scope,
					"panel_id", res.PanelID,
					"action", res.Action,
					"version", res.Version)
			}
			return
		}
		log.Warn("command panel sync failed",
			"component", "command-panel", "attempt", attempt,
			"max_attempts", total, "error", err.Error())

		if attempt < total {
			delay = commandPanelBackoff[attempt-1]
		}
	}
	log.Warn("command panel auto sync gave up",
		"component", "command-panel", "attempts", total)
}

// sleepContext waits for d, returning false when the context is cancelled.
func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// slogDefault keeps the nil-logger fallback in one place.
func slogDefault() *slog.Logger { return slog.Default() }
