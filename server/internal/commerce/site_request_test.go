package commerce

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestSitePublicJWKUsesRFC7638Fingerprint(t *testing.T) {
	t.Parallel()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	jwk := sitePublicJWKFromKey(&privateKey.PublicKey)
	fingerprint, err := jwk.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if jwk.KeyID != fingerprint || len(fingerprint) != 43 {
		t.Fatalf("fingerprint = %q, key ID = %q", fingerprint, jwk.KeyID)
	}
	publicKey, err := jwk.PublicKey()
	if err != nil {
		t.Fatalf("PublicKey() error = %v", err)
	}
	if !publicKey.Equal(&privateKey.PublicKey) {
		t.Fatal("PublicKey() did not preserve the P-256 key")
	}
}

func TestVerifySiteRequestTokenBindsTheWholeRequest(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	jwk := sitePublicJWKFromKey(&privateKey.PublicKey)
	siteID := uuid.MustParse("01900000-0000-4000-8000-000000000101")
	operationID := uuid.MustParse("01900000-0000-4000-8000-000000000102")
	bodyDigest := sha256.Sum256([]byte(`{"offerAlias":"pro","operationId":"01900000-0000-4000-8000-000000000102"}`))
	claims := siteRequestClaims{
		SchemaVersion:         1,
		Environment:           EnvironmentSandbox,
		Origin:                "http://localhost:3000",
		SessionID:             "session-1",
		AuthenticatedAt:       now.Add(-time.Minute).Format(time.RFC3339),
		AuthenticationMethods: []string{"password"},
		Action:                siteRequestActionCheckout,
		Method:                "POST",
		Path:                  "/api/v1/site-commerce/checkout-sessions",
		OperationID:           operationID.String(),
		BodySHA256:            hex.EncodeToString(bodyDigest[:]),
		Nonce:                 "0123456789012345678901234567890123456789012",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: siteID.String(), Subject: "customer-1",
			Audience: jwt.ClaimStrings{siteRequestAudience},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(siteRequestMaximumTTL)),
			ID:        operationID.String(),
		},
	}
	raw := signSiteRequestForTest(t, privateKey, jwk.KeyID, claims)
	expectation := siteRequestExpectation{
		SiteID: siteID, Environment: EnvironmentSandbox,
		Origin: "http://localhost:3000", Action: siteRequestActionCheckout,
		Method: "POST", Path: "/api/v1/site-commerce/checkout-sessions",
		OperationID: operationID, BodySHA256: bodyDigest, RequireNonce: true,
	}
	identity, err := verifySiteRequestToken(raw, jwk, expectation, now)
	if err != nil {
		t.Fatalf("verifySiteRequestToken() error = %v", err)
	}
	if identity.Subject != "customer-1" || identity.SessionID != "session-1" {
		t.Fatalf("identity = %#v", identity)
	}
	if _, err := verifySiteRequestToken(
		raw,
		jwk,
		expectation,
		now.Add(siteRequestMaximumTTL),
	); err == nil {
		t.Fatal("verifySiteRequestToken() accepted a token at its exact expiration")
	}

	tests := []struct {
		name   string
		mutate func(*siteRequestExpectation)
	}{
		{name: "cross origin", mutate: func(value *siteRequestExpectation) {
			value.Origin = "https://attacker.example"
		}},
		{name: "cross environment", mutate: func(value *siteRequestExpectation) {
			value.Environment = EnvironmentLive
		}},
		{name: "different body", mutate: func(value *siteRequestExpectation) {
			value.BodySHA256 = sha256.Sum256([]byte("{}"))
		}},
		{name: "different path", mutate: func(value *siteRequestExpectation) {
			value.Path = "/api/v1/site-commerce/portal-sessions"
		}},
		{name: "cross site", mutate: func(value *siteRequestExpectation) {
			value.SiteID = uuid.MustParse("01900000-0000-4000-8000-000000000103")
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			changed := expectation
			test.mutate(&changed)
			if _, err := verifySiteRequestToken(raw, jwk, changed, now); err == nil {
				t.Fatal("verifySiteRequestToken() accepted a request with a changed binding")
			}
		})
	}
}

