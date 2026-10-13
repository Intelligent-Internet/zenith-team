package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"boardly.dev/boardly/server/internal/contentbundle"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	db "boardly.dev/boardly/server/pkg/db/generated"
)

// localDirectoryResourceType is the only company-resource type a run context
// carries today. It mirrors the CHECK constraint on company_resources and the
// constant the daemon matches on; renaming one means renaming all three.
const localDirectoryResourceType = "local_directory"

// runResourceContext is one company resource as the daemon reads it. The JSON
// tags are the wire contract with internal/daemon.RunResourceContext — the two
// structs are deliberately separate types so the daemon package does not
// import the server's, and deliberately identical on the wire.
type runResourceContext struct {
	ID            string `json:"id"`
	ResourceType  string `json:"resourceType"`
	LocalDaemonID string `json:"localDaemonId,omitempty"`
	LocalPath     string `json:"localPath,omitempty"`
	Label         string `json:"label,omitempty"`
}

// runAgentBrief is the agent's execution identity as frozen into the run
// context. The JSON tags are the wire contract with the daemon's
// RunAgentContext decode — separate types, identical wire — and empty members
// are omitted so absence keeps meaning "use the runtime default".
type runAgentBrief struct {
	SkillRefs       []contentbundle.SkillRef `json:"skillRefs,omitempty"`
	SkillSetHash    string                   `json:"skillSetHash,omitempty"`
	Capabilities    json.RawMessage          `json:"capabilities,omitempty"`
	DisabledPlugins json.RawMessage          `json:"disabledPlugins,omitempty"`
	Name            string                   `json:"name,omitempty"`
	Title           string                   `json:"title,omitempty"`
	Instructions    string                   `json:"instructions,omitempty"`
	Model           string                   `json:"model,omitempty"`
	ThinkingLevel   string                   `json:"thinkingLevel,omitempty"`
	// IsRoot marks the CEO Agent — the root of the reporting tree. It is
	// frozen here because the Run's planning authority and workspace scope
	// follow the Agent's place in the tree at enqueue time.
	IsRoot bool `json:"isRoot,omitempty"`
}

// runCompanyBrief is the founder-supplied business identity every interactive
// Run receives. It is context for the work, never execution authority.
//
// It carries founder-stated identity and attested milestones. The daemon prefers
// the authored workspace context when present. It mirrors daemon.RunCompanyContext field for field; the two
// types are deliberately separate (the daemon links no server package) and the
// JSON tags are the contract between them.
type runCompanyBrief struct {
	Name               string   `json:"name"`
	BusinessContext    string   `json:"businessContext"`
	WebsiteURL         string   `json:"websiteUrl,omitempty"`
	AttestedMilestones []string `json:"attestedMilestones,omitempty"`
}

// runExecutionProfile is the immutable product context that decides how the
// daemon may execute one Run. It stays inside runs.context so retries inherit
// the exact same contract without adding another family of snapshot columns.
type runProjectBrief struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Revision    int64  `json:"revision"`
}

type runExecutionProfile struct {
	DocumentWriting  bool             `json:"documentWriting,omitempty"`
	DocumentThreadID string           `json:"documentThreadId,omitempty"`
	Project          *runProjectBrief `json:"project,omitempty"`
	Source           RunEnqueueSource `json:"source"`
	Task             *runTaskBrief    `json:"task,omitempty"`
}

type runTaskBrief struct {
	HumanWork      bool   `json:"humanWork,omitempty"`
	Key            string `json:"key"`
	Title          string `json:"title"`
	Description    string `json:"description"`
	Status         string `json:"status"`
	Priority       string `json:"priority"`
	ConversationID string `json:"conversationId"`
	// WorkspaceGeneration is the identity of the Task's workspace on the
	// assignee's machine (ADR 0014): only a confirmed handoff advances it.
	// The daemon keys the Task's worktree on it, so a root prepared under an
	// older value is retired rather than continued. Frozen here because a
	// handoff cancels every Run of the previous generation, so the value a
	// Run carries is the value it may run under.
	WorkspaceGeneration int64                    `json:"workspaceGeneration"`
	Dependencies        []runTaskDependencyBrief `json:"dependencies,omitempty"`
}

type runTaskDependencyBrief struct {
	ID             string     `json:"id"`
	ConversationID string     `json:"conversationId"`
	Key            string     `json:"key"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Status         string     `json:"status"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
}

