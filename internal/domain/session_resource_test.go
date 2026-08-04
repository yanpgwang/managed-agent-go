package domain

import (
	"strings"
	"testing"
)

func TestNormalizeSessionFileMountPath(t *testing.T) {
	custom := "/reports/output.csv"
	canonical := "/mnt/session/uploads/already.txt"
	parent := "/safe/../escape"
	relative := "relative.txt"
	control := "/reports/line\nbreak.txt"
	root := SessionUploadsRoot
	longFinalPath := "/" + strings.Repeat("a/", 510) + "end"
	longComponent := "/" + strings.Repeat("b", MaxSessionFileMountComponentBytes+1)
	tests := []struct {
		name      string
		requested *string
		want      string
		wantError bool
	}{
		{name: "default", want: "/mnt/session/uploads/file_a"},
		{name: "custom", requested: &custom, want: "/mnt/session/uploads/reports/output.csv"},
		{name: "canonical", requested: &canonical, want: canonical},
		{name: "parent traversal", requested: &parent, wantError: true},
		{name: "relative", requested: &relative, wantError: true},
		{name: "control character", requested: &control, wantError: true},
		{name: "uploads root", requested: &root, wantError: true},
		{name: "normalized path too long", requested: &longFinalPath, wantError: true},
		{name: "component too long", requested: &longComponent, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeSessionFileMountPath("file_a", test.requested)
			if test.wantError {
				if err == nil {
					t.Fatalf("NormalizeSessionFileMountPath = %q, want error", got)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("NormalizeSessionFileMountPath = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestSessionFileMountPathsConflict(t *testing.T) {
	tests := []struct {
		left, right string
		want        bool
	}{
		{"/mnt/session/uploads/a", "/mnt/session/uploads/a", true},
		{"/mnt/session/uploads/a", "/mnt/session/uploads/a/child", true},
		{"/mnt/session/uploads/a/child", "/mnt/session/uploads/a", true},
		{"/mnt/session/uploads/a", "/mnt/session/uploads/ab", false},
	}
	for _, test := range tests {
		if got := SessionFileMountPathsConflict(test.left, test.right); got != test.want {
			t.Fatalf("SessionFileMountPathsConflict(%q, %q) = %t, want %t", test.left, test.right, got, test.want)
		}
	}
}

func TestNormalizeSessionFileMountPathRejectsEscapingFileID(t *testing.T) {
	if got, err := NormalizeSessionFileMountPath("..", nil); err == nil {
		t.Fatalf("NormalizeSessionFileMountPath escaping ID = %q, want error", got)
	}
}

func TestNormalizeSessionFileMountPathRejectsOversizedDefaultFileID(t *testing.T) {
	fileID := strings.Repeat("f", MaxSessionFileMountComponentBytes+1)
	if got, err := NormalizeSessionFileMountPath(fileID, nil); err == nil {
		t.Fatalf("NormalizeSessionFileMountPath oversized ID = %q, want error", got)
	}
}

func TestNormalizeSessionFileMountPathRejectsInvalidDefaultFileID(t *testing.T) {
	for _, fileID := range []string{"file/nested", "file\ncontrol", string([]byte{0xff})} {
		if got, err := NormalizeSessionFileMountPath(fileID, nil); err == nil {
			t.Fatalf("NormalizeSessionFileMountPath(%q) = %q, want error", fileID, got)
		}
	}
}
