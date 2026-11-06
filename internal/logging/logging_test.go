package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func newTestLogger(t *testing.T, ring *Ring, buf *bytes.Buffer) *slog.Logger {
	t.Helper()
	h, _ := NewHandler(Options{Writer: buf, Level: slog.LevelDebug, Ring: ring})
	return slog.New(h)
}

func TestHandlerAvoidsDuplicateComponentKey(t *testing.T) {
	var buf bytes.Buffer
	ring := NewRing(10)
	logger := newTestLogger(t, ring, &buf)

	// Bind the component, then pass it again per call — the common mistake this
	// handler has to absorb.
	bound := logger.With("component", "ws")
	bound.Info("gateway ready", "component", "ws", "session_id", "abc")

	line := strings.TrimSpace(buf.String())
	if strings.Count(line, `"component"`) != 1 {
		t.Fatalf("component must appear exactly once, got: %s", line)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("log line is not valid JSON: %v", err)
	}
	if parsed["component"] != "ws" {
		t.Fatalf("component = %v", parsed["component"])
	}
	if parsed["session_id"] != "abc" {
		t.Fatalf("session_id = %v", parsed["session_id"])
	}
}

func TestHandlerKeepsPerCallComponentWhenUnbound(t *testing.T) {
	var buf bytes.Buffer
	logger := newTestLogger(t, NewRing(10), &buf)

	logger.Info("hello", "component", "dispatch")

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if parsed["component"] != "dispatch" {
		t.Fatalf("component = %v", parsed["component"])
	}
}

func TestRingBuffersNewestFirstAndFilters(t *testing.T) {
	ring := NewRing(3)
	var buf bytes.Buffer
	logger := newTestLogger(t, ring, &buf)

	logger.Debug("debug one", "component", "a")
	logger.Info("info two", "component", "a")
	logger.Warn("warn three", "component", "b")
	logger.Error("error four", "component", "b")

	all := ring.List(slog.LevelDebug, "", 10, 0)
	if len(all) != 3 {
		t.Fatalf("ring keeps 3 entries, got %d", len(all))
	}
	if all[0].Message != "error four" {
		t.Fatalf("newest entry should come first, got %q", all[0].Message)
	}
	if all[0].ID <= all[1].ID {
		t.Fatalf("ids should increase, got %d then %d", all[0].ID, all[1].ID)
	}

	warnUp := ring.List(slog.LevelWarn, "", 10, 0)
	if len(warnUp) != 2 {
		t.Fatalf("warn+ filter returned %d entries", len(warnUp))
	}
	for _, e := range warnUp {
		if e.Level == "info" || e.Level == "debug" {
			t.Fatalf("level filter leaked %q", e.Level)
		}
	}

	if got := ring.List(slog.LevelDebug, "warn", 10, 0); len(got) != 1 || got[0].Message != "warn three" {
		t.Fatalf("substring filter failed: %#v", got)
	}

	// before_id pagination walks backwards: "older than warn three" is info two.
	older := ring.List(slog.LevelDebug, "", 10, all[1].ID)
	if len(older) != 1 || older[0].Message != "info two" {
		t.Fatalf("before_id filter failed: %#v", older)
	}
	// Asking for entries older than the oldest returns nothing.
	if got := ring.List(slog.LevelDebug, "", 10, all[len(all)-1].ID); len(got) != 0 {
		t.Fatalf("expected no entries older than the oldest, got %#v", got)
	}
}

func TestRingEvictsOldestWhenFull(t *testing.T) {
	ring := NewRing(2)
	var buf bytes.Buffer
	logger := newTestLogger(t, ring, &buf)

	logger.Info("one")
	logger.Info("two")
	logger.Info("three")

	got := ring.List(slog.LevelDebug, "", 10, 0)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	for _, e := range got {
		if e.Message == "one" {
			t.Fatal("the oldest entry should have been evicted")
		}
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":         slog.LevelInfo,
		"debug":    slog.LevelDebug,
		"INFO":     slog.LevelInfo,
		"warn":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"nonsense": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
