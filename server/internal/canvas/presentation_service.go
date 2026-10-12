package canvas

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
)

// PresentationService owns immutable captures, retained source reads and Run preparation.
type PresentationService interface {
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
	store          *db.Store
	bus            *events.Bus
	attachments    attachment.AttachmentService
	workflowLookup func(string) (json.RawMessage, error)
}

func NewPresentationService(
	store *db.Store,
	bus *events.Bus,
	attachments attachment.AttachmentService,
	catalog ...func(string) (json.RawMessage, error),
) *DefaultPresentationService {
	s := &DefaultPresentationService{store: store, bus: bus, attachments: attachments}
	if len(catalog) > 0 {
		s.workflowLookup = catalog[0]
	}
	return s
}
