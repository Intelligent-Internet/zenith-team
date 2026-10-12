package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"boardly.dev/boardly/server/internal/db"
)

func TestNewDesktopHandoffServiceRequiresStoreAndTokens(t *testing.T) {
	t.Parallel()

	tokens, err := NewTokenManager(TokenManagerOptions{
		Secret: []byte("01234567890123456789012345678901"), AccessTTL: time.Minute, RefreshTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}

	if _, err := NewDesktopHandoffService(DesktopHandoffOptions{Tokens: tokens}); err == nil {
		t.Fatal("NewDesktopHandoffService() without store error = nil")
	}
	if _, err := NewDesktopHandoffService(DesktopHandoffOptions{Store: &db.Store{}}); err == nil {
		t.Fatal("NewDesktopHandoffService() without tokens error = nil")
	}
	if _, err := NewDesktopHandoffService(DesktopHandoffOptions{
		Store: &db.Store{}, Tokens: tokens, TTL: -time.Second,
	}); err == nil {
		t.Fatal("NewDesktopHandoffService() with negative TTL error = nil")
	}
}

// TestDesktopHandoffServiceCompleteRejectsNonCanonicalCodeWithoutTouchingStorage
// pins that code-shape validation runs before any side effect: the service's
// store owns no pool, so a consume attempt would panic rather than run.
func TestDesktopHandoffServiceCompleteRejectsNonCanonicalCodeWithoutTouchingStorage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
	}{
		{name: "empty"},
		{name: "short", code: strings.Repeat("a", 42)},
		{name: "long", code: strings.Repeat("a", 44)},
		{name: "padded", code: strings.Repeat("a", 42) + "="},
		{name: "noncanonical alphabet", code: strings.Repeat("_", 43)},
		{name: "non-ASCII", code: strings.Repeat("a", 42) + "é"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newStorelessDesktopHandoffService(t)

			_, err := service.Complete(context.Background(), DesktopHandoffCompleteInput{Code: test.code})
			if !errors.Is(err, ErrDesktopHandoffCodeInvalid) {
				t.Fatalf("Complete(%q) error = %v, want ErrDesktopHandoffCodeInvalid", test.code, err)
			}
		})
	}
}

// TestDesktopHandoffServiceCompleteRejectsMalformedClientMetadataBeforeConsuming
// pins the second pre-storage gate: a canonical code paired with metadata
// outside the closed allowlist is rejected before the code is burned.
func TestDesktopHandoffServiceCompleteRejectsMalformedClientMetadataBeforeConsuming(t *testing.T) {
	t.Parallel()

	code := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	for _, metadata := range []OAuthClientMetadata{
		{AppVersion: string([]byte{0xff})},
		{AppVersion: "2.0.0\nprovider-code"},
		{AppVersion: strings.Repeat("a", 256)},
		{Platform: "darwin\x00provider-token"},
		{Platform: strings.Repeat("p", 256)},
	} {
		service := newStorelessDesktopHandoffService(t)
		if _, err := service.Complete(context.Background(), DesktopHandoffCompleteInput{
			Code: code, ClientMetadata: metadata,
		}); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("Complete(%#v) error = %v, want ErrInvalidInput", metadata, err)
		}
	}
}

// TestDesktopHandoffClientMetadataDetailsOmitsEmptyFields pins the envelope
// narrowing that keeps a desktop session's client_metadata identical in shape
// to the one Login and Signup persist.
func TestDesktopHandoffClientMetadataDetailsOmitsEmptyFields(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		metadata OAuthClientMetadata
		want     string
	}{
		{name: "empty", want: `{"client":"desktop"}`},
		{
			name:     "both fields",
			metadata: OAuthClientMetadata{AppVersion: "2.0.0", Platform: "darwin-arm64"},
			want:     `{"client":"desktop","details":{"appVersion":"2.0.0","platform":"darwin-arm64"}}`,
		},
		{
			name:     "platform only",
			metadata: OAuthClientMetadata{Platform: "darwin-arm64"},
			want:     `{"client":"desktop","details":{"platform":"darwin-arm64"}}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := encodeClientMetadata(
				ClientDesktop,
				desktopHandoffClientMetadataDetails(test.metadata),
			)
			if err != nil {
				t.Fatalf("encodeClientMetadata() error = %v", err)
			}
			if string(encoded) != test.want {
				t.Fatalf("client metadata = %s, want %s", encoded, test.want)
			}
		})
	}
}

// newStorelessDesktopHandoffService builds a service over a store that owns no
// pool: any storage call panics, which is what makes "rejected before any side
// effect" an assertion rather than a comment.
func newStorelessDesktopHandoffService(t *testing.T) *DefaultDesktopHandoffService {
	t.Helper()
	now := time.Date(2026, time.July, 18, 10, 0, 0, 0, time.UTC)
	tokens, err := NewTokenManager(TokenManagerOptions{
		Secret:     []byte("01234567890123456789012345678901"),
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
		Now:        func() time.Time { return now },
		Random:     bytes.NewReader(make([]byte, 256)),
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	service, err := NewDesktopHandoffService(DesktopHandoffOptions{
		Store:  &db.Store{},
		Tokens: tokens,
		TTL:    60 * time.Second,
		Now:    func() time.Time { return now },
		Random: bytes.NewReader(make([]byte, 32)),
	})
	if err != nil {
		t.Fatalf("NewDesktopHandoffService() error = %v", err)
	}
	return service
}
