package execution

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	dbgen "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

type ArtifactListOptions struct {
	Limit  int32
	Before *Cursor
	RunID  uuid.UUID
}

// ArtifactRunResult is one Run's Artifact. RuntimeID names the machine the
// Run executed on to its registrant alone, nil for everyone else (ADR 0014).
type ArtifactRunResult struct {
	Library     *ArtifactLibraryMetadata `json:"library,omitempty"`
	RunID       *uuid.UUID               `json:"runId"`
	RuntimeID   *uuid.UUID               `json:"runtimeId"`
	RunStatus   string                   `json:"runStatus"`
	Artifact    DeliverableOutput        `json:"artifact"`
	Deliverable *Deliverable             `json:"deliverable"`
	Publication *Publication             `json:"publication"`
}

type ConversationArtifactPage struct {
	Items []ArtifactRunResult `json:"items"`
	Next  *Cursor             `json:"-"`
}

func (s *DefaultDeliverableService) ListConversationArtifacts(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	options ArtifactListOptions,
) (ConversationArtifactPage, error) {
	if !validDeliverableService(s) || conversationID == uuid.Nil ||
		options.Limit < 1 || options.Limit > 50 {
		return ConversationArtifactPage{}, ErrDeliverableInvalidInput
	}
	page := ConversationArtifactPage{Items: []ArtifactRunResult{}}
	err := s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *dbgen.Queries) error {
			_, err := q.GetConversationForArtifactList(
				ctx,
				dbgen.GetConversationForArtifactListParams{
					ConversationID: conversationID,
					CompanyID:      principal.CompanyID,
					ActorUserID:    principal.UserID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrArtifactNotFound
			}
			if err != nil {
				return fmt.Errorf("read Conversation for Artifacts: %w", err)
			}
			params := dbgen.ListConversationArtifactsParams{
				CompanyID: principal.CompanyID, ConversationID: conversationID,
				ActorUserID: principal.UserID,
				RowLimit:    options.Limit + 1,
				RunID:       options.RunID,
			}
			if options.Before != nil {
				params.BeforeCreatedAt = pgtype.Timestamptz{
					Time: options.Before.CreatedAt, Valid: true,
				}
				params.BeforeID = options.Before.ID
			}
			page.Items, page.Next, err = listConversationArtifactResults(ctx, q, params)
			return err
		},
	)
	if err != nil {
		return ConversationArtifactPage{}, err
	}
	return page, nil
}

func listConversationArtifactResults(
	ctx context.Context,
	q *dbgen.Queries,
	params dbgen.ListConversationArtifactsParams,
) ([]ArtifactRunResult, *Cursor, error) {
	rows, err := q.ListConversationArtifacts(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list Conversation Artifacts: %w", err)
	}
	rows, next := pageArtifactRows(rows, params.RowLimit)
	results, err := artifactRunResults(ctx, q, params.CompanyID, params.ActorUserID, rows)
	return results, next, err
}

// pageArtifactRows trims the look-ahead row a list query fetched beyond the
// page and returns the cursor that continues after the page's last row.
func pageArtifactRows(
	rows []dbgen.ListConversationArtifactsRow,
	rowLimit int32,
) ([]dbgen.ListConversationArtifactsRow, *Cursor) {
	if len(rows) < int(rowLimit) {
		return rows, nil
	}
	rows = rows[:len(rows)-1]
	last := rows[len(rows)-1].Output
	return rows, &Cursor{ID: last.ID, CreatedAt: last.CreatedAt.Time}
}

// artifactRunResults projects Artifact rows the actor may open, with their
// items, Deliverable review and publication, in the rows' order.
func artifactRunResults(
	ctx context.Context,
	q *dbgen.Queries,
	companyID, actorUserID uuid.UUID,
	rows []dbgen.ListConversationArtifactsRow,
) ([]ArtifactRunResult, error) {
	results := make([]ArtifactRunResult, 0, len(rows))
	if len(rows) == 0 {
		return results, nil
	}
	outputIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		outputIDs = append(outputIDs, row.Output.ID)
	}
	items, err := loadOutputItems(ctx, q, companyID, actorUserID, outputIDs)
	if err != nil {
		return nil, err
	}
	deliverables, err := q.ListOutputDeliverables(ctx, dbgen.ListOutputDeliverablesParams{
		CompanyID: companyID, OutputIds: outputIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list Artifact Deliverables: %w", err)
	}
	publications, err := q.ListOutputPublications(ctx, dbgen.ListOutputPublicationsParams{
		CompanyID: companyID, OutputIds: outputIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("list Artifact publications: %w", err)
	}
	reviews := make(map[uuid.UUID]dbgen.Deliverable, len(deliverables))
	for _, review := range deliverables {
		reviews[review.OutputID] = review
	}
	sites := make(map[uuid.UUID]Publication, len(publications))
	for _, publication := range publications {
		sites[publication.OutputID] = publicationFromRow(publication)
	}
	for _, row := range rows {
		artifact := outputFromProjection(
			row.Output.Title,
			row.Output.Summary,
			util.TextToPtr(row.Output.SubmissionNotes),
			util.TextToPtr(row.Output.PreviewUrl),
			row.Output.CreatedAt.Time,
			items[row.Output.ID],
		)
		artifact.ID, artifact.Type = row.Output.ID, ArtifactType(row.Output.Type)
		result := ArtifactRunResult{
			RunID:     util.NullUUIDPtr(row.RunID),
			RuntimeID: ViewerRuntimeID(row.RuntimeID.UUID, row.RuntimeOwnerUserID, actorUserID),
			RunStatus: row.RunStatus, Artifact: artifact,
		}
		if review, ok := reviews[artifact.ID]; ok {
			deliverable := deliverableFromProjection(
				review.ID,
				row.RunID.UUID,
				review.Status,
				artifact,
				review.SubmittedAt.Time,
				util.TimestampToTime(review.ReviewedAt),
				util.TextToPtr(review.ReviewFeedback),
				review.Version,
			)
			result.Deliverable = &deliverable
		}
		if publication, ok := sites[artifact.ID]; ok {
			result.Publication = &publication
		}
		results = append(results, result)
	}
	return results, nil
}
