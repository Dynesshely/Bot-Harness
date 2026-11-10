package core

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

func joinGateSettings() settings.Settings {
	s := settings.Defaults()
	s.JoinGateEnabled = true
	s.JoinGateMode = "llm"
	s.JoinGateWindow = 20
	s.JoinGateConfidence = 0.7
	s.JoinGateEvalCooldownSeconds = 60
	s.JoinGateMinIntervalMinutes = 5
	s.JoinGateMaxPerHour = 3
	s.JoinGateMaxPerDay = 12
	s.JoinGateWaitRetrySeconds = 30
	return s
}

func TestJoinGateSystemPromptCarriesTheContract(t *testing.T) {
	for _, want := range []string{
		joinGateMarker, "join", "wait", "skip", "addressed_to", "confidence",
		"topics", "message_ids", "reason", "type",
	} {
		if !strings.Contains(joinGateSystemPrompt, want) {
			t.Fatalf("system prompt lost %q", want)
		}
	}
	// v2 的三分类与强信号说明必须写进提示词，否则模型只会输出旧布尔判定。
	for _, want := range []string{"引用 / 点名 / 公开求助是强信号", "两人私聊", "话题还在发展时选 wait"} {
		if !strings.Contains(joinGateSystemPrompt, want) {
			t.Fatalf("system prompt lost the v2 guidance %q", want)
		}
	}
	// 话题生命周期是 v3 的重点：模型必须知道 status / resumable 的写法与「上次话题」的语义。
	for _, want := range []string{
		"status", "resumable", "上次话题", "closed 的话题不要再接", "旧事重提",
		"dormant 的话题只有当前消息明显接回它时才可 wait / join",
	} {
		if !strings.Contains(joinGateSystemPrompt, want) {
			t.Fatalf("system prompt lost the topic lifecycle guidance %q", want)
		}
	}
}

func TestParseJoinGateOutputAcceptsSurroundingProse(t *testing.T) {
	raw := "好的，判断如下：\n" + `{"topics":[{"id":"t1","title":"性能问题","summary":"群里在讨论慢查询","message_ids":[12,13],"type":"question"}],"current_topic_id":"t1","addressed_to":"group","action":"join","confidence":0.82,"reason":"有人明确求助"}` + "\n以上。"
	got, err := parseJoinGateOutput(raw, map[int64]bool{12: true, 13: true})
	if err != nil {
		t.Fatalf("valid verdict rejected: %v", err)
	}
	if got.Action != joinGateActionJoin || got.AddressedTo != joinGateAddressedGroup {
		t.Fatalf("verdict action/addressed_to parsed wrong: %+v", got)
	}
	if got.Confidence != 0.82 || got.CurrentTopicID != "t1" || got.Reason != "有人明确求助" {
		t.Fatalf("verdict parsed wrong: %+v", got)
	}
	if got.Topics[0].Type != joinGateTopicTypeQuestion {
		t.Fatalf("topic type = %q, want question", got.Topics[0].Type)
	}
	if len(got.Topics) != 1 || got.Topics[0].ID != "t1" || len(got.Topics[0].MessageIDs) != 2 {
		t.Fatalf("topics parsed wrong: %+v", got.Topics)
	}
}

func TestParseJoinGateOutputClampsTitleSummary(t *testing.T) {
	long := strings.Repeat("长", 100)
	raw := `{"topics":[{"id":"t1","title":"` + long + `","summary":"` + long + `","message_ids":[],"type":"chat"}],"addressed_to":"group","action":"skip","confidence":0.2,"reason":"闲聊"}`
	got, err := parseJoinGateOutput(raw, map[int64]bool{})
	if err != nil {
		t.Fatalf("verdict rejected: %v", err)
	}
	if n := len([]rune(got.Topics[0].Title)); n != joinGateTopicTitleRunes {
		t.Fatalf("title should be clamped to %d runes, got %d", joinGateTopicTitleRunes, n)
	}
	if n := len([]rune(got.Topics[0].Summary)); n != joinGateTopicSumRunes {
		t.Fatalf("summary should be clamped to %d runes, got %d", joinGateTopicSumRunes, n)
	}
}

