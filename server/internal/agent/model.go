package agent

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// FromRow projects a trusted sqlc Agent row. Database constraints and sqlc own
// stored-row invariants; this mapper only converts nullable and timestamp
// representations.
func FromRow(row generated.Agent) Response {
	return Response{
		Voice:            voice.FromSettings(row.Settings),
		Capabilities:     capabilitySettingsFromRow(row),
		ID:               row.ID,
		CompanyID:        row.CompanyID,
		Name:             row.Name,
		Title:            row.Title,
		Instructions:     row.Instructions,
		ReportsToAgentID: optionalUUID(row.ReportsToAgentID),
		AvatarKey:        optionalString(row.AvatarKey),
		AccentColor:      optionalString(row.AccentColor),
		Provider:         row.Provider,
		Model:            optionalString(row.Model),
		ThinkingLevel:    optionalString(row.ThinkingLevel),
		// ActiveRunCount is not on the row; the projection fills it from
		// CountActiveTaskLaneRunsByAgentForBinder.
		MaxConcurrentRuns: row.MaxConcurrentRuns,
		ArchivedAt:        util.TimestampToTime(row.ArchivedAt),
		CreatedAt:         row.CreatedAt.Time.UTC(),
		UpdatedAt:         row.UpdatedAt.Time.UTC(),
		Revision:          row.Version,
	}
}

func optionalString(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	cloned := value.String
	return &cloned
}

func optionalUUID(value uuid.NullUUID) *uuid.UUID {
	return util.NullUUIDPtr(value)
}

func sameOptionalText(row pgtype.Text, value *string) bool {
	if value == nil {
		return !row.Valid
	}
	return row.Valid && row.String == *value
}

func updateTargetMatches(row generated.Agent, input UpdateInput) bool {
	if input.Voice != nil && voice.FromSettings(row.Settings) != *input.Voice {
		return false
	}
	if input.Capabilities != nil && !sameCapabilitySettings(capabilitySettingsFromRow(row), *input.Capabilities) ||
		input.Name != nil && row.Name != *input.Name ||
		input.Title != nil && row.Title != *input.Title ||
		input.Instructions != nil && row.Instructions != *input.Instructions ||
		input.MaxConcurrentRuns != nil && row.MaxConcurrentRuns != *input.MaxConcurrentRuns ||
		input.Model.Present && !sameOptionalText(row.Model, input.Model.Value) ||
		input.ThinkingLevel.Present && !sameOptionalText(row.ThinkingLevel, input.ThinkingLevel.Value) ||
		input.AvatarKey.Present && !sameOptionalText(row.AvatarKey, input.AvatarKey.Value) ||
		input.AccentColor.Present && !sameOptionalText(row.AccentColor, input.AccentColor.Value) {
		return false
	}
	if input.ReportsToAgentID == nil {
		return true
	}
	return row.ReportsToAgentID.Valid && row.ReportsToAgentID.UUID == *input.ReportsToAgentID
}

func validateActiveAgentParent(graph []generated.Agent, agentID, parentID uuid.UUID) error {
	byID := make(map[uuid.UUID]generated.Agent, len(graph))
	for _, row := range graph {
		byID[row.ID] = row
	}
	parent, ok := byID[parentID]
	if !ok || parent.ArchivedAt.Valid {
		return ErrNotFound
	}
	seen := map[uuid.UUID]struct{}{agentID: {}}
	currentID := parentID
	for {
		if _, exists := seen[currentID]; exists {
			return ErrCycle
		}
		seen[currentID] = struct{}{}
		current, exists := byID[currentID]
		if !exists {
			return ErrNotFound
		}
		if !current.ReportsToAgentID.Valid {
			return nil
		}
		currentID = current.ReportsToAgentID.UUID
	}
}

func activeDirectReport(graph []generated.Agent, agentID uuid.UUID) bool {
	for _, row := range graph {
		if !row.ArchivedAt.Valid && row.ReportsToAgentID.Valid &&
			row.ReportsToAgentID.UUID == agentID {
			return true
		}
	}
	return false
}

func mapCreateError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrConflict)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) && postgresError.Code == "23505" {
		return fmt.Errorf("%s: %w", operation, ErrConflict)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func mapMutationError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", operation, ErrRevisionConflict)
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23505", "23514":
			return fmt.Errorf("%s: %w", operation, ErrRevisionConflict)
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
