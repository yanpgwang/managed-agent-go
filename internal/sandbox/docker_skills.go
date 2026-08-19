package sandbox

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yanpgwang/mango/internal/domain"
)

const (
	maxDockerSkillArchiveBytes int64 = 30_000_000
	maxDockerSkillFiles              = 1000
	dockerSkillTempPrefix            = ".mango-skill-"
)

func validateReadOnlySkillMount(mount ReadOnlySkillMount) error {
	if err := validateResourceIdentity(mount.Identity); err != nil {
		return err
	}
	if !validSkillRuntimeName(mount.Name) {
		return errors.New("sandbox: Skill runtime name is invalid")
	}
	if !validSkillRuntimePath(resolvedSkillRuntimePath(mount), mount.Name) {
		return errors.New("sandbox: Skill runtime path is invalid")
	}
	if !validSkillArchiveRoot(mount.ArchiveRoot, mount.Name) {
		return errors.New("sandbox: Skill archive root is invalid")
	}
	if mount.SizeBytes < 0 || mount.SizeBytes >= maxDockerSkillArchiveBytes {
		return errors.New("sandbox: Skill archive size is invalid")
	}
	if mount.UncompressedSizeBytes != domain.UnknownSkillUncompressedSize &&
		(mount.UncompressedSizeBytes < 0 ||
			mount.UncompressedSizeBytes >= maxDockerSkillArchiveBytes) {
		return errors.New("sandbox: Skill expanded size is invalid")
	}
	decoded, err := hex.DecodeString(mount.ChecksumSHA256)
	if err != nil || len(decoded) != sha256.Size ||
		strings.ToLower(mount.ChecksumSHA256) != mount.ChecksumSHA256 {
		return errors.New("sandbox: Skill checksum must be a lowercase SHA-256 digest")
	}
	return nil
}

func resolvedSkillRuntimePath(mount ReadOnlySkillMount) string {
	if mount.RuntimePath != "" {
		return mount.RuntimePath
	}
	return domain.SessionSkillsRoot + "/" + mount.Name
}

func validSkillRuntimePath(runtimePath string, name string) bool {
	prefix := domain.SessionSkillsRoot + "/"
	if !strings.HasPrefix(runtimePath, prefix) || path.Clean(runtimePath) != runtimePath ||
		strings.ContainsRune(runtimePath, '\x00') {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(runtimePath, prefix), "/")
	if len(parts) == 1 {
		return parts[0] == name
	}
	if len(parts) != 3 || parts[0] != ".agents" || parts[2] != name ||
		len(parts[1]) != 24 {
		return false
	}
	_, err := hex.DecodeString(parts[1])
	return err == nil
}

