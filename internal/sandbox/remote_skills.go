package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

const (
	remoteSkillControlRoot = "/var/lib/mango/skills"
	remoteSkillMarkerRoot  = remoteSkillControlRoot + "/markers"
	remoteSkillMarkerLimit = 16 * 1024
	remoteSkillModeBatch   = 32 * 1024
	remoteSkillTempPrefix  = ".mango-remote-skill-"
)

type remoteSkillExecute func(context.Context, Command) (*Result, error)

// remoteSkillBundles owns the provider-independent Skill materialization
// protocol for durable remote sandboxes. Provider adapters supply only their
// official file data plane and isolated command execution boundary.
type remoteSkillBundles struct {
	provider  string
	resources *remoteFileResources
	execute   remoteSkillExecute
	mu        sync.Mutex
}

type remoteSkillMarker struct {
	Bundle            string `json:"bundle"`
	InstructionBytes  int64  `json:"instruction_bytes"`
	InstructionSHA256 string `json:"instruction_sha256"`
}

type preparedRemoteSkill struct {
	directory         string
	instructionBytes  int64
	instructionSHA256 string
}

func newRemoteSkillBundles(
	provider string,
	resources *remoteFileResources,
	execute remoteSkillExecute,
) *remoteSkillBundles {
	return &remoteSkillBundles{
		provider: provider, resources: resources, execute: execute,
	}
}

func (r *remoteSkillBundles) HasReadOnlySkill(
	ctx context.Context,
	mount ReadOnlySkillMount,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateReadOnlySkillMount(mount); err != nil {
		return false, Permanent(err)
	}
	if r == nil || r.resources == nil || r.resources.files == nil || r.execute == nil {
		return false, errors.New("sandbox: remote Skill data plane is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hasReadOnlySkill(ctx, mount)
}

func (r *remoteSkillBundles) hasReadOnlySkill(
	ctx context.Context,
	mount ReadOnlySkillMount,
) (bool, error) {
	marker, err := r.readMarker(ctx, mount)
	if r.resources.files.ResourceIsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if marker.Bundle != skillMarker(mount) || marker.InstructionBytes < 0 ||
		marker.InstructionBytes >= maxSkillArchiveBytes ||
		!validSHA256(marker.InstructionSHA256) {
		return false, nil
	}

	target := resolvedSkillRuntimePath(mount)
	targetInfo, err := r.resources.files.ResourceStat(ctx, target)
	if r.resources.files.ResourceIsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"sandbox: %s inspect Skill directory: %w", r.provider, err,
		)
	}
	if !targetInfo.Directory {
		return false, nil
	}
	instructionPath := path.Join(target, "SKILL.md")
	instructionInfo, err := r.resources.files.ResourceStat(ctx, instructionPath)
	if r.resources.files.ResourceIsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"sandbox: %s inspect Skill instructions: %w", r.provider, err,
		)
	}
	if !instructionInfo.Regular || instructionInfo.SizeBytes != marker.InstructionBytes {
		return false, nil
	}
	size, checksum, err := r.remoteFileDigest(ctx, instructionPath, marker.InstructionBytes)
	if err != nil {
		return false, err
	}
	return size == marker.InstructionBytes && checksum == marker.InstructionSHA256, nil
}

func (r *remoteSkillBundles) ImportReadOnlySkill(
	ctx context.Context,
	mount ReadOnlySkillMount,
	content io.Reader,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if content == nil {
		return errors.New("sandbox: Skill archive content is required")
	}
	if err := validateReadOnlySkillMount(mount); err != nil {
		return Permanent(err)
	}
	if r == nil || r.resources == nil || r.resources.files == nil || r.execute == nil {
		return errors.New("sandbox: remote Skill data plane is required")
	}
	prepared, cleanup, err := prepareRemoteSkill(ctx, mount, content)
	if err != nil {
		return err
	}
	defer cleanup()

	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureLayout(ctx, mount); err != nil {
		return err
	}
	target, staging, backup := remoteSkillPublishPaths(mount)
	if err := r.cleanupPublishPaths(ctx, staging, backup); err != nil {
		return err
	}
	if err := r.uploadPreparedSkill(ctx, prepared.directory, staging); err != nil {
		return err
	}
	if err := r.hardenPreparedSkill(ctx, prepared.directory, staging); err != nil {
		return err
	}
	if err := r.publish(ctx, target, staging, backup); err != nil {
		return err
	}
	if err := r.writeMarker(ctx, mount, remoteSkillMarker{
		Bundle:            skillMarker(mount),
		InstructionBytes:  prepared.instructionBytes,
		InstructionSHA256: prepared.instructionSHA256,
	}); err != nil {
		return err
	}
	present, err := r.hasReadOnlySkill(ctx, mount)
	if err != nil {
		return err
	}
	if !present {
		return errors.New("sandbox: provider did not persist the complete Skill bundle")
	}
	return nil
}

