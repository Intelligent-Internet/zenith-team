package company

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/task"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

var _ approval.CompanyPlanActionParticipant = (*DefaultPlanService)(nil)

// ApplyCompanyPlan is a person's own apply: a Company action under
// company.plan.apply that changes only the Projects they may write and read
// the plan from (ADR 0017).
func (s *DefaultPlanService) ApplyCompanyPlan(
	ctx context.Context,
	principal auth.UserPrincipal,
	artifact PlanArtifact,
) (PlanApplyResult, error) {
	if err := validateCompanyPlanArtifact(artifact, principal.CompanyID); err != nil {
		return PlanApplyResult{}, err
	}
	var result PlanApplyResult
	var inboxResults []inbox.CreateResult
	var pending []events.Event
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite},
		func(q *generated.Queries) error {
			pending = pending[:0]
			inboxResults = nil
			result = PlanApplyResult{}
			role, err := lockPlanApplier(ctx, q, principal)
			if err != nil {
				return err
			}
			if !role.Can(auth.PermissionCompanyPlanApply) {
				return ErrForbidden
			}
			locked, err := lockPlanForApply(ctx, q, planScopeRule{
				actor: approval.CompanyPlanActor{Principal: principal}, base: &artifact.Base, writeNeeded: true,
			}, artifact)
			if errors.Is(err, errPlanSlugChanged) || errors.Is(err, errPlanStale) {
				return ErrPlanConflict
			}
			if err != nil {
				return err
			}
			if !locked.matchesBase() {
				return ErrPlanConflict
			}
			inboxResults, err = applyDesiredPlan(
				ctx, q, &pending, planApplier{principal: principal, role: role}, locked, &result,
			)
			return err
		},
	)
	if err != nil {
		return PlanApplyResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	for _, inboxResult := range inboxResults {
		inbox.PresentCreated(s.inboxPresenter, inboxResult)
	}
	return result, nil
}

// LockCompanyPlanScope takes, for the Approval raise, the Project and Task
// chat locks PrepareCompanyPlanApply takes, before the raise locks its origin
// chat and Run: the ladder is company -> member -> project -> conversation
// -> run. It locks the chats of every Project the raise may reach, which
// covers every chat Prepare locks again. The caller holds the Company and
// member locks.
func (s *DefaultPlanService) LockCompanyPlanScope(
	ctx context.Context,
	q *generated.Queries,
	actor approval.CompanyPlanActor,
) error {
	_, err := lockPlanScope(ctx, q, planScopeRule{
		actor: actor, writeNeeded: !actor.TaskProjectID.Valid,
	}, PlanDocument{})
	return err
}

// PrepareCompanyPlanApply freezes a server-owned action and Markdown preview
// from the authoritative plan snapshot. The agent-supplied Base is
// intentionally discarded; only the validated desired document is treated as
// input, and the change list behind the preview is recomputed here. Both
// are narrowed to what the requesting member reaches (ADR 0017): a plan
// raised in a Task chat with a Project covers that Project, which the
// Approval's readers read; any other plan covers the Projects the member may
// write. The frozen base names those Projects, and the apply reaches no
// others.
func (s *DefaultPlanService) PrepareCompanyPlanApply(
	ctx context.Context,
	q *generated.Queries,
	actor approval.CompanyPlanActor,
	request json.RawMessage,
) (approval.PreparedCompanyPlanAction, error) {
	decoder := json.NewDecoder(bytes.NewReader(request))
	decoder.DisallowUnknownFields()
	var artifact PlanArtifact
	if err := decoder.Decode(&artifact); err != nil {
		return approval.PreparedCompanyPlanAction{}, ErrPlanInvalidInput
	}
	artifact.Base = PlanBase{}
	companyID := actor.Principal.CompanyID
	if err := validateCompanyPlanArtifact(artifact, companyID); err != nil {
		return approval.PreparedCompanyPlanAction{}, err
	}
	locked, err := lockPlanForApply(ctx, q, planScopeRule{
		actor: actor, writeNeeded: !actor.TaskProjectID.Valid,
	}, artifact)
	if errors.Is(err, errPlanSlugChanged) {
		return approval.PreparedCompanyPlanAction{}, ErrPlanConflict
	}
	if err != nil {
		return approval.PreparedCompanyPlanAction{}, err
	}
	projects, tasks, dependencies := locked.projects, locked.tasks, locked.dependencies
	artifact.Desired = locked.desired
	// Resolve every requested Agent while the snapshot is locked. This
	// makes a missing or archived Agent a request error, not a
	// surprise discovered after a human approves it.
	desiredTasks := desiredTasksByKey(artifact.Desired)
	if _, err := resolvePlanAgents(ctx, q, companyID, tasks, desiredTasks); err != nil {
		return approval.PreparedCompanyPlanAction{}, err
	}
	if err := validatePlanProjects(artifact.Desired, projects, tasks); err != nil {
		return approval.PreparedCompanyPlanAction{}, err
	}
	artifact.Base = locked.snapshot()
	changes := append(planProjectChanges(artifact.Desired, projects), computeCompanyPlanChanges(artifact.Desired, tasks, dependencies)...)
	canonical, err := json.Marshal(artifact)
	if err != nil {
		return approval.PreparedCompanyPlanAction{}, fmt.Errorf("encode canonical Company Plan action: %w", err)
	}
	digest := sha256.Sum256(canonical)
	presentation, err := json.Marshal(CompanyPlanApprovalPresentation{
		Kind: "company_plan", CompanySlug: locked.slug,
		DraftDigest: fmt.Sprintf("%x", digest), TotalChanges: len(changes),
		Changes: changes, Plan: artifact.Desired,
	})
	if err != nil {
		return approval.PreparedCompanyPlanAction{}, fmt.Errorf("encode Company Plan presentation: %w", err)
	}
	summary := fmt.Sprintf("%d authoritative Company Plan changes", len(changes))
	return approval.PreparedCompanyPlanAction{
		CanonicalAction: canonical,
		Presentation:    presentation,
		PreviewMarkdown: renderCompanyPlanPreview(changes),
		Summary:         &summary,
	}, nil
}

