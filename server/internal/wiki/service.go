// Package wiki owns Company Wiki pages, collections, immutable revisions and
// the current link graph. PostgreSQL is its only authority. Agent access is
// read-only (AgentReadService: search, read and the evidence trail of what a Run
// read); Agents cannot write, and reviewed proposals are not part of this
// package yet.
package wiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
	"boardly.dev/boardly/server/pkg/redact"
)

var (
	ErrPageNotFound       = errors.New("Wiki page not found")
	ErrPageForbidden      = errors.New("Wiki publication requires wiki.publish")
	ErrPageInvalidInput   = errors.New("invalid Wiki page input")
	ErrPageBodyTooLarge   = errors.New("Wiki body exceeds the size limit")
	ErrPageConflict       = errors.New("Wiki page revision conflict")
	ErrPageArchived       = errors.New("Wiki page is archived")
	ErrCollectionNotFound = errors.New("Wiki collection not found")
	ErrCollectionConflict = errors.New("Wiki collection conflict")
	// ErrArtifactRefUnavailable covers a newly cited Artifact that is missing,
	// has no such item, or that the author cannot open: one answer for all
	// three, so a citation cannot probe what exists.
	ErrArtifactRefUnavailable = errors.New("cited Artifact is unavailable")
	ErrArtifactRefLimit       = errors.New("too many cited Artifacts")
)

type CollectionResponse struct {
	ID        uuid.UUID `json:"id"`
	CompanyID uuid.UUID `json:"companyId"`
	Name      string    `json:"name"`
	Revision  int64     `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type PageResponse struct {
	Content           json.RawMessage `json:"content"`
	ID                uuid.UUID       `json:"id"`
	CompanyID         uuid.UUID       `json:"companyId"`
	CollectionID      *uuid.UUID      `json:"collectionId"`
	Slug              string          `json:"slug"`
	Title             string          `json:"title"`
	Body              string          `json:"body"`
	Summary           string          `json:"summary"`
	Status            string          `json:"status"`
	Revision          int64           `json:"revision"`
	ContentRevision   int64           `json:"contentRevision"`
	CurrentRevisionID *uuid.UUID      `json:"currentRevisionId"`
	CreatedByUserID   uuid.UUID       `json:"createdByUserId"`
	LastAuthorUserID  uuid.UUID       `json:"lastAuthorUserId"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedAt         time.Time       `json:"updatedAt"`
	RevisionCreatedAt time.Time       `json:"revisionCreatedAt"`
	ArchivedAt        *time.Time      `json:"archivedAt"`
	Favorite          bool            `json:"favorite"`
}

type RevisionResponse struct {
	Content      json.RawMessage `json:"content"`
	ID           uuid.UUID       `json:"id"`
	PageID       uuid.UUID       `json:"pageId"`
	Number       int64           `json:"number"`
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	Summary      string          `json:"summary"`
	CollectionID *uuid.UUID      `json:"collectionId"`
	AuthorUserID uuid.UUID       `json:"authorUserId"`
	CreatedAt    time.Time       `json:"createdAt"`
}

type RevisionList struct {
	Items []RevisionResponse `json:"items"`
	Total int64              `json:"total"`
}