func prepareRemoteSkill(
	ctx context.Context,
	mount ReadOnlySkillMount,
	content io.Reader,
) (preparedRemoteSkill, func(), error) {
	archive, err := os.CreateTemp("", remoteSkillTempPrefix+"archive-")
	if err != nil {
		return preparedRemoteSkill{}, func() {}, fmt.Errorf(
			"sandbox: create remote Skill archive temp file: %w", err,
		)
	}
	archiveName := archive.Name()
	cleanupArchive := func() {
		_ = archive.Close()
		_ = os.Remove(archiveName)
	}
	written, err := storeVerifiedSkillArchive(archive, mount, content)
	if err != nil {
		cleanupArchive()
		return preparedRemoteSkill{}, func() {}, err
	}
	staging, err := os.MkdirTemp("", remoteSkillTempPrefix+mount.Name+"-")
	if err != nil {
		cleanupArchive()
		return preparedRemoteSkill{}, func() {}, fmt.Errorf(
			"sandbox: create remote Skill validation directory: %w", err,
		)
	}
	cleanup := func() {
		cleanupArchive()
		_ = os.RemoveAll(staging)
	}
	if err := extractCanonicalSkill(ctx, archive, written, staging, mount); err != nil {
		cleanup()
		return preparedRemoteSkill{}, func() {}, err
	}
	if err := archive.Close(); err != nil {
		cleanup()
		return preparedRemoteSkill{}, func() {}, fmt.Errorf(
			"sandbox: close remote Skill archive: %w", err,
		)
	}
	instructionPath := filepath.Join(staging, "SKILL.md")
	instructionBytes, instructionSHA256, err := localFileDigest(ctx, instructionPath)
	if err != nil {
		cleanup()
		return preparedRemoteSkill{}, func() {}, err
	}
	return preparedRemoteSkill{
		directory: staging, instructionBytes: instructionBytes,
		instructionSHA256: instructionSHA256,
	}, cleanup, nil
}

