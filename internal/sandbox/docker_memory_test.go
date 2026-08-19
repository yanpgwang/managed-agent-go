package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/domain"
)

func newDockerMemoryTestSandbox(t *testing.T) (*dockerSandbox, MemoryStoreMount, string) {
	t.Helper()
	root := t.TempDir()
	mountRoot := filepath.Join(root, dockerResourceMemoryDir, "project")
	for _, directory := range []string{
		filepath.Join(root, dockerResourceStateDir),
		mountRoot,
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	mount := MemoryStoreMount{
		Identity:    "sesrsc_memory",
		StoreID:     "memstore_memory",
		RuntimePath: "/mnt/memory/project",
		Access:      domain.MemoryAccessReadWrite,
	}
	return &dockerSandbox{
		resourceRoot: root,
		memoryMounts: map[string]string{mount.Identity: mountRoot},
	}, mount, mountRoot
}

func dockerMemoryTestFile(id, memoryPath, content string) MemoryStoreFile {
	sum := sha256.Sum256([]byte(content))
	return MemoryStoreFile{
		MemoryID:      id,
		Path:          memoryPath,
		Content:       []byte(content),
		ContentSHA256: hex.EncodeToString(sum[:]),
	}
}

func TestDockerMemory_InterruptedRefreshHasNoStaleBaseline(t *testing.T) {
	box, mount, mountRoot := newDockerMemoryTestSandbox(t)
	if err := box.ReplaceMemoryStore(context.Background(), mount, []MemoryStoreFile{
		dockerMemoryTestFile("mem_note", "/note.md", "remote"),
	}); err != nil {
		t.Fatal(err)
	}
	_, manifestPath, err := box.memoryStorePaths(mount)
	if err != nil {
		t.Fatal(err)
	}
	// ReplaceMemoryStore removes the manifest before refreshing files. Simulate
	// a process interruption in that window and leave a partially written tree.
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mountRoot, "note.md"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	snapshot, err := box.ReadMemoryStore(context.Background(), mount)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Initialized {
		t.Fatalf("interrupted refresh retained a stale baseline: %+v", snapshot.Baseline)
	}
	if len(snapshot.Baseline) != 0 || len(snapshot.Current) != 1 ||
		snapshot.Current[0].Path != "/note.md" ||
		string(snapshot.Current[0].Content) != "partial" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestDockerResourceSynchronization_SharedToolsAndExclusiveMemorySync(t *testing.T) {
	box, mount, _ := newDockerMemoryTestSandbox(t)
	ctx := context.Background()

	unlockFirst, err := box.LockResourceOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unlockSecond, err := box.LockResourceOperation(ctx)
	if err != nil {
		unlockFirst()
		t.Fatalf("second shared operation lock: %v", err)
	}
	unlockSecond()

	_, releaseTry, acquired, err := box.TryLockResourceSync(ctx)
	if err != nil {
		unlockFirst()
		t.Fatal(err)
	}
	if acquired {
		releaseTry()
		unlockFirst()
		t.Fatal("exclusive Memory sync acquired while a tool operation was active")
	}

	locked := make(chan context.Context, 1)
	released := make(chan struct{})
	go func() {
		lockedCtx, release, lockErr := box.LockResourceSync(ctx)
		if lockErr != nil {
			locked <- nil
			return
		}
		locked <- lockedCtx
		<-released
		release()
	}()
	select {
	case <-locked:
		unlockFirst()
		close(released)
		t.Fatal("exclusive Memory sync did not wait for the active tool operation")
	case <-time.After(100 * time.Millisecond):
	}

	unlockFirst()
	var lockedCtx context.Context
	select {
	case lockedCtx = <-locked:
		if lockedCtx == nil {
			close(released)
			t.Fatal("exclusive Memory sync failed")
		}
	case <-time.After(time.Second):
		close(released)
		t.Fatal("exclusive Memory sync did not acquire after tools completed")
	}

	// The marked context suppresses nested locking in the snapshot primitives;
	// the caller already owns the complete synchronization transaction.
	if err := box.ReplaceMemoryStore(lockedCtx, mount, []MemoryStoreFile{
		dockerMemoryTestFile("mem_note", "/note.md", "coordinated"),
	}); err != nil {
		close(released)
		t.Fatal(err)
	}
	snapshot, err := box.ReadMemoryStore(lockedCtx, mount)
	close(released)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Initialized || len(snapshot.Current) != 1 ||
		string(snapshot.Current[0].Content) != "coordinated" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestDockerMemory_ReadRejectsUnsafeEntries(t *testing.T) {
	tests := map[string]func(*testing.T, string){
		"symbolic link": func(t *testing.T, root string) {
			t.Helper()
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
		},
		"invalid UTF-8": func(t *testing.T, root string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "invalid"), []byte{0xff}, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"oversized content": func(t *testing.T, root string) {
			t.Helper()
			if err := os.WriteFile(
				filepath.Join(root, "large"), []byte(strings.Repeat("x", 102401)), 0o600,
			); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			box, mount, mountRoot := newDockerMemoryTestSandbox(t)
			setup(t, mountRoot)
			if _, err := box.ReadMemoryStore(context.Background(), mount); err == nil ||
				!IsPermanent(err) {
				t.Fatalf("ReadMemoryStore error = %v, want permanent error", err)
			}
		})
	}
}

func TestValidateMemoryStoreFilePath(t *testing.T) {
	for _, value := range []string{
		"", "relative", "/", "/a/../b", "/a//b", "/a\x00b", "/a\u200bb",
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := validateMemoryStoreFilePath(value); err == nil {
				t.Fatalf("validateMemoryStoreFilePath(%q) succeeded", value)
			}
		})
	}
	if got, err := validateMemoryStoreFilePath("/notes/editor.md"); err != nil ||
		got != "notes/editor.md" {
		t.Fatalf("valid path = %q, %v", got, err)
	}
}
