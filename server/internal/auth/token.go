package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	accessTokenIssuer   = "zenith-team-api"
	accessTokenAudience = "zenith-team-clients"
	accessTokenUse      = "access"
	refreshTokenPrefix  = "brf_"
)

var ErrInvalidToken = errors.New("invalid token")

// errAccessTokenExpired is ErrInvalidToken for an authentic access token past
// its expiry, which Authenticate reports apart from a forged or garbled one.
var errAccessTokenExpired = fmt.Errorf("%w: access token expired", ErrInvalidToken)

type TokenManagerOptions struct {
	Secret     []byte
	AccessTTL  time.Duration
	RefreshTTL time.Duration
	Now        func() time.Time
	Random     io.Reader
}

type TokenManager struct {
	accessKey  []byte
	refreshKey []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
	random     io.Reader
}

type IssuedTokens struct {
	SessionID          uuid.UUID
	IssuedAt           time.Time
	AccessToken        string
	RefreshToken       string
	RefreshTokenHash   []byte
	RefreshTokenPrefix string
	AccessExpiresAt    time.Time
	RefreshExpiresAt   time.Time
}

type IssuedRefresh struct {
	Token     string
	Hash      []byte
	Prefix    string
	ExpiresAt time.Time
}

type AccessClaims struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	ExpiresAt time.Time
}

type accessJWTClaims struct {
	SessionID string `json:"sid"`
	TokenUse  string `json:"tokenUse"`
	jwt.RegisteredClaims
}

func NewTokenManager(options TokenManagerOptions) (*TokenManager, error) {
	if len(options.Secret) < 32 {
		return nil, errors.New("token secret must contain at least 32 bytes")
	}
	if options.AccessTTL <= 0 || options.RefreshTTL <= options.AccessTTL {
		return nil, errors.New("token lifetimes are invalid")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	return &TokenManager{
		accessKey:  deriveTokenKey(options.Secret, "access"),
		refreshKey: deriveTokenKey(options.Secret, "refresh"),
		accessTTL:  options.AccessTTL,
		refreshTTL: options.RefreshTTL,
		now:        options.Now,
		random:     options.Random,
	}, nil
}

func (m *TokenManager) Issue(userID, sessionID uuid.UUID) (IssuedTokens, error) {
	if m == nil || userID == uuid.Nil || sessionID == uuid.Nil {
		return IssuedTokens{}, errors.New("user and session IDs are required")
	}
	issuedAt := m.now().UTC()
	accessToken, accessExpiresAt, err := m.issueAccessAt(userID, sessionID, issuedAt)
	if err != nil {
		return IssuedTokens{}, err
	}
	refresh, err := m.IssueRefresh()
	if err != nil {
		return IssuedTokens{}, err
	}
	return IssuedTokens{
		SessionID:          sessionID,
		IssuedAt:           issuedAt,
		AccessToken:        accessToken,
		RefreshToken:       refresh.Token,
		RefreshTokenHash:   refresh.Hash,
		RefreshTokenPrefix: refresh.Prefix,
		AccessExpiresAt:    accessExpiresAt,
		RefreshExpiresAt:   refresh.ExpiresAt,
	}, nil
}

func (m *TokenManager) IssueAccess(userID, sessionID uuid.UUID) (string, time.Time, error) {
	if m == nil || userID == uuid.Nil || sessionID == uuid.Nil {
		return "", time.Time{}, errors.New("user and session IDs are required")
	}
	return m.issueAccessAt(userID, sessionID, m.now().UTC())
}

func (m *TokenManager) issueAccessAt(
	userID, sessionID uuid.UUID,
	issuedAt time.Time,
) (string, time.Time, error) {
	accessExpiresAt := issuedAt.Add(m.accessTTL)
	claims := accessJWTClaims{
		SessionID: sessionID.String(),
		TokenUse:  accessTokenUse,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    accessTokenIssuer,
			Subject:   userID.String(),
			Audience:  jwt.ClaimStrings{accessTokenAudience},
			ExpiresAt: jwt.NewNumericDate(accessExpiresAt),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			NotBefore: jwt.NewNumericDate(issuedAt),
		},
	}
	accessToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.accessKey)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign access token: %w", err)
	}
	return accessToken, accessExpiresAt, nil
}

func (m *TokenManager) IssueRefresh() (IssuedRefresh, error) {
	if m == nil {
		return IssuedRefresh{}, errors.New("token manager is not initialized")
	}
	randomBytes := make([]byte, 32)
	if _, err := io.ReadFull(m.random, randomBytes); err != nil {
		return IssuedRefresh{}, fmt.Errorf("generate refresh token: %w", err)
	}
	refreshToken := refreshTokenPrefix + base64.RawURLEncoding.EncodeToString(randomBytes)
	refreshHash := m.HashRefresh(refreshToken)
	prefixLength := min(len(refreshToken), 12)
	return IssuedRefresh{
		Token:     refreshToken,
		Hash:      refreshHash,
		Prefix:    refreshToken[:prefixLength],
		ExpiresAt: m.now().UTC().Add(m.refreshTTL),
	}, nil
}

