package obs

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseFormat(t *testing.T) {
	cases := map[string]string{
		"":       FormatText,
		"text":   FormatText,
		"json":   FormatJSON,
		"JSON":   FormatJSON,
		" json ": FormatJSON,
	}
	for input, want := range cases {
		got, err := ParseFormat(input)
		if err != nil {
			t.Fatalf("ParseFormat(%q) = %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseFormat(%q) = %q, want %q", input, got, want)
		}
	}
	if _, err := ParseFormat("logfmt"); err == nil {
		t.Fatal("ParseFormat accepted an unknown format; a typo must not silently " +
			"change the shape of production logs")
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"WARNING": slog.LevelWarn,
		"error":   slog.LevelError,
	}
	for input, want := range cases {
		got, err := ParseLevel(input)
		if err != nil {
			t.Fatalf("ParseLevel(%q) = %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", input, got, want)
		}
	}
	if _, err := ParseLevel("verbose"); err == nil {
		t.Fatal("ParseLevel accepted an unknown level")
	}
}

func TestNewLogger_JSONHandlerCarriesRoleAndRespectsLevel(t *testing.T) {
	var sink bytes.Buffer
	logger, err := NewLogger(&sink, Options{
		Format: FormatJSON, Level: slog.LevelWarn, Role: "orchestrate",
	})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("suppressed below the configured level")
	logger.Warn("relay cycle failed", slog.String("session_id", "sesn_1"))

	lines := strings.Split(strings.TrimSpace(sink.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("emitted %d records, want only the warn record: %q", len(lines), sink.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("record is not JSON: %v (%s)", err, lines[0])
	}
	if record["role"] != "orchestrate" {
		t.Fatalf("role = %v, want orchestrate", record["role"])
	}
	if record["session_id"] != "sesn_1" {
		t.Fatalf("session_id = %v, want sesn_1", record["session_id"])
	}
	if record["msg"] != "relay cycle failed" {
		t.Fatalf("msg = %v", record["msg"])
	}
}

func TestNewLogger_TextHandlerIsTheDefault(t *testing.T) {
	var sink bytes.Buffer
	logger, err := NewLogger(&sink, Options{Role: "serve"})
	if err != nil {
		t.Fatal(err)
	}
	logger.Info("http listener started", slog.String("addr", "127.0.0.1:8080"))
	out := sink.String()
	if !strings.Contains(out, "role=serve") || !strings.Contains(out, "addr=127.0.0.1:8080") {
		t.Fatalf("text output = %q, want key=value pairs", out)
	}
	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("default format emitted JSON: %q", out)
	}
}

func TestNewLogger_RejectsUnknownFormat(t *testing.T) {
	if _, err := NewLogger(&bytes.Buffer{}, Options{Format: "xml"}); err == nil {
		t.Fatal("NewLogger accepted an unknown format")
	}
}

func TestConfigure_InstallsTheDefaultLogger(t *testing.T) {
	previous := slog.Default()
	t.Cleanup(func() { slog.SetDefault(previous) })

	var sink bytes.Buffer
	if _, err := Configure(&sink, Options{Format: FormatJSON, Role: "serve"}); err != nil {
		t.Fatal(err)
	}
	// Packages such as internal/pg and internal/temporal log through the
	// package-level slog functions; Configure is what gives them the process
	// handler.
	slog.Info("live event notification failed", slog.String("component", "pg"))
	if !strings.Contains(sink.String(), `"component":"pg"`) {
		t.Fatalf("default logger did not receive the record: %q", sink.String())
	}
}