// ExecuteCompanyPlanApply runs the frozen action inside ApprovalService's
// transaction, as the applier (ADR 0017): it changes only the Projects of
// the frozen base the applier may write, and within a Task chat's plan only
// that chat's Project. A plan raised outside a Task chat is a Company action
// and needs company.plan.apply; a Task chat's plan was decided in its
// Project, where the approval checked project.approvals.resolve and
// project.tasks.write. A stale Base is a successful approved/skipped
// outcome; an infrastructure error returns an error so the Approval CAS and
// every Plan mutation roll back together.
func (s *DefaultPlanService) ExecuteCompanyPlanApply(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	actor approval.CompanyPlanActor,
	canonicalAction json.RawMessage,
) (approval.CompanyPlanActionResult, error) {
	var artifact PlanArtifact
	if err := json.Unmarshal(canonicalAction, &artifact); err != nil {
		return approval.CompanyPlanActionResult{}, fmt.Errorf("decode canonical Company Plan action: %w", err)
	}
	if artifact.Desired.FormatVersion != planFormatVersion || artifact.Desired.Tasks == nil {
		reason := "the Company Plan format changed after the approval preview"
		return approval.CompanyPlanActionResult{SkippedReason: &reason}, nil
	}
	principal := actor.Principal
	role, err := lockPlanApplier(ctx, q, principal)
	if err != nil {
		return approval.CompanyPlanActionResult{}, err
	}
	if !actor.TaskProjectID.Valid && !role.Can(auth.PermissionCompanyPlanApply) {
		return approval.CompanyPlanActionResult{}, ErrForbidden
	}
	locked, err := lockPlanForApply(ctx, q, planScopeRule{
		actor: actor, base: &artifact.Base, writeNeeded: true,
	}, artifact)
	if errors.Is(err, errPlanSlugChanged) {
		reason := "the Company slug changed after the approval preview"
		return approval.CompanyPlanActionResult{SkippedReason: &reason}, nil
	}
	if errors.Is(err, errPlanStale) {
		reason := "the Company Plan base changed after the approval preview"
		return approval.CompanyPlanActionResult{SkippedReason: &reason}, nil
	}
	if err != nil {
		return approval.CompanyPlanActionResult{}, err
	}
	if !locked.matchesBase() {
		reason := "the Company Plan base changed after the approval preview"
		return approval.CompanyPlanActionResult{SkippedReason: &reason}, nil
	}
	var result PlanApplyResult
	// The Inbox items this apply raises are durable rows whose inbox.created
	// events ride pending and publish with the Approval's commit; the
	// transient toast hint is the one thing this path does not present.
	applier := planApplier{principal: principal, role: role, run: actor.Run}
	if _, err := applyDesiredPlan(ctx, q, pending, applier, locked, &result); err != nil {
		return approval.CompanyPlanActionResult{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return approval.CompanyPlanActionResult{}, fmt.Errorf("encode Company Plan apply result: %w", err)
	}
	return approval.CompanyPlanActionResult{Result: encoded}, nil
}

// applyDesiredPlan walks the scoped desired document against the locked
// scope: creates, updates, and deletes Tasks with CAS-guarded writes that
// each check the applier's project.tasks.write in the statement (ADR 0017).
// On top of project.tasks.write it keeps the Task rules a Task PATCH keeps
// (requirePlanTaskAuthority). It returns the Inbox items the Task writes
// raised, for the caller to present after its commit.
func applyDesiredPlan(
	ctx context.Context,
	q *generated.Queries,
	pending *[]events.Event,
	applier planApplier,
	locked lockedPlan,
	result *PlanApplyResult,
) ([]inbox.CreateResult, error) {
	principal := applier.principal
	result.Skipped = append([]PlanSkip{}, locked.skipped...)
	desired, projects := locked.desired, locked.projects
	tasks, dependencies := locked.tasks, locked.dependencies
	if err := validatePlanProjects(desired, projects, tasks); err != nil {
		return nil, err
	}
	if err := applyPlanProjects(ctx, q, pending, applier, desired, locked, result); err != nil {
		return nil, err
	}
	desiredTasks := desiredTasksByKey(desired)
	planAgents, err := resolvePlanAgents(ctx, q, principal.CompanyID, tasks, desiredTasks)
	if err != nil {
		return nil, err
	}
	fence := auth.ProjectPermissionHeldSets()
	writePermission := string(auth.ProjectPermissionTasksWrite)
	for key, current := range tasks {
		target, ok := desiredTasks[key]
		targetAgentID := planAgents[target.AgentID]
		if terminalTaskStatus(current.Status) && ok &&
			(!taskMatchesDesired(current, target, targetAgentID)) {
			return nil, fmt.Errorf("%w: terminal Task %q is immutable", ErrPlanConflict, key)
		}
	}
	for key, target := range desiredTasks {
		current, exists := tasks[key]
		if !validDesiredPlanTaskStatus(target.Status, current, exists) {
			return nil, ErrPlanInvalidInput
		}
	}
	for _, key := range sortedPersistedTaskKeys(tasks) {
		target, retained := desiredTasks[key]
		if err := requirePlanTaskAuthority(
			applier, tasks[key], target, retained, planAgents[target.AgentID],
		); err != nil {
			return nil, err
		}
	}
	if err := lockChangedPlanTaskRunFences(
		ctx,
		q,
		principal.CompanyID,
		principal.UserID,
		tasks,
		desiredTasks,
		dependencies,
		planAgents,
	); err != nil {
		return nil, err
	}
	history, err := observePlanHistoryInTx(ctx, q, principal.CompanyID, tasks, desiredTasks)
	if err != nil {
		return nil, err
	}
	taskIDs := map[string]uuid.UUID{}
	for key, value := range tasks {
		taskIDs[key] = value.ID
	}
	metadataUpdatedTasks := map[string]bool{}
	taskChangeTypes := map[string]task.ChangeType{}
	// The keys the apply handed to another Agent, each true when that stopped
	// a live Run: the Task's Inbox rules tell the assignee and close its stale
	// review notices.
	handoffRunStopped := map[string]bool{}
	// A restated status that crosses `done` flips every dependent's
	// dependenciesResolved projection; the dependency walk announces them.
	resolvedFlipTasks := map[string]bool{}
	for _, key := range sortedTaskKeys(desiredTasks) {
		target := desiredTasks[key]
		agentID := planAgents[target.AgentID]
		executedByAgentID := planTaskExecutedBy(agentID)
		if current, ok := tasks[key]; ok {
			// The assignee is kept: a restated plan moves the work to another
			// Agent or to the assignee's own hands, never to another member.
			if !taskMatchesDesired(current, target, agentID) {
				// Moving the work of a Task that has run is a handoff (ADR
				// 0014), and applying the plan is its confirmation: the
				// Task's live Runs are cancelled first and the write below
				// advances both generations, exactly as a confirmed Task
				// PATCH does.
				handoff := current.ExecutionGeneration > 0 &&
					!samePlanUUID(current.ExecutedByAgentID, executedByAgentID)
				if handoff {
					cancelled, err := execution.CancelTaskRunsForHandoffInTx(ctx, q, pending, execution.TaskHandoff{
						CompanyID: principal.CompanyID, TaskID: current.ID,
					})
					if err != nil {
						return nil, fmt.Errorf("cancel Task Runs for Company Plan handoff: %w", err)
					}
					handoffRunStopped[key] = len(cancelled) > 0
				}
				// The handoff fences the earlier Runs out of any merge, so
				// work that was in progress or in review starts fresh: the
				// Task goes back to To do for its assignee to start, as a
				// confirmed Task PATCH does.
				status := target.Status
				if handoff && (status == string(task.StatusInProgress) || status == string(task.StatusInReview)) {
					status = string(task.StatusTodo)
				}
				// The assignee, labels and dates are kept: a plan names none
				// of them.
				if _, err := q.UpdateTask(ctx, generated.UpdateTaskParams{
					MilestoneType:             string(target.MilestoneType),
					Title:                     target.Title,
					Description:               target.Description,
					Status:                    status,
					Priority:                  target.Priority,
					AssigneeUserID:            current.AssigneeUserID,
					ExecutedByAgentID:         executedByAgentID,
					AdvanceGeneration:         handoff,
					TaskID:                    current.ID,
					CompanyID:                 principal.CompanyID,
					ExpectedVersion:           current.Version,
					FenceUserID:               uuid.NullUUID{UUID: principal.UserID, Valid: true},
					FencePermission:           writePermission,
					CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
					ManagementPermissions:     fence.ManagementPermissions,
					ManagementRoles:           fence.ManagementRoles,
				}); err != nil {
					return nil, mapPlanMutationError(fmt.Sprintf("update Task %q", key), err)
				}
				renamed, err := q.RenameCompanyPlanTaskConversation(ctx, generated.RenameCompanyPlanTaskConversationParams{
					Title:                     target.Title,
					CompanyID:                 principal.CompanyID,
					ConversationID:            current.ConversationID,
					FenceUserID:               principal.UserID,
					FencePermission:           writePermission,
					CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
					ManagementPermissions:     fence.ManagementPermissions,
					ManagementRoles:           fence.ManagementRoles,
				})
				if err != nil {
					return nil, fmt.Errorf("update Task conversation title: %w", err)
				}
				if renamed != 1 {
					return nil, ErrPlanConflict
				}
				result.Updated++
				metadataUpdatedTasks[key] = true
				taskChangeTypes[key] = task.ChangeTypeUpdated
				if task.DependenciesResolvedFlips(
					task.Status(current.Status), task.Status(status),
				) {
					resolvedFlipTasks[key] = true
				}
			}
			continue
		}
		allocated, err := q.AllocateTaskNumber(ctx, principal.CompanyID)
		if err != nil {
			return nil, mapPlanMutationError("allocate Company Plan Task number", err)
		}
		inserted, err := q.InsertTaskWithConversation(
			ctx,
			generated.InsertTaskWithConversationParams{
				CompanyID: principal.CompanyID,
				ProjectID: projects[target.ProjectKey].ID,
				// The applier writes the Task, so the insert needs their
				// project.tasks.write (ADR 0017). On a Project this apply
				// created they hold it as its creator.
				FenceUserID:               uuid.NullUUID{UUID: principal.UserID, Valid: true},
				FencePermission:           writePermission,
				CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
				ManagementPermissions:     fence.ManagementPermissions,
				ManagementRoles:           fence.ManagementRoles,
				Title:                     target.Title,
				MilestoneType:             string(target.MilestoneType),
				Number:                    allocated.Number,
				Key:                       key,
				Description:               target.Description,
				Status:                    target.Status,
				Priority:                  target.Priority,
				// The applier is responsible for every Task the plan creates
				// (ADR 0014); no Run starts here, they start it themselves.
				AssigneeUserID:    principal.UserID,
				ExecutedByAgentID: executedByAgentID,
				CreatorUserID:     uuid.NullUUID{UUID: principal.UserID, Valid: true},
				CreatorAgentID:    applier.creatorAgentID(),
			},
		)
		if err != nil {
			return nil, mapPlanMutationError(fmt.Sprintf("create Task %q", key), err)
		}
		taskIDs[key] = inserted.ID
		history.tasks[inserted.ID] = nil
		history.conversations[inserted.ConversationID] = nil
		taskChangeTypes[key] = task.ChangeTypeCreated
		result.Created++
	}
	if err := applyDesiredDependencies(
		ctx, q, principal, desiredTasks, tasks, taskIDs, dependencies,
		metadataUpdatedTasks, resolvedFlipTasks, taskChangeTypes, result,
	); err != nil {
		return nil, err
	}
	for _, key := range sortedPersistedTaskKeys(tasks) {
		current := tasks[key]
		if _, ok := desiredTasks[key]; ok || terminalTaskStatus(current.Status) {
			continue
		}
		// The Task's live Runs end with it: once it is gone nothing would
		// stop a queued one from being claimed for work nobody holds.
		if _, err := execution.CancelDeletedTaskRunsInTx(ctx, q, pending, principal.CompanyID, current.ID); err != nil {
			return nil, fmt.Errorf("cancel the Runs of a deleted Task: %w", err)
		}
		detached, err := q.DetachDeletedTaskConversation(ctx, generated.DetachDeletedTaskConversationParams{
			CompanyID: principal.CompanyID, ConversationID: current.ConversationID, TaskID: current.ID,
		})
		if err != nil {
			return nil, fmt.Errorf("detach deleted Task Conversation: %w", err)
		}
		deleted, err := q.DeleteCompanyPlanTask(ctx, generated.DeleteCompanyPlanTaskParams{
			CompanyID:                 principal.CompanyID,
			TaskID:                    current.ID,
			ExpectedVersion:           current.Version,
			FenceUserID:               principal.UserID,
			FencePermission:           writePermission,
			CompanyDefaultPermissions: fence.CompanyDefaultPermissions,
			ManagementPermissions:     fence.ManagementPermissions,
			ManagementRoles:           fence.ManagementRoles,
		})
		if err != nil {
			return nil, fmt.Errorf("delete Company Plan Task: %w", err)
		}
		if deleted != 1 {
			return nil, ErrPlanConflict
		}
		if err := conversation.StageUpdatedInTx(ctx, q, pending, principal.UserID, detached); err != nil {
			return nil, err
		}
		// Publish the removal so clients discard the deleted Task.
		*pending = append(*pending, task.NewDeletedEvent(
			principal.UserID, principal.CompanyID, current.ProjectID, current.ID, current.Version+1,
		))
		// The deferred reverse FK detaches the historical task Conversation.
		// Its Messages and Runs remain canonical usage/reporting evidence; a
		// second Conversation delete here would destroy that history.
		result.Deleted++
	}
	taskChanges := make([]task.Change, 0, len(taskChangeTypes))
	changedKeys := make([]string, 0, len(taskChangeTypes))
	for key := range taskChangeTypes {
		changedKeys = append(changedKeys, key)
	}
	sort.Strings(changedKeys)
	for _, key := range changedKeys {
		changeType := taskChangeTypes[key]
		taskID, exists := taskIDs[key]
		if !exists {
			return nil, fmt.Errorf("read Company Plan Task event identity %q", key)
		}
		change := task.Change{ID: taskID, Type: changeType}
		// An updated Task always has a locked snapshot: the change type is
		// only ever set for a key lockPlanForApply locked (LockCompanyPlanTasks).
		if changeType == task.ChangeTypeUpdated {
			before := tasks[key]
			change.PreviousAssignee = planTaskAssignee(before.AssigneeUserID)
			change.PreviousStatus = task.Status(before.Status)
			change.RunStopped, change.Handoff = handoffRunStopped[key]
		}
		taskChanges = append(taskChanges, change)
	}
	inboxResults, err := task.StageChangesInTx(ctx, q, pending, principal, taskChanges)
	if err != nil {
		return nil, err
	}
	if len(desiredTasks) > 0 {
		if err := q.CompleteFirstPlanApplied(ctx, principal.CompanyID); err != nil {
			return nil, fmt.Errorf("record first applied Company Plan: %w", err)
		}
	}
	if err := archivePlanProjects(ctx, q, pending, applier, desired, locked, result); err != nil {
		return nil, err
	}
	if err := history.recordInTx(ctx, q, pending, applier.actor()); err != nil {
		return nil, err
	}
	// Every frame the apply staged reaches the readers of its Project,
	// decided before commit (ADR 0017).
	if err := auth.StampProjectAudiencesInTx(ctx, q, *pending); err != nil {
		return nil, err
	}
	return inboxResults, nil
}

// planApplier is the person a Company Plan applies as, with the Company role
// their membership lock returned.
type planApplier struct {
	principal auth.UserPrincipal
	role      auth.CompanyRole
	// run is the Run applying the plan it raised for principal in Proactive
	// Mode, nil when principal applied or approved it
	// (approval.CompanyPlanActor.Run).
	run *execution.RunOrigin
}

// actor is who every row the apply writes names (D31).
func (applier planApplier) actor() events.Actor {
	return approval.CompanyPlanActor{Principal: applier.principal, Run: applier.run}.Actor()
}

// creatorAgentID is the Agent a Task the apply creates names beside the
// applier: the Run's Agent when a Run applies it (IA-FR5), none otherwise.
func (applier planApplier) creatorAgentID() uuid.NullUUID {
	if applier.run == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: applier.run.AgentID, Valid: true}
}

