package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type ContentInput struct {
	ArtifactID uuid.UUID
	SnapshotID *uuid.UUID
}

type Content struct {
	ArtifactID uuid.UUID       `json:"artifactId"`
	SnapshotID uuid.UUID       `json:"snapshotId"`
	Number     int64           `json:"number"`
	Type       string          `json:"type"`
	Title      string          `json:"title"`
	Summary    string          `json:"summary"`
	Payload    json.RawMessage `json:"payload"`
	Items      []ContentItem   `json:"items"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type ContentItem struct {
	ID             uuid.UUID `json:"id"`
	AssetVersionID uuid.UUID `json:"assetVersionId"`
	Ordinal        int32     `json:"ordinal"`
	Role           string    `json:"role"`
	Label          string    `json:"label"`
	Filename       string    `json:"filename"`
	ContentType    string    `json:"contentType"`
	SizeBytes      int64     `json:"sizeBytes"`
}

type ContentAttachmentInput struct {
	ArtifactID, SnapshotID, ItemID uuid.UUID
}

// Content resolves current work or an exact saved state through live Artifact
// authority. Sharing a Run delivery does not open this surface.
func (s *DefaultArtifactService) Content(ctx context.Context, p auth.UserPrincipal, in ContentInput) (Content, error) {
	var result Content
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		access, err := auth.ResolveArtifactAccess(ctx, q, p, in.ArtifactID)
		if err != nil {
			return err
		}
		if !access.Read {
			return ErrNotFound
		}
		var snapshotID uuid.NullUUID
		if in.SnapshotID != nil {
			snapshotID = uuid.NullUUID{UUID: *in.SnapshotID, Valid: true}
		}
		row, err := q.ReadRetainedSnapshot(ctx, generated.ReadRetainedSnapshotParams{
			CompanyID: p.CompanyID, ArtifactID: in.ArtifactID, SnapshotID: snapshotID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		items, err := q.ListRetainedSnapshotItems(
			ctx,
			generated.ListRetainedSnapshotItemsParams{
				CompanyID:  p.CompanyID,
				SnapshotID: row.ArtifactSnapshot.ID,
			},
		)
		if err != nil {
			return err
		}
		result = Content{
			ArtifactID: in.ArtifactID,
			SnapshotID: row.ArtifactSnapshot.ID,
			Number:     row.ArtifactSnapshot.Number,
			Type:       row.RetainedSnapshot.ContentType,
			Title:      row.ArtifactSnapshot.Title,
			Summary:    row.RetainedSnapshot.Summary,
			Payload:    row.RetainedSnapshot.Payload,
			CreatedAt:  row.ArtifactSnapshot.CreatedAt.Time,
			Items:      []ContentItem{},
		}
		for _, item := range items {
			result.Items = append(result.Items, ContentItem{
				ID:             item.ID,
				Ordinal:        item.Ordinal,
				Role:           item.Role,
				Label:          item.Label,
				AssetVersionID: item.AssetVersion.ID,
				Filename:       item.AssetVersion.Filename,
				ContentType:    item.AssetVersion.ContentType,
				SizeBytes:      item.AssetVersion.SizeBytes,
			})
		}
		return nil
	})
	return result, err
}

// ContentAttachment authorizes the exact item, not every occurrence of its
// bytes. An unrelated private delivery cannot veto an authorized snapshot.
func (s *DefaultArtifactService) ContentAttachment(
	ctx context.Context,
	p auth.UserPrincipal,
	in ContentAttachmentInput,
) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		access, err := auth.ResolveArtifactAccess(ctx, q, p, in.ArtifactID)
		if err != nil {
			return err
		}
		if !access.Read {
			return ErrNotFound
		}
		id, err = q.ReadRetainedSnapshotAttachment(ctx, generated.ReadRetainedSnapshotAttachmentParams{
			CompanyID: p.CompanyID, ArtifactID: in.ArtifactID, SnapshotID: in.SnapshotID, ID: in.ItemID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return id, err
}
