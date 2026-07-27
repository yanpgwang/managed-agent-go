package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLocal_ExecEcho(t *testing.T) {
	sb, err := NewLocalProvider().Provision(context.Background(), Spec{Timeout: 5 * time.Second})
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
	sb, _ := NewLocalProvider().Provision(context.Background(), Spec{Timeout: 5 * time.Second})
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
	sb, err := NewLocalProvider().Provision(context.Background(), Spec{
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
	sb, _ := NewLocalProvider().Provision(context.Background(), Spec{Timeout: 200 * time.Millisecond})
	defer sb.Destroy(context.Background())
	res, err := sb.Exec(context.Background(), Command{Path: "/bin/sh", Args: []string{"-c", "sleep 5"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", res)
	}
}
