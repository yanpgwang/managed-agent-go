package domain

import "testing"

// Environment metadata deletes a key on null OR on the empty string. Session
// metadata deletes only on null. The two rules are documented separately and
// are implemented separately; this test exists so a future "shared metadata
// patch helper" refactor fails loudly.
func TestEnvironmentApply_MetadataDeletesOnNullAndEmptyString(t *testing.T) {
	base := Environment{
		Name:       "e",
		ConfigType: "cloud",
		Config:     map[string]any{"type": "cloud"},
		Metadata:   map[string]any{"keep": "yes", "byNull": "a", "byEmpty": "b"},
	}
	next, changed, err := base.Apply(EnvironmentPatch{
		Metadata: map[string]any{"byNull": nil, "byEmpty": "", "added": "new"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !changed {
		t.Fatal("metadata patch reported no change")
	}
	if _, ok := next.Metadata["byNull"]; ok {
		t.Error("null did not delete the key")
	}
	if _, ok := next.Metadata["byEmpty"]; ok {
		t.Error("empty string did not delete the key")
	}
	if next.Metadata["keep"] != "yes" || next.Metadata["added"] != "new" {
		t.Errorf("metadata = %v", next.Metadata)
	}
	// The patch must not mutate the receiver's map in place.
	if len(base.Metadata) != 3 {
		t.Errorf("source metadata mutated: %v", base.Metadata)
	}
}

func TestEnvironmentApply_OmittedFieldsPreserveAndReportNoChange(t *testing.T) {
	base := Environment{
		Name:        "e",
		Description: "d",
		Scope:       "account",
		ConfigType:  "cloud",
		Config: map[string]any{
			"type":     "cloud",
			"packages": map[string]any{"type": "packages", "pip": []any{"pandas"}},
		},
		Metadata: map[string]any{"team": "ml"},
	}
	next, changed, err := base.Apply(EnvironmentPatch{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if changed {
		t.Fatal("empty patch reported a change")
	}
	if next.Name != "e" || next.Description != "d" || next.Scope != "account" {
		t.Fatalf("empty patch altered scalars: %+v", next)
	}

	// Re-supplying an identical config is also not a change.
	if _, changed, err = base.Apply(EnvironmentPatch{
		Config: map[string]any{
			"type":     "cloud",
			"packages": map[string]any{"type": "packages", "pip": []any{"pandas"}},
		},
	}); err != nil {
		t.Fatalf("apply identical config: %v", err)
	} else if changed {
		t.Fatal("identical config reported a change")
	}
}

func TestEnvironmentApply_ConfigTypeIsRequiredAndImmutable(t *testing.T) {
	base := Environment{Name: "e", ConfigType: "cloud", Config: map[string]any{"type": "cloud"}}
	if _, _, err := base.Apply(EnvironmentPatch{
		Config: map[string]any{"networking": map[string]any{"type": "unrestricted"}},
	}); err == nil {
		t.Error("config without type was accepted")
	}
	if _, _, err := base.Apply(EnvironmentPatch{
		Config: map[string]any{"type": "self_hosted"},
	}); err == nil {
		t.Error("in-place config type change was accepted")
	}
	scope := "team"
	if _, _, err := base.Apply(EnvironmentPatch{Scope: &scope}); err == nil {
		t.Error("invalid scope was accepted")
	}
}
