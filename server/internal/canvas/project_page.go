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
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type PageCreateInput struct {
	RequestKey uuid.UUID      `json:"requestKey"         binding:"required"`
	Title      string         `json:"title"              binding:"required,max=200"`
	Content    wiki.BlockNode `json:"content,omitempty"`
	Markdown   string         `json:"markdown,omitempty"`
	Theme      string         `json:"theme,omitempty"`
}

type PageCreateResult struct {
	ChangeID   *uuid.UUID `json:"changeId,omitempty"`
	VersionID  uuid.UUID  `json:"versionId"`
	ArtifactID uuid.UUID  `json:"artifactId"`
	PageID     uuid.UUID  `json:"pageId"`
	Title      string     `json:"title"`
	Sequence   int64      `json:"sequence"`
}

// editableProjectPage selects a native Page in the caller's Project.
// Imported outputs retain their immutable source and cannot be rewritten.
func editableProjectPage(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
) (generated.CanvasVersionSource, error) {
	source, err := q.GetProjectPageVersion(
		ctx,
		generated.GetProjectPageVersionParams{CompanyID: p.CompanyID, ProjectID: project, ID: pageID},
	)
	if err != nil {
		return source, lookup(err)
	}
	if !source.WikiPageID.Valid || source.AccessOutputID.Valid {
		return source, ErrInvalid
	}
	if _, err = q.LockCanvasArtifact(
		ctx,
		generated.LockCanvasArtifactParams{CompanyID: p.CompanyID, ProjectID: project, ID: source.ArtifactID},
	); err != nil {
		return source, lookup(err)
	}
	page, err := q.LockCanvasWikiPage(
		ctx,
		generated.LockCanvasWikiPageParams{CompanyID: p.CompanyID, ID: source.WikiPageID.UUID},
	)
	if err != nil {
		return source, lookup(err)
	}
	if page.Status != "active" {
		return source, ErrNotFound
	}
	return source, nil
}

func (s *DefaultPageService) CreatePageForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run uuid.UUID,
	in PageCreateInput,
) (PageCreateResult, error) {
	return s.createPage(ctx, p, uuid.Nil, &run, in)
}

func (s *DefaultPageService) CreatePage(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	in PageCreateInput,
) (PageCreateResult, error) {
	return s.createPage(ctx, p, project, nil, in)
}

