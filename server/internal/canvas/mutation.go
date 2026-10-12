package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/wiki"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type mutation struct {
	Principal           auth.UserPrincipal
	ProjectID, CanvasID uuid.UUID
	Access              auth.ProjectAccess
	Input               Command
	Changes             *Changes
}

func normalizeCommand(c *Command) error {
	if err := validateCommentAgents(*c); err != nil {
		return err
	}
	if c.AttachmentID != nil && (*c.AttachmentID == uuid.Nil ||
		(c.Type != "add_artifact" && c.Type != "add_version") ||
		c.WikiPageID != nil || c.WikiRevisionID != nil || c.OutputID != nil) {
		return ErrInvalid
	}
	if c.PageID != nil && (*c.PageID == uuid.Nil || c.Type != "comment" || c.VersionID != nil) {
		return ErrInvalid
	}
	if c.RequestKey == uuid.Nil {
		return ErrInvalid
	}
	if c.Type == "import_output_document" && (c.ArtifactID == nil || *c.ArtifactID == uuid.Nil ||
		c.VersionID == nil || *c.VersionID == uuid.Nil || c.OutputItemID == nil || *c.OutputItemID == uuid.Nil) {
		return ErrInvalid
	}
	if c.TaskID != nil && (*c.TaskID == uuid.Nil || c.ArtifactID != nil ||
		(c.Type != "create_document" && c.Type != "add_artifact")) {
		return ErrInvalid
	}
	if !slices.Contains(
		[]string{
			"create_document",
			"import_output_document",
			"branch_document",
			"save_template", "use_template", "delete_template", "save_view", "delete_view", "restore_item",
			"add_artifact",
			"add_version",
			"place",
			"place_all",
			"frame",
			"move",
			"configure",
			"remove",
			"connect",
			"disconnect",
			"comment",
			"reply",
			"resolve",
			"edit_comment",
			"delete_comment",
			"rename",
			"checkpoint",
			"restore_version",
		},
		c.Type,
	) {
		return ErrInvalid
	}
	if len(c.Content) > 2<<20 || (len(c.Content) > 0 && c.Type != "create_document") {
		return ErrInvalid
	}
	c.Title = strings.TrimSpace(c.Title)
	if !utf8.ValidString(c.Title) || !utf8.ValidString(c.Body) || utf8.RuneCountInString(c.Title) > 240 ||
		len(c.Body) > 262144 ||
		len(c.Moves) > 2000 {
		return ErrLimit
	}
	if c.Width == 0 {
		c.Width = 320
	}
	if c.Height == 0 {
		c.Height = 280
	}
	if !geometry(c.X, c.Y, c.Width, c.Height) {
		return ErrInvalid
	}
	for _, m := range c.Moves {
		if m.ID == uuid.Nil || m.ExpectedRevision < 1 || !geometry(m.X, m.Y, m.Width, m.Height) {
			return ErrInvalid
		}
	}
	for _, raw := range []*json.RawMessage{&c.ViewConfig, &c.Anchor} {
		if len(*raw) == 0 {
			*raw = json.RawMessage(`{}`)
		}
		var object map[string]json.RawMessage
		limit := 16384
		if c.Type == "save_template" && raw == &c.ViewConfig {
			limit = 131072
		}
		if len(*raw) > limit || json.Unmarshal(*raw, &object) != nil || object == nil {
			return ErrInvalid
		}
	}
	return nil
}

func geometry(x, y, w, h float64) bool {
	return !math.IsNaN(x) && !math.IsNaN(y) && math.Abs(x) <= 1000000 && math.Abs(y) <= 1000000 && w >= 120 &&
		w <= 10000 &&
		h >= 80 &&
		h <= 10000
}

