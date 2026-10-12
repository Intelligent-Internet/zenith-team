package mailbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/infra/email"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type ProviderRateLimitError struct {
	RetryAfter time.Duration
}

func (e *ProviderRateLimitError) Error() string {
	return fmt.Sprintf("%s; retry after %s", ErrProviderRateLimited, e.RetryAfter)
}

func (e *ProviderRateLimitError) Unwrap() error {
	return ErrProviderRateLimited
}

func (s *DefaultMailboxService) GetDetail(
	ctx context.Context,
	principal auth.UserPrincipal,
	messageID uuid.UUID,
) (Detail, error) {
	row, message, err := s.authorizeMessage(ctx, principal, messageID)
	if err != nil {
		return Detail{}, err
	}
	attachments, err := s.listAttachments(ctx, principal, messageID)
	if err != nil {
		return Detail{}, err
	}
	content, err := s.getDetailContent(ctx, row.ProviderEmailID)
	if err != nil {
		return Detail{}, err
	}
	return Detail{Message: message, DetailContent: content, Attachments: attachments}, nil
}

func (s *DefaultMailboxService) MarkRead(
	ctx context.Context,
	principal auth.UserPrincipal,
	messageID uuid.UUID,
) (Message, error) {
	if _, _, err := s.authorizeMessage(ctx, principal, messageID); err != nil {
		return Message{}, err
	}
	var row generated.MailboxMessage
	var change events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := lockMailboxMutationMembership(ctx, queries, principal, ErrForbidden); err != nil {
			return err
		}
		var err error
		row, err = queries.MarkMailboxMessageRead(ctx, generated.MarkMailboxMessageReadParams{
			CompanyID: principal.CompanyID, MessageID: messageID,
			ActorUserID: principal.UserID, ActorRoles: auth.RolesWith(auth.PermissionMailboxManage),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrMessageNotFound
		}
		if err != nil {
			return fmt.Errorf("mark mailbox message read: %w", err)
		}
		change, err = newChangedEvent(
			ctx,
			queries,
			principal.CompanyID,
			"user",
			&principal.UserID,
			ChangeReasonMessageRead,
			&messageID,
			s.now(),
		)
		return err
	})
	if err != nil {
		return Message{}, err
	}
	publishMailboxEvents(ctx, s.bus, []events.Event{change})
	return messageFromRow(row), nil
}

