package canvas

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/wiki"
)

// PageService owns Page authoring, collaboration, contributions and Overview selection.
type PageService interface {
	PageReview(context.Context, auth.UserPrincipal, uuid.UUID) ([]Thread, error)
	MutatePageReview(context.Context, auth.UserPrincipal, uuid.UUID, Command) (Result, error)
	CheckpointPageDocument(context.Context, auth.UserPrincipal, uuid.UUID, int64) (uuid.UUID, error)
	ReadPageDocument(context.Context, auth.UserPrincipal, uuid.UUID, DocumentReadInput) (Document, error)
	SyncPageDocument(context.Context, auth.UserPrincipal, uuid.UUID, DocumentSyncInput) (Document, error)
	ReservePageBlockIDs(context.Context, auth.UserPrincipal, uuid.UUID, int32) ([]uuid.UUID, error)
	ReadPagePreview(context.Context, auth.UserPrincipal, PagePreviewMemberRef) (PagePreviewGeneration, error)
	ListPagePreviews(context.Context, auth.UserPrincipal, PagePreviewListInput) ([]PagePreviewSummary, error)
	PurgeExpiredPagePreviews(context.Context) (int64, error)
	CreatePagePreviewForRun(
		context.Context,
		auth.UserPrincipal,
		PagePreviewRef,
		PagePreviewCreateInput,
	) (PagePreviewGeneration, error)
	ReadPagePreviewForRun(context.Context, auth.UserPrincipal, PagePreviewRef) (PagePreviewGeneration, error)
	PatchPagePreviewForRun(
		context.Context,
		auth.UserPrincipal,
		PagePreviewRef,
		wiki.SourcePreviewPatch,
	) (PagePreviewGeneration, error)
	PreviewPageSource(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		PageSourcePreviewInput,
	) (PageSourcePreview, error)
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
	OutputDocumentVersion(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (DocumentVersion, error)
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
	SyncDocument(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, DocumentSyncInput) (Document, error)
	ReserveBlockIDs(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int32) ([]uuid.UUID, error)
	VersionBlocks(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (json.RawMessage, error)
}

var _ PageService = (*DefaultPageService)(nil)

type DefaultPageService struct {
	store               *db.Store
	bus                 *events.Bus
	previewObserver     func(string, string, time.Duration, int)
	sourceObserver      func(string, string, time.Duration, int)
	authoring           wiki.UIAuthoringPolicy
	invokeDocumentAgent DocumentAgentInvoker
}

type PageServiceOption func(*DefaultPageService)

func WithPageDocumentAgentInvoker(invoke DocumentAgentInvoker) PageServiceOption {
	return func(service *DefaultPageService) { service.invokeDocumentAgent = invoke }
}

func WithPageUIAuthoringPolicy(policy wiki.UIAuthoringPolicy) PageServiceOption {
	return func(service *DefaultPageService) { service.authoring = policy }
}

func WithPagePreviewObserver(observer func(string, string, time.Duration, int)) PageServiceOption {
	return func(service *DefaultPageService) { service.previewObserver = observer }
}

func WithPageSourceObserver(observer func(string, string, time.Duration, int)) PageServiceOption {
	return func(service *DefaultPageService) { service.sourceObserver = observer }
}

func NewPageService(store *db.Store, bus *events.Bus, options ...PageServiceOption) *DefaultPageService {
	service := &DefaultPageService{store: store, bus: bus}
	for _, option := range options {
		option(service)
	}
	return service
}
