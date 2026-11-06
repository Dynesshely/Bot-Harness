package core

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 聊天内命令：管理员在聊天里用 `/命令 参数` 开关功能。
//
// 触发范围刻意收窄：只有「对机器人说话」的消息才算命令 —— `@机器人` 或
// c2c 私聊。全量群模式下机器人能看见所有消息，如果未 @ 的 `/xxx` 也当命令，
// 群友随口一句 `/tokenstats off` 就能改掉全局配置。未命中的消息原样走
// 普通消息管线（含触发门、人味、AI），行为与加命令之前完全一致。
//
// 命令的执行点在 dispatcher.route 里：去重/白名单/屏蔽词/触发门之后，
// 人味沉默判定之前。命中命令**不**进入 processMessage：不调模型、不润色、
// 不施加人味延迟、不发提前回复，也不写会话上下文。回复只是一条绑定
// 原 msg_id 的被动消息（每 msg 最多 5 条被动回复，所以只发一条）。

// commandOutput is what one chat command produced: plain text, a rendered PNG,
// or both. Text is always the fallback: when Image is set the sender tries the
// image first and falls back to Text if the upload or the media message fails,
// so a broken image path can never swallow the answer.
type commandOutput struct {
	Text     string
	Image    []byte
	ImageAlt string // audit/outbound description, e.g. "[图片] 聊天对象状态推测（8 人）"
}

// textOutput is the common case: a command that only has text.
func textOutput(text string) commandOutput { return commandOutput{Text: text} }

// command is one chat command.
type command struct {
	name      string
	aliases   []string
	adminOnly bool
	usage     string
	// desc is a one-line description used by /help and the QQ command panel.
	// Keep it at most 30 runes: that is the platform's item.desc limit.
	desc string
	// markdown asks the sender to deliver this command's reply as a markdown
	// message when markdown_enabled is on.
	markdown bool
	run      func(ctx context.Context, d *Dispatcher, in *Inbound, s settings.Settings, args []string) (out commandOutput, err error)
}

const (
	tokenStatsName  = "tokenstats"
	tokenStatsUsage = "/tokenstats [on|off|status]"
	tokenStatsDesc  = "开关 token 用量统计"

	helpName  = "help"
	helpUsage = "/help"
	helpDesc  = "查看可用命令"

	humanStatusName  = "human-status"
	humanStatusUsage = "/human-status [人数]"
	humanStatusDesc  = "分析最近24h发言者状态"

	// recordStatusCommand marks an inbound message that was handled as a
	// command: it neither entered the conversation nor was dropped.
	recordStatusCommand = "command"
)

// registerCommand adds a command and its aliases. A later registration of the
// same name wins, so a future command can deliberately replace a built-in.
func (d *Dispatcher) registerCommand(cmd *command) {
	if d.commands == nil {
		d.commands = map[string]*command{}
	}
	d.commands[strings.ToLower(cmd.name)] = cmd
	for _, alias := range cmd.aliases {
		d.commands[strings.ToLower(alias)] = cmd
	}
}

// registerBuiltinCommands wires the commands shipped with the bot. Keeping it
// in one place is what makes "add a command" a one-line change plus its run
// function.
func (d *Dispatcher) registerBuiltinCommands() {
	d.registerCommand(&command{
		name:      tokenStatsName,
		aliases:   []string{"tokens"},
		adminOnly: true,
		usage:     tokenStatsUsage,
		desc:      tokenStatsDesc,
		run:       runTokenStats,
	})
	// /human-status 是管理员命令：它展示的是对聊天对象的状态推测，
	// 属于运行期信息，不向全群公开。
	d.registerCommand(&command{
		name:      humanStatusName,
		aliases:   []string{"humanstatus"},
		adminOnly: true,
		usage:     humanStatusUsage,
		desc:      humanStatusDesc,
		markdown:  true,
		run:       runHumanStatus,
	})
	// /help 对所有人开放：它只描述已经注册的命令，不泄露任何配置或身份信息。
	d.registerCommand(&command{
		name:      helpName,
		adminOnly: false,
		usage:     helpUsage,
		desc:      helpDesc,
		run:       runHelp,
	})
}

