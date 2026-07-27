package app

import (
	"context"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/store"
)

func newEnvService(t *testing.T) (*EnvironmentService, *store.SessionRepo) {
	t.Helper()
	db, _ := store.OpenMemory()
	t.Cleanup(func() { db.Close() })
	sr := store.NewSessionRepo(db)
	svc := NewEnvironmentService(store.NewEnvironmentRepo(db),
		domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1, 0).UTC()})
	return svc, sr
}

func TestEnvironmentService_DeleteReferenced(t *testing.T) {
	db, _ := store.OpenMemory()
	defer db.Close()
	envSvc := NewEnvironmentService(store.NewEnvironmentRepo(db),
		domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1, 0).UTC()})
	ctx := context.Background()
	env, _ := envSvc.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	// reference it from a session row
	sr := store.NewSessionRepo(db)
	_ = sr.Put(ctx, domain.Session{ID: "sesn_1", EnvironmentID: env.ID, Status: domain.StatusIdle,
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(1, 0).UTC()})
	if err := envSvc.Delete(ctx, env.ID); err == nil {
		t.Fatal("expected conflict deleting referenced environment")
	}
}

func TestEnvironmentService_DeleteUnreferencedSucceeds(t *testing.T) {
	svc, _ := newEnvService(t)
	ctx := context.Background()

	env, err := svc.Create(ctx, domain.Environment{Name: "unreferenced", ConfigType: "cloud"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := svc.Delete(ctx, env.ID); err != nil {
		t.Fatalf("delete unreferenced env: %v", err)
	}

	_, err = svc.Get(ctx, env.ID)
	if err == nil {
		t.Fatal("expected NotFound after delete, got nil error")
	}
	de, ok := err.(*domain.DomainError)
	if !ok || de.Kind != domain.KindNotFound {
		t.Fatalf("expected DomainError KindNotFound, got %v", err)
	}
}

func TestEnvironmentService_DeleteMissingReturnsNotFound(t *testing.T) {
	svc, _ := newEnvService(t)
	ctx := context.Background()

	err := svc.Delete(ctx, "env_bogus_id")
	if err == nil {
		t.Fatal("expected NotFound error for missing env ID")
	}
	de, ok := err.(*domain.DomainError)
	if !ok || de.Kind != domain.KindNotFound {
		t.Fatalf("expected DomainError KindNotFound, got %v", err)
	}
}
