package core

import (
	"context"
	"encoding/json"
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
	return s
}

func TestJoinGateSystemPromptCarriesTheContract(t *testing.T) {
	for _, want := range []string{joinGateMarker, "join", "confidence", "topics", "message_ids", "reason"} {
		if !strings.Contains(joinGateSystemPrompt, want) {
			t.Fatalf("system prompt lost %q", want)
		}
	}
}

func TestParseJoinGateOutputAcceptsSurroundingProse(t *testing.T) {
	raw := "好的，判断如下：\n" + `{"topics":[{"id":"t1","title":"性能问题","summary":"群里在讨论慢查询","message_ids":[12,13]}],"current_topic_id":"t1","join":true,"confidence":0.82,"reason":"有人明确求助"}` + "\n以上。"
	got, err := parseJoinGateOutput(raw, map[int64]bool{12: true, 13: true})
	if err != nil {
		t.Fatalf("valid verdict rejected: %v", err)
	}
	if !got.Join || got.Confidence != 0.82 || got.CurrentTopicID != "t1" || got.Reason != "有人明确求助" {
		t.Fatalf("verdict parsed wrong: %+v", got)
	}
	if len(got.Topics) != 1 || got.Topics[0].ID != "t1" || len(got.Topics[0].MessageIDs) != 2 {
		t.Fatalf("topics parsed wrong: %+v", got.Topics)
	}
}

func TestParseJoinGateOutputClampsTitleSummary(t *testing.T) {
	long := strings.Repeat("长", 100)
	raw := `{"topics":[{"id":"t1","title":"` + long + `","summary":"` + long + `","message_ids":[]}],"join":false,"confidence":0.2,"reason":"闲聊"}`
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
	valid := `{"topics":[{"id":"t1","title":"话题","summary":"摘要","message_ids":[7]}],"join":true,"confidence":0.9,"reason":"求助"}`
	longReason := strings.Repeat("因", joinGateReasonRunes+1)
	tooMany := make([]map[string]any, joinGateMaxTopics+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"id": "t" + string(rune('a'+i)), "title": "t"}
	}
	tooManyJSON, _ := json.Marshal(map[string]any{"topics": tooMany, "join": true, "confidence": 0.9, "reason": "x"})

	cases := []struct {
		name  string
		raw   string
		allow map[int64]bool
	}{
		{"not json", "我觉得可以加入", map[int64]bool{7: true}},
		{"broken json", `{"topics":[}`, map[int64]bool{7: true}},
		{"no topics", `{"topics":[],"join":true,"confidence":0.9,"reason":"x"}`, map[int64]bool{7: true}},
		{"too many topics", string(tooManyJSON), map[int64]bool{}},
		{"unknown message id", valid, map[int64]bool{8: true}},
		{"confidence too high", `{"topics":[{"id":"t1"}],"join":true,"confidence":1.2,"reason":"x"}`, map[int64]bool{}},
		{"confidence negative", `{"topics":[{"id":"t1"}],"join":true,"confidence":-0.1,"reason":"x"}`, map[int64]bool{}},
		{"reason too long", `{"topics":[{"id":"t1","title":"t"}],"join":false,"confidence":0.1,"reason":"` + longReason + `"}`, map[int64]bool{}},
		{"empty topic id", `{"topics":[{"id":"","title":"t"}],"join":false,"confidence":0.1,"reason":"x"}`, map[int64]bool{}},
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
	topics := `[{"id":"t1","title":"部署","summary":"在聊容器部署"},{"id":"t2","title":"周末"}]`
	fresh := now.Add(-5 * time.Minute).Format(time.RFC3339)

	got := renderJoinTopics(topics, fresh, now)
	if !strings.Contains(got, "群里最近的话题（仅背景，不要强行转回）") {
		t.Fatalf("background block lost its guard line: %q", got)
	}
	if !strings.Contains(got, "部署：在聊容器部署") || !strings.Contains(got, "- 周末") {
		t.Fatalf("topics rendered wrong: %q", got)
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
	}
	got, _, err := m.Apply(context.Background(), good)
	if err != nil {
		t.Fatalf("valid join gate settings rejected: %v", err)
	}
	if !got.JoinGateEnabled || got.JoinGateMode != "llm" || got.JoinGateModel != "gate-model" ||
		got.JoinGateWindow != 5 || got.JoinGateConfidence != 0.5 ||
		got.JoinGateEvalCooldownSeconds != 10 || got.JoinGateMinIntervalMinutes != 1 ||
		got.JoinGateMaxPerHour != 1 || got.JoinGateMaxPerDay != 1 {
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
