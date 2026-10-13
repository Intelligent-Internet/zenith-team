package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/text/unicode/norm"

	"boardly.dev/boardly/server/internal/daemon/execenv"
	"boardly.dev/boardly/server/internal/daemon/workspace"
	"boardly.dev/boardly/server/internal/runchannel"
	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/runfailure"
)

// isRunTerminal mirrors the server-side terminal statuses.
func isRunTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// shouldInterruptAgent decides whether the running agent should be cancelled
// based on the latest GetRunStatus call: a server-side terminal state, or a
// 404 "run not found" (the row was deleted mid-flight). Other errors do NOT
// interrupt — a flaky link must not kill an in-flight agent.
func shouldInterruptAgent(status string, err error) bool {
	if err != nil {
		return isRunNotFoundError(err)
	}
	return isRunTerminal(status)
}

// errRunHandedOff is the cause a Run's context is cancelled with when the
// server cancelled the Run for a confirmed handoff (ADR 0014). The Task's
// workspace generation moved on, so the Run's root is superseded rather than
// completed.
var errRunHandedOff = errors.New("run cancelled by a task handoff")

// watchRunCancellation polls the server for the run's status every
// pollInterval (nudged immediately by a reconcile broadcast) and returns a
// channel closed when the running agent should be interrupted, with the
// cause to interrupt it with: errRunHandedOff for a handoff, else nil. The
// cause is read only after the channel closes.
func (d *Daemon) watchRunCancellation(
	ctx context.Context,
	runID uuid.UUID,
	pollInterval time.Duration,
	runLog *slog.Logger,
) (<-chan struct{}, func() error) {
	cancelled := make(chan struct{})
	var cause error
	// Subscribe before launching the goroutine so a WS reconnect firing in
	// the gap is not dropped.
	var reconcileCh <-chan struct{}
	if d.reconcile != nil {
		reconcileCh = d.reconcile.notify()
	}
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		check := func() bool {
			status, err := d.client.GetRunStatus(ctx, runID)
			if !shouldInterruptAgent(status.Status, err) {
				return false
			}
			if err != nil {
				runLog.Info("run gone server-side, interrupting agent", "error", err)
			} else {
				runLog.Info(
					"run reached terminal state server-side, interrupting agent",
					"status",
					status.Status,
					"reason",
					status.Reason,
				)
			}
			if status.handedOff() {
				cause = errRunHandedOff
			}
			close(cancelled)
			return true
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-reconcileCh:
				if d.reconcile != nil {
					reconcileCh = d.reconcile.notify()
				}
				if check() {
					return
				}
			case <-ticker.C:
				if check() {
					return
				}
			}
		}
	}()
	return cancelled, func() error { return cause }
}

// decodeRunContext parses the claim's context document leniently: a missing
// or malformed document degrades to the zero value rather than failing the
// run.
func decodeRunContext(run Run, log *slog.Logger) RunContext {
	if len(run.Context) == 0 {
		return RunContext{}
	}
	var doc RunContext
	if err := json.Unmarshal(run.Context, &doc); err != nil {
		log.Warn("run context document malformed; proceeding with defaults", "error", err)
		return RunContext{}
	}
	return doc
}

// localDirectoryFor returns the local_directory resource pinned to this
// daemon, when the run carries one. A Task Run ignores it, and so does a
// tagged Run: a channel answer never draws on its person's local folders,
// which no Project access governs, so it never works in its person's own
// folder (Access TECH B7).
func localDirectoryFor(doc RunContext, daemonID string) *RunResourceContext {
	if doc.ExecutionProfile.Task != nil || isTaggedRun(doc) {
		return nil
	}
	for i := range doc.Resources {
		r := &doc.Resources[i]
		if r.ResourceType != "local_directory" {
			continue
		}
		if r.LocalDaemonID != "" && r.LocalDaemonID != daemonID {
			continue
		}
		if r.LocalPath == "" {
			continue
		}
		return r
	}
	return nil
}

func (d *Daemon) handleRun(ctx context.Context, run Run, slot int) {
	d.mu.Lock()
	rt, tracked := d.runtimeIndex[run.RuntimeID]
	d.mu.Unlock()
	// The runtime can go offline between the batch claim leaving with its ID
	// and the claimed run arriving here. Failing it back with runtime_offline
	// is what the server already retries on.
	if !tracked {
		d.logger.Warn("claimed run targets a runtime this daemon no longer hosts; failing it back for retry",
			"run", run.ID, "runtime_id", run.RuntimeID)
		if err := d.reportTerminalRun(ctx, terminalRunReport{
			kind:          terminalRunReportFail,
			runID:         run.ID,
			transcript:    RunTranscriptProof{},
			errorMessage:  "runtime went offline before the run started",
			failureReason: string(runfailure.ReasonRuntimeOffline),
		}); err != nil {
			d.logger.Error("fail run callback failed", "run", run.ID, "error", err)
		}
		return
	}
	provider := rt.Provider

	runLog := d.logger.With("run", run.ID)
	runLog.Info("picked run", "conversation", shortID(run.ConversationID), "provider", provider)

	runCtx, runCancel := context.WithCancelCause(ctx)
	defer runCancel(nil)

	pollInterval := d.cancelPollInterval
	if pollInterval == 0 {
		pollInterval = 5 * time.Second
	}
	cancelledByPoll, cancelCause := d.watchRunCancellation(runCtx, run.ID, pollInterval, runLog)
	go func() {
		select {
		case <-cancelledByPoll:
			runCancel(cancelCause())
		case <-runCtx.Done():
		}
	}()

	result, err := d.runner.run(runCtx, run, provider, slot, runLog)

	// Report usage before any early return — the agent accumulates tokens
	// whether the run completes, errors, or is cancelled mid-run. Detached
	// like the terminal report: daemon shutdown must not drop the
	// tokens-consumed facts.
	if len(result.Usage) > 0 {
		usageCtx, usageCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		if usageErr := d.client.ReportRunUsage(usageCtx, run.ID, result.Usage); usageErr != nil {
			runLog.Warn("report run usage failed", "error", usageErr)
		}
		usageCancel()
	}

	select {
	case <-cancelledByPoll:
		runLog.Info("run cancelled during execution, discarding result")
		if errors.Is(cancelCause(), errRunHandedOff) {
			// The Run may have finished, and completed its root, before
			// the poll's cause reached it.
			d.supersedeFinishedTaskRoot(result.TaskRoot, runLog)
		}
		// runner.run has returned, so the transcript flush is complete — tell
		// the server. Best-effort: the sweeper covers a lost ack.
		if ackErr := d.client.AckRunCancelled(ctx, run.ID); ackErr != nil {
			runLog.Warn("cancel ack failed; server sweeper will finalize", "error", ackErr)
		}
		return
	default:
	}

	if err != nil {
		errorMessage := err.Error()
		if phase, classified := runPreparationFailurePhase(err); classified {
			errorMessage = runPreparationDiagnosticEvent(err)
			runLog.Error(
				"run preparation failed",
				"error_code",
				errorMessage,
				"phase",
				string(phase),
			)
		} else {
			runLog.Error("run failed", "error", err)
		}
		if failErr := d.reportTerminalRun(ctx, terminalRunReport{
			kind:          terminalRunReportFail,
			runID:         run.ID,
			transcript:    result.Transcript,
			pendingItem:   result.PendingItem,
			errorMessage:  errorMessage,
			failureReason: runFailureReason(err),
		}); failErr != nil {
			runLog.Error("fail run callback failed", "error", failErr)
		}
		return
	}

	// Final pre-completion check: if the server already finalized or deleted
	// the run, skip reporting — the callbacks would fail anyway.
	if status, err := d.client.GetRunStatus(ctx, run.ID); shouldInterruptAgent(status.Status, err) {
		runLog.Info("run cancelled during execution, discarding result", "status", status.Status, "error", err)
		if status.handedOff() {
			// The agent finished just as a handoff landed: its root was
			// completed before the daemon could know.
			d.supersedeFinishedTaskRoot(result.TaskRoot, runLog)
		}
		if ackErr := d.client.AckRunCancelled(ctx, run.ID); ackErr != nil {
			runLog.Warn("cancel ack failed; server sweeper will finalize", "error", ackErr)
		}
		return
	}

	d.reportRunResult(ctx, run.ID, result, runLog)
}

func runFailureReason(err error) string {
	if errors.Is(err, errRunPrepareTimeout) {
		return string(runfailure.ReasonTimeout)
	}
	if errors.Is(err, errSkillBundleUnavailable) {
		return string(runfailure.ReasonSkillBundleUnavailable)
	}
	if errors.Is(err, errTranscriptDeliveryUnavailable) {
		// Retryable infrastructure: the work was healthy, the delivery lane
		// was not. Auto-retry resumes when the lane returns.
		return string(runfailure.ReasonTimeout)
	}
	if errors.Is(err, errConversationSnapshotInvalid) {
		return string(runfailure.ReasonRunContextInvalid)
	}
	if _, classified := runPreparationFailurePhase(err); classified {
		// Preparation diagnostics sent over the wire are deliberately reduced
		// to a bounded, PII-safe code. Classify from the private cause here so
		// replacing the raw message does not change retry policy.
		return runfailure.Classify(err.Error()).String()
	}
	return ""
}

// reportRunResult writes the final run disposition back to the server. Fail
// closed: only an explicit completed disposition is reported as success —
// anything else goes through FailRun so a run that never produced a real
// result can never render as completed. SessionID is forwarded on every path
// so the next turn resumes instead of forgetting the conversation. The
// workspace remains daemon-local.
func (d *Daemon) reportRunResult(ctx context.Context, runID uuid.UUID, result RunResult, runLog *slog.Logger) {
	switch result.Disposition {
	case RunDispositionCompleted:
		runLog.Info("run completed")
		err := d.reportTerminalRun(ctx, terminalRunReport{
			kind:                  terminalRunReportComplete,
			runID:                 runID,
			transcript:            result.Transcript,
			pendingItem:           result.PendingItem,
			sessionID:             result.SessionID,
			sessionRolloutMissing: result.SessionRolloutMissing,
			retiredSessionID:      result.RetiredSessionID,
		})
		if err == nil {
			return
		}
		// A transient error reaching here means the retry schedule was
		// exhausted while the upstream stayed unreachable. Converting that
		// into a fail would lose the agent's actual result — leave the run in
		// running so the server-side reaper can recover it. Only permanent
		// rejections fall back to fail.
		if isTransientError(err) {
			runLog.Error(
				"complete run failed after retries; leaving run in running rather than falling back to fail",
				"error",
				err,
			)
			return
		}
		// The fail callback proves the same transcript prefix the complete
		// just failed to prove, so a head reconciliation that did not converge
		// would only be refused again. Re-sending it is what turned bug R1
		// into a permanently wedged run; leave it to the reaper instead.
		if _, unreconciled := terminalHeadMismatch(err, runID); unreconciled {
			runLog.Error(
				"transcript head reconciliation did not converge; leaving run in running "+
					"rather than re-proving the same prefix through fail",
				"error",
				err,
			)
			return
		}
		runLog.Error("complete run rejected by server, falling back to fail", "error", err)
		fallbackErrMsg := fmt.Sprintf("complete run failed: %s", err.Error())
		if failErr := d.reportTerminalRun(ctx, terminalRunReport{
			kind:                  terminalRunReportFail,
			runID:                 runID,
			transcript:            result.Transcript,
			pendingItem:           result.PendingItem,
			errorMessage:          fallbackErrMsg,
			sessionID:             result.SessionID,
			sessionRolloutMissing: result.SessionRolloutMissing,
			retiredSessionID:      result.RetiredSessionID,
		}); failErr != nil {
			runLog.Error("fail run fallback also failed", "error", failErr)
		}
	default:
		failureReason := result.FailureReason
		if failureReason == "" && result.Disposition == RunDispositionCancelled {
			failureReason = string(runfailure.ReasonCancelled)
		}
		runLog.Info(
			"run did not complete, reporting failure",
			"disposition",
			result.Disposition,
			"failure_reason",
			failureReason,
		)
		if err := d.reportTerminalRun(ctx, terminalRunReport{
			kind:                  terminalRunReportFail,
			runID:                 runID,
			transcript:            result.Transcript,
			pendingItem:           result.PendingItem,
			errorMessage:          result.Comment,
			sessionID:             result.SessionID,
			failureReason:         failureReason,
			sessionRolloutMissing: result.SessionRolloutMissing,
			retiredSessionID:      result.RetiredSessionID,
		}); err != nil {
			runLog.Error("report failed run failed", "error", err)
		}
	}
}

