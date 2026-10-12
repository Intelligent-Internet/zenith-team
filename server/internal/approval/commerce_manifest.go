package approval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

const commerceManifestApprovalLifetime = 30 * 24 * time.Hour

// CommerceManifestPublishRequest is the frozen approval companion Commerce
// creates beside one immutable manifest. The Commerce transaction owns the
// resource write; approval owns the human decision and continuation receipt.
type CommerceManifestPublishRequest struct {
	CompanyID       uuid.UUID
	ManifestID      uuid.UUID
	TaskID          uuid.UUID
	ConversationID  uuid.UUID
	AgentID         uuid.UUID
	ActorUserID     uuid.UUID
	OriginRunID     uuid.UUID
	RequestKey      uuid.UUID
	Digest          string
	Summary         string
	PreviewMarkdown string
	Now             time.Time
}

// CommerceManifestPublishResolution records that Commerce successfully
// activated the exact digest the owner reviewed. The resulting pending
// continuation receipt is consumed by the existing Approval lifecycle worker.
type CommerceManifestPublishResolution struct {
	CompanyID       uuid.UUID
	ManifestID      uuid.UUID
	DecidedByUserID uuid.UUID
	Digest          string
	Now             time.Time
}

// CommerceManifestPublishCheck fences provider I/O with the exact still-live
// owner approval. AllowApproved is reserved for idempotent reads of a manifest
// that is already active.
type CommerceManifestPublishCheck struct {
	CompanyID     uuid.UUID
	ManifestID    uuid.UUID
	Digest        string
	Now           time.Time
	AllowApproved bool
}

type commerceManifestPublishAction struct {
	SchemaVersion int       `json:"schemaVersion"`
	ManifestID    uuid.UUID `json:"manifestId"`
	Digest        string    `json:"digest"`
}

