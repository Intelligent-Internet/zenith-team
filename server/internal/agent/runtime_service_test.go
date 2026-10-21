package agent

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func TestRuntimeFromStoredRowProjectsRegistrationMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		metadata            []byte
		wantProviderVersion string
		wantCLIVersion      string
		wantLaunchedBy      string
	}{
		{
			name: "desktop registration",
			metadata: []byte(
				`{"version":"1.2.3","cliVersion":"0.9.0","launchedBy":"desktop","deviceName":"MacBook Pro"}`,
			),
			wantProviderVersion: "1.2.3",
			wantCLIVersion:      "0.9.0",
			wantLaunchedBy:      RuntimeLaunchedByDesktop,
		},
		{
			// The daemon sends an empty launchedBy for a terminal start; the
			// projection names it.
			name: "terminal registration",
			metadata: []byte(
				`{"version":"1.2.3","cliVersion":"0.9.0","launchedBy":"","deviceName":"x"}`,
			),
			wantProviderVersion: "1.2.3",
			wantCLIVersion:      "0.9.0",
			wantLaunchedBy:      RuntimeLaunchedByCLI,
		},
		{
			name:           "default document",
			metadata:       []byte(`{}`),
			wantLaunchedBy: RuntimeLaunchedByCLI,
		},
		{
			name:           "legacy keys",
			metadata:       []byte(`{"arch":"arm64","os":"darwin"}`),
			wantLaunchedBy: RuntimeLaunchedByCLI,
		},
		{
			name:           "nil metadata",
			metadata:       nil,
			wantLaunchedBy: RuntimeLaunchedByCLI,
		},
		{
			name:           "unparseable metadata",
			metadata:       []byte(`not json`),
			wantLaunchedBy: RuntimeLaunchedByCLI,
		},
		{
			name:           "unknown launcher",
			metadata:       []byte(`{"launchedBy":"kiosk"}`),
			wantLaunchedBy: RuntimeLaunchedByCLI,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := runtimeFromStoredRow(generated.Runtime{Metadata: test.metadata})
			if got.ProviderVersion != test.wantProviderVersion ||
				got.CLIVersion != test.wantCLIVersion ||
				got.LaunchedBy != test.wantLaunchedBy {
				t.Fatalf(
					"runtimeFromStoredRow() versions = %q/%q, launchedBy = %q; want %q/%q, %q",
					got.ProviderVersion, got.CLIVersion, got.LaunchedBy,
					test.wantProviderVersion, test.wantCLIVersion, test.wantLaunchedBy,
				)
			}
		})
	}
}

func TestRuntimeFromStoredRowKeepsTheBareRowFields(t *testing.T) {
	t.Parallel()
	runtimeID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	createdAt := time.Date(2026, 8, 19, 9, 10, 11, 0, time.FixedZone("ICT", 7*3600))

	got := runtimeFromStoredRow(generated.Runtime{
		ID:         runtimeID,
		CompanyID:  companyID,
		DaemonID:   "daemon-a",
		Name:       "Codex (MacBook Pro)",
		Provider:   "codex",
		Status:     "online",
		DeviceInfo: "MacBook Pro · 1.2.3",
		Metadata:   []byte(`{"version":"1.2.3"}`),
		CreatedAt:  pgtype.Timestamptz{Time: createdAt, Valid: true},
		UpdatedAt:  pgtype.Timestamptz{Time: createdAt, Valid: true},
	})
	if got.ID != runtimeID || got.CompanyID != companyID || got.DaemonID != "daemon-a" ||
		got.Name != "Codex (MacBook Pro)" || got.Provider != "codex" || got.Status != "online" ||
		got.DeviceInfo != "MacBook Pro · 1.2.3" || got.Owner != nil {
		t.Fatalf("runtimeFromStoredRow() = %+v", got)
	}
	if got.LastSeenAt != nil {
		t.Fatalf("LastSeenAt = %v, want nil for an invalid timestamp", got.LastSeenAt)
	}
	if got.CreatedAt.Location() != time.UTC || got.UpdatedAt.Location() != time.UTC ||
		!got.CreatedAt.Equal(createdAt) {
		t.Fatalf("timestamps = %v / %v, want %v in UTC", got.CreatedAt, got.UpdatedAt, createdAt)
	}
}

