package httpapi

import (
	"encoding/json"
	"testing"
)

func TestMemoryInputsDistinguishOmittedEmptyAndNull(t *testing.T) {
	var empty memoryUpdateRequest
	if err := json.Unmarshal([]byte(`{"content":""}`), &empty); err != nil {
		t.Fatalf("decode empty content: %v", err)
	}
	if !empty.Content.Present || empty.Content.Null || empty.Content.Value != "" {
		t.Fatalf("empty content field = %#v", empty.Content)
	}
	if empty.Path.Present || empty.Precondition.Present {
		t.Fatalf("omitted fields were marked present: %#v", empty)
	}

	var null memoryUpdateRequest
	if err := json.Unmarshal([]byte(`{"content":null}`), &null); err != nil {
		t.Fatalf("decode null content: %v", err)
	}
	if !null.Content.Present || !null.Content.Null {
		t.Fatalf("null content field = %#v", null.Content)
	}

	var metadata memoryStoreUpdateRequest
	if err := json.Unmarshal([]byte(`{"metadata":{"remove":null,"keep":"value"}}`), &metadata); err != nil {
		t.Fatalf("decode metadata patch: %v", err)
	}
	if metadata.Metadata.Null || metadata.Metadata.Value["remove"] != nil ||
		metadata.Metadata.Value["keep"] == nil || *metadata.Metadata.Value["keep"] != "value" {
		t.Fatalf("metadata patch = %#v", metadata.Metadata)
	}
}

func TestMemoryOptionalObjectKeepsStrictNestedFields(t *testing.T) {
	var body memoryUpdateRequest
	err := json.Unmarshal([]byte(`{
  "precondition": {
    "type": "content_sha256",
    "content_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    "unknown": true
  }
}`), &body)
	if err == nil {
		t.Fatal("nested unknown precondition field was accepted")
	}
}

func TestSessionMemoryResourceRejectsExplicitNullOptions(t *testing.T) {
	for _, raw := range []string{
		`{"type":"memory_store","memory_store_id":"memstore_test","access":null}`,
		`{"type":"memory_store","memory_store_id":"memstore_test","instructions":null}`,
	} {
		if _, err := parseSessionMemoryResourceInput(json.RawMessage(raw)); err == nil {
			t.Fatalf("explicit null option was accepted: %s", raw)
		}
	}
}