func TestParseJoinGateOutputFailClosed(t *testing.T) {
	valid := `{"topics":[{"id":"t1","title":"话题","summary":"摘要","message_ids":[7],"type":"help"}],"addressed_to":"bot","action":"join","confidence":0.9,"reason":"求助"}`
	longReason := strings.Repeat("因", joinGateReasonRunes+1)
	tooMany := make([]map[string]any, joinGateMaxTopics+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"id": "t" + string(rune('a'+i)), "title": "t"}
	}
	tooManyJSON, _ := json.Marshal(map[string]any{"topics": tooMany, "action": "join", "confidence": 0.9, "reason": "x"})

	cases := []struct {
		name  string
		raw   string
		allow map[int64]bool
	}{
		{"not json", "我觉得可以加入", map[int64]bool{7: true}},
		{"broken json", `{"topics":[}`, map[int64]bool{7: true}},
		{"no topics", `{"topics":[],"action":"join","confidence":0.9,"reason":"x"}`, map[int64]bool{7: true}},
		{"missing action", `{"topics":[{"id":"t1"}],"addressed_to":"group","confidence":0.9,"reason":"x"}`, map[int64]bool{}},
		{"illegal action", `{"topics":[{"id":"t1"}],"addressed_to":"group","action":"maybe","confidence":0.9,"reason":"x"}`, map[int64]bool{}},
		{"too many topics", string(tooManyJSON), map[int64]bool{}},
		{"unknown message id", valid, map[int64]bool{8: true}},
		{"confidence too high", `{"topics":[{"id":"t1"}],"action":"join","confidence":1.2,"reason":"x"}`, map[int64]bool{}},
		{"confidence negative", `{"topics":[{"id":"t1"}],"action":"join","confidence":-0.1,"reason":"x"}`, map[int64]bool{}},
		{"reason too long", `{"topics":[{"id":"t1","title":"t"}],"action":"skip","confidence":0.1,"reason":"` + longReason + `"}`, map[int64]bool{}},
		{"empty topic id", `{"topics":[{"id":"","title":"t"}],"action":"skip","confidence":0.1,"reason":"x"}`, map[int64]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseJoinGateOutput(tc.raw, tc.allow); err == nil {
				t.Fatalf("malformed verdict must be rejected: %s", tc.raw)
			}
		})
	}
}

func TestJoinGateSkipReasonOrdering(t *testing.T) {
	s := joinGateSettings()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	passing := joinGateSnapshot{JoinsLastHour: 1, JoinsLastDay: 2}

	cases := []struct {
		name    string
		content string
		snap    joinGateSnapshot
		want    string
	}{
		{"too short", " 嗯 ", passing, "skip:"},
		{"command", "/help", passing, "skip: 命令消息"},
		{"cooldown", "在吗", joinGateSnapshot{LastDecisionAt: now.Add(-30 * time.Second).Format(time.RFC3339), JoinsLastHour: 0}, "cooldown:"},
		{"hourly cap", "在吗", joinGateSnapshot{JoinsLastHour: 3, JoinsLastDay: 4}, "rate: 每小时"},
		{"daily cap", "在吗", joinGateSnapshot{JoinsLastHour: 1, JoinsLastDay: 12}, "rate: 每天"},
		{"min interval", "在吗", joinGateSnapshot{JoinsLastHour: 1, JoinsLastDay: 2, JoinsSinceMinInterval: 1}, "rate: 距上次加入"},
		{"all clear", "在吗", joinGateSnapshot{LastDecisionAt: now.Add(-2 * time.Minute).Format(time.RFC3339), JoinsLastHour: 1, JoinsLastDay: 2}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := joinGateSkipReason(now, tc.content, tc.snap, s)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("expected to pass the cheap gates, got %q", got)
				}
				return
			}
			if !strings.HasPrefix(got, tc.want) {
				t.Fatalf("reason = %q, want prefix %q", got, tc.want)
			}
		})
	}
}

func TestJoinGateCooldownExpires(t *testing.T) {
	s := joinGateSettings()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	snap := joinGateSnapshot{LastDecisionAt: now.Add(-61 * time.Second).Format(time.RFC3339)}
	if got := joinGateSkipReason(now, "在吗", snap, s); got != "" {
		t.Fatalf("an expired cooldown must not skip, got %q", got)
	}
	s.JoinGateEvalCooldownSeconds = 120
	if got := joinGateSkipReason(now, "在吗", snap, s); !strings.HasPrefix(got, "cooldown:") {
		t.Fatalf("expected the longer cooldown to skip, got %q", got)
	}
}

// 三分类是 v2 契约：join / wait / skip 都要能解析，大小写与空白要归一。
func TestParseJoinGateOutputThreeActions(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"join", `{"topics":[{"id":"t1"}],"addressed_to":"group","action":"join","confidence":0.8,"reason":"x"}`, joinGateActionJoin},
		{"wait", `{"topics":[{"id":"t1"}],"addressed_to":"someone_else","action":"wait","confidence":0.6,"reason":"x"}`, joinGateActionWait},
		{"skip", `{"topics":[{"id":"t1"}],"addressed_to":"unknown","action":"skip","confidence":0.2,"reason":"x"}`, joinGateActionSkip},
		{"uppercase and spaces", `{"topics":[{"id":"t1"}],"action":" WAIT ","confidence":0.5,"reason":"x"}`, joinGateActionWait},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseJoinGateOutput(tc.raw, map[int64]bool{})
			if err != nil {
				t.Fatalf("action %q rejected: %v", tc.want, err)
			}
			if got.Action != tc.want {
				t.Fatalf("action = %q, want %q", got.Action, tc.want)
			}
		})
	}
}