// registeredCommands returns each command once (aliases point at the same
// *command), sorted by canonical name so both /help and the panel are stable.
func (d *Dispatcher) registeredCommands() []*command {
	if d == nil {
		return nil
	}
	seen := make(map[*command]bool, len(d.commands))
	out := make([]*command, 0, len(d.commands))
	for _, cmd := range d.commands {
		if cmd == nil || seen[cmd] {
			continue
		}
		seen[cmd] = true
		out = append(out, cmd)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].name == out[j].name {
			return out[i].usage < out[j].usage
		}
		return out[i].name < out[j].name
	})
	return out
}

// addressed reports whether the message is something a command may answer:
// an @ mention in a group, or any c2c message (a private chat has no @).
func addressed(in *Inbound) bool {
	return in != nil && (in.AtBot || in.Scene == "c2c")
}

// parseCommand recognises the classic `/name args` shape and returns the
// lower-cased name plus the arguments after it.
//
// It is syntactic only: whether the name is registered, and whether the caller
// may run it, are decided later. Ordering matters:
//   - the trigger check comes first, so an un-addressed group message is never
//     even considered a command;
//   - only the ASCII slash starts a command (full-width ／ stays ordinary text);
//   - whitespace splitting uses strings.Fields, so repeated spaces and tabs are
//     handled and an empty command name ("/" alone) is not a command.
func parseCommand(in *Inbound) (name string, args []string, ok bool) {
	if !addressed(in) {
		return "", nil, false
	}
	text := strings.TrimSpace(in.Content)
	if text == "" || text[0] != '/' {
		return "", nil, false
	}
	fields := strings.Fields(text[1:])
	if len(fields) == 0 {
		return "", nil, false
	}
	return strings.ToLower(fields[0]), fields[1:], true
}

// parseBareCommand recognises the QQ command-panel tap shape: an addressed
// message whose entire content is exactly one token, with no slash.
//
// The platform strips a leading "/" from item.name (writing "/help" reads back
// as "help") and the client inserts that name verbatim into the input box, so a
// tap sends "tokenstats" — a message the slash-only parser used to classify as
// ordinary chat. The panel would look alive but do nothing.
//
// Only the *syntax* is decided here, and it is deliberately strict:
//   - the whole trimmed message must be one token, so "help me with X" stays
//     ordinary chat instead of turning a question into a command;
//   - registration is not known here; lookupCommand rejects unregistered
//     single words, so "foo" keeps flowing through the ordinary message path.
func parseBareCommand(in *Inbound) (name string, ok bool) {
	if !addressed(in) {
		return "", false
	}
	text := strings.TrimSpace(in.Content)
	if text == "" || text[0] == '/' {
		return "", false
	}
	fields := strings.Fields(text)
	if len(fields) != 1 {
		return "", false
	}
	return strings.ToLower(fields[0]), true
}

// lookupCommand resolves an inbound message against the registry.
//
// Two shapes are accepted:
//   - `/name args` — the slash form, any arguments allowed;
//   - the panel-tap form: the whole message is exactly one token that names a
//     registered command or alias, in which case args is empty and the command
//     runs with its default action (for /tokenstats that is "status").
//
// ok=false covers "not a command syntactically", "addressed to the bot but not a
// known command" and "bare single word that is not registered"; an unknown
// `/xxx` and an unregistered bare word therefore keep flowing through the
// ordinary message path instead of getting a "no such command" reply.
func (d *Dispatcher) lookupCommand(in *Inbound) (*command, []string, bool) {
	if name, args, ok := parseCommand(in); ok {
		if cmd := d.commands[name]; cmd != nil {
			return cmd, args, true
		}
		return nil, nil, false
	}
	name, ok := parseBareCommand(in)
	if !ok {
		return nil, nil, false
	}
	cmd := d.commands[name]
	if cmd == nil {
		return nil, nil, false
	}
	return cmd, nil, true
}

