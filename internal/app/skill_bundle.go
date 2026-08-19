package app

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yanpgwang/mango/internal/domain"
	"gopkg.in/yaml.v3"
)

const (
	// MaxSkillUploadBytes follows the upstream custom Skill requirement that a
	// complete upload remain below 30 MB.
	MaxSkillUploadBytes int64 = 30_000_000
	MaxSkillFiles       int   = 1000
)

var (
	skillNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)
	xmlTagPattern    = regexp.MustCompile(`<[^>]+>`)
)

type SkillUploadFile struct {
	Filename string
	Body     []byte
}

type preparedSkillBundle struct {
	Archive               []byte
	Name                  string
	Description           string
	Directory             string
	UncompressedSizeBytes int64
}

type skillBundleEntry struct {
	name string
	body []byte
	mode fs.FileMode
}

func prepareSkillBundle(files []SkillUploadFile) (preparedSkillBundle, error) {
	if len(files) == 0 {
		return preparedSkillBundle{}, domain.Validation("files must contain a Skill bundle")
	}
	if len(files) > MaxSkillFiles {
		return preparedSkillBundle{}, domain.Validation("Skill bundle contains too many files")
	}

	var (
		entries []skillBundleEntry
		err     error
	)
	if len(files) == 1 && strings.EqualFold(path.Ext(files[0].Filename), ".zip") {
		entries, err = readSkillZip(files[0].Body)
	} else {
		entries, err = readSkillParts(files)
	}
	if err != nil {
		return preparedSkillBundle{}, err
	}
	return validateAndArchiveSkillEntries(entries)
}

func readSkillZip(body []byte) ([]skillBundleEntry, error) {
	if int64(len(body)) >= MaxSkillUploadBytes {
		return nil, domain.TooLarge("Skill upload must be smaller than 30 MB")
	}
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, domain.Validation("Skill zip archive is invalid")
	}
	if len(reader.File) > MaxSkillFiles {
		return nil, domain.Validation("Skill bundle contains too many files")
	}
	entries := make([]skillBundleEntry, 0, len(reader.File))
	var total int64
	for _, file := range reader.File {
		name, isDirectory, err := validateSkillPath(file.Name)
		if err != nil {
			return nil, err
		}
		mode := file.Mode()
		if isDirectory {
			continue
		}
		if !mode.IsRegular() {
			return nil, domain.Validation("Skill bundle may contain only regular files")
		}
		if file.UncompressedSize64 >= uint64(MaxSkillUploadBytes) ||
			total >= MaxSkillUploadBytes-int64(file.UncompressedSize64) {
			return nil, domain.TooLarge("Skill upload must be smaller than 30 MB")
		}
		opened, err := file.Open()
		if err != nil {
			return nil, domain.Validation("Skill zip archive is invalid")
		}
		data, readErr := io.ReadAll(io.LimitReader(opened, MaxSkillUploadBytes-total))
		closeErr := opened.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) != int64(file.UncompressedSize64) {
			return nil, domain.Validation("Skill zip archive is invalid")
		}
		total += int64(len(data))
		entries = append(entries, skillBundleEntry{name: name, body: data, mode: mode.Perm()})
	}
	return entries, nil
}

func readSkillParts(files []SkillUploadFile) ([]skillBundleEntry, error) {
	entries := make([]skillBundleEntry, 0, len(files))
	var total int64
	for _, file := range files {
		name, isDirectory, err := validateSkillPath(file.Filename)
		if err != nil {
			return nil, err
		}
		if isDirectory {
			return nil, domain.Validation("multipart Skill entries must be files")
		}
		if int64(len(file.Body)) >= MaxSkillUploadBytes ||
			total >= MaxSkillUploadBytes-int64(len(file.Body)) {
			return nil, domain.TooLarge("Skill upload must be smaller than 30 MB")
		}
		total += int64(len(file.Body))
		entries = append(entries, skillBundleEntry{name: name, body: file.Body, mode: 0o644})
	}
	return entries, nil
}

func validateSkillPath(raw string) (string, bool, error) {
	if raw == "" || !utf8.ValidString(raw) || strings.Contains(raw, "\\") || strings.HasPrefix(raw, "/") {
		return "", false, domain.Validation("Skill bundle contains an unsafe path")
	}
	isDirectory := strings.HasSuffix(raw, "/")
	trimmed := strings.TrimSuffix(raw, "/")
	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned != trimmed || strings.HasPrefix(cleaned, "../") ||
		strings.Contains(cleaned, "/../") || strings.ContainsRune(cleaned, '\x00') {
		return "", false, domain.Validation("Skill bundle contains an unsafe path")
	}
	return cleaned, isDirectory, nil
}

