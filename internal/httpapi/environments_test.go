package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestEnvironments_DefaultCloudWireShape(t *testing.T) {
	srv := newTestServer(t)
	rec := do(srv, http.MethodPost, "/v1/environments", `{"name":"default"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body)
	}
	var environment map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &environment); err != nil {
		t.Fatal(err)
	}
	if environment["description"] != "" {
		t.Fatalf("description = %#v", environment["description"])
	}
	if metadata, ok := environment["metadata"].(map[string]any); !ok || len(metadata) != 0 {
		t.Fatalf("metadata = %#v", environment["metadata"])
	}
	config, _ := environment["config"].(map[string]any)
	if config["type"] != "cloud" {
		t.Fatalf("config = %#v", config)
	}
	networking, _ := config["networking"].(map[string]any)
	if networking["type"] != "unrestricted" {
		t.Fatalf("networking = %#v", networking)
	}
	packages, _ := config["packages"].(map[string]any)
	for _, key := range []string{"apt", "cargo", "gem", "go", "npm", "pip"} {
		if values, ok := packages[key].([]any); !ok || len(values) != 0 {
			t.Errorf("packages.%s = %#v", key, packages[key])
		}
	}
}

func TestEnvironments_RejectsUnenforcedConfiguration(t *testing.T) {
	srv := newTestServer(t)
	for _, body := range []string{
		`{"name":"limited","config":{"type":"cloud","networking":{"type":"limited"}}}`,
		`{"name":"packages","config":{"type":"cloud","packages":{"pip":["requests"]}}}`,
	} {
		rec := do(srv, http.MethodPost, "/v1/environments", body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unsupported config status = %d, want 422: %s", rec.Code, rec.Body)
		}
	}
}
