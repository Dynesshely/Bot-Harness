package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/dynesshely/bot-harness/internal/testpg"
)

// pgScratchDatabase creates a throwaway database next to the one PG_TEST_DSN
// points at and returns a DSN for it. The normalization tests below need a
// database whose memories constraint they can put into a legacy state, which
// must never happen to a shared scratch database.
func pgScratchDatabase(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set; skipping the PostgreSQL schema-normalization tests")
	}
	// Refuse the production database before any connection is opened.
	testpg.Guard(t, dsn)
	ctx := context.Background()
	base, err := Open(dsn)
	if err != nil {
		t.Fatalf("open PG_TEST_DSN: %v", err)
	}
	defer base.Close()

	name := fmt.Sprintf("store_norm_%d", time.Now().UnixNano())
	if _, err := base.db.ExecContext(ctx, "CREATE DATABASE \""+name+"\""); err != nil {
		t.Fatalf("create scratch database: %v", err)
	}
	t.Cleanup(func() {
		c, err := Open(dsn)
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = c.db.ExecContext(context.Background(), "DROP DATABASE IF EXISTS \""+name+"\" WITH (FORCE)")
	})

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse PG_TEST_DSN: %v", err)
	}
	parsed.Path = "/" + name
	return parsed.String()
}

// dollarSign is PostgreSQL's bind-placeholder prefix. It lives in a constant so
// the SQL above stays readable next to SQLite-style "?" fixtures elsewhere.
const dollarSign = "$"

// memoriesUniqueNames lists the UNIQUE constraints of the memories table by name.
func memoriesUniqueNames(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		"SELECT conname FROM pg_constraint WHERE conrelid = 'memories'::regclass AND contype = 'u' ORDER BY conname")
	if err != nil {
		t.Fatalf("list memories unique constraints: %v", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan constraint name: %v", err)
		}
		names = append(names, n)
	}
	return names
}

// TestPostgresMemoriesUniqueNameMatchesMigrator pins the property that makes a
// database created by the store and one created by internal/pgmigrate
// interchangeable: the memories UNIQUE constraint carries the name PostgreSQL
// itself would generate, which is exactly what pgmigrate synthesizes for
// SQLite's anonymous sqlite_autoindex. If this name drifts back to a bespoke
// "uq_…" spelling, running the migrator against a store-created database adds a
// second, redundant unique index.
func TestPostgresMemoriesUniqueNameMatchesMigrator(t *testing.T) {
	dsn := pgScratchDatabase(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	defer s.Close()

	if memoriesUnique != "memories_scope_group_openid_member_openid_text_key" {
		t.Fatalf("shared constraint name = %q, want the PostgreSQL-generated memories_scope_group_openid_member_openid_text_key", memoriesUnique)
	}
	if got := memoriesUniqueNames(t, s.db); len(got) != 1 || got[0] != memoriesUnique {
		t.Fatalf("memories unique constraints = %v, want exactly [%s]", got, memoriesUnique)
	}

	// Re-opening must not create anything new: the schema is idempotent.
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("re-open scratch database: %v", err)
	}
	defer s2.Close()
	if again := memoriesUniqueNames(t, s2.db); len(again) != 1 || again[0] != memoriesUnique {
		t.Fatalf("after re-open: memories unique constraints = %v, want exactly [%s]", again, memoriesUnique)
	}
}

