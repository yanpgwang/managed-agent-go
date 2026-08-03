package domain

import "testing"

func TestEnvironmentApplyPatchesFieldsAndMetadata(t *testing.T) {
	current := Environment{
		Name: "before", Description: "old", Scope: "account",
		Metadata:   map[string]any{"keep": "old", "drop_null": "x", "drop_empty": "x"},
		ConfigType: "self_hosted", Config: map[string]any{"type": "self_hosted"},
	}
	name, description, scope := "after", "new", "organization"
	next, changed := current.Apply(EnvironmentPatch{
		Name: &name, Description: &description, Scope: &scope,
		Metadata: map[string]any{
			"keep": "updated", "add": "value", "drop_null": nil, "drop_empty": "",
		},
	})
	if !changed || next.Name != "after" || next.Description != "new" || next.Scope != "organization" {
		t.Fatalf("patched environment = %#v, changed=%v", next, changed)
	}
	if len(next.Metadata) != 2 || next.Metadata["keep"] != "updated" || next.Metadata["add"] != "value" {
		t.Fatalf("patched metadata = %#v", next.Metadata)
	}
	if current.Metadata["keep"] != "old" || len(current.Metadata) != 3 {
		t.Fatalf("source metadata was mutated: %#v", current.Metadata)
	}

	unchanged, changed := current.Apply(EnvironmentPatch{})
	if changed || unchanged.Name != current.Name {
		t.Fatalf("empty patch changed environment: %#v", unchanged)
	}
	emptyMetadata := Environment{Name: "empty", Metadata: map[string]any{}}
	if _, changed := emptyMetadata.Apply(EnvironmentPatch{}); changed {
		t.Fatal("empty patch changed an empty metadata bag")
	}
}

func TestEnvironmentSessionConfigIsDeeplyDetached(t *testing.T) {
	environment := Environment{Config: map[string]any{
		"type": "cloud",
		"packages": map[string]any{
			"pip": []any{"httpx", map[string]any{"future": []any{"value"}}},
		},
	}}
	snapshot := environment.SessionConfig()
	snapshotPackages := snapshot["packages"].(map[string]any)
	snapshotPackages["pip"].([]any)[1].(map[string]any)["future"].([]any)[0] = "changed"

	original := environment.Config["packages"].(map[string]any)["pip"].([]any)[1].(map[string]any)["future"].([]any)[0]
	if original != "value" {
		t.Fatalf("Session config mutated Environment: %#v", original)
	}
}
