package agent

// Runtime rename: the one user-plane write on a runtime row. A person names
// the machine; the daemon keeps naming the runtime. The two names live in
// separate columns so a re-registration can never overwrite the user's
// choice, and clients render `customName ?? name`.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// RuntimeRenameInput is the rename request after the handler bound it.
// CustomName is trimmed here; blank clears the name. ApplyToMachine renames
// every runtime the same daemon registered in the company — the machine —
// rather than the one row the path names.
type RuntimeRenameInput struct {
	CustomName     string
	ApplyToMachine bool
}

// Rename sets or clears a runtime's custom name and returns the row's full
// user-plane projection. Only the runtime's registrant may rename it
// (ADR 0014), whatever their Company role, judged inside the write
// transaction on the stored membership and the locked row, so a revoked
// member or a moved owner cannot slip through between check and write. A
// non-member, a foreign or unknown runtime and another member's runtime —
// invisible to every role — are all ErrRuntimeNotFound; a name past 100
// runes is ErrRuntimeInvalidInput before any statement runs. After commit
// one daemon.roster_changed `update` names the machine and every row it
// touched; availability did not change, so no agent.status_changed follows.
func (s *DefaultRuntimeService) Rename(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	input RuntimeRenameInput,
) (Runtime, error) {
	customName, err := normalizeRuntimeRenameInput(input)
	if err != nil {
		return Runtime{}, err
	}

	var renamed Runtime
	var daemonID string
	var renamedIDs []string
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		renamedIDs = renamedIDs[:0]
		if _, err := lockMemberRole(
			ctx,
			tx,
			principal.CompanyID,
			principal.UserID,
			"runtime rename",
		); errors.Is(
			err,
			ErrNotFound,
		) {
			return ErrRuntimeNotFound
		} else if err != nil {
			return err
		}
		row, err := tx.GetRuntimeForUpdate(ctx, generated.GetRuntimeForUpdateParams{
			CompanyID: principal.CompanyID,
			ID:        runtimeID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRuntimeNotFound
		}
		if err != nil {
			return fmt.Errorf("lock runtime for rename: %w", err)
		}
		// Another member's machine is invisible to every role (ADR 0014),
		// the Company owner included, so it answers like an unknown id.
		if !registeredBy(row.OwnerUserID, principal.UserID) {
			return ErrRuntimeNotFound
		}
		daemonID = row.DaemonID

		if input.ApplyToMachine {
			siblings, err := tx.SetRuntimeCustomNameForDaemon(
				ctx,
				generated.SetRuntimeCustomNameForDaemonParams{
					CustomName:  customName,
					CompanyID:   principal.CompanyID,
					DaemonID:    row.DaemonID,
					OwnerUserID: principal.UserID,
				},
			)
			if err != nil {
				return fmt.Errorf("rename runtimes of daemon: %w", err)
			}
			for _, sibling := range siblings {
				renamedIDs = append(renamedIDs, sibling.ID.String())
			}
		} else {
			updated, err := tx.SetRuntimeCustomName(ctx, generated.SetRuntimeCustomNameParams{
				CustomName: customName,
				CompanyID:  principal.CompanyID,
				ID:         runtimeID,
			})
			if err != nil {
				return fmt.Errorf("rename runtime: %w", err)
			}
			renamedIDs = append(renamedIDs, updated.ID.String())
		}

		detail, err := tx.GetRuntimeForCompany(ctx, generated.GetRuntimeForCompanyParams{
			CompanyID:    principal.CompanyID,
			ID:           runtimeID,
			ViewerUserID: principal.UserID,
		})
		if err != nil {
			return fmt.Errorf("read renamed runtime: %w", err)
		}
		renamed = runtimeFromDetailRow(detail)
		return nil
	})
	if err != nil {
		return Runtime{}, err
	}

	// The UPDATE returns rows in no particular order; the frame is stable
	// so two viewers reconciling the same rename see the same ids.
	sort.Strings(renamedIDs)
	s.publishRoster(
		ctx,
		protocol.DaemonRosterActionUpdate,
		principal.CompanyID,
		principal.UserID,
		daemonID,
		renamedIDs,
	)
	return renamed, nil
}

// normalizeRuntimeRenameInput is the before-the-write validation: the name
// is trimmed, blank clears (an invalid pgtype.Text writes NULL), and more
// than 100 runes is refused. Runes rather than bytes, so a name in any
// script gets the same 100 characters.
func normalizeRuntimeRenameInput(input RuntimeRenameInput) (pgtype.Text, error) {
	name := strings.TrimSpace(input.CustomName)
	if name == "" {
		return pgtype.Text{}, nil
	}
	if utf8.RuneCountInString(name) > 100 {
		return pgtype.Text{}, ErrRuntimeInvalidInput
	}
	return pgtype.Text{String: name, Valid: true}, nil
}
