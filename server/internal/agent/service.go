// Package agent implements persistent AI Agents and their reporting tree.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/agent/voice"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	provideragent "boardly.dev/boardly/server/pkg/agent"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// AgentService owns the Agent records and their reporting tree, plus the
// the organization tree derived from the agents in a Company.
type AgentService interface { //nolint:revive // AGENTS.md mandates <Domain>Service; bare Service is forbidden.
	List(context.Context, auth.UserPrincipal, ListOptions) ([]Response, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (Response, error)
	Create(context.Context, auth.UserPrincipal, CreateInput) (Response, error)
	Update(context.Context, auth.UserPrincipal, uuid.UUID, UpdateInput) (Response, error)
	Archive(context.Context, auth.UserPrincipal, uuid.UUID, RevisionInput) (Response, error)
	Restore(context.Context, auth.UserPrincipal, uuid.UUID, RevisionInput) (Response, error)
}

var _ AgentService = (*DefaultAgentService)(nil)

type DefaultAgentService struct {
	store *db.Store
	bus   *events.Bus
	now   func() time.Time
}

type ServiceOptions struct {
	Store *db.Store
	Bus   *events.Bus
	Now   func() time.Time
}

func NewAgentService(options ServiceOptions) (*DefaultAgentService, error) {
	if options.Store == nil {
		return nil, errors.New("Agent database store is required")
	}
	if options.Bus == nil {
		return nil, errors.New("Agent event bus is required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &DefaultAgentService{store: options.Store, bus: options.Bus, now: now}, nil
}

func (s *DefaultAgentService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ListOptions,
) ([]Response, error) {
	if err := s.authorizeMemberRead(ctx, principal); err != nil {
		return nil, err
	}
	return ListProjectionsForMember(ctx, s.store.Queries(), principal, options)
}

func (s *DefaultAgentService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
) (Response, error) {
	if !util.ValidUUID(agentID) {
		return Response{}, ErrNotFound
	}
	if err := s.authorizeMemberRead(ctx, principal); err != nil {
		return Response{}, err
	}
	return projectionForMember(ctx, s.store.Queries(), principal, agentID)
}

// authorizeMemberRead enforces the read policy off the actor's active
// membership: every active member reads every Agent (ADR 0013). A missing
// membership is ErrNotFound so a non-member cannot distinguish "no access"
// from "no such Company".
func (s *DefaultAgentService) authorizeMemberRead(ctx context.Context, principal auth.UserPrincipal) error {
	return authorizeMemberRead(ctx, s.store.Queries(), principal)
}

func authorizeMemberRead(ctx context.Context, q *generated.Queries, principal auth.UserPrincipal) error {
	if _, err := q.GetActiveCompanyMembership(ctx, generated.GetActiveCompanyMembershipParams{
		CompanyID: principal.CompanyID,
		UserID:    principal.UserID,
	}); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("authorize Agent read: %w", err)
	}
	return nil
}

// authorizeAgentConfiguration is the write gate for an existing Agent
// (ADR 0013): the caller's membership is locked and their role read, and the
// change is theirs to make when they are a Company owner or admin or the
// Agent's creator. The caller has already locked the Agent graph, so the
// creator judged here is the one the write will see.
func authorizeAgentConfiguration(
	role auth.CompanyRole,
	principal auth.UserPrincipal,
	current generated.Agent,
) error {
	if !canConfigureAgent(role, current, principal.UserID) {
		return ErrForbidden
	}
	return nil
}

// lockMemberRole takes the canonical Company and membership locks before
// returning the role, so a concurrent revocation serializes behind this
// transaction instead of racing it. Only meaningful inside a transaction; the
// operation word ("create"/"mutation") keeps the wrap strings distinct for its
// two callers.
func lockMemberRole(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
	operation string,
) (auth.CompanyRole, error) {
	if _, err := q.LockCompanyForMembershipMutation(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("lock Company for Agent %s: %w", operation, err)
	}
	membership, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: companyID,
		UserID:    userID,
	})
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && membership.Status != "active") {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("lock acting Company member for Agent %s: %w", operation, err)
	}
	return auth.CompanyRole(membership.Role), nil
}

