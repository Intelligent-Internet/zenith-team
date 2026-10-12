// Package execution owns the run lifecycle kernel: enqueue, claim, terminal
// transitions, and auto-retry. It no longer owns the daemon wakeup plumbing:
// it publishes that a runtime may have claimable work and stops there, so
// nothing here holds a handle to whoever wakes daemons.
package execution

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/telemetry"
	"boardly.dev/boardly/server/internal/util"
	db "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/redact"
	"boardly.dev/boardly/server/pkg/runfailure"
)

// Transactor runs fn inside one transaction, handing it a query set bound to
// that transaction. *db.Store satisfies it. The run lifecycle deliberately
// does not begin transactions itself, so the rollback discipline — a detached
// context, because pgx destroys a connection rolled back on an already
// cancelled one — lives in exactly one place.
type Transactor interface {
	WithinTx(ctx context.Context, options pgx.TxOptions, fn func(*db.Queries) error) error
}

// RunQueuePromoter is the narrow cross-domain seam used after a Run reaches a
// terminal state. The Conversation domain owns queued prompts and their
// Attachment reservations; the execution domain owns the transaction that
// clears the active-Run barrier. Keeping this seam here avoids either domain
// importing the other's service implementation.
type RunQueuePromoter interface {
	FinalizeRunConversationInTx(
		context.Context,
		*db.Queries,
		*[]events.Event,
		db.Run,
		string,
	) (*uuid.UUID, error)
}

// RunQueuePromoterFunc adapts a function to RunQueuePromoter at composition.
type RunQueuePromoterFunc func(
	context.Context,
	*db.Queries,
	*[]events.Event,
	db.Run,
	string,
) (*uuid.UUID, error)

func (f RunQueuePromoterFunc) FinalizeRunConversationInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	run db.Run,
	output string,
) (*uuid.UUID, error) {
	return f(ctx, q, pending, run, output)
}

// TaskTerminalEffectWriter is the narrow Task-domain seam used after queue
// promotion decides whether the terminal Run still owns its Task generation.
// The Task domain owns the status write and task.updated event.
type TaskTerminalEffectWriter interface {
	ApplyRunTerminalInTx(
		context.Context,
		*db.Queries,
		*[]events.Event,
		db.Run,
	) error
}

// TaskTerminalEffectWriterFunc adapts the Task domain's exported
// in-transaction function at composition.
type TaskTerminalEffectWriterFunc func(
	context.Context,
	*db.Queries,
	*[]events.Event,
	db.Run,
) error

func (f TaskTerminalEffectWriterFunc) ApplyRunTerminalInTx(
	ctx context.Context,
	queries *db.Queries,
	pending *[]events.Event,
	run db.Run,
) error {
	return f(ctx, queries, pending, run)
}

// RunObserver is the metrics seam: it receives one observation for every
// terminal Run outcome the lifecycle has just persisted, and nothing for a
// callback that found the Run already finalized. The telemetry Registry
// implements it in production.
type RunObserver interface {
	ObserveRunTerminal(telemetry.RunTerminalObservation)
}

// ConnectorSnapshotter freezes the reviewed Connector contract for a freshly
// claimed Run inside the claim transaction. The Connector domain implements
// the policy/discovery rules; execution owns only when the freeze occurs.
type ConnectorSnapshotter interface {
	FreezeRunConnectorSnapshotInTx(
		context.Context,
		*db.Queries,
		uuid.UUID,
		uuid.UUID,
	) (db.Run, error)
}

// ConnectorSnapshotterFunc adapts the Connector domain's exported
// in-transaction function at the composition root.
type ConnectorSnapshotterFunc func(
	context.Context,
	*db.Queries,
	uuid.UUID,
	uuid.UUID,
) (db.Run, error)

// ErrRunConnectorSnapshotUnavailable means an exact reviewed Connector
// selection no longer exists in full at claim time. The claim is rolled back
// and the queued Run is terminalized instead of dispatching with partial
// authority or retrying the same impossible claim forever.
var ErrRunConnectorSnapshotUnavailable = errors.New("Run Connector snapshot is unavailable")

// CancellationConnectorUnavailable is the terminal reason for a Run whose
// reviewed Connector scope drifted before or during claim.
const CancellationConnectorUnavailable = "connector_unavailable"

func (f ConnectorSnapshotterFunc) FreezeRunConnectorSnapshotInTx(
	ctx context.Context,
	q *db.Queries,
	runID uuid.UUID,
	runtimeID uuid.UUID,
) (db.Run, error) {
	return f(ctx, q, runID, runtimeID)
}

// ConversationRouteResolver reports a conversation's kind and creator from
// its authoritative row. It is injected by the conversation domain to avoid an
// execution -> conversation import cycle.
//
// It asks a domain question, not a transport one: execution learns what kind
// of conversation it is writing to, and the browser bridge decides on its own
// what that means for visibility.
type ConversationRouteResolver func(
	context.Context,
	*db.Queries,
	uuid.UUID,
	uuid.UUID,
) (ConversationRoute, error)

