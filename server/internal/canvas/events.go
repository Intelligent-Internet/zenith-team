package canvas

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const (
	TopicBoard              = "whiteboard"
	EventBoardChanged       = "whiteboard.changed"
	EventBoardRenamed       = "whiteboard.renamed"
	Topic                   = "canvas"
	EventChanged            = "canvas.changed"
	EventDocumentChanged    = "canvas.document_changed"
	EventPagePreviewChanged = "canvas.page_preview_changed"
)

type Changed struct {
	auth.ProjectFrame
	ProjectID uuid.UUID `json:"projectId"`
	Sequence  int64     `json:"sequence"`
}

type DocumentChanged struct {
	auth.ProjectFrame
	ProjectID uuid.UUID `json:"projectId"`
	PageID    uuid.UUID `json:"pageId"`
	Sequence  int64     `json:"sequence"`
}

// Capture recipients in the producing transaction; Project content never broadcasts Company-wide by default.
func changeEvent(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	project uuid.UUID,
	sequence int64,
	page uuid.UUID,
) (*events.Event, error) {
	audience, err := auth.ProjectFrameAudienceInTx(ctx, q, p.CompanyID, project, false)
	if err != nil {
		return nil, err
	}
	frame := auth.ProjectFrame{Audience: audience}
	event := &events.Event{
		EntityType: Topic,
		Name:       EventChanged,
		CompanyID:  p.CompanyID,
		Actor:      events.UserActor(p.UserID.String()),
		CreatedAt:  time.Now().UTC(),
		Payload:    Changed{ProjectFrame: frame, ProjectID: project, Sequence: sequence},
	}
	if page != uuid.Nil {
		event.Name = EventDocumentChanged
		event.Payload = DocumentChanged{
			ProjectFrame: frame,
			ProjectID:    project,
			PageID:       page,
			Sequence:     sequence,
		}
	}
	return event, nil
}

// Output documents use the same private delivery mechanism as Project documents;
// their recipient list comes from current Output access in the write transaction.
type OutputDocumentChanged struct {
	auth.ProjectFrame
	OutputID uuid.UUID `json:"outputId"`
	PageID   uuid.UUID `json:"pageId"`
	Sequence int64     `json:"sequence"`
}

type BoardChanged struct {
	RecipientUserIDs []uuid.UUID `json:"-"`
	BoardID          uuid.UUID   `json:"boardId"`
	Sequence         int64       `json:"sequence"`
}

// boardAudienceOf is the audience stampBoardAudience gave a change event.
func boardAudienceOf(event events.Event) []uuid.UUID {
	change, err := events.PayloadOf[BoardChanged](event)
	if err != nil {
		return nil
	}
	return change.RecipientUserIDs
}

func stampBoardAudience(ctx context.Context, q *generated.Queries, event *events.Event) error {
	change, err := events.PayloadOf[BoardChanged](*event)
	if err != nil {
		return err
	}
	change.RecipientUserIDs, err = q.ListBoardAudience(
		ctx,
		generated.ListBoardAudienceParams{CompanyID: event.CompanyID, ArtifactID: change.BoardID},
	)
	event.Payload = change
	return err
}

// PagePreviewChanged is a refetch hint. Source text and compiled content remain
// behind the member read endpoint's current Page and Conversation access checks.
type PagePreviewChanged struct {
	auth.ProjectFrame
	ProjectID      *uuid.UUID `json:"projectId,omitempty"`
	PageID         uuid.UUID  `json:"pageId"`
	RunID          uuid.UUID  `json:"runId"`
	ConversationID uuid.UUID  `json:"conversationId"`
	GenerationID   uuid.UUID  `json:"generationId"`
	Revision       int64      `json:"revision"`
	Discarded      bool       `json:"discarded"`
	ExpiresAt      time.Time  `json:"expiresAt"`
}

func pagePreviewChangeEvent(
	ctx context.Context,
	q *generated.Queries,
	row generated.ProjectPageSourcePreview,
) (*events.Event, error) {
	readers, err := q.ListPageSourcePreviewReaders(
		ctx,
		generated.ListPageSourcePreviewReadersParams{CompanyID: row.CompanyID, ID: row.ID},
	)
	if err != nil {
		return nil, err
	}
	run, err := q.GetRunInCompany(ctx, generated.GetRunInCompanyParams{CompanyID: row.CompanyID, ID: row.RunID})
	if err != nil {
		return nil, err
	}
	return &events.Event{
		EntityType: Topic, Name: EventPagePreviewChanged, CompanyID: row.CompanyID,
		RunID: row.RunID, ConversationID: row.ConversationID,
		Actor:     events.RunActor(run.AgentID.String(), row.RunID.String(), row.CreatedByUserID.String()),
		CreatedAt: time.Now().UTC(),
		Payload: PagePreviewChanged{
			ProjectFrame: auth.ProjectFrame{Audience: &auth.ProjectAudience{UserIDs: readers}},
			ProjectID: outputSource(
				row.ProjectID,
			),
			PageID:         row.PageID,
			RunID:          row.RunID,
			ConversationID: row.ConversationID,
			GenerationID:   row.ID,
			Revision:       row.Revision,
			Discarded:      row.Discarded,
			ExpiresAt:      row.ExpiresAt.Time,
		},
	}, nil
}
