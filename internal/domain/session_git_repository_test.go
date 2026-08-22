package domain

import (
	"errors"
	"testing"
)

func TestNormalizeGitRepositoryMountPath(t *testing.T) {
	got, err := NormalizeGitRepositoryMountPath(
		"https://github.com/acme/widgets.git", nil,
	)
	if err != nil || got != "/workspace/widgets" {
		t.Fatalf("default mount = %q, %v", got, err)
	}
	custom := "/workspace/projects/widgets"
	got, err = NormalizeGitRepositoryMountPath(
		"https://git.example.com/acme/widgets", &custom,
	)
	if err != nil || got != custom {
		t.Fatalf("custom mount = %q, %v", got, err)
	}
	for _, invalid := range []string{
		"/workspace", "/tmp/widgets", "/workspace/skills",
		"/workspace/skills/nested", "/workspace/../tmp",
	} {
		invalid := invalid
		if _, err := NormalizeGitRepositoryMountPath(
			"https://github.com/acme/widgets.git", &invalid,
		); err == nil {
			t.Fatalf("mount %q accepted", invalid)
		}
	}
	if _, err := NormalizeGitRepositoryMountPath(
		"https://github.com/acme/skills.git", nil,
	); err == nil {
		t.Fatal("default repository mount overlapped the custom Skill directory")
	}
}

func TestValidateGitRepositoryURLRejectsCredentialsAndNonHTTPS(t *testing.T) {
	for _, invalid := range []string{
		"http://github.com/acme/widgets.git",
		"https://token@github.com/acme/widgets.git",
		"https://github.com/acme/widgets.git?token=secret",
		"https://github.com/acme/widgets.git?",
		"https://github.com/acme/widgets.git#main",
		"https://github.com/acme/widgets.git#",
		"ssh://git@github.com/acme/widgets.git",
		"https://github.com/",
	} {
		if err := ValidateGitRepositoryURL(invalid); err == nil {
			t.Fatalf("URL %q accepted", invalid)
		}
	}
	if err := ValidateGitRepositoryURL("https://github.com/acme/widgets.git"); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeGitRepositoryCheckout(t *testing.T) {
	typeName, value, err := NormalizeGitRepositoryCheckout("", "")
	if err != nil || typeName != "" || value != "" {
		t.Fatalf("default checkout = %q/%q, %v", typeName, value, err)
	}
	typeName, value, err = NormalizeGitRepositoryCheckout("branch", "feature/safe-name")
	if err != nil || typeName != "branch" || value != "feature/safe-name" {
		t.Fatalf("branch checkout = %q/%q, %v", typeName, value, err)
	}
	upper := "0123456789ABCDEF0123456789ABCDEF01234567"
	typeName, value, err = NormalizeGitRepositoryCheckout("commit", upper)
	if err != nil || typeName != "commit" || value != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("commit checkout = %q/%q, %v", typeName, value, err)
	}
	for _, branch := range []string{"../main", ".hidden", "-option", "main..next", "main.lock", "main~1"} {
		_, _, err := NormalizeGitRepositoryCheckout("branch", branch)
		var domainErr *DomainError
		if !errors.As(err, &domainErr) || domainErr.Kind != KindValidation {
			t.Fatalf("branch %q error = %v", branch, err)
		}
	}
}