func (s *DefaultCanvasService) apply(ctx context.Context, q *generated.Queries, m mutation) (uuid.UUID, error) {
	c := m.Input
	if slices.Contains(
		[]string{
			"save_template",
			"use_template",
			"delete_template",
			"save_view",
			"delete_view",
			"restore_item",
		},
		c.Type,
	) {
		return s.libraryMutation(ctx, q, m)
	}
	p := m.Principal
	switch c.Type {
	case "create_document",
		"import_output_document",
		"branch_document",
		"add_artifact",
		"add_version",
		"checkpoint",
		"restore_version":
		return s.register(ctx, q, m)
	case "place_all":
		if _, err := q.LockCanvasPlacementOutputs(
			ctx,
			generated.LockCanvasPlacementOutputsParams{CompanyID: p.CompanyID, ProjectID: m.ProjectID},
		); err != nil {
			return uuid.Nil, err
		}
		ids, err := q.PlaceUnplacedCanvasArtifacts(ctx, generated.PlaceUnplacedCanvasArtifactsParams{
			CompanyID: p.CompanyID, ProjectID: m.ProjectID, CanvasID: m.CanvasID, ActorUserID: p.UserID,
		})
		if err != nil {
			return uuid.Nil, err
		}
		count, err := q.CountCanvasItems(ctx, m.ProjectID)
		if err != nil {
			return uuid.Nil, err
		}
		if count > 2000 {
			return uuid.Nil, ErrLimit
		}
		m.Changes.Items = append(m.Changes.Items, ids...)
		return m.CanvasID, nil
	case "place", "frame":
		count, err := q.CountCanvasItems(ctx, m.ProjectID)
		if err != nil {
			return uuid.Nil, err
		}
		if count >= 2000 {
			return uuid.Nil, ErrLimit
		}
		kind := "frame"
		var artifact, version uuid.NullUUID
		if c.Type == "place" {
			id, err := requireID(c.VersionID)
			if err != nil {
				return uuid.Nil, err
			}
			if err = canVersion(ctx, q, p, m.ProjectID, id); err != nil {
				return uuid.Nil, err
			}
			v, err := q.GetCanvasVersion(
				ctx,
				generated.GetCanvasVersionParams{
					CompanyID: p.CompanyID,
					ProjectID: m.ProjectID,
					ID:        id,
				},
			)
			if err != nil {
				return uuid.Nil, err
			}
			if c.VersionPolicy == "draft" && !v.WikiPageID.Valid {
				return uuid.Nil, ErrInvalid
			}
			kind = "artifact"
			artifact = nullable(&v.ArtifactID)
			version = nullable(&id)
		}
		item, err := q.CreateCanvasItem(
			ctx,
			generated.CreateCanvasItemParams{
				CompanyID:       p.CompanyID,
				ProjectID:       m.ProjectID,
				CanvasID:        m.CanvasID,
				ItemType:        kind,
				ArtifactID:      artifact,
				PinnedVersionID: version,
				Title:           c.Title,
				X:               c.X,
				Y:               c.Y,
				Width:           c.Width,
				Height:          c.Height,
				CreatedByUserID: p.UserID,
				ViewConfig:      c.ViewConfig,
			},
		)
		if err == nil {
			if c.VersionPolicy == "draft" && kind == "artifact" {
				err = q.ConfigureCanvasItem(
					ctx,
					generated.ConfigureCanvasItemParams{
						ID:              item.ID,
						Title:           c.Title,
						Representation:  "preview",
						ViewConfig:      c.ViewConfig,
						VersionPolicy:   "draft",
						PinnedVersionID: version,
					},
				)
			}
			m.Changes.Items = append(m.Changes.Items, item.ID)
		}
		return item.ID, err
	case "move":
		if len(c.Moves) == 0 {
			return uuid.Nil, ErrInvalid
		}
		c.Moves = slices.Clone(c.Moves)
		slices.SortFunc(c.Moves, func(a, b Move) int { return strings.Compare(a.ID.String(), b.ID.String()) })
		for _, move := range c.Moves {
			item, err := authorizedItem(ctx, q, m, move.ID)
			if err != nil {
				return uuid.Nil, err
			}
			if item.GeometryRevision != move.ExpectedRevision {
				return uuid.Nil, ErrConflict
			}
			n, err := q.MoveCanvasItem(
				ctx,
				generated.MoveCanvasItemParams{
					ID:               item.ID,
					X:                move.X,
					Y:                move.Y,
					Width:            move.Width,
					Height:           move.Height,
					GeometryRevision: move.ExpectedRevision,
				},
			)
			if err != nil {
				return uuid.Nil, err
			}
			if n != 1 {
				return uuid.Nil, ErrConflict
			}
			m.Changes.Items = append(m.Changes.Items, item.ID)
		}
		return c.Moves[0].ID, nil
	case "configure", "remove":
		id, err := requireID(c.ID)
		if err != nil {
			return uuid.Nil, err
		}
		item, err := authorizedItem(ctx, q, m, id)
		if err != nil {
			return uuid.Nil, err
		}
		if item.ContentRevision != c.ExpectedRevision {
			return uuid.Nil, ErrConflict
		}
		if c.Type == "remove" {
			if err = q.DeleteCanvasItem(ctx, id); err != nil {
				return uuid.Nil, err
			}
			children, err := q.UngroupCanvasChildren(ctx, nullable(&id))
			if err != nil {
				return uuid.Nil, err
			}
			edges, err := q.DeleteCanvasItemEdges(ctx, id)
			if err != nil {
				return uuid.Nil, err
			}
			m.Changes.Items = append(m.Changes.Items, children...)
			m.Changes.Removed = append(m.Changes.Removed, id)
			m.Changes.Removed = append(m.Changes.Removed, edges...)
			return id, nil
		}
		if c.Representation == "" {
			c.Representation = item.Representation
		}
		if c.VersionPolicy == "" {
			c.VersionPolicy = item.VersionPolicy
		}
		if !slices.Contains([]string{"compact", "excerpt", "preview"}, c.Representation) ||
			!slices.Contains([]string{"pinned", "draft"}, c.VersionPolicy) {
			return uuid.Nil, ErrInvalid
		}
		pin := item.PinnedVersionID
		if c.VersionID != nil {
			if err = canVersion(ctx, q, p, m.ProjectID, *c.VersionID); err != nil {
				return uuid.Nil, err
			}
			pin = nullable(c.VersionID)
		}
		if item.ItemType == "artifact" && pin.Valid {
			version, err := q.GetCanvasVersion(ctx, generated.GetCanvasVersionParams{
				CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: pin.UUID,
			})
			if err != nil {
				return uuid.Nil, lookup(err)
			}
			if version.ArtifactID != item.ArtifactID.UUID ||
				(c.VersionPolicy == "draft" && !version.WikiPageID.Valid) {
				return uuid.Nil, ErrInvalid
			}
		}
		if c.ParentFrameID != nil {
			parent, err := authorizedItem(ctx, q, m, *c.ParentFrameID)
			if err != nil {
				return uuid.Nil, err
			}
			if parent.ItemType != "frame" || parent.ID == id || item.ItemType == "frame" {
				return uuid.Nil, ErrInvalid
			}
		}
		err = q.ConfigureCanvasItem(
			ctx,
			generated.ConfigureCanvasItemParams{
				ID:              id,
				Title:           c.Title,
				Representation:  c.Representation,
				ViewConfig:      c.ViewConfig,
				VersionPolicy:   c.VersionPolicy,
				PinnedVersionID: pin,
				ParentFrameID:   nullable(c.ParentFrameID),
			},
		)
		m.Changes.Items = append(m.Changes.Items, id)
		return id, err
	case "connect":
		count, err := q.CountCanvasEdges(ctx, m.ProjectID)
		if err != nil {
			return uuid.Nil, err
		}
		if count >= 4000 {
			return uuid.Nil, ErrLimit
		}
		source, err := requireID(c.SourceID)
		if err != nil {
			return uuid.Nil, err
		}
		target, err := requireID(c.TargetID)
		if err != nil {
			return uuid.Nil, err
		}
		if _, err = authorizedItem(ctx, q, m, source); err != nil {
			return uuid.Nil, err
		}
		if _, err = authorizedItem(ctx, q, m, target); err != nil {
			return uuid.Nil, err
		}
		id, err := q.CreateCanvasEdge(
			ctx,
			generated.CreateCanvasEdgeParams{
				CompanyID:    p.CompanyID,
				ProjectID:    m.ProjectID,
				CanvasID:     m.CanvasID,
				SourceItemID: source,
				TargetItemID: target,
				Label:        c.Title,
			},
		)
		m.Changes.Edges = append(m.Changes.Edges, id)
		return id, err
	case "disconnect":
		id, err := requireID(c.ID)
		if err != nil {
			return uuid.Nil, err
		}
		edge, err := q.LockCanvasEdge(
			ctx,
			generated.LockCanvasEdgeParams{CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: id},
		)
		if err != nil {
			return uuid.Nil, lookup(err)
		}
		if edge.Version != c.ExpectedRevision {
			return uuid.Nil, ErrConflict
		}
		for _, endpoint := range []uuid.UUID{edge.SourceItemID, edge.TargetItemID} {
			if _, err = authorizedItem(ctx, q, m, endpoint); err != nil {
				return uuid.Nil, err
			}
		}
		n, err := q.DeleteCanvasEdge(
			ctx,
			generated.DeleteCanvasEdgeParams{CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: id},
		)
		if err != nil {
			return uuid.Nil, err
		}
		if n == 0 {
			return uuid.Nil, ErrNotFound
		}
		m.Changes.Removed = append(m.Changes.Removed, id)
		return id, nil
	case "comment", "reply", "resolve", "edit_comment", "delete_comment":
		return reviewComment(ctx, q, m)
	case "rename":
		id, err := requireID(c.ArtifactID)
		if err != nil {
			return uuid.Nil, err
		}
		a, err := q.LockCanvasArtifact(
			ctx,
			generated.LockCanvasArtifactParams{CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: id},
		)
		if err != nil {
			return uuid.Nil, lookup(err)
		}
		if a.Version != c.ExpectedRevision {
			return uuid.Nil, ErrConflict
		}
		v, err := requireID(c.VersionID)
		if err != nil {
			return uuid.Nil, err
		}
		if err = canVersion(ctx, q, p, m.ProjectID, v); err != nil {
			return uuid.Nil, err
		}
		version, err := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: v},
		)
		if err != nil {
			return uuid.Nil, err
		}
		if version.ArtifactID != id {
			return uuid.Nil, ErrInvalid
		}
		if c.Title == "" {
			return uuid.Nil, ErrInvalid
		}
		if version.WikiPageID.Valid && !version.AccessOutputID.Valid {
			page, lookupErr := q.GetCanvasWikiPage(
				ctx,
				generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: version.WikiPageID.UUID},
			)
			if lookupErr != nil {
				return uuid.Nil, lookup(lookupErr)
			}
			if page.ProjectID.Valid {
				draft, lookupErr := wiki.BlockDraftInTx(ctx, q, p.CompanyID, page.ID)
				if lookupErr != nil {
					return uuid.Nil, lookupErr
				}
				if err = q.UpdateProjectPageMetadata(
					ctx,
					generated.UpdateProjectPageMetadataParams{
						CompanyID: p.CompanyID,
						ProjectID: m.ProjectID,
						PageID:    page.ID,
						Title:     pageText(&c.Title),
					},
				); err != nil {
					return uuid.Nil, err
				}
				var root wiki.BlockNode
				if err = json.Unmarshal(draft.Content, &root); err != nil {
					return uuid.Nil, err
				}
				if err = savePageEdit(
					ctx,
					q,
					pageDraftEdit{
						principal: p,
						page:      page.ID,
						sequence:  draft.HeadSeq,
						state:     draft.State,
						root:      root,
					},
				); err != nil {
					return uuid.Nil, err
				}
			}
		}
		err = q.RenameCanvasArtifact(ctx, generated.RenameCanvasArtifactParams{ID: id, Title: c.Title})
		m.Changes.Artifacts = append(m.Changes.Artifacts, id)
		items, e := q.ListCanvasArtifactItems(
			ctx,
			generated.ListCanvasArtifactItemsParams{ProjectID: m.ProjectID, ArtifactID: nullable(&id)},
		)
		if e != nil {
			return uuid.Nil, e
		}
		m.Changes.Items = append(m.Changes.Items, items...)
		return id, err
	}
	return uuid.Nil, ErrInvalid
}

