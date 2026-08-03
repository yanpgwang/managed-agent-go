package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
)

// eventCursor carries the complete processed_at ordering key. ProcessedAt and
// Unprocessed are mutually exclusive so a null timestamp cannot be confused
// with a malformed cursor that simply omitted its boundary.
type eventCursor struct {
	Version     int    `json:"v"`
	Kind        string `json:"kind"`
	Order       string `json:"order"`
	SessionID   string `json:"session_id"`
	Filter      string `json:"filter"`
	ProcessedAt string `json:"processed_at,omitempty"`
	Unprocessed bool   `json:"unprocessed,omitempty"`
	Sequence    int64  `json:"sequence"`
}

func encodeEventCursor(cursor eventCursor) string {
	cursor.Version = 3
	cursor.Kind = "event_list"
	body, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(body)
}

func decodeEventCursor(token string) (eventCursor, bool) {
	body, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return eventCursor{}, false
	}
	var cursor eventCursor
	if err := json.Unmarshal(body, &cursor); err != nil {
		return eventCursor{}, false
	}
	if cursor.Version != 3 ||
		cursor.Kind != "event_list" ||
		(cursor.Order != "asc" && cursor.Order != "desc") ||
		cursor.SessionID == "" ||
		cursor.Filter == "" ||
		cursor.Sequence <= 0 ||
		((cursor.ProcessedAt != "") == cursor.Unprocessed) {
		return eventCursor{}, false
	}
	return cursor, true
}

type eventCursorFilter struct {
	Types        []string `json:"types,omitempty"`
	CreatedAtGt  string   `json:"created_at_gt,omitempty"`
	CreatedAtGte string   `json:"created_at_gte,omitempty"`
	CreatedAtLt  string   `json:"created_at_lt,omitempty"`
	CreatedAtLte string   `json:"created_at_lte,omitempty"`
}

func (f eventCursorFilter) fingerprint() string {
	f.Types = append([]string(nil), f.Types...)
	sort.Strings(f.Types)
	body, _ := json.Marshal(f)
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// sessionCursor is deliberately separate from the event cursor above. Session
// pagination is bidirectional and uses a stable (created_at,id) key, while
// event pagination is forward-only and uses (processed_at,sequence).
type sessionCursor struct {
	Version   int    `json:"v"`
	Kind      string `json:"kind"`
	Direction string `json:"direction"`
	Order     string `json:"order"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Filter    string `json:"filter"`
}

type sessionCursorFilter struct {
	AgentID         string   `json:"agent_id,omitempty"`
	AgentVersion    *int     `json:"agent_version,omitempty"`
	CreatedAtGt     string   `json:"created_at_gt,omitempty"`
	CreatedAtGte    string   `json:"created_at_gte,omitempty"`
	CreatedAtLt     string   `json:"created_at_lt,omitempty"`
	CreatedAtLte    string   `json:"created_at_lte,omitempty"`
	DeploymentID    *string  `json:"deployment_id,omitempty"`
	IncludeArchived bool     `json:"include_archived"`
	MemoryStoreID   *string  `json:"memory_store_id,omitempty"`
	Statuses        []string `json:"statuses,omitempty"`
}

func (f sessionCursorFilter) fingerprint() string {
	f.Statuses = append([]string(nil), f.Statuses...)
	sort.Strings(f.Statuses)
	body, _ := json.Marshal(f)
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func encodeSessionCursor(cursor sessionCursor) string {
	cursor.Version = 1
	cursor.Kind = "session_list"
	body, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(body)
}

func decodeSessionCursor(token string) (sessionCursor, bool) {
	body, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return sessionCursor{}, false
	}
	var cursor sessionCursor
	if err := json.Unmarshal(body, &cursor); err != nil {
		return sessionCursor{}, false
	}
	if cursor.Version != 1 ||
		cursor.Kind != "session_list" ||
		(cursor.Direction != "next" && cursor.Direction != "prev") ||
		(cursor.Order != "asc" && cursor.Order != "desc") ||
		cursor.CreatedAt == "" ||
		cursor.ID == "" ||
		cursor.Filter == "" {
		return sessionCursor{}, false
	}
	return cursor, true
}
