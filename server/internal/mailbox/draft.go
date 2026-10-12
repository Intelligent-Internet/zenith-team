package mailbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	maxDraftSubjectLength      = 200
	maxDraftBodyBytes          = 64 << 10
	maxAgentMailDeliveries     = 100
	maxAgentMailRecipients     = 100
	maxAgentMailListSourceRuns = 100
	maxDeliveryRecipients      = 50
	defaultDailyRecipientLimit = 100
	dailyRecipientWindow       = 24 * time.Hour
)

var (
	ErrAgentMailCompanyEmailRequired = errors.New("Agent Mail Company Email is required")
	ErrAgentMailForbidden            = errors.New("Agent Mail access forbidden")
	ErrAgentMailDraftNotFound        = errors.New("Agent Mail draft not found")
	ErrAgentMailDraftConflict        = errors.New("Agent Mail draft conflict")
	ErrAgentMailInvalidRecipients    = errors.New("Agent Mail recipients are invalid")
	ErrAgentMailSendLimitExceeded    = errors.New("Agent Mail send limit exceeded")
	ErrAgentMailInvalidInput         = errors.New("Agent Mail input is invalid")
)

type DraftStatus string

const (
	DraftStatusDraft   DraftStatus = "draft"
	DraftStatusSending DraftStatus = "sending"
	DraftStatusSent    DraftStatus = "sent"
	DraftStatusFailed  DraftStatus = "failed"
)

type DraftDeliveryInput struct {
	To []string `json:"to" binding:"required,min=1,max=50,dive,max=320"`
	CC []string `json:"cc" binding:"max=50,dive,max=320"`
}

type DraftInput struct {
	Subject    string               `json:"subject"    binding:"required,max=200"`
	Text       string               `json:"text"       binding:"required,max=65536"`
	Deliveries []DraftDeliveryInput `json:"deliveries" binding:"required,min=1,max=100,dive"`
}

type DraftDelivery struct {
	ID         uuid.UUID  `json:"id"`
	To         []string   `json:"to"`
	CC         []string   `json:"cc"`
	AcceptedAt *time.Time `json:"acceptedAt"`
}

