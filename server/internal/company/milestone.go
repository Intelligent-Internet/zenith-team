package company

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	taskdomain "boardly.dev/boardly/server/internal/task"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

// maxMilestoneAttestationNote bounds the founder's own explanation of what was
// already done. It is a sentence, not a document.
const maxMilestoneAttestationNote = 2000

// MilestoneAttestation is a founder's statement that a milestone was completed
// outside Zenith Team. It is the only part of milestone progress that is
// stored: everything else is derived from Tasks at read time.
type MilestoneAttestation struct {
	MilestoneType  taskdomain.MilestoneType `json:"milestoneType"`
	CatalogVersion int32                    `json:"catalogVersion"`
	Note           string                   `json:"note"`
	AttestedAt     time.Time                `json:"attestedAt"`
}

// MilestoneService records what the founder says was already finished outside
// Zenith Team. There is no read surface: an Agent receives attestations in its
// frozen Run brief, and no screen lists them.
type MilestoneService interface {
	AttestMilestones(
		context.Context,
		auth.UserPrincipal,
		[]taskdomain.MilestoneType,
		string,
	) ([]MilestoneAttestation, error)
}

var _ MilestoneService = (*DefaultMilestoneService)(nil)

type DefaultMilestoneService struct {
	store *db.Store
}

func NewMilestoneService(store *db.Store) (*DefaultMilestoneService, error) {
	if store == nil {
		return nil, errors.New("Company milestone database store is required")
	}
	return &DefaultMilestoneService{store: store}, nil
}

// AttestMilestones records, in one transaction, everything the founder has just
// confirmed already exists. It is written as a set because that is how the
// question is asked — a founder lists what they have, rather than answering ten
// times — and because a partial write would leave the planner with a half-true
// history.
func (s *DefaultMilestoneService) AttestMilestones(
	ctx context.Context,
	principal auth.UserPrincipal,
	milestones []taskdomain.MilestoneType,
	note string,
) ([]MilestoneAttestation, error) {
	milestones, note, err := normalizeMilestoneAttestations(milestones, note)
	if err != nil {
		return nil, err
	}
	var result []MilestoneAttestation
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		result = result[:0]
		if err := authorizeMilestoneMutation(ctx, tx, principal, auth.PermissionCompanyPlanApply); err != nil {
			return err
		}
		for _, milestone := range milestones {
			row, err := tx.UpsertCompanyMilestoneAttestation(
				ctx,
				generated.UpsertCompanyMilestoneAttestationParams{
					CompanyID:        principal.CompanyID,
					MilestoneType:    string(milestone),
					CatalogVersion:   int32(taskdomain.MilestoneCatalogVersion),
					Note:             note,
					AttestedByUserID: principal.UserID,
				},
			)
			if err != nil {
				return fmt.Errorf("record Company milestone attestation: %w", err)
			}
			result = append(result, milestoneAttestationResponse(row))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// authorizeMilestoneMutation repeats the Company lifecycle fence every
// permission-scoped write takes: an attestation is a live member's word, so
// the role must still hold the permission at the moment it is written.
func authorizeMilestoneMutation(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	permission auth.Permission,
) error {
	if _, err := q.LockCompanyForMembershipMutation(
		ctx, principal.CompanyID,
	); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock Company for milestone mutation: %w", err)
	}
	membership, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: principal.CompanyID,
		UserID:    principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock milestone actor membership: %w", err)
	}
	if !auth.CompanyRole(membership.Role).Can(permission) {
		return ErrForbidden
	}
	return nil
}

// normalizeMilestoneAttestations rejects anything outside the catalogue and any
// repeat, so a mistyped or duplicated value fails loudly instead of quietly
// marking the wrong outcome done.
func normalizeMilestoneAttestations(
	milestones []taskdomain.MilestoneType,
	note string,
) ([]taskdomain.MilestoneType, string, error) {
	if len(milestones) == 0 {
		return nil, "", ErrInvalidInput
	}
	note = protocol.TrimContractWhitespace(note)
	if len(note) > maxMilestoneAttestationNote {
		return nil, "", ErrInvalidInput
	}
	seen := make(map[taskdomain.MilestoneType]struct{}, len(milestones))
	for _, milestone := range milestones {
		if !taskdomain.ValidMilestoneType(milestone) {
			return nil, "", ErrInvalidInput
		}
		if _, duplicate := seen[milestone]; duplicate {
			return nil, "", ErrInvalidInput
		}
		seen[milestone] = struct{}{}
	}
	return milestones, note, nil
}

func milestoneAttestationResponse(
	row generated.CompanyMilestoneAttestation,
) MilestoneAttestation {
	return MilestoneAttestation{
		MilestoneType:  taskdomain.MilestoneType(row.MilestoneType),
		CatalogVersion: row.CatalogVersion,
		Note:           row.Note,
		AttestedAt:     row.CreatedAt.Time.UTC(),
	}
}
