package pgmigrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Result summarises a finished migration run.
type Result struct {
	Copied     map[string]int64
	Created    []string
	Sequences  []SequenceState
	Verify     *Report
	SourceRows map[string]int64
}

// Run executes the migrate subcommand.
//
// Behaviour by mode:
//
//	--verify-only                  compare source and target, write nothing
//	--dry-run                      print the plan (DDL, counts, sequence plan)
//	                               and the target state, write nothing
//	default                        create missing tables, refuse a non-empty
//	                               target, copy every table in its own
//	                               transaction, fix the identity sequences,
//	                               then verify
func Run(ctx context.Context, opts Options, out io.Writer) error {
	if strings.TrimSpace(opts.From) == "" {
		return fmt.Errorf("--from is required (path to the source SQLite database)")
	}
	if strings.TrimSpace(opts.To) == "" {
		opts.To = os.Getenv("DATABASE_URL")
	}
	if strings.TrimSpace(opts.To) == "" {
		return fmt.Errorf("--to is required (PostgreSQL DSN), or set DATABASE_URL")
	}
	if opts.Batch <= 0 {
		opts.Batch = DefaultBatch
	}

	src, err := OpenSource(opts.From)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	tables := src.AllTables()
	srcRows := map[string]int64{}
	for _, tbl := range tables {
		n, cerr := src.Count(tbl)
		if cerr != nil {
			return cerr
		}
		srcRows[tbl.Name] = n
	}

	fmt.Fprintf(out, "source: %s\n", opts.From)
	fmt.Fprintf(out, "tables: %d\n", len(tables))

	tgt, err := OpenTarget(ctx, opts.To)
	if err != nil {
		return err
	}
	defer func() { _ = tgt.Close(context.WithoutCancel(ctx)) }()

	version, verr := tgt.ServerVersion(ctx)
	if verr != nil {
		version = "unknown"
	}
	fmt.Fprintf(out, "target: PostgreSQL %s\n", version)

	if opts.VerifyOnly {
		return runVerify(ctx, src, tgt, tables, out)
	}

	if opts.DryRun {
		return runDryRun(ctx, src, tgt, tables, srcRows, opts, out)
	}

	// Schema: create what is missing, never touch what exists.
	created, err := tgt.EnsureSchema(ctx, tables)
	if err != nil {
		return err
	}
	if len(created) > 0 {
		fmt.Fprintf(out, "created %d table(s): %s\n", len(created), strings.Join(created, ", "))
	}

	if opts.Replace {
		if err := tgt.Truncate(ctx, tables); err != nil {
			return err
		}
		fmt.Fprintf(out, "truncated %d table(s) (--replace)\n", len(tables))
	} else if err := tgt.EnsureEmpty(ctx, tables); err != nil {
		return err
	}

	copied := map[string]int64{}
	start := time.Now()
	for _, tbl := range tables {
		t0 := time.Now()
		var lastLog int64
		n, cerr := tgt.CopyTable(ctx, tbl, src, opts.Batch, func(done int64) {
			if srcRows[tbl.Name] >= 5000 && done-lastLog >= 5000 {
				lastLog = done
				fmt.Fprintf(out, "  ... %s %d/%d rows\n", tbl.Name, done, srcRows[tbl.Name])
			}
		})
		if cerr != nil {
			return fmt.Errorf("copy %s: %w", tbl.Name, cerr)
		}
		copied[tbl.Name] = n
		fmt.Fprintf(out, "copied %-18s %6d row(s) in %s\n", tbl.Name, n, time.Since(t0).Round(time.Millisecond))
	}

	var seqs []SequenceState
	for _, tbl := range tables {
		if _, ok := tbl.IdentityColumn(); !ok {
			continue
		}
		maxID, merr := src.maxPrimaryKey(tbl)
		if merr != nil {
			return merr
		}
		if err := tgt.FixSequenceTo(ctx, tbl, maxID); err != nil {
			return err
		}
	}
	seqs, err = tgt.NextIdentityIDs(ctx, tables)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "sequences: %s\n", formatSequences(seqs))
	fmt.Fprintf(out, "copied %d table(s) in %s\n", len(tables), time.Since(start).Round(time.Millisecond))

	// Verify right away: a migration that cannot prove itself is not done.
	fmt.Fprintln(out)
	return runVerify(ctx, src, tgt, tables, out)
}

