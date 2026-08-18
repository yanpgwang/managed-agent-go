package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

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

func TestAgentApply_ModelDefaultsAndStickyEffort(t *testing.T) {
	base := Agent{
		Version: 1,
		Model: Model{
			ID:             "claude-sonnet-a",
			Effort:         "max",
			Speed:          "fast",
			EffortExplicit: true,
			SpeedExplicit:  true,
		},
	}

	sameModel, changed, err := base.Apply(AgentPatch{Model: &Model{ID: "claude-sonnet-a"}})
	if err != nil || !changed {
		t.Fatalf("same-model update: changed=%v err=%v", changed, err)
	}
	if sameModel.Model.Effort != "max" || !sameModel.Model.EffortExplicit {
		t.Fatalf("same-model omitted effort must stay sticky: %#v", sameModel.Model)
	}
	if sameModel.Model.Speed != DefaultModelSpeed || sameModel.Model.SpeedExplicit {
		t.Fatalf("omitted speed must reset to its default: %#v", sameModel.Model)
	}

	newModel, changed, err := base.Apply(AgentPatch{Model: &Model{ID: "claude-sonnet-b"}})
	if err != nil || !changed {
		t.Fatalf("new-model update: changed=%v err=%v", changed, err)
	}
	if newModel.Model.Effort != DefaultModelEffort || newModel.Model.EffortExplicit {
		t.Fatalf("new model must use its default effort: %#v", newModel.Model)
	}
}

func TestAgentWithOverrides_IgnoresSessionEffort(t *testing.T) {
	base := Agent{Model: Model{
		ID:             "claude-opus",
		Effort:         "high",
		Speed:          "standard",
		EffortExplicit: true,
	}}
	overridden := base.WithOverrides(AgentOverrides{Model: &Model{
		ID:             "claude-sonnet",
		Effort:         "low",
		Speed:          "fast",
		EffortExplicit: true,
		SpeedExplicit:  true,
	}})
	if overridden.Model.ID != "claude-sonnet" || overridden.Model.Speed != "fast" {
		t.Fatalf("model id/speed override not applied: %#v", overridden.Model)
	}
	if overridden.Model.Effort != "high" || !overridden.Model.EffortExplicit {
		t.Fatalf("session effort must remain the Agent effort: %#v", overridden.Model)
	}
}

func TestValidateModel_RejectsUnknownEnums(t *testing.T) {
	for _, model := range []Model{
		{ID: "m", Effort: "ultra"},
		{ID: "m", Speed: "turbo"},
	} {
		if err := ValidateModel(model); err == nil {
			t.Fatalf("ValidateModel(%#v) unexpectedly succeeded", model)
		}
	}
}

func TestAgentReference_PreservesLegacyStringDuringStorageRoundTrip(t *testing.T) {
	var agent Agent
	if err := json.Unmarshal([]byte(`{"Multiagent":{"type":"coordinator","agents":["agent_legacy",{"type":"agent","id":"agent_unpinned"}]}}`), &agent); err != nil {
		t.Fatal(err)
	}
	if agent.Multiagent == nil || !agent.Multiagent.Agents[0].StringForm ||
		agent.Multiagent.Agents[0].ID != "agent_legacy" {
		t.Fatalf("decoded legacy roster = %#v", agent.Multiagent)
	}
	encoded, err := json.Marshal(agent)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"agents":["agent_legacy",{"type":"agent","id":"agent_unpinned"}]`) {
		t.Fatalf("legacy roster changed during storage round trip: %s", encoded)
	}

	var opaque Agent
	if err := json.Unmarshal([]byte(`{"Multiagent":{"extension":true,"agents":[1]}}`), &opaque); err != nil {
		t.Fatal(err)
	}
	if opaque.Multiagent == nil || !opaque.Multiagent.IsLegacy() {
		t.Fatalf("opaque pre-feature roster was not retained: %#v", opaque.Multiagent)
	}
	encoded, err = json.Marshal(opaque)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"Multiagent":{"extension":true,"agents":[1]}`) {
		t.Fatalf("opaque pre-feature roster changed during storage round trip: %s", encoded)
	}
}

func TestAgentReference_AdvisorUsesExactResolvedShape(t *testing.T) {
	var reference AgentReference
	if err := json.Unmarshal([]byte(`{"type":"advisor","model":"claude-opus-5"}`), &reference); err != nil {
		t.Fatal(err)
	}
	if reference.Type != "advisor" || reference.Model != "claude-opus-5" ||
		reference.ID != "" || reference.Version != 0 {
		t.Fatalf("advisor reference = %#v", reference)
	}
	topology := &Multiagent{Type: "coordinator", Agents: []AgentReference{
		{Type: "agent", ID: "agent_peer", Version: 1},
		reference,
	}}
	if !topology.IsResolved() || topology.HasCallableAgents() == false ||
		topology.Advisor() == nil || topology.Advisor().Model != "claude-opus-5" {
		t.Fatalf("resolved advisor topology = %#v", topology)
	}
	for _, raw := range []string{
		`{"type":"advisor"}`,
		`{"type":"advisor","model":""}`,
		`{"type":"advisor","model":"claude-opus-5","max_uses":1}`,
	} {
		if err := json.Unmarshal([]byte(raw), &reference); err == nil {
			t.Fatalf("accepted invalid advisor entry %s", raw)
		}
	}
}

func strPtr(s string) *string { return &s }
