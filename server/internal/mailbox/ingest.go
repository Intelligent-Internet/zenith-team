package mailbox

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/infra/email"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type IngestResult struct {
	Delivered int
	Duplicate bool
}

func (s *DefaultMailboxService) Ingest(ctx context.Context, event email.ReceivedEvent) (IngestResult, error) {
	normalized, localParts, err := s.normalizeReceivedEvent(event)
	if err != nil {
		s.observeWebhook("invalid")
		return IngestResult{}, err
	}

	result := IngestResult{}
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		mailboxes, err := queries.ResolveMailboxesByLocalParts(ctx, localParts)
		if err != nil {
			return fmt.Errorf("resolve webhook mailbox recipients: %w", err)
		}
		outcome := "ignored"
		if len(mailboxes) > 0 {
			outcome = "delivered"
		}
		_, err = queries.InsertMailboxReceivedReceipt(ctx, generated.InsertMailboxReceivedReceiptParams{
			ProviderEventID: normalized.EventID, ProviderEmailID: normalized.EmailID,
			Outcome: outcome, ReceivedAt: timestamp(normalized.ReceivedAt),
			ExpiresAt: timestamp(normalized.ReceivedAt.Add(retentionPeriod)),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			result.Duplicate = true
			return nil
		}
		if err != nil {
			return fmt.Errorf("insert mailbox webhook receipt: %w", err)
		}

		for _, target := range mailboxes {
			messageID, created, err := insertReceivedMessage(ctx, queries, target, normalized)
			if err != nil {
				return err
			}
			if created {
				change, err := newChangedEvent(
					ctx, queries,
					target.CompanyID,
					"system",
					nil,
					ChangeReasonMessageReceived,
					&messageID,
					s.now(),
				)
				if err != nil {
					return err
				}
				pending = append(pending, change)
				result.Delivered++
			}
		}
		return nil
	})
	if err != nil {
		s.observeWebhook("failed")
		return IngestResult{}, fmt.Errorf("ingest received email metadata: %w", err)
	}
	publishMailboxEvents(ctx, s.bus, pending)
	switch {
	case result.Duplicate:
		s.observeWebhook("replay")
	case result.Delivered == 0:
		s.observeWebhook("ignored")
	default:
		s.observeWebhook("delivered")
	}
	return result, nil
}

func (s *DefaultMailboxService) normalizeReceivedEvent(
	event email.ReceivedEvent,
) (email.ReceivedEvent, []string, error) {
	event.EventID = strings.TrimSpace(event.EventID)
	event.EmailID = strings.TrimSpace(event.EmailID)
	event.From = strings.TrimSpace(event.From)
	event.Subject = strings.TrimSpace(event.Subject)
	if !validProviderID(event.EventID) || !validProviderID(event.EmailID) ||
		!validMetadataText(event.From, 2048, false) ||
		!validMetadataText(event.Subject, 998, true) || event.ReceivedAt.IsZero() ||
		len(event.To) < 1 || len(event.To) > 100 || len(event.Attachments) > 100 {
		return email.ReceivedEvent{}, nil, ErrInvalidInput
	}
	if _, err := mail.ParseAddress(event.From); err != nil {
		return email.ReceivedEvent{}, nil, ErrInvalidInput
	}

	localSet := make(map[string]struct{}, len(event.To))
	canonicalRecipients := make([]string, 0, len(event.To))
	for _, rawRecipient := range event.To {
		address, err := mail.ParseAddress(strings.TrimSpace(rawRecipient))
		if err != nil || !validMetadataText(address.Address, 320, false) {
			return email.ReceivedEvent{}, nil, ErrInvalidInput
		}
		canonicalRecipients = append(canonicalRecipients, strings.ToLower(address.Address))
		at := strings.LastIndexByte(address.Address, '@')
		if at < 1 || !strings.EqualFold(address.Address[at+1:], s.domain) {
			continue
		}
		localPart, err := ParseLocalPart(address.Address[:at])
		if err == nil {
			localSet[localPart] = struct{}{}
		}
	}
	event.To = canonicalRecipients
	for _, list := range [][]string{event.CC, event.BCC} {
		if len(list) > 100 {
			return email.ReceivedEvent{}, nil, ErrInvalidInput
		}
	}
	if err := validateReceivedAttachments(event.Attachments); err != nil {
		return email.ReceivedEvent{}, nil, err
	}
	localParts := make([]string, 0, len(localSet))
	for localPart := range localSet {
		localParts = append(localParts, localPart)
	}
	return event, localParts, nil
}

func insertReceivedMessage(
	ctx context.Context,
	queries *generated.Queries,
	target generated.CompanyMailbox,
	event email.ReceivedEvent,
) (uuid.UUID, bool, error) {
	message, err := queries.InsertMailboxMessage(ctx, generated.InsertMailboxMessageParams{
		CompanyID: target.CompanyID, MailboxID: target.ID,
		ProviderEmailID: event.EmailID, Sender: event.From, Subject: event.Subject,
		Recipients: event.To, ReceivedAt: timestamp(event.ReceivedAt),
		ExpiresAt:      timestamp(event.ReceivedAt.Add(retentionPeriod)),
		HasAttachments: len(event.Attachments) > 0,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, fmt.Errorf("insert mailbox message metadata: %w", err)
	}
	for _, attachment := range event.Attachments {
		_, err = queries.InsertMailboxMessageAttachment(
			ctx,
			generated.InsertMailboxMessageAttachmentParams{
				MessageID: message.ID, CompanyID: message.CompanyID,
				ProviderAttachmentID: attachment.ID, Filename: attachment.Filename,
				MimeType: attachment.ContentType, SizeBytes: optionalInt64(attachment.SizeBytes),
				Disposition: optionalText(attachment.Disposition),
				ContentID:   optionalText(attachment.ContentID),
			},
		)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, fmt.Errorf("insert mailbox attachment metadata: %w", err)
		}
	}
	return message.ID, true, nil
}

func validateReceivedAttachments(attachments []email.ReceivedAttachment) error {
	seen := make(map[string]struct{}, len(attachments))
	for _, attachment := range attachments {
		if !validProviderID(attachment.ID) ||
			!validMetadataText(attachment.Filename, 1024, false) ||
			!validMetadataText(attachment.ContentType, 255, false) ||
			(attachment.SizeBytes != nil && *attachment.SizeBytes < 0) ||
			(attachment.Disposition != "" && attachment.Disposition != "attachment" &&
				attachment.Disposition != "inline") ||
			!validMetadataText(attachment.ContentID, 1024, true) {
			return ErrInvalidInput
		}
		if _, duplicate := seen[attachment.ID]; duplicate {
			return ErrInvalidInput
		}
		seen[attachment.ID] = struct{}{}
	}
	return nil
}

func validProviderID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 512 &&
		utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validMetadataText(value string, maximum int, emptyAllowed bool) bool {
	return (emptyAllowed || value != "") && strings.TrimSpace(value) == value &&
		len(value) <= maximum && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func optionalInt64(value *int64) pgtype.Int8 {
	if value == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *value, Valid: true}
}
