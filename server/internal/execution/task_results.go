package execution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	dbgen "boardly.dev/boardly/server/pkg/db/generated"
)

type TaskResultListOptions struct {
	Limit        int32
	Before       *Cursor
	SourceBefore *TaskResultSourceCursor
	SkipResults  bool
	SkipSources  bool
}

type TaskResultSourceCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// TaskRunResult is one Run's Output. RuntimeID names the machine the Run
// executed on to its registrant alone, nil for everyone else (ADR 0014).
type TaskRunResult struct {
	RunID       *uuid.UUID        `json:"runId"`
	RuntimeID   *uuid.UUID        `json:"runtimeId"`
	RunStatus   string            `json:"runStatus"`
	Output      DeliverableOutput `json:"output"`
	Deliverable *Deliverable      `json:"deliverable"`
	Publication *Publication      `json:"publication"`
}

type TaskResultSource struct {
	MessageID  uuid.UUID           `json:"messageId"`
	Attachment attachment.Response `json:"attachment"`
}

type TaskResultPage struct {
	Items      []TaskRunResult         `json:"items"`
	Sources    []TaskResultSource      `json:"sources"`
	Next       *Cursor                 `json:"-"`
	NextSource *TaskResultSourceCursor `json:"-"`
}

// ListTaskResults reads durable results independently of the chat's loaded
// window. Both feeds share one membership-checked snapshot and use bulk reads.
func (s *DefaultDeliverableService) ListTaskResults(
	ctx context.Context,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
	options TaskResultListOptions,
) (TaskResultPage, error) {
	if options.Limit < 1 || options.Limit > 50 {
		return TaskResultPage{}, ErrDeliverableInvalidInput
	}
	page := TaskResultPage{Items: []TaskRunResult{}, Sources: []TaskResultSource{}}
	err := s.store.WithinTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly},
		func(q *dbgen.Queries) error {
			task, err := q.GetTaskForActiveMember(ctx, dbgen.GetTaskForActiveMemberParams{
				CompanyID: principal.CompanyID, ActorUserID: principal.UserID, TaskID: taskID,
			})
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrDeliverableNotFound
			}
			if err != nil {
				return fmt.Errorf("read Task for results: %w", err)
			}
			if !options.SkipResults {
				params := dbgen.ListTaskRunOutputsParams{
					CompanyID: principal.CompanyID, TaskID: taskID,
					ActorUserID: principal.UserID, RowLimit: options.Limit + 1,
				}
				if options.Before != nil {
					params.BeforeCreatedAt = pgtype.Timestamptz{
						Time:  options.Before.CreatedAt,
						Valid: true,
					}
					params.BeforeID = options.Before.ID
				}
				page.Items, page.Next, err = listTaskOutputResults(ctx, q, params)
				if err != nil {
					return err
				}
			}
			if !options.SkipSources {
				params := dbgen.ListTaskResultSourcesParams{
					CompanyID:      principal.CompanyID,
					ConversationID: task.ConversationID,
					ActorUserID:    principal.UserID,
					RowLimit:       options.Limit + 1,
				}
				if options.SourceBefore != nil {
					params.BeforeCreatedAt = pgtype.Timestamptz{
						Time:  options.SourceBefore.CreatedAt,
						Valid: true,
					}
					params.BeforeID = options.SourceBefore.ID
				}
				page.Sources, page.NextSource, err = listTaskResultSources(ctx, q, params)
			}
			return err
		})
	if err != nil {
		return TaskResultPage{}, err
	}
	return page, nil
}

func listTaskOutputResults(
	ctx context.Context,
	q *dbgen.Queries,
	params dbgen.ListTaskRunOutputsParams,
) ([]TaskRunResult, *Cursor, error) {
	rows, err := q.ListTaskRunOutputs(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list Task Outputs: %w", err)
	}
	var next *Cursor
	if len(rows) == int(params.RowLimit) {
		rows = rows[:len(rows)-1]
		last := rows[len(rows)-1]
		next = &Cursor{ID: last.Output.ID, CreatedAt: last.Output.CreatedAt.Time}
	}
	results := make([]TaskRunResult, 0, len(rows))
	if len(rows) == 0 {
		return results, next, nil
	}
	outputIDs := make([]uuid.UUID, 0, len(rows))
	for _, row := range rows {
		outputIDs = append(outputIDs, row.Output.ID)
	}
	items, err := loadOutputItems(ctx, q, params.CompanyID, params.ActorUserID, outputIDs)
	if err != nil {
		return nil, nil, err
	}
	deliverables, err := q.ListOutputDeliverables(ctx, dbgen.ListOutputDeliverablesParams{
		CompanyID: params.CompanyID, OutputIds: outputIDs,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list Task Deliverables: %w", err)
	}
	publications, err := q.ListOutputPublications(ctx, dbgen.ListOutputPublicationsParams{
		CompanyID: params.CompanyID, OutputIds: outputIDs,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list Task publications: %w", err)
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
		output := outputFromProjection(row.Output.Title, row.Output.Summary,
			util.TextToPtr(row.Output.SubmissionNotes), util.TextToPtr(row.Output.PreviewUrl),
			row.Output.CreatedAt.Time, items[row.Output.ID])
		output.ID, output.Type = row.Output.ID, ArtifactType(row.Output.Type)
		result := TaskRunResult{
			RunID:     util.NullUUIDPtr(row.RunID),
			RuntimeID: ViewerRuntimeID(row.RuntimeID.UUID, row.RuntimeOwnerUserID, params.ActorUserID),
			RunStatus: row.RunStatus, Output: output,
		}
		if review, ok := reviews[output.ID]; ok {
			deliverable := deliverableFromProjection(review.ID, row.RunID.UUID, review.Status, output,
				review.SubmittedAt.Time, util.TimestampToTime(review.ReviewedAt),
				util.TextToPtr(review.ReviewFeedback), review.Version)
			result.Deliverable = &deliverable
		}
		if publication, ok := sites[output.ID]; ok {
			result.Publication = &publication
		}
		results = append(results, result)
	}
	return results, next, nil
}

func listTaskResultSources(
	ctx context.Context,
	q *dbgen.Queries,
	params dbgen.ListTaskResultSourcesParams,
) ([]TaskResultSource, *TaskResultSourceCursor, error) {
	rows, err := q.ListTaskResultSources(ctx, params)
	if err != nil {
		return nil, nil, fmt.Errorf("list Task input attachments: %w", err)
	}
	var next *TaskResultSourceCursor
	if len(rows) == int(params.RowLimit) {
		rows = rows[:len(rows)-1]
		last := rows[len(rows)-1].Attachment
		next = &TaskResultSourceCursor{ID: last.ID, CreatedAt: last.CreatedAt.Time}
	}
	sources := make([]TaskResultSource, 0, len(rows))
	for _, row := range rows {
		file := row.Attachment
		sources = append(sources, TaskResultSource{MessageID: row.MessageID, Attachment: attachment.Response{
			ID: file.ID, CompanyID: file.CompanyID, UploadedByUserID: file.UploadedByUserID,
			Filename: file.Filename, ContentType: file.ContentType, SizeBytes: file.SizeBytes,
			Status: attachment.PublicStatusReady, CreatedAt: file.CreatedAt.Time,
			UpdatedAt: file.UpdatedAt.Time, Revision: file.Version,
		}})
	}
	return sources, next, nil
}
