package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	cubesandbox "github.com/tencentcloud/CubeSandbox/sdk/go"
)

func TestE2BTransportAdaptsCloudControlPlane(t *testing.T) {
	var gotAPIKey string
	var gotAuthorization string
	var gotTimeout int
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		gotAPIKey = request.Header.Get("X-API-Key")
		gotAuthorization = request.Header.Get("Authorization")
		var payload struct {
			Timeout int `json:"timeout"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode connect payload: %v", err)
		}
		gotTimeout = payload.Timeout
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client, err := newE2BTransport(
		server.Client(),
		server.URL,
		"test-api-key",
		7*time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		server.URL+"/sandboxes/sbx-1/connect",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer should-be-replaced")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, response.Body)

	if gotAPIKey != "test-api-key" {
		t.Fatalf("X-API-Key = %q", gotAPIKey)
	}
	if gotAuthorization != "" {
		t.Fatalf("Authorization was forwarded to E2B: %q", gotAuthorization)
	}
	if gotTimeout != 420 {
		t.Fatalf("connect timeout = %d, want 420", gotTimeout)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("normalized connect status = %d, want 200", response.StatusCode)
	}
}

func TestRemoteProviderConstructorsRejectMissingRequiredConfig(t *testing.T) {
	if _, err := NewE2BProvider(E2BConfig{}); err == nil {
		t.Fatal("NewE2BProvider accepted an empty API key")
	}
	if _, err := NewCubeProvider(CubeConfig{}); err == nil {
		t.Fatal("NewCubeProvider accepted an empty API URL and template")
	}
	if _, err := NewOpenSandboxProvider(OpenSandboxConfig{}); err == nil {
		t.Fatal("NewOpenSandboxProvider accepted an empty base URL")
	}
	if _, err := NewDaytonaProvider(DaytonaConfig{}); err == nil {
		t.Fatal("NewDaytonaProvider accepted an empty API key")
	}
}

func TestE2BControlClientListsEveryPageWithMetadataFilter(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		requests++
		if request.URL.Path != "/v2/sandboxes" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.URL.Query().Get("limit") != "100" {
			t.Errorf("limit = %q", request.URL.Query().Get("limit"))
		}
		if request.URL.Query().Get("state") != "running,paused" {
			t.Errorf("state = %q", request.URL.Query().Get("state"))
		}
		metadata, err := url.ParseQuery(request.URL.Query().Get("metadata"))
		if err != nil {
			t.Errorf("parse metadata: %v", err)
		}
		if metadata.Get(remoteManagedKey) != remoteManagedValue ||
			metadata.Get(remoteSessionKey) != "session-hash" {
			t.Errorf("metadata = %v", metadata)
		}
		if request.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("X-API-Key = %q", request.Header.Get("X-API-Key"))
		}

		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("nextToken") == "" {
			writer.Header().Set("X-Next-Token", "page-2")
			_, _ = io.WriteString(
				writer,
				`[{"sandboxID":"sbx-1","metadata":{"page":"1"}}]`,
			)
			return
		}
		if request.URL.Query().Get("nextToken") != "page-2" {
			t.Errorf("nextToken = %q", request.URL.Query().Get("nextToken"))
		}
		_, _ = io.WriteString(
			writer,
			`[{"sandboxID":"sbx-2","metadata":{"page":"2"}}]`,
		)
	}))
	defer server.Close()

	client := &e2bControlClient{
		apiURL: server.URL,
		apiKey: "test-key",
		client: server.Client(),
	}
	items, err := client.List(context.Background(), map[string]string{
		remoteManagedKey: remoteManagedValue,
		remoteSessionKey: "session-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if len(items) != 2 ||
		items[0].SandboxID != "sbx-1" ||
		items[1].SandboxID != "sbx-2" {
		t.Fatalf("items = %+v", items)
	}
}

func TestE2BControlClientGetMapsNotFound(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()

	client := &e2bControlClient{
		apiURL: server.URL,
		client: server.Client(),
	}
	_, err := client.Get(context.Background(), "missing/id")
	if !errors.Is(err, cubesandbox.ErrSandboxNotFound) {
		t.Fatalf("Get error = %v, want ErrSandboxNotFound", err)
	}
}

func TestE2BControlClientRejectsRepeatedPageToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("X-Next-Token", "same-token")
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()

	client := &e2bControlClient{
		apiURL: server.URL,
		client: server.Client(),
	}
	_, err := client.List(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatalf("List error = %v, want repeated-token error", err)
	}
}

func TestE2BCreateUsesDocumentedDefaultDenyField(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost || request.URL.Path != "/sandboxes" {
			t.Errorf("%s %s", request.Method, request.URL.Path)
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode create payload: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(
			writer,
			`{"sandboxID":"sbx-1","templateID":"base","clientID":"client-1","envdVersion":"1.0.0"}`,
		)
	}))
	defer server.Close()

	client := cubesandbox.NewClient(
		cubesandbox.Config{
			APIURL:     server.URL,
			TemplateID: defaultE2BTemplate,
		},
		cubesandbox.WithHTTPClient(server.Client()),
	)
	service := &cubeSDKService{
		name:        E2BProviderName,
		client:      client,
		idleTimeout: time.Minute,
	}
	remote, err := service.Create(context.Background(), "session-1", Spec{})
	if err != nil {
		t.Fatal(err)
	}
	if remote.ID() != "sbx-1" {
		t.Fatalf("sandbox ID = %q", remote.ID())
	}
	if value, ok := payload["allow_internet_access"].(bool); !ok || value {
		t.Fatalf("allow_internet_access = %#v, want false", payload["allow_internet_access"])
	}
	if _, exists := payload["allowInternetAccess"]; exists {
		t.Fatalf("unexpected camel-case network field in payload: %v", payload)
	}
}

func TestRemoteControlHTTPClientHasBoundedDefault(t *testing.T) {
	client := remoteControlHTTPClient(nil)
	if client.Timeout != remoteDefaultPeriod {
		t.Fatalf("timeout = %s, want %s", client.Timeout, remoteDefaultPeriod)
	}

	custom := &http.Client{Timeout: 17 * time.Second}
	cloned := remoteControlHTTPClient(custom)
	if cloned == custom {
		t.Fatal("remoteControlHTTPClient mutated the caller-owned client")
	}
	if cloned.Timeout != custom.Timeout {
		t.Fatalf("custom timeout = %s, want %s", cloned.Timeout, custom.Timeout)
	}
}
