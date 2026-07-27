package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/agentruntime"
	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/model"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

func NewTestHandler(t *testing.T) http.Handler {
	t.Helper()
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1000, 0).UTC()}
	hub := app.NewHub(64)
	es := app.NewEventService(store.NewEventStore(db, ids, clk), hub)
	agents := app.NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := app.NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := app.NewSessionService(store.NewSessionRepo(db), store.NewAgentRepo(db),
		store.NewEnvironmentRepo(db), es, store.NewRunStore(db, ids, clk),
		agentruntime.NewFake(), sandbox.NewLocalProvider(), ids, clk)
	srv := NewServer(Deps{Agents: agents, Envs: envs, Sessions: sessions, Events: es, Hub: hub},
		Config{RequireBeta: false, RequireAuth: false})
	return srv.Handler()
}

// NewTestHandlerAgentCore mirrors NewTestHandler but wires the real AgentCore
// over model.Fake, so runs stream agent.message previews to opted-in stream
// clients (model.NewFake returns a Fake, whose CreateMessageStream feeds deltas).
func NewTestHandlerAgentCore(t *testing.T) http.Handler {
	t.Helper()
	db, err := store.OpenMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ids := domain.NewSeqIDGen()
	clk := domain.FixedClock{T: time.Unix(1000, 0).UTC()}
	hub := app.NewHub(256)
	es := app.NewEventService(store.NewEventStore(db, ids, clk), hub)
	agents := app.NewAgentService(store.NewAgentRepo(db), ids, clk)
	envs := app.NewEnvironmentService(store.NewEnvironmentRepo(db), ids, clk)
	sessions := app.NewSessionService(store.NewSessionRepo(db), store.NewAgentRepo(db),
		store.NewEnvironmentRepo(db), es, store.NewRunStore(db, ids, clk),
		agentruntime.NewAgentCore(model.NewFake(), ids), sandbox.NewLocalProvider(), ids, clk)
	srv := NewServer(Deps{Agents: agents, Envs: envs, Sessions: sessions, Events: es, Hub: hub},
		Config{RequireBeta: false, RequireAuth: false})
	return srv.Handler()
}
