package main

import (
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/pg"
	temporalpkg "github.com/yanpgwang/managed-agent-go/internal/temporal"
)

// TestAPIConfigFromEnv_ZeroConfigDevPath asserts the documented default: with
// no keys configured and no -strict, the server starts, authentication is off,
// and the operator is warned. It must never be the old "any non-empty string
// authenticates" behavior.
func TestAPIConfigFromEnv_ZeroConfigDevPath(t *testing.T) {
	t.Setenv(envAPIKeys, "")
	t.Setenv(envDisableAuthorizationHeader, "")
	cfg, warning, err := apiConfigFromEnv(false)
	if err != nil {
		t.Fatalf("zero-config serve failed to start: %v", err)
	}
	if cfg.RequireAuth {
		t.Fatal("RequireAuth is true without any configured key")
	}
	if cfg.APIKeys.Len() != 0 {
		t.Fatalf("APIKeys.Len() = %d, want 0", cfg.APIKeys.Len())
	}
	if warning == "" {
		t.Fatal("the zero-config path produced no warning")
	}
	for _, want := range []string{"authentication is DISABLED", envAPIKeys} {
		if !strings.Contains(warning, want) {
			t.Errorf("warning does not mention %q: %s", want, warning)
		}
	}
	// Strict wire-header validation is unaffected by key configuration.
	if cfg.RequireBeta || cfg.RequireVersion || cfg.RequireContentType {
		t.Fatal("non-strict serve required wire headers")
	}
}

// TestAPIConfigFromEnv_KeysEnableAuthWithoutStrict asserts that configuring
// keys is sufficient to turn authentication on: it is not gated behind a flag.
func TestAPIConfigFromEnv_KeysEnableAuthWithoutStrict(t *testing.T) {
	t.Setenv(envAPIKeys, "ops:secret-one,ci:secret-two")
	t.Setenv(envDisableAuthorizationHeader, "")
	cfg, warning, err := apiConfigFromEnv(false)
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning with keys configured: %s", warning)
	}
	if !cfg.RequireAuth {
		t.Fatal("RequireAuth is false with keys configured")
	}
	if got := strings.Join(cfg.APIKeys.IDs(), ","); got != "ci,ops" {
		t.Fatalf("key ids = %q, want ci,ops", got)
	}
	if principal, ok := cfg.APIKeys.Lookup("secret-one"); !ok || principal.KeyID != "ops" {
		t.Fatalf("configured key did not resolve: %+v ok=%v", principal, ok)
	}
	if _, ok := cfg.APIKeys.Lookup("anything-else"); ok {
		t.Fatal("an unconfigured value authenticated")
	}
	if cfg.DisableAuthorizationHeader {
		t.Fatal("the documented `authorization: Bearer` header must be accepted by default")
	}
}

// TestAPIConfigFromEnv_StrictWithoutKeysFailsClosed asserts -strict refuses to
// serve an unauthenticated API. Previously it enabled a presence-only check
// that any string satisfied.
func TestAPIConfigFromEnv_StrictWithoutKeysFailsClosed(t *testing.T) {
	t.Setenv(envAPIKeys, "")
	_, _, err := apiConfigFromEnv(true)
	if err == nil {
		t.Fatal("-strict started without any configured API key")
	}
	if !strings.Contains(err.Error(), envAPIKeys) {
		t.Fatalf("error does not point at %s: %v", envAPIKeys, err)
	}
}

