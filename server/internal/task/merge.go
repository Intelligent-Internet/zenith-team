package task

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const maxMergeResultTextBytes = 256

var (
	ErrMergeClaimNotFound = errors.New("Task merge claim not found")
	ErrMergeConflict      = errors.New("Task merge result conflicts with current state")
)

// MergeClaim is the exact completed Task Run a daemon may merge.
type MergeClaim struct {
	JobID                   uuid.UUID       `json:"jobId"`
	CompanyID               uuid.UUID       `json:"companyId"`
	TaskID                  uuid.UUID       `json:"taskId"`
	RunID                   uuid.UUID       `json:"runId"`
	TaskExecutionGeneration int64           `json:"taskExecutionGeneration"`
	Attempt                 int32           `json:"attempt"`
	ConversationID          uuid.UUID       `json:"conversationId"`
	Context                 json.RawMessage `json:"context"`
}

// ManualMergeClaimInput binds an explicit desktop acceptance to the exact
// Task revision and execution generation the person reviewed.
type ManualMergeClaimInput struct {
	TaskID                      uuid.UUID
	RunID                       uuid.UUID
	ExpectedRevision            int64
	ExpectedExecutionGeneration int64
}

type MergeResultStatus string

const (
	MergeResultMerged  MergeResultStatus = "merged"
	MergeResultBlocked MergeResultStatus = "blocked"
)

// MergeResultInput is the daemon's durable acknowledgement of the local Git
// operation, not an HTTP transport acknowledgement.
type MergeResultInput struct {
	Attempt      int32
	Status       MergeResultStatus
	ResultCode   string
	MergedCommit string
}

type MergeService interface {
	Claim(context.Context, auth.UserPrincipal, uuid.UUID, string) (*MergeClaim, error)
	ClaimManual(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		string,
		ManualMergeClaimInput,
	) (MergeClaim, error)
	RenewLease(context.Context, auth.UserPrincipal, uuid.UUID, string, uuid.UUID, int32) error
	RecordResult(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		string,
		uuid.UUID,
		MergeResultInput,
	) error
}

var _ MergeService = (*DefaultMergeService)(nil)

type DefaultMergeService struct {
	store *db.Store
	bus   *events.Bus
}

func NewMergeService(store *db.Store, bus *events.Bus) (*DefaultMergeService, error) {
	if store == nil || bus == nil {
		return nil, errors.New("Task merge dependencies are incomplete")
	}
	return &DefaultMergeService{store: store, bus: bus}, nil
}

func (s *DefaultMergeService) Claim(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
) (*MergeClaim, error) {
	daemonID = strings.TrimSpace(daemonID)
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(runtimeID) || daemonID == "" {
		return nil, ErrMergeClaimNotFound
	}
	candidate, err := s.store.Queries().GetNextTaskMergeJobClaimCandidate(
		ctx, mergeClaimCandidateParams(principal.UserID, runtimeID, daemonID),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve Task merge claim candidate: %w", err)
	}

	var claim *MergeClaim
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		if err := lockMergeAuthority(ctx, tx, principal.UserID, candidate.CompanyID); err != nil {
			return err
		}
		if _, err := tx.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				CompanyID: candidate.CompanyID,
				TaskID:    candidate.TaskID,
			},
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeClaimNotFound
		} else if err != nil {
			return fmt.Errorf("lock Task merge claim fence: %w", err)
		}
		row, err := tx.ClaimTaskMergeJob(
			ctx,
			mergeClaimParams(candidate.JobID, principal.UserID, runtimeID, daemonID),
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeClaimNotFound
		}
		if err != nil {
			return fmt.Errorf("claim Task merge job: %w", err)
		}
		contextDocument := json.RawMessage(row.Context)
		if len(contextDocument) == 0 {
			contextDocument = json.RawMessage("{}")
		}
		claim = &MergeClaim{
			JobID: row.JobID, CompanyID: row.CompanyID, TaskID: row.TaskID,
			RunID: row.RunID, TaskExecutionGeneration: row.TaskExecutionGeneration,
			Attempt:        row.Attempt,
			ConversationID: row.ConversationID, Context: contextDocument,
		}
		return nil
	})
	if errors.Is(err, ErrMergeClaimNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return claim, nil
}

