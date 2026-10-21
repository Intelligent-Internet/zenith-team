package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func admitPageRun(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	runID uuid.UUID,
	write bool,
) (uuid.UUID, uuid.UUID, error) {
	document, documentErr := q.GetDocumentAgentRun(
		ctx,
		generated.GetDocumentAgentRunParams{CompanyID: p.CompanyID, RunID: runID, ActorUserID: p.UserID},
	)
	if documentErr == nil {
		project := document.ProjectID.UUID
		if _, err := authorize(ctx, q, p, project, write); err != nil {
			return project, uuid.Nil, err
		}
		if err := auth.RequireProjectPermissionHeld(
			ctx,
			q,
			p.CompanyID,
			project,
			p.UserID,
			auth.ProjectPermissionTasksExecute,
		); err != nil {
			return project, uuid.Nil, err
		}
		if _, err := q.LockConversationForSystemRunQueueMutation(
			ctx,
			generated.LockConversationForSystemRunQueueMutationParams{
				CompanyID:      p.CompanyID,
				ConversationID: document.ConversationID,
			},
		); err != nil {
			return project, uuid.Nil, lookup(err)
		}
		run, err := q.LockRunForOutputPresentation(
			ctx,
			generated.LockRunForOutputPresentationParams{CompanyID: p.CompanyID, RunID: runID},
		)
		if err != nil {
			return project, uuid.Nil, lookup(err)
		}
		if run.Status != "running" || !run.ExecutionAuthorizedByUserID.Valid ||
			run.ExecutionAuthorizedByUserID.UUID != p.UserID {
			return project, uuid.Nil, ErrForbidden
		}
		return project, uuid.Nil, nil
	}
	if !errors.Is(documentErr, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, documentErr
	}
	identity, err := q.GetOverviewTaskRun(
		ctx,
		generated.GetOverviewTaskRunParams{CompanyID: p.CompanyID, ID: runID, AssigneeUserID: p.UserID},
	)
	if err != nil {
		return uuid.Nil, uuid.Nil, lookup(err)
	}
	project := identity.ProjectID
	if _, err = authorize(ctx, q, p, project, write); err != nil {
		return project, uuid.Nil, err
	}
	if err = auth.RequireProjectPermissionHeld(
		ctx,
		q,
		p.CompanyID,
		project,
		p.UserID,
		auth.ProjectPermissionTasksExecute,
	); err != nil {
		return project, uuid.Nil, err
	}
	// Same order as Task/execution mutations: Company, member, Task,
	// Conversation, Run, then the Project's canvas/page.
	task, err := q.GetTaskForCompanyByIDForUpdate(
		ctx,
		generated.GetTaskForCompanyByIDForUpdateParams{CompanyID: p.CompanyID, TaskID: identity.TaskID.UUID},
	)
	if err != nil {
		return project, uuid.Nil, lookup(err)
	}
	if _, err = q.LockConversationForSystemRunQueueMutation(
		ctx,
		generated.LockConversationForSystemRunQueueMutationParams{
			CompanyID:      p.CompanyID,
			ConversationID: identity.ConversationID,
		},
	); err != nil {
		return project, uuid.Nil, lookup(err)
	}
	run, err := q.LockRunForOutputPresentation(
		ctx,
		generated.LockRunForOutputPresentationParams{CompanyID: p.CompanyID, RunID: runID},
	)
	if err != nil {
		return project, uuid.Nil, lookup(err)
	}
	if run.Status != "running" || !run.TaskExecutionGeneration.Valid ||
		run.TaskExecutionGeneration.Int64 != task.ExecutionGeneration ||
		task.AssigneeUserID != p.UserID ||
		!run.ExecutionAuthorizedByUserID.Valid ||
		run.ExecutionAuthorizedByUserID.UUID != p.UserID {
		return project, uuid.Nil, ErrForbidden
	}
	return project, task.ID, nil
}

