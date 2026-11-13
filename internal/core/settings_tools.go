package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/tools"
)

// settingsDeniedKeys may never be written through a chat message, even by an
// admin: one is a secret, the other hands out privileges. Both stay editable in
// the admin panel.
var settingsDeniedKeys = map[string]string{
	"openai_api_key": "密钥不通过聊天修改，请到「系统设置」里填",
	"qq_app_secret":  "QQ Secret 不通过聊天修改，请到「接入与运维」里填",
	"admin_token":    "后台口令不通过聊天修改，请到「接入与运维」里改，避免被提示词注入锁定",
	"admin_openids":  "管理员名单不通过聊天修改（防止一条消息就把别人提权），请到「策略配置」里改",
}

// registerSettingsTools lets an admin change the harness through conversation.
func (d *Dispatcher) registerSettingsTools(reg *tools.Registry) {
	if reg == nil || d.settings == nil {
		return
	}
	reg.Register(&settingsReadTool{mgr: d.settings})
	reg.Register(&settingsWriteTool{mgr: d.settings})
}

type settingsReadTool struct{ mgr *settings.Manager }

func (t *settingsReadTool) Name() string     { return "settings_read" }
func (t *settingsReadTool) Group() string    { return "admin" }
func (t *settingsReadTool) Risk() tools.Risk { return tools.ReadOnly }

func (t *settingsReadTool) Description() string {
	return "读取当前 harness 的全部运行配置（模型、上下文、限流、人味、记忆、联网、执行、主动消息等）。" +
		"键名即返回值里的字段名，改之前先读一次。"
}

func (t *settingsReadTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}

func (t *settingsReadTool) Run(_ context.Context, _ json.RawMessage, env *tools.Env) (tools.Result, error) {
	if env == nil || !env.IsAdmin {
		return tools.Result{}, tools.ErrAdminOnly
	}
	return tools.Result{Text: renderSettings(t.mgr.Get())}, nil
}

// renderSettings marshals the effective settings with every secret blanked.
func renderSettings(s settings.Settings) string {
	safe := settings.MaskSecrets(s)
	raw, err := json.MarshalIndent(safe, "", "  ")
	if err != nil {
		return "（无法序列化配置）"
	}
	// 只读派生项太长，不进上下文。
	return string(raw)
}

type settingsWriteTool struct{ mgr *settings.Manager }

func (t *settingsWriteTool) Name() string     { return "settings_write" }
func (t *settingsWriteTool) Group() string    { return "admin" }
func (t *settingsWriteTool) Risk() tools.Risk { return tools.SideEffect }

func (t *settingsWriteTool) Description() string {
	return "修改 harness 的运行配置（保存即生效，可随时改回）。用法：先 settings_read 看清键名，" +
		"再传要改的键值对。键名写错会报错并列出所有键，不会静默忽略。"
}

func (t *settingsWriteTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"settings":{"type":"object","description":"要修改的键值对，例如 {\"temperature\":0.5,\"humanize_level\":\"soft\"}"}},
		"required":["settings"]}`)
}

func (t *settingsWriteTool) Run(ctx context.Context, raw json.RawMessage, env *tools.Env) (tools.Result, error) {
	if env == nil || !env.IsAdmin {
		return tools.Result{}, tools.ErrAdminOnly
	}
	var args struct {
		Settings map[string]json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return tools.Result{}, fmt.Errorf("参数需要 {\"settings\": {\"键\": 值}}: %w", err)
	}
	if len(args.Settings) == 0 {
		return tools.Result{}, fmt.Errorf("settings 不能为空")
	}
	for key := range args.Settings {
		if why, denied := settingsDeniedKeys[key]; denied {
			return tools.Result{}, fmt.Errorf("%s：%s", key, why)
		}
	}

	before := t.mgr.Get()
	updated, warnings, err := t.mgr.Apply(ctx, args.Settings)
	if err != nil {
		// 把可用键名一起回给模型，省掉一轮试错。
		return tools.Result{Text: fmt.Sprintf("没有改成：%v\n可用键名：%s",
			err, strings.Join(settingsKeys(), ", "))}, nil
	}

	changed := make([]string, 0, len(args.Settings))
	for key := range args.Settings {
		changed = append(changed, key)
	}
	sort.Strings(changed)

	var b strings.Builder
	fmt.Fprintf(&b, "已更新 %d 项：%s", len(changed), strings.Join(changed, ", "))
	if len(warnings) > 0 {
		fmt.Fprintf(&b, "\n注意：%s", strings.Join(warnings, "；"))
	}
	// 明确告诉模型哪些确实变了，避免它复述成"已全部生效"。
	if diff := describeDiff(before, updated); diff != "" {
		fmt.Fprintf(&b, "\n变化：%s", diff)
	}
	if updated.ExecEnabled && len(updated.AdminOpenIDs) == 0 {
		b.WriteString("\n提醒：exec_enabled 已开，但 admin_openids 为空，run_shell 仍然没人能用。")
	}
	return tools.Result{Text: b.String()}, nil
}

// describeDiff reports the handful of values that actually changed.
func describeDiff(before, after settings.Settings) string {
	keys := settingsKeys()
	parts := make([]string, 0, 4)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	var beforeMap, afterMap map[string]any
	_ = json.Unmarshal(b, &beforeMap)
	_ = json.Unmarshal(a, &afterMap)
	for _, key := range keys {
		// Secrets must never be echoed by a diff, even when they changed.
		switch key {
		case "openai_api_key", "qq_app_secret", "admin_token":
			continue
		}
		bv, av := beforeMap[key], afterMap[key]
		if fmt.Sprint(bv) == fmt.Sprint(av) {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %v → %v", key, bv, av))
		if len(parts) == 6 {
			parts = append(parts, "…")
			break
		}
	}
	return strings.Join(parts, "；")
}

// settingsKeys lists the flat JSON keys of Settings (what the model may write).
func settingsKeys() []string {
	raw, err := json.Marshal(settings.Settings{})
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}
