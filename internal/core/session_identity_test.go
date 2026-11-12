package core

import (
	"strings"
	"testing"
)

func TestSessionIdentityBlock(t *testing.T) {
	group := sessionIdentityBlock(&Inbound{Scene: "group", GroupOpenID: "GROUP_OPENID"})
	if !strings.Contains(group, "GROUP_OPENID") || !strings.Contains(group, "群聊") {
		t.Fatalf("group block = %q", group)
	}
	c2c := sessionIdentityBlock(&Inbound{Scene: "c2c", UserOpenID: "USER_OPENID"})
	if !strings.Contains(c2c, "USER_OPENID") || !strings.Contains(c2c, "私聊") {
		t.Fatalf("c2c block = %q", c2c)
	}
	if sessionIdentityBlock(&Inbound{Scene: "group"}) != "" {
		t.Fatal("group without an openid must not inject an empty id")
	}
	if sessionIdentityBlock(&Inbound{Scene: "c2c"}) != "" {
		t.Fatal("c2c without a user openid must not inject an empty id")
	}
}