// RunService is the run lifecycle surface the daemon plane and the
// composition root consume.
type RunService interface {
	List(context.Context, auth.UserPrincipal, ListOptions) (Page, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (RunView, error)
	Cancel(context.Context, auth.UserPrincipal, uuid.UUID, string) (RunView, error)
	AppendTranscriptItem(
		context.Context,
		uuid.UUID,
		uuid.UUID,
		TranscriptItemInput,
	) (TranscriptItemReceipt, error)
	ResolveRunOrigin(context.Context, string) (RunOrigin, error)
	ClaimRunsForRuntimes(ctx context.Context, runtimeIDs []uuid.UUID, maxRuns int) ([]db.Run, error)
	FinalizeRunClaim(ctx context.Context, token db.CreateRunTokenParams) error
	RequeueRunAfterClaimFailure(ctx context.Context, run db.Run) (*db.Run, error)
	PromoteDueDeferredRunsForRuntimes(ctx context.Context, runtimeIDs []uuid.UUID) error
	PromoteDueDeferredRuns(ctx context.Context) ([]db.Run, error)
	StartRun(ctx context.Context, runID uuid.UUID, runtimeID uuid.UUID) (*db.Run, error)
	ExtendRunPrepareLease(ctx context.Context, runID uuid.UUID, runtimeID uuid.UUID) error
	MarkRunWaitingLocalDirectory(ctx context.Context, runID uuid.UUID) (*db.Run, error)
	WaitForStudioAuth(ctx context.Context, runID uuid.UUID, waiting bool) (*db.Run, error)
	CompleteRun(
		ctx context.Context,
		runID uuid.UUID,
		proof TerminalTranscriptProof,
		sessionID string,
		sessionRolloutMissing bool,
		retiredSessionID string,
	) (*db.Run, error)
	FailRun(
		ctx context.Context,
		runID uuid.UUID,
		proof TerminalTranscriptProof,
		errMsg, sessionID, failureReason string,
		sessionRolloutMissing bool,
		retiredSessionID string,
	) (*db.Run, error)
	CancelRun(ctx context.Context, runID uuid.UUID, reason string) (*db.Run, error)
	FailRunsForOfflineRuntimes(context.Context) (FailedRunBatch, error)
	FailStaleRuns(context.Context, StaleRunOptions) (FailedRunBatch, error)
	ExpireStaleQueuedRuns(context.Context, QueuedRunExpiryOptions) (FailedRunBatch, error)
	RecoverOrphanedRunsForRuntime(context.Context, uuid.UUID) (FailedRunBatch, error)
	NotifyRunEnqueued(ctx context.Context, run db.Run)
	NotifyRunFinished(run db.Run)
}

var _ RunService = (*DefaultRunService)(nil)

var (
	// ErrRunProofInvalid deliberately collapses malformed, expired, unknown,
	// and no-longer-running run tokens. Callers cannot use the proof endpoint
	// to distinguish credential state.
	ErrRunProofInvalid = errors.New("run proof is invalid")
	// ErrTranscriptHeadMismatch means the daemon's terminal observation does
	// not name the Run's authoritative committed transcript prefix. The caller
	// must reconcile before retrying the terminal transition; every error that
	// satisfies it is a *TranscriptHeadMismatchError carrying the head to
	// reconcile against.
	ErrTranscriptHeadMismatch = errors.New("transcript head does not match terminal proof")
	// ErrTerminalFinalItemInvalid means a completion did not select the newest
	// committed agent item containing text.
	ErrTerminalFinalItemInvalid = errors.New("terminal final item is invalid")
	// ErrRunContextExhausted means the "successful" final output is really the
	// provider announcing its context window is full. Completing would keep
	// the poisoned session pointer and every follow-up would resume an
	// exhausted session; the caller converts this into a failed run with
	// runfailure.ReasonAgentContextOverflow and retires the session.
	ErrRunContextExhausted = errors.New("run output reports an exhausted context window")
)

// TerminalTranscriptProof binds a provider terminal event to the exact
// PostgreSQL-acknowledged transcript prefix observed by the daemon.
// ThroughSeq is a Conversation-wide seq, so it is int64 like the column, and
// it is the whole proof: an append holds the Conversation row lock and
// conversation_items rows are immutable, so one seq names one history.
type TerminalTranscriptProof struct {
	ThroughSeq   int64
	FinalItemKey string
}

// TranscriptHeadMismatchError is what a refused terminal transition returns,
// and the reason a refusal is now recoverable. It carries the server's OWN
// head beside the proof that was rejected.
//
// Bug R1: without the server's head the daemon has nothing to reconcile
// against. It re-sent the identical stale proof through FailRun, was refused
// again, and gave up — leaving the Run non-terminal forever with the agent's
// answer already durable in the Conversation. The boundary must put
// ServerThroughSeq on the 409 body (protocol.RunTerminalHeadMismatch) so the
// daemon adopts the real head, re-appends whatever the server never
// acknowledged — idempotent through the item_key replay fence — and re-sends
// the terminal transition with a proof that matches.
type TranscriptHeadMismatchError struct {
	RunID            uuid.UUID
	ProofThroughSeq  int64
	ServerThroughSeq int64
}

func (e *TranscriptHeadMismatchError) Error() string {
	return fmt.Sprintf(
		"%s: run %s proved through seq %d, server is at seq %d",
		ErrTranscriptHeadMismatch.Error(),
		e.RunID,
		e.ProofThroughSeq,
		e.ServerThroughSeq,
	)
}

// Unwrap keeps every existing errors.Is(err, ErrTranscriptHeadMismatch) call
// site — the 409 mapping among them — working unchanged.
func (e *TranscriptHeadMismatchError) Unwrap() error { return ErrTranscriptHeadMismatch }

// Head is the reconciliation body the boundary serializes onto the 409.
func (e *TranscriptHeadMismatchError) Head() protocol.RunTerminalHeadMismatch {
	return protocol.RunTerminalHeadMismatch{
		RunID:      e.RunID,
		ThroughSeq: e.ServerThroughSeq,
	}
}

// RunOrigin is supplemental scope proved by X-Boardly-Run-Token. It never
// replaces the ordinary authenticated UserPrincipal.
type RunOrigin struct {
	RunID            uuid.UUID
	CompanyID        uuid.UUID
	AgentID          uuid.UUID
	TaskID           *uuid.UUID
	ConversationID   uuid.UUID
	ConversationKind string
	OriginUserID     *uuid.UUID
	// AuthorizedByUserID is the member the Run works for: its authorizer,
	// on whose binding of the Agent it executes (ADR 0014). OriginUserID is
	// who opened the Conversation or tagged the Agent, which for a Task Run
	// need not be the assignee. Nil only on a Run that predates authorizers.
	AuthorizedByUserID *uuid.UUID
	// InvocationChannelID is the channel or group DM a tagged Run answers
	// into. It marks a tagged Run and bounds nothing: the Run uses its
	// Authorizer's access (D31). Nil for any other Run.
	InvocationChannelID *uuid.UUID
	// ProjectID is the Project of the chat the Run answers into, set for a
	// Task chat. A Task Run's answer reaches that Project's readers only, so
	// it reads and files work only there (ADR 0017).
	ProjectID *uuid.UUID
	// RootAgent marks a Run of the Company's root Agent, the CEO Agent
	// that plans the Company. PlanningDisabled says that Agent has its
	// Company Plan tools switched off now. Both are read live with the
	// proof, so the server, not the daemon, decides whether the Run reads
	// as the Company's planner (IA-FR12).
	RootAgent        bool
	PlanningDisabled bool
}

// Actor is who a write this Run proves is attributed to (D31): its Agent,
// acting in this Run for its authorizer. A Run with no authorizer predates
// authorizers and is attributed to its Agent alone; no route admits its
// proof (runProofBearer), so only a direct service call reaches that case.
func (origin RunOrigin) Actor() events.Actor {
	if origin.AuthorizedByUserID == nil {
		return events.AgentActor(origin.AgentID.String())
	}
	return events.RunActor(origin.AgentID.String(), origin.RunID.String(), origin.AuthorizedByUserID.String())
}

type FailedRunBatch struct {
	Runs       []db.Run
	RetryCount int
}

type StaleRunOptions struct {
	DispatchTimeout time.Duration
	RunningTimeout  time.Duration
	RuntimeStale    time.Duration
}

type QueuedRunExpiryOptions struct {
	TTL      time.Duration
	MaxBatch int32
}

// RunService owns the run lifecycle.
type DefaultRunService struct {
	// RouteResolver is only needed by the transcript append path, whose
	// statement updates runs alone and so cannot join the conversation.
	RouteResolver ConversationRouteResolver

	Queries  *db.Queries
	Tx       Transactor
	Bus      *events.Bus
	Promoter RunQueuePromoter
	// InboxPresenter is optional presentation noise over durable Inbox rows.
	// Production wires it; focused lifecycle tests may leave it nil.
	InboxPresenter inbox.Presenter
	// TaskTerminalEffects is optional only for focused execution tests.
	// Production wires the Task-owned generation-safe status transition.
	TaskTerminalEffects TaskTerminalEffectWriter
	// ConnectorSnapshotter is optional for focused lifecycle tests. Production
	// wires it so every dispatched Run carries an explicit snapshot, including
	// the safe empty scope when no reviewed Connector is currently executable.
	ConnectorSnapshotter ConnectorSnapshotter
	// RunObserver is optional: production wires the metrics registry so
	// /metrics counts every newly persisted terminal outcome; a nil observer
	// is skipped.
	RunObserver RunObserver
	// EmptyClaim caches "this runtime has no queued run" so the daemon poll path can skip a Postgres scan on the steady-state empty case. Optional —
	// a nil cache disables the fast path and every claim goes through the DB.
	EmptyClaim *EmptyClaimCache
	// capWarned remembers which agents have already had their out-of-bounds
	// max_concurrent_runs reported, so agentTaskLaneCapacity warns once per
	// agent instead of once per poll. Its zero value is ready to use, so a
	// hand-built DefaultRunService needs nothing here.
	capWarned sync.Map
}

// RunServiceOptions supplies the lifecycle's database, event, routing, and
// daemon-notification dependencies.
type RunServiceOptions struct {
	// RouteResolver supplies the conversation facts the transcript append
	// path cannot read for itself.
	RouteResolver ConversationRouteResolver

	Queries              *db.Queries
	Transactor           Transactor
	Bus                  *events.Bus
	InboxPresenter       inbox.Presenter
	ConnectorSnapshotter ConnectorSnapshotter
	TaskTerminalEffects  TaskTerminalEffectWriter
	RunObserver          RunObserver
}

func NewRunService(options RunServiceOptions) *DefaultRunService {
	return &DefaultRunService{
		RouteResolver:        options.RouteResolver,
		Queries:              options.Queries,
		Tx:                   options.Transactor,
		Bus:                  options.Bus,
		InboxPresenter:       options.InboxPresenter,
		ConnectorSnapshotter: options.ConnectorSnapshotter,
		TaskTerminalEffects:  options.TaskTerminalEffects,
		RunObserver:          options.RunObserver,
	}
}

// ResolveRunOrigin resolves a minted run credential only while both the token
// and its originating Run are live. PostgreSQL owns expiry and status; the Go
// boundary validates only the credential's wire shape before hashing it.
func (s *DefaultRunService) ResolveRunOrigin(ctx context.Context, token string) (RunOrigin, error) {
	tokenHash, err := runTokenHash(token)
	if err != nil {
		return RunOrigin{}, err
	}
	row, err := s.Queries.ResolveLiveRunOrigin(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunOrigin{}, ErrRunProofInvalid
	}
	if err != nil {
		return RunOrigin{}, fmt.Errorf("resolve live Run proof: %w", err)
	}
	originUserID := row.OriginUserID
	if row.InvocationCallerID.Valid {
		originUserID = row.InvocationCallerID
	}
	if row.InvocationID.Valid {
		if err := AuthorizeChannelInvocationInTx(ctx, s.Queries, RunTrigger{CompanyID: row.CompanyID, ConversationID: row.ConversationID, AgentID: row.AgentID, InvocationID: row.InvocationID}); err != nil {
			if errors.Is(err, ErrChannelInvocationDenied) || errors.Is(err, ErrRunAgentNotBound) {
				return RunOrigin{}, ErrRunProofInvalid
			}
			return RunOrigin{}, err
		}
	}
	return RunOrigin{
		RunID:               row.RunID,
		CompanyID:           row.CompanyID,
		AgentID:             row.AgentID,
		TaskID:              util.NullUUIDPtr(row.TaskID),
		ConversationID:      row.ConversationID,
		ConversationKind:    row.ConversationKind,
		OriginUserID:        util.NullUUIDPtr(originUserID),
		AuthorizedByUserID:  util.NullUUIDPtr(row.ExecutionAuthorizedByUserID),
		InvocationChannelID: util.NullUUIDPtr(row.InvocationChannelID),
		ProjectID:           util.NullUUIDPtr(row.ConversationProjectID),
		RootAgent:           row.RootAgent,
		PlanningDisabled:    row.PlanningDisabled,
	}, nil
}

func runTokenHash(token string) ([]byte, error) {
	if len(token) != 68 || token[:4] != "brt_" {
		return nil, ErrRunProofInvalid
	}
	for _, value := range token[4:] {
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return nil, ErrRunProofInvalid
			}
		}
	}
	digest := sha256.Sum256([]byte(token))
	return digest[:], nil
}

// SetRunQueuePromoter wires the Conversation-owned queue materializer. A nil
// promoter leaves terminal transitions valid but disables queued-prompt
// promotion, which is useful only in focused execution tests.
func (s *DefaultRunService) SetRunQueuePromoter(promoter RunQueuePromoter) {
	s.Promoter = promoter
}

const (
	// claimResponseRecoveryWindow must exceed the daemon client timeout for
	// /runs/claim (30s) plus /runs/{id}/start (30s) plus scheduling slack.
	// Longer pre-start work is protected by prepareLeaseDuration instead of
	// stretching this global crash-recovery window.
	claimResponseRecoveryWindow = 90 * time.Second
	prepareLeaseDuration        = 45 * time.Second
)

// runInTx executes fn inside a single DB transaction. There is deliberately no
// "no transactor" fallback: running fn on the plain query set would turn a
// multi-statement flow into independent autocommits — FailRun + CreateRetryRun
// would leave a queued retry whose parent never reached a terminal state.
func (s *DefaultRunService) runInTx(ctx context.Context, fn func(*db.Queries) error) error {
	return s.runInTxWithOptions(ctx, pgx.TxOptions{}, fn)
}

// runInTxWithOptions is runInTx for a flow that names its isolation level
// instead of inheriting default_transaction_isolation. Both spellings go
// through the same missing-transactor check, which is the point: a flow that
// needs a transaction must never quietly run as autocommits.
func (s *DefaultRunService) runInTxWithOptions(
	ctx context.Context,
	options pgx.TxOptions,
	fn func(*db.Queries) error,
) error {
	if s.Tx == nil {
		return errors.New("run service is not wired to a transaction runner")
	}
	return s.Tx.WithinTx(ctx, options, fn)
}

func lockTerminalRunConversation(ctx context.Context, q *db.Queries, run db.Run) error {
	_, err := q.LockConversationForSystemRunQueueMutation(
		ctx,
		db.LockConversationForSystemRunQueueMutationParams{
			ConversationID: run.ConversationID,
			CompanyID:      run.CompanyID,
		},
	)
	if err != nil {
		return fmt.Errorf("lock terminal Run Conversation: %w", err)
	}
	return nil
}

func lockAndVerifyTerminalTranscript(
	ctx context.Context,
	q *db.Queries,
	runID uuid.UUID,
	proof TerminalTranscriptProof,
) (db.Run, error) {
	run, err := q.LockRunForTerminalTranscript(ctx, runID)
	if err != nil {
		return db.Run{}, fmt.Errorf("lock Run for terminal transcript: %w", err)
	}
	if run.TranscriptThroughSeq != proof.ThroughSeq {
		return db.Run{}, &TranscriptHeadMismatchError{
			RunID:            runID,
			ProofThroughSeq:  proof.ThroughSeq,
			ServerThroughSeq: run.TranscriptThroughSeq,
		}
	}
	return run, nil
}