func TestAPIConfigFromEnv_StrictWithKeys(t *testing.T) {
	t.Setenv(envAPIKeys, "ops:secret-one")
	cfg, warning, err := apiConfigFromEnv(true)
	if err != nil {
		t.Fatal(err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning: %s", warning)
	}
	if !cfg.RequireAuth || !cfg.RequireBeta || !cfg.RequireVersion || !cfg.RequireContentType {
		t.Fatalf("strict config = %+v", cfg)
	}
}

// TestAPIConfigFromEnv_AuthorizationHeaderOptOut asserts the knob narrows the
// accepted credential headers rather than widening them: `authorization:
// Bearer` is a documented Claude API credential header and is on unless an
// operator explicitly turns it off.
func TestAPIConfigFromEnv_AuthorizationHeaderOptOut(t *testing.T) {
	t.Setenv(envAPIKeys, "ops:secret-one")
	t.Setenv(envDisableAuthorizationHeader, "")
	cfg, _, err := apiConfigFromEnv(false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DisableAuthorizationHeader {
		t.Fatal("the documented bearer header was disabled without being asked")
	}

	t.Setenv(envDisableAuthorizationHeader, "true")
	cfg, _, err = apiConfigFromEnv(false)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.DisableAuthorizationHeader {
		t.Fatal("the opt-out did not disable the authorization header")
	}

	t.Setenv(envDisableAuthorizationHeader, "maybe")
	if _, _, err := apiConfigFromEnv(false); err == nil {
		t.Fatal("an invalid boolean was accepted")
	}
}

func TestAPIConfigFromEnv_RejectsMalformedKeySpec(t *testing.T) {
	t.Setenv(envAPIKeys, "no-separator-here")
	_, _, err := apiConfigFromEnv(false)
	if err == nil {
		t.Fatal("a malformed key specification was accepted")
	}
	if strings.Contains(err.Error(), "no-separator-here") {
		t.Fatalf("error echoed the configured value: %v", err)
	}
}

// TestPoolConfigFromEnv_Defaults asserts the zero-config path still produces
// the bounded pool defaults rather than pgx's unmanaged ones.
func TestPoolConfigFromEnv_Defaults(t *testing.T) {
	for _, name := range []string{
		envDBMaxConns, envDBMinConns, envDBMaxConnLifetime,
		envDBMaxConnIdleTime, envDBHealthCheckPeriod, envDBStatementTimeout,
	} {
		t.Setenv(name, "")
	}
	cfg, err := poolConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := pg.PoolConfig{
		MaxConns:          pg.DefaultMaxConns,
		MinConns:          pg.DefaultMinConns,
		MaxConnLifetime:   pg.DefaultMaxConnLifetime,
		MaxConnIdleTime:   pg.DefaultMaxConnIdleTime,
		HealthCheckPeriod: pg.DefaultHealthCheckPeriod,
		StatementTimeout:  pg.DefaultStatementTimeout,
	}
	if cfg != want {
		t.Fatalf("poolConfigFromEnv() = %+v, want %+v", cfg, want)
	}
}

func TestPoolConfigFromEnv_ReadsEveryKnob(t *testing.T) {
	t.Setenv(envDBMaxConns, "24")
	t.Setenv(envDBMinConns, "4")
	t.Setenv(envDBMaxConnLifetime, "45m")
	t.Setenv(envDBMaxConnIdleTime, "90s")
	t.Setenv(envDBHealthCheckPeriod, "15s")
	t.Setenv(envDBStatementTimeout, "12s")
	cfg, err := poolConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := pg.PoolConfig{
		MaxConns:          24,
		MinConns:          4,
		MaxConnLifetime:   45 * time.Minute,
		MaxConnIdleTime:   90 * time.Second,
		HealthCheckPeriod: 15 * time.Second,
		StatementTimeout:  12 * time.Second,
	}
	if cfg != want {
		t.Fatalf("poolConfigFromEnv() = %+v, want %+v", cfg, want)
	}
}

// TestPoolConfigFromEnv_ZeroStatementTimeoutDisablesIt asserts "0" is accepted
// as an explicit "leave PostgreSQL's statement_timeout alone" rather than being
// silently replaced by the default.
func TestPoolConfigFromEnv_ZeroStatementTimeoutDisablesIt(t *testing.T) {
	t.Setenv(envDBStatementTimeout, "0")
	cfg, err := poolConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StatementTimeout >= 0 {
		t.Fatalf("StatementTimeout = %v, want a negative sentinel meaning disabled",
			cfg.StatementTimeout)
	}
}

func TestPoolConfigFromEnv_RejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		envDBMaxConns:         "many",
		envDBMinConns:         "-1",
		envDBMaxConnLifetime:  "forever",
		envDBMaxConnIdleTime:  "0",
		envDBStatementTimeout: "-5s",
	}
	for name, value := range cases {
		t.Run(name+"="+value, func(t *testing.T) {
			t.Setenv(name, value)
			_, err := poolConfigFromEnv()
			if err == nil {
				t.Fatalf("poolConfigFromEnv accepted %s=%q", name, value)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error does not name the variable: %v", err)
			}
		})
	}
}

func TestWorkerConfigFromEnv_Defaults(t *testing.T) {
	for _, name := range []string{
		envWorkerMaxActivities, envWorkerMaxWorkflowTasks,
		envWorkerActivityPollers, envWorkerWorkflowPollers, envWorkerDrainTimeout,
	} {
		t.Setenv(name, "")
	}
	cfg, err := workerConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := temporalpkg.WorkerConfig{
		MaxConcurrentActivities:    temporalpkg.DefaultMaxConcurrentActivities,
		MaxConcurrentWorkflowTasks: temporalpkg.DefaultMaxConcurrentWorkflowTasks,
		ActivityPollers:            temporalpkg.DefaultActivityPollers,
		WorkflowPollers:            temporalpkg.DefaultWorkflowPollers,
		DrainTimeout:               temporalpkg.DefaultWorkerDrainTimeout,
	}
	if cfg != want {
		t.Fatalf("workerConfigFromEnv() = %+v, want %+v", cfg, want)
	}
}