// terminalHeadReconcileAttempts bounds how often a refused terminal
// transition is re-sent with a corrected proof. Only this daemon appends to
// this run, so adopting the server's head converges on the first try; a second
// covers an append that lands between the refusal and the retry. Past that the
// divergence is real, and looping on it would hold the run in 'running' just
// as surely as giving up did.
const terminalHeadReconcileAttempts = 2

// terminalItemReplayTimeout bounds the reconciliation replay of an
// unacknowledged item. Short on purpose: the run is over, and the lane that
// swallowed the first append is exactly the lane this rides — waiting out the
// full delivery window here would only delay the terminal transition.
const terminalItemReplayTimeout = 30 * time.Second

// reportTerminalRun is the only path that sends complete/fail callbacks. It
// preserves context values while discarding cancellation: daemon shutdown
// cancels the root context before the drain window, but terminal callbacks
// must still use that remaining window. Fail-closed mapping lives in
// reportRunResult; the retry schedule and 6m outer timeout live here.
//
// A refusal that names the run's real transcript head is not a failure: the
// proof is corrected from what the server reported and the transition is
// re-sent. Bug R1 was this loop's absence — one stale proof, refused twice,
// abandoned the run in 'running' with the agent's answer already durable.
func (d *Daemon) reportTerminalRun(parentCtx context.Context, report terminalRunReport) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parentCtx), 6*time.Minute)
	defer cancel()

	for attempt := 0; ; attempt++ {
		err := d.sendTerminalRun(ctx, report)
		if err == nil {
			return nil
		}
		head, reconcilable := terminalHeadMismatch(err, report.runID)
		if !reconcilable {
			return err
		}
		if attempt >= terminalHeadReconcileAttempts {
			return fmt.Errorf(
				"terminal transition still refused after %d head reconciliations: %w",
				attempt,
				err,
			)
		}
		report = d.reconcileTerminalHead(ctx, report, head)
	}
}

// reconcileTerminalHead rebuilds the proof from what the server said it holds.
// Nothing is invented here: either an item the daemon sent but never saw
// acknowledged is replayed — an exact replay returns the existing receipt
// through the (run_id, item_key) fence, so it is safe whether or not the first
// attempt committed — or the refusal's own head is adopted verbatim.
//
// The replay is what recovers FinalItemKey. Adopting the head alone would make
// the transition pass while the completion still nominated an older agent-text
// item as the run's answer, which the server refuses separately.
func (d *Daemon) reconcileTerminalHead(
	ctx context.Context,
	report terminalRunReport,
	head protocol.RunTerminalHeadMismatch,
) terminalRunReport {
	runLog := d.logger.With("run", report.runID)
	runLog.Warn(
		"terminal transition refused: adopting the server's transcript head",
		"proof_through_seq", report.transcript.ThroughSeq,
		"server_through_seq", head.ThroughSeq,
		"replaying_item", report.pendingItem != nil,
	)

	if pending := report.pendingItem; pending != nil {
		// One shot only: either the item is already committed and the replay
		// says so immediately, or the delivery lane is still down and waiting
		// again would not change that.
		report.pendingItem = nil
		if replayed, ok := d.replayPendingItem(ctx, report.transcript, *pending, runLog); ok {
			report.transcript = replayed
			return report
		}
	}

	report.transcript.ThroughSeq = head.ThroughSeq
	return report
}

// replayPendingItem re-sends the item whose receipt never arrived and rebuilds
// the head from the receipt that comes back. The receipt is preferred over the
// refusal's head because it is the only thing that also identifies the item —
// which is what a completion needs to nominate the run's final answer.
func (d *Daemon) replayPendingItem(
	ctx context.Context,
	head RunTranscriptProof,
	request protocol.RunTranscriptAppendRequest,
	runLog *slog.Logger,
) (RunTranscriptProof, bool) {
	replayCtx, cancel := context.WithTimeout(ctx, terminalItemReplayTimeout)
	defer cancel()
	receipt, err := d.appendTranscriptItem(replayCtx, request)
	if err != nil {
		runLog.Warn("could not replay the unacknowledged item",
			"item_key", request.ItemKey, "error", err)
		return head, false
	}
	if err := updateRunTranscriptHead(&head, request, receipt); err != nil {
		runLog.Warn("replayed item returned an unusable receipt",
			"item_key", request.ItemKey, "error", err)
		return head, false
	}
	runLog.Info("unacknowledged item replayed; proof rebuilt from its receipt",
		"item_key", request.ItemKey, "through_seq", head.ThroughSeq)
	return head, true
}

// sendTerminalRun issues exactly one complete/fail callback.
func (d *Daemon) sendTerminalRun(ctx context.Context, report terminalRunReport) error {
	switch report.kind {
	case terminalRunReportComplete:
		return d.client.CompleteRun(
			ctx,
			report.runID,
			report.transcript,
			report.sessionID,
			report.sessionRolloutMissing,
			report.retiredSessionID,
		)
	case terminalRunReportFail:
		return d.client.FailRun(
			ctx,
			report.runID,
			report.transcript,
			report.errorMessage,
			report.sessionID,
			report.failureReason,
			report.sessionRolloutMissing,
			report.retiredSessionID,
		)
	default:
		return fmt.Errorf("unsupported terminal run report kind %d", report.kind)
	}
}

// ---------------------------------------------------------------------------
// Skill bundles
// ---------------------------------------------------------------------------

const (
	skillBundleResolveMinTimeout = 30 * time.Second
	skillBundleResolveMaxTimeout = 5 * time.Minute
)

// skillBundleResolveTimeout scales the per-skill resolve deadline to bundle
// size, floored and capped.
func skillBundleResolveTimeout(sizeBytes int64) time.Duration {
	if sizeBytes <= 0 {
		return skillBundleResolveMinTimeout
	}
	scaled := time.Duration(sizeBytes/50*1024) * time.Second
	if scaled < skillBundleResolveMinTimeout {
		return skillBundleResolveMinTimeout
	}
	if scaled > skillBundleResolveMaxTimeout {
		return skillBundleResolveMaxTimeout
	}
	return scaled
}

func skillRefKey(source, id string) string {
	return source + "\x00" + id
}

// ensureRunSkillBundles resolves the run's skill refs — cache first, then one
// server request per miss, caching each the moment it arrives so even a
// failed dispatch leaves the fetched skills cached for the next one.
func (d *Daemon) ensureRunSkillBundles(ctx context.Context, run Run, doc *RunContext) error {
	if len(doc.Agent.SkillRefs) == 0 {
		return nil
	}
	resolved := make(map[string]SkillData, len(doc.Agent.SkillRefs))
	for _, bundle := range doc.Agent.Skills {
		resolved[skillRefKey(bundle.Source, bundle.ID)] = bundle
	}
	misses := make([]SkillRefData, 0)
	for _, ref := range doc.Agent.SkillRefs {
		if _, ok := resolved[skillRefKey(ref.Source, ref.ID)]; ok {
			continue
		}
		var bundle SkillData
		if err := d.skillCache.WithRefLock(run.CompanyID, ref, func() error {
			if cached, ok := d.skillCache.Load(run.CompanyID, ref); ok {
				bundle = cached
			}
			return nil
		}); err != nil {
			return fmt.Errorf("load skill bundle cache: %w", err)
		}
		if bundle.ID != "" {
			resolved[skillRefKey(ref.Source, ref.ID)] = bundle
			continue
		}
		misses = append(misses, ref)
	}

	for _, ref := range misses {
		started := time.Now()
		bundle, err := d.resolveSkillBundle(ctx, run, ref)
		if err != nil {
			// Name the skill, its declared size, and how long we waited so a
			// dead link is distinguishable from a too-big bundle.
			return fmt.Errorf("%w: skill %q (id=%s, %d bytes) after %s: %w",
				errSkillBundleUnavailable, ref.Name, ref.ID, ref.SizeBytes,
				time.Since(started).Round(time.Millisecond), err)
		}
		resolved[skillRefKey(bundle.Source, bundle.ID)] = bundle
	}

	skills := make([]SkillData, 0, len(doc.Agent.SkillRefs))
	for _, ref := range doc.Agent.SkillRefs {
		bundle, ok := resolved[skillRefKey(ref.Source, ref.ID)]
		if !ok || !validateSkillBundle(ref, bundle) {
			return fmt.Errorf(
				"skill bundle missing or invalid after resolve: skill_id=%s source=%s hash=%s",
				ref.ID,
				ref.Source,
				ref.Hash,
			)
		}
		skills = append(skills, bundle)
	}
	doc.Agent.Skills = skills
	return nil
}

// resolveSkillBundle downloads one skill bundle under its own size-scaled
// deadline and writes it to the on-disk cache before returning.
func (d *Daemon) resolveSkillBundle(ctx context.Context, run Run, ref SkillRefData) (SkillData, error) {
	reqCtx, cancel := context.WithTimeout(ctx, skillBundleResolveTimeout(ref.SizeBytes))
	defer cancel()

	bundle, err := d.client.ResolveSkillBundle(reqCtx, run.RuntimeID, run.ID, ref)
	if err != nil {
		return SkillData{}, err
	}
	if bundle.Source != ref.Source || bundle.ID != ref.ID {
		return SkillData{}, fmt.Errorf(
			"resolve skill bundle returned wrong skill: requested source=%s id=%s, got source=%s id=%s",
			ref.Source,
			ref.ID,
			bundle.Source,
			bundle.ID,
		)
	}
	if !validateSkillBundle(ref, bundle) {
		return SkillData{}, fmt.Errorf(
			"resolve skill bundle returned invalid bundle: skill_id=%s source=%s hash=%s",
			bundle.ID,
			bundle.Source,
			bundle.Hash,
		)
	}
	if err := d.skillCache.WithRefLock(run.CompanyID, ref, func() error {
		return d.skillCache.Store(run.CompanyID, bundle)
	}); err != nil {
		return SkillData{}, fmt.Errorf("store skill bundle cache: %w", err)
	}
	return bundle, nil
}

// ---------------------------------------------------------------------------
// Prepare lease
// ---------------------------------------------------------------------------