// loadTerminalOutput reads the words a completed Run answered with. There is
// no copy to make: the text is already an 'agent_text' item of the one
// Conversation stream, committed when the daemon appended it, and this only
// selects which item the terminal event nominated.
func loadTerminalOutput(
	ctx context.Context,
	q *db.Queries,
	runID uuid.UUID,
	finalItemKey string,
) (string, error) {
	// An empty finalItemKey is the daemon's claim that the successful turn
	// produced no final agent text — the provider contract's explicit
	// no-output signal (a tool-only or reasoning-only turn). Nothing to load,
	// and nothing to record: a Run that said nothing simply has no
	// 'agent_text' item.
	if finalItemKey == "" {
		return "", nil
	}
	item, err := q.GetFinalAgentTextItem(
		ctx,
		db.GetFinalAgentTextItemParams{RunID: runID, ItemKey: finalItemKey},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTerminalFinalItemInvalid
	}
	if err != nil {
		return "", fmt.Errorf("load terminal agent text item: %w", err)
	}
	// content is the plain-text rendering the append already derived from the
	// item's text parts, so there is no parts document to re-decode here.
	return item.Content, nil
}

// terminalObservationInTx names what a terminal row this transaction just
// wrote will be counted as on /metrics: the bound runtime's provider, the
// outcome class, and for a failure whether it happened inside the provider
// or in the platform around it. The runtime is read with the
// removal-tolerant query because a Run keeps its runtime through a removal.
// Built inside the transaction and handed over only after commit, like the
// events beside it.
func terminalObservationInTx(
	ctx context.Context,
	q *db.Queries,
	run db.Run,
	outcome string,
) (telemetry.RunTerminalObservation, error) {
	runtime, err := q.GetRunRuntime(ctx, db.GetRunRuntimeParams{
		CompanyID: run.CompanyID,
		ID:        run.RuntimeID,
	})
	if err != nil {
		return telemetry.RunTerminalObservation{}, fmt.Errorf("read terminal Run runtime: %w", err)
	}
	observation := telemetry.RunTerminalObservation{Provider: runtime.Provider, Outcome: outcome}
	if outcome == "failed" {
		observation.FailureKind = "infrastructure"
		if runfailure.Reason(run.FailureReason.String).IsAgentError() {
			observation.FailureKind = "provider"
		}
	}
	return observation, nil
}

// observeRunTerminals hands committed terminal outcomes to the metrics
// registry when one is wired.
func (s *DefaultRunService) observeRunTerminals(observations ...telemetry.RunTerminalObservation) {
	if s.RunObserver == nil {
		return
	}
	for _, observation := range observations {
		s.RunObserver.ObserveRunTerminal(observation)
	}
}

func (s *DefaultRunService) promoteRunQueueInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	run db.Run,
	output string,
) (*db.Run, error) {
	if s.Promoter == nil {
		if output != "" {
			return nil, errors.New("completed Run output coordinator is not wired")
		}
		return nil, nil
	}
	promotedID, err := s.Promoter.FinalizeRunConversationInTx(
		ctx,
		q,
		pending,
		run,
		output,
	)
	if err != nil {
		return nil, fmt.Errorf("promote Conversation Run queue: %w", err)
	}
	if promotedID == nil {
		return nil, nil
	}
	promoted, err := q.GetRun(ctx, *promotedID)
	if err != nil {
		return nil, fmt.Errorf("load promoted Run: %w", err)
	}
	return &promoted, nil
}

func (s *DefaultRunService) applyTaskTerminalEffectInTx(
	ctx context.Context,
	queries *db.Queries,
	pending *[]events.Event,
	run db.Run,
) error {
	if !run.TaskID.Valid || s.TaskTerminalEffects == nil {
		return nil
	}
	if err := s.TaskTerminalEffects.ApplyRunTerminalInTx(
		ctx,
		queries,
		pending,
		run,
	); err != nil {
		return fmt.Errorf("apply terminal Run Task effect: %w", err)
	}
	return nil
}

func (s *DefaultRunService) publishRunQueuePromotion(
	ctx context.Context,
	pending []events.Event,
	promoted *db.Run,
) {
	for _, group := range events.GroupByTopic(pending) {
		s.Bus.Publish(ctx, group.Topic, group.Events...)
	}
	if promoted != nil {
		s.NotifyRunEnqueued(ctx, *promoted)
	}
}

func (s *DefaultRunService) promoteRunQueuesForTerminalRunsInTx(
	ctx context.Context,
	q *db.Queries,
	pending *[]events.Event,
	runs []db.Run,
) ([]db.Run, error) {
	byConversation := make(map[uuid.UUID]db.Run, len(runs))
	conversationIDs := make([]uuid.UUID, 0, len(runs))
	for _, run := range runs {
		if _, exists := byConversation[run.ConversationID]; exists {
			continue
		}
		byConversation[run.ConversationID] = run
		conversationIDs = append(conversationIDs, run.ConversationID)
	}
	sort.Slice(conversationIDs, func(i, j int) bool {
		return conversationIDs[i].String() < conversationIDs[j].String()
	})

	promoted := make([]db.Run, 0, len(conversationIDs))
	for _, conversationID := range conversationIDs {
		run := byConversation[conversationID]
		if err := lockTerminalRunConversation(ctx, q, run); err != nil {
			return nil, err
		}
		queued, err := s.promoteRunQueueInTx(ctx, q, pending, run, "")
		if err != nil {
			return nil, err
		}
		if queued != nil {
			promoted = append(promoted, *queued)
		}
	}
	return promoted, nil
}

func (s *DefaultRunService) publishRunQueuePromotions(
	ctx context.Context,
	pending []events.Event,
	promoted []db.Run,
) {
	for _, group := range events.GroupByTopic(pending) {
		s.Bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, run := range promoted {
		s.NotifyRunEnqueued(ctx, run)
	}
}

// ---------------------------------------------------------------------------
// Claim
// ---------------------------------------------------------------------------

// MinTaskLaneCapacity and MaxTaskLaneCapacity bound agents.max_concurrent_runs.
// They live next to the gate that acts on the number, and the request binder's
// named `maxConcurrentRuns` rule reads them from here, so one rule has one
// author. The binder remains the only thing that REFUSES a value out of these
// bounds; what the gate does with a value that never came through the binder is
// clamp it and say so — see agentTaskLaneCapacity.
const (
	MinTaskLaneCapacity = 1
	MaxTaskLaneCapacity = 10
)

// claimTxOptions pins the claim transaction's isolation level. The capacity
// gate's correctness depends on per-statement snapshots: the count is its own
// statement AFTER the agent-row lock is granted precisely so it sees the
// dispatch a competing claimer committed while this transaction waited. Under
// REPEATABLE READ that count is answered from the snapshot taken before the
// lock wait, so the two-claims-one-slot race comes straight back — and the
// lock is still there, making it look handled. pgx maps a zero TxOptions to a
// bare BEGIN, which takes whatever default_transaction_isolation happens to
// be, so this names the level out loud like every other write path that
// reasons about its own snapshot.
var claimTxOptions = pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite}

// agentTaskLaneCapacity turns the agent's stored cap into a slot count the gate
// can act on.
//
// The 1..10 bound is the request binder's rule and nothing here restates it as
// a rule. But a stored 0 or a negative — which only a write that went around
// the binder can produce — would close that agent's task lane forever: every
// claim would answer "nothing to do", the runs would sit queued, and there
// would be no line anywhere saying why. A silent permanent stall is the worst
// failure this gate can have, so it is clamped to something workable and
// reported once per agent per process. Once, because the claim path runs on
// every poll and a per-poll WARN is a log nobody reads.
func (s *DefaultRunService) agentTaskLaneCapacity(agentID uuid.UUID, stored int32) int32 {
	if stored >= MinTaskLaneCapacity && stored <= MaxTaskLaneCapacity {
		return stored
	}
	clamped := min(max(stored, MinTaskLaneCapacity), MaxTaskLaneCapacity)
	if _, alreadyWarned := s.capWarned.LoadOrStore(agentID, struct{}{}); !alreadyWarned {
		slog.Warn(
			"agent max_concurrent_runs is outside the accepted bounds; clamping",
			"agent_id", agentID.String(),
			"stored", stored,
			"using", clamped,
			"min", MinTaskLaneCapacity,
			"max", MaxTaskLaneCapacity,
		)
	}
	return clamped
}

