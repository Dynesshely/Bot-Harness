package pgmigrate

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// typeSpec is the PostgreSQL rendering of one SQLite declared type.
type typeSpec struct {
	// SQL is the PostgreSQL column type.
	SQL string
	// Kind is a coarse family used by the copy path: "text", "int", "real",
	// "blob".
	Kind string
}

// mapType maps a SQLite declared type to the PostgreSQL target design:
// TEXT / INTEGER / DOUBLE PRECISION / BYTEA. SQLite's type affinity rules are
// applied first (sqlite.org/datatype3.html#determination_of_column_affinity),
// so "VARCHAR(32)" and "TEXT" converge on TEXT, and an untyped column converges
// through its affinity as well.
func mapType(declType string) typeSpec {
	t := strings.ToUpper(strings.TrimSpace(declType))
	if i := strings.IndexByte(t, '('); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	switch {
	case t == "":
		// No declared type: BLOB affinity, but untyped columns are in practice
		// text; TEXT is the widest lossless container because values are
		// passed through as strings.
		return typeSpec{SQL: "TEXT", Kind: "text"}
	case strings.Contains(t, "INT"):
		return typeSpec{SQL: "INTEGER", Kind: "int"}
	case strings.Contains(t, "CHAR"), strings.Contains(t, "CLOB"), strings.Contains(t, "TEXT"):
		return typeSpec{SQL: "TEXT", Kind: "text"}
	case strings.Contains(t, "BLOB"), t == "BINARY", t == "VARBINARY":
		return typeSpec{SQL: "BYTEA", Kind: "blob"}
	case strings.Contains(t, "REAL"), strings.Contains(t, "FLOA"), strings.Contains(t, "DOUB"):
		// REAL affinity, but SQLite stores integers there too. DOUBLE
		// PRECISION is the target design and round-trips every finite value
		// bit-exactly; the copy passes values in via text so integers that
		// happen to live in a REAL column are preserved as integers.
		return typeSpec{SQL: "DOUBLE PRECISION", Kind: "real"}
	case strings.Contains(t, "BOOL"):
		// SQLite keeps booleans as INTEGER; the target keeps INTEGER too so
		// the bytes survive verbatim.
		return typeSpec{SQL: "INTEGER", Kind: "int"}
	case strings.Contains(t, "NUM"), strings.Contains(t, "DEC"), strings.Contains(t, "MONEY"):
		// NUMERIC affinity: SQLite stores integers, reals and numeric-looking
		// text. TEXT is lossless for all three; NUMERIC is not (it would
		// rewrite "007" and overflow 64-bit integers).
		return typeSpec{SQL: "TEXT", Kind: "text"}
	case strings.Contains(t, "DATE"), strings.Contains(t, "TIME"):
		return typeSpec{SQL: "TEXT", Kind: "text"}
	default:
		return typeSpec{SQL: "TEXT", Kind: "text"}
	}
}

// columnTypeOverrides pins the PostgreSQL type of specific columns where the
// target design deliberately differs from the naive SQLite type mapping.
//
// This is not a column list: the copy still discovers every column at runtime.
// It only keeps the schema this tool creates byte-identical to the schema the
// runtime store creates (internal/store/pg.go), so a database populated here
// and a database created by the store are interchangeable.
var columnTypeOverrides = map[string]string{
	// media.size is a blob length; the store declares it BIGINT.
	"media.size": "BIGINT",
}

// timestampColumns is every (table, column) the harness stores as an RFC3339
// timestamp. SQLite keeps them as TEXT; the PostgreSQL target uses native
// TIMESTAMPTZ so an instant keeps its meaning (and its ordering) no matter
// which UTC offset the source happened to write. The keys are qualified
// because a column name alone is not unique (created_at appears eight times).
var timestampColumns = map[string]bool{
	"conversations.created_at":     true,
	"conversations.last_active_at": true,
	"dedup.created_at":             true,
	"media.created_at":             true,
	"memories.created_at":          true,
	"memories.last_used_at":        true,
	"messages.created_at":          true,
	"proactive_state.last_sent_at": true,
	"proactive_state.updated_at":   true,
	"records.created_at":           true,
	"scheduled_tasks.created_at":   true,
	"scheduled_tasks.due_at":       true,
	"scheduled_tasks.lease_until":  true,
	"scheduled_tasks.sent_at":      true,
	"schema_migrations.applied_at": true,
	"settings.updated_at":          true,
	"stats.bucket":                 true,
	"tool_calls.created_at":        true,
	"commitments.created_at":       true,
	"commitments.deadline_at":      true,
	"commitments.next_check_at":    true,
	"commitments.last_nudge_at":    true,
	"commitments.ack_at":           true,
	"human_status.updated_at":      true,
	"human_status.expires_at":      true,
	"affect_states.ticked_at":      true,
	"affect_states.updated_at":     true,
	"affect_states.paused_until":   true,
	"affect_events.created_at":     true,
	"affect_history.at":            true,
}

