package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/workspace"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

// TaskMergeStatus is the closed daemon-plane result status.
type TaskMergeStatus string

const (
	TaskMergeStatusMerged  TaskMergeStatus = "merged"
	TaskMergeStatusBlocked TaskMergeStatus = "blocked"
)

// taskMergeLeaseRenewInterval stays well inside the server's five-minute
// lease. It is a package variable so focused tests can exercise renewal
// without waiting for wall-clock minutes.
var taskMergeLeaseRenewInterval = time.Minute

// TaskMergeClaim is one durable Task merge job fenced to its original Run and
// Runtime. Context is the same frozen document that governed Run execution.
type TaskMergeClaim struct {
	JobID                   string          `json:"jobId"`
	CompanyID               string          `json:"companyId"`
	TaskID                  string          `json:"taskId"`
	RunID                   uuid.UUID       `json:"runId"`
	TaskExecutionGeneration int64           `json:"taskExecutionGeneration"`
	Attempt                 int32           `json:"attempt"`
	ConversationID          string          `json:"conversationId"`
	Context                 json.RawMessage `json:"context"`
}

type manualTaskMergeClaimRequest struct {
	CompanyID                   string    `json:"-"`
	DaemonID                    string    `json:"daemonId"`
	TaskID                      string    `json:"taskId"`
	RunID                       uuid.UUID `json:"runId"`
	ExpectedRevision            int64     `json:"expectedRevision"`
	ExpectedExecutionGeneration int64     `json:"expectedExecutionGeneration"`
}

// TaskMergeResult is the daemon's durable result for one merge claim.
type TaskMergeResult struct {
	Attempt      int32           `json:"attempt"`
	Status       TaskMergeStatus `json:"status"`
	ResultCode   string          `json:"resultCode"`
	MergedCommit string          `json:"mergedCommit"`
}

type taskMergeResultRequest struct {
	DaemonID     string          `json:"daemonId"`
	Attempt      int32           `json:"attempt"`
	Status       TaskMergeStatus `json:"status"`
	ResultCode   string          `json:"resultCode"`
	MergedCommit string          `json:"mergedCommit"`
}

// ClaimTaskMerge claims at most one merge job for a Runtime. No-content is a
// normal empty poll, not a decode failure.
func (c *Client) ClaimTaskMerge(
	ctx context.Context,
	daemonID, runtimeID string,
) (*TaskMergeClaim, error) {
	if !validTaskMergeID(daemonID) || !validTaskMergeID(runtimeID) {
		return nil, errors.New("invalid task merge claim identity")
	}
	path := "/api/v1/daemon/runtimes/" + runtimeID + "/task-merges/claim"
	var claim TaskMergeClaim
	status, err := c.postJSONStatus(ctx, path, map[string]any{"daemonId": daemonID}, &claim)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		if !validTaskMergeClaim(claim) {
			return nil, errors.New("invalid task merge claim response")
		}
		return &claim, nil
	default:
		return nil, fmt.Errorf("unexpected task merge claim status %d", status)
	}
}

// ClaimManualTaskMerge turns one explicit desktop review into the exact
// server-owned claim the local Git path is allowed to execute.
func (c *Client) ClaimManualTaskMerge(
	ctx context.Context,
	daemonID, runtimeID string,
	input manualTaskMergeClaimRequest,
) (TaskMergeClaim, error) {
	if !validTaskMergeID(daemonID) || !validTaskMergeID(runtimeID) ||
		!validTaskMergeID(input.CompanyID) || !validTaskMergeID(input.TaskID) || input.RunID == uuid.Nil ||
		input.ExpectedRevision < 1 || input.ExpectedRevision > 9_007_199_254_740_991 ||
		input.ExpectedExecutionGeneration < 1 ||
		input.ExpectedExecutionGeneration > 9_007_199_254_740_991 {
		return TaskMergeClaim{}, errors.New("invalid manual task merge claim")
	}
	input.DaemonID = daemonID
	path := "/api/v1/daemon/runtimes/" + runtimeID + "/task-merges/manual"
	var claim TaskMergeClaim
	if err := c.postJSON(ctx, path, input, &claim); err != nil {
		return TaskMergeClaim{}, err
	}
	if !validTaskMergeClaim(claim) || claim.CompanyID != input.CompanyID || claim.TaskID != input.TaskID ||
		claim.RunID != input.RunID ||
		claim.TaskExecutionGeneration != input.ExpectedExecutionGeneration {
		return TaskMergeClaim{}, errors.New("invalid manual task merge claim response")
	}
	return claim, nil
}

