package config

import "testing"

// DATABASE_URL 为空时必须仍然返回 SQLite 文件路径：所有既有部署走的都是这条路。
func TestDataSourceDefaultsToSQLite(t *testing.T) {
	c := &Config{DBPath: "./data/bot.db"}
	if got := c.DataSource(); got != "./data/bot.db" {
		t.Fatalf("DataSource() = %q, want the SQLite path", got)
	}
	if c.UsesPostgres() {
		t.Fatal("an empty DATABASE_URL must not select PostgreSQL")
	}
}

// 配了 DATABASE_URL 就走 PG，且 SQLite 路径不再参与选择。
func TestDataSourcePrefersDatabaseURL(t *testing.T) {
	dsn := "postgres://bot:pw@127.0.0.1:5432/bot?sslmode=disable"
	c := &Config{DBPath: "./data/bot.db", DatabaseURL: dsn}
	if got := c.DataSource(); got != dsn {
		t.Fatalf("DataSource() = %q, want the DSN", got)
	}
	if !c.UsesPostgres() {
		t.Fatal("a postgres:// DSN must select PostgreSQL")
	}
}

// postgresql:// 是同一协议的另一写法，必须同样识别。
func TestUsesPostgresAcceptsBothSchemes(t *testing.T) {
	for _, dsn := range []string{
		"postgres://bot@h/db",
		"postgresql://bot@h/db",
		"POSTGRES://bot@h/db",
		"  postgres://bot@h/db  ",
	} {
		if !(&Config{DatabaseURL: dsn}).UsesPostgres() {
			t.Errorf("UsesPostgres() = false for %q", dsn)
		}
	}
	// 这些都不是 PG：要么是 SQLite 路径，要么是别的引擎。
	for _, dsn := range []string{
		"", "./data/bot.db", "/var/lib/bot.db", "mysql://u@h/db", "postgres",
	} {
		if (&Config{DatabaseURL: dsn}).UsesPostgres() {
			t.Errorf("UsesPostgres() = true for %q", dsn)
		}
	}
}

// Load 从环境读 DATABASE_URL，且 DB_PATH 仍然有默认值。
func TestLoadReadsDatabaseURLAndKeepsDBPath(t *testing.T) {
	t.Setenv("ENV_FILE", "/nonexistent-env-file")
	dsn := "postgres://bot:pw@127.0.0.1:5432/bot?sslmode=disable"
	t.Setenv("DATABASE_URL", dsn)
	t.Setenv("DB_PATH", "/tmp/custom.db")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DatabaseURL != dsn {
		t.Fatalf("DatabaseURL = %q, want %q", c.DatabaseURL, dsn)
	}
	if c.DBPath != "/tmp/custom.db" {
		t.Fatalf("DBPath = %q, want the override", c.DBPath)
	}
	if c.DataSource() != dsn {
		t.Fatalf("DataSource() = %q, want the DSN", c.DataSource())
	}
}

// 没设 DATABASE_URL 时 DB_PATH 有一个可用的默认值。
func TestLoadDefaultsDBPath(t *testing.T) {
	t.Setenv("ENV_FILE", "/nonexistent-env-file")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DB_PATH", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DBPath == "" {
		t.Fatal("DBPath must have a default")
	}
	if c.UsesPostgres() {
		t.Fatal("no DATABASE_URL must mean SQLite")
	}
}

// nil Config 不应 panic（有些调用路径会在错误分支里拿到 nil）。
func TestNilConfigIsSafe(t *testing.T) {
	var c *Config
	if c.DataSource() != "" {
		t.Fatal("nil Config DataSource() must be empty")
	}
	if c.UsesPostgres() {
		t.Fatal("nil Config must not claim PostgreSQL")
	}
}
