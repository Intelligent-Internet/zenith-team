package commerce

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	sessionAssertionIssuer   = "boardly-commerce"
	sessionAssertionType     = "boardly-commerce+jwt"
	sessionAssertionTTL      = 5 * time.Minute
	sessionJWKKeyUse         = "sig"
	sessionSigningKeyPurpose = "boardly-commerce-session-signing-v1"
)

type SessionJWK struct {
	KeyType   string `json:"kty"`
	Use       string `json:"use"`
	Curve     string `json:"crv"`
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	X         string `json:"x"`
	Y         string `json:"y"`
}

type SessionJWKSet struct {
	Keys []SessionJWK `json:"keys"`
}

type SessionAssertionInput struct {
	Environment  Environment
	Audience     string
	SiteID       uuid.UUID
	Subject      string
	SessionID    string
	Entitlements []Entitlement
}

type SessionAssertion struct {
	Assertion string    `json:"assertion"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type sessionAssertionClaims struct {
	SchemaVersion int           `json:"schemaVersion"`
	Environment   Environment   `json:"environment"`
	SiteID        uuid.UUID     `json:"siteId"`
	SessionID     string        `json:"sessionId"`
	Entitlements  []Entitlement `json:"entitlements"`
	jwt.RegisteredClaims
}

type sessionAssertionSigner struct {
	privateKey     *ecdsa.PrivateKey
	publicKeyBytes []byte
	keyID          string
	now            func() time.Time
}

func newSessionAssertionSigner(
	masterKey []byte,
	now func() time.Time,
) (*sessionAssertionSigner, error) {
	if len(masterKey) < sha256.Size {
		return nil, errors.New("Commerce session signing master key must contain at least 32 bytes")
	}
	if now == nil {
		now = time.Now
	}
	privateKey, err := deriveCommerceSigningKey(masterKey, sessionSigningKeyPurpose)
	if err != nil {
		return nil, errors.New("derive Commerce session signing key")
	}
	encodedPublicKey, err := privateKey.PublicKey.Bytes()
	if err != nil || len(encodedPublicKey) != 65 || encodedPublicKey[0] != 4 {
		return nil, errors.New("encode Commerce session signing public key")
	}
	keyDigest := sha256.Sum256(encodedPublicKey)
	keyID := "bcs1_" + base64.RawURLEncoding.EncodeToString(keyDigest[:16])
	return &sessionAssertionSigner{
		privateKey:     privateKey,
		publicKeyBytes: append([]byte(nil), encodedPublicKey...),
		keyID:          keyID,
		now:            now,
	}, nil
}

func (signer *sessionAssertionSigner) Issue(
	input SessionAssertionInput,
) (SessionAssertion, error) {
	if signer == nil || signer.privateKey == nil ||
		!validSiteOrigin(input.Audience, input.Environment) ||
		input.SiteID == uuid.Nil || !validSiteIdentityValue(input.Subject, 255) ||
		!validSiteIdentityValue(input.SessionID, 255) {
		return SessionAssertion{}, ErrInvalidInput
	}
	now := signer.now().UTC().Truncate(time.Second)
	expiresAt := now.Add(sessionAssertionTTL)
	assertionID, err := uuid.NewV7()
	if err != nil {
		return SessionAssertion{}, errors.New("generate Commerce session assertion ID")
	}
	claims := sessionAssertionClaims{
		SchemaVersion: 1,
		Environment:   input.Environment,
		SiteID:        input.SiteID,
		SessionID:     input.SessionID,
		Entitlements:  append([]Entitlement(nil), input.Entitlements...),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    sessionAssertionIssuer,
			Subject:   input.Subject,
			Audience:  jwt.ClaimStrings{input.Audience},
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			NotBefore: jwt.NewNumericDate(now),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        assertionID.String(),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["typ"] = sessionAssertionType
	token.Header["kid"] = signer.keyID
	encoded, err := token.SignedString(signer.privateKey)
	if err != nil {
		return SessionAssertion{}, errors.New("sign Commerce session assertion")
	}
	return SessionAssertion{Assertion: encoded, ExpiresAt: expiresAt}, nil
}

func (signer *sessionAssertionSigner) JWKSet() SessionJWKSet {
	if signer == nil || signer.privateKey == nil || len(signer.publicKeyBytes) != 65 ||
		signer.publicKeyBytes[0] != 4 {
		return SessionJWKSet{Keys: []SessionJWK{}}
	}
	coordinateBytes := (len(signer.publicKeyBytes) - 1) / 2
	return SessionJWKSet{Keys: []SessionJWK{{
		KeyType:   "EC",
		Use:       sessionJWKKeyUse,
		Curve:     "P-256",
		KeyID:     signer.keyID,
		Algorithm: jwt.SigningMethodES256.Alg(),
		X: base64.RawURLEncoding.EncodeToString(
			signer.publicKeyBytes[1 : 1+coordinateBytes],
		),
		Y: base64.RawURLEncoding.EncodeToString(
			signer.publicKeyBytes[1+coordinateBytes:],
		),
	}}}
}

func deriveCommerceSigningKey(masterKey []byte, purpose string) (*ecdsa.PrivateKey, error) {
	mac := hmac.New(sha256.New, masterKey)
	_, _ = mac.Write([]byte(purpose))
	seed := mac.Sum(nil)
	curve := elliptic.P256()
	maximumScalar := new(big.Int).Sub(curve.Params().N, big.NewInt(1))
	scalar := new(big.Int).SetBytes(seed)
	scalar.Mod(scalar, maximumScalar)
	scalar.Add(scalar, big.NewInt(1))
	coordinateBytes := (curve.Params().BitSize + 7) / 8
	return ecdsa.ParseRawPrivateKey(
		curve,
		scalar.FillBytes(make([]byte, coordinateBytes)),
	)
}