func validateAndArchiveSkillEntries(entries []skillBundleEntry) (preparedSkillBundle, error) {
	if len(entries) == 0 {
		return preparedSkillBundle{}, domain.Validation("Skill bundle contains no files")
	}
	seen := make(map[string]struct{}, len(entries))
	directory := ""
	var skillMarkdown []byte
	var uncompressedSize int64
	for _, entry := range entries {
		if _, exists := seen[entry.name]; exists {
			return preparedSkillBundle{}, domain.Validation("Skill bundle contains duplicate paths")
		}
		seen[entry.name] = struct{}{}
		parts := strings.Split(entry.name, "/")
		if len(parts) < 2 || parts[0] == "" {
			return preparedSkillBundle{}, domain.Validation("Skill files must share one top-level directory")
		}
		if directory == "" {
			directory = parts[0]
		} else if directory != parts[0] {
			return preparedSkillBundle{}, domain.Validation("Skill files must share one top-level directory")
		}
		if len(parts) == 2 && parts[1] == "SKILL.md" {
			skillMarkdown = entry.body
		}
		uncompressedSize += int64(len(entry.body))
	}
	if skillMarkdown == nil {
		return preparedSkillBundle{}, domain.Validation("Skill bundle must contain SKILL.md at its root")
	}
	name, description, err := parseSkillFrontmatter(skillMarkdown)
	if err != nil {
		return preparedSkillBundle{}, err
	}
	if normalizeSkillDirectory(directory) != name {
		return preparedSkillBundle{}, domain.Validation("Skill directory must match the SKILL.md name")
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	fixedTime := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate, Modified: fixedTime}
		header.SetMode(entry.mode.Perm())
		part, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return preparedSkillBundle{}, fmt.Errorf("prepare Skill archive: %w", err)
		}
		if _, err := part.Write(entry.body); err != nil {
			_ = writer.Close()
			return preparedSkillBundle{}, fmt.Errorf("prepare Skill archive: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return preparedSkillBundle{}, fmt.Errorf("prepare Skill archive: %w", err)
	}
	if int64(archive.Len()) >= MaxSkillUploadBytes {
		return preparedSkillBundle{}, domain.TooLarge("Skill upload must be smaller than 30 MB")
	}
	return preparedSkillBundle{
		Archive: archive.Bytes(), Name: name, Description: description, Directory: directory,
		UncompressedSizeBytes: uncompressedSize,
	}, nil
}

func parseSkillFrontmatter(body []byte) (string, string, error) {
	if !utf8.Valid(body) {
		return "", "", domain.Validation("SKILL.md must be valid UTF-8")
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return "", "", domain.Validation("SKILL.md must begin with YAML frontmatter")
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return "", "", domain.Validation("SKILL.md YAML frontmatter is not closed")
	}
	end += 4
	if end > 64<<10 {
		return "", "", domain.Validation("SKILL.md YAML frontmatter is too large")
	}
	after := text[end+4:]
	if after != "" && !strings.HasPrefix(after, "\n") {
		return "", "", domain.Validation("SKILL.md YAML frontmatter is not closed")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(text[4:end]), &metadata); err != nil {
		return "", "", domain.Validation("SKILL.md YAML frontmatter is invalid")
	}
	name := strings.TrimSpace(metadata.Name)
	description := strings.TrimSpace(metadata.Description)
	if name == "" || utf8.RuneCountInString(name) > 64 || !skillNamePattern.MatchString(name) ||
		strings.Contains(name, "anthropic") || strings.Contains(name, "claude") || xmlTagPattern.MatchString(name) {
		return "", "", domain.Validation("SKILL.md name is invalid")
	}
	if description == "" || utf8.RuneCountInString(description) > 1024 || xmlTagPattern.MatchString(description) {
		return "", "", domain.Validation("SKILL.md description is invalid")
	}
	return name, description, nil
}

func normalizeSkillDirectory(directory string) string {
	return strings.ReplaceAll(strings.ToLower(directory), "_", "-")
}