func TestVerifySiteRequestTokenRejectsTamperingAndWrongSigningKey(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	wrongKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(wrong) error = %v", err)
	}
	jwk := sitePublicJWKFromKey(&privateKey.PublicKey)
	siteID := uuid.MustParse("01900000-0000-4000-8000-000000000121")
	operationID := uuid.MustParse("01900000-0000-4000-8000-000000000122")
	claims := validEntitlementClaims(now, siteID, operationID)
	expectation := siteRequestExpectation{
		SiteID: siteID, Environment: EnvironmentSandbox,
		Origin: "http://localhost:3000", Action: siteRequestActionEntitlements,
		Method: "POST", Path: "/api/v1/site-commerce/entitlements/verify",
		BodySHA256: sha256.Sum256(nil),
	}
	valid := signSiteRequestForTest(t, privateKey, jwk.KeyID, claims)
	parts := strings.Split(valid, ".")
	if parts[2][0] == 'A' {
		parts[2] = "B" + parts[2][1:]
	} else {
		parts[2] = "A" + parts[2][1:]
	}
	tampered := strings.Join(parts, ".")
	wronglySigned := signSiteRequestForTest(t, wrongKey, jwk.KeyID, claims)
	for name, raw := range map[string]string{
		"tampered signature": tampered,
		"wrong signing key":  wronglySigned,
	} {
		if _, err := verifySiteRequestToken(raw, jwk, expectation, now); err == nil {
			t.Fatalf("verifySiteRequestToken() accepted %s", name)
		}
	}
}

func TestVerifySiteRequestTokenAcceptsDistinctEntitlementOperationAndJWTIDs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	jwk := sitePublicJWKFromKey(&privateKey.PublicKey)
	siteID := uuid.MustParse("01900000-0000-4000-8000-000000000131")
	operationID := uuid.MustParse("01900000-0000-4000-8000-000000000132")
	claims := validEntitlementClaims(now, siteID, operationID)
	claims.ID = "01900000-0000-4000-8000-000000000133"
	raw := signSiteRequestForTest(t, privateKey, jwk.KeyID, claims)
	identity, err := verifySiteRequestToken(raw, jwk, siteRequestExpectation{
		SiteID: siteID, Environment: EnvironmentSandbox,
		Origin: "http://localhost:3000", Action: siteRequestActionEntitlements,
		Method: "POST", Path: "/api/v1/site-commerce/entitlements/verify",
		BodySHA256: sha256.Sum256(nil),
	}, now)
	if err != nil {
		t.Fatalf("verifySiteRequestToken() error = %v", err)
	}
	if identity.OperationID != operationID {
		t.Fatalf("operation ID = %s, want %s", identity.OperationID, operationID)
	}
}

func TestVerifySiteRequestTokenRejectsClockSkewThatExtendsWallClockLifetime(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	jwk := sitePublicJWKFromKey(&privateKey.PublicKey)
	siteID := uuid.MustParse("01900000-0000-4000-8000-000000000141")
	operationID := uuid.MustParse("01900000-0000-4000-8000-000000000142")
	claims := validEntitlementClaims(now, siteID, operationID)
	futureIssuedAt := now.Add(siteRequestClockSkew)
	claims.IssuedAt = jwt.NewNumericDate(futureIssuedAt)
	claims.NotBefore = jwt.NewNumericDate(futureIssuedAt)
	claims.ExpiresAt = jwt.NewNumericDate(futureIssuedAt.Add(55 * time.Second))
	boundaryRaw := signSiteRequestForTest(t, privateKey, jwk.KeyID, claims)
	expectation := siteRequestExpectation{
		SiteID: siteID, Environment: EnvironmentSandbox,
		Origin: "http://localhost:3000", Action: siteRequestActionEntitlements,
		Method: "POST", Path: "/api/v1/site-commerce/entitlements/verify",
		BodySHA256: sha256.Sum256(nil),
	}
	if _, err := verifySiteRequestToken(boundaryRaw, jwk, expectation, now); err != nil {
		t.Fatalf("verifySiteRequestToken(60 second wall-clock boundary) error = %v", err)
	}
	claims.ExpiresAt = jwt.NewNumericDate(futureIssuedAt.Add(siteRequestMaximumTTL))
	raw := signSiteRequestForTest(t, privateKey, jwk.KeyID, claims)
	_, err = verifySiteRequestToken(raw, jwk, expectation, now)
	if err == nil {
		t.Fatal("verifySiteRequestToken() accepted exp more than 60 seconds from now")
	}
}

