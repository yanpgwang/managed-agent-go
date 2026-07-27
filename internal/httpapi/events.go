package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/store"
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
		if k == "id" || k == "type" || k == "processed_at" {
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

	eq := store.EventQuery{Limit: limit, Desc: desc}
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

	requireString := func(key string) error {
		if value, ok := event[key].(string); !ok || value == "" {
			return domain.Validation(fmt.Sprintf("%s is required for %s", key, t))
		}
		return nil
	}
	requireContent := func() error {
		content, ok := event["content"].([]any)
		if !ok || len(content) == 0 {
			return domain.Validation(fmt.Sprintf("content is required for %s", t))
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
			if blockType == "text" {
				if _, ok := block["text"].(string); !ok {
					return domain.Validation("text content blocks require text")
				}
			}
		}
		return nil
	}

	switch t {
	case domain.EvUserMessage, domain.EvSystemMessage:
		return requireContent()
	case domain.EvUserInterrupt:
		return optionalString(event, "session_thread_id")
	case domain.EvUserCustomToolResult:
		if err := requireString("custom_tool_use_id"); err != nil {
			return err
		}
	case domain.EvUserToolResult:
		if err := requireString("tool_use_id"); err != nil {
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
		if _, present := event["deny_message"]; present && result != "deny" {
			return domain.Validation("deny_message is only allowed when result is deny")
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
	return optionalString(event, "session_thread_id")
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
	ch, cancel := s.deps.Hub.Subscribe(sessionID, deltaOptIn)
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

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
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
