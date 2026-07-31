package mcpclient

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDialPublicMCPRejectsLocalhostResolution(t *testing.T) {
	connection, err := dialPublicMCP(
		context.Background(),
		"tcp",
		"localhost:80",
	)
	if connection != nil {
		_ = connection.Close()
		t.Fatal("localhost connection unexpectedly succeeded")
	}
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("expected non-public egress rejection, got %v", err)
	}
}

func TestNewRemoteDefaultTransportDisablesAmbientProxy(t *testing.T) {
	remote := NewRemote(nil)
	transport, ok := remote.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T", remote.http.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("default MCP transport inherited an ambient proxy")
	}
	if transport.DialContext == nil {
		t.Fatal("default MCP transport has no egress-enforcing dialer")
	}
}
