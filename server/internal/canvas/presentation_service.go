package canvas

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

// PresentationService owns immutable captures, retained source reads and Run preparation.
type PresentationService interface {
	Publication(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (Publication, error)
	PublishPresentation(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		PublicationInput,
	) (Publication, error)
	RevokePublication(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, uuid.UUID) error
	PublicPresentationHTML(context.Context, uuid.UUID) (string, error)
	PreviewScenario(context.Context, auth.UserPrincipal, uuid.UUID, PresentationScenarioInput) (Presentation, error)
	PreviewProposalForRun(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
	) (Presentation, error)
	PreviewPresentation(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) (Presentation, error)
	CapturePresentation(context.Context, auth.UserPrincipal, uuid.UUID, PresentationInput) (Presentation, error)
	Presentation(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (Presentation, error)
	Presentations(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) ([]PresentationEntry, error)
	PresentationExport(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (string, error)
	PreviewPresentationForRun(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (Presentation, error)
	ReadPageSource(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, string) (PageSource, error)
	PreparePresentationForRun(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		PresentationInput,
	) (PageArtifactPreparation, error)
}

var _ PresentationService = (*DefaultPresentationService)(nil)

type DefaultPresentationService struct {
	store           *db.Store
	bus             *events.Bus
	attachments     attachment.AttachmentService
	workflowLookup  func(string) (json.RawMessage, error)
	captureObserver func(string, string, time.Duration)
}

type PresentationServiceOption func(*DefaultPresentationService)

func WithPresentationWorkflowLookup(lookup func(string) (json.RawMessage, error)) PresentationServiceOption {
	return func(service *DefaultPresentationService) { service.workflowLookup = lookup }
}

func WithPresentationCaptureObserver(observer func(string, string, time.Duration)) PresentationServiceOption {
	return func(service *DefaultPresentationService) { service.captureObserver = observer }
}

func NewPresentationService(
	store *db.Store,
	bus *events.Bus,
	attachments attachment.AttachmentService,
	options ...PresentationServiceOption,
) *DefaultPresentationService {
	s := &DefaultPresentationService{store: store, bus: bus, attachments: attachments}
	for _, option := range options {
		option(s)
	}
	return s
}