// RecordTaskMergeResult durably resolves a claimed merge job. The server
// endpoint is idempotent, so transient failures use the terminal retry policy.
func (c *Client) RecordTaskMergeResult(
	ctx context.Context,
	daemonID, runtimeID, jobID string,
	result TaskMergeResult,
) error {
	if !validTaskMergeID(daemonID) || !validTaskMergeID(runtimeID) ||
		!validTaskMergeID(jobID) || !validTaskMergeResult(result) {
		return errors.New("invalid task merge result")
	}
	path := "/api/v1/daemon/runtimes/" + runtimeID + "/task-merges/" + jobID + "/result"
	return c.postJSONWithRetry(ctx, path, taskMergeResultRequest{
		DaemonID: daemonID, Attempt: result.Attempt, Status: result.Status,
		ResultCode: result.ResultCode, MergedCommit: result.MergedCommit,
	}, defaultTerminalRetrySchedule)
}

// RenewTaskMergeLease extends one exact claim attempt. A later attempt cannot
// be renewed through this call, so losing authority is observable before any
// local Git mutation starts.
func (c *Client) RenewTaskMergeLease(
	ctx context.Context,
	daemonID, runtimeID, jobID string,
	attempt int32,
) error {
	if !validTaskMergeID(daemonID) || !validTaskMergeID(runtimeID) ||
		!validTaskMergeID(jobID) || attempt <= 0 {
		return errors.New("invalid task merge lease")
	}
	path := "/api/v1/daemon/runtimes/" + runtimeID + "/task-merges/" + jobID + "/lease"
	return c.postJSON(ctx, path, map[string]any{"daemonId": daemonID, "attempt": attempt}, nil)
}

func (d *Daemon) runTaskMergePoller(ctx context.Context, wakeup <-chan struct{}) {
	for {
		if ctx.Err() != nil {
			return
		}
		runtimeIDs := d.claimableRuntimeIDs()
		didWork := false
		hadFailure := false
		for _, runtimeID := range runtimeIDs {
			if ctx.Err() != nil {
				return
			}
			claim, err := d.client.ClaimTaskMerge(ctx, d.cfg.DaemonID, runtimeID)
			if err != nil {
				hadFailure = true
				if ctx.Err() == nil {
					d.logger.Warn("task merge claim failed", "runtime_id", runtimeID, "error", err)
				}
				continue
			}
			if claim == nil {
				continue
			}
			didWork = true
			if err := d.processTaskMergeClaim(ctx, runtimeID, *claim); err != nil {
				hadFailure = true
				if ctx.Err() == nil {
					d.logger.Warn(
						"task merge processing failed",
						"job_id", claim.JobID,
						"run_id", claim.RunID,
						"error", err,
					)
				}
				continue
			}
		}
		if didWork && !hadFailure {
			continue
		}
		if err := sleepWithContextOrWakeup(ctx, d.cfg.PollInterval, wakeup); err != nil {
			return
		}
	}
}

func (d *Daemon) processTaskMergeClaim(
	ctx context.Context,
	runtimeID string,
	claim TaskMergeClaim,
) error {
	_, err := d.completeTaskMergeClaim(ctx, runtimeID, claim)
	return err
}

func (d *Daemon) completeTaskMergeClaim(
	ctx context.Context,
	runtimeID string,
	claim TaskMergeClaim,
) (TaskMergeResult, error) {
	renewer := newTaskMergeLeaseRenewer(
		ctx,
		d.client,
		d.cfg.DaemonID,
		runtimeID,
		claim.JobID,
		claim.Attempt,
		taskMergeLeaseRenewInterval,
	)
	defer renewer.Stop()

	result, mergeErr := d.acceptTaskMergeWithLease(renewer.Context(), claim, renewer.Fence)
	if cause := context.Cause(renewer.Context()); cause != nil {
		return TaskMergeResult{}, cause
	}
	if mergeErr != nil {
		d.logger.Warn(
			"task merge blocked",
			"job_id", claim.JobID,
			"run_id", claim.RunID,
			"result_code", result.ResultCode,
			"error", mergeErr,
		)
	}

	// Some fail-closed outcomes are decided before workspace locks are needed.
	// Fence those results here; for normal Git work this also extends the lease
	// from the final mutation fence through durable result acknowledgement.
	if err := renewer.Fence(renewer.Context()); err != nil {
		return TaskMergeResult{}, err
	}
	result.Attempt = claim.Attempt
	if err := d.recordTaskMergeResultUntilFinal(
		renewer.Context(),
		runtimeID,
		claim.JobID,
		result,
	); err != nil {
		if cause := context.Cause(renewer.Context()); cause != nil {
			return TaskMergeResult{}, cause
		}
		return TaskMergeResult{}, err
	}
	d.logger.Info(
		"task merge result delivered",
		"job_id", claim.JobID,
		"run_id", claim.RunID,
		"status", result.Status,
		"result_code", result.ResultCode,
	)
	return result, nil
}