// requirePlanTaskAuthority keeps, for one locked Task, the rules a Task
// PATCH keeps on top of project.tasks.write (ADR 0014; ADR 0017 decision
// 5). Moving another member's open Task to another Agent or to their own
// hands is a handoff, and moving it out of review decides their work: both
// belong to its assignee or a tasks.assign holder. So does deleting it by
// leaving it out of the plan, which takes their work away. A finished Task
// is never changed or deleted by a plan, so it needs nothing here.
func requirePlanTaskAuthority(
	applier planApplier,
	current persistedTask,
	target desiredTask,
	retained bool,
	targetAgentID uuid.UUID,
) error {
	if terminalTaskStatus(current.Status) || current.AssigneeUserID == applier.principal.UserID ||
		applier.role.Can(auth.PermissionTasksAssign) {
		return nil
	}
	switch {
	case !retained:
		return ErrPlanTaskDeletePermissionRequired
	case !samePlanUUID(current.ExecutedByAgentID, planTaskExecutedBy(targetAgentID)):
		return task.ErrHandoffPermissionRequired
	case current.Status == string(task.StatusInReview) && target.Status != string(task.StatusInReview):
		return task.ErrReviewPermissionRequired
	}
	return nil
}

// lockChangedPlanTaskRunFences keeps a Company Plan apply from invalidating a
// Task generation while a daemon still owns its live merge lease.
// lockPlanForApply already locked every Task in id order
// (LockCompanyPlanTasks); the shared execution seam
// supersedes unclaimed jobs and fails closed for every executing job. Lease
// recovery remains the claimant's responsibility because an expired owner may
// still be unwinding local Git work.
func lockChangedPlanTaskRunFences(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	actorUserID uuid.UUID,
	tasks map[string]persistedTask,
	desiredTasks map[string]desiredTask,
	dependencies map[string]persistedDependency,
	planAgents map[string]uuid.UUID,
) error {
	dependencyChangedTasks := changedPlanDependencyTaskKeys(
		tasks,
		desiredTasks,
		dependencies,
	)
	for _, key := range sortedPersistedTaskKeys(tasks) {
		current := tasks[key]
		target, retained := desiredTasks[key]
		changed := !retained || dependencyChangedTasks[key]
		if retained {
			changed = changed || !taskMatchesDesired(current, target, planAgents[target.AgentID])
		}
		if !changed {
			continue
		}
		if err := execution.LockTaskRunMutationFenceInTx(
			ctx,
			q,
			companyID,
			current.ID,
		); errors.Is(err, execution.ErrTaskMergeInProgress) {
			return ErrPlanConflict
		} else if err != nil {
			return fmt.Errorf("lock Company Plan Task merge fence: %w", err)
		}
	}
	return nil
}