// addressed_to 与 topic.type 是标签，不是闸门：未知值归一化，不能让整次判定失败。
func TestParseJoinGateOutputNormalizesLabels(t *testing.T) {
	raw := `{"topics":[{"id":"t1","title":"闲聊","summary":"","message_ids":[],"type":"WEEKEND"},{"id":"t2","type":""}],"current_topic_id":"t1","addressed_to":"everyone","action":"skip","confidence":0.3,"reason":"闲聊"}`
	got, err := parseJoinGateOutput(raw, map[int64]bool{})
	if err != nil {
		t.Fatalf("label normalisation must not reject the verdict: %v", err)
	}
	if got.AddressedTo != joinGateAddressedUnknown {
		t.Fatalf("addressed_to = %q, want unknown", got.AddressedTo)
	}
	if got.Topics[0].Type != joinGateTopicTypeOther || got.Topics[1].Type != joinGateTopicTypeOther {
		t.Fatalf("unknown/empty type must become other: %+v", got.Topics)
	}
	// 缺失 addressed_to 同样是 unknown，而不是解析失败。
	got, err = parseJoinGateOutput(`{"topics":[{"id":"t1"}],"action":"skip","confidence":0.1,"reason":"x"}`, map[int64]bool{})
	if err != nil || got.AddressedTo != joinGateAddressedUnknown {
		t.Fatalf("missing addressed_to must be unknown: %+v err=%v", got, err)
	}
}

// wait 重试：上次判定是 wait 且满 join_gate_wait_retry_seconds，才允许绕过评估冷却；
// 其他 action、未满重试窗口，以及频率闸门都不受影响。
func TestJoinGateWaitRetryBypassesCooldownOnlyForWait(t *testing.T) {
	s := joinGateSettings()
	s.JoinGateEvalCooldownSeconds = 600
	s.JoinGateWaitRetrySeconds = 30
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)

	base := joinGateSnapshot{
		LastDecisionAt: now.Add(-31 * time.Second).Format(time.RFC3339),
		LastAction:     joinGateActionWait,
	}
	if got := joinGateSkipReason(now, "在吗", base, s); got != "" {
		t.Fatalf("a wait older than the retry window must re-evaluate, got %q", got)
	}

	tooSoon := base
	tooSoon.LastDecisionAt = now.Add(-29 * time.Second).Format(time.RFC3339)
	if got := joinGateSkipReason(now, "在吗", tooSoon, s); !strings.HasPrefix(got, "cooldown:") {
		t.Fatalf("inside the retry window the cooldown must still hold, got %q", got)
	}

	notWait := base
	notWait.LastAction = joinGateActionSkip
	if got := joinGateSkipReason(now, "在吗", notWait, s); !strings.HasPrefix(got, "cooldown:") {
		t.Fatalf("only wait unlocks the cooldown, got %q", got)
	}

	// 频率闸门不能被 wait 重试绕过：每小时上限仍然生效。
	capped := base
	capped.JoinsLastHour = 3
	if got := joinGateSkipReason(now, "在吗", capped, s); !strings.HasPrefix(got, "rate: 每小时") {
		t.Fatalf("wait retry must not bypass the hourly cap, got %q", got)
	}

	// 最小的加入间隔同样生效。
	minInterval := base
	minInterval.JoinsSinceMinInterval = 1
	if got := joinGateSkipReason(now, "在吗", minInterval, s); !strings.HasPrefix(got, "rate: 距上次加入") {
		t.Fatalf("wait retry must not bypass the minimum join interval, got %q", got)
	}
}
func TestBuildJoinGateContextUsesIDsAndTruncates(t *testing.T) {
	content := strings.Repeat("字", joinGateMessageRunes+20)
	recent := []store.GroupMessage{
		{ID: 11, ConversationID: "c1", Role: "user", Speaker: "小明", Content: content},
		{ID: 12, ConversationID: "c1", Role: "assistant", Speaker: "小助手", Content: "旧回复一"},
		{ID: 13, ConversationID: "c1", Role: "assistant", Speaker: "小助手", Content: "旧回复二"},
		{ID: 14, ConversationID: "c1", Role: "assistant", Speaker: "小助手", Content: "旧回复三"},
	}
	current := &Inbound{MemberName: "小红", Content: "有人会 Go 吗"}
	text, allowed := buildJoinGateContext(recent, "c1", current)

	if !strings.Contains(text, "[#11] 小明: ") {
		t.Fatalf("recent message lost its id/name label:\n%s", text)
	}
	if !strings.Contains(text, strings.Repeat("字", joinGateMessageRunes)) ||
		strings.Contains(text, strings.Repeat("字", joinGateMessageRunes+1)) {
		t.Fatalf("history content must be capped at %d runes", joinGateMessageRunes)
	}
	lines := strings.Split(text, "\n")
	if lines[len(lines)-1] != "[#0] 小红: 有人会 Go 吗" {
		t.Fatalf("current message must be the last line, got %q", lines[len(lines)-1])
	}
	if !allowed[0] || !allowed[11] {
		t.Fatalf("allowed ids missing: %+v", allowed)
	}
	if allowed[0] && !strings.Contains(text, "[#0]") {
		t.Fatal("current message must be rendered with id 0")
	}
	botCount := strings.Count(text, "[bot]")
	if botCount != 2 {
		t.Fatalf("only the last two bot replies may be referenced, got %d", botCount)
	}
	if strings.Contains(text, "旧回复一") {
		t.Fatalf("the oldest bot reply should have been dropped:\n%s", text)
	}
}

