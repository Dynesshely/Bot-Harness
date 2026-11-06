// Package testpg protects PostgreSQL-backed tests from pointing at the
// production database.
//
// It exists because of a real failure mode: running "go test" with PG_TEST_DSN
// aimed at the live bot database lets the admin configuration tests write test
// values into settings (qq_app_id, model, openai_base_url, the API key and
// admin token), which makes the live bot fail token authentication and health
// checks. Keeping test DSNs away from the production database is cheaper than
// recovering from that. See docs/DECISIONS.md D-046.
//
// This package is a test-only helper. Production code must never import it;
// it is imported from _test.go files and from test helpers.
package testpg

import (
	"net/url"
	"os"
	"strings"
	"testing"
	"unicode"
)

// DefaultProdDatabase is the production database name guarded by default.
// BOT_HARNESS_PROD_DB overrides it (comma-separated names are accepted).
const DefaultProdDatabase = "bot"

// Guard fails the test when dsn names a production database.
//
// dsn may be a URL DSN (postgres://user:pass@host:5432/bot) or a libpq
// key=value DSN (host=... dbname=bot ...). Other schemes and SQLite paths are
// ignored: this guard is only about PostgreSQL. The comparison is
// case-insensitive, and production names come from BOT_HARNESS_PROD_DB when it
// is set, otherwise {"bot"}.
//
// It must be called before opening the connection, so a misconfigured DSN
// never reaches the server. The message only names the database; it never
// prints the DSN, because that may contain a password.
func Guard(t *testing.T, dsn string) {
	t.Helper()
	guard(t, dsn)
}

// fataler is the part of testing.T Guard uses, so the rejection path can be
// unit-tested without a test that is expected to fail.
type fataler interface {
	Fatalf(format string, args ...any)
}

func guard(t fataler, dsn string) {
	name, ok := databaseName(dsn)
	if !ok || name == "" {
		return
	}
	for _, prod := range productionDatabases() {
		if strings.EqualFold(name, prod) {
			t.Fatalf("refusing to run a test against production database %q: "+
				"point PG_TEST_DSN/BOT_HARNESS_TEST_PG_DSN at bot_test or a throwaway scratch database, "+
				"never at the live database (tests write settings; see D-046)", name)
			return
		}
	}
}

// productionDatabases returns the database names Guard rejects.
func productionDatabases() []string {
	raw := strings.TrimSpace(os.Getenv("BOT_HARNESS_PROD_DB"))
	if raw == "" {
		return []string{DefaultProdDatabase}
	}
	var names []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			names = append(names, part)
		}
	}
	if len(names) == 0 {
		return []string{DefaultProdDatabase}
	}
	return names
}

// databaseName extracts the database name from a PostgreSQL DSN.
// ok is false when dsn is not a PostgreSQL DSN at all.
func databaseName(dsn string) (string, bool) {
	dsn = strings.TrimSpace(dsn)
	if dsn == "" {
		return "", false
	}

	if rest, ok := cutPostgresScheme(dsn); ok {
		return databaseNameFromURL(rest)
	}
	if strings.Contains(dsn, "://") {
		// A URL, but not PostgreSQL (mysql://, sqlite://, ...).
		return "", false
	}

	kv, ok := parseKeyValue(dsn)
	if !ok || !looksLikePostgresKV(kv) {
		return "", false
	}
	return kv["dbname"], true
}

// cutPostgresScheme reports whether dsn starts with postgres:// or
// postgresql:// (case-insensitive) and returns the remainder after the scheme.
func cutPostgresScheme(dsn string) (string, bool) {
	for _, scheme := range []string{"postgresql://", "postgres://"} {
		if len(dsn) >= len(scheme) && strings.EqualFold(dsn[:len(scheme)], scheme) {
			return dsn[len(scheme):], true
		}
	}
	return "", false
}

// databaseNameFromURL extracts the database from the part after the scheme.
// It re-adds the scheme so net/url can parse userinfo and query correctly.
func databaseNameFromURL(rest string) (string, bool) {
	u, err := url.Parse("postgres://" + rest)
	if err != nil {
		return "", false
	}
	if db := strings.TrimPrefix(u.Path, "/"); db != "" {
		return db, true
	}
	// libpq accepts a dbname query parameter as well.
	if db := u.Query().Get("dbname"); db != "" {
		return db, true
	}
	return "", true
}

// looksLikePostgresKV reports whether the key=value map is a PostgreSQL
// connection string rather than an unrelated SQLite path that happens to
// contain an "=".
func looksLikePostgresKV(kv map[string]string) bool {
	for _, key := range []string{
		"dbname", "host", "hostaddr", "port", "user", "password",
		"sslmode", "sslcert", "sslkey", "sslrootcert", "connect_timeout",
		"application_name", "options", "service",
	} {
		if _, ok := kv[key]; ok {
			return true
		}
	}
	return false
}

// parseKeyValue parses a libpq key=value connection string: whitespace
// separates pairs, values may be single-quoted, and backslash escapes the next
// character. Keys are lower-cased.
func parseKeyValue(dsn string) (map[string]string, bool) {
	kv := make(map[string]string)
	i := 0
	for {
		for i < len(dsn) && unicode.IsSpace(rune(dsn[i])) {
			i++
		}
		if i >= len(dsn) {
			break
		}
		start := i
		for i < len(dsn) && dsn[i] != '=' && !unicode.IsSpace(rune(dsn[i])) {
			i++
		}
		key := strings.ToLower(dsn[start:i])
		for i < len(dsn) && unicode.IsSpace(rune(dsn[i])) {
			i++
		}
		if i >= len(dsn) || dsn[i] != '=' || i == start {
			return nil, false
		}
		i++ // '='
		for i < len(dsn) && unicode.IsSpace(rune(dsn[i])) {
			i++
		}

		var value strings.Builder
		if i < len(dsn) && dsn[i] == '\'' {
			i++
			closed := false
			for i < len(dsn) {
				c := dsn[i]
				if c == '\\' && i+1 < len(dsn) {
					value.WriteByte(dsn[i+1])
					i += 2
					continue
				}
				if c == '\'' {
					i++
					closed = true
					break
				}
				value.WriteByte(c)
				i++
			}
			if !closed {
				return nil, false
			}
		} else {
			for i < len(dsn) && !unicode.IsSpace(rune(dsn[i])) {
				c := dsn[i]
				if c == '\\' && i+1 < len(dsn) {
					value.WriteByte(dsn[i+1])
					i += 2
					continue
				}
				value.WriteByte(c)
				i++
			}
		}
		kv[key] = value.String()
	}
	return kv, len(kv) > 0
}