// ClaimRun claims the next runnable run for (agent, runtime).
//
// Per-candidate serialization — one active run per (task, agent), one active
// run per direct conversation — lives in the ClaimRun SQL's NOT EXISTS arms.
// The per-agent capacity gate (agents.max_concurrent_runs, task lane only)
// cannot join them: a bare count arm inside the claim statement evaluates on
// that statement's own snapshot, so two concurrent claims would both see the
// last free slot and both dispatch past the cap. The claim is therefore a
// READ COMMITTED transaction:
//
//  1. lock the agent row FOR NO KEY UPDATE;
//  2. count active task-lane runs in a fresh statement under that lock;
//  3. run the claim statement with the verdict as its @task_lane_open arm.
//
// The lock mode is load-bearing. FOR NO KEY UPDATE conflicts with itself, so
// two claims for the SAME agent serialize and the second one's count sees the
// first one's dispatch — the mutual exclusion the gate needs. It also
// conflicts with a plain UPDATE of the agent row, so a cap edit cannot land
// between reading the cap and acting on it. It does NOT conflict with FOR KEY
// SHARE, which is what every INSERT INTO runs takes on the agents row through
// runs_company_agent_fkey and holds for the rest of that transaction. FOR
// UPDATE would conflict with it, and then a user's open chat-send transaction
// would block the daemon's claim on its very first statement — and since
// ClaimRunsForRuntimes claims sequentially, that one wait would stall every
// later (runtime, agent) pair in the batch, including agents on other
// runtimes. Claims for other agents never touch this row at all.
//
// At capacity the claim still runs — it just only sees direct-lane candidates,
// which neither wait for a slot nor occupy one. Task-lane candidates stay
// 'queued' untouched; NotifyRunFinished on the next terminal transition is the
// wakeup that revisits them.
func (s *DefaultRunService) ClaimRun(ctx context.Context, agentID, runtimeID uuid.UUID) (*db.Run, error) {
	start := time.Now()
	outcome := "unknown"
	defer func() {
		if totalMs := time.Since(start).Milliseconds(); totalMs >= 300 {
			slog.Info(
				"claim_run slow",
				"agent_id", agentID.String(),
				"runtime_id", runtimeID.String(),
				"outcome", outcome,
				"total_ms", totalMs,
			)
		}
	}()

	var claimed *db.Run
	var historyEvents []events.Event
	var unavailableSnapshotRunID uuid.UUID
	taskLaneClosed := false
	err := s.runInTxWithOptions(ctx, claimTxOptions, func(q *db.Queries) error {
		// The binding's claim lock, BEFORE the count and BEFORE the candidate
		// SELECT. Its scope is one (agent, runtime) pair — capacity is per
		// binding (ADR 0014) — and its span is this transaction;
		// releasing at commit is what makes the next claimer's count see the
		// run this one dispatched. It waits on no other writer — see the
		// query's comment for why it is advisory and not a row lock.
		if err := q.LockAgentClaimSlot(ctx, db.LockAgentClaimSlotParams{AgentID: agentID, RuntimeID: runtimeID}); err != nil {
			return fmt.Errorf("lock agent for claim: %w", err)
		}
		storedCapacity, err := q.GetAgentClaimCapacity(ctx, agentID)
		if err != nil {
			// The agent can vanish between the candidate listing and this
			// claim (company deletion cascades); nothing left to claim then.
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("read agent claim capacity: %w", err)
		}
		activeTaskLaneRuns, err := q.CountActiveTaskLaneRuns(ctx, db.CountActiveTaskLaneRunsParams{AgentID: agentID, RuntimeID: runtimeID})
		if err != nil {
			return fmt.Errorf("count active task-lane runs: %w", err)
		}
		taskLaneOpen := activeTaskLaneRuns < int64(s.agentTaskLaneCapacity(agentID, storedCapacity))
		taskLaneClosed = !taskLaneOpen

		fence := claimCandidateParams()
		candidate, err := q.SelectClaimableRun(ctx, db.SelectClaimableRunParams{
			AgentID: agentID, RuntimeID: runtimeID, TaskLaneOpen: taskLaneOpen,
			ExecutePermission:         fence.ExecutePermission,
			ReadPermission:            fence.ReadPermission,
			CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("select Run for claim: %w", err)
		}
		beforeHistory, err := activity.ObserveInTx(ctx, q, Topic, candidate.ID)
		if err != nil {
			return err
		}
		run, err := q.ClaimRun(ctx, db.ClaimRunParams{
			RunID:            candidate.ID,
			AgentID:          agentID,
			RuntimeID:        runtimeID,
			PrepareLeaseSecs: prepareLeaseDuration.Seconds(),
		})
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return fmt.Errorf("claim run: %w", err)
		}
		if s.ConnectorSnapshotter != nil {
			claimedRunID := run.ID
			frozen, freezeErr := s.ConnectorSnapshotter.FreezeRunConnectorSnapshotInTx(
				ctx,
				q,
				claimedRunID,
				runtimeID,
			)
			if freezeErr != nil {
				if errors.Is(freezeErr, ErrRunConnectorSnapshotUnavailable) {
					unavailableSnapshotRunID = claimedRunID
				}
				return fmt.Errorf("freeze claimed Run Connector snapshot: %w", freezeErr)
			}
			run = frozen
		}
		run, err = refreshChannelRunContext(ctx, q, run)
		if err != nil {
			return err
		}
		claimed = &run
		return recordRunChangeInTx(ctx, q, &historyEvents, events.SystemActor(), EventRunDispatched, beforeHistory, run)
	})
	if err != nil {
		if errors.Is(err, ErrRunConnectorSnapshotUnavailable) && unavailableSnapshotRunID != uuid.Nil {
			outcome = "connector_unavailable"
			if cancelErr := s.cancelQueuedRunAfterConnectorSnapshotFailure(
				ctx,
				unavailableSnapshotRunID,
			); cancelErr != nil && !errors.Is(cancelErr, pgx.ErrNoRows) {
				return nil, fmt.Errorf(
					"cancel Run with unavailable Connector snapshot: %w",
					cancelErr,
				)
			}
			return nil, nil
		}
		outcome = "error_claim"
		return nil, err
	}
	for _, group := range events.GroupByTopic(historyEvents) {
		s.Bus.Publish(ctx, group.Topic, group.Events...)
	}
	if claimed == nil {
		// A throttled agent and an idle one both return no run, and telling
		// them apart from the outside is otherwise guesswork: the slow log
		// names which one it was.
		if taskLaneClosed {
			slog.Debug("run claim: agent is at task-lane capacity", "agent_id", agentID.String())
			outcome = "at_capacity"
			return nil, nil
		}
		slog.Debug("run claim: no runs available", "agent_id", agentID.String())
		outcome = "no_runs"
		return nil, nil
	}

	slog.Info("run claimed", "run_id", claimed.ID, "agent_id", agentID.String())
	// Post-commit, exactly as the single-statement claim behaved: an event
	// must never announce a dispatch the transaction could still roll back.
	outcome = "claimed"
	return claimed, nil
}

// ClaimRunsForRuntimes claims up to maxRuns runs across every runtime in
// runtimeIDs in one call, so a daemon can poll for all of its runtimes with a
// constant number of DB queries. It preserves the per-runtime semantics,
// set-ified:
//  1. promote due deferred runs across the set (one UPDATE);
//  2. reclaim stale-dispatched runs across the set (one UPDATE) — before the
//     empty-cache check because a lost claim response moves the run out of
//     `queued`, which the empty-queued cache cannot represent;
//  3. short-circuit runtimes whose empty-claim verdict is cached, sampling
//     the invalidation version for the rest BEFORE the candidate SELECT;
//  4. list queued candidates across the non-empty set (one SELECT);
//  5. mark still-empty runtimes so their next idle poll skips Postgres;
//  6. claim per distinct (runtime, agent) via ClaimRun until maxRuns.
func (s *DefaultRunService) ClaimRunsForRuntimes(
	ctx context.Context,
	runtimeIDs []uuid.UUID,
	maxRuns int,
) ([]db.Run, error) {
	if len(runtimeIDs) == 0 || maxRuns <= 0 {
		return nil, nil
	}

	// De-dup runtime IDs defensively so MarkEmpty/version bookkeeping stays
	// unambiguous even if a daemon ever sends a duplicate.
	seen := make(map[string]struct{}, len(runtimeIDs))
	uniqueIDs := make([]uuid.UUID, 0, len(runtimeIDs))
	for _, rid := range runtimeIDs {
		key := rid.String()
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		uniqueIDs = append(uniqueIDs, rid)
	}

	claimed := make([]db.Run, 0, maxRuns)

	// 1. Promote due deferred runs across the whole set (promote-first, like
	// the singular path) so a just-promoted run invalidates its runtime's
	// cached empty verdict BEFORE the empty-cache filter in step 3.
	if err := s.PromoteDueDeferredRunsForRuntimes(ctx, uniqueIDs); err != nil {
		return nil, err
	}

	// 2. Reclaim lost-response dispatched runs across the set, capped at the
	// batch the daemon can accept so undelivered rows keep their expired
	// fence and stay immediately reclaimable on the next poll.
	reclaimed, err := s.Queries.ReclaimStaleDispatchedRunsForRuntimes(
		ctx,
		db.ReclaimStaleDispatchedRunsForRuntimesParams{
			RuntimeIds:        uniqueIDs,
			ClaimRecoverySecs: claimResponseRecoveryWindow.Seconds(),
			PrepareLeaseSecs:  prepareLeaseDuration.Seconds(),
			MaxRuns:           int32(maxRuns),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("reclaim stale dispatched runs: %w", err)
	}
	for i := range reclaimed {
		if len(claimed) >= maxRuns {
			break
		}
		claimed = append(claimed, reclaimed[i])
		slog.Info(
			"stale dispatched run reclaimed (batch)",
			"run_id", reclaimed[i].ID,
			"runtime_id", reclaimed[i].RuntimeID.String(),
			"agent_id", reclaimed[i].AgentID.String(),
		)
	}
	if len(claimed) >= maxRuns {
		return claimed[:maxRuns], nil
	}

	// 3. Empty-cache short-circuit + version sampling for the remaining runtimes.
	nonEmpty := make([]uuid.UUID, 0, len(uniqueIDs))
	versions := make(map[string]int64, len(uniqueIDs))
	for _, rid := range uniqueIDs {
		key := rid.String()
		if s.EmptyClaim.IsEmpty(ctx, key) {
			continue
		}
		versions[key] = s.EmptyClaim.CurrentVersion(ctx, key)
		nonEmpty = append(nonEmpty, rid)
	}
	if len(nonEmpty) == 0 {
		return claimed, nil
	}

	// 4. One candidate SELECT across the non-empty set.
	candidates, err := s.Queries.ListQueuedClaimCandidatesByRuntimes(ctx, claimCandidateParams(nonEmpty...))
	if err != nil {
		// Step 2 committed reclaimed runs in its own statement, so `claimed`
		// may already hold runs dispatched server-side. Dropping them with an
		// error would make the daemon claim a SECOND batch into the same free
		// slots. Prefer partial success: hand back what committed; the errored
		// candidates stay queued for the next poll.
		if len(claimed) > 0 {
			slog.Error(
				"batch claim: candidate query failed after partial success; returning claimed runs to avoid loss",
				"error",
				err,
				"claimed",
				len(claimed),
			)
			return claimed, nil
		}
		return nil, fmt.Errorf("list queued claim candidates: %w", err)
	}

	// 5. Mark runtimes with zero candidates empty so their next idle poll
	// skips Postgres. Runtimes that had at least one candidate are
	// intentionally not marked (positive results always re-check the DB,
	// matching the singular path).
	withCandidates := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		withCandidates[candidates[i].RuntimeID.String()] = struct{}{}
	}
	for _, rid := range nonEmpty {
		key := rid.String()
		if _, ok := withCandidates[key]; !ok {
			s.EmptyClaim.MarkEmpty(ctx, key, versions[key])
		}
	}

	// 6. Claim per distinct (runtime, agent) pair (unchanged path → same
	// serialization arms and dispatch side effects) until maxRuns is reached.
	// The claim itself is runtime-scoped, so no cross-daemon stray-claim guard
	// is needed. This loop is sequential, so anything one pair waits on delays
	// every pair after it — which is why the capacity gate's agent-row lock is
	// FOR NO KEY UPDATE and cannot be blocked by an open enqueue transaction
	// (see ClaimRun). What remains is bounded and per-agent: a claim waits
	// only on another claim for the same agent, or on an edit of that agent's
	// row. An at-capacity agent does not wait at all — it yields (nil, nil)
	// and the loop moves on. Its runtime is also not marked empty (step 5 keys
	// off candidates, and the blocked task run is one), so the next poll
	// re-checks the DB.
	tried := make(map[string]struct{}, len(candidates))
	for i := range candidates {
		if len(claimed) >= maxRuns {
			break
		}
		pairKey := candidates[i].RuntimeID.String() + "|" + candidates[i].AgentID.String()
		if _, dup := tried[pairKey]; dup {
			continue
		}
		tried[pairKey] = struct{}{}

		run, err := s.ClaimRun(ctx, candidates[i].AgentID, candidates[i].RuntimeID)
		if err != nil {
			// Each ClaimRun commits on its own, so earlier iterations (and
			// step-2 reclaims) are already dispatched server-side. Return the
			// partial batch instead of dropping them; the failed pair's run
			// stays queued.
			if len(claimed) > 0 {
				slog.Error(
					"batch claim: claim run failed after partial success; returning claimed runs to avoid loss",
					"error",
					err,
					"claimed",
					len(claimed),
				)
				return claimed, nil
			}
			return nil, fmt.Errorf("claim run: %w", err)
		}
		if run == nil {
			continue
		}
		claimed = append(claimed, *run)
	}

	return claimed, nil
}

