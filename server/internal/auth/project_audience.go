package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// ProjectAudience is who may receive a realtime frame about a Project or the
// work inside it (ADR 0017), decided in the transaction that staged the
// frame. CompanyWide means every active member reads the Project, so one
// Company frame is enough; otherwise UserIDs lists the recipients, sorted and
// without duplicates, and an empty list reaches nobody.
//
// It travels on the payload as a routing fact tagged json:"-" (see
// ProjectFrame), never on the wire, and cmd/server/realtime_routing.go is
// the only reader.
type ProjectAudience struct {
	CompanyWide bool
	UserIDs     []uuid.UUID
}

// ProjectFrame is embedded in every payload whose frame a Project's readers
// receive. Audience is nil until a producer decides it; the router refuses a
// Project frame without one rather than widening it to the Company.
type ProjectFrame struct {
	Audience *ProjectAudience `json:"-"`
}

// ProjectAudience returns the audience stamped on the payload, or nil.
func (f ProjectFrame) ProjectAudience() *ProjectAudience { return f.Audience }

// ProjectAudienceCarrier is a payload that may carry a Project audience: any
// payload embedding ProjectFrame. The realtime router reads the audience
// through it, so each carrier type asserts it at compile time.
type ProjectAudienceCarrier interface {
	ProjectAudience() *ProjectAudience
}

// ProjectFramePayload is a payload StampProjectAudiencesInTx can decide an
// audience for. ProjectFrameScope says whether the frame can belong to a
// Project at all (a direct chat's message cannot) and, when the payload
// already knows it, which Project. WithProjectAudience returns a copy of the
// payload carrying the audience, because payloads travel by value.
type ProjectFramePayload interface {
	ProjectAudienceCarrier
	ProjectFrameScope() (projectID uuid.UUID, scoped bool)
	WithProjectAudience(*ProjectAudience) any
}

// ProjectFrameAudienceInTx is ProjectAudienceInTx as a frame's routing fact.
// includeProjectsManageHolders is set only for the Project lifecycle and
// access frames; frames about the work inside a Project reach its readers
// alone. A Project that no longer exists reaches nobody.
func ProjectFrameAudienceInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, projectID uuid.UUID,
	includeProjectsManageHolders bool,
) (*ProjectAudience, error) {
	userIDs, companyWide, err := ProjectAudienceInTx(ctx, q, companyID, projectID, includeProjectsManageHolders)
	if errors.Is(err, ErrProjectNotVisible) {
		return &ProjectAudience{UserIDs: []uuid.UUID{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if userIDs == nil {
		userIDs = []uuid.UUID{}
	}
	return &ProjectAudience{CompanyWide: companyWide, UserIDs: userIDs}, nil
}

// ConversationFrameAudienceInTx is who receives a frame about one chat: a
// Task chat's Project readers, and every active member for a legacy Task chat
// with no Project (the pocket conversation_readers also opens to them). Any
// other chat, and a chat that is gone, answers nil: it is not a Project
// frame and keeps its own route.
func ConversationFrameAudienceInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, conversationID uuid.UUID,
) (*ProjectAudience, error) {
	return newFrameAudiences(q).forConversation(ctx, companyID, conversationID)
}

// StampProjectAudiencesInTx decides, inside the producing transaction, the
// audience of every staged frame whose payload is a ProjectFramePayload and
// has none yet. The Project comes from the payload when it knows it, else
// from the envelope: the chat it names when that is a Task chat, else its
// Task. A frame that belongs to no Project is left as it is. Producers call
// it after staging and before commit; calling it again is harmless.
//
// It takes no lock; see ProjectAudienceInTx for the window that leaves.
func StampProjectAudiencesInTx(ctx context.Context, q *generated.Queries, pending []events.Event) error {
	var audiences *frameAudiences
	for index := range pending {
		payload, ok := pending[index].Payload.(ProjectFramePayload)
		if !ok || payload.ProjectAudience() != nil {
			continue
		}
		projectID, scoped := payload.ProjectFrameScope()
		if !scoped {
			continue
		}
		if audiences == nil {
			audiences = newFrameAudiences(q)
		}
		audience, err := audiences.forEvent(ctx, pending[index], projectID)
		if err != nil {
			return fmt.Errorf("decide %s audience: %w", pending[index].Name, err)
		}
		if audience != nil {
			pending[index].Payload = payload.WithProjectAudience(audience)
		}
	}
	return nil
}

// frameAudiences remembers the audiences one stamping pass already read, so
// a batch of frames about one Project reads it once.
type frameAudiences struct {
	q             *generated.Queries
	projects      map[uuid.UUID]*ProjectAudience
	conversations map[uuid.UUID]*ProjectAudience
}

func newFrameAudiences(q *generated.Queries) *frameAudiences {
	return &frameAudiences{
		q:             q,
		projects:      map[uuid.UUID]*ProjectAudience{},
		conversations: map[uuid.UUID]*ProjectAudience{},
	}
}

func (a *frameAudiences) forEvent(
	ctx context.Context,
	event events.Event,
	projectID uuid.UUID,
) (*ProjectAudience, error) {
	if projectID != uuid.Nil {
		return a.forProject(ctx, event.CompanyID, projectID)
	}
	if event.ConversationID != uuid.Nil {
		audience, err := a.forConversation(ctx, event.CompanyID, event.ConversationID)
		if err != nil || audience != nil {
			return audience, err
		}
	}
	if event.TaskID == uuid.Nil {
		return nil, nil
	}
	taskProjectID, err := a.q.GetTaskProjectID(ctx, generated.GetTaskProjectIDParams{
		CompanyID: event.CompanyID, TaskID: event.TaskID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Task's Project: %w", err)
	}
	return a.forProject(ctx, event.CompanyID, taskProjectID)
}

func (a *frameAudiences) forConversation(
	ctx context.Context,
	companyID, conversationID uuid.UUID,
) (*ProjectAudience, error) {
	if audience, ok := a.conversations[conversationID]; ok {
		return audience, nil
	}
	row, err := a.q.GetConversationProjectID(ctx, generated.GetConversationProjectIDParams{
		CompanyID: companyID, ConversationID: conversationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		a.conversations[conversationID] = nil
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the chat's Project: %w", err)
	}
	var audience *ProjectAudience
	switch {
	case row.Kind != "task":
	case !row.ProjectID.Valid:
		audience = &ProjectAudience{CompanyWide: true}
	default:
		audience, err = a.forProject(ctx, companyID, row.ProjectID.UUID)
		if err != nil {
			return nil, err
		}
	}
	a.conversations[conversationID] = audience
	return audience, nil
}

func (a *frameAudiences) forProject(ctx context.Context, companyID, projectID uuid.UUID) (*ProjectAudience, error) {
	if audience, ok := a.projects[projectID]; ok {
		return audience, nil
	}
	audience, err := ProjectFrameAudienceInTx(ctx, a.q, companyID, projectID, false)
	if err != nil {
		return nil, err
	}
	a.projects[projectID] = audience
	return audience, nil
}