// TestWorkerConfigFromEnv_ReachesWorkerOptions proves the env values are not
// merely parsed but end up in the Temporal SDK options the worker is built
// with.
func TestWorkerConfigFromEnv_ReachesWorkerOptions(t *testing.T) {
	t.Setenv(envWorkerMaxActivities, "9")
	t.Setenv(envWorkerMaxWorkflowTasks, "11")
	t.Setenv(envWorkerActivityPollers, "3")
	t.Setenv(envWorkerWorkflowPollers, "4")
	t.Setenv(envWorkerDrainTimeout, "45s")
	cfg, err := workerConfigFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	opts := cfg.WorkerOptions()
	if opts.MaxConcurrentActivityExecutionSize != 9 ||
		opts.MaxConcurrentWorkflowTaskExecutionSize != 11 ||
		opts.MaxConcurrentActivityTaskPollers != 3 ||
		opts.MaxConcurrentWorkflowTaskPollers != 4 ||
		opts.WorkerStopTimeout != 45*time.Second {
		t.Fatalf("worker options from env = %+v", opts)
	}
}

func TestWorkerConfigFromEnv_RejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		envWorkerMaxActivities: "lots",
		envWorkerDrainTimeout:  "0",
	}
	for name, value := range cases {
		t.Run(name+"="+value, func(t *testing.T) {
			t.Setenv(name, value)
			_, err := workerConfigFromEnv()
			if err == nil {
				t.Fatalf("workerConfigFromEnv accepted %s=%q", name, value)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error does not name the variable: %v", err)
			}
		})
	}
}

// TestShutdownTimeout_DefaultsAboveTheOldHardCodedFiveSeconds pins the intent
// of the change: the API drain window is configurable and its default is long
// enough to unwind streaming clients.
func TestShutdownTimeout_DefaultsAboveTheOldHardCodedFiveSeconds(t *testing.T) {
	t.Setenv(envShutdownTimeout, "")
	got, err := envDurationOr(envShutdownTimeout, defaultShutdownTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if got != defaultShutdownTimeout {
		t.Fatalf("shutdown timeout = %v, want %v", got, defaultShutdownTimeout)
	}
	if defaultShutdownTimeout <= 5*time.Second {
		t.Fatalf("defaultShutdownTimeout = %v; the previous hard-coded value was 5s",
			defaultShutdownTimeout)
	}
	t.Setenv(envShutdownTimeout, "2m")
	got, err = envDurationOr(envShutdownTimeout, defaultShutdownTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2*time.Minute {
		t.Fatalf("shutdown timeout = %v, want 2m", got)
	}
	t.Setenv(envShutdownTimeout, "soon")
	if _, err := envDurationOr(envShutdownTimeout, defaultShutdownTimeout); err == nil {
		t.Fatal("envDurationOr accepted an invalid duration")
	}
}

// stubWorker is a worker.Worker stand-in whose Stop blocks for a fixed time, so
// the drain bound can be asserted without a Temporal server.
type stubWorker struct{ stopFor time.Duration }

func (s stubWorker) Stop() { time.Sleep(s.stopFor) }

// TestDrainWorker_WaitsForInFlightActivities asserts the shutdown path waits
// for the worker instead of returning immediately.
func TestDrainWorker_WaitsForInFlightActivities(t *testing.T) {
	start := time.Now()
	if !drainWorker(stubWorker{stopFor: 150 * time.Millisecond}, time.Second) {
		t.Fatal("drainWorker reported a timeout for a worker that stopped in time")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("drainWorker returned after %v; it did not wait for Stop", elapsed)
	}
}

// TestDrainWorker_IsBounded asserts a wedged worker cannot block process exit
// forever: the drain is reported as incomplete once the bound elapses.
func TestDrainWorker_IsBounded(t *testing.T) {
	start := time.Now()
	if drainWorker(stubWorker{stopFor: time.Hour}, 50*time.Millisecond) {
		t.Fatal("drainWorker reported success for a worker that never stopped")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("drainWorker took %v; the bound was not enforced", elapsed)
	}
	// The production bound is the SDK drain plus a fixed margin, so a worker
	// that finishes right at WorkerStopTimeout is not misreported.
	if drainGraceMargin <= 0 {
		t.Fatal("drainGraceMargin must be positive")
	}
}
