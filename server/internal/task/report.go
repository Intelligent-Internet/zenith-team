package task

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const (
	maxReportPreviewEvents = 100
	maxReportMessageBytes  = 60 << 10
	maxReportSummaryRunes  = 600

	reportSourceManual               = "manual"
	reportSourceReviewAccepted       = "review_accepted"
	reportSourceAgentBusinessFailure = "agent_business_failure"

	reportDeliveryManual    = "manual"
	reportDeliveryProactive = "proactive"
)

type ReportEvent struct {
	ID               uuid.UUID `json:"id"`
	TaskID           uuid.UUID `json:"taskId"`
	TaskKey          string    `json:"taskKey"`
	TaskTitle        string    `json:"taskTitle"`
	Outcome          Status    `json:"outcome"`
	CompletionSource string    `json:"completionSource"`
	CompletedAt      time.Time `json:"completedAt"`
	ResultSummary    string    `json:"resultSummary"`
}

type ReportPreview struct {
	RecipientAgentID uuid.UUID     `json:"recipientAgentId"`
	ConversationID   uuid.UUID     `json:"conversationId"`
	PendingCount     int64         `json:"pendingCount"`
	AgentBusy        bool          `json:"agentBusy"`
	Truncated        bool          `json:"truncated"`
	Events           []ReportEvent `json:"events"`
}

type SendReportInput struct {
	RequestKey uuid.UUID
}

type SendReportResult struct {
	BatchID    uuid.UUID `json:"batchId"`
	MessageID  uuid.UUID `json:"messageId"`
	RunID      uuid.UUID `json:"runId"`
	EventCount int32     `json:"eventCount"`
	Created    bool      `json:"-"`
}

// OutcomeReportInput describes one Task status transition evaluated inside
// the caller's transaction.
type OutcomeReportInput struct {
	Actor ActorRef
	// RunActor, when set, is who the report frames and their Activity name:
	// the Agent that declared the outcome in its Run, for that Run's
	// authorizer. The zero value names Actor alone.
	RunActor       events.Actor
	PreviousStatus Status
	Task           Response
	ResultSummary  string
	SourceRunID    uuid.UUID
	RequestKey     uuid.UUID
}

type taskReportChangedRef struct {
	companyID        uuid.UUID
	recipientAgentID uuid.UUID
	taskID           uuid.UUID
	taskVersion      int64
	occurredAt       time.Time
	// projectID is the report's recorded Project, when known; the frame
	// then reaches its readers even if the Task has moved since.
	projectID uuid.UUID
}

type ReportService interface {
	Preview(context.Context, auth.UserPrincipal, uuid.UUID) (ReportPreview, error)
	Send(context.Context, auth.UserPrincipal, uuid.UUID, SendReportInput) (SendReportResult, error)
	DispatchProactiveOnce(context.Context) (bool, error)
}

var _ ReportService = (*DefaultReportService)(nil)

type DefaultReportService struct {
	store *db.Store
	bus   *events.Bus
}

func NewReportService(store *db.Store, bus *events.Bus) (*DefaultReportService, error) {
	if store == nil {
		return nil, errors.New("Task report database store is required")
	}
	if bus == nil {
		return nil, errors.New("Task report event bus is required")
	}
	return &DefaultReportService{store: store, bus: bus}, nil
}

