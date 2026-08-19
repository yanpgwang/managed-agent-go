package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/yanpgwang/mango/internal/domain"
)

func TestCredentialJSONNeverIncludesRepositorySecretFields(t *testing.T) {
	item := domain.VaultCredential{
		ID: "vcrd_1", VaultID: "vlt_1", CredentialKey: "https://mcp.example/",
		Version: 3, Auth: domain.CredentialAuth{
			Type: domain.CredentialAuthStaticBearer, MCPServerURL: "https://mcp.example/",
		},
		SecretEnvelope: &domain.SecretEnvelope{
			Version: 1, Algorithm: "AES-256-GCM", KeyID: "operator-secret-key-id",
			Nonce: []byte("nonce"), Ciphertext: []byte("ciphertext"),
		},
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
	}
	body, err := json.Marshal(credentialToJSON(item))
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(body)
	for _, forbidden := range []string{"credential_key", "secret_envelope", "ciphertext", "operator-secret-key-id", "version"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("response contains %q: %s", forbidden, serialized)
		}
	}
}

func TestEnvironmentVariableCredentialParserIsExplicitlyUnsupported(t *testing.T) {
	_, err := parseCredentialAuthCreate(json.RawMessage(`{
        "type":"environment_variable",
        "secret_name":"TOKEN",
        "secret_value":"secret"
    }`))
	if err == nil {
		t.Fatal("environment_variable credential was accepted")
	}
	if domainErr, ok := err.(*domain.DomainError); !ok || domainErr.Kind != domain.KindUnsupported {
		t.Fatalf("error = %v, want unsupported", err)
	}
}
