package media

import (
	"strconv"
	"strings"
)

// Driver names accepted by NewStoreWithDriver and Store.SetDialect.
//
// They mirror internal/store's DriverSQLite / DriverPostgres, so a caller that
// already knows the backend can hand the same string to both packages.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// dialect is this package's copy of store.dialect: the only difference between
// the two backends that matters here is how a bind parameter is spelled — "?"
// on SQLite, "$n" on PostgreSQL.
//
// It is a copy instead of a reuse because store.dialect is unexported; the
// rewriting rules (skip '...', "...", -- comments; number placeholders in order
// of appearance) are kept identical to internal/store/dialect.go, and
// TestDialectMatchesStoreRebind pins that equivalence against the real store
// implementation.
type dialect struct {
	name string
}

// newDialect maps a driver name onto its dialect. Unknown or empty names
// resolve to SQLite, matching store.NewDialect: an unwired caller keeps the
// historical "?" behaviour instead of emitting "$1" into a SQLite handle.
func newDialect(driver string) dialect {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case DriverPostgres, "pg", "postgresql", "pgx", "pgx/v5":
		return dialect{name: DriverPostgres}
	default:
		return dialect{name: DriverSQLite}
	}
}

// isPostgres reports whether this dialect targets PostgreSQL.
func (d dialect) isPostgres() bool { return d.name == DriverPostgres }

// placeholder returns the bind marker for the n-th (1-based) argument.
func (d dialect) placeholder(n int) string {
	if n < 1 {
		n = 1
	}
	if d.isPostgres() {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// rebind rewrites the positional "?" placeholders of query into the markers
// this dialect expects, numbering them in order of appearance.
//
// SQLite gets the query back untouched (no copy is made). For PostgreSQL the
// scan skips over string literals ('...', where a doubled quote is the escape),
// over "..." identifiers and over SQL line comments (-- ...), so a question mark
// inside any of them is never mistaken for a bind marker. This package never
// emits dollar-quoted literals ($$...$$), so those are not special-cased —
// exactly the contract of store.dialect.Rebind.
func (d dialect) rebind(query string) string {
	if !d.isPostgres() || !strings.ContainsRune(query, '?') {
		return query
	}

	var b strings.Builder
	b.Grow(len(query) + 8)

	n := 0
	inSingle := false
	inDouble := false
	inLineComment := false

	for i := 0; i < len(query); i++ {
		c := query[i]

		switch {
		case inLineComment:
			b.WriteByte(c)
			if c == '\n' {
				inLineComment = false
			}
		case inSingle:
			b.WriteByte(c)
			if c == '\'' {
				// '' is an escaped quote inside a literal, not its end.
				if i+1 < len(query) && query[i+1] == '\'' {
					b.WriteByte(query[i+1])
					i++
					continue
				}
				inSingle = false
			}
		case inDouble:
			b.WriteByte(c)
			if c == '"' {
				if i+1 < len(query) && query[i+1] == '"' {
					b.WriteByte(query[i+1])
					i++
					continue
				}
				inDouble = false
			}
		case c == '\'':
			inSingle = true
			b.WriteByte(c)
		case c == '"':
			inDouble = true
			b.WriteByte(c)
		case c == '-' && i+1 < len(query) && query[i+1] == '-':
			inLineComment = true
			b.WriteString("--")
			i++
		case c == '?':
			n++
			b.WriteString(d.placeholder(n))
		default:
			b.WriteByte(c)
		}
	}

	return b.String()
}
