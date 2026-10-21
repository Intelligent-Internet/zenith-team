package company

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

var (
	ErrInitializationConflict = errors.New("company initialization cannot advance")
	ErrInitializationModel    = errors.New("Luna is unavailable on the selected runtime")
)

// InitializationService owns the durable research receipt and the one
// background research Run that writes the Company Wiki. Starting is idempotent:
// reconnecting clients resume the same receipt, and nothing after the start
// needs a client: the Run terminal path completes the receipt and notifies.
type InitializationService interface {
	Get(context.Context, auth.UserPrincipal) (Initialization, error)
	Advance(context.Context, auth.UserPrincipal, InitializationInput) (Initialization, error)
	Reconcile(context.Context) error
}

var _ InitializationService = (*DefaultInitializationService)(nil)

type DefaultInitializationService struct {
	store           *db.Store
	bus             *events.Bus
	runtimes        agent.RuntimeService
	taskMessageRuns conversation.TaskMessageRunPreparer
	reconcileMu     sync.Mutex
	reconcileAfter  uuid.UUID
}

type InitializationServiceOptions struct {
	Store           *db.Store
	Bus             *events.Bus
	Runtimes        agent.RuntimeService
	TaskMessageRuns conversation.TaskMessageRunPreparer
}

func NewInitializationService(options InitializationServiceOptions) (*DefaultInitializationService, error) {
	if options.Store == nil || options.Bus == nil || options.Runtimes == nil || options.TaskMessageRuns == nil {
		return nil, errors.New(
			"initialization store, bus, runtime service and Task Message Run preparer are required",
		)
	}
	return &DefaultInitializationService{
		store:           options.Store,
		bus:             options.Bus,
		runtimes:        options.Runtimes,
		taskMessageRuns: options.TaskMessageRuns,
	}, nil
}

type InitializationInput struct {
	ConversationID   uuid.UUID
	ModelDiscoveryID uuid.UUID
	Retry            bool
}

// Initialization is the research's progress. Phase is not_started before the
// Run exists, research while it works or after it failed, and ready once it
// completed; PagesWritten and OverviewPageID show what it has saved so far.
type Initialization struct {
	Phase          string     `json:"phase"`
	Status         string     `json:"status"`
	RunID          *uuid.UUID `json:"runId,omitempty"`
	ConversationID *uuid.UUID `json:"conversationId,omitempty"`
	Message        string     `json:"message,omitempty"`
	PagesWritten   int        `json:"pagesWritten"`
	OverviewPageID *uuid.UUID `json:"overviewPageId,omitempty"`
}

// researchOverviewKey is the hub page key the research prompt names.
const researchOverviewKey = "company-overview"

func (s *DefaultInitializationService) Get(ctx context.Context, p auth.UserPrincipal) (Initialization, error) {
	row, err := s.store.Queries().GetCompanyInitialization(ctx, p.CompanyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Initialization{Phase: "not_started", Status: "idle"}, nil
	}
	if err != nil {
		return Initialization{}, fmt.Errorf("read initialization: %w", err)
	}
	if row.CreatedByUserID != p.UserID {
		return Initialization{}, ErrForbidden
	}
	return readInitialization(ctx, s.store.Queries(), row)
}

func readInitialization(
	ctx context.Context,
	q *generated.Queries,
	row generated.CompanyInitialization,
) (Initialization, error) {
	state := Initialization{Phase: "not_started", Status: "awaiting_model", ConversationID: &row.ConversationID}
	if !row.ResearchRunID.Valid {
		return state, nil
	}
	state.Phase = "research"
	run, err := q.GetInitializationRun(ctx, row.ResearchRunID.UUID)
	if err != nil {
		return Initialization{}, fmt.Errorf("read initialization Run: %w", err)
	}
	state.RunID = &run.ID
	// Every page the research has written, across its automatic retries and
	// the founder's retries: what an earlier attempt saved still counts.
	pages, err := q.CountInitializationWikiWrites(ctx, row.CompanyID)
	if err != nil {
		return Initialization{}, fmt.Errorf("count initialization pages: %w", err)
	}
	state.PagesWritten = int(pages)
	overview, err := q.GetInitializationOverviewPage(
		ctx,
		generated.GetInitializationOverviewPageParams{CompanyID: row.CompanyID, Key: researchOverviewKey},
	)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Initialization{}, fmt.Errorf("read initialization overview page: %w", err)
	}
	if err == nil {
		state.OverviewPageID = &overview
	}
	switch run.Status {
	case "completed":
		state.Phase = "ready"
		state.Status = "completed"
	case "failed", "cancelled":
		state.Status = "failed"
		state.Message = "The company research could not finish. Check your Codex connection and retry."
	default:
		state.Status = run.Status
		if run.RuntimeStatus != "online" {
			state.Status = "waiting_runtime"
			state.Message = "Reconnect your computer to continue. Your progress is saved."
		}
	}
	return state, nil
}