func TestRuntimeWithOwnerJoinsTheOwningUser(t *testing.T) {
	t.Parallel()
	ownerID := uuid.Must(uuid.NewV7())

	withOwner := runtimeWithOwner(
		generated.Runtime{Name: "Claude (host)"},
		uuid.NullUUID{UUID: ownerID, Valid: true},
		pgtype.Text{String: "Alex Rivera", Valid: true},
		pgtype.Text{String: "https://cdn.example/avatar.png", Valid: true},
	)
	if withOwner.Owner == nil || withOwner.Owner.ID != ownerID ||
		withOwner.Owner.Kind != "user" || withOwner.Owner.DisplayName != "Alex Rivera" ||
		withOwner.Owner.AvatarURL == nil || *withOwner.Owner.AvatarURL != "https://cdn.example/avatar.png" {
		t.Fatalf("runtimeWithOwner().Owner = %+v", withOwner.Owner)
	}
	if withOwner.Name != "Claude (host)" || withOwner.LaunchedBy != RuntimeLaunchedByCLI {
		t.Fatalf("runtimeWithOwner() = %+v", withOwner)
	}

	orphan := runtimeWithOwner(generated.Runtime{}, uuid.NullUUID{}, pgtype.Text{}, pgtype.Text{})
	if orphan.Owner != nil {
		t.Fatalf("runtimeWithOwner(no owner).Owner = %+v, want nil", orphan.Owner)
	}
}

// The row a viewer receives is always the whole row: the reads filter in
// SQL to the viewer's own machines (ADR 0014), so there is no per-viewer
// verdict to carry and nothing to hide — sign-in state, tools and versions
// all project as stored.
func TestRuntimeWithOwnerProjectsTheWholeRowForItsRegistrant(t *testing.T) {
	t.Parallel()
	ownerID := uuid.Must(uuid.NewV7())
	row := generated.Runtime{
		Name:                    "Codex (Office Mac)",
		DeviceInfo:              "Office Mac · 0.9.0",
		OwnerUserID:             uuid.NullUUID{UUID: ownerID, Valid: true},
		ProviderSignInState:     pgtype.Text{String: "signed_in", Valid: true},
		ProviderSignInDetail:    pgtype.Text{String: "Signed in (max).", Valid: true},
		ProviderSignInCheckedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
		Metadata:                []byte(`{"version":"1.2.3","cliVersion":"0.9.0","tools":[{"name":"gh"}]}`),
	}
	ownerRef := uuid.NullUUID{UUID: ownerID, Valid: true}
	name := pgtype.Text{String: "Sam", Valid: true}

	registrant := runtimeWithOwner(row, ownerRef, name, pgtype.Text{})
	if registrant.SignIn == nil || registrant.SignIn.State != "signed_in" ||
		registrant.ProviderVersion != "1.2.3" ||
		len(registrant.Tools) != 1 ||
		registrant.DeviceInfo != "Office Mac · 0.9.0" ||
		registrant.Owner == nil ||
		registrant.Owner.DisplayName != "Sam" {
		t.Fatalf("registrant projection = %+v", registrant)
	}
}

// The stored unusable-reason columns project as one nullable object; NULL
// columns are the ordinary "nothing wrong" row and serialize as null.
func TestRuntimeFromStoredRowProjectsUnusableReason(t *testing.T) {
	t.Parallel()
	unusable := runtimeFromStoredRow(generated.Runtime{
		Status:                "offline",
		UnusableReasonCode:    pgtype.Text{String: protocol.RuntimeUnusableCodeNotExecutable, Valid: true},
		UnusableReasonMessage: pgtype.Text{String: "Codex CLI is not executable", Valid: true},
	})
	want := protocol.RuntimeUnusableReason{
		Code:    protocol.RuntimeUnusableCodeNotExecutable,
		Message: "Codex CLI is not executable",
	}
	if unusable.UnusableReason == nil || *unusable.UnusableReason != want {
		t.Fatalf("runtimeFromStoredRow(unusable).UnusableReason = %+v, want %+v", unusable.UnusableReason, want)
	}
	healthy := runtimeFromStoredRow(generated.Runtime{Status: "online"})
	if healthy.UnusableReason != nil {
		t.Fatalf("runtimeFromStoredRow(healthy).UnusableReason = %+v, want nil", healthy.UnusableReason)
	}
}

// The custom name is served beside the daemon's name, never in its place:
// a renamed row keeps Name and gains CustomName, and a row nobody renamed
// carries nil rather than an empty string.
func TestRuntimeFromStoredRowProjectsCustomNameBesideTheDaemonName(t *testing.T) {
	t.Parallel()
	renamed := runtimeFromStoredRow(generated.Runtime{
		Name:       "Claude (host)",
		CustomName: pgtype.Text{String: "Office Mac", Valid: true},
	})
	if renamed.Name != "Claude (host)" || renamed.CustomName == nil || *renamed.CustomName != "Office Mac" {
		t.Fatalf("runtimeFromStoredRow(renamed) = %+v", renamed)
	}
	plain := runtimeFromStoredRow(generated.Runtime{Name: "Claude (host)"})
	if plain.Name != "Claude (host)" || plain.CustomName != nil {
		t.Fatalf("runtimeFromStoredRow(plain).CustomName = %v, want nil", plain.CustomName)
	}
}