// FinalizeRunClaim persists the run-scoped token minted for a claim. The
// handler must call this only after the full response payload has been built
// and before writing any response bytes; a failure means the claim must be
// returned to the queue via RequeueRunAfterClaimFailure.
func (s *DefaultRunService) FinalizeRunClaim(ctx context.Context, token db.CreateRunTokenParams) error {
	if _, err := s.Queries.CreateRunToken(ctx, token); err != nil {
		return fmt.Errorf("create run token: %w", err)
	}
	return nil
}

// RequeueRunAfterClaimFailure immediately releases an exact dispatched claim
// whose payload finalization failed before the HTTP response was written. The
// SQL CAS includes dispatched_at so a late handler cannot roll back a newer
// reclaim. This is not a fresh enqueue.
func (s *DefaultRunService) RequeueRunAfterClaimFailure(ctx context.Context, run db.Run) (*db.Run, error) {
	requeued, err := s.changeRun(ctx, run.ID, EventRunQueued, func(q *db.Queries) (db.Run, error) {
		return q.RequeueRunAfterClaimFailure(ctx, db.RequeueRunAfterClaimFailureParams{ID: run.ID, RuntimeID: run.RuntimeID, UpdatedAt: run.UpdatedAt})
	})
	if err != nil {
		return nil, fmt.Errorf("requeue run after claim failure: %w", err)
	}

	PublishRuntimeWork(context.Background(), s.Bus, requeued)
	slog.Info(
		"run requeued after claim finalization failure",
		"run_id", requeued.ID,
		"runtime_id", requeued.RuntimeID.String(),
	)
	return &requeued, nil
}

// PromoteDueDeferredRunsForRuntimes flips due deferred runs to queued and
// replays the enqueue side effects per row — crucially EmptyClaim.Bump (via
// NotifyRunEnqueued → notifyRunAvailable) so a just-promoted run invalidates
// its runtime's cached empty verdict, plus the deferred→queued UI event.
func (s *DefaultRunService) PromoteDueDeferredRunsForRuntimes(ctx context.Context, runtimeIDs []uuid.UUID) error {
	if len(runtimeIDs) == 0 {
		return nil
	}
	runs, err := s.promoteDueDeferredRuns(ctx, runtimeIDs)
	if err != nil {
		return fmt.Errorf("promote due deferred runs: %w", err)
	}
	s.replayDeferredPromotion(ctx, runs)
	return nil
}

// PromoteDueDeferredRuns is the sweeper arm of deferred promotion: every
// runtime, not just the ones a daemon is polling for. Without it a deferred
// retry whose daemon never polls again (machine retired) counts as its
// conversation's current run forever — the same silent-wedge class as an
// orphaned run, but with no sweeper naming it.
func (s *DefaultRunService) PromoteDueDeferredRuns(ctx context.Context) ([]db.Run, error) {
	runs, err := s.promoteDueDeferredRuns(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("promote due deferred runs: %w", err)
	}
	s.replayDeferredPromotion(ctx, runs)
	return runs, nil
}

// replayDeferredPromotion replays the enqueue side effects for freshly
// promoted rows — crucially EmptyClaim.Bump (via NotifyRunEnqueued →
// notifyRunAvailable) so a just-promoted run invalidates its runtime's cached
// empty verdict, plus the deferred→queued UI event.
func (s *DefaultRunService) replayDeferredPromotion(ctx context.Context, runs []db.Run) {
	for _, run := range runs {
		slog.Info(
			"deferred run promoted",
			"run_id", run.ID,
			"runtime_id", run.RuntimeID.String(),
			"agent_id", run.AgentID.String(),
		)

		s.NotifyRunEnqueued(ctx, run)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle transitions (called from the daemon handlers)
// ---------------------------------------------------------------------------

// StartRun transitions a dispatched run to running. The transition is fenced
// by runtimeID, not by the run id alone (bug R2): a run is started by the
// machine that claimed it, and a daemon still holding a stale id must not be
// able to start work another runtime now owns.
func (s *DefaultRunService) StartRun(
	ctx context.Context,
	runID uuid.UUID,
	runtimeID uuid.UUID,
) (*db.Run, error) {
	run, err := s.changeRun(ctx, runID, EventRunRunning, func(q *db.Queries) (db.Run, error) {
		return q.StartRun(ctx, db.StartRunParams{ID: runID, RuntimeID: runtimeID})
	})
	if err != nil {
		return nil, fmt.Errorf("start run: %w", err)
	}

	slog.Info(
		"run started",
		"run_id",
		run.ID,
		"conversation_id",
		run.ConversationID.String(),
	)
	// Tell every connected company WS client that this run transitioned
	// (dispatched | waiting_local_directory) → running so any UI that
	// distinguishes "queued" from "running" updates immediately.

	return &run, nil
}

// ExtendRunPrepareLease keeps a claimed-but-not-started run protected while
// the daemon resolves cached inputs and prepares the execution environment.
func (s *DefaultRunService) ExtendRunPrepareLease(
	ctx context.Context,
	runID uuid.UUID,
	runtimeID uuid.UUID,
) error {
	if _, err := s.Queries.ExtendRunPrepareLease(ctx, db.ExtendRunPrepareLeaseParams{
		ID:               runID,
		RuntimeID:        runtimeID,
		PrepareLeaseSecs: prepareLeaseDuration.Seconds(),
	}); err != nil {
		return fmt.Errorf("extend run prepare lease: %w", err)
	}
	return nil
}

// MarkRunWaitingLocalDirectory parks a dispatched run in the
// waiting_local_directory state while the daemon waits for another in-flight
// run to release the local-directory path lock. The persisted reason is a
// closed code; the daemon keeps the contested path local.
func (s *DefaultRunService) MarkRunWaitingLocalDirectory(
	ctx context.Context,
	runID uuid.UUID,
) (*db.Run, error) {
	run, err := s.changeRun(ctx, runID, EventRunWaitingLocalDirectory, func(q *db.Queries) (db.Run, error) {
		return q.MarkRunWaitingLocalDirectory(ctx, db.MarkRunWaitingLocalDirectoryParams{ID: runID, PrepareLeaseSecs: prepareLeaseDuration.Seconds()})
	})
	if err != nil {
		return nil, fmt.Errorf("mark run waiting_local_directory: %w", err)
	}

	slog.Info(
		"run waiting_local_directory",
		"run_id", run.ID,
		"conversation_id", run.ConversationID.String(),
	)

	return &run, nil
}

// CompleteRun marks a run as completed after proving the exact committed
// transcript prefix and deriving the Conversation output from its final
// normalized agent text item. A completed callback may be replayed with the
// same proof.
func (s *DefaultRunService) CompleteRun(
	ctx context.Context,
	runID uuid.UUID,
	proof TerminalTranscriptProof,
	sessionID string,
	sessionRolloutMissing bool,
	retiredSessionID string,
) (*db.Run, error) {
	base, err := s.Queries.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load Run before completion: %w", err)
	}
	var (
		run          db.Run
		transitioned bool
		pending      []events.Event
		promoted     *db.Run
		inboxResult  inbox.CreateResult
		observation  telemetry.RunTerminalObservation
	)
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		// Account deletion and every membership mutation take the Company before
		// membership and owned-graph locks. Terminal promotion later locks the
		// founder membership, so join that order before taking Conversation/Run.
		if _, err := qtx.LockCompanyForMembershipRead(ctx, base.CompanyID); err != nil {
			return fmt.Errorf("lock Company for terminal completion: %w", err)
		}
		inboxTargets, err := lockRunInboxTargets(ctx, qtx, []db.Run{base})
		if err != nil {
			return err
		}
		if err := lockTerminalRunConversation(ctx, qtx, base); err != nil {
			return err
		}
		locked, err := lockAndVerifyTerminalTranscript(ctx, qtx, runID, proof)
		if err != nil {
			return err
		}
		output, err := loadTerminalOutput(ctx, qtx, runID, proof.FinalItemKey)
		if err != nil {
			return err
		}
		if runfailure.ContextExhaustedCompletion(output) {
			// Abort before any write: the transaction rolls back and the
			// handler re-submits this terminal observation as a failure.
			return ErrRunContextExhausted
		}
		beforeHistory, err := activity.ObserveInTx(ctx, qtx, Topic, runID)
		if err != nil {
			return err
		}
		completed, completeErr := qtx.CompleteRun(ctx, db.CompleteRunParams{
			ID:                    runID,
			SessionID:             sessionID,
			SessionRolloutMissing: sessionRolloutMissing,
			RetiredSessionID:      retiredSessionID,
		})
		if errors.Is(completeErr, pgx.ErrNoRows) {
			if locked.Status != "completed" && locked.Status != "cancelled" {
				return completeErr
			}
			run = locked
			return nil
		}
		if completeErr != nil {
			return completeErr
		}
		run = completed
		if err := qtx.RetainOutputRunCompletion(ctx, run.ID); err != nil {
			return fmt.Errorf("retain Output Run completion: %w", err)
		}
		if err := recordRunChangeInTx(ctx, qtx, &pending, events.SystemActor(), EventRunCompleted, beforeHistory, run); err != nil {
			return err
		}
		transitioned = true
		// An empty final item key is the daemon's word that the turn ended
		// without agent text — the metrics count it apart from a reply.
		outcome := "message"
		if proof.FinalItemKey == "" {
			outcome = "no_response"
		}
		if observation, err = terminalObservationInTx(ctx, qtx, run, outcome); err != nil {
			return err
		}
		promoted, completeErr = s.promoteRunQueueInTx(ctx, qtx, &pending, run, output)
		if completeErr != nil {
			return completeErr
		}
		if err := s.applyTaskTerminalEffectInTx(ctx, qtx, &pending, run); err != nil {
			return err
		}
		target, ok := inboxTargets[run.ID]
		if !ok {
			return nil
		}
		inboxResult, completeErr = createRunTerminalInboxItem(
			ctx,
			qtx,
			&pending,
			run,
			target,
		)
		return completeErr
	})
	if err != nil {
		if existing, lookupErr := s.Queries.GetRun(ctx, runID); lookupErr == nil {
			slog.Warn(
				"complete run failed",
				"run_id", runID,
				"current_status", existing.Status,
				"error", err,
			)
		} else {
			slog.Warn(
				"complete run failed: run not found",
				"run_id", runID,
				"lookup_error", lookupErr,
			)
		}
		return nil, fmt.Errorf("complete run: %w", err)
	}
	if !transitioned {
		slog.Info(
			"complete run: already finalized",
			"run_id", runID,
			"current_status", run.Status,
			"agent_id", run.AgentID.String(),
		)
		return &run, nil
	}

	slog.Info(
		"run completed",
		"run_id",
		run.ID,
		"conversation_id",
		run.ConversationID.String(),
	)

	s.publishRunQueuePromotion(ctx, pending, promoted)
	s.observeRunTerminals(observation)
	inbox.PresentCreated(s.InboxPresenter, inboxResult)
	return &run, nil
}