// buildRunContext produces the run's context document: whatever the caller
// supplied, with the execution profile frozen under `executionProfile`, the
// company's business context frozen under `company`, its resources merged in
// under `resources`, the trigger message's content frozen under `prompt`, the
// agent's execution identity frozen under `agent`, the Conversation's recent
// chat frozen under `conversationSnapshot`, the member who wrote the
// trigger under `trigger`, and a Task's attached files under
// `taskAttachments`.
//
// These keys are attached here rather than by each caller because the enqueue
// kernel is the one place every run passes through. A run that reaches a
// daemon without Company context makes the agent rediscover information the
// founder already supplied. A run that reaches a
// daemon without its resources silently loses the local-directory binding —
// the daemon would prepare a fresh worktree and the agent would edit the
// wrong files. A run that reaches a daemon without its prompt forces a
// claim-time fetch back through the user plane — the seam that once handed
// the provider a generic continue instruction instead of the user's words. A
// run that reaches a daemon without its agent brief executes with the
// provider defaults instead of the model, instructions, and thinking level
// the user configured. The execution profile is also the structural fence for
// Schedule isolation and the Task execution contract; prompt text cannot grant
// either capability. A caller-composed prompt wins, while the kernel always
// overwrites agent, executionProfile, resources, conversationSnapshot,
// trigger and taskAttachments from authoritative rows; the snapshot and
// trigger are what let the agent read who said what before this Run, so no
// caller may forge them. Retry children
// inherit all of them through CreateRetryRun's context copy. A Schedule Run
// carries no chat history by contract.
func buildRunContext(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
	runtimeID uuid.UUID,
) ([]byte, error) {
	document := map[string]json.RawMessage{}
	if len(in.Context) > 0 {
		if err := json.Unmarshal(in.Context, &document); err != nil {
			return nil, fmt.Errorf("decode caller run context: %w", err)
		}
	}
	if _, composed := document["prompt"]; !composed && in.TriggerPrompt != "" {
		encodedPrompt, err := json.Marshal(in.TriggerPrompt)
		if err != nil {
			return nil, fmt.Errorf("encode frozen run prompt: %w", err)
		}
		document["prompt"] = encodedPrompt
	}
	brief, err := runContextAgentBrief(ctx, q, in, runtimeID)
	if err != nil {
		return nil, err
	}
	if in.Source == RunSourceCompanyResearch {
		var research runAgentBrief
		if err := json.Unmarshal(brief, &research); err != nil {
			return nil, err
		}
		research.Model, research.ThinkingLevel = in.ResearchModel, in.ResearchThinkingLevel
		brief, err = json.Marshal(research)
		if err != nil {
			return nil, err
		}
	}
	document["agent"] = brief
	profile, err := runContextExecutionProfile(ctx, q, in)
	if err != nil {
		return nil, err
	}
	document["executionProfile"] = profile
	// A caller cannot inject files into a Run; only its immutable Task project
	// grants them, and only the documents the Run's authorizer may open
	// (attachment_access, ADR 0017).
	delete(document, "projectDocuments")
	if in.TaskID.Valid {
		files, err := q.ListTaskProjectDocuments(ctx, db.ListTaskProjectDocumentsParams{
			CompanyID: in.CompanyID, TaskID: in.TaskID.UUID, ActorUserID: in.AuthorizedByUserID,
		})
		if err != nil {
			return nil, fmt.Errorf("freeze Project documents: %w", err)
		}
		briefs := make([]runAttachedFileBrief, 0, len(files))
		for _, file := range files {
			briefs = append(
				briefs,
				runAttachedFileBrief{
					ID:          file.ID.String(),
					Filename:    file.Filename,
					ContentType: file.ContentType,
					SizeBytes:   file.SizeBytes,
				},
			)
		}
		if len(briefs) > 0 {
			encoded, err := json.Marshal(briefs)
			if err != nil {
				return nil, err
			}
			document["projectDocuments"] = encoded
		}
	}
	// Task files are frozen from rows only, like the snapshot and trigger.
	delete(document, "taskAttachments")

	if in.Source == RunSourceSchedule {
		delete(document, "company")
		delete(document, "resources")
		delete(document, "conversationSnapshot")
		delete(document, "trigger")
		return encodeRunContext(document)
	}
	queued, err := runContextQueuedTaskMessages(ctx, q, in)
	if err != nil {
		return nil, err
	}
	snapshot, err := runContextConversationSnapshot(ctx, q, in, queued)
	if err != nil {
		return nil, err
	}
	document["conversationSnapshot"] = snapshot
	if in.InvocationID.Valid {
		if err := freezeChannelInvocation(ctx, q, in, document); err != nil {
			return nil, err
		}
	} else if err := freezeConversationWhiteboards(ctx, q, in, document); err != nil {
		return nil, err
	}
	trigger, err := runContextTriggerBrief(ctx, q, in)
	if err != nil {
		return nil, err
	}
	if trigger == nil {
		delete(document, "trigger")
	} else {
		document["trigger"] = trigger
	}
	company, err := runContextCompanyBrief(ctx, q, in.CompanyID)
	if err != nil {
		return nil, err
	}
	document["company"] = company
	references, err := runContextCompanyReferences(ctx, q, in.CompanyID)
	if err != nil {
		return nil, err
	}
	if references != nil {
		document["companyReferences"] = references
	}
	if in.TaskID.Valid {
		files, err := runContextTaskAttachments(ctx, q, in, queued)
		if err != nil {
			return nil, err
		}
		if files != nil {
			document["taskAttachments"] = files
		}
	}
	resources, err := runContextResources(ctx, q, in.CompanyID)
	if err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		// Absent rather than an empty array: the daemon treats a run with no
		// local_directory as "prepare a fresh worktree", which is the normal
		// case and should not carry a key to say so.
		delete(document, "resources")
		return encodeRunContext(document)
	}

	encoded, err := json.Marshal(resources)
	if err != nil {
		return nil, fmt.Errorf("encode run context resources: %w", err)
	}
	document["resources"] = encoded
	return encodeRunContext(document)
}

