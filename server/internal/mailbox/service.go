// Package mailbox owns the shared Company Email identity, inbound-message
// metadata and retention, and reviewable outbound Agent Mail drafts with their
// durable send queue. It is separate from inbox because email is user content,
// not a Boardly notification.
package mailbox

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/singleflight"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/infra/email"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	minLocalPartLength  = 3
	maxLocalPartLength  = 64
	defaultListLimit    = 50
	maxListLimit        = 100
	detailCacheTTL      = 5 * time.Minute
	expiryCacheTTL      = 5 * time.Minute
	retentionPeriod     = 30 * 24 * time.Hour
	maxLocalExpirations = 4096
)

var (
	ErrInvalidLocalPart    = errors.New("invalid mailbox local part")
	ErrReservedLocalPart   = errors.New("reserved mailbox local part")
	ErrInvalidInput        = errors.New("invalid mailbox input")
	ErrUnauthenticated     = errors.New("mailbox authentication required")
	ErrForbidden           = errors.New("mailbox access forbidden")
	ErrAlreadyConfigured   = errors.New("mailbox already configured")
	ErrAddressUnavailable  = errors.New("mailbox address unavailable")
	ErrMessageNotFound     = errors.New("mailbox message not found")
	ErrMessageExpired      = errors.New("mailbox message expired")
	ErrProviderRateLimited = errors.New("mailbox provider rate limited")
	ErrProviderUnavailable = errors.New("mailbox provider unavailable")
	ErrInvalidStore        = errors.New("invalid mailbox store data")

	localPartPattern = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*$`)
	domainPattern    = regexp.MustCompile(
		`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`,
	)
	reservedLocalParts = map[string]struct{}{
		"abuse": {}, "admin": {}, "mailer-daemon": {}, "no-reply": {},
		"noreply": {}, "postmaster": {}, "root": {}, "security": {},
	}
)

type Status string

const (
	StatusDisabled     Status = "disabled"
	StatusUnconfigured Status = "unconfigured"
	StatusActive       Status = "active"
)

type Mailbox struct {
	ID        uuid.UUID `json:"id"`
	LocalPart string    `json:"localPart"`
	Address   string    `json:"address"`
	CreatedAt time.Time `json:"createdAt"`
	Revision  int64     `json:"revision"`
}

type State struct {
	Status      Status   `json:"status"`
	Domain      string   `json:"domain"`
	Mailbox     *Mailbox `json:"mailbox"`
	CanCreate   bool     `json:"canCreate"`
	UnreadCount int64    `json:"unreadCount"`
}

type Message struct {
	ID             uuid.UUID  `json:"id"`
	Sender         string     `json:"sender"`
	Subject        string     `json:"subject"`
	Recipients     []string   `json:"recipients"`
	ReceivedAt     time.Time  `json:"receivedAt"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	ReadAt         *time.Time `json:"readAt"`
	HasAttachments bool       `json:"hasAttachments"`
	Revision       int64      `json:"revision"`
}

type Attachment struct {
	ID          uuid.UUID `json:"id"`
	Filename    string    `json:"filename"`
	MIMEType    string    `json:"mimeType"`
	SizeBytes   *int64    `json:"sizeBytes"`
	Disposition *string   `json:"disposition,omitempty"`
	ContentID   *string   `json:"contentId,omitempty"`
}

type DetailContent struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	BCC     []string `json:"bcc"`
	ReplyTo []string `json:"replyTo"`
	HTML    string   `json:"html"`
	Text    string   `json:"text"`
}

type Detail struct {
	Message
	DetailContent
	Attachments []Attachment `json:"attachments"`
}

type AttachmentDownload struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Cursor struct {
	ReceivedAt time.Time
	ID         uuid.UUID
}

type ListOptions struct {
	Limit  int
	Before *Cursor
}

type Page struct {
	Messages   []Message
	NextCursor *Cursor
}

func timestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