// FailRun marks a run as failed and, for infrastructure-shaped failures with
// remaining attempt budget, creates the auto-retry child inside the SAME
// transaction as the fail so no newer conversation run can jump ahead of it.
//
// failureReason is a coarse classifier consumed by the auto-retry path. Pass
// "" when unknown — the raw error text runs through runfailure.Classify so
// the persisted failure_reason still lands in the canonical taxonomy.
func (s *DefaultRunService) FailRun(
	ctx context.Context,
	runID uuid.UUID,
	proof TerminalTranscriptProof,
	errMsg, sessionID, failureReason string,
	sessionRolloutMissing bool,
	retiredSessionID string,
) (*db.Run, error) {
	base, err := s.Queries.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load Run before failure: %w", err)
	}
	if failureReason == "" {
		failureReason = runfailure.Classify(errMsg).String()
	}
	// The daemon plane is authenticated, but provider diagnostics are still
	// untrusted external text. Redact again at the database authority boundary
	// so a buggy or older daemon cannot persist credentials in runs.error.
	errMsg = redact.Diagnostic(errMsg)

	// Pre-compute the auto-retry so the retry child can be created inside the
	// SAME transaction as the fail.
	var (
		wantRetry   bool
		retryFresh  bool
		retryFireAt pgtype.Timestamptz
	)
	if retryableReasons[failureReason] {
		if retryEligible(failureReason, base) {
			wantRetry = true
			retryFresh = ResumeUnsafeFailure(failureReason, errMsg)
			// Defer this attempt when the reason's schedule calls for a
			// backoff (provider_network's final attempt waits ~5s); a zero
			// delay leaves fire_at NULL so the child is created
			// immediately-claimable.
			if delay := retryDelayForAttempt(failureReason, base.Attempt); delay > 0 {
				retryFireAt = pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true}
			}
		}
	}

	var (
		pending     []events.Event
		promoted    *db.Run
		inboxResult inbox.CreateResult
		observation telemetry.RunTerminalObservation
	)
	failTx := func(withRetry bool) (db.Run, *db.Run, error) {
		var run db.Run
		var retried *db.Run
		var txPending []events.Event
		var txPromoted *db.Run
		var txInboxResult inbox.CreateResult
		var txObservation telemetry.RunTerminalObservation
		err := s.runInTx(ctx, func(qtx *db.Queries) error {
			inboxTargets, err := lockRunInboxTargets(ctx, qtx, []db.Run{base})
			if err != nil {
				return err
			}
			if err := lockTerminalRunConversation(ctx, qtx, base); err != nil {
				return err
			}
			locked, err := lockAndVerifyTerminalTranscript(ctx, qtx, runID, proof)
			if err != nil {
				return err
			}
			// Cancellation can win while the daemon is sending its terminal
			// callback. Acknowledge the final state without creating a retry
			// or replacing the cancellation's history and attribution.
			if locked.Status == "cancelled" {
				run = locked
				return nil
			}
			beforeHistory, err := activity.ObserveInTx(ctx, qtx, Topic, runID)
			if err != nil {
				return err
			}
			t, err := qtx.FailRun(ctx, db.FailRunParams{
				ID:                    runID,
				Error:                 pgtype.Text{String: errMsg, Valid: true},
				FailureReason:         failureReason,
				SessionID:             sessionID,
				SessionRolloutMissing: sessionRolloutMissing,
				RetiredSessionID:      retiredSessionID,
			})
			if err != nil {
				return err
			}
			run = t
			if txObservation, err = terminalObservationInTx(ctx, qtx, run, "failed"); err != nil {
				return err
			}

			if withRetry {
				child, cerr := qtx.CreateRetryRun(ctx, db.CreateRetryRunParams{
					ParentRunID:       runID,
					FireAt:            retryFireAt,
					ForceFreshSession: retryFresh,
				})
				switch {
				case errors.Is(cerr, pgx.ErrNoRows):
					// The Task no longer admits this automatic retry: a newer
					// generation, handoff, status change, or unresolved
					// dependency wins over the failed Run.
					retried = nil
				case cerr != nil:
					return fmt.Errorf("create retry run: %w", cerr)
				default:
					retry := db.Run(child)
					retried = &retry
					if err := recordRunChangeInTx(ctx, qtx, &txPending, events.SystemActor(), EventRunQueued, nil, retry); err != nil {
						return err
					}
				}
			}
			if err := recordRunChangeInTx(ctx, qtx, &txPending, events.SystemActor(), EventRunFailed, beforeHistory, run); err != nil {
				return err
			}
			txPromoted, err = s.promoteRunQueueInTx(ctx, qtx, &txPending, run, "")
			if err != nil {
				return err
			}
			if retried == nil {
				if err := s.applyTaskTerminalEffectInTx(ctx, qtx, &txPending, run); err != nil {
					return err
				}
			}
			target, ok := inboxTargets[run.ID]
			if !ok {
				return nil
			}
			txInboxResult, err = createRunTerminalInboxItem(
				ctx,
				qtx,
				&txPending,
				run,
				target,
			)
			return err
		})
		if err == nil {
			pending = txPending
			promoted = txPromoted
			inboxResult = txInboxResult
			observation = txObservation
		}
		return run, retried, err
	}

	run, retried, err := failTx(wantRetry)
	// A concurrent enqueue can already hold the (task, agent) pending slot the
	// retry child needs; that conflict must not wedge the fail itself, so
	// re-run the terminal transaction without the child.
	if err != nil && wantRetry && isDuplicatePendingRunErr(err) {
		slog.Debug("fail run auto-retry coalesced: pending run already exists",
			"run_id", runID)
		run, retried, err = failTx(false)
	}
	if err != nil {
		if existing, lookupErr := s.Queries.GetRun(ctx, runID); lookupErr == nil {
			if errors.Is(err, pgx.ErrNoRows) && existing.Status == "failed" {
				slog.Info(
					"fail run: already finalized",
					"run_id", runID,
					"current_status", existing.Status,
					"agent_id", existing.AgentID.String(),
				)
				return &existing, nil
			}
			slog.Warn(
				"fail run failed",
				"run_id", runID,
				"current_status", existing.Status,
				"error", err,
			)
		} else {
			slog.Warn(
				"fail run failed: run not found",
				"run_id", runID,
				"lookup_error", lookupErr,
			)
		}
		return nil, fmt.Errorf("fail run: %w", err)
	}

	if run.Status == "cancelled" {
		return &run, nil
	}
	slog.Warn("run failed", "run_id", run.ID, "error", errMsg, "failure_reason", failureReason)

	s.publishRunQueuePromotion(ctx, pending, promoted)
	s.observeRunTerminals(observation)
	// Surface the retry child now: broadcast queued first, then notify the
	// daemon — bump-before-wakeup ordering. A deferred child (backoff armed
	// via fire_at) is NOT queued yet: PromoteDueDeferredRunsForRuntimes emits
	// its queued event and daemon wakeup when fire_at arrives.
	if retried != nil {
		slog.Info(
			"run auto-retry enqueued",
			"parent_run_id", run.ID,
			"child_run_id", retried.ID,
			"reason", failureReason,
			"attempt", retried.Attempt,
			"max_attempts", MaxAttemptsFor(failureReason, retried.Attempt),
			"status", retried.Status,
		)
		if retried.Status == "queued" {
			s.NotifyRunEnqueued(ctx, *retried)
		}
	}

	inbox.PresentCreated(s.InboxPresenter, inboxResult)
	return &run, nil
}

// ---------------------------------------------------------------------------
// Cancel
// ---------------------------------------------------------------------------

// cancelQueuedRunAfterConnectorSnapshotFailure terminally projects the exact
// queued attempt whose claim-time Connector freeze rolled back. The
// Conversation lock serializes the terminal projection with other lifecycle
// writers; the SQL CAS leaves a newer dispatch or frozen-and-requeued claim
// untouched.
func (s *DefaultRunService) cancelQueuedRunAfterConnectorSnapshotFailure(
	ctx context.Context,
	runID uuid.UUID,
) error {
	base, err := s.Queries.GetRun(ctx, runID)
	if err != nil {
		return fmt.Errorf("load Run before Connector snapshot cancellation: %w", err)
	}
	var (
		run          db.Run
		transitioned bool
		pending      []events.Event
		promoted     *db.Run
	)
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		if err := lockTerminalRunConversation(ctx, qtx, base); err != nil {
			return err
		}
		beforeHistory, err := ObserveRunsInTx(ctx, qtx, []uuid.UUID{runID})
		if err != nil {
			return err
		}
		cancelled, cancelErr := qtx.CancelQueuedRunAfterConnectorSnapshotFailure(
			ctx,
			db.CancelQueuedRunAfterConnectorSnapshotFailureParams{
				RunID:  runID,
				Reason: CancellationConnectorUnavailable,
			},
		)
		if errors.Is(cancelErr, pgx.ErrNoRows) {
			return nil
		}
		if cancelErr != nil {
			return cancelErr
		}
		run = cancelled
		if err := recordRunChangeInTx(ctx, qtx, &pending, events.SystemActor(), EventRunCancelled, beforeHistory[runID], run); err != nil {
			return err
		}
		transitioned = true
		promoted, cancelErr = s.promoteRunQueueInTx(ctx, qtx, &pending, run, "")
		if cancelErr != nil {
			return cancelErr
		}
		return s.applyTaskTerminalEffectInTx(ctx, qtx, &pending, run)
	})
	if err != nil {
		return fmt.Errorf("cancel queued Run after Connector snapshot failure: %w", err)
	}
	if !transitioned {
		return nil
	}

	slog.Info(
		"run cancelled after Connector snapshot failure",
		"run_id",
		run.ID,
		"conversation_id",
		run.ConversationID.String(),
	)
	s.revokeRunTokens(ctx, run.ID)

	s.publishRunQueuePromotion(ctx, pending, promoted)
	s.NotifyRunFinished(run)
	return nil
}

// CancelRun cancels a single run by ID and broadcasts run.cancelled so
// frontends update immediately. It deliberately leaves an owning Task in its
// current state, so stopping one turn does not close the Task Conversation.
// An already-terminal run is an idempotent success. The run-scoped token is
// revoked eagerly: cancellation is terminal, so the agent process no longer
// needs to call back.
func (s *DefaultRunService) CancelRun(ctx context.Context, runID uuid.UUID, reason string) (*db.Run, error) {
	return s.cancelRun(ctx, runID, reason, nil)
}