type Draft struct {
	ID             uuid.UUID       `json:"id"`
	GroupID        uuid.UUID       `json:"groupId"`
	Subject        string          `json:"subject"`
	Text           string          `json:"text"`
	FromName       string          `json:"fromName"`
	FromAddress    string          `json:"fromAddress"`
	Status         DraftStatus     `json:"status"`
	SendGeneration int64           `json:"sendGeneration"`
	RecipientCount int             `json:"recipientCount"`
	Deliveries     []DraftDelivery `json:"deliveries"`
	SentAt         *time.Time      `json:"sentAt"`
	FailedAt       *time.Time      `json:"failedAt"`
	FailureReason  *string         `json:"failureReason"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	Revision       int64           `json:"revision"`
}

type DraftGroup struct {
	ID             uuid.UUID `json:"id"`
	ConversationID uuid.UUID `json:"conversationId"`
	SourceRunID    uuid.UUID `json:"sourceRunId"`
	AgentID        uuid.UUID `json:"agentId"`
	RecipientCount int       `json:"recipientCount"`
	Drafts         []Draft   `json:"drafts"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type TrustedDraftContext struct {
	CompanyID      uuid.UUID
	ConversationID uuid.UUID
	RunID          uuid.UUID
	AgentID        uuid.UUID
	RequestKey     uuid.UUID
}

type DraftCreateResult struct {
	DraftGroup
	Created bool
}

type UpdateDraftInput struct {
	Subject          string
	Text             string
	Deliveries       []DraftDeliveryInput
	ExpectedRevision int64
}

type DraftRevision struct {
	ID       uuid.UUID `json:"id"       binding:"required"`
	Revision int64     `json:"revision" binding:"required,min=1"`
}

// DraftService owns reviewable Agent Mail drafts and their durable send
// requests.
type DraftService interface {
	Create(context.Context, auth.UserPrincipal, TrustedDraftContext, []DraftInput) (DraftCreateResult, error)
	ListGroups(context.Context, auth.UserPrincipal, uuid.UUID, []uuid.UUID) ([]DraftGroup, error)
	UpdateDraft(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, UpdateDraftInput) (Draft, error)
	DeleteDraft(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) error
	QueueDraft(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64) (Draft, error)
	QueueGroup(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []DraftRevision) (DraftGroup, error)
}

var _ DraftService = (*DefaultDraftService)(nil)

type DraftServiceOptions struct {
	Store               *db.Store
	Bus                 *events.Bus
	Domain              string
	Now                 func() time.Time
	DailyRecipientLimit int
}

type DefaultDraftService struct {
	store               *db.Store
	bus                 *events.Bus
	domain              string
	now                 func() time.Time
	dailyRecipientLimit int
}

func NewDraftService(options DraftServiceOptions) (*DefaultDraftService, error) {
	if options.Store == nil || options.Bus == nil {
		return nil, errors.New("Agent Mail database store and event bus are required")
	}
	domain := strings.ToLower(strings.TrimSpace(options.Domain))
	if len(domain) > 253 || !domainPattern.MatchString(domain) {
		return nil, errors.New("Agent Mail domain is invalid")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.DailyRecipientLimit == 0 {
		options.DailyRecipientLimit = defaultDailyRecipientLimit
	}
	if options.DailyRecipientLimit < 1 || options.DailyRecipientLimit > 10000 {
		return nil, errors.New("Agent Mail daily recipient limit is invalid")
	}
	return &DefaultDraftService{
		store: options.Store, bus: options.Bus, domain: domain, now: options.Now,
		dailyRecipientLimit: options.DailyRecipientLimit,
	}, nil
}

func (s *DefaultDraftService) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	trusted TrustedDraftContext,
	drafts []DraftInput,
) (DraftCreateResult, error) {
	if !validPrincipal(principal) || !validTrustedDraftContext(trusted) ||
		principal.CompanyID != trusted.CompanyID {
		return DraftCreateResult{}, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return DraftCreateResult{}, ErrAgentMailForbidden
	}
	canonical, digest, err := canonicalizeDraftInputs(drafts)
	if err != nil {
		return DraftCreateResult{}, err
	}
	now := s.now().UTC()
	var result DraftCreateResult
	var changes []events.Event
	err = s.store.WithinTx(
		ctx,
		pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadWrite},
		func(queries *generated.Queries) error {
			if lockErr := lockMailboxMutationMembership(
				ctx,
				queries,
				principal,
				ErrAgentMailForbidden,
			); lockErr != nil {
				return lockErr
			}
			conversationRoute, lockErr := queries.LockAgentMailConversationForMutation(
				ctx,
				generated.LockAgentMailConversationForMutationParams{
					ActorUserID: principal.UserID,
					CompanyID:   trusted.CompanyID, ConversationID: trusted.ConversationID,
				},
			)
			if lockErr != nil {
				if errors.Is(lockErr, pgx.ErrNoRows) {
					return ErrAgentMailForbidden
				}
				return fmt.Errorf("lock Agent Mail Conversation: %w", lockErr)
			}
			lockedRun, lockErr := queries.LockAgentMailRunForCreate(
				ctx,
				generated.LockAgentMailRunForCreateParams{
					RunID: trusted.RunID, CompanyID: trusted.CompanyID,
					ConversationID: trusted.ConversationID, AgentID: trusted.AgentID,
				},
			)
			if lockErr != nil {
				if errors.Is(lockErr, pgx.ErrNoRows) {
					return ErrAgentMailForbidden
				}
				return fmt.Errorf("lock Agent Mail Run: %w", lockErr)
			}
			if lockedRun.Status != "running" ||
				(conversationRoute.ConversationKind != "direct" && conversationRoute.ConversationKind != "task") {
				return ErrAgentMailForbidden
			}
			if !conversationRoute.MailboxID.Valid || !conversationRoute.LocalPart.Valid {
				return ErrAgentMailCompanyEmailRequired
			}

			existing, existingErr := queries.GetMailboxDraftGroupByRunForUpdate(
				ctx, generated.GetMailboxDraftGroupByRunForUpdateParams{
					CompanyID: trusted.CompanyID, SourceRunID: trusted.RunID,
				},
			)
			if existingErr == nil {
				if existing.RequestKey != trusted.RequestKey ||
					!bytes.Equal(existing.PayloadSha256, digest) {
					return ErrAgentMailDraftConflict
				}
				loaded, loadErr := loadDraftGroup(ctx, queries, existing)
				result.DraftGroup = loaded
				return loadErr
			}
			if !errors.Is(existingErr, pgx.ErrNoRows) {
				return fmt.Errorf("get Agent Mail replay group: %w", existingErr)
			}
			groupRow, insertErr := queries.InsertMailboxDraftGroup(
				ctx,
				generated.InsertMailboxDraftGroupParams{
					CompanyID:       trusted.CompanyID,
					MailboxID:       conversationRoute.MailboxID.UUID,
					ConversationID:  trusted.ConversationID,
					SourceRunID:     trusted.RunID,
					RequestKey:      trusted.RequestKey,
					AgentID:         trusted.AgentID,
					CreatedByUserID: principal.UserID,
					PayloadSha256:   digest,
				},
			)
			if insertErr != nil {
				return mapAgentMailCreateError(insertErr)
			}
			fromName := strings.TrimSpace(conversationRoute.CompanyName)
			fromAddress := conversationRoute.LocalPart.String + "@" + s.domain
			if fromName == "" || !utf8.ValidString(fromName) ||
				utf8.RuneCountInString(fromName) > 200 || strings.ContainsAny(fromName, "\r\n") ||
				!validSimpleAddress(fromAddress) {
				return ErrInvalidStore
			}
			draftRows := make([]generated.MailboxDraft, 0, len(canonical))
			deliveryRows := make([]generated.MailboxDraftDelivery, 0)
			for draftIndex, input := range canonical {
				draftRow, draftErr := queries.InsertMailboxDraft(
					ctx,
					generated.InsertMailboxDraftParams{
						CompanyID: trusted.CompanyID,
						GroupID:   groupRow.ID,
						Ordinal: int32(
							draftIndex,
						),
						Subject:     input.Subject,
						BodyText:    input.Text,
						FromName:    fromName,
						FromAddress: fromAddress,
					},
				)
				if draftErr != nil {
					return fmt.Errorf("insert Agent Mail draft: %w", draftErr)
				}
				draftRows = append(draftRows, draftRow)
				for deliveryIndex, delivery := range input.Deliveries {
					deliveryRow, deliveryErr := queries.InsertMailboxDraftDelivery(
						ctx, generated.InsertMailboxDraftDeliveryParams{
							CompanyID: trusted.CompanyID, DraftID: draftRow.ID,
							Ordinal: int32(deliveryIndex), ToAddresses: delivery.To,
							CcAddresses: delivery.CC,
						},
					)
					if deliveryErr != nil {
						return fmt.Errorf("insert Agent Mail delivery: %w", deliveryErr)
					}
					deliveryRows = append(deliveryRows, deliveryRow)
				}
			}
			result.DraftGroup = composeDraftGroup(groupRow, draftRows, deliveryRows)
			result.Created = true
			for _, draft := range result.Drafts {
				change, eventErr := newDraftChangedEvent(
					ctx, queries,
					trusted.CompanyID, trusted.ConversationID,
					// The Run writes the drafts for its member (D31).
					events.RunActor(
						trusted.AgentID.String(),
						trusted.RunID.String(),
						principal.UserID.String(),
					),
					draft, mailboxDraftChangeCreated, conversationRoute.ConversationKind,
					util.NullUUIDPtr(conversationRoute.CreatedByUserID), now,
				)
				if eventErr != nil {
					return eventErr
				}
				changes = append(changes, change)
			}
			return nil
		},
	)
	if err != nil {
		return DraftCreateResult{}, err
	}
	publishMailboxEvents(ctx, s.bus, changes)
	return result, nil
}