type DetailCache interface {
	Get(context.Context, string) (DetailContent, bool, error)
	Set(context.Context, string, DetailContent, time.Duration) error
	Delete(context.Context, string) error
}

// ProviderExpiryCache shares short-lived provider 404 tombstones across API
// replicas without storing the provider identifier in plaintext.
type ProviderExpiryCache interface {
	IsExpired(context.Context, string) (bool, error)
	MarkExpired(context.Context, string, time.Duration) error
}

type Observer interface {
	ObserveMailboxWebhook(string)
	ObserveMailboxProvider(string)
	ObserveMailboxCache(string)
	ObserveMailboxRetention(PurgeResult)
}

// MailboxService owns Company Email configuration, inbound metadata, detail
// retrieval, and retention.
type MailboxService interface { //nolint:revive // AGENTS.md mandates <Domain>Service; bare Service is forbidden.
	GetState(context.Context, auth.UserPrincipal) (State, error)
	Create(context.Context, auth.UserPrincipal, string) (State, error)
	List(context.Context, auth.UserPrincipal, ListOptions) (Page, error)
	GetDetail(context.Context, auth.UserPrincipal, uuid.UUID) (Detail, error)
	MarkRead(context.Context, auth.UserPrincipal, uuid.UUID) (Message, error)
	GetAttachment(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (AttachmentDownload, error)
	Ingest(context.Context, email.ReceivedEvent) (IngestResult, error)
	PurgeExpired(context.Context, int) (PurgeResult, error)
}

var _ MailboxService = (*DefaultMailboxService)(nil)

type ServiceOptions struct {
	Store       *db.Store
	Provider    email.ReceivedProvider
	Cache       DetailCache
	ExpiryCache ProviderExpiryCache
	Bus         *events.Bus
	Observer    Observer
	Domain      string
	Now         func() time.Time
}

type DefaultMailboxService struct {
	store       *db.Store
	provider    email.ReceivedProvider
	cache       DetailCache
	expiryCache ProviderExpiryCache
	bus         *events.Bus
	observer    Observer
	domain      string
	now         func() time.Time
	detailGroup singleflight.Group
	expirations providerExpiryMemo
}

func NewMailboxService(options ServiceOptions) (*DefaultMailboxService, error) {
	if options.Store == nil || options.Provider == nil || options.Bus == nil {
		return nil, errors.New("Mailbox database store, provider, and event bus are required")
	}
	domain := strings.ToLower(strings.TrimSpace(options.Domain))
	if len(domain) > 253 || !domainPattern.MatchString(domain) {
		return nil, errors.New("Mailbox domain is invalid")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &DefaultMailboxService{
		store: options.Store, provider: options.Provider, cache: options.Cache,
		expiryCache: options.ExpiryCache, bus: options.Bus,
		observer: options.Observer, domain: domain, now: options.Now,
		expirations: providerExpiryMemo{values: make(map[string]time.Time)},
	}, nil
}

type providerExpiryMemo struct {
	mu     sync.Mutex
	values map[string]time.Time
}

func (m *providerExpiryMemo) Contains(providerEmailID string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	expiresAt, found := m.values[providerEmailID]
	if !found {
		return false
	}
	if !expiresAt.After(now) {
		delete(m.values, providerEmailID)
		return false
	}
	return true
}

func (m *providerExpiryMemo) Mark(providerEmailID string, expiresAt, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, deadline := range m.values {
		if !deadline.After(now) {
			delete(m.values, key)
		}
	}
	if len(m.values) >= maxLocalExpirations {
		var oldestKey string
		var oldestDeadline time.Time
		for key, deadline := range m.values {
			if oldestKey == "" || deadline.Before(oldestDeadline) {
				oldestKey = key
				oldestDeadline = deadline
			}
		}
		delete(m.values, oldestKey)
	}
	m.values[providerEmailID] = expiresAt
}

func ParseLocalPart(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < minLocalPartLength || len(value) > maxLocalPartLength ||
		!localPartPattern.MatchString(value) {
		return "", fmt.Errorf("%w: expected 3-64 lowercase ASCII characters", ErrInvalidLocalPart)
	}
	if _, reserved := reservedLocalParts[value]; reserved {
		return "", ErrReservedLocalPart
	}
	return value, nil
}

func (s *DefaultMailboxService) GetState(ctx context.Context, principal auth.UserPrincipal) (State, error) {
	if !validPrincipal(principal) {
		return State{}, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return State{}, ErrForbidden
	}
	row, err := s.store.Queries().GetMailboxStateForMember(ctx, generated.GetMailboxStateForMemberParams{
		ActorRoles: auth.RolesWith(auth.PermissionMailboxManage),
		CompanyID:  principal.CompanyID, ActorUserID: principal.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return State{}, ErrForbidden
	}
	if err != nil {
		return State{}, fmt.Errorf("get Company mailbox state: %w", err)
	}
	state := State{
		Status: StatusUnconfigured, Domain: s.domain, UnreadCount: row.UnreadCount,
		CanCreate: principal.Can(auth.PermissionMailboxManage),
	}
	if !row.MailboxID.Valid {
		return state, nil
	}
	state.Status = StatusActive
	state.CanCreate = false
	state.Mailbox = &Mailbox{
		ID: row.MailboxID.UUID, LocalPart: row.LocalPart.String,
		Address:   row.LocalPart.String + "@" + s.domain,
		CreatedAt: row.CreatedAt.Time.UTC(), Revision: row.MailboxVersion.Int64,
	}
	return state, nil
}

func (s *DefaultMailboxService) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	localPart string,
) (State, error) {
	if !validPrincipal(principal) {
		return State{}, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return State{}, ErrForbidden
	}
	localPart, err := ParseLocalPart(localPart)
	if err != nil {
		return State{}, errors.Join(ErrInvalidInput, err)
	}
	var change events.Event
	var created generated.CompanyMailbox
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		lockErr := lockMailboxMutationMembership(
			ctx,
			queries,
			principal,
			ErrForbidden,
		)
		if lockErr != nil {
			return lockErr
		}
		created, err = queries.InsertCompanyMailbox(ctx, generated.InsertCompanyMailboxParams{
			CompanyID: principal.CompanyID,
			LocalPart: localPart, CreatedByUserID: principal.UserID,
		})
		if err != nil {
			return err
		}
		change, err = newChangedEvent(
			ctx,
			queries,
			principal.CompanyID,
			"user",
			&principal.UserID,
			ChangeReasonConfigured,
			nil,
			s.now(),
		)
		return err
	})
	if err != nil {
		return State{}, mapCreateError(err)
	}
	publishMailboxEvents(ctx, s.bus, []events.Event{change})
	mailbox := mailboxFromRow(created, s.domain)
	return State{Status: StatusActive, Domain: s.domain, Mailbox: &mailbox}, nil
}