// 引用上下文标注：当前消息引用窗口内的谁必须写清楚；引用机器人时标「你」，
// 引用窗口外的消息不得编造 id。
func TestBuildJoinGateContextAnnotatesQuotes(t *testing.T) {
	recent := []store.GroupMessage{
		{ID: 21, ConversationID: "c1", Role: "user", Speaker: "小明", Content: "有人会 Go 吗", QuoteIndex: "REF_Q_USER"},
		{ID: 22, ConversationID: "c1", Role: "assistant", Speaker: "小助手", Content: "我会一点", QuoteIndex: "REF_Q_BOT"},
	}
	current := &Inbound{MemberName: "小红", Content: "真的吗", RefMsgIndex: "REF_Q_USER"}
	text, _ := buildJoinGateContext(recent, "c1", current)
	lines := strings.Split(text, "\n")
	if !strings.Contains(lines[len(lines)-1], "（回复 [#21]）") {
		t.Fatalf("quote of a window message not annotated: %q", lines[len(lines)-1])
	}
	if !strings.Contains(text, "[#21] 小明: 有人会 Go 吗（当前消息引用了这条）") {
		t.Fatalf("quoted window line not marked:\n%s", text)
	}

	// 引用机器人：目标语义是「你」，而不是某个 [#id]。
	current = &Inbound{MemberName: "小红", Content: "真的吗", RefMsgIndex: "REF_Q_BOT"}
	text, _ = buildJoinGateContext(recent, "c1", current)
	lines = strings.Split(text, "\n")
	if !strings.Contains(lines[len(lines)-1], "（回复 你）") {
		t.Fatalf("quote of the bot not annotated as 你: %q", lines[len(lines)-1])
	}
	if !strings.Contains(text, "[bot] 小助手: 我会一点（当前消息引用了这条）") {
		t.Fatalf("quoted bot line not marked:\n%s", text)
	}

	// 引用窗口外的消息：仍要给出提示，但不能编造 id。
	current = &Inbound{MemberName: "小红", Content: "真的吗", RefMsgIndex: "REF_UNKNOWN"}
	text, _ = buildJoinGateContext(recent, "c1", current)
	lines = strings.Split(text, "\n")
	if !strings.Contains(lines[len(lines)-1], "（引用了窗口外的一条消息）") {
		t.Fatalf("out-of-window quote must say so: %q", lines[len(lines)-1])
	}

	// 没有引用时不能凭空加标注。
	current = &Inbound{MemberName: "小红", Content: "真的吗"}
	text, _ = buildJoinGateContext(recent, "c1", current)
	if strings.Contains(text, "（回复") || strings.Contains(text, "当前消息引用了这条") {
		t.Fatalf("no reference must produce no annotation:\n%s", text)
	}
}
func TestBuildJoinGateContextSkipsOtherConversationsBotReplies(t *testing.T) {
	recent := []store.GroupMessage{
		{ID: 21, ConversationID: "other", Role: "assistant", Speaker: "小助手", Content: "别人的会话"},
		{ID: 22, ConversationID: "mine", Role: "assistant", Speaker: "小助手", Content: "我的会话"},
	}
	text, _ := buildJoinGateContext(recent, "mine", &Inbound{Content: "继续"})
	if strings.Contains(text, "别人的会话") {
		t.Fatalf("bot replies from another conversation leaked in:\n%s", text)
	}
	if !strings.Contains(text, "[bot] 小助手: 我的会话") {
		t.Fatalf("same-conversation bot reply missing:\n%s", text)
	}
}

func TestRenderJoinTopics(t *testing.T) {
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	topics := `[{"id":"t1","title":"部署","summary":"在聊容器部署","type":"plan"},{"id":"t2","title":"周末"}]`
	fresh := now.Add(-5 * time.Minute).Format(time.RFC3339)

	got := renderJoinTopics(topics, fresh, now)
	if !strings.Contains(got, "群里最近的话题（仅背景，不要强行转回）") {
		t.Fatalf("background block lost its guard line: %q", got)
	}
	if !strings.Contains(got, "- 部署（进行中 · plan）：在聊容器部署") || !strings.Contains(got, "- 周末（进行中）") {
		t.Fatalf("topics rendered wrong: %q", got)
	}
	// other 是缺省标签，不显示噪音。
	if strings.Contains(got, "other") {
		t.Fatalf("the default type must not be rendered: %q", got)
	}

	if got := renderJoinTopics(topics, now.Add(-16*time.Minute).Format(time.RFC3339), now); got != "" {
		t.Fatalf("stale topics must be ignored, got %q", got)
	}
	if got := renderJoinTopics(topics, "", now); got != "" {
		t.Fatalf("missing analyzed_at must be ignored, got %q", got)
	}
	if got := renderJoinTopics("not-json", fresh, now); got != "" {
		t.Fatalf("broken topics JSON must be ignored, got %q", got)
	}
	if got := renderJoinTopics(`[]`, fresh, now); got != "" {
		t.Fatalf("empty topics must be ignored, got %q", got)
	}
	if got := renderJoinTopics(topics, now.Add(5*time.Minute).Format(time.RFC3339), now); got != "" {
		t.Fatalf("a future timestamp must be ignored, got %q", got)
	}

	// 400 字的硬上限。
	longTopic := `[{"id":"t1","title":"` + strings.Repeat("长", 250) + `","summary":"` + strings.Repeat("文", 250) + `"}]`
	if got := renderJoinTopics(longTopic, fresh, now); len([]rune(got)) != joinGateBackgroundRunes {
		t.Fatalf("background block should be capped at %d runes, got %d", joinGateBackgroundRunes, len([]rune(got)))
	}
}