// TestPostgresNormalizesLegacyMemoriesUnique covers the upgrade path: a
// database created by an older build carries "uq_memories_…". Opening it must
// rename the constraint in place, keep the data, and stay a no-op afterwards.
func TestPostgresNormalizesLegacyMemoriesUnique(t *testing.T) {
	dsn := pgScratchDatabase(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	ctx := context.Background()
	// Put the schema back into the pre-fix shape.
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE memories RENAME CONSTRAINT "+
		pgIdent(memoriesUnique)+" TO "+pgIdent(legacyMemoriesUnique)); err != nil {
		t.Fatalf("simulate legacy constraint: %v", err)
	}
	// A row inserted before the upgrade must survive it.
	in := MemoryInput{Scope: MemoryGroup, GroupOpenID: "norm-group", Text: "改名前的记忆"}
	id, err := s.AddMemory(ctx, in)
	if err != nil {
		t.Fatalf("add memory under the legacy name: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Open behaves like a restart of the new binary against the old database.
	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("re-open legacy database: %v", err)
	}
	defer s2.Close()
	if got := memoriesUniqueNames(t, s2.db); len(got) != 1 || got[0] != memoriesUnique {
		t.Fatalf("after upgrade: memories unique constraints = %v, want exactly [%s]", got, memoriesUnique)
	}
	var n int
	if err := s2.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM memories WHERE id = "+dollarSign+"1", id).Scan(&n); err != nil {
		t.Fatalf("count upgraded row: %v", err)
	}
	if n != 1 {
		t.Fatalf("the upgrade lost the pre-existing memory row (found %d)", n)
	}
	// The constraint still enforces uniqueness under its new name.
	if _, err := s2.AddMemory(ctx, in); err != nil {
		t.Fatalf("upsert after the rename: %v", err)
	}
	dup := "INSERT INTO memories (scope, group_openid, member_openid, kind, text, created_at) VALUES (" +
		dollarSign + "1, " + dollarSign + "2, " + dollarSign + "3, " + dollarSign + "4, " + dollarSign + "5, " + dollarSign + "6)"
	if _, err := s2.db.ExecContext(ctx, dup,
		in.Scope, in.GroupOpenID, in.MemberOpenID, "fact", in.Text, nowRFC3339()); err == nil {
		t.Fatal("the unique constraint no longer rejects a duplicate memory after the rename")
	}

	// A second open is a no-op: the rename must not be attempted twice.
	if err := s2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s3, err := Open(dsn)
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	defer s3.Close()
	if got := memoriesUniqueNames(t, s3.db); len(got) != 1 || got[0] != memoriesUnique {
		t.Fatalf("re-open after rename: memories unique constraints = %v, want exactly [%s]", got, memoriesUnique)
	}
}

// TestPostgresDropsDuplicateMemoriesUnique covers the state the bug actually
// produced: both names present, covering the same columns. Opening such a
// database must leave exactly one unique constraint (the shared name).
func TestPostgresDropsDuplicateMemoriesUnique(t *testing.T) {
	dsn := pgScratchDatabase(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	ctx := context.Background()
	// Exactly what pgmigrate used to add on top of a store-created database.
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE memories ADD CONSTRAINT "+
		pgIdent(legacyMemoriesUnique)+" UNIQUE (scope, group_openid, member_openid, text)"); err != nil {
		t.Fatalf("simulate the redundant index: %v", err)
	}
	if got := memoriesUniqueNames(t, s.db); len(got) != 2 {
		t.Fatalf("setup: memories unique constraints = %v, want 2", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("re-open duplicated database: %v", err)
	}
	defer s2.Close()
	if got := memoriesUniqueNames(t, s2.db); len(got) != 1 || got[0] != memoriesUnique {
		t.Fatalf("after normalization: memories unique constraints = %v, want exactly [%s]", got, memoriesUnique)
	}
}

// TestPostgresKeepsDistinctMemoriesUnique makes sure the cleanup only ever
// touches a genuine duplicate: a legacy-named constraint over different columns
// is not ours to drop.
func TestPostgresKeepsDistinctMemoriesUnique(t *testing.T) {
	dsn := pgScratchDatabase(t)
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open scratch database: %v", err)
	}
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, "ALTER TABLE memories ADD CONSTRAINT "+
		pgIdent(legacyMemoriesUnique)+" UNIQUE (group_openid, kind)"); err != nil {
		t.Fatalf("add a distinct legacy constraint: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, err := Open(dsn)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer s2.Close()
	if got := memoriesUniqueNames(t, s2.db); len(got) != 2 {
		t.Fatalf("a legacy constraint over different columns was dropped: %v", got)
	}
}
