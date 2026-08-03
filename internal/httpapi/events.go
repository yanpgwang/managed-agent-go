package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/app"
	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

const defaultEventLimit = 100

// maxPageLimit is the shared upper bound on the `limit` query parameter for both
// the List Sessions and List Events endpoints. A request above this bound is a
// validation error rather than a silently-clamped value, so a client that asks
// for more than the server will serve learns it explicitly. Existing defaults
// and cursor semantics are unchanged.
const maxPageLimit = 1000

// maxDeltaOptIn bounds the number of event_deltas[] opt-in values a stream
// request may carry.
const maxDeltaOptIn = 100

// deltaOptInTypes is the closed set of event types a client may opt into for
// preview frames. Only agent.message previews are currently emitted, but the
// opt-in contract accepts agent.thinking too.
var deltaOptInTypes = map[string]bool{
	domain.EvAgentMessage: true,
	"agent.thinking":      true,
}

// defaultSSEKeepAlive is the idle interval between SSE comment keepalives. It
// sits comfortably under the 30-60s idle timeout common to reverse proxies and
// load balancers.
const defaultSSEKeepAlive = 15 * time.Second

// sseKeepAliveFrame is a bare SSE comment frame. Conformant parsers ignore any
// line starting with ":", and the data:-only parsers in the official
// documentation never see it.
const sseKeepAliveFrame = ": keepalive\n\n"

// sseKeepAlive resolves the configured keepalive interval: zero selects the
// default and a negative value disables keepalives.
func (c Config) sseKeepAlive() time.Duration {
	if c.SSEKeepAlive == 0 {
		return defaultSSEKeepAlive
	}
	return c.SSEKeepAlive
}

// toDrafts converts raw event objects (top-level tagged union) into internal
// drafts, flattening every field except "type" into the payload. It does not
// validate; callers validate types separately.
func toDrafts(items []map[string]any) []domain.EventDraft {
	var out []domain.EventDraft
	for _, it := range items {
		t, _ := it["type"].(string)
		if t == "" {
			continue
		}
		payload := map[string]any{}
		for k, v := range it {
			if k != "type" {
				payload[k] = v
			}
		}
		out = append(out, domain.EventDraft{Type: t, Payload: payload})
	}
	return out
}

// eventToJSON renders the public wire shape of a persisted event: a top-level
// tagged union of {id, type, ...type-specific fields, processed_at}. The
// internal sequence number is never emitted.
func eventToJSON(e domain.Event) map[string]any {
	out := map[string]any{"id": e.ID, "type": e.Type}
	for k, v := range e.Payload {
		if k == "id" || k == "type" || k == "processed_at" || strings.HasPrefix(k, "__") {
			continue
		}
		out[k] = v
	}
	if e.ProcessedAt != nil {
		out["processed_at"] = e.ProcessedAt.Format(timeFmt)
	} else {
		out["processed_at"] = nil
	}
	return out
}

