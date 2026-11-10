package pgmigrate

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // SQLite driver used by the migrator
)

// Source is an opened SQLite database plus its discovered schema.
type Source struct {
	Path   string
	db     *sql.DB
	Tables []*Table // in TableOrder order
	Extra  []*Table // source tables that are not in TableOrder
}

// OpenSource opens the SQLite database read-only and discovers its schema.
//
// The open is strictly read-only: the migrator must never be able to modify
// the production database it is copying from.
func OpenSource(path string) (*Source, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("source database %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open source database: %w", err)
	}
	// A single connection keeps PRAGMA state and avoids WAL reopen races.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open source database: %w", err)
	}
	s := &Source{Path: path, db: db}
	if err := s.discover(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the source handle.
func (s *Source) Close() error { return s.db.Close() }

// TableNames returns the tables in copy order, extras last.
func (s *Source) TableNames() []string {
	names := make([]string, 0, len(s.Tables)+len(s.Extra))
	for _, t := range s.Tables {
		names = append(names, t.Name)
	}
	for _, t := range s.Extra {
		names = append(names, t.Name)
	}
	return names
}

// AllTables returns the ordered tables plus the extras.
func (s *Source) AllTables() []*Table {
	out := make([]*Table, 0, len(s.Tables)+len(s.Extra))
	out = append(out, s.Tables...)
	return append(out, s.Extra...)
}

// optionalTables are tables added after the original migration contract. A
// pre-upgrade SQLite file does not have them yet; the runtime store creates them
// on the target when it next opens, so the copy skips their absence.
var optionalTables = map[string]bool{
	"commitments":    true,
	"human_status":   true,
	"affect_states":  true,
	"affect_events":  true,
	"affect_history": true,
}

// discover reads sqlite_master, applies TableOrder and loads every column.
func (s *Source) discover() error {
	rows, err := s.db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return fmt.Errorf("list source tables: %w", err)
	}
	present := map[string]bool{}
	var all []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return err
		}
		present[name] = true
		all = append(all, name)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	for _, name := range TableOrder {
		if !present[name] {
			// Tables introduced after a deployment's schema was frozen (e.g.
			// commitments / human_status) are absent from a pre-upgrade SQLite
			// file. The runtime store creates them when it next opens the target,
			// so the copy may skip them instead of refusing to migrate at all.
			if optionalTables[name] {
				continue
			}
			return fmt.Errorf("source database is missing table %q: the schema does not match the expected bot-harness schema (found: %s)",
				name, strings.Join(all, ", "))
		}
	}
	known := map[string]bool{}
	for _, name := range TableOrder {
		known[name] = true
	}

	for _, name := range TableOrder {
		if !present[name] {
			continue
		}
		t, err := s.loadTable(name)
		if err != nil {
			return err
		}
		s.Tables = append(s.Tables, t)
	}
	for _, name := range all {
		if known[name] {
			continue
		}
		t, err := s.loadTable(name)
		if err != nil {
			return err
		}
		s.Extra = append(s.Extra, t)
	}
	return nil
}

