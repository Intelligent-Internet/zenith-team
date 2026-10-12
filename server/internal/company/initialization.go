package company

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	provideragent "boardly.dev/boardly/server/pkg/agent"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

var (
	ErrInitializationConflict = errors.New("company initialization cannot advance")
	ErrInitializationModel    = errors.New("Luna is unavailable on the selected runtime")
)

// InitializationService owns the durable research receipt and the one welcome
// Run. Advancing is idempotent: reconnecting clients resume the same receipt.
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
		return nil, errors.New("initialization store, bus, runtime service and Task Message Run preparer are required")
	}
	return &DefaultInitializationService{
		store: options.Store, bus: options.Bus, runtimes: options.Runtimes, taskMessageRuns: options.TaskMessageRuns,
	}, nil
}

type InitializationInput struct {
	ConversationID   uuid.UUID
	ModelDiscoveryID uuid.UUID
	Retry            bool
}

type Initialization struct {
	Phase          string     `json:"phase"`
	Status         string     `json:"status"`
	RunID          *uuid.UUID `json:"runId,omitempty"`
	ConversationID *uuid.UUID `json:"conversationId,omitempty"`
	Message        string     `json:"message,omitempty"`
}

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

func readInitialization(ctx context.Context, q *generated.Queries, row generated.CompanyInitialization) (Initialization, error) {
	state := Initialization{Phase: "research", Status: "failed", ConversationID: &row.ConversationID}
	runID := row.ResearchRunID
	if row.ContextMarkdown != "" {
		state.Phase = "greeting"
		runID = row.WelcomeRunID
	}
	if !runID.Valid {
		if state.Phase == "research" {
			state.Status = "awaiting_model"
			return state, nil
		}
		state.Message = "The initialization run is no longer available. Retry to continue."
		return state, nil
	}
	run, err := q.GetInitializationRun(ctx, runID.UUID)
	if err != nil {
		return Initialization{}, fmt.Errorf("read initialization Run: %w", err)
	}
	state.RunID = &run.ID
	switch run.Status {
	case "completed":
		if state.Phase == "greeting" && strings.TrimSpace(run.Output) != "" {
			state.Phase = "ready"
			state.Status = "completed"
		} else if _, ok := initializationContext(run.Output); state.Phase == "research" && ok {
			state.Status = "completed"
		} else {
			state.Message = "This step finished without the expected content. Retry to continue."
		}
	case "failed", "cancelled":
		state.Message = "This step could not finish. Check your Codex connection and retry."
	default:
		state.Status = run.Status
		if run.RuntimeStatus != "online" {
			state.Status = "waiting_runtime"
			state.Message = "Reconnect your computer to continue. Your progress is saved."
		}
	}
	return state, nil
}

// Advance is also the recovery path after a reload. The company lock serializes
// receipt creation and greeting enqueue with membership changes and other callers.
// Research output is already durable in the Run transcript if no client is open.
func (s *DefaultInitializationService) Advance(ctx context.Context, p auth.UserPrincipal, in InitializationInput) (Initialization, error) {
	return s.advance(ctx, p.CompanyID, p.UserID, in, &p)
}