type Page struct {
	CanReviewSuggestions bool              `json:"canReviewSuggestions"`
	PendingCreation      bool              `json:"pendingCreation"`
	PageID               uuid.UUID         `json:"pageId"`
	VersionID            uuid.UUID         `json:"versionId"`
	ArtifactID           uuid.UUID         `json:"artifactId"`
	Title                string            `json:"title"`
	Theme                string            `json:"theme"`
	Sequence             int64             `json:"sequence"`
	CanEdit              bool              `json:"canEdit"`
	Content              json.RawMessage   `json:"content"`
	BlockHashes          map[string]string `json:"blockHashes"`
	Sources              []PageSourceState `json:"sources"`
	Changes              []PageChange      `json:"changes"`
}
type ProjectRead struct {
	ProjectID      uuid.UUID      `json:"projectId"`
	OverviewPageID *uuid.UUID     `json:"overviewPageId"`
	Tasks          []OverviewTask `json:"tasks"`
	Pages          []Artifact     `json:"pages"`
	Results        []Artifact     `json:"results"`
	Changes        []PageChange   `json:"changes"`
}

func (s *DefaultPageService) ReadProjectForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run uuid.UUID,
) (ProjectRead, error) {
	var result ProjectRead
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		project, _, err := admitPageRun(ctx, q, p, run, false)
		if err != nil {
			return err
		}
		result.ProjectID = project
		overview, err := q.GetProjectOverviewVersion(
			ctx,
			generated.GetProjectOverviewVersionParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			result.OverviewPageID = pointer(overview.WikiPageID.UUID)
		}
		tasks, err := q.ListOverviewTasks(
			ctx,
			generated.ListOverviewTasksParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		result.Tasks = []OverviewTask{}
		for _, t := range tasks {
			result.Tasks = append(
				result.Tasks,
				OverviewTask{
					ID:             t.ID,
					Title:          t.Title,
					Status:         t.Status,
					AssigneeUserID: t.AssigneeUserID,
					AssigneeName:   t.AssigneeName,
					AgentName:      t.AgentName,
					AgentID:        outputSource(t.ExecutedByAgentID),
					Revision:       t.Version,
				},
			)
		}
		rows, err := q.ListCanvasArtifacts(
			ctx,
			generated.ListCanvasArtifactsParams{
				CompanyID:    p.CompanyID,
				ProjectID:    project,
				ActorUserID:  p.UserID,
				AllArtifacts: true,
			},
		)
		if err != nil {
			return err
		}
		artifacts, err := decodeArtifacts(ctx, q, p, project, rows)
		if err != nil {
			return err
		}
		result.Pages = []Artifact{}
		result.Results = []Artifact{}
		for _, a := range artifacts {
			if a.PageID == nil && a.LatestVersion.WikiPageID != nil &&
				a.LatestVersion.SourceOutputID == nil {
				// Wiki body reads go through wiki_read, which enforces the Agent's
				// Wiki policy and records the published revision on its evidence trail.
				a.LatestVersion.Excerpt = ""
			}
			if a.PageID != nil && a.LatestVersion.OutputID == nil && a.LatestVersion.SourceOutputID == nil {
				result.Pages = append(result.Pages, a)
			} else {
				result.Results = append(result.Results, a)
			}
		}
		result.Changes, err = projectPageChanges(ctx, q, p.CompanyID, project, nil)
		return err
	})
	return result, mapWrite(err)
}

func (s *DefaultPageService) ReadPageForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run, pageID uuid.UUID,
) (Page, error) {
	var result Page
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := checkDocumentAgentPage(ctx, q, p, run, pageID); err != nil {
			return err
		}
		project, _, err := admitPageRun(ctx, q, p, run, false)
		if err != nil {
			return err
		}
		canEdit, err := auth.HoldsProjectPermission(
			ctx,
			q,
			p.CompanyID,
			project,
			p.UserID,
			auth.ProjectPermissionTasksWrite,
		)
		if err != nil {
			return err
		}
		result, err = readPage(ctx, q, p, project, pageID, canEdit)
		return err
	})
	return result, mapWrite(err)
}

