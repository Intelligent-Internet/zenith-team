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

func (s *DefaultCanvasService) Snapshot(
	ctx context.Context,
	p auth.UserPrincipal,
	project uuid.UUID,
	since int64,
) (Snapshot, error) {
	result := Snapshot{
		ArtifactIDs:      []uuid.UUID{},
		ReviewVersionIDs: []uuid.UUID{},
		Full:             since == 0,
		Items:            []Item{},
		Artifacts:        []Artifact{},
		Edges:            []Edge{},
		Removed:          []uuid.UUID{},
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(q *generated.Queries) error {
		projectAccess, err := authorize(ctx, q, p, project, false)
		if err != nil {
			return err
		}
		result.CanEdit = projectAccess.Can(auth.ProjectPermissionTasksWrite)
		result.CanManageFiles = projectAccess.Can(auth.ProjectPermissionFilesManage)
		result.CanEditDocuments = projectAccess.Can(auth.ProjectPermissionTasksWrite)
		board, err := q.GetProjectCanvas(
			ctx,
			generated.GetProjectCanvasParams{CompanyID: p.CompanyID, ProjectID: project},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			result.Full = true
			return nil
		}
		if err != nil {
			return err
		}
		result.CanvasID = board.ID
		result.Sequence = board.HeadSeq
		if since < 0 {
			return ErrInvalid
		}
		if since > board.HeadSeq {
			result.Full = true
		}
		changes := Changes{}
		if !result.Full {
			rows, err := q.ListCanvasChanges(
				ctx,
				generated.ListCanvasChangesParams{ProjectID: project, Seq: since},
			)
			if err != nil {
				return err
			}
			if len(rows) > 500 || int64(len(rows)) != board.HeadSeq-since {
				result.Full = true
			} else {
				for i, r := range rows {
					if r.Seq != since+int64(i)+1 {
						result.Full = true
						break
					}
					var change Changes
					if err = json.Unmarshal(r.Changes, &change); err != nil {
						return err
					}
					// Older receipts cannot identify the review versions they changed.
					if change.ReviewVersionIDs == nil {
						result.Full = true
						break
					}
					changes.Items = append(changes.Items, change.Items...)
					changes.Artifacts = append(changes.Artifacts, change.Artifacts...)
					changes.Edges = append(changes.Edges, change.Edges...)
					changes.Removed = append(changes.Removed, change.Removed...)
					changes.ReviewVersionIDs = append(
						changes.ReviewVersionIDs,
						change.ReviewVersionIDs...)
				}
			}
		}
		if result.Full {
			changes = Changes{}
		}
		result.Removed = append(result.Removed, changes.Removed...)
		accessRows, err := q.ListCanvasAccessibleVersions(
			ctx,
			generated.ListCanvasAccessibleVersionsParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				UserID:    p.UserID,
			},
		)
		if err != nil {
			return err
		}
		access := map[uuid.UUID]bool{}
		accepted := map[uuid.UUID]uuid.UUID{}
		artifactAccess := map[uuid.UUID]bool{}
		for _, row := range accessRows {
			access[row.ID] = true
			artifactAccess[row.ArtifactID] = true
			if row.AcceptedVersionID.Valid {
				accepted[row.ArtifactID] = row.AcceptedVersionID.UUID
			}
		}
		for id := range artifactAccess {
			result.ArtifactIDs = append(result.ArtifactIDs, id)
		}
		for _, id := range changes.ReviewVersionIDs {
			if access[id] && !slices.Contains(result.ReviewVersionIDs, id) {
				result.ReviewVersionIDs = append(result.ReviewVersionIDs, id)
			}
		}
		artifactRows, err := q.ListCanvasArtifacts(
			ctx,
			generated.ListCanvasArtifactsParams{
				CompanyID:    p.CompanyID,
				ProjectID:    project,
				ActorUserID:  p.UserID,
				AllArtifacts: result.Full,
				Ids:          changes.Artifacts,
			},
		)
		if err != nil {
			return err
		}
		artifacts, err := decodeArtifacts(ctx, q, p, project, artifactRows)
		if err != nil {
			return err
		}
		result.Artifacts = artifacts
		changedArtifacts := map[uuid.UUID]bool{}
		for _, id := range changes.Artifacts {
			changedArtifacts[id] = true
		}

		itemRows, err := q.ListCanvasItems(
			ctx,
			generated.ListCanvasItemsParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				AllItems:  true,
				Ids:       []uuid.UUID{},
			},
		)
		if err != nil {
			return err
		}
		items, err := decodeRows[Item](itemRows)
		if err != nil {
			return err
		}
		visible := map[uuid.UUID]bool{}
		wantedItems := map[uuid.UUID]bool{}
		for _, id := range changes.Items {
			wantedItems[id] = true
		}
		for _, item := range items {
			ok := item.Type == "frame"
			if item.ArtifactID != nil {
				exists := artifactAccess[*item.ArtifactID]
				v := item.PinnedVersionID
				if item.VersionPolicy == "accepted" {
					v = pointer(accepted[*item.ArtifactID])
				}
				ok = exists && v != nil && access[*v]
			}
			if !ok {
				result.Removed = append(result.Removed, item.ID)
				continue
			}
			visible[item.ID] = true
			if result.Full || wantedItems[item.ID] ||
				(item.ArtifactID != nil && changedArtifacts[*item.ArtifactID]) {
				result.Items = append(result.Items, item)
			}
		}
		ids := []uuid.UUID{}
		for _, item := range result.Items {
			if item.Type != "artifact" {
				continue
			}
			id := item.PinnedVersionID
			if item.VersionPolicy == "accepted" {
				id = pointer(accepted[*item.ArtifactID])
			}
			if id != nil {
				ids = append(ids, *id)
			}
		}
		if len(ids) > 0 {
			rows, err := q.ListCanvasSceneVersions(
				ctx,
				generated.ListCanvasSceneVersionsParams{
					CompanyID:   p.CompanyID,
					ProjectID:   project,
					ActorUserID: p.UserID,
					Ids:         ids,
				},
			)
			if err != nil {
				return err
			}
			versions, err := decodeVersions(ctx, q, p, project, rows)
			if err != nil {
				return err
			}
			byID := map[uuid.UUID]*Version{}
			for i := range versions {
				byID[versions[i].ID] = &versions[i]
			}
			for i := range result.Items {
				item := &result.Items[i]
				id := item.PinnedVersionID
				if item.VersionPolicy == "accepted" && item.ArtifactID != nil {
					id = pointer(accepted[*item.ArtifactID])
				}
				if id != nil {
					item.Version = byID[*id]
				}
			}
		}
		edgeRows, err := q.ListCanvasEdges(
			ctx,
			generated.ListCanvasEdgesParams{
				CompanyID: p.CompanyID,
				ProjectID: project,
				AllEdges:  true,
				Ids:       []uuid.UUID{},
			},
		)
		if err != nil {
			return err
		}
		edges, err := decodeRows[Edge](edgeRows)
		if err != nil {
			return err
		}
		for _, edge := range edges {
			if visible[edge.Source] && visible[edge.Target] {
				if result.Full || slices.Contains(changes.Edges, edge.ID) {
					result.Edges = append(result.Edges, edge)
				}
			} else {
				result.Removed = append(result.Removed, edge.ID)
			}
		}
		return nil
	})
	return result, err
}

