package commerce

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	siteRequestAudience   = "boardly-site-commerce"
	siteRequestTokenType  = "boardly-site-request+jwt"
	siteRequestMaximumTTL = time.Minute
	siteRequestClockSkew  = 5 * time.Second
	maximumSiteTokenBytes = 16 << 10
)

type siteRequestAction string

const (
	siteRequestActionCheckout     siteRequestAction = "checkout"
	siteRequestActionPortal       siteRequestAction = "portal"
	siteRequestActionEntitlements siteRequestAction = "entitlements"
)

var base64URLSHA256Pattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// SitePublicKey is the minimal public-key shape accepted from a daemon. The
// server derives every JOSE metadata field and the RFC 7638 thumbprint.
type SitePublicKey struct {
	KeyType string `json:"kty"`
	Curve   string `json:"crv"`
	X       string `json:"x"`
	Y       string `json:"y"`
}

// SitePublicJWK is the exact public signing-key contract served by a Site.
type SitePublicJWK struct {
	KeyType   string `json:"kty"`
	Use       string `json:"use"`
	Curve     string `json:"crv"`
	KeyID     string `json:"kid"`
	Algorithm string `json:"alg"`
	X         string `json:"x"`
	Y         string `json:"y"`
}

// NewSitePublicJWK validates a daemon-provided P-256 key and derives its JOSE
// metadata and RFC 7638 thumbprint.
func NewSitePublicJWK(input SitePublicKey) (SitePublicJWK, error) {
	jwk := SitePublicJWK{
		KeyType:   input.KeyType,
		Use:       "sig",
		Curve:     input.Curve,
		Algorithm: jwt.SigningMethodES256.Alg(),
		X:         input.X,
		Y:         input.Y,
	}
	fingerprint, err := jwk.Fingerprint()
	if err != nil {
		return SitePublicJWK{}, err
	}
	jwk.KeyID = fingerprint
	return jwk, nil
}

