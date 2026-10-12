package email

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"
)

const (
	maxOutboundRecipients = 50
	maxOutboundBatch      = 100
	maxOutboundSubject    = 200
	maxOutboundTextBytes  = 64 << 10
)

// OutboundMessage is one provider-neutral, plain-text email. Boardly domains
// own authorization and From construction; adapters only transport this exact
// immutable payload.
type OutboundMessage struct {
	From    string
	To      []string
	CC      []string
	Subject string
	Text    string
}

// OutboundProvider accepts one email or an ordered batch. The idempotency key
// fences the complete request and must be reused unchanged for retries.
type OutboundProvider interface {
	Send(context.Context, OutboundMessage, string) (Delivery, error)
	SendBatch(context.Context, []OutboundMessage, string) ([]Delivery, error)
}

func validateOutboundRequest(messages []OutboundMessage, idempotencyKey string) error {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if len(messages) < 1 || len(messages) > maxOutboundBatch || idempotencyKey == "" ||
		len(idempotencyKey) > maxIdempotencyKeyLength {
		return fmt.Errorf("%w: outbound request is invalid", ErrInvalidMessage)
	}
	for _, message := range messages {
		if err := validateOutboundMessage(message); err != nil {
			return err
		}
	}
	return nil
}

func validateOutboundMessage(message OutboundMessage) error {
	from, err := mail.ParseAddress(strings.TrimSpace(message.From))
	if err != nil || from.Address == "" {
		return fmt.Errorf("%w: sender is invalid", ErrInvalidMessage)
	}
	count := len(message.To) + len(message.CC)
	if count < 1 || count > maxOutboundRecipients {
		return fmt.Errorf("%w: recipient count is invalid", ErrInvalidMessage)
	}
	seen := make(map[string]struct{}, count)
	for _, values := range [][]string{message.To, message.CC} {
		for _, value := range values {
			address, parseErr := mail.ParseAddress(value)
			if parseErr != nil || address.Address != value ||
				value != strings.ToLower(strings.TrimSpace(value)) {
				return fmt.Errorf("%w: recipient is invalid", ErrInvalidMessage)
			}
			if _, duplicate := seen[value]; duplicate {
				return fmt.Errorf("%w: duplicate recipient", ErrInvalidMessage)
			}
			seen[value] = struct{}{}
		}
	}
	subjectLength := utf8.RuneCountInString(message.Subject)
	if !utf8.ValidString(message.Subject) || subjectLength < 1 ||
		subjectLength > maxOutboundSubject ||
		strings.ContainsAny(message.Subject, "\r\n") ||
		len(message.Text) < 1 || len(message.Text) > maxOutboundTextBytes {
		return fmt.Errorf("%w: outbound content is invalid", ErrInvalidMessage)
	}
	return nil
}

func cloneOutboundMessages(messages []OutboundMessage) []OutboundMessage {
	cloned := make([]OutboundMessage, len(messages))
	for index, message := range messages {
		message.To = append([]string(nil), message.To...)
		message.CC = append([]string(nil), message.CC...)
		cloned[index] = message
	}
	return cloned
}

var _ OutboundProvider = (*ResendProvider)(nil)