func (d *Daemon) startRunPrepareLeaseExtender(ctx context.Context, run Run, runLog *slog.Logger) func() {
	leaseCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(runPrepareLeaseRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				reqCtx, reqCancel := context.WithTimeout(leaseCtx, runPrepareLeaseTimeout)
				err := d.client.ExtendRunPrepareLease(reqCtx, run.RuntimeID, run.ID)
				reqCancel()
				if err != nil {
					runLog.Warn("extend run prepare lease failed", "error", err)
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}

func (d *Daemon) effectiveRunPrepareTimeout() time.Duration {
	if d.runPrepareTimeout > 0 {
		return d.runPrepareTimeout
	}
	return defaultRunPrepareTimeout
}

// ---------------------------------------------------------------------------
// Prompt
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// Environment helpers
// ---------------------------------------------------------------------------

// isBlockedEnvKey returns true if the key must never be overridden in an
// agent child environment — daemon-internal variables and critical system
// paths. The BOARDLY_ prefix block is what makes the codex-memory and
// multi-agent escape hatches operator-only.
func isBlockedEnvKey(key string) bool {
	upper := strings.ToUpper(key)
	if strings.HasPrefix(upper, "BOARDLY_") {
		return true
	}
	switch upper {
	case "HOME", "PATH", "USER", "SHELL", "TERM", "TMPDIR", "TMP", "TEMP", "CODEX_HOME":
		return true
	}
	return false
}

// configureCodexRunShellEnvironment writes the managed shell policy after the
// run environment is fully assembled, so the allowlist reflects the child
// environment that actually launches. Failure is fatal: launching with an
// unowned policy could drop the run-scoped token or expose inherited daemon
// credentials.
func configureCodexRunShellEnvironment(
	providerName, codexHome string,
	inherited []string,
	agentEnv map[string]string,
	logger *slog.Logger,
) error {
	if providerName != "codex" {
		return nil
	}
	if strings.TrimSpace(codexHome) == "" {
		return errors.New("configure codex shell environment: run CODEX_HOME is missing")
	}
	includeOnly := execenv.CodexShellEnvAllowlist(inherited, agentEnv, nil)
	configPath := filepath.Join(codexHome, "config.toml")
	if err := execenv.EnsureCodexShellEnvPolicyConfig(configPath, includeOnly, logger); err != nil {
		return fmt.Errorf("configure codex shell environment: %w", err)
	}
	return nil
}

type commerceDevBootstrapKey [sha256.Size]byte

type taskCommerceDevBootstrap struct {
	companyID string
	runID     uuid.UUID
	taskID    string
	authToken string
	ctx       context.Context
	cancel    context.CancelFunc
	inflight  sync.WaitGroup
}

type commerceDevBootstrapAuthority struct {
	scoped       bool
	companyID    string
	runID        uuid.UUID
	taskID       string
	runAuthToken string
	bootstrapKey commerceDevBootstrapKey
	ctx          context.Context
}

type runCLIEnvironment struct {
	boardlyHome string
	// channel reports that run.json is in boardlyHome: the Run channel is
	// open, so zen and boardly-mcp can reach the server through it.
	channel bool
	// outputChannel is the Run channel when it can also complete the
	// present_output MCP tool (the Run has an output presenter), else nil.
	outputChannel       *runchannel.RunControlConfig
	executableDirectory string
	// commands lists the zen commands the Run channel admits; the system
	// prompt advertises exactly these.
	commands []string
	cleanup  func() error
}

// channelHome is the isolated home when it holds run.json, else "".
func (e runCLIEnvironment) channelHome() string {
	if !e.channel {
		return ""
	}
	return e.boardlyHome
}

func (d *Daemon) registerTaskCommerceBootstrap(run Run) (string, func() error, error) {
	if run.ID == uuid.Nil || !validCommerceCompanyID(run.CompanyID) || !validCommerceCompanyID(run.TaskID) {
		return "", nil, errors.New("task commerce bootstrap run identity is invalid")
	}
	runAuthToken, err := runScopedAuthToken(run)
	if err != nil {
		return "", nil, err
	}
	capability, err := newCommerceDevCapability()
	if err != nil {
		return "", nil, err
	}
	key := commerceDevBootstrapKey(sha256.Sum256([]byte(capability)))
	issuanceCtx, cancelIssuance := context.WithCancel(context.Background())
	binding := &taskCommerceDevBootstrap{
		companyID: run.CompanyID,
		runID:     run.ID,
		taskID:    run.TaskID,
		authToken: runAuthToken,
		ctx:       issuanceCtx,
		cancel:    cancelIssuance,
	}
	d.commerceDevBootstrapMu.Lock()
	if d.taskCommerceDevBootstraps == nil {
		d.taskCommerceDevBootstraps = make(map[commerceDevBootstrapKey]*taskCommerceDevBootstrap)
	}
	if _, collision := d.taskCommerceDevBootstraps[key]; collision {
		d.commerceDevBootstrapMu.Unlock()
		cancelIssuance()
		return "", nil, errors.New("task commerce bootstrap capability collision")
	}
	d.taskCommerceDevBootstraps[key] = binding
	d.commerceDevBootstrapMu.Unlock()
	var cleanupOnce sync.Once
	return capability, func() error {
		cleanupOnce.Do(func() {
			d.commerceDevBootstrapMu.Lock()
			if d.taskCommerceDevBootstraps[key] == binding {
				delete(d.taskCommerceDevBootstraps, key)
			}
			d.commerceDevBootstrapMu.Unlock()
			binding.cancel()
			binding.inflight.Wait()
			binding.authToken = ""
			if d.commerceDev != nil {
				d.commerceDev.revokeBootstrapLeases(key)
			}
		})
		return nil
	}, nil
}

func (d *Daemon) commerceDevBootstrapAuthorization(
	header string,
) (commerceDevBootstrapAuthority, func(), bool) {
	capability, ok := parseCommerceDevAuthorization(header, "BoardlyCommerceTask")
	if !ok || !validCommerceDevCapability(capability) {
		return commerceDevBootstrapAuthority{}, nil, false
	}
	key := commerceDevBootstrapKey(sha256.Sum256([]byte(capability)))
	d.commerceDevBootstrapMu.Lock()
	binding, ok := d.taskCommerceDevBootstraps[key]
	if !ok {
		d.commerceDevBootstrapMu.Unlock()
		return commerceDevBootstrapAuthority{}, nil, false
	}
	binding.inflight.Add(1)
	d.commerceDevBootstrapMu.Unlock()
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(binding.inflight.Done)
	}
	return commerceDevBootstrapAuthority{
		scoped:       true,
		companyID:    binding.companyID,
		runID:        binding.runID,
		taskID:       binding.taskID,
		runAuthToken: binding.authToken,
		bootstrapKey: key,
		ctx:          binding.ctx,
	}, release, true
}

func boardlyExecutableName() string {
	if runtime.GOOS == "windows" {
		return "zen.exe"
	}
	return "zen"
}

func pinBoardlyExecutable(sourcePath string) (*os.File, func() error, error) {
	directory, err := os.MkdirTemp("", "boardly-daemon-cli-")
	if err != nil {
		return nil, nil, fmt.Errorf("create pinned boardly executable directory: %w", err)
	}
	removeDirectory := func() error {
		if err := os.RemoveAll(directory); err != nil {
			return fmt.Errorf("remove pinned boardly executable directory: %w", err)
		}
		return nil
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("open boardly executable: %w", err),
			removeDirectory(),
		)
	}
	if err := copyBoardlyExecutable(source, directory); err != nil {
		closeErr := source.Close()
		if closeErr != nil {
			closeErr = fmt.Errorf("close boardly executable: %w", closeErr)
		}
		return nil, nil, errors.Join(err, closeErr, removeDirectory())
	}
	if err := source.Close(); err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("close boardly executable: %w", err),
			removeDirectory(),
		)
	}
	pinned, err := os.Open(filepath.Join(directory, boardlyExecutableName()))
	if err != nil {
		return nil, nil, errors.Join(
			fmt.Errorf("open pinned boardly executable: %w", err),
			removeDirectory(),
		)
	}
	var (
		cleanupOnce sync.Once
		cleanupErr  error
	)
	cleanup := func() error {
		cleanupOnce.Do(func() {
			var cleanupErrors []error
			if err := pinned.Close(); err != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("close pinned boardly executable: %w", err),
				)
			}
			if err := removeDirectory(); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
			cleanupErr = errors.Join(cleanupErrors...)
		})
		return cleanupErr
	}
	return pinned, cleanup, nil
}

func copyBoardlyExecutable(source *os.File, directory string) error {
	if source == nil {
		return errors.New("pinned boardly executable is unavailable")
	}
	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat boardly executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("boardly executable is not a regular file")
	}
	destination := filepath.Join(directory, boardlyExecutableName())
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o500)
	if err != nil {
		return fmt.Errorf("create run-local boardly executable: %w", err)
	}
	failed := true
	defer func() {
		if failed {
			_ = output.Close()
			_ = os.Remove(destination)
		}
	}()
	copied, err := io.Copy(output, io.NewSectionReader(source, 0, info.Size()))
	if err != nil {
		return fmt.Errorf("copy run-local boardly executable: %w", err)
	}
	if copied != info.Size() {
		return fmt.Errorf(
			"copy run-local boardly executable: copied %d bytes, want %d",
			copied,
			info.Size(),
		)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync run-local boardly executable: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close run-local boardly executable: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(destination, 0o500); err != nil {
			return fmt.Errorf("secure run-local boardly executable: %w", err)
		}
	}
	failed = false
	return nil
}

// prepareRunCLIEnvironment prevents every provider process from falling back
// to the operator's normal Zenith Team home. Every attended Run (Task or
// direct Conversation) additionally receives one exact zen binary on PATH and
// a run.json that binds that binary to this Run's channel; Task runs also
// receive the run-scoped Commerce bootstrap when the local control endpoint
// is available. Scheduled Runs get the empty home and nothing else. launch
// is the Run's own folders and hidden folders, as its sandbox gets them: the
// channel's output presenter reads for the Run only what they allow.
func (d *Daemon) prepareRunCLIEnvironment(
	run Run,
	doc RunContext,
	envRoot string,
	workDir string,
	launch execenv.SeatbeltOptions,
) (runCLIEnvironment, error) {
	// The sandbox denies every Run's home by this name and opens only the
	// Run's own (Access TECH B3e).
	home, err := os.MkdirTemp(envRoot, execenv.RunHomePrefix)
	if err != nil {
		return runCLIEnvironment{}, fmt.Errorf("create isolated boardly home: %w", err)
	}
	var (
		executableDirectory string
		revokeBootstrap     func() error
		revokeChannel       func() error
	)
	prepared := runCLIEnvironment{boardlyHome: home}
	prepared.cleanup = func() error {
		var cleanupErrors []error
		if revokeChannel != nil {
			cleanupErrors = append(cleanupErrors, revokeChannel())
		}
		if revokeBootstrap != nil {
			cleanupErrors = append(cleanupErrors, revokeBootstrap())
		}
		if executableDirectory != "" {
			if err := os.RemoveAll(executableDirectory); err != nil {
				cleanupErrors = append(
					cleanupErrors,
					fmt.Errorf("remove run-local boardly executable: %w", err),
				)
			}
		}
		if err := os.RemoveAll(home); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove isolated boardly home: %w", err))
		}
		return errors.Join(cleanupErrors...)
	}
	fail := func(preparationErr error) (runCLIEnvironment, error) {
		return runCLIEnvironment{}, errors.Join(preparationErr, prepared.cleanup())
	}
	if len(runCLICommands(run, doc)) == 0 {
		return prepared, nil
	}
	executableDirectory, err = os.MkdirTemp(envRoot, ".boardly-bin-")
	if err != nil {
		return fail(fmt.Errorf("create run-local zen executable directory: %w", err))
	}
	prepared.executableDirectory = executableDirectory
	if err := copyBoardlyExecutable(d.boardlyExecutable, executableDirectory); err != nil {
		return fail(err)
	}
	if runchannel.ValidEndpoint(d.localPlaneEndpoint) {
		runToken, err := runScopedAuthToken(run)
		if err != nil {
			return fail(err)
		}
		presenter := d.newRunOutputPresenter(run, doc, runToken, workDir, launch)
		control, revoke, err := d.registerRunChannel(
			run, doc, runToken,
			runChannelWorkspace{present: presenter, path: workDir, guard: runFileGuard(launch)},
		)
		if err != nil {
			return fail(err)
		}
		revokeChannel = revoke
		prepared.commands = control.Commands
		if presenter != nil {
			prepared.outputChannel = &control
		}
		if _, err := writeRunControlConfig(home, control); err != nil {
			return fail(fmt.Errorf("write run control configuration: %w", err))
		}
		prepared.channel = true
	} else if d.logger != nil {
		d.logger.Debug("Run channel unavailable: local control endpoint not bound")
	}
	if doc.ExecutionProfile.Task == nil {
		return prepared, nil
	}
	if !runchannel.ValidEndpoint(d.commerceDevEndpoint) {
		if d.logger != nil {
			d.logger.Debug("Task Commerce control unavailable")
		}
		return prepared, nil
	}
	capability, revoke, err := d.registerTaskCommerceBootstrap(run)
	if err != nil {
		return fail(err)
	}
	revokeBootstrap = revoke
	if _, err := writeCommerceDevControlConfig(home, d.commerceDevEndpoint, capability); err != nil {
		return fail(fmt.Errorf("write task commerce cli home: %w", err))
	}
	return prepared, nil
}

