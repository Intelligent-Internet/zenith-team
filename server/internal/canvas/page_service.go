package canvas

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
)

// PageService owns Page authoring, collaboration, contributions and Overview selection.
type PageService interface {
	DocumentHistory(
		context.Context,
		auth.UserPrincipal,
		DocumentHistoryRef,
		DocumentHistoryInput,
	) (DocumentHistory, error)
	DocumentHistoryState(
		context.Context,
		auth.UserPrincipal,
		DocumentHistoryRef,
		int64,
	) (DocumentHistoryState, error)
	RestoreDocumentHistory(
		context.Context,
		auth.UserPrincipal,
		DocumentHistoryRef,
		DocumentHistoryRestore,
	) (DocumentHistoryState, error)
	NameDocumentHistory(context.Context, auth.UserPrincipal, DocumentHistoryRef, DocumentHistoryName) error
	CheckpointOutputDocument(context.Context, auth.UserPrincipal, OutputDocumentRef, int64) (uuid.UUID, error)
	OutputDocumentVersion(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (DocumentVersion, error)
	ReadOutputDocument(context.Context, auth.UserPrincipal, OutputDocumentRef, DocumentReadInput) (Document, error)
	SyncOutputDocument(context.Context, auth.UserPrincipal, OutputDocumentRef, DocumentSyncInput) (Document, error)
	ReserveOutputBlockIDs(context.Context, auth.UserPrincipal, OutputDocumentRef, int32) ([]uuid.UUID, error)
	Overview(context.Context, auth.UserPrincipal, uuid.UUID) (Overview, error)
	UpdateOverview(context.Context, auth.UserPrincipal, uuid.UUID, OverviewInput) (Overview, error)
	ReadProjectForRun(context.Context, auth.UserPrincipal, uuid.UUID) (ProjectRead, error)
	CreatePageForRun(context.Context, auth.UserPrincipal, uuid.UUID, PageCreateInput) (PageCreateResult, error)
	CreatePage(context.Context, auth.UserPrincipal, uuid.UUID, PageCreateInput) (PageCreateResult, error)
	ReadPageForRun(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (Page, error)
	EditPageForRun(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, PageEditInput) (PageEditResult, error)
	Page(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (Page, error)
	UpdatePage(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, PageMetadataInput) (Page, error)
	Pages(context.Context, auth.UserPrincipal, uuid.UUID, *uuid.UUID) ([]Artifact, error)
	PageChanges(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, string) (PageChangeFeed, error)
	ReviewPageChange(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		uuid.UUID,
		PageChangeReviewInput,
	) (PageEditResult, error)
	PageChangeDetail(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, uuid.UUID) (PageChangeDetail, error)
	PageThreads(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) ([]Thread, error)
	Document(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) (Document, error)
	ReadDocument(context.Context, auth.UserPrincipal, uuid.UUID, DocumentReadInput) (Document, error)
	SyncDocument(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []byte, int64) (Document, error)
	ReserveBlockIDs(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int32) ([]uuid.UUID, error)
	VersionBlocks(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (json.RawMessage, error)
}

var _ PageService = (*DefaultPageService)(nil)

type DefaultPageService struct {
	store *db.Store
	bus   *events.Bus
}

func NewPageService(store *db.Store, bus *events.Bus) *DefaultPageService {
	return &DefaultPageService{store: store, bus: bus}
}
