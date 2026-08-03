// Package health implements Mango's local liveness and readiness surface.
//
// This is deployment infrastructure with no Claude Managed Agents wire
// contract: the official API documents no health, readiness, or status
// endpoint. The handlers are therefore mounted outside /v1, are excluded from
// the OpenAPI document, and their bodies are free to change.
//
// Liveness and readiness deliberately mean different things:
//
//   - /healthz answers "is this process running and able to serve HTTP?". It
//     never touches PostgreSQL, Temporal, or NATS, so a dependency outage does
//     not cause an orchestrator to kill otherwise healthy processes.
//   - /readyz answers "can this process serve real traffic right now?". It
//     probes every required dependency and fails closed.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Default probe bounds. The timeout keeps one unresponsive dependency from
// holding a readiness request open, and the cache keeps aggressive readiness
// polling (Compose, Kubernetes, a load balancer, and an operator at once) from
// amplifying into dependency load.
const (
	DefaultTimeout  = 2 * time.Second
	DefaultCacheTTL = time.Second
)

// Probe is one named dependency check. Check must respect ctx and must not
// return values derived from credentials: a readiness failure names the
// dependency to unauthenticated callers, and the error text is logged.
type Probe struct {
	Name  string
	Check func(context.Context) error
}

// Prober reports whether every required dependency is currently usable. It
// returns the name of the first failing dependency together with its error, or
// ("", nil) when everything is reachable.
type Prober interface {
	Check(ctx context.Context) (dependency string, err error)
}

// Config tunes a Checker. Zero values select the defaults above.
type Config struct {
	// Timeout bounds a single full probe pass.
	Timeout time.Duration
	// CacheTTL is how long a probe result is reused before dependencies are
	// contacted again. Set it negative to disable caching.
	CacheTTL time.Duration
	// Now is injectable for deterministic cache tests.
	Now func() time.Time
}

func (c Config) withDefaults() Config {
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.CacheTTL == 0 {
		c.CacheTTL = DefaultCacheTTL
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Checker runs a fixed set of dependency probes with a short timeout and a
// brief result cache.
type Checker struct {
	cfg    Config
	probes []Probe

	// mu is held across a probe pass on purpose: concurrent readiness requests
	// queue behind one in-flight pass instead of each opening its own
	// connection attempt.
	mu         sync.Mutex
	cachedAt   time.Time
	cachedDep  string
	cachedErr  error
	haveCached bool
}

// NewChecker returns a Checker for probes. A Checker with no probes is always
// ready, which keeps embedders that have not wired dependencies (tests, the
// in-memory HTTP suite) behaving exactly as before.
func NewChecker(cfg Config, probes ...Probe) *Checker {
	return &Checker{cfg: cfg.withDefaults(), probes: probes}
}

// Check runs every probe in order and returns the first failure. Results are
// cached for CacheTTL.
func (c *Checker) Check(ctx context.Context) (string, error) {
	if c == nil || len(c.probes) == 0 {
		return "", nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.cfg.Now()
	if c.haveCached && c.cfg.CacheTTL > 0 && now.Sub(c.cachedAt) < c.cfg.CacheTTL {
		return c.cachedDep, c.cachedErr
	}

	probeCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	dependency, err := "", error(nil)
	for _, probe := range c.probes {
		if probe.Check == nil {
			continue
		}
		if probeErr := probe.Check(probeCtx); probeErr != nil {
			dependency, err = probe.Name, probeErr
			break
		}
	}
	// A caller that canceled or timed out mid-pass has not learned anything
	// durable about the dependency; do not poison the cache with it.
	if err == nil || !errors.Is(err, context.Canceled) {
		c.cachedAt, c.cachedDep, c.cachedErr, c.haveCached = now, dependency, err, true
	}
	return dependency, err
}

// LiveHandler answers the cheap liveness probe. It performs no dependency I/O
// by design.
func LiveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeStatus(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ReadyHandler answers the dependency-aware readiness probe. A failure returns
// 503 with a small body naming the failing dependency. The underlying error is
// logged rather than returned: /readyz is unauthenticated, and a dependency
// error string can carry connection details that do not belong in an
// anonymous response.
func ReadyHandler(prober Prober, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if prober == nil {
			writeStatus(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		dependency, err := prober.Check(r.Context())
		if err == nil {
			writeStatus(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
		if logger != nil {
			logger.Warn("readiness probe failed",
				slog.String("dependency", dependency),
				slog.String("error", err.Error()),
			)
		}
		writeStatus(w, http.StatusServiceUnavailable, map[string]string{
			"status":     "unavailable",
			"dependency": dependency,
		})
	}
}

// Mux is the standalone health listener used by processes that expose no other
// HTTP surface (the Temporal worker). Any other path is 404.
func Mux(prober Prober, logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /healthz", LiveHandler())
	mux.Handle("GET /readyz", ReadyHandler(prober, logger))
	return mux
}

func writeStatus(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