func TestJoinGateSettingsDefaults(t *testing.T) {
	s := settings.Defaults()
	if s.JoinGateEnabled {
		t.Fatal("join_gate_enabled must be off by default")
	}
	if s.JoinGateMode != "llm" {
		t.Fatalf("join_gate_mode default = %q, want llm", s.JoinGateMode)
	}
	if s.JoinGateModel != "" {
		t.Fatalf("join_gate_model default = %q, want empty", s.JoinGateModel)
	}
	if s.JoinGateWindow != 20 || s.JoinGateConfidence != 0.7 ||
		s.JoinGateEvalCooldownSeconds != 60 || s.JoinGateMinIntervalMinutes != 5 ||
		s.JoinGateMaxPerHour != 3 || s.JoinGateMaxPerDay != 12 {
		t.Fatalf("join gate defaults changed: %+v", s)
	}
	if s.JoinGateWaitRetrySeconds != 30 {
		t.Fatalf("join_gate_wait_retry_seconds default = %d, want 30", s.JoinGateWaitRetrySeconds)
	}
	if s.JoinGateTopicDormantMinutes != 20 || s.JoinGateTopicClosedMinutes != 180 {
		t.Fatalf("topic lifecycle defaults = %d/%d, want 20/180",
			s.JoinGateTopicDormantMinutes, s.JoinGateTopicClosedMinutes)
	}
}

func TestJoinGateSettingsApplyAndValidate(t *testing.T) {
	m, err := settings.New(context.Background(), nil, settings.Defaults())
	if err != nil {
		t.Fatalf("new settings: %v", err)
	}
	good := map[string]json.RawMessage{
		"join_gate_enabled":               json.RawMessage("true"),
		"join_gate_mode":                  json.RawMessage("\"LLM\""),
		"join_gate_model":                 json.RawMessage("\" gate-model \""),
		"join_gate_window":                json.RawMessage("5"),
		"join_gate_confidence":            json.RawMessage("0.5"),
		"join_gate_eval_cooldown_seconds": json.RawMessage("10"),
		"join_gate_min_interval_minutes":  json.RawMessage("1"),
		"join_gate_max_per_hour":          json.RawMessage("1"),
		"join_gate_max_per_day":           json.RawMessage("1"),
		"join_gate_wait_retry_seconds":    json.RawMessage("45"),
		"join_gate_topic_dormant_minutes": json.RawMessage("30"),
		"join_gate_topic_closed_minutes":  json.RawMessage("300"),
	}
	got, _, err := m.Apply(context.Background(), good)
	if err != nil {
		t.Fatalf("valid join gate settings rejected: %v", err)
	}
	if !got.JoinGateEnabled || got.JoinGateMode != "llm" || got.JoinGateModel != "gate-model" ||
		got.JoinGateWindow != 5 || got.JoinGateConfidence != 0.5 ||
		got.JoinGateEvalCooldownSeconds != 10 || got.JoinGateMinIntervalMinutes != 1 ||
		got.JoinGateMaxPerHour != 1 || got.JoinGateMaxPerDay != 1 ||
		got.JoinGateWaitRetrySeconds != 45 ||
		got.JoinGateTopicDormantMinutes != 30 || got.JoinGateTopicClosedMinutes != 300 {
		t.Fatalf("join gate settings not applied: %+v", got)
	}

	bad := []map[string]json.RawMessage{
		{"join_gate_mode": json.RawMessage("\"auto\"")},
		{"join_gate_window": json.RawMessage("4")},
		{"join_gate_window": json.RawMessage("51")},
		{"join_gate_confidence": json.RawMessage("-0.1")},
		{"join_gate_confidence": json.RawMessage("1.1")},
		{"join_gate_eval_cooldown_seconds": json.RawMessage("9")},
		{"join_gate_eval_cooldown_seconds": json.RawMessage("3601")},
		{"join_gate_min_interval_minutes": json.RawMessage("0")},
		{"join_gate_min_interval_minutes": json.RawMessage("241")},
		{"join_gate_max_per_hour": json.RawMessage("0")},
		{"join_gate_max_per_hour": json.RawMessage("31")},
		{"join_gate_max_per_day": json.RawMessage("0")},
		{"join_gate_max_per_day": json.RawMessage("101")},
		{"join_gate_wait_retry_seconds": json.RawMessage("9")},
		{"join_gate_wait_retry_seconds": json.RawMessage("301")},
		{"join_gate_topic_dormant_minutes": json.RawMessage("4")},
		{"join_gate_topic_dormant_minutes": json.RawMessage("241")},
		{"join_gate_topic_closed_minutes": json.RawMessage("29")},
		{"join_gate_topic_closed_minutes": json.RawMessage("1441")},
		// closed 必须先于 dormant 触发，否则两个档位没有意义。
		{"join_gate_topic_dormant_minutes": json.RawMessage("100"), "join_gate_topic_closed_minutes": json.RawMessage("60")},
	}
	for _, patch := range bad {
		if _, _, err := m.Apply(context.Background(), patch); err == nil {
			t.Fatalf("invalid join gate patch accepted: %v", patch)
		}
	}
}

