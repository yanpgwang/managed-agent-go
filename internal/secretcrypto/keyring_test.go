package secretcrypto

import (
	"bytes"
	"errors"
	"testing"
)

func TestAESGCMKeyringRoundTripAndAADBinding(t *testing.T) {
	keyring, err := newAESGCMKeyring("current", map[string][]byte{
		"current": bytes.Repeat([]byte{1}, 32),
	}, bytes.NewReader(bytes.Repeat([]byte{2}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := keyring.Seal([]byte("secret"), []byte("vault:v1"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := keyring.Open(envelope, []byte("vault:v1"))
	if err != nil || string(plaintext) != "secret" {
		t.Fatalf("open = %q, %v", plaintext, err)
	}
	if _, err := keyring.Open(envelope, []byte("vault:v2")); !errors.Is(err, ErrOpen) {
		t.Fatalf("wrong AAD error = %v, want ErrOpen", err)
	}
}

func TestAESGCMKeyringDecryptsOldKey(t *testing.T) {
	old, err := newAESGCMKeyring("old", map[string][]byte{
		"old": bytes.Repeat([]byte{3}, 32),
	}, bytes.NewReader(bytes.Repeat([]byte{4}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := old.Seal([]byte("rotate"), nil)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := NewAESGCMKeyring("new", map[string][]byte{
		"old": bytes.Repeat([]byte{3}, 32),
		"new": bytes.Repeat([]byte{5}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := rotated.Open(envelope, nil)
	if err != nil || string(plaintext) != "rotate" {
		t.Fatalf("open old envelope = %q, %v", plaintext, err)
	}
}

func TestAESGCMKeyringRejectsPostgresUnsafeKeyIDs(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	for _, test := range []struct {
		name   string
		active string
		keys   map[string][]byte
	}{
		{name: "active NUL", active: "current\x00bad", keys: map[string][]byte{"current\x00bad": key}},
		{name: "inactive NUL", active: "current", keys: map[string][]byte{"current": key, "old\x00bad": key}},
		{name: "invalid UTF-8", active: "current", keys: map[string][]byte{"current": key, string([]byte{0xff}): key}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewAESGCMKeyring(test.active, test.keys); err == nil {
				t.Fatal("NewAESGCMKeyring succeeded")
			}
		})
	}
}