// Advance only starts the research, and is also the recovery path after a
// reload: an existing research Run is returned as it is, and only a failed one
// restarts, and only on Retry. The company lock serializes receipt creation and
// the Run enqueue with membership changes and other callers.
func (s *DefaultInitializationService) Advance(
	ctx context.Context,
	p auth.UserPrincipal,
	in InitializationInput,
) (Initialization, error) {
	return s.advance(ctx, p.CompanyID, p.UserID, in, &p)
}

// Reconcile advances one bounded page of existing receipts. Its process-local
// cursor is only scan position; PostgreSQL and the Company lock own progress.
// Moving past every candidate, including unavailable ones, prevents starvation.
func (s *DefaultInitializationService) Reconcile(ctx context.Context) error {
	const pageSize = 100
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	rows, err := s.store.Queries().
		ListPendingCompanyInitializations(ctx, generated.ListPendingCompanyInitializationsParams{
			AfterCompanyID: s.reconcileAfter, RowLimit: pageSize,
		})
	if err != nil {
		return fmt.Errorf("list pending initializations: %w", err)
	}
	if len(rows) == 0 {
		s.reconcileAfter = uuid.Nil
		return nil
	}
	var failures []error
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		s.reconcileAfter = row.CompanyID
		_, err := s.advance(ctx, row.CompanyID, row.CreatedByUserID, InitializationInput{}, nil)
		if err == nil || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) ||
			errors.Is(err, ErrForbidden) ||
			errors.Is(err, execution.ErrRunAgentNotBound) ||
			errors.Is(err, execution.ErrRunAgentArchived) ||
			errors.Is(err, conversation.ErrNotFound) {
			continue
		}
		failures = append(failures, fmt.Errorf("reconcile initialization %s: %w", row.CompanyID, err))
	}
	if len(rows) < pageSize {
		s.reconcileAfter = uuid.Nil
	}
	return errors.Join(failures...)
}

// A nil requester restricts reconciliation to completed phases. It cannot
// create a receipt, discover a model, or request a retry. Creator authority is
// re-read under its membership lock; the worker never invents a principal.
func (s *DefaultInitializationService) advance(
	ctx context.Context,
	companyID, actorUserID uuid.UUID,
	in InitializationInput,
	requester *auth.UserPrincipal,
) (Initialization, error) {
	var result Initialization
	var pending []events.Event
	var queued *generated.Run
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		pending = nil
		queued = nil
		if _, err := q.LockCompanyForMembershipMutation(ctx, companyID); err != nil {
			return err
		}
		if _, err := authorizeMembershipActor(
			ctx,
			q,
			companyID,
			actorUserID,
			auth.PermissionAgentsManage,
		); err != nil {
			return err
		}
		row, err := q.GetCompanyInitialization(ctx, companyID)
		fresh := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !fresh {
			return err
		}
		if !fresh && row.CreatedByUserID != actorUserID {
			return ErrForbidden
		}
		if fresh {
			if requester == nil {
				return nil
			}
			if in.ConversationID == uuid.Nil {
				return ErrInitializationConflict
			}
			row = generated.CompanyInitialization{
				CompanyID:       companyID,
				CreatedByUserID: actorUserID,
				ConversationID:  in.ConversationID,
			}
		} else {
			result, err = readInitialization(ctx, q, row)
			if err != nil {
				return err
			}
			retrying := requester != nil && in.Retry && result.Status == "failed"
			final := result.Status == "completed" || result.Status == "failed"
			if row.ResearchRunID.Valid && final && !retrying && !row.CompletedAt.Valid {
				// The Run terminal path normally completes the receipt; this is
				// the recovery for a receipt it could not reach.
				if err := s.settleInTx(ctx, q, &pending, &queued, actorUserID, row); err != nil {
					return err
				}
				return nil
			}
			if row.ResearchRunID.Valid && !retrying || requester == nil {
				return nil
			}
		}
		target, err := q.GetInitializationTarget(
			ctx,
			generated.GetInitializationTargetParams{
				ActorUserID:    actorUserID,
				CompanyID:      companyID,
				ConversationID: row.ConversationID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return execution.ErrRunAgentNotBound
		}
		if err != nil {
			return err
		}
		if in.ModelDiscoveryID == uuid.Nil {
			if in.Retry && !fresh {
				return ErrInitializationModel
			}
			// The receipt is recorded before the model is known, so a reload
			// resumes the same step.
			if fresh {
				if err := q.InsertCompanyInitialization(
					ctx,
					generated.InsertCompanyInitializationParams{
						CompanyID:       companyID,
						CreatedByUserID: actorUserID,
						ConversationID:  row.ConversationID,
					},
				); err != nil {
					return err
				}
			}
			result, err = readInitialization(ctx, q, row)
			return err
		}
		discovery, err := s.runtimes.GetModelDiscovery(ctx, *requester, target.RuntimeID, in.ModelDiscoveryID)
		if err != nil {
			return err
		}
		if discovery.Status != agent.ModelDiscoveryCompleted {
			return ErrInitializationModel
		}
		model, level, err := initializationModel(discovery.Models)
		if err != nil {
			return err
		}
		run, err := conversation.MaterializeInitializationRunInTx(ctx, q, &pending, execution.RunTrigger{
			CompanyID:             companyID,
			ConversationID:        row.ConversationID,
			AgentID:               target.AgentID,
			AuthorizedByUserID:    actorUserID,
			ForceFreshSession:     true,
			Source:                execution.RunSourceCompanyResearch,
			ResearchModel:         model,
			ResearchThinkingLevel: level,
			TriggerPrompt:         "Research this company in the background and write what you learn into the Company Wiki as a small set of linked pages, starting with the company-overview page. Use the company description, website, reference documents and available authorized Gmail, Drive and GitHub sources.",
		})
		if err != nil {
			return err
		}
		queued = &run
		id := uuid.NullUUID{UUID: run.ID, Valid: true}
		if fresh {
			err = q.InsertCompanyInitialization(
				ctx,
				generated.InsertCompanyInitializationParams{
					CompanyID:       companyID,
					CreatedByUserID: actorUserID,
					ConversationID:  row.ConversationID,
					ResearchRunID:   id,
				},
			)
		} else {
			err = q.SetInitializationResearchRun(
				ctx,
				generated.SetInitializationResearchRunParams{CompanyID: companyID, ResearchRunID: id},
			)
			if err == nil && result.RunID != nil {
				// The retry answers the failure notice: archive it so the
				// Inbox does not keep saying the research could not finish.
				err = inbox.CloseEntityInTx(ctx, q, &pending, companyID,
					inbox.Entity{Type: protocol.EntityRun, ID: result.RunID.String()},
					inbox.Actor{Kind: "user", ID: &actorUserID})
			}
		}
		if err != nil {
			return err
		}
		row.ResearchRunID = id
		result, err = readInitialization(ctx, q, row)
		return err
	})
	if err != nil {
		return Initialization{}, fmt.Errorf("advance initialization: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	if queued != nil {
		execution.PublishRuntimeWork(context.Background(), s.bus, *queued)
	}
	return result, nil
}

