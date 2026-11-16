package core

import "testing"

func TestMentionsBotName(t *testing.T) {
	cases := []struct {
		content string
		name    string
		want    bool
	}{
		{"kaku，你觉得呢", "Kaku", true},
		{"KAKU 在吗", "Kaku", true},
		{"另一个名字", "Kaku", false},
		{"卡", "卡", false},
		{"卡卡 在吗", "卡卡", true},
	}
	for _, c := range cases {
		if got := mentionsBotName(c.content, c.name); got != c.want {
			t.Fatalf("mentionsBotName(%q, %q) = %v, want %v", c.content, c.name, got, c.want)
		}
	}
}
