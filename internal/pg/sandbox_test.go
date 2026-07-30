package pg

import (
	"context"
	"errors"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/sandbox"
)

func TestSandboxBindingPersistsAndFencesSessionDeletion(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_sandbox_binding")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}

	first := sandbox.Binding{
		SessionID: session.ID,
		Ref:       sandbox.Ref{Provider: "docker", ID: "container-1"},
		SpecHash:  "sha256:first",
	}
	authoritative, err := store.PutSandboxBinding(ctx, first)
	if err != nil {
		t.Fatalf("put binding: %v", err)
	}
	if authoritative != first {
		t.Fatalf("binding = %+v, want %+v", authoritative, first)
	}

	// A second worker cannot overwrite the elected provider resource.
	loser := sandbox.Binding{
		SessionID: session.ID,
		Ref:       sandbox.Ref{Provider: "docker", ID: "container-2"},
		SpecHash:  "sha256:second",
	}
	authoritative, err = store.PutSandboxBinding(ctx, loser)
	if err != nil {
		t.Fatalf("put competing binding: %v", err)
	}
	if authoritative != first {
		t.Fatalf("competing put returned %+v, want original %+v", authoritative, first)
	}

	// A fresh Store instance (standing in for a restarted worker) reads the same
	// opaque provider identity from PostgreSQL.
	restarted := NewStore(store.pool, &seqIDGen{}, fixedClock{})
	got, found, err := restarted.GetSandboxBinding(ctx, session.ID)
	if err != nil || !found || got != first {
		t.Fatalf("restarted GetSandboxBinding = %+v, found=%v, err=%v", got, found, err)
	}

	if err := store.PrepareSessionDeletion(ctx, session.ID); err != nil {
		t.Fatalf("prepare deletion: %v", err)
	}
	if err := store.FinalizeSessionDeletion(ctx, session.ID); err == nil {
		t.Fatal("session deletion discarded a live sandbox binding")
	} else {
		var domainErr *domain.DomainError
		if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindConflict {
			t.Fatalf("finalize with live binding = %v, want conflict", err)
		}
	}
	if err := store.DeleteSandboxBinding(ctx, first); err != nil {
		t.Fatalf("delete binding: %v", err)
	}
	if err := store.FinalizeSessionDeletion(ctx, session.ID); err != nil {
		t.Fatalf("finalize after sandbox teardown: %v", err)
	}
}
