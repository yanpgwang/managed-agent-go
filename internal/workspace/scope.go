// Package workspace defines Mango's only tenant boundary.
//
// A Workspace is resolved from an authenticated API key at the HTTP edge and
// carried in context. It is deliberately not part of CMA request or response
// bodies: the upstream contract scopes resources through credentials too.
package workspace

import (
	"context"
	"errors"
	"strings"
)

const (
	// DefaultID owns rows created before Workspace tenancy was introduced and
	// is used by the local bootstrap path.
	DefaultID = "wrkspc_default"
	Prefix    = "wrkspc_"
	KeyPrefix = "sk-mango-"
)

var (
	ErrMissingScope  = errors.New("workspace scope is required")
	ErrInvalidAPIKey = errors.New("invalid API key")
)

type Scope struct {
	ID string
}

type contextKey struct{}

func WithScope(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, contextKey{}, Scope{ID: id})
}

func FromContext(ctx context.Context) (Scope, bool) {
	scope, ok := ctx.Value(contextKey{}).(Scope)
	return scope, ok && scope.ID != ""
}

func Require(ctx context.Context) (Scope, error) {
	scope, ok := FromContext(ctx)
	if !ok {
		return Scope{}, ErrMissingScope
	}
	return scope, nil
}

// BlobKey places object-store data beneath the authenticated Workspace. The
// suffix is an internal stable path such as files/file_... or skills/skill_....
func BlobKey(ctx context.Context, suffix string) string {
	workspaceID := DefaultID
	if scope, ok := FromContext(ctx); ok {
		workspaceID = scope.ID
	}
	return workspaceID + "/" + strings.TrimPrefix(suffix, "/")
}