// loadTable reads PRAGMA table_info plus the AUTOINCREMENT metadata.
func (s *Source) loadTable(name string) (*Table, error) {
	t := &Table{Name: name}
	rows, err := s.db.Query("PRAGMA table_info(" + quoteIdent(name) + ")")
	if err != nil {
		return nil, fmt.Errorf("table_info(%s): %w", name, err)
	}
	defer func() { _ = rows.Close() }()

	type pkCol struct {
		name  string
		order int
	}
	var pks []pkCol
	for rows.Next() {
		var (
			cid     int
			colName string
			decl    string
			notNull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &colName, &decl, &notNull, &dflt, &pk); err != nil {
			return nil, fmt.Errorf("table_info(%s): %w", name, err)
		}
		col := Column{Name: colName, DeclType: decl, NotNull: notNull != 0, PKOrder: pk}
		if dflt.Valid {
			col.HasDefault = true
			col.DefaultExpr = dflt.String
		}
		t.Columns = append(t.Columns, col)
		if pk > 0 {
			pks = append(pks, pkCol{name: colName, order: pk})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(t.Columns) == 0 {
		return nil, fmt.Errorf("source table %q has no columns", name)
	}
	// PRAGMA table_info reports pk as a 1-based key position.
	for order := 1; order <= len(pks); order++ {
		for _, p := range pks {
			if p.order == order {
				t.PK = append(t.PK, p.name)
			}
		}
	}
	if len(t.PK) == 0 {
		// No declared key: fall back to a full-row ordering so the copy and
		// the hash are still deterministic.
		t.PK = t.ColumnNames()
	}

	var ddl string
	err = s.db.QueryRow("SELECT COALESCE(sql,'') FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&ddl)
	if err != nil && err != sql.ErrNoRows {
		return nil, fmt.Errorf("read sqlite_master for %s: %w", name, err)
	}
	t.AutoIncrement = strings.Contains(strings.ToUpper(ddl), "AUTOINCREMENT")

	if t.AutoIncrement {
		var seq sql.NullInt64
		// sqlite_sequence only exists once an AUTOINCREMENT table was written.
		// A read-only connection to a database that never wrote to an
		// AUTOINCREMENT table has no sqlite_sequence at all, which SQLite
		// reports as "no such table"; both cases mean "no counter yet".
		err := s.db.QueryRow("SELECT seq FROM sqlite_sequence WHERE name=?", name).Scan(&seq)
		switch {
		case err == nil && seq.Valid:
			t.HasSeq, t.SeqValue = true, seq.Int64
		case err != nil && err != sql.ErrNoRows && !isMissingTable(err):
			return nil, fmt.Errorf("read sqlite_sequence for %s: %w", name, err)
		}
	}
	if err := s.loadIndexes(t); err != nil {
		return nil, err
	}
	return t, nil
}

// isMissingTable reports whether err is SQLite's "no such table".
func isMissingTable(err error) bool {
	return err != nil && strings.Contains(err.Error(), "no such table")
}

// syntheticPrefix marks index names synthesized for UNIQUE constraints, which
// SQLite stores as sqlite_autoindex_* and therefore cannot be replayed by name.
const syntheticPrefix = "sqlite_autoindex_"

// uniqueConstraintName builds the name PostgreSQL itself would pick for a
// UNIQUE constraint, so a schema created by this tool and one created by the
// runtime store (which writes the constraint inline) share the same object.
func uniqueConstraintName(table string, columns []IndexColumn) string {
	names := make([]string, len(columns))
	for i, c := range columns {
		names[i] = c.Name
	}
	return table + "_" + strings.Join(names, "_") + "_key"
}

// loadIndexes reads PRAGMA index_list / index_xinfo for one table and splits
// the result into user indexes and UNIQUE constraints.
func (s *Source) loadIndexes(t *Table) error {
	rows, err := s.db.Query("PRAGMA index_list(" + quoteIdent(t.Name) + ")")
	if err != nil {
		return fmt.Errorf("index_list(%s): %w", t.Name, err)
	}
	defer func() { _ = rows.Close() }()

	type rawIndex struct {
		name    string
		unique  bool
		origin  string
		partial bool
	}
	var raws []rawIndex
	for rows.Next() {
		var (
			seq     int
			name    string
			unique  int
			origin  string
			partial int
		)
		if err := rows.Scan(&seq, &name, &unique, &origin, &partial); err != nil {
			return fmt.Errorf("index_list(%s): %w", t.Name, err)
		}
		raws = append(raws, rawIndex{name: name, unique: unique != 0, origin: origin, partial: partial != 0})
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, raw := range raws {
		// origin: 'c' = CREATE INDEX, 'u' = UNIQUE constraint, 'pk' = the
		// primary key (already rendered as the table's PRIMARY KEY clause).
		if raw.origin == "pk" {
			continue
		}
		idx := Index{Name: raw.name, Unique: raw.unique}
		cols, werr, cerr := s.indexColumns(raw.name)
		if cerr != nil {
			return cerr
		}
		idx.Columns = cols
		idx.Where = werr
		if len(idx.Columns) == 0 {
			idx.SkipReason = "index has no key columns"
		}
		if raw.origin == "u" {
			// SQLite auto-names UNIQUE constraints (sqlite_autoindex_*), which
			// carries no meaning; PostgreSQL needs a deterministic name, so
			// use the one PostgreSQL itself would generate for the same
			// constraint written inline.
			idx.Name = uniqueConstraintName(t.Name, idx.Columns)
			t.Uniques = append(t.Uniques, idx)
			continue
		}
		t.Indexes = append(t.Indexes, idx)
	}
	return nil
}

// indexColumns reads the key columns and the partial-index predicate.
func (s *Source) indexColumns(indexName string) ([]IndexColumn, string, error) {
	rows, err := s.db.Query("PRAGMA index_xinfo(" + quoteIdent(indexName) + ")")
	if err != nil {
		return nil, "", fmt.Errorf("index_xinfo(%s): %w", indexName, err)
	}
	defer func() { _ = rows.Close() }()
	var cols []IndexColumn
	for rows.Next() {
		var (
			seqno int
			cid   int
			name  sql.NullString
			desc  int
			coll  sql.NullString
			isKey int
		)
		if err := rows.Scan(&seqno, &cid, &name, &desc, &coll, &isKey); err != nil {
			return nil, "", err
		}
		// isKey=0 marks the rowid payload column of a covering index.
		if isKey != 1 || !name.Valid {
			continue
		}
		cols = append(cols, IndexColumn{Name: name.String, Desc: desc != 0})
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var where sql.NullString
	err = s.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='index' AND name=?", indexName).Scan(&where)
	if err != nil && err != sql.ErrNoRows {
		return nil, "", err
	}
	predicate := ""
	if where.Valid {
		if i := strings.Index(strings.ToUpper(where.String), " WHERE "); i >= 0 {
			predicate = strings.TrimSpace(where.String[i+len(" WHERE "):])
		}
	}
	return cols, predicate, nil
}

// Count returns the number of rows in a source table.
func (s *Source) Count(t *Table) (int64, error) {
	var n int64
	err := s.db.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(t.Name)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count %s: %w", t.Name, err)
	}
	return n, nil
}

// Row is one scanned source row, in column discovery order.
type Row struct {
	Values []any
}

// Rows streams the table in primary-key order.
func (s *Source) Rows(t *Table) (*RowIter, error) {
	q := "SELECT " + qualifiedColumns(t.ColumnNames()) + " FROM " + quoteIdent(t.Name) + orderByClause(t.PK)
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", t.Name, err)
	}
	return &RowIter{rows: rows, n: len(t.Columns)}, nil
}

// RowIter iterates source rows.
type RowIter struct {
	rows *sql.Rows
	n    int
	cur  Row
	err  error
}

// Next advances; it returns false at EOF.
func (it *RowIter) Next() bool {
	if !it.rows.Next() {
		it.err = it.rows.Err()
		return false
	}
	vals := make([]any, it.n)
	ptrs := make([]any, it.n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := it.rows.Scan(ptrs...); err != nil {
		it.err = err
		return false
	}
	it.cur = Row{Values: vals}
	return true
}

// Row returns the current row.
func (it *RowIter) Row() Row { return it.cur }

// Err reports a scan error, if any.
func (it *RowIter) Err() error { return it.err }

// Close releases the cursor.
func (it *RowIter) Close() error { return it.rows.Close() }

// CountRows returns the row count of a source table.
func CountRows(db *sql.DB, table string) (int64, error) {
	var n int64
	if err := db.QueryRow("SELECT COUNT(*) FROM " + quoteIdent(table)).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// formatInt is a tiny helper for progress lines.
func formatInt(n int64) string { return strconv.FormatInt(n, 10) }