// commandAllowed applies the admin-only gate, using the same identity rule as
// core.isAdmin (member_openid or user_openid in admin_openids).
//
// A non-admin gets silence, not "unknown command" or "no permission": in a
// group either of those would confirm that the command exists. The audit row is
// the only place that records what happened.
func (d *Dispatcher) commandAllowed(cmd *command, s settings.Settings, in *Inbound) bool {
	if !cmd.adminOnly {
		return true
	}
	return d.isAdmin(s, in)
}

// handleCommand executes one command and delivers its output.
//
// Frame order matters: the inbound row reaches its terminal status ("command")
// and is published BEFORE the outbound row exists, so the live SSE stream shows
// in/command first and out/replied second — the same order the audit table
// reads in (docs/ADMIN-API.md, §SSE). Publishing the outbound frame first made
// the first frame of a command "out/replied" with no inbound counterpart yet.
//
// The outbound audit row is written even when delivery fails (status=failed),
// mirroring the normal reply path; the inbound row's terminal status is
// "command" either way, because the message was handled as a command rather
// than answered by the model.
func (d *Dispatcher) handleCommand(in *Inbound, conv convTarget, rec *store.Record, cmd *command, args []string, s settings.Settings) {
	if !d.commandAllowed(cmd, s, in) {
		d.metrics.EventsIgnored.Add(1)
		d.logIgnored(in, "command: 非管理员")
		d.finishRecord(rec, "ignored", "command: 非管理员", 0)
		return
	}

	out, err := cmd.run(context.Background(), d, in, s, args)
	if err != nil {
		// A failed command still answers: the operator has to learn that the
		// change did not stick. Other commands and the reply pipeline are not
		// affected. A failed run has no image to fall back to.
		out = textOutput("修改失败：" + err.Error())
		d.log.Warn("command failed", "component", "command", "command", cmd.name,
			"member", in.MemberName, "group_openid", in.GroupOpenID, "error", err.Error())
	} else {
		d.log.Info("command handled", "component", "command", "command", cmd.name,
			"member", in.MemberName, "group_openid", in.GroupOpenID,
			"args", strings.Join(args, " "))
	}

	// Finalise the inbound row first: the admin stream must never show a command's
	// reply before the message that triggered it.
	d.finishRecord(rec, recordStatusCommand, "", 0)

	opts := qq.SendOptions{MsgID: in.MsgID, MaxChars: s.ReplyMaxChars, MaxSegments: 1}
	if cmd.markdown && s.MarkdownEnabled {
		opts.Markdown = true
	}
	if in.Scene == "c2c" {
		opts.UserOpenID = in.UserOpenID
	} else {
		opts.GroupOpenID = in.GroupOpenID
	}
	sendCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// content is what the audit row and the SSE frame describe. An image is
	// recorded with its alt text (the operator sees a description, not a blob);
	// a text fallback is recorded verbatim.
	content := out.Text
	var (
		res     *qq.SendResult
		sendErr error
	)
	if len(out.Image) > 0 && d.sender != nil {
		res, sendErr = d.sender.SendImage(sendCtx, opts, out.Image)
		if sendErr == nil {
			content = strings.TrimSpace(out.ImageAlt)
			if content == "" {
				content = "[图片]"
			}
		} else {
			// The command succeeded; only the image delivery did not. Fall back to
			// the text version the command already built, and keep the reason in
			// the log (not in the reply).
			d.log.Warn("command image failed; falling back to text",
				"component", "sender", "command", cmd.name, "error", sendErr.Error())
			fallback := strings.TrimSpace(out.Text)
			if fallback == "" {
				fallback = "图片发送失败"
			}
			res, sendErr = d.sender.Send(sendCtx, opts, fallback)
			content = fallback
		}
	} else {
		res, sendErr = d.sender.Send(sendCtx, opts, out.Text)
	}

	outRec := &store.Record{
		Direction:      "out",
		Scene:          conv.Scene,
		ConversationID: conv.ID,
		GroupOpenID:    conv.GroupOpenID,
		MemberOpenID:   in.MemberOpenID,
		MemberName:     in.MemberName,
		Content:        content,
		MsgID:          in.MsgID,
		Status:         "replied",
	}
	if sendErr != nil {
		outRec.Status = "failed"
		outRec.Error = sendErr.Error()
		d.metrics.RepliesFailed.Add(1)
		d.stats.RecordError()
		d.log.Warn("command reply failed", "component", "sender", "command", cmd.name,
			"error", sendErr.Error())
	} else {
		if res != nil {
			outRec.Delivery = store.DeliveryPassive
			if !res.Passive {
				outRec.Delivery = store.DeliveryActive
			}
		}
		d.metrics.RepliesSent.Add(1)
		d.stats.RecordOut()
	}
	if id, err := d.st.AddRecord(context.Background(), outRec); err == nil {
		outRec.ID = id
	}
	d.publish("message", outRec)
}

