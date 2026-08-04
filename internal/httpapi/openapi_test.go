package httpapi

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/app"
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

func TestOpenAPISessionEventContract(t *testing.T) {
	doc := parseOpenAPIDocument(t)
	paths := openAPIMap(t, doc["paths"], "paths")
	eventsPath := openAPIMap(t, paths["/v1/sessions/{session_id}/events"], "events path")

	post := openAPIMap(t, eventsPath["post"], "send events operation")
	requestBody := openAPIMap(t, post["requestBody"], "send events request body")
	requestContent := openAPIMap(t, requestBody["content"], "send events request content")
	requestJSON := openAPIMap(t, requestContent["application/json"], "send events JSON request")
	assertOpenAPIRef(t, requestJSON["schema"], "#/components/schemas/SendSessionEventsRequest")
	postResponses := openAPIMap(t, post["responses"], "send events responses")
	assertOpenAPIRef(t, postResponses["200"], "#/components/responses/SessionEventBatchResponse")

	get := openAPIMap(t, eventsPath["get"], "list events operation")
	assertOpenAPIParameterNames(t, doc, get["parameters"], []string{
		"limit", "order", "page", "types[]", "created_at[gt]", "created_at[gte]",
		"created_at[lt]", "created_at[lte]",
	})
	getResponses := openAPIMap(t, get["responses"], "list events responses")
	assertOpenAPIRef(t, getResponses["200"], "#/components/responses/SessionEventListResponse")

	streamPath := openAPIMap(t, paths["/v1/sessions/{session_id}/events/stream"], "stream path")
	stream := openAPIMap(t, streamPath["get"], "stream events operation")
	assertOpenAPIParameterNames(t, doc, stream["parameters"], []string{"event_deltas[]"})
	streamResponses := openAPIMap(t, stream["responses"], "stream responses")
	streamSuccess := resolveOpenAPIRef(t, doc, streamResponses["200"])
	streamContent := openAPIMap(t, streamSuccess["content"], "stream success content")
	sse := openAPIMap(t, streamContent["text/event-stream"], "SSE media type")
	assertOpenAPIRef(t, sse["schema"], "#/components/schemas/EventStreamFrame")
	frames := openAPIMap(t, sse["x-sse-event-schemas"], "SSE event schemas")
	assertOpenAPIRef(t, frames["persisted"], "#/components/schemas/SessionEvent")
	assertOpenAPIRef(t, frames["event_start"], "#/components/schemas/EventStart")
	assertOpenAPIRef(t, frames["event_delta"], "#/components/schemas/EventDelta")

	assertOpenAPIEventUnion(t, doc, "ClientSessionEventInput", []string{
		"user.message", "user.interrupt", "user.tool_confirmation",
		"user.custom_tool_result", "user.define_outcome", "user.tool_result",
		"system.message",
	})
	assertOpenAPIEventUnion(t, doc, "SessionEvent", []string{
		"user.message", "user.interrupt", "user.tool_confirmation",
		"user.custom_tool_result", "user.tool_result", "user.define_outcome",
		"system.message", "agent.message", "agent.thinking", "agent.custom_tool_use", "agent.tool_use",
		"agent.tool_result", "agent.mcp_tool_use", "agent.mcp_tool_result",
		"session.status_idle", "session.status_running", "session.status_terminated",
		"session.status_rescheduled", "session.error", "session.updated", "session.deleted",
		"span.outcome_evaluation_start", "span.outcome_evaluation_ongoing",
		"span.outcome_evaluation_end", "span.model_request_start", "span.model_request_end",
	})
	validateOpenAPIRefs(t, doc, doc)
}

func assertOpenAPIRef(t *testing.T, value any, want string) {
	t.Helper()
	ref, _ := openAPIMap(t, value, "reference")["$ref"].(string)
	if ref != want {
		t.Fatalf("OpenAPI reference = %q, want %q", ref, want)
	}
}

func assertOpenAPIParameterNames(t *testing.T, doc map[string]any, value any, want []string) {
	t.Helper()
	parameters, ok := value.([]any)
	if !ok {
		t.Fatalf("parameters are %T, want array", value)
	}
	got := make([]string, 0, len(parameters))
	for _, raw := range parameters {
		parameter := resolveOpenAPIRef(t, doc, raw)
		name, _ := parameter["name"].(string)
		got = append(got, name)
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("parameter names = %v, want %v", got, want)
	}
}

func assertOpenAPIEventUnion(t *testing.T, doc map[string]any, name string, want []string) {
	t.Helper()
	components := openAPIMap(t, doc["components"], "components")
	schemas := openAPIMap(t, components["schemas"], "schemas")
	union := openAPIMap(t, schemas[name], name)
	variants, ok := union["oneOf"].([]any)
	if !ok {
		t.Fatalf("%s oneOf is %T, want array", name, union["oneOf"])
	}
	got := make([]string, 0, len(variants))
	for _, raw := range variants {
		variant := resolveOpenAPIRef(t, doc, raw)
		got = append(got, openAPIEventTypeConst(t, variant, name))
	}
	sort.Strings(got)
	want = append([]string(nil), want...)
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s variants = %v, want %v", name, got, want)
	}
}