// lockMailboxMutationMembership is the Company-first lifecycle fence shared
// by Mailbox and Agent Mail writes. The middleware principal remains the
// request identity; this lock only proves that the same membership is still
// active and holds mailbox.manage in the transaction that mutates durable state.
func lockMailboxMutationMembership(
	ctx context.Context,
	queries *generated.Queries,
	principal auth.UserPrincipal,
	forbidden error,
) error {
	if _, err := queries.LockCompanyForMembershipMutation(
		ctx,
		principal.CompanyID,
	); errors.Is(err, pgx.ErrNoRows) {
		return forbidden
	} else if err != nil {
		return fmt.Errorf("lock Company for Mailbox mutation: %w", err)
	}
	membership, err := queries.GetCompanyMemberForUpdate(
		ctx,
		generated.GetCompanyMemberForUpdateParams{
			CompanyID: principal.CompanyID,
			UserID:    principal.UserID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && membership.Status != "active" {
		return forbidden
	}
	if err != nil {
		return fmt.Errorf("lock acting Company member for Mailbox mutation: %w", err)
	}
	role := auth.CompanyRole(membership.Role)
	if !role.Can(auth.PermissionMailboxManage) {
		return forbidden
	}
	return nil
}

func (s *DefaultMailboxService) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options ListOptions,
) (Page, error) {
	if !validPrincipal(principal) {
		return Page{}, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return Page{}, ErrForbidden
	}
	limit := options.Limit
	if limit == 0 {
		limit = defaultListLimit
	}
	if limit < 1 || limit > maxListLimit || !validCursor(options.Before) {
		return Page{}, ErrInvalidInput
	}
	params := generated.ListMailboxMessagesForMemberParams{
		ActorRoles:  auth.RolesWith(auth.PermissionMailboxManage),
		ActorUserID: principal.UserID,
		CompanyID:   principal.CompanyID, PageSize: int32(limit + 1),
	}
	if options.Before != nil {
		params.HasCursor = true
		params.BeforeReceivedAt = timestamp(options.Before.ReceivedAt)
		params.BeforeID = options.Before.ID
	}
	rows, err := s.store.Queries().ListMailboxMessagesForMember(ctx, params)
	if err != nil {
		return Page{}, fmt.Errorf("list mailbox messages: %w", err)
	}
	page := Page{Messages: make([]Message, 0, min(len(rows), limit))}
	for index, row := range rows {
		if index == limit {
			last := page.Messages[len(page.Messages)-1]
			page.NextCursor = &Cursor{ReceivedAt: last.ReceivedAt, ID: last.ID}
			break
		}
		page.Messages = append(page.Messages, messageFromRow(row))
	}
	return page, nil
}

func validPrincipal(principal auth.UserPrincipal) bool {
	return util.ValidUUID(principal.UserID) && util.ValidUUID(principal.SessionID) &&
		util.ValidUUID(principal.CompanyID) &&
		(principal.AuthKind == auth.TokenKindSession || principal.AuthKind == auth.TokenKindBearer) &&
		principal.Role.Valid()
}

func validCursor(cursor *Cursor) bool {
	return cursor == nil || (!cursor.ReceivedAt.IsZero() && util.ValidUUID(cursor.ID))
}

func mailboxFromRow(row generated.CompanyMailbox, domain string) Mailbox {
	return Mailbox{
		ID: row.ID, LocalPart: row.LocalPart, Address: row.LocalPart + "@" + domain,
		CreatedAt: row.CreatedAt.Time.UTC(), Revision: row.Version,
	}
}

func messageFromRow(row generated.MailboxMessage) Message {
	return Message{
		ID: row.ID, Sender: row.Sender, Subject: row.Subject,
		Recipients: append([]string(nil), row.Recipients...), ReceivedAt: row.ReceivedAt.Time.UTC(),
		ExpiresAt: row.ExpiresAt.Time.UTC(), ReadAt: util.TimestampToTime(row.ReadAt),
		HasAttachments: row.HasAttachments, Revision: row.Version,
	}
}

func mapCreateError(err error) error {
	if errors.Is(err, ErrForbidden) {
		return ErrForbidden
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.ConstraintName {
		case "company_mailboxes_company_key":
			return ErrAlreadyConfigured
		case "company_mailboxes_local_part_key":
			return ErrAddressUnavailable
		}
	}
	return fmt.Errorf("create Company mailbox: %w", err)
}

func (s *DefaultMailboxService) observeWebhook(outcome string) {
	if s.observer != nil {
		s.observer.ObserveMailboxWebhook(outcome)
	}
}

func (s *DefaultMailboxService) observeProvider(outcome string) {
	if s.observer != nil {
		s.observer.ObserveMailboxProvider(outcome)
	}
}

func (s *DefaultMailboxService) observeCache(outcome string) {
	if s.observer != nil {
		s.observer.ObserveMailboxCache(outcome)
	}
}
