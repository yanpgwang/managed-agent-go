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

func TestSandboxProvisioningIntentReconcilesCrashBoundaries(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_sandbox_intent")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	intent := sandbox.ProvisioningIntent{
		SessionID: session.ID,
		Provider:  "docker",
		Spec: sandbox.Spec{
			Image:   "example.test/sandbox:fixed",
			Timeout: 15,
		},
		SpecHash: "sha256:intent",
	}
	authoritative, err := store.PutSandboxProvisioningIntent(ctx, intent)
	if err != nil {
		t.Fatalf("put provisioning intent: %v", err)
	}
	if authoritative != intent {
		t.Fatalf("intent = %+v, want %+v", authoritative, intent)
	}
	listed, err := store.ListSandboxProvisioningIntents(ctx, "docker", 10)
	if err != nil {
		t.Fatalf("list provisioning intents: %v", err)
	}
	if len(listed) != 1 || listed[0] != intent {
		t.Fatalf("listed intents = %+v, want [%+v]", listed, intent)
	}

	// Committing the elected binding and clearing its crash-recovery intent is
	// one transaction.
	binding := sandbox.Binding{
		SessionID: session.ID,
		Ref:       sandbox.Ref{Provider: "docker", ID: "container-intent"},
		SpecHash:  intent.SpecHash,
	}
	if _, err := store.PutSandboxBinding(ctx, binding); err != nil {
		t.Fatalf("put binding: %v", err)
	}
	if _, found, err := store.GetSandboxProvisioningIntent(ctx, session.ID); err != nil || found {
		t.Fatalf("intent after binding commit: found=%v err=%v", found, err)
	}
}

func TestSandboxProvisioningIntentFencesDeletionUntilReconciled(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sesn_sandbox_intent_delete")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("create session: %v", err)
	}
	intent := sandbox.ProvisioningIntent{
		SessionID: session.ID,
		Provider:  "docker",
		Spec:      sandbox.Spec{Image: "example.test/sandbox:fixed"},
		SpecHash:  "sha256:delete-intent",
	}
	if _, err := store.PutSandboxProvisioningIntent(ctx, intent); err != nil {
		t.Fatalf("put provisioning intent: %v", err)
	}
	if err := store.PrepareSessionDeletion(ctx, session.ID); err != nil {
		t.Fatalf("prepare deletion: %v", err)
	}
	deleting, err := store.ListDeletingSessionIDs(ctx, 10)
	if err != nil || len(deleting) != 1 || deleting[0] != session.ID {
		t.Fatalf("deleting sessions = %v, err=%v", deleting, err)
	}
	listed, err := store.ListSandboxProvisioningIntents(ctx, "docker", 10)
	if err != nil || len(listed) != 1 || !listed[0].Deleting {
		t.Fatalf("deleting intent projection = %+v, err=%v", listed, err)
	}
	if err := store.FinalizeSessionDeletion(ctx, session.ID); err == nil {
		t.Fatal("finalized deletion while provisioning intent was unresolved")
	}
	// A deletion fence also prevents a late worker from opening a new
	// provisioning obligation.
	late := intent
	late.SpecHash = "sha256:late"
	if _, err := store.PutSandboxProvisioningIntent(ctx, late); err == nil {
		t.Fatal("created provisioning intent after deletion fence")
	}
	if err := store.DeleteSandboxProvisioningIntent(ctx, intent); err != nil {
		t.Fatalf("delete provisioning intent: %v", err)
	}
	if err := store.FinalizeSessionDeletion(ctx, session.ID); err != nil {
		t.Fatalf("finalize after intent reconciliation: %v", err)
	}
}