func (s *DefaultCanvasService) Threads(
	ctx context.Context,
	p auth.UserPrincipal,
	project, version uuid.UUID,
) ([]Thread, error) {
	result := []Thread{}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		if err := canVersion(ctx, q, p, project, version); err != nil {
			return err
		}
		rows, err := q.ListReviewThreads(
			ctx,
			generated.ListReviewThreadsParams{
				CompanyID:         p.CompanyID,
				ProjectID:         nullable(&project),
				ArtifactVersionID: nullable(&version),
			},
		)
		if err != nil {
			return err
		}
		result, err = decodeRows[Thread](rows)
		return err
	})
	return result, err
}

func (s *DefaultCanvasService) History(
	ctx context.Context,
	p auth.UserPrincipal,
	project, artifact uuid.UUID,
) (History, error) {
	result := History{Versions: []Version{}, Decisions: []Decision{}}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		rows, err := q.ListCanvasVersions(
			ctx,
			generated.ListCanvasVersionsParams{
				CompanyID:   p.CompanyID,
				ProjectID:   project,
				ArtifactID:  artifact,
				ActorUserID: p.UserID,
			},
		)
		if err != nil {
			return err
		}
		result.Versions, err = decodeVersions(ctx, q, p, project, rows)
		if err != nil {
			return err
		}
		if len(result.Versions) == 0 {
			return ErrNotFound
		}
		decisions, err := q.ListCanvasDecisions(
			ctx,
			generated.ListCanvasDecisionsParams{
				CompanyID:  p.CompanyID,
				ProjectID:  project,
				ArtifactID: artifact,
			},
		)
		if err != nil {
			return err
		}
		all, err := decodeRows[Decision](decisions)
		if err != nil {
			return err
		}
		visible := map[uuid.UUID]bool{}
		for _, v := range result.Versions {
			visible[v.ID] = true
		}
		for _, d := range all {
			if visible[d.VersionID] {
				result.Decisions = append(result.Decisions, d)
			}
		}
		return nil
	})
	return result, err
}

func (s *DefaultCanvasService) VersionBody(
	ctx context.Context,
	p auth.UserPrincipal,
	project, version uuid.UUID,
) (string, error) {
	var body string
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := authorize(ctx, q, p, project, false); err != nil {
			return err
		}
		if err := canVersion(ctx, q, p, project, version); err != nil {
			return err
		}
		var err error
		body, err = q.GetCanvasVersionBody(
			ctx,
			generated.GetCanvasVersionBodyParams{CompanyID: p.CompanyID, ProjectID: project, ID: version},
		)
		return err
	})
	return body, err
}
