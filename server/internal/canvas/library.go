package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type TemplateItem struct {
	Type           string          `json:"type"`
	Title          string          `json:"title"`
	VersionID      *uuid.UUID      `json:"versionId,omitempty"`
	Parent         *int            `json:"parent,omitempty"`
	Representation string          `json:"representation"`
	VersionPolicy  string          `json:"versionPolicy"`
	ViewConfig     json.RawMessage `json:"viewConfig"`
	X              float64         `json:"x"`
	Y              float64         `json:"y"`
	Width          float64         `json:"width"`
	Height         float64         `json:"height"`
}
type TemplateEdge struct {
	Source int    `json:"source"`
	Target int    `json:"target"`
	Label  string `json:"label"`
}
type TemplateContent struct {
	Items []TemplateItem `json:"items"`
	Edges []TemplateEdge `json:"edges"`
}
type LibraryEntry struct {
	ID     uuid.UUID       `json:"id"`
	Title  string          `json:"title"`
	Config json.RawMessage `json:"config"`
}
type RemovedItem struct {
	ID       uuid.UUID `json:"id"`
	Title    string    `json:"title"`
	Revision int64     `json:"revision"`
}
type Library struct {
	Templates []LibraryEntry `json:"templates"`
	Views     []LibraryEntry `json:"views"`
	Removed   []RemovedItem  `json:"removed"`
}

func (s *DefaultCanvasService) Library(ctx context.Context, p auth.UserPrincipal, project uuid.UUID) (Library, error) {
	result := Library{Templates: []LibraryEntry{}, Views: []LibraryEntry{}, Removed: []RemovedItem{}}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		templates, err := q.ListCanvasTemplates(
			ctx,
			generated.ListCanvasTemplatesParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		for _, row := range templates {
			var content TemplateContent
			if err = json.Unmarshal(row.Content, &content); err != nil {
				return err
			}
			visible := true
			for _, item := range content.Items {
				if item.VersionID != nil {
					if err = canVersion(ctx, q, p, project, *item.VersionID); err != nil {
						if !errors.Is(err, ErrNotFound) {
							return err
						}
						visible = false
						break
					}
				}
			}
			if visible {
				result.Templates = append(
					result.Templates,
					LibraryEntry{ID: row.ID, Title: row.Title, Config: row.Content},
				)
			}
		}
		views, err := q.ListCanvasPersonalViews(
			ctx,
			generated.ListCanvasPersonalViewsParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				UserID:    p.UserID,
			},
		)
		if err != nil {
			return err
		}
		for _, row := range views {
			result.Views = append(
				result.Views,
				LibraryEntry{ID: row.ID, Title: row.Title, Config: row.Config},
			)
		}
		removed, err := q.ListRemovedCanvasItems(
			ctx,
			generated.ListRemovedCanvasItemsParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if err != nil {
			return err
		}
		for _, row := range removed {
			if row.SourceVersionID.Valid {
				if err = canVersion(ctx, q, p, project, row.SourceVersionID.UUID); err != nil {
					if !errors.Is(err, ErrNotFound) {
						return err
					}
					continue
				}
			} else if row.ArtifactID.Valid {
				continue
			}
			result.Removed = append(
				result.Removed,
				RemovedItem{ID: row.ID, Title: row.Title, Revision: row.ContentRevision},
			)
		}
		return nil
	})
	return result, err
}