// runAttachedFileBrief names one founder-supplied document the Company keeps
// for its Agents. It mirrors daemon.RunAttachedFile; the JSON tags are the
// contract between them.
type runAttachedFileBrief struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType,omitempty"`
	SizeBytes   int64  `json:"sizeBytes,omitempty"`
}

// runContextCompanyReferences freezes the founder-supplied documents the
// Company keeps for its Agents. Unlike prompt attachments these belong to the
// Company rather than to one message, so every interactive Run receives them
// and the daemon can place them in the Company workspace once.
func runContextCompanyReferences(
	ctx context.Context,
	q *db.Queries,
	companyID uuid.UUID,
) (json.RawMessage, error) {
	rows, err := q.ListCompanyReferenceDocuments(ctx, companyID)
	if err != nil {
		return nil, fmt.Errorf("load frozen Company reference documents: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	briefs := make([]runAttachedFileBrief, 0, len(rows))
	for _, row := range rows {
		briefs = append(briefs, runAttachedFileBrief{
			ID:          row.ID.String(),
			Filename:    row.Filename,
			ContentType: row.ContentType,
			SizeBytes:   row.SizeBytes,
		})
	}
	encoded, err := json.Marshal(briefs)
	if err != nil {
		return nil, fmt.Errorf("encode frozen Company reference documents: %w", err)
	}
	return encoded, nil
}

// maxTaskRunAttachments bounds the files one Task Run downloads. It matches
// the most files one message may carry, so a Task launched with a full set
// always reaches its first Run whole; later Runs keep the newest.
const maxTaskRunAttachments = 20

// runContextTaskAttachments freezes the files a member attached to the Task:
// those on their messages in its Conversation, those the triggering write is
// about to link, which count only when the member who authorized the Run
// uploaded them, and those reserved by the colleagues' queued messages this
// Run carries (ADR 0014), which it materializes right after the enqueue.
// Every Run of the Task receives them, because a Task's inputs outlive the
// message that carried them, and the daemon downloads them into the Run's
// scratch directory rather than the workspace.
func runContextTaskAttachments(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
	carried []db.ListQueuedRunQueueItemsForTaskRunRow,
) (json.RawMessage, error) {
	triggerIDs := in.TriggerAttachmentIDs
	if triggerIDs == nil {
		triggerIDs = []uuid.UUID{}
	}
	carriedIDs := make([]uuid.UUID, len(carried))
	for index, row := range carried {
		carriedIDs[index] = row.ConversationRunQueueItem.ID
	}
	rows, err := q.ListTaskRunInputAttachments(ctx, db.ListTaskRunInputAttachmentsParams{
		TaskID: in.TaskID.UUID, CompanyID: in.CompanyID,
		TriggerAttachmentIds: triggerIDs, AuthorizedByUserID: in.AuthorizedByUserID,
		CarriedQueueItemIds: carriedIDs, RowLimit: maxTaskRunAttachments,
	})
	if err != nil {
		return nil, fmt.Errorf("load frozen Task attachments: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	briefs := make([]runAttachedFileBrief, 0, len(rows))
	for index := len(rows) - 1; index >= 0; index-- {
		row := rows[index]
		briefs = append(briefs, runAttachedFileBrief{
			ID:          row.ID.String(),
			Filename:    row.Filename,
			ContentType: row.ContentType,
			SizeBytes:   row.SizeBytes,
		})
	}
	encoded, err := json.Marshal(briefs)
	if err != nil {
		return nil, fmt.Errorf("encode frozen Task attachments: %w", err)
	}
	return encoded, nil
}

func runContextCompanyBrief(
	ctx context.Context,
	q *db.Queries,
	companyID uuid.UUID,
) (json.RawMessage, error) {
	row, err := q.GetCompanyRunBrief(ctx, companyID)
	if err != nil {
		return nil, fmt.Errorf("load frozen Company context: %w", err)
	}
	attestations, err := q.ListCompanyMilestoneAttestations(ctx, companyID)
	if err != nil {
		return nil, fmt.Errorf("load frozen Company milestone attestations: %w", err)
	}
	attested := make([]string, 0, len(attestations))
	for _, attestation := range attestations {
		attested = append(attested, attestation.MilestoneType)
	}
	encoded, err := json.Marshal(runCompanyBrief{
		Name:               row.Name,
		BusinessContext:    row.BusinessContext,
		WebsiteURL:         row.WebsiteUrl.String,
		AttestedMilestones: attested,
	})
	if err != nil {
		return nil, fmt.Errorf("encode frozen Company context: %w", err)
	}
	return encoded, nil
}

func runContextExecutionProfile(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
) (json.RawMessage, error) {
	if !validRunEnqueueSource(in.Source) {
		return nil, errors.New("run enqueue source is invalid")
	}
	profile := runExecutionProfile{Source: in.Source}
	document, documentErr := q.GetDocumentAgentContext(ctx, db.GetDocumentAgentContextParams{CompanyID: in.CompanyID, ConversationID: in.ConversationID})
	if documentErr == nil {
		profile.DocumentThreadID = document.ThreadID.UUID.String()
		profile.DocumentWriting = in.DocumentWriting
	} else if !errors.Is(documentErr, pgx.ErrNoRows) {
		return nil, documentErr
	}
	if in.TaskID.Valid {
		task, err := runContextTaskBrief(ctx, q, in)
		if err != nil {
			return nil, err
		}
		profile.Task = &task
		project, err := q.GetTaskProjectForShare(
			ctx,
			db.GetTaskProjectForShareParams{CompanyID: in.CompanyID, TaskID: in.TaskID.UUID},
		)
		if err != nil {
			return nil, fmt.Errorf("freeze Project: %w", err)
		}
		if project.ArchivedAt.Valid {
			return nil, errors.New("Task Project is archived")
		}
		profile.Project = &runProjectBrief{
			ID:          project.ID.String(),
			Key:         project.Key,
			Title:       project.Title,
			Description: project.Description,
			Revision:    project.Version,
		}
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return nil, fmt.Errorf("encode frozen execution profile: %w", err)
	}
	return encoded, nil
}

func validRunEnqueueSource(source RunEnqueueSource) bool {
	switch source {
	case RunSourceMessage, RunSourceAssign, RunSourceExecution, RunSourcePromotion,
		RunSourceHumanInput, RunSourceSchedule, RunSourceTaskReport, RunSourceCompanyResearch:
		return true
	default:
		return false
	}
}

func runContextTaskBrief(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
) (runTaskBrief, error) {
	row, err := q.GetRunTaskBrief(ctx, db.GetRunTaskBriefParams{
		TaskID: in.TaskID.UUID, CompanyID: in.CompanyID,
		ConversationID: in.ConversationID, AgentID: in.AgentID,
		AuthorizedByUserID: in.AuthorizedByUserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// The brief's assignee fence is the kernel-level "only the assignee
		// starts a Task Run" (ADR 0014); the caller has already matched the
		// Task, Conversation and executing Agent, so a miss names the
		// authorizer.
		return runTaskBrief{}, ErrRunTaskAssigneeRequired
	}
	if err != nil {
		return runTaskBrief{}, fmt.Errorf("load frozen Task brief: %w", err)
	}
	dependencies, err := q.ListRunTaskPrerequisiteBriefs(ctx, db.ListRunTaskPrerequisiteBriefsParams{
		CompanyID: in.CompanyID, DependentTaskID: in.TaskID.UUID,
	})
	if err != nil {
		return runTaskBrief{}, fmt.Errorf("list frozen Task prerequisites: %w", err)
	}
	brief := runTaskBrief{
		HumanWork: row.HumanWork,
		Key:       row.Key, Title: row.Title, Description: row.Description,
		Status: row.Status, Priority: row.Priority,
		ConversationID:      row.ConversationID.String(),
		WorkspaceGeneration: int64(row.WorkspaceGeneration),
		Dependencies:        make([]runTaskDependencyBrief, 0, len(dependencies)),
	}
	for _, dependency := range dependencies {
		var completedAt *time.Time
		if dependency.CompletedAt.Valid {
			value := dependency.CompletedAt.Time.UTC()
			completedAt = &value
		}
		brief.Dependencies = append(brief.Dependencies, runTaskDependencyBrief{
			ID: dependency.ID.String(), ConversationID: dependency.ConversationID.String(),
			Key: dependency.Key, Title: dependency.Title, Description: dependency.Description,
			Status: dependency.Status, CompletedAt: completedAt,
		})
	}
	return brief, nil
}

// frozenStudioEnabled reads the Run's Studio access from the agent brief this
// Run just froze, so runs.studio_enabled and the daemon's document agree.
// The document was built by buildRunContext, not read back.
func frozenStudioEnabled(runContext []byte) bool {
	var document struct {
		Agent struct {
			Capabilities struct {
				EnabledTools []string `json:"enabledTools"`
			} `json:"capabilities"`
		} `json:"agent"`
	}
	_ = json.Unmarshal(runContext, &document)
	return slices.Contains(document.Agent.Capabilities.EnabledTools, "zenith-studio")
}

// runContextAgentBrief reads the agent's execution identity inside the
// enqueue transaction and encodes it for the context document. The enqueue
// kernel has already resolved the agent row for this company, so the id
// cannot miss here except on a concurrent hard delete — which fails the
// enqueue rather than freezing an empty brief. The plugin observations are
// the Run's runtime's — the binding the enqueue resolved — since the
// template itself has no machine.
func runContextAgentBrief(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
	runtimeID uuid.UUID,
) (json.RawMessage, error) {
	row, err := q.GetRunAgentBrief(
		ctx,
		db.GetRunAgentBriefParams{AgentID: in.AgentID, RuntimeID: runtimeID, ConversationID: in.ConversationID},
	)
	if err != nil {
		return nil, fmt.Errorf("load frozen agent brief: %w", err)
	}
	var skills []contentbundle.SkillRef
	if in.Source != RunSourceSchedule {
		skills, err = runAgentSkillRefs(ctx, q, in.CompanyID, in.AgentID)
		if err != nil {
			return nil, err
		}
	}
	encoded, err := json.Marshal(runAgentBrief{
		SkillRefs: skills, SkillSetHash: contentbundle.SkillSetHash(skills),
		Capabilities: row.Capabilities, DisabledPlugins: row.DisabledPlugins,
		Name:          row.Name,
		Title:         row.Title,
		Instructions:  row.Instructions,
		Model:         row.Model,
		ThinkingLevel: row.ThinkingLevel,
		IsRoot:        row.IsRoot,
	})
	if err != nil {
		return nil, fmt.Errorf("encode frozen agent brief: %w", err)
	}
	return encoded, nil
}

// runContextResources reads the company's resources and projects the ones a
// daemon can act on.
func runContextResources(
	ctx context.Context,
	q *db.Queries,
	companyID uuid.UUID,
) ([]runResourceContext, error) {
	rows, err := q.ListCompanyResourcesForRun(ctx, companyID)
	if err != nil {
		return nil, fmt.Errorf("list company resources: %w", err)
	}

	resources := make([]runResourceContext, 0, len(rows))
	for _, row := range rows {
		resources = append(resources, runResourceContext{
			ID:            row.ID.String(),
			ResourceType:  row.ResourceType,
			LocalDaemonID: row.DaemonID,
			LocalPath:     row.LocalPath,
			Label:         row.Label,
		})
	}
	return resources, nil
}

// freezeConversationWhiteboards names the Whiteboards made in the Agent or
// Task chat a Run answers that its Authorizer may open, as a channel Run
// names its channel's, so the Run knows which ones it can work on.
func freezeConversationWhiteboards(
	ctx context.Context,
	q *db.Queries,
	in RunTrigger,
	document map[string]json.RawMessage,
) error {
	whiteboards, err := q.ListRunConversationWhiteboards(ctx, db.ListRunConversationWhiteboardsParams{
		CompanyID:      in.CompanyID,
		ConversationID: in.ConversationID,
		ActorUserID:    in.AuthorizedByUserID,
	})
	if err != nil {
		return fmt.Errorf("freeze chat Whiteboards: %w", err)
	}
	briefs := make([]runWhiteboardBrief, 0, len(whiteboards))
	for _, board := range whiteboards {
		briefs = append(briefs, runWhiteboardBrief{ID: board.ID.String(), Title: board.Title})
	}
	return freezeChatWhiteboards(document, briefs)
}
