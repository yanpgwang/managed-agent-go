package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocal_ExecEcho(t *testing.T) {
	_, sb, err := NewLocalProvider().Create(
		context.Background(),
		t.Name(),
		Spec{Timeout: 5 * time.Second},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy(context.Background())
	res, err := sb.Exec(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "echo hello"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(string(res.Stdout)) != "hello" {
		t.Fatalf("res = %+v", res)
	}
}

func TestLocal_FileRoundTripAndConfinement(t *testing.T) {
	_, sb, _ := NewLocalProvider().Create(
		context.Background(),
		t.Name(),
		Spec{Timeout: 5 * time.Second},
	)
	defer sb.Destroy(context.Background())
	if err := sb.WriteFile(context.Background(), "sub/a.txt", []byte("data")); err != nil {
		t.Fatal(err)
	}
	b, err := sb.ReadFile(context.Background(), "sub/a.txt")
	if err != nil || string(b) != "data" {
		t.Fatalf("read = %q err=%v", b, err)
	}
	if _, err := sb.ReadFile(context.Background(), "../escape"); err == nil {
		t.Fatal("path escape must be rejected")
	}
}

func TestLocal_IgnoresDockerSpecFields(t *testing.T) {
	_, sb, err := NewLocalProvider().Create(context.Background(), t.Name(), Spec{
		Timeout: 5 * time.Second,
		Image:   "alpine:latest", Memory: "256m", CPUs: "1.0", Network: "none", PidsLimit: 128,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Destroy(context.Background())
	res, err := sb.Exec(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "echo ok"}})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != "ok" {
		t.Fatalf("local should ignore docker spec fields: res=%+v err=%v", res, err)
	}
}

func TestLocal_Timeout(t *testing.T) {
	_, sb, _ := NewLocalProvider().Create(
		context.Background(),
		t.Name(),
		Spec{Timeout: 200 * time.Millisecond},
	)
	defer sb.Destroy(context.Background())
	res, err := sb.Exec(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "sleep 5"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", res)
	}
}

func TestLocal_AttachPreservesWorkspace(t *testing.T) {
	ctx := context.Background()
	firstProvider := NewLocalProvider()
	ref, first, err := firstProvider.Create(ctx, t.Name(), Spec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Destroy(context.Background()) })
	if err := first.WriteFile(ctx, "state.txt", []byte("durable")); err != nil {
		t.Fatal(err)
	}

	secondProvider := NewLocalProvider()
	second, err := secondProvider.Attach(ctx, t.Name(), ref, Spec{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := second.ReadFile(ctx, "state.txt")
	if err != nil || string(data) != "durable" {
		t.Fatalf("attached data = %q, err=%v", data, err)
	}
}

func TestLocal_AttachReportsNotFoundAfterBaseDirectoryLoss(t *testing.T) {
	ctx := context.Background()
	base := filepath.Join(t.TempDir(), "provider-base")
	provider := &localProvider{baseDir: base}
	ref, box, err := provider.Create(ctx, t.Name(), Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if err := box.Destroy(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(base); err != nil {
		t.Fatal(err)
	}

	_, err = provider.Attach(ctx, t.Name(), ref, Spec{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Attach after base loss = %v, want ErrNotFound", err)
	}
}