func pathWithRunCLI(pathValue, executableDirectory string) string {
	if pathValue == "" {
		return executableDirectory
	}
	return executableDirectory + string(os.PathListSeparator) + pathValue
}

// boardlyMcpConfig merges the agent's saved MCP config with the daemon's own
// boardly-mcp stdio server entry (when BOARDLY_MCP_EXECUTABLE is set).
// boardly-mcp gets the Run's isolated BOARDLY_HOME, whose run.json opens the
// Run channel, and no credential: neither the person's bearer, nor the path
// to it, nor the Run token. The daemon adds the session and the proof to
// every call it forwards (Access TECH B3b). channelHome is that isolated
// home when it holds run.json, and empty when the Run has no channel; then
// boardly-mcp could not reach the server, so no boardly server entry is added
// at all. outputChannel is that same channel when it can complete
// present_output, artifact_inspect and artifact_fetch, the calls the daemon
// completes itself; without it those tools are not offered.
func (d *Daemon) boardlyMcpConfig(
	agentConfig json.RawMessage,
	run Run,
	allowedTools []string,
	snapshotDir string,
	planRoot string,
	channelHome string,
	outputChannel *runchannel.RunControlConfig,
) json.RawMessage {
	if outputChannel == nil {
		allowedTools = slices.DeleteFunc(
			slices.Clone(allowedTools),
			func(name string) bool {
				return name == "present_output" || name == "artifact_inspect" || name == "artifact_fetch"
			},
		)
	}
	if d.cfg.MCPExecutable == "" || len(allowedTools) == 0 {
		return agentConfig
	}
	if channelHome == "" {
		if d.logger != nil {
			d.logger.Warn(
				"Run channel unavailable; boardly-mcp is not configured for this Run",
				"run",
				run.ID,
			)
		}
		return agentConfig
	}
	servers := map[string]any{}
	if len(agentConfig) > 0 {
		var doc struct {
			McpServers map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(agentConfig, &doc); err == nil {
			for name, server := range doc.McpServers {
				servers[name] = server
			}
		} else {
			// The agent runs anyway, but without the MCP servers its operator
			// explicitly configured — that must never happen silently.
			slog.Warn("agent MCP config unparseable; running with the boardly server only",
				"error", err)
		}
	}
	env := map[string]string{
		"BOARDLY_HOME":                channelHome,
		"BOARDLY_COMPANY_ID":          run.CompanyID,
		"BOARDLY_MCP_COMPANY_ID":      run.CompanyID,
		"BOARDLY_MCP_CONVERSATION_ID": run.ConversationID,
		"BOARDLY_MCP_TASK_ID":         run.TaskID,
		"BOARDLY_MCP_RUN_ID":          run.ID.String(),
		"BOARDLY_MCP_AGENT_ID":        run.AgentID,
		"BOARDLY_MCP_ALLOWED_TOOLS":   strings.Join(allowedTools, ","),
	}
	// present_output, artifact_inspect and artifact_fetch ride the Run
	// channel boardly-mcp already opens through run.json in channelHome, so
	// neither its endpoint nor its capability travels in the environment
	// (Access TECH B3b).
	if snapshotDir != "" {
		env["BOARDLY_SKILL_SNAPSHOT_DIR"] = snapshotDir
	}
	if planRoot != "" {
		env["BOARDLY_MCP_PLAN_ROOT"] = planRoot
	}
	servers[boardlyMCPServerName] = map[string]any{
		"command": d.cfg.MCPExecutable,
		"args":    []string{},
		"env":     env,
	}
	merged, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		// Returning the raw agent config drops the boardly server entry — loud,
		// because the agent then cannot call back as its run.
		slog.Warn("merge MCP config failed; boardly-mcp entry dropped", "error", err)
		return agentConfig
	}
	return merged
}

func mergeRunMCPConfig(
	agentConfig json.RawMessage,
	connectorServers map[string]json.RawMessage,
) (json.RawMessage, error) {
	if len(connectorServers) == 0 {
		return agentConfig, nil
	}
	servers := cloneRawMessages(connectorServers)
	if len(agentConfig) != 0 {
		var document struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal(agentConfig, &document); err != nil || document.Servers == nil {
			return nil, errors.New("agent MCP configuration is invalid")
		}
		for name, config := range document.Servers {
			if _, duplicate := servers[name]; duplicate {
				return nil, fmt.Errorf(
					"agent MCP configuration duplicates selected connector server %q",
					name,
				)
			}
			servers[name] = append(json.RawMessage(nil), config...)
		}
	}
	encoded, err := json.Marshal(struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}{Servers: servers})
	if err != nil {
		return nil, fmt.Errorf("encode run MCP configuration: %w", err)
	}
	return encoded, nil
}

// runAgentEnvironment builds the extra environment for the agent process.
func runAgentEnvironment(run Run, agentName, token, serverURL string, healthPort, slot int) map[string]string {
	return map[string]string{
		"BOARDLY_RUN_TOKEN":   token,
		"BOARDLY_SERVER_URL":  serverURL,
		"BOARDLY_DAEMON_PORT": strconv.Itoa(healthPort),
		"BOARDLY_COMPANY_ID":  run.CompanyID,
		"BOARDLY_AGENT_NAME":  agentName,
		"BOARDLY_AGENT_ID":    run.AgentID,
		"BOARDLY_RUN_ID":      run.ID.String(),
		"BOARDLY_RUN_SLOT":    strconv.Itoa(slot),
	}
}

