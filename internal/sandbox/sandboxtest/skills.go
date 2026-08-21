package sandboxtest

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/sandbox"
)

// RunSkillBundles exercises the provider-neutral custom Skill contract. It is
// separate from Run because providers must explicitly opt in to canonical zip
// validation and the absolute immutable Skill runtime tree.
func RunSkillBundles(t *testing.T, cfg Config) {
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
	ctx := context.Background()
	provider := cfg.NewProvider(t)
	capability, ok := provider.(sandbox.SkillBundleProvider)
	if !ok || !capability.SupportsSkillBundles() {
		t.Fatalf("provider %q does not advertise custom Skills", provider.Name())
	}
	session := sessionKey(t)
	ref, box, err := provider.Create(ctx, session, cfg.Spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = box.Destroy(context.Background()) })
	skills, ok := box.(sandbox.SkillBundleSandbox)
	if !ok {
		t.Fatalf("provider %q sandbox does not expose custom Skills", provider.Name())
	}
	archive, expanded := skillArchive(t, "Portable_Tool", map[string]skillFile{
		"SKILL.md": {
			body: []byte("---\nname: portable-tool\ndescription: Portable Skill\n---\nUse the helper.\n"),
			mode: 0o644,
		},
		"scripts/run.sh": {
			body: []byte("#!/bin/sh\nprintf portable-skill-ok"),
			mode: 0o755,
		},
	})
	mount := skillMount(
		"skill_portable@100", "portable-tool", "Portable_Tool", archive, expanded,
	)
	present, err := skills.HasReadOnlySkill(ctx, mount)
	if err != nil || present {
		t.Fatalf("initial HasReadOnlySkill = %t, %v", present, err)
	}
	if err := skills.ImportReadOnlySkill(
		ctx, mount, &failingReader{data: archive[:len(archive)/2]},
	); err == nil {
		t.Fatal("partial Skill import unexpectedly succeeded")
	}
	present, err = skills.HasReadOnlySkill(ctx, mount)
	if err != nil || present {
		t.Fatalf("partial Skill import presence = %t, %v", present, err)
	}
	if err := skills.ImportReadOnlySkill(ctx, mount, bytes.NewReader(archive)); err != nil {
		t.Fatalf("ImportReadOnlySkill: %v", err)
	}
	assertSkillBundle(t, ctx, box, skills, mount, "Portable Skill", cfg.ShellPath)
	if err := box.WriteFile(
		ctx, mount.RuntimePath+"/SKILL.md", []byte("changed"),
	); err == nil {
		t.Fatal("WriteFile accepted the immutable Skill runtime path")
	}

	restarted := cfg.NewProvider(t)
	attached, err := restarted.Attach(ctx, session, ref, cfg.Spec)
	if err != nil {
		t.Fatalf("Attach Skill sandbox: %v", err)
	}
	attachedSkills, ok := attached.(sandbox.SkillBundleSandbox)
	if !ok {
		t.Fatalf("attached provider %q sandbox lost custom Skills", provider.Name())
	}
	assertSkillBundle(
		t, ctx, attached, attachedSkills, mount, "Portable Skill", cfg.ShellPath,
	)
}

func assertSkillBundle(
	t *testing.T,
	ctx context.Context,
	box sandbox.Sandbox,
	skills sandbox.SkillBundleSandbox,
	mount sandbox.ReadOnlySkillMount,
	want string,
	shellPath string,
) {
	t.Helper()
	present, err := skills.HasReadOnlySkill(ctx, mount)
	if err != nil || !present {
		t.Fatalf("HasReadOnlySkill = %t, %v", present, err)
	}
	body, err := box.ReadFile(ctx, mount.RuntimePath+"/SKILL.md")
	if err != nil || !bytes.Contains(body, []byte(want)) {
		t.Fatalf("read Skill instructions = %q, %v", body, err)
	}
	result, err := box.Exec(ctx, sandbox.Command{
		Path: shellPath,
		Args: []string{
			"-c",
			"test -x " + mount.RuntimePath + "/scripts/run.sh && " +
				mount.RuntimePath + "/scripts/run.sh",
		},
	})
	if err != nil || result.ExitCode != 0 ||
		strings.TrimSpace(string(result.Stdout)) != "portable-skill-ok" {
		t.Fatalf("execute Skill helper: result=%+v err=%v", result, err)
	}
}

type skillFile struct {
	body []byte
	mode os.FileMode
}

func skillArchive(
	t *testing.T,
	root string,
	files map[string]skillFile,
) ([]byte, int64) {
	t.Helper()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	var expanded int64
	for _, relative := range []string{"SKILL.md", "scripts/run.sh"} {
		file, ok := files[relative]
		if !ok {
			continue
		}
		header := &zip.FileHeader{Name: root + "/" + relative, Method: zip.Deflate}
		header.SetMode(file.mode)
		part, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(file.body); err != nil {
			t.Fatal(err)
		}
		expanded += int64(len(file.body))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes(), expanded
}

func skillMount(
	identity string,
	name string,
	archiveRoot string,
	archive []byte,
	expanded int64,
) sandbox.ReadOnlySkillMount {
	sum := sha256.Sum256(archive)
	return sandbox.ReadOnlySkillMount{
		Identity: identity, Name: name,
		RuntimePath: sandbox.SessionSkillsRoot + "/" + name,
		ArchiveRoot: archiveRoot, SizeBytes: int64(len(archive)),
		UncompressedSizeBytes: expanded,
		ChecksumSHA256:        hex.EncodeToString(sum[:]),
	}
}
