package pgmigrate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// RowRef is one hashed row, keyed by its primary key values.
type RowRef struct {
	PK     []any
	Digest [32]byte
}

// TableReport is the verification result for one table.
type TableReport struct {
	Table        string
	ExpectedRows int64
	ActualRows   int64
	ExpectedHash string
	ActualHash   string
	OK           bool
	// Diff is the first concrete difference found, empty when OK.
	Diff string
	// Note carries extra per-table evidence (media per-row sha256 checks,
	// settings key comparison, sequence fixups).
	Note string
}

// Report is the full verification result.
type Report struct {
	Tables []TableReport
	OK     bool
	// SequenceNote describes the identity state after the copy.
	SequenceNote string
	// SequenceProblems lists identity sequences that would hand out an id
	// that is already taken (or the wrong one for memories).
	SequenceProblems []string
}

// Failed returns the tables that did not match.
func (r *Report) Failed() []TableReport {
	var out []TableReport
	for _, t := range r.Tables {
		if !t.OK {
			out = append(out, t)
		}
	}
	return out
}

// Verify compares every table on both sides: row count, ordered row-hash list
// (primary key order) and the aggregate hash of those row hashes. The first
// difference is resolved down to the offending primary key and column.
func Verify(ctx context.Context, src *Source, tgt *Target, tables []*Table) (*Report, error) {
	rep := &Report{OK: true}
	for _, tbl := range tables {
		tr, err := verifyTable(ctx, src, tgt, tbl)
		if err != nil {
			return nil, err
		}
		if !tr.OK {
			rep.OK = false
		}
		rep.Tables = append(rep.Tables, tr)
	}
	// Sequence sanity is part of zero-loss: a copied table with a stale
	// identity would hand out an id that already exists.
	seqNote, problems, err := verifySequences(ctx, tgt, tables)
	if err != nil {
		return nil, err
	}
	rep.SequenceNote = seqNote
	rep.SequenceProblems = problems
	if len(problems) > 0 {
		rep.OK = false
	}
	return rep, nil
}

// verifyTable hashes both sides and compares them row by row.
func verifyTable(ctx context.Context, src *Source, tgt *Target, tbl *Table) (TableReport, error) {
	tr := TableReport{Table: tbl.Name}

	srcRefs, srcAgg, err := hashSource(src, tbl)
	if err != nil {
		return tr, err
	}
	tgtRefs, tgtAgg, err := hashTarget(ctx, tgt, tbl)
	if err != nil {
		return tr, err
	}

	tr.ExpectedRows = int64(len(srcRefs))
	tr.ActualRows = int64(len(tgtRefs))
	tr.ExpectedHash = srcAgg
	tr.ActualHash = tgtAgg
	tr.OK = srcAgg == tgtAgg && len(srcRefs) == len(tgtRefs)

	if !tr.OK {
		tr.Diff, err = describeDifference(ctx, src, tgt, tbl, srcRefs, tgtRefs)
		if err != nil {
			return tr, err
		}
	}

	switch tbl.Name {
	case MediaTable:
		note, err := verifyMedia(ctx, src, tgt, tbl)
		if err != nil {
			return tr, err
		}
		tr.Note = note
		if note != "" && !strings.HasPrefix(note, "ok") {
			tr.OK = false
			if tr.Diff == "" {
				tr.Diff = note
			}
		}
	case SettingsTable:
		note, ok, err := verifySettings(ctx, src, tgt, tbl)
		if err != nil {
			return tr, err
		}
		tr.Note = note
		if !ok {
			tr.OK = false
			if tr.Diff == "" {
				tr.Diff = note
			}
		}
	}
	return tr, nil
}

// hashSource streams the source table in primary-key order.
func hashSource(src *Source, tbl *Table) ([]RowRef, string, error) {
	it, err := src.Rows(tbl)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = it.Close() }()

	pkIdx := pkIndexes(tbl)
	agg := sha256.New()
	var refs []RowRef
	for it.Next() {
		row := it.Row()
		d, herr := hashRow(row.Values)
		if herr != nil {
			return nil, "", fmt.Errorf("%s: %w", tbl.Name, herr)
		}
		agg.Write(d[:])
		refs = append(refs, RowRef{PK: pick(row.Values, pkIdx), Digest: d})
	}
	if err := it.Err(); err != nil {
		return nil, "", fmt.Errorf("scan %s: %w", tbl.Name, err)
	}
	return refs, hex.EncodeToString(agg.Sum(nil)), nil
}

