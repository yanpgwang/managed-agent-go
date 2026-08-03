package health

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChecker_ReportsFirstFailingDependencyInOrder(t *testing.T) {
	boom := errors.New("temporal frontend unavailable")
	checker := NewChecker(Config{CacheTTL: -1},
		Probe{Name: "postgres", Check: func(context.Context) error { return nil }},
		Probe{Name: "temporal", Check: func(context.Context) error { return boom }},
		Probe{Name: "nats", Check: func(context.Context) error {
			t.Fatal("probes after the first failure must not run")
			return nil
		}},
	)
	dependency, err := checker.Check(context.Background())
	if dependency != "temporal" {
		t.Fatalf("dependency = %q, want temporal", dependency)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want %v", err, boom)
	}
}

func TestChecker_WithoutProbesIsReady(t *testing.T) {
	dependency, err := NewChecker(Config{}).Check(context.Background())
	if dependency != "" || err != nil {
		t.Fatalf("empty checker = (%q, %v), want ready", dependency, err)
	}
}

// TestChecker_CachesResults proves readiness polling cannot amplify into
// dependency load: repeated calls inside the TTL reuse one probe pass.
func TestChecker_CachesResults(t *testing.T) {
	var calls int
	now := time.Unix(0, 0)
	checker := NewChecker(
		Config{CacheTTL: time.Second, Now: func() time.Time { return now }},
		Probe{Name: "postgres", Check: func(context.Context) error {
			calls++
			return nil
		}},
	)
	for range 10 {
		if _, err := checker.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("probe ran %d times inside the cache TTL, want 1", calls)
	}
	now = now.Add(time.Second)
	if _, err := checker.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("probe ran %d times after the TTL elapsed, want 2", calls)
	}
}

// TestChecker_ConcurrentCallsRunOneProbePass proves a burst of simultaneous
// readiness requests collapses onto a single dependency round trip.
func TestChecker_ConcurrentCallsRunOneProbePass(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	checker := NewChecker(Config{CacheTTL: time.Minute},
		Probe{Name: "postgres", Check: func(context.Context) error {
			mu.Lock()
			calls++
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			return nil
		}},
	)
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = checker.Check(context.Background())
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("concurrent readiness ran %d probe passes, want 1", calls)
	}
}

// TestChecker_TimesOutSlowProbe proves one wedged dependency cannot hold a
// readiness request open indefinitely.
func TestChecker_TimesOutSlowProbe(t *testing.T) {
	checker := NewChecker(
		Config{Timeout: 20 * time.Millisecond, CacheTTL: -1},
		Probe{Name: "postgres", Check: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}},
	)
	started := time.Now()
	dependency, err := checker.Check(context.Background())
	if err == nil {
		t.Fatal("slow probe reported ready")
	}
	if dependency != "postgres" {
		t.Fatalf("dependency = %q, want postgres", dependency)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("probe took %v, want it bounded by the configured timeout", elapsed)
	}
}

func TestLiveHandler_DoesNotProbe(t *testing.T) {
	rec := httptest.NewRecorder()
	LiveHandler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v (%s)", err, rec.Body)
	}
	if body["status"] != "ok" {
		t.Fatalf("body = %v, want status ok", body)
	}
}

func TestReadyHandler_NamesFailingDependencyWithoutLeakingTheError(t *testing.T) {
	checker := NewChecker(Config{CacheTTL: -1},
		Probe{Name: "nats", Check: func(context.Context) error {
			return errors.New("nats://user:hunter2@nats:4222 refused")
		}},
	)
	rec := httptest.NewRecorder()
	ReadyHandler(checker, nil).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"dependency":"nats"`) {
		t.Fatalf("body = %s, want it to name nats", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("readiness body leaked probe error detail: %s", rec.Body)
	}
}

func TestMux_ServesLivenessAndReadiness(t *testing.T) {
	checker := NewChecker(Config{CacheTTL: -1},
		Probe{Name: "postgres", Check: func(context.Context) error {
			return errors.New("down")
		}},
	)
	mux := Mux(checker, nil)

	live := httptest.NewRecorder()
	mux.ServeHTTP(live, httptest.NewRequest("GET", "/healthz", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", live.Code)
	}

	ready := httptest.NewRecorder()
	mux.ServeHTTP(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d, want 503", ready.Code)
	}

	other := httptest.NewRecorder()
	mux.ServeHTTP(other, httptest.NewRequest("GET", "/v1/sessions", nil))
	if other.Code != http.StatusNotFound {
		t.Fatalf("unrelated path status = %d, want 404; the health listener must "+
			"not expose an API surface", other.Code)
	}
}