func (s *Server) sendEvents(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Events []map[string]any `json:"events"`
	}
	if err := decodeJSONBody(r, &in); err != nil {
		writeError(w, err)
		return
	}
	if len(in.Events) == 0 {
		writeError(w, domain.Validation("events must contain at least one event"))
		return
	}
	// Validate the tagged-union variant before touching the session.
	for _, it := range in.Events {
		if err := validateClientEvent(it); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := validateClientEventBatch(in.Events); err != nil {
		writeError(w, err)
		return
	}
	drafts := toDrafts(in.Events)
	out, err := s.deps.Sessions.SendEvent(r.Context(), r.PathValue("id"), drafts)
	if err != nil {
		writeError(w, err)
		return
	}
	data := make([]any, 0, len(out))
	for _, e := range out {
		data = append(data, eventToJSON(e))
	}
	writeJSON(w, 200, map[string]any{"data": data})
}

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	if _, err := s.deps.Sessions.Get(r.Context(), sessionID); err != nil {
		writeError(w, err)
		return
	}
	q := r.URL.Query()

	order := q.Get("order")
	if order != "" && order != "asc" && order != "desc" {
		writeError(w, domain.Validation("order must be asc or desc"))
		return
	}
	desc := order == "desc"
	limit := defaultEventLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			writeError(w, domain.Validation("limit must be a positive integer"))
			return
		}
		if n > maxPageLimit {
			writeError(w, domain.Validation("limit must not exceed 1000"))
			return
		}
		limit = n
	}

	eq := app.EventQuery{Limit: limit, Desc: desc}
	if types, ok := q["types[]"]; ok {
		eq.Types = types
	}
	cursorFilter := eventCursorFilter{Types: eq.Types}
	for _, item := range []struct {
		key        string
		dst        **time.Time
		normalized *string
	}{
		{"created_at[gt]", &eq.CreatedAtGt, &cursorFilter.CreatedAtGt},
		{"created_at[gte]", &eq.CreatedAtGte, &cursorFilter.CreatedAtGte},
		{"created_at[lt]", &eq.CreatedAtLt, &cursorFilter.CreatedAtLt},
		{"created_at[lte]", &eq.CreatedAtLte, &cursorFilter.CreatedAtLte},
	} {
		if raw := q.Get(item.key); raw != "" {
			t, ok := parseTimeParam(raw)
			if !ok {
				writeError(w, domain.Validation(item.key+" must be an RFC 3339 timestamp"))
				return
			}
			*item.dst = t
			*item.normalized = t.UTC().Format(timeFmt)
		}
	}
	filterFingerprint := cursorFilter.fingerprint()

	// An opaque page cursor supersedes the default bounds. It must have been
	// created with the same order as this request.
	if page := q.Get("page"); page != "" {
		c, ok := decodeCursor(page)
		if !ok {
			writeError(w, domain.Validation("invalid page cursor"))
			return
		}
		wantOrder := "asc"
		if desc {
			wantOrder = "desc"
		}
		if c.order != wantOrder {
			writeError(w, domain.Validation("page cursor order mismatch"))
			return
		}
		if c.sessionID != sessionID || c.filter != filterFingerprint {
			writeError(w, domain.Validation("page cursor scope mismatch"))
			return
		}
		if desc {
			eq.BeforeSeq = c.seq
		} else {
			eq.AfterSeq = c.seq
		}
	}

	// Fetch one extra row to decide whether a next page exists.
	eq.Limit = limit + 1
	hist, err := s.deps.Events.Query(r.Context(), sessionID, eq)
	if err != nil {
		writeError(w, err)
		return
	}

	var nextPage any
	if len(hist) > limit {
		hist = hist[:limit]
		if len(hist) > 0 {
			last := hist[len(hist)-1]
			order := "asc"
			if desc {
				order = "desc"
			}
			nextPage = encodeCursor(cursor{
				seq: last.Sequence, order: order, sessionID: sessionID, filter: filterFingerprint,
			})
		}
	}

	data := make([]any, 0, len(hist))
	for _, e := range hist {
		data = append(data, eventToJSON(e))
	}
	writeJSON(w, 200, map[string]any{"data": data, "next_page": nextPage})
}

