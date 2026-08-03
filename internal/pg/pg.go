// Package pg provides the authoritative control-plane persistence: a pgx pool,
// embedded goose migrations, and stores built on sqlc-generated queries.
package pg

import (
	"context"
	"embed"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:generate sqlc generate -f ../../sqlc.yaml

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Pool sizing and lifetime defaults. They are deliberately modest: the control
// plane runs as several replicas against one PostgreSQL instance, so an
// unbounded per-process pool is the fastest way to exhaust server connections.
const (
	DefaultMaxConns          = 10
	DefaultMinConns          = 2
	DefaultMaxConnLifetime   = 30 * time.Minute
	DefaultMaxConnIdleTime   = 5 * time.Minute
	DefaultHealthCheckPeriod = time.Minute
	DefaultStatementTimeout  = 30 * time.Second
)

// PoolConfig bounds the control-plane connection pool.
//
// Precedence: any pool parameter already present in the connection string wins.
// pgx parses `pool_max_conns`, `pool_min_conns`, `pool_max_conn_lifetime`,
// `pool_max_conn_idle_time`, and `pool_health_check_period` from the URL, and a
// `statement_timeout` runtime parameter likewise passes through untouched. A
// PoolConfig field is applied only when the connection string is silent about
// it; a zero field then falls back to the package default above. This ordering
// lets an operator pin a single value in the URL without restating the rest.
type PoolConfig struct {
	MaxConns          int32
	MinConns          int32
	MaxConnLifetime   time.Duration
	MaxConnIdleTime   time.Duration
	HealthCheckPeriod time.Duration

	// StatementTimeout bounds a single server-side statement. It is applied as
	// the PostgreSQL `statement_timeout` runtime parameter, so a runaway query
	// cannot pin a pooled connection forever. Set it negative to leave
	// statement_timeout unset (PostgreSQL's own default, usually unlimited).
	StatementTimeout time.Duration
}

func (c PoolConfig) withDefaults() PoolConfig {
	if c.MaxConns <= 0 {
		c.MaxConns = DefaultMaxConns
	}
	if c.MinConns < 0 {
		c.MinConns = 0
	}
	if c.MinConns == 0 {
		c.MinConns = DefaultMinConns
	}
	if c.MinConns > c.MaxConns {
		c.MinConns = c.MaxConns
	}
	if c.MaxConnLifetime <= 0 {
		c.MaxConnLifetime = DefaultMaxConnLifetime
	}
	if c.MaxConnIdleTime <= 0 {
		c.MaxConnIdleTime = DefaultMaxConnIdleTime
	}
	if c.HealthCheckPeriod <= 0 {
		c.HealthCheckPeriod = DefaultHealthCheckPeriod
	}
	if c.StatementTimeout == 0 {
		c.StatementTimeout = DefaultStatementTimeout
	}
	return c
}

// Pool opens a pgx connection pool for the given PostgreSQL URL. The caller owns
// the returned pool and must Close it.
//
// An optional PoolConfig supplies bounds for anything the connection string
// does not already specify; omitting it uses the package defaults.
func Pool(ctx context.Context, databaseURL string, configs ...PoolConfig) (*pgxpool.Pool, error) {
	var poolCfg PoolConfig
	if len(configs) > 0 {
		poolCfg = configs[len(configs)-1]
	}
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pg: parse database url: %w", err)
	}
	applyPoolConfig(cfg, poolCfg.withDefaults(), connStringParams(databaseURL))
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return pool, nil
}

// applyPoolConfig writes the resolved bounds onto a parsed pgxpool config,
// skipping every parameter the connection string already set. pgxpool cannot
// report which values came from the URL (it substitutes its own defaults), so
// presence is determined from the connection string itself.
func applyPoolConfig(cfg *pgxpool.Config, resolved PoolConfig, present map[string]bool) {
	if !present["pool_max_conns"] {
		cfg.MaxConns = resolved.MaxConns
	}
	if !present["pool_min_conns"] {
		cfg.MinConns = resolved.MinConns
	}
	if !present["pool_max_conn_lifetime"] {
		cfg.MaxConnLifetime = resolved.MaxConnLifetime
	}
	if !present["pool_max_conn_idle_time"] {
		cfg.MaxConnIdleTime = resolved.MaxConnIdleTime
	}
	if !present["pool_health_check_period"] {
		cfg.HealthCheckPeriod = resolved.HealthCheckPeriod
	}
	if cfg.MinConns > cfg.MaxConns {
		cfg.MinConns = cfg.MaxConns
	}
	if resolved.StatementTimeout <= 0 {
		return
	}
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	// A statement_timeout supplied in the connection string (directly or via
	// `options=-c statement_timeout=...`) wins.
	if cfg.ConnConfig.RuntimeParams["statement_timeout"] != "" ||
		strings.Contains(cfg.ConnConfig.RuntimeParams["options"], "statement_timeout") {
		return
	}
	cfg.ConnConfig.RuntimeParams["statement_timeout"] =
		fmt.Sprintf("%d", resolved.StatementTimeout.Milliseconds())
}

// connStringParams reports which parameter names the connection string sets. It
// understands both the URL form (postgres://host/db?pool_max_conns=8) and the
// keyword/value DSN form (host=localhost pool_max_conns=8).
func connStringParams(connString string) map[string]bool {
	present := map[string]bool{}
	if u, err := url.Parse(connString); err == nil &&
		(u.Scheme == "postgres" || u.Scheme == "postgresql") {
		for key := range u.Query() {
			present[key] = true
		}
		return present
	}
	for _, field := range strings.Fields(connString) {
		if key, _, ok := strings.Cut(field, "="); ok {
			present[strings.TrimSpace(key)] = true
		}
	}
	return present
}

// Migrate applies all embedded goose migrations up to the latest version. It is
// idempotent: goose records applied versions and skips them on the next run.
// goose uses database/sql, so the pool's config is borrowed through the pgx
// stdlib adapter and the temporary *sql.DB is closed before returning (it does
// not close the caller's pool).
//
// Migration statements run under the pool's configured statement_timeout. Raise
// or disable PoolConfig.StatementTimeout before applying a migration expected to
// exceed it.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("pg: set goose dialect: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer sqlDB.Close()
	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("pg: run migrations: %w", err)
	}
	return nil
}
