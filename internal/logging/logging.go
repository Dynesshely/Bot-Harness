// Package logging wires slog to stdout, an in-memory ring buffer served by the
// admin API, and the live event bus.
package logging

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/dynesshely/bot-harness/internal/bus"
)

// Entry is one log record as exposed by GET /api/logs and the SSE stream.
type Entry struct {
	ID        int64          `json:"id"`
	TS        string         `json:"ts"`
	Level     string         `json:"level"`
	Component string         `json:"component"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// Ring is a bounded, concurrency-safe buffer of the most recent log entries.
type Ring struct {
	mu   sync.RWMutex
	buf  []Entry
	size int
	next int64
}

// NewRing creates a ring buffer holding at most size entries.
func NewRing(size int) *Ring {
	if size <= 0 {
		size = 2000
	}
	return &Ring{buf: make([]Entry, 0, size), size: size}
}

func (r *Ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	e.ID = r.next
	if len(r.buf) >= r.size {
		copy(r.buf, r.buf[1:])
		r.buf[len(r.buf)-1] = e
		return
	}
	r.buf = append(r.buf, e)
}

// List returns up to limit entries with the given minimum level, newest first,
// optionally filtered by a case-insensitive substring and an upper id bound.
func (r *Ring) List(minLevel slog.Level, query string, limit int, beforeID int64) []Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	q := strings.ToLower(query)
	out := make([]Entry, 0, limit)
	for i := len(r.buf) - 1; i >= 0 && len(out) < limit; i-- {
		e := r.buf[i]
		if beforeID > 0 && e.ID >= beforeID {
			continue
		}
		if parseLevel(e.Level) < minLevel {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(e.Message), q) && !strings.Contains(strings.ToLower(e.Component), q) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// Handler is an slog.Handler with ring-buffer and bus side effects.
type Handler struct {
	json  slog.Handler
	ring  *Ring
	bus   *bus.Bus
	level *slog.LevelVar
	attrs []slog.Attr
	group string
}

// Options configures the root handler.
type Options struct {
	Writer io.Writer
	Level  slog.Level
	Ring   *Ring
	Bus    *bus.Bus
}

// NewHandler builds a root handler writing JSON lines to w.
func NewHandler(opts Options) (*Handler, *slog.LevelVar) {
	lv := new(slog.LevelVar)
	lv.Set(opts.Level)
	w := opts.Writer
	h := &Handler{
		json: slog.NewJSONHandler(w, &slog.HandlerOptions{
			Level: lv,
			ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
				// Keep timestamps RFC3339 with offset for humans reading docker logs.
				if a.Key == slog.TimeKey {
					if t, ok := a.Value.Any().(time.Time); ok {
						return slog.String(slog.TimeKey, t.Format(time.RFC3339))
					}
				}
				return a
			},
		}),
		ring:  opts.Ring,
		bus:   opts.Bus,
		level: lv,
	}
	return h, lv
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.level.Level() <= l
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, rec slog.Record) error {
	fields := make(map[string]any, rec.NumAttrs()+len(h.attrs))
	component := ""
	for _, a := range h.attrs {
		collect(a, fields, &component)
	}
	rec.Attrs(func(a slog.Attr) bool {
		collect(a, fields, &component)
		return true
	})
	if component == "" {
		component = "app"
	}
	if h.group != "" && len(fields) > 0 {
		fields = map[string]any{h.group: fields}
	}

	entry := Entry{
		TS:        rec.Time.Format(time.RFC3339),
		Level:     levelName(rec.Level),
		Component: component,
		Message:   rec.Message,
		Fields:    fields,
	}
	if len(entry.Fields) == 0 {
		entry.Fields = nil
	}

	if h.ring != nil {
		h.ring.add(entry)
	}
	if h.bus != nil {
		// id 0 means "live stream entry"; the stored copy carries the real id.
		live := entry
		live.ID = 0
		h.bus.Publish("log", live)
	}

	// Callers may either bind the component (logger.With("component", ...)) or
	// pass it per call. Drop the per-call duplicate so stdout never carries the
	// key twice.
	boundComponent := false
	for _, a := range h.attrs {
		if a.Key == "component" {
			boundComponent = true
			break
		}
	}
	if !boundComponent {
		return h.json.Handle(ctx, rec)
	}
	inner := slog.NewRecord(rec.Time, rec.Level, rec.Message, rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key != "component" {
			inner.AddAttrs(a)
		}
		return true
	})
	return h.json.Handle(ctx, inner)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	clone := *h
	clone.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	clone.json = h.json.WithAttrs(attrs)
	return &clone
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	clone := *h
	clone.group = name
	clone.json = h.json.WithGroup(name)
	return &clone
}

func collect(a slog.Attr, fields map[string]any, component *string) {
	if a.Key == "" {
		return
	}
	v := a.Value.Resolve()
	if a.Key == "component" {
		*component = v.String()
		return
	}
	fields[a.Key] = value(v)
}

func value(v slog.Value) any {
	if v.Kind() == slog.KindGroup {
		m := make(map[string]any)
		for _, ga := range v.Group() {
			m[ga.Key] = value(ga.Value.Resolve())
		}
		return m
	}
	return v.Any()
}

// ParseLevel converts a textual level into slog.Level.
func ParseLevel(s string) slog.Level {
	return parseLevel(s)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func levelName(l slog.Level) string {
	switch {
	case l < slog.LevelInfo:
		return "debug"
	case l < slog.LevelWarn:
		return "info"
	case l < slog.LevelError:
		return "warn"
	default:
		return "error"
	}
}

// Discard is a no-op logger used in tests.
var Discard = slog.New(slog.NewTextHandler(io.Discard, nil))