// targetSelectList renders the SELECT list for a target table. A timestamptz
// column is projected to the RFC3339 text the SQLite side stores, so the two
// sides hash like for like: a native timestamp would otherwise scan back as
// time.Time (which has no canonical encoding), and NULL would not equal the
// source spelling of "unset" (the empty string).
//
// The projection is conditional on the actual target column type, so a
// database whose schema predates the timestamptz change still verifies
// against its TEXT columns instead of failing on an unknown function.
func targetSelectList(tgt *Target, tbl *Table) string {
	types := tgt.Columns[tbl.Name]
	items := make([]string, len(tbl.Columns))
	for i, c := range tbl.Columns {
		if types[c.Name] == "timestamp with time zone" {
			items[i] = targetTimestampProjection(tbl.Name, c.Name)
		} else {
			items[i] = quoteIdent(c.Name)
		}
	}
	return strings.Join(items, ", ")
}

// targetTimestampProjection renders one timestamptz column as the RFC3339
// text with a +08:00 suffix the application reads and the SQLite source
// stores. Asia/Shanghai is a fixed +08:00 offset, so appending the suffix is
// exact; to_char's OF format would print just "+08" (no minutes). A NULL
// (the target spelling of "unset") projects back to ”.
func targetTimestampProjection(table, column string) string {
	expr := "to_char(" + quoteIdent(column) +
		" AT TIME ZONE 'Asia/Shanghai', 'YYYY-MM-DD\"T\"HH24:MI:SS') || '+08:00'"
	if nullableTimestampColumns[table+"."+column] {
		return "COALESCE(" + expr + ", '')"
	}
	return expr
}

// hashTarget streams the target table in primary-key order.
func hashTarget(ctx context.Context, tgt *Target, tbl *Table) ([]RowRef, string, error) {
	q := "SELECT " + targetSelectList(tgt, tbl) + " FROM " + quoteIdent(tbl.Name) + orderByClause(tbl.PK)
	rows, err := tgt.DB.Query(ctx, q)
	if err != nil {
		return nil, "", fmt.Errorf("scan target %s: %w", tbl.Name, err)
	}
	defer rows.Close()

	pkIdx := pkIndexes(tbl)
	agg := sha256.New()
	var refs []RowRef
	for rows.Next() {
		vals, verr := rows.Values()
		if verr != nil {
			return nil, "", fmt.Errorf("scan target %s: %w", tbl.Name, verr)
		}
		d, herr := hashRow(vals)
		if herr != nil {
			return nil, "", fmt.Errorf("%s (target): %w", tbl.Name, herr)
		}
		agg.Write(d[:])
		refs = append(refs, RowRef{PK: pick(vals, pkIdx), Digest: d})
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("scan target %s: %w", tbl.Name, err)
	}
	return refs, hex.EncodeToString(agg.Sum(nil)), nil
}

// describeDifference finds the first differing row and explains it.
func describeDifference(ctx context.Context, src *Source, tgt *Target, tbl *Table, srcRefs, tgtRefs []RowRef) (string, error) {
	n := len(srcRefs)
	if len(tgtRefs) < n {
		n = len(tgtRefs)
	}
	for i := 0; i < n; i++ {
		if bytes.Equal(srcRefs[i].Digest[:], tgtRefs[i].Digest[:]) {
			continue
		}
		pk := describePK(tbl.PK, srcRefs[i].PK)
		detail, err := rowDifference(ctx, src, tgt, tbl, srcRefs[i].PK)
		if err != nil {
			return fmt.Sprintf("row #%d %s differs (column detail unavailable: %v)", i+1, pk, err), nil
		}
		return fmt.Sprintf("row #%d %s: %s", i+1, pk, detail), nil
	}
	if len(srcRefs) > len(tgtRefs) {
		return fmt.Sprintf("target is missing %d row(s); first missing %s",
			len(srcRefs)-len(tgtRefs), describePK(tbl.PK, srcRefs[n].PK)), nil
	}
	if len(tgtRefs) > len(srcRefs) {
		return fmt.Sprintf("target has %d extra row(s); first extra %s",
			len(tgtRefs)-len(srcRefs), describePK(tbl.PK, tgtRefs[n].PK)), nil
	}
	return "hashes differ but no row-level difference was located", nil
}

// rowDifference fetches one row from each side and reports the first column
// whose canonical encoding differs.
func rowDifference(ctx context.Context, src *Source, tgt *Target, tbl *Table, pk []any) (string, error) {
	cols := tbl.ColumnNames()
	where := make([]string, len(tbl.PK))
	for i, name := range tbl.PK {
		where[i] = quoteIdent(name) + " = $" + fmt.Sprint(i+1)
	}
	whereSQL := strings.Join(where, " AND ")

	srcVals, err := sourceRowByPK(src, tbl, cols, pk, whereSQL)
	if err != nil {
		return "", err
	}
	tgtVals, err := targetRowByPK(ctx, tgt, tbl, cols, pk, whereSQL)
	if err != nil {
		return "", err
	}
	for i := range cols {
		a, err := encodeValue(srcVals[i])
		if err != nil {
			return "", err
		}
		b, err := encodeValue(tgtVals[i])
		if err != nil {
			return "", err
		}
		if !bytes.Equal(a, b) {
			return fmt.Sprintf("column %s: expected %s got %s", cols[i], shortValue(srcVals[i]), shortValue(tgtVals[i])), nil
		}
	}
	return "row encodings differ but every column matches (ordering bug?)", nil
}

