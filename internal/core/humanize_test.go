package core

import (
	"strings"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/settings"
)

func TestHumanizeLevels(t *testing.T) {
	if humanizeDelay(HumanizeOff, 500, 1, 0) != 0 {
		t.Fatal("off 应该零延迟")
	}
	soft := humanizeDelay(HumanizeSoft, 100, 1, 0)
	medium := humanizeDelay(HumanizeMedium, 100, 1, 0)
	strong := humanizeDelay(HumanizeStrong, 100, 1, 0)
	if !(soft < medium && medium < strong) {
		t.Fatalf("档位应递增：soft=%v medium=%v strong=%v", soft, medium, strong)
	}
	// 100 字 ≈ 0.8s + 10*0.6s = 6.8s，未超过 8s 上限
	if medium < 6*time.Second || medium > 8*time.Second {
		t.Fatalf("medium 100 字应在 6~8s，实际 %v", medium)
	}
}

func TestHumanizeDelayIsCapped(t *testing.T) {
	long := humanizeDelay(HumanizeMedium, 100000, 1, 1)
	if long > 8*time.Second+time.Millisecond {
		t.Fatalf("超过上限：%v", long)
	}
	scaled := humanizeDelay(HumanizeMedium, 100000, 2, 1)
	if scaled > 16*time.Second+time.Millisecond {
		t.Fatalf("缩放后上限应为 16s：%v", scaled)
	}
}

func TestHumanizeDelayJitter(t *testing.T) {
	low := humanizeDelay(HumanizeMedium, 50, 1, -1)
	high := humanizeDelay(HumanizeMedium, 50, 1, 1)
	if low >= high {
		t.Fatalf("抖动应改变结果：low=%v high=%v", low, high)
	}
	mid := humanizeDelay(HumanizeMedium, 50, 1, 0)
	if mid < low || mid > high {
		t.Fatalf("抖动应落在区间内：%v not in [%v,%v]", mid, low, high)
	}
}

func TestHumanizeDelayAccountsForModelTime(t *testing.T) {
	if got := humanizeDelayForTurn(HumanizeMedium, 20, 1, 10*time.Second); got != 0 {
		t.Fatalf("模型已经花掉更久时不该再等：%v", got)
	}
	if got := humanizeDelayForTurn(HumanizeMedium, 20, 1, 0); got <= 0 {
		t.Fatalf("全新回合应当有停顿：%v", got)
	}
}

func TestSilenceOnlyAppliesWhenNotAddressed(t *testing.T) {
	if silenceProbability(HumanizeOff) != 0 || silenceProbability(HumanizeSoft) != 0 {
		t.Fatal("off/soft 不该有沉默概率")
	}
	if silenceProbability(HumanizeMedium) <= 0 {
		t.Fatal("medium 应有沉默概率")
	}
	if silenceProbability(HumanizeStrong) <= silenceProbability(HumanizeMedium) {
		t.Fatal("strong 应比 medium 更爱沉默")
	}
}

func TestMoodLine(t *testing.T) {
	late := time.Date(2026, 10, 30, 3, 0, 0, 0, time.Local)
	if !strings.Contains(moodLine(late, false), "深夜") {
		t.Fatal("凌晨应识别为深夜")
	}
	morning := time.Date(2026, 10, 30, 8, 0, 0, 0, time.Local)
	if !strings.Contains(moodLine(morning, false), "早上") {
		t.Fatal("上午应识别为早上")
	}
	noon := time.Date(2026, 10, 30, 14, 0, 0, 0, time.Local)
	if moodLine(noon, false) != "" {
		t.Fatal("普通时段不该有人设提示")
	}
	if !strings.Contains(moodLine(noon, true), "热闹") {
		t.Fatal("繁忙时应提示群很热闹")
	}
}

func TestHumanizePromptGuards(t *testing.T) {
	if humanizePrompt(HumanizeOff, "随便", time.Now(), true) != "" {
		t.Fatal("off 不应注入任何内容")
	}
	p := humanizePrompt(HumanizeMedium, "说话带点幽默", time.Now(), false)
	for _, want := range []string{"不要像客服", "表格", "作为 AI", "人设：说话带点幽默"} {
		if !strings.Contains(p, want) {
			t.Fatalf("提示词缺少 %q：%s", want, p)
		}
	}
}

func TestHumanizeLevelOverride(t *testing.T) {
	settings := settingsWithHumanize("medium")
	override := "off"
	if got := humanizeLevel(settings, settingsGroupConfig(&override)); got != "off" {
		t.Fatalf("按群覆盖应生效，实际 %q", got)
	}
	none := humanizeLevel(settings, settingsGroupConfig(nil))
	if none != "medium" {
		t.Fatalf("无覆盖时应跟随全局，实际 %q", none)
	}
}

func settingsWithHumanize(level string) settings.Settings {
	return settings.Settings{HumanizeLevel: level, HumanizeDelayScale: 1}
}

func settingsGroupConfig(level *string) settings.GroupConfig {
	cfg := settings.GroupConfig{}
	if level != nil {
		cfg.HumanizeLevel = *level
	}
	return cfg
}