// Fingerprint returns the RFC 7638 JWK thumbprint for an EC public key.
func (jwk SitePublicJWK) Fingerprint() (string, error) {
	if jwk.KeyType != "EC" || jwk.Curve != "P-256" {
		return "", ErrSiteAuthority
	}
	if _, _, err := jwk.publicKeyCoordinates(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(struct {
		Curve   string `json:"crv"`
		KeyType string `json:"kty"`
		X       string `json:"x"`
		Y       string `json:"y"`
	}{Curve: jwk.Curve, KeyType: jwk.KeyType, X: jwk.X, Y: jwk.Y})
	if err != nil {
		return "", ErrSiteAuthority
	}
	digest := sha256.Sum256(canonical)
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

// PublicKey validates all JOSE metadata and returns the P-256 verification key.
func (jwk SitePublicJWK) PublicKey() (*ecdsa.PublicKey, error) {
	if jwk.Use != "sig" || jwk.Algorithm != jwt.SigningMethodES256.Alg() ||
		!base64URLSHA256Pattern.MatchString(jwk.KeyID) {
		return nil, ErrSiteAuthority
	}
	fingerprint, err := jwk.Fingerprint()
	if err != nil || fingerprint != jwk.KeyID {
		return nil, ErrSiteAuthority
	}
	x, y, err := jwk.publicKeyCoordinates()
	if err != nil {
		return nil, err
	}
	return &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, nil
}

func (jwk SitePublicJWK) publicKeyCoordinates() (*big.Int, *big.Int, error) {
	return decodeP256Coordinates(jwk.X, jwk.Y)
}

func decodeP256Coordinates(xValue, yValue string) (*big.Int, *big.Int, error) {
	xBytes, xErr := base64.RawURLEncoding.DecodeString(xValue)
	yBytes, yErr := base64.RawURLEncoding.DecodeString(yValue)
	if xErr != nil || yErr != nil || len(xBytes) != 32 || len(yBytes) != 32 ||
		base64.RawURLEncoding.EncodeToString(xBytes) != xValue ||
		base64.RawURLEncoding.EncodeToString(yBytes) != yValue {
		return nil, nil, ErrSiteAuthority
	}
	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)
	if x.Sign() == 0 || y.Sign() == 0 || !elliptic.P256().IsOnCurve(x, y) {
		return nil, nil, ErrSiteAuthority
	}
	return x, y, nil
}

func sitePublicJWKFromKey(publicKey *ecdsa.PublicKey) SitePublicJWK {
	coordinateBytes := (publicKey.Curve.Params().BitSize + 7) / 8
	jwk, _ := NewSitePublicJWK(SitePublicKey{
		KeyType: "EC",
		Curve:   "P-256",
		X: base64.RawURLEncoding.EncodeToString(
			publicKey.X.FillBytes(make([]byte, coordinateBytes)),
		),
		Y: base64.RawURLEncoding.EncodeToString(
			publicKey.Y.FillBytes(make([]byte, coordinateBytes)),
		),
	})
	return jwk
}

type SiteSignedRequest struct {
	Token      string
	Method     string
	Path       string
	BodySHA256 [sha256.Size]byte
}

type siteRequestClaims struct {
	SchemaVersion         int               `json:"schemaVersion"`
	Environment           Environment       `json:"environment"`
	Origin                string            `json:"origin"`
	SessionID             string            `json:"sessionId"`
	AuthenticatedAt       string            `json:"authenticatedAt"`
	AuthenticationMethods []string          `json:"authenticationMethods"`
	Action                siteRequestAction `json:"action"`
	Method                string            `json:"method"`
	Path                  string            `json:"path"`
	OperationID           string            `json:"operationId"`
	BodySHA256            string            `json:"bodySha256"`
	Nonce                 string            `json:"nonce,omitempty"`
	jwt.RegisteredClaims
}

type siteRequestHeader struct {
	Algorithm string `json:"alg"`
	Type      string `json:"typ"`
	KeyID     string `json:"kid"`
}

type siteRequestExpectation struct {
	SiteID       uuid.UUID
	Environment  Environment
	Origin       string
	Action       siteRequestAction
	Method       string
	Path         string
	OperationID  uuid.UUID
	BodySHA256   [sha256.Size]byte
	RequireNonce bool
}

type siteRequestIdentity struct {
	Subject               string
	SessionID             string
	AuthenticatedAt       time.Time
	AuthenticationMethods []string
	OperationID           uuid.UUID
	Nonce                 string
}

func inspectSiteRequestToken(raw string) (uuid.UUID, string, error) {
	header, claims, err := decodeStrictSiteRequestToken(raw)
	if err != nil || header.Algorithm != jwt.SigningMethodES256.Alg() ||
		header.Type != siteRequestTokenType || !base64URLSHA256Pattern.MatchString(header.KeyID) {
		return uuid.Nil, "", ErrUnauthenticated
	}
	siteID, err := canonicalUUID(claims.Issuer)
	if err != nil {
		return uuid.Nil, "", ErrUnauthenticated
	}
	return siteID, header.KeyID, nil
}

func verifySiteRequestToken(
	raw string,
	jwk SitePublicJWK,
	expectation siteRequestExpectation,
	now time.Time,
) (siteRequestIdentity, error) {
	header, strictClaims, err := decodeStrictSiteRequestToken(raw)
	if err != nil || header.Algorithm != jwt.SigningMethodES256.Alg() ||
		header.Type != siteRequestTokenType || header.KeyID != jwk.KeyID {
		return siteRequestIdentity{}, ErrUnauthenticated
	}
	publicKey, err := jwk.PublicKey()
	if err != nil {
		return siteRequestIdentity{}, ErrUnauthenticated
	}
	parsed, err := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodES256.Alg()}),
		jwt.WithIssuer(expectation.SiteID.String()),
		jwt.WithAudience(siteRequestAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(siteRequestClockSkew),
		jwt.WithTimeFunc(func() time.Time { return now }),
	).ParseWithClaims(raw, &strictClaims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodES256 {
			return nil, ErrUnauthenticated
		}
		return publicKey, nil
	})
	if err != nil || parsed == nil || !parsed.Valid {
		return siteRequestIdentity{}, ErrUnauthenticated
	}
	return validateSiteRequestClaims(strictClaims, expectation, now)
}