func readPage(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
	canEdit bool,
) (Page, error) {
	var result Page
	v, err := editableProjectPage(ctx, q, p, project, pageID)
	if err != nil {
		return result, err
	}
	draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, pageID)
	if err != nil {
		return result, err
	}
	metadata, err := q.GetProjectPageMetadata(
		ctx,
		generated.GetProjectPageMetadataParams{
			CompanyID: p.CompanyID,
			ProjectID: nullable(&project),
			ID:        pageID,
		},
	)
	if err != nil {
		return result, err
	}
	pendingCreation, err := q.PagePendingCreation(
		ctx,
		generated.PagePendingCreationParams{CompanyID: p.CompanyID, WikiPageID: nullable(&pageID)},
	)
	if err != nil {
		return result, err
	}
	result = Page{
		PageID:               pageID,
		VersionID:            v.ID,
		ArtifactID:           v.ArtifactID,
		Title:                metadata.Title,
		Theme:                metadata.Theme,
		Sequence:             draft.HeadSeq,
		CanEdit:              canEdit && !pendingCreation,
		CanReviewSuggestions: canEdit,
		PendingCreation:      pendingCreation,
		Content:              draft.Content,
		BlockHashes:          map[string]string{},
	}
	var root wiki.BlockNode
	if err = json.Unmarshal(draft.Content, &root); err != nil {
		return result, err
	}
	var hash func(wiki.BlockNode)
	hash = func(n wiki.BlockNode) {
		if id, ok := n.Attrs["blockId"].(string); ok {
			result.BlockHashes[id] = wiki.BlockHash(n)
		}
		for _, child := range n.Content {
			hash(child)
		}
	}
	hash(root)
	result.Sources, err = pageSourceStates(ctx, q, p, project, root)
	if err != nil {
		return result, err
	}
	result.Changes, err = projectPageChanges(ctx, q, p.CompanyID, project, &pageID)
	return result, err
}

type pageProposal struct {
	CompanyID     uuid.UUID
	ProjectID     uuid.UUID
	PageID        uuid.UUID
	RunID         uuid.UUID
	ActorUserID   uuid.UUID
	RequestKey    uuid.UUID
	RequestHash   string
	Summary       string
	Operations    []wiki.BlockOperation
	BeforeContent json.RawMessage
	Sequence      int64
	BeforeTheme   *string
	AfterTheme    *string
}

// Both Page tools and captured writing requests persist the same reviewable
// proposal. Callers validate their own input and retain transaction/event ownership.
func insertPageProposal(ctx context.Context, q *generated.Queries, in pageProposal) (PageEditResult, error) {
	result := PageEditResult{Status: "proposed", Sequence: in.Sequence}
	operations, err := json.Marshal(in.Operations)
	if err != nil {
		return PageEditResult{}, err
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		return PageEditResult{}, err
	}
	change, err := q.InsertProjectPageChange(ctx, generated.InsertProjectPageChangeParams{
		CompanyID: in.CompanyID, ProjectID: in.ProjectID, PageID: in.PageID,
		RunID: nullable(&in.RunID), ActorUserID: in.ActorUserID,
		RequestKey: in.RequestKey, RequestHash: in.RequestHash,
		Operations: operations, BeforeContent: in.BeforeContent,
		Result: encodedResult, Summary: in.Summary, Status: result.Status,
		BeforeTheme: pageText(in.BeforeTheme), AfterTheme: pageText(in.AfterTheme),
	})
	if err != nil {
		return PageEditResult{}, err
	}
	result.ID = change.ID
	return result, nil
}

