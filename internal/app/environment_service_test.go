package app

import (
	"context"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func newEnvService(t *testing.T) *EnvironmentService {
	t.Helper()
	return NewEnvironmentService(newMemoryEnvironmentRepository(),
		domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1, 0).UTC()})
}

func TestEnvironmentService_DeleteReferenced(t *testing.T) {
	repository := newMemoryEnvironmentRepository()
	envSvc := NewEnvironmentService(repository,
		domain.NewSeqIDGen(), domain.FixedClock{T: time.Unix(1, 0).UTC()})
	ctx := context.Background()
	env, _ := envSvc.Create(ctx, domain.Environment{Name: "e", ConfigType: "cloud"})
	repository.markReferenced(env.ID)
	if err := envSvc.Delete(ctx, env.ID); err == nil {
		t.Fatal("expected conflict deleting referenced environment")
	}
}

func TestEnvironmentService_DeleteUnreferencedSucceeds(t *testing.T) {
	svc := newEnvService(t)
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
	svc := newEnvService(t)
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

func TestEnvironmentService_RejectsUnenforcedConfiguration(t *testing.T) {
	svc := newEnvService(t)
	ctx := context.Background()
	cases := []map[string]any{
		{"type": "cloud", "networking": map[string]any{"type": "limited"}},
		{"type": "cloud", "packages": map[string]any{"pip": []any{"requests"}}},
		{"type": "cloud", "future_policy": true},
		{"type": 42},
	}
	for _, config := range cases {
		_, err := svc.Create(ctx, domain.Environment{
			Name: "unsupported", ConfigType: "cloud", Config: config,
		})
		if err == nil {
			t.Fatalf("unsupported config was accepted: %#v", config)
		}
	}
}

func TestEnvironmentService_NormalizesSupportedConfiguration(t *testing.T) {
	svc := newEnvService(t)
	created, err := svc.Create(context.Background(), domain.Environment{
		Name: "cloud", Description: "analysis", Metadata: map[string]any{"team": "data"},
		Config: map[string]any{"networking": nil, "packages": nil},
	})
	if err != nil {
		t.Fatalf("create default cloud environment: %v", err)
	}
	if created.ConfigType != "cloud" || len(created.Config) != 1 || created.Config["type"] != "cloud" {
		t.Fatalf("normalized config = %#v", created.Config)
	}
	if created.Description != "analysis" || created.Metadata["team"] != "data" {
		t.Fatalf("resource fields = %#v", created)
	}

	defaults, err := svc.Create(context.Background(), domain.Environment{Name: "defaults"})
	if err != nil {
		t.Fatalf("create default fields: %v", err)
	}
	if defaults.Description != "" || defaults.Scope != "" || defaults.Metadata == nil || len(defaults.Metadata) != 0 {
		t.Fatalf("default resource fields = %#v", defaults)
	}
}

func TestEnvironmentService_ValidatesMetadataAndScope(t *testing.T) {
	svc := newEnvService(t)
	cases := []domain.Environment{
		{Name: "bad metadata", Metadata: map[string]any{"bad": 1}},
		{Name: "bad scope", ConfigType: "self_hosted", Scope: "workspace"},
		{Name: "cloud scope", ConfigType: "cloud", Scope: "account"},
	}
	for _, environment := range cases {
		if _, err := svc.Create(context.Background(), environment); err == nil {
			t.Fatalf("invalid environment was accepted: %#v", environment)
		}
	}

	created, err := svc.Create(context.Background(), domain.Environment{
		Name: "self-hosted", ConfigType: "self_hosted", Scope: "account",
	})
	if err != nil {
		t.Fatalf("create scoped self-hosted environment: %v", err)
	}
	if created.Scope != "account" {
		t.Fatalf("scope = %q", created.Scope)
	}
}