// RequestCommerceManifestPublishInTx creates the owner approval, Inbox item,
// and requested event in the caller's manifest transaction. Replaying the
// same manifest request returns the existing Approval without duplicating
// either side effect. Fresh Inbox results are returned so the Commerce owner
// can present them only after its transaction commits.
func RequestCommerceManifestPublishInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input CommerceManifestPublishRequest,
) (Approval, []inbox.CreateResult, bool, error) {
	action, digest, err := normalizeCommerceManifestPublishRequest(input)
	if err != nil || q == nil || pending == nil {
		return Approval{}, nil, false, ErrApprovalInvalidInput
	}
	existing, err := q.GetApprovalByOriginRequestKey(
		ctx,
		generated.GetApprovalByOriginRequestKeyParams{
			OriginRunID: input.OriginRunID,
			RequestKey:  input.RequestKey,
		},
	)
	if err == nil {
		if !commerceManifestApprovalMatches(existing, input.ManifestID, digest) {
			return Approval{}, nil, false, ErrApprovalConflict
		}
		result, projectionErr := approvalProjectionForSystem(ctx, q, existing)
		return result, nil, false, projectionErr
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, nil, false, fmt.Errorf("load Commerce manifest Approval replay: %w", err)
	}

	subjectID := input.ManifestID
	row, err := q.CreateApproval(ctx, generated.CreateApprovalParams{
		CompanyID:          input.CompanyID,
		Type:               string(ApprovalTypeCommerceManifestPublish),
		CommerceManifestID: util.NullUUIDFromPtr(&subjectID),
		RuleKey:            string(ApprovalTypeCommerceManifestPublish),
		Title:              "Review & publish payment setup",
		Summary:            pgtype.Text{String: input.Summary, Valid: input.Summary != ""},
		PreviewMarkdown:    input.PreviewMarkdown,
		RequestedByAgentID: input.AgentID,
		OriginRunID:        input.OriginRunID,
		RequestKey:         input.RequestKey,
		TaskID:             util.NullUUIDFromPtr(&input.TaskID),
		ConversationID:     input.ConversationID,
		CanonicalAction:    action,
		RequestSha256:      digest,
		ActionSha256:       digest,
		ExpiresAt: pgtype.Timestamptz{
			Time: input.Now.UTC().Add(commerceManifestApprovalLifetime), Valid: true,
		},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		existing, replayErr := q.GetApprovalByOriginRequestKey(
			ctx,
			generated.GetApprovalByOriginRequestKeyParams{
				OriginRunID: input.OriginRunID,
				RequestKey:  input.RequestKey,
			},
		)
		if replayErr != nil {
			return Approval{}, nil, false, fmt.Errorf(
				"load concurrent Commerce manifest Approval replay: %w",
				replayErr,
			)
		}
		if !commerceManifestApprovalMatches(existing, input.ManifestID, digest) {
			return Approval{}, nil, false, ErrApprovalConflict
		}
		result, projectionErr := approvalProjectionForSystem(ctx, q, existing)
		return result, nil, false, projectionErr
	}
	if err != nil {
		return Approval{}, nil, false, fmt.Errorf("create Commerce manifest Approval: %w", err)
	}
	result, err := approvalProjectionForSystem(ctx, q, row)
	if err != nil {
		return Approval{}, nil, false, err
	}
	// Publishing payment setup is a Company owner action behind its own
	// recent-owner-auth fence, not a decision inside the Project, so its
	// recipients are the Company approvals.resolve holders; but the Approval
	// is read only by readers of the Task chat it was raised in (ADR 0017),
	// so only those among them are told.
	recipients, scoped, err := commerceApprovalRecipientsInTx(ctx, q, input.CompanyID, input.ConversationID)
	if err != nil {
		return Approval{}, nil, false, err
	}
	inboxResults, err := createApprovalInboxItems(ctx, q, pending, row, recipients, scoped)
	if err != nil {
		return Approval{}, nil, false, err
	}
	if err := stageApprovalEvent(
		ctx, q, pending,
		EventApprovalRequested,
		events.RunActor(input.AgentID.String(), input.OriginRunID.String(), input.ActorUserID.String()),
		result,
	); err != nil {
		return Approval{}, nil, false, err
	}
	return result, inboxResults, true, nil
}

// ResolveCommerceManifestPublishInTx closes the companion Approval only after
// Commerce has activated the exact reviewed manifest in the same transaction.
func ResolveCommerceManifestPublishInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	input CommerceManifestPublishResolution,
) (Approval, bool, error) {
	if q == nil || pending == nil || !validCommerceManifestResolution(input) {
		return Approval{}, false, ErrApprovalInvalidInput
	}
	current, err := q.LockCommerceManifestApproval(
		ctx,
		generated.LockCommerceManifestApprovalParams{
			CompanyID:          input.CompanyID,
			CommerceManifestID: input.ManifestID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, false, ErrApprovalConflict
	}
	if err != nil {
		return Approval{}, false, fmt.Errorf("lock Commerce manifest Approval: %w", err)
	}
	_, actionDigest, err := commerceManifestAction(input.ManifestID, input.Digest)
	if err != nil || !commerceManifestApprovalMatches(current, input.ManifestID, actionDigest) {
		return Approval{}, false, ErrApprovalConflict
	}
	if current.Status == string(ApprovalStatusApproved) &&
		current.ExecutionStatus.Valid &&
		current.ExecutionStatus.String == string(ApprovalExecutionExecuted) &&
		current.ContinuationStatus.Valid {
		result, projectionErr := approvalProjectionForSystem(ctx, q, current)
		return result, false, projectionErr
	}
	if current.Status != string(ApprovalStatusPending) ||
		approvalHasLapsed(current, input.Now.UTC()) {
		return Approval{}, false, ErrApprovalConflict
	}
	resultDocument, err := json.Marshal(struct {
		ManifestID uuid.UUID `json:"manifestId"`
		State      string    `json:"state"`
	}{ManifestID: input.ManifestID, State: "active"})
	if err != nil {
		return Approval{}, false, ErrApprovalInvalidInput
	}
	continuationPrompt := fmt.Sprintf(
		"The owner approved and published Zenith Team Commerce manifest %s with digest %s. Call commerce_manifest_status with this manifestId, continue the payment integration, and verify the sandbox checkout and entitlement flow.",
		input.ManifestID,
		input.Digest,
	)
	resolved, err := q.ResolveApproval(ctx, generated.ResolveApprovalParams{
		Status:             string(ApprovalStatusApproved),
		DecidedByUserID:    input.DecidedByUserID,
		ExecutionStatus:    pgtype.Text{String: string(ApprovalExecutionExecuted), Valid: true},
		ExecutionResult:    resultDocument,
		ContinuationPrompt: continuationPrompt,
		CompanyID:          input.CompanyID,
		ApprovalID:         current.ID,
		ExpectedVersion:    current.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, false, ErrApprovalConflict
	}
	if err != nil {
		return Approval{}, false, fmt.Errorf("resolve Commerce manifest Approval: %w", err)
	}
	actor := inbox.Actor{Kind: "user", ID: &input.DecidedByUserID}
	if err := inbox.CloseEntityInTx(
		ctx,
		q,
		pending,
		input.CompanyID,
		inbox.Entity{Type: protocol.EntityApproval, ID: current.ID.String()},
		actor,
	); err != nil {
		return Approval{}, false, fmt.Errorf("close Commerce Approval Inbox items: %w", err)
	}
	result, err := approvalProjectionForSystem(ctx, q, resolved)
	if err != nil {
		return Approval{}, false, err
	}
	if err := stageApprovalEvent(
		ctx, q, pending,
		EventApprovalResolved,
		events.UserActor(input.DecidedByUserID.String()),
		result,
	); err != nil {
		return Approval{}, false, err
	}
	return result, true, nil
}

// CheckCommerceManifestPublishInTx locks and validates the companion Approval
// before Commerce makes any provider call. The same facts are checked again
// by ResolveCommerceManifestPublishInTx in the final activation transaction.
func CheckCommerceManifestPublishInTx(
	ctx context.Context,
	q *generated.Queries,
	input CommerceManifestPublishCheck,
) error {
	if q == nil || !util.ValidUUID(input.CompanyID) || !util.ValidUUID(input.ManifestID) ||
		input.Now.IsZero() {
		return ErrApprovalInvalidInput
	}
	_, digest, err := commerceManifestAction(input.ManifestID, input.Digest)
	if err != nil {
		return err
	}
	current, err := q.LockCommerceManifestApproval(
		ctx,
		generated.LockCommerceManifestApprovalParams{
			CompanyID:          input.CompanyID,
			CommerceManifestID: input.ManifestID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrApprovalConflict
	}
	if err != nil {
		return fmt.Errorf("lock Commerce manifest Approval: %w", err)
	}
	if !commerceManifestApprovalMatches(current, input.ManifestID, digest) {
		return ErrApprovalConflict
	}
	if current.Status == string(ApprovalStatusPending) &&
		!approvalHasLapsed(current, input.Now.UTC()) {
		return nil
	}
	if input.AllowApproved && current.Status == string(ApprovalStatusApproved) &&
		current.ExecutionStatus.Valid &&
		current.ExecutionStatus.String == string(ApprovalExecutionExecuted) &&
		current.ContinuationStatus.Valid {
		return nil
	}
	return ErrApprovalConflict
}

func normalizeCommerceManifestPublishRequest(
	input CommerceManifestPublishRequest,
) ([]byte, []byte, error) {
	if !util.ValidUUID(input.CompanyID) || !util.ValidUUID(input.ManifestID) ||
		!util.ValidUUID(input.TaskID) || !util.ValidUUID(input.ConversationID) ||
		!util.ValidUUID(input.AgentID) || !util.ValidUUID(input.ActorUserID) ||
		!util.ValidUUID(input.RequestKey) || input.OriginRunID == uuid.Nil ||
		input.Now.IsZero() ||
		!validCommerceApprovalText(input.Summary, 512) ||
		!validCommerceApprovalText(input.PreviewMarkdown, 128<<10) {
		return nil, nil, ErrApprovalInvalidInput
	}
	return commerceManifestAction(input.ManifestID, input.Digest)
}

func commerceManifestAction(manifestID uuid.UUID, digest string) ([]byte, []byte, error) {
	decoded, err := hex.DecodeString(digest)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(digest) != digest ||
		!util.ValidUUID(manifestID) {
		return nil, nil, ErrApprovalInvalidInput
	}
	action, err := json.Marshal(commerceManifestPublishAction{
		SchemaVersion: 1,
		ManifestID:    manifestID,
		Digest:        digest,
	})
	if err != nil {
		return nil, nil, ErrApprovalInvalidInput
	}
	hash := sha256.Sum256(action)
	return action, hash[:], nil
}

func commerceManifestApprovalMatches(
	row generated.Approval,
	manifestID uuid.UUID,
	digest []byte,
) bool {
	return row.Type == string(ApprovalTypeCommerceManifestPublish) &&
		row.CommerceManifestID.Valid && row.CommerceManifestID.UUID == manifestID &&
		bytes.Equal(row.RequestSha256, digest) && bytes.Equal(row.ActionSha256, digest)
}

func validCommerceManifestResolution(input CommerceManifestPublishResolution) bool {
	if !util.ValidUUID(input.CompanyID) || !util.ValidUUID(input.ManifestID) ||
		!util.ValidUUID(input.DecidedByUserID) || input.Now.IsZero() {
		return false
	}
	_, _, err := commerceManifestAction(input.ManifestID, input.Digest)
	return err == nil
}

func validCommerceApprovalText(value string, maximumBytes int) bool {
	return value != "" && len(value) <= maximumBytes && utf8.ValidString(value)
}
