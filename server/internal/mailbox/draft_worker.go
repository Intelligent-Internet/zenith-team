package mailbox

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/infra/email"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	defaultDraftWorkerPollInterval = time.Second
	defaultDraftWorkerLease        = time.Minute
	maxDraftWorkerRounds           = 100
	maxDraftSendAttempts           = 8
	maxDraftSendRetryAge           = 23 * time.Hour
	maxDraftSendRetryDelay         = 15 * time.Minute
)

var ErrDraftWorkerConfiguration = errors.New("invalid Agent Mail worker configuration")

type DraftWorkerOptions struct {
	Store         *db.Store
	Provider      email.OutboundProvider
	Bus           *events.Bus
	Now           func() time.Time
	PollInterval  time.Duration
	LeaseDuration time.Duration
	OnError       func(error)
}

type DraftWorker struct {
	store    *db.Store
	provider email.OutboundProvider
	bus      *events.Bus
	now      func() time.Time
	options  DraftWorkerOptions
}

func NewDraftWorker(options DraftWorkerOptions) (*DraftWorker, error) {
	if options.Store == nil || options.Provider == nil {
		return nil, ErrDraftWorkerConfiguration
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultDraftWorkerPollInterval
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = defaultDraftWorkerLease
	}
	if options.PollInterval < 100*time.Millisecond || options.PollInterval > time.Hour ||
		options.LeaseDuration < 10*time.Second || options.LeaseDuration > time.Hour {
		return nil, ErrDraftWorkerConfiguration
	}
	return &DraftWorker{
		store: options.Store, provider: options.Provider, bus: options.Bus,
		now: options.Now, options: options,
	}, nil
}