func validateSiteRequestClaims(
	claims siteRequestClaims,
	expectation siteRequestExpectation,
	now time.Time,
) (siteRequestIdentity, error) {
	issuedAt, notBefore, expiresAt := claims.IssuedAt, claims.NotBefore, claims.ExpiresAt
	if claims.SchemaVersion != 1 || issuedAt == nil || notBefore == nil || expiresAt == nil ||
		!issuedAt.Time.Equal(notBefore.Time) || !expiresAt.Time.After(issuedAt.Time) ||
		expiresAt.Time.Sub(issuedAt.Time) > siteRequestMaximumTTL ||
		expiresAt.Time.Sub(now) > siteRequestMaximumTTL ||
		!expiresAt.Time.After(now) ||
		issuedAt.Time.After(now.Add(siteRequestClockSkew)) ||
		len(claims.Audience) != 1 || claims.Audience[0] != siteRequestAudience ||
		claims.Issuer != expectation.SiteID.String() ||
		!validSiteIdentityValue(claims.Subject, 255) ||
		!validSiteIdentityValue(claims.SessionID, 255) ||
		claims.Environment != expectation.Environment || claims.Origin != expectation.Origin ||
		claims.Action != expectation.Action || claims.Method != expectation.Method ||
		claims.Path != expectation.Path || claims.BodySHA256 != hex.EncodeToString(expectation.BodySHA256[:]) {
		return siteRequestIdentity{}, ErrUnauthenticated
	}
	operationID, err := canonicalUUID(claims.OperationID)
	if err != nil {
		return siteRequestIdentity{}, ErrUnauthenticated
	}
	_, err = canonicalUUID(claims.ID)
	if err != nil || expectation.OperationID != uuid.Nil && operationID != expectation.OperationID {
		return siteRequestIdentity{}, ErrUnauthenticated
	}
	authenticatedAt, err := time.Parse(time.RFC3339, claims.AuthenticatedAt)
	if err != nil || authenticatedAt.After(now.Add(siteRequestClockSkew)) ||
		!validAuthenticationMethods(claims.AuthenticationMethods) {
		return siteRequestIdentity{}, ErrUnauthenticated
	}
	if expectation.RequireNonce {
		if !base64URLSHA256Pattern.MatchString(claims.Nonce) {
			return siteRequestIdentity{}, ErrUnauthenticated
		}
	} else if claims.Nonce != "" {
		return siteRequestIdentity{}, ErrUnauthenticated
	}
	return siteRequestIdentity{
		Subject: claims.Subject, SessionID: claims.SessionID,
		AuthenticatedAt:       authenticatedAt.UTC(),
		AuthenticationMethods: append([]string(nil), claims.AuthenticationMethods...),
		OperationID:           operationID, Nonce: claims.Nonce,
	}, nil
}

func decodeStrictSiteRequestToken(raw string) (siteRequestHeader, siteRequestClaims, error) {
	if len(raw) == 0 || len(raw) > maximumSiteTokenBytes || strings.TrimSpace(raw) != raw ||
		strings.ContainsAny(raw, " \t\r\n,") {
		return siteRequestHeader{}, siteRequestClaims{}, ErrUnauthenticated
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 || parts[2] == "" {
		return siteRequestHeader{}, siteRequestClaims{}, ErrUnauthenticated
	}
	headerBytes, headerErr := base64.RawURLEncoding.DecodeString(parts[0])
	claimsBytes, claimsErr := base64.RawURLEncoding.DecodeString(parts[1])
	if headerErr != nil || claimsErr != nil {
		return siteRequestHeader{}, siteRequestClaims{}, ErrUnauthenticated
	}
	var header siteRequestHeader
	if err := decodeClosedJSON(headerBytes, &header); err != nil {
		return siteRequestHeader{}, siteRequestClaims{}, ErrUnauthenticated
	}
	var claims siteRequestClaims
	if err := decodeClosedJSON(claimsBytes, &claims); err != nil {
		return siteRequestHeader{}, siteRequestClaims{}, ErrUnauthenticated
	}
	return header, claims, nil
}

func decodeClosedJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func canonicalUUID(value string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed == uuid.Nil || parsed.String() != value {
		return uuid.Nil, ErrUnauthenticated
	}
	return parsed, nil
}

func validSiteIdentityValue(value string, maximum int) bool {
	return len(value) >= 1 && len(value) <= maximum && strings.TrimSpace(value) == value &&
		!containsControl(value)
}

func validAuthenticationMethods(methods []string) bool {
	if len(methods) == 0 || len(methods) > 16 {
		return false
	}
	seen := make(map[string]struct{}, len(methods))
	for _, method := range methods {
		if !validSiteIdentityValue(method, 64) {
			return false
		}
		if _, exists := seen[method]; exists {
			return false
		}
		seen[method] = struct{}{}
	}
	return true
}

func marshalSitePublicJWK(jwk SitePublicJWK) ([]byte, error) {
	if _, err := jwk.PublicKey(); err != nil {
		return nil, ErrSiteAuthority
	}
	raw, err := json.Marshal(jwk)
	if err != nil {
		return nil, fmt.Errorf("encode Commerce Site public JWK: %w", err)
	}
	return raw, nil
}

func unmarshalSitePublicJWK(raw []byte) (SitePublicJWK, error) {
	var jwk SitePublicJWK
	if err := decodeClosedJSON(raw, &jwk); err != nil {
		return SitePublicJWK{}, ErrSiteAuthority
	}
	if _, err := jwk.PublicKey(); err != nil {
		return SitePublicJWK{}, ErrSiteAuthority
	}
	return jwk, nil
}
