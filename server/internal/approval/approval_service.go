package approval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

const maxCompanyPlanActionBytes = 16 << 20

// PreparedCompanyPlanAction is the server-owned preview and frozen action
// produced from the authoritative Company Plan snapshot.
type PreparedCompanyPlanAction struct {
	CanonicalAction json.RawMessage
	Presentation    json.RawMessage
	PreviewMarkdown string
	Summary         *string
}

// CompanyPlanActionResult is the transactional action outcome. A non-nil
// SkippedReason means the authoritative base moved and no plan mutation was
// attempted; an error means infrastructure failed and the Approval remains
// pending because the whole transaction rolls back.
type CompanyPlanActionResult struct {
	Result        json.RawMessage
	SkippedReason *string
}

// CompanyPlanActor is who a Company Plan action is prepared or applied for
// (ADR 0017). Principal is that person: the member whose Run raised the
// plan, whose Project access bounds the preview, or the applier (the
// Approval's decider, or the Proactive Mode origin user), whose Project
// access bounds the apply. TaskProjectID is the Project of the Task chat the
// plan was raised in: the action then reaches that Project alone, and
// approving it is decided there, with project.approvals.resolve and
// project.tasks.write and no Company permission. It is not set for a plan
// raised outside a Task chat, which stays a Company action under
// company.plan.apply.
type CompanyPlanActor struct {
	Principal     auth.UserPrincipal
	TaskProjectID uuid.NullUUID
	// Run is set when a Run applies the plan it raised for Principal without
	// waiting for a decision (Proactive Mode). Every row the apply writes
	// then names the Run's Agent, Principal and the Run, and every
	// Task it creates names the Agent beside Principal. It is nil
	// when a person approved the plan, and for a raise.
	Run *execution.RunOrigin
}

// Actor is who the apply's rows name: the Run's Agent for Principal in the
// Run, or Principal when a person approved the plan.
func (actor CompanyPlanActor) Actor() events.Actor {
	if actor.Run == nil {
		return events.UserActor(actor.Principal.UserID.String())
	}
	return actor.Run.Actor()
}

// CompanyPlanActionParticipant lets the Company domain prepare and execute
// its own action without approval importing company. Every method uses the
// query set bound to the ApprovalService transaction. LockCompanyPlanScope
// takes the Project and Task chat locks PrepareCompanyPlanApply will take,
// so the raise can take them after its Company and member locks and before
// its origin chat and Run locks, in the ladder's order.
type CompanyPlanActionParticipant interface {
	LockCompanyPlanScope(context.Context, *generated.Queries, CompanyPlanActor) error
	PrepareCompanyPlanApply(
		context.Context,
		*generated.Queries,
		CompanyPlanActor,
		json.RawMessage,
	) (PreparedCompanyPlanAction, error)
	ExecuteCompanyPlanApply(
		context.Context,
		*generated.Queries,
		*[]events.Event,
		CompanyPlanActor,
		json.RawMessage,
	) (CompanyPlanActionResult, error)
}