func (w *DraftWorker) Run(ctx context.Context) error {
	if w == nil || w.store == nil || w.provider == nil || ctx == nil {
		return ErrDraftWorkerConfiguration
	}
	ticker := time.NewTicker(w.options.PollInterval)
	defer ticker.Stop()
	for {
		if err := w.processAvailable(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			w.observeError(err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *DraftWorker) processAvailable(ctx context.Context) error {
	for range maxDraftWorkerRounds {
		processed, err := w.ProcessOnce(ctx)
		if err != nil {
			return err
		}
		if !processed {
			return nil
		}
	}
	return nil
}

func (w *DraftWorker) ProcessOnce(ctx context.Context) (bool, error) {
	if w == nil || w.store == nil || w.provider == nil || ctx == nil {
		return false, ErrDraftWorkerConfiguration
	}
	now := w.now().UTC()
	leaseID, err := uuid.NewV7()
	if err != nil {
		return false, errors.New("generate Agent Mail send lease ID")
	}
	var draft generated.MailboxDraft
	err = w.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		claimed, claimErr := queries.ClaimNextMailboxDraftSend(
			ctx, generated.ClaimNextMailboxDraftSendParams{
				LeaseID: leaseID, LeaseExpiresAt: timestamp(now.Add(w.options.LeaseDuration)),
				NowAt: timestamp(now),
			},
		)
		if claimErr != nil {
			return claimErr
		}
		draft = claimed
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim Agent Mail send: %w", err)
	}

	deliveryRows, err := loadDraftDeliveries(ctx, w.store.Queries(), draft.CompanyID, draft.ID)
	if err != nil {
		return true, fmt.Errorf("load claimed Agent Mail delivery: %w", err)
	}
	// Resend only guarantees idempotency keys for 24 hours. Stop one hour before
	// that boundary so a process crash after provider acceptance can never turn
	// a stale durable retry into a duplicate delivery.
	if !draft.SendRequestedAt.Valid || now.Sub(draft.SendRequestedAt.Time) >= maxDraftSendRetryAge {
		return true, w.finalizeFailed(
			ctx, draft, deliveryRows, leaseID, "send_expired", now,
		)
	}
	messages := outboundMessages(draft, deliveryRows)
	key := fmt.Sprintf("agent-mail/%s/%d", draft.ID, draft.SendGeneration)
	accepted, sendErr := w.send(ctx, messages, key)
	if sendErr != nil {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		return true, w.handleSendFailure(ctx, draft, deliveryRows, leaseID, sendErr, now)
	}
	if len(accepted) != len(deliveryRows) {
		return true, w.handleSendFailure(
			ctx, draft, deliveryRows, leaseID,
			fmt.Errorf("%w: provider accepted an invalid delivery count", email.ErrUnavailable), now,
		)
	}
	return true, w.finalizeSent(ctx, draft, deliveryRows, accepted, leaseID, now)
}

func outboundMessages(
	draft generated.MailboxDraft,
	deliveries []generated.MailboxDraftDelivery,
) []email.OutboundMessage {
	from := (&mail.Address{Name: draft.FromName, Address: draft.FromAddress}).String()
	messages := make([]email.OutboundMessage, len(deliveries))
	for index, delivery := range deliveries {
		messages[index] = email.OutboundMessage{
			From: from, To: append([]string(nil), delivery.ToAddresses...),
			CC:      append([]string(nil), delivery.CcAddresses...),
			Subject: draft.Subject, Text: draft.BodyText,
		}
	}
	return messages
}

func (w *DraftWorker) send(
	ctx context.Context,
	messages []email.OutboundMessage,
	idempotencyKey string,
) ([]email.Delivery, error) {
	if len(messages) == 1 {
		delivery, err := w.provider.Send(ctx, messages[0], idempotencyKey)
		if err != nil {
			return nil, err
		}
		return []email.Delivery{delivery}, nil
	}
	return w.provider.SendBatch(ctx, messages, idempotencyKey)
}

func (w *DraftWorker) finalizeSent(
	ctx context.Context,
	claimed generated.MailboxDraft,
	deliveryRows []generated.MailboxDraftDelivery,
	accepted []email.Delivery,
	leaseID uuid.UUID,
	now time.Time,
) error {
	var change events.Event
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := lockDraftSendLease(ctx, queries, claimed, leaseID); err != nil {
			return err
		}
		acceptedRows := make([]generated.MailboxDraftDelivery, len(deliveryRows))
		for index, delivery := range accepted {
			if !validProviderID(delivery.ID) {
				return errors.New("Agent Mail provider returned an invalid delivery ID")
			}
			row, err := queries.AcceptMailboxDraftDelivery(
				ctx, generated.AcceptMailboxDraftDeliveryParams{
					ProviderEmailID: delivery.ID, AcceptedAt: timestamp(now),
					CompanyID: claimed.CompanyID, DeliveryID: deliveryRows[index].ID,
					DraftID: claimed.ID,
				},
			)
			if err != nil {
				return fmt.Errorf("accept Agent Mail delivery: %w", err)
			}
			acceptedRows[index] = row
		}
		sent, err := queries.MarkMailboxDraftSent(ctx, generated.MarkMailboxDraftSentParams{
			SentAt: timestamp(now), CompanyID: claimed.CompanyID, DraftID: claimed.ID,
			SendGeneration: claimed.SendGeneration, LeaseID: leaseID,
		})
		if err != nil {
			return fmt.Errorf("mark Agent Mail draft sent: %w", err)
		}
		group, err := queries.GetMailboxDraftGroupWithConversationRoute(
			ctx, generated.GetMailboxDraftGroupWithConversationRouteParams{
				CompanyID: claimed.CompanyID, GroupID: claimed.GroupID,
			},
		)
		if err != nil {
			return fmt.Errorf("get sent Agent Mail group: %w", err)
		}
		mapped := draftFromRows(sent, acceptedRows)
		change, err = newDraftChangedEvent(
			ctx, queries,
			claimed.CompanyID, group.MailboxDraftGroup.ConversationID, events.SystemActor(),
			mapped, mailboxDraftChangeSent, group.ConversationKind,
			util.NullUUIDPtr(group.CreatedByUserID), now,
		)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	publishMailboxEvents(ctx, w.bus, []events.Event{change})
	return nil
}

func (w *DraftWorker) handleSendFailure(
	ctx context.Context,
	claimed generated.MailboxDraft,
	deliveryRows []generated.MailboxDraftDelivery,
	leaseID uuid.UUID,
	sendErr error,
	now time.Time,
) error {
	reason := "provider_unavailable"
	if errors.Is(sendErr, email.ErrInvalidMessage) {
		reason = "provider_rejected"
	} else if errors.Is(sendErr, email.ErrRateLimited) {
		reason = "provider_rate_limited"
	}
	retry := errors.Is(sendErr, email.ErrRateLimited) || errors.Is(sendErr, email.ErrUnavailable)
	if !claimed.SendRequestedAt.Valid || now.Sub(claimed.SendRequestedAt.Time) >= maxDraftSendRetryAge ||
		claimed.SendAttempts >= maxDraftSendAttempts {
		retry = false
	}
	if retry {
		delay := draftRetryDelay(claimed.SendAttempts, sendErr)
		return w.reschedule(ctx, claimed, leaseID, reason, now.Add(delay), now)
	}
	return w.finalizeFailed(ctx, claimed, deliveryRows, leaseID, reason, now)
}

func draftRetryDelay(attempt int32, sendErr error) time.Duration {
	var rateLimit *email.RateLimitError
	if errors.As(sendErr, &rateLimit) && rateLimit.RetryAfter > 0 {
		if rateLimit.RetryAfter > maxDraftSendRetryDelay {
			return maxDraftSendRetryDelay
		}
		return rateLimit.RetryAfter
	}
	delay := time.Second
	for current := int32(1); current < attempt && delay < maxDraftSendRetryDelay; current++ {
		delay *= 2
	}
	if delay > maxDraftSendRetryDelay {
		return maxDraftSendRetryDelay
	}
	return delay
}

func (w *DraftWorker) reschedule(
	ctx context.Context,
	claimed generated.MailboxDraft,
	leaseID uuid.UUID,
	reason string,
	nextAttemptAt time.Time,
	now time.Time,
) error {
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := lockDraftSendLease(ctx, queries, claimed, leaseID); err != nil {
			return err
		}
		_, err := queries.RescheduleMailboxDraftSend(ctx, generated.RescheduleMailboxDraftSendParams{
			NextAttemptAt: timestamp(nextAttemptAt), FailureReason: reason, NowAt: timestamp(now),
			CompanyID: claimed.CompanyID, DraftID: claimed.ID,
			SendGeneration: claimed.SendGeneration, LeaseID: leaseID,
		})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reschedule Agent Mail draft: %w", err)
	}
	return nil
}

func (w *DraftWorker) finalizeFailed(
	ctx context.Context,
	claimed generated.MailboxDraft,
	deliveryRows []generated.MailboxDraftDelivery,
	leaseID uuid.UUID,
	reason string,
	now time.Time,
) error {
	var change events.Event
	err := w.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := lockDraftSendLease(ctx, queries, claimed, leaseID); err != nil {
			return err
		}
		failed, err := queries.MarkMailboxDraftFailed(ctx, generated.MarkMailboxDraftFailedParams{
			FailedAt: timestamp(now), FailureReason: reason, CompanyID: claimed.CompanyID,
			DraftID: claimed.ID, SendGeneration: claimed.SendGeneration, LeaseID: leaseID,
		})
		if err != nil {
			return fmt.Errorf("mark Agent Mail draft failed: %w", err)
		}
		group, err := queries.GetMailboxDraftGroupWithConversationRoute(
			ctx, generated.GetMailboxDraftGroupWithConversationRouteParams{
				CompanyID: claimed.CompanyID, GroupID: claimed.GroupID,
			},
		)
		if err != nil {
			return fmt.Errorf("get failed Agent Mail group: %w", err)
		}
		mapped := draftFromRows(failed, deliveryRows)
		change, err = newDraftChangedEvent(
			ctx, queries,
			claimed.CompanyID, group.MailboxDraftGroup.ConversationID, events.SystemActor(),
			mapped, mailboxDraftChangeFailed, group.ConversationKind,
			util.NullUUIDPtr(group.CreatedByUserID), now,
		)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	publishMailboxEvents(ctx, w.bus, []events.Event{change})
	return nil
}

// lockDraftSendLease re-takes the claimed draft's row inside the caller's
// transaction and fails when the send generation or lease has moved on, so a
// superseded worker cannot finish the send it no longer owns.
func lockDraftSendLease(
	ctx context.Context,
	queries *generated.Queries,
	draft generated.MailboxDraft,
	leaseID uuid.UUID,
) error {
	_, err := queries.LockMailboxDraftSendLease(
		ctx, generated.LockMailboxDraftSendLeaseParams{
			DraftID: draft.ID, CompanyID: draft.CompanyID,
			SendGeneration: draft.SendGeneration, LeaseID: leaseID,
		},
	)
	return err
}

func (w *DraftWorker) observeError(err error) {
	if w.options.OnError == nil || err == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		w.options.OnError(err)
	}()
}