// sourceRowByPK reads one row from SQLite by primary key.
func sourceRowByPK(src *Source, tbl *Table, cols []string, pk []any, _ string) ([]any, error) {
	where := make([]string, len(tbl.PK))
	args := make([]any, len(tbl.PK))
	for i, name := range tbl.PK {
		where[i] = quoteIdent(name) + " = ?"
		args[i] = pk[i]
	}
	q := "SELECT " + qualifiedColumns(cols) + " FROM " + quoteIdent(tbl.Name) + " WHERE " + strings.Join(where, " AND ")
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := src.db.QueryRow(q, args...).Scan(ptrs...); err != nil {
		return nil, fmt.Errorf("read source row: %w", err)
	}
	return vals, nil
}

// targetRowByPK reads one row from PostgreSQL by primary key.
func targetRowByPK(ctx context.Context, tgt *Target, tbl *Table, _ []string, pk []any, whereSQL string) ([]any, error) {
	q := "SELECT " + targetSelectList(tgt, tbl) + " FROM " + quoteIdent(tbl.Name) + " WHERE " + whereSQL
	args := make([]any, len(pk))
	copy(args, pk)
	rows, err := tgt.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("read target row: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("row not found on target")
	}
	vals, err := rows.Values()
	if err != nil {
		return nil, err
	}
	return vals, nil
}

// pkIndexes maps primary key column names to their position in the row.
func pkIndexes(tbl *Table) []int {
	idx := make([]int, 0, len(tbl.PK))
	for _, name := range tbl.PK {
		for i, c := range tbl.Columns {
			if c.Name == name {
				idx = append(idx, i)
				break
			}
		}
	}
	return idx
}

// pick selects the given indexes from a row.
func pick(vals []any, idx []int) []any {
	out := make([]any, len(idx))
	for i, j := range idx {
		out[i] = vals[j]
	}
	return out
}

// verifySequences checks that every identity column continues exactly where
// the source would: after GREATEST(max(pk), source AUTOINCREMENT counter). An
// empty memories table whose source counter is 1 therefore hands out id 2 next;
// three rows with a counter of 4 hand out 5. No table needs a special case.
func verifySequences(ctx context.Context, tgt *Target, tables []*Table) (string, []string, error) {
	var problems []string
	var notes []string
	for _, tbl := range tables {
		col, ok := tbl.IdentityColumn()
		if !ok {
			continue
		}
		states, err := tgt.NextIdentityIDs(ctx, []*Table{tbl})
		if err != nil {
			return "", nil, err
		}
		if len(states) != 1 {
			continue
		}
		next := states[0].Next
		var maxID int64
		if err := tgt.DB.QueryRow(ctx, "SELECT COALESCE(MAX("+quoteIdent(col)+"), 0) FROM "+quoteIdent(tbl.Name)).Scan(&maxID); err != nil {
			return "", nil, err
		}
		// Same formula the copy applies and the dry-run plan prints, so the
		// three can never disagree.
		want := nextIDFromState(maxID, tbl.SeqValue)
		if next != want {
			problems = append(problems, fmt.Sprintf("%s next id %d != %d", tbl.Name, next, want))
		}
		notes = append(notes, fmt.Sprintf("%s=%d", tbl.Name, next))
	}
	return strings.Join(notes, ", "), problems, nil
}

// verifyMedia recomputes sha256(data) for every media row on both sides and
// checks it against the row's id and sha256 columns.
func verifyMedia(ctx context.Context, src *Source, tgt *Target, tbl *Table) (string, error) {
	idx := colIndexes(tbl, []string{"id", "sha256", "data"})
	if idx == nil {
		return "", nil
	}
	srcBad, srcTotal, srcAgg, err := mediaSide(src, tbl, idx)
	if err != nil {
		return "", err
	}
	tgtBad, tgtTotal, tgtAgg, err := mediaSideTarget(ctx, tgt, tbl, idx)
	if err != nil {
		return "", err
	}
	if srcBad > 0 || tgtBad > 0 || srcAgg != tgtAgg || srcTotal != tgtTotal {
		return fmt.Sprintf("media sha256 mismatch: source %d/%d rows bad agg=%s; target %d/%d rows bad agg=%s",
			srcBad, srcTotal, srcAgg, tgtBad, tgtTotal, tgtAgg), nil
	}
	return fmt.Sprintf("ok: %d rows, sha256(data)==id for all, per-row digest aggregate=%s", srcTotal, srcAgg), nil
}