type planDependencyKey struct {
	dependent    string
	prerequisite string
}

// retainOmittedTerminalDependency preserves historical edges when either
// endpoint is an omitted terminal Task and neither endpoint will be deleted.
// Preview, merge fences, and execution must agree on this implicit retention.
func retainOmittedTerminalDependency(
	key planDependencyKey,
	current map[string]persistedTask,
	desired map[string]desiredTask,
) bool {
	_, dependentExplicit := desired[key.dependent]
	_, prerequisiteExplicit := desired[key.prerequisite]
	dependentTerminal := terminalTaskStatus(current[key.dependent].Status)
	prerequisiteTerminal := terminalTaskStatus(current[key.prerequisite].Status)
	return (dependentExplicit || dependentTerminal) && (prerequisiteExplicit || prerequisiteTerminal) &&
		(!dependentExplicit && dependentTerminal || !prerequisiteExplicit && prerequisiteTerminal)
}

// changedPlanDependencyTaskKeys returns the existing dependent Tasks whose
// dependency projection will change. It mirrors applyDesiredDependencies'
// retention of edges attached to omitted terminal Tasks, so an identical graph
// remains a no-op and does not acquire an unrelated merge fence.
func changedPlanDependencyTaskKeys(
	currentTasks map[string]persistedTask,
	desiredTasks map[string]desiredTask,
	dependencies map[string]persistedDependency,
) map[string]bool {
	taskKeysByID := make(map[uuid.UUID]string, len(currentTasks))
	for key, current := range currentTasks {
		taskKeysByID[current.ID] = key
	}

	currentKeys := make(map[planDependencyKey]struct{}, len(dependencies))
	desiredKeys := make(map[planDependencyKey]struct{})
	for _, dependency := range dependencies {
		key := planDependencyKey{
			dependent:    taskKeysByID[dependency.DependentTaskID],
			prerequisite: taskKeysByID[dependency.PrerequisiteTaskID],
		}
		currentKeys[key] = struct{}{}

		if retainOmittedTerminalDependency(key, currentTasks, desiredTasks) {
			desiredKeys[key] = struct{}{}
		}
	}
	for dependentKey, target := range desiredTasks {
		for _, dependency := range target.DependsOn {
			desiredKeys[planDependencyKey{
				dependent: dependentKey, prerequisite: dependency.TaskKey,
			}] = struct{}{}
		}
	}

	changed := map[string]bool{}
	for key := range currentKeys {
		if _, retained := desiredKeys[key]; !retained {
			changed[key.dependent] = true
		}
	}
	for key := range desiredKeys {
		if _, existed := currentKeys[key]; !existed {
			changed[key.dependent] = true
		}
	}
	return changed
}