func (s *DefaultRunService) cancelRun(ctx context.Context, runID uuid.UUID, reason string, principal *auth.UserPrincipal) (*db.Run, error) {
	actor := events.SystemActor()
	if principal != nil {
		actor = events.UserActor(principal.UserID.String())
	}
	base, err := s.Queries.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load Run before cancellation: %w", err)
	}
	var (
		run          db.Run
		transitioned bool
		pending      []events.Event
		promoted     *db.Run
	)
	err = s.runInTx(ctx, func(qtx *db.Queries) error {
		if principal != nil {
			if _, err := qtx.LockCompanyForMembershipMutation(ctx, principal.CompanyID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrRunNotFound
				}
				return fmt.Errorf("lock Run cancellation company: %w", err)
			}
			member, err := qtx.GetCompanyMemberForUpdate(ctx, db.GetCompanyMemberForUpdateParams{CompanyID: principal.CompanyID, UserID: principal.UserID})
			if errors.Is(err, pgx.ErrNoRows) || err == nil && member.Status != "active" {
				return ErrRunNotFound
			}
			if err != nil {
				return fmt.Errorf("lock Run cancellation membership: %w", err)
			}
			// Cancelling a Task Run stops work in its Project and promotes
			// the next queued prompt, so a reader needs
			// project.tasks.execute; one who no longer reads the chat does
			// not see the Run (ADR 0017). This is checked here, under the
			// Company lock every access change takes, not only by the read
			// before the transaction.
			_, _, err = auth.RequireTaskChatPermission(
				ctx, qtx, *principal, base.ConversationID, auth.ProjectPermissionTasksExecute,
			)
			if errors.Is(err, auth.ErrTaskNotVisible) {
				return ErrRunNotFound
			}
			if err != nil {
				return err
			}
			// Stopping a Run acts on the computer it runs on, its
			// authorizer's own (ADR 0014). Nobody else stops it but a
			// tasks.assign holder, who may hand its Task off anyway.
			if base.ExecutionAuthorizedByUserID != (uuid.NullUUID{UUID: principal.UserID, Valid: true}) &&
				!auth.CompanyRole(member.Role).Can(auth.PermissionTasksAssign) {
				return ErrRunCancelPermissionRequired
			}
		}

		if err := lockTerminalRunConversation(ctx, qtx, base); err != nil {
			return err
		}
		beforeHistory, err := ObserveRunsInTx(ctx, qtx, []uuid.UUID{runID})
		if err != nil {
			return err
		}
		cancelled, cancelErr := qtx.CancelRun(ctx, db.CancelRunParams{ID: runID, Reason: reason})
		if errors.Is(cancelErr, pgx.ErrNoRows) {
			current, lookupErr := qtx.GetRun(ctx, runID)
			if lookupErr != nil {
				return cancelErr
			}
			run = current
			return nil
		}
		if cancelErr != nil {
			return cancelErr
		}
		run = cancelled
		if err := recordRunChangeInTx(ctx, qtx, &pending, actor, EventRunCancelled, beforeHistory[runID], run); err != nil {
			return err
		}
		transitioned = true
		promoted, cancelErr = s.promoteRunQueueInTx(ctx, qtx, &pending, run, "")
		return cancelErr
	})
	if err != nil {
		return nil, fmt.Errorf("cancel run: %w", err)
	}
	if !transitioned {
		return &run, nil
	}

	slog.Info(
		"run cancelled",
		"run_id",
		run.ID,
		"conversation_id",
		run.ConversationID.String(),
	)
	s.revokeRunTokens(ctx, run.ID)

	s.publishRunQueuePromotion(ctx, pending, promoted)
	s.NotifyRunFinished(run)
	return &run, nil
}

// Bulk cancellation lives with its owners: the user-facing single-run arm is
// CancelRun above, and the account-deletion arm cancels, revokes tokens, and
// stages run.cancelled events inside its own transaction (internal/account's
// Delete) because session revocation must land in the same commit. A new bulk
// arm should start from that in-transaction shape, not from a
// transaction-owning service method — three of those accumulated here with no
// callers and were deleted.

// revokeRunTokens eagerly deletes any brt_ tokens minted for the run.
// Failure is non-fatal — the 24h expiry and the FK cascade are the durable
// guards.
func (s *DefaultRunService) revokeRunTokens(ctx context.Context, runID uuid.UUID) {
	if err := s.Queries.DeleteRunTokensByRun(ctx, runID); err != nil {
		slog.Warn("failed to revoke run tokens",
			"run_id", runID, "error", err)
	}
}

// ---------------------------------------------------------------------------
// Auto-retry
// ---------------------------------------------------------------------------

// retryableReasons enumerates failure reasons the auto-retry path is allowed
// to act on. Agent-side errors are intentionally excluded — those are real
// problems the user should see, not infrastructure flakiness. The one
// agent_error.* exception is provider_network: a mid-stream provider
// disconnect is transient infrastructure flakiness, not an agent decision.
// skill_bundle_unavailable is retryable because the agent process never
// started, so there is nothing to be idempotent about.
var retryableReasons = map[string]bool{
	string(runfailure.ReasonRuntimeOffline):         true,
	string(runfailure.ReasonRuntimeRecovery):        true,
	string(runfailure.ReasonTimeout):                true,
	string(runfailure.ReasonAgentProviderNetwork):   true,
	string(runfailure.ReasonSkillBundleUnavailable): true,
}

// How many attempts the auto-retry path allows. Transient provider stream
// cuts (provider_network) get a bespoke three-tier schedule — first run +
// immediate retry + one retry deferred ~5s — and every other retryable reason
// takes the generic ceiling and retries immediately.
//
// These are constants rather than a runs.max_attempts column. The column only
// ever held one of these two numbers, written from this same code, so every
// row carried a copy of something the code already knew — and a run that had
// once escalated kept the wider ceiling for later failures of unrelated
// kinds. The ceiling now follows the reason this attempt failed for.
const (
	defaultMaxAttempts         = 2
	providerNetworkMaxAttempts = 3
)

// MaxAttemptsFor reports how many attempts a run gets, given the reason its
// current attempt failed for and which attempt that is. It is exported
// because it is also what the wire field `maxAttempts` means: with no column
// to read, every surface that shows "attempt 2 of 3" derives it here, so
// they cannot disagree.
//
// An empty reason is a run that has not failed, which takes the generic
// ceiling. A reason the auto-retry path will not act on reports the run's
// own attempt: the lineage stops here, and `failureReason` is an open set a
// client cannot classify, so attempt == maxAttempts has to be what says a
// failure is final. The ceiling is never below the attempt either: a third
// attempt earned by two provider stream cuts sits above the generic ceiling
// and still has to read as final rather than as "attempt 3 of 2".
func MaxAttemptsFor(reason string, attempt int32) int32 {
	ceiling := attempt
	switch {
	case reason == "":
		ceiling = defaultMaxAttempts
	case reason == string(runfailure.ReasonAgentProviderNetwork):
		ceiling = providerNetworkMaxAttempts
	case retryableReasons[reason]:
		ceiling = defaultMaxAttempts
	}
	return max(ceiling, attempt)
}

// retryDelayForAttempt reports how long to defer the NEXT attempt after a
// failure at failedAttempt. Only provider_network's final attempt is deferred
// (~5s); every other retry is immediate (zero delay → the child is created
// 'queued', claimable at once).
func retryDelayForAttempt(reason string, failedAttempt int32) time.Duration {
	if reason == string(runfailure.ReasonAgentProviderNetwork) &&
		failedAttempt >= providerNetworkMaxAttempts-1 {
		return 5 * time.Second
	}
	return 0
}

// resumeUnsafeFailureReason reports failures that poison the agent
// CONVERSATION (not the workdir): resuming the same session would
// immediately replay the stuck/oversized state. Trimmed to boardly's
// claude/codex runfailure taxonomy.
func resumeUnsafeFailureReason(reason string) bool {
	switch reason {
	case string(runfailure.ReasonIterationLimit),
		string(runfailure.ReasonAPIInvalidRequest),
		string(runfailure.ReasonAgentContextOverflow):
		return true
	default:
		return false
	}
}

// ResumeUnsafeFailure reports whether a failed run's agent session must NOT
// be resumed on a retry. It combines the canonical failure-reason poison set
// with Claude's invalid-request response, which means the conversation history
// itself is unprocessable.
func ResumeUnsafeFailure(failureReason, errorText string) bool {
	if resumeUnsafeFailureReason(failureReason) {
		return true
	}
	lower := strings.ToLower(errorText)
	return strings.Contains(lower, "400") && strings.Contains(lower, "invalid_request_error")
}

// retryEligible reports whether a failed run qualifies for an automatic retry
// attempt: an infrastructure-shaped failure_reason and remaining attempt
// budget. Shared by every single and batch failure transition.
func retryEligible(failureReason string, run db.Run) bool {
	return retryableReasons[failureReason] &&
		run.Attempt < MaxAttemptsFor(failureReason, run.Attempt)
}

