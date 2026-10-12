package daemon

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

func commercePublicJWK(key *ecdsa.PrivateKey) (CommercePublicJWK, string, error) {
	if key == nil || key.Curve != elliptic.P256() {
		return CommercePublicJWK{}, "", errors.New("local Commerce signing key must be P-256")
	}
	publicKey, err := key.PublicKey.Bytes()
	if err != nil || len(publicKey) != 65 || publicKey[0] != 4 {
		return CommercePublicJWK{}, "", errors.New("local Commerce signing key material is invalid")
	}
	jwk := CommercePublicJWK{
		Kty: "EC",
		Crv: "P-256",
		X:   base64.RawURLEncoding.EncodeToString(publicKey[1:33]),
		Y:   base64.RawURLEncoding.EncodeToString(publicKey[33:65]),
	}
	fingerprint, err := commercePublicJWKFingerprint(jwk)
	return jwk, fingerprint, err
}

func commercePublicJWKFingerprint(jwk CommercePublicJWK) (string, error) {
	if jwk.Kty != "EC" || jwk.Crv != "P-256" {
		return "", errors.New("local Commerce public key must be P-256")
	}
	x, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil || len(x) != 32 || base64.RawURLEncoding.EncodeToString(x) != jwk.X {
		return "", errors.New("local Commerce public key x coordinate is invalid")
	}
	y, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil || len(y) != 32 || base64.RawURLEncoding.EncodeToString(y) != jwk.Y {
		return "", errors.New("local Commerce public key y coordinate is invalid")
	}
	uncompressed := make([]byte, 1+len(x)+len(y))
	uncompressed[0] = 4
	copy(uncompressed[1:], x)
	copy(uncompressed[1+len(x):], y)
	if _, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), uncompressed); err != nil {
		return "", errors.New("local Commerce public key point is invalid")
	}
	thumbprintDocument := []byte(
		`{"crv":"P-256","kty":"EC","x":"` + jwk.X + `","y":"` + jwk.Y + `"}`,
	)
	digest := sha256.Sum256(thumbprintDocument)
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

// writePrivateFile installs data at path readable by this user alone: the
// directory is 0700, the bytes land in a temporary sibling that is chmod 0600,
// written, synced and renamed into place, so a reader never sees a partial
// file and nothing else on the machine can read it.
func writePrivateFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create private state directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("secure private state directory: %w", err)
		}
	}
	temporary, err := os.CreateTemp(directory, ".private-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary private file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("secure temporary private file: %w", err)
	}
	if _, err := io.Copy(temporary, bytes.NewReader(data)); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary private file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync temporary private file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary private file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install private file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("secure private file: %w", err)
		}
	}
	return nil
}
