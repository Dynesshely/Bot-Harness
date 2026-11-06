// Package tools holds the function-calling registry: what the model may call,
// how risky each call is, and what it gets back.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/ai"
)

// Risk decides whether a tool may be exposed automatically.
//
// ReadOnly tools only observe (time, math, fetch). SideEffect tools change state
// the operator cares about (memory, schedules). Dangerous tools can run
// arbitrary code and stay off unless the deployment opts in.
type Risk int

const (
	ReadOnly Risk = iota
	SideEffect
	Dangerous
)

func (r Risk) String() string {
	switch r {
	case ReadOnly:
		return "read-only"
	case SideEffect:
		return "side-effect"
	default:
		return "dangerous"
	}
}

// Env is the per-call context a tool may use. It deliberately carries no
// credentials: tools run with whatever the harness gives them, nothing more.
type Env struct {
	ConversationID string
	GroupOpenID    string
	MemberOpenID   string
	MemberName     string
	IsGroup        bool
	// IsAdmin is true when the caller is one of admin_openids.
	IsAdmin bool
	// AdminCount is how many admin_openids are configured. It is deliberately
	// separate from IsAdmin: an admin-only tool must be able to reject the
	// "nobody is an administrator" configuration without trusting the identity
	// computation to keep returning false for an empty list (T28).
	AdminCount int
	// Now lets tests pin the clock.
	Now time.Time
	Log *slog.Logger
}

// Clock returns the env clock, falling back to the wall clock.
func (e *Env) Clock() time.Time {
	if e != nil && !e.Now.IsZero() {
		return e.Now
	}
	return time.Now()
}

// Result is what a tool hands back to the model.
type Result struct {
	Text      string
	Truncated bool
}

// Tool is one callable function.
type Tool interface {
	Name() string
	Description() string
	// Parameters returns a JSON Schema object describing the arguments.
	Parameters() json.RawMessage
	Risk() Risk
	// Group names the capability this tool belongs to ("util" / "memory" /
	// "web" / "exec"), so settings can switch a whole family on or off.
	Group() string
	Run(ctx context.Context, args json.RawMessage, env *Env) (Result, error)
}

// Registry holds the tools available to the agent loop.
type Registry struct {
	tools map[string]Tool
}

// New builds an empty registry.
func New() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds a tool; a later registration with the same name wins, which
// keeps tests able to stub built-ins.
func (r *Registry) Register(t Tool) {
	if t == nil || strings.TrimSpace(t.Name()) == "" {
		return
	}
	r.tools[t.Name()] = t
}

// Lookup finds a tool by name.
func (r *Registry) Lookup(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// Names lists the registered tool names, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for name := range r.tools {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Definitions renders the schemas to advertise to the model, filtered by allow
// (nil means "everything").
func (r *Registry) Definitions(allow func(Tool) bool) []ai.ToolDef {
	names := r.Names()
	out := make([]ai.ToolDef, 0, len(names))
	for _, name := range names {
		t := r.tools[name]
		if allow != nil && !allow(t) {
			continue
		}
		out = append(out, ai.NewToolDef(t.Name(), t.Description(), t.Parameters()))
	}
	return out
}

// Call runs one tool by name and caps the result size so a chatty tool cannot
// blow up the context window.
func (r *Registry) Call(ctx context.Context, name string, args json.RawMessage, env *Env, maxChars int) (Result, error) {
	t, ok := r.Lookup(name)
	if !ok {
		return Result{}, fmt.Errorf("unknown tool %q", name)
	}
	if maxChars <= 0 {
		maxChars = DefaultResultChars
	}
	res, err := t.Run(ctx, args, env)
	if err != nil {
		return Result{}, err
	}
	if runes := []rune(res.Text); len(runes) > maxChars {
		res.Text = string(runes[:maxChars]) + "\n…（结果过长已截断）"
		res.Truncated = true
	}
	return res, nil
}

// DefaultResultChars caps a single tool result (~2k tokens of CJK).
const DefaultResultChars = 8000