func applyDesiredDependencies(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	desiredTasks map[string]desiredTask,
	currentTasks map[string]persistedTask,
	taskIDs map[string]uuid.UUID,
	current map[string]persistedDependency,
	metadataUpdatedTasks map[string]bool,
	resolvedFlipTasks map[string]bool,
	taskChangeTypes map[string]task.ChangeType,
	result *PlanApplyResult,
) error {
	taskKeysByID := make(map[uuid.UUID]string, len(taskIDs))
	for key, id := range taskIDs {
		taskKeysByID[id] = key
	}
	retainedTasks := map[string]bool{}
	for key := range desiredTasks {
		retainedTasks[key] = true
	}
	for key, task := range currentTasks {
		if terminalTaskStatus(task.Status) {
			retainedTasks[key] = true
		}
	}
	desired := map[string]persistedDependency{}
	for _, dependentKey := range sortedTaskKeys(desiredTasks) {
		dependentID, exists := taskIDs[dependentKey]
		if !exists {
			return ErrPlanInvalidInput
		}
		currentTask, dependentExists := currentTasks[dependentKey]
		for _, dependency := range desiredTasks[dependentKey].DependsOn {
			prerequisiteID, exists := taskIDs[dependency.TaskKey]
			if !exists {
				return ErrPlanInvalidInput
			}
			identity := dependencyIdentity(dependentID, prerequisiteID)
			if _, existed := current[identity]; !existed {
				if desiredTasks[dependency.TaskKey].Status == "cancelled" ||
					!canAddPlanTaskDependency(currentTask, dependentExists) {
					return ErrPlanInvalidInput
				}
			}
			desired[identity] = persistedDependency{
				DependentTaskID: dependentID, PrerequisiteTaskID: prerequisiteID,
			}
		}
	}
	for identity, dependency := range current {
		dependentKey := taskKeysByID[dependency.DependentTaskID]
		prerequisiteKey := taskKeysByID[dependency.PrerequisiteTaskID]
		key := planDependencyKey{dependent: dependentKey, prerequisite: prerequisiteKey}
		if retainOmittedTerminalDependency(key, currentTasks, desiredTasks) {
			desired[identity] = dependency
		}
	}
	changedTasks := map[string]bool{}
	for _, identity := range sortedDependencyIdentities(current) {
		dependency := current[identity]
		if _, retained := desired[identity]; retained {
			continue
		}
		_, err := q.DeleteTaskDependency(ctx, generated.DeleteTaskDependencyParams{
			CompanyID:          principal.CompanyID,
			DependentTaskID:    dependency.DependentTaskID,
			PrerequisiteTaskID: dependency.PrerequisiteTaskID,
			ExpectedVersion:    dependency.Version,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPlanConflict
		}
		if err != nil {
			return fmt.Errorf("delete Company Plan Task dependency: %w", err)
		}
		changedTasks[taskKeysByID[dependency.DependentTaskID]] = true
	}
	for _, identity := range sortedDependencyIdentities(desired) {
		dependency := desired[identity]
		if _, existed := current[identity]; existed {
			// A retained edge whose prerequisite crossed `done` changes the
			// dependent's projection just as an edge change would.
			if resolvedFlipTasks[taskKeysByID[dependency.PrerequisiteTaskID]] {
				changedTasks[taskKeysByID[dependency.DependentTaskID]] = true
			}
			continue
		}
		cycle, err := q.TaskDependencyPathExists(ctx, generated.TaskDependencyPathExistsParams{
			CompanyID:  principal.CompanyID,
			FromTaskID: dependency.DependentTaskID,
			ToTaskID:   dependency.PrerequisiteTaskID,
		})
		if err != nil {
			return fmt.Errorf("validate Company Plan Task dependency cycle: %w", err)
		}
		if cycle {
			return ErrPlanInvalidInput
		}
		if _, err := q.InsertTaskDependency(ctx, generated.InsertTaskDependencyParams{
			CompanyID:          principal.CompanyID,
			DependentTaskID:    dependency.DependentTaskID,
			PrerequisiteTaskID: dependency.PrerequisiteTaskID,
			CreatedByUserID:    uuid.NullUUID{UUID: principal.UserID, Valid: true},
		}); err != nil {
			return fmt.Errorf("insert Company Plan Task dependency: %w", err)
		}
		changedTasks[taskKeysByID[dependency.DependentTaskID]] = true
	}
	for taskKey := range changedTasks {
		retained := retainedTasks[taskKey]
		if _, existed := currentTasks[taskKey]; retained && existed {
			taskChangeTypes[taskKey] = task.ChangeTypeUpdated
			if !metadataUpdatedTasks[taskKey] {
				if err := task.AdvanceDependencyProjectionInTx(
					ctx,
					q,
					principal.CompanyID,
					taskIDs[taskKey],
					currentTasks[taskKey].Version,
				); errors.Is(err, task.ErrConflict) {
					return ErrPlanConflict
				} else if err != nil {
					return fmt.Errorf("advance Company Plan Task dependency projection: %w", err)
				}
				result.Updated++
			}
		}
	}
	return nil
}

// resolvePlanAgents locks the active Agents the plan names and returns them
// keyed by the id the plan used. A plan that names an unknown, foreign or
// archived Agent is a request error, never a silent reassignment. A Task that
// keeps the Agent it is already executed by is the one exception, as in a
// Task PATCH: archiving an Agent leaves it on its finished Tasks, and a done
// Task cannot drop it, so restating that Task must not block the plan.
func resolvePlanAgents(
	ctx context.Context,
	q *generated.Queries,
	companyID uuid.UUID,
	tasks map[string]persistedTask,
	desiredTasks map[string]desiredTask,
) (map[string]uuid.UUID, error) {
	requested := make(map[string]struct{}, len(desiredTasks))
	kept := make(map[string]uuid.UUID)
	for key, desired := range desiredTasks {
		// Work the assignee does themself names no Agent, so there is
		// nothing to resolve or lock.
		if desired.AgentID == "" {
			continue
		}
		if current, exists := tasks[key]; exists && planTaskNamesAgent(current, desired) {
			kept[desired.AgentID] = current.ExecutedByAgentID.UUID
			continue
		}
		requested[desired.AgentID] = struct{}{}
	}
	ids := make([]uuid.UUID, 0, len(requested))
	keys := make([]string, 0, len(requested))
	for key := range requested {
		parsed := uuidOrNil(key)
		if parsed == uuid.Nil {
			return nil, fmt.Errorf("%w: Task agent id %q is not a UUID", ErrPlanInvalidInput, key)
		}
		ids = append(ids, parsed)
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return kept, nil
	}
	result := make(map[string]uuid.UUID, len(keys)+len(kept))
	active, err := q.LockActiveCompanyAgents(ctx, generated.LockActiveCompanyAgentsParams{
		CompanyID: companyID, AgentIds: ids,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve Company Plan agents: %w", err)
	}
	for _, agentID := range active {
		result[agentID.String()] = agentID
	}
	for _, key := range keys {
		if _, exists := result[key]; !exists {
			return nil, fmt.Errorf("%w: no active agent %q in this company", ErrPlanInvalidInput, key)
		}
	}
	for key, agentID := range kept {
		result[key] = agentID
	}
	return result, nil
}

func desiredTasksByKey(document PlanDocument) map[string]desiredTask {
	result := make(map[string]desiredTask)
	for _, task := range document.Tasks {
		result[task.Key] = desiredTask{PlanTask: task}
	}
	return result
}

func computeCompanyPlanChanges(
	document PlanDocument,
	tasks map[string]persistedTask,
	dependencies map[string]persistedDependency,
) []PlanChange {
	desiredTasks := desiredTasksByKey(document)
	changes := make([]PlanChange, 0)
	taskKeyByID := make(map[uuid.UUID]string, len(tasks))
	for key, task := range tasks {
		taskKeyByID[task.ID] = key
	}
	for key, target := range desiredTasks {
		current, exists := tasks[key]
		switch {
		case !exists:
			changes = append(changes, PlanChange{EntityType: "task", Operation: "create", Key: key})
		case current.Title != target.Title || current.Description != target.Description ||
			current.Status != target.Status || current.Priority != target.Priority ||
			current.MilestoneType != string(target.MilestoneType) ||
			!planTaskNamesAgent(current, target):
			changes = append(changes, PlanChange{EntityType: "task", Operation: "update", Key: key})
		}
	}
	for key, current := range tasks {
		if _, retained := desiredTasks[key]; !retained && !terminalTaskStatus(current.Status) {
			changes = append(changes, PlanChange{EntityType: "task", Operation: "delete", Key: key})
		}
	}
	currentDependencies := make(map[string]struct{}, len(dependencies))
	for _, dependency := range dependencies {
		dependentKey := taskKeyByID[dependency.DependentTaskID]
		prerequisiteKey := taskKeyByID[dependency.PrerequisiteTaskID]
		currentDependencies[dependentKey+"\x00"+prerequisiteKey] = struct{}{}
	}
	desiredDependencies := make(map[string]struct{})
	for dependentKey, task := range desiredTasks {
		for _, dependency := range task.DependsOn {
			key := dependentKey + "\x00" + dependency.TaskKey
			desiredDependencies[key] = struct{}{}
			if _, exists := currentDependencies[key]; !exists {
				changes = append(changes, PlanChange{
					EntityType: "dependency", Operation: "create",
					Key: dependentKey + " -> " + dependency.TaskKey,
				})
			}
		}
	}
	for key := range currentDependencies {
		if _, retained := desiredDependencies[key]; retained {
			continue
		}
		dependentKey, prerequisiteKey, _ := strings.Cut(key, "\x00")
		if retainOmittedTerminalDependency(
			planDependencyKey{dependent: dependentKey, prerequisite: prerequisiteKey}, tasks, desiredTasks,
		) {
			continue
		}
		changes = append(changes, PlanChange{
			EntityType: "dependency", Operation: "delete",
			Key: dependentKey + " -> " + prerequisiteKey,
		})
	}
	sort.Slice(changes, func(left, right int) bool {
		if changes[left].EntityType != changes[right].EntityType {
			return changes[left].EntityType < changes[right].EntityType
		}
		if changes[left].Key != changes[right].Key {
			return changes[left].Key < changes[right].Key
		}
		return changes[left].Operation < changes[right].Operation
	})
	return changes
}

func renderCompanyPlanPreview(changes []PlanChange) string {
	if len(changes) == 0 {
		return "- No Company Plan changes."
	}
	var preview strings.Builder
	for _, change := range changes {
		preview.WriteString("- ")
		preview.WriteString(strings.ToUpper(change.Operation[:1]))
		preview.WriteString(change.Operation[1:])
		preview.WriteString(" ")
		preview.WriteString(change.EntityType)
		preview.WriteString(" ")
		fmt.Fprintf(&preview, "%q", change.Key)
		preview.WriteString("\n")
	}
	return strings.TrimSuffix(preview.String(), "\n")
}

func dependencyIdentity(dependentTaskID, prerequisiteTaskID uuid.UUID) string {
	return dependentTaskID.String() + "\x00" + prerequisiteTaskID.String()
}

func sortedDependencyIdentities(values map[string]persistedDependency) []string {
	result := make([]string, 0, len(values))
	for identity := range values {
		result = append(result, identity)
	}
	sort.Strings(result)
	return result
}

// planTaskNamesAgent reports whether the persisted Task already has the
// executing Agent named by the plan — none when the assignee does the work.
func planTaskNamesAgent(current persistedTask, target desiredTask) bool {
	if target.AgentID == "" {
		return !current.ExecutedByAgentID.Valid
	}
	return current.ExecutedByAgentID.Valid && current.ExecutedByAgentID.UUID.String() == target.AgentID
}

// refuseLabelShapedNewKeys keeps a plan's new slugs out of the
// {taskPrefix}-{number} label namespace, which belongs to the server: it
// mints those keys from the number it allocates, so a slug shaped like a
// label would lie about a number or take a later Task's label. Existing
// keys are left alone; renaming is not a plan operation.
func refuseLabelShapedNewKeys(
	desired PlanDocument,
	existing map[string]persistedTask,
	taskPrefix string,
) error {
	for _, planned := range desired.Tasks {
		if _, exists := existing[planned.Key]; exists {
			continue
		}
		if task.KeyInMintedNamespace(taskPrefix, planned.Key) {
			return fmt.Errorf(
				"%w: Task key %q has the shape of a %s-{number} label, which the server mints; "+
					"choose a slug",
				ErrPlanInvalidInput, planned.Key, taskPrefix,
			)
		}
	}
	return nil
}

// errPlanSlugChanged says the artifact was authored against another slug
// than the Company's live one. Each entry point decides what that means to
// its caller: a conflict for a direct apply or a prepare, a skipped outcome
// for an approved action.
var errPlanSlugChanged = errors.New("Company slug changed since the plan was authored")
