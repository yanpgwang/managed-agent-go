// Package sandboxtest provides the lifecycle conformance suite every Mango
// sandbox provider must pass.
package sandboxtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/sandbox"
)

// Factory returns a fresh client for the same provider deployment. It may call
// t.Skip when an optional daemon or credential is unavailable.
type Factory func(t *testing.T) sandbox.Provider

// Config describes the portable POSIX surface exercised by the suite.
type Config struct {
	NewProvider Factory
	Spec        sandbox.Spec
	ShellPath   string
}

// Run exercises the provider behavior required by SessionManager and the
// built-in tool runtime. Provider-specific isolation and capability tests remain
// alongside the adapter.
func Run(t *testing.T, cfg Config) {
	t.Helper()
	if cfg.NewProvider == nil {
		t.Fatal("sandboxtest: NewProvider is required")
	}
	if cfg.Spec.Timeout == 0 {
		cfg.Spec.Timeout = 30 * time.Second
	}
	if cfg.ShellPath == "" {
		cfg.ShellPath = "/bin/sh"
	}

	t.Run("stable_identity", func(t *testing.T) {
		first := cfg.NewProvider(t)
		second := cfg.NewProvider(t)
		if first == nil || second == nil {
			t.Fatal("provider factory returned nil")
		}
		if first.Name() == "" {
			t.Fatal("provider name is empty")
		}
		if second.Name() != first.Name() {
			t.Fatalf(
				"provider name changed across clients: %q != %q",
				first.Name(),
				second.Name(),
			)
		}
	})

	t.Run("execution_and_files", func(t *testing.T) {
		ctx := context.Background()
		provider := cfg.NewProvider(t)
		_, box, err := provider.Create(ctx, sessionKey(t), cfg.Spec)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = box.Destroy(context.Background()) })

		if box.Root() == "" {
			t.Fatal("sandbox root is empty")
		}
		content := []byte{'d', 'u', 'r', 'a', 'b', 'l', 'e', 0, '\n'}
		if err := box.WriteFile(ctx, "nested/state.bin", content); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		got, err := box.ReadFile(ctx, "nested/state.bin")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("file round trip = %q, want %q", got, content)
		}

		result, err := box.Exec(ctx, sandbox.Command{
			Path: cfg.ShellPath,
			Args: []string{"-c", "printf conformance-exec"},
		})
		if err != nil {
			t.Fatalf("Exec: %v", err)
		}
		if result.ExitCode != 0 || string(result.Stdout) != "conformance-exec" {
			t.Fatalf("Exec result = %+v", result)
		}
		result, err = box.Exec(ctx, sandbox.Command{
			Path: cfg.ShellPath,
			Args: []string{"-c", "exit 7"},
		})
		if err != nil {
			t.Fatalf("Exec non-zero exit: %v", err)
		}
		if result.ExitCode != 7 {
			t.Fatalf("non-zero exit code = %d, want 7", result.ExitCode)
		}

		if _, err := box.ReadFile(ctx, "../escape"); err == nil {
			t.Fatal("ReadFile accepted a path outside the workspace")
		}
		if err := box.WriteFile(ctx, "../escape", []byte("x")); err == nil {
			t.Fatal("WriteFile accepted a path outside the workspace")
		}

		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := box.Exec(cancelled, sandbox.Command{
			Path: cfg.ShellPath,
			Args: []string{"-c", "true"},
		}); !errors.Is(err, context.Canceled) {
			t.Fatalf("Exec with cancelled context = %v, want context.Canceled", err)
		}
	})

	t.Run("idempotent_create_and_restart_attach", func(t *testing.T) {
		ctx := context.Background()
		session := sessionKey(t)
		firstProvider := cfg.NewProvider(t)
		firstRef, first, err := firstProvider.Create(ctx, session, cfg.Spec)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = first.Destroy(context.Background()) })
		if firstRef.Provider != firstProvider.Name() || firstRef.ID == "" {
			t.Fatalf("invalid durable reference: %+v", firstRef)
		}
		if err := first.WriteFile(ctx, "restart.txt", []byte("preserved")); err != nil {
			t.Fatal(err)
		}

		restartedProvider := cfg.NewProvider(t)
		sameRef, same, err := restartedProvider.Create(ctx, session, cfg.Spec)
		if err != nil {
			t.Fatalf("repeated Create: %v", err)
		}
		if sameRef != firstRef {
			t.Fatalf("repeated Create ref = %+v, want %+v", sameRef, firstRef)
		}
		assertFile(t, ctx, same, "restart.txt", "preserved")

		attached, err := restartedProvider.Attach(ctx, session, firstRef, cfg.Spec)
		if err != nil {
			t.Fatalf("Attach: %v", err)
		}
		assertFile(t, ctx, attached, "restart.txt", "preserved")
	})

	t.Run("ownership_and_missing_reference", func(t *testing.T) {
		ctx := context.Background()
		session := sessionKey(t)
		provider := cfg.NewProvider(t)
		ref, box, err := provider.Create(ctx, session, cfg.Spec)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = box.Destroy(context.Background()) })

		if _, err := provider.Attach(
			ctx,
			session+"-other",
			ref,
			cfg.Spec,
		); err == nil || !sandbox.IsPermanent(err) {
			t.Fatalf("cross-session Attach = %v, want permanent ownership error", err)
		}
		if _, err := provider.Attach(
			ctx,
			session,
			sandbox.Ref{Provider: "wrong-provider", ID: ref.ID},
			cfg.Spec,
		); err == nil || !sandbox.IsPermanent(err) {
			t.Fatalf("wrong-provider Attach = %v, want permanent error", err)
		}

	})

	t.Run("idempotent_destroy", func(t *testing.T) {
		ctx := context.Background()
		session := sessionKey(t)
		provider := cfg.NewProvider(t)
		ref, box, err := provider.Create(ctx, session, cfg.Spec)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = box.Destroy(context.Background()) })

		if err := box.Destroy(ctx); err != nil {
			t.Fatalf("first Destroy: %v", err)
		}
		if err := box.Destroy(ctx); err != nil {
			t.Fatalf("repeated Destroy: %v", err)
		}
		restartedProvider := cfg.NewProvider(t)
		if _, err := restartedProvider.Attach(
			ctx,
			session,
			ref,
			cfg.Spec,
		); !errors.Is(err, sandbox.ErrNotFound) {
			t.Fatalf("Attach after Destroy = %v, want ErrNotFound", err)
		}
	})
}

func assertFile(
	t *testing.T,
	ctx context.Context,
	box sandbox.Sandbox,
	path string,
	want string,
) {
	t.Helper()
	got, err := box.ReadFile(ctx, path)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("ReadFile(%q) = %q, want %q", path, got, want)
	}
}

func sessionKey(t *testing.T) string {
	t.Helper()
	name := strings.NewReplacer(
		"/", "-",
		" ", "-",
		"_", "-",
	).Replace(strings.ToLower(t.Name()))
	return fmt.Sprintf("sesn-conformance-%s-%d", name, time.Now().UnixNano())
}
