package domain

import "testing"

func TestAgentApply_MetadataMergeAndNoop(t *testing.T) {
	base := Agent{Version: 1, Name: "a", Metadata: map[string]any{"k1": "v1", "k2": "v2"}}
	// per-key merge: update k1, delete k2 (nil), add k3
	next, changed, err := base.Apply(AgentPatch{Metadata: map[string]any{"k1": "x", "k2": nil, "k3": "y"}})
	if err != nil || !changed {
		t.Fatalf("expected changed, err=%v", err)
	}
	if next.Metadata["k1"] != "x" || next.Metadata["k3"] != "y" {
		t.Fatalf("merge wrong: %v", next.Metadata)
	}
	if _, ok := next.Metadata["k2"]; ok {
		t.Fatalf("k2 should be deleted: %v", next.Metadata)
	}
	// no-op: empty patch => changed false
	if _, ch, _ := base.Apply(AgentPatch{}); ch {
		t.Fatalf("empty patch should be no-op")
	}
}

func TestAgentApply_VersionConflict(t *testing.T) {
	base := Agent{Version: 3, Name: "a"}
	bad := 2
	if _, _, err := base.Apply(AgentPatch{Name: strPtr("b"), ExpectedVersion: &bad}); err == nil {
		t.Fatalf("expected conflict")
	} else if de, ok := err.(*DomainError); !ok || de.Kind != KindConflict {
		t.Fatalf("expected conflict kind, got %v", err)
	}
}

func TestAgentApply_NilMetadataNoop(t *testing.T) {
	base := Agent{Version: 1, Name: "a"} // nil Metadata
	if _, ch, _ := base.Apply(AgentPatch{}); ch {
		t.Fatal("nil-Metadata agent: empty patch must be a no-op")
	}
	if next, ch, _ := base.Apply(AgentPatch{Name: strPtr("a")}); ch {
		t.Fatalf("re-setting Name to same value must be no-op, got changed; meta=%v", next.Metadata)
	}
	// adding a metadata key IS a change
	if _, ch, _ := base.Apply(AgentPatch{Metadata: map[string]any{"k": "v"}}); !ch {
		t.Fatal("adding a metadata key must be a change")
	}
}

func strPtr(s string) *string { return &s }
