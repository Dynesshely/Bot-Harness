// Package config holds the *bootstrap* configuration: the handful of values that
// must exist before the database is open.
//
// Everything else (QQ credentials, transport, admin token, listen address, TLS,
// logging, media limits, model/reply/capability settings…) lives in the database
// and is editable at runtime from the admin panel. Keeping this package tiny is
// deliberate: a setting that is read from the environment cannot be changed
// without a redeploy, and redeploys are exactly what we are trying to avoid.
package config

import (
	"os"
	"strings"

	"github.com/joho/godotenv"
)

// Version is overridable at build time via -ldflags "-X .../config.Version=...".
var Version = "0.4.4"

// Config is the bootstrap configuration: only what is needed to find the
// database. Everything else lives in package settings.
type Config struct {
	Version string
	// DBPath is the SQLite file. It cannot come from the database itself.
	DBPath string
	// DatabaseURL is an optional PostgreSQL DSN (postgres:// or postgresql://).
	// It is a *bootstrap* value for the same reason DBPath is: the location of
	// the database cannot be read out of the database. When it is set the caller
	// should pass it to store.Open instead of DBPath; DBPath is kept both for
	// SQLite-only deployments and for the tools that still assume a file on
	// disk (the token subcommand stats it).
	DatabaseURL string
	// EnvFile records which dotenv file was loaded, for diagnostics only.
	EnvFile string
}

// Load reads the bootstrap environment, seeding from a dotenv file when present.
//
// Real environment variables always win over the file contents.
func Load() (*Config, error) {
	envFile := strings.TrimSpace(os.Getenv("ENV_FILE"))
	if envFile == "" {
		envFile = ".env"
	}
	loaded := ""
	if _, err := os.Stat(envFile); err == nil {
		if err := godotenv.Load(envFile); err == nil {
			loaded = envFile
		}
	}

	dbPath := strings.TrimSpace(os.Getenv("DB_PATH"))
	if dbPath == "" {
		dbPath = "./data/bot.db"
	}
	// DATABASE_URL is deliberately *not* defaulted: an empty value means "stay
	// on SQLite", which keeps every existing deployment on the path it is on.
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	return &Config{
		Version:     Version,
		DBPath:      dbPath,
		DatabaseURL: databaseURL,
		EnvFile:     loaded,
	}, nil
}

// DataSource is the argument to hand to store.Open.
//
// DATABASE_URL wins when it is set, so a deployment switches engine by adding
// one variable; when it is empty the SQLite file path is returned unchanged and
// the process behaves exactly as it did before PostgreSQL existed.
func (c *Config) DataSource() string {
	if c == nil {
		return ""
	}
	if u := strings.TrimSpace(c.DatabaseURL); u != "" {
		return u
	}
	return c.DBPath
}

// UsesPostgres reports whether the selected data source is a PostgreSQL DSN.
//
// The prefix check mirrors store.isPostgresDSN on purpose: this package must
// stay dependency-free (it is imported before the database exists), and the two
// spellings are the whole contract.
func (c *Config) UsesPostgres() bool {
	if c == nil {
		return false
	}
	u := strings.ToLower(strings.TrimSpace(c.DatabaseURL))
	return strings.HasPrefix(u, "postgres://") || strings.HasPrefix(u, "postgresql://")
}
