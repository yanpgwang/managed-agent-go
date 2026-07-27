package httpapi

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

func handlerOn(t *testing.T, db *store.DB) http.Handler {
	t.Helper()
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1000, 0).UTC()}
	hub := app.NewHub(64)
	es := app.NewEventService(store.NewEventStore(db, ids, clk), hub)
	agents := app.NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := app.NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := app.NewSessionService(store.NewSessionRepo(db), store.NewAgentRepo(db),
		store.NewEnvironmentRepo(db), es, store.NewRunStore(db, ids, clk),
		agentruntime.NewFake(), sandbox.NewLocalProvider(), ids, clk)
	return NewServerAdapter(agents, envs, sessions, es, hub)
}

// pollEvents polls GET /v1/sessions/{id}/events until the event count
// stabilises (two consecutive reads return the same count) or the deadline
// expires.  It returns (count, true) when stable and (lastSeen, false) when
// the deadline expires without stabilising.
func pollEvents(h http.Handler, sessID string, deadline time.Duration) (int, bool) {
	end := time.Now().Add(deadline)
	prev := -1
	for time.Now().Before(end) {
		rec := do(h, "GET", "/v1/sessions/"+sessID+"/events", "")
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		data, ok := m["data"].([]any)
		if !ok {
			time.Sleep(25 * time.Millisecond)
			continue
		}
		if len(data) == prev && prev >= 0 {
			return prev, true
		}
		prev = len(data)
		time.Sleep(25 * time.Millisecond)
	}
	return prev, false
}

func TestRestart_ResourcesAndEventsSurvive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	// ---- phase 1: populate on the first DB handle ----
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	h := handlerOn(t, db)

	ag := createID(t, h, "POST", "/v1/agents", `{"name":"a","model":"claude-opus-4-8"}`)
	env := createID(t, h, "POST", "/v1/environments", `{"name":"e","config":{"type":"cloud"}}`)
	sess := createID(t, h, "POST", "/v1/sessions", `{"agent":"`+ag+`","environment_id":"`+env+`"}`)

	// Send a user.message; the fake runtime will append additional events
	// (agent.message + session.status_idle). Poll until the count stabilises
	// so the before/after comparison is deterministic.
	do(h, "POST", "/v1/sessions/"+sess+"/events", `{"events":[{"type":"user.message","content":[{"type":"text","text":"hello"}]}]}`)

	nBefore, stable := pollEvents(h, sess, 3*time.Second)
	if !stable {
		t.Fatalf("event count did not stabilise within deadline (last seen %d)", nBefore)
	}
	if nBefore <= 0 {
		t.Fatal("expected at least one event before restart, got 0")
	}

	// Close the DB, simulating a process restart.
	db.Close()

	// ---- phase 2: reopen and verify durability ----
	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	h2 := handlerOn(t, db2)

	after := do(h2, "GET", "/v1/sessions/"+sess+"/events", "")
	var a map[string]any
	if err := json.Unmarshal(after.Body.Bytes(), &a); err != nil {
		t.Fatalf("unmarshal after-restart events: %v", err)
	}
	afterData, ok := a["data"].([]any)
	if !ok {
		t.Fatalf("after-restart events: unexpected response: %s", after.Body)
	}
	nAfter := len(afterData)
	if nAfter != nBefore {
		t.Fatalf("events lost across restart: before %d after %d", nBefore, nAfter)
	}

	// agent + session still retrievable
	if rec := do(h2, "GET", "/v1/agents/"+ag, ""); rec.Code != 200 {
		t.Fatalf("agent lost after restart: %d %s", rec.Code, rec.Body)
	}
	if rec := do(h2, "GET", "/v1/sessions/"+sess, ""); rec.Code != 200 {
		t.Fatalf("session lost after restart: %d %s", rec.Code, rec.Body)
	}
}
