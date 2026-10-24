package execution

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// The library includes ongoing work as well as retained Run submissions. Live
// work has no Run, delivery review, or manufactured Output row.
func appendLiveArtifacts(
	ctx context.Context,
	q *generated.Queries,
	items []ArtifactRunResult,
	in generated.ListLiveArtifactsParams,
) ([]ArtifactRunResult, error) {
	if in.Ids == nil {
		in.Ids = []uuid.UUID{}
	}
	rows, err := q.ListLiveArtifacts(ctx, in)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		liveURL := fmt.Sprintf("/app/companies/%s/artifacts/%s", in.CompanyID, row.ID)
		if row.Type == "page" {
			liveURL = fmt.Sprintf("/app/companies/%s/pages/%s", in.CompanyID, row.ID)
		}
		items = append(items, ArtifactRunResult{
			Artifact: DeliverableOutput{
				ID:          row.ID,
				Type:        ArtifactType(row.Type),
				Title:       row.Title,
				Summary:     row.Summary,
				LiveURL:     &liveURL,
				ActorType:   "user",
				ActorUserID: &row.CreatedByUserID,
				Freshness:   "not_applicable",
				CreatedAt:   row.CreatedAt.Time,
				Items:       []DeliverableOutputItem{},
			},
			Library: &ArtifactLibraryMetadata{
				OwnerName: row.OwnerName,
				Owned:     row.Owned,
				CanShare:  row.CanShare,
				Source: ArtifactLibrarySource{
					Type:   row.SourceType,
					Title:  row.SourceTitle,
					Detail: row.SourceDetail,
				},
			},
		})
	}
	slices.SortFunc(items, func(a, b ArtifactRunResult) int {
		if order := b.Artifact.CreatedAt.Compare(a.Artifact.CreatedAt); order != 0 {
			return order
		}
		return bytes.Compare(b.Artifact.ID[:], a.Artifact.ID[:])
	})
	return items, nil
}

func pageArtifactResults(items []ArtifactRunResult, limit int32) ([]ArtifactRunResult, *Cursor) {
	if len(items) <= int(limit) {
		return items, nil
	}
	items = items[:limit]
	last := items[len(items)-1].Artifact
	return items, &Cursor{ID: last.ID, CreatedAt: last.CreatedAt}
}
