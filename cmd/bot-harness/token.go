package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dynesshely/bot-harness/internal/config"
	"github.com/dynesshely/bot-harness/internal/settings"
	"github.com/dynesshely/bot-harness/internal/store"
)

// adminTokenCmd prints the effective admin token (the one persisted in the
// database) to stdout and exits. It replaces the old "grep ADMIN_TOKEN .env"
// SOP: after D-036 the .env value is not what authenticates the admin panel, so
// reading it there is how people ended up locked out.
//
// Only the token reaches stdout; nothing else is printed and no log file is
// written, so `bot-harness token` can be used in a shell pipeline. Other secrets
// (qq_app_secret, openai_api_key) are never read or printed.
//
// It opens the database the normal read-write way, so a copy that is behind would
// have its schema migrations applied first; an up-to-date database is left
// untouched (only data reads, no settings/business writes — pinned by
// TestAdminTokenCmdLeavesAnExistingDatabaseUnchanged). Deleting the database is
// not an option here: the command refuses to run and never creates an empty one.
func adminTokenCmd(stdout, stderr io.Writer) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(stderr, "token:", err)
		return 1
	}
	// store.Open migrates and would happily create an empty database. A
	// read-only command must not leave a stray bot.db behind (a mistaken DB_PATH
	// would then look like a fresh deployment), so refuse to run before that.
	//
	// PostgreSQL is different: a remote database cannot be stat'ed, and creating
	// one is not something this command could do anyway. Skipping the check there
	// is what makes `bot-harness token` work in a PostgreSQL deployment at all.
	if !cfg.UsesPostgres() {
		if _, err := os.Stat(cfg.DBPath); err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(stderr, "token: 数据库 %s 不存在；先启动一次 bot-harness 让它初始化并生成口令\n", cfg.DBPath)
			} else {
				fmt.Fprintf(stderr, "token: 无法读取数据库 %s: %v\n", cfg.DBPath, err)
			}
			return 1
		}
	}
	st, err := store.Open(cfg.DataSource())
	if err != nil {
		fmt.Fprintln(stderr, "token: open database:", err)
		return 1
	}
	defer func() { _ = st.Close() }()

	// Deliberately no SeedFromEnv: reading the token must not mutate the
	// database or import stale .env values.
	mgr, err := settings.New(context.Background(), st, settings.Defaults())
	if err != nil {
		fmt.Fprintln(stderr, "token: read settings:", err)
		return 1
	}
	token := strings.TrimSpace(mgr.Effective().AdminToken)
	if token == "" {
		fmt.Fprintln(stderr, "token: 数据库里还没有后台口令；先启动一次 bot-harness 让它生成并写库")
		return 1
	}
	fmt.Fprintln(stdout, token)
	return 0
}