func validSkillRuntimeName(name string) bool {
	if name == "" || len(name) > 64 || !utf8.ValidString(name) {
		return false
	}
	for _, character := range name {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validSkillArchiveRoot(root string, name string) bool {
	if root == "" || len(root) > 64 || !utf8.ValidString(root) ||
		strings.ContainsAny(root, "/\\") {
		return false
	}
	for _, character := range root {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' {
			continue
		}
		return false
	}
	return strings.ReplaceAll(strings.ToLower(root), "_", "-") == name
}

func (s *dockerSandbox) skillPaths(mount ReadOnlySkillMount) (string, string, error) {
	if !s.skillMountReady || s.resourceRoot == "" {
		return "", "", Permanent(errors.New(
			"sandbox: Docker container predates the read-only Skill mount; recreate its sandbox",
		))
	}
	runtimePath := resolvedSkillRuntimePath(mount)
	relative := strings.TrimPrefix(runtimePath, domain.SessionSkillsRoot+"/")
	target := filepath.Join(
		s.resourceRoot, dockerResourceSkillsDir, filepath.FromSlash(relative),
	)
	sum := sha256.Sum256([]byte(runtimePath))
	marker := filepath.Join(
		s.resourceRoot,
		dockerResourceStateDir,
		"skill-"+hex.EncodeToString(sum[:]),
	)
	return target, marker, nil
}

func skillMarker(mount ReadOnlySkillMount) string {
	return mount.Identity + "\n" + mount.Name + "\n" +
		resolvedSkillRuntimePath(mount) + "\n" + mount.ArchiveRoot + "\n" +
		strconv.FormatInt(mount.SizeBytes, 10) + "\n" +
		strconv.FormatInt(mount.UncompressedSizeBytes, 10) + "\n" +
		mount.ChecksumSHA256 + "\n"
}

func (s *dockerSandbox) HasReadOnlySkill(
	ctx context.Context,
	mount ReadOnlySkillMount,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := validateReadOnlySkillMount(mount); err != nil {
		return false, Permanent(err)
	}
	if !s.skillMountReady || s.resourceRoot == "" {
		return false, nil
	}
	target, marker, err := s.skillPaths(mount)
	if err != nil {
		return false, err
	}
	unlock, err := s.acquireResourceReadLock(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	stored, err := os.ReadFile(marker)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sandbox: read Skill marker: %w", err)
	}
	if string(stored) != skillMarker(mount) {
		return false, nil
	}
	return validMaterializedSkillTree(ctx, target, mount.UncompressedSizeBytes)
}

func validMaterializedSkillTree(
	ctx context.Context,
	target string,
	expectedBytes int64,
) (bool, error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("sandbox: inspect Skill directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	limit := expectedBytes
	if limit == domain.UnknownSkillUncompressedSize {
		limit = maxDockerSkillArchiveBytes - 1
	}
	var total int64
	foundSkillMD := false
	err = filepath.WalkDir(target, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if current == target {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("unexpected symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if !entryInfo.Mode().IsRegular() || entryInfo.Mode().Perm()&0o222 != 0 {
			return errors.New("unexpected writable or non-regular file")
		}
		if entryInfo.Size() > limit-total {
			return errors.New("expanded size mismatch")
		}
		total += entryInfo.Size()
		if current == filepath.Join(target, "SKILL.md") {
			foundSkillMD = true
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, nil
	}
	return foundSkillMD &&
		(expectedBytes == domain.UnknownSkillUncompressedSize || total == expectedBytes), nil
}

func (s *dockerSandbox) ImportReadOnlySkill(
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
	target, marker, err := s.skillPaths(mount)
	if err != nil {
		return err
	}
	unlock, err := s.acquireResourceLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	skillsRoot := filepath.Join(s.resourceRoot, dockerResourceSkillsDir)
	if err := sweepSkillTemps(skillsRoot); err != nil {
		return err
	}
	archive, err := os.CreateTemp(
		filepath.Join(s.resourceRoot, dockerResourceStateDir),
		dockerSkillTempPrefix+"archive-",
	)
	if err != nil {
		return fmt.Errorf("sandbox: create Skill archive temp file: %w", err)
	}
	archiveName := archive.Name()
	defer os.Remove(archiveName) //nolint:errcheck // best-effort retry cleanup

	hash := sha256.New()
	limited := &io.LimitedReader{R: content, N: mount.SizeBytes + 1}
	written, copyErr := io.Copy(io.MultiWriter(archive, hash), limited)
	if copyErr != nil {
		archive.Close() //nolint:errcheck // copy error is authoritative
		return fmt.Errorf("sandbox: stream Skill archive: %w", copyErr)
	}
	if written != mount.SizeBytes {
		archive.Close() //nolint:errcheck // size mismatch is authoritative
		return fmt.Errorf(
			"sandbox: Skill archive size mismatch: received %d bytes, expected %d",
			written, mount.SizeBytes,
		)
	}
	if checksum := hex.EncodeToString(hash.Sum(nil)); checksum != mount.ChecksumSHA256 {
		archive.Close() //nolint:errcheck // checksum mismatch is authoritative
		return errors.New("sandbox: Skill archive checksum mismatch")
	}
	if err := archive.Sync(); err != nil {
		archive.Close() //nolint:errcheck // sync error is authoritative
		return fmt.Errorf("sandbox: sync Skill archive: %w", err)
	}

	staging, err := os.MkdirTemp(skillsRoot, dockerSkillTempPrefix+mount.Name+"-")
	if err != nil {
		archive.Close() //nolint:errcheck // create error is authoritative
		return fmt.Errorf("sandbox: create Skill staging directory: %w", err)
	}
	stagingPublished := false
	defer func() {
		if !stagingPublished {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := extractCanonicalSkill(ctx, archive, written, staging, mount); err != nil {
		archive.Close() //nolint:errcheck // extraction error is authoritative
		return err
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("sandbox: close Skill archive: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("sandbox: replace stale Skill directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("sandbox: create Skill scope directory: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return fmt.Errorf("sandbox: publish Skill directory: %w", err)
	}
	stagingPublished = true
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	if err := writeResourceMarker(marker, skillMarker(mount)); err != nil {
		return err
	}
	return nil
}

func sweepSkillTemps(skillsRoot string) error {
	entries, err := os.ReadDir(skillsRoot)
	if err != nil {
		return fmt.Errorf("sandbox: list Skill staging directory: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), dockerSkillTempPrefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(skillsRoot, entry.Name())); err != nil {
			return fmt.Errorf("sandbox: remove abandoned Skill staging directory: %w", err)
		}
	}
	return nil
}

func extractCanonicalSkill(
	ctx context.Context,
	archive *os.File,
	size int64,
	staging string,
	mount ReadOnlySkillMount,
) error {
	reader, err := zip.NewReader(archive, size)
	if err != nil {
		return errors.New("sandbox: stored Skill archive is invalid")
	}
	if len(reader.File) == 0 || len(reader.File) > maxDockerSkillFiles {
		return errors.New("sandbox: stored Skill archive has an invalid file count")
	}
	seen := make(map[string]struct{}, len(reader.File))
	expandedLimit := mount.UncompressedSizeBytes
	if expandedLimit == domain.UnknownSkillUncompressedSize {
		expandedLimit = maxDockerSkillArchiveBytes - 1
	}
	var total int64
	foundSkillMD := false
	for _, entry := range reader.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := skillArchiveRelativePath(entry.Name, mount.ArchiveRoot)
		if err != nil {
			return err
		}
		if _, exists := seen[relative]; exists {
			return errors.New("sandbox: stored Skill archive contains duplicate paths")
		}
		seen[relative] = struct{}{}
		if entry.FileInfo().IsDir() || !entry.Mode().IsRegular() {
			return errors.New("sandbox: stored Skill archive contains a non-regular file")
		}
		if entry.UncompressedSize64 > uint64(expandedLimit-total) {
			return errors.New("sandbox: stored Skill archive expanded size mismatch")
		}
		target := filepath.Join(staging, filepath.FromSlash(relative))
		if err := secureMkdirAll(staging, filepath.Dir(target)); err != nil {
			return err
		}
		opened, err := entry.Open()
		if err != nil {
			return errors.New("sandbox: stored Skill archive is invalid")
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			opened.Close() //nolint:errcheck // create error is authoritative
			return fmt.Errorf("sandbox: create extracted Skill file: %w", err)
		}
		written, copyErr := io.Copy(output, io.LimitReader(opened, int64(entry.UncompressedSize64)+1))
		closeInputErr := opened.Close()
		if copyErr != nil || closeInputErr != nil || written != int64(entry.UncompressedSize64) {
			output.Close() //nolint:errcheck // archive error is authoritative
			return errors.New("sandbox: stored Skill archive is invalid")
		}
		if err := output.Sync(); err != nil {
			output.Close() //nolint:errcheck // sync error is authoritative
			return fmt.Errorf("sandbox: sync extracted Skill file: %w", err)
		}
		mode := os.FileMode(0o444) | (entry.Mode().Perm() & 0o111)
		if err := output.Chmod(mode); err != nil {
			output.Close() //nolint:errcheck // chmod error is authoritative
			return fmt.Errorf("sandbox: make extracted Skill file read-only: %w", err)
		}
		if err := output.Close(); err != nil {
			return fmt.Errorf("sandbox: close extracted Skill file: %w", err)
		}
		total += written
		if relative == "SKILL.md" {
			foundSkillMD = true
		}
	}
	if !foundSkillMD ||
		(mount.UncompressedSizeBytes != domain.UnknownSkillUncompressedSize &&
			total != mount.UncompressedSizeBytes) {
		return errors.New("sandbox: stored Skill archive does not match its metadata")
	}
	return nil
}

func skillArchiveRelativePath(raw string, archiveRoot string) (string, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.Contains(raw, "\\") ||
		strings.HasPrefix(raw, "/") || strings.ContainsRune(raw, '\x00') {
		return "", errors.New("sandbox: stored Skill archive contains an unsafe path")
	}
	cleaned := path.Clean(raw)
	if cleaned != raw || strings.HasSuffix(raw, "/") {
		return "", errors.New("sandbox: stored Skill archive contains an unsafe path")
	}
	prefix := archiveRoot + "/"
	if !strings.HasPrefix(cleaned, prefix) {
		return "", errors.New("sandbox: stored Skill archive root does not match metadata")
	}
	relative := strings.TrimPrefix(cleaned, prefix)
	if relative == "" || relative == "." || strings.HasPrefix(relative, "../") ||
		strings.Contains(relative, "/../") {
		return "", errors.New("sandbox: stored Skill archive contains an unsafe path")
	}
	return relative, nil
}

var _ SkillBundleSandbox = (*dockerSandbox)(nil)