// settleInTx completes a receipt whose research Run is final and releases the
// Conversation's preserved prompt queue. The Run terminal path normally does
// this already; the caller owns the Company lock, and this takes the
// Conversation lock the queue promotion needs.
func (s *DefaultInitializationService) settleInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	queued **generated.Run,
	actorUserID uuid.UUID,
	row generated.CompanyInitialization,
) error {
	if _, err := q.LockConversationForActiveUserMutation(ctx, generated.LockConversationForActiveUserMutationParams{
		ActorUserID: actorUserID, CompanyID: row.CompanyID, ConversationID: row.ConversationID,
	}); err != nil {
		return err
	}
	if err := q.CompleteCompanyInitialization(ctx, row.CompanyID); err != nil {
		return err
	}
	promotion, err := conversation.ResumeInitializationQueueInTx(
		ctx,
		q,
		pending,
		s.taskMessageRuns,
		row.CompanyID,
		row.ConversationID,
	)
	if err != nil || promotion.RunID == nil {
		return err
	}
	run, err := q.GetRun(ctx, *promotion.RunID)
	if err != nil {
		return err
	}
	*queued = &run
	return nil
}

// lunaModelID is the Luna family as Codex names it: the bare alias or
// gpt-<version>-luna ("gpt-6-luna", "gpt-5.6-luna"). Runtimes differ in which
// generation they advertise, so any member qualifies, but a model that merely
// ends in "-luna" does not.
var lunaModelID = regexp.MustCompile(`^(?:luna|gpt-(\d+)(?:\.(\d+))?-luna)$`)

// initializationModel picks the newest advertised Luna and its lowest known
// reasoning level, or the runtime default when it advertises none we know.
func initializationModel(models []provideragent.RuntimeModel) (string, string, error) {
	var chosen *provideragent.RuntimeModel
	bestMajor, bestMinor := -1, -1
	for i := range models {
		match := lunaModelID.FindStringSubmatch(models[i].ID)
		if match == nil {
			continue
		}
		// The bare alias ranks below any versioned Luna.
		major, minor := 0, 0
		if match[1] != "" {
			major, _ = strconv.Atoi(match[1])
			minor, _ = strconv.Atoi(match[2])
			major++
		}
		if major > bestMajor || major == bestMajor && minor > bestMinor {
			chosen, bestMajor, bestMinor = &models[i], major, minor
		}
	}
	if chosen == nil {
		return "", "", ErrInitializationModel
	}
	if chosen.Reasoning == nil || len(chosen.Reasoning.Levels) == 0 {
		return chosen.ID, "", nil
	}
	for _, preferred := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		for _, level := range chosen.Reasoning.Levels {
			if level.Value == preferred {
				return chosen.ID, level.Value, nil
			}
		}
	}
	return chosen.ID, "", nil
}