func (s *DefaultCanvasService) libraryMutation(
	ctx context.Context,
	q *generated.Queries,
	m mutation,
) (uuid.UUID, error) {
	c, p := m.Input, m.Principal
	switch c.Type {
	case "save_view":
		if c.Title == "" {
			return uuid.Nil, ErrInvalid
		}
		rows, err := q.ListCanvasPersonalViews(
			ctx,
			generated.ListCanvasPersonalViewsParams{
				CompanyID: p.CompanyID,
				ProjectID: m.ProjectID,
				UserID:    p.UserID,
			},
		)
		if err != nil {
			return uuid.Nil, err
		}
		if len(rows) >= 50 {
			return uuid.Nil, ErrLimit
		}
		return q.CreateCanvasPersonalView(
			ctx,
			generated.CreateCanvasPersonalViewParams{
				CompanyID: p.CompanyID,
				ProjectID: m.ProjectID,
				UserID:    p.UserID,
				Title:     c.Title,
				Config:    c.ViewConfig,
			},
		)
	case "delete_view":
		id, err := requireID(c.ID)
		if err != nil {
			return uuid.Nil, err
		}
		return id, q.DeleteCanvasPersonalView(
			ctx,
			generated.DeleteCanvasPersonalViewParams{
				CompanyID: p.CompanyID,
				ProjectID: m.ProjectID,
				UserID:    p.UserID,
				ID:        id,
			},
		)
	case "delete_template":
		id, err := requireID(c.ID)
		if err != nil {
			return uuid.Nil, err
		}
		return id, q.DeleteCanvasTemplate(
			ctx,
			generated.DeleteCanvasTemplateParams{CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: id},
		)
	case "restore_item":
		id, err := requireID(c.ID)
		if err != nil {
			return uuid.Nil, err
		}
		row, err := q.RestoreCanvasItem(
			ctx,
			generated.RestoreCanvasItemParams{
				CompanyID:       p.CompanyID,
				ProjectID:       m.ProjectID,
				ID:              id,
				ContentRevision: c.ExpectedRevision,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrConflict
		}
		if err != nil {
			return uuid.Nil, err
		}
		if _, err = authorizedItem(ctx, q, m, row.ID); err != nil {
			return uuid.Nil, err
		}
		m.Changes.Items = append(m.Changes.Items, id)
		return id, nil
	case "save_template":
		if c.Title == "" {
			return uuid.Nil, ErrInvalid
		}
		rows, err := q.ListCanvasTemplates(
			ctx,
			generated.ListCanvasTemplatesParams{CompanyID: p.CompanyID, ProjectID: m.ProjectID},
		)
		if err != nil {
			return uuid.Nil, err
		}
		if len(rows) >= 100 {
			return uuid.Nil, ErrLimit
		}
		var content TemplateContent
		if json.Unmarshal(c.ViewConfig, &content) != nil {
			return uuid.Nil, ErrInvalid
		}
		if err = validateTemplate(ctx, q, m, content); err != nil {
			return uuid.Nil, err
		}
		return q.CreateCanvasTemplate(
			ctx,
			generated.CreateCanvasTemplateParams{
				CompanyID:       p.CompanyID,
				ProjectID:       m.ProjectID,
				Title:           c.Title,
				Content:         c.ViewConfig,
				CreatedByUserID: p.UserID,
			},
		)
	case "use_template":
		id, err := requireID(c.ID)
		if err != nil {
			return uuid.Nil, err
		}
		row, err := q.GetCanvasTemplate(
			ctx,
			generated.GetCanvasTemplateParams{CompanyID: p.CompanyID, ProjectID: m.ProjectID, ID: id},
		)
		if err != nil {
			return uuid.Nil, lookup(err)
		}
		var content TemplateContent
		if err = json.Unmarshal(row.Content, &content); err != nil {
			return uuid.Nil, err
		}
		return s.placeTemplate(ctx, q, m, content)
	}
	return uuid.Nil, ErrInvalid
}

func validateTemplate(ctx context.Context, q *generated.Queries, m mutation, content TemplateContent) error {
	if len(content.Items) == 0 || len(content.Items) > 100 || len(content.Edges) > 200 {
		return ErrLimit
	}
	for i, item := range content.Items {
		if !slices.Contains([]string{"frame", "artifact"}, item.Type) ||
			!geometry(item.X, item.Y, item.Width, item.Height) {
			return ErrInvalid
		}
		if item.Parent != nil &&
			(*item.Parent < 0 || *item.Parent >= len(content.Items) || *item.Parent == i || content.Items[*item.Parent].Type != "frame" || item.Type == "frame") {
			return ErrInvalid
		}
		if item.Type == "artifact" {
			if item.VersionID == nil {
				return ErrInvalid
			}
			if err := canVersion(ctx, q, m.Principal, m.ProjectID, *item.VersionID); err != nil {
				return err
			}
		}
	}
	for _, edge := range content.Edges {
		if edge.Source < 0 || edge.Target < 0 || edge.Source >= len(content.Items) ||
			edge.Target >= len(content.Items) ||
			edge.Source == edge.Target {
			return ErrInvalid
		}
	}
	return nil
}

func (s *DefaultCanvasService) placeTemplate(
	ctx context.Context,
	q *generated.Queries,
	m mutation,
	content TemplateContent,
) (uuid.UUID, error) {
	if err := validateTemplate(ctx, q, m, content); err != nil {
		return uuid.Nil, err
	}
	ids := make([]uuid.UUID, len(content.Items))
	for i, item := range content.Items {
		kind := "place"
		if item.Type == "frame" {
			kind = "frame"
		}
		command := Command{
			RequestKey:    m.Input.RequestKey,
			Type:          kind,
			Title:         item.Title,
			VersionID:     item.VersionID,
			ViewConfig:    item.ViewConfig,
			VersionPolicy: item.VersionPolicy,
			X:             item.X + m.Input.X,
			Y:             item.Y + m.Input.Y,
			Width:         item.Width,
			Height:        item.Height,
		}
		if err := normalizeCommand(&command); err != nil {
			return uuid.Nil, err
		}
		child := m
		child.Input = command
		id, err := s.apply(ctx, q, child)
		if err != nil {
			return uuid.Nil, err
		}
		ids[i] = id
	}
	for i, item := range content.Items {
		parent := (*uuid.UUID)(nil)
		if len(item.ViewConfig) == 0 {
			item.ViewConfig = json.RawMessage(`{}`)
		}
		if item.Parent != nil {
			parent = &ids[*item.Parent]
		}
		representation := item.Representation
		if representation == "" {
			representation = "preview"
		}
		if !slices.Contains([]string{"compact", "excerpt", "preview"}, representation) {
			return uuid.Nil, ErrInvalid
		}
		// Templates pin the reviewed versions they were saved with. Applying one
		// never silently switches an old handoff to the latest live draft.
		if err := q.ConfigureCanvasItem(
			ctx,
			generated.ConfigureCanvasItemParams{
				ID:              ids[i],
				Title:           item.Title,
				Representation:  representation,
				VersionPolicy:   "pinned",
				PinnedVersionID: nullable(item.VersionID),
				ParentFrameID:   nullable(parent),
				ViewConfig:      item.ViewConfig,
			},
		); err != nil {
			return uuid.Nil, err
		}
	}
	for _, edge := range content.Edges {
		child := m
		child.Input = Command{
			RequestKey: m.Input.RequestKey,
			Type:       "connect",
			SourceID:   &ids[edge.Source],
			TargetID:   &ids[edge.Target],
			Title:      edge.Label,
		}
		if err := normalizeCommand(&child.Input); err != nil {
			return uuid.Nil, err
		}
		if _, err := s.apply(ctx, q, child); err != nil {
			return uuid.Nil, err
		}
	}
	return ids[0], nil
}