func (s *DefaultAgentService) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input CreateInput,
) (Response, error) {
	normalized, err := normalizeCreateInput(input)
	if err != nil {
		return Response{}, err
	}

	var result Response
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		role, err := lockMemberRole(ctx, tx, principal.CompanyID, principal.UserID, "create")
		if err != nil {
			return err
		}
		// Any active member creates Agents (ADR 0013). The root Agent is the
		// exception: creating it is the onboarding step that stamps the
		// Company's agent-setup completion, so it needs agents.manage.
		if normalized.ReportsToAgentID == nil && !role.Can(auth.PermissionAgentsManage) {
			return ErrForbidden
		}
		if normalized.ReportsToAgentID != nil {
			if _, err := tx.LockActiveAgentParent(ctx, generated.LockActiveAgentParentParams{
				AgentID:   *normalized.ReportsToAgentID,
				CompanyID: principal.CompanyID,
			}); errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return fmt.Errorf("lock Agent parent: %w", err)
			}
		}
		// "Also set it up on my computer": the runtime must be the caller's
		// own (another member's machine answers like an unknown id, ADR 0014)
		// of the template's provider. Judged before the insert so nothing is
		// written when it is refused.
		var bindTo *generated.Runtime
		if normalized.RuntimeID != nil {
			runtime, err := bindableRuntime(ctx, tx, principal, *normalized.RuntimeID)
			if err != nil {
				return err
			}
			if runtime.Provider != normalized.Provider {
				return ErrBindingProviderMismatch
			}
			bindTo = &runtime
		}
		// The level is the provider's vocabulary: a Claude level cannot be
		// created onto a Codex template.
		if err := validateThinkingLevel(normalized.Provider, normalized.ThinkingLevel); err != nil {
			return err
		}
		// A root agent has no parent, and the column is nullable for exactly
		// that case — the zero UUID would fail the self-referencing FK.
		var parent uuid.NullUUID
		if normalized.ReportsToAgentID != nil {
			parent = uuid.NullUUID{UUID: *normalized.ReportsToAgentID, Valid: true}
		}
		row, err := tx.InsertAgent(ctx, generated.InsertAgentParams{
			CompanyID:        principal.CompanyID,
			Name:             normalized.Name,
			Title:            normalized.Title,
			Instructions:     normalized.Instructions,
			ReportsToAgentID: parent,
			Provider:         normalized.Provider,
			// The creator (ADR 0013) is who may configure the Agent
			// afterwards, with the owners.
			CreatedByUserID:   principal.UserID,
			Model:             util.PtrToText(normalized.Model),
			ThinkingLevel:     util.PtrToText(normalized.ThinkingLevel),
			AvatarKey:         util.PtrToText(normalized.AvatarKey),
			AccentColor:       util.PtrToText(normalized.AccentColor),
			MaxConcurrentRuns: ptrToInt4(normalized.MaxConcurrentRuns),
		})
		if err != nil {
			return mapCreateError("insert Agent", err)
		}
		if bindTo != nil {
			if err := bindCreatedAgentInTx(ctx, tx, &pending, principal, row.ID, *bindTo); err != nil {
				return err
			}
		}
		// Creating the root agent is the moment the org gains a configured
		// provider, which is what the Company's agent-setup stamp records.
		if normalized.ReportsToAgentID == nil {
			if err := tx.CompleteAgentSetup(ctx, principal.CompanyID); err != nil {
				return fmt.Errorf("complete root Agent setup: %w", err)
			}
		}
		created, err := projectionForMember(ctx, tx, principal, row.ID)
		if err != nil {
			return fmt.Errorf("load created Agent projection: %w", err)
		}
		stageAgentLifecycle(&pending, principal.UserID, EventAgentCreated, created)
		if normalized.ReportsToAgentID == nil {
			starters, err := provisionStarterAgents(ctx, tx, &pending, principal, row, bindTo)
			if err != nil {
				return err
			}
			for _, starter := range starters {
				stageAgentLifecycle(&pending, principal.UserID, EventAgentCreated, starter)
			}
		}
		result = created
		return nil
	})
	if err != nil {
		return Response{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

// bindCreatedAgentInTx is the create-and-bind seam: the caller's binding of
// the Agent they just created, on the runtime the create named. The runtime
// row is the caller's own, judged by bindableRuntime; the write restates
// the invariants, and a miss means one stopped holding under the locks.
func bindCreatedAgentInTx(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	runtime generated.Runtime,
) error {
	binding, err := q.UpsertAgentRuntimeBinding(ctx, generated.UpsertAgentRuntimeBindingParams{
		UserID: principal.UserID, RuntimeID: runtime.ID,
		AgentID: agentID, CompanyID: principal.CompanyID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRuntimeNotFound
	}
	if err != nil {
		return fmt.Errorf("bind created Agent: %w", err)
	}
	*pending = append(*pending, bindingChangeEvent(
		principal.CompanyID, principal.UserID, agentID, bindingRefFromRow(binding, runtimeRefFromRow(runtime)),
	))
	return nil
}

// provisionStarterAgents seeds the starter personas under a freshly created
// root Agent — templates of the root's provider, model and thinking level,
// set up on the founder's runtime exactly as the root is — so the first
// Company Plan has Agents to run it. They are ordinary Agents from this
// moment on.
func provisionStarterAgents(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	principal auth.UserPrincipal,
	root generated.Agent,
	bindTo *generated.Runtime,
) ([]Response, error) {
	starters := make([]Response, 0, len(starterAgentTemplates))
	for _, template := range starterAgentTemplates {
		avatarKey, accentColor := template.AvatarKey, template.AccentColor
		row, err := q.InsertAgent(ctx, generated.InsertAgentParams{
			CompanyID:        principal.CompanyID,
			Name:             template.Name,
			Title:            template.Title,
			Instructions:     template.Instructions,
			ReportsToAgentID: uuid.NullUUID{UUID: root.ID, Valid: true},
			Provider:         root.Provider,
			CreatedByUserID:  principal.UserID,
			Model:            root.Model,
			ThinkingLevel:    root.ThinkingLevel,
			AvatarKey:        util.PtrToText(&avatarKey),
			AccentColor:      util.PtrToText(&accentColor),
		})
		if err != nil {
			return nil, mapCreateError("insert starter Agent", err)
		}
		if bindTo != nil {
			if err := bindCreatedAgentInTx(ctx, q, pending, principal, row.ID, *bindTo); err != nil {
				return nil, err
			}
		}
		starter, err := projectionForMember(ctx, q, principal, row.ID)
		if err != nil {
			return nil, fmt.Errorf("load starter Agent projection: %w", err)
		}
		starters = append(starters, starter)
	}
	return starters, nil
}

func (s *DefaultAgentService) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input UpdateInput,
) (Response, error) {
	if !util.ValidUUID(agentID) {
		return Response{}, ErrNotFound
	}
	normalized, err := normalizeUpdateInput(input)
	if err != nil {
		return Response{}, err
	}

	var result Response
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		role, err := lockMemberRole(ctx, tx, principal.CompanyID, principal.UserID, "mutation")
		if err != nil {
			return err
		}
		graph, current, err := lockGraph(ctx, tx, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		if err := authorizeAgentConfiguration(role, principal, current); err != nil {
			return err
		}
		if current.Version != normalized.ExpectedRevision {
			if current.Version == normalized.ExpectedRevision+1 &&
				updateTargetMatches(current, normalized) {
				agent, err := projectionForMember(ctx, tx, principal, current.ID)
				if err != nil {
					return fmt.Errorf("load replayed Agent update projection: %w", err)
				}
				result = agent
				return nil
			}
			return ErrRevisionConflict
		}

		name, title, instructions := current.Name, current.Title, current.Instructions
		parent := current.ReportsToAgentID
		model, thinking := current.Model, current.ThinkingLevel
		maxConcurrentRuns := current.MaxConcurrentRuns
		changed := false
		if normalized.Name != nil && *normalized.Name != current.Name {
			name = *normalized.Name
			changed = true
		}
		if normalized.Title != nil && *normalized.Title != current.Title {
			title = *normalized.Title
			changed = true
		}
		if normalized.Instructions != nil && *normalized.Instructions != current.Instructions {
			instructions = *normalized.Instructions
			changed = true
		}
		if normalized.ReportsToAgentID != nil &&
			(!current.ReportsToAgentID.Valid || current.ReportsToAgentID.UUID != *normalized.ReportsToAgentID) {
			if !current.ReportsToAgentID.Valid {
				return ErrRootImmutable
			}
			if err := validateActiveAgentParent(graph, agentID, *normalized.ReportsToAgentID); err != nil {
				return err
			}
			parent = uuid.NullUUID{UUID: *normalized.ReportsToAgentID, Valid: true}
			changed = true
		}
		if normalized.Model.Present {
			if !sameOptionalText(current.Model, normalized.Model.Value) {
				changed = true
			}
			model = util.PtrToText(normalized.Model.Value)
		}
		if normalized.ThinkingLevel.Present {
			// The level is the template's provider's vocabulary.
			if err := validateThinkingLevel(current.Provider, normalized.ThinkingLevel.Value); err != nil {
				return err
			}
			if !sameOptionalText(current.ThinkingLevel, normalized.ThinkingLevel.Value) {
				changed = true
			}
			thinking = util.PtrToText(normalized.ThinkingLevel.Value)
		}
		if normalized.MaxConcurrentRuns != nil &&
			current.MaxConcurrentRuns != *normalized.MaxConcurrentRuns {
			maxConcurrentRuns = *normalized.MaxConcurrentRuns
			changed = true
		}
		avatarKey, accentColor := current.AvatarKey, current.AccentColor
		if normalized.AvatarKey.Present {
			if !sameOptionalText(current.AvatarKey, normalized.AvatarKey.Value) {
				changed = true
			}
			avatarKey = util.PtrToText(normalized.AvatarKey.Value)
		}
		if normalized.AccentColor.Present {
			if !sameOptionalText(current.AccentColor, normalized.AccentColor.Value) {
				changed = true
			}
			accentColor = util.PtrToText(normalized.AccentColor.Value)
		}

		currentCapabilities := capabilitySettingsFromRow(current)
		capabilities := currentCapabilities
		if normalized.Capabilities != nil {
			capabilities = *normalized.Capabilities
			if err := validateCapabilitySources(
				ctx,
				tx,
				principal.CompanyID,
				current.Provider,
				currentCapabilities,
				capabilities,
			); err != nil {
				return err
			}
		}
		changed = changed || !sameCapabilitySettings(currentCapabilities, capabilities)
		encodedCapabilities, err := json.Marshal(capabilities)
		if err != nil {
			return fmt.Errorf("encode Agent capabilities: %w", err)
		}
		var encodedVoice []byte
		if normalized.Voice != nil {
			changed = changed || voice.FromSettings(current.Settings) != *normalized.Voice
			encodedVoice, err = json.Marshal(normalized.Voice)
			if err != nil {
				return fmt.Errorf("encode Agent voice settings: %w", err)
			}
		}
		if !changed {
			agent, err := projectionForMember(ctx, tx, principal, current.ID)
			if err != nil {
				return fmt.Errorf("load unchanged Agent projection: %w", err)
			}
			result = agent
			return nil
		}

		row, err := tx.UpdateAgentConfiguration(ctx, generated.UpdateAgentConfigurationParams{
			Voice:             encodedVoice,
			Capabilities:      encodedCapabilities,
			DisabledPluginIds: capabilities.DisabledPlugins,
			Name:              name,
			Title:             title,
			Instructions:      instructions,
			ReportsToAgentID:  parent,
			Model:             model,
			ThinkingLevel:     thinking,
			AvatarKey:         avatarKey,
			AccentColor:       accentColor,
			MaxConcurrentRuns: maxConcurrentRuns,
			AgentID:           agentID,
			CompanyID:         principal.CompanyID,
			ExpectedVersion:   current.Version,
		})
		if err != nil {
			return mapMutationError("update Agent configuration", err)
		}
		result, err = finishAgentMutation(ctx, tx, principal, &pending, row.ID)
		return err
	})
	if err != nil {
		return Response{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

// validateThinkingLevel fences a non-nil level to the vocabulary of the
// provider the agent executes on. A nil level is "the provider default" and
// needs no provider at all.
func validateThinkingLevel(provider string, level *string) error {
	if level == nil {
		return nil
	}
	if !provideragent.KnownThinkingLevel(provider, *level) {
		return ErrThinkingLevelUnsupported
	}
	return nil
}

func ptrToInt4(value *int32) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *value, Valid: true}
}

// bindableRuntime loads a runtime the caller may bind an Agent TO and
// fences it to the company. GetRuntime hides removed rows, so a removed,
// foreign or unknown runtime is the same non-disclosing ErrRuntimeNotFound,
// and so is another member's machine (ADR 0014): it is invisible to the
// caller, so binding onto it answers exactly like an unknown id — a 403
// here would tell a member that a registration exists.
func bindableRuntime(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
) (generated.Runtime, error) {
	runtime, err := q.GetRuntime(ctx, runtimeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.Runtime{}, ErrRuntimeNotFound
	}
	if err != nil {
		return generated.Runtime{}, fmt.Errorf("load bindable runtime: %w", err)
	}
	if runtime.CompanyID != principal.CompanyID || !registeredBy(runtime.OwnerUserID, principal.UserID) {
		return generated.Runtime{}, ErrRuntimeNotFound
	}
	return runtime, nil
}

func (s *DefaultAgentService) Archive(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input RevisionInput,
) (Response, error) {
	return s.mutateArchiveState(ctx, principal, agentID, input, true)
}

func (s *DefaultAgentService) Restore(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input RevisionInput,
) (Response, error) {
	return s.mutateArchiveState(ctx, principal, agentID, input, false)
}

func (s *DefaultAgentService) mutateArchiveState(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	input RevisionInput,
	archive bool,
) (Response, error) {
	if !util.ValidUUID(agentID) {
		return Response{}, ErrNotFound
	}
	var result Response
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		role, err := lockMemberRole(ctx, tx, principal.CompanyID, principal.UserID, "mutation")
		if err != nil {
			return err
		}
		graph, current, err := lockGraph(ctx, tx, principal.CompanyID, agentID)
		if err != nil {
			return err
		}
		if err := authorizeAgentConfiguration(role, principal, current); err != nil {
			return err
		}
		targetStateMatches := archive == current.ArchivedAt.Valid
		if current.Version != input.ExpectedRevision {
			if current.Version == input.ExpectedRevision+1 && targetStateMatches {
				agent, err := projectionForMember(ctx, tx, principal, current.ID)
				if err != nil {
					return fmt.Errorf("load replayed Agent archive projection: %w", err)
				}
				result = agent
				return nil
			}
			return ErrRevisionConflict
		}
		if !current.ReportsToAgentID.Valid {
			return ErrRootImmutable
		}
		if targetStateMatches {
			agent, err := projectionForMember(ctx, tx, principal, current.ID)
			if err != nil {
				return fmt.Errorf("load unchanged Agent archive projection: %w", err)
			}
			result = agent
			return nil
		}

		if archive {
			if activeDirectReport(graph, agentID) {
				return ErrArchiveBlocked
			}
			blocked, lockErr := lockActiveAssignments(ctx, tx, principal.CompanyID, agentID)
			if lockErr != nil {
				return lockErr
			}
			if blocked {
				return ErrArchiveBlocked
			}
			row, archiveErr := tx.ArchiveAgent(ctx, generated.ArchiveAgentParams{
				AgentID:   agentID,
				CompanyID: principal.CompanyID, ExpectedVersion: current.Version,
			})
			if archiveErr != nil {
				return mapMutationError("change Agent archive state", archiveErr)
			}
			if err = conversation.RevokeDiscussionAgentsInTx(
				ctx,
				tx,
				&pending,
				conversation.DiscussionAgentRevocation{
					CompanyID: principal.CompanyID,
					AgentID:   agentID,
				},
			); err != nil {
				return err
			}
			result, err = finishAgentMutation(ctx, tx, principal, &pending, row.ID)
			return err
		}
		parentID := current.ReportsToAgentID.UUID
		if err := validateActiveAgentParent(graph, agentID, parentID); err != nil {
			return err
		}
		row, restoreErr := tx.RestoreAgent(ctx, generated.RestoreAgentParams{
			AgentID:   agentID,
			CompanyID: principal.CompanyID, ExpectedVersion: current.Version,
		})
		if restoreErr != nil {
			return mapMutationError("change Agent archive state", restoreErr)
		}
		result, err = finishAgentMutation(ctx, tx, principal, &pending, row.ID)
		return err
	})
	if err != nil {
		return Response{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

// lockGraph locks the company's whole Agent graph and returns the trusted sqlc
// rows plus the target row. Mutation invariants are checked against this one
// locked graph before writes.
func lockGraph(
	ctx context.Context,
	q *generated.Queries,
	companyID, agentID uuid.UUID,
) ([]generated.Agent, generated.Agent, error) {
	rows, err := q.LockCompanyAgentGraph(ctx, companyID)
	if err != nil {
		return nil, generated.Agent{}, fmt.Errorf("lock Company Agent graph: %w", err)
	}
	for _, row := range rows {
		if row.ID == agentID {
			return rows, row, nil
		}
	}
	return nil, generated.Agent{}, ErrNotFound
}

// lockActiveAssignments reports whether the Agent still has active Runs or
// Tasks. Daemon callbacks lock their Run before any owning Task. Preserve
// that global Run -> Task order so archive cannot deadlock terminalization.
func lockActiveAssignments(
	ctx context.Context,
	q *generated.Queries,
	companyID, agentID uuid.UUID,
) (bool, error) {
	runs, lockErr := q.LockActiveRunsForAgent(ctx, agentID)
	if lockErr != nil {
		return false, fmt.Errorf("lock active Agent Runs: %w", lockErr)
	}
	tasks, lockErr := q.LockActiveTasksExecutedByAgent(ctx, generated.LockActiveTasksExecutedByAgentParams{
		CompanyID: companyID,
		AgentID:   agentID,
	})
	if lockErr != nil {
		return false, fmt.Errorf("lock active Agent Tasks: %w", lockErr)
	}
	return len(tasks) != 0 || len(runs) != 0, nil
}

// finishAgentMutation maps the mutated row and stages the wire event for one
// committed Agent mutation.
func finishAgentMutation(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	pending *[]events.Event,
	agentID uuid.UUID,
) (Response, error) {
	agent, err := projectionForMember(ctx, q, principal, agentID)
	if err != nil {
		return Response{}, fmt.Errorf("load mutated Agent projection: %w", err)
	}
	stageAgentLifecycle(pending, principal.UserID, EventAgentUpdated, agent)
	return agent, nil
}