// mergeClaimCandidateParams binds the lock-free first read of a Proactive
// merge claim. Merging a Task's work into its Project runs that Task, so
// both claim statements check that the daemon's member holds
// project.tasks.execute there (ADR 0017).
func mergeClaimCandidateParams(
	userID, runtimeID uuid.UUID,
	daemonID string,
) generated.GetNextTaskMergeJobClaimCandidateParams {
	sets := auth.ProjectPermissionHeldSets()
	return generated.GetNextTaskMergeJobClaimCandidateParams{
		ActorRoles:  auth.RolesWith(auth.PermissionCompanyProactiveModeManage),
		ActorUserID: userID, RuntimeID: runtimeID, DaemonID: daemonID,
		ExecutePermission:         string(auth.ProjectPermissionTasksExecute),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
	}
}

// mergeClaimParams binds the locked claim of one job, repeating every check
// of mergeClaimCandidateParams.
func mergeClaimParams(jobID, userID, runtimeID uuid.UUID, daemonID string) generated.ClaimTaskMergeJobParams {
	candidate := mergeClaimCandidateParams(userID, runtimeID, daemonID)
	return generated.ClaimTaskMergeJobParams{
		JobID:       jobID,
		ActorRoles:  candidate.ActorRoles,
		ActorUserID: userID, RuntimeID: runtimeID, DaemonID: daemonID,
		ExecutePermission:         candidate.ExecutePermission,
		CompanyDefaultPermissions: candidate.CompanyDefaultPermissions,
		ManagementPermissions:     candidate.ManagementPermissions,
		ManagementRoles:           candidate.ManagementRoles,
	}
}

// ClaimManual establishes the durable lifecycle fence for an explicit review
// acceptance. The returned claim is the only identity the daemon may pass to
// the local workspace merge path; request ids are rechecked under the Task
// lock and never become local mutation authority directly.
func (s *DefaultMergeService) ClaimManual(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
	runtimeID uuid.UUID,
	daemonID string,
	input ManualMergeClaimInput,
) (MergeClaim, error) {
	daemonID = strings.TrimSpace(daemonID)
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(companyID) ||
		!util.ValidUUID(runtimeID) || !util.ValidUUID(input.TaskID) || daemonID == "" ||
		input.RunID == uuid.Nil || input.ExpectedRevision < 1 ||
		input.ExpectedRevision > 9_007_199_254_740_991 ||
		input.ExpectedExecutionGeneration < 1 ||
		input.ExpectedExecutionGeneration > 9_007_199_254_740_991 {
		return MergeClaim{}, ErrMergeConflict
	}

	// A manual acceptance merges too, under the same Project permission as a
	// Proactive claim (mergeClaimCandidateParams).
	fence := auth.ProjectPermissionHeldSets()
	var claim MergeClaim
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		if err := lockMergeAuthority(ctx, tx, principal.UserID, companyID); err != nil {
			return err
		}
		current, err := tx.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				CompanyID: companyID,
				TaskID:    input.TaskID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeClaimNotFound
		}
		if err != nil {
			return fmt.Errorf("lock manual Task merge fence: %w", err)
		}
		// The permission is asked first, with the rule the claim statement
		// repeats, so a member who may not run Tasks in the Project is told
		// so instead of hearing that the review is stale. A member who
		// cannot read the Project hears that the Task is not there, as a
		// read of it answers.
		reads, err := auth.HoldsProjectPermission(
			ctx, tx, companyID, current.ProjectID, principal.UserID, auth.ProjectPermissionRead,
		)
		if err != nil {
			return err
		}
		if !reads {
			return ErrMergeClaimNotFound
		}
		executes, err := auth.HoldsProjectPermission(
			ctx, tx, companyID, current.ProjectID, principal.UserID, auth.ProjectPermissionTasksExecute,
		)
		if err != nil {
			return err
		}
		if !executes {
			return auth.ErrProjectPermissionRequired
		}
		if current.Version != input.ExpectedRevision ||
			current.ExecutionGeneration != input.ExpectedExecutionGeneration ||
			current.Status != string(StatusInReview) {
			return ErrMergeConflict
		}
		row, err := tx.ClaimManualTaskMergeJob(
			ctx,
			generated.ClaimManualTaskMergeJobParams{
				ActorUserID:                 principal.UserID,
				CompanyID:                   companyID,
				TaskID:                      input.TaskID,
				RunID:                       input.RunID,
				RuntimeID:                   runtimeID,
				DaemonID:                    daemonID,
				ExpectedExecutionGeneration: input.ExpectedExecutionGeneration,
				ExpectedRevision:            input.ExpectedRevision,
				ExecutePermission:           string(auth.ProjectPermissionTasksExecute),
				CompanyDefaultPermissions:   fence.CompanyDefaultPermissions,
				ManagementPermissions:       fence.ManagementPermissions,
				ManagementRoles:             fence.ManagementRoles,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeConflict
		}
		if err != nil {
			return fmt.Errorf("claim manual Task merge job: %w", err)
		}
		contextDocument := json.RawMessage(row.Context)
		if len(contextDocument) == 0 {
			contextDocument = json.RawMessage("{}")
		}
		claim = MergeClaim{
			JobID: row.JobID, CompanyID: row.CompanyID, TaskID: row.TaskID,
			RunID: row.RunID, TaskExecutionGeneration: row.TaskExecutionGeneration,
			Attempt: row.Attempt, ConversationID: row.ConversationID,
			Context: contextDocument,
		}
		return nil
	})
	if err != nil {
		return MergeClaim{}, err
	}
	return claim, nil
}

