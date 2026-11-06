package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dynesshely/bot-harness/internal/pgmigrate"
)

// migrateCommand implements `bot-harness migrate`:
//
//	bot-harness migrate --from /data/bot.db --to postgres://... [flags]
//
// It copies the SQLite database into PostgreSQL and proves the copy with a
// full-table hash comparison. It never touches the running bot: the source is
// opened read-only and the target is written over its own connection.
func migrateCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		from       = fs.String("from", "", "path to the source SQLite database (required)")
		to         = fs.String("to", "", "target PostgreSQL DSN (default: $DATABASE_URL)")
		dryRun     = fs.Bool("dry-run", false, "print the plan and the target state, write nothing")
		verifyOnly = fs.Bool("verify-only", false, "compare source and target instead of copying")
		replace    = fs.Bool("replace", false, "truncate non-empty target tables before copying")
		batch      = fs.Int("batch", pgmigrate.DefaultBatch, "rows per COPY batch (media is capped at 16)")
		timeout    = fs.Duration("timeout", 30*time.Minute, "overall time budget")
	)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: bot-harness migrate --from <sqlite path> --to <postgres dsn> [options]")
		fmt.Fprintln(stderr)
		fmt.Fprintln(stderr, "Copies every table from the SQLite database into PostgreSQL, then verifies")
		fmt.Fprintln(stderr, "the copy with per-table row counts and a canonical SHA-256 of every row.")
		fmt.Fprintln(stderr)
		fs.PrintDefaults()
	}
	// Support both "--flag value" and "--flag=value"; flag.Parse does that for
	// a FlagSet too, so no manual splitting is needed.
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "migrate: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if *dryRun && *verifyOnly {
		fmt.Fprintln(stderr, "migrate: --dry-run and --verify-only are mutually exclusive")
		return 2
	}
	if *from == "" {
		fmt.Fprintln(stderr, "migrate: --from is required")
		fs.Usage()
		return 2
	}
	if *batch <= 0 {
		fmt.Fprintln(stderr, "migrate: --batch must be positive")
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	opts := pgmigrate.Options{
		From:       *from,
		To:         *to,
		DryRun:     *dryRun,
		VerifyOnly: *verifyOnly,
		Replace:    *replace,
		Batch:      *batch,
	}
	fmt.Fprintf(stdout, "bot-harness migrate (%s)\n", strings.Join(modeLabels(opts), ", "))
	if err := pgmigrate.Run(ctx, opts, stdout); err != nil {
		fmt.Fprintln(stderr, "migrate:", err)
		return 1
	}
	return 0
}

// modeLabels summarises the requested mode for the banner.
func modeLabels(opts pgmigrate.Options) []string {
	var labels []string
	switch {
	case opts.VerifyOnly:
		labels = append(labels, "verify-only")
	case opts.DryRun:
		labels = append(labels, "dry-run")
	default:
		labels = append(labels, "copy")
	}
	if opts.Replace {
		labels = append(labels, "replace")
	}
	labels = append(labels, "batch="+strconv.Itoa(opts.Batch))
	return labels
}