func validateClientEvent(event map[string]any) error {
	t, ok := event["type"].(string)
	if !ok || t == "" || !domain.IsClientSubmittable(t) {
		return domain.Validation("event type not accepted from clients: " + t)
	}
	if _, ok := event["id"]; ok {
		return domain.Validation("client events must not include id")
	}
	if _, ok := event["processed_at"]; ok {
		return domain.Validation("client events must not include processed_at")
	}
	allowedFields := map[string]map[string]bool{
		domain.EvUserMessage:          {"type": true, "content": true},
		domain.EvSystemMessage:        {"type": true, "content": true},
		domain.EvUserInterrupt:        {"type": true, "session_thread_id": true},
		domain.EvUserCustomToolResult: {"type": true, "custom_tool_use_id": true, "content": true, "is_error": true},
		domain.EvUserToolResult:       {"type": true, "tool_use_id": true, "content": true, "is_error": true},
		domain.EvUserToolConfirmation: {"type": true, "tool_use_id": true, "result": true, "deny_message": true},
		domain.EvUserDefineOutcome:    {"type": true, "description": true, "rubric": true, "max_iterations": true},
	}
	for key := range event {
		if !allowedFields[t][key] {
			return domain.Validation(fmt.Sprintf("unknown field %q for %s", key, t))
		}
	}

	requireString := func(key string) error {
		if value, ok := event[key].(string); !ok || value == "" {
			return domain.Validation(fmt.Sprintf("%s is required for %s", key, t))
		}
		return nil
	}
	validateContent := func(required bool, allowedTypes map[string]bool) error {
		content, ok := event["content"].([]any)
		if !ok {
			if !required {
				if _, present := event["content"]; !present {
					return nil
				}
			}
			return domain.Validation(fmt.Sprintf("content is required for %s", t))
		}
		if required && len(content) == 0 {
			return domain.Validation(fmt.Sprintf("content is required for %s", t))
		}
		if len(content) > 1000 {
			return domain.Validation("content must contain at most 1000 blocks")
		}
		for _, raw := range content {
			block, ok := raw.(map[string]any)
			if !ok {
				return domain.Validation("content blocks must be objects")
			}
			blockType, _ := block["type"].(string)
			if blockType == "" {
				return domain.Validation("content block type is required")
			}
			if !allowedTypes[blockType] {
				return domain.Validation(fmt.Sprintf("content block type %q is not allowed for %s", blockType, t))
			}
			if blockType == "text" {
				if _, ok := block["text"].(string); !ok {
					return domain.Validation("text content blocks require text")
				}
			}
			if blockType == "image" || blockType == "document" {
				source, ok := block["source"].(map[string]any)
				if !ok {
					return domain.Validation(blockType + " content blocks require a source")
				}
				sourceType, _ := source["type"].(string)
				if sourceType == "" {
					return domain.Validation(blockType + " content source type is required")
				}
				if sourceType == "file" {
					return domain.Unsupported("file-sourced content requires the Files API")
				}
			}
		}
		return nil
	}
	messageContent := map[string]bool{"text": true, "image": true, "document": true}
	resultContent := map[string]bool{
		"text": true, "image": true, "document": true, "search_result": true,
	}
	systemContent := map[string]bool{"text": true}

	switch t {
	case domain.EvUserMessage:
		return validateContent(true, messageContent)
	case domain.EvSystemMessage:
		return validateContent(true, systemContent)
	case domain.EvUserInterrupt:
		return optionalString(event, "session_thread_id")
	case domain.EvUserCustomToolResult:
		if err := requireString("custom_tool_use_id"); err != nil {
			return err
		}
		if err := validateContent(false, resultContent); err != nil {
			return err
		}
	case domain.EvUserToolResult:
		if err := requireString("tool_use_id"); err != nil {
			return err
		}
		if err := validateContent(false, resultContent); err != nil {
			return err
		}
	case domain.EvUserToolConfirmation:
		if err := requireString("tool_use_id"); err != nil {
			return err
		}
		result, _ := event["result"].(string)
		if result != "allow" && result != "deny" {
			return domain.Validation("result must be allow or deny for user.tool_confirmation")
		}
		if _, present := event["deny_message"]; present {
			if result != "deny" {
				return domain.Validation("deny_message is only allowed when result is deny")
			}
			if err := optionalString(event, "deny_message"); err != nil {
				return err
			}
		}
	case domain.EvUserDefineOutcome:
		if err := requireString("description"); err != nil {
			return err
		}
		rubric, ok := event["rubric"].(map[string]any)
		if !ok {
			return domain.Validation("rubric is required for user.define_outcome")
		}
		switch rubric["type"] {
		case "text":
			if value, ok := rubric["content"].(string); !ok || value == "" {
				return domain.Validation("text rubric requires content")
			}
		case "file":
			if value, ok := rubric["file_id"].(string); !ok || value == "" {
				return domain.Validation("file rubric requires file_id")
			}
			return domain.Unsupported("file outcome rubrics require the Files API")
		default:
			return domain.Validation("rubric type must be text or file")
		}
		if raw, present := event["max_iterations"]; present {
			value, ok := raw.(float64)
			if !ok || value != float64(int(value)) || value < 1 || value > 20 {
				return domain.Validation("max_iterations must be an integer from 1 to 20")
			}
		}
	}
	if raw, present := event["content"]; present {
		if _, ok := raw.([]any); !ok {
			return domain.Validation("content must be an array")
		}
	}
	if raw, present := event["is_error"]; present {
		if _, ok := raw.(bool); !ok {
			return domain.Validation("is_error must be a boolean")
		}
	}
	return nil
}

func validateClientEventBatch(events []map[string]any) error {
	defineOutcomes := 0
	systemMessages := 0
	for i, event := range events {
		typeName, _ := event["type"].(string)
		if typeName == domain.EvUserDefineOutcome {
			defineOutcomes++
		}
		if typeName == domain.EvSystemMessage {
			systemMessages++
			if i != len(events)-1 || i == 0 {
				return domain.Validation("system.message must be the final event and immediately follow its accompanying user event")
			}
			previousType, _ := events[i-1]["type"].(string)
			switch previousType {
			case domain.EvUserMessage, domain.EvUserToolResult, domain.EvUserCustomToolResult:
			default:
				return domain.Validation("system.message must immediately follow user.message, user.tool_result, or user.custom_tool_result")
			}
		}
	}
	if defineOutcomes > 1 {
		return domain.Validation("events may contain at most one user.define_outcome")
	}
	if systemMessages > 1 {
		return domain.Validation("events may contain at most one system.message")
	}
	return nil
}

