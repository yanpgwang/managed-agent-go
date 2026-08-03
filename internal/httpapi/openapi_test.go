package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenAPIServed(t *testing.T) {
	h := NewTestHandler(t)
	rec := do(h, "GET", "/openapi.yaml", "")
	if rec.Code != 200 {
		t.Fatalf("openapi status %d", rec.Code)
	}
	if len(rec.Body.String()) < 50 {
		t.Fatal("openapi doc too short")
	}
}

func TestOpenAPIResourceLifecycleContract(t *testing.T) {
	doc := parseOpenAPIDocument(t)
	paths := openAPIMap(t, doc["paths"], "paths")
	operations := map[string][]string{
		"/v1/agents":                                {"get", "post"},
		"/v1/agents/{agent_id}":                     {"get", "post"},
		"/v1/agents/{agent_id}/versions":            {"get"},
		"/v1/agents/{agent_id}/archive":             {"post"},
		"/v1/environments":                          {"get", "post"},
		"/v1/environments/{environment_id}":         {"delete", "get", "post"},
		"/v1/environments/{environment_id}/archive": {"post"},
		"/v1/sessions":                              {"get", "post"},
		"/v1/sessions/{session_id}":                 {"delete", "get", "post"},
		"/v1/sessions/{session_id}/archive":         {"post"},
	}
	requestBodies := map[string]bool{
		"post /v1/agents":                        true,
		"post /v1/agents/{agent_id}":             true,
		"post /v1/environments":                  true,
		"post /v1/environments/{environment_id}": true,
		"post /v1/sessions":                      true,
		"post /v1/sessions/{session_id}":         true,
	}

	seenOperationIDs := map[string]bool{}
	for path, methods := range operations {
		pathItem := openAPIMap(t, paths[path], "path "+path)
		for _, method := range methods {
			operation := openAPIMap(t, pathItem[method], method+" "+path)
			operationID, ok := operation["operationId"].(string)
			if !ok || operationID == "" || seenOperationIDs[operationID] {
				t.Fatalf("%s %s has missing or duplicate operationId %#v", method, path, operation["operationId"])
			}
			seenOperationIDs[operationID] = true

			responses := openAPIMap(t, operation["responses"], method+" "+path+" responses")
			success := resolveOpenAPIRef(t, doc, responses["200"])
			content := openAPIMap(t, success["content"], method+" "+path+" success content")
			media := openAPIMap(t, content["application/json"], method+" "+path+" JSON response")
			if _, ok := openAPIMap(t, media["schema"], method+" "+path+" response schema")["$ref"]; !ok {
				t.Fatalf("%s %s success response does not name a reusable schema", method, path)
			}

			key := method + " " + path
			_, hasBody := operation["requestBody"]
			if hasBody != requestBodies[key] {
				t.Fatalf("%s requestBody presence = %t", key, hasBody)
			}
			if hasBody {
				body := openAPIMap(t, operation["requestBody"], key+" requestBody")
				bodyContent := openAPIMap(t, body["content"], key+" request content")
				bodyJSON := openAPIMap(t, bodyContent["application/json"], key+" JSON request")
				if _, ok := openAPIMap(t, bodyJSON["schema"], key+" request schema")["$ref"]; !ok {
					t.Fatalf("%s request does not name a reusable schema", key)
				}
			}
		}
	}
	if len(seenOperationIDs) != 18 {
		t.Fatalf("resource lifecycle operation count = %d, want 18", len(seenOperationIDs))
	}
	validateOpenAPIRefs(t, doc, doc)
}

func parseOpenAPIDocument(t *testing.T) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(openapiDoc), &doc); err != nil {
		t.Fatalf("parse OpenAPI document: %v", err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatalf("OpenAPI version = %#v", doc["openapi"])
	}
	return doc
}

func openAPIMap(t *testing.T, value any, context string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s is %T, want object", context, value)
	}
	return result
}

func resolveOpenAPIRef(t *testing.T, doc map[string]any, value any) map[string]any {
	t.Helper()
	object := openAPIMap(t, value, "reference")
	ref, ok := object["$ref"].(string)
	if !ok {
		return object
	}
	resolved, ok := lookupOpenAPIRef(doc, ref)
	if !ok {
		t.Fatalf("unresolved OpenAPI reference %q", ref)
	}
	return openAPIMap(t, resolved, ref)
}

func validateOpenAPIRefs(t *testing.T, doc map[string]any, value any) {
	t.Helper()
	switch value := value.(type) {
	case map[string]any:
		if ref, ok := value["$ref"].(string); ok {
			if _, found := lookupOpenAPIRef(doc, ref); !found {
				t.Errorf("unresolved OpenAPI reference %q", ref)
			}
		}
		for _, child := range value {
			validateOpenAPIRefs(t, doc, child)
		}
	case []any:
		for _, child := range value {
			validateOpenAPIRefs(t, doc, child)
		}
	}
}

func lookupOpenAPIRef(doc map[string]any, ref string) (any, bool) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	var current any = doc
	for _, segment := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func TestOpenAPICoreOperationInventory(t *testing.T) {
	doc := parseOpenAPIDocument(t)
	paths := openAPIMap(t, doc["paths"], "paths")
	count := 0
	for path, rawPathItem := range paths {
		if !strings.HasPrefix(path, "/v1/") {
			continue
		}
		pathItem := openAPIMap(t, rawPathItem, "path "+path)
		for _, method := range []string{"delete", "get", "post"} {
			if operation, ok := pathItem[method]; ok {
				count++
				if id, _ := openAPIMap(t, operation, fmt.Sprintf("%s %s", method, path))["operationId"].(string); id == "" {
					t.Errorf("%s %s has no operationId", method, path)
				}
			}
		}
	}
	if count != 21 {
		t.Fatalf("core operation count = %d, want 21", count)
	}
}