// runVerify prints the EXPECTED/ACTUAL report.
func runVerify(ctx context.Context, src *Source, tgt *Target, tables []*Table, out io.Writer) error {
	rep, err := Verify(ctx, src, tgt, tables)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%-18s %10s %10s  %-16s %-16s %s\n", "TABLE", "EXPECTED", "ACTUAL", "EXPECTED_SHA256", "ACTUAL_SHA256", "RESULT")
	for _, t := range rep.Tables {
		status := "OK"
		if !t.OK {
			status = "MISMATCH"
		}
		fmt.Fprintf(out, "%-18s %10d %10d  %-16s %-16s %s\n",
			t.Table, t.ExpectedRows, t.ActualRows, shortHash(t.ExpectedHash), shortHash(t.ActualHash), status)
		if t.Note != "" {
			fmt.Fprintf(out, "  %-16s %s\n", "", t.Note)
		}
		if !t.OK && t.Diff != "" {
			fmt.Fprintf(out, "  first difference: %s\n", t.Diff)
		}
	}
	if rep.SequenceNote != "" {
		fmt.Fprintf(out, "sequences: %s\n", rep.SequenceNote)
	}
	for _, p := range rep.SequenceProblems {
		fmt.Fprintf(out, "  sequence problem: %s\n", p)
	}
	if !rep.OK {
		var bad []string
		for _, t := range rep.Failed() {
			bad = append(bad, t.Table)
		}
		if len(bad) == 0 {
			bad = append(bad, "identity sequences")
		}
		return fmt.Errorf("verification FAILED for %d table(s): %s", len(bad), strings.Join(bad, ", "))
	}
	fmt.Fprintf(out, "verification PASSED: %d/%d tables match\n", len(rep.Tables), len(rep.Tables))
	return nil
}

// runDryRun reports what the migration would do without writing anything.
func runDryRun(ctx context.Context, src *Source, tgt *Target, tables []*Table, srcRows map[string]int64, opts Options, out io.Writer) error {
	fmt.Fprintln(out, "dry run: nothing is written")
	fmt.Fprintf(out, "batch: %d rows (media capped at %d)\n", opts.Batch, MediaBatchMax)
	fmt.Fprintln(out)

	missing := 0
	nonEmpty := 0
	for _, tbl := range tables {
		state := "present"
		if !tgt.TableExists(tbl.Name) {
			state = "would create"
			missing++
		} else {
			n, err := tgt.Count(ctx, tbl.Name)
			if err != nil {
				return err
			}
			if n > 0 {
				state = fmt.Sprintf("holds %d rows", n)
				nonEmpty++
			}
		}
		cols := len(tbl.Columns)
		fmt.Fprintf(out, "%-18s %6d source row(s)  %2d col(s)  pk=%s  %s\n",
			tbl.Name, srcRows[tbl.Name], cols, strings.Join(tbl.PK, ","), state)
		if !tgt.TableExists(tbl.Name) {
			fmt.Fprintf(out, "  DDL: %s\n", strings.Join(strings.Fields(renderDDL(tbl)), " "))
		}
	}
	fmt.Fprintln(out)
	if nonEmpty > 0 && !opts.Replace {
		fmt.Fprintf(out, "would REFUSE: %d table(s) already hold rows (rerun with --replace)\n", nonEmpty)
	} else if nonEmpty > 0 {
		fmt.Fprintf(out, "would TRUNCATE %d table(s) first (--replace)\n", nonEmpty)
	}
	if missing > 0 {
		fmt.Fprintf(out, "would CREATE %d table(s)\n", missing)
	}
	var seqPlan []SequenceState
	for _, tbl := range tables {
		col, ok := tbl.IdentityColumn()
		if !ok {
			continue
		}
		maxID, merr := src.maxPrimaryKey(tbl)
		if merr != nil {
			return merr
		}
		// Exactly the formula the real run applies (FixSequenceTo ->
		// renderSequenceFixup -> setval(GREATEST(max(pk), seq))): the plan can
		// never announce a different number than the run it describes.
		seqPlan = append(seqPlan, SequenceState{Table: tbl.Name + "." + col, Next: nextIDFromState(maxID, tbl.SeqValue)})
	}
	fmt.Fprintf(out, "would set sequences: %s\n", formatSequences(seqPlan))
	return nil
}

// nextIDFromState is the single source of truth for "which id does an identity
// column hand out next", shared by the dry-run plan, the real copy and the
// verifier.
//
// SQLite's sqlite_sequence holds the largest id the counter ever produced;
// the copy applies setval(max(pk, seq, 0)+1, false), which leaves the next
// nextval() at the same value. The next id is max(pk, seq) + 1 in both
// directions. Using max(pk) as a floor also protects against a stale counter:
// an id inserted explicitly beyond the counter can never be handed out twice.
//
// memories is not special: an empty table with seq=1 yields 2, a table with
// three rows and seq=4 yields 5 — no per-table branch anywhere.
func nextIDFromState(maxID, seqValue int64) int64 {
	value := maxID
	if seqValue > value {
		value = seqValue
	}
	if value < 0 {
		value = 0
	}
	return value + 1
}

// formatSequences renders "table=next,table=next".
func formatSequences(seqs []SequenceState) string {
	parts := make([]string, 0, len(seqs))
	for _, s := range seqs {
		parts = append(parts, fmt.Sprintf("%s=%d", s.Table, s.Next))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ", ")
}

// shortHash shortens a hex digest for the report table.
func shortHash(h string) string {
	if len(h) <= 16 {
		return h
	}
	return h[:16]
}