// ApprovalService owns the Approval request/read/resolve lifecycle.
// RequestCompanyPlanApply's boolean reports whether this call created the
// Approval (false on an exact idempotent replay); it is a process-local fact
// for the transport, never a wire field.
//
// The Approval prefix is the subject, not the package: QuestionService is its
// sibling here, and AGENTS.md pins every service to the trio XService /
// DefaultXService / NewXService and forbids a bare Service.
type ApprovalService interface {
	RequestCompanyPlanApply(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		uuid.UUID,
		json.RawMessage,
	) (Approval, bool, error)
	List(context.Context, auth.UserPrincipal, ApprovalListOptions) (ApprovalPage, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (Approval, error)
	Stats(context.Context, auth.UserPrincipal, ApprovalStatsOptions) (ApprovalStats, error)
	Approve(context.Context, auth.UserPrincipal, uuid.UUID, ApprovalDecisionInput) (Approval, error)
	Reject(context.Context, auth.UserPrincipal, uuid.UUID, ApprovalDecisionInput) (Approval, error)
	Cancel(context.Context, auth.UserPrincipal, uuid.UUID, ApprovalDecisionInput) (Approval, error)
}

var _ ApprovalService = (*DefaultApprovalService)(nil)

// DefaultApprovalService stores durable approvals and delegates the frozen
// Company Plan action to its owning domain inside the same raw transaction.
type DefaultApprovalService struct {
	store          *db.Store
	bus            *events.Bus
	participant    CompanyPlanActionParticipant
	inboxPresenter inbox.Presenter
}

func NewApprovalService(
	store *db.Store,
	bus *events.Bus,
	participant CompanyPlanActionParticipant,
	inboxPresenter inbox.Presenter,
) (*DefaultApprovalService, error) {
	if store == nil || bus == nil || participant == nil {
		return nil, ErrApprovalConfiguration
	}
	return &DefaultApprovalService{
		store:          store,
		bus:            bus,
		participant:    participant,
		inboxPresenter: inboxPresenter,
	}, nil
}

func (s *DefaultApprovalService) RequestCompanyPlanApply(
	ctx context.Context,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	requestKey uuid.UUID,
	request json.RawMessage,
) (Approval, bool, error) {
	request, requestDigest, err := normalizeApprovalRequest(principal, origin, requestKey, request)
	if err != nil {
		return Approval{}, false, err
	}
	var result Approval
	var created, noResolver bool
	var pending []events.Event
	var inboxResults []inbox.CreateResult
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = pending[:0]
		inboxResults = inboxResults[:0]
		created, noResolver = false, false
		role, err := lockHumanInputOriginMember(ctx, q, principal, origin)
		if err != nil {
			return err
		}
		// A plan raised in a Task chat with a Project reaches that Project
		// alone and is decided there (ADR 0017): approved only by its readers
		// holding project.approvals.resolve and project.tasks.write, the
		// permissions the apply uses. With nobody who could approve it the
		// raising Run ends and the raise is refused. A Task's Project never
		// changes, so reading it before the chat lock is safe.
		requester := auth.UserPrincipal{
			UserID: humanInputProjectionActor(principal, origin), CompanyID: principal.CompanyID,
		}
		actor := CompanyPlanActor{Principal: requester}
		var scoped bool
		if origin.TaskID != nil {
			var projectID uuid.UUID
			projectID, scoped, err = auth.TaskChatProjectID(ctx, q, origin.CompanyID, origin.ConversationID)
			if err != nil {
				return err
			}
			if scoped {
				actor.TaskProjectID = uuid.NullUUID{UUID: projectID, Valid: true}
			}
		}
		// The plan's Project and Task chat locks come before the origin chat
		// and Run locks: a Project lock never follows a Conversation or Run
		// lock in one transaction.
		if err := s.participant.LockCompanyPlanScope(ctx, q, actor); err != nil {
			return err
		}
		if err := lockHumanInputOriginChat(ctx, q, origin); err != nil {
			return err
		}
		if origin.TaskID == nil && !role.Can(auth.PermissionApprovalsResolve) {
			return ErrApprovalForbidden
		}
		existing, err := q.GetApprovalByOriginRequestKey(
			ctx,
			generated.GetApprovalByOriginRequestKeyParams{
				OriginRunID: origin.RunID, RequestKey: requestKey,
			},
		)
		if err == nil {
			if !bytes.Equal(existing.RequestSha256, requestDigest) {
				return ErrApprovalConflict
			}
			result, err = approvalProjectionForActor(
				ctx,
				q,
				existing,
				humanInputProjectionActor(principal, origin),
			)
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load Approval request replay: %w", err)
		}
		var deciders []uuid.UUID
		if scoped {
			deciders, err = companyPlanApproversInTx(ctx, q, origin.CompanyID, actor.TaskProjectID.UUID)
			if err != nil {
				return err
			}
			if len(deciders) == 0 {
				noResolver = true
				notice, err := endRunWithoutResolverInTx(
					ctx,
					q,
					&pending,
					origin.CompanyID,
					origin.RunID,
				)
				inboxResults = append(inboxResults, notice)
				return err
			}
		}

		prepared, err := s.participant.PrepareCompanyPlanApply(ctx, q, actor, request)
		if err != nil {
			return err
		}
		prepared, actionDigest, err := normalizePreparedCompanyPlanAction(prepared)
		if err != nil {
			return err
		}
		row, err := q.CreateApproval(ctx, generated.CreateApprovalParams{
			CompanyID:          principal.CompanyID,
			Type:               string(ApprovalTypeCompanyPlanApply),
			RuleKey:            string(ApprovalTypeCompanyPlanApply),
			Title:              "Apply Company Plan",
			Summary:            util.PtrToText(prepared.Summary),
			PreviewMarkdown:    prepared.PreviewMarkdown,
			Presentation:       prepared.Presentation,
			RequestedByAgentID: origin.AgentID,
			OriginRunID:        origin.RunID,
			RequestKey:         requestKey,
			TaskID:             util.NullUUIDFromPtr(origin.TaskID),
			ConversationID:     origin.ConversationID,
			OriginUserID:       util.NullUUIDFromPtr(origin.OriginUserID),
			CanonicalAction:    prepared.CanonicalAction,
			RequestSha256:      requestDigest,
			ActionSha256:       actionDigest,
			ExpiresAt: pgtype.Timestamptz{
				Time: time.Now().UTC().Add(24 * time.Hour), Valid: true,
			},
		})
		if errors.Is(err, pgx.ErrNoRows) {
			existing, replayErr := q.GetApprovalByOriginRequestKey(
				ctx,
				generated.GetApprovalByOriginRequestKeyParams{
					OriginRunID: origin.RunID, RequestKey: requestKey,
				},
			)
			if replayErr != nil {
				return fmt.Errorf("load concurrent Approval request replay: %w", replayErr)
			}
			if !bytes.Equal(existing.RequestSha256, requestDigest) {
				return ErrApprovalConflict
			}
			result, err = approvalProjectionForActor(
				ctx,
				q,
				existing,
				humanInputProjectionActor(principal, origin),
			)
			return err
		}
		if err != nil {
			return fmt.Errorf("create Approval: %w", err)
		}
		automatic, err := q.HasProactiveCompanyPlanApprovalAuthority(
			ctx,
			generated.HasProactiveCompanyPlanApprovalAuthorityParams{
				ActorRoles:     auth.RolesWith(auth.PermissionCompanyProactiveModeManage),
				ActorUserID:    principal.UserID,
				ConversationID: origin.ConversationID,
				AgentID:        origin.AgentID,
				CompanyID:      principal.CompanyID,
			},
		)
		if err != nil {
			return fmt.Errorf("authorize proactive Company Plan approval: %w", err)
		}
		if automatic {
			result, err = s.resolveCompanyPlanApplyInline(
				ctx,
				companyPlanInlineResolution{
					queries:       q,
					pending:       &pending,
					principal:     principal,
					taskProjectID: actor.TaskProjectID,
					run:           origin,
					approval:      row,
				},
			)
			if err != nil {
				return err
			}
			created = true
			return nil
		}
		result, err = approvalProjectionForActor(ctx, q, row, humanInputProjectionActor(principal, origin))
		if err != nil {
			return err
		}
		inboxResults, err = createApprovalInboxItems(ctx, q, &pending, row, deciders, scoped)
		if err != nil {
			return err
		}
		if err := stageApprovalEvent(
			ctx, q, &pending,
			EventApprovalRequested,
			origin.Actor(),
			result,
		); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return Approval{}, false, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, inboxResult := range inboxResults {
		inbox.PresentCreated(s.inboxPresenter, inboxResult)
	}
	if noResolver {
		return Approval{}, false, auth.ErrNoEligibleResolver
	}
	return result, created, nil
}

type companyPlanInlineResolution struct {
	queries   *generated.Queries
	pending   *[]events.Event
	principal auth.UserPrincipal
	// taskProjectID bounds the apply to the Task chat's Project when the
	// plan was raised there (CompanyPlanActor).
	taskProjectID uuid.NullUUID
	// run is the Run that raised the plan and now applies it for principal.
	run      execution.RunOrigin
	approval generated.Approval
}

func (s *DefaultApprovalService) resolveCompanyPlanApplyInline(
	ctx context.Context,
	input companyPlanInlineResolution,
) (Approval, error) {
	actor := CompanyPlanActor{Principal: input.principal, TaskProjectID: input.taskProjectID, Run: &input.run}
	executionResult, err := s.participant.ExecuteCompanyPlanApply(
		ctx,
		input.queries,
		input.pending,
		actor,
		input.approval.CanonicalAction,
	)
	if err != nil {
		return Approval{}, fmt.Errorf("execute proactive Company Plan action: %w", err)
	}
	executionStatus := ApprovalExecutionExecuted
	if executionResult.SkippedReason != nil {
		executionStatus = ApprovalExecutionSkipped
	}
	resolved, err := input.queries.ResolveApprovalInline(ctx, generated.ResolveApprovalInlineParams{
		DecidedByUserID: input.principal.UserID,
		ExecutionStatus: string(executionStatus),
		ExecutionResult: executionResult.Result,
		SkippedReason:   util.PtrToText(executionResult.SkippedReason),
		CompanyID:       input.approval.CompanyID,
		ApprovalID:      input.approval.ID,
		ExpectedVersion: input.approval.Version,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, ErrApprovalConflict
	}
	if err != nil {
		return Approval{}, fmt.Errorf("resolve proactive Approval: %w", err)
	}
	result, err := approvalProjectionForActor(
		ctx,
		input.queries,
		resolved,
		input.principal.UserID,
	)
	if err != nil {
		return Approval{}, err
	}
	if err := stageApprovalEvent(
		ctx, input.queries, input.pending,
		EventApprovalResolved,
		actor.Actor(),
		result,
	); err != nil {
		return Approval{}, err
	}
	return result, nil
}

func (s *DefaultApprovalService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ApprovalListOptions,
) (ApprovalPage, error) {
	options, err := normalizeApprovalListOptions(options)
	if err != nil {
		return ApprovalPage{}, err
	}
	sets := auth.ProjectPermissionHeldSets()
	params := generated.ListApprovalProjectionsParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID,
		PageSize: options.Limit + 1, Statuses: []string{},
		DecideView:                options.Decide,
		DecidePermission:          string(auth.ProjectPermissionApprovalsResolve),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
		ActorRoles:                auth.RolesWith(auth.PermissionApprovalsResolve),
	}
	if len(options.Statuses) > 0 {
		params.HasStatuses = true
		params.Statuses = make([]string, len(options.Statuses))
		for index, status := range options.Statuses {
			params.Statuses[index] = string(status)
		}
	}
	if options.AgentID != nil {
		params.HasAgentID = true
		params.AgentID = *options.AgentID
	}
	if options.TaskID != nil {
		params.HasTaskID = true
		params.TaskID = *options.TaskID
	}
	if options.Before != nil {
		params.HasCursor = true
		params.BeforeCreatedAt = pgtype.Timestamptz{Time: options.Before.CreatedAt.UTC(), Valid: true}
		params.BeforeID = options.Before.ID
	}
	rows, err := s.store.Queries().ListApprovalProjections(ctx, params)
	if err != nil {
		return ApprovalPage{}, fmt.Errorf("list Approvals: %w", err)
	}
	hasNext := len(rows) > int(options.Limit)
	if hasNext {
		rows = rows[:options.Limit]
	}
	page := ApprovalPage{Items: make([]Approval, len(rows))}
	for index, row := range rows {
		page.Items[index] = approvalFromProjection(
			row.Approval,
			row.RequestingAgentName,
			row.DecidedByDisplayName,
			row.DecidedByAvatarUrl,
		)
	}
	if hasNext {
		last := page.Items[len(page.Items)-1]
		page.Next = &ApprovalCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

func (s *DefaultApprovalService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	approvalID uuid.UUID,
) (Approval, error) {
	row, err := s.store.Queries().GetApprovalProjection(
		ctx,
		generated.GetApprovalProjectionParams{
			CompanyID: principal.CompanyID, ApprovalID: approvalID, ActorUserID: principal.UserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, ErrApprovalNotFound
	}
	if err != nil {
		return Approval{}, fmt.Errorf("get Approval: %w", err)
	}
	return approvalFromProjection(
		row.Approval,
		row.RequestingAgentName,
		row.DecidedByDisplayName,
		row.DecidedByAvatarUrl,
	), nil
}

func (s *DefaultApprovalService) Stats(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ApprovalStatsOptions,
) (ApprovalStats, error) {
	// Nobody is refused: the counts cover only the Approvals the caller may
	// decide (ADR 0017), so a member who decides in no Project gets zeros.
	if options.Since.IsZero() || options.Since.After(time.Now().UTC()) ||
		options.AgentID != nil && !util.ValidUUID(*options.AgentID) {
		return ApprovalStats{}, ErrApprovalInvalidInput
	}
	sets := auth.ProjectPermissionHeldSets()
	params := generated.ApprovalStatsByRuleParams{
		CompanyID:                 principal.CompanyID,
		ActorUserID:               principal.UserID,
		Since:                     pgtype.Timestamptz{Time: options.Since.UTC(), Valid: true},
		DecidePermission:          string(auth.ProjectPermissionApprovalsResolve),
		CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
		ManagementPermissions:     sets.ManagementPermissions,
		ManagementRoles:           sets.ManagementRoles,
		ActorRoles:                auth.RolesWith(auth.PermissionApprovalsResolve),
	}
	if options.AgentID != nil {
		params.HasAgentID = true
		params.AgentID = *options.AgentID
	}
	rows, err := s.store.Queries().ApprovalStatsByRule(ctx, params)
	if err != nil {
		return ApprovalStats{}, fmt.Errorf("read Approval stats: %w", err)
	}
	result := ApprovalStats{Since: options.Since.UTC(), Items: make([]ApprovalStatsItem, len(rows))}
	for index, row := range rows {
		result.Items[index] = ApprovalStatsItem{
			Kind: row.RuleKey, Requested: row.Requested, Approved: row.Approved,
			Rejected: row.Rejected, Expired: row.Expired, Cancelled: row.Cancelled,
		}
		result.Requested += row.Requested
		result.Approved += row.Approved
		result.Rejected += row.Rejected
		result.Expired += row.Expired
		result.Cancelled += row.Cancelled
	}
	return result, nil
}

func (s *DefaultApprovalService) Approve(
	ctx context.Context,
	principal auth.UserPrincipal,
	approvalID uuid.UUID,
	input ApprovalDecisionInput,
) (Approval, error) {
	return s.resolve(ctx, principal, approvalID, ApprovalStatusApproved, input)
}

func (s *DefaultApprovalService) Reject(
	ctx context.Context,
	principal auth.UserPrincipal,
	approvalID uuid.UUID,
	input ApprovalDecisionInput,
) (Approval, error) {
	return s.resolve(ctx, principal, approvalID, ApprovalStatusRejected, input)
}

func (s *DefaultApprovalService) Cancel(
	ctx context.Context,
	principal auth.UserPrincipal,
	approvalID uuid.UUID,
	input ApprovalDecisionInput,
) (Approval, error) {
	return s.resolve(ctx, principal, approvalID, ApprovalStatusCancelled, input)
}

func (s *DefaultApprovalService) resolve(
	ctx context.Context,
	principal auth.UserPrincipal,
	approvalID uuid.UUID,
	status ApprovalStatus,
	input ApprovalDecisionInput,
) (Approval, error) {
	if !util.ValidUUID(approvalID) {
		return Approval{}, ErrApprovalNotFound
	}
	input, err := normalizeApprovalDecisionInput(input)
	if err != nil {
		return Approval{}, err
	}
	var result Approval
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = pending[:0]
		role, active, err := lockActiveHumanInputMember(
			ctx,
			q,
			principal.CompanyID,
			principal.UserID,
		)
		if err != nil {
			return err
		}
		if !active {
			return ErrApprovalNotFound
		}
		current, err := q.LockApprovalForResolution(
			ctx,
			generated.LockApprovalForResolutionParams{
				CompanyID: principal.CompanyID, ApprovalID: approvalID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrApprovalNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Approval: %w", err)
		}
		if !current.TaskID.Valid && current.OriginUserID.UUID != principal.UserID {
			return ErrApprovalNotFound
		}
		projectAccess, scoped, err := requireApprovalDecider(ctx, q, principal, role, current)
		if err != nil {
			return err
		}
		// Approving runs the frozen Company Plan action as the decider, with
		// what companyPlanApplyScope asks for (ADR 0017). Rejecting and
		// cancelling need only the decider's permission. Recipients and
		// the raise's eligibility count only people who may approve
		// (companyPlanApproversInTx).
		var taskProjectID uuid.NullUUID
		if status == ApprovalStatusApproved {
			taskProjectID, err = companyPlanApplyScope(role, projectAccess, scoped)
			if err != nil {
				return err
			}
		}
		if approvalHasLapsed(current, time.Now().UTC()) {
			return ErrApprovalExpired
		}
		if current.Status != string(ApprovalStatusPending) || current.Version != input.ExpectedRevision {
			return ErrApprovalConflict
		}

		params := generated.ResolveApprovalParams{
			Status:             string(status),
			DecisionNote:       util.StrToText(input.DecisionNote),
			DecidedByUserID:    principal.UserID,
			ContinuationPrompt: approvalContinuationPrompt(status, input.DecisionNote),
			CompanyID:          principal.CompanyID,
			ApprovalID:         approvalID,
			ExpectedVersion:    input.ExpectedRevision,
		}
		if status == ApprovalStatusApproved {
			executionResult, err := s.participant.ExecuteCompanyPlanApply(
				ctx, q, &pending,
				CompanyPlanActor{Principal: principal, TaskProjectID: taskProjectID},
				current.CanonicalAction,
			)
			if err != nil {
				return fmt.Errorf("execute approved Company Plan action: %w", err)
			}
			params.ExecutionResult = executionResult.Result
			if executionResult.SkippedReason != nil {
				params.ExecutionStatus = pgtype.Text{
					String: string(ApprovalExecutionSkipped), Valid: true,
				}
				params.SkippedReason = util.PtrToText(executionResult.SkippedReason)
			} else {
				params.ExecutionStatus = pgtype.Text{
					String: string(ApprovalExecutionExecuted), Valid: true,
				}
			}
		}
		resolved, err := q.ResolveApproval(ctx, params)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrApprovalConflict
		}
		if err != nil {
			return fmt.Errorf("resolve Approval: %w", err)
		}
		actor := inbox.Actor{Kind: "user", ID: &principal.UserID}
		if err := inbox.CloseEntityInTx(
			ctx,
			q,
			&pending,
			principal.CompanyID,
			inbox.Entity{Type: protocol.EntityApproval, ID: approvalID.String()},
			actor,
		); err != nil {
			return fmt.Errorf("close Approval Inbox items: %w", err)
		}
		result, err = approvalProjectionForActor(ctx, q, resolved, principal.UserID)
		if err != nil {
			return err
		}
		return stageApprovalEvent(
			ctx, q, &pending,
			EventApprovalResolved,
			events.UserActor(principal.UserID.String()),
			result,
		)
	})
	if err != nil {
		return Approval{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

// requireApprovalDecider decides whether the caller may resolve the locked
// Approval. A Task-origin Approval raised in a Task chat with a Project is
// decided inside that Project (ADR 0017): the caller needs
// project.approvals.resolve there, the Company catalog is not consulted, and
// a caller who cannot read the Project cannot see the Approval at all. Every
// other Approval needs the Company approvals.resolve, as before. scoped
// reports the Project case, and access is the caller's standing there.
func requireApprovalDecider(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	role auth.CompanyRole,
	current generated.Approval,
) (access auth.ProjectAccess, scoped bool, err error) {
	if current.TaskID.Valid {
		access, scoped, err = auth.RequireTaskChatPermission(
			ctx, q, principal, current.ConversationID, auth.ProjectPermissionApprovalsResolve,
		)
		if errors.Is(err, auth.ErrTaskNotVisible) {
			return auth.ProjectAccess{}, scoped, ErrApprovalNotFound
		}
		if scoped || err != nil {
			return access, scoped, err
		}
	}
	if !role.Can(auth.PermissionApprovalsResolve) {
		return auth.ProjectAccess{}, false, ErrApprovalForbidden
	}
	return auth.ProjectAccess{}, false, nil
}

// companyPlanApplyScope decides what approving a Company Plan Approval
// needs on top of deciding it, and the Project the apply is bound to. A plan
// raised in a Task chat with a Project (scoped) is applied in that Project
// alone, so the decider needs project.tasks.write there, beside the
// project.approvals.resolve that requireApprovalDecider checked; the Company
// role plays no part. Any other plan is a Company action under
// company.plan.apply, which the Plan participant checks again; refusing
// here keeps that check from surfacing as a 500.
func companyPlanApplyScope(
	role auth.CompanyRole,
	access auth.ProjectAccess,
	scoped bool,
) (uuid.NullUUID, error) {
	switch {
	case scoped && !access.Can(auth.ProjectPermissionTasksWrite):
		return uuid.NullUUID{}, auth.ErrProjectPermissionRequired
	case scoped:
		return uuid.NullUUID{UUID: access.ProjectID, Valid: true}, nil
	case !role.Can(auth.PermissionCompanyPlanApply):
		return uuid.NullUUID{}, ErrApprovalForbidden
	}
	return uuid.NullUUID{}, nil
}

// approvalHasLapsed decides whether a locked Approval is past its deadline, by
// either of the two ways that can be true: the sweeper already stamped the row
// expired, or the row is still pending because the sweeper has not reached it
// yet and its deadline has passed. The row cannot say so on its own — status
// is what the last writer left, and expires_at is only a timestamp — so the
// answer needs the clock as well as the row.
func approvalHasLapsed(current generated.Approval, now time.Time) bool {
	return current.Status == string(ApprovalStatusExpired) ||
		current.Status == string(ApprovalStatusPending) && !current.ExpiresAt.Time.After(now)
}

// raiseOriginMatchesPrincipal decides whether the Run raising this Approval
// belongs to the caller: the run token must name the caller's own Company, and
// it must carry a real idempotency key.
//
// Neither fact is in the request body — the origin comes from the run token
// and the key from the Idempotency-Key header — so no binding tag can state
// either, and the Company match is the cross-Company fence for a token that
// authenticated somewhere else.
func raiseOriginMatchesPrincipal(
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	requestKey uuid.UUID,
) bool {
	return principal.CompanyID == origin.CompanyID && util.ValidUUID(requestKey)
}

// companyPlanRequestIsWellFormed decides whether the raised action is a JSON
// document this server will store: present, inside the byte budget, and
// parseable. It is checked before json.Compact, which would otherwise be
// handed unbounded bytes from a Run.
func companyPlanRequestIsWellFormed(request json.RawMessage) bool {
	return len(request) > 0 && len(request) <= maxCompanyPlanActionBytes && json.Valid(request)
}

func normalizeApprovalRequest(
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	requestKey uuid.UUID,
	request json.RawMessage,
) (json.RawMessage, []byte, error) {
	if !raiseOriginMatchesPrincipal(principal, origin, requestKey) ||
		!companyPlanRequestIsWellFormed(request) {
		return nil, nil, ErrApprovalInvalidInput
	}
	if origin.TaskID == nil {
		if origin.OriginUserID == nil ||
			(origin.ConversationKind != "channel_agent" && *origin.OriginUserID != principal.UserID) {
			return nil, nil, ErrApprovalForbidden
		}
	}
	compacted := make([]byte, 0, len(request))
	buffer := bytes.NewBuffer(compacted)
	if err := json.Compact(buffer, request); err != nil {
		return nil, nil, ErrApprovalInvalidInput
	}
	canonical := json.RawMessage(buffer.Bytes())
	digest := sha256.Sum256(canonical)
	return canonical, digest[:], nil
}

// preparedActionIsWellFormed decides whether a prepared company-plan action
// can be stored and shown: the canonical action is present, inside the byte
// budget, and parseable JSON; the preview is non-empty, valid UTF-8, and
// inside both of its budgets — 64 Ki runes for what a person will read, 256
// KiB for what the column will hold.
//
// The value is built by the server, never bound from a request, so no tag
// reaches it; and the rune and byte caps are two different limits on one
// string, which is why they are both here rather than either one alone.
func preparedActionIsWellFormed(prepared PreparedCompanyPlanAction) bool {
	return len(prepared.CanonicalAction) > 0 &&
		len(prepared.CanonicalAction) <= maxCompanyPlanActionBytes &&
		json.Valid(prepared.CanonicalAction) &&
		len(prepared.Presentation) > 0 &&
		len(prepared.Presentation) <= maxCompanyPlanActionBytes &&
		json.Valid(prepared.Presentation) &&
		prepared.PreviewMarkdown != "" &&
		utf8.ValidString(prepared.PreviewMarkdown) &&
		utf8.RuneCountInString(prepared.PreviewMarkdown) <= 64*1024 &&
		len(prepared.PreviewMarkdown) <= 256*1024
}

func normalizePreparedCompanyPlanAction(
	prepared PreparedCompanyPlanAction,
) (PreparedCompanyPlanAction, []byte, error) {
	prepared.PreviewMarkdown = protocol.TrimContractWhitespace(prepared.PreviewMarkdown)
	if !preparedActionIsWellFormed(prepared) {
		return PreparedCompanyPlanAction{}, nil, ErrApprovalInvalidInput
	}
	if prepared.Summary != nil {
		value := protocol.TrimContractWhitespace(*prepared.Summary)
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 2000 || len(value) > 8192 {
			return PreparedCompanyPlanAction{}, nil, ErrApprovalInvalidInput
		}
		prepared.Summary = &value
	}
	digest := sha256.Sum256(prepared.CanonicalAction)
	return prepared, digest[:], nil
}

func normalizeApprovalListOptions(options ApprovalListOptions) (ApprovalListOptions, error) {
	if options.Limit == 0 {
		options.Limit = 50
	}
	if len(options.Statuses) == 0 {
		options.Statuses = []ApprovalStatus{ApprovalStatusPending}
	}
	if options.Limit < 1 || options.Limit > 100 || options.AgentID != nil && !util.ValidUUID(*options.AgentID) ||
		options.TaskID != nil && !util.ValidUUID(*options.TaskID) {
		return ApprovalListOptions{}, ErrApprovalInvalidInput
	}
	seen := make(map[ApprovalStatus]struct{}, len(options.Statuses))
	for _, status := range options.Statuses {
		if !validApprovalStatus(status) {
			return ApprovalListOptions{}, ErrApprovalInvalidInput
		}
		if _, duplicate := seen[status]; duplicate {
			return ApprovalListOptions{}, ErrApprovalInvalidInput
		}
		seen[status] = struct{}{}
	}
	if options.Before != nil && (options.Before.CreatedAt.IsZero() || !util.ValidUUID(options.Before.ID)) {
		return ApprovalListOptions{}, ErrApprovalInvalidInput
	}
	return options, nil
}

// createApprovalInboxItems addresses the Approval to who may decide it. A
// Task-origin Approval in a Task chat with a Project goes to deciders, its
// Project's readers holding project.approvals.resolve (scoped, ADR 0017);
// every other one to the Company approvals.resolve holders, narrowed to the
// origin person for a direct chat.
func createApprovalInboxItems(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	row generated.Approval,
	deciders []uuid.UUID,
	scoped bool,
) ([]inbox.CreateResult, error) {
	recipients := deciders
	if !scoped {
		var err error
		recipients, err = q.ListActiveCompanyMemberIDsByRoles(
			ctx,
			generated.ListActiveCompanyMemberIDsByRolesParams{
				CompanyID: row.CompanyID,
				Roles:     auth.RolesWith(auth.PermissionApprovalsResolve),
			},
		)
		if err != nil {
			return nil, fmt.Errorf("list Approval resolvers: %w", err)
		}
	}
	if !row.TaskID.Valid {
		filtered := recipients[:0]
		for _, recipientID := range recipients {
			if recipientID == row.OriginUserID.UUID {
				filtered = append(filtered, recipientID)
			}
		}
		recipients = filtered
	}
	deepLink := requestChatDeepLink(row.RequestedByAgentID, row.ConversationID, row.TaskID) +
		"&approvalId=" + row.ID.String()
	body := "An agent needs approval to apply the Company Plan."
	results := make([]inbox.CreateResult, 0, len(recipients))
	for _, recipientID := range recipients {
		result, err := inbox.CreateInTx(ctx, q, pending, inbox.CreateInput{
			CompanyID:       row.CompanyID,
			RecipientUserID: recipientID,
			ConversationID:  &row.ConversationID,
			Type:            inbox.TypeApprovalRequest,
			Entity:          inbox.Entity{Type: protocol.EntityApproval, ID: row.ID.String()},
			Title:           row.Title,
			Body:            body,
			DeepLink:        &deepLink,
			DedupeKey:       "approval:" + row.ID.String() + ":requested",
			Actor:           inbox.Actor{Kind: "agent", ID: &row.RequestedByAgentID},
		})
		if err != nil {
			return nil, fmt.Errorf("create Approval Inbox item: %w", err)
		}
		if result.Created {
			results = append(results, result)
		}
	}
	return results, nil
}

func requestChatDeepLink(agentID, conversationID uuid.UUID, taskID uuid.NullUUID) string {
	query := url.Values{
		"agentId":          []string{agentID.String()},
		"conversationId":   []string{conversationID.String()},
		"conversationKind": []string{"direct"},
	}
	if taskID.Valid {
		query.Set("conversationKind", "task")
		query.Set("taskId", taskID.UUID.String())
	}
	return "/app?" + query.Encode()
}

func approvalProjectionForActor(
	ctx context.Context,
	q *generated.Queries,
	row generated.Approval,
	actorUserID uuid.UUID,
) (Approval, error) {
	projection, err := q.GetApprovalProjection(ctx, generated.GetApprovalProjectionParams{
		CompanyID: row.CompanyID, ApprovalID: row.ID, ActorUserID: actorUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, ErrApprovalNotFound
	}
	if err != nil {
		return Approval{}, fmt.Errorf("load Approval projection: %w", err)
	}
	return approvalFromProjection(
		projection.Approval,
		projection.RequestingAgentName,
		projection.DecidedByDisplayName,
		projection.DecidedByAvatarUrl,
	), nil
}

// approvalProjectionForSystem reads the Approval with no reader filter, for
// frames and lifecycle writes the server stages itself; never for a person's read.
func approvalProjectionForSystem(
	ctx context.Context,
	q *generated.Queries,
	row generated.Approval,
) (Approval, error) {
	projection, err := q.GetApprovalProjectionForSystem(ctx, generated.GetApprovalProjectionForSystemParams{
		CompanyID: row.CompanyID, ApprovalID: row.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, ErrApprovalNotFound
	}
	if err != nil {
		return Approval{}, fmt.Errorf("load Approval projection: %w", err)
	}
	return approvalFromProjection(
		projection.Approval,
		projection.RequestingAgentName,
		projection.DecidedByDisplayName,
		projection.DecidedByAvatarUrl,
	), nil
}

func approvalFromProjection(
	row generated.Approval,
	requestingAgentName string,
	decidedByDisplayName, decidedByAvatarURL pgtype.Text,
) Approval {
	value := Approval{
		ID: row.ID, CompanyID: row.CompanyID, Type: ApprovalType(row.Type),
		Status: ApprovalStatus(row.Status), Title: row.Title,
		Summary: util.TextToPtr(row.Summary), PreviewMarkdown: row.PreviewMarkdown,
		Presentation: json.RawMessage(row.Presentation),
		RequestedByAgent: ActorRef{
			ID: row.RequestedByAgentID, Kind: "agent", DisplayName: requestingAgentName,
		},
		OriginRunID: row.OriginRunID, TaskID: util.NullUUIDPtr(row.TaskID),
		ConversationID: row.ConversationID, OriginUserID: row.OriginUserID.UUID,
		ExpiresAt: row.ExpiresAt.Time.UTC(), DecisionNote: util.TextToPtr(row.DecisionNote),
		ContinuationRunID: util.NullUUIDPtr(row.ContinuationRunID),
		CreatedAt:         row.CreatedAt.Time.UTC(), UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
	if row.DecidedByUserID.Valid {
		value.DecidedBy = &ActorRef{
			ID: row.DecidedByUserID.UUID, Kind: "user",
			DisplayName: decidedByDisplayName.String,
			AvatarURL:   util.TextToPtr(decidedByAvatarURL),
		}
	}
	if row.ExecutionStatus.Valid {
		value.Execution = &ApprovalExecution{
			Status:        ApprovalExecutionStatus(row.ExecutionStatus.String),
			SkippedReason: util.TextToPtr(row.SkippedReason),
			Result:        json.RawMessage(row.ExecutionResult),
		}
	}
	return value
}

// stageApprovalEvent adds one Approval frame to the producing transaction's
// batch. A staging failure aborts that transaction: an event that cannot be
// event describes a fact that must not commit.
//
// A request no Task owns names its origin user, and the realtime bridge
// derives that route from the payload. One raised in a Task chat, or naming
// a Task, reaches the readers of that Project, decided here in the
// transaction (ADR 0017).
func stageApprovalEvent(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	name string,
	actor events.Actor,
	value Approval,
) error {
	return stageApprovalFrames(ctx, q, pending, approvalEvent(name, actor, value))
}

// stageApprovalFrames stamps the Project audience of Approval and Question
// frames and adds them to the transaction's batch.
func stageApprovalFrames(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	staged ...events.Event,
) error {
	if err := auth.StampProjectAudiencesInTx(ctx, q, staged); err != nil {
		return err
	}
	*pending = append(*pending, staged...)
	return nil
}

// approvalEvent builds one approval.* frame around an Approval snapshot.
func approvalEvent(name string, actor events.Actor, value Approval) events.Event {
	return events.Event{
		EntityType:     Topic,
		Name:           name,
		CompanyID:      value.CompanyID,
		CreatedAt:      time.Now().UTC(),
		RunID:          value.OriginRunID,
		TaskID:         util.UUIDToValue(value.TaskID),
		ConversationID: value.ConversationID,
		Actor:          actor,
		Payload:        value,
	}
}

func approvalContinuationPrompt(status ApprovalStatus, note string) string {
	var builder strings.Builder
	builder.WriteString("The Company Plan approval was ")
	builder.WriteString(string(status))
	builder.WriteString(".")
	if note != "" {
		builder.WriteString(" Decision note: ")
		builder.WriteString(note)
	}
	return builder.String()
}