// doRun executes one claimed run end to end: prepare the workspace and the
// provider execution environment, spawn the provider, consume its ordered
// event stream, and report the terminal state to the server. It is the
// production implementation behind d.runner, which tests replace to avoid
// spawning real agent processes.
func (d *Daemon) doRun(
	ctx context.Context,
	run Run,
	providerName string,
	slot int,
	runLog *slog.Logger,
) (runResult RunResult, returnErr error) {
	if run.CompanyID == "" {
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseExecution,
			fmt.Errorf("refusing to spawn agent: run has no company_id (run_id=%s)", run.ID),
		)
	}
	if run.ConversationID == "" {
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseExecution,
			fmt.Errorf(
				"refusing to spawn agent: run has no conversation_id (run_id=%s)",
				run.ID,
			),
		)
	}

	prepareTimeout := d.effectiveRunPrepareTimeout()
	prepareCtx, cancelPrepare := context.WithTimeoutCause(ctx, prepareTimeout, errRunPrepareTimeout)
	prepareComplete := false
	defer func() {
		cancelPrepare()
		if prepareComplete || returnErr == nil || !errors.Is(context.Cause(prepareCtx), errRunPrepareTimeout) {
			return
		}
		// Collapse every deadline shape into one sentinel handleRun can
		// classify as a retryable platform timeout while retaining the bounded
		// phase that tells operators which preparation step stalled.
		runResult = RunResult{}
		phase, classified := runPreparationFailurePhase(returnErr)
		if !classified {
			phase = runPreparationPhasePrepare
		}
		returnErr = wrapRunPreparationError(
			phase,
			fmt.Errorf("%w after %s", errRunPrepareTimeout, prepareTimeout),
		)
	}()

	entry, ok := d.agents()[providerName]
	if !ok {
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseProviderAvailability,
			fmt.Errorf("no agent configured for provider %q", providerName),
		)
	}
	if !agentExecutablePresent(entry.Path) {
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseProviderAvailability,
			fmt.Errorf("agent executable vanished: %s", entry.Path),
		)
	}

	// On the outer ctx, not the prepare deadline: the extender must survive a
	// local-directory park longer than the prepare budget, or the server's
	// lease sweep fails the parked run. The deferred stop bounds it.
	stopPrepareLease := d.startRunPrepareLeaseExtender(ctx, run, runLog)
	defer stopPrepareLease()

	doc := decodeRunContext(run, runLog)
	if err := validateDocumentRunTools(doc, d.cfg.MCPExecutable); err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseProviderAvailability, err)
	}
	if isDocumentRun(doc) {
		isolateDocumentRun(&run, &doc)
	}
	if err := checkAgentCapabilityRuntime(doc, run.RuntimeID); err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseConnector, err)
	}
	scheduled := isScheduledRun(doc)
	restrictedTools := runToolPolicy(doc) != ""
	if scheduled {
		isolateScheduledRun(&run, &doc)
	} else if !restrictedTools {
		if err := d.ensureRunSkillBundles(prepareCtx, run, &doc); err != nil {
			return RunResult{}, wrapRunPreparationError(runPreparationPhaseSkillSnapshot, err)
		}
	}
	if isTaggedRun(doc) {
		// A tagged Run starts a new provider session on purpose (Access TECH
		// B7). Its new folder has none to resume, and a resumed session would
		// carry what earlier Runs of the chat read, perhaps for another person
		// who tagged the Agent there, into this answer. The prompt carries the
		// chat's frozen history instead, with no notice of a lost session.
		run.PriorSessionID = ""
		run.PriorSessionThroughSeq = 0
	}
	// Validated before any workspace or filesystem work: a Run whose frozen
	// history the daemon cannot trust must not launch at all.
	snapshot, err := decodeConversationSnapshot(run, doc)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhasePrepare, err)
	}
	doc = applyAgentCapabilityDenials(doc)
	connectorApps, err := connectorAppsForRun(doc.ConnectorSnapshot, providerName)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseConnector,
			fmt.Errorf("resolve run connector apps: %w", err),
		)
	}
	var connectorState connectorExecution
	if doc.ConnectorSnapshot != nil {
		if d.connectors == nil {
			return RunResult{}, wrapRunPreparationError(
				runPreparationPhaseConnector,
				errors.New("resolve run connectors: connector service is unavailable"),
			)
		}
		connectorState, err = d.connectors.resolveRunExecution(
			prepareCtx,
			run.CompanyID,
			run.RuntimeID,
			providerName,
			doc.ConnectorSnapshot,
		)
		if err != nil {
			return RunResult{}, wrapRunPreparationError(
				runPreparationPhaseConnector,
				fmt.Errorf("resolve run connectors: %w", err),
			)
		}
	}
	if err := requireResearchWikiTools(doc); err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhasePrepare, err)
	}
	agentMCPConfig := doc.Agent.McpConfig
	if restrictedTools {
		agentMCPConfig = nil
	}
	managedMCPConfig, err := mergeRunMCPConfig(agentMCPConfig, connectorState.MCPServers)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseConnector,
			fmt.Errorf("resolve run connectors: %w", err),
		)
	}

	agentName := doc.Agent.Name
	if agentName == "" {
		agentName = "agent"
	}

	// Local-directory runs execute in place under the in-process path lock;
	// direct conversation runs execute in the durable Company main workspace;
	// task, scheduled and tagged Runs use the managed workspace selected by
	// their frozen profile.
	var (
		envRoot  string
		workDir  string
		localDir bool
		// gitDir is a Task worktree's own folder in its Project main's
		// repository, the one place there its Run writes (Access TECH B3f).
		gitDir string
	)
	if resource := localDirectoryFor(doc, d.cfg.DaemonID); !restrictedTools && resource != nil {
		lease, parked, err := d.acquireLocalDirectory(ctx, run, resource.LocalPath, runLog)
		if err != nil {
			return RunResult{}, wrapRunPreparationError(runPreparationPhaseWorkspace, err)
		}
		defer lease.Release()
		if parked {
			// The wait consumed wall-clock, not preparation budget. Restart
			// the deadline so the steps after the park keep their full
			// liveness bound.
			cancelPrepare()
			prepareCtx, cancelPrepare = context.WithTimeoutCause(ctx, prepareTimeout, errRunPrepareTimeout)
			defer cancelPrepare()
		}
		workDir = lease.Path()
		localDir = true
		envRoot, err = d.ensureRunScratchEnvRoot(run.ID)
		if err != nil {
			return RunResult{}, wrapRunPreparationError(runPreparationPhaseWorkspace, err)
		}
	} else if runUsesMainWorkspace(doc) {
		mainDir, lease, parked, err := d.acquireMainRunWorkspace(ctx, prepareCtx, run, doc, runLog)
		if err != nil {
			return RunResult{}, wrapRunPreparationError(runPreparationPhaseWorkspace, err)
		}
		defer lease.Release()
		if parked {
			// Same deadline restart as the local-directory park above.
			cancelPrepare()
			prepareCtx, cancelPrepare = context.WithTimeoutCause(ctx, prepareTimeout, errRunPrepareTimeout)
			defer cancelPrepare()
		}
		workDir = mainDir
		// Execute-in-place semantics: the main workspace is a durable
		// directory the user browses, so provider state stays in the scratch
		// env root and Claude skills are never materialized into it.
		localDir = true
		envRoot, err = d.ensureRunScratchEnvRoot(run.ID)
		if err != nil {
			return RunResult{}, wrapRunPreparationError(runPreparationPhaseWorkspace, err)
		}
	} else {
		acquired, err := d.acquireManagedRunWorkspace(prepareCtx, run, doc)
		if err != nil {
			return RunResult{}, wrapRunPreparationError(
				runPreparationPhaseWorkspace,
				fmt.Errorf("acquire conversation workspace: %w", err),
			)
		}
		envRoot = acquired.Root
		workDir = acquired.WorkDir
		gitDir = acquired.GitDir
		taskRoot := !scheduled && doc.ExecutionProfile.Task != nil
		defer func() {
			// Only a clean completion goes unchecked here: the final check in
			// handleRun sees a handoff that lands after it.
			checkHandoff := taskRoot && (returnErr != nil ||
				runResult.Disposition != RunDispositionCompleted || ctx.Err() != nil)
			if err := d.finishRunWorkspace(ctx, run.ID, acquired.Root, checkHandoff); err != nil {
				runLog.Warn("mark workspace finished failed", "error", err)
			}
			if taskRoot {
				runResult.TaskRoot = acquired.Root
			}
		}()
	}
	if localDir {
		// The signed receipt is the one record Task Review and generated-file
		// access resolve this Run by, in this process and after a restart.
		identity := workspace.InPlaceRunIdentity{
			CompanyID: run.CompanyID, ConversationID: run.ConversationID, TaskID: run.TaskID,
		}
		if err := d.workspaces.RecordInPlaceRunStart(prepareCtx, run.ID, identity, workDir); err != nil {
			runLog.Warn("record in-place run start failed; task review will be unavailable", "error", err)
		} else {
			defer func() {
				completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
				defer cancel()
				if err := d.workspaces.CompleteInPlaceRun(completeCtx, run.ID); err != nil {
					runLog.Warn("record in-place run completion failed", "error", err)
				}
			}()
		}
	}

	companyContext, err := d.companyRunContext(prepareCtx, run, doc, workDir)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseWorkspace, err)
	}
	if doc.Company != nil {
		doc.Company.BusinessContextPath = companyContext.BusinessContextPath
	}
	// Founder-supplied documents are input to the business context, not
	// Company content: only the Agent that builds that context receives them,
	// and only for the length of its Run.
	if doc.Agent.IsRoot && doc.ExecutionProfile.Task == nil {
		doc.CompanyReferences = d.placeCompanyReferences(prepareCtx, run, doc, envRoot, runLog)
	} else {
		doc.CompanyReferences = nil
	}

	if len(doc.ProjectDocuments) > 0 && doc.ExecutionProfile.Task != nil {
		d.placeProjectDocuments(prepareCtx, run, &doc, envRoot, runLog)
		companyContext.ReadDirs = append(companyContext.ReadDirs, doc.ProjectDocumentRoot)
	}

	if doc.ChannelInvocation != nil && len(doc.ChannelAttachments) > 0 {
		doc.ChannelAttachmentRoot = filepath.Join(envRoot, "channel-attachments")
		requested := doc.ChannelAttachments
		doc.ChannelAttachments = d.materializeAttachedFiles(
			prepareCtx,
			run,
			requested,
			doc.ChannelAttachmentRoot,
			runLog,
		)
		companyContext.ReadDirs = append(companyContext.ReadDirs, doc.ChannelAttachmentRoot)
		doc.UnavailableChannelAttachments = unavailableAttachedFiles(requested, doc.ChannelAttachments)
	}
	d.placeTaskAttachments(prepareCtx, run, &doc, envRoot, runLog)
	if doc.TaskAttachmentRoot != "" {
		companyContext.ReadDirs = append(companyContext.ReadDirs, doc.TaskAttachmentRoot)
		// An execute-in-place Run's env root is its own scratch, which nothing
		// removes and no later Run reuses, so its copies of the Task's files
		// go when it ends instead of piling up one set per Run.
		if localDir {
			defer pruneTaskAttachments(doc.TaskAttachmentRoot, nil, runLog)
		}
	}

	studioEnvironment := studioRunEnvironment{}
	if !restrictedTools {
		studioEnvironment, err = d.prepareStudioEnvironment(prepareCtx, studioRunInput{
			Enabled: studioEnabledForRun(doc), EnvRoot: envRoot,
			Run: run,
		})
		if err != nil {
			return RunResult{}, wrapRunPreparationError(runPreparationPhaseSkillSnapshot, err)
		}
	}
	if studioEnvironment.SnapshotDir != "" {
		defer os.RemoveAll(studioEnvironment.SnapshotDir)
	}
	bundles := append([]SkillData(nil), doc.Agent.Skills...)
	if studioEnvironment.Bundle != nil {
		bundles = append(bundles, *studioEnvironment.Bundle)
	}
	skillSnapshotDir, err := writeRunSkillSnapshot(envRoot, run, bundles)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseSkillSnapshot, err)
	}
	if skillSnapshotDir != "" {
		defer os.RemoveAll(skillSnapshotDir)
		companyContext.ReadDirs = append(companyContext.ReadDirs, skillSnapshotDir)
	}

	var skills []execenv.Skill
	skills = append(skills, studioEnvironment.Skills...)
	for _, bundle := range doc.Agent.Skills {
		skill := execenv.Skill{Name: bundle.Name, Description: bundle.Description, Content: bundle.Content}
		for _, f := range bundle.Files {
			skill.Files = append(
				skill.Files,
				execenv.SkillFile{Path: f.Path, Content: f.Content, Data: f.Data},
			)
		}
		skills = append(skills, skill)
	}

	sessionStoreKey := ""
	if !restrictedTools {
		sessionStoreKey = execenv.CodexSessionStoreKey(run.AgentID, run.ConversationID)
	}
	if snapshot != nil {
		if _, err := writeRunConversationSnapshot(envRoot, doc.ConversationSnapshot); err != nil {
			return RunResult{}, wrapRunPreparationError(runPreparationPhaseExecution, err)
		}
	}
	// One decision for the whole launch, so the provider's own sandbox
	// settings and the wrapper always agree. A Run without the sandbox keeps
	// the computer's report at not_enforced until it returns, after its
	// provider has ended (Access TECH B3d).
	sandboxed, isolationDone := d.startLaunchIsolation()
	defer isolationDone()
	env, err := execenv.Prepare(execenv.Options{
		EnvRoot:                  envRoot,
		WorkDir:                  workDir,
		Provider:                 providerName,
		CodexVersion:             d.agentVersion("codex"),
		ResumeSessionID:          run.PriorSessionID,
		LocalDirectory:           localDir,
		SessionStoreKey:          sessionStoreKey,
		ConnectorOnly:            restrictedTools,
		ReadOnlyDirs:             companyContext.ReadDirs,
		ProtectedCredentialPaths: protectedCredentialPaths(),
		Sandboxed:                sandboxed,
		Skills:                   skills,
		DisabledPlugins:          disabledPluginNames(doc),
		DisabledRuntimeSkills:    studioRuntimeSkillDenials(),
	}, d.logger)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseExecution,
			fmt.Errorf("prepare execution environment: %w", err),
		)
	}
	if env.ClaudeSkillsRoot != "" {
		defer os.RemoveAll(env.ClaudeSkillsRoot)
	}
	// A provider session is useful only when its local rollout is still
	// available to this daemon:
	// the rollout must actually be present in this run's CODEX_HOME sessions.
	if providerName == "codex" && run.PriorSessionID != "" &&
		!execenv.CodexResumeRolloutPresent(env.CodexHome, run.PriorSessionID) {
		runLog.Warn(
			"dropping prior codex session: rollout not present in run CODEX_HOME; starting a fresh thread",
			"session_id",
			run.PriorSessionID,
			"codex_home",
			env.CodexHome,
		)
		run.PriorSessionID = ""
	}
	if run.ForceFreshSession || restrictedTools {
		run.PriorSessionID = ""
	}
	continuity := turnContinuityFor(run)
	// The Run's own folders: what it works in and reads (its env root,
	// workdir, read-only mounts and Tool installations), and what it writes
	// under the workspaces root (its env root, which holds its scratch,
	// isolated home and provider homes; its workdir, for a direct chat the
	// Company main; its Task worktree's own folder in the Project main's
	// repository; its Agent's Studio sign-in folder). A tagged Run may open
	// nothing else in its hidden folders (Access TECH B3f, B7b). Its sandbox
	// is built from these, and the daemon reads files for the Run only where
	// they allow, sandboxed or not.
	launch := execenv.SeatbeltOptions{
		RunDirs: slices.Concat(
			[]string{envRoot, workDir}, companyContext.ReadDirs, studioEnvironment.ToolDirs,
		),
		Writable: slices.DeleteFunc(
			[]string{envRoot, workDir, gitDir, studioEnvironment.Home},
			func(folder string) bool { return folder == "" },
		),
		Hidden: d.runHiddenFolders(doc),
	}
	cliEnvironment, err := d.prepareRunCLIEnvironment(run, doc, envRoot, workDir, launch)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseExecution, err)
	}
	defer func() {
		if err := cliEnvironment.cleanup(); err != nil {
			runLog.Warn("remove isolated Zenith Team CLI environment failed", "error", err)
		}
	}()
	planRoot, err := ensureRunCompanyPlanRoot(envRoot)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseExecution, err)
	}
	var sandbox *agent.Sandbox
	if sandboxed {
		// The sandbox opens this Run's isolated home and no other Run's
		// (Access TECH B3e), so it is built once the home exists. A Run whose
		// own folder lies inside a denied path, or a tagged Run whose hidden
		// folder holds the person's home or the temporary folder, fails here
		// rather than starting unprotected on a computer that reports
		// enforced.
		launch.RunHome = cliEnvironment.boardlyHome
		launch.WritableRoots = env.SandboxWritableRoots
		sandbox, err = d.launchSandbox(launch)
		if err != nil {
			return RunResult{}, wrapRunPreparationError(
				runPreparationPhaseExecution,
				fmt.Errorf("build the run sandbox: %w", err),
			)
		}
	}

	// StartRun only after the workdir is on disk, so a consumer reading
	// status==running can resolve the path without racing MkdirAll.
	if err := d.client.StartRun(prepareCtx, run.RuntimeID, run.ID); err != nil {
		stopPrepareLease()
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseStartAuthority,
			fmt.Errorf("start run failed: %w", err),
		)
	}
	stopPrepareLease()
	prepareComplete = true
	cancelPrepare()

	token, err := runScopedAuthToken(run)
	if err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseExecution, err)
	}
	generatedImagePresenter := d.newRunOutputPresenter(run, doc, token, workDir, launch)
	presentation := runStreamPresentation{workDir: workDir, presenter: generatedImagePresenter, launch: launch}
	agentEnv := runAgentEnvironment(run, agentName, token, d.cfg.ServerBaseURL, d.cfg.HealthPort, slot)
	if restrictedTools {
		delete(agentEnv, "BOARDLY_RUN_TOKEN")
	}
	if env.CodexHome != "" {
		agentEnv["CODEX_HOME"] = env.CodexHome
	}
	agentEnv["BOARDLY_HOME"] = cliEnvironment.boardlyHome
	if cliEnvironment.executableDirectory != "" {
		agentEnv["PATH"] = pathWithRunCLI(os.Getenv("PATH"), cliEnvironment.executableDirectory)
	}
	if studioEnvironment.BinDir != "" {
		currentPath := agentEnv["PATH"]
		if currentPath == "" {
			currentPath = os.Getenv("PATH")
		}
		agentEnv["PATH"] = pathWithRunCLI(currentPath, studioEnvironment.BinDir)
	}
	if err := configureCodexRunShellEnvironment(
		providerName,
		env.CodexHome,
		os.Environ(),
		agentEnv,
		d.logger,
	); err != nil {
		return RunResult{}, wrapRunPreparationError(runPreparationPhaseExecution, err)
	}

	agentProvider, err := newAgentProvider(providerName, agent.Config{
		ExecutablePath: entry.Path,
		CLIVersion:     d.agentVersion(providerName),
		Env:            agentEnv,
		Logger:         d.logger,
		RunID:          run.ID.String(),
		RuntimeID:      run.RuntimeID,
		DaemonVersion:  d.cfg.CLIVersion,
		CodexVersion:   d.agentVersion("codex"),
	})
	if err != nil {
		return RunResult{}, wrapRunPreparationError(
			runPreparationPhaseProviderBackend,
			fmt.Errorf("create agent provider: %w", err),
		)
	}

	basePrompt := d.resolveRunPrompt(run, doc, runLog)
	turn := renderTurnInput(basePrompt, run, doc, snapshot, continuity)
	prompt := turn.prompt
	logRunTurnInput(runLog, snapshot, continuity)

	model := doc.Agent.Model
	if model == "" {
		model = entry.Model
	}

	runLog.Info("starting agent", "provider", providerName, "workdir", workDir, "model", model)
	if run.PriorSessionID != "" {
		runLog.Info("resuming session", "session_id", run.PriorSessionID)
	}
	runStart := time.Now()
	d.mu.Lock()
	billingMode := "unknown"
	if signIn := d.runtimeIndex[run.RuntimeID].SignIn; signIn != nil && signIn.BillingMode != "" {
		billingMode = signIn.BillingMode
	}
	d.mu.Unlock()

	execOpts := applyRunExecutionPolicy(doc, connectorState, agent.ExecOptions{
		RequireFinalAnswerProof: doc.ChannelInvocation != nil,
		DisabledPlugins:         disabledPluginNames(doc),
		Cwd:                     workDir,
		Model:                   model,
		SystemPrompt: buildRunSystemPromptWithCLI(doc, workDir, runMemoryContext{
			// A tagged Run's fresh scratch holds no memory, and it is said
			// outright rather than left to that folder's contents (Access
			// TECH B7).
			WorkspaceMemory:      !scheduled && !isTaggedRun(doc) && workspaceMemoryAvailable(workDir),
			CompanyReadDirs:      companyContext.MemoryReadDirs,
			CompanyReferenceRoot: filepath.Join(envRoot, companyReferenceDirectory),
			InPlace:              localDir,
		}, runPlatformPromptContext{
			SkillSnapshotRoot: skillSnapshotDir,
			CompanyID:         run.CompanyID,
			ReadOnlyDirs:      companyContext.ReadDirs,
			Commands:          cliEnvironment.commands,
			PresentOutput:     cliEnvironment.outputChannel != nil && d.cfg.MCPExecutable != "",
			CompanyPlanRoot:   planRoot,
		}),
		ThreadName:                runThreadName(doc),
		Timeout:                   d.cfg.AgentTimeout,
		SemanticInactivityTimeout: d.cfg.CodexSemanticInactivityTimeout,
		HandshakeTimeout:          d.cfg.CodexHandshakeTimeout,
		ResumeSessionID:           run.PriorSessionID,
		ResumeExpected:            run.PriorSessionID != "",
		ResumeContinuityNotice:    turn.continuityNotice,
		ExtraArgs:                 defaultArgsForProvider(d.cfg, providerName),
		McpConfig: d.boardlyMcpConfig(
			managedMCPConfig,
			run,
			boardlyMCPToolsForRun(doc),
			skillSnapshotDir,
			planRoot,
			cliEnvironment.channelHome(),
			cliEnvironment.outputChannel,
		),
		ConnectorApps:      connectorApps,
		PluginDirectories:  connectorState.PluginDirectories,
		ThinkingLevel:      doc.Agent.ThinkingLevel,
		ClaudeSettingsPath: env.ClaudeSettingsPath,
		Sandbox:            sandbox,
		ClaudeSkillsRoot:   env.ClaudeSkillsRoot,
	})

	outcome, err := d.executeRunStream(
		ctx,
		agentProvider,
		prompt,
		execOpts,
		runLog,
		run,
		env.CodexHome,
		presentation,
	)
	if err != nil {
		return RunResult{Transcript: outcome.Transcript, PendingItem: outcome.PendingItem}, err
	}
	result := outcome.Result
	tools := outcome.ToolCount
	transcript := outcome.Transcript
	pendingItem := outcome.PendingItem
	defer func() {
		runResult.Transcript = transcript
		runResult.PendingItem = pendingItem
	}()

	var retiredSessionID string
	defer func() { runResult.RetiredSessionID = retiredSessionID }()

	if shouldRetryWithFreshSession(result, run.PriorSessionID, tools) {
		retiredSessionID = run.PriorSessionID
		runLog.Warn("session resume failed, retrying with fresh session", "error", result.Error)
		execOpts.ResumeSessionID = ""
		// The re-rendered prompt carries the loss itself; nothing may prepend
		// the notice a second time.
		execOpts.ResumeContinuityNotice = ""
		run.PriorSessionID = ""
		continuity = continuity.afterRefusedResume()
		prompt = renderTurnInput(basePrompt, run, doc, snapshot, continuity).prompt
		retryOutcome, retryErr := d.executeRunStream(
			ctx,
			agentProvider,
			prompt,
			execOpts,
			runLog,
			run,
			env.CodexHome,
			presentation,
		)
		if retryErr != nil {
			if phase, classified := runPreparationFailurePhase(retryErr); classified {
				runLog.Error(
					"fresh session also failed to start; keeping the original result",
					"error_code",
					runPreparationDiagnosticEvent(retryErr),
					"phase",
					string(phase),
				)
			} else {
				runLog.Error(
					"fresh session also failed to start; keeping the original result",
					"error",
					retryErr,
				)
			}
		} else {
			retryResult := retryOutcome.Result
			retryResult.Usage = mergeUsage(result.Usage, retryResult.Usage)
			result = retryResult
			tools += retryOutcome.ToolCount
			if retryOutcome.Transcript.ThroughSeq > 0 {
				transcript = retryOutcome.Transcript
				pendingItem = retryOutcome.PendingItem
			}
		}
	}

	elapsed := time.Since(runStart).Round(time.Second)
	runLog.Info("agent finished", "status", result.Status, "duration", elapsed.String(), "tools", tools)

	var usageEntries []RunUsageEntry
	for model, u := range result.Usage {
		if u.InputTokens == 0 && u.OutputTokens == 0 && u.CacheReadTokens == 0 && u.CacheWriteTokens == 0 {
			continue
		}
		basis := u.UsageBasis
		if basis == "" {
			basis = agent.UsageBasisPerRun
		}
		usageEntries = append(usageEntries, RunUsageEntry{
			Provider:           providerName,
			Model:              model,
			InputTokens:        u.InputTokens,
			OutputTokens:       u.OutputTokens,
			CacheReadTokens:    u.CacheReadTokens,
			CacheWriteTokens:   u.CacheWriteTokens,
			UsageBasis:         string(basis),
			CostUSDTicks:       u.CostUSDTicks,
			ReasoningTokens:    u.ReasoningTokens,
			Turns:              u.Turns,
			ProviderDurationMs: u.ProviderDurationMs,
			BillingMode:        billingMode,
			RawJSON:            json.RawMessage(u.RawJSON),
		})
	}

	// Withhold a Codex session whose rollout never reached the store, for ANY
	// terminal state — a completed turn with a missing rollout must not be
	// recorded as a resume pointer the next follow-up would only drop.
	var sessionRolloutMissing bool
	if providerName == "codex" && result.SessionID != "" &&
		!codexSessionResumable(env.CodexHome, result.SessionID, codexRolloutFlushWait) {
		runLog.Warn(
			"codex session rollout not present in run CODEX_HOME; withholding resume pointer and flagging continuity gap",
			"session_id",
			result.SessionID,
			"codex_home",
			env.CodexHome,
			"status",
			result.Status,
		)
		result.SessionID = ""
		sessionRolloutMissing = true
	}
	defer func() { runResult.SessionRolloutMissing = sessionRolloutMissing }()

	switch result.Status {
	case agent.ResultCompleted:
		runResult = RunResult{
			Disposition: RunDispositionCompleted,
			Comment:     result.Output,
			SessionID:   result.SessionID,
			Usage:       usageEntries,
		}
		return runResult, nil
	case agent.ResultTimeout:
		comment := result.Error
		if comment == "" {
			comment = fmt.Sprintf("%s timed out after %s", providerName, d.cfg.AgentTimeout)
		}
		return RunResult{
			Disposition:   RunDispositionFailed,
			Comment:       comment,
			SessionID:     result.SessionID,
			FailureReason: string(runfailure.ReasonTimeout),
			Usage:         usageEntries,
		}, nil
	case agent.ResultIdleWatchdog:
		comment := result.Error
		if comment == "" {
			comment = idleWatchdogReason(d.cfg.AgentIdleWatchdog)
		}
		return RunResult{
			Disposition:   RunDispositionFailed,
			Comment:       comment,
			SessionID:     result.SessionID,
			FailureReason: string(runfailure.ReasonIdleWatchdog),
			Usage:         usageEntries,
		}, nil
	case agent.ResultCancelled:
		return RunResult{
			Disposition: RunDispositionCancelled,
			Comment:     "run cancelled by server",
			SessionID:   result.SessionID,
			Usage:       usageEntries,
		}, nil
	default:
		errMsg := result.Error
		if errMsg == "" {
			errMsg = fmt.Sprintf("%s execution %s", providerName, result.Status)
		}
		return RunResult{
			Disposition: RunDispositionFailed,
			Comment:     errMsg,
			SessionID:   result.SessionID,
			Usage:       usageEntries,
		}, nil
	}
}

