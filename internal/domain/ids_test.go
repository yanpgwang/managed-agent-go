package domain

import (
	"strings"
	"testing"
)

func TestSeqIDGen(t *testing.T) {
	g := NewSeqIDGen()
	if got := g.NewID(PrefixAgent); got != "agent_1" {
		t.Fatalf("got %q, want agent_1", got)
	}
	if got := g.NewID(PrefixAgent); got != "agent_2" {
		t.Fatalf("got %q, want agent_2", got)
	}
	if got := g.NewID(PrefixEvent); got != "sevt_1" {
		t.Fatalf("got %q, want sevt_1", got)
	}
}

func TestRandomIDGen(t *testing.T) {
	g := NewRandomIDGen()
	id1 := g.NewID(PrefixAgent)
	id2 := g.NewID(PrefixAgent)

	if !strings.HasPrefix(id1, "agent_") {
		t.Fatalf("id1 %q does not start with %q", id1, "agent_")
	}
	if id1 == id2 {
		t.Fatalf("two successive calls returned the same id: %q", id1)
	}
	// prefix "agent_" (6 chars) + 16 hex chars = 22 chars total
	const wantLen = len(PrefixAgent) + 16
	if got := len(id1); got != wantLen {
		t.Fatalf("id length = %d, want %d (got %q)", got, wantLen, id1)
	}
}
