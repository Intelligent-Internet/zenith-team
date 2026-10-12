package auth

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestProjectUserIncludesAuthenticationCapabilities(t *testing.T) {
	t.Parallel()

	verifiedAt := time.Date(2026, time.August, 19, 4, 5, 6, 789_123_000, time.UTC)
	createdAt := verifiedAt.Add(-time.Hour)
	response := ProjectUser(User{
		PasswordHash:    "encoded-password",
		EmailVerifiedAt: &verifiedAt,
		CreatedAt:       createdAt,
	})
	if response.EmailVerifiedAt == nil || *response.EmailVerifiedAt != protocol.FormatWireTime(verifiedAt) {
		t.Fatalf(
			"ProjectUser().EmailVerifiedAt = %v, want %q",
			response.EmailVerifiedAt,
			protocol.FormatWireTime(verifiedAt),
		)
	}
	if !response.HasPassword {
		t.Fatal("ProjectUser().HasPassword = false, want true")
	}
	if response.LinkedProviders == nil || len(response.LinkedProviders) != 0 {
		t.Fatalf("ProjectUser().LinkedProviders = %#v, want an exact empty list", response.LinkedProviders)
	}

	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("json.Marshal(ProjectUser()) error = %v", err)
	}
	if !strings.Contains(string(encoded), `"linkedProviders":[]`) {
		t.Fatalf("json.Marshal(ProjectUser()) = %s, want linkedProviders:[]", encoded)
	}
}

func TestProjectUserPreservesLinkedProviders(t *testing.T) {
	t.Parallel()

	response := ProjectUser(User{LinkedProviders: []string{"google"}})
	if len(response.LinkedProviders) != 1 || response.LinkedProviders[0] != "google" {
		t.Fatalf("ProjectUser().LinkedProviders = %#v, want [google]", response.LinkedProviders)
	}
	if response.EmailVerifiedAt != nil {
		t.Fatalf("ProjectUser().EmailVerifiedAt = %v, want nil", response.EmailVerifiedAt)
	}
}

func TestUserFromDBPreservesVersionAndDeletionMarker(t *testing.T) {
	t.Parallel()

	deletedAt := time.Date(2026, 7, 24, 3, 4, 5, 0, time.UTC)
	user := userFromDB(generated.User{
		Version:   37,
		DeletedAt: pgtype.Timestamptz{Time: deletedAt, Valid: true},
	})
	if user.Version != 37 {
		t.Fatalf("userFromDB().Version = %d, want 37", user.Version)
	}
	if user.DeletedAt == nil || !user.DeletedAt.Equal(deletedAt) {
		t.Fatalf("userFromDB().DeletedAt = %v, want %v", user.DeletedAt, deletedAt)
	}
}