func (d *Daemon) acquireManagedRunWorkspace(
	ctx context.Context,
	run Run,
	doc RunContext,
) (workspace.Acquired, error) {
	// A restricted document or Schedule Run and a tagged Run start in a single-use
	// scratch folder, so nothing an earlier Run left there becomes their
	// input. A channel answer never draws on its person's Company main or
	// private Memory, which no Project access governs, so a tagged Run must
	// not read files from it (Access TECH B7).
	if runToolPolicy(doc) != "" || isTaggedRun(doc) {
		return d.workspaces.AcquireIsolatedScratch(run.ConversationID, run.ID)
	}
	if task := doc.ExecutionProfile.Task; task != nil {
		if doc.ExecutionProfile.Project == nil || !validCommerceCompanyID(doc.ExecutionProfile.Project.ID) {
			return workspace.Acquired{}, errors.New("Task Run requires frozen Project identity")
		}
		if task.ConversationID != run.ConversationID {
			return workspace.Acquired{}, errors.New("task context conversation does not match run")
		}
		// The Task workspace generation the server froze into this Run picks
		// the root: a confirmed handoff advances it, so the work starts in a
		// fresh worktree instead of continuing another member's files
		// (ADR 0014).
		return d.workspaces.AcquireScopedScratch(
			ctx,
			run.CompanyID,
			"projects/"+doc.ExecutionProfile.Project.ID,
			run.ConversationID,
			run.ID,
			task.WorkspaceGeneration,
		)
	}
	// A direct conversation Run executes in the durable main workspace
	// (acquireMainRunWorkspace) and never falls back to a detached scratch
	// folder, which would cut its person off from the Company files they are
	// asking about. A tagged Run is the one conversation Run that does use
	// scratch, above.
	return workspace.Acquired{}, errors.New("run has no managed workspace binding")
}