func (s *DefaultPageService) EditPageForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run, pageID uuid.UUID,
	in PageEditInput,
) (PageEditResult, error) {
	if err := normalizePageEdit(&in); err != nil {
		return PageEditResult{}, err
	}
	raw, _ := json.Marshal(in)
	hash := sha256.Sum256(append([]byte(run.String()+pageID.String()), raw...))
	requestHash := hex.EncodeToString(hash[:])
	// Allocation edits subtrees. Keep the caller's replay input unchanged.
	var working PageEditInput
	if err := json.Unmarshal(raw, &working); err != nil {
		return PageEditResult{}, ErrInvalid
	}
	in = working
	var result PageEditResult
	var notification *events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := checkDocumentAgentPage(ctx, q, p, run, pageID); err != nil {
			return err
		}
		project, _, err := admitPageRun(ctx, q, p, run, true)
		if err != nil {
			return err
		}
		if _, err = q.EnsureProjectCanvas(
			ctx,
			generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
		); err != nil {
			return err
		}
		receipt, err := q.GetProjectPageChangeReceipt(
			ctx,
			generated.GetProjectPageChangeReceiptParams{
				CompanyID:   p.CompanyID,
				ProjectID:   project,
				ActorUserID: p.UserID,
				RequestKey:  in.RequestKey,
			},
		)
		if err == nil {
			if receipt.RequestHash != requestHash {
				return ErrConflict
			}
			if err = json.Unmarshal(receipt.Result, &result); err != nil {
				return err
			}
			result.ID = receipt.ID
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		v, err := editableProjectPage(ctx, q, p, project, pageID)
		if err != nil {
			return err
		}
		draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, v.WikiPageID.UUID)
		if err != nil {
			return err
		}
		if in.SupersedesID != nil {
			prior, e := q.GetProjectPageChange(
				ctx,
				generated.GetProjectPageChangeParams{
					CompanyID: p.CompanyID,
					ProjectID: project,
					ID:        *in.SupersedesID,
				},
			)
			if e != nil {
				return lookup(e)
			}
			if prior.PageID != pageID {
				return ErrNotFound
			}
			if prior.Status != "proposed" && prior.Status != "needs_update" &&
				prior.Status != "partially_applied" {
				return ErrConflict
			}
			if e = q.SetProjectPageChangeStatus(
				ctx,
				generated.SetProjectPageChangeStatusParams{
					CompanyID: p.CompanyID,
					ProjectID: project,
					ID:        prior.ID,
					Status:    "superseded",
				},
			); e != nil {
				return e
			}
		}
		var beforeTheme *string
		if in.Theme != nil {
			if in.Sequence != draft.HeadSeq {
				return ErrConflict
			}
			metadata, err := q.GetProjectPageMetadata(
				ctx,
				generated.GetProjectPageMetadataParams{
					CompanyID: p.CompanyID,
					ProjectID: nullable(&project),
					ID:        pageID,
				},
			)
			if err != nil {
				return err
			}
			beforeTheme = &metadata.Theme
		}

		var root wiki.BlockNode
		if err = json.Unmarshal(draft.Content, &root); err != nil {
			return err
		}
		for i := range in.Operations {
			op := &in.Operations[i]
			if op.Content != nil {
				if err = resolvePageReferences(ctx, q, p, project, op.Content); err != nil {
					return err
				}
			}
			if op.Type == "insert" {
				if op.Content == nil {
					return ErrInvalid
				}
				if containsProtection(*op.Content) {
					return ErrForbidden
				}
				parentType := "doc"
				if op.ParentID != "" {
					parent := wiki.FindBlock(root, op.ParentID)
					if parent == nil {
						return ErrConflict
					}
					parentType = parent.Type
				}
				if err = wiki.ValidateBlockInsert(*op.Content, parentType); err != nil {
					if errors.Is(err, wiki.ErrPageInvalidInput) {
						return ErrPageInvalidInsert
					}
					return err
				}
				// Allocate on the actual subtree, not an encoded replacement document.
				wrapped := wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{*op.Content}}
				if err = wiki.AllocateBlockTreeInTx(
					ctx,
					q,
					p.CompanyID,
					v.WikiPageID.UUID,
					&wrapped,
				); err != nil {
					return err
				}
				op.Content = &wrapped.Content[0]
			} else {
				target := wiki.FindBlock(root, op.BlockID)
				if target == nil || wiki.BlockHash(*target) != op.ExpectedHash {
					return ErrConflict
				}
				if op.Content != nil {
					if !sameProtection(*op.Content, *target) {
						return ErrForbidden
					}
				}
			}
		}
		_, edited, err := wiki.ApplyBlockOperations(draft.State, in.Operations)
		if err != nil {
			return err
		}
		if err = validateBlockReferences(ctx, q, p, project, edited); err != nil {
			return err
		}
		result, err = insertPageProposal(ctx, q, pageProposal{
			CompanyID: p.CompanyID, ProjectID: project, PageID: v.WikiPageID.UUID,
			RunID: run, ActorUserID: p.UserID, RequestKey: in.RequestKey,
			RequestHash: requestHash, Summary: in.Summary, Operations: in.Operations,
			BeforeContent: draft.Content, Sequence: draft.HeadSeq,
			BeforeTheme: beforeTheme, AfterTheme: in.Theme,
		})
		if err != nil {
			return err
		}
		if in.SupersedesID != nil {
			if err = q.SetPageChangeSupersedes(
				ctx,
				generated.SetPageChangeSupersedesParams{
					CompanyID:    p.CompanyID,
					ID:           result.ID,
					SupersedesID: nullable(in.SupersedesID),
				},
			); err != nil {
				return err
			}
		}
		if in.SupersedesID != nil {
			if err = q.AdvanceArtifactCreationProposal(
				ctx,
				generated.AdvanceArtifactCreationProposalParams{
					CompanyID:          p.CompanyID,
					CreationChangeID:   nullable(in.SupersedesID),
					CreationChangeID_2: nullable(&result.ID),
				},
			); err != nil {
				return err
			}
		}

		sequence, err := q.AdvanceCanvasHead(
			ctx,
			generated.AdvanceCanvasHeadParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		notification, err = changeEvent(ctx, q, p, project, sequence, uuid.Nil)
		return err
	})
	if err != nil {
		return PageEditResult{}, mapWrite(err)
	}
	if notification != nil {
		s.bus.Publish(ctx, Topic, *notification)
	}
	return result, nil
}