func (s *DefaultPageService) createPage(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	run *uuid.UUID,
	in PageCreateInput,
) (PageCreateResult, error) {
	in.Title = strings.TrimSpace(in.Title)
	if in.Markdown != "" {
		if in.Content.Type != "" {
			return PageCreateResult{}, ErrInvalid
		}
		in.Content = wiki.BlocksFromMarkdown(in.Markdown)
	}
	if in.Theme == "" {
		in.Theme = "default"
	}
	if !validPageTheme(in.Theme) {
		return PageCreateResult{}, ErrInvalid
	}
	if in.RequestKey == uuid.Nil || in.Title == "" || len(in.Title) > 200 || containsProtection(in.Content) {
		return PageCreateResult{}, ErrInvalid
	}
	if err := wiki.ValidateBlockImport(in.Content); err != nil {
		return PageCreateResult{}, err
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return PageCreateResult{}, ErrInvalid
	}
	hash := sha256.Sum256(append([]byte("project_page_create:"+fmt.Sprint(run)), raw...))
	// Database identity allocation must not mutate a request reused for retry.
	var working PageCreateInput
	if err = json.Unmarshal(raw, &working); err != nil {
		return PageCreateResult{}, ErrInvalid
	}
	in = working
	var result PageCreateResult
	var pending *events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var taskID *uuid.UUID
		if run != nil {
			scope, task, err := admitPageRun(ctx, q, p, *run, true)
			if err != nil {
				return err
			}
			if task == uuid.Nil {
				return ErrForbidden
			}
			project = scope
			taskID = &task
		} else {
			if _, err := authorize(ctx, q, p, project, true); err != nil {
				return err
			}
		}
		if _, err = q.EnsureProjectCanvas(
			ctx,
			generated.EnsureProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
		); err != nil {
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
			return json.Unmarshal(receipt.Result, &result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		count, err := q.CountCanvasArtifacts(ctx, project)
		if err != nil {
			return err
		}
		if count >= 2000 {
			return ErrLimit
		}
		if err = resolvePageReferences(ctx, q, p, project, &in.Content); err != nil {
			return err
		}
		if err = validateBlockReferences(ctx, q, p, project, in.Content); err != nil {
			return err
		}
		proposedContent := in.Content
		if run != nil {
			in.Content = wiki.BlockNode{Type: "doc", Content: []wiki.BlockNode{{Type: "paragraph"}}}
		}
		frozen, err := frozenPageContent(ctx, q, p, project, in.Content)
		if err != nil {
			return err
		}
		var historyAgent *uuid.UUID
		if run != nil {
			origin, e := q.GetRun(ctx, *run)
			if e != nil {
				return e
			}
			historyAgent = &origin.AgentID
		}
		page, revision, err := wiki.CreateCanvasPageInTx(
			ctx,
			q,
			wiki.CanvasPageInput{
				HistoryAgentID:  historyAgent,
				Principal:       p,
				ProjectID:       project,
				Theme:           in.Theme,
				Title:           in.Title,
				Body:            wiki.BlocksMarkdown(in.Content),
				Content:         &in.Content,
				SnapshotContent: &frozen,
			},
		)
		if err != nil {
			return err
		}
		artifact, err := q.CreateCanvasArtifact(
			ctx,
			generated.CreateCanvasArtifactParams{
				CompanyID:       p.CompanyID,
				ProjectID:       project,
				Title:           in.Title,
				CreatedByUserID: p.UserID,
				TaskID:          nullable(taskID),
				SourceRunID:     nullable(run),
			},
		)
		if err != nil {
			return err
		}
		number, err := q.AllocateCanvasVersion(ctx, artifact.ID)
		if err != nil {
			return err
		}
		version, err := q.CreateCanvasVersion(
			ctx,
			generated.CreateCanvasVersionParams{
				CompanyID:       p.CompanyID,
				ProjectID:       project,
				ArtifactID:      artifact.ID,
				Number:          number,
				SourceType:      "wiki",
				WikiPageID:      nullable(&page),
				WikiRevisionID:  nullable(&revision),
				CreatedByUserID: p.UserID,
			},
		)
		if err != nil {
			return err
		}
		draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, page)
		if err != nil {
			return err
		}
		var root wiki.BlockNode
		if err = json.Unmarshal(draft.Content, &root); err != nil {
			return err
		}
		result = PageCreateResult{
			VersionID:  version.ID,
			ArtifactID: artifact.ID,
			PageID:     page,
			Title:      in.Title,
			Sequence:   draft.HeadSeq,
		}
		if run != nil {
			if err = wiki.AllocateBlockTreeInTx(ctx, q, p.CompanyID, page, &proposedContent); err != nil {
				return err
			}
			placeholder := root.Content[0]
			placeholderID, _ := placeholder.Attrs["blockId"].(string)
			operations := make([]wiki.BlockOperation, 0, len(proposedContent.Content)+1)
			for i := range proposedContent.Content {
				operations = append(
					operations,
					wiki.BlockOperation{
						Type:     "insert",
						BeforeID: placeholderID,
						Content:  &proposedContent.Content[i],
					},
				)
			}
			operations = append(
				operations,
				wiki.BlockOperation{
					Type:         "remove",
					BlockID:      placeholderID,
					ExpectedHash: wiki.BlockHash(placeholder),
				},
			)
			ops, _ := json.Marshal(operations)
			changeResult, _ := json.Marshal(PageEditResult{Status: "proposed", Sequence: draft.HeadSeq})
			change, e := q.InsertProjectPageChange(
				ctx,
				generated.InsertProjectPageChangeParams{
					CompanyID:     p.CompanyID,
					ProjectID:     project,
					PageID:        page,
					RunID:         nullable(run),
					ActorUserID:   p.UserID,
					RequestKey:    in.RequestKey,
					RequestHash:   fmt.Sprintf("%x", hash[:]),
					Operations:    ops,
					BeforeContent: draft.Content,
					Result:        changeResult,
					Summary:       "Create " + in.Title,
					Status:        "proposed",
				},
			)
			if e != nil {
				return e
			}
			if e = q.SetArtifactCreationProposal(
				ctx,
				generated.SetArtifactCreationProposalParams{
					CompanyID:        p.CompanyID,
					ID:               artifact.ID,
					CreationChangeID: nullable(&change.ID),
				},
			); e != nil {
				return e
			}
			result.ChangeID = &change.ID
		}
		sequence, err := q.AdvanceCanvasHead(
			ctx,
			generated.AdvanceCanvasHeadParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err = q.InsertCanvasReceipt(
			ctx,
			generated.InsertCanvasReceiptParams{
				CompanyID:   p.CompanyID,
				ProjectID:   project,
				ActorUserID: p.UserID,
				RequestKey:  in.RequestKey,
				RequestHash: hash[:],
				Seq:         sequence,
				Changes:     json.RawMessage(`{}`),
				Result:      encoded,
			},
		); err != nil {
			return err
		}
		pending, err = changeEvent(ctx, q, p, project, sequence, uuid.Nil)
		return err
	})
	if err != nil {
		return PageCreateResult{}, mapWrite(err)
	}
	if pending != nil {
		s.bus.Publish(ctx, Topic, *pending)
	}
	return result, nil
}