func (d *Daemon) recordTaskMergeResultUntilFinal(
	ctx context.Context,
	runtimeID, jobID string,
	result TaskMergeResult,
) error {
	for {
		err := d.client.RecordTaskMergeResult(
			ctx,
			d.cfg.DaemonID,
			runtimeID,
			jobID,
			result,
		)
		if err == nil || !isTransientError(err) {
			return err
		}
		if sleepErr := sleepWithContext(ctx, taskMergeRetryDelay(d.cfg.PollInterval)); sleepErr != nil {
			return sleepErr
		}
	}
}

type taskMergeLeaseRenewer struct {
	client       *Client
	daemonID     string
	runtimeID    string
	jobID        string
	attempt      int32
	interval     time.Duration
	operationCtx context.Context
	cancel       context.CancelCauseFunc
	loopCtx      context.Context
	stopLoop     context.CancelFunc
	renewMu      sync.Mutex
	startOnce    sync.Once
	loopDone     chan struct{}
}

func newTaskMergeLeaseRenewer(
	ctx context.Context,
	client *Client,
	daemonID, runtimeID, jobID string,
	attempt int32,
	interval time.Duration,
) *taskMergeLeaseRenewer {
	if interval <= 0 {
		interval = time.Minute
	}
	operationCtx, cancel := context.WithCancelCause(ctx)
	loopCtx, stopLoop := context.WithCancel(operationCtx)
	return &taskMergeLeaseRenewer{
		client:       client,
		daemonID:     daemonID,
		runtimeID:    runtimeID,
		jobID:        jobID,
		attempt:      attempt,
		interval:     interval,
		operationCtx: operationCtx,
		cancel:       cancel,
		loopCtx:      loopCtx,
		stopLoop:     stopLoop,
		loopDone:     make(chan struct{}),
	}
}

func (r *taskMergeLeaseRenewer) Context() context.Context {
	return r.operationCtx
}

// Fence synchronously renews the exact claim attempt. The workspace invokes
// it only while its process and cross-process mutation locks are held, so a
// successful return is the authority boundary immediately before Git work.
func (r *taskMergeLeaseRenewer) Fence(ctx context.Context) error {
	if cause := context.Cause(r.operationCtx); cause != nil {
		return cause
	}
	if err := r.renew(ctx); err != nil {
		return err
	}
	r.startOnce.Do(func() { go r.run() })
	return nil
}

func (r *taskMergeLeaseRenewer) renew(ctx context.Context) error {
	r.renewMu.Lock()
	defer r.renewMu.Unlock()
	if cause := context.Cause(r.operationCtx); cause != nil {
		return cause
	}
	err := r.client.RenewTaskMergeLease(
		ctx,
		r.daemonID,
		r.runtimeID,
		r.jobID,
		r.attempt,
	)
	if err == nil {
		return nil
	}
	if r.loopCtx.Err() != nil {
		return err
	}
	authorityErr := fmt.Errorf("renew Task merge lease: %w", err)
	r.cancel(authorityErr)
	return authorityErr
}

func (r *taskMergeLeaseRenewer) run() {
	defer close(r.loopDone)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.loopCtx.Done():
			return
		case <-ticker.C:
			if err := r.renew(r.loopCtx); err != nil {
				return
			}
		}
	}
}

func (r *taskMergeLeaseRenewer) Stop() {
	r.stopLoop()
	r.startOnce.Do(func() { close(r.loopDone) })
	<-r.loopDone
	r.cancel(context.Canceled)
}

func taskMergeRetryDelay(configured time.Duration) time.Duration {
	if configured <= 0 || configured > time.Second {
		return time.Second
	}
	return configured
}

func (d *Daemon) acceptTaskMerge(ctx context.Context, claim TaskMergeClaim) (TaskMergeResult, error) {
	return d.acceptTaskMergeWithLease(ctx, claim, nil)
}