func createRetryRunInTx(ctx context.Context, q *db.Queries, parent db.Run) (*db.Run, error) {
	if parent.Status != "failed" {
		return nil, nil
	}
	reason := ""
	if parent.FailureReason.Valid {
		reason = parent.FailureReason.String
	}
	if !retryableReasons[reason] {
		return nil, nil
	}
	if parent.Attempt >= MaxAttemptsFor(reason, parent.Attempt) {
		slog.Info(
			"run auto-retry skipped: budget exhausted",
			"run_id", parent.ID,
			"attempt", parent.Attempt,
			"ceiling", MaxAttemptsFor(reason, parent.Attempt),
		)
		return nil, nil
	}
	if !retryEligible(reason, parent) {
		return nil, nil
	}

	// Mirror FailRun's in-tx backoff.
	var retryFireAt pgtype.Timestamptz
	if delay := retryDelayForAttempt(reason, parent.Attempt); delay > 0 {
		retryFireAt = pgtype.Timestamptz{Time: time.Now().Add(delay), Valid: true}
	}
	child, err := q.CreateRetryRun(ctx, db.CreateRetryRunParams{
		ParentRunID:       parent.ID,
		FireAt:            retryFireAt,
		ForceFreshSession: ResumeUnsafeFailure(reason, parent.Error.String),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if isDuplicatePendingRunErr(err) {
			slog.Debug("run auto-retry coalesced: pending run already exists",
				"parent_run_id", parent.ID)
			return nil, nil
		}
		slog.Warn(
			"run auto-retry failed",
			"parent_run_id", parent.ID,
			"reason", reason,
			"error", err,
		)
		return nil, err
	}
	retry := db.Run(child)
	return &retry, nil
}

type runFailureCandidate struct {
	id             uuid.UUID
	companyID      uuid.UUID
	conversationID uuid.UUID
}

// lockRunFailureCandidateConversations continues the Company/member ->
// Conversation order established by lockRunInboxTargets. Candidate selection
// is deliberately a separate, non-locking statement; the later Run update is
// restricted to these ids and rechecks eligibility after every Conversation
// lock is held.
func lockRunFailureCandidateConversations(
	ctx context.Context,
	q *db.Queries,
	candidates []runFailureCandidate,
) error {
	byConversation := make(map[uuid.UUID]runFailureCandidate, len(candidates))
	conversationIDs := make([]uuid.UUID, 0, len(candidates))
	for _, candidate := range candidates {
		if _, exists := byConversation[candidate.conversationID]; exists {
			continue
		}
		byConversation[candidate.conversationID] = candidate
		conversationIDs = append(conversationIDs, candidate.conversationID)
	}
	sort.Slice(conversationIDs, func(i, j int) bool {
		return conversationIDs[i].String() < conversationIDs[j].String()
	})
	for _, conversationID := range conversationIDs {
		candidate := byConversation[conversationID]
		_, err := q.LockConversationForSystemRunQueueMutation(
			ctx,
			db.LockConversationForSystemRunQueueMutationParams{
				ConversationID: candidate.conversationID,
				CompanyID:      candidate.companyID,
			},
		)
		if err != nil {
			return fmt.Errorf("lock bulk-failure Run Conversation: %w", err)
		}
	}
	return nil
}

// transitionFailedRuns keeps bulk terminal paths on the canonical Company/
// member -> Conversation -> Run -> Task lock order. The selector never locks
// Run rows; each transition callback must update only the supplied ids and
// repeat the selector's eligibility predicates at the database write boundary.
func (s *DefaultRunService) transitionFailedRuns(
	ctx context.Context,
	selectCandidates func(*db.Queries) ([]runFailureCandidate, error),
	transition func(*db.Queries, []uuid.UUID) ([]db.Run, error),
) (FailedRunBatch, error) {
	var (
		failed       []db.Run
		retries      = make(map[uuid.UUID]db.Run)
		pending      []events.Event
		promoted     []db.Run
		inboxResults []inbox.CreateResult
		observations []telemetry.RunTerminalObservation
	)
	err := s.runInTx(ctx, func(qtx *db.Queries) error {
		candidates, err := selectCandidates(qtx)
		if err != nil {
			return err
		}
		if len(candidates) == 0 {
			return nil
		}
		candidateRuns := make([]db.Run, len(candidates))
		for index, candidate := range candidates {
			run, err := qtx.GetRun(ctx, candidate.id)
			if err != nil {
				return fmt.Errorf("load bulk-failure Run for Inbox recipient: %w", err)
			}
			candidateRuns[index] = run
		}
		inboxTargets, err := lockRunInboxTargets(ctx, qtx, candidateRuns)
		if err != nil {
			return err
		}
		if err := lockRunFailureCandidateConversations(ctx, qtx, candidates); err != nil {
			return err
		}
		runIDs := make([]uuid.UUID, len(candidates))
		for index, candidate := range candidates {
			runIDs[index] = candidate.id
		}
		beforeHistory, err := ObserveRunsInTx(ctx, qtx, runIDs)
		if err != nil {
			return err
		}
		rows, err := transition(qtx, runIDs)
		if err != nil {
			return err
		}
		failed = rows
		for _, run := range failed {
			observation, err := terminalObservationInTx(ctx, qtx, run, "failed")
			if err != nil {
				return err
			}
			observations = append(observations, observation)
			child, retryErr := createRetryRunInTx(ctx, qtx, run)
			if retryErr != nil {
				return retryErr
			}
			if child != nil {
				if err := recordRunChangeInTx(ctx, qtx, &pending, events.SystemActor(), EventRunQueued, nil, *child); err != nil {
					return err
				}
				retries[run.ID] = *child
			}
			if err := recordRunChangeInTx(ctx, qtx, &pending, events.SystemActor(), EventRunFailed, beforeHistory[run.ID], run); err != nil {
				return err
			}
		}
		promoted, err = s.promoteRunQueuesForTerminalRunsInTx(ctx, qtx, &pending, failed)
		if err != nil {
			return err
		}
		for _, run := range failed {
			if _, retryPending := retries[run.ID]; !retryPending {
				if err := s.applyTaskTerminalEffectInTx(
					ctx,
					qtx,
					&pending,
					run,
				); err != nil {
					return err
				}
			}
			target, ok := inboxTargets[run.ID]
			if !ok {
				continue
			}
			result, err := createRunTerminalInboxItem(ctx, qtx, &pending, run, target)
			if err != nil {
				return err
			}
			if result.Created {
				inboxResults = append(inboxResults, result)
			}
		}
		return nil
	})
	if err != nil {
		return FailedRunBatch{}, err
	}

	s.publishRunQueuePromotions(ctx, pending, promoted)
	for _, run := range failed {
		child, retryPending := retries[run.ID]
		if retryPending {
			slog.Info(
				"run auto-retry enqueued",
				"parent_run_id", run.ID,
				"child_run_id", child.ID,
				"attempt", child.Attempt,
				"max_attempts", MaxAttemptsFor(run.FailureReason.String, child.Attempt),
				"status", child.Status,
			)
			if child.Status == "queued" {
				s.NotifyRunEnqueued(ctx, child)
			}
		}

	}
	s.observeRunTerminals(observations...)
	for _, inboxResult := range inboxResults {
		inbox.PresentCreated(s.InboxPresenter, inboxResult)
	}
	s.notifyRunsFinished(failed)
	return FailedRunBatch{Runs: failed, RetryCount: len(retries)}, nil
}

func (s *DefaultRunService) FailRunsForOfflineRuntimes(ctx context.Context) (FailedRunBatch, error) {
	return s.transitionFailedRuns(
		ctx,
		func(qtx *db.Queries) ([]runFailureCandidate, error) {
			rows, err := qtx.ListOfflineRuntimeFailureCandidates(ctx)
			if err != nil {
				return nil, err
			}
			candidates := make([]runFailureCandidate, len(rows))
			for index, row := range rows {
				candidates[index] = runFailureCandidate{
					id: row.ID, companyID: row.CompanyID, conversationID: row.ConversationID,
				}
			}
			return candidates, nil
		},
		func(qtx *db.Queries, runIDs []uuid.UUID) ([]db.Run, error) {
			return qtx.FailRunsForOfflineRuntimes(ctx, runIDs)
		},
	)
}

func (s *DefaultRunService) FailStaleRuns(
	ctx context.Context,
	options StaleRunOptions,
) (FailedRunBatch, error) {
	return s.transitionFailedRuns(
		ctx,
		func(qtx *db.Queries) ([]runFailureCandidate, error) {
			rows, err := qtx.ListStaleRunFailureCandidates(
				ctx,
				db.ListStaleRunFailureCandidatesParams{
					DispatchTimeoutSecs: options.DispatchTimeout.Seconds(),
					RunningTimeoutSecs:  options.RunningTimeout.Seconds(),
					RuntimeStaleSecs:    options.RuntimeStale.Seconds(),
				},
			)
			if err != nil {
				return nil, err
			}
			candidates := make([]runFailureCandidate, len(rows))
			for index, row := range rows {
				candidates[index] = runFailureCandidate{
					id: row.ID, companyID: row.CompanyID, conversationID: row.ConversationID,
				}
			}
			return candidates, nil
		},
		func(qtx *db.Queries, runIDs []uuid.UUID) ([]db.Run, error) {
			return qtx.FailStaleRuns(ctx, db.FailStaleRunsParams{
				RunIds:              runIDs,
				DispatchTimeoutSecs: options.DispatchTimeout.Seconds(),
				RunningTimeoutSecs:  options.RunningTimeout.Seconds(),
				RuntimeStaleSecs:    options.RuntimeStale.Seconds(),
			})
		},
	)
}

func (s *DefaultRunService) ExpireStaleQueuedRuns(
	ctx context.Context,
	options QueuedRunExpiryOptions,
) (FailedRunBatch, error) {
	return s.transitionFailedRuns(
		ctx,
		func(qtx *db.Queries) ([]runFailureCandidate, error) {
			rows, err := qtx.ListQueuedRunExpiryCandidates(
				ctx,
				db.ListQueuedRunExpiryCandidatesParams{
					TtlSecs: options.TTL.Seconds(), MaxPerTick: options.MaxBatch,
				},
			)
			if err != nil {
				return nil, err
			}
			candidates := make([]runFailureCandidate, len(rows))
			for index, row := range rows {
				candidates[index] = runFailureCandidate{
					id: row.ID, companyID: row.CompanyID, conversationID: row.ConversationID,
				}
			}
			return candidates, nil
		},
		func(qtx *db.Queries, runIDs []uuid.UUID) ([]db.Run, error) {
			return qtx.ExpireStaleQueuedRuns(ctx, db.ExpireStaleQueuedRunsParams{
				RunIds: runIDs, TtlSecs: options.TTL.Seconds(),
			})
		},
	)
}

func (s *DefaultRunService) RecoverOrphanedRunsForRuntime(
	ctx context.Context,
	runtimeID uuid.UUID,
) (FailedRunBatch, error) {
	return s.transitionFailedRuns(
		ctx,
		func(qtx *db.Queries) ([]runFailureCandidate, error) {
			rows, err := qtx.ListRuntimeRecoveryFailureCandidates(ctx, runtimeID)
			if err != nil {
				return nil, err
			}
			candidates := make([]runFailureCandidate, len(rows))
			for index, row := range rows {
				candidates[index] = runFailureCandidate{
					id: row.ID, companyID: row.CompanyID, conversationID: row.ConversationID,
				}
			}
			return candidates, nil
		},
		func(qtx *db.Queries, runIDs []uuid.UUID) ([]db.Run, error) {
			return qtx.RecoverOrphanedRunsForRuntime(
				ctx,
				db.RecoverOrphanedRunsForRuntimeParams{
					RunIds: runIDs, RuntimeID: runtimeID,
				},
			)
		},
	)
}

// NotifyRunEnqueued is the cross-package shim for callers that insert a run
// row directly (the enqueue kernel's post-commit hook, promotion, retry).
// Invalidates the empty-claim cache and kicks the daemon WS so the new run is
// claimed without waiting for the next poll.
func (s *DefaultRunService) NotifyRunEnqueued(_ context.Context, run db.Run) {
	PublishRuntimeWork(context.Background(), s.Bus, run)
}

// NotifyRunFinished invalidates a runtime's empty-claim verdict and emits a
// best-effort daemon wakeup after a run reaches a terminal state. The run ID
// is deliberately omitted from the wakeup payload: the completed run itself
// is not available; the hint only means a queued successor may have become
// claimable because a serialization barrier cleared.
func (s *DefaultRunService) NotifyRunFinished(run db.Run) {
	PublishRuntimeUnblocked(context.Background(), s.Bus, run)
}

// notifyRunsFinished is the batch form used by bulk terminal transitions.
// Coalesce by runtime so cancelling many runs on one machine produces one
// cache bump and one websocket hint rather than a burst of identical work.
func (s *DefaultRunService) notifyRunsFinished(runs []db.Run) {
	seen := make(map[string]struct{}, len(runs))
	for _, run := range runs {
		runtimeKey := run.RuntimeID.String()
		if _, ok := seen[runtimeKey]; ok {
			continue
		}
		seen[runtimeKey] = struct{}{}
		PublishRuntimeUnblocked(context.Background(), s.Bus, run)
	}
}

// ---------------------------------------------------------------------------
// Event builders
// ---------------------------------------------------------------------------
