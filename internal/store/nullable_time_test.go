package store

import (
	"testing"
	"time"
)

// nullableTimeText is the single place both engines' nullable timestamp values
// become the RFC3339 strings the application speaks. These cases run without a
// database, so a formatting drift is caught even in SQLite-only CI.
func TestNullableTimeTextFormatting(t *testing.T) {
	cases := []struct {
		name string
		src  any
		want string
	}{
		{name: "SQLite unset", src: "", want: ""},
		{name: "SQLite value passes through", src: "2026-11-01T17:14:40+08:00", want: "2026-11-01T17:14:40+08:00"},
		{name: "PostgreSQL NULL", src: nil, want: ""},
		{
			name: "PostgreSQL instant is rendered in +08:00",
			src:  time.Date(2026, 11, 1, 17, 14, 40, 0, time.UTC),
			want: "2026-11-02T01:14:40+08:00",
		},
		{
			name: "PostgreSQL instant keeps the wall clock of +08:00 input",
			src:  time.Date(2026, 11, 1, 19, 30, 0, 0, time.FixedZone("CST", 8*3600)),
			want: "2026-11-01T19:30:00+08:00",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got nullableTimeText
			if err := got.Scan(tc.src); err != nil {
				t.Fatalf("scan %T: %v", tc.src, err)
			}
			if got.Value != tc.want {
				t.Fatalf("Value = %q, want %q", got.Value, tc.want)
			}
		})
	}
}

// A byte slice arrives for SQLite TEXT through some drivers; it must not be
// mistaken for an unset value.
func TestNullableTimeTextAcceptsBytes(t *testing.T) {
	var got nullableTimeText
	if err := got.Scan([]byte("2026-11-01T10:00:00+08:00")); err != nil {
		t.Fatalf("scan bytes: %v", err)
	}
	if got.Value != "2026-11-01T10:00:00+08:00" {
		t.Fatalf("Value = %q", got.Value)
	}
}

// redactDSN is what keeps a PostgreSQL password out of the startup log.
func TestRedactDSNHidesThePassword(t *testing.T) {
	got := redactDSN("postgres://bot:s3cret-primary@db.internal:5432/bot?sslmode=disable")
	if want := "s3cret-primary"; containsSubstring(got, want) {
		t.Fatalf("redactDSN leaked the password: %q", got)
	}
	if !containsSubstring(got, "redacted") {
		t.Fatalf("redactDSN did not mark the password as redacted: %q", got)
	}
	if !containsSubstring(got, "db.internal") || !containsSubstring(got, "/bot") {
		t.Fatalf("redactDSN dropped the useful target: %q", got)
	}
	if unparsable := redactDSN("not a dsn"); unparsable != "postgres://(redacted)" {
		t.Fatalf("redactDSN(unparsable) = %q", unparsable)
	}
}

func containsSubstring(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