func (s *DefaultDraftService) ListGroups(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	sourceRunIDs []uuid.UUID,
) ([]DraftGroup, error) {
	if !validPrincipal(principal) || !util.ValidUUID(conversationID) {
		return nil, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return nil, ErrAgentMailForbidden
	}
	if !validAgentMailListSourceRunIDs(sourceRunIDs) {
		return nil, ErrAgentMailInvalidInput
	}
	queries := s.store.Queries()
	if _, err := queries.AuthorizeAgentMailConversationForMember(
		ctx, generated.AuthorizeAgentMailConversationForMemberParams{
			ActorRoles:  auth.RolesWith(auth.PermissionMailboxManage),
			ActorUserID: principal.UserID,
			CompanyID:   principal.CompanyID, ConversationID: conversationID,
		},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAgentMailDraftNotFound
		}
		return nil, fmt.Errorf("authorize Agent Mail Conversation: %w", err)
	}
	rows, err := queries.ListMailboxDraftGroupsForConversation(
		ctx, generated.ListMailboxDraftGroupsForConversationParams{
			CompanyID: principal.CompanyID, ConversationID: conversationID,
			SourceRunIds: sourceRunIDs,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list Agent Mail groups: %w", err)
	}
	return loadDraftGroups(ctx, queries, rows)
}

