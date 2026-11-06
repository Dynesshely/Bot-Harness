package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// Driver names accepted by NewDialect. They match the database/sql driver
// names the harness registers, so callers can hand over whatever the
// configuration selected.
const (
	DriverSQLite   = "sqlite"
	DriverPostgres = "postgres"
)

// dialect carries the two SQL differences that are not worth a third-party
// compatibility layer: how a bind parameter is spelled, and whether a query
// needs its placeholders rewritten.
//
// Everything else the store relies on (ON CONFLICT DO NOTHING, UPSERT with
// DO UPDATE, INSERT ... RETURNING id, TEXT/INTEGER/REAL columns) is common to
// SQLite (3.24+, 3.35+ for RETURNING) and PostgreSQL.
type dialect struct {
	name string
}

var (
	dialectSQLite   = dialect{name: DriverSQLite}
	dialectPostgres = dialect{name: DriverPostgres}
)

// NewDialect maps a driver name onto its dialect.
//
// Unknown or empty names resolve to SQLite: Open historically only spoke
// SQLite, and the zero value of Store.dialect is the empty name, so an
// unwired caller keeps the original "?" behaviour instead of silently
// emitting "$1" into a SQLite handle.
func NewDialect(driver string) dialect {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case DriverPostgres, "pg", "postgresql", "pgx", "pgx/v5":
		return dialectPostgres
	default:
		return dialectSQLite
	}
}

// SetDialect installs d on the handle. It must be called right after sql.Open
// and before migrate, so every statement that follows is rewritten
// consistently. Open itself is left alone here: the driver/DSN selection is
// owned by the DDL side, which calls SetDialect(NewDialect(driver)).
func (s *Store) SetDialect(d dialect) { s.dialect = d }

// IsPostgres reports whether this dialect targets PostgreSQL.
func (d dialect) IsPostgres() bool { return d.name == DriverPostgres }

// Name returns the canonical driver name of the dialect.
func (d dialect) Name() string {
	if d.name == "" {
		return DriverSQLite
	}
	return d.name
}

// Placeholder returns the bind marker for the n-th (1-based) argument:
// "?" for SQLite, "$n" for PostgreSQL.
func (d dialect) Placeholder(n int) string {
	if n < 1 {
		n = 1
	}
	if d.IsPostgres() {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// Rebind rewrites the positional "?" placeholders of query into the markers
// this dialect expects, numbering them in order of appearance.
//
// SQLite and the zero dialect get the query back untouched (no copy is made).
// For PostgreSQL the scan skips over string literals ('...', where a doubled
// quote is the escape), over "..." identifiers and over SQL line comments
// (-- ...), so a question mark inside any of them is never mistaken for a bind
// marker. This package never emits dollar-quoted literals ($$...$$), so those
// are not special-cased.
func (d dialect) Rebind(query string) string {
	if !d.IsPostgres() || !strings.ContainsRune(query, '?') {
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
			b.WriteString(d.Placeholder(n))
		default:
			b.WriteByte(c)
		}
	}

	return b.String()
}

// --- statement wrappers -----------------------------------------------------
//
// Every SQL call site in this package goes through these, so the dialect is
// applied in one place instead of in ~90 hand-edited queries.

func (s *Store) execContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, s.dialect.Rebind(query), args...)
}

func (s *Store) queryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, s.dialect.Rebind(query), args...)
}

func (s *Store) queryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, s.dialect.Rebind(query), args...)
}

// Transaction variants: *sql.Tx has no pointer back to the Store, so the
// dialect-bearing handle has to be passed along.

func (s *Store) txExecContext(ctx context.Context, tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, s.dialect.Rebind(query), args...)
}

func (s *Store) txQueryContext(ctx context.Context, tx *sql.Tx, query string, args ...any) (*sql.Rows, error) {
	return tx.QueryContext(ctx, s.dialect.Rebind(query), args...)
}

func (s *Store) txQueryRowContext(ctx context.Context, tx *sql.Tx, query string, args ...any) *sql.Row {
	return tx.QueryRowContext(ctx, s.dialect.Rebind(query), args...)
}

// --- schema introspection ---------------------------------------------------
//
// tableColumns is the one place the store has to ask the engine about its own
// DDL, and the two engines answer in completely different ways: SQLite has a
// PRAGMA, PostgreSQL has information_schema. Both statements below are written
// with a single "?" bind, so they run unchanged through queryRowContext /
// queryContext and the Rebind layer turns it into $1 on PostgreSQL.
//
// SQLite's PRAGMA form is the table-valued pragma_table_info(name), not
// "PRAGMA table_info(name)": the latter is a statement of its own and cannot
// take a bind parameter, which is exactly why the table name used to be
// concatenated into the SQL text. The table-valued form binds normally and has
// been available since SQLite 3.16 (the driver here is far newer).

// --- timestamp columns -----------------------------------------------------
//
// SQLite keeps every timestamp as the RFC3339 TEXT the application writes.
// PostgreSQL keeps them as native timestamptz, and the two representations
// differ in exactly three ways: NULL cannot scan into a Go string, the value
// PostgreSQL hands back is in the connection location (UTC by default), and
// the empty string is not a valid timestamptz literal. The helpers below are
// the only place those differences are spelled out; every call site still uses
// one SQL statement shared by both engines.
//
// Nullable timestamp reads select the raw column and scan it with
// nullableTimeText (store.go): that helper accepts NULL (unset) and both
// engines' native values, and formats the instant in scanLocation on the Go
// side. No to_char / AT TIME ZONE projection is involved, so the SQL stays one
// statement shared by both engines.

// NullableTimeArg renders the bind expression used to write a Go string into a
// nullable timestamp column. The empty string means "unset": SQLite stores it
// verbatim (its column is TEXT NOT NULL DEFAULT ”), PostgreSQL stores NULL.
// NULLIF(?, ”) resolves to text on its own, so the explicit cast is what
// makes the assignment type-correct.
func (d dialect) NullableTimeArg() string {
	if d.IsPostgres() {
		return "NULLIF(?, '')::timestamptz"
	}
	return "?"
}

// TimeIsUnset renders the predicate "this nullable timestamp has no value".
// On PostgreSQL "unset" is NULL; the SQLite column has no NULLs and spells it
// as the empty string.
func (d dialect) TimeIsUnset(column string) string {
	if d.IsPostgres() {
		return column + " IS NULL"
	}
	return column + " = ''"
}

// EmptyTimeLiteral renders the SQL literal for "unset" on the right-hand side
// of an assignment. A bare ” would be parsed as timestamptz on PostgreSQL
// and rejected, so the literal has to be NULL there.
func (d dialect) EmptyTimeLiteral() string {
	if d.IsPostgres() {
		return "NULL"
	}
	return "''"
}

// columnsQuery returns the statement that lists the column names of table.
// Both engines hand back a single TEXT column named "name".
func (d dialect) columnsQuery() string {
	if d.IsPostgres() {
		return `SELECT column_name AS name
  FROM information_schema.columns
 WHERE table_schema = current_schema() AND table_name = ?
 ORDER BY ordinal_position`
	}
	return `SELECT name FROM pragma_table_info(?) ORDER BY cid`
}
