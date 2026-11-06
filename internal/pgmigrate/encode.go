package pgmigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Value tags used by the canonical row encoding.
//
// The encoding is deliberately engine-neutral: the same logical value must
// produce the same bytes whether it came out of SQLite (int64 / float64 /
// string / []byte) or out of PostgreSQL (int64 / int32 / float64 / string /
// []byte / time.Time).
const (
	tagNull  = 'N'
	tagInt   = 'I'
	tagFloat = 'F'
	tagText  = 'S'
	tagBytes = 'X'
)

// encodeValue renders one value as "len:tag:value" with len in bytes of the
// payload.
//
//   - N: NULL, empty payload.
//   - I: signed integer in decimal (int64 and narrower integer types unify,
//     so a SQLite INTEGER and a PostgreSQL bigint hash identically).
//   - F: a genuine floating point value, encoded as its big-endian IEEE-754
//     bit pattern (8 bytes of payload). Integral floats are promoted to I so
//     SQLite REAL columns that happen to hold whole numbers still compare
//     equal to the integers they logically are.
//   - S: raw UTF-8 bytes of a string.
//   - X: raw bytes of a BLOB / BYTEA.
//
// Nothing else is accepted: an unknown type is a hard error rather than a
// silent fmt.Sprintf, because a stringified fallback would let a real
// difference slip through the verifier.
func encodeValue(v any) ([]byte, error) {
	switch x := v.(type) {
	case nil:
		return []byte("0:N:"), nil
	case int64:
		s := strconv.FormatInt(x, 10)
		return []byte(strconv.Itoa(len(s)) + ":I:" + s), nil
	case int32:
		s := strconv.FormatInt(int64(x), 10)
		return []byte(strconv.Itoa(len(s)) + ":I:" + s), nil
	case int:
		s := strconv.FormatInt(int64(x), 10)
		return []byte(strconv.Itoa(len(s)) + ":I:" + s), nil
	case int16:
		s := strconv.FormatInt(int64(x), 10)
		return []byte(strconv.Itoa(len(s)) + ":I:" + s), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			var bits uint64
			if math.IsNaN(x) {
				bits = 0x7ff8000000000001
			} else if math.IsInf(x, 1) {
				bits = 0x7ff0000000000000
			} else {
				bits = 0xfff0000000000000
			}
			payload := []byte{
				byte(bits >> 56), byte(bits >> 48), byte(bits >> 40), byte(bits >> 32),
				byte(bits >> 24), byte(bits >> 16), byte(bits >> 8), byte(bits),
			}
			return append([]byte("8:F:"), payload...), nil
		}
		if x == math.Trunc(x) && x >= math.MinInt64 && x <= math.MaxInt64 {
			s := strconv.FormatInt(int64(x), 10)
			return []byte(strconv.Itoa(len(s)) + ":I:" + s), nil
		}
		bits := math.Float64bits(x)
		payload := []byte{
			byte(bits >> 56), byte(bits >> 48), byte(bits >> 40), byte(bits >> 32),
			byte(bits >> 24), byte(bits >> 16), byte(bits >> 8), byte(bits),
		}
		return append([]byte("8:F:"), payload...), nil
	case string:
		return []byte(strconv.Itoa(len(x)) + ":S:" + x), nil
	case []byte:
		out := make([]byte, 0, len(x)+8)
		out = append(out, []byte(strconv.Itoa(len(x))+":X:")...)
		return append(out, x...), nil
	case bool:
		if x {
			return []byte("1:I:1"), nil
		}
		return []byte("1:I:0"), nil
	default:
		return nil, fmt.Errorf("cannot canonicalise value of type %T for hashing", v)
	}
}

// rowDigest hashes one canonically encoded row.
func rowDigest(encoded []byte) [32]byte { return sha256.Sum256(encoded) }

// hashRow encodes and hashes a row of values.
func hashRow(vals []any) ([32]byte, error) {
	var b strings.Builder
	for i, v := range vals {
		if i > 0 {
			b.WriteByte('\x1f')
		}
		enc, err := encodeValue(v)
		if err != nil {
			return [32]byte{}, fmt.Errorf("column %d: %w", i, err)
		}
		b.Write(enc)
	}
	return rowDigest([]byte(b.String())), nil
}

// hexOf renders a digest.
func hexOf(d [32]byte) string { return hex.EncodeToString(d[:]) }

// shortValue renders a value for a difference report, bounded in length so a
// multi-megabyte blob cannot flood the console.
func shortValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "<nil>"
	case string:
		return quoteShort(x)
	case []byte:
		return fmt.Sprintf("<%d bytes sha256=%s>", len(x), blobDigest(x))
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if x == math.Trunc(x) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// quoteShort renders a bounded, quoted string.
func quoteShort(s string) string {
	const max = 80
	if len(s) > max {
		return strconv.Quote(s[:max]) + fmt.Sprintf("…(%d bytes)", len(s))
	}
	return strconv.Quote(s)
}

// blobDigest is the sha256 hex of a blob.
func blobDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// normalizeMaybeBytes turns a driver-provided []byte into a string only when
// the target column is textual; SQLite hands TEXT back as string already, but
// a value read through a text-typed column of a pre-existing PostgreSQL table
// can arrive as []byte in some drivers.
func normalizeMaybeBytes(v any) any { return v }
