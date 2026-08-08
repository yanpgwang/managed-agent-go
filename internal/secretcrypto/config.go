package secretcrypto

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const maxKeyringFileBytes = 64 * 1024

type keyringFile struct {
	ActiveKeyID string            `json:"active_key_id"`
	Keys        map[string]string `json:"keys"`
}

// LoadAESGCMKeyringFile loads an operator-mounted keyring. Values are standard
// base64-encoded 32-byte AES keys. Keeping this parser in the crypto package
// gives API and worker processes one strict configuration contract.
func LoadAESGCMKeyringFile(path string) (*AESGCMKeyring, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secret keyring: %w", err)
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, maxKeyringFileBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read secret keyring: %w", err)
	}
	defer Zero(body)
	if len(body) > maxKeyringFileBytes {
		return nil, errors.New("secret keyring file exceeds 64 KiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var config keyringFile
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode secret keyring: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("decode secret keyring: trailing JSON value")
	}
	keys := make(map[string][]byte, len(config.Keys))
	defer func() {
		for _, key := range keys {
			Zero(key)
		}
	}()
	for id, encoded := range config.Keys {
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode secret keyring key %q: invalid base64", id)
		}
		keys[id] = key
	}
	return NewAESGCMKeyring(config.ActiveKeyID, keys)
}
