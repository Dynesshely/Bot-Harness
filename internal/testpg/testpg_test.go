package testpg

import (
	"fmt"
	"strings"
	"testing"
)

// fakeT records Fatalf calls so the rejection path can be asserted without a
// deliberately failing test.
type fakeT struct {
	fatals []string
}

func (f *fakeT) Fatalf(format string, args ...any) {
	f.fatals = append(f.fatals, fmt.Sprintf(format, args...))
}

func TestGuardRejectsProductionDatabase(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
	}{
		{"url", "postgres://u:p@h:5432/bot"},
		{"url with query", "postgres://u:p@h:5432/bot?sslmode=disable"},
		{"uppercase database", "postgres://u:p@h:5432/BOT"},
		{"postgresql scheme", "postgresql://u:p@h:5432/bot"},
		{"key=value", "host=h port=5432 user=u dbname=bot sslmode=disable"},
		{"key=value quoted", "dbname='bot' host=h"},
		{"key=value uppercase", "host=h DBNAME=BOT"},
		{"key=value spaces around equals", "host=h dbname = bot sslmode=disable"},
		{"key=value spaces and quotes", "host = h dbname = 'bot'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeT{}
			guard(f, c.dsn)
			if len(f.fatals) != 1 {
				t.Fatalf("guard(%q) fatals = %v, want exactly one rejection", c.dsn, f.fatals)
			}
			msg := f.fatals[0]
			if !strings.Contains(msg, "bot_test") || !strings.Contains(msg, "scratch") {
				t.Fatalf("rejection message does not point at bot_test/scratch: %q", msg)
			}
		})
	}
}

func TestGuardAllowsTestDatabases(t *testing.T) {
	cases := []string{
		"postgres://u:p@h:5432/bot_test",
		"postgres://u:p@h:5432/bot_pgsmoke",
		"postgres://u:p@h:5432/x_test",
		"postgresql://u:p@h:5432/bot_test?sslmode=disable",
		"host=h port=5432 user=u dbname=bot_test sslmode=disable",
		"host=h dbname='bot_pgsmoke'",
	}
	for _, dsn := range cases {
		t.Run(dsn, func(t *testing.T) {
			f := &fakeT{}
			guard(f, dsn)
			if len(f.fatals) != 0 {
				t.Fatalf("guard(%q) rejected a test database: %v", dsn, f.fatals)
			}
		})
	}
}

func TestGuardIgnoresNonPostgresDSNs(t *testing.T) {
	cases := []string{
		"",
		"/tmp/bot.db",
		"file:bot.db",
		"sqlite:///tmp/bot.db",
		"mysql://u:p@h:3306/bot",
		"file:foo=bar.db",
	}
	for _, dsn := range cases {
		t.Run(dsn, func(t *testing.T) {
			f := &fakeT{}
			guard(f, dsn)
			if len(f.fatals) != 0 {
				t.Fatalf("guard(%q) rejected a non-PostgreSQL DSN: %v", dsn, f.fatals)
			}
		})
	}
}

func TestGuardDoesNotLeakPassword(t *testing.T) {
	f := &fakeT{}
	guard(f, "postgres://u:supersecret@h:5432/bot")
	if len(f.fatals) != 1 {
		t.Fatalf("fatals = %v, want one rejection", f.fatals)
	}
	if strings.Contains(f.fatals[0], "supersecret") {
		t.Fatalf("rejection message leaks the password: %q", f.fatals[0])
	}
}

func TestGuardProductionNameOverride(t *testing.T) {
	t.Setenv("BOT_HARNESS_PROD_DB", "live_db")
	f := &fakeT{}
	guard(f, "postgres://u:p@h:5432/live_db")
	if len(f.fatals) != 1 {
		t.Fatalf("BOT_HARNESS_PROD_DB=live_db did not reject live_db: %v", f.fatals)
	}
	// The override replaces the default, so "bot" is no longer special.
	f = &fakeT{}
	guard(f, "postgres://u:p@h:5432/bot")
	if len(f.fatals) != 0 {
		t.Fatalf("override still rejected the default name: %v", f.fatals)
	}
}

// TestGuardPublicAPI calls the exported Guard on a safe DSN: a real *testing.T
// must accept it (and is exercised by every other PostgreSQL test).
func TestGuardPublicAPI(t *testing.T) {
	Guard(t, "postgres://u:p@h:5432/bot_test")
}
