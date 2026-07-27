package httpapi

import "testing"

func TestOpenAPIServed(t *testing.T) {
	h := NewTestHandler(t)
	rec := do(h, "GET", "/openapi.yaml", "")
	if rec.Code != 200 {
		t.Fatalf("openapi status %d", rec.Code)
	}
	if len(rec.Body.String()) < 50 {
		t.Fatal("openapi doc too short")
	}
}
