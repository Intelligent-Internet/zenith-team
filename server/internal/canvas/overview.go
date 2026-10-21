package canvas

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type OverviewTask struct {
	ID             uuid.UUID  `json:"id"`
	Title          string     `json:"title"`
	Status         string     `json:"status"`
	AssigneeUserID uuid.UUID  `json:"assigneeUserId"`
	AssigneeName   string     `json:"assigneeName"`
	AgentName      string     `json:"agentName,omitempty"`
	AgentID        *uuid.UUID `json:"agentId"`
	Revision       int64      `json:"revision"`
}

type PageChange struct {
	AgentID   *uuid.UUID `json:"agentId,omitempty"`
	AgentName string     `json:"agentName,omitempty"`
	Feedback  string     `json:"feedback,omitempty"`
	ID        uuid.UUID  `json:"id"`
	PageID    uuid.UUID  `json:"pageId"`
	PageTitle string     `json:"pageTitle"`
	RunID     *uuid.UUID `json:"runId"`
	Summary   string     `json:"summary"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
}
type Overview struct {
	PageID  *uuid.UUID   `json:"pageId"`
	Title   string       `json:"title"`
	CanEdit bool         `json:"canEdit"`
	Changes []PageChange `json:"changes"`
}
type OverviewInput struct {
	RequestKey uuid.UUID  `json:"requestKey"       binding:"required"`
	Action     string     `json:"action"           binding:"required,oneof=create designate"`
	PageID     *uuid.UUID `json:"pageId,omitempty"`
}
type PageEditInput struct {
	SupersedesID *uuid.UUID            `json:"supersedesId,omitempty"`
	RequestKey   uuid.UUID             `json:"requestKey"             binding:"required"`
	Sequence     int64                 `json:"sequence"               binding:"min=0"`
	Summary      string                `json:"summary"                binding:"required,max=500"`
	Operations   []wiki.BlockOperation `json:"operations,omitempty"   binding:"max=32,dive"`
	Theme        *string               `json:"theme,omitempty"`
}
type PageEditResult struct {
	ID       uuid.UUID `json:"id"`
	Status   string    `json:"status"`
	Sequence int64     `json:"sequence"`
}

func (s *DefaultPageService) Overview(ctx context.Context, p auth.UserPrincipal, project uuid.UUID) (Overview, error) {
	result := Overview{Changes: []PageChange{}}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		access, err := authorize(ctx, q, p, project, false)
		if err != nil {
			return err
		}
		result.CanEdit = access.Can(auth.ProjectPermissionTasksWrite)
		version, err := q.GetProjectOverviewVersion(
			ctx,
			generated.GetProjectOverviewVersionParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil {
			result.PageID = pointer(version.WikiPageID.UUID)
			metadata, err := q.GetProjectPageMetadata(
				ctx,
				generated.GetProjectPageMetadataParams{
					CompanyID: p.CompanyID,
					ProjectID: nullable(&project),
					ID:        *result.PageID,
				},
			)
			if err != nil {
				return err
			}
			result.Title = metadata.Title
		}
		result.Changes, err = projectPageChanges(ctx, q, p.CompanyID, project, nil)
		return err
	})
	return result, mapWrite(err)
}

func projectPageChanges(
	ctx context.Context,
	q *generated.Queries,
	company, project uuid.UUID,
	page *uuid.UUID,
) ([]PageChange, error) {
	rows, err := q.ListProjectPageChanges(
		ctx,
		generated.ListProjectPageChangesParams{CompanyID: company, ProjectID: project, PageID: nullable(page)},
	)
	if err != nil {
		return nil, err
	}
	changes := make([]PageChange, 0, len(rows))
	for _, change := range rows {
		changes = append(changes, PageChange{
			AgentID: outputSource(change.ActorAgentID), AgentName: change.AgentName,
			ID:        change.ID,
			Feedback:  change.Feedback,
			PageID:    change.PageID,
			PageTitle: change.PageTitle,
			RunID:     outputSource(change.RunID),
			Summary:   change.Summary,
			Status:    change.Status,
			CreatedAt: change.CreatedAt.Time,
		})
	}
	return changes, nil
}

func (s *DefaultPageService) UpdateOverview(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	in OverviewInput,
) (Overview, error) {
	if in.RequestKey == uuid.Nil {
		return Overview{}, ErrInvalid
	}
	raw, _ := json.Marshal(in)
	hash := sha256.Sum256(raw)
	var sequence int64
	var pending *events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		_, err := authorize(ctx, q, p, project, true)
		if err != nil {
			return err
		}

		_, err = q.EnsureProjectCanvas(
			ctx,
			generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		receipt, err := q.GetCanvasReceipt(
			ctx,
			generated.GetCanvasReceiptParams{
				ProjectID:   project,
				ActorUserID: p.UserID,
				RequestKey:  in.RequestKey,
			},
		)
		if err == nil {
			if string(receipt.RequestHash) != string(hash[:]) {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch in.Action {
		case "create":
			projectRow, err := q.GetProjectForActiveMember(
				ctx,
				generated.GetProjectForActiveMemberParams{
					ManagementRoles: []string{},
					CompanyID:       p.CompanyID,
					ProjectID:       project,
					ActorUserID:     p.UserID,
				},
			)
			if err != nil {
				return lookup(err)
			}
			if err = EnsureOverviewInTx(ctx, q, p, projectRow.Project); err != nil {
				return err
			}
		case "designate":
			id, err := requireID(in.PageID)
			if err != nil {
				return err
			}
			v, err := q.GetProjectPageVersion(
				ctx,
				generated.GetProjectPageVersionParams{
					CompanyID: p.CompanyID,
					ProjectID: project,
					ID:        id,
				},
			)
			if err != nil {
				return err
			}
			if !v.WikiPageID.Valid || v.AccessOutputID.Valid {
				return ErrInvalid
			}
			pendingCreation, err := q.PagePendingCreation(
				ctx,
				generated.PagePendingCreationParams{CompanyID: p.CompanyID, WikiPageID: nullable(&id)},
			)
			if err != nil {
				return err
			}
			if pendingCreation {
				return ErrConflict
			}
			if err = q.SetProjectOverview(
				ctx,
				generated.SetProjectOverviewParams{
					CompanyID:      p.CompanyID,
					ProjectID:      project,
					OverviewPageID: nullable(&id),
				},
			); err != nil {
				return err
			}
		default:
			return ErrInvalid
		}
		sequence, err = q.AdvanceCanvasHead(
			ctx,
			generated.AdvanceCanvasHeadParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		pending, err = changeEvent(ctx, q, p, project, sequence, uuid.Nil)
		if err != nil {
			return err
		}
		return q.InsertCanvasReceipt(
			ctx,
			generated.InsertCanvasReceiptParams{
				CompanyID:   p.CompanyID,
				ProjectID:   project,
				ActorUserID: p.UserID,
				RequestKey:  in.RequestKey,
				RequestHash: hash[:],
				Seq:         sequence,
				Changes:     json.RawMessage(`{}`),
				Result:      json.RawMessage(`{}`),
			},
		)
	})
	if err != nil {
		return Overview{}, mapWrite(err)
	}
	if pending != nil {
		s.bus.Publish(ctx, Topic, *pending)
	}
	return s.Overview(ctx, p, project)
}

// EnsureOverviewInTx creates the one native Overview Page while its caller owns
// Project creation or an admitted Task-start transaction. It preserves an existing Page.
func EnsureOverviewInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project generated.Project,
) error {
	_, err := ensureOverviewInTx(ctx, q, p, project)
	return err
}

func ensureOverviewInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project generated.Project,
) (bool, error) {
	board, err := q.EnsureProjectCanvas(
		ctx,
		generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project.ID},
	)
	if err != nil || board.OverviewPageID.Valid {
		return false, err
	}
	root := wiki.BlocksFromMarkdown(
		"## Outcome\n\n" + project.Description + "\n\n## Progress and decisions\n\n## Highlights\n",
	)
	page, revision, err := wiki.CreateCanvasPageInTx(
		ctx,
		q,
		wiki.CanvasPageInput{
			Principal: p,
			ProjectID: project.ID,
			Title:     project.Title,
			Body:      wiki.BlocksMarkdown(root),
			Content:   &root,
		},
	)
	if err != nil {
		return false, err
	}
	artifact, err := q.CreateCanvasArtifact(
		ctx,
		generated.CreateCanvasArtifactParams{
			CompanyID:       p.CompanyID,
			ProjectID:       project.ID,
			Title:           project.Title,
			CreatedByUserID: p.UserID,
		},
	)
	if err != nil {
		return false, err
	}
	number, err := q.AllocateCanvasVersion(ctx, artifact.ID)
	if err != nil {
		return false, err
	}
	_, err = q.CreateCanvasVersion(
		ctx,
		generated.CreateCanvasVersionParams{
			CompanyID:       p.CompanyID,
			ProjectID:       project.ID,
			ArtifactID:      artifact.ID,
			Number:          number,
			SourceType:      "wiki",
			WikiPageID:      nullable(&page),
			WikiRevisionID:  nullable(&revision),
			CreatedByUserID: p.UserID,
		},
	)
	if err != nil {
		return false, err
	}
	err = q.SetProjectOverview(
		ctx,
		generated.SetProjectOverviewParams{
			CompanyID:      p.CompanyID,
			ProjectID:      project.ID,
			OverviewPageID: nullable(&page),
		},
	)
	return err == nil, err
}

func blockProtected(root wiki.BlockNode, id string) bool {
	if id == "" {
		return false
	}
	var walk func(wiki.BlockNode, bool) bool
	walk = func(node wiki.BlockNode, protected bool) bool {
		protected = protected || node.Attrs["agentProtected"] == true
		if node.Attrs["blockId"] == id {
			return protected || containsProtection(node)
		}
		sectionLevel := 0
		for _, child := range node.Content {
			if child.Type == "heading" {
				level := headingLevel(child)
				if sectionLevel > 0 && level <= sectionLevel {
					sectionLevel = 0
				}
				if child.Attrs["agentProtected"] == true && sectionLevel == 0 {
					sectionLevel = level
				}
			}
			if walk(child, protected || sectionLevel > 0) {
				return true
			}
		}
		return false
	}
	return walk(root, false)
}

func headingLevel(node wiki.BlockNode) int {
	var level int
	switch value := node.Attrs["level"].(type) {
	case int:
		level = value
	case float64:
		level = int(value)
	case float32:
		level = int(value)
	case int64:
		level = int(value)
	}
	return level
}

func containsProtection(node wiki.BlockNode) bool {
	if node.Attrs["agentProtected"] == true {
		return true
	}
	for _, child := range node.Content {
		if containsProtection(child) {
			return true
		}
	}
	return false
}

// forgetHumanChanges makes a human edit end direct Agent ownership, even if a
// later edit restores the same words. Protection is enforced separately.
func forgetHumanChanges(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	page uuid.UUID,
	root wiki.BlockNode,
) error {
	managed, err := q.ListPageManagedBlocks(
		ctx,
		generated.ListPageManagedBlocksParams{CompanyID: p.CompanyID, PageID: page},
	)
	if err != nil {
		return err
	}
	for _, block := range managed {
		node := wiki.FindBlock(root, block.BlockID.String())
		if node == nil || wiki.BlockHash(*node) != block.ContentHash {
			if err = q.ForgetPageManagedBlock(
				ctx,
				generated.ForgetPageManagedBlockParams{
					CompanyID: p.CompanyID,
					PageID:    page,
					BlockID:   block.BlockID,
				},
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizePageEdit(in *PageEditInput) error {
	if in.Theme != nil && (!validPageTheme(*in.Theme) || len(in.Operations) > 0) {
		return ErrInvalid
	}
	in.Summary = strings.TrimSpace(in.Summary)
	if in.RequestKey == uuid.Nil || in.Sequence < 0 || in.Summary == "" || len(in.Summary) > 500 ||
		(len(in.Operations) < 1 && in.Theme == nil) ||
		len(in.Operations) > 32 {
		return ErrInvalid
	}
	return nil
}

type PageSourceState struct {
	VersionID        uuid.UUID  `json:"versionId"`
	ArtifactID       *uuid.UUID `json:"artifactId"`
	CurrentVersionID *uuid.UUID `json:"currentVersionId"`
	Title            string     `json:"title"`
	Status           string     `json:"status"`
}

func declaredSources(root wiki.BlockNode) []uuid.UUID {
	ids := []uuid.UUID{}
	seen := map[uuid.UUID]bool{}
	var walk func(wiki.BlockNode)
	walk = func(node wiki.BlockNode) {
		sources := sourceVersionIDs(node)
		if node.Type == "artifactReference" {
			if raw, ok := node.Attrs["versionId"].(string); ok {
				if id, err := uuid.Parse(raw); err == nil {
					sources = append(sources, id)
				}
			}
		}
		for _, id := range sources {
			if !seen[id] && len(ids) < 256 {
				ids = append(ids, id)
				seen[id] = true
			}
		}
		for _, child := range node.Content {
			walk(child)
		}
	}
	walk(root)
	return ids
}

func pageSourceStates(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	root wiki.BlockNode,
) ([]PageSourceState, error) {
	ids := declaredSources(root)
	result := []PageSourceState{}
	if len(ids) == 0 {
		return result, nil
	}
	rows, err := q.ListPageSourceVersions(
		ctx,
		generated.ListPageSourceVersionsParams{
			CompanyID:   p.CompanyID,
			ProjectID:   project,
			VersionIds:  ids,
			ActorUserID: p.UserID,
		},
	)
	if err != nil {
		return nil, err
	}
	// Live Page references follow their saved draft. Only pinned evidence and
	// authored source annotations need review when a newer version appears.
	exact := map[uuid.UUID]bool{}
	var collectExact func(wiki.BlockNode)
	collectExact = func(node wiki.BlockNode) {
		for _, id := range sourceVersionIDs(node) {
			exact[id] = true
		}
		if node.Type == "artifactReference" && node.Attrs["format"] != "live" {
			if id, err := uuid.Parse(fmt.Sprint(node.Attrs["versionId"])); err == nil {
				exact[id] = true
			}
		}
		for _, child := range node.Content {
			collectExact(child)
		}
	}
	collectExact(root)
	states := map[uuid.UUID]PageSourceState{}
	for _, row := range rows {
		if err = canVersion(ctx, q, p, project, row.VersionID); errors.Is(err, ErrNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		status := "current"
		if row.LatestVersionID != row.VersionID && exact[row.VersionID] {
			status = "needs_refresh"
		}
		states[row.VersionID] = PageSourceState{
			VersionID:        row.VersionID,
			ArtifactID:       pointer(row.ArtifactID),
			CurrentVersionID: pointer(row.LatestVersionID),
			Title:            row.Title,
			Status:           status,
		}
	}
	for _, id := range ids {
		value, ok := states[id]
		if !ok {
			value = PageSourceState{VersionID: id, Status: "unavailable"}
		}
		result = append(result, value)
	}
	return result, nil
}

// EnsureTaskOverviewInTx fills a missing overview when an admitted Task starts.
// The enqueue kernel owns the authorizer, Task generation, and transaction fences.
func EnsureTaskOverviewInTx(ctx context.Context, q *generated.Queries, run generated.Run) (*events.Event, error) {
	if !run.TaskID.Valid {
		return nil, nil
	}
	p := auth.UserPrincipal{CompanyID: run.CompanyID, UserID: run.ExecutionAuthorizedByUserID.UUID}
	identity, err := q.GetOverviewTaskRun(
		ctx,
		generated.GetOverviewTaskRunParams{CompanyID: p.CompanyID, ID: run.ID, AssigneeUserID: p.UserID},
	)
	if err != nil {
		return nil, lookup(err)
	}
	project, err := q.GetProjectForActiveMember(
		ctx,
		generated.GetProjectForActiveMemberParams{
			ManagementRoles: []string{},
			CompanyID:       p.CompanyID,
			ProjectID:       identity.ProjectID,
			ActorUserID:     p.UserID,
		},
	)
	if err != nil {
		return nil, lookup(err)
	}
	canWrite, err := auth.HoldsProjectPermission(
		ctx,
		q,
		p.CompanyID,
		identity.ProjectID,
		p.UserID,
		auth.ProjectPermissionTasksWrite,
	)
	if err != nil || !canWrite {
		return nil, err
	}
	created, err := ensureOverviewInTx(ctx, q, p, project.Project)
	if err != nil || !created {
		return nil, err
	}
	sequence, err := q.AdvanceCanvasHead(
		ctx,
		generated.AdvanceCanvasHeadParams{CompanyID: p.CompanyID, ProjectID: project.Project.ID},
	)
	if err != nil {
		return nil, err
	}
	return changeEvent(ctx, q, p, project.Project.ID, sequence, uuid.Nil)
}