func TestVerifySiteRequestTokenRejectsJOSEConfusion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	jwk := sitePublicJWKFromKey(&privateKey.PublicKey)
	siteID := uuid.MustParse("01900000-0000-4000-8000-000000000111")
	operationID := uuid.MustParse("01900000-0000-4000-8000-000000000112")
	claims := siteRequestClaims{
		SchemaVersion: 1, Environment: EnvironmentSandbox,
		Origin: "http://localhost:3000", SessionID: "session-1",
		AuthenticatedAt: now.Format(time.RFC3339), AuthenticationMethods: []string{"password"},
		Action: siteRequestActionEntitlements, Method: "POST",
		Path: "/api/v1/site-commerce/entitlements/verify", OperationID: operationID.String(),
		BodySHA256: hex.EncodeToString(sha256.New().Sum(nil)),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: siteID.String(), Subject: "customer-1",
			Audience: jwt.ClaimStrings{siteRequestAudience},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(siteRequestMaximumTTL)), ID: operationID.String(),
		},
	}
	expectation := siteRequestExpectation{
		SiteID: siteID, Environment: EnvironmentSandbox,
		Origin: "http://localhost:3000", Action: siteRequestActionEntitlements,
		Method: "POST", Path: "/api/v1/site-commerce/entitlements/verify",
		OperationID: operationID, BodySHA256: sha256.Sum256(nil),
	}

	tests := []struct {
		name string
		typ  string
		kid  string
	}{
		{name: "wrong type", typ: "JWT", kid: jwk.KeyID},
		{name: "wrong key ID", typ: siteRequestTokenType, kid: "attacker"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
			token.Header["typ"] = test.typ
			token.Header["kid"] = test.kid
			raw, signErr := token.SignedString(privateKey)
			if signErr != nil {
				t.Fatalf("SignedString() error = %v", signErr)
			}
			if _, err := verifySiteRequestToken(raw, jwk, expectation, now); err == nil {
				t.Fatal("verifySiteRequestToken() accepted confused JOSE metadata")
			}
		})
	}
	t.Run("wrong algorithm", func(t *testing.T) {
		t.Parallel()
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		token.Header["typ"] = siteRequestTokenType
		token.Header["kid"] = jwk.KeyID
		raw, signErr := token.SignedString([]byte("attacker-secret"))
		if signErr != nil {
			t.Fatalf("SignedString() error = %v", signErr)
		}
		if _, err := verifySiteRequestToken(raw, jwk, expectation, now); err == nil {
			t.Fatal("verifySiteRequestToken() accepted HS256")
		}
	})
}

func validEntitlementClaims(
	now time.Time,
	siteID uuid.UUID,
	operationID uuid.UUID,
) siteRequestClaims {
	return siteRequestClaims{
		SchemaVersion: 1, Environment: EnvironmentSandbox,
		Origin: "http://localhost:3000", SessionID: "session-1",
		AuthenticatedAt: now.Format(time.RFC3339), AuthenticationMethods: []string{"password"},
		Action: siteRequestActionEntitlements, Method: "POST",
		Path: "/api/v1/site-commerce/entitlements/verify", OperationID: operationID.String(),
		BodySHA256: hex.EncodeToString(sha256.New().Sum(nil)),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: siteID.String(), Subject: "customer-1",
			Audience: jwt.ClaimStrings{siteRequestAudience},
			IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(siteRequestMaximumTTL)), ID: operationID.String(),
		},
	}
}

func signSiteRequestForTest(
	t *testing.T,
	privateKey *ecdsa.PrivateKey,
	keyID string,
	claims siteRequestClaims,
) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["typ"] = siteRequestTokenType
	token.Header["kid"] = keyID
	raw, err := token.SignedString(privateKey)
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return raw
}