func resolvePageReferences(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	node *wiki.BlockNode,
) error {
	if node.Type == "artifactReference" {
		if raw, _ := node.Attrs["versionId"].(string); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				return ErrInvalid
			}
			ref, err := q.GetCanvasReference(
				ctx,
				generated.GetCanvasReferenceParams{CompanyID: p.CompanyID, ProjectID: project, ID: id},
			)
			if err != nil {
				return lookup(err)
			}
			if existing, _ := node.Attrs["artifactId"].(string); existing != "" &&
				existing != ref.ArtifactID.String() {
				return ErrInvalid
			}
			node.Attrs["artifactId"] = ref.ArtifactID.String()
		}
	}
	for i := range node.Content {
		if err := resolvePageReferences(ctx, q, p, project, &node.Content[i]); err != nil {
			return err
		}
	}
	return nil
}

type PageMetadataInput struct {
	Sequence int64   `json:"sequence"        binding:"min=0"`
	Title    *string `json:"title,omitempty" binding:"omitempty,max=200"`
	Theme    *string `json:"theme,omitempty" binding:"omitempty,oneof=default editorial compact"`
}

func validPageTheme(theme string) bool {
	return theme == "default" || theme == "editorial" || theme == "compact"
}

func (s *DefaultPageService) Page(ctx context.Context, p auth.UserPrincipal, project, pageID uuid.UUID) (Page, error) {
	var result Page
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		access, err := authorize(ctx, q, p, project, false)
		if err != nil {
			return err
		}
		result, err = readPage(ctx, q, p, project, pageID, access.Can(auth.ProjectPermissionTasksWrite))
		return err
	})
	return result, mapWrite(err)
}

type PageChangeFeed struct {
	Pending    []PageChange `json:"pending"`
	History    []PageChange `json:"history"`
	NextCursor *string      `json:"nextCursor"`
}

func (s *DefaultPageService) PageChanges(
	ctx context.Context,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
	cursor string,
) (PageChangeFeed, error) {
	result := PageChangeFeed{Pending: []PageChange{}, History: []PageChange{}}
	var beforeTime pgtype.Timestamptz
	var beforeID uuid.NullUUID
	if cursor != "" {
		parts := strings.Split(cursor, "|")
		if len(parts) != 2 {
			return result, ErrInvalid
		}
		stamp, err := time.Parse(time.RFC3339Nano, parts[0])
		if err != nil {
			return result, ErrInvalid
		}
		id, err := uuid.Parse(parts[1])
		if err != nil {
			return result, ErrInvalid
		}
		beforeTime = pgtype.Timestamptz{Time: stamp, Valid: true}
		beforeID = nullable(&id)
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		if _, err := editableProjectPage(ctx, q, p, project, pageID); err != nil {
			return err
		}
		pending, err := q.ListPendingPageChanges(
			ctx,
			generated.ListPendingPageChangesParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				PageID:    pageID,
			},
		)
		if err != nil {
			return err
		}
		for _, r := range pending {
			result.Pending = append(
				result.Pending,
				PageChange{
					AgentID: outputSource(
						r.ActorAgentID,
					),
					AgentName: r.AgentName,
					Feedback:  r.Feedback,
					ID:        r.ID,
					PageID:    r.PageID,
					PageTitle: r.PageTitle,
					RunID:     outputSource(r.RunID),
					Summary:   r.Summary,
					Status:    r.Status,
					CreatedAt: r.CreatedAt.Time,
				},
			)
		}
		rows, err := q.ListPageChangeHistory(
			ctx,
			generated.ListPageChangeHistoryParams{
				CompanyID:  p.CompanyID,
				ProjectID:  project,
				PageID:     pageID,
				BeforeTime: beforeTime,
				BeforeID:   beforeID,
			},
		)
		if err != nil {
			return err
		}
		if len(rows) > 50 {
			rows = rows[:50]
			last := rows[len(rows)-1]
			next := last.CreatedAt.Time.Format(time.RFC3339Nano) + "|" + last.ID.String()
			result.NextCursor = &next
		}
		for _, r := range rows {
			result.History = append(
				result.History,
				PageChange{
					AgentID: outputSource(
						r.ActorAgentID,
					),
					AgentName: r.AgentName,
					Feedback:  r.Feedback,
					ID:        r.ID,
					PageID:    r.PageID,
					PageTitle: r.PageTitle,
					RunID:     outputSource(r.RunID),
					Summary:   r.Summary,
					Status:    r.Status,
					CreatedAt: r.CreatedAt.Time,
				},
			)
		}
		return nil
	})
	return result, mapWrite(err)
}