func optionalString(object map[string]any, key string) error {
	if value, present := object[key]; present {
		if _, ok := value.(string); !ok {
			return domain.Validation(key + " must be a string")
		}
	}
	return nil
}

func parseTimeParam(s string) (*time.Time, bool) {
	if s == "" {
		return nil, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, s); err != nil {
			return nil, false
		}
	}
	return &t, true
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")

	// Parse and validate the event_deltas[] opt-in before subscribing. Go's
	// net/url parses a "event_deltas[]=x" query into the key "event_deltas[]";
	// accept the unbracketed "event_deltas" spelling too for robustness. Each
	// value must name a previewable event type and the total is capped.
	q := r.URL.Query()
	optInValues := append([]string(nil), q["event_deltas[]"]...)
	optInValues = append(optInValues, q["event_deltas"]...)
	if len(optInValues) > maxDeltaOptIn {
		writeError(w, domain.Validation(fmt.Sprintf("event_deltas[] accepts at most %d values", maxDeltaOptIn)))
		return
	}
	var deltaOptIn map[string]bool
	for _, v := range optInValues {
		if !deltaOptInTypes[v] {
			writeError(w, domain.Validation("event_deltas[] value not accepted: "+v))
			return
		}
		if deltaOptIn == nil {
			deltaOptIn = map[string]bool{}
		}
		deltaOptIn[v] = true
	}

	// Subscribe before the existence check. Once Get succeeds, a concurrent
	// delete cannot slip between the check and subscription without delivering
	// the terminal session.deleted event to this stream.
	ch, cancel, err := s.deps.Stream.SubscribeContext(r.Context(), sessionID, deltaOptIn)
	if err != nil {
		writeError(w, err)
		return
	}
	defer cancel()
	if _, err := s.deps.Sessions.Get(r.Context(), sessionID); err != nil {
		writeError(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, domain.Validation("streaming unsupported"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	flusher.Flush()

	// Idle keepalives are SSE *comment* frames only. A comment line begins with
	// ":" and is discarded by every conformant SSE parser, including the
	// data:-only shell parsers the official documentation uses, so an idle
	// stream survives a proxy idle timeout without adding a wire field.
	//
	// No "id:" line and no "retry:" directive is ever emitted. An "id:" line
	// would make a browser EventSource replay it as Last-Event-ID on
	// reconnect, advertising a resumption capability the Managed Agents
	// contract does not define: the documented recovery procedure is to open a
	// new stream, list history, and skip already-seen event ids.
	var keepalive <-chan time.Time
	if interval := s.cfg.sseKeepAlive(); interval > 0 {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		keepalive = ticker.C
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-keepalive:
			if _, err := w.Write([]byte(sseKeepAliveFrame)); err != nil {
				return
			}
			flusher.Flush()
		case f, open := <-ch:
			if !open {
				return // slow-consumer drop: client should reconnect
			}
			// Exactly one of Event/Preview is set per frame. A persisted event is
			// rendered as today (event: <type>, data: <event JSON>). A preview frame
			// (delivered only because this stream opted in) is rendered with the
			// preview kind as the SSE event line and its wire JSON as data; it is
			// never persisted and never appears in List Events.
			switch {
			case f.Event != nil:
				ev := *f.Event
				payload, _ := json.Marshal(eventToJSON(ev))
				_, _ = fmt.Fprintf(w, "event: %s\n", ev.Type)
				_, _ = w.Write([]byte("data: "))
				_, _ = w.Write(payload)
				_, _ = w.Write([]byte("\n\n"))
				flusher.Flush()
			case f.Preview != nil:
				payload, _ := json.Marshal(f.Preview.WireJSON())
				_, _ = fmt.Fprintf(w, "event: %s\n", f.Preview.Kind)
				_, _ = w.Write([]byte("data: "))
				_, _ = w.Write(payload)
				_, _ = w.Write([]byte("\n\n"))
				flusher.Flush()
			}
		}
	}
}
