package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yanpgwang/mango/internal/domain"
)

func TestParseGitRepositorySessionResource(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{
        "type":"git_repository",
        "url":"https://github.com/acme/widgets.git",
        "checkout":{"type":"commit","sha":"0123456789ABCDEF0123456789ABCDEF01234567"},
        "mount_path":"/workspace/widgets"
    }`)}
	files, memories, repositories, err := parseSessionResourceInputs(&raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 || len(memories) != 0 || len(repositories) != 1 {
		t.Fatalf("parsed resources = files:%d memories:%d repositories:%d", len(files), len(memories), len(repositories))
	}
	repository := repositories[0]
	if repository.Checkout == nil || repository.Checkout.Type != domain.GitRepositoryCheckoutCommit ||
		repository.Checkout.Value != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("checkout = %+v", repository.Checkout)
	}
	if repository.MountPath == nil || *repository.MountPath != "/workspace/widgets" {
		t.Fatalf("mount path = %v", repository.MountPath)
	}
}

func TestParseGitRepositorySessionResourceRejectsVendorAndCredentialSurfaces(t *testing.T) {
	for _, body := range []string{
		`{"type":"github_repository","url":"https://github.com/acme/widgets.git"}`,
		`{"type":"git_repository","url":"https://token@github.com/acme/widgets.git"}`,
		`{"type":"git_repository","url":"https://github.com/acme/widgets.git","authorization_token":"secret"}`,
		`{"type":"git_repository","url":"https://github.com/acme/widgets.git","checkout":null}`,
	} {
		raw := []json.RawMessage{json.RawMessage(body)}
		if _, _, _, err := parseSessionResourceInputs(&raw); err == nil {
			t.Fatalf("resource accepted: %s", body)
		}
	}
}

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
		{http.MethodDelete, "/v1/sessions/sesn_1/resources/sesrsc_1", ""},
	}
	for _, test := range tests {
		response := do(handler, test.method, test.path, test.body)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s %s status = %d body=%s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}
