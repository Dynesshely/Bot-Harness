// Package pgmigrate implements the one-shot SQLite → PostgreSQL full copy
// behind the `bot-harness migrate` subcommand, plus its zero-loss verifier.
//
// The package deliberately owns its own PostgreSQL DDL so the copy can run
// before the runtime store learns to speak PostgreSQL (T46): the generated
// schema follows the target design (TEXT / INTEGER / BYTEA, IDENTITY BY
// DEFAULT, no foreign keys). When the target schema already exists the DDL is
// skipped and the existing tables are used verbatim, so the same binary keeps
// working after the store-side schema lands.
//
// Column lists are never hardcoded: they come from the source
// `PRAGMA table_info` and are reconciled against the target
// `information_schema.columns`.
package pgmigrate

import (
	"fmt"
	"strings"
)

// TableOrder is the load order. Parents come before children and media (the
// only table with large blobs) comes last so a failed run never leaves the
// expensive table half-copied behind cheap ones.
//
// schema_migrations is copied first because it records which migrations the
// source already applied; media is copied last because it carries blobs. New
// store tables must be added here or the copy silently leaves them behind.
var TableOrder = []string{
	"schema_migrations",
	"settings",
	"conversations",
	"dedup",
	"stats",
	"proactive_state",
	"messages",
	"records",
	"tool_calls",
	"scheduled_tasks",
	"commitments",
	"human_status",
	"affect_states",
	"affect_events",
	"affect_history",
	"join_decisions",
	"join_topics",
	"memories",
	"media",
}

const (
	// DefaultBatch is the default number of rows per COPY batch.
	DefaultBatch = 1000
	// MediaBatchMax caps the batch size for the media table. Media rows carry
	// multi-megabyte blobs, so the copy keeps at most 16 rows (≈50 MB) in
	// memory at a time regardless of --batch.
	MediaBatchMax = 16
	// MemoriesTable is the memories table name. It deliberately gets no special
	// sequence handling: the one setval(GREATEST(max(pk), seq)) formula covers
	// it exactly like every other identity table (an empty table whose counter
	// is 1 yields next id 2 all by itself; three rows with counter 4 yield 5).
	// Anything table-specific here would be a second source of truth and would
	// drift from the dry-run plan, the copy and the verifier.
	MemoriesTable = "memories"
	// MediaTable is verified per row (content sha256) in addition to the
	// generic row hash.
	MediaTable = "media"
	// SettingsTable is verified per key with JSON semantic equality.
	SettingsTable = "settings"
)

// Options are the CLI options of the migrate subcommand.
type Options struct {
	From       string // path to the source SQLite database
	To         string // target PostgreSQL DSN
	DryRun     bool   // report the plan, write nothing
	VerifyOnly bool   // only compare source and target
	Replace    bool   // truncate non-empty target tables before loading
	Batch      int    // rows per batch for non-media tables
}

// Table describes one table discovered on the source side and matched against
// the target side.
type Table struct {
	Name    string
	Columns []Column
	// PK lists the primary key columns in key order.
	PK []string
	// AutoIncrement is true when the source table declares AUTOINCREMENT.
	AutoIncrement bool
	// SeqValue is the source AUTOINCREMENT counter, when the database has one.
	SeqValue int64
	HasSeq   bool
	// Indexes are the user-created (origin 'c') secondary indexes.
	Indexes []Index
	// Uniques are the UNIQUE constraints (origin 'u') on the table.
	Uniques []Index
}

// Column is one column with just enough metadata to build DDL and to detect
// schema drift.
type Column struct {
	Name     string
	DeclType string
	NotNull  bool
	// PKOrder is 0 for non-key columns, 1..n for key columns in key order.
	PKOrder int
	// HasDefault is true when the source declares a default. DefaultExpr is
	// the raw SQLite expression, replayed verbatim on the target so that
	// INSERT statements that omit a column keep working after the switch.
	HasDefault  bool
	DefaultExpr string
}

// IndexColumn is one column of an index, in index order.
type IndexColumn struct {
	Name string
	Desc bool
}

// Index is a secondary index or a UNIQUE constraint discovered on the source.
//
// Preserving them matters for more than performance: memories has a UNIQUE
// (scope, group_openid, member_openid, text) constraint that the store relies
// on to keep memories de-duplicated, and the indexes are what make the admin
// queries acceptable on 12k+ row tables.
type Index struct {
	Name    string
	Unique  bool
	Columns []IndexColumn
	// Where is the partial-index predicate; empty for a full index.
	Where string
	// SkipReason explains why an index is not replayed on the target.
	SkipReason string
}

// ColumnNames returns the column names in discovery order.
func (t *Table) ColumnNames() []string {
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
	}
	return names
}

// Column looks up a column by name.
func (t *Table) Column(name string) (Column, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c, true
		}
	}
	return Column{}, false
}

// IdentityColumn reports the single AUTOINCREMENT primary key column, if any.
func (t *Table) IdentityColumn() (string, bool) {
	if !t.AutoIncrement || len(t.PK) != 1 {
		return "", false
	}
	return t.PK[0], true
}

// BatchSize returns the effective batch size for this table.
func (t *Table) BatchSize(requested int) int {
	if requested <= 0 {
		requested = DefaultBatch
	}
	if t.Name == MediaTable && requested > MediaBatchMax {
		return MediaBatchMax
	}
	return requested
}

// quoteIdent quotes a PostgreSQL / SQLite identifier. Double quotes are the
// SQL standard form and work on both engines (backticks are MySQL-only and
// would fail on PostgreSQL).
func quoteIdent(name string) string {
	return "\"" + strings.ReplaceAll(name, "\"", "\"\"") + "\""
}

// quoteAll quotes every identifier in the list.
func quoteAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteIdent(n)
	}
	return out
}

// qualifiedColumns joins quoted column names for a SELECT list.
func qualifiedColumns(names []string) string {
	return strings.Join(quoteAll(names), ", ")
}

// orderByClause renders " ORDER BY <pk columns>" for a deterministic scan.
func orderByClause(pk []string) string {
	if len(pk) == 0 {
		return ""
	}
	return " ORDER BY " + qualifiedColumns(pk)
}

// describePK renders a primary key value for reports.
func describePK(pk []string, vals []any) string {
	parts := make([]string, len(pk))
	for i := range pk {
		parts[i] = fmt.Sprintf("%s=%s", pk[i], shortValue(vals[i]))
	}
	return strings.Join(parts, ",")
}
