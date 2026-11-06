package store

import (
	"context"
	"testing"

	"github.com/dynesshely/bot-harness/internal/pgmigrate"
)

// TestPostgresSchemaIsWhatTheMigratorExpects is the end-to-end regression for
// the memories UNIQUE constraint name.
//
// The bug it pins: pgmigrate recreates SQLite UNIQUE constraints under the name
// PostgreSQL itself would generate (memories_scope_group_openid_member_openid_
// text_key) and looks that exact name up in pg_constraint before adding it. When
// the store spelled the same constraint "uq_memories_…", the lookup missed and
// the migrator appended a second, identical unique index on top of a
// store-created database.
//
// So: create the schema the way the runtime does, then run the migrator's
// EnsureSchema over it and count the unique constraints on memories. One is
// correct; two is the bug.
func TestPostgresSchemaIsWhatTheMigratorExpects(t *testing.T) {
	dsn := pgScratchDatabase(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	defer s.Close()
	before := memoriesUniqueNames(t, s.db)
	if len(before) != 1 {
		t.Fatalf("setup: memories unique constraints = %v, want 1", before)
	}

	// A source-shaped description of memories, exactly as pgmigrate discovers it
	// from SQLite: same columns, the UNIQUE constraint under the name it
	// synthesizes for SQLite's anonymous sqlite_autoindex.
	memories := &pgmigrate.Table{
		Name: "memories",
		PK:   []string{"id"},
		Columns: []pgmigrate.Column{
			{Name: "id", DeclType: "INTEGER", PKOrder: 1},
			{Name: "scope", DeclType: "TEXT", NotNull: true},
			{Name: "group_openid", DeclType: "TEXT", NotNull: true},
			{Name: "member_openid", DeclType: "TEXT", NotNull: true},
			{Name: "kind", DeclType: "TEXT", NotNull: true},
			{Name: "text", DeclType: "TEXT", NotNull: true},
			{Name: "source", DeclType: "TEXT", NotNull: true},
			{Name: "salience", DeclType: "REAL", NotNull: true},
			{Name: "hits", DeclType: "INTEGER", NotNull: true},
			{Name: "last_used_at", DeclType: "TEXT", NotNull: true},
			{Name: "created_at", DeclType: "TEXT", NotNull: true},
		},
		Uniques: []pgmigrate.Index{{
			Name:   "memories_scope_group_openid_member_openid_text_key",
			Unique: true,
			Columns: []pgmigrate.IndexColumn{
				{Name: "scope"}, {Name: "group_openid"},
				{Name: "member_openid"}, {Name: "text"},
			},
		}},
	}

	ctx := context.Background()
	tgt, err := pgmigrate.OpenTarget(ctx, dsn)
	if err != nil {
		t.Fatalf("open migrator target: %v", err)
	}
	defer func() { _ = tgt.Close(ctx) }()
	if _, err := tgt.EnsureSchema(ctx, []*pgmigrate.Table{memories}); err != nil {
		t.Fatalf("migrator EnsureSchema over a store-created schema: %v", err)
	}

	after := memoriesUniqueNames(t, s.db)
	if len(after) != 1 || after[0] != memoriesUnique {
		t.Fatalf("the migrator added a redundant unique index: before=%v after=%v, want exactly [%s]",
			before, after, memoriesUnique)
	}
}