func (s *DefaultMergeService) RenewLease(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
	jobID uuid.UUID,
	attempt int32,
) error {
	daemonID = strings.TrimSpace(daemonID)
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(runtimeID) ||
		daemonID == "" || !util.ValidUUID(jobID) {
		return ErrMergeClaimNotFound
	}
	if attempt < 1 {
		return ErrMergeConflict
	}
	candidate, err := s.store.Queries().GetTaskMergeJobResultCandidate(
		ctx,
		generated.GetTaskMergeJobResultCandidateParams{
			JobID: jobID, RuntimeID: runtimeID, DaemonID: daemonID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMergeClaimNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve Task merge lease candidate: %w", err)
	}
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		if err := lockMergeAuthority(ctx, tx, principal.UserID, candidate.CompanyID); err != nil {
			return err
		}
		if _, err := tx.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				TaskID: candidate.TaskID, CompanyID: candidate.CompanyID,
			},
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeClaimNotFound
		} else if err != nil {
			return fmt.Errorf("lock Task merge lease fence: %w", err)
		}
		locked, err := tx.LockTaskMergeJobForResult(
			ctx,
			generated.LockTaskMergeJobForResultParams{
				JobID: jobID, RuntimeID: runtimeID, DaemonID: daemonID,
				ActorUserID: principal.UserID, Attempt: attempt,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeConflict
		}
		if err != nil {
			return fmt.Errorf("lock Task merge lease: %w", err)
		}
		if locked.TaskMergeJob.Status != "executing" || !locked.LeaseActive {
			return ErrMergeConflict
		}
		if _, err := tx.RenewTaskMergeJobLease(
			ctx,
			generated.RenewTaskMergeJobLeaseParams{JobID: jobID, Attempt: attempt},
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeConflict
		} else if err != nil {
			return fmt.Errorf("renew Task merge lease: %w", err)
		}
		return nil
	})
}

