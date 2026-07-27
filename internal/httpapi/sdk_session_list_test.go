package httpapi_test

import (
	"context"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestSDK_SessionListBidirectionalPaginationAndStatusesFilter(t *testing.T) {
	client, server := sdkClientAndServer(t)
	ctx := context.Background()
	agent := mustAgent(t, client, "opus", "sys")
	environmentID := mustEnv(t, server.URL)

	created := make([]string, 0, 5)
	for range 5 {
		session, err := client.Beta.Sessions.New(ctx, anthropic.BetaSessionNewParams{
			Agent: anthropic.BetaSessionNewParamsAgentUnion{
				OfString: anthropic.String(agent.ID),
			},
			EnvironmentID: environmentID,
		})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		created = append(created, session.ID)
	}

	params := anthropic.BetaSessionListParams{
		AgentID:  anthropic.String(agent.ID),
		Limit:    anthropic.Int(2),
		Order:    anthropic.BetaSessionListParamsOrderDesc,
		Statuses: []string{"idle"},
	}
	first, err := client.Beta.Sessions.List(ctx, params)
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first.Data) != 2 || first.NextPage == "" || first.PrevPage != "" {
		t.Fatalf("first page: rows=%d next=%q prev=%q", len(first.Data), first.NextPage, first.PrevPage)
	}
	if first.Data[0].ID != created[4] || first.Data[1].ID != created[3] {
		t.Fatalf("first page ids = [%s %s]", first.Data[0].ID, first.Data[1].ID)
	}

	second, err := first.GetNextPage()
	if err != nil {
		t.Fatalf("get next page: %v", err)
	}
	if second == nil || len(second.Data) != 2 || second.NextPage == "" || second.PrevPage == "" {
		t.Fatalf("second page: %#v", second)
	}
	if second.Data[0].ID != created[2] || second.Data[1].ID != created[1] {
		t.Fatalf("second page ids = [%s %s]", second.Data[0].ID, second.Data[1].ID)
	}

	params.Page = anthropic.String(second.PrevPage)
	back, err := client.Beta.Sessions.List(ctx, params)
	if err != nil {
		t.Fatalf("list previous page: %v", err)
	}
	if len(back.Data) != 2 ||
		back.Data[0].ID != created[4] ||
		back.Data[1].ID != created[3] {
		t.Fatalf("previous page did not return the first page: %#v", back.Data)
	}
}