func sameProtection(left, right wiki.BlockNode) bool {
	flags := func(root wiki.BlockNode) map[string]bool {
		result := map[string]bool{}
		var walk func(wiki.BlockNode)
		walk = func(node wiki.BlockNode) {
			if node.Attrs["agentProtected"] == true {
				result[fmt.Sprint(node.Attrs["blockId"])] = true
			}
			for _, child := range node.Content {
				walk(child)
			}
		}
		walk(root)
		return result
	}
	a, b := flags(left), flags(right)
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

type pageDraftEdit struct {
	principal   auth.UserPrincipal
	page        uuid.UUID
	sequence    int64
	state       []byte
	beforeState []byte
	root        wiki.BlockNode
	structural  bool
}

func savePageEdit(ctx context.Context, q *generated.Queries, in pageDraftEdit) error {
	p, page, seq, state, root := in.principal, in.page, in.sequence, in.state, in.root
	ids := wiki.BlockIDs(root)
	count, err := q.ValidateWikiBlockIDs(
		ctx,
		generated.ValidateWikiBlockIDsParams{CompanyID: p.CompanyID, PageID: page, Ids: ids},
	)
	if err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return ErrInvalid
	}
	content, err := json.Marshal(root)
	if err != nil {
		return err
	}
	rows, err := q.UpdateWikiBlockDraft(
		ctx,
		generated.UpdateWikiBlockDraftParams{
			CompanyID:  p.CompanyID,
			PageID:     page,
			State:      state,
			Content:    content,
			Markdown:   wiki.BlocksMarkdown(root),
			HeadSeq:    seq,
			Structural: in.structural,
		},
	)
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrConflict
	}
	if err = wiki.RecordDocumentChangeInTx(ctx, q, wiki.DocumentChangeInput{
		CompanyID: p.CompanyID, PageID: page, ActorType: "user", ActorID: p.UserID,
		ChangeType: "edit", Sequence: seq + 1, Before: in.beforeState, State: state,
	}); err != nil {
		return err
	}
	return reconcilePageComments(ctx, q, pageCommentEdit{
		companyID: p.CompanyID, pageID: page, before: in.beforeState, after: state,
	})
}

type PageChangeDetail struct {
	Fingerprint string             `json:"fingerprint"`
	Suggestions []PageSuggestion   `json:"suggestions"`
	Reviews     []PageChangeReview `json:"reviews"`
	PageChange
	Before      json.RawMessage `json:"before"`
	After       json.RawMessage `json:"after"`
	BeforeTheme *string         `json:"beforeTheme,omitempty"`
	AfterTheme  *string         `json:"afterTheme,omitempty"`
}