func (s *DefaultMailboxService) GetAttachment(
	ctx context.Context,
	principal auth.UserPrincipal,
	messageID uuid.UUID,
	attachmentID uuid.UUID,
) (AttachmentDownload, error) {
	if !validPrincipal(principal) {
		return AttachmentDownload{}, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return AttachmentDownload{}, ErrForbidden
	}
	if !util.ValidUUID(messageID) || !util.ValidUUID(attachmentID) {
		return AttachmentDownload{}, ErrInvalidInput
	}
	row, err := s.store.Queries().GetMailboxAttachmentForMember(
		ctx,
		generated.GetMailboxAttachmentForMemberParams{
			ActorRoles:  auth.RolesWith(auth.PermissionMailboxManage),
			ActorUserID: principal.UserID,
			CompanyID:   principal.CompanyID, MessageID: messageID, AttachmentID: attachmentID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AttachmentDownload{}, ErrMessageNotFound
	}
	if err != nil {
		return AttachmentDownload{}, fmt.Errorf("authorize mailbox attachment: %w", err)
	}
	if !row.MessageExpiresAt.Time.After(s.now()) {
		if err := s.expireProviderEmailIndexes(ctx, row.ProviderEmailID); err != nil {
			return AttachmentDownload{}, err
		}
		return AttachmentDownload{}, ErrMessageExpired
	}
	download, err := s.provider.RetrieveReceivedAttachment(
		ctx,
		row.ProviderEmailID,
		row.ProviderAttachmentID,
	)
	if err != nil {
		return AttachmentDownload{}, s.resolveProviderError(
			ctx,
			s.classifyProviderError(err),
			row.ProviderEmailID,
		)
	}
	s.observeProvider("success")
	return AttachmentDownload{URL: download.URL, ExpiresAt: download.ExpiresAt}, nil
}

func (s *DefaultMailboxService) authorizeMessage(
	ctx context.Context,
	principal auth.UserPrincipal,
	messageID uuid.UUID,
) (generated.MailboxMessage, Message, error) {
	if !validPrincipal(principal) {
		return generated.MailboxMessage{}, Message{}, ErrUnauthenticated
	}
	if !principal.Can(auth.PermissionMailboxManage) {
		return generated.MailboxMessage{}, Message{}, ErrForbidden
	}
	if !util.ValidUUID(messageID) {
		return generated.MailboxMessage{}, Message{}, ErrInvalidInput
	}
	row, err := s.store.Queries().GetMailboxMessageForMember(
		ctx,
		generated.GetMailboxMessageForMemberParams{
			ActorRoles:  auth.RolesWith(auth.PermissionMailboxManage),
			ActorUserID: principal.UserID,
			CompanyID:   principal.CompanyID, MessageID: messageID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.MailboxMessage{}, Message{}, ErrMessageNotFound
	}
	if err != nil {
		return generated.MailboxMessage{}, Message{}, fmt.Errorf("authorize mailbox message: %w", err)
	}
	message := messageFromRow(row)
	if !row.ExpiresAt.Time.After(s.now()) {
		if err := s.expireProviderEmailIndexes(ctx, row.ProviderEmailID); err != nil {
			return generated.MailboxMessage{}, Message{}, err
		}
		return generated.MailboxMessage{}, Message{}, ErrMessageExpired
	}
	return row, message, nil
}

func (s *DefaultMailboxService) listAttachments(
	ctx context.Context,
	principal auth.UserPrincipal,
	messageID uuid.UUID,
) ([]Attachment, error) {
	rows, err := s.store.Queries().ListMailboxMessageAttachmentsForMember(
		ctx,
		generated.ListMailboxMessageAttachmentsForMemberParams{
			ActorRoles:  auth.RolesWith(auth.PermissionMailboxManage),
			ActorUserID: principal.UserID,
			CompanyID:   principal.CompanyID, MessageID: messageID,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list mailbox message attachments: %w", err)
	}
	attachments := make([]Attachment, 0, len(rows))
	for _, row := range rows {
		attachment := Attachment{
			ID: row.ID, Filename: row.Filename, MIMEType: row.MimeType,
		}
		if row.SizeBytes.Valid {
			attachment.SizeBytes = &row.SizeBytes.Int64
		}
		if row.Disposition.Valid {
			attachment.Disposition = &row.Disposition.String
		}
		if row.ContentID.Valid {
			attachment.ContentID = &row.ContentID.String
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func (s *DefaultMailboxService) getDetailContent(
	ctx context.Context,
	providerEmailID string,
) (DetailContent, error) {
	if s.providerEmailExpired(ctx, providerEmailID) {
		return DetailContent{}, ErrMessageExpired
	}
	if content, found := s.cachedDetail(ctx, providerEmailID); found {
		return content, nil
	}
	value, err, _ := s.detailGroup.Do(providerEmailID, func() (any, error) {
		if s.providerEmailExpired(ctx, providerEmailID) {
			return DetailContent{}, ErrMessageExpired
		}
		if content, found := s.cachedDetail(ctx, providerEmailID); found {
			return content, nil
		}
		received, err := s.provider.RetrieveReceivedEmail(ctx, providerEmailID)
		if err != nil {
			classified := s.classifyProviderError(err)
			if errors.Is(classified, email.ErrReceivedEmailNotFound) {
				if err := s.expireProviderEmailIndexes(ctx, providerEmailID); err != nil {
					return DetailContent{}, err
				}
				return DetailContent{}, ErrMessageExpired
			}
			return DetailContent{}, classified
		}
		if received.ID != providerEmailID {
			s.observeProvider("invalid")
			return DetailContent{}, ErrProviderUnavailable
		}
		content := normalizeDetailContent(DetailContent{
			From: received.From, To: received.To, CC: received.CC,
			BCC: received.BCC, ReplyTo: received.ReplyTo,
			HTML: sanitizeHTML(received.HTML), Text: sanitizeText(received.Text),
		})
		s.observeProvider("success")
		if s.cache != nil {
			if err := s.cache.Set(ctx, providerEmailID, content, detailCacheTTL); err != nil {
				s.observeCache("error")
			}
		}
		return content, nil
	})
	if err != nil {
		return DetailContent{}, err
	}
	content, ok := value.(DetailContent)
	if !ok {
		return DetailContent{}, ErrInvalidStore
	}
	return content, nil
}

func (s *DefaultMailboxService) cachedDetail(ctx context.Context, providerEmailID string) (DetailContent, bool) {
	if s.cache == nil {
		s.observeCache("miss")
		return DetailContent{}, false
	}
	content, found, err := s.cache.Get(ctx, providerEmailID)
	if err != nil {
		s.observeCache("error")
		return DetailContent{}, false
	}
	if found {
		s.observeCache("hit")
		return content, true
	}
	s.observeCache("miss")
	return DetailContent{}, false
}

func (s *DefaultMailboxService) classifyProviderError(err error) error {
	if errors.Is(err, email.ErrReceivedEmailNotFound) {
		s.observeProvider("not_found")
		return email.ErrReceivedEmailNotFound
	}
	var providerRateLimit *email.RateLimitError
	if errors.As(err, &providerRateLimit) {
		s.observeProvider("rate_limited")
		return &ProviderRateLimitError{RetryAfter: providerRateLimit.RetryAfter}
	}
	s.observeProvider("unavailable")
	return ErrProviderUnavailable
}

func (s *DefaultMailboxService) resolveProviderError(
	ctx context.Context,
	err error,
	providerEmailID string,
) error {
	if errors.Is(err, email.ErrReceivedEmailNotFound) {
		if deleteErr := s.expireProviderEmailIndexes(ctx, providerEmailID); deleteErr != nil {
			return deleteErr
		}
		return ErrMessageExpired
	}
	var providerRateLimit *ProviderRateLimitError
	if errors.As(err, &providerRateLimit) {
		return providerRateLimit
	}
	return ErrProviderUnavailable
}

func (s *DefaultMailboxService) expireProviderEmailIndexes(ctx context.Context, providerEmailID string) error {
	var pending []events.Event
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		refs, err := queries.ListMailboxMessageRefsByProviderEmailID(ctx, providerEmailID)
		if err != nil {
			return fmt.Errorf("list expired mailbox message indexes: %w", err)
		}
		messageIDs := make([]uuid.UUID, 0, len(refs))
		for _, ref := range refs {
			messageIDs = append(messageIDs, ref.ID)
			change, err := newChangedEvent(
				ctx, queries,
				ref.CompanyID,
				"system",
				nil,
				ChangeReasonMessageRemoved,
				&ref.ID,
				s.now(),
			)
			if err != nil {
				return err
			}
			pending = append(pending, change)
		}
		if len(messageIDs) == 0 {
			return nil
		}
		if _, err := queries.DeleteMailboxAttachmentsByMessageIDs(ctx, messageIDs); err != nil {
			return fmt.Errorf("delete expired mailbox attachments: %w", err)
		}
		if _, err := queries.DeleteMailboxMessagesByIDs(ctx, messageIDs); err != nil {
			return fmt.Errorf("delete expired mailbox messages: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("delete expired mailbox indexes: %w", err)
	}
	if s.cache != nil {
		if err := s.cache.Delete(ctx, providerEmailID); err != nil {
			s.observeCache("error")
		}
	}
	s.markProviderEmailExpired(ctx, providerEmailID)
	publishMailboxEvents(ctx, s.bus, pending)
	return nil
}

func (s *DefaultMailboxService) providerEmailExpired(ctx context.Context, providerEmailID string) bool {
	now := s.now()
	if s.expirations.Contains(providerEmailID, now) {
		s.observeCache("hit")
		return true
	}
	if s.expiryCache == nil {
		return false
	}
	expired, err := s.expiryCache.IsExpired(ctx, providerEmailID)
	if err != nil {
		s.observeCache("error")
		return false
	}
	if !expired {
		return false
	}
	s.observeCache("hit")
	s.expirations.Mark(providerEmailID, now.Add(expiryCacheTTL), now)
	return true
}

func (s *DefaultMailboxService) markProviderEmailExpired(ctx context.Context, providerEmailID string) {
	now := s.now()
	s.expirations.Mark(providerEmailID, now.Add(expiryCacheTTL), now)
	if s.expiryCache == nil {
		return
	}
	if err := s.expiryCache.MarkExpired(ctx, providerEmailID, expiryCacheTTL); err != nil {
		s.observeCache("error")
	}
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}

func normalizeDetailContent(content DetailContent) DetailContent {
	content.To = cloneStrings(content.To)
	content.CC = cloneStrings(content.CC)
	content.BCC = cloneStrings(content.BCC)
	content.ReplyTo = cloneStrings(content.ReplyTo)
	return content
}
