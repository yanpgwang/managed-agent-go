package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/yanpgwang/managed-agent-go/internal/domain"
	"github.com/yanpgwang/managed-agent-go/internal/mcpclient"
	"github.com/yanpgwang/managed-agent-go/internal/pg/pgstore"
)

func (s *Store) GetMCPDiscoverySnapshot(
	ctx context.Context,
	sessionID string,
	server domain.MCPServer,
) ([]mcpclient.Tool, bool, error) {
	row, err := s.q.GetMCPDiscoverySnapshot(
		ctx,
		pgstore.GetMCPDiscoverySnapshotParams{
			SessionID:  sessionID,
			ServerName: server.Name,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if row.ServerUrl != server.URL {
		return nil, false, domain.Conflict(
			"MCP server URL differs from the Session discovery snapshot",
		)
	}
	var tools []mcpclient.Tool
	if err := json.Unmarshal(row.Tools, &tools); err != nil {
		return nil, false, fmt.Errorf(
			"pg: decode MCP discovery snapshot %s/%s: %w",
			sessionID,
			server.Name,
			err,
		)
	}
	return tools, true, nil
}

func (s *Store) PutMCPDiscoverySnapshot(
	ctx context.Context,
	sessionID string,
	server domain.MCPServer,
	tools []mcpclient.Tool,
) ([]mcpclient.Tool, error) {
	raw, err := json.Marshal(tools)
	if err != nil {
		return nil, err
	}
	if err := s.q.InsertMCPDiscoverySnapshot(
		ctx,
		pgstore.InsertMCPDiscoverySnapshotParams{
			SessionID:  sessionID,
			ServerName: server.Name,
			ServerUrl:  server.URL,
			Tools:      raw,
			CreatedAt:  tsUTC(s.clock.Now().UTC()),
		},
	); err != nil {
		return nil, err
	}
	authoritative, found, err := s.GetMCPDiscoverySnapshot(
		ctx,
		sessionID,
		server,
	)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("pg: MCP discovery snapshot insert was not visible")
	}
	return authoritative, nil
}
