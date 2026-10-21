// Package inbox owns durable account and Company notifications: the per-recipient
// item list, its derived counts projection, and the realtime hints both
// raise. Producing domains call inbox.CreateInTx inside their own transaction;
// inbox never imports those domains back.
package inbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	protocol "github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// ---- Item entities ----

type Status string

const (
	StatusActive   Status = "active"
	StatusAll      Status = "all"
	StatusUnread   Status = "unread"
	StatusArchived Status = "archived"
)

// Entity is what an inbox item is about. It addresses the rows CloseEntityInTx
// closes and feeds the dedupe identity; it is storage addressing, not a wire
// field — clients branch on kind and navigate by deepLink.
type Entity struct {
	Type protocol.EntityType `json:"type"`
	ID   string              `json:"id"`
}

type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

type ListOptions struct {
	Status Status
	Type   Type
	Limit  int
	Before *Cursor
}

type Page struct {
	Items      []Item
	NextCursor *Cursor
}

type Mutation string

const (
	MutationRead    Mutation = "read"
	MutationUnread  Mutation = "unread"
	MutationArchive Mutation = "archive"
	MutationRestore Mutation = "restore"
)

type MutationResult struct {
	Item   Item   `json:"item"`
	Counts Counts `json:"counts"`
}

// ReadAllResult is the receipt of a set write over the caller's own rows.
// Items is the affected row set itself — its length is the change count, so
// there is no separate counter.
type ReadAllResult struct {
	Items  []Item `json:"items"`
	Counts Counts `json:"counts"`
}

type Actor struct {
	Kind string
	ID   *uuid.UUID
}

type CreateInput struct {
	CompanyID       uuid.UUID
	RecipientUserID uuid.UUID
	ConversationID  *uuid.UUID
	Type            Type
	Entity          Entity
	Title           string
	Body            string
	DeepLink        *string
	DedupeKey       string
	Actor           Actor
}

type CreateResult struct {
	Item            Item
	Counts          Counts
	RecipientUserID uuid.UUID
	Created         bool
}

// ValidCreateInput reports whether input would pass CreateInTx's validation.
// It is exported for the packages that build CreateInputs cross-package — the
// question subpackage pins that its notifications never render an input the
// Inbox rejects.
func ValidCreateInput(input CreateInput) bool {
	personal := input.CompanyID == uuid.Nil && input.Type == TypeCalendarReminder && input.ConversationID == nil
	return (util.ValidUUID(input.CompanyID) || personal) && util.ValidUUID(input.RecipientUserID) &&
		(input.ConversationID == nil || util.ValidUUID(*input.ConversationID)) &&
		validType(input.Type) && validEntity(input.Entity) && validText(input.Title, 160) &&
		input.Body != "" && strings.TrimSpace(input.Body) == input.Body && len(input.Body) <= 2048 &&
		validDeepLink(input.DeepLink) && validText(input.DedupeKey, 256) &&
		validActor(input.Actor)
}

func validEntity(entity Entity) bool {
	return entity.ID != "" && len(entity.ID) <= 256 &&
		protocol.ValidEntityType(string(entity.Type))
}

func validActor(actor Actor) bool {
	switch actor.Kind {
	case "", "system":
		return actor.ID == nil
	case "user", "agent":
		return actor.ID != nil && util.ValidUUID(*actor.ID)
	default:
		return false
	}
}

func validType(notificationType Type) bool {
	switch notificationType {
	case TypeTaskAssigned, TypeBlocker, TypeRunCompleted, TypeRunFailed,
		TypeApprovalRequest, TypeQuestionRequest, TypeRuntimeOffline, TypeInvitationRenewal,
		TypeCalendarReminder, TypeCalendarUpdate, TypeDiscussionMention,
		TypeTaskRunRequested, TypeTaskHandoff, TypeAnalyticsBudget, TypeHuddleMissed,
		TypeProjectAccessGranted:
		return true
	default:
		return false
	}
}

func validStatus(status Status) bool {
	return status == StatusActive || status == StatusAll || status == StatusUnread ||
		status == StatusArchived
}

func validMutation(mutation Mutation) bool {
	return mutation == MutationRead || mutation == MutationUnread ||
		mutation == MutationArchive || mutation == MutationRestore
}

func validCursor(cursor *Cursor) bool {
	return cursor == nil || (!cursor.CreatedAt.IsZero() && cursor.CreatedAt.UnixMicro() > 0 &&
		cursor.CreatedAt.Nanosecond()%1000 == 0 && util.ValidUUID(cursor.ID))
}

