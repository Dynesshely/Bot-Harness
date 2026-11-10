package core

import (
	"testing"

	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/tools"
)

// view_image 只在 vision_enabled 打开时下发给模型；关闭时连定义都不出现。
func TestViewImageAdvertisementFollowsVisionSwitch(t *testing.T) {
	reg := tools.New()
	reg.Register(tools.NewViewImage(nil, nil, func() tools.VisionConfig {
		return tools.VisionConfig{Enabled: true}
	}))
	d := &Dispatcher{tools: reg}
	cfg := settings.GroupConfig{ToolsEnabled: true}

	on := settings.Defaults()
	on.VisionEnabled = true
	if names := advertisedNamesFrom(t, d, on, cfg); !hasTool(names, "view_image") {
		t.Fatalf("vision 开启时应下发 view_image，实际 %v", names)
	}

	off := settings.Defaults()
	off.VisionEnabled = false
	if names := advertisedNamesFrom(t, d, off, cfg); hasTool(names, "view_image") {
		t.Fatalf("vision 关闭时不应下发 view_image，实际 %v", names)
	}
}

func advertisedNamesFrom(t *testing.T, d *Dispatcher, s settings.Settings, cfg settings.GroupConfig) []string {
	t.Helper()
	defs := d.agentTools(s, cfg, &Inbound{})
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		out = append(out, def.Function.Name)
	}
	return out
}
