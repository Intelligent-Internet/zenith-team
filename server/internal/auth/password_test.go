package auth

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

func TestPasswordHasherNeverStoresOrComparesPlaintext(t *testing.T) {
	t.Parallel()

	hasher := newTestPasswordHasher(t)
	hash, err := hasher.Hash("correct horse battery staple")
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	if hash == "correct horse battery staple" {
		t.Fatal("Hash() returned the plaintext password")
	}
	if !hasher.Verify(hash, "correct horse battery staple") {
		t.Fatal("Verify() rejected the matching password")
	}
	if hasher.Verify(hash, "wrong password") {
		t.Fatal("Verify() accepted a different password")
	}
}

func TestPasswordHasherRejectsMalformedOrUnboundedStoredParameters(t *testing.T) {
	t.Parallel()

	hasher := newTestPasswordHasher(t)
	salt := base64.RawStdEncoding.EncodeToString(make([]byte, 16))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	for name, encoded := range map[string]string{
		"memory over 64 MiB":      "$argon2id$v=19$m=65537,t=1,p=1$" + salt + "$" + key,
		"iterations over cap":     "$argon2id$v=19$m=64,t=11,p=1$" + salt + "$" + key,
		"parallelism over cap":    "$argon2id$v=19$m=64,t=1,p=9$" + salt + "$" + key,
		"noncanonical parameters": "$argon2id$v=19$m=00064,t=1,p=1$" + salt + "$" + key,
		"short salt":              "$argon2id$v=19$m=64,t=1,p=1$YQ$" + key,
		"oversized encoding":      "$argon2id$v=19$m=64,t=1,p=1$" + strings.Repeat("A", 1024) + "$" + key,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, _, err := parseArgon2idHash(encoded); err == nil {
				t.Fatal("parseArgon2idHash() error = nil")
			}
			if hasher.Verify(encoded, "password") {
				t.Fatal("Verify() accepted an unsafe stored hash")
			}
		})
	}
}

func TestPasswordHasherSupportsTheServiceMaximumPasswordLength(t *testing.T) {
	t.Parallel()

	hasher := newTestPasswordHasher(t)
	password := strings.Repeat("a", 128)
	hash, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash(128 bytes) error = %v", err)
	}
	if !hasher.Verify(hash, password) {
		t.Fatal("Verify() rejected the 128-byte password")
	}
}

func newTestPasswordHasher(t *testing.T) *PasswordHasher {
	t.Helper()
	hasher, err := NewPasswordHasher(PasswordHasherOptions{
		MemoryKiB:   64,
		Iterations:  1,
		Parallelism: 1,
		SaltLength:  16,
		KeyLength:   32,
		Random:      bytes.NewReader(make([]byte, 1024)),
	})
	if err != nil {
		t.Fatalf("NewPasswordHasher() error = %v", err)
	}
	return hasher
}
