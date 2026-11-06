package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsLoopbackAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"0.0.0.0:8080", false},
		{":8080", false},
		{"192.0.2.10:8080", false},
		{"panel.internal.example:8080", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopbackAddr(c.addr); got != c.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestAnnounceAdminTokenHidesSecretWhenNotTTY(t *testing.T) {
	const token = "botharness-secret-value"
	var buf bytes.Buffer
	announceAdminToken(&buf, token, false)
	if strings.Contains(buf.String(), token) {
		t.Fatalf("non-TTY output must not contain the token: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "token") || !strings.Contains(buf.String(), "docker exec") {
		t.Fatalf("non-TTY output must explain how to read the token: %q", buf.String())
	}

	buf.Reset()
	announceAdminToken(&buf, token, true)
	if !strings.Contains(buf.String(), token) {
		t.Fatalf("TTY output should show the generated token: %q", buf.String())
	}
}

func TestRandomHexFailsClosed(t *testing.T) {
	got, err := randomHex(16)
	if err != nil {
		t.Fatalf("randomHex: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("len(randomHex(16)) = %d, want 32", len(got))
	}
}
