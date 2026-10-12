package canvas

import (
	"context"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	TopicBoard           = "whiteboard"
	EventBoardChanged    = "whiteboard.changed"
	Topic                = "canvas"
	EventChanged         = "canvas.changed"
	EventDocumentChanged = "canvas.document_changed"
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
	// Access marks a change of who reaches the Board (sharing, visibility, a
	// claim or a revoked grant). It reaches the audience before and after the
	// change at once; content hints go to those with the Board open.
	Access bool `json:"access,omitempty"`
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
		generated.ListBoardAudienceParams{
			CompanyID:  event.CompanyID,
			BoardID:    change.BoardID,
			ActorRoles: auth.RolesWith(auth.PermissionWhiteboardsRead),
		},
	)
	event.Payload = change
	return err
}