func authorizedItem(ctx context.Context, q *generated.Queries, m mutation, id uuid.UUID) (generated.CanvasItem, error) {
	item, err := q.LockCanvasItem(
		ctx,
		generated.LockCanvasItemParams{CompanyID: m.Principal.CompanyID, ProjectID: m.ProjectID, ID: id},
	)
	if err != nil {
		return item, lookup(err)
	}
	if item.ItemType == "artifact" {
		v := item.PinnedVersionID
		if item.VersionPolicy == "accepted" {
			a, err := q.LockCanvasArtifact(
				ctx,
				generated.LockCanvasArtifactParams{
					CompanyID: m.Principal.CompanyID,
					ProjectID: m.ProjectID,
					ID:        item.ArtifactID.UUID,
				},
			)
			if err != nil {
				return item, err
			}
			v = a.AcceptedVersionID
		}
		if !v.Valid {
			return item, ErrNotFound
		}
		err = canVersion(ctx, q, m.Principal, m.ProjectID, v.UUID)
	}
	return item, err
}

func reviewComment(ctx context.Context, q *generated.Queries, m mutation) (uuid.UUID, error) {
	c := m.Input
	p := m.Principal
	var thread generated.ReviewThread
	var err error
	if c.Type == "comment" {
		var id uuid.UUID
		var e error
		if c.PageID != nil {
			source, err := editableProjectPage(ctx, q, p, m.ProjectID, *c.PageID)
			if err != nil {
				return uuid.Nil, err
			}
			id = source.ID
			var anchor struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(c.Anchor, &anchor) != nil ||
				!slices.Contains([]string{"version", "textQuote", "block"}, anchor.Type) {
				return uuid.Nil, ErrInvalid
			}
		} else {
			id, e = requireID(c.VersionID)
			if e != nil {
				return uuid.Nil, e
			}
		}
		if c.PageID == nil {
			if e = canVersion(ctx, q, p, m.ProjectID, id); e != nil {
				return uuid.Nil, e
			}
		}
		anchor, e := parseReviewAnchor(c.Anchor)
		if e != nil {
			return uuid.Nil, e
		}
		if anchor != nil {
			outputItem := uuid.NullUUID{}
			if anchor.Target.OutputItemID != nil {
				outputItem = uuid.NullUUID{UUID: *anchor.Target.OutputItemID, Valid: true}
			}
			contentType, e := q.CanvasAnchorSource(
				ctx,
				generated.CanvasAnchorSourceParams{
					CompanyID:      p.CompanyID,
					ProjectID:      m.ProjectID,
					VersionID:      id,
					AssetVersionID: anchor.Target.AssetVersionID,
					OutputItemID:   outputItem,
				},
			)
			if e != nil {
				return uuid.Nil, e
			}
			if anchorSurface(contentType) != anchor.Surface {
				return uuid.Nil, ErrInvalid
			}
		}
		count, e := q.CountReviewThreads(
			ctx,
			generated.CountReviewThreadsParams{
				ArtifactVersionID: nullable(&id),
				PageID:            nullable(c.PageID),
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		if count >= 100 {
			return uuid.Nil, ErrLimit
		}
		thread, err = q.CreateReviewThread(
			ctx,
			generated.CreateReviewThreadParams{
				CompanyID:         p.CompanyID,
				ProjectID:         nullable(&m.ProjectID),
				ArtifactVersionID: nullable(&id),
				PageID:            nullable(c.PageID),
				Anchor:            c.Anchor,
				CreatedByUserID:   p.UserID,
			},
		)
	} else {
		id, e := requireID(c.ThreadID)
		if e != nil {
			return uuid.Nil, e
		}
		thread, err = q.LockReviewThread(
			ctx,
			generated.LockReviewThreadParams{
				CompanyID: p.CompanyID,
				ProjectID: nullable(&m.ProjectID),
				ID:        id,
			},
		)
		if err == nil {
			if thread.PageID.Valid {
				_, err = editableProjectPage(ctx, q, p, m.ProjectID, thread.PageID.UUID)
			} else {
				err = canVersion(ctx, q, p, m.ProjectID, thread.ArtifactVersionID.UUID)
			}
		}
	}
	if err != nil {
		return uuid.Nil, lookup(err)
	}
	switch c.Type {
	case "comment", "reply":
		count, e := q.CountVersionComments(
			ctx,
			generated.CountVersionCommentsParams{
				ArtifactVersionID: thread.ArtifactVersionID,
				PageID:            thread.PageID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		if count >= 500 {
			return uuid.Nil, ErrLimit
		}
		if strings.TrimSpace(c.Body) == "" || len(c.Body) > 8192 {
			return uuid.Nil, ErrInvalid
		}
		comment, e := q.CreateReviewComment(
			ctx,
			generated.CreateReviewCommentParams{
				CompanyID:    p.CompanyID,
				ProjectID:    nullable(&m.ProjectID),
				ThreadID:     thread.ID,
				Body:         c.Body,
				AuthorUserID: p.UserID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		err = q.RecordReviewCommentRevision(
			ctx,
			generated.RecordReviewCommentRevisionParams{CommentID: comment.ID, ActorUserID: p.UserID},
		)
	case "resolve":
		if c.Action != "resolve" && c.Action != "reopen" {
			return uuid.Nil, ErrInvalid
		}
		if thread.Version != c.ExpectedRevision {
			return uuid.Nil, ErrConflict
		}
		status := "resolved"
		actor := nullable(&p.UserID)
		at := pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
		if c.Action == "reopen" {
			status = "open"
			actor = uuid.NullUUID{}
			at = pgtype.Timestamptz{}
		}
		err = q.SetReviewThreadStatus(
			ctx,
			generated.SetReviewThreadStatusParams{
				ID:               thread.ID,
				Status:           status,
				ResolvedByUserID: actor,
				ResolvedAt:       at,
			},
		)
		if err == nil {
			action := "resolved"
			if status == "open" {
				action = "reopened"
			}
			err = q.RecordReviewThreadEvent(
				ctx,
				generated.RecordReviewThreadEventParams{
					ThreadID:    thread.ID,
					Action:      action,
					ActorUserID: p.UserID,
				},
			)
		}
	case "edit_comment", "delete_comment":
		id, e := requireID(c.ID)
		if e != nil {
			return uuid.Nil, e
		}
		deleted := pgtype.Timestamptz{}
		if c.Type == "delete_comment" {
			deleted = pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
			c.Body = ""
		} else if strings.TrimSpace(c.Body) == "" || len(c.Body) > 8192 {
			return uuid.Nil, ErrInvalid
		}
		comment, e := q.EditReviewComment(
			ctx,
			generated.EditReviewCommentParams{
				CompanyID:    p.CompanyID,
				ProjectID:    nullable(&m.ProjectID),
				ID:           id,
				Body:         c.Body,
				Version:      c.ExpectedRevision,
				AuthorUserID: p.UserID,
				DeletedAt:    deleted,
			},
		)
		if e != nil {
			return uuid.Nil, ErrConflict
		}
		if comment.ThreadID != thread.ID {
			return uuid.Nil, ErrInvalid
		}
		err = q.RecordReviewCommentRevision(
			ctx,
			generated.RecordReviewCommentRevisionParams{CommentID: comment.ID, ActorUserID: p.UserID},
		)
	}
	version, e := q.GetCanvasVersion(
		ctx,
		generated.GetCanvasVersionParams{
			CompanyID: p.CompanyID,
			ProjectID: m.ProjectID,
			ID:        thread.ArtifactVersionID.UUID,
		},
	)
	if e != nil {
		return uuid.Nil, e
	}
	m.Changes.Artifacts = append(m.Changes.Artifacts, version.ArtifactID)
	m.Changes.ReviewVersionIDs = append(m.Changes.ReviewVersionIDs, thread.ArtifactVersionID.UUID)
	return thread.ID, err
}

func (s *DefaultCanvasService) register(ctx context.Context, q *generated.Queries, m mutation) (uuid.UUID, error) {
	c := m.Input
	p := m.Principal
	var artifact generated.ProjectArtifact
	var err error
	if c.ArtifactID != nil {
		artifact, err = q.LockCanvasArtifact(
			ctx,
			generated.LockCanvasArtifactParams{
				CompanyID: p.CompanyID,
				ProjectID: m.ProjectID,
				ID:        *c.ArtifactID,
			},
		)
		if err != nil {
			return uuid.Nil, lookup(err)
		}
		if c.Type != "import_output_document" && artifact.Version != c.ExpectedRevision {
			return uuid.Nil, ErrConflict
		}
		if c.VersionID == nil {
			return uuid.Nil, ErrInvalid
		}
		if err = canVersion(ctx, q, p, m.ProjectID, *c.VersionID); err != nil {
			return uuid.Nil, err
		}
		if c.Type == "import_output_document" {
			id, e := q.GetCanvasImportedDocument(ctx, generated.GetCanvasImportedDocumentParams{
				CompanyID: p.CompanyID, ArtifactID: artifact.ID, OutputItemID: *c.OutputItemID,
			})
			if e == nil {
				return id, nil
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return uuid.Nil, e
			}
		}
	} else {
		if c.TaskID != nil {
			if _, err = q.GetCanvasTask(ctx, generated.GetCanvasTaskParams{
				CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: *c.TaskID,
			}); err != nil {
				return uuid.Nil, lookup(err)
			}
		}
		count, e := q.CountCanvasArtifacts(ctx, m.ProjectID)
		if e != nil {
			return uuid.Nil, e
		}
		if count >= 2000 {
			return uuid.Nil, ErrLimit
		}
		if c.Title == "" {
			return uuid.Nil, ErrInvalid
		}
		artifact, err = q.CreateCanvasArtifact(
			ctx,
			generated.CreateCanvasArtifactParams{
				CompanyID:       p.CompanyID,
				ProjectID:       m.ProjectID,
				Title:           c.Title,
				CreatedByUserID: p.UserID,
				TaskID:          nullable(c.TaskID),
			},
		)
		if err != nil {
			return uuid.Nil, err
		}
	}
	count, e := q.CountCanvasVersions(ctx, artifact.ID)
	if e != nil {
		return uuid.Nil, e
	}
	if count >= 200 {
		return uuid.Nil, ErrLimit
	}
	source := generated.CreateCanvasVersionParams{
		CompanyID:       p.CompanyID,
		ProjectID:       m.ProjectID,
		ArtifactID:      artifact.ID,
		CreatedByUserID: p.UserID,
		BaseVersionID:   nullable(c.VersionID),
	}
	switch {
	case c.Type == "import_output_document":
		page, revision, e := importOutputDocumentInTx(ctx, q, m, artifact)
		if e != nil {
			return uuid.Nil, e
		}
		source.SourceType = "wiki"
		source.WikiPageID = nullable(&page)
		source.WikiRevisionID = nullable(&revision)
	case c.Type == "branch_document":
		if c.VersionID == nil || c.ArtifactID == nil || !m.Access.Can(auth.ProjectPermissionTasksWrite) {
			return uuid.Nil, ErrForbidden
		}
		prior, e := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{
				CompanyID: p.CompanyID,
				ProjectID: m.ProjectID,
				ID:        *c.VersionID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		if prior.ArtifactID != artifact.ID || !prior.WikiPageID.Valid {
			return uuid.Nil, ErrInvalid
		}
		revision, e := q.GetCanvasWikiRevision(
			ctx,
			generated.GetCanvasWikiRevisionParams{
				CompanyID: p.CompanyID,
				PageID:    prior.WikiPageID.UUID,
				ID:        prior.WikiRevisionID.UUID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		var root wiki.BlockNode
		if e = json.Unmarshal(revision.BlockContent, &root); e != nil {
			return uuid.Nil, e
		}
		if root.Type == "" {
			root = wiki.BlocksFromMarkdown(revision.Body)
		}
		if c.Title == "" {
			c.Title = "Alternative"
		}
		page, rev, e := wiki.CreateCanvasPageInTx(
			ctx,
			q,
			wiki.CanvasPageInput{
				ProjectID:      m.ProjectID,
				Principal:      p,
				Title:          artifact.Title,
				Body:           revision.Body,
				Content:        &root,
				SourceOutputID: outputSource(prior.AccessOutputID),
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		source.SourceType = "wiki"
		source.WikiPageID = nullable(&page)
		source.WikiRevisionID = nullable(&rev)
	case c.Type == "restore_version":
		if c.VersionID == nil || c.ArtifactID == nil {
			return uuid.Nil, ErrInvalid
		}
		prior, e := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{
				CompanyID: p.CompanyID,
				ProjectID: m.ProjectID,
				ID:        *c.VersionID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		if prior.ArtifactID != artifact.ID {
			return uuid.Nil, ErrInvalid
		}
		if prior.WikiPageID.Valid {
			page, err := q.GetCanvasWikiPage(
				ctx,
				generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: prior.WikiPageID.UUID},
			)
			if err != nil {
				return uuid.Nil, lookup(err)
			}
			if !page.ProjectID.Valid && !page.SourceOutputID.Valid {
				return uuid.Nil, ErrForbidden
			}
			if page.SourceOutputID.Valid {
				if _, err = authorizeOutputDocument(
					ctx,
					q,
					p,
					page.SourceOutputID.UUID,
					true,
				); err != nil {
					return uuid.Nil, err
				}
			}
		}
		source.SourceType = prior.SourceType
		source.WikiPageID = prior.WikiPageID
		source.WikiRevisionID = prior.WikiRevisionID
		source.AssetVersionID = prior.AssetVersionID
		source.OutputID = prior.OutputID
	case c.Type == "create_document":
		if !m.Access.Can(auth.ProjectPermissionTasksWrite) {
			return uuid.Nil, ErrForbidden
		}
		var sourceOutputID *uuid.UUID
		if c.VersionID != nil {
			prior, e := q.GetCanvasVersion(ctx, generated.GetCanvasVersionParams{
				CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: *c.VersionID,
			})
			if e != nil {
				return uuid.Nil, e
			}
			sourceOutputID = outputSource(prior.AccessOutputID)
		}
		var native, snapshot *wiki.BlockNode
		if len(c.Content) > 0 {
			native = &wiki.BlockNode{}
			if json.Unmarshal(c.Content, native) != nil {
				return uuid.Nil, ErrInvalid
			}
			if err = wiki.ValidateBlockImport(*native); err != nil {
				return uuid.Nil, err
			}
			if err = validateBlockReferences(ctx, q, p, m.ProjectID, *native); err != nil {
				return uuid.Nil, err
			}
			frozen, err := frozenPageContent(ctx, q, p, m.ProjectID, *native)
			if err != nil {
				return uuid.Nil, err
			}
			snapshot = &frozen
			c.Body = wiki.BlocksMarkdown(*native)
		}
		page, revision, e := wiki.CreateCanvasPageInTx(
			ctx,
			q,
			wiki.CanvasPageInput{
				ProjectID:       m.ProjectID,
				Principal:       p,
				Title:           c.Title,
				Body:            c.Body,
				Content:         native,
				SnapshotContent: snapshot,
				SourceOutputID:  sourceOutputID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		source.SourceType = "wiki"
		source.WikiPageID = nullable(&page)
		source.WikiRevisionID = nullable(&revision)
	case c.Type == "checkpoint":
		if c.WikiPageID == nil || !m.Access.Can(auth.ProjectPermissionTasksWrite) {
			return uuid.Nil, ErrForbidden
		}
		if c.VersionID == nil || c.ArtifactID == nil {
			return uuid.Nil, ErrInvalid
		}
		base, e := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{
				CompanyID: p.CompanyID,
				ProjectID: m.ProjectID,
				ID:        *c.VersionID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		if base.ArtifactID != artifact.ID || !base.WikiPageID.Valid || base.WikiPageID.UUID != *c.WikiPageID {
			return uuid.Nil, ErrInvalid
		}
		page, e := q.GetCanvasWikiPage(
			ctx,
			generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: *c.WikiPageID},
		)
		if e != nil {
			return uuid.Nil, lookup(e)
		}
		if !page.ProjectID.Valid && !page.SourceOutputID.Valid {
			return uuid.Nil, ErrForbidden
		}
		if page.SourceOutputID.Valid {
			if _, e = authorizeOutputDocument(ctx, q, p, page.SourceOutputID.UUID, true); e != nil {
				return uuid.Nil, e
			}
		}
		if _, e = q.LockCanvasWikiPage(
			ctx,
			generated.LockCanvasWikiPageParams{CompanyID: p.CompanyID, ID: *c.WikiPageID},
		); e != nil {
			return uuid.Nil, e
		}
		draft, e := q.LockWikiDraft(
			ctx,
			generated.LockWikiDraftParams{CompanyID: p.CompanyID, PageID: *c.WikiPageID},
		)
		if e != nil {
			return uuid.Nil, lookup(e)
		}
		var frozen wiki.BlockNode
		if e = json.Unmarshal(draft.Content, &frozen); e != nil {
			return uuid.Nil, e
		}
		if frozen.Type == "doc" {
			originalHash := wiki.BlockHash(frozen)
			if e = freezeBlockReferences(ctx, q, p, m.ProjectID, &frozen); e != nil {
				return uuid.Nil, e
			}
			draft.Content, e = json.Marshal(frozen)
			if e != nil {
				return uuid.Nil, e
			}
			if wiki.BlockHash(frozen) != originalHash {
				draft.Markdown = wiki.BlocksMarkdown(frozen)
			}
		}
		revision, e := wiki.CheckpointCanvasPageInTx(
			ctx,
			q,
			wiki.CanvasCheckpointInput{
				Principal: p,
				PageID:    *c.WikiPageID,
				Body:      draft.Markdown,
				Sequence:  draft.HeadSeq,
				Content:   draft.Content,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		source.SourceType = "wiki"
		source.WikiPageID = nullable(c.WikiPageID)
		source.WikiRevisionID = nullable(&revision)
	case c.WikiPageID != nil:
		page, e := q.GetCanvasWikiPage(
			ctx,
			generated.GetCanvasWikiPageParams{CompanyID: p.CompanyID, ID: *c.WikiPageID},
		)
		if e != nil {
			return uuid.Nil, lookup(e)
		}
		if page.SourceOutputID.Valid {
			return uuid.Nil, ErrForbidden
		}
		if page.ProjectID.Valid {
			if page.ProjectID.UUID != m.ProjectID || c.ArtifactID == nil {
				return uuid.Nil, ErrInvalid
			}
			native, err := q.GetProjectPageVersion(
				ctx,
				generated.GetProjectPageVersionParams{
					CompanyID: p.CompanyID,
					ProjectID: m.ProjectID,
					ID:        page.ID,
				},
			)
			if err != nil {
				return uuid.Nil, lookup(err)
			}
			if native.ArtifactID != artifact.ID {
				return uuid.Nil, ErrInvalid
			}
		}
		revision := page.CurrentRevisionID
		if c.WikiRevisionID != nil {
			revision = nullable(c.WikiRevisionID)
		}
		if !revision.Valid {
			return uuid.Nil, ErrInvalid
		}
		selected, e := q.GetCanvasWikiRevision(
			ctx,
			generated.GetCanvasWikiRevisionParams{
				CompanyID: p.CompanyID,
				PageID:    page.ID,
				ID:        revision.UUID,
			},
		)
		if e != nil {
			return uuid.Nil, lookup(e)
		}
		if !page.ProjectID.Valid && !selected.PublishedAt.Valid {
			return uuid.Nil, ErrNotFound
		}
		source.SourceType = "wiki"
		source.WikiPageID = nullable(&page.ID)
		source.WikiRevisionID = revision
	case c.OutputID != nil:
		if _, e := q.LockCanvasOutput(
			ctx,
			generated.LockCanvasOutputParams{CompanyID: p.CompanyID, ID: *c.OutputID},
		); e != nil {
			return uuid.Nil, lookup(e)
		}
		yes, e := q.CanvasOutputAccess(
			ctx,
			generated.CanvasOutputAccessParams{
				CompanyID: p.CompanyID,
				OutputID:  *c.OutputID,
				UserID:    p.UserID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		if !yes {
			return uuid.Nil, ErrNotFound
		}
		source.SourceType = "output"
		source.OutputID = nullable(c.OutputID)
	case c.AttachmentID != nil:
		version, e := registerProjectFileInTx(
			ctx,
			q,
			projectFileInput{
				Principal:    p,
				ProjectID:    m.ProjectID,
				AttachmentID: *c.AttachmentID,
				Title:        artifact.Title,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		source.SourceType = "file"
		source.AssetVersionID = nullable(&version)
	default:
		return uuid.Nil, ErrInvalid
	}
	number, err := q.AllocateCanvasVersion(ctx, artifact.ID)
	if err != nil {
		return uuid.Nil, err
	}
	source.Number = number
	version, err := q.CreateCanvasVersion(ctx, source)
	if err != nil {
		return uuid.Nil, err
	}
	branchTitle := "Main"
	if c.Type == "branch_document" || (c.Type == "import_output_document" && c.Title != "") {
		branchTitle = c.Title
	} else if c.VersionID != nil {
		base, e := q.GetCanvasVersion(
			ctx,
			generated.GetCanvasVersionParams{
				CompanyID: p.CompanyID,
				ProjectID: m.ProjectID,
				ID:        *c.VersionID,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		branchTitle = base.BranchTitle
	}
	if err = q.SetCanvasBranchTitle(
		ctx,
		generated.SetCanvasBranchTitleParams{
			CompanyID:   p.CompanyID,
			ProjectID:   m.ProjectID,
			ID:          version.ID,
			BranchTitle: branchTitle,
		},
	); err != nil {
		return uuid.Nil, err
	}
	m.Changes.Artifacts = append(m.Changes.Artifacts, artifact.ID)
	if c.ArtifactID == nil {
		count, e := q.CountCanvasItems(ctx, m.ProjectID)
		if e != nil {
			return uuid.Nil, e
		}
		if count >= 2000 {
			return uuid.Nil, ErrLimit
		}
		item, e := q.CreateCanvasItem(
			ctx,
			generated.CreateCanvasItemParams{
				CompanyID:       p.CompanyID,
				ProjectID:       m.ProjectID,
				CanvasID:        m.CanvasID,
				ItemType:        "artifact",
				ArtifactID:      nullable(&artifact.ID),
				PinnedVersionID: nullable(&version.ID),
				X:               c.X,
				Y:               c.Y,
				Width:           c.Width,
				Height:          c.Height,
				CreatedByUserID: p.UserID,
				ViewConfig:      c.ViewConfig,
			},
		)
		if e != nil {
			return uuid.Nil, e
		}
		m.Changes.Items = append(m.Changes.Items, item.ID)
	}
	return version.ID, nil
}
