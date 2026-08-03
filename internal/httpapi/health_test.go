package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/health"
)

// countingProber records how many times readiness was evaluated so a test can
// prove /healthz never triggers dependency I/O.
type countingProber struct {
	calls      atomic.Int64
	dependency string
	err        error
}

func (p *countingProber) Check(context.Context) (string, error) {
	p.calls.Add(1)
	return p.dependency, p.err
}

func newHealthTestHandler(t *testing.T, prober health.Prober) http.Handler {
	t.Helper()
	ids := domain.NewSeqIDGen()
	clock := domain.FixedClock{T: time.Unix(1000, 0).UTC()}
	agentsRepo := newTestAgentRepository()
	environmentsRepo := newTestEnvironmentRepository()
	hub := app.NewHub(16)
	sessions := newTestSessionService(
		agentsRepo, environmentsRepo, ids, clock, hub, false,
	)
	return NewServer(Deps{
		Agents:   app.NewAgentService(agentsRepo, ids, clock),
		Envs:     app.NewEnvironmentService(environmentsRepo, ids, clock),
		Sessions: sessions, Events: sessions, Stream: hub,
		Readiness: prober,
	}, Config{}).Handler()
}

// TestReadyz_FailsWhenDependencyProbeFails proves /readyz is no longer an
// unconditional 200: a disconnected dependency produces a non-200 naming it, so
// an orchestrator (and the Compose worker gate) stops treating a
// database-disconnected API as ready.
func TestReadyz_FailsWhenDependencyProbeFails(t *testing.T) {
	prober := &countingProber{
		dependency: "postgres",
		err:        errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"),
	}
	h := newHealthTestHandler(t, prober)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want 503", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal /readyz body: %v (%s)", err, rec.Body)
	}
	if body["dependency"] != "postgres" {
		t.Fatalf("/readyz dependency = %q, want postgres", body["dependency"])
	}
	if body["status"] != "unavailable" {
		t.Fatalf("/readyz status field = %q, want unavailable", body["status"])
	}
	// /readyz is unauthenticated: the raw dependency error (which can carry
	// connection detail) must stay in the logs, not the response.
	if got := rec.Body.String(); strings.Contains(got, "connection refused") {
		t.Fatalf("/readyz body leaked the dependency error: %s", got)
	}
}

func TestReadyz_PassesWhenDependenciesAreHealthy(t *testing.T) {
	prober := &countingProber{}
	h := newHealthTestHandler(t, prober)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz status = %d, want 200", rec.Code)
	}
	if prober.calls.Load() != 1 {
		t.Fatalf("/readyz ran %d probes, want 1", prober.calls.Load())
	}
}

// TestHealthz_IsLivenessOnly proves /healthz and /readyz mean different things:
// liveness stays 200 and performs zero dependency I/O even while every
// dependency is down, so an outage cannot cause healthy processes to be killed.
func TestHealthz_IsLivenessOnly(t *testing.T) {
	prober := &countingProber{dependency: "temporal", err: errors.New("unavailable")}
	h := newHealthTestHandler(t, prober)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200 while dependencies are down", rec.Code)
	}
	if prober.calls.Load() != 0 {
		t.Fatalf("/healthz ran %d dependency probes, want 0", prober.calls.Load())
	}
}

// TestReadyz_WithoutProbesStaysReady keeps embedders that wire no dependencies
// (the in-memory HTTP suite) behaving as before.
func TestReadyz_WithoutProbesStaysReady(t *testing.T) {
	h := NewTestHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz status = %d, want 200 with no probes configured", rec.Code)
	}
}

// TestHealthEndpointsStayOutsideV1 pins the contract boundary: the Claude
// Managed Agents API documents no health surface, so these paths must not gain
// a /v1 twin.
func TestHealthEndpointsStayOutsideV1(t *testing.T) {
	h := NewTestHandler(t)
	for _, path := range []string{"/v1/healthz", "/v1/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, rec.Code)
		}
	}
}
