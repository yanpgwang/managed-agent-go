package secretcrypto

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAESGCMKeyringFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyring.json")
	encoded := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	body := []byte(`{"active_key_id":"k1","keys":{"k1":"` + encoded + `"}}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	keyring, err := LoadAESGCMKeyringFile(path)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := keyring.Seal([]byte("secret"), []byte("aad"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := keyring.Open(envelope, []byte("aad"))
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("open = %q, %v", plaintext, err)
	}
}
