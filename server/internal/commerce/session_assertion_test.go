package commerce

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestSessionAssertionSignerIssuesSiteBoundES256Assertion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 24, 9, 10, 11, 0, time.UTC)
	signer, err := newSessionAssertionSigner(bytes.Repeat([]byte{0x42}, 32), func() time.Time {
		return now
	})
	if err != nil {
		t.Fatalf("newSessionAssertionSigner() error = %v", err)
	}
	siteID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca65001")
	issued, err := signer.Issue(SessionAssertionInput{
		Environment: EnvironmentLive,
		Audience:    "https://store.example",
		SiteID:      siteID,
		Subject:     "site-user-123",
		SessionID:   "site-session-456",
		Entitlements: []Entitlement{{
			Key: "plan.pro", Status: "active", UpdatedAt: now.Add(-time.Minute),
		}},
	})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if issued.ExpiresAt != now.Add(sessionAssertionTTL) || issued.Assertion == "" {
		t.Fatalf("Issue() = %+v", issued)
	}

	claims := new(sessionAssertionClaims)
	token, err := jwt.ParseWithClaims(
		issued.Assertion,
		claims,
		func(token *jwt.Token) (any, error) {
			if token.Header["typ"] != sessionAssertionType ||
				token.Header["kid"] != signer.keyID {
				t.Fatalf("assertion header = %+v", token.Header)
			}
			return &signer.privateKey.PublicKey, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}),
		jwt.WithIssuer(sessionAssertionIssuer),
		jwt.WithAudience("https://store.example"),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(func() time.Time { return now }),
	)
	if err != nil || !token.Valid {
		t.Fatalf("ParseWithClaims() token=%v error=%v", token, err)
	}
	if claims.SchemaVersion != 1 || claims.Subject != "site-user-123" ||
		claims.SiteID != siteID || claims.SessionID != "site-session-456" ||
		len(claims.Entitlements) != 1 || claims.Entitlements[0].Key != "plan.pro" {
		t.Fatalf("assertion claims = %+v", claims)
	}
	if claims.ID == "" {
		t.Fatalf("assertion JTI is empty: %+v", claims.RegisteredClaims)
	}
}

func TestSessionAssertionSignerPublishesStablePublicOnlyJWK(t *testing.T) {
	t.Parallel()
	masterKey := bytes.Repeat([]byte{0x7a}, 32)
	left, err := newSessionAssertionSigner(masterKey, time.Now)
	if err != nil {
		t.Fatalf("newSessionAssertionSigner(left) error = %v", err)
	}
	right, err := newSessionAssertionSigner(masterKey, time.Now)
	if err != nil {
		t.Fatalf("newSessionAssertionSigner(right) error = %v", err)
	}
	leftKeys := left.JWKSet()
	rightKeys := right.JWKSet()
	if len(leftKeys.Keys) != 1 || len(rightKeys.Keys) != 1 ||
		leftKeys.Keys[0] != rightKeys.Keys[0] {
		t.Fatalf("JWK sets are not deterministic: %+v / %+v", leftKeys, rightKeys)
	}
	key := leftKeys.Keys[0]
	if key.KeyType != "EC" || key.Curve != "P-256" || key.Algorithm != "ES256" ||
		key.Use != "sig" || key.KeyID == "" || key.X == "" || key.Y == "" {
		t.Fatalf("JWK = %+v", key)
	}
	if key.KeyID != left.keyID || key.KeyID == base64.RawURLEncoding.EncodeToString(masterKey) {
		t.Fatalf("JWK key ID leaked or mismatched: %q", key.KeyID)
	}
	privateKeyBytes, err := left.privateKey.Bytes()
	if err != nil {
		t.Fatalf("encode derived ES256 key: %v", err)
	}
	parsed, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), privateKeyBytes)
	if err != nil || !left.privateKey.Equal(parsed) {
		t.Fatal("derived ES256 key is invalid")
	}
}

func TestSessionAssertionSignerRejectsInvalidAuthority(t *testing.T) {
	t.Parallel()
	if _, err := newSessionAssertionSigner([]byte("too-short"), time.Now); err == nil {
		t.Fatal("newSessionAssertionSigner() accepted a short master key")
	}
	signer, err := newSessionAssertionSigner(bytes.Repeat([]byte{0x42}, 32), time.Now)
	if err != nil {
		t.Fatalf("newSessionAssertionSigner() error = %v", err)
	}
	for _, input := range []SessionAssertionInput{
		{
			Environment: EnvironmentSandbox, Audience: "http://store.example",
			SiteID: uuid.New(), Subject: "user", SessionID: "session",
		},
		{
			Environment: EnvironmentLive, Audience: "https://store.example",
			SiteID: uuid.Nil, Subject: "user", SessionID: "session",
		},
	} {
		if _, err := signer.Issue(input); err == nil {
			t.Fatalf("Issue(%+v) accepted invalid authority", input)
		}
	}
}

func TestSessionAssertionSignerAllowsLoopbackAudienceOnlyInSandbox(t *testing.T) {
	t.Parallel()
	signer, err := newSessionAssertionSigner(bytes.Repeat([]byte{0x42}, 32), time.Now)
	if err != nil {
		t.Fatalf("newSessionAssertionSigner() error = %v", err)
	}
	tests := []struct {
		name        string
		environment Environment
		audience    string
		wantValid   bool
	}{
		{
			name: "sandbox localhost", environment: EnvironmentSandbox,
			audience: "http://localhost:5173", wantValid: true,
		},
		{
			name: "sandbox IPv4", environment: EnvironmentSandbox,
			audience: "http://127.0.0.1:5173", wantValid: true,
		},
		{
			name: "sandbox IPv6", environment: EnvironmentSandbox,
			audience: "http://[::1]:5173", wantValid: true,
		},
		{
			name: "live localhost", environment: EnvironmentLive,
			audience: "http://localhost:5173",
		},
		{
			name: "sandbox non-loopback", environment: EnvironmentSandbox,
			audience: "http://store.example",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := signer.Issue(SessionAssertionInput{
				Environment: test.environment,
				Audience:    test.audience,
				SiteID:      uuid.New(),
				Subject:     "user",
				SessionID:   "session",
			})
			if got := err == nil; got != test.wantValid {
				t.Fatalf("Issue() valid = %t, want %t; error = %v", got, test.wantValid, err)
			}
		})
	}
}