type PageSummary struct {
	Published    bool       `json:"published"`
	ID           uuid.UUID  `json:"id"`
	CollectionID *uuid.UUID `json:"collectionId"`
	Slug         string     `json:"slug"`
	Title        string     `json:"title"`
	Summary      string     `json:"summary"`
	Status       string     `json:"status"`
	Revision     int64      `json:"revision"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	Favorite     bool       `json:"favorite"`
}

type PageListInput struct {
	Search        string
	Status        string
	CollectionID  *uuid.UUID
	FavoritesOnly bool
	Offset        int32
	Limit         int32
}

type PageList struct {
	Items []PageSummary `json:"items"`
	Total int64         `json:"total"`
}

type LinkResponse struct {
	FromPageID uuid.UUID `json:"fromPageId"`
	ToPageID   uuid.UUID `json:"toPageId"`
}

type LinkedPage struct {
	ID    uuid.UUID `json:"id"`
	Title string    `json:"title"`
}

// PageLinks lists each direction up to 200 pages, with its own truncation
// flag, so a heavily linked page still shows its outgoing links.
type PageLinks struct {
	Incoming          []LinkedPage `json:"incoming"`
	Outgoing          []LinkedPage `json:"outgoing"`
	IncomingTruncated bool         `json:"incomingTruncated"`
	OutgoingTruncated bool         `json:"outgoingTruncated"`
}

type GraphResponse struct {
	Pages     []PageSummary  `json:"pages"`
	Links     []LinkResponse `json:"links"`
	Truncated bool           `json:"truncated"`
}

type CreatePageInput struct {
	Title        string
	Body         string
	Summary      string
	CollectionID *uuid.UUID
}

// WikiService is the human Wiki's full public domain surface.
type WikiService interface {
	ListCollections(context.Context, auth.UserPrincipal) ([]CollectionResponse, error)
	CreateCollection(context.Context, auth.UserPrincipal, string) (CollectionResponse, error)
	RenameCollection(context.Context, auth.UserPrincipal, uuid.UUID, int64, string) (CollectionResponse, error)
	DeleteCollection(context.Context, auth.UserPrincipal, uuid.UUID, int64) error
	ListPages(context.Context, auth.UserPrincipal, PageListInput) (PageList, error)
	GetGraph(context.Context, auth.UserPrincipal) (GraphResponse, error)
	GetPage(context.Context, auth.UserPrincipal, uuid.UUID) (PageResponse, error)
	GetPageLinks(context.Context, auth.UserPrincipal, uuid.UUID) (PageLinks, error)
	CreatePage(context.Context, auth.UserPrincipal, CreatePageInput) (PageResponse, error)
	ArchivePage(context.Context, auth.UserPrincipal, uuid.UUID, int64) (PageResponse, error)
	RestorePage(context.Context, auth.UserPrincipal, uuid.UUID, int64) (PageResponse, error)
	ListRevisions(context.Context, auth.UserPrincipal, uuid.UUID, int32, int32) (RevisionList, error)
	RestoreRevision(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, RestoreRevisionInput) (Document, error)
	ReadDocument(context.Context, auth.UserPrincipal, uuid.UUID, DocumentReadInput) (Document, error)
	SyncDocument(context.Context, auth.UserPrincipal, uuid.UUID, SyncDocumentInput) (Document, error)
	ReserveBlockIDs(context.Context, auth.UserPrincipal, uuid.UUID, int32) ([]uuid.UUID, error)
	UpdateDraftMetadata(context.Context, auth.UserPrincipal, uuid.UUID, DraftMetadataInput) (Document, error)
	PublishPage(context.Context, auth.UserPrincipal, uuid.UUID, PublishPageInput) (PageResponse, error)
	SetFavorite(context.Context, auth.UserPrincipal, uuid.UUID, bool) error
}

var _ WikiService = (*DefaultWikiService)(nil)

type DefaultWikiService struct {
	store *db.Store
	bus   *events.Bus
}

func NewWikiService(store *db.Store, bus *events.Bus) (*DefaultWikiService, error) {
	if store == nil || bus == nil {
		return nil, errors.New("Wiki store and event bus are required")
	}
	return &DefaultWikiService{store: store, bus: bus}, nil
}

func collectionResponse(row generated.WikiCollection) CollectionResponse {
	return CollectionResponse{
		ID: row.ID, CompanyID: row.CompanyID, Name: row.Name,
		Revision: row.Version, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func pageResponse(row generated.GetWikiPageForMemberRow) PageResponse {
	response := PageResponse{
		ID: row.ID, CompanyID: row.CompanyID, CollectionID: uuidPointer(row.CollectionID),
		Slug: row.Slug, Title: row.Title, Body: row.Body, Summary: row.Summary,
		Status: row.Status, Revision: row.Version, ContentRevision: row.ContentRevision,
		CurrentRevisionID: uuidPointer(row.CurrentRevisionID),
		Content:           revisionContent(row.BlockContent, row.Body),
		CreatedByUserID:   row.CreatedByUserID, LastAuthorUserID: row.LastAuthorUserID,
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
		RevisionCreatedAt: row.RevisionCreatedAt.Time, Favorite: row.Favorite,
	}
	if row.ArchivedAt.Valid {
		archived := row.ArchivedAt.Time
		response.ArchivedAt = &archived
	}
	return response
}

func revisionResponse(row generated.WikiRevision) RevisionResponse {
	return RevisionResponse{
		ID: row.ID, PageID: row.PageID, Number: row.Number, Title: row.Title,
		Content: revisionContent(row.BlockContent, row.Body),
		Body:    row.Body, Summary: row.Summary, CollectionID: uuidPointer(row.CollectionID),
		AuthorUserID: row.AuthorUserID, CreatedAt: row.CreatedAt.Time,
	}
}

func uuidPointer(value uuid.NullUUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	id := value.UUID
	return &id
}

func nullableUUID(value *uuid.UUID) uuid.NullUUID {
	if value == nil {
		return uuid.NullUUID{}
	}
	return uuid.NullUUID{UUID: *value, Valid: true}
}

func normalizePageInput(title, body, summary string) (string, string, string, error) {
	title = redact.Credentials(protocol.TrimContractWhitespace(title))
	summary = redact.Credentials(protocol.TrimContractWhitespace(summary))
	if !utf8.ValidString(title) || title == "" || utf8.RuneCountInString(title) > 240 ||
		strings.ContainsRune(title, 0) || !utf8.ValidString(summary) ||
		utf8.RuneCountInString(summary) > 2048 || strings.ContainsRune(summary, 0) ||
		!utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return "", "", "", ErrPageInvalidInput
	}
	if utf8.RuneCountInString(body) > 262144 {
		return "", "", "", ErrPageBodyTooLarge
	}
	body = redact.Credentials(body)
	return title, body, summary, nil
}

func normalizeCollectionName(name string) (string, error) {
	name = redact.Credentials(protocol.TrimContractWhitespace(name))
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 120 ||
		strings.ContainsRune(name, 0) {
		return "", ErrPageInvalidInput
	}
	return name, nil
}

func authorizePublish(ctx context.Context, q *generated.Queries, principal auth.UserPrincipal) error {
	if _, err := q.LockCompanyForMembershipMutation(ctx, principal.CompanyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrPageNotFound
	} else if err != nil {
		return fmt.Errorf("lock Wiki Company: %w", err)
	}
	membership, err := q.GetCompanyMemberForUpdate(ctx, generated.GetCompanyMemberForUpdateParams{
		CompanyID: principal.CompanyID, UserID: principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return ErrPageNotFound
	}
	if err != nil {
		return fmt.Errorf("lock Wiki actor membership: %w", err)
	}
	if !auth.CompanyRole(membership.Role).Can(auth.PermissionWikiPublish) {
		return ErrPageForbidden
	}
	return nil
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ErrPageConflict
		case "23503":
			return ErrCollectionNotFound
		case "23514", "22021":
			return ErrPageInvalidInput
		}
	}
	return err
}

func (s *DefaultWikiService) ListCollections(
	ctx context.Context,
	principal auth.UserPrincipal,
) ([]CollectionResponse, error) {
	rows, err := s.store.Queries().ListWikiCollectionsForMember(ctx, generated.ListWikiCollectionsForMemberParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID,
	})
	if err != nil {
		return nil, fmt.Errorf("list Wiki collections: %w", err)
	}
	result := make([]CollectionResponse, len(rows))
	for i, row := range rows {
		result[i] = collectionResponse(row)
	}
	return result, nil
}

func (s *DefaultWikiService) CreateCollection(
	ctx context.Context,
	principal auth.UserPrincipal,
	name string,
) (CollectionResponse, error) {
	name, err := normalizeCollectionName(name)
	if err != nil {
		return CollectionResponse{}, err
	}
	var row generated.WikiCollection
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := authorizePublish(ctx, q, principal); err != nil {
			return err
		}
		var err error
		row, err = q.CreateWikiCollection(
			ctx,
			generated.CreateWikiCollectionParams{CompanyID: principal.CompanyID, Name: name},
		)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrCollectionConflict
			}
			return fmt.Errorf("create Wiki collection: %w", err)
		}
		return nil
	})
	if err != nil {
		return CollectionResponse{}, err
	}
	response := collectionResponse(row)
	s.publishCollectionEvent(ctx, principal, EventCollectionCreated, response)
	return response, nil
}

func (s *DefaultWikiService) RenameCollection(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	expectedRevision int64,
	name string,
) (CollectionResponse, error) {
	name, err := normalizeCollectionName(name)
	if err != nil || id == uuid.Nil || expectedRevision < 1 {
		return CollectionResponse{}, ErrPageInvalidInput
	}
	var row generated.WikiCollection
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := authorizePublish(ctx, q, principal); err != nil {
			return err
		}
		current, err := q.GetWikiCollectionForUpdate(ctx, generated.GetWikiCollectionForUpdateParams{
			CompanyID: principal.CompanyID, CollectionID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCollectionNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Wiki collection: %w", err)
		}
		if current.Version != expectedRevision {
			return ErrCollectionConflict
		}
		row, err = q.RenameWikiCollection(ctx, generated.RenameWikiCollectionParams{
			CompanyID: principal.CompanyID, CollectionID: id, ExpectedVersion: expectedRevision, Name: name,
		})
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrCollectionConflict
		}
		if err != nil {
			return fmt.Errorf("rename Wiki collection: %w", err)
		}
		return nil
	})
	if err != nil {
		return CollectionResponse{}, err
	}
	response := collectionResponse(row)
	s.publishCollectionEvent(ctx, principal, EventCollectionUpdated, response)
	return response, nil
}

// DeleteCollection moves active and archived pages to Unfiled. Page versions
// advance so drafts opened before the move cannot overwrite it. The existing
// revision FK clears the deleted collection reference without losing content.
func (s *DefaultWikiService) DeleteCollection(ctx context.Context, principal auth.UserPrincipal, id uuid.UUID, expectedRevision int64) error {
	if id == uuid.Nil || expectedRevision < 1 {
		return ErrPageInvalidInput
	}
	var deleted CollectionResponse
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		// The Company lock serializes Wiki mutations before any page/collection locks.
		if err := authorizePublish(ctx, q, principal); err != nil {
			return err
		}
		current, err := q.GetWikiCollectionForUpdate(ctx, generated.GetWikiCollectionForUpdateParams{CompanyID: principal.CompanyID, CollectionID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCollectionNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Wiki collection: %w", err)
		}
		if current.Version != expectedRevision {
			return ErrCollectionConflict
		}
		if err := q.UnfileWikiCollectionPages(ctx, generated.UnfileWikiCollectionPagesParams{CompanyID: principal.CompanyID, CollectionID: id}); err != nil {
			return fmt.Errorf("unfile Wiki collection pages: %w", err)
		}
		if err := q.DeleteWikiCollection(ctx, generated.DeleteWikiCollectionParams{CompanyID: principal.CompanyID, CollectionID: id}); err != nil {
			return fmt.Errorf("delete Wiki collection: %w", err)
		}
		deleted = collectionResponse(current)
		return nil
	})
	if err != nil {
		return err
	}
	s.publishCollectionEvent(ctx, principal, EventCollectionDeleted, deleted)
	return nil
}

func (s *DefaultWikiService) ListPages(ctx context.Context, principal auth.UserPrincipal, input PageListInput) (PageList, error) {
	if input.Status == "" {
		input.Status = "active"
	}
	if input.Status != "active" && input.Status != "archived" || input.Offset < 0 || input.Limit < 1 ||
		input.Limit > 100 ||
		utf8.RuneCountInString(input.Search) > 200 ||
		input.CollectionID != nil && *input.CollectionID == uuid.Nil {
		return PageList{}, ErrPageInvalidInput
	}
	params := generated.ListWikiPagesForMemberParams{
		PublisherRoles: auth.RolesWith(auth.PermissionWikiPublish),
		ActorUserID:    principal.UserID, CompanyID: principal.CompanyID, Status: input.Status,
		CollectionID: nullableUUID(input.CollectionID), FavoritesOnly: input.FavoritesOnly,
		Search: input.Search, PageOffset: input.Offset, PageLimit: input.Limit,
	}
	rows, err := s.store.Queries().ListWikiPagesForMember(ctx, params)
	if err != nil {
		return PageList{}, fmt.Errorf("list Wiki pages: %w", err)
	}
	count, err := s.store.Queries().CountWikiPagesForMember(ctx, generated.CountWikiPagesForMemberParams{
		PublisherRoles: auth.RolesWith(auth.PermissionWikiPublish),
		ActorUserID:    params.ActorUserID, CompanyID: params.CompanyID,
		Status: params.Status, CollectionID: params.CollectionID,
		FavoritesOnly: params.FavoritesOnly, Search: params.Search,
	})
	if err != nil {
		return PageList{}, fmt.Errorf("count Wiki pages: %w", err)
	}
	items := make([]PageSummary, len(rows))
	for i, row := range rows {
		items[i] = PageSummary{
			ID: row.ID, CollectionID: uuidPointer(row.CollectionID),
			Slug: row.Slug, Title: row.Title, Summary: row.Summary, Status: row.Status,
			Revision: row.Version, UpdatedAt: row.UpdatedAt.Time, Favorite: row.Favorite, Published: row.Published,
		}
	}
	return PageList{Items: items, Total: count}, nil
}

func (s *DefaultWikiService) GetGraph(ctx context.Context, principal auth.UserPrincipal) (GraphResponse, error) {
	rows, err := s.store.Queries().ListWikiPagesForMember(ctx, generated.ListWikiPagesForMemberParams{
		PublisherRoles: auth.RolesWith(auth.PermissionWikiPublish),
		ActorUserID:    principal.UserID, CompanyID: principal.CompanyID,
		Status: "active", PageLimit: 501,
	})
	if err != nil {
		return GraphResponse{}, fmt.Errorf("list Wiki graph pages: %w", err)
	}
	result := GraphResponse{
		Pages:     make([]PageSummary, 0, min(len(rows), 500)),
		Links:     []LinkResponse{},
		Truncated: len(rows) > 500,
	}
	for _, row := range rows[:min(len(rows), 500)] {
		result.Pages = append(result.Pages, PageSummary{
			ID: row.ID, CollectionID: uuidPointer(row.CollectionID), Slug: row.Slug,
			Title: row.Title, Summary: row.Summary, Status: row.Status,
			Revision: row.Version, UpdatedAt: row.UpdatedAt.Time, Favorite: row.Favorite, Published: row.Published,
		})
	}
	visibleIDs := make([]uuid.UUID, len(result.Pages))
	for i, page := range result.Pages {
		visibleIDs[i] = page.ID
	}
	links, err := s.store.Queries().ListWikiLinksForMember(ctx, generated.ListWikiLinksForMemberParams{
		ActorUserID: principal.UserID, CompanyID: principal.CompanyID,
		VisiblePageIds: visibleIDs,
	})
	if err != nil {
		return GraphResponse{}, fmt.Errorf("list Wiki graph links: %w", err)
	}
	if len(links) > 5000 {
		result.Truncated = true
		links = links[:5000]
	}
	visible := make(map[uuid.UUID]struct{}, len(result.Pages))
	for _, page := range result.Pages {
		visible[page.ID] = struct{}{}
	}
	for _, link := range links {
		_, from := visible[link.FromPageID]
		_, to := visible[link.ToPageID]
		if from && to {
			result.Links = append(
				result.Links,
				LinkResponse{FromPageID: link.FromPageID, ToPageID: link.ToPageID},
			)
		}
	}
	return result, nil
}

func (s *DefaultWikiService) GetPage(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
) (PageResponse, error) {
	if id == uuid.Nil {
		return PageResponse{}, ErrPageNotFound
	}
	row, err := s.store.Queries().GetWikiPageForMember(ctx, generated.GetWikiPageForMemberParams{
		PublisherRoles: auth.RolesWith(auth.PermissionWikiPublish),
		CompanyID:      principal.CompanyID, ActorUserID: principal.UserID, PageID: id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PageResponse{}, ErrPageNotFound
	}
	if err != nil {
		return PageResponse{}, fmt.Errorf("read Wiki page: %w", err)
	}
	return pageResponse(row), nil
}

func (s *DefaultWikiService) GetPageLinks(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
) (PageLinks, error) {
	if _, err := s.GetPage(ctx, principal, id); err != nil {
		return PageLinks{}, err
	}
	rows, err := s.store.Queries().ListWikiPageLinksForMember(ctx, generated.ListWikiPageLinksForMemberParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID, PageID: id,
	})
	if err != nil {
		return PageLinks{}, fmt.Errorf("list Wiki page links: %w", err)
	}
	result := PageLinks{Incoming: []LinkedPage{}, Outgoing: []LinkedPage{}}
	for _, row := range rows {
		linked := LinkedPage{ID: row.LinkedPageID, Title: row.LinkedTitle}
		if row.Direction == "incoming" {
			result.Incoming = append(result.Incoming, linked)
		} else {
			result.Outgoing = append(result.Outgoing, linked)
		}
	}
	result.IncomingTruncated = len(result.Incoming) > 200
	result.OutgoingTruncated = len(result.Outgoing) > 200
	result.Incoming = result.Incoming[:min(len(result.Incoming), 200)]
	result.Outgoing = result.Outgoing[:min(len(result.Outgoing), 200)]
	return result, nil
}

func (s *DefaultWikiService) CreatePage(ctx context.Context, principal auth.UserPrincipal, input CreatePageInput) (PageResponse, error) {
	title, body, summary, err := normalizePageInput(input.Title, input.Body, input.Summary)
	if err != nil {
		return PageResponse{}, err
	}
	if input.CollectionID != nil && *input.CollectionID == uuid.Nil {
		return PageResponse{}, ErrPageInvalidInput
	}
	var response PageResponse
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := authorizePublish(ctx, q, principal); err != nil {
			return err
		}
		if err := checkCollection(ctx, q, principal.CompanyID, input.CollectionID); err != nil {
			return err
		}
		id, err := q.CreateWikiPage(ctx, generated.CreateWikiPageParams{CompanyID: principal.CompanyID, CollectionID: nullableUUID(input.CollectionID), Title: title, Summary: summary, CreatedByUserID: principal.UserID})
		if err != nil {
			return mapWriteError(err)
		}
		root := BlocksFromMarkdown(body)
		if err = AllocateBlockTreeInTx(ctx, q, principal.CompanyID, id, &root); err != nil {
			return err
		}
		state, err := EncodeBlocks(root)
		if err != nil {
			return err
		}
		if _, err = q.CreateCanvasDraft(ctx, generated.CreateCanvasDraftParams{CompanyID: principal.CompanyID, PageID: id, State: state, Markdown: body}); err != nil {
			return err
		}
		content, err := json.Marshal(root)
		if err != nil {
			return err
		}
		if _, err = q.UpgradeWikiDraft(ctx, generated.UpgradeWikiDraftParams{CompanyID: principal.CompanyID, PageID: id, State: state, Content: content}); err != nil {
			return err
		}
		response, err = readPage(ctx, q, principal, id)
		return err
	})
	if err != nil {
		return PageResponse{}, err
	}
	// Draft creation is not a Company publication event: ordinary readers cannot
	// see its title or body until an explicit publish succeeds.
	return response, nil
}

func checkCollection(ctx context.Context, q *generated.Queries, companyID uuid.UUID, id *uuid.UUID) error {
	if id == nil {
		return nil
	}
	_, err := q.GetWikiCollectionForUpdate(ctx, generated.GetWikiCollectionForUpdateParams{
		CompanyID: companyID, CollectionID: *id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCollectionNotFound
	}
	if err != nil {
		return fmt.Errorf("read Wiki collection: %w", err)
	}
	return nil
}

func readPage(
	ctx context.Context,
	q *generated.Queries,
	principal auth.UserPrincipal,
	id uuid.UUID,
) (PageResponse, error) {
	row, err := q.GetWikiPageForMember(ctx, generated.GetWikiPageForMemberParams{
		PublisherRoles: auth.RolesWith(auth.PermissionWikiPublish),
		CompanyID:      principal.CompanyID, ActorUserID: principal.UserID, PageID: id,
	})
	if err != nil {
		return PageResponse{}, fmt.Errorf("read committed Wiki page: %w", err)
	}
	return pageResponse(row), nil
}

var wikiLinkPattern = regexp.MustCompile(`\[\[([0-9a-fA-F-]{36})(?:\|[^\]]*)?\]\]`)

func replaceLinks(ctx context.Context, q *generated.Queries, companyID, pageID uuid.UUID, body string) error {
	if err := q.DeleteWikiLinksFromPage(
		ctx,
		generated.DeleteWikiLinksFromPageParams{CompanyID: companyID, PageID: pageID},
	); err != nil {
		return fmt.Errorf("remove prior Wiki links: %w", err)
	}
	seen := make(map[uuid.UUID]struct{})
	ids := make([]uuid.UUID, 0)
	for _, match := range wikiLinkPattern.FindAllStringSubmatch(body, -1) {
		id, err := uuid.Parse(match[1])
		if err != nil || id == pageID {
			continue
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if err := q.InsertWikiLinksFromPage(
		ctx,
		generated.InsertWikiLinksFromPageParams{CompanyID: companyID, PageID: pageID, TargetIds: ids},
	); err != nil {
		return fmt.Errorf("record Wiki links: %w", err)
	}
	return nil
}

// artifactRefPattern mirrors the editor's citation token: an Artifact id and an
// optional 0-based item ordinal. It never matches a page link, whose id follows
// `[[` directly.
var artifactRefPattern = regexp.MustCompile(`\[\[artifact:([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})(?:#(\d{1,2}))?\]\]`)

// artifactRef is one citation; Ordinal -1 cites the whole Artifact.
type artifactRef struct {
	OutputID uuid.UUID
	Ordinal  int32
}

func parseArtifactRefs(body string) []artifactRef {
	seen := make(map[artifactRef]struct{})
	refs := make([]artifactRef, 0)
	for _, match := range artifactRefPattern.FindAllStringSubmatch(body, -1) {
		id, err := uuid.Parse(match[1])
		if err != nil {
			continue
		}
		ref := artifactRef{OutputID: id, Ordinal: -1}
		if match[2] != "" {
			ordinal, err := strconv.Atoi(match[2])
			if err != nil {
				continue
			}
			ref.Ordinal = int32(ordinal)
		}
		if _, exists := seen[ref]; !exists {
			seen[ref] = struct{}{}
			refs = append(refs, ref)
		}
	}
	return refs
}

// replaceArtifactRefs rebuilds the page's citation index. A citation that is
// not carried over must name an Artifact item the author can open now; a
// carried one is indexed while its Artifact still exists. Citing grants no
// access: readers are checked again on every read.
func replaceArtifactRefs(ctx context.Context, q *generated.Queries, principal auth.UserPrincipal, pageID uuid.UUID, body string, carried []artifactRef) error {
	refs := parseArtifactRefs(body)
	if len(refs) > 50 {
		return ErrArtifactRefLimit
	}
	trusted := make(map[artifactRef]struct{}, len(carried))
	for _, ref := range carried {
		trusted[ref] = struct{}{}
	}
	added := make([]uuid.UUID, 0)
	for _, ref := range refs {
		if _, ok := trusted[ref]; !ok {
			added = append(added, ref.OutputID)
		}
	}
	if len(added) > 0 {
		targets, err := q.ListWikiArtifactRefTargetsForMember(ctx, generated.ListWikiArtifactRefTargetsForMemberParams{
			CompanyID: principal.CompanyID, ActorUserID: principal.UserID, OutputIds: added,
		})
		if err != nil {
			return fmt.Errorf("check cited Artifacts: %w", err)
		}
		visible := make(map[uuid.UUID][]int32, len(targets))
		for _, target := range targets {
			visible[target.OutputID] = target.Ordinals
		}
		for _, ref := range refs {
			if _, ok := trusted[ref]; ok {
				continue
			}
			ordinals, ok := visible[ref.OutputID]
			if !ok || ref.Ordinal >= 0 && !slices.Contains(ordinals, ref.Ordinal) {
				return ErrArtifactRefUnavailable
			}
		}
	}
	if err := q.DeleteWikiArtifactRefsFromPage(ctx, generated.DeleteWikiArtifactRefsFromPageParams{
		CompanyID: principal.CompanyID, PageID: pageID,
	}); err != nil {
		return fmt.Errorf("remove prior Wiki Artifact citations: %w", err)
	}
	if len(refs) == 0 {
		return nil
	}
	outputIDs := make([]uuid.UUID, len(refs))
	ordinals := make([]int32, len(refs))
	for i, ref := range refs {
		outputIDs[i], ordinals[i] = ref.OutputID, ref.Ordinal
	}
	if err := q.InsertWikiArtifactRefs(ctx, generated.InsertWikiArtifactRefsParams{
		CompanyID: principal.CompanyID, PageID: pageID, OutputIds: outputIDs, Ordinals: ordinals,
	}); err != nil {
		return fmt.Errorf("record Wiki Artifact citations: %w", err)
	}
	return nil
}

func (s *DefaultWikiService) ArchivePage(ctx context.Context, principal auth.UserPrincipal, id uuid.UUID, expectedRevision int64) (PageResponse, error) {
	return s.setArchiveStatus(ctx, principal, id, expectedRevision, true)
}

func (s *DefaultWikiService) RestorePage(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	expectedRevision int64,
) (PageResponse, error) {
	return s.setArchiveStatus(ctx, principal, id, expectedRevision, false)
}

func (s *DefaultWikiService) setArchiveStatus(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	expectedRevision int64,
	archive bool,
) (PageResponse, error) {
	if id == uuid.Nil || expectedRevision < 0 {
		return PageResponse{}, ErrPageInvalidInput
	}
	var response PageResponse
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := authorizePublish(ctx, q, principal); err != nil {
			return err
		}
		current, err := q.GetWikiPageForUpdate(
			ctx,
			generated.GetWikiPageForUpdateParams{CompanyID: principal.CompanyID, PageID: id},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPageNotFound
		}
		if err != nil {
			return fmt.Errorf("lock Wiki page: %w", err)
		}
		if current.Version != expectedRevision {
			return ErrPageConflict
		}
		if archive && current.Status != "active" || !archive && current.Status != "archived" {
			return ErrPageConflict
		}
		if archive {
			_, err = q.SetWikiPageArchived(
				ctx,
				generated.SetWikiPageArchivedParams{
					CompanyID:       principal.CompanyID,
					PageID:          id,
					ExpectedVersion: expectedRevision,
				},
			)
		} else {
			_, err = q.SetWikiPageRestored(
				ctx,
				generated.SetWikiPageRestoredParams{
					CompanyID:       principal.CompanyID,
					PageID:          id,
					ExpectedVersion: expectedRevision,
				},
			)
		}
		if err != nil {
			return fmt.Errorf("change Wiki archive status: %w", err)
		}
		response, err = readPage(ctx, q, principal, id)
		return err
	})
	if err != nil {
		return PageResponse{}, err
	}
	if response.CurrentRevisionID == nil {
		return response, nil
	}
	if archive {
		s.publishPageEvent(ctx, principal, EventPageArchived, response)
	} else {
		s.publishPageEvent(ctx, principal, EventPageRestored, response)
	}
	return response, nil
}

func (s *DefaultWikiService) ListRevisions(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	offset, limit int32,
) (RevisionList, error) {
	if offset < 0 || limit < 1 || limit > 100 {
		return RevisionList{}, ErrPageInvalidInput
	}
	if _, err := s.GetPage(ctx, principal, id); err != nil {
		return RevisionList{}, err
	}
	rows, err := s.store.Queries().ListWikiRevisionsForMember(ctx, generated.ListWikiRevisionsForMemberParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID, PageID: id,
		PageOffset: offset, PageLimit: limit,
	})
	if err != nil {
		return RevisionList{}, fmt.Errorf("list Wiki revisions: %w", err)
	}
	count, err := s.store.Queries().CountWikiRevisionsForMember(ctx, generated.CountWikiRevisionsForMemberParams{
		CompanyID: principal.CompanyID, ActorUserID: principal.UserID, PageID: id,
	})
	if err != nil {
		return RevisionList{}, fmt.Errorf("count Wiki revisions: %w", err)
	}
	result := make([]RevisionResponse, len(rows))
	for i, row := range rows {
		result[i] = revisionResponse(row)
	}
	return RevisionList{Items: result, Total: count}, nil
}

// SetFavorite marks or clears the caller's favorite. Clearing works on an
// archived page, so it can leave the favorites list; marking one is refused.
func (s *DefaultWikiService) SetFavorite(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	favorite bool,
) error {
	if id == uuid.Nil {
		return ErrPageNotFound
	}
	if _, err := s.GetPage(ctx, principal, id); err != nil {
		return err
	}
	if favorite {
		status, err := s.store.Queries().SetWikiFavorite(ctx, generated.SetWikiFavoriteParams{
			CompanyID: principal.CompanyID, PageID: id, ActorUserID: principal.UserID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPageNotFound
		}
		if err != nil {
			return fmt.Errorf("favorite Wiki page: %w", err)
		}
		if status != "active" {
			return ErrPageArchived
		}
		return nil
	}
	if _, err := s.GetPage(ctx, principal, id); err != nil {
		return err
	}
	if err := s.store.Queries().ClearWikiFavorite(ctx, generated.ClearWikiFavoriteParams{
		CompanyID: principal.CompanyID, PageID: id, ActorUserID: principal.UserID,
	}); err != nil {
		return fmt.Errorf("clear Wiki favorite: %w", err)
	}
	return nil
}