func validText(value string, maximum int) bool {
	return value != "" && strings.TrimSpace(value) == value &&
		utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func validDeepLink(value *string) bool {
	if value == nil {
		return true
	}
	if len(*value) < 1 || len(*value) > 1024 || strings.TrimSpace(*value) != *value ||
		!strings.HasPrefix(*value, "/") || strings.HasPrefix(*value, "//") ||
		strings.Contains(*value, "\\") {
		return false
	}
	parsed, err := url.Parse(*value)
	return err == nil && !parsed.IsAbs() && parsed.Host == ""
}

// mutationAlreadyApplied reports whether the item is already in the mutation's
// target state. Mutate replays such requests without writing, bumping the
// revision, or staging events.
func mutationAlreadyApplied(item Item, mutation Mutation) bool {
	switch mutation {
	case MutationRead:
		return item.ReadAt != nil
	case MutationUnread:
		return item.ReadAt == nil
	case MutationArchive:
		return item.ArchivedAt != nil
	case MutationRestore:
		return item.ArchivedAt == nil
	default:
		return true
	}
}

// ---- Service ----

// Service owns the per-recipient notification list, its derived counts
// projection, and the read/archive mutations over both.
type Service interface {
	List(context.Context, auth.UserPrincipal, ListOptions) (Page, error)
	Counts(context.Context, auth.UserPrincipal) (Counts, error)
	Get(context.Context, auth.UserPrincipal, uuid.UUID) (Item, error)
	Mutate(context.Context, auth.UserPrincipal, uuid.UUID, Mutation, int64) (MutationResult, error)
	ReadAll(context.Context, auth.UserPrincipal) (ReadAllResult, error)
	ReadConversation(context.Context, auth.UserPrincipal, uuid.UUID) (ReadAllResult, error)
}

var _ Service = (*DefaultService)(nil)

type DefaultService struct {
	store     *db.Store
	bus       *events.Bus
	presenter Presenter
}

func NewService(store *db.Store, bus *events.Bus, presenter Presenter) (*DefaultService, error) {
	if store == nil {
		return nil, errors.New("Inbox database store is required")
	}
	if bus == nil {
		return nil, errors.New("Inbox event bus is required")
	}
	return &DefaultService{store: store, bus: bus, presenter: presenter}, nil
}

func (s *DefaultService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ListOptions,
) (Page, error) {
	status := options.Status
	if status == "" {
		status = StatusActive
	}
	limit := options.Limit
	if limit == 0 {
		limit = 50
	}
	if !validStatus(status) || (options.Type != "" && !validType(options.Type)) ||
		limit < 1 || limit > 100 || !validCursor(options.Before) {
		return Page{}, ErrInvalidInput
	}
	return listPage(ctx, s.store.Queries(), principal.CompanyID, principal.UserID,
		ListOptions{Status: status, Type: options.Type, Limit: limit, Before: options.Before})
}

func (s *DefaultService) Counts(
	ctx context.Context,
	principal auth.UserPrincipal,
) (Counts, error) {
	// The query inner-joins active company_members, so a missing row is the
	// membership fence, not an absent projection: ErrNotFound.
	row, err := s.store.Queries().GetInboxCounts(ctx, generated.GetInboxCountsParams{
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
		CompanyID:       principal.CompanyID, UserID: principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Counts{}, ErrNotFound
	}
	if err != nil {
		return Counts{}, fmt.Errorf("get Inbox counts: %w", err)
	}
	return Counts{
		UnreadCount:   row.UnreadCount,
		CountsVersion: countsVersionFromOrderingToken(row.OrderingToken),
	}, nil
}

func (s *DefaultService) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	itemID uuid.UUID,
) (Item, error) {
	// An id that cannot name a row is indistinguishable from one that names
	// nothing: 404, never 422.
	if !util.ValidUUID(itemID) {
		return Item{}, ErrNotFound
	}
	row, err := s.store.Queries().GetInboxItem(ctx, generated.GetInboxItemParams{
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
		CompanyID:       principal.CompanyID,
		UserID:          principal.UserID,
		ItemID:          itemID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, fmt.Errorf("get Inbox item: %w", err)
	}
	return itemFromRow(row), nil
}

func (s *DefaultService) Mutate(
	ctx context.Context,
	principal auth.UserPrincipal,
	itemID uuid.UUID,
	mutation Mutation,
	expectedRevision int64,
) (MutationResult, error) {
	if !util.ValidUUID(itemID) {
		return MutationResult{}, ErrNotFound
	}
	// A mutation is a path segment, which no binding tag reaches, so its
	// vocabulary stays this package's to state. The revision bound that used
	// to sit beside it does not: it is the `revision` binding rule on
	// inboxMutationRequest.ExpectedRevision.
	if !validMutation(mutation) {
		return MutationResult{}, ErrInvalidInput
	}
	var result MutationResult
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		scope := principal.CompanyID
		if scope == uuid.Nil {
			row, err := tx.GetInboxItem(
				ctx,
				generated.GetInboxItemParams{
					ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
					UserID:          principal.UserID,
					ItemID:          itemID,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			scope = row.CompanyID.UUID
		}
		if err := lockActiveMembership(ctx, tx, scope, principal.UserID); err != nil {
			return err
		}
		item, err := lockItem(ctx, tx, principal.CompanyID, principal.UserID,
			itemID, expectedRevision)
		if err != nil {
			return err
		}
		if mutationAlreadyApplied(item, mutation) {
			counts, err := readVisibleCounts(ctx, tx, principal)
			if err != nil {
				return err
			}
			result = MutationResult{Item: item, Counts: counts}
			return nil
		}
		item, err = applyMutation(ctx, tx, itemID, expectedRevision, mutation)
		if err != nil {
			return err
		}
		counts, err := readVisibleCounts(ctx, tx, principal)
		if err != nil {
			return fmt.Errorf("update Inbox counts: %w", err)
		}
		eventCounts, err := ReadCountsInTx(ctx, tx, util.NullUUIDFromPtr(item.CompanyID).UUID, principal.UserID)
		if err != nil {
			return err
		}
		stageUpdatedEvent(&pending, item, eventCounts, Actor{Kind: "user", ID: &principal.UserID})
		result = MutationResult{Item: item, Counts: counts}
		return nil
	})
	if err != nil {
		return MutationResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func (s *DefaultService) ReadAll(
	ctx context.Context,
	principal auth.UserPrincipal,
) (ReadAllResult, error) {
	var result ReadAllResult
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		if err := authorizeActivePrincipal(ctx, tx, principal); err != nil {
			return err
		}
		scopes := []uuid.UUID{principal.CompanyID}
		if principal.CompanyID == uuid.Nil {
			companies, err := tx.ListInboxCompaniesForUser(ctx, generated.ListInboxCompaniesForUserParams{
				RecipientUserID: principal.UserID,
				ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
			})
			if err != nil {
				return err
			}
			for _, company := range companies {
				if err := lockActiveMembership(
					ctx,
					tx,
					company,
					principal.UserID,
				); errors.Is(
					err,
					ErrNotFound,
				) {
					continue
				} else if err != nil {
					return err
				}
				scopes = append(scopes, company)
			}
		}
		counts, err := readVisibleCounts(ctx, tx, principal)
		if err != nil {
			return err
		}
		if counts.UnreadCount == 0 {
			result.Items = []Item{}
			result.Counts = counts
			return nil
		}
		items := []Item{}
		for _, scope := range scopes {
			rows, err := tx.MarkAllInboxItemsRead(
				ctx,
				generated.MarkAllInboxItemsReadParams{
					ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
					CompanyID:       scope,
					UserID:          principal.UserID,
				},
			)
			if err != nil {
				return fmt.Errorf("mark all Inbox items read: %w", err)
			}
			for _, row := range rows {
				items = append(items, itemFromRow(row))
			}
		}
		counts, err = readVisibleCounts(ctx, tx, principal)
		if err != nil {
			return fmt.Errorf("reset Inbox counts: %w", err)
		}
		sortInboxItemsNewest(items)
		// Deliberately no realtime frames: an unread backlog has no upper
		// bound, and the writer already holds the rows and counts from this
		// receipt. Another session converges on its next read.
		result = ReadAllResult{Items: items, Counts: counts}
		return nil
	})
	if err != nil {
		return ReadAllResult{}, err
	}
	return result, nil
}

func (s *DefaultService) ReadConversation(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
) (ReadAllResult, error) {
	if !util.ValidUUID(conversationID) {
		return ReadAllResult{}, ErrInvalidInput
	}
	var result ReadAllResult
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		_, err := tx.GetVisibleConversationForInboxRead(
			ctx,
			generated.GetVisibleConversationForInboxReadParams{
				ConversationID: conversationID,
				CompanyID:      principal.CompanyID,
				UserID:         principal.UserID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConversationNotFound
		}
		if err != nil {
			return fmt.Errorf("resolve visible Inbox conversation: %w", err)
		}
		rows, err := tx.MarkConversationInboxItemsRead(
			ctx,
			generated.MarkConversationInboxItemsReadParams{
				ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
				CompanyID:       principal.CompanyID,
				UserID:          principal.UserID,
				ConversationID:  conversationID,
			},
		)
		if err != nil {
			return fmt.Errorf("mark Conversation Inbox items read: %w", err)
		}
		counts, err := ReadCountsInTx(ctx, tx, principal.CompanyID, principal.UserID)
		if err != nil {
			return err
		}
		items := make([]Item, 0, len(rows))
		for _, row := range rows {
			items = append(items, itemFromRow(row))
		}
		sortInboxItemsNewest(items)
		// Each changed row rides its own inbox.updated frame with the
		// refreshed counts beside it, so the caller's other sessions converge
		// without a read. The set is bounded by one conversation's unread
		// notifications.
		actor := Actor{Kind: "user", ID: &principal.UserID}
		for _, item := range items {
			stageUpdatedEvent(&pending, item, counts, actor)
		}
		result = ReadAllResult{Items: items, Counts: counts}
		return nil
	})
	if err != nil {
		return ReadAllResult{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

// lockActiveMembership locks the enabled User for personal items. Company
// items take the canonical Company lock and then the
// membership share lock — that order is load-bearing, it is what serializes a
// concurrent revocation behind this transaction. A missing Company, a missing
// membership, or a non-active membership all classify as ErrNotFound; any
// other error is returned unwrapped because the two callers wrap with
// different messages ("authorize Inbox membership" vs "resolve Inbox recipient").
func lockActiveMembership(
	ctx context.Context,
	tx *generated.Queries,
	companyID, userID uuid.UUID,
) error {
	if companyID == uuid.Nil {
		_, err := tx.LockEnabledUserForMembershipMutation(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.LockCompanyForMembershipRead(ctx, companyID); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	membership, err := tx.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{
			CompanyID: companyID,
			UserID:    userID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if membership.Status != "active" {
		return ErrNotFound
	}
	return nil
}

func authorizeActivePrincipal(
	ctx context.Context,
	tx *generated.Queries,
	principal auth.UserPrincipal,
) error {
	err := lockActiveMembership(ctx, tx, principal.CompanyID, principal.UserID)
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("authorize Inbox membership: %w", err)
	}
	return nil
}

// CreateInTx adds one durable item and its count projection to the caller's
// source-mutation transaction, staging both realtime events on pending. It is
// a transaction participant, not an owner: it takes the caller's open handle,
// never a *db.Store, and must never open a transaction of its own. The caller
// publishes pending on the Bus only after its transaction commits.
//
// The item stores the Project it is about (ADR 0017). A recipient who cannot
// read that Project gets no item and a zero CreateResult, as an inactive
// recipient does.
//
// Callers whose own lock order reaches a Conversation before this insert may
// call LockRecipientInTx first, while Company/member locks are still first.
// CreateInTx safely re-enters those share locks when it performs the write.
func CreateInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	input CreateInput,
) (CreateResult, error) {
	if tx == nil || pending == nil || !ValidCreateInput(input) {
		return CreateResult{}, ErrInvalidInput
	}
	eligible, err := LockRecipientInTx(ctx, tx, input.CompanyID, input.RecipientUserID)
	if err != nil {
		return CreateResult{}, err
	}
	if !eligible {
		return CreateResult{}, nil
	}
	project, readable, err := itemProjectInTx(ctx, tx, input)
	if err != nil {
		return CreateResult{}, err
	}
	if !readable {
		// A person who cannot read the Project the item is about is not told
		// about it (ADR 0017); the read predicate would hide it anyway.
		return CreateResult{}, nil
	}
	// The INSERT is ON CONFLICT (company_id, recipient_user_id, dedupe_key) DO
	// NOTHING RETURNING ..., so a dedupe hit arrives as pgx.ErrNoRows and takes
	// the replay path below. It must never escape to a handler, which would turn
	// a deduplicated create into an HTTP 409.
	row, err := tx.CreateInboxItem(ctx, generated.CreateInboxItemParams{
		CompanyID: input.CompanyID, RecipientUserID: input.RecipientUserID,
		ConversationID: util.NullUUIDFromPtr(input.ConversationID),
		Kind:           string(input.Type),
		EntityType:     string(input.Entity.Type), EntityID: input.Entity.ID,
		Title: input.Title, Body: input.Body,
		DeepLink: util.PtrToText(input.DeepLink), DedupeKey: input.DedupeKey,
		ProjectID: project,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// Inside the transaction whose INSERT just conflicted the row exists,
		// so the replay read comes back empty only when the recipient cannot
		// read the row's Project (ADR 0017): it is not theirs to see, exactly
		// as a new item would not be, so nothing is created or replayed.
		deduplicated, dedupeErr := tx.GetInboxItemByDedupeKey(ctx, generated.GetInboxItemByDedupeKeyParams{
			ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
			CompanyID:       input.CompanyID, RecipientUserID: input.RecipientUserID,
			DedupeKey: input.DedupeKey,
		})
		if errors.Is(dedupeErr, pgx.ErrNoRows) {
			return CreateResult{}, nil
		}
		if dedupeErr != nil {
			return CreateResult{}, fmt.Errorf("load deduplicated Inbox item: %w", dedupeErr)
		}
		item := itemFromRow(deduplicated)
		counts, countsErr := ReadCountsInTx(ctx, tx, input.CompanyID, input.RecipientUserID)
		if countsErr != nil {
			return CreateResult{}, countsErr
		}
		return CreateResult{Item: item, Counts: counts, RecipientUserID: input.RecipientUserID}, nil
	}
	if err != nil {
		return CreateResult{}, fmt.Errorf("create Inbox item: %w", err)
	}
	item := itemFromRow(row)
	counts, err := ReadCountsInTx(ctx, tx, input.CompanyID, input.RecipientUserID)
	if err != nil {
		return CreateResult{}, fmt.Errorf("increment Inbox counts: %w", err)
	}
	result := CreateResult{
		Item: item, Counts: counts, RecipientUserID: input.RecipientUserID,
		Created: true,
	}
	stageCreatedEvent(pending, result, input.Actor)
	return result, nil
}

// itemProjectInTx names the Project an item is about (ADR 0017): the chat it
// links to, else the Task, Run, Approval, Question or Deliverable it names.
// readable is whether the recipient reads that Project; an item about no
// Project is always readable. An account item (no Company) is about none.
func itemProjectInTx(
	ctx context.Context,
	tx *generated.Queries,
	input CreateInput,
) (project uuid.NullUUID, readable bool, err error) {
	if input.CompanyID == uuid.Nil {
		return uuid.NullUUID{}, true, nil
	}
	subject, parseErr := uuid.Parse(input.Entity.ID)
	row, err := tx.ResolveInboxItemProject(ctx, generated.ResolveInboxItemProjectParams{
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
		CompanyID:       input.CompanyID, RecipientUserID: input.RecipientUserID,
		ConversationID: util.NullUUIDFromPtr(input.ConversationID),
		EntityType:     string(input.Entity.Type),
		SubjectID:      uuid.NullUUID{UUID: subject, Valid: parseErr == nil},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.NullUUID{}, true, nil
	}
	if err != nil {
		return uuid.NullUUID{}, false, fmt.Errorf("resolve Inbox item Project: %w", err)
	}
	return uuid.NullUUID{UUID: row.ProjectID, Valid: true}, row.RecipientReads, nil
}

// LockRecipientInTx proves that a recipient is still active while taking the
// canonical Company then membership locks. It lets a producing domain keep
// that order ahead of its own Conversation/Run locks; false means the former
// recipient is no longer eligible and the source mutation should continue
// without creating personal Inbox state.
func LockRecipientInTx(
	ctx context.Context,
	tx *generated.Queries,
	companyID uuid.UUID,
	recipientUserID uuid.UUID,
) (bool, error) {
	if tx == nil || !util.ValidUUID(recipientUserID) {
		return false, ErrInvalidInput
	}
	err := lockActiveMembership(ctx, tx, companyID, recipientUserID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("resolve Inbox recipient: %w", err)
	}
	return true, nil
}

// CloseEntityInTx marks every recipient copy about one resolved entity read
// and archived inside the producing domain's terminal transaction. The Inbox
// rows remain durable, and their per-recipient projection events are event
// for publication only after that transaction commits.
func CloseEntityInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	companyID uuid.UUID,
	entity Entity,
	actor Actor,
) error {
	if tx == nil || pending == nil || !util.ValidUUID(companyID) || !validEntity(entity) ||
		!validActor(actor) {
		return ErrInvalidInput
	}
	rows, err := tx.CloseInboxEntityItems(ctx, generated.CloseInboxEntityItemsParams{
		CompanyID: companyID, EntityType: string(entity.Type), EntityID: entity.ID,
	})
	if err != nil {
		return fmt.Errorf("close Inbox entity items: %w", err)
	}
	return stageClosedItems(ctx, tx, pending, companyID, rows, actor)
}

// CloseTaskRunReviewsInTx closes every "Ready to review" notice about a Run
// of one Task inside a confirmed handoff's transaction (ADR 0014): the
// handoff advances the Task's execution generation, so none of those Runs
// can be reviewed or merged any more. The rows stay in the archived view.
func CloseTaskRunReviewsInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	companyID, taskID uuid.UUID,
	actor Actor,
) error {
	if tx == nil || pending == nil || !util.ValidUUID(companyID) || !util.ValidUUID(taskID) ||
		!validActor(actor) {
		return ErrInvalidInput
	}
	rows, err := tx.CloseTaskRunReviewInboxItems(ctx, generated.CloseTaskRunReviewInboxItemsParams{
		CompanyID: companyID, TaskID: taskID,
	})
	if err != nil {
		return fmt.Errorf("close Task Run review Inbox items: %w", err)
	}
	return stageClosedItems(ctx, tx, pending, companyID, rows, actor)
}

// CloseTaskAssignmentsInTx closes the "Task assigned" notices one member
// holds about a Task, inside the Task write that moved it to someone else
// (ADR 0014): only the assignee starts a Task, so a notice telling the
// previous one to start it is stale. The rows stay in the archived view.
func CloseTaskAssignmentsInTx(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	companyID, taskID, recipientUserID uuid.UUID,
	actor Actor,
) error {
	if tx == nil || pending == nil || !util.ValidUUID(companyID) || !util.ValidUUID(taskID) ||
		!util.ValidUUID(recipientUserID) || !validActor(actor) {
		return ErrInvalidInput
	}
	rows, err := tx.CloseTaskAssignedInboxItems(ctx, generated.CloseTaskAssignedInboxItemsParams{
		CompanyID: companyID, RecipientUserID: recipientUserID, TaskID: taskID,
	})
	if err != nil {
		return fmt.Errorf("close Task assignment Inbox items: %w", err)
	}
	return stageClosedItems(ctx, tx, pending, companyID, rows, actor)
}

// stageClosedItems stages the projection events of rows a close just moved.
func stageClosedItems(
	ctx context.Context,
	tx *generated.Queries,
	pending *[]events.Event,
	companyID uuid.UUID,
	rows []generated.InboxItem,
	actor Actor,
) error {
	// Each recipient's refreshed counts ride that recipient's item frames —
	// the counts are read once per recipient, after the close moved the rows.
	countsByRecipient := make(map[uuid.UUID]Counts, len(rows))
	for _, row := range rows {
		if _, done := countsByRecipient[row.RecipientUserID]; done {
			continue
		}
		counts, err := ReadCountsInTx(ctx, tx, companyID, row.RecipientUserID)
		if err != nil {
			return fmt.Errorf("read closed Inbox counts: %w", err)
		}
		countsByRecipient[row.RecipientUserID] = counts
	}
	for _, row := range rows {
		// A recipient who no longer reads the item's Project (ADR 0017) gets
		// no frame: it would carry the item's title. The row still closes.
		if row.ProjectID.Valid {
			reads, err := tx.InboxRecipientReadsProject(ctx, generated.InboxRecipientReadsProjectParams{
				CompanyID:       companyID,
				ProjectID:       row.ProjectID.UUID,
				RecipientUserID: row.RecipientUserID,
			})
			if err != nil {
				return fmt.Errorf("check closed Inbox item reader: %w", err)
			}
			if !reads {
				continue
			}
		}
		stageUpdatedEvent(pending, itemFromRow(row), countsByRecipient[row.RecipientUserID], actor)
	}
	return nil
}

// ---- Row mapping and query wrappers ----

// listPage returns one keyset page. The caller passes options that are already
// defaulted and validated; the scoped query owns tenancy and ordering.
func listPage(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
	options ListOptions,
) (Page, error) {
	params := generated.ListInboxItemsParams{
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
		CompanyID:       companyID, UserID: userID,
		Status: string(options.Status), HasKind: options.Type != "", Kind: string(options.Type),
		PageSize:        int32(options.Limit + 1),
		BeforeCreatedAt: pgtype.Timestamptz{Time: time.Unix(0, 0).UTC(), Valid: true},
	}
	if options.Before != nil {
		params.HasCursor = true
		params.BeforeCreatedAt = pgtype.Timestamptz{Time: options.Before.CreatedAt.UTC(), Valid: true}
		params.BeforeID = options.Before.ID
	}
	rows, err := q.ListInboxItems(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list Inbox items: %w", err)
	}
	hasNext := len(rows) > options.Limit
	if hasNext {
		rows = rows[:options.Limit]
	}
	page := Page{Items: make([]Item, 0, len(rows))}
	for _, row := range rows {
		page.Items = append(page.Items, itemFromRow(row))
	}
	if hasNext {
		last := page.Items[len(page.Items)-1]
		page.NextCursor = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// lockItem takes the row lock and checks the optimistic version before mapping,
// so a stale write surfaces as ErrConflict.
func lockItem(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID, itemID uuid.UUID,
	expectedRevision int64,
) (Item, error) {
	row, err := q.GetInboxItemForUpdate(ctx, generated.GetInboxItemForUpdateParams{
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
		CompanyID:       companyID, UserID: userID, ItemID: itemID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, fmt.Errorf("lock Inbox item: %w", err)
	}
	if row.Version != expectedRevision {
		return Item{}, ErrConflict
	}
	return itemFromRow(row), nil
}

// applyMutation runs the version-guarded UPDATE for one mutation. The guard
// matching nothing — the version moved, or the row is already in the target
// state — surfaces as pgx.ErrNoRows and maps to ErrConflict.
func applyMutation(
	ctx context.Context,
	q *generated.Queries,
	itemID uuid.UUID,
	expectedRevision int64,
	mutation Mutation,
) (Item, error) {
	params := generated.MarkInboxItemReadParams{ItemID: itemID, ExpectedVersion: expectedRevision}
	var row generated.InboxItem
	var err error
	switch mutation {
	case MutationRead:
		row, err = q.MarkInboxItemRead(ctx, params)
	case MutationUnread:
		row, err = q.MarkInboxItemUnread(ctx, generated.MarkInboxItemUnreadParams(params))
	case MutationArchive:
		row, err = q.ArchiveInboxItem(ctx, generated.ArchiveInboxItemParams(params))
	case MutationRestore:
		row, err = q.RestoreInboxItem(ctx, generated.RestoreInboxItemParams(params))
	default:
		return Item{}, ErrInvalidInput
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrConflict
	}
	if err != nil {
		return Item{}, fmt.Errorf("%s Inbox item: %w", mutation, err)
	}
	return itemFromRow(row), nil
}

// ReadCountsInTx derives the recipient's unread count plus the counts ordering
// token for a transaction participant. The caller has already established the
// recipient through an active-membership fence or a stored Inbox row, so the
// projection remains readable while terminal cleanup revokes that membership.
// There is no counter row to maintain: COUNT(*) over the partial unread index
// is the authority.
func ReadCountsInTx(
	ctx context.Context,
	q *generated.Queries,
	companyID, userID uuid.UUID,
) (Counts, error) {
	row, err := q.GetInboxCountsForRecipient(ctx, generated.GetInboxCountsForRecipientParams{
		ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
		CompanyID:       companyID, UserID: userID,
	})
	if err != nil {
		return Counts{}, fmt.Errorf("read Inbox counts: %w", err)
	}
	return Counts{
		UnreadCount:   row.UnreadCount,
		CountsVersion: countsVersionFromOrderingToken(row.OrderingToken),
	}, nil
}

// countsVersionFromOrderingToken renders the counts ordering token (the
// newest write among the recipient's items and their Projects' access
// changes, see GetInboxCounts) as the monotonic countsVersion the Counts
// projection carries. An empty inbox has no ordering token (NULL) and uses
// the initial version 1.
func countsVersionFromOrderingToken(token pgtype.Timestamptz) int64 {
	if !token.Valid {
		return 1
	}
	return token.Time.UnixMilli()
}

// itemFromRow is the row-to-entity mapping. The schema and sqlc already own
// stored-row invariants; mapping does not revalidate trusted database output.
func itemFromRow(row generated.InboxItem) Item {
	item := Item{
		ID: row.ID, CompanyID: util.NullUUIDPtr(row.CompanyID), RecipientUserID: row.RecipientUserID,
		Type: Type(row.Kind), EntityType: protocol.EntityType(row.EntityType),
		Title: row.Title, Body: row.Body, ReadAt: util.TimestampToTime(row.ReadAt),
		ArchivedAt: util.TimestampToTime(row.ArchivedAt), CreatedAt: row.CreatedAt.Time.UTC(),
		UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
	if row.DeepLink.Valid {
		value := row.DeepLink.String
		item.DeepLink = &value
	}
	item.Unread = item.ReadAt == nil && item.ArchivedAt == nil
	return item
}

func sortInboxItemsNewest(items []Item) {
	sort.Slice(items, func(left, right int) bool {
		if items[left].CreatedAt.Equal(items[right].CreatedAt) {
			return bytes.Compare(items[left].ID[:], items[right].ID[:]) > 0
		}
		return items[left].CreatedAt.After(items[right].CreatedAt)
	})
}

// ---- Event staging ----

// The stage functions are unexported on purpose: no other package may build
// inbox wire events. They append the events to the caller's per-transaction
// slice; the transaction owner publishes them on the Bus only after commit.
func stageCreatedEvent(pending *[]events.Event, result CreateResult, actor Actor) {
	stageItemEvent(pending, EventItemCreated, result.Item, result.Counts, actor)
}

func stageUpdatedEvent(pending *[]events.Event, item Item, counts Counts, actor Actor) {
	stageItemEvent(pending, EventItemUpdated, item, counts, actor)
}

func stageItemEvent(
	pending *[]events.Event,
	name string,
	item Item,
	counts Counts,
	actor Actor,
) {
	*pending = append(*pending, events.Event{
		EntityType: Topic,
		Name:       name,
		CompanyID:  util.NullUUIDFromPtr(item.CompanyID).UUID,
		CreatedAt:  time.Now().UTC(),
		Actor:      eventActor(actor),
		Payload:    ItemWithCounts{Item: item, Counts: counts},
	})
}

// eventActor maps the domain actor onto the envelope pair. An unattributed or
// machine-driven change is recorded as the system actor.
func eventActor(actor Actor) events.Actor {
	switch actor.Kind {
	case "user":
		return events.UserActor(actor.ID.String())
	case "agent":
		return events.AgentActor(actor.ID.String())
	default:
		return events.SystemActor()
	}
}

// ---- Presenter ----

// Notification is the transient presentation hint a freshly created Inbox item
// raises. The Inbox row stays authoritative; this is best-effort desktop noise.
type Notification struct {
	NotificationID uuid.UUID
	CompanyID      uuid.UUID
	RecipientID    uuid.UUID
	Type           Type
	EntityType     protocol.EntityType
	Title          string
	Body           string
	DeepLink       *string
}

// Presenter delivers a Notification. It is optional everywhere it is consumed.
type Presenter interface {
	Present(Notification)
}

var _ Presenter = (*RealtimePresenter)(nil)

// RealtimePresenter publishes the notification.issued hint straight on the
// event Bus for the exact recipient in the exact Company.
type RealtimePresenter struct {
	bus *events.Bus
}

func NewRealtimePresenter(bus *events.Bus) *RealtimePresenter {
	return &RealtimePresenter{bus: bus}
}

func (p *RealtimePresenter) Present(notification Notification) {
	if p == nil || p.bus == nil {
		return
	}
	p.bus.Publish(context.Background(), TopicNotification, events.Event{
		EntityType: TopicNotification,
		Name:       EventNotificationIssued,
		CompanyID:  notification.CompanyID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.SystemActor(),
		Payload: ToastHint{
			NotificationID:  notification.NotificationID,
			Type:            notification.Type,
			EntityType:      notification.EntityType,
			Title:           notification.Title,
			Body:            notification.Body,
			DeepLink:        notification.DeepLink,
			CompanyID:       notification.CompanyID,
			RecipientUserID: notification.RecipientID,
		},
	})
}

func PresentCreated(presenter Presenter, result CreateResult) {
	if presenter == nil || !result.Created {
		return
	}
	presenter.Present(Notification{
		NotificationID: result.Item.ID, CompanyID: util.NullUUIDFromPtr(result.Item.CompanyID).UUID,
		RecipientID: result.RecipientUserID, Type: result.Item.Type,
		EntityType: result.Item.EntityType,
		Title:      result.Item.Title, Body: result.Item.Body,
		DeepLink: result.Item.DeepLink,
	})
}

// ---- Sentinels ----

var (
	ErrUnauthenticated      = errors.New("user is not authenticated")
	ErrInvalidInput         = errors.New("invalid Inbox input")
	ErrNotFound             = errors.New("Inbox item or Company not found")
	ErrConversationNotFound = errors.New("Inbox conversation not found")
	ErrConflict             = errors.New("Inbox item mutation conflict")
)

// Type is the kind of thing an inbox item is about.
type Type string

// Inbox item types.
const (
	TypeTaskAssigned    Type = "task_assigned"
	TypeBlocker         Type = "blocker"
	TypeRunCompleted    Type = "run_completed"
	TypeRunFailed       Type = "run_failed"
	TypeApprovalRequest Type = "approval_request"
	TypeQuestionRequest Type = "question_request"
	// TypeRuntimeOffline says a machine went offline for its owner: it
	// stopped answering while it still held work, or another person signed
	// in on it. Its entity is the runtime, so the sweep or registration
	// that raised it and the heartbeat or registration that resolves it
	// address the same rows.
	TypeRuntimeOffline    Type = "runtime_offline"
	TypeInvitationRenewal Type = "invitation_renewal"
	TypeCalendarReminder  Type = "calendar_reminder"
	TypeCalendarUpdate    Type = "calendar_update"
	TypeDiscussionMention Type = "discussion_mention"
	// TypeTaskRunRequested nudges a Task's assignee: a colleague wrote in the
	// Task chat while only the assignee could start the Agent (ADR 0014), so
	// the words wait in the Run queue for their next Run.
	TypeTaskRunRequested Type = "task_run_requested"
	// TypeTaskHandoff tells a Task's previous assignee that the Task moved to
	// another member's machine; files not merged stay on theirs.
	TypeTaskHandoff     Type = "task_handoff"
	TypeAnalyticsBudget Type = "analytics_budget"
	TypeHuddleMissed    Type = "huddle_missed"
	// TypeProjectAccessGranted tells the people who manage a Project's access
	// that a Group membership let someone read it. Its entity is the
	// Project itself, so it reads for the Project's readers and for the
	// members whose Company role holds projects.manage.
	TypeProjectAccessGranted Type = "project_access_granted"
)

// Item is the one inbox wire type: the HTTP reads and the inbox.new /
// inbox.updated frames carry these identical bytes. The recipient never rides
// the wire — it is always the caller on the routes and the routing fact on
// the frames — so RecipientUserID stays process-local.
type Item struct {
	ID              uuid.UUID           `json:"id"`
	CompanyID       *uuid.UUID          `json:"companyId"`
	RecipientUserID uuid.UUID           `json:"-"`
	Type            Type                `json:"kind"`
	EntityType      protocol.EntityType `json:"-"`
	Title           string              `json:"title"`
	Body            string              `json:"body"`
	DeepLink        *string             `json:"deepLink,omitempty"`
	Unread          bool                `json:"unread"`
	ReadAt          *time.Time          `json:"readAt"`
	ArchivedAt      *time.Time          `json:"archivedAt"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
	Revision        int64               `json:"revision"`
}

// Counts is the recipient's badge state for the requested Inbox scope.
// Realtime item sidecars keep the item's Company scope (or personal-only scope);
// account clients refetch their aggregate rather than treating a sidecar as a total.
type Counts struct {
	UnreadCount   int64 `json:"unreadCount"`
	CountsVersion int64 `json:"countsVersion"`
}

// readVisibleCounts returns the HTTP caller's scope; event sidecars keep their
// original per-Company scope, even when a mutation came through the account API.
func readVisibleCounts(ctx context.Context, q *generated.Queries, p auth.UserPrincipal) (Counts, error) {
	row, err := q.GetInboxCounts(
		ctx,
		generated.GetInboxCountsParams{
			ManagementRoles: auth.RolesWith(auth.PermissionProjectsManage),
			CompanyID:       p.CompanyID,
			UserID:          p.UserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Counts{}, ErrNotFound
	}
	if err != nil {
		return Counts{}, fmt.Errorf("read visible Inbox counts: %w", err)
	}
	return Counts{
		UnreadCount:   row.UnreadCount,
		CountsVersion: countsVersionFromOrderingToken(row.OrderingToken),
	}, nil
}