// nullableTimestampColumns are the timestamp columns whose SQLite value may be
// the empty string, meaning "unset". The empty string is not a valid
// timestamptz literal, so on the target these columns are nullable and the
// copy maps ” to NULL (the verifier projects NULL back to ”).
var nullableTimestampColumns = map[string]bool{
	"memories.last_used_at":        true,
	"proactive_state.last_sent_at": true,
	"proactive_state.updated_at":   true,
	"scheduled_tasks.lease_until":  true,
	"scheduled_tasks.sent_at":      true,
	"commitments.deadline_at":      true,
	"commitments.last_nudge_at":    true,
	"commitments.ack_at":           true,
	"affect_states.paused_until":   true,
}

// renderDDL builds the PostgreSQL CREATE TABLE for one source table.
//
// Design points, matching the runtime store's PostgreSQL schema:
//   - TEXT / INTEGER / DOUBLE PRECISION / BYTEA only, except timestamp
//     columns, which become native TIMESTAMPTZ (see timestampColumns).
//   - INTEGER PRIMARY KEY AUTOINCREMENT becomes GENERATED BY DEFAULT AS
//     IDENTITY, so explicit ids can be inserted and the sequence can be
//     advanced with setval afterwards.
//   - No FOREIGN KEY constraints at all: the source has none and records has
//     conversation_id orphans that must survive the copy.
//   - NOT NULL is carried over (the source database has zero NULLs), except
//     for nullableTimestampColumns: those are the columns where the source
//     spells "unset" as ”, which timestamptz cannot store, so they are
//     created nullable and without a default.
func renderDDL(t *Table) string {
	var b strings.Builder
	b.WriteString("CREATE TABLE IF NOT EXISTS ")
	b.WriteString(quoteIdent(t.Name))
	b.WriteString(" (\n")
	for i, c := range t.Columns {
		key := t.Name + "." + c.Name
		nullableTime := nullableTimestampColumns[key]
		b.WriteString("  ")
		b.WriteString(quoteIdent(c.Name))
		b.WriteString(" ")
		sqlType := mapType(c.DeclType).SQL
		if override, ok := columnTypeOverrides[key]; ok {
			sqlType = override
		}
		if timestampColumns[key] {
			sqlType = "TIMESTAMPTZ"
		}
		if idCol, isIdentity := t.IdentityColumn(); isIdentity && c.Name == idCol {
			// The runtime store declares identity primary keys as BIGINT; the
			// copy uses the same type so a table created here and a table
			// created by the store are interchangeable.
			sqlType = "BIGINT"
		}
		b.WriteString(sqlType)
		if idCol, isIdentity := t.IdentityColumn(); isIdentity && c.Name == idCol {
			b.WriteString(" GENERATED BY DEFAULT AS IDENTITY")
		}
		if c.NotNull && !nullableTime {
			b.WriteString(" NOT NULL")
		}
		// A SQLite text default cannot be replayed on a timestamptz column (the
		// empty string is not a valid timestamp), and the runtime store writes
		// every timestamp explicitly, so timestamp defaults are dropped.
		if !timestampColumns[key] {
			if def, ok := renderDefault(c); ok {
				b.WriteString(" DEFAULT ")
				b.WriteString(def)
			}
		}
		if i < len(t.Columns)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	if len(t.PK) > 0 {
		b.WriteString("  , PRIMARY KEY (")
		b.WriteString(strings.Join(quoteAll(t.PK), ", "))
		b.WriteString(")\n")
	}
	b.WriteString(")")
	return b.String()
}

// renderDefault renders one column default for PostgreSQL.
//
// A SQLite default may be a literal ('x', 0, 1.5, NULL), a bare numeric
// keyword, or a parenthesised expression. String, numeric and NULL literals
// carry over verbatim; anything else (CURRENT_TIMESTAMP and friends) is
// reported as unsupported and dropped, because silently rewriting a default
// would change insert behaviour without anybody noticing. The store always
// writes created_at/updated_at explicitly, so no source column relies on it
// today; the migration reports it loudly if that ever changes.
func renderDefault(c Column) (string, bool) {
	if !c.HasDefault {
		return "", false
	}
	expr := strings.TrimSpace(c.DefaultExpr)
	if expr == "" {
		return "", false
	}
	upper := strings.ToUpper(expr)
	switch upper {
	case "NULL":
		// NOT NULL + DEFAULT NULL is a contradiction; skip it.
		return "", false
	case "TRUE":
		return "TRUE", true
	case "FALSE":
		return "FALSE", true
	}
	// Quoted string literal.
	if len(expr) >= 2 && expr[0] == '\'' && expr[len(expr)-1] == '\'' {
		return expr, true
	}
	// Numeric literal.
	if _, err := strconv.ParseFloat(expr, 64); err == nil {
		return expr, true
	}
	// Anything else is an expression we refuse to guess at.
	return "", false
}

// renderIndexes renders the CREATE INDEX statements for one table.
func renderIndexes(t *Table) []string {
	var out []string
	for _, idx := range t.Indexes {
		if idx.SkipReason != "" {
			continue
		}
		cols := make([]string, len(idx.Columns))
		for i, c := range idx.Columns {
			cols[i] = quoteIdent(c.Name)
			if c.Desc {
				cols[i] += " DESC"
			}
		}
		stmt := "CREATE INDEX IF NOT EXISTS " + quoteIdent(idx.Name) +
			" ON " + quoteIdent(t.Name) + " (" + strings.Join(cols, ", ") + ")"
		if idx.Where != "" {
			// SQLite and PostgreSQL agree on simple predicates over
			// non-null columns; anything the source cannot prove is skipped
			// rather than risking a broken index on the target.
			stmt += " WHERE " + idx.Where
		}
		out = append(out, stmt)
	}
	return out
}

// renderUniqueConstraints renders the ALTER TABLE ... ADD CONSTRAINT
// statements for the UNIQUE constraints of one table.
func renderUniqueConstraints(t *Table) []string {
	var out []string
	for _, u := range t.Uniques {
		if u.SkipReason != "" {
			continue
		}
		cols := make([]string, len(u.Columns))
		for i, c := range u.Columns {
			cols[i] = quoteIdent(c.Name)
		}
		out = append(out, "ALTER TABLE "+quoteIdent(t.Name)+
			" ADD CONSTRAINT "+quoteIdent(u.Name)+
			" UNIQUE ("+strings.Join(cols, ", ")+")")
	}
	return out
}

// renderSequenceFixup produces the statements that move the identity sequence
// of one table to the right place after an explicit-id copy.
//
// SQLite's sqlite_sequence stores the largest id it has ever handed out, so
// the next id it would produce is max(pk, seq)+1. PostgreSQL's setval(value,
// false) makes the next nextval() return value itself, so the cleanest way to
// express the same state is setval(GREATEST(max(pk), seq, 0)+1, false).
//
// The +1 also fixes an empty table that never had a sqlite_sequence row
// (commitments / human_status in a fresh source): the old
// setval(GREATEST(...,0), true) called setval(0), which PostgreSQL rejects
// because sequences start at 1. The new form calls setval(1, false) and the
// first nextval() is 1 — exactly the id SQLite would hand out.
//
// GREATEST also protects against a stale counter: if rows were ever inserted
// with explicit ids beyond the counter, max(pk) wins and the sequence cannot
// hand out an id that is already taken.
func renderSequenceFixup(t *Table, col string, seqValue int64) []string {
	if seqValue < 0 {
		seqValue = 0
	}
	return []string{
		fmt.Sprintf("SELECT setval(pg_get_serial_sequence(%s, %s), GREATEST(COALESCE((SELECT MAX(%s) FROM %s), 0), %d, 0) + 1, false)",
			sqlQuote(t.Name), sqlQuote(col), quoteIdent(col), quoteIdent(t.Name), seqValue),
	}
}

// maxPrimaryKey returns the largest value of the identity column, or 0.
func (s *Source) maxPrimaryKey(t *Table) (int64, error) {
	col, ok := t.IdentityColumn()
	if !ok {
		return 0, nil
	}
	var maxID sql.NullInt64
	if err := s.db.QueryRow("SELECT MAX(" + quoteIdent(col) + ") FROM " + quoteIdent(t.Name)).Scan(&maxID); err != nil {
		return 0, fmt.Errorf("max %s.%s: %w", t.Name, col, err)
	}
	if !maxID.Valid {
		return 0, nil
	}
	return maxID.Int64, nil
}

// sqlQuote renders a SQL string literal.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