// IssueRefreshForRotation creates a refresh credential that can be reproduced
// only with the parent credential's server-side hash and the same rotation
// id. That lets an exact retry recover a response lost after commit without
// making a different replay idempotent.
func (m *TokenManager) IssueRefreshForRotation(
	parentHash []byte,
	rotationID uuid.UUID,
) (IssuedRefresh, error) {
	if m == nil {
		return IssuedRefresh{}, errors.New("token manager is not initialized")
	}
	return m.refreshForRotation(
		parentHash,
		rotationID,
		m.now().UTC().Add(m.refreshTTL),
	)
}

// RestoreRefreshForRotation reconstructs a previously issued rotation while
// preserving the expiry stored with its child session.
func (m *TokenManager) RestoreRefreshForRotation(
	parentHash []byte,
	rotationID uuid.UUID,
	expiresAt time.Time,
) (IssuedRefresh, error) {
	if m == nil {
		return IssuedRefresh{}, errors.New("token manager is not initialized")
	}
	return m.refreshForRotation(parentHash, rotationID, expiresAt)
}

func (m *TokenManager) refreshForRotation(
	parentHash []byte,
	rotationID uuid.UUID,
	expiresAt time.Time,
) (IssuedRefresh, error) {
	if len(parentHash) != sha256.Size || rotationID == uuid.Nil ||
		rotationID.Variant() != uuid.RFC4122 {
		return IssuedRefresh{}, errors.New("refresh rotation inputs are invalid")
	}
	mac := hmac.New(sha256.New, m.refreshKey)
	_, _ = mac.Write([]byte("refresh-rotation-v1"))
	_, _ = mac.Write(parentHash)
	_, _ = mac.Write(rotationID[:])
	randomBytes := mac.Sum(nil)
	refreshToken := refreshTokenPrefix + base64.RawURLEncoding.EncodeToString(randomBytes)
	prefixLength := min(len(refreshToken), 12)
	return IssuedRefresh{
		Token:     refreshToken,
		Hash:      m.HashRefresh(refreshToken),
		Prefix:    refreshToken[:prefixLength],
		ExpiresAt: expiresAt,
	}, nil
}

func (m *TokenManager) ValidateAccess(raw string) (AccessClaims, error) {
	if m == nil || strings.TrimSpace(raw) == "" {
		return AccessClaims{}, ErrInvalidToken
	}
	claims := new(accessJWTClaims)
	token, err := jwt.ParseWithClaims(raw, claims,
		func(token *jwt.Token) (any, error) {
			if token.Method != jwt.SigningMethodHS256 {
				return nil, ErrInvalidToken
			}
			return m.accessKey, nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(accessTokenIssuer),
		jwt.WithAudience(accessTokenAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(m.now),
	)
	// The parser checks the signature before the claims, so an expiry error
	// means the token itself was ours.
	if errors.Is(err, jwt.ErrTokenExpired) {
		return AccessClaims{}, errAccessTokenExpired
	}
	if err != nil || !token.Valid || claims.TokenUse != accessTokenUse {
		return AccessClaims{}, ErrInvalidToken
	}
	userID, err := uuid.Parse(claims.Subject)
	if err != nil || userID == uuid.Nil {
		return AccessClaims{}, ErrInvalidToken
	}
	sessionID, err := uuid.Parse(claims.SessionID)
	if err != nil || sessionID == uuid.Nil {
		return AccessClaims{}, ErrInvalidToken
	}
	if claims.ExpiresAt == nil || claims.ExpiresAt.IsZero() {
		return AccessClaims{}, ErrInvalidToken
	}
	return AccessClaims{
		UserID: userID, SessionID: sessionID, ExpiresAt: claims.ExpiresAt.UTC(),
	}, nil
}

func (m *TokenManager) HashRefresh(raw string) []byte {
	mac := hmac.New(sha256.New, m.refreshKey)
	_, _ = mac.Write([]byte(raw))
	return mac.Sum(nil)
}

func (m *TokenManager) ValidateRefresh(raw string, expectedHash []byte) error {
	if m == nil || !validRefreshToken(raw) || len(expectedHash) != sha256.Size {
		return ErrInvalidToken
	}
	actual := m.HashRefresh(raw)
	if subtle.ConstantTimeCompare(actual, expectedHash) != 1 {
		return ErrInvalidToken
	}
	return nil
}

func (m *TokenManager) LookupRefresh(raw string) ([]byte, error) {
	if m == nil || !validRefreshToken(raw) {
		return nil, ErrInvalidToken
	}
	return m.HashRefresh(raw), nil
}

func validRefreshToken(raw string) bool {
	if !strings.HasPrefix(raw, refreshTokenPrefix) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(raw, refreshTokenPrefix))
	return err == nil && len(decoded) == 32
}

func deriveTokenKey(secret []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("zenith-team-auth-v1:" + purpose))
	return mac.Sum(nil)
}
