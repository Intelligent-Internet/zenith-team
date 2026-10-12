package auth

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTokenManagerIssuesAndValidatesSessionBoundTokens(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 11, 8, 30, 0, 0, time.UTC)
	manager, err := NewTokenManager(TokenManagerOptions{
		Secret:     []byte("01234567890123456789012345678901"),
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 30 * 24 * time.Hour,
		Now:        func() time.Time { return now },
		Random:     bytes.NewReader(make([]byte, 64)),
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	sessionID := uuid.MustParse("0197f590-70bd-78c3-825f-5f6e4df341af")

	issued, err := manager.Issue(userID, sessionID)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if issued.AccessToken == "" || issued.RefreshToken == "" {
		t.Fatal("Issue() returned an empty token")
	}
	if issued.SessionID != sessionID || !issued.IssuedAt.Equal(now) {
		t.Fatalf("session metadata = %s at %s", issued.SessionID, issued.IssuedAt)
	}
	if issued.RefreshTokenPrefix == "" || len(issued.RefreshTokenHash) != 32 {
		t.Fatalf(
			"refresh lookup material = prefix %q, hash bytes %d",
			issued.RefreshTokenPrefix,
			len(issued.RefreshTokenHash),
		)
	}
	if !issued.AccessExpiresAt.Equal(now.Add(15 * time.Minute)) {
		t.Errorf("AccessExpiresAt = %v", issued.AccessExpiresAt)
	}
	if !issued.RefreshExpiresAt.Equal(now.Add(30 * 24 * time.Hour)) {
		t.Errorf("RefreshExpiresAt = %v", issued.RefreshExpiresAt)
	}

	claims, err := manager.ValidateAccess(issued.AccessToken)
	if err != nil {
		t.Fatalf("ValidateAccess() error = %v", err)
	}
	if claims.UserID != userID || claims.SessionID != sessionID {
		t.Errorf("claims = %#v", claims)
	}
	if _, err := manager.ValidateAccess(issued.AccessToken + "tampered"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("ValidateAccess(tampered) error = %v, want ErrInvalidToken", err)
	}
	if err := manager.ValidateRefresh(issued.RefreshToken, issued.RefreshTokenHash); err != nil {
		t.Fatalf("ValidateRefresh() error = %v", err)
	}
	if err := manager.ValidateRefresh(
		issued.RefreshToken+"tampered",
		issued.RefreshTokenHash,
	); !errors.Is(
		err,
		ErrInvalidToken,
	) {
		t.Fatalf("ValidateRefresh(tampered) error = %v, want ErrInvalidToken", err)
	}
	now = now.Add(16 * time.Minute)
	if _, err := manager.ValidateAccess(issued.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("ValidateAccess(expired) error = %v, want ErrInvalidToken", err)
	}
}

func TestTokenManagerRestoresTheSameRefreshForARotation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 19, 10, 0, 0, 0, time.UTC)
	manager, err := NewTokenManager(TokenManagerOptions{
		Secret:     []byte("01234567890123456789012345678901"),
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 7 * 24 * time.Hour,
		Now:        func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewTokenManager() error = %v", err)
	}
	parentHash := bytes.Repeat([]byte{7}, 32)
	rotationID := uuid.MustParse("0199a143-7ac0-7d4f-b93e-4be838f5772e")

	issued, err := manager.IssueRefreshForRotation(parentHash, rotationID)
	if err != nil {
		t.Fatalf("IssueRefreshForRotation() error = %v", err)
	}
	restored, err := manager.RestoreRefreshForRotation(
		parentHash,
		rotationID,
		issued.ExpiresAt,
	)
	if err != nil {
		t.Fatalf("RestoreRefreshForRotation() error = %v", err)
	}
	if restored.Token != issued.Token || !bytes.Equal(restored.Hash, issued.Hash) ||
		!restored.ExpiresAt.Equal(issued.ExpiresAt) {
		t.Fatalf("restored refresh = %#v, want %#v", restored, issued)
	}

	differentRotation, err := manager.RestoreRefreshForRotation(
		parentHash,
		uuid.MustParse("0199a143-98ab-7d60-82db-89b4386379f5"),
		issued.ExpiresAt,
	)
	if err != nil {
		t.Fatalf("RestoreRefreshForRotation(different id) error = %v", err)
	}
	if differentRotation.Token == issued.Token {
		t.Fatal("different rotation id restored the same refresh token")
	}
}