func (s *DefaultMergeService) RecordResult(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
	jobID uuid.UUID,
	input MergeResultInput,
) error {
	daemonID = strings.TrimSpace(daemonID)
	normalized, valid := normalizeMergeResult(input)
	if !util.ValidUUID(principal.UserID) || !util.ValidUUID(runtimeID) || daemonID == "" ||
		!util.ValidUUID(jobID) || !valid {
		return ErrMergeConflict
	}
	candidate, err := s.store.Queries().GetTaskMergeJobResultCandidate(
		ctx,
		generated.GetTaskMergeJobResultCandidateParams{
			JobID: jobID, RuntimeID: runtimeID, DaemonID: daemonID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrMergeClaimNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve Task merge result candidate: %w", err)
	}

	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		if err := lockMergeAuthority(ctx, tx, principal.UserID, candidate.CompanyID); err != nil {
			return err
		}
		currentRow, err := tx.GetTaskForCompanyByIDForUpdate(
			ctx,
			generated.GetTaskForCompanyByIDForUpdateParams{
				TaskID: candidate.TaskID, CompanyID: candidate.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeClaimNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Task merge result fence: %w", err)
		}
		locked, err := tx.LockTaskMergeJobForResult(
			ctx,
			generated.LockTaskMergeJobForResultParams{
				JobID: jobID, RuntimeID: runtimeID, DaemonID: daemonID,
				ActorUserID: principal.UserID, Attempt: normalized.Attempt,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeConflict
		}
		if err != nil {
			return fmt.Errorf("lock Task merge result: %w", err)
		}
		job := locked.TaskMergeJob
		if job.Status == "succeeded" || job.Status == "blocked" {
			if !mergeTerminalReplayMatches(job, normalized) {
				return ErrMergeConflict
			}
			return nil
		}
		if job.Status != "executing" {
			return ErrMergeConflict
		}
		if !locked.LeaseActive {
			return ErrMergeConflict
		}

		if normalized.Status == MergeResultMerged {
			if currentRow.ExecutionGeneration != job.TaskExecutionGeneration {
				return ErrMergeConflict
			}
			if currentRow.Status != string(StatusDone) {
				if currentRow.Status != string(StatusInReview) {
					return ErrMergeConflict
				}
				previous := taskFromRow(currentRow)
				target := previous
				target.Status = StatusDone
				if _, err := updateTask(
					ctx,
					tx,
					target,
					previous.Revision,
					&pending,
					events.UserActor(job.AuthorityUserID.String()),
					false,
					projectWriteFence{},
				); err != nil {
					return err
				}
				projection, err := tx.GetTaskProjectionForSystem(
					ctx,
					systemProjectionParams(job.CompanyID, job.TaskID),
				)
				if err != nil {
					return fmt.Errorf("project merged Task: %w", err)
				}
				updated := responseFromSystemProjectionRow(projection)
				if err := stageTaskEvent(
					ctx, tx, &pending,
					newTaskEvent(job.AuthorityUserID, EventUpdated, updated),
				); err != nil {
					return err
				}
				if err := stageDependentProjectionEventsInTx(
					ctx,
					tx,
					&pending,
					events.UserActor(job.AuthorityUserID.String()),
					job.CompanyID,
					job.TaskID,
					previous.Status,
					updated.Status,
				); err != nil {
					return err
				}
				if err := ReconcileOutcomeReportInTx(
					ctx,
					tx,
					&pending,
					OutcomeReportInput{
						Actor:          ActorRef{Kind: ActorUser, ID: job.AuthorityUserID},
						PreviousStatus: previous.Status,
						Task:           updated,
					},
				); err != nil {
					return err
				}
			}
		}

		status := "blocked"
		if normalized.Status == MergeResultMerged {
			status = "succeeded"
		}
		_, err = tx.CompleteTaskMergeJob(ctx, generated.CompleteTaskMergeJobParams{
			Status: status, ResultCode: mergeOptionalText(normalized.ResultCode),
			MergedCommit: mergeOptionalText(normalized.MergedCommit), JobID: jobID,
			Attempt: normalized.Attempt,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeConflict
		}
		if err != nil {
			return fmt.Errorf("complete Task merge job: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return nil
}

// lockMergeAuthority is the daemon mutation lifecycle fence. Company and
// acting membership locks precede Task and job locks, so deletion or access
// revocation either commits first or waits for the whole mutation.
func lockMergeAuthority(
	ctx context.Context,
	queries *generated.Queries,
	actorUserID uuid.UUID,
	companyID uuid.UUID,
) error {
	if _, err := queries.LockCompanyForMembershipRead(ctx, companyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMergeClaimNotFound
		}
		return fmt.Errorf("lock Task merge Company: %w", err)
	}
	membership, err := queries.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{
			CompanyID: companyID,
			UserID:    actorUserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return ErrMergeClaimNotFound
	}
	if err != nil {
		return fmt.Errorf("lock Task merge acting membership: %w", err)
	}
	return nil
}

func normalizeMergeResult(input MergeResultInput) (MergeResultInput, bool) {
	if input.Attempt < 1 {
		return MergeResultInput{}, false
	}
	input.ResultCode = strings.TrimSpace(input.ResultCode)
	input.MergedCommit = strings.TrimSpace(input.MergedCommit)
	if len(input.ResultCode) > maxMergeResultTextBytes ||
		len(input.MergedCommit) > maxMergeResultTextBytes {
		return MergeResultInput{}, false
	}
	switch input.Status {
	case MergeResultMerged:
		if input.MergedCommit == "" || strings.ToLower(input.MergedCommit) != input.MergedCommit ||
			len(input.MergedCommit) != 40 && len(input.MergedCommit) != 64 {
			return MergeResultInput{}, false
		}
		if _, err := hex.DecodeString(input.MergedCommit); err != nil {
			return MergeResultInput{}, false
		}
	case MergeResultBlocked:
		if input.ResultCode == "" || input.MergedCommit != "" {
			return MergeResultInput{}, false
		}
	default:
		return MergeResultInput{}, false
	}
	return input, true
}

func mergeTerminalReplayMatches(job generated.TaskMergeJob, input MergeResultInput) bool {
	wantedStatus := "blocked"
	if input.Status == MergeResultMerged {
		wantedStatus = "succeeded"
	}
	return job.Status == wantedStatus && mergeTextValue(job.ResultCode) == input.ResultCode &&
		mergeTextValue(job.MergedCommit) == input.MergedCommit
}

func mergeOptionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func mergeTextValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
