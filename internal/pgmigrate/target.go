package pgmigrate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Target is an open PostgreSQL connection plus cached target metadata.
type Target struct {
	DB *pgx.Conn
	// Columns maps table -> column -> pg data type (lower case).
	Columns map[string]map[string]string
	// Tables is the set of target tables that exist.
	Tables map[string]bool
}

// OpenTarget connects to the PostgreSQL DSN.
func OpenTarget(ctx context.Context, dsn string) (*Target, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse target DSN: %w", err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect target: %w", err)
	}
	t := &Target{DB: conn, Columns: map[string]map[string]string{}, Tables: map[string]bool{}}
	if err := t.loadCatalog(ctx); err != nil {
		_ = conn.Close(ctx)
		return nil, err
	}
	return t, nil
}

// Close releases the connection.
func (t *Target) Close(ctx context.Context) error { return t.DB.Close(ctx) }

// ServerVersion returns the PostgreSQL version string.
func (t *Target) ServerVersion(ctx context.Context) (string, error) {
	var v string
	if err := t.DB.QueryRow(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// loadCatalog caches the tables and columns visible on the search path.
func (t *Target) loadCatalog(ctx context.Context) error {
	const q = "SELECT table_name, column_name, data_type FROM information_schema.columns " +
		"WHERE table_schema = current_schema() ORDER BY table_name, ordinal_position"
	rows, err := t.DB.Query(ctx, q)
	if err != nil {
		return fmt.Errorf("read information_schema.columns: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, column, dataType string
		if err := rows.Scan(&table, &column, &dataType); err != nil {
			return err
		}
		m, ok := t.Columns[table]
		if !ok {
			m = map[string]string{}
			t.Columns[table] = m
		}
		m[column] = strings.ToLower(dataType)
		t.Tables[table] = true
	}
	return rows.Err()
}

// TableExists reports whether the target table is present.
func (t *Target) TableExists(name string) bool { return t.Tables[name] }

// Count returns the target row count.
func (t *Target) Count(ctx context.Context, table string) (int64, error) {
	var n int64
	if err := t.DB.QueryRow(ctx, "SELECT COUNT(*) FROM "+quoteIdent(table)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

// EnsureSchema creates every missing table. Existing tables are left alone:
// the runtime store may have created them (T46) and this tool must not fight
// over the definition. A pre-existing table that is missing source columns is
// a hard error, because copying into a narrower table loses data silently.
func (t *Target) EnsureSchema(ctx context.Context, tables []*Table) ([]string, error) {
	var created []string
	for _, tbl := range tables {
		if t.TableExists(tbl.Name) {
			if err := t.checkColumns(tbl); err != nil {
				return created, err
			}
			continue
		}
		ddl := renderDDL(tbl)
		if _, err := t.DB.Exec(ctx, ddl); err != nil {
			return created, fmt.Errorf("create table %s: %w\n%s", tbl.Name, err, ddl)
		}
		created = append(created, tbl.Name)
	}
	if len(created) > 0 {
		if err := t.loadCatalog(ctx); err != nil {
			return created, err
		}
	}

	// Secondary objects are created for every table, not just new ones: a
	// table that already exists may have been created by the runtime store
	// without the UNIQUE constraint the source relies on. CREATE INDEX IF NOT
	// EXISTS is idempotent; the UNIQUE constraint is added only when it is
	// absent (PostgreSQL has no ADD CONSTRAINT IF NOT EXISTS, so the catalog
	// is consulted first).
	for _, tbl := range tables {
		for _, stmt := range renderIndexes(tbl) {
			if _, err := t.DB.Exec(ctx, stmt); err != nil {
				return created, fmt.Errorf("create index on %s: %w\n%s", tbl.Name, err, stmt)
			}
		}
		for _, u := range tbl.Uniques {
			if u.SkipReason != "" {
				continue
			}
			exists, err := t.constraintExists(ctx, tbl.Name, u.Name)
			if err != nil {
				return created, err
			}
			if exists {
				continue
			}
			stmt := renderUniqueConstraints(tbl)
			for _, s := range stmt {
				if _, err := t.DB.Exec(ctx, s); err != nil {
					return created, fmt.Errorf("create unique constraint on %s: %w\n%s", tbl.Name, err, s)
				}
			}
		}
	}
	return created, nil
}

// constraintExists reports whether a named constraint is already present.
func (t *Target) constraintExists(ctx context.Context, table, name string) (bool, error) {
	var exists bool
	err := t.DB.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = $1 AND conrelid = $2::regclass)",
		name, quoteIdent(table)).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check constraint %s on %s: %w", name, table, err)
	}
	return exists, nil
}

// checkColumns verifies that a pre-existing target table has every source
// column.
func (t *Target) checkColumns(tbl *Table) error {
	have := t.Columns[tbl.Name]
	var missing []string
	for _, c := range tbl.Columns {
		if _, ok := have[c.Name]; !ok {
			missing = append(missing, c.Name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("target table %s is missing column(s) %s required by the source; refusing to migrate into a narrower schema",
			tbl.Name, strings.Join(missing, ", "))
	}
	return nil
}

// Truncate empties the given tables and restarts their identities.
func (t *Target) Truncate(ctx context.Context, tables []*Table) error {
	if len(tables) == 0 {
		return nil
	}
	names := make([]string, len(tables))
	for i, tbl := range tables {
		names[i] = quoteIdent(tbl.Name)
	}
	stmt := "TRUNCATE TABLE " + strings.Join(names, ", ") + " RESTART IDENTITY"
	if _, err := t.DB.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("truncate: %w", err)
	}
	return nil
}

// EnsureEmpty verifies every target table is empty.
func (t *Target) EnsureEmpty(ctx context.Context, tables []*Table) error {
	var busy []string
	for _, tbl := range tables {
		if !t.TableExists(tbl.Name) {
			continue
		}
		n, err := t.Count(ctx, tbl.Name)
		if err != nil {
			return err
		}
		if n > 0 {
			busy = append(busy, fmt.Sprintf("%s=%d", tbl.Name, n))
		}
	}
	if len(busy) > 0 {
		return fmt.Errorf("target is not empty (%s); rerun with --replace to truncate first, or use --verify-only to compare",
			strings.Join(busy, ", "))
	}
	return nil
}

// Exec runs a statement.
func (t *Target) Exec(ctx context.Context, sql string, args ...any) error {
	_, err := t.DB.Exec(ctx, sql, args...)
	return err
}

// CopyTable copies one table inside a single PostgreSQL transaction.
//
// The whole table is one transaction: a crash mid-table leaves the target
// exactly as it was, so rerunning after --replace is always safe.
func (t *Target) CopyTable(ctx context.Context, tbl *Table, src *Source, batch int, progress func(copied int64)) (int64, error) {
	batch = tbl.BatchSize(batch)
	if batch <= 0 {
		batch = DefaultBatch
	}
	cols := tbl.ColumnNames()
	colTypes := t.Columns[tbl.Name]

	tx, err := t.DB.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin %s: %w", tbl.Name, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The table was empty before this transaction and the whole copy is
	// rollback-able, so relaxing the per-commit flush is safe and fast.
	if _, err := tx.Exec(ctx, "SET LOCAL synchronous_commit = off"); err != nil {
		return 0, err
	}

	var copied int64
	buf := make([][]any, 0, batch)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		srcRows := &sliceSource{rows: buf, cols: colTypes, names: cols}
		n, cerr := tx.CopyFrom(ctx, pgx.Identifier{tbl.Name}, cols, srcRows)
		if cerr != nil {
			return fmt.Errorf("copy %s: %w", tbl.Name, cerr)
		}
		copied += n
		buf = buf[:0]
		if progress != nil {
			progress(copied)
		}
		return nil
	}

	it, err := src.Rows(tbl)
	if err != nil {
		return 0, err
	}
	defer func() { _ = it.Close() }()
	for it.Next() {
		buf = append(buf, it.Row().Values)
		if len(buf) >= batch {
			if err := flush(); err != nil {
				return copied, err
			}
		}
	}
	if err := it.Err(); err != nil {
		return copied, fmt.Errorf("scan %s: %w", tbl.Name, err)
	}
	if err := flush(); err != nil {
		return copied, err
	}
	if err := tx.Commit(ctx); err != nil {
		return copied, fmt.Errorf("commit %s: %w", tbl.Name, err)
	}
	return copied, nil
}

// sliceSource adapts [][]any to pgx.CopyFromSource.
type sliceSource struct {
	rows  [][]any
	cols  map[string]string
	names []string
	idx   int
}

// Next reports whether another row is available.
func (s *sliceSource) Next() bool { return s.idx < len(s.rows) }

// Values returns the next row, coerced to the target column types.
func (s *sliceSource) Values() ([]any, error) {
	vals := s.rows[s.idx]
	s.idx++
	out := make([]any, len(vals))
	for i := range vals {
		out[i] = coerceForColumn(vals[i], s.cols[s.names[i]])
	}
	return out, nil
}

// Err always returns nil: rows are already materialised.
func (s *sliceSource) Err() error { return nil }

// coerceForColumn converts a SQLite value to something the PostgreSQL column
// type accepts. The target design matches the source, but a pre-existing
// table (created by the runtime store) may use BOOLEAN or NUMERIC, so the
// value is adapted by target type here instead of failing the copy.
func coerceForColumn(v any, targetType string) any {
	if v == nil {
		return nil
	}
	switch targetType {
	case "boolean":
		switch x := v.(type) {
		case int64:
			return x != 0
		case float64:
			return x != 0
		case string:
			return x == "1" || strings.EqualFold(x, "true")
		case []byte:
			return len(x) == 1 && x[0] != '0'
		}
		return v
	case "timestamp with time zone", "timestamp without time zone":
		// The source value is the RFC3339 text SQLite stores. pgx's binary
		// COPY path has no encode plan from string to a timestamp OID, so it
		// has to be parsed here; an empty string is the source spelling of
		// "unset" and becomes NULL (the nullable timestamp columns are the
		// only ones that can legally hold it).
		switch x := v.(type) {
		case string:
			if strings.TrimSpace(x) == "" {
				return nil
			}
			if ts, err := time.Parse(time.RFC3339, x); err == nil {
				return ts
			}
			return v
		case []byte:
			if len(x) == 0 {
				return nil
			}
			if ts, err := time.Parse(time.RFC3339, string(x)); err == nil {
				return ts
			}
			return v
		}
		return v
	case "text", "character varying", "character", "json", "jsonb":
		switch x := v.(type) {
		case int64:
			return strconv.FormatInt(x, 10)
		case float64:
			return strconv.FormatFloat(x, 'g', -1, 64)
		case []byte:
			return string(x)
		}
		return v
	case "integer", "bigint", "smallint", "numeric":
		if s, ok := v.(string); ok {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil {
				return n
			}
		}
		return v
	case "double precision", "real":
		switch x := v.(type) {
		case int64:
			return float64(x)
		case string:
			if f, err := strconv.ParseFloat(x, 64); err == nil {
				return f
			}
		}
		return v
	case "bytea":
		if s, ok := v.(string); ok {
			return []byte(s)
		}
		return v
	}
	return v
}

// FixSequenceTo advances the identity sequence of a table that was copied
// with explicit ids, so the next id continues right after the highest id
// actually stored (and after the source's AUTOINCREMENT counter).
func (t *Target) FixSequenceTo(ctx context.Context, tbl *Table, maxID int64) error {
	col, ok := tbl.IdentityColumn()
	if !ok {
		return nil
	}
	value := tbl.SeqValue
	if maxID > value {
		value = maxID
	}
	for _, stmt := range renderSequenceFixup(tbl, col, value) {
		if _, err := t.DB.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("fix sequence for %s: %w", tbl.Name, err)
		}
	}
	return nil
}

// SequenceState is the next id an identity column would hand out.
type SequenceState struct {
	Table string
	Next  int64
}

// NextIdentityIDs reports the next id for every identity table.
func (t *Target) NextIdentityIDs(ctx context.Context, tables []*Table) ([]SequenceState, error) {
	var out []SequenceState
	for _, tbl := range tables {
		col, ok := tbl.IdentityColumn()
		if !ok {
			continue
		}
		var seqName *string
		if err := t.DB.QueryRow(ctx, "SELECT pg_get_serial_sequence($1, $2)", tbl.Name, col).Scan(&seqName); err != nil {
			return nil, err
		}
		if seqName == nil {
			out = append(out, SequenceState{Table: tbl.Name, Next: -1})
			continue
		}
		var last int64
		var called bool
		q := "SELECT last_value, is_called FROM " + *seqName
		if err := t.DB.QueryRow(ctx, q).Scan(&last, &called); err != nil {
			return nil, err
		}
		next := last
		if called {
			next = last + 1
		}
		out = append(out, SequenceState{Table: tbl.Name, Next: next})
	}
	return out, nil
}

// IsPgError reports whether err is a PostgreSQL error with the given code.
func IsPgError(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
