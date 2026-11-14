package store

import (
	"context"
	"os"
	"testing"

	"github.com/dynesshely/bot-harness/internal/testpg"
)

// TestPostgresMigrationRunner proves that a new dual-dialect migration is
// actually executed on PostgreSQL after the baseline, not just recorded.
func TestPostgresMigrationRunner(t *testing.T) {
	dsn := os.Getenv("PG_TEST_DSN")
	if dsn == "" {
		t.Skip("PG_TEST_DSN not set")
	}
	testpg.Guard(t, dsn)
	ctx := context.Background()
	s, err := Open(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	const table = "migration_probe_test"
	const name = "9999_probe_test"
	_, _ = s.execContext(ctx, `DROP TABLE IF EXISTS `+table)
	_, _ = s.execContext(ctx, `DELETE FROM schema_migrations WHERE name = ?`, name)
	defer func() {
		_, _ = s.execContext(ctx, `DROP TABLE IF EXISTS `+table)
		_, _ = s.execContext(ctx, `DELETE FROM schema_migrations WHERE name = ?`, name)
	}()

	list := []migration{{
		name: name,
		postgres: func(ctx context.Context) error {
			_, err := s.execContext(ctx, `CREATE TABLE `+table+` (id INTEGER PRIMARY KEY)`)
			return err
		},
	}}
	if err := s.applyPostgresMigrations(ctx, list); err != nil {
		t.Fatalf("apply: %v", err)
	}
	var exists int
	if err := s.queryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, table).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != 1 {
		t.Fatalf("migration did not create the probe table")
	}
	if err := s.applyPostgresMigrations(ctx, list); err != nil {
		t.Fatalf("re-apply should be idempotent: %v", err)
	}
}