func localFileDigest(ctx context.Context, filePath string) (int64, string, error) {
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return 0, "", fmt.Errorf("sandbox: open validated Skill instructions: %w", err)
	}
	defer file.Close() //nolint:errcheck // copy error below is authoritative
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return 0, "", fmt.Errorf("sandbox: hash validated Skill instructions: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

func (r *remoteSkillBundles) remoteFileDigest(
	ctx context.Context,
	filePath string,
	expectedBytes int64,
) (int64, string, error) {
	reader, err := r.resources.files.ResourceOpen(ctx, filePath)
	if err != nil {
		return 0, "", fmt.Errorf(
			"sandbox: %s open Skill instructions: %w", r.provider, err,
		)
	}
	hash := sha256.New()
	written, copyErr := io.Copy(hash, io.LimitReader(reader, expectedBytes+1))
	closeErr := reader.Close()
	if copyErr != nil {
		return 0, "", fmt.Errorf(
			"sandbox: %s read Skill instructions: %w", r.provider, copyErr,
		)
	}
	if closeErr != nil {
		return 0, "", fmt.Errorf(
			"sandbox: %s close Skill instructions: %w", r.provider, closeErr,
		)
	}
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

func (r *remoteSkillBundles) ensureLayout(
	ctx context.Context,
	mount ReadOnlySkillMount,
) error {
	for _, directory := range []struct {
		path string
		mode int
	}{
		{remoteSkillControlRoot, 0o700},
		{remoteSkillMarkerRoot, 0o700},
		{SessionSkillsRoot, 0o755},
	} {
		if err := r.resources.ensureDirectory(ctx, directory.path, directory.mode); err != nil {
			return err
		}
	}
	parent := SessionSkillsRoot
	relative := strings.TrimPrefix(
		path.Dir(resolvedSkillRuntimePath(mount)), SessionSkillsRoot,
	)
	for _, component := range strings.Split(strings.TrimPrefix(relative, "/"), "/") {
		if component == "" || component == "." {
			continue
		}
		parent = path.Join(parent, component)
		if err := r.resources.ensureDirectory(ctx, parent, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func remoteSkillPublishPaths(mount ReadOnlySkillMount) (string, string, string) {
	target := resolvedSkillRuntimePath(mount)
	sum := sha256.Sum256([]byte(target))
	suffix := hex.EncodeToString(sum[:12])
	parent := path.Dir(target)
	return target,
		path.Join(parent, ".mango-skill-"+suffix+"-staging"),
		path.Join(parent, ".mango-skill-"+suffix+"-backup")
}

func (r *remoteSkillBundles) cleanupPublishPaths(
	ctx context.Context,
	paths ...string,
) error {
	var script strings.Builder
	script.WriteString("set -eu\n")
	for _, item := range paths {
		quoted := shellQuote(item)
		script.WriteString("if [ -e ")
		script.WriteString(quoted)
		script.WriteString(" ] || [ -L ")
		script.WriteString(quoted)
		script.WriteString(" ]; then chmod -R u+w ")
		script.WriteString(quoted)
		script.WriteString(" 2>/dev/null || true; rm -rf ")
		script.WriteString(quoted)
		script.WriteString("; fi\n")
	}
	return r.runCommand(ctx, "clean abandoned Skill staging", script.String())
}

func (r *remoteSkillBundles) uploadPreparedSkill(
	ctx context.Context,
	localRoot string,
	remoteRoot string,
) error {
	if err := r.resources.ensureDirectory(ctx, remoteRoot, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(localRoot, func(
		localPath string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if localPath == localRoot {
			return nil
		}
		relative, err := filepath.Rel(localRoot, localPath)
		if err != nil {
			return err
		}
		remotePath := path.Join(remoteRoot, filepath.ToSlash(relative))
		if entry.IsDir() {
			return r.resources.ensureDirectory(ctx, remotePath, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("sandbox: validated Skill tree contains a non-regular file")
		}
		file, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("sandbox: open validated Skill file: %w", err)
		}
		uploadErr := r.resources.files.ResourceUpload(
			ctx, remotePath, file,
			remoteFilePermissions{Mode: int(info.Mode().Perm())},
		)
		closeErr := file.Close()
		if uploadErr != nil {
			return fmt.Errorf(
				"sandbox: %s transfer Skill file: %w", r.provider, uploadErr,
			)
		}
		if closeErr != nil {
			return fmt.Errorf("sandbox: close validated Skill file: %w", closeErr)
		}
		stored, err := r.resources.files.ResourceStat(ctx, remotePath)
		if err != nil {
			return fmt.Errorf(
				"sandbox: %s inspect transferred Skill file: %w", r.provider, err,
			)
		}
		if !stored.Regular || stored.SizeBytes != info.Size() {
			return errors.New("sandbox: provider did not persist a complete Skill file")
		}
		return nil
	})
}

// hardenPreparedSkill restores the canonical executable bits after upload and
// removes every write bit. The E2B/Cube and Daytona clients currently ignore
// upload modes, so ResourceUpload alone cannot preserve executable helpers.
// Bounded batches avoid one command round-trip per file without exceeding
// provider or process command-size limits for bundles with many paths.
func (r *remoteSkillBundles) hardenPreparedSkill(
	ctx context.Context,
	localRoot string,
	remoteRoot string,
) error {
	var fileCommands []string
	var directoryCommands []string
	err := filepath.WalkDir(localRoot, func(
		localPath string,
		entry fs.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(localRoot, localPath)
		if err != nil {
			return err
		}
		remotePath := remoteRoot
		if relative != "." {
			remotePath = path.Join(remoteRoot, filepath.ToSlash(relative))
		}
		if entry.IsDir() {
			directoryCommands = append(
				directoryCommands, "chmod 0555 "+shellQuote(remotePath),
			)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("sandbox: validated Skill tree contains a non-regular file")
		}
		mode := 0o444 | int(info.Mode().Perm()&0o111)
		fileCommands = append(
			fileCommands,
			"chmod "+remotePermissionDigits(mode)+" "+shellQuote(remotePath),
		)
		return nil
	})
	if err != nil {
		return err
	}
	// Harden children before parents so every path stays traversable while the
	// script applies the complete manifest. WalkDir visits parents first.
	for index := len(directoryCommands) - 1; index >= 0; index-- {
		fileCommands = append(fileCommands, directoryCommands[index])
	}
	var batch strings.Builder
	batch.WriteString("set -eu\n")
	flush := func() error {
		if batch.Len() == len("set -eu\n") {
			return nil
		}
		if err := r.runCommand(ctx, "harden Skill staging", batch.String()); err != nil {
			return err
		}
		batch.Reset()
		batch.WriteString("set -eu\n")
		return nil
	}
	for _, command := range fileCommands {
		if batch.Len() > len("set -eu\n") &&
			batch.Len()+len(command)+1 > remoteSkillModeBatch {
			if err := flush(); err != nil {
				return err
			}
		}
		batch.WriteString(command)
		batch.WriteByte('\n')
	}
	return flush()
}

func (r *remoteSkillBundles) publish(
	ctx context.Context,
	target string,
	staging string,
	backup string,
) error {
	targetQ := shellQuote(target)
	stagingQ := shellQuote(staging)
	backupQ := shellQuote(backup)
	script := "set -u\n" +
		"chmod -R a-w " + stagingQ + " || exit $?\n" +
		"if [ -e " + targetQ + " ] || [ -L " + targetQ + " ]; then " +
		"mv " + targetQ + " " + backupQ + " || exit $?; fi\n" +
		"if mv " + stagingQ + " " + targetQ + "; then\n" +
		"  if [ -e " + backupQ + " ] || [ -L " + backupQ + " ]; then " +
		"chmod -R u+w " + backupQ + " 2>/dev/null || true; rm -rf " + backupQ + "; fi\n" +
		"else\n" +
		"  status=$?\n" +
		"  if { [ -e " + backupQ + " ] || [ -L " + backupQ + " ]; } && " +
		"{ [ ! -e " + targetQ + " ] && [ ! -L " + targetQ + " ]; }; then " +
		"mv " + backupQ + " " + targetQ + " || true; fi\n" +
		"  exit $status\n" +
		"fi\n"
	return r.runCommand(ctx, "publish Skill bundle", script)
}

func (r *remoteSkillBundles) runCommand(
	ctx context.Context,
	action string,
	script string,
) error {
	result, err := r.execute(ctx, Command{
		Path: "/bin/sh", Args: []string{"-c", script},
	})
	if err != nil {
		return fmt.Errorf("sandbox: %s %s: %w", r.provider, action, err)
	}
	if result != nil && result.TimedOut {
		return fmt.Errorf("sandbox: %s %s timed out", r.provider, action)
	}
	if result == nil || result.ExitCode != 0 {
		message := "command exited without a result"
		if result != nil {
			message = fmt.Sprintf("command exited with code %d", result.ExitCode)
			if stderr := strings.TrimSpace(string(result.Stderr)); stderr != "" {
				message += ": " + stderr
			}
		}
		return Permanent(fmt.Errorf(
			"sandbox: %s cannot %s: %s", r.provider, action, message,
		))
	}
	return nil
}

func (r *remoteSkillBundles) writeMarker(
	ctx context.Context,
	mount ReadOnlySkillMount,
	marker remoteSkillMarker,
) error {
	raw, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("sandbox: encode remote Skill marker: %w", err)
	}
	raw = append(raw, '\n')
	if len(raw) > remoteSkillMarkerLimit {
		return errors.New("sandbox: remote Skill marker exceeds its limit")
	}
	if err := r.resources.files.ResourceUpload(
		ctx,
		remoteSkillMarkerPath(mount),
		strings.NewReader(string(raw)),
		remoteFilePermissions{Mode: 0o600},
	); err != nil {
		return fmt.Errorf("sandbox: %s write Skill marker: %w", r.provider, err)
	}
	return nil
}

func (r *remoteSkillBundles) readMarker(
	ctx context.Context,
	mount ReadOnlySkillMount,
) (remoteSkillMarker, error) {
	markerPath := remoteSkillMarkerPath(mount)
	info, err := r.resources.files.ResourceStat(ctx, markerPath)
	if err != nil {
		return remoteSkillMarker{}, err
	}
	if !info.Regular || info.SizeBytes <= 0 || info.SizeBytes > remoteSkillMarkerLimit {
		return remoteSkillMarker{}, errors.New("sandbox: remote Skill marker is invalid")
	}
	reader, err := r.resources.files.ResourceOpen(ctx, markerPath)
	if err != nil {
		return remoteSkillMarker{}, fmt.Errorf(
			"sandbox: %s open Skill marker: %w", r.provider, err,
		)
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, remoteSkillMarkerLimit+1))
	closeErr := reader.Close()
	if readErr != nil {
		return remoteSkillMarker{}, fmt.Errorf(
			"sandbox: %s read Skill marker: %w", r.provider, readErr,
		)
	}
	if closeErr != nil {
		return remoteSkillMarker{}, fmt.Errorf(
			"sandbox: %s close Skill marker: %w", r.provider, closeErr,
		)
	}
	if len(raw) > remoteSkillMarkerLimit {
		return remoteSkillMarker{}, errors.New("sandbox: remote Skill marker exceeds its limit")
	}
	var marker remoteSkillMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return remoteSkillMarker{}, nil
	}
	return marker, nil
}

func remoteSkillMarkerPath(mount ReadOnlySkillMount) string {
	sum := sha256.Sum256([]byte(resolvedSkillRuntimePath(mount)))
	return path.Join(remoteSkillMarkerRoot, hex.EncodeToString(sum[:]))
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}