// finishRunWorkspace ends a Run's use of its managed workspace. A Run a
// confirmed handoff cancelled leaves its root superseded: the Task's
// workspace generation moved on, so no later Run continues the root, and GC
// keeps the unmerged files instead of collecting them a day later (ADR
// 0014). Every other ending completes the root.
//
// The cancellation poll is often not what ends a handed-off Run: once the
// cancel commits, the server refuses the Run's next transcript append, and
// that refusal ends the Run first. So when checkHandoff is set — a Task Run
// that did not complete cleanly — the server's run status decides.
func (d *Daemon) finishRunWorkspace(ctx context.Context, runID uuid.UUID, root string, checkHandoff bool) error {
	if errors.Is(context.Cause(ctx), errRunHandedOff) || checkHandoff && d.runHandedOff(ctx, runID) {
		return d.workspaces.MarkSuperseded(root)
	}
	return d.workspaces.MarkCompleted(root)
}

// runHandedOff asks the server whether a confirmed handoff cancelled runID.
// Superseding a root is final — a later Run of its generation is refused —
// so only the server's answer may do it: when the server cannot be reached,
// the root is completed as it always was.
func (d *Daemon) runHandedOff(ctx context.Context, runID uuid.UUID) bool {
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	status, err := d.client.GetRunStatus(readCtx, runID)
	return err == nil && status.handedOff()
}

// supersedeFinishedTaskRoot retires the root of a Task Run that finished,
// and completed its root, before the daemon learned a handoff had cancelled
// it. root is empty for every other Run.
func (d *Daemon) supersedeFinishedTaskRoot(root string, runLog *slog.Logger) {
	if root == "" {
		return
	}
	if err := d.workspaces.SupersedeFinished(root); err != nil {
		runLog.Warn("supersede handed-off workspace failed", "error", err)
	}
}

// companyWorkspaceScope owns direct Conversations and shared Company context.
// Task worktrees use their frozen projects/<projectId> scope.
const companyWorkspaceScope = "company"

// runUsesMainWorkspace reports whether a managed run executes directly in the
// durable Company main workspace: every conversation run that is not a Task
// worktree run, an isolated Schedule occurrence or a tagged Run, which starts
// in its own scratch (Access TECH B7). A local_directory resource binding
// still wins for unrestricted Runs — the caller checks it first.
func runUsesMainWorkspace(doc RunContext) bool {
	return runToolPolicy(doc) == "" && !isTaggedRun(doc) && doc.ExecutionProfile.Task == nil
}

// mainWorkspaceScopeFor is the durable workspace owner for a direct run.
func mainWorkspaceScopeFor(RunContext) string {
	return companyWorkspaceScope
}

// acquireMainRunWorkspace prepares the durable main workspace a non-task
// conversation run executes in and takes the same in-process path lock
// local-directory runs use, so two runs never execute in one main
// concurrently. ctx is the run's outer context — the busy wait is the
// server-sanctioned indefinite park — while prepareCtx bounds the Git
// initialization with the preparation budget.
func (d *Daemon) acquireMainRunWorkspace(
	ctx context.Context,
	prepareCtx context.Context,
	run Run,
	doc RunContext,
	runLog *slog.Logger,
) (string, *workspace.LocalDirectoryLease, bool, error) {
	mainDir, err := d.workspaces.EnsureMain(prepareCtx, run.CompanyID, mainWorkspaceScopeFor(doc))
	if err != nil {
		return "", nil, false, fmt.Errorf("prepare main workspace: %w", err)
	}
	lease, parked, err := d.acquireLocalDirectory(ctx, run, mainDir, runLog)
	if err != nil {
		return "", nil, false, err
	}
	return mainDir, lease, parked, nil
}

type companyRunContext struct {
	ReadDirs            []string
	MemoryReadDirs      []string
	BusinessContextPath string
}

// companyRunContext makes the Company main workspace's memory and business
// context available read-only to runs that execute elsewhere (legacy scoped
// roots). Company runs already work in that main workspace; scheduled runs
// remain isolated from both; a tagged Run gets the business context alone.
func (d *Daemon) companyRunContext(
	ctx context.Context,
	run Run,
	doc RunContext,
	workDir string,
) (companyRunContext, error) {
	if isDocumentRun(doc) {
		return companyRunContext{}, nil
	}
	// A tagged Run works in its own scratch, and a channel answer never draws
	// on its person's private Memory, so it gets the Company's business
	// context folder read-only and no memory (Access TECH B7). It reads the
	// main only when it is already there: a Company with no main has no saved
	// business context.
	if isTaggedRun(doc) {
		companyMain, err := d.workspaces.ResolveMain(ctx, run.CompanyID, companyWorkspaceScope)
		if errors.Is(err, fs.ErrNotExist) {
			return companyRunContext{}, nil
		}
		if err != nil {
			return companyRunContext{}, fmt.Errorf("resolve Company context workspace: %w", err)
		}
		result := companyRunContext{BusinessContextPath: existingBusinessContextPath(companyMain)}
		if result.BusinessContextPath != "" {
			result.ReadDirs = []string{filepath.Dir(result.BusinessContextPath)}
		}
		return result, nil
	}
	// A direct Run works in the Company main itself (or in a bound local
	// directory), so the business context is a file in its own workspace. A
	// Task Run works in a worktree branched from that main — which never carries
	// what the main holds uncommitted, such as the CEO's saved business context
	// — so every Task Run gets the live Company main Memory and business
	// context mounted read-only, whichever scope its worktree lives under.
	if isScheduledRun(doc) || doc.ExecutionProfile.Task == nil {
		return companyRunContext{
			BusinessContextPath: existingBusinessContextPath(workDir),
		}, nil
	}
	companyMain, err := d.workspaces.EnsureMain(ctx, run.CompanyID, companyWorkspaceScope)
	if err != nil {
		return companyRunContext{}, fmt.Errorf("prepare Company context workspace: %w", err)
	}
	memoryDir := filepath.Join(companyMain, localWorkspaceMemoryDirectory)
	result := companyRunContext{
		ReadDirs:       []string{memoryDir},
		MemoryReadDirs: []string{memoryDir},
	}
	result.BusinessContextPath = existingBusinessContextPath(companyMain)
	if result.BusinessContextPath != "" {
		result.ReadDirs = append(result.ReadDirs, filepath.Dir(result.BusinessContextPath))
	}
	return result, nil
}

const (
	// companyReferenceDirectory holds the founder-supplied documents for one
	// Run. It lives under the Run's scratch env root, never in the workspace:
	// these are the raw material the shared business context is written from,
	// and committing them would put a founder's documents into the Company
	// repository permanently and into every worktree branched from it.
	companyReferenceDirectory = "references"
	// taskAttachmentDirectory holds, under the Run's env root, the files
	// members attached to the Task, for the same reason: they are its inputs,
	// and the Task's branch holds only its work.
	taskAttachmentDirectory = "task-files"
	// runAttachmentDownloadTimeout bounds one file's download. A reference
	// document that cannot arrive in this long is not worth delaying the Run
	// the founder is waiting on.
	runAttachmentDownloadTimeout = 2 * time.Minute
	// taskAttachmentPrepareReserve is the part of the preparation budget
	// Task file downloads never spend: the execution environment and StartRun
	// still have to fit after them, and a slow file must end up Unavailable
	// rather than time the whole Run out.
	taskAttachmentPrepareReserve = time.Minute
	// attachedFileDownloadPattern names a download still in progress. The
	// leading dot keeps it apart from every placed name, which
	// safeAttachmentFilename never lets start with one.
	attachedFileDownloadPattern = ".download-*"
)

// materializeAttachedFiles downloads the files a Run is entitled to read into
// root and returns the ones that arrived, each renamed to the on-disk name the
// Agent can actually open. It never fails the Run: a reference document that
// cannot be fetched is a degraded conversation, not a broken one, and the
// prompt policy tells the Agent to say which files it could not read.
func (d *Daemon) materializeAttachedFiles(
	ctx context.Context,
	run Run,
	files []RunAttachedFile,
	root string,
	runLog *slog.Logger,
) []RunAttachedFile {
	if len(files) == 0 {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		runLog.Warn("attached file directory unavailable", "error", err)
		return nil
	}
	materialized := make([]RunAttachedFile, 0, len(files))
	taken := make(map[string]struct{}, len(files))
	for _, file := range files {
		name := uniqueAttachmentFilename(safeAttachmentFilename(file.Filename, file.ID), taken)
		if placed, ok := d.placeAttachedFile(ctx, run, file, root, name, runLog); ok {
			materialized = append(materialized, placed)
		}
	}
	return materialized
}

// placeAttachedFile puts one file at root/name and returns it renamed to that
// path relative to root. A regular file already there is kept, because the
// caller's naming makes it the same file: a Company reference placed by an
// earlier Run, or a Task file under its attachment id. Re-downloading it would
// churn the workspace for nothing.
func (d *Daemon) placeAttachedFile(
	ctx context.Context,
	run Run,
	file RunAttachedFile,
	root, name string,
	runLog *slog.Logger,
) (RunAttachedFile, bool) {
	path := filepath.Join(root, name)
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		if err := d.downloadAttachedFile(ctx, run, file, path); err != nil {
			runLog.Warn("attached file unavailable",
				"attachment_id", file.ID, "filename", file.Filename, "error", err)
			return RunAttachedFile{}, false
		}
	}
	file.Filename = name
	return file, true
}

// unavailableAttachedFiles names, by the filename a member gave it, each
// requested file that did not arrive, so the prompt can say which ones the
// Agent must not guess at.
func unavailableAttachedFiles(requested, placed []RunAttachedFile) []string {
	arrived := make(map[string]bool, len(placed))
	for _, file := range placed {
		arrived[file.ID] = true
	}
	var missing []string
	for _, file := range requested {
		if !arrived[file.ID] {
			missing = append(missing, file.Filename)
		}
	}
	return missing
}

// placeTaskAttachments downloads the files members attached to the Task into
// task-files/ under the Run's env root, each at <attachment id>/<its
// filename>. They are the Task's inputs rather than its work, so they stay out
// of the workspace and the Task's branch, and a Run outside a Task never
// receives them.
//
// A managed Task's env root belongs to its Conversation's current workspace
// generation (ADR 0014) and is reused by every Run of that generation while
// the frozen set shifts between them. So a file is reused only by its
// immutable attachment id, never by a name another attachment may have held,
// and whatever is not in this Run's set is removed first, so a file that left
// the set can no longer be read. A handoff starts a new root, which downloads
// the files afresh; a superseded root keeps its copies with whatever else was
// left in it.
func (d *Daemon) placeTaskAttachments(
	ctx context.Context,
	run Run,
	doc *RunContext,
	envRoot string,
	runLog *slog.Logger,
) {
	if doc.ExecutionProfile.Task == nil {
		doc.TaskAttachments = nil
		return
	}
	root := filepath.Join(envRoot, taskAttachmentDirectory)
	requested := doc.TaskAttachments
	names := make(map[string]string, len(requested))
	for _, file := range requested {
		names[file.ID] = safeAttachmentFilename(file.Filename, file.ID)
	}
	pruneTaskAttachments(root, names, runLog)
	if len(requested) == 0 {
		doc.TaskAttachments = nil
		return
	}
	// A Task's files can be many and large enough to outlast the preparation
	// budget; they share it only down to the reserve the Run needs to start.
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-taskAttachmentPrepareReserve))
		defer cancel()
	}
	doc.TaskAttachmentRoot = root
	doc.TaskAttachments = make([]RunAttachedFile, 0, len(requested))
	for _, file := range requested {
		// The id names a directory, so it must be one ordinary path segment.
		if file.ID == "" || safeAttachmentFilename(file.ID, "") != file.ID {
			runLog.Warn("attached file unavailable", "attachment_id", file.ID, "filename", file.Filename,
				"error", "attachment id is not a single path segment")
			continue
		}
		if err := os.MkdirAll(filepath.Join(root, file.ID), 0o755); err != nil {
			runLog.Warn("attached file unavailable",
				"attachment_id", file.ID, "filename", file.Filename, "error", err)
			continue
		}
		name := filepath.Join(file.ID, names[file.ID])
		if placed, ok := d.placeAttachedFile(ctx, run, file, root, name, runLog); ok {
			doc.TaskAttachments = append(doc.TaskAttachments, placed)
		}
	}
	doc.UnavailableTaskAttachments = unavailableAttachedFiles(requested, doc.TaskAttachments)
}