func (s *DefaultPageService) UpdatePage(
	ctx context.Context,
	p auth.UserPrincipal,
	project, pageID uuid.UUID,
	in PageMetadataInput,
) (Page, error) {
	if in.Title == nil && in.Theme == nil {
		return Page{}, ErrInvalid
	}
	if in.Theme != nil && !validPageTheme(*in.Theme) {
		return Page{}, ErrInvalid
	}
	if in.Title != nil {
		title := strings.TrimSpace(*in.Title)
		if title == "" || len(title) > 200 {
			return Page{}, ErrInvalid
		}
		in.Title = &title
	}
	var sequence int64
	var pending, documentEvent *events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, true); err != nil {
			return err
		}
		v, err := editableProjectPage(ctx, q, p, project, pageID)
		if err != nil {
			return err
		}
		draft, err := wiki.BlockDraftInTx(ctx, q, p.CompanyID, pageID)
		if err != nil {
			return err
		}
		if draft.HeadSeq != in.Sequence {
			return ErrConflict
		}
		pendingCreation, err := q.PagePendingCreation(
			ctx,
			generated.PagePendingCreationParams{CompanyID: p.CompanyID, WikiPageID: nullable(&pageID)},
		)
		if err != nil {
			return err
		}
		if pendingCreation {
			return ErrConflict
		}
		if err = q.UpdateProjectPageMetadata(
			ctx,
			generated.UpdateProjectPageMetadataParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				PageID:    pageID,
				Title:     pageText(in.Title),
				Theme:     pageText(in.Theme),
			},
		); err != nil {
			return err
		}
		if in.Title != nil {
			if err = q.RenameCanvasArtifact(
				ctx,
				generated.RenameCanvasArtifactParams{ID: v.ArtifactID, Title: *in.Title},
			); err != nil {
				return err
			}
		}
		var root wiki.BlockNode
		if err = json.Unmarshal(draft.Content, &root); err != nil {
			return err
		}
		if err = savePageEdit(
			ctx,
			q,
			pageDraftEdit{
				principal: p,
				page:      pageID,
				sequence:  draft.HeadSeq,
				state:     draft.State,
				root:      root,
			},
		); err != nil {
			return err
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
		documentEvent, err = changeEvent(ctx, q, p, project, in.Sequence+1, pageID)
		return err
	})
	if err != nil {
		return Page{}, mapWrite(err)
	}
	s.bus.Publish(ctx, Topic, *pending, *documentEvent)
	return s.Page(ctx, p, project, pageID)
}

func pageText(value *string) pgtype.Text {
	if value == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *value, Valid: true}
}

func (s *DefaultPageService) Pages(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	taskID *uuid.UUID,
) ([]Artifact, error) {
	result := []Artifact{}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
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
		contributed := map[uuid.UUID]bool{}
		if taskID != nil {
			ids, err := q.ListProjectPageContributions(
				ctx,
				generated.ListProjectPageContributionsParams{
					CompanyID: p.CompanyID,
					ProjectID: project,
					TaskID:    nullable(taskID),
				},
			)
			if err != nil {
				return err
			}
			for _, id := range ids {
				contributed[id] = true
			}
		}
		for _, a := range artifacts {
			if a.PageID == nil || a.LatestVersion.OutputID != nil || a.LatestVersion.SourceOutputID != nil {
				continue
			}
			if taskID != nil && (a.TaskID == nil || *a.TaskID != *taskID) && !contributed[*a.PageID] {
				continue
			}
			result = append(result, a)
		}
		return nil
	})
	return result, mapWrite(err)
}
