package wiki

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const (
	TopicWiki         = "wiki"
	EventPageCreated  = "wiki.page_created"
	EventPageUpdated  = "wiki.page_updated"
	EventPageArchived = "wiki.page_archived"
	EventPageRestored = "wiki.page_restored"

	EventCollectionCreated = "wiki.collection_created"
	EventCollectionUpdated = "wiki.collection_updated"
	EventCollectionDeleted = "wiki.collection_deleted"
	EventRunReadsChanged   = "wiki.run_reads_changed"
)

// RunReadsChanged says a Run's Wiki evidence trail changed. A Task Run's
// frame is about work inside a Project, so it carries the Project audience
// decided in the producing transaction (ADR 0017) and reaches only the
// Project's readers.
type RunReadsChanged struct {
	auth.ProjectFrame
	RunID                 uuid.UUID  `json:"runId"`
	ConversationKind      string     `json:"-"`
	ConversationCreatedBy *uuid.UUID `json:"-"`
}

var _ auth.ProjectAudienceCarrier = RunReadsChanged{}

func runReadsChangedInTx(ctx context.Context, q *generated.Queries, companyID, runID uuid.UUID) (events.Event, error) {
	run, err := q.GetRun(ctx, runID)
	if err != nil {
		return events.Event{}, fmt.Errorf("read Wiki evidence Run: %w", err)
	}
	route, err := q.GetConversationRoute(
		ctx,
		generated.GetConversationRouteParams{CompanyID: companyID, ConversationID: run.ConversationID},
	)
	if err != nil {
		return events.Event{}, fmt.Errorf("read Wiki evidence scope: %w", err)
	}
	payload := RunReadsChanged{
		RunID:                 runID,
		ConversationKind:      route.Kind,
		ConversationCreatedBy: util.NullUUIDPtr(route.CreatedByUserID),
	}
	if route.Kind == "task" {
		audience, err := auth.ConversationFrameAudienceInTx(ctx, q, companyID, run.ConversationID)
		if err != nil {
			return events.Event{}, fmt.Errorf("decide Wiki evidence audience: %w", err)
		}
		payload.Audience = audience
	}
	return events.Event{
		EntityType: TopicWiki,
		Name:       EventRunReadsChanged,
		CompanyID:  companyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload:    payload,
	}, nil
}

type PageEvent struct {
	ID                uuid.UUID  `json:"id"`
	Title             string     `json:"title"`
	Status            string     `json:"status"`
	Revision          int64      `json:"revision"`
	ContentRevision   int64      `json:"contentRevision"`
	CurrentRevisionID *uuid.UUID `json:"currentRevisionId"`
}

// newPageEvent attributes the change to actor: the member for their own edit,
// events.RunActor for a page an Agent's Run wrote on their behalf.
func newPageEvent(companyID uuid.UUID, actor events.Actor, name string, page PageResponse) events.Event {
	return events.Event{
		Name: name, EntityType: TopicWiki, CompanyID: companyID,
		CreatedAt: time.Now().UTC(), Actor: actor,
		Payload: PageEvent{
			ID: page.ID, Title: page.Title, Status: page.Status,
			Revision: page.Revision, ContentRevision: page.ContentRevision,
			CurrentRevisionID: page.CurrentRevisionID,
		},
	}
}

func (s *DefaultWikiService) publishPageEvent(
	ctx context.Context,
	principal auth.UserPrincipal,
	name string,
	page PageResponse,
) {
	s.bus.Publish(
		ctx,
		TopicWiki,
		newPageEvent(principal.CompanyID, events.UserActor(principal.UserID.String()), name, page),
	)
}

type CollectionEvent struct {
	ID       uuid.UUID `json:"id"`
	Name     string    `json:"name"`
	Revision int64     `json:"revision"`
}

func newCollectionEvent(
	companyID uuid.UUID,
	actor events.Actor,
	name string,
	collection CollectionResponse,
) events.Event {
	return events.Event{
		Name: name, EntityType: TopicWiki, CompanyID: companyID,
		CreatedAt: time.Now().UTC(), Actor: actor,
		Payload: CollectionEvent{ID: collection.ID, Name: collection.Name, Revision: collection.Revision},
	}
}

func (s *DefaultWikiService) publishCollectionEvent(
	ctx context.Context,
	principal auth.UserPrincipal,
	name string,
	collection CollectionResponse,
) {
	s.bus.Publish(
		ctx,
		TopicWiki,
		newCollectionEvent(principal.CompanyID, events.UserActor(principal.UserID.String()), name, collection),
	)
}
