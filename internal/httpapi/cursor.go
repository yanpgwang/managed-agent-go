package httpapi

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
)

// Pagination cursors are opaque to callers. Internally they encode the last
// sequence number returned plus the order they were created with, so that
// reusing a cursor under a different order can be rejected (per the public
// contract, a page cursor is only valid with the order it was created with).
// The internal sequence number never appears un-encoded on the wire.

type cursor struct {
	seq       int64
	order     string // "asc" or "desc"
	sessionID string
	filter    string
}

func encodeCursor(c cursor) string {
	raw := "v2:" + strconv.FormatInt(c.seq, 10) + ":" + c.order + ":" + c.sessionID + ":" + c.filter
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(token string) (cursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return cursor{}, false
	}
	parts := strings.Split(string(b), ":")
	if len(parts) != 5 || parts[0] != "v2" {
		return cursor{}, false
	}
	seq, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return cursor{}, false
	}
	if (parts[2] != "asc" && parts[2] != "desc") || parts[3] == "" || parts[4] == "" {
		return cursor{}, false
	}
	return cursor{seq: seq, order: parts[2], sessionID: parts[3], filter: parts[4]}, true
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
// event pagination is forward-only and keyed by the internal event sequence.
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

// resourceCursor is the forward-only cursor used by List Agents and List
// Environments. Neither endpoint documents `order` or `prev_page`, so unlike
// the session cursor it carries no direction and no order; like the session
// cursor it carries a filter fingerprint, so replaying a page token under
// different filters is rejected rather than silently returning a page from a
// different result set. Kind separates the two resources so an agents cursor
// cannot be replayed against environments.
type resourceCursor struct {
	Version   int    `json:"v"`
	Kind      string `json:"kind"`
	CreatedAt string `json:"created_at"`
	ID        string `json:"id"`
	Filter    string `json:"filter"`
}

const (
	agentListCursorKind       = "agent_list"
	environmentListCursorKind = "environment_list"
	// resourceCursorPrefix keeps the wire token visibly opaque and matches the
	// shape of the `page_...` cursors in the official examples.
	resourceCursorPrefix = "page_"
)

func encodeResourceCursor(cursor resourceCursor) string {
	cursor.Version = 1
	body, _ := json.Marshal(cursor)
	return resourceCursorPrefix + base64.RawURLEncoding.EncodeToString(body)
}

func decodeResourceCursor(token, kind string) (resourceCursor, bool) {
	encoded, ok := strings.CutPrefix(token, resourceCursorPrefix)
	if !ok {
		return resourceCursor{}, false
	}
	body, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return resourceCursor{}, false
	}
	var cursor resourceCursor
	if err := json.Unmarshal(body, &cursor); err != nil {
		return resourceCursor{}, false
	}
	if cursor.Version != 1 ||
		cursor.Kind != kind ||
		cursor.CreatedAt == "" ||
		cursor.ID == "" ||
		cursor.Filter == "" {
		return resourceCursor{}, false
	}
	return cursor, true
}

// agentCursorFilter fingerprints exactly the documented List Agents filters.
type agentCursorFilter struct {
	CreatedAtGte    string `json:"created_at_gte,omitempty"`
	CreatedAtLte    string `json:"created_at_lte,omitempty"`
	IncludeArchived bool   `json:"include_archived"`
}

func (f agentCursorFilter) fingerprint() string {
	body, _ := json.Marshal(f)
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// environmentCursorFilter is deliberately a separate type from
// agentCursorFilter. List Environments documents no created_at filters, so it
// must not be possible to fingerprint one here.
type environmentCursorFilter struct {
	IncludeArchived bool `json:"include_archived"`
}

func (f environmentCursorFilter) fingerprint() string {
	body, _ := json.Marshal(f)
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// parsePositiveLimit parses a `limit` value without applying any bound. Each
// endpoint owns its own maximum because the documented bounds differ per
// resource, so the caller compares against its own constant.
func parsePositiveLimit(raw string) (int, error) {
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0, domain.Validation("limit must be a positive integer")
	}
	return limit, nil
}

func parseBoolParam(raw, field string) (bool, error) {
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, domain.Validation(field + " must be true or false")
}

// rejectUnsupportedListParams turns a named-but-unsupported query parameter into
// an explicit validation error. It deliberately checks a closed list rather than
// rejecting every unrecognized key: the official SDK appends parameters of its
// own (for example `beta=true`) to these paths.
func rejectUnsupportedListParams(values url.Values, unsupported ...string) error {
	for _, key := range unsupported {
		if values.Has(key) {
			return domain.Validation(key + " is not a supported query parameter for this endpoint")
		}
	}
	return nil
}
