package store

import (
	"reflect"
	"regexp"
	"testing"
)

var createTableRE = regexp.MustCompile(`CREATE TABLE IF NOT EXISTS ([a-z_]+)`)

func tableSet(ddl string) map[string]bool {
	out := map[string]bool{}
	for _, m := range createTableRE.FindAllStringSubmatch(ddl, -1) {
		out[m[1]] = true
	}
	return out
}

func TestSchemaTablesMatch(t *testing.T) {
	sqlite := tableSet(schema)
	postgres := tableSet(pgSchema)
	if !reflect.DeepEqual(sqlite, postgres) {
		t.Fatalf("schema table sets differ: sqlite=%v postgres=%v", sqlite, postgres)
	}
	if len(sqlite) < 18 {
		t.Fatalf("expected at least 18 tables, got %d", len(sqlite))
	}
}

func TestMigrationRegistry(t *testing.T) {
	list := migrationList(&Store{})
	seen := map[string]bool{}
	baseline := append([]string{}, baselineMigrations...)
	for i, m := range list {
		if seen[m.name] {
			t.Fatalf("duplicate migration name %q", m.name)
		}
		seen[m.name] = true
		if m.sqlite == nil {
			t.Fatalf("%s has no sqlite function", m.name)
		}
		if i < len(baseline) && m.name != baseline[i] {
			t.Fatalf("migration %d = %s, baseline = %s", i, m.name, baseline[i])
		}
		if i >= len(baseline) && m.postgres == nil {
			t.Fatalf("new migration %s has no postgres function", m.name)
		}
	}
	if len(list) < len(baseline) {
		t.Fatalf("migration list shorter than baseline: %d < %d", len(list), len(baseline))
	}
}