// mediaSide walks the source media table.
func mediaSide(src *Source, tbl *Table, idx []int) (bad, total int, agg string, err error) {
	it, err := src.Rows(tbl)
	if err != nil {
		return 0, 0, "", err
	}
	defer func() { _ = it.Close() }()
	h := sha256.New()
	for it.Next() {
		row := it.Row()
		total++
		data, _ := row.Values[idx[2]].([]byte)
		sum := sha256.Sum256(data)
		h.Write(sum[:])
		id, _ := row.Values[idx[0]].(string)
		declared, _ := row.Values[idx[1]].(string)
		if hex.EncodeToString(sum[:]) != id || id != declared {
			bad++
		}
	}
	if err := it.Err(); err != nil {
		return 0, 0, "", err
	}
	return bad, total, hex.EncodeToString(h.Sum(nil)), nil
}

// mediaSideTarget walks the target media table.
func mediaSideTarget(ctx context.Context, tgt *Target, tbl *Table, idx []int) (bad, total int, agg string, err error) {
	h := sha256.New()
	rows, err := tgt.DB.Query(ctx, "SELECT "+
		quoteIdent(tbl.Columns[idx[0]].Name)+", "+
		quoteIdent(tbl.Columns[idx[1]].Name)+", "+
		quoteIdent(tbl.Columns[idx[2]].Name)+
		" FROM "+quoteIdent(tbl.Name)+orderByClause(tbl.PK))
	if err != nil {
		return 0, 0, "", fmt.Errorf("scan target media: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, declared string
		var data []byte
		if err := rows.Scan(&id, &declared, &data); err != nil {
			return 0, 0, "", err
		}
		total++
		sum := sha256.Sum256(data)
		h.Write(sum[:])
		if hex.EncodeToString(sum[:]) != id || id != declared {
			bad++
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, "", err
	}
	return bad, total, hex.EncodeToString(h.Sum(nil)), nil
}

// verifySettings compares every key with JSON semantic equality and reports
// keys that only exist on one side or whose JSON meaning differs.
func verifySettings(ctx context.Context, src *Source, tgt *Target, tbl *Table) (string, bool, error) {
	srcVals, err := settingsMapSource(src, tbl)
	if err != nil {
		return "", false, err
	}
	tgtVals, err := settingsMapTarget(ctx, tgt, tbl)
	if err != nil {
		return "", false, err
	}

	var missing, extra, diff []string
	for k, sv := range srcVals {
		tv, ok := tgtVals[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		if !jsonEqual(sv, tv) {
			diff = append(diff, k)
		}
	}
	for k := range tgtVals {
		if _, ok := srcVals[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	sort.Strings(diff)
	if len(missing)+len(extra)+len(diff) > 0 {
		var parts []string
		if len(missing) > 0 {
			parts = append(parts, "missing on target: "+strings.Join(missing, ","))
		}
		if len(extra) > 0 {
			parts = append(parts, "extra on target: "+strings.Join(extra, ","))
		}
		if len(diff) > 0 {
			parts = append(parts, "JSON differs: "+strings.Join(diff, ","))
		}
		return "settings: " + strings.Join(parts, "; "), false, nil
	}
	return fmt.Sprintf("ok: %d keys, JSON semantic equality for every key", len(srcVals)), true, nil
}

// settingsMapSource reads key -> raw JSON from SQLite.
func settingsMapSource(src *Source, tbl *Table) (map[string]string, error) {
	idx := colIndexes(tbl, []string{"k", "v"})
	if idx == nil {
		return nil, fmt.Errorf("settings table has no k/v columns")
	}
	rows, err := src.db.Query("SELECT " + quoteIdent("k") + ", " + quoteIdent("v") + " FROM " + quoteIdent(tbl.Name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// settingsMapTarget reads key -> raw JSON from PostgreSQL.
func settingsMapTarget(ctx context.Context, tgt *Target, tbl *Table) (map[string]string, error) {
	rows, err := tgt.DB.Query(ctx, "SELECT "+quoteIdent("k")+", "+quoteIdent("v")+" FROM "+quoteIdent(tbl.Name))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// jsonEqual compares two JSON documents semantically. Non-JSON values are
// compared as raw strings.
func jsonEqual(a, b string) bool {
	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		return a == b
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		return false
	}
	return reflect.DeepEqual(av, bv)
}

// colIndexes returns the positions of the named columns, or nil when any is
// missing.
func colIndexes(tbl *Table, names []string) []int {
	out := make([]int, len(names))
	for i, n := range names {
		found := -1
		for j, c := range tbl.Columns {
			if c.Name == n {
				found = j
				break
			}
		}
		if found < 0 {
			return nil
		}
		out[i] = found
	}
	return out
}