func validAgentMailListSourceRunIDs(sourceRunIDs []uuid.UUID) bool {
	if len(sourceRunIDs) < 1 || len(sourceRunIDs) > maxAgentMailListSourceRuns {
		return false
	}
	seen := make(map[uuid.UUID]struct{}, len(sourceRunIDs))
	for _, sourceRunID := range sourceRunIDs {
		if sourceRunID == uuid.Nil {
			return false
		}
		if _, duplicate := seen[sourceRunID]; duplicate {
			return false
		}
		seen[sourceRunID] = struct{}{}
	}
	return true
}

func canonicalizeDraftInputs(drafts []DraftInput) ([]DraftInput, []byte, error) {
	if len(drafts) < 1 || len(drafts) > maxAgentMailDeliveries {
		return nil, nil, ErrAgentMailInvalidInput
	}
	canonical := make([]DraftInput, len(drafts))
	totalDeliveries, totalRecipients := 0, 0
	for draftIndex, draft := range drafts {
		draft.Subject = strings.TrimSpace(draft.Subject)
		subjectLength := utf8.RuneCountInString(draft.Subject)
		if !utf8.ValidString(draft.Subject) || subjectLength < 1 ||
			subjectLength > maxDraftSubjectLength ||
			strings.ContainsAny(draft.Subject, "\x00\r\n") || len(draft.Text) < 1 ||
			len(draft.Text) > maxDraftBodyBytes || !utf8.ValidString(draft.Text) ||
			strings.ContainsRune(draft.Text, '\x00') || len(draft.Deliveries) < 1 {
			return nil, nil, ErrAgentMailInvalidInput
		}
		copyDraft := DraftInput{Subject: draft.Subject, Text: draft.Text}
		copyDraft.Deliveries = make([]DraftDeliveryInput, len(draft.Deliveries))
		for deliveryIndex, delivery := range draft.Deliveries {
			copyDelivery, recipientCount, err := canonicalizeDraftDelivery(delivery)
			if err != nil {
				return nil, nil, err
			}
			copyDraft.Deliveries[deliveryIndex] = copyDelivery
			totalDeliveries++
			totalRecipients += recipientCount
		}
		canonical[draftIndex] = copyDraft
	}
	if totalDeliveries > maxAgentMailDeliveries || totalRecipients > maxAgentMailRecipients {
		return nil, nil, ErrAgentMailSendLimitExceeded
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, nil, errors.New("encode canonical Agent Mail drafts")
	}
	digest := sha256.Sum256(encoded)
	return canonical, digest[:], nil
}

func canonicalizeDraftDelivery(input DraftDeliveryInput) (DraftDeliveryInput, int, error) {
	if len(input.To) < 1 || len(input.To)+len(input.CC) > maxDeliveryRecipients {
		return DraftDeliveryInput{}, 0, ErrAgentMailInvalidRecipients
	}
	result := DraftDeliveryInput{
		To: make([]string, len(input.To)), CC: make([]string, len(input.CC)),
	}
	seen := make(map[string]struct{}, len(input.To)+len(input.CC))
	for _, target := range []struct {
		source []string
		target []string
	}{{input.To, result.To}, {input.CC, result.CC}} {
		for index, value := range target.source {
			value = strings.ToLower(strings.TrimSpace(value))
			if !validSimpleAddress(value) {
				return DraftDeliveryInput{}, 0, ErrAgentMailInvalidRecipients
			}
			if _, duplicate := seen[value]; duplicate {
				return DraftDeliveryInput{}, 0, ErrAgentMailInvalidRecipients
			}
			seen[value] = struct{}{}
			target.target[index] = value
		}
	}
	return result, len(seen), nil
}

func validSimpleAddress(value string) bool {
	if value == "" || len(value) > 320 || strings.ContainsAny(value, "\r\n") {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Name == "" && parsed.Address == value &&
		strings.Count(value, "@") == 1
}

func validTrustedDraftContext(value TrustedDraftContext) bool {
	return util.ValidUUID(value.CompanyID) && util.ValidUUID(value.ConversationID) &&
		value.RunID != uuid.Nil && util.ValidUUID(value.AgentID) &&
		util.ValidUUID(value.RequestKey)
}

func mapAgentMailCreateError(err error) error {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) &&
		postgresError.ConstraintName == "mailbox_draft_groups_company_run_key" {
		return ErrAgentMailDraftConflict
	}
	return fmt.Errorf("create Agent Mail draft group: %w", err)
}
