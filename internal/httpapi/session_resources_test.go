package httpapi

import (
	"net/http"
	"testing"
)

func TestSessionResourceHandlersRejectUnsupportedSurface(t *testing.T) {
	handler := newTestServer(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "unsupported variant",
			body: `{"type":"github_repository","url":"https://example.com/repo"}`,
			want: http.StatusUnprocessableEntity,
		},
		{
			name: "unknown field",
			body: `{"type":"file","file_id":"file_source","unexpected":true}`,
			want: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := do(
				handler,
				http.MethodPost,
				"/v1/sessions/sesn_missing/resources",
				test.body,
			)
			if response.Code != test.want {
				t.Fatalf("status = %d body=%s, want %d", response.Code, response.Body.String(), test.want)
			}
		})
	}

	response := do(
		handler,
		http.MethodPost,
		"/v1/sessions/sesn_missing/resources/sesrsc_missing",
		`{}`,
	)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing update token status = %d body=%s", response.Code, response.Body.String())
	}
}

func TestSessionResourceHandlersReturnUnsupportedWhenDeploymentDisabled(t *testing.T) {
	handler := NewServer(Deps{}, Config{}).Handler()
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/v1/sessions/sesn_1/resources", `{"type":"file","file_id":"file_1"}`},
		{http.MethodGet, "/v1/sessions/sesn_1/resources", ""},
		{http.MethodGet, "/v1/sessions/sesn_1/resources/sesrsc_1", ""},
		{http.MethodPost, "/v1/sessions/sesn_1/resources/sesrsc_1", `{"authorization_token":"token"}`},
		{http.MethodDelete, "/v1/sessions/sesn_1/resources/sesrsc_1", ""},
	}
	for _, test := range tests {
		response := do(handler, test.method, test.path, test.body)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s %s status = %d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}