func TestJoinGateSettingsGroupOverrideResolution(t *testing.T) {
	m, err := settings.New(context.Background(), nil, settings.Defaults())
	if err != nil {
		t.Fatalf("new settings: %v", err)
	}
	ctx := context.Background()
	patch := func(raw string) map[string]json.RawMessage {
		var p map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("bad fixture: %v", err)
		}
		return p
	}

	// 全局开、无覆盖 / 群显式开 → 生效。
	if _, _, err := m.Apply(ctx, patch(`{"join_gate_enabled":true,"group_overrides":[{"group_openid":"G1","join_gate":true}]}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !m.GroupConfig("G1").JoinGateEnabled || !m.GroupConfig("G2").JoinGateEnabled {
		t.Fatalf("global on should reach every group without a false override: %+v",
			m.GroupConfig("G1"))
	}

	// 群 false 是可靠关闭。
	if _, _, err := m.Apply(ctx, patch(`{"group_overrides":[{"group_openid":"G1","join_gate":false}]}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.GroupConfig("G1").JoinGateEnabled {
		t.Fatal("group join_gate=false must win over the global switch")
	}
	if !m.GroupConfig("G2").JoinGateEnabled {
		t.Fatal("an unlisted group must still follow the global switch")
	}

	// 全局关是总开关：群 true 打不开。
	if _, _, err := m.Apply(ctx, patch(`{"join_gate_enabled":false,"group_overrides":[{"group_openid":"G1","join_gate":true}]}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.GroupConfig("G1").JoinGateEnabled {
		t.Fatal("global off is a master kill switch")
	}

	// mode=off 与总开关关闭等效，即使 enabled=true。
	if _, _, err := m.Apply(ctx, patch(`{"join_gate_enabled":true,"join_gate_mode":"off"}`)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if m.GroupConfig("G1").JoinGateEnabled {
		t.Fatal("join_gate_mode=off must resolve to the same disabled state")
	}
}

// 话题寿命兜底只能是单向降级：时间过去可以把 active 压成 dormant / closed，
// 但绝不能把模型自己判定的 closed 复活；closed 永远不可接回。
func TestJoinGateTopicLifecycleDowngradesByAge(t *testing.T) {
	s := joinGateSettings()
	s.JoinGateTopicDormantMinutes = 20
	s.JoinGateTopicClosedMinutes = 180
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	created := map[int64]time.Time{
		11: now.Add(-5 * time.Minute),
		12: now.Add(-30 * time.Minute),
		13: now.Add(-4 * time.Hour),
	}
	topics := []joinGateTopic{
		{ID: "t1", Title: "刚聊的", MessageIDs: []int64{11}, Status: joinGateTopicStatusActive},
		{ID: "t2", Title: "停了一会儿", MessageIDs: []int64{12}, Status: joinGateTopicStatusActive},
		{ID: "t3", Title: "很久没动", MessageIDs: []int64{13}, Status: joinGateTopicStatusActive},
		{ID: "t4", Title: "模型说结束了", MessageIDs: []int64{11}, Status: joinGateTopicStatusClosed},
	}
	got := applyJoinGateTopicLifecycle(topics, created, now, now, s)
	if got[0].Status != joinGateTopicStatusActive || !got[0].Resumable {
		t.Fatalf("a fresh topic must stay active and resumable: %+v", got[0])
	}
	if got[1].Status != joinGateTopicStatusDormant {
		t.Fatalf("a topic quiet for the dormant window must be dormant: %+v", got[1])
	}
	if got[2].Status != joinGateTopicStatusClosed || got[2].Resumable {
		t.Fatalf("a topic quiet for the closed window must be closed and non-resumable: %+v", got[2])
	}
	if got[3].Status != joinGateTopicStatusClosed || got[3].Resumable {
		t.Fatalf("a model-closed topic must stay closed and non-resumable: %+v", got[3])
	}

	// 时钟偏差 / 窗口外的时间戳保持模型判断，不能被当成「很久以前」。
	future := []joinGateTopic{{ID: "t1", Title: "未来", MessageIDs: []int64{11}, Status: joinGateTopicStatusDormant}}
	got = applyJoinGateTopicLifecycle(future, map[int64]time.Time{11: now.Add(5 * time.Minute)}, now, now, s)
	if got[0].Status != joinGateTopicStatusDormant {
		t.Fatalf("a future timestamp must not change the status: %+v", got[0])
	}
}

// 全 closed 的话题缓存不再注入背景：否则模型会一直被告知「群里在聊」已经结束的事。
func TestRenderJoinTopicsSkipsFullyClosedCache(t *testing.T) {
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Minute).Format(time.RFC3339)
	closed := `[{"id":"t1","title":"旧事","status":"closed"},{"id":"t2","title":"另一件旧事","status":"closed"}]`
	if got := renderJoinTopics(closed, fresh, now); got != "" {
		t.Fatalf("an all-closed cache must not be injected, got %q", got)
	}
	mixed := `[{"id":"t1","title":"旧事","status":"closed"},{"id":"t2","title":"进行中","status":"active"}]`
	got := renderJoinTopics(mixed, fresh, now)
	if !strings.Contains(got, "已结束，不要提") || !strings.Contains(got, "进行中（进行中）") {
		t.Fatalf("mixed cache should label both statuses: %q", got)
	}
}

// 话题寿命兜底：按该话题在窗口消息里的最新 CreatedAt 降级；只降不升。
func TestJoinGateTopicLifecycleAgeFallback(t *testing.T) {
	s := joinGateSettings()
	s.JoinGateTopicDormantMinutes = 20
	s.JoinGateTopicClosedMinutes = 180
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)

	fresh := joinGateTopic{ID: "t1", Title: "进行中", MessageIDs: []int64{1}}
	dormant := joinGateTopic{ID: "t2", Title: "暂歇", MessageIDs: []int64{2}}
	closed := joinGateTopic{ID: "t3", Title: "结束", MessageIDs: []int64{3}}
	createdAt := map[int64]time.Time{
		1: now.Add(-2 * time.Minute),
		2: now.Add(-30 * time.Minute),
		3: now.Add(-200 * time.Minute),
	}
	got := applyJoinGateTopicLifecycle([]joinGateTopic{fresh, dormant, closed}, createdAt, now, now, s)
	if got[0].Status != joinGateTopicStatusActive || !got[0].Resumable {
		t.Fatalf("a fresh topic must stay active/resumable: %+v", got[0])
	}
	if got[1].Status != joinGateTopicStatusDormant {
		t.Fatalf("a topic idle past the dormant window must be dormant: %+v", got[1])
	}
	if got[2].Status != joinGateTopicStatusClosed || got[2].Resumable {
		t.Fatalf("a topic idle past the closed window must be closed and non-resumable: %+v", got[2])
	}

	// 只降不升：模型说 dormant / closed 的，时间再新也不能变回 active。
	revived := applyJoinGateTopicLifecycle([]joinGateTopic{
		{ID: "d", Status: joinGateTopicStatusDormant, MessageIDs: []int64{1}},
		{ID: "c", Status: joinGateTopicStatusClosed, MessageIDs: []int64{1}, Resumable: true},
	}, createdAt, now, now, s)
	if revived[0].Status != joinGateTopicStatusDormant {
		t.Fatalf("age must never upgrade dormant to active: %+v", revived[0])
	}
	if revived[1].Status != joinGateTopicStatusClosed || revived[1].Resumable {
		t.Fatalf("closed must stay closed and non-resumable: %+v", revived[1])
	}

	// 没有消息时间（例如全在窗口外）时退回分析时刻，不凭空关闭话题。
	noTimes := applyJoinGateTopicLifecycle([]joinGateTopic{{ID: "x", Status: joinGateTopicStatusActive, MessageIDs: []int64{99}}},
		map[int64]time.Time{}, now.Add(-5*time.Minute), now, s)
	if noTimes[0].Status != joinGateTopicStatusActive {
		t.Fatalf("a topic without window evidence must keep the model status: %+v", noTimes[0])
	}
}

// 上一次分析的话题要作为「上次话题」回灌给闸门模型，带状态且最多 8 条。
func TestRenderJoinGatePreviousTopics(t *testing.T) {
	s := joinGateSettings()
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	analyzedAt := now.Add(-3 * time.Minute).Format(time.RFC3339)
	topics := `[
		{"id":"t1","title":"部署","summary":"在聊容器部署","status":"active","resumable":true,"message_ids":[1]},
		{"id":"t2","title":"周末","summary":"约饭","status":"dormant","resumable":true,"message_ids":[2]},
		{"id":"t3","title":"旧账单","summary":"早就结了","status":"closed","message_ids":[3]}
	]`
	createdAt := map[int64]time.Time{
		1: now.Add(-2 * time.Minute),
		2: now.Add(-15 * time.Minute),
		3: now.Add(-10 * time.Minute),
	}
	got := renderJoinGatePreviousTopics(topics, analyzedAt, createdAt, now, s)
	if !strings.Contains(got, "上次话题") || !strings.Contains(got, "仅用于判断是否旧事重提") {
		t.Fatalf("previous-topic block lost its guard line: %q", got)
	}
	for _, want := range []string{
		"部署 [进行中]", "周末 [暂歇，可自然接回]（可接回）", "旧账单 [已结束，不要提]",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("previous-topic block missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "旧账单 [进行中]") {
		t.Fatalf("a closed topic must never be rendered as active:\n%s", got)
	}

	// 最多 8 条：第 9 条不出现。
	many := make([]string, 0, joinGatePrevTopicsMax+1)
	for i := 0; i < joinGatePrevTopicsMax+1; i++ {
		many = append(many, `{"id":"t`+strconv.Itoa(i)+`","title":"话题`+strconv.Itoa(i)+`","status":"active"}`)
	}
	got = renderJoinGatePreviousTopics("["+strings.Join(many, ",")+"]", analyzedAt, createdAt, now, s)
	if strings.Contains(got, "话题"+strconv.Itoa(joinGatePrevTopicsMax)) {
		t.Fatalf("previous-topic block must cap at %d topics:\n%s", joinGatePrevTopicsMax, got)
	}
	if n := strings.Count(got, "- "); n != joinGatePrevTopicsMax {
		t.Fatalf("topic lines = %d, want %d", n, joinGatePrevTopicsMax)
	}

	// 500 字硬上限：8 个话题、每个都写满 title/summary 一定超预算。
	longTopics := make([]string, 0, joinGatePrevTopicsMax)
	for i := 0; i < joinGatePrevTopicsMax; i++ {
		longTopics = append(longTopics, `{"id":"t`+strconv.Itoa(i)+`","title":"`+strings.Repeat("长", 300)+
			`","summary":"`+strings.Repeat("文", 300)+`","status":"active"}`)
	}
	got = renderJoinGatePreviousTopics("["+strings.Join(longTopics, ",")+"]", analyzedAt, createdAt, now, s)
	if len([]rune(got)) != joinGatePrevTopicsRunes {
		t.Fatalf("previous-topic block must be capped at %d runes, got %d",
			joinGatePrevTopicsRunes, len([]rune(got)))
	}

	if got := renderJoinGatePreviousTopics("not-json", analyzedAt, createdAt, now, s); got != "" {
		t.Fatalf("broken cache must produce no block, got %q", got)
	}
	if got := renderJoinGatePreviousTopics("[]", analyzedAt, createdAt, now, s); got != "" {
		t.Fatalf("empty cache must produce no block, got %q", got)
	}
}

// 回复侧的话题背景：带状态标签；全部 closed 时不注入。
func TestRenderJoinTopicsStatusLabelsAndAllClosed(t *testing.T) {
	now := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute).Format(time.RFC3339)
	topics := `[
		{"id":"t1","title":"部署","summary":"在聊容器部署","status":"active"},
		{"id":"t2","title":"周末","summary":"约饭","status":"dormant","resumable":true},
		{"id":"t3","title":"旧账单","summary":"早就结了","status":"closed"}
	]`
	got := renderJoinTopics(topics, fresh, now)
	if !strings.Contains(got, "部署（进行中）") ||
		!strings.Contains(got, "周末（暂歇，可自然接回）") ||
		!strings.Contains(got, "旧账单（已结束，不要提）") {
		t.Fatalf("lifecycle labels missing from the background block: %q", got)
	}

	allClosed := `[{"id":"t1","title":"旧账单","status":"closed"},{"id":"t2","title":"旧部署","status":"closed"}]`
	if got := renderJoinTopics(allClosed, fresh, now); got != "" {
		t.Fatalf("an all-closed cache must not be injected: %q", got)
	}
}

// status/resumable 的解析：空 = active；非法值 fail-closed；closed 强制不可接回。
func TestParseJoinGateOutputTopicLifecycle(t *testing.T) {
	got, err := parseJoinGateOutput(`{"topics":[{"id":"t1","title":"x"}],"action":"skip","confidence":0.1,"reason":"x"}`, map[int64]bool{})
	if err != nil {
		t.Fatalf("missing status must default to active: %v", err)
	}
	if got.Topics[0].Status != joinGateTopicStatusActive {
		t.Fatalf("status = %q, want active", got.Topics[0].Status)
	}
	got, err = parseJoinGateOutput(`{"topics":[{"id":"t1","status":"closed","resumable":true}],"action":"skip","confidence":0.1,"reason":"x"}`, map[int64]bool{})
	if err != nil {
		t.Fatalf("closed topic rejected: %v", err)
	}
	if got.Topics[0].Resumable {
		t.Fatal("a closed topic must never be resumable")
	}
	if _, err := parseJoinGateOutput(`{"topics":[{"id":"t1","status":"archived"}],"action":"skip","confidence":0.1,"reason":"x"}`, map[int64]bool{}); err == nil {
		t.Fatal("an illegal topic status must fail closed")
	}
}
