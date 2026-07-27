package store

import (
	"database/sql"
	_ "embed"
	"fmt"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

// rfc3339 is the canonical time format used for all TEXT time columns.
const rfc3339 = time.RFC3339Nano

// nanoKeySQL builds a SQL expression that normalizes a UTC RFC 3339 timestamp
// column to a fixed-width nanosecond representation. time.RFC3339Nano omits
// trailing fractional zeros, so comparing raw TEXT values would sort an
// exact-second value after a fractional value in the same second. Callers pass
// the column name (e.g. "created_at", "processed_at").
func nanoKeySQL(column string) string {
	return `(CASE
	WHEN substr(` + column + `, 20, 1) = 'Z'
		THEN substr(` + column + `, 1, 19) || '.000000000Z'
	ELSE substr(` + column + `, 1, 20) ||
		substr(substr(` + column + `, 21, length(` + column + `) - 21) || '000000000', 1, 9) || 'Z'
	END)`
}

// DB wraps *sql.DB with the current schema applied.
type DB struct{ *sql.DB }

// Open opens (or creates) a SQLite database at dsn, applies PRAGMAs, and creates
// the current schema. During the pre-release phase, old development schemas are
// intentionally not migrated; rebuild the local database when the schema
// changes.
func Open(dsn string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	for _, p := range []string{
		`PRAGMA foreign_keys=ON`,
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
	} {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, err
		}
	}
	if _, err := sqlDB.Exec(schema); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return &DB{sqlDB}, nil
}

var memSeq atomic.Int64

// OpenMemory opens a unique named in-memory SQLite database suitable for tests.
// Each call gets an isolated database so tests do not share state.
func OpenMemory() (*DB, error) {
	n := memSeq.Add(1)
	return Open(fmt.Sprintf("file:memdb%d?mode=memory&cache=shared", n))
}

// parseRFC3339 parses an RFC3339Nano string stored in SQLite TEXT columns.
func parseRFC3339(s string) (time.Time, error) {
	t, err := time.Parse(rfc3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: parse time %q: %w", s, err)
	}
	return t, nil
}

// timeVal formats t as an RFC3339Nano string for storage in TEXT columns.
func timeVal(t time.Time) string {
	return t.UTC().Format(rfc3339)
}

// nullableTime returns the RFC3339Nano string for t, or nil if t is nil.
func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(rfc3339)
}