func (s *DefaultReportService) Preview(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (ReportPreview, error) {
	if !validReportPrincipal(principal) {
		return ReportPreview{}, ErrUnauthenticated
	}
	if !util.ValidUUID(conversationID) {
		return ReportPreview{}, ErrNotFound
	}
	target, err := taskReportTarget(ctx, s.store.Queries(), principal, conversationID)
	if err != nil {
		return ReportPreview{}, err
	}
	count, rows, err := readablePendingReports(ctx, s.store.Queries(), principal, target.RecipientAgentID)
	if err != nil {
		return ReportPreview{}, err
	}
	items := make([]ReportEvent, len(rows))
	for index, row := range rows {
		items[index] = reportEventFromRow(row)
	}
	return ReportPreview{
		RecipientAgentID: target.RecipientAgentID, ConversationID: conversationID,
		PendingCount: count, AgentBusy: target.AgentBusy,
		Truncated: count > int64(len(items)), Events: items,
	}, nil
}

// readablePendingReports counts and lists the reports queued for one Agent
// that the member asking reads (ADR 0017). A batch lands in that member's
// direct chat, so a report about a Project they cannot read stays queued
// until a reader asks.
func readablePendingReports(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	recipientAgentID uuid.UUID,
) (int64, []generated.TaskReportEvent, error) {
	count, err := q.CountPendingTaskReportEvents(ctx, generated.CountPendingTaskReportEventsParams{
		CompanyID: principal.CompanyID, RecipientAgentID: recipientAgentID, ReaderUserID: principal.UserID,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("count pending Task reports: %w", err)
	}
	rows, err := q.ListPendingTaskReportEvents(ctx, generated.ListPendingTaskReportEventsParams{
		CompanyID: principal.CompanyID, RecipientAgentID: recipientAgentID, ReaderUserID: principal.UserID,
		PageSize: maxReportPreviewEvents,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("list pending Task reports: %w", err)
	}
	return count, rows, nil
}

// lockReadablePendingReports locks the queued reports for one Agent that
// the member sending the batch reads, under the same rule as the preview.
func lockReadablePendingReports(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
	recipientAgentID uuid.UUID,
) ([]generated.TaskReportEvent, error) {
	rows, err := tx.LockPendingTaskReportEvents(ctx, generated.LockPendingTaskReportEventsParams{
		CompanyID: principal.CompanyID, RecipientAgentID: recipientAgentID, ReaderUserID: principal.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("lock pending Task reports: %w", err)
	}
	return rows, nil
}

func (s *DefaultReportService) Send(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	input SendReportInput,
) (SendReportResult, error) {
	return s.send(ctx, principal, conversationID, input, reportDeliveryManual, uuid.Nil)
}

// DispatchProactiveOnce sends at most one eligible root-Agent report batch.
// Returning false means the durable queue is currently empty or lost its
// exact authority while being locked; callers can poll again later.
func (s *DefaultReportService) DispatchProactiveOnce(ctx context.Context) (bool, error) {
	target, err := s.store.Queries().
		GetNextProactiveTaskReportTarget(ctx, auth.RolesWith(auth.PermissionCompanyProactiveModeManage))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("find proactive Task report target: %w", err)
	}
	principal := auth.UserPrincipal{
		UserID: target.ActorUserID, CompanyID: target.CompanyID, Role: auth.CompanyRole(target.ActorRole),
	}
	_, err = s.send(
		ctx,
		principal,
		target.ConversationID,
		SendReportInput{RequestKey: uuid.New()},
		reportDeliveryProactive,
		target.RecipientAgentID,
	)
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrAgentBusy) ||
		errors.Is(err, ErrNoPendingReports) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *DefaultReportService) send(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	input SendReportInput,
	deliveryType string,
	expectedRecipientID uuid.UUID,
) (SendReportResult, error) {
	if !validReportPrincipal(principal) {
		return SendReportResult{}, ErrUnauthenticated
	}
	if !validSendReportInput(conversationID, input) ||
		deliveryType != reportDeliveryManual && deliveryType != reportDeliveryProactive ||
		deliveryType == reportDeliveryProactive && !util.ValidUUID(expectedRecipientID) {
		return SendReportResult{}, ErrInvalidInput
	}
	var (
		result  SendReportResult
		pending []events.Event
		queued  *generated.Run
	)
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		queued = nil
		if deliveryType == reportDeliveryManual {
			if err := lockReportSendAuthority(ctx, tx, principal); err != nil {
				return err
			}
			if err := tx.LockTaskReportRequest(ctx, generated.LockTaskReportRequestParams{
				CompanyID: principal.CompanyID, ActorUserID: principal.UserID,
				RequestKey: input.RequestKey,
			}); err != nil {
				return fmt.Errorf("lock Task report request: %w", err)
			}
			replay, err := tx.GetTaskReportBatchReplay(ctx, generated.GetTaskReportBatchReplayParams{
				ActorUserID: principal.UserID, RequestKey: input.RequestKey,
				CompanyID: principal.CompanyID,
			})
			if err == nil {
				if replay.ConversationID != conversationID ||
					!replay.ConfirmedByUserID.Valid || replay.ConfirmedByUserID.UUID != principal.UserID {
					return ErrConflict
				}
				result = reportResultFromBatch(replay, false)
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("read Task report replay: %w", err)
			}
		} else if err := lockProactiveReportTarget(
			ctx, tx, principal, conversationID, expectedRecipientID,
		); err != nil {
			return err
		}

		locked, err := tx.LockConversationForActiveUserMutation(
			ctx,
			generated.LockConversationForActiveUserMutationParams{
				ActorUserID: principal.UserID, ConversationID: conversationID,
				CompanyID: principal.CompanyID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && locked.Kind != "direct" {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Task report Conversation: %w", err)
		}
		target, err := taskReportTarget(ctx, tx, principal, conversationID)
		if err != nil {
			return err
		}
		if deliveryType == reportDeliveryProactive && target.RecipientAgentID != expectedRecipientID {
			return ErrNotFound
		}
		if _, err := tx.LockTaskReportRecipientAgent(
			ctx,
			generated.LockTaskReportRecipientAgentParams{
				CompanyID: principal.CompanyID,
				AgentID:   target.RecipientAgentID,
			},
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock Task report recipient Agent: %w", err)
		}
		lockedTarget, err := taskReportTarget(ctx, tx, principal, conversationID)
		if err != nil {
			return err
		}
		if lockedTarget.RecipientAgentID != target.RecipientAgentID {
			return errors.New("Task report recipient Agent changed while locked")
		}
		if deliveryType == reportDeliveryProactive {
			if err := lockProactiveReportTarget(
				ctx, tx, principal, conversationID, expectedRecipientID,
			); err != nil {
				return err
			}
		}
		if lockedTarget.AgentBusy {
			return ErrAgentBusy
		}
		target = lockedTarget
		rows, err := lockReadablePendingReports(ctx, tx, principal, target.RecipientAgentID)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrNoPendingReports
		}
		if len(rows) > int(^uint32(0)>>1) {
			return errors.New("Task report batch is too large")
		}
		content, included, err := buildTaskReportMessage(input.RequestKey, rows)
		if err != nil {
			return err
		}
		rows = rows[:included]

		run, err := execution.EnqueueRunInTx(ctx, tx, &pending, execution.RunTrigger{
			CompanyID: principal.CompanyID, ConversationID: conversationID,
			AgentID: target.RecipientAgentID, TriggerPrompt: content,
			Source: execution.RunSourceTaskReport, ActorUserID: principal.UserID,
			AuthorizedByUserID: principal.UserID,
		})
		if err != nil {
			return fmt.Errorf("enqueue Task report Run: %w", err)
		}
		queued = &run
		message, err := tx.InsertUserMessageAfterConversationLock(
			ctx,
			generated.InsertUserMessageAfterConversationLockParams{
				RunID: run.ID, Content: content, ActorUserID: principal.UserID,
				ConversationID: conversationID, CompanyID: principal.CompanyID,
			},
		)
		if err != nil {
			return fmt.Errorf("insert Task report Message: %w", err)
		}
		confirmedBy := uuid.NullUUID{}
		automationUser := uuid.NullUUID{}
		if deliveryType == reportDeliveryManual {
			confirmedBy = uuid.NullUUID{UUID: principal.UserID, Valid: true}
		} else {
			automationUser = uuid.NullUUID{UUID: principal.UserID, Valid: true}
		}
		batch, err := tx.InsertTaskReportBatch(ctx, generated.InsertTaskReportBatchParams{
			CompanyID: principal.CompanyID, RecipientAgentID: target.RecipientAgentID,
			ConversationID: conversationID, ConfirmedByUserID: confirmedBy,
			DeliveryType: deliveryType, AutomationUserID: automationUser,
			RequestKey: input.RequestKey, MessageID: message.ID, RunID: run.ID,
			EventCount: int32(included),
		})
		if err != nil {
			return fmt.Errorf("insert Task report batch: %w", err)
		}
		eventIDs := make([]uuid.UUID, len(rows))
		for index := range rows {
			eventIDs[index] = rows[index].ID
		}
		assigned, err := tx.AssignTaskReportEventsToBatch(ctx,
			generated.AssignTaskReportEventsToBatchParams{
				BatchID: batch.ID, EventIds: eventIDs, CompanyID: principal.CompanyID,
				RecipientAgentID: target.RecipientAgentID,
			})
		if err != nil {
			return fmt.Errorf("assign Task reports to batch: %w", err)
		}
		if assigned != int64(len(rows)) {
			return errors.New("Task report batch assignment is incomplete")
		}
		if err := stageTaskEvents(ctx, tx, &pending, reportChangedPerProject(
			ActorRef{Kind: ActorUser, ID: principal.UserID},
			principal.CompanyID, target.RecipientAgentID, batch.CreatedAt.Time, rows,
		)...); err != nil {
			return err
		}
		if err := conversation.StageTaskReportTurnInTx(
			ctx, tx, &pending, principal.UserID, principal.CompanyID, message,
		); err != nil {
			return err
		}
		result = reportResultFromBatch(batch, true)
		return nil
	})
	if err != nil {
		return SendReportResult{}, fmt.Errorf("send Task reports: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	if queued != nil {
		execution.PublishRuntimeWork(ctx, s.bus, *queued)
	}
	return result, nil
}

type taskReportTargetValue struct {
	RecipientAgentID uuid.UUID
	AgentBusy        bool
}

// lockReportSendAuthority is the producer fence for a user-confirmed batch.
// Company and membership precede Conversation/Run locks so account deletion
// either completes first or waits for the whole committed report turn.
func lockReportSendAuthority(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
) error {
	if _, err := queries.LockCompanyForMembershipRead(ctx, principal.CompanyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock Task report Company: %w", err)
	}
	membership, err := queries.GetCompanyMemberForShare(ctx, generated.GetCompanyMemberForShareParams{
		CompanyID: principal.CompanyID,
		UserID:    principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock Task report membership: %w", err)
	}
	return nil
}

func lockProactiveReportTarget(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	recipientAgentID uuid.UUID,
) error {
	row, err := queries.LockProactiveTaskReportTarget(
		ctx,
		generated.LockProactiveTaskReportTargetParams{
			ActorRoles: auth.RolesWith(auth.PermissionCompanyProactiveModeManage),
			CompanyID:  principal.CompanyID, ActorUserID: principal.UserID,
			ConversationID: conversationID, RecipientAgentID: recipientAgentID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock proactive Task report authority: %w", err)
	}
	if row.ActorUserID != principal.UserID || row.RecipientAgentID != recipientAgentID {
		return ErrNotFound
	}
	return nil
}

func taskReportTarget(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (taskReportTargetValue, error) {
	row, err := queries.GetTaskReportConversationTarget(ctx,
		generated.GetTaskReportConversationTargetParams{
			ActorUserID: principal.UserID, ConversationID: conversationID, CompanyID: principal.CompanyID,
		})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !row.RecipientAgentID.Valid {
		return taskReportTargetValue{}, ErrNotFound
	}
	if err != nil {
		return taskReportTargetValue{}, fmt.Errorf("resolve Task report Conversation: %w", err)
	}
	return taskReportTargetValue{
		RecipientAgentID: row.RecipientAgentID.UUID, AgentBusy: row.AgentBusy,
	}, nil
}

func reconcileTaskReportEvent(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	actorID uuid.UUID,
	previous Response,
	current Response,
) error {
	return ReconcileOutcomeReportInTx(ctx, queries, pending, OutcomeReportInput{
		Actor: ActorRef{Kind: ActorUser, ID: actorID}, PreviousStatus: previous.Status, Task: current,
	})
}

// ReconcileOutcomeReportInTx supersedes a stale pending report and creates the
// current done or failed outcome inside the caller's transaction.
func ReconcileOutcomeReportInTx(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	input OutcomeReportInput,
) error {
	if queries == nil || pending == nil || !validActor(input.Actor) {
		return errors.New("Task outcome report actor is invalid")
	}
	current := input.Task
	eventActor := reportEventActor(input.Actor)
	if input.RunActor.Type != "" {
		eventActor = input.RunActor
	}
	if reportableOutcome(input.PreviousStatus) && input.PreviousStatus != current.Status {
		if err := supersedeTaskReportEvents(ctx, queries, pending, eventActor, current); err != nil {
			return err
		}
	}
	if input.PreviousStatus == current.Status || !reportableOutcome(current.Status) ||
		current.ExecutedBy.Kind != ActorAgent || current.CompletedAt == nil {
		return nil
	}
	resultSummary := strings.TrimSpace(input.ResultSummary)
	if current.Status == StatusFailed && (input.Actor.Kind != ActorAgent || resultSummary == "" ||
		input.SourceRunID == uuid.Nil || !util.ValidUUID(input.RequestKey)) {
		return errors.New("failed Task outcome report is invalid")
	}
	if current.Status != StatusFailed && (input.SourceRunID != uuid.Nil || input.RequestKey != uuid.Nil) {
		return errors.New("non-failed Task outcome report carries failure origin")
	}
	completionSource := reportSourceManual
	if current.Status == StatusFailed {
		completionSource = reportSourceAgentBusinessFailure
	} else if input.PreviousStatus == StatusInReview {
		completionSource = reportSourceReviewAccepted
	}
	row, err := queries.InsertTaskReportEvent(ctx, generated.InsertTaskReportEventParams{
		CompanyID: current.CompanyID, TaskID: current.ID, TaskVersion: current.Revision,
		TaskKey: current.Key, TaskTitle: current.Title, CompletionSource: completionSource,
		CompletedAt:   pgtype.Timestamptz{Time: current.CompletedAt.UTC(), Valid: true},
		ResultSummary: resultSummary, SourceAgentID: current.ExecutedBy.ID,
		SourceRunID: uuid.NullUUID{UUID: input.SourceRunID, Valid: input.SourceRunID != uuid.Nil},
		RequestKey:  uuid.NullUUID{UUID: input.RequestKey, Valid: input.RequestKey != uuid.Nil},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("insert Task report event: %w", err)
	}
	return stageTaskEvent(ctx, queries, pending, newTaskReportReadyEvent(eventActor, row))
}

func newTaskReportReadyEvent(actor events.Actor, row generated.TaskReportEvent) events.Event {
	payload, _ := NewReportReadyPayload(row.ID, row.RecipientAgentID, row.TaskID, row.TaskVersion)
	return events.Event{
		EntityType: Topic, Name: EventReportReady, CompanyID: row.CompanyID,
		CreatedAt: row.CreatedAt.Time.UTC(), TaskID: row.TaskID,
		Actor: actor, Payload: payload,
	}
}

func supersedeTaskReportEvents(
	ctx context.Context,
	queries *generated.Queries,
	pending *[]events.Event,
	actor events.Actor,
	current Response,
) error {
	recipientAgentIDs, err := queries.SupersedePendingTaskReportEvents(ctx,
		generated.SupersedePendingTaskReportEventsParams{
			CompanyID: current.CompanyID, TaskID: current.ID,
		})
	if err != nil {
		return fmt.Errorf("supersede pending Task report events: %w", err)
	}
	for _, recipientAgentID := range recipientAgentIDs {
		if err := stageTaskEvent(ctx, queries, pending, newTaskReportChangedEvent(actor, taskReportChangedRef{
			companyID: current.CompanyID, recipientAgentID: recipientAgentID,
			taskID: current.ID, taskVersion: current.Revision, occurredAt: current.UpdatedAt,
		})); err != nil {
			return err
		}
	}
	return nil
}

func reportableOutcome(status Status) bool {
	return status == StatusDone || status == StatusFailed
}

// reportChangedPerProject is the task.report_changed a sent batch raises:
// one per Project its reports come from, each naming that Project's latest
// report in the batch. A frame reaches only its Task's Project readers
// (ADR 0017), so one frame for the whole batch would leave the readers of
// the other Projects with a stale pending-report preview. A report that
// recorded no Project keys on its own Task, whose Project the stamp reads.
func reportChangedPerProject(
	actor ActorRef,
	companyID, recipientAgentID uuid.UUID,
	occurredAt time.Time,
	rows []generated.TaskReportEvent,
) []events.Event {
	latest := map[uuid.UUID]int{}
	order := make([]uuid.UUID, 0, 1)
	for index, row := range rows {
		key := row.TaskID
		if row.ProjectID.Valid {
			key = row.ProjectID.UUID
		}
		if _, seen := latest[key]; !seen {
			order = append(order, key)
		}
		latest[key] = index
	}
	staged := make([]events.Event, len(order))
	for position, key := range order {
		row := rows[latest[key]]
		staged[position] = newTaskReportChangedEvent(reportEventActor(actor), taskReportChangedRef{
			companyID: companyID, recipientAgentID: recipientAgentID,
			taskID: row.TaskID, taskVersion: row.TaskVersion, occurredAt: occurredAt,
			projectID: row.ProjectID.UUID,
		})
	}
	return staged
}

func newTaskReportChangedEvent(actor events.Actor, ref taskReportChangedRef) events.Event {
	payload, _ := NewReportChangedPayload(ref.recipientAgentID, ref.taskID, ref.taskVersion)
	payload.ProjectID = ref.projectID
	return events.Event{
		EntityType: Topic, Name: EventReportChanged, CompanyID: ref.companyID,
		CreatedAt: ref.occurredAt.UTC(), TaskID: ref.taskID,
		Actor: actor, Payload: payload,
	}
}

func reportEventActor(actor ActorRef) events.Actor {
	if actor.Kind == ActorAgent {
		return events.AgentActor(actor.ID.String())
	}
	return events.UserActor(actor.ID.String())
}

func reportEventFromRow(row generated.TaskReportEvent) ReportEvent {
	return ReportEvent{
		ID: row.ID, TaskID: row.TaskID, TaskKey: row.TaskKey, TaskTitle: row.TaskTitle,
		Outcome:          reportOutcomeFromSource(row.CompletionSource),
		CompletionSource: row.CompletionSource, CompletedAt: row.CompletedAt.Time.UTC(),
		ResultSummary: row.ResultSummary,
	}
}

func reportOutcomeFromSource(source string) Status {
	if source == reportSourceAgentBusinessFailure {
		return StatusFailed
	}
	return StatusDone
}

func reportResultFromBatch(row generated.TaskReportBatch, created bool) SendReportResult {
	return SendReportResult{
		BatchID: row.ID, MessageID: row.MessageID, RunID: row.RunID,
		EventCount: row.EventCount, Created: created,
	}
}

func validReportPrincipal(principal auth.UserPrincipal) bool {
	return util.ValidUUID(principal.UserID) && util.ValidUUID(principal.CompanyID)
}

func validSendReportInput(conversationID uuid.UUID, input SendReportInput) bool {
	return util.ValidUUID(conversationID) && util.ValidUUID(input.RequestKey)
}

func buildTaskReportMessage(
	requestKey uuid.UUID,
	rows []generated.TaskReportEvent,
) (string, int, error) {
	const envelopeReserve = 256
	items := make([]string, 0, len(rows))
	itemsBytes := 0
	for _, row := range rows {
		var item strings.Builder
		item.WriteString("<task-update><task-id>")
		writeEscapedXML(&item, row.TaskID.String())
		item.WriteString("</task-id><key>")
		writeEscapedXML(&item, row.TaskKey)
		item.WriteString("</key><title>")
		writeEscapedXML(&item, row.TaskTitle)
		item.WriteString("</title><outcome>")
		writeEscapedXML(&item, string(reportOutcomeFromSource(row.CompletionSource)))
		item.WriteString("</outcome>")
		item.WriteString("<completed-at>")
		writeEscapedXML(&item, row.CompletedAt.Time.UTC().Format(time.RFC3339Nano))
		item.WriteString("</completed-at><completion-source>")
		writeEscapedXML(&item, row.CompletionSource)
		item.WriteString("</completion-source>")
		if summary := truncateRunes(
			strings.TrimSpace(row.ResultSummary),
			maxReportSummaryRunes,
		); summary != "" {
			item.WriteString("<result-summary>")
			writeEscapedXML(&item, summary)
			item.WriteString("</result-summary>")
		}
		item.WriteString("</task-update>")
		if itemsBytes+item.Len()+envelopeReserve > maxReportMessageBytes {
			break
		}
		items = append(items, item.String())
		itemsBytes += item.Len()
	}
	if len(items) == 0 {
		return "", 0, errors.New("Task report item exceeds Message size limit")
	}
	var builder strings.Builder
	builder.Grow(min(maxReportMessageBytes, envelopeReserve+itemsBytes))
	fmt.Fprintf(
		&builder,
		`<task-updates version="2" request-key="%s" count="%d">`,
		requestKey,
		len(items),
	)
	for _, item := range items {
		builder.WriteString(item)
	}
	builder.WriteString("</task-updates>")
	content := builder.String()
	if content == "" || len(content) > maxReportMessageBytes || !utf8.ValidString(content) {
		return "", 0, errors.New("Task report Message is invalid")
	}
	return content, len(items), nil
}

func writeEscapedXML(builder *strings.Builder, value string) {
	var escaped bytes.Buffer
	_ = xml.EscapeText(&escaped, []byte(value))
	builder.Write(escaped.Bytes())
}

func truncateRunes(value string, maximum int) string {
	if utf8.RuneCountInString(value) <= maximum {
		return value
	}
	runes := []rune(value)
	return string(runes[:maximum-1]) + "…"
}