func (d *Daemon) acceptTaskMergeWithLease(
	ctx context.Context,
	claim TaskMergeClaim,
	leaseFence func(context.Context) error,
) (TaskMergeResult, error) {
	if !validTaskMergeClaim(claim) {
		return blockedDaemonTaskMerge("invalid_claim", errors.New("invalid task merge claim"))
	}
	var document RunContext
	if err := json.Unmarshal(claim.Context, &document); err != nil {
		return blockedDaemonTaskMerge("invalid_context", errors.New("invalid task merge context"))
	}
	task := document.ExecutionProfile.Task
	if task == nil || task.ConversationID != claim.ConversationID || document.ExecutionProfile.Project == nil ||
		uuid.Validate(document.ExecutionProfile.Project.ID) != nil {
		return blockedDaemonTaskMerge("invalid_context", errors.New("invalid task merge context"))
	}
	input := workspace.TaskMergeInput{
		CompanyID: claim.CompanyID, Scope: "projects/" + document.ExecutionProfile.Project.ID,
		ConversationID: claim.ConversationID, RunID: claim.RunID,
		WorkspaceGeneration: task.WorkspaceGeneration,
		LeaseFence:          leaseFence,
	}
	var localLease *workspace.LocalDirectoryLease
	if resource := localDirectoryFor(document, d.cfg.DaemonID); resource != nil {
		if d.localDirs == nil {
			return blockedDaemonTaskMerge(
				"workspace_unavailable",
				errors.New("local directory manager is unavailable"),
			)
		}
		lease, err := d.acquireTaskMergeLocalDirectory(ctx, resource.LocalPath)
		if err != nil {
			return blockedDaemonTaskMerge("workspace_unavailable", err)
		}
		localLease = lease
		defer localLease.Release()
		input.LocalPath = localLease.Path()
	}
	if d.workspaces == nil {
		return blockedDaemonTaskMerge("local_failure", errors.New("workspace manager is unavailable"))
	}
	localResult, err := d.workspaces.MergeTask(ctx, input)
	if localResult.ResultCode != "" {
		return blockedDaemonTaskMerge(localResult.ResultCode, err)
	}
	if err != nil || localResult.MergedCommit == "" {
		return blockedDaemonTaskMerge("local_failure", err)
	}
	return TaskMergeResult{
		Status: TaskMergeStatusMerged, MergedCommit: localResult.MergedCommit,
	}, nil
}

func (d *Daemon) acquireTaskMergeLocalDirectory(
	ctx context.Context,
	path string,
) (*workspace.LocalDirectoryLease, error) {
	for {
		lease, err := d.localDirs.Acquire(path)
		if err == nil {
			return lease, nil
		}
		if !errors.Is(err, workspace.ErrLocalDirectoryBusy) {
			return nil, err
		}
		if err := sleepWithContext(ctx, taskMergeRetryDelay(d.cfg.PollInterval)); err != nil {
			return nil, err
		}
	}
}

func validTaskMergeClaim(claim TaskMergeClaim) bool {
	return validTaskMergeID(claim.JobID) && validTaskMergeID(claim.CompanyID) &&
		validTaskMergeID(claim.TaskID) && validTaskMergeID(claim.ConversationID) &&
		claim.RunID != uuid.Nil && claim.TaskExecutionGeneration > 0 && claim.Attempt > 0 &&
		len(claim.Context) != 0 && json.Valid(claim.Context)
}

func validTaskMergeResult(result TaskMergeResult) bool {
	if result.Attempt <= 0 {
		return false
	}
	switch result.Status {
	case TaskMergeStatusMerged:
		return result.ResultCode == "" && validTaskMergeCommit(result.MergedCommit)
	case TaskMergeStatusBlocked:
		return validTaskMergeResultCode(result.ResultCode) && result.MergedCommit == ""
	default:
		return false
	}
}

func validTaskMergeID(value string) bool {
	id, err := util.ParseGoUUID(value)
	return err == nil && id.String() == value
}

func validTaskMergeCommit(value string) bool {
	if value != strings.ToLower(value) || len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		isDigit := character >= '0' && character <= '9'
		isLowerHex := character >= 'a' && character <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}
	return true
}

func validTaskMergeResultCode(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if character != '_' && (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func blockedDaemonTaskMerge(code string, err error) (TaskMergeResult, error) {
	if err == nil {
		err = errors.New("task merge blocked")
	}
	return TaskMergeResult{Status: TaskMergeStatusBlocked, ResultCode: code}, err
}
