package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

type ToolObservationReport struct {
	RuntimeID uuid.UUID
	DaemonID  string
	Items     []protocol.ToolObservation
}

func (s *DefaultRuntimeService) ReportTools(
	ctx context.Context,
	principal auth.UserPrincipal,
	report ToolObservationReport,
) error {
	if err := normalizeToolObservations(report.Items); err != nil {
		return err
	}
	var toolsChanged bool
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := lockMemberRole(ctx, q, principal.CompanyID, principal.UserID, "report tools"); err != nil {
			if errors.Is(err, ErrNotFound) {
				return ErrRuntimeNotFound
			}
			return err
		}
		row, err := q.GetRuntimeForUpdate(
			ctx,
			generated.GetRuntimeForUpdateParams{CompanyID: principal.CompanyID, ID: report.RuntimeID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRuntimeNotFound
		}
		if err != nil {
			return fmt.Errorf("lock runtime for tool observation: %w", err)
		}
		if row.DaemonID != report.DaemonID || !row.OwnerUserID.Valid ||
			row.OwnerUserID.UUID != principal.UserID {
			return ErrRuntimeNotFound
		}
		// Auth observations may name only the reporting member's current bindings.
		bindings, err := q.ListAgentRuntimeBindingsForRuntimeForUpdate(
			ctx,
			generated.ListAgentRuntimeBindingsForRuntimeForUpdateParams{
				CompanyID: principal.CompanyID,
				RuntimeID: report.RuntimeID,
			},
		)
		if err != nil {
			return fmt.Errorf("lock tool Agent bindings: %w", err)
		}
		allowed := make(map[string]bool, len(bindings))
		for _, binding := range bindings {
			if binding.UserID == principal.UserID {
				allowed[binding.AgentID.String()] = true
			}
		}
		for _, item := range report.Items {
			if item.AgentID != "" && !allowed[item.AgentID] {
				return ErrRuntimeInvalidInput
			}
		}
		var metadata runtimeMetadata
		_ = json.Unmarshal(row.Metadata, &metadata)
		toolsChanged = !sameToolStates(metadata.Tools, report.Items)
		encoded, err := json.Marshal(report.Items)
		if err != nil {
			return fmt.Errorf("encode tool observation: %w", err)
		}
		return q.UpdateRuntimeTools(
			ctx,
			generated.UpdateRuntimeToolsParams{
				CompanyID: principal.CompanyID,
				RuntimeID: report.RuntimeID,
				Tools:     encoded,
			},
		)
	})
	if err != nil {
		return err
	}
	// Only a real change is worth a frame, and it goes to the member whose
	// machine reported it (ADR 0014).
	if toolsChanged {
		s.publishRoster(
			ctx,
			protocol.DaemonRosterActionToolsChanged,
			principal.CompanyID,
			principal.UserID,
			report.DaemonID,
			[]string{report.RuntimeID.String()},
		)
	}
	return nil
}

// Probes renew observedAt even when nothing changed. Persist that freshness,
// but only notify clients when the tool state they display actually moves.
func sameToolStates(previous, current []protocol.ToolObservation) bool {
	if len(previous) != len(current) {
		return false
	}
	for _, before := range previous {
		index := slices.IndexFunc(current, func(item protocol.ToolObservation) bool {
			return item.Key == before.Key && item.AgentID == before.AgentID
		})
		if index < 0 {
			return false
		}
		after := current[index]
		before.ObservedAt, after.ObservedAt = "", ""
		if before != after {
			return false
		}
	}
	return true
}

func normalizeToolObservations(items []protocol.ToolObservation) error {
	if len(items) > 4096 {
		return ErrRuntimeInvalidInput
	}
	seen := make(map[string]bool)
	for _, item := range items {
		if !slices.Contains([]string{"zenith-studio", "hyperframes"}, item.Key) ||
			len(item.Version) > 128 ||
			!regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})`+
				`(-((0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*)(\.(0|[1-9][0-9]*|[0-9]*[a-zA-Z-][0-9a-zA-Z-]*))*))?`+
				`(\+[0-9a-zA-Z-]+(\.[0-9a-zA-Z-]+)*)?$`).
				MatchString(item.Version) ||
			seen[item.Key+"/"+item.AgentID] {
			return ErrRuntimeInvalidInput
		}
		seen[item.Key+"/"+item.AgentID] = true
		if item.AgentID != "" {
			id, err := util.ParseGoUUID(item.AgentID)
			if err != nil || id == uuid.Nil || item.Key == "hyperframes" {
				return ErrRuntimeInvalidInput
			}
		} else if item.AuthState != "unknown" && item.AuthState != "not_required" {
			return ErrRuntimeInvalidInput
		}
		if item.APIOrigin != "" {
			origin, err := util.CanonicalStudioOrigin(item.APIOrigin)
			if err != nil || origin != item.APIOrigin || item.Key != "zenith-studio" {
				return ErrRuntimeInvalidInput
			}
		}
		if !slices.Contains([]string{"pending", "installing", "installed", "error"}, item.State) ||
			!slices.Contains(
				[]string{
					"unknown",
					"needs_auth",
					"authenticated",
					"connecting",
					"error",
					"not_required",
				},
				item.AuthState,
			) ||
			!slices.Contains([]string{"", "setup_failed", "prerequisites_missing"}, item.ReasonCode) {
			return ErrRuntimeInvalidInput
		}
		if (item.Key == "hyperframes") != (item.AuthState == "not_required") {
			return ErrRuntimeInvalidInput
		}
		observed, err := time.Parse(time.RFC3339Nano, item.ObservedAt)
		if err != nil || observed.After(time.Now().Add(5*time.Minute)) {
			return ErrRuntimeInvalidInput
		}
	}
	return nil
}