// pruneTaskAttachments removes from root everything but the Task files named
// in keep, by attachment id: a file that left the Run's frozen set, a download
// a killed daemon abandoned, and anything that is not a real directory where
// an attachment's directory belongs.
func pruneTaskAttachments(root string, keep map[string]string, runLog *slog.Logger) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			runLog.Warn("task files directory unreadable", "error", err)
		}
		return
	}
	for _, entry := range entries {
		dir := filepath.Join(root, entry.Name())
		name, kept := keep[entry.Name()]
		if !kept || !entry.IsDir() {
			if err := os.RemoveAll(dir); err != nil {
				runLog.Warn("remove stale task file failed", "path", dir, "error", err)
			}
			continue
		}
		inside, err := os.ReadDir(dir)
		if err != nil {
			runLog.Warn("task file directory unreadable", "path", dir, "error", err)
			continue
		}
		for _, stale := range inside {
			// NFC because an HFS+ volume hands names back decomposed.
			if norm.NFC.String(stale.Name()) == norm.NFC.String(name) && stale.Type().IsRegular() {
				continue
			}
			path := filepath.Join(dir, stale.Name())
			if err := os.RemoveAll(path); err != nil {
				runLog.Warn("remove stale task file failed", "path", path, "error", err)
			}
		}
	}
}

// placeCompanyReferences downloads the Company's founder-supplied documents
// into this Run's scratch directory and returns the ones that arrived, so the
// prompt can name paths the Agent can actually open.
//
// They are deliberately not written into the workspace: a document the founder
// hands over to explain their business is input to the shared business
// context, and committing it would keep it in the Company repository forever
// and copy it into every worktree branched afterwards.
func (d *Daemon) placeCompanyReferences(
	ctx context.Context,
	run Run,
	doc RunContext,
	envRoot string,
	runLog *slog.Logger,
) []RunAttachedFile {
	if len(doc.CompanyReferences) == 0 {
		return nil
	}
	root := filepath.Join(envRoot, companyReferenceDirectory)
	return d.materializeAttachedFiles(ctx, run, doc.CompanyReferences, root, runLog)
}

// downloadAttachedFile fetches one file into a temporary file beside path and
// renames it into place only once every byte has arrived and been closed, so
// a daemon killed mid-download never leaves a truncated file at path for a
// later Run to reuse as whole. A context already done starts no download.
func (d *Daemon) downloadAttachedFile(
	ctx context.Context,
	run Run,
	attachment RunAttachedFile,
	path string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, runAttachmentDownloadTimeout)
	defer cancel()

	file, err := os.CreateTemp(filepath.Dir(path), attachedFileDownloadPattern)
	if err != nil {
		return err
	}
	temporary := file.Name()
	err = file.Chmod(0o644)
	if err == nil {
		err = d.client.DownloadRunAttachment(reqCtx, run.RuntimeID, run.ID, attachment.ID, file)
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(temporary, path)
	}
	if err != nil {
		os.Remove(temporary)
		return err
	}
	return nil
}

// safeAttachmentFilename keeps a founder-supplied name readable while making
// it a single, ordinary path segment: a name carrying separators or a leading
// dot would otherwise write outside the attachments directory or hide there.
func safeAttachmentFilename(filename, fallback string) string {
	cleaned := strings.TrimSpace(filepath.Base(filepath.Clean("/" + strings.TrimSpace(filename))))
	cleaned = strings.TrimLeft(cleaned, ".")
	if cleaned == "" || cleaned == "/" {
		return fallback
	}
	return cleaned
}

// uniqueAttachmentFilename keeps two files with the same name from colliding;
// the Agent is given the names it can actually open. Names are compared as
// the default macOS and Windows volumes compare them, ignoring case and
// Unicode normalization, so "Report.pdf" and "report.pdf" never share a file.
func uniqueAttachmentFilename(name string, taken map[string]struct{}) string {
	candidate := name
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	for index := 2; ; index++ {
		key := norm.NFC.String(strings.ToLower(candidate))
		if _, clash := taken[key]; !clash {
			taken[key] = struct{}{}
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", stem, index, extension)
	}
}

func existingBusinessContextPath(workspaceRoot string) string {
	path := filepath.Join(workspaceRoot, "assets", "general", "business_context.md")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	return path
}

// workspaceMemoryAvailable reports whether the run workspace carries a real
// {workDir}/.memory directory. Lstat keeps a symlink from counting.
func workspaceMemoryAvailable(workDir string) bool {
	info, err := os.Lstat(filepath.Join(workDir, localWorkspaceMemoryDirectory))
	return err == nil && info.IsDir()
}

// ensureRunCompanyPlanRoot creates the Run-environment directory that owns
// the Company Plan draft and export, so neither lands in the directory the
// Agent works in (Runs TECH §11.2: a Run's temporary artifacts stay out of
// cwd; WS-FR10: acceptance excludes daemon-generated files).
func ensureRunCompanyPlanRoot(envRoot string) (string, error) {
	planRoot := filepath.Join(envRoot, "company-plan")
	if err := os.MkdirAll(planRoot, 0o700); err != nil {
		return "", fmt.Errorf("create Company Plan scratch root: %w", err)
	}
	return planRoot, nil
}

// ensureRunScratchEnvRoot creates the daemon-owned per-run environment root
// execute-in-place runs use, so provider homes and settings never land in the
// directory the agent works in.
func (d *Daemon) ensureRunScratchEnvRoot(runID uuid.UUID) (string, error) {
	envRoot := filepath.Join(
		d.cfg.WorkspacesRoot, workspace.LocalScratchDirectoryName, runID.String(),
	)
	if err := os.MkdirAll(envRoot, 0o700); err != nil {
		return "", fmt.Errorf("create local scratch root: %w", err)
	}
	return envRoot, nil
}

// shouldRetryWithFreshSession reports whether a failed run that requested a
// resume should be retried once from a fresh session. Two independent
// questions must both answer yes: would a fresh session fix this (the
// backend's positive ResumeRejected evidence), and is re-running safe (no
// tool executed — a retry after real work would duplicate side effects).
func shouldRetryWithFreshSession(result agent.Result, priorSessionID string, tools int32) bool {
	if result.Status != agent.ResultFailed || priorSessionID == "" || tools > 0 {
		return false
	}
	return result.ResumeRejected
}

func mergeUsage(a, b map[string]agent.TokenUsage) map[string]agent.TokenUsage {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	merged := make(map[string]agent.TokenUsage, len(a)+len(b))
	for model, u := range a {
		merged[model] = u
	}
	for model, u := range b {
		existing, present := merged[model]
		if !present {
			merged[model] = u
			continue
		}
		existing.InputTokens += u.InputTokens
		existing.OutputTokens += u.OutputTokens
		existing.CacheReadTokens += u.CacheReadTokens
		existing.CacheWriteTokens += u.CacheWriteTokens
		existing.CostUSDTicks = sumOptionalUsage(existing.CostUSDTicks, u.CostUSDTicks)
		existing.ReasoningTokens = sumOptionalUsage(existing.ReasoningTokens, u.ReasoningTokens)
		existing.ProviderDurationMs = sumOptionalUsage(existing.ProviderDurationMs, u.ProviderDurationMs)
		if existing.Turns != nil && u.Turns != nil {
			turns := *existing.Turns + *u.Turns
			existing.Turns = &turns
		} else {
			existing.Turns = nil
		}
		// Both attempts ran the same provider, so the basis is shared; carry
		// it when the first attempt's entry did not set one.
		if existing.UsageBasis == "" {
			existing.UsageBasis = u.UsageBasis
		}
		existing.RawJSON = mergedUsageFragment(existing)
		merged[model] = existing
	}
	return merged
}

func mergedUsageFragment(usage agent.TokenUsage) string {
	raw, err := json.Marshal(map[string]int64{
		"input_tokens":       usage.InputTokens,
		"output_tokens":      usage.OutputTokens,
		"cache_read_tokens":  usage.CacheReadTokens,
		"cache_write_tokens": usage.CacheWriteTokens,
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

func sumOptionalUsage(a, b *int64) *int64 {
	if a == nil || b == nil {
		return nil
	}
	value := *a + *b
	return &value
}

const (
	// codexRolloutFlushWait bounds how long the daemon waits for Codex to
	// finish writing a session's rollout before treating it as unrecoverable.
	codexRolloutFlushWait    = 2 * time.Second
	codexRolloutPollInterval = 50 * time.Millisecond
)

// codexSessionResumable reports whether a Codex session's rollout is present
// in the run's session store, with a brief bounded window for flush lag.
func codexSessionResumable(codexHome, sessionID string, wait time.Duration) bool {
	if codexHome == "" || sessionID == "" {
		return true
	}
	deadline := time.Now().Add(wait)
	for {
		if execenv.CodexResumeRolloutPresent(codexHome, sessionID) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(codexRolloutPollInterval)
	}
}

// waitCodexRolloutPresent blocks until sessionID's rollout appears or ctx is
// done. Backs the mid-flight pin: Codex reveals the session id once, so a
// one-shot check would miss a rollout that flushes a beat later.
func waitCodexRolloutPresent(ctx context.Context, codexHome, sessionID string) bool {
	if codexHome == "" || sessionID == "" {
		return true
	}
	if execenv.CodexResumeRolloutPresent(codexHome, sessionID) {
		return true
	}
	ticker := time.NewTicker(codexRolloutPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return execenv.CodexResumeRolloutPresent(codexHome, sessionID)
		case <-ticker.C:
			if execenv.CodexResumeRolloutPresent(codexHome, sessionID) {
				return true
			}
		}
	}
}

// acquireLocalDirectory validates the run's local_directory path and takes
// the in-process path lock (workspace.LocalDirectoryManager), parking the
// run server-side (POST wait-local-directory) while another run holds it.
// The caller passes the run's OUTER context, not the prepare deadline: the
// park is the server-sanctioned indefinite wait (waiting_local_directory,
// lease kept fresh), and bounding it by the prepare budget converted healthy
// queued work into timeout failures whenever a long run held the directory.
// The returned parked flag tells the caller a wait actually happened, so it
// can restart its prepare deadline for the steps that follow.
func (d *Daemon) acquireLocalDirectory(
	ctx context.Context,
	run Run,
	path string,
	runLog *slog.Logger,
) (*workspace.LocalDirectoryLease, bool, error) {
	lease, err := d.localDirs.Acquire(path)
	if err == nil {
		runLog.Info("local_directory: lock acquired", "path", lease.Path())
		return lease, false, nil
	}
	if !errors.Is(err, workspace.ErrLocalDirectoryBusy) {
		return nil, false, fmt.Errorf("local_directory: %w", err)
	}

	d.resourceWaitRuns.Add(1)
	defer d.resourceWaitRuns.Add(-1)
	runLog.Info("local_directory: waiting on path lock", "path", path)
	if waitErr := d.client.MarkRunWaitingLocalDirectory(ctx, run.ID); waitErr != nil {
		// Non-fatal: the UI just won't show the explicit waiting badge.
		runLog.Warn("local_directory: mark waiting status failed", "error", waitErr)
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, true, fmt.Errorf("local_directory wait cancelled: %w", context.Cause(ctx))
		case <-ticker.C:
			lease, err := d.localDirs.Acquire(path)
			if err == nil {
				runLog.Info("local_directory: lock acquired", "path", lease.Path())
				return lease, true, nil
			}
			if !errors.Is(err, workspace.ErrLocalDirectoryBusy) {
				return nil, true, fmt.Errorf("local_directory: %w", err)
			}
		}
	}
}
