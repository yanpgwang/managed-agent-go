package pg

import (
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// resolvePoolConfig mirrors what Pool does before it dials, so the resolved
// bounds can be asserted without a database.
func resolvePoolConfig(t *testing.T, databaseURL string, cfg PoolConfig) *pgxpool.Config {
	t.Helper()
	parsed, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("ParseConfig(%q): %v", databaseURL, err)
	}
	applyPoolConfig(parsed, cfg.withDefaults(), connStringParams(databaseURL))
	return parsed
}

const testDatabaseURL = "postgres://u:p@localhost:5432/db?sslmode=disable"

// TestPool_AppliesDefaultBounds asserts the pool is bounded even when the
// caller supplies nothing: before this, pgxpool's own defaults left lifetime,
// idle time, minimum connections, and statement timeout effectively unmanaged.
func TestPool_AppliesDefaultBounds(t *testing.T) {
	cfg := resolvePoolConfig(t, testDatabaseURL, PoolConfig{})
	if cfg.MaxConns != DefaultMaxConns {
		t.Errorf("MaxConns = %d, want %d", cfg.MaxConns, DefaultMaxConns)
	}
	if cfg.MinConns != DefaultMinConns {
		t.Errorf("MinConns = %d, want %d", cfg.MinConns, DefaultMinConns)
	}
	if cfg.MaxConnLifetime != DefaultMaxConnLifetime {
		t.Errorf("MaxConnLifetime = %v, want %v", cfg.MaxConnLifetime, DefaultMaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != DefaultMaxConnIdleTime {
		t.Errorf("MaxConnIdleTime = %v, want %v", cfg.MaxConnIdleTime, DefaultMaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != DefaultHealthCheckPeriod {
		t.Errorf("HealthCheckPeriod = %v, want %v", cfg.HealthCheckPeriod, DefaultHealthCheckPeriod)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "30000" {
		t.Errorf("statement_timeout = %q, want 30000 (ms)", got)
	}
}

// TestPool_AppliesConfiguredBounds asserts an explicit PoolConfig reaches the
// pgx configuration rather than being silently ignored.
func TestPool_AppliesConfiguredBounds(t *testing.T) {
	cfg := resolvePoolConfig(t, testDatabaseURL, PoolConfig{
		MaxConns:          7,
		MinConns:          3,
		MaxConnLifetime:   11 * time.Minute,
		MaxConnIdleTime:   4 * time.Minute,
		HealthCheckPeriod: 20 * time.Second,
		StatementTimeout:  90 * time.Second,
	})
	if cfg.MaxConns != 7 {
		t.Errorf("MaxConns = %d, want 7", cfg.MaxConns)
	}
	if cfg.MinConns != 3 {
		t.Errorf("MinConns = %d, want 3", cfg.MinConns)
	}
	if cfg.MaxConnLifetime != 11*time.Minute {
		t.Errorf("MaxConnLifetime = %v, want 11m", cfg.MaxConnLifetime)
	}
	if cfg.MaxConnIdleTime != 4*time.Minute {
		t.Errorf("MaxConnIdleTime = %v, want 4m", cfg.MaxConnIdleTime)
	}
	if cfg.HealthCheckPeriod != 20*time.Second {
		t.Errorf("HealthCheckPeriod = %v, want 20s", cfg.HealthCheckPeriod)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "90000" {
		t.Errorf("statement_timeout = %q, want 90000 (ms)", got)
	}
}

// TestPool_ConnectionStringWins pins the documented precedence: a pool
// parameter carried by the URL is authoritative and the corresponding
// PoolConfig field is ignored, while unspecified parameters still get the
// configured value.
func TestPool_ConnectionStringWins(t *testing.T) {
	url := "postgres://u:p@localhost:5432/db?sslmode=disable" +
		"&pool_max_conns=42&pool_max_conn_lifetime=3m&statement_timeout=1234"
	cfg := resolvePoolConfig(t, url, PoolConfig{
		MaxConns:         5,
		MinConns:         1,
		MaxConnLifetime:  time.Hour,
		MaxConnIdleTime:  9 * time.Minute,
		StatementTimeout: 90 * time.Second,
	})
	if cfg.MaxConns != 42 {
		t.Errorf("MaxConns = %d, want the URL's 42", cfg.MaxConns)
	}
	if cfg.MaxConnLifetime != 3*time.Minute {
		t.Errorf("MaxConnLifetime = %v, want the URL's 3m", cfg.MaxConnLifetime)
	}
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "1234" {
		t.Errorf("statement_timeout = %q, want the URL's 1234", got)
	}
	// Not present in the URL, so the configured values apply.
	if cfg.MinConns != 1 {
		t.Errorf("MinConns = %d, want the configured 1", cfg.MinConns)
	}
	if cfg.MaxConnIdleTime != 9*time.Minute {
		t.Errorf("MaxConnIdleTime = %v, want the configured 9m", cfg.MaxConnIdleTime)
	}
}

// TestPool_StatementTimeoutCanBeDisabled asserts a negative StatementTimeout
// leaves PostgreSQL's own statement_timeout untouched, which an operator needs
// for a long migration.
func TestPool_StatementTimeoutCanBeDisabled(t *testing.T) {
	cfg := resolvePoolConfig(t, testDatabaseURL, PoolConfig{StatementTimeout: -1})
	if got, ok := cfg.ConnConfig.RuntimeParams["statement_timeout"]; ok && got != "" {
		t.Fatalf("statement_timeout = %q, want unset", got)
	}
}

// TestPool_SubMillisecondStatementTimeoutNeverBecomesUnlimited guards the
// silent inversion: PostgreSQL takes statement_timeout in milliseconds and
// reads 0 as "no limit", so truncating a sub-millisecond configured timeout
// would turn the tightest possible bound into an unbounded one.
func TestPool_SubMillisecondStatementTimeoutNeverBecomesUnlimited(t *testing.T) {
	for _, timeout := range []time.Duration{
		1, 999 * time.Microsecond, 1500 * time.Microsecond, time.Millisecond,
	} {
		cfg := resolvePoolConfig(t, testDatabaseURL, PoolConfig{StatementTimeout: timeout})
		got := cfg.ConnConfig.RuntimeParams["statement_timeout"]
		if got == "0" || got == "" {
			t.Fatalf("StatementTimeout %v resolved to %q, which PostgreSQL reads as unlimited",
				timeout, got)
		}
		millis, err := strconv.Atoi(got)
		if err != nil {
			t.Fatalf("statement_timeout = %q, want an integer millisecond count", got)
		}
		if millis < 1 {
			t.Fatalf("StatementTimeout %v resolved to %d ms", timeout, millis)
		}
	}
}

// TestPool_OptionsRespectAnOptionsStatementTimeout covers the `options=-c
// statement_timeout=...` spelling, which pgx passes through as a runtime
// parameter of its own.
func TestPool_OptionsRespectAnOptionsStatementTimeout(t *testing.T) {
	url := "postgres://u:p@localhost:5432/db?sslmode=disable" +
		"&options=-c%20statement_timeout%3D4321"
	cfg := resolvePoolConfig(t, url, PoolConfig{StatementTimeout: 90 * time.Second})
	if got := cfg.ConnConfig.RuntimeParams["statement_timeout"]; got != "" {
		t.Fatalf("statement_timeout = %q, want the URL's options to stand alone", got)
	}
}

func TestPoolConfig_WithDefaultsClampsMinToMax(t *testing.T) {
	resolved := PoolConfig{MaxConns: 2, MinConns: 8}.withDefaults()
	if resolved.MinConns != 2 {
		t.Fatalf("MinConns = %d, want it clamped to MaxConns=2", resolved.MinConns)
	}
	cfg := resolvePoolConfig(t, testDatabaseURL, PoolConfig{MaxConns: 2, MinConns: 8})
	if cfg.MinConns > cfg.MaxConns {
		t.Fatalf("MinConns %d exceeds MaxConns %d", cfg.MinConns, cfg.MaxConns)
	}
}

func TestConnStringParams_URLAndDSNForms(t *testing.T) {
	url := connStringParams("postgres://u:p@h:5432/db?sslmode=disable&pool_max_conns=4")
	if !url["pool_max_conns"] || !url["sslmode"] {
		t.Fatalf("URL form parameters = %v", url)
	}
	if url["pool_min_conns"] {
		t.Fatal("URL form reported an absent parameter as present")
	}
	dsn := connStringParams("host=localhost user=u pool_min_conns=3")
	if !dsn["pool_min_conns"] || !dsn["host"] {
		t.Fatalf("DSN form parameters = %v", dsn)
	}
	if dsn["pool_max_conns"] {
		t.Fatal("DSN form reported an absent parameter as present")
	}
}
