package social

import (
	"bytes"
	"testing"
)

func TestCredentialsAreBoundToOwnerAndTamperEvident(t *testing.T) {
	t.Parallel()
	vault, err := newVault(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := vault.seal([]byte("access-secret"), "user-a:x")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, []byte("access-secret")) {
		t.Fatal("plaintext token stored")
	}
	if _, err := vault.open(encrypted, "user-b:x"); err == nil {
		t.Fatal("cross-user decrypt succeeded")
	}
	decrypted, err := vault.open(encrypted, "user-a:x")
	if err != nil || string(decrypted) != "access-secret" {
		t.Fatalf("decrypt = %q, %v", decrypted, err)
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err := vault.open(encrypted, "user-a:x"); err == nil {
		t.Fatal("tampered token accepted")
	}
}