// applySettings writes one or more settings keys through the manager, so they
// are validated, persisted and effective immediately just like the admin panel.
func (d *Dispatcher) applySettings(ctx context.Context, patch map[string]json.RawMessage) error {
	if d.settings == nil {
		return errors.New("设置管理器未初始化")
	}
	_, _, err := d.settings.Apply(ctx, patch)
	return err
}

// runTokenStats switches the per-reply footer ("—— 用时 3.2s · 约 128 tokens").
// The footer is presentation only: reply content, context and model calls are
// unaffected either way.
func runTokenStats(ctx context.Context, d *Dispatcher, in *Inbound, s settings.Settings, args []string) (commandOutput, error) {
	action := "status"
	if len(args) > 1 {
		return textOutput("用法：" + tokenStatsUsage), nil
	}
	if len(args) == 1 {
		action = strings.ToLower(args[0])
	}
	switch action {
	case "", "status":
		state := "已关闭"
		if s.ReplyFooterEnabled {
			state = "已开启"
		}
		return textOutput("token 用量统计：" + state + "\n用法：" + tokenStatsUsage), nil
	case "on":
		if err := d.applySettings(ctx, map[string]json.RawMessage{
			"reply_footer_enabled": json.RawMessage("true"),
		}); err != nil {
			return commandOutput{}, err
		}
		return textOutput("✅ 已开启 token 用量统计：之后每条 AI 回复末尾会追加「用时 x.xs · 约 N tokens」。仅影响页脚，回复内容与上下文不变。"), nil
	case "off":
		if err := d.applySettings(ctx, map[string]json.RawMessage{
			"reply_footer_enabled": json.RawMessage("false"),
		}); err != nil {
			return commandOutput{}, err
		}
		return textOutput("✅ 已关闭 token 用量统计：之后 AI 回复不再追加用时/token 页脚。"), nil
	default:
		return textOutput("用法：" + tokenStatsUsage), nil
	}
}

// runHelp lists every registered command.
//
// Aliases are collapsed by registeredCommands, so "/tokens" never appears as a
// second line. Admin-only commands are marked in the text; the command itself
// stays available to everyone (it only discloses names and descriptions).
//
// The final line states both accepted shapes in the same words as README
// §5.13.1 and D-043: the slash form for anything with arguments, and the panel
// tap — a bare command name the platform inserted after stripping the leading
// "/" — which runs the command with its default action. It goes last because
// the command list is the answer to "/help"; the hint is the footnote.
func runHelp(_ context.Context, d *Dispatcher, _ *Inbound, _ settings.Settings, _ []string) (commandOutput, error) {
	commands := d.registeredCommands()
	lines := make([]string, 0, len(commands)+2)
	lines = append(lines, "可用命令：")
	for _, cmd := range commands {
		usage := strings.TrimSpace(cmd.usage)
		if usage == "" {
			usage = "/" + cmd.name
		}
		line := usage
		if desc := strings.TrimSpace(cmd.desc); desc != "" {
			line += " — " + desc
		}
		if cmd.adminOnly {
			line += "（仅管理员）"
		}
		lines = append(lines, line)
	}
	// The panel-tap path is easy to miss: the platform inserts the bare name it
	// read back (it strips the leading "/" on write), so a tap sends "tokenstats".
	// Say it where people look for commands, not only in the README.
	lines = append(lines,
		"点击指令面板会填入不带斜杠的命令名（如 tokenstats），等价于 /tokenstats status；带参数请用 /命令 参数。")
	return textOutput(strings.Join(lines, "\n")), nil
}
