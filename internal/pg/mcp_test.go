package pg

import (
	"context"
	"testing"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/mcpclient"
)

func TestMCPDiscoverySnapshot_IsInsertOncePerSessionServer(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	session := newSession("sess_mcp_snapshot")
	if _, err := store.CreateSession(ctx, session, nil); err != nil {
		t.Fatal(err)
	}
	server := domain.MCPServer{
		Name: "github", URL: "https://mcp.example.com",
	}
	first := []mcpclient.Tool{{
		Name: "list_issues", InputSchema: map[string]any{"type": "object"},
	}}
	got, err := store.PutMCPDiscoverySnapshot(
		ctx,
		session.ID,
		server,
		first,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "list_issues" {
		t.Fatalf("first snapshot = %#v", got)
	}
	second := []mcpclient.Tool{{
		Name: "new_remote_tool", InputSchema: map[string]any{"type": "object"},
	}}
	got, err = store.PutMCPDiscoverySnapshot(
		ctx,
		session.ID,
		server,
		second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "list_issues" {
		t.Fatalf("snapshot changed after second insert: %#v", got)
	}
}