type PageChangeReview struct {
	ID          uuid.UUID `json:"id"`
	ActorUserID uuid.UUID `json:"actorUserId"`
	Action      string    `json:"action"`
	Indexes     []int     `json:"indexes"`
	Feedback    string    `json:"feedback"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (s *DefaultPageService) PageChangeDetail(
	ctx context.Context,
	p auth.UserPrincipal,
	project, pageID, id uuid.UUID,
) (PageChangeDetail, error) {
	var result PageChangeDetail
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		if _, err := editableProjectPage(ctx, q, p, project, pageID); err != nil {
			return err
		}
		row, err := q.GetProjectPageChange(
			ctx,
			generated.GetProjectPageChangeParams{CompanyID: p.CompanyID, ProjectID: project, ID: id},
		)
		if err != nil {
			return lookup(err)
		}
		if row.PageID != pageID {
			return ErrNotFound
		}
		var root wiki.BlockNode
		if err = json.Unmarshal(row.BeforeContent, &root); err != nil {
			return err
		}
		var operations []wiki.BlockOperation
		if err = json.Unmarshal(row.Operations, &operations); err != nil {
			return err
		}
		state, err := wiki.EncodeBlocks(root)
		if err != nil {
			return err
		}
		_, after, err := wiki.ApplyBlockOperations(state, operations)
		if err != nil {
			return err
		}
		before := root
		for _, document := range []*wiki.BlockNode{&before, &after} {
			if err = redactUnavailableReferences(ctx, q, p, project, document); err != nil {
				return err
			}
		}
		pageTitle, err := q.GetProjectPageChangeTitle(
			ctx,
			generated.GetProjectPageChangeTitleParams{
				CompanyID: p.CompanyID,
				ProjectID: nullable(&project),
				ID:        row.PageID,
			},
		)
		if err != nil {
			return err
		}
		result = PageChangeDetail{
			PageChange: PageChange{
				AgentID:   outputSource(row.ActorAgentID),
				ID:        row.ID,
				PageID:    row.PageID,
				PageTitle: pageTitle,
				RunID:     outputSource(row.RunID),
				Summary:   row.Summary,
				Status:    row.Status,
				CreatedAt: row.CreatedAt.Time,
			},
		}
		result.AgentName, err = q.PageChangeAuthorName(
			ctx,
			generated.PageChangeAuthorNameParams{CompanyID: p.CompanyID, ID: row.ID},
		)
		if err != nil {
			return err
		}
		if row.BeforeTheme.Valid {
			result.BeforeTheme = &row.BeforeTheme.String
			result.AfterTheme = &row.AfterTheme.String
		}
		draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, pageID)
		if err != nil {
			return err
		}
		var current wiki.BlockNode
		if err = json.Unmarshal(draft.Content, &current); err != nil {
			return err
		}
		peers, err := q.ListPageSuggestionChanges(
			ctx,
			generated.ListPageSuggestionChangesParams{CompanyID: p.CompanyID, PageID: pageID},
		)
		if err != nil {
			return err
		}
		metadata, err := q.GetProjectPageMetadata(
			ctx,
			generated.GetProjectPageMetadataParams{
				CompanyID: p.CompanyID,
				ProjectID: nullable(&project),
				ID:        pageID,
			},
		)
		if err != nil {
			return err
		}
		review, err := pageSuggestionState(row, current, metadata.Theme, peers)
		if err != nil {
			return err
		}
		result.Fingerprint, result.Suggestions = review.fingerprint, review.suggestions
		for i := range result.Suggestions {
			for _, document := range []*wiki.BlockNode{result.Suggestions[i].Before, result.Suggestions[i].After} {
				if document != nil {
					if err = redactUnavailableReferences(ctx, q, p, project, document); err != nil {
						return err
					}
				}
			}
		}
		reviews, err := q.ListPageSuggestionReviews(
			ctx,
			generated.ListPageSuggestionReviewsParams{CompanyID: p.CompanyID, ChangeID: id},
		)
		if err != nil {
			return err
		}
		result.Reviews = []PageChangeReview{}
		for _, entry := range reviews {
			if entry.Feedback != "" {
				result.Feedback = entry.Feedback
			}
			indexes := []int{}
			if err = json.Unmarshal(entry.Selection, &indexes); err != nil {
				return err
			}
			if indexes == nil {
				indexes = []int{}
			}
			result.Reviews = append(
				result.Reviews,
				PageChangeReview{
					ID:          entry.ID,
					ActorUserID: entry.ActorUserID,
					Action:      entry.Action,
					Indexes:     indexes,
					Feedback:    entry.Feedback,
					CreatedAt:   entry.CreatedAt.Time,
				},
			)
		}
		result.Before, err = json.Marshal(before)
		if err != nil {
			return err
		}
		result.After, err = json.Marshal(after)
		return err
	})
	return result, mapWrite(err)
}

func redactUnavailableReferences(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	root *wiki.BlockNode,
) error {
	denied := map[uuid.UUID]bool{}
	var walk func(wiki.BlockNode) error
	walk = func(node wiki.BlockNode) error {
		ids := sourceVersionIDs(node)
		if node.Type == "artifactReference" {
			if id, err := uuid.Parse(fmt.Sprint(node.Attrs["versionId"])); err == nil {
				ids = append(ids, id)
			}
		}
		for _, id := range ids {
			if err := canVersion(ctx, q, p, project, id); errors.Is(err, ErrNotFound) {
				denied[id] = true
			} else if err != nil {
				return err
			}
		}
		for _, child := range node.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(*root); err != nil {
		return err
	}
	redactPresentation(root, denied)
	return nil
}

// PageSource exposes a bounded exact source to the Run that is assembling
// a Page. It grants no source-edit authority.
type blockPlacement struct{ parent, previous, next string }

func locatePageBlock(root wiki.BlockNode, id string) (blockPlacement, bool) {
	for i, child := range root.Content {
		if child.Attrs["blockId"] == id {
			placement := blockPlacement{}
			placement.parent, _ = root.Attrs["blockId"].(string)
			if i > 0 {
				placement.previous, _ = root.Content[i-1].Attrs["blockId"].(string)
			}
			if i+1 < len(root.Content) {
				placement.next, _ = root.Content[i+1].Attrs["blockId"].(string)
			}
			return placement, true
		}
		if placement, ok := locatePageBlock(child, id); ok {
			return placement, true
		}
	}
	return blockPlacement{}, false
}

// operationProtected checks both the existing subtree and its destination.
// An insertion inherits the preceding section at its actual sibling position.
func operationProtected(root wiki.BlockNode, op wiki.BlockOperation) bool {
	if blockProtected(root, op.BlockID) || blockProtected(root, op.ParentID) {
		return true
	}
	if op.Type != "insert" && op.Type != "move" {
		return false
	}
	if op.AfterID != "" {
		return blockProtected(root, op.AfterID)
	}
	if op.BeforeID != "" {
		placement, ok := locatePageBlock(root, op.BeforeID)
		return ok && blockProtected(root, placement.previous)
	}
	parent := &root
	if op.ParentID != "" {
		parent = wiki.FindBlock(root, op.ParentID)
	}
	return parent != nil && len(parent.Content) > 0 &&
		blockProtected(root, fmt.Sprint(parent.Content[len(parent.Content)-1].Attrs["blockId"]))
}

func undoPageOperations(
	state []byte,
	before wiki.BlockNode,
	ops []wiki.BlockOperation,
) ([]byte, wiki.BlockNode, error) {
	_, root, _, err := wiki.MergeBlocks(state, nil)
	if err != nil {
		return nil, root, err
	}
	for i := len(ops) - 1; i >= 0; i-- {
		_, root, _, err = wiki.MergeBlocks(state, nil)
		if err != nil {
			return nil, root, err
		}
		op := ops[i]
		inverse := wiki.BlockOperation{BlockID: op.BlockID}
		switch op.Type {
		case "insert":
			id, _ := op.Content.Attrs["blockId"].(string)
			inverse = wiki.BlockOperation{
				Type:         "remove",
				BlockID:      id,
				ExpectedHash: wiki.BlockHash(*op.Content),
			}
		case "update":
			old := wiki.FindBlock(before, op.BlockID)
			if old == nil || op.Content == nil {
				return nil, root, ErrConflict
			}
			inverse.Type, inverse.ExpectedHash, inverse.Content = "update", wiki.BlockHash(*op.Content), old
		case "move", "remove":
			old := wiki.FindBlock(before, op.BlockID)
			original, ok := locatePageBlock(before, op.BlockID)
			if old == nil || !ok {
				return nil, root, ErrConflict
			}
			inverse.ParentID = original.parent
			switch {
			case original.next != "" && wiki.FindBlock(root, original.next) != nil:
				inverse.BeforeID = original.next
			case original.previous != "" && wiki.FindBlock(root, original.previous) != nil:
				inverse.AfterID = original.previous
			default:
				parent := &root
				if original.parent != "" {
					parent = wiki.FindBlock(root, original.parent)
				}
				if parent == nil {
					return nil, root, ErrConflict
				}
				if len(parent.Content) > 0 {
					inverse.BeforeID, _ = parent.Content[0].Attrs["blockId"].(string)
				}
			}
			if op.Type == "remove" {
				if wiki.FindBlock(root, op.BlockID) != nil {
					return nil, root, ErrConflict
				}
				inverse.Type, inverse.BlockID, inverse.Content = "insert", "", old
			} else {
				current, ok := locatePageBlock(root, op.BlockID)
				if !ok || current.parent != op.ParentID ||
					op.AfterID != "" && current.previous != op.AfterID ||
					op.BeforeID != "" && current.next != op.BeforeID ||
					op.AfterID == "" && op.BeforeID == "" && current.next != "" {
					return nil, root, ErrConflict
				}
				inverse.Type, inverse.ExpectedHash = "move", op.ExpectedHash
			}
		default:
			return nil, root, ErrInvalid
		}
		state, root, err = wiki.ApplyBlockOperations(state, []wiki.BlockOperation{inverse})
		if err != nil {
			return nil, root, err
		}
	}
	return state, root, nil
}
