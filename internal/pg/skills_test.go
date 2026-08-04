package pg

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

func TestSkillRepository_ImmutableLifecyclePagingAndDeleteGuard(t *testing.T) {
	store := testStore(t)
	repo := NewSkillRepository(store)
	ctx := context.Background()
	base := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	skill := domain.Skill{
		ID: "skill_repo", CreatedAt: base, UpdatedAt: base,
		DisplayTitle: "Repository Skill", Source: "custom", TitleExplicit: true,
	}
	first := repositorySkillVersion(skill.ID, "100", base, true)
	if err := repo.BeginSkill(ctx, skill, first); err != nil {
		t.Fatalf("BeginSkill: %v", err)
	}
	if _, err := repo.GetSkill(ctx, skill.ID); err == nil {
		t.Fatal("creating Skill became visible before its archive committed")
	}
	readySkill, readyFirst, err := repo.CompleteVersion(ctx, skill.ID, first.Version, app.BlobInfo{
		SizeBytes: 10, ChecksumSHA256: "first",
	})
	if err != nil {
		t.Fatalf("CompleteVersion: %v", err)
	}
	if readySkill.LatestVersion != first.Version || readyFirst.State != domain.SkillVersionReady {
		t.Fatalf("completed = %+v, %+v", readySkill, readyFirst)
	}

	duplicate := skill
	duplicate.ID = "skill_duplicate"
	duplicate.CreatedAt = base.Add(time.Second)
	duplicate.UpdatedAt = duplicate.CreatedAt
	duplicateVersion := repositorySkillVersion(duplicate.ID, "150", duplicate.CreatedAt, true)
	if err := repo.BeginSkill(ctx, duplicate, duplicateVersion); err == nil {
		t.Fatal("duplicate explicit display_title was accepted")
	}

	second := repositorySkillVersion(skill.ID, "200", base.Add(2*time.Second), false)
	third := repositorySkillVersion(skill.ID, "300", base.Add(3*time.Second), false)
	for _, item := range []domain.SkillVersion{second, third} {
		if err := repo.BeginVersion(ctx, item); err != nil {
			t.Fatalf("BeginVersion %s: %v", item.Version, err)
		}
	}
	// Complete the newer Version first and the older Version last. Completion
	// order must not let an older upload steal latest_version.
	for _, item := range []domain.SkillVersion{third, second} {
		if _, _, err := repo.CompleteVersion(ctx, skill.ID, item.Version, app.BlobInfo{
			SizeBytes: 10, ChecksumSHA256: item.Version,
		}); err != nil {
			t.Fatalf("CompleteVersion %s: %v", item.Version, err)
		}
	}
	latest, err := repo.GetSkill(ctx, skill.ID)
	if err != nil || latest.LatestVersion != third.Version {
		t.Fatalf("latest after reverse completion = %+v, %v", latest, err)
	}
	page, err := repo.ListVersions(ctx, skill.ID, app.SkillVersionListQuery{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	assertSkillVersions(t, page.Versions, "300", "200")
	if !page.HasNext {
		t.Fatal("first Version page missing next page")
	}
	last := page.Versions[len(page.Versions)-1]
	next, err := repo.ListVersions(ctx, skill.ID, app.SkillVersionListQuery{
		Limit: 2, After: &app.ResourcePageBoundary{CreatedAt: last.CreatedAt, ID: last.Version},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertSkillVersions(t, next.Versions, "100")

	if _, err := repo.DeleteSkill(ctx, skill.ID); err == nil {
		t.Fatal("Skill with Versions was deleted")
	} else {
		var domainErr *domain.DomainError
		if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindValidation {
			t.Fatalf("delete guard error = %T %v", err, err)
		}
	}

	for _, version := range []string{"300", "200", "100"} {
		deleting, err := repo.BeginDeleteVersion(ctx, skill.ID, version)
		if err != nil || deleting.State != domain.SkillVersionDeleting {
			t.Fatalf("BeginDeleteVersion %s = %+v, %v", version, deleting, err)
		}
		if _, err := repo.GetVersion(ctx, skill.ID, version); err == nil {
			t.Fatalf("deleting Version %s remains visible", version)
		}
		if err := repo.RemoveIncompleteVersion(ctx, skill.ID, version); err != nil {
			t.Fatal(err)
		}
	}
	empty, err := repo.GetSkill(ctx, skill.ID)
	if err != nil || empty.LatestVersion != "" {
		t.Fatalf("empty Skill = %+v, %v", empty, err)
	}
	if _, err := repo.DeleteSkill(ctx, skill.ID); err != nil {
		t.Fatalf("DeleteSkill: %v", err)
	}
}

func repositorySkillVersion(skillID, version string, createdAt time.Time, initial bool) domain.SkillVersion {
	return domain.SkillVersion{
		ID: version, SkillID: skillID, Version: version, CreatedAt: createdAt,
		Description: "Performs repository tests when requested.",
		Directory:   "repository-skill", Name: "repository-skill",
		BlobKey: "skills/" + skillID + "/" + version + ".zip",
		State:   domain.SkillVersionUploading, Initial: initial,
	}
}

func assertSkillVersions(t *testing.T, items []domain.SkillVersion, want ...string) {
	t.Helper()
	if len(items) != len(want) {
		t.Fatalf("Version count = %d, want %d: %+v", len(items), len(want), items)
	}
	for index, version := range want {
		if items[index].Version != version {
			t.Fatalf("Version[%d] = %s, want %s", index, items[index].Version, version)
		}
	}
}

func TestSessionSkillPinsGuardVersionDeletionAndRollbackMissingReferences(t *testing.T) {
	store := testStore(t)
	repo := NewSkillRepository(store)
	ctx := context.Background()
	base := time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC)
	skill := domain.Skill{
		ID: "skill_session_pin", CreatedAt: base, UpdatedAt: base,
		DisplayTitle: "Session Pin", Source: "custom", TitleExplicit: true,
	}
	version := repositorySkillVersion(skill.ID, "400", base, true)
	if err := repo.BeginSkill(ctx, skill, version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CompleteVersion(ctx, skill.ID, version.Version, app.BlobInfo{
		SizeBytes: 10, ChecksumSHA256: "pin",
	}); err != nil {
		t.Fatal(err)
	}

	session := newSession("sesn_skill_pin")
	session.AgentSnapshot.Skills = []domain.SkillReference{{
		Type: "custom", SkillID: skill.ID, Version: version.Version,
	}}
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatalf("CreateSession with Skill pin: %v", err)
	}
	persisted, err := store.GetSession(ctx, session.ID)
	if err != nil || len(persisted.AgentSnapshot.Skills) != 1 ||
		persisted.AgentSnapshot.Skills[0].Version != version.Version {
		t.Fatalf("persisted Session Skill pin = %+v, %v", persisted.AgentSnapshot.Skills, err)
	}
	if _, err := repo.BeginDeleteVersion(ctx, skill.ID, version.Version); err == nil {
		t.Fatal("deleted Skill Version referenced by Session")
	} else {
		var domainErr *domain.DomainError
		if !errors.As(err, &domainErr) || domainErr.Kind != domain.KindValidation {
			t.Fatalf("delete referenced Version = %T %v", err, err)
		}
	}
	if err := store.PrepareSessionDeletion(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.FinalizeSessionDeletion(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BeginDeleteVersion(ctx, skill.ID, version.Version); err != nil {
		t.Fatalf("delete Version after Session release: %v", err)
	}
	if err := repo.RemoveIncompleteVersion(ctx, skill.ID, version.Version); err != nil {
		t.Fatal(err)
	}

	missing := newSession("sesn_missing_skill_pin")
	missing.AgentSnapshot.Skills = []domain.SkillReference{{
		Type: "custom", SkillID: skill.ID, Version: "missing",
	}}
	if _, err := store.CreateSession(ctx, missing, nil); err == nil {
		t.Fatal("Session with missing Skill Version was created")
	}
	if _, err := store.GetSession(ctx, missing.ID); err == nil {
		t.Fatal("failed Skill pin left a partial Session")
	}
}

func TestSessionSkillPinAndVersionDeletionLinearize(t *testing.T) {
	store := testStore(t)
	repo := NewSkillRepository(store)
	ctx := context.Background()
	base := time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC)
	skill := domain.Skill{
		ID: "skill_pin_race", CreatedAt: base, UpdatedAt: base,
		DisplayTitle: "Pin Race", Source: "custom", TitleExplicit: true,
	}
	version := repositorySkillVersion(skill.ID, "500", base, true)
	if err := repo.BeginSkill(ctx, skill, version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CompleteVersion(ctx, skill.ID, version.Version, app.BlobInfo{
		SizeBytes: 10, ChecksumSHA256: "race",
	}); err != nil {
		t.Fatal(err)
	}
	session := newSession("sesn_skill_pin_race")
	session.AgentSnapshot.Skills = []domain.SkillReference{{
		Type: "custom", SkillID: skill.ID, Version: version.Version,
	}}

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	var sessionErr, deleteErr error
	go func() {
		defer wait.Done()
		<-start
		_, sessionErr = store.CreateSession(ctx, session, nil)
	}()
	go func() {
		defer wait.Done()
		<-start
		_, deleteErr = repo.BeginDeleteVersion(ctx, skill.ID, version.Version)
	}()
	close(start)
	wait.Wait()
	if (sessionErr == nil) == (deleteErr == nil) {
		t.Fatalf("Session/Delete race = session:%v delete:%v; exactly one must commit", sessionErr, deleteErr)
	}
	if sessionErr == nil {
		if err := store.PrepareSessionDeletion(ctx, session.ID); err != nil {
			t.Fatal(err)
		}
		if err := store.FinalizeSessionDeletion(ctx, session.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.BeginDeleteVersion(ctx, skill.ID, version.Version); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.RemoveIncompleteVersion(ctx, skill.ID, version.Version); err != nil {
		t.Fatal(err)
	}
}
