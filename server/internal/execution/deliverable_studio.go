package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/studiocanvas"
)

type SavedStudioCanvas struct {
	Canvas  studiocanvas.Canvas `json:"canvas"`
	AddedAt time.Time           `json:"addedAt"`
}

// AttachStudioCanvas adds missing provenance once, without revising the frozen
// result. The caller needs artifact management and the review permission.
func (s *DefaultDeliverableService) AttachStudioCanvas(
	ctx context.Context, principal auth.UserPrincipal, runID uuid.UUID, canvas studiocanvas.Canvas,
) (SavedStudioCanvas, error) {
	if !validDeliverableService(s) || runID == uuid.Nil {
		return SavedStudioCanvas{}, ErrDeliverableInvalidInput
	}
	if canvas.Edges == nil {
		canvas.Edges = []studiocanvas.Edge{}
	}
	var result SavedStudioCanvas
	var pending *events.Event
	var artifactChanged events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *dbgen.Queries) error {
		role, active, err := lockActiveMember(ctx, tx, principal.CompanyID, principal.UserID)
		if err != nil {
			return err
		}
		if !active || !role.Can(auth.PermissionResultsReview) {
			return ErrDeliverableForbidden
		}
		run, err := tx.LockRunForOutputPresentation(
			ctx,
			dbgen.LockRunForOutputPresentationParams{CompanyID: principal.CompanyID, RunID: runID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDeliverableNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Run for Studio source: %w", err)
		}
		visible, err := fetchDeliverableRunResult(ctx, tx, principal, runID, nil)
		if err != nil {
			return err
		}
		if visible.Output == nil {
			return ErrDeliverableNotFound
		}
		level, err := tx.GetOutputAccessForManage(ctx, dbgen.GetOutputAccessForManageParams{
			CompanyID: principal.CompanyID, OutputID: visible.Output.ID,
			ActorUserID: principal.UserID,
		})
		if err != nil {
			return fmt.Errorf("authorize Studio source: %w", err)
		}
		if level < 3 {
			return ErrDeliverableNotFound
		}
		if visible.Output.Type != ArtifactTypeMedia && visible.Output.Type != ArtifactTypeFile ||
			studiocanvas.Validate(&canvas, len(visible.Output.Items)) != nil {
			return ErrDeliverableInvalidInput
		}
		for _, item := range visible.Output.Items {
			if item.ContentType == "application/vnd.zenith-studio.canvas+json" {
				return ErrDeliverableConflict
			}
		}
		locked, err := tx.GetLatestOutputByRunForUpdate(
			ctx,
			dbgen.GetLatestOutputByRunForUpdateParams{CompanyID: principal.CompanyID, RunID: runID},
		)
		if err != nil {
			return fmt.Errorf("lock output for Studio source: %w", err)
		}
		encoded, encodeErr := json.Marshal(canvas)
		if encodeErr != nil {
			return ErrDeliverableInvalidInput
		}
		if visible.StudioCanvas != nil {
			canonical, err := json.Marshal(visible.StudioCanvas.Canvas)
			if err != nil {
				return fmt.Errorf("encode saved Studio source: %w", err)
			}
			if string(canonical) != string(encoded) {
				return ErrDeliverableConflict
			}
			result = *visible.StudioCanvas
			return nil
		}
		row, err := tx.InsertOutputStudioCanvas(
			ctx,
			dbgen.InsertOutputStudioCanvasParams{
				OutputID:  locked.ID,
				CompanyID: principal.CompanyID,
				Canvas:    encoded,
				UserID:    principal.UserID,
			},
		)
		if err != nil {
			return fmt.Errorf("save Studio source: %w", err)
		}
		result = SavedStudioCanvas{Canvas: canvas, AddedAt: row.CreatedAt.Time}
		snapshot, err := LoadRunSnapshotInTx(ctx, tx, run)
		if err != nil {
			return err
		}
		pending = &events.Event{
			EntityType:     Topic,
			Name:           EventRunStudioCanvasAdded,
			CompanyID:      principal.CompanyID,
			RunID:          run.ID,
			TaskID:         util.NullUUIDToValue(run.TaskID),
			ConversationID: run.ConversationID,
			CreatedAt:      row.CreatedAt.Time,
			Actor:          events.UserActor(principal.UserID.String()),
			Payload:        snapshot,
		}
		artifactChanged, err = artifactChangedInTx(ctx, tx, principal.CompanyID, locked.ID, row.CreatedAt.Time)
		return err
	})
	if err == nil && pending != nil {
		s.bus.Publish(ctx, Topic, *pending)
		s.bus.Publish(ctx, TopicArtifact, artifactChanged)
	}
	return result, err
}