func openAPIEventTypeConst(t *testing.T, schema map[string]any, context string) string {
	t.Helper()
	if properties, ok := schema["properties"].(map[string]any); ok {
		if typeSchema, ok := properties["type"].(map[string]any); ok {
			if value, ok := typeSchema["const"].(string); ok {
				return value
			}
		}
	}
	if allOf, ok := schema["allOf"].([]any); ok {
		for _, raw := range allOf {
			part := openAPIMap(t, raw, context+" allOf member")
			if _, isRef := part["$ref"]; isRef {
				continue
			}
			if value := openAPIEventTypeConst(t, part, context); value != "" {
				return value
			}
		}
	}
	t.Fatalf("%s has no event type const", context)
	return ""
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
		if strings.HasPrefix(path, "/v1/files") {
			continue
		}
		if strings.HasPrefix(path, "/v1/skills") {
			continue
		}
		if strings.Contains(path, "/resources") {
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

func TestOpenAPISessionResourcesContract(t *testing.T) {
	doc := parseOpenAPIDocument(t)
	paths := openAPIMap(t, doc["paths"], "paths")
	operations := map[string][]string{
		"/v1/sessions/{session_id}/resources":               {"get", "post"},
		"/v1/sessions/{session_id}/resources/{resource_id}": {"delete", "get", "post"},
	}
	count := 0
	for path, methods := range operations {
		pathItem := openAPIMap(t, paths[path], "path "+path)
		for _, method := range methods {
			operation := openAPIMap(t, pathItem[method], method+" "+path)
			if id, _ := operation["operationId"].(string); id == "" {
				t.Fatalf("%s %s has no operationId", method, path)
			}
			count++
		}
	}
	if count != 5 {
		t.Fatalf("Session Resources operation count = %d, want 5", count)
	}
	schemas := openAPIMap(
		t,
		openAPIMap(t, doc["components"], "components")["schemas"],
		"schemas",
	)
	session := openAPIMap(t, schemas["Session"], "Session schema")
	properties := openAPIMap(t, session["properties"], "Session properties")
	resources := openAPIMap(t, properties["resources"], "Session resources")
	if fmt.Sprint(resources["maxItems"]) != "500" {
		t.Fatalf("Session resources maxItems = %v, want 500", resources["maxItems"])
	}
}

func TestOpenAPIFilesContract(t *testing.T) {
	doc := parseOpenAPIDocument(t)
	paths := openAPIMap(t, doc["paths"], "paths")
	operations := map[string][]string{
		"/v1/files":                   {"get", "post"},
		"/v1/files/{file_id}":         {"delete", "get"},
		"/v1/files/{file_id}/content": {"get"},
	}
	count := 0
	for path, methods := range operations {
		pathItem := openAPIMap(t, paths[path], "path "+path)
		for _, method := range methods {
			operation := openAPIMap(t, pathItem[method], method+" "+path)
			if id, _ := operation["operationId"].(string); id == "" {
				t.Fatalf("%s %s has no operationId", method, path)
			}
			count++
		}
	}
	if count != 5 {
		t.Fatalf("Files operation count = %d, want 5", count)
	}
	upload := openAPIMap(t, openAPIMap(t, paths["/v1/files"], "Files path")["post"], "upload")
	request := openAPIMap(t, upload["requestBody"], "upload request")
	content := openAPIMap(t, request["content"], "upload content")
	assertOpenAPIRef(t, openAPIMap(t, content["multipart/form-data"], "multipart")["schema"],
		"#/components/schemas/FileUploadRequest")
	list := openAPIMap(t, openAPIMap(t, paths["/v1/files"], "Files path")["get"], "list")
	assertOpenAPIParameterNames(t, doc, list["parameters"],
		[]string{"after_id", "before_id", "limit", "scope_id"})
	validateOpenAPIRefs(t, doc, doc)
}

func TestOpenAPISkillsContract(t *testing.T) {
	doc := parseOpenAPIDocument(t)
	paths := openAPIMap(t, doc["paths"], "paths")
	operations := map[string][]string{
		"/v1/skills":                                       {"get", "post"},
		"/v1/skills/{skill_id}":                            {"delete", "get"},
		"/v1/skills/{skill_id}/versions":                   {"get", "post"},
		"/v1/skills/{skill_id}/versions/{version}":         {"delete", "get"},
		"/v1/skills/{skill_id}/versions/{version}/content": {"get"},
	}
	count := 0
	for path, methods := range operations {
		pathItem := openAPIMap(t, paths[path], "path "+path)
		for _, method := range methods {
			operation := openAPIMap(t, pathItem[method], method+" "+path)
			if id, _ := operation["operationId"].(string); id == "" {
				t.Fatalf("%s %s has no operationId", method, path)
			}
			count++
		}
	}
	if count != 9 {
		t.Fatalf("Skills operation count = %d, want 9", count)
	}
	for path, schema := range map[string]string{
		"/v1/skills":                     "#/components/schemas/SkillUploadRequest",
		"/v1/skills/{skill_id}/versions": "#/components/schemas/SkillVersionUploadRequest",
	} {
		post := openAPIMap(t, openAPIMap(t, paths[path], path)["post"], "post "+path)
		request := openAPIMap(t, post["requestBody"], "request "+path)
		content := openAPIMap(t, request["content"], "content "+path)
		assertOpenAPIRef(t, openAPIMap(t, content["multipart/form-data"], "multipart")["schema"], schema)
	}
	list := openAPIMap(t, openAPIMap(t, paths["/v1/skills"], "Skills path")["get"], "list Skills")
	assertOpenAPIParameterNames(t, doc, list["parameters"], []string{"limit", "page", "source"})
	versions := openAPIMap(t,
		openAPIMap(t, paths["/v1/skills/{skill_id}/versions"], "Versions path")["get"],
		"list Versions",
	)
	assertOpenAPIParameterNames(t, doc, versions["parameters"], []string{"limit", "page"})
	validateOpenAPIRefs(t, doc, doc)
}

func TestOpenAPISkillReferenceContract(t *testing.T) {
	doc := parseOpenAPIDocument(t)
	components := openAPIMap(t, doc["components"], "components")
	schemas := openAPIMap(t, components["schemas"], "schemas")
	for _, name := range []string{"CustomSkillReferenceInput", "AnthropicSkillReferenceInput"} {
		schema := openAPIMap(t, schemas[name], name)
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("%s additionalProperties = %v, want false", name, schema["additionalProperties"])
		}
		required, _ := schema["required"].([]any)
		if fmt.Sprint(required) != "[type skill_id]" {
			t.Fatalf("%s required = %v", name, required)
		}
		version := openAPIMap(t, openAPIMap(t, schema["properties"], name+" properties")["version"], name+" version")
		if version["minLength"] != 1 || version["pattern"] != `^\S(?:[\s\S]*\S)?$` {
			t.Fatalf("%s Version constraints = %#v", name, version)
		}
	}
	resolved := openAPIMap(t, schemas["ResolvedSkillReference"], "resolved Skill reference")
	if required, _ := resolved["required"].([]any); fmt.Sprint(required) != "[type skill_id version]" {
		t.Fatalf("resolved Skill required = %v", required)
	}
	resolvedType := openAPIMap(t, openAPIMap(t, resolved["properties"], "resolved Skill properties")["type"], "resolved Skill type")
	if resolvedType["const"] != "custom" {
		t.Fatalf("resolved Skill type = %v, want custom", resolvedType["const"])
	}
	legacy := openAPIMap(t, schemas["LegacySkillReference"], "legacy Skill reference")
	assertOpenAPIRef(t, legacy["not"], "#/components/schemas/ResolvedSkillReference")
	response := openAPIMap(t, schemas["SkillReferenceResponse"], "Skill reference response")
	variants, _ := response["oneOf"].([]any)
	if len(variants) != 2 {
		t.Fatalf("Skill response variants = %v, want resolved and legacy", variants)
	}
	assertOpenAPIRef(t, variants[0], "#/components/schemas/ResolvedSkillReference")
	assertOpenAPIRef(t, variants[1], "#/components/schemas/LegacySkillReference")

	create := openAPIMap(t, schemas["AgentCreateRequest"], "Agent create")
	createSkills := openAPIMap(t, openAPIMap(t, create["properties"], "Agent create properties")["skills"], "Agent create skills")
	if max, _ := createSkills["maxItems"].(int); max != app.MaxSessionSkills {
		t.Fatalf("Agent create max Skills = %v, want %d", createSkills["maxItems"], app.MaxSessionSkills)
	}
	assertOpenAPIRef(t, createSkills["items"], "#/components/schemas/SkillReferenceInput")

	agent := openAPIMap(t, schemas["Agent"], "Agent")
	agentSkills := openAPIMap(t, openAPIMap(t, agent["properties"], "Agent properties")["skills"], "Agent skills")
	assertOpenAPIRef(t, agentSkills["items"], "#/components/schemas/SkillReferenceResponse")
	snapshot := openAPIMap(t, schemas["AgentSnapshot"], "Agent snapshot")
	snapshotSkills := openAPIMap(t, openAPIMap(t, snapshot["properties"], "Agent snapshot properties")["skills"], "Agent snapshot skills")
	assertOpenAPIRef(t, snapshotSkills["items"], "#/components/schemas/SkillReferenceResponse")
	validateOpenAPIRefs(t, doc, doc)
}