// Reconcile advances one bounded page of existing receipts. Its process-local
// cursor is only scan position; PostgreSQL and the Company lock own progress.
// Moving past every candidate, including unavailable ones, prevents starvation.
func (s *DefaultInitializationService) Reconcile(ctx context.Context) error {
	const pageSize = 100
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()
	rows, err := s.store.Queries().ListPendingCompanyInitializations(ctx, generated.ListPendingCompanyInitializationsParams{
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
		if err == nil || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) ||
			errors.Is(err, execution.ErrRunAgentNotBound) || errors.Is(err, execution.ErrRunAgentArchived) ||
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
		if _, err := authorizeMembershipActor(ctx, q, companyID, actorUserID, auth.PermissionAgentsManage); err != nil {
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
			row = generated.CompanyInitialization{CompanyID: companyID, CreatedByUserID: actorUserID, ConversationID: in.ConversationID}
		} else {
			result, err = readInitialization(ctx, q, row)
			if err != nil {
				return err
			}
			if row.CompletedAt.Valid || requester == nil && result.Status != "completed" {
				return nil
			}
			if result.Status != "completed" && result.Status != "awaiting_model" && !(result.Status == "failed" && in.Retry) {
				return nil
			}
			if result.Phase == "research" && result.Status == "completed" {
				run, err := q.GetInitializationRun(ctx, row.ResearchRunID.UUID)
				if err != nil {
					return err
				}
				markdown, ok := initializationContext(run.Output)
				if !ok {
					return ErrInitializationConflict
				}
				row.ContextMarkdown = markdown
				if err := q.SaveInitializationContext(ctx, generated.SaveInitializationContextParams{CompanyID: companyID, ContextMarkdown: markdown}); err != nil {
					return err
				}
			}
		}
		if result.Phase == "ready" {
			// The welcome's terminal proof establishes completed work. Removing
			// its binding afterward must not keep preparation pending; queued
			// prompts independently recheck their authority before promotion.
			if _, err := q.LockConversationForActiveUserMutation(ctx, generated.LockConversationForActiveUserMutationParams{
				ActorUserID: actorUserID, CompanyID: companyID, ConversationID: row.ConversationID,
			}); err != nil {
				return err
			}
			if err := q.CompleteCompanyInitialization(ctx, companyID); err != nil {
				return err
			}
			promotion, err := conversation.ResumeInitializationQueueInTx(ctx, q, &pending, s.taskMessageRuns, companyID, row.ConversationID)
			if err != nil || promotion.RunID == nil {
				return err
			}
			run, err := q.GetRun(ctx, *promotion.RunID)
			queued = &run
			return err
		}
		target, err := q.GetInitializationTarget(ctx, generated.GetInitializationTargetParams{ActorUserID: actorUserID, CompanyID: companyID, ConversationID: row.ConversationID})
		if errors.Is(err, pgx.ErrNoRows) {
			return execution.ErrRunAgentNotBound
		}
		if err != nil {
			return err
		}
		if row.ContextMarkdown == "" && in.ModelDiscoveryID == uuid.Nil && (fresh || result.Status == "awaiting_model") {
			if fresh {
				if err := q.InsertCompanyInitialization(ctx, generated.InsertCompanyInitializationParams{CompanyID: companyID, CreatedByUserID: actorUserID, ConversationID: row.ConversationID}); err != nil {
					return err
				}
			}
			result, err = readInitialization(ctx, q, row)
			return err
		}
		trigger := execution.RunTrigger{CompanyID: companyID, ConversationID: row.ConversationID, AgentID: target.AgentID, AuthorizedByUserID: actorUserID, ForceFreshSession: true}
		if row.ContextMarkdown == "" {
			if in.ModelDiscoveryID == uuid.Nil || requester == nil {
				return ErrInitializationModel
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
			trigger.Source = execution.RunSourceCompanyResearch
			trigger.ResearchModel = model
			trigger.ResearchThinkingLevel = level
			trigger.TriggerPrompt = "Prepare the initial business context using the company description, website, reference documents and available authorized Gmail, Drive and GitHub sources."
		} else {
			trigger.Source = execution.RunSourceCompanyWelcome
			trigger.TriggerPrompt = "The initial business context has been saved. Welcome the founder using your configured name, briefly explain what you learned and ask one useful question."
		}
		run, err := conversation.MaterializeInitializationRunInTx(ctx, q, &pending, trigger)
		if err != nil {
			return err
		}
		queued = &run
		id := uuid.NullUUID{UUID: run.ID, Valid: true}
		if fresh {
			err = q.InsertCompanyInitialization(ctx, generated.InsertCompanyInitializationParams{CompanyID: companyID, CreatedByUserID: actorUserID, ConversationID: row.ConversationID, ResearchRunID: id})
			row.ResearchRunID = id
		} else if row.ContextMarkdown == "" {
			err = q.SetInitializationResearchRun(ctx, generated.SetInitializationResearchRunParams{CompanyID: companyID, ResearchRunID: id})
			row.ResearchRunID = id
		} else {
			err = q.SetInitializationWelcomeRun(ctx, generated.SetInitializationWelcomeRunParams{CompanyID: companyID, WelcomeRunID: id})
			row.WelcomeRunID = id
		}
		if err != nil {
			return err
		}
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

// initializationContext extracts the business context from the research
// answer. Models often wrap Markdown in a code fence or open with a sentence,
// and rejecting that would repeat the whole research, so everything before
// the heading and a closing fence are dropped.
func initializationContext(content string) (string, bool) {
	start := strings.Index(content, "# Business context")
	if start < 0 || start > 0 && content[start-1] != '\n' {
		return "", false
	}
	content = strings.TrimSpace(content[start:])
	if fence := strings.LastIndex(content, "\n```"); fence >= 0 && strings.TrimSpace(content[fence:]) == "```" {
		content = strings.TrimSpace(content[:fence])
	}
	ok := len(content) <= 65536 && strings.Contains(content, "\n## Sources") && strings.Contains(content, "\n## Open questions")
	return content, ok
}
