package commerce

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	webhookReceiptLease = time.Minute
	maxWebhookBatch     = 100
	maxWebhookAttempts  = int32(32)
)

var (
	ErrInvalidWebhook  = errors.New("invalid Commerce webhook")
	ErrWebhookConflict = errors.New("Commerce webhook receipt conflict")
	ErrWebhookBusy     = errors.New("Commerce webhook processing is busy")

	errWebhookAccountBusy = errors.New("Commerce webhook account is busy")
	errWebhookDependency  = errors.New("Commerce webhook dependency is not ready")
)

type WebhookReceiptResult struct {
	Processed bool `json:"processed"`
	Duplicate bool `json:"duplicate"`
}

type WebhookReconciliationResult struct {
	Processed    int `json:"processed"`
	DeadLettered int `json:"deadLettered"`
}

type webhookReceiptOutcome uint8

const (
	webhookReceiptProcessed webhookReceiptOutcome = iota + 1
	webhookReceiptDeadLettered
)

type entitlementDecision string

const (
	entitlementActivate entitlementDecision = "activate"
	entitlementPreserve entitlementDecision = "preserve"
	entitlementDeny     entitlementDecision = "deny"
)

func (s *DefaultCommerceService) ProcessWebhook(
	ctx context.Context,
	payload []byte,
	signature string,
) (WebhookReceiptResult, error) {
	if ctx == nil || len(payload) == 0 || signature == "" {
		return WebhookReceiptResult{}, ErrInvalidWebhook
	}
	environment, provider, event, err := s.constructWebhookEvent(payload, signature)
	if err != nil {
		return WebhookReceiptResult{}, err
	}
	digest := sha256.Sum256(payload)
	return s.ingestProviderEvent(ctx, environment, provider, event, digest)
}

func (s *DefaultCommerceService) ingestPolledSandboxEvent(
	ctx context.Context,
	event WebhookEvent,
) (WebhookReceiptResult, error) {
	if ctx == nil || !validPolledSandboxEvent(event) {
		return WebhookReceiptResult{}, ErrInvalidWebhook
	}
	configuration, exists := s.environments[EnvironmentSandbox]
	if !exists || configuration.Webhook == nil {
		return WebhookReceiptResult{}, ErrInvalidProviderState
	}
	event.ProviderTime = event.ProviderTime.UTC()
	return s.ingestProviderEvent(
		ctx,
		EnvironmentSandbox,
		configuration.Webhook,
		event,
		developmentEventDigest(event),
	)
}

func (s *DefaultCommerceService) ingestProviderEvent(
	ctx context.Context,
	environment Environment,
	provider WebhookProvider,
	event WebhookEvent,
	digest [sha256.Size]byte,
) (WebhookReceiptResult, error) {
	account, err := s.store.Queries().GetCommerceAccountByProviderID(
		ctx,
		generated.GetCommerceAccountByProviderIDParams{
			Environment: string(environment), ProviderAccountID: event.AccountID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return WebhookReceiptResult{}, ErrInvalidWebhook
	}
	if err != nil {
		return WebhookReceiptResult{}, fmt.Errorf("resolve Commerce webhook account: %w", err)
	}
	receipt, insertErr := s.store.Queries().InsertCommerceWebhookReceipt(
		ctx,
		generated.InsertCommerceWebhookReceiptParams{
			CompanyID: account.CompanyID, Environment: string(environment),
			ProviderEventID: event.ID, ProviderAccountID: event.AccountID,
			EventType: event.Type, ProviderObjectID: nullableText(event.ObjectID),
			PayloadSha256: digest[:], Livemode: event.LiveMode,
			ProviderCreatedAt: requiredTimestamp(event.ProviderTime),
		},
	)
	duplicate := false
	if errors.Is(insertErr, pgx.ErrNoRows) {
		duplicate = true
		receipt, insertErr = s.store.Queries().GetCommerceWebhookReceipt(
			ctx,
			generated.GetCommerceWebhookReceiptParams{
				Environment: string(environment), ProviderEventID: event.ID,
			},
		)
	}
	if insertErr != nil {
		return WebhookReceiptResult{}, fmt.Errorf("record Commerce webhook receipt: %w", insertErr)
	}
	if !sameWebhookReceipt(receipt, account.CompanyID, environment, event) {
		return WebhookReceiptResult{}, ErrWebhookConflict
	}
	if receipt.State == "succeeded" || receipt.State == "dead_letter" {
		return WebhookReceiptResult{
			Processed: receipt.State == "succeeded",
			Duplicate: true,
		}, nil
	}
	outcome, err := s.processWebhookReceipt(ctx, provider, receipt, event)
	if err != nil {
		return WebhookReceiptResult{}, err
	}
	return WebhookReceiptResult{
		Processed: outcome == webhookReceiptProcessed,
		Duplicate: duplicate,
	}, nil
}

func validPolledSandboxEvent(event WebhookEvent) bool {
	if event.LiveMode || !providerIDPattern.MatchString(event.AccountID) ||
		!validPolledProviderID(event.ID, "evt_") ||
		!validPolledProviderID(event.ObjectID, "") || event.ProviderTime.IsZero() {
		return false
	}
	for _, eventType := range sandboxCommerceEventTypes {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func validPolledProviderID(value, prefix string) bool {
	if len(value) < len(prefix)+1 || len(value) > 255 || !strings.HasPrefix(value, prefix) {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return character != '_' &&
			(character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9')
	}) == -1
}

func developmentEventDigest(event WebhookEvent) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		"boardly-commerce-development-event-v1",
		event.ID,
		event.AccountID,
		event.Type,
		event.ObjectID,
		strconv.FormatBool(event.LiveMode),
		strconv.FormatInt(event.ProviderTime.UTC().Unix(), 10),
	}, "\n")))
}

// ReconcileWebhookReceipts safely resumes receipts whose request was lost,
// whose dependency arrived out of order, or whose worker lease expired.
func (s *DefaultCommerceService) ReconcileWebhookReceipts(
	ctx context.Context,
	limit int32,
) (WebhookReconciliationResult, error) {
	if ctx == nil || limit < 1 || limit > maxWebhookBatch {
		return WebhookReconciliationResult{}, ErrInvalidInput
	}
	terminal, err := s.store.Queries().DeadLetterExhaustedCommerceWebhookReceipts(ctx, limit)
	if err != nil {
		return WebhookReconciliationResult{}, fmt.Errorf(
			"dead-letter exhausted Commerce webhooks: %w",
			err,
		)
	}
	result := WebhookReconciliationResult{DeadLettered: len(terminal)}
	var joined error
	for range limit {
		leaseID := uuid.New()
		receipt, claimErr := s.store.Queries().ClaimNextCommerceWebhookReceipt(
			ctx,
			generated.ClaimNextCommerceWebhookReceiptParams{
				LeaseID:                   leaseID,
				LeaseDurationMilliseconds: webhookReceiptLease.Milliseconds(),
			},
		)
		if errors.Is(claimErr, pgx.ErrNoRows) {
			break
		}
		if claimErr != nil {
			joined = errors.Join(joined, fmt.Errorf("claim Commerce webhook: %w", claimErr))
			break
		}
		environment := Environment(receipt.Environment)
		configuration, exists := s.environments[environment]
		if !exists || configuration.Webhook == nil || receipt.ProviderObjectID.String == "" {
			outcome, finalizeErr := s.finalizeWebhookFailure(
				ctx,
				receipt,
				leaseID,
				"invalid_provider_state",
				true,
			)
			if finalizeErr != nil && !errors.Is(finalizeErr, ErrWebhookBusy) {
				joined = errors.Join(joined, finalizeErr)
			}
			if outcome == webhookReceiptDeadLettered {
				result.DeadLettered++
			}
			continue
		}
		event := WebhookEvent{
			ID: receipt.ProviderEventID, AccountID: receipt.ProviderAccountID,
			Type: receipt.EventType, ObjectID: receipt.ProviderObjectID.String,
			LiveMode: receipt.Livemode, ProviderTime: receipt.ProviderCreatedAt.Time,
		}
		outcome, processErr := s.processClaimedWebhookReceipt(
			ctx,
			configuration.Webhook,
			receipt,
			event,
			leaseID,
		)
		if processErr != nil {
			if !errors.Is(processErr, ErrWebhookBusy) {
				joined = errors.Join(joined, processErr)
			}
			continue
		}
		if outcome == webhookReceiptDeadLettered {
			result.DeadLettered++
		} else {
			result.Processed++
		}
	}
	return result, joined
}

func (s *DefaultCommerceService) constructWebhookEvent(
	payload []byte,
	signature string,
) (Environment, WebhookProvider, WebhookEvent, error) {
	for _, environment := range []Environment{EnvironmentSandbox, EnvironmentLive} {
		provider := s.environments[environment].Webhook
		if provider == nil {
			continue
		}
		event, err := provider.ConstructWebhookEvent(payload, signature)
		if err != nil {
			continue
		}
		if event.LiveMode != (environment == EnvironmentLive) {
			return "", nil, WebhookEvent{}, ErrInvalidWebhook
		}
		return environment, provider, event, nil
	}
	return "", nil, WebhookEvent{}, ErrInvalidWebhook
}

func (s *DefaultCommerceService) processWebhookReceipt(
	ctx context.Context,
	provider WebhookProvider,
	receipt generated.CommerceWebhookReceipt,
	event WebhookEvent,
) (webhookReceiptOutcome, error) {
	leaseID := uuid.New()
	claimed, err := s.store.Queries().ClaimCommerceWebhookReceipt(
		ctx,
		generated.ClaimCommerceWebhookReceiptParams{
			LeaseID: leaseID, LeaseDurationMilliseconds: webhookReceiptLease.Milliseconds(),
			ID: receipt.ID, CompanyID: receipt.CompanyID, Environment: receipt.Environment,
			PayloadSha256: receipt.PayloadSha256,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		current, getErr := s.store.Queries().GetCommerceWebhookReceipt(
			ctx,
			generated.GetCommerceWebhookReceiptParams{
				Environment: receipt.Environment, ProviderEventID: receipt.ProviderEventID,
			},
		)
		if getErr != nil {
			return 0, fmt.Errorf("reload Commerce webhook receipt after claim race: %w", getErr)
		}
		if current.State == "succeeded" {
			return webhookReceiptProcessed, nil
		}
		if current.State == "dead_letter" {
			return webhookReceiptDeadLettered, nil
		}
		return 0, ErrWebhookBusy
	}
	if err != nil {
		return 0, fmt.Errorf("claim Commerce webhook receipt: %w", err)
	}
	return s.processClaimedWebhookReceipt(ctx, provider, claimed, event, leaseID)
}

func (s *DefaultCommerceService) processClaimedWebhookReceipt(
	ctx context.Context,
	provider WebhookProvider,
	claimed generated.CommerceWebhookReceipt,
	event WebhookEvent,
	leaseID uuid.UUID,
) (webhookReceiptOutcome, error) {
	_, err := s.store.Queries().ClaimCommerceWebhookAccountLease(
		ctx,
		generated.ClaimCommerceWebhookAccountLeaseParams{
			CompanyID: claimed.CompanyID, Environment: claimed.Environment,
			ProviderAccountID: claimed.ProviderAccountID, LeaseID: leaseID,
			LeaseDurationMilliseconds: webhookReceiptLease.Milliseconds(),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		outcome, finalizeErr := s.finalizeWebhookFailure(
			ctx,
			claimed,
			leaseID,
			"account_busy",
			false,
		)
		if finalizeErr != nil {
			return 0, finalizeErr
		}
		if outcome == webhookReceiptDeadLettered {
			return outcome, nil
		}
		return 0, ErrWebhookBusy
	}
	if err != nil {
		return 0, fmt.Errorf("claim Commerce webhook account lease: %w", err)
	}

	providerCtx, cancel := context.WithTimeout(ctx, s.providerCallTimeout)
	reconciled, providerErr := provider.ReconcileWebhookEvent(providerCtx, event)
	cancel()
	if providerErr != nil {
		if releaseErr := s.releaseWebhookAccountLease(ctx, claimed, leaseID); releaseErr != nil {
			return 0, releaseErr
		}
		permanent := errors.Is(providerErr, ErrInvalidProviderState) ||
			errors.Is(providerErr, ErrInvalidStore)
		errorCode := "provider_unavailable"
		if permanent {
			errorCode = webhookErrorCode(providerErr)
		}
		outcome, finalizeErr := s.finalizeWebhookFailure(
			ctx,
			claimed,
			leaseID,
			errorCode,
			permanent,
		)
		if finalizeErr != nil {
			return 0, finalizeErr
		}
		if outcome == webhookReceiptDeadLettered {
			return outcome, nil
		}
		return 0, providerError("reconcile Stripe webhook object", providerErr)
	}

	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		accountLease, accountErr := queries.LockCommerceWebhookAccountLease(
			ctx,
			generated.LockCommerceWebhookAccountLeaseParams{
				CompanyID: claimed.CompanyID, Environment: claimed.Environment,
				ProviderAccountID: claimed.ProviderAccountID, LeaseID: leaseID,
			},
		)
		if errors.Is(accountErr, pgx.ErrNoRows) {
			return errWebhookAccountBusy
		}
		if accountErr != nil {
			return fmt.Errorf("lock Commerce webhook account lease: %w", accountErr)
		}
		account := accountLease.CommerceAccount
		if account.CompanyID != claimed.CompanyID || account.Environment != claimed.Environment ||
			!account.ProviderAccountID.Valid ||
			account.ProviderAccountID.String != claimed.ProviderAccountID {
			return ErrInvalidStore
		}
		if applyErr := applyWebhookProjection(ctx, queries, account, reconciled); applyErr != nil {
			return applyErr
		}
		_, completeErr := queries.CompleteCommerceWebhookReceipt(
			ctx,
			generated.CompleteCommerceWebhookReceiptParams{
				ID: claimed.ID, CompanyID: claimed.CompanyID,
				Environment: claimed.Environment, PayloadSha256: claimed.PayloadSha256,
				LeaseID: leaseID, ExpectedVersion: claimed.Version,
			},
		)
		if errors.Is(completeErr, pgx.ErrNoRows) {
			return ErrWebhookConflict
		}
		if completeErr != nil {
			return fmt.Errorf("complete Commerce webhook receipt: %w", completeErr)
		}
		_, releaseErr := queries.ReleaseCommerceWebhookAccountLease(
			ctx,
			generated.ReleaseCommerceWebhookAccountLeaseParams{
				CompanyID: claimed.CompanyID, Environment: claimed.Environment,
				ProviderAccountID: claimed.ProviderAccountID, LeaseID: leaseID,
			},
		)
		if errors.Is(releaseErr, pgx.ErrNoRows) {
			return ErrWebhookConflict
		}
		if releaseErr != nil {
			return fmt.Errorf("release Commerce webhook account lease: %w", releaseErr)
		}
		return nil
	})
	if err != nil {
		if releaseErr := s.releaseWebhookAccountLease(ctx, claimed, leaseID); releaseErr != nil {
			return 0, errors.Join(err, releaseErr)
		}
		if errors.Is(err, ErrWebhookConflict) {
			return 0, ErrWebhookBusy
		}
		permanent := errors.Is(err, ErrInvalidProviderState) || errors.Is(err, ErrInvalidStore)
		outcome, finalizeErr := s.finalizeWebhookFailure(
			ctx,
			claimed,
			leaseID,
			webhookErrorCode(err),
			permanent,
		)
		if finalizeErr != nil {
			return 0, finalizeErr
		}
		if outcome == webhookReceiptDeadLettered {
			return outcome, nil
		}
		if errors.Is(err, errWebhookAccountBusy) || errors.Is(err, errWebhookDependency) ||
			errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrWebhookBusy
		}
		return 0, fmt.Errorf("apply Commerce webhook projection: %w", err)
	}
	return webhookReceiptProcessed, nil
}

func (s *DefaultCommerceService) releaseWebhookAccountLease(
	ctx context.Context,
	receipt generated.CommerceWebhookReceipt,
	leaseID uuid.UUID,
) error {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, err := s.store.Queries().ReleaseCommerceWebhookAccountLease(
		releaseCtx,
		generated.ReleaseCommerceWebhookAccountLeaseParams{
			CompanyID: receipt.CompanyID, Environment: receipt.Environment,
			ProviderAccountID: receipt.ProviderAccountID, LeaseID: leaseID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("release Commerce webhook account lease: %w", err)
	}
	return nil
}

func (s *DefaultCommerceService) finalizeWebhookFailure(
	ctx context.Context,
	receipt generated.CommerceWebhookReceipt,
	leaseID uuid.UUID,
	errorCode string,
	permanent bool,
) (webhookReceiptOutcome, error) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if permanent || receipt.AttemptCount >= maxWebhookAttempts {
		_, err := s.store.Queries().MarkCommerceWebhookReceiptDeadLetter(
			releaseCtx,
			generated.MarkCommerceWebhookReceiptDeadLetterParams{
				LastErrorCode: errorCode, ID: receipt.ID, CompanyID: receipt.CompanyID,
				Environment: receipt.Environment, LeaseID: leaseID,
				ExpectedVersion: receipt.Version,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrWebhookBusy
		}
		if err != nil {
			return 0, fmt.Errorf("dead-letter Commerce webhook receipt: %w", err)
		}
		return webhookReceiptDeadLettered, nil
	}
	_, err := s.store.Queries().ReleaseCommerceWebhookReceiptForRetry(
		releaseCtx,
		generated.ReleaseCommerceWebhookReceiptForRetryParams{
			LastErrorCode: errorCode, ID: receipt.ID, CompanyID: receipt.CompanyID,
			Environment: receipt.Environment, LeaseID: leaseID,
			ExpectedVersion: receipt.Version,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrWebhookBusy
	}
	if err != nil {
		return 0, fmt.Errorf("reschedule Commerce webhook receipt: %w", err)
	}
	return 0, nil
}

func applyWebhookProjection(
	ctx context.Context,
	queries *generated.Queries,
	account generated.CommerceAccount,
	event WebhookEvent,
) error {
	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded",
		"checkout.session.async_payment_failed", "checkout.session.expired":
		return applyCheckoutProjection(ctx, queries, account, event)
	case "customer.subscription.created", "customer.subscription.updated",
		"customer.subscription.deleted", "invoice.paid", "invoice.payment_failed":
		return applySubscriptionProjection(ctx, queries, account, event)
	case "charge.refunded":
		return applyRefundProjection(ctx, queries, account, event)
	case "charge.dispute.created", "charge.dispute.closed":
		return applyDisputeProjection(ctx, queries, account, event)
	default:
		return nil
	}
}

func applyCheckoutProjection(
	ctx context.Context,
	queries *generated.Queries,
	account generated.CommerceAccount,
	event WebhookEvent,
) error {
	object := event.Object
	if object.CheckoutSessionID == "" || object.CustomerID == "" || object.PriceID == "" ||
		object.Amount < 0 || !currencyPattern.MatchString(object.Currency) {
		return ErrInvalidProviderState
	}
	offer, customer, err := resolveWebhookAuthority(
		ctx,
		queries,
		account,
		object.PriceID,
		object.CustomerID,
	)
	if err != nil {
		return err
	}
	if object.Currency != offer.Currency {
		return ErrInvalidProviderState
	}
	if object.SubscriptionID != "" {
		if object.PaymentIntentID != "" || offer.OfferType != string(OfferTypeSubscription) ||
			object.Amount < offer.Amount {
			return ErrInvalidProviderState
		}
		// Checkout never grants a subscription entitlement. The current
		// Subscription and its latest paid Invoice are projected separately.
		return nil
	}
	if object.PaymentIntentID == "" && event.Type == "checkout.session.expired" &&
		object.Status == "expired" && object.PaymentStatus != "paid" {
		return nil
	}
	if object.PaymentIntentID == "" || offer.OfferType != string(OfferTypeOneTime) ||
		object.Amount < offer.Amount {
		return ErrInvalidProviderState
	}
	status := "processing"
	if object.PaymentStatus == "paid" {
		status = "paid"
	} else if event.Type == "checkout.session.async_payment_failed" ||
		event.Type == "checkout.session.expired" || object.Status == "expired" {
		status = "failed"
	}
	var paidAt *time.Time
	if status == "paid" {
		paid := event.ProviderTime.UTC()
		paidAt = &paid
	}
	payment, err := queries.UpsertCommercePaymentProjection(
		ctx,
		generated.UpsertCommercePaymentProjectionParams{
			CompanyID:                 account.CompanyID,
			SiteID:                    offer.SiteID,
			CustomerID:                customer.ID,
			OfferID:                   offer.ID,
			Environment:               offer.Environment,
			ProviderPaymentIntentID:   nullableText(object.PaymentIntentID),
			ProviderCheckoutSessionID: nullableText(object.CheckoutSessionID),
			Status:                    status,
			Amount:                    object.Amount,
			Currency:                  object.Currency,
			PaidAt:                    optionalTimestamp(paidAt),
			ProviderEventAt:           requiredTimestamp(event.ProviderTime),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidStore
	}
	if err != nil {
		return fmt.Errorf("upsert Commerce payment projection: %w", err)
	}
	entitlementStatus := "processing"
	reason := ""
	switch status {
	case "paid":
		entitlementStatus = "active"
	case "failed":
		entitlementStatus = "denied"
		reason = "payment_failed"
	}
	return upsertWebhookEntitlement(
		ctx,
		queries,
		account.CompanyID,
		offer,
		customer.ID,
		payment.ID,
		uuid.Nil,
		"one_time",
		entitlementStatus,
		paidAt,
		nil,
		reason,
		event.ProviderTime.UTC(),
	)
}

func applySubscriptionProjection(
	ctx context.Context,
	queries *generated.Queries,
	account generated.CommerceAccount,
	event WebhookEvent,
) error {
	object := event.Object
	if object.SubscriptionID == "" || object.CustomerID == "" || object.PriceID == "" ||
		object.PeriodStart == nil || object.PeriodEnd == nil ||
		!object.PeriodEnd.After(*object.PeriodStart) {
		return ErrInvalidProviderState
	}
	offer, customer, err := resolveWebhookAuthority(
		ctx,
		queries,
		account,
		object.PriceID,
		object.CustomerID,
	)
	if err != nil {
		return err
	}
	if offer.OfferType != string(OfferTypeSubscription) {
		return ErrInvalidProviderState
	}
	subscription, err := queries.UpsertCommerceSubscriptionProjection(
		ctx,
		generated.UpsertCommerceSubscriptionProjectionParams{
			CompanyID: account.CompanyID, SiteID: offer.SiteID,
			CustomerID: customer.ID, OfferID: offer.ID, Environment: offer.Environment,
			ProviderSubscriptionID:  object.SubscriptionID,
			ProviderLatestInvoiceID: nullableText(object.InvoiceID), Status: object.Status,
			CancelAtPeriodEnd:  object.CancelAtPeriodEnd,
			CurrentPeriodStart: optionalTimestamp(object.PeriodStart),
			CurrentPeriodEnd:   optionalTimestamp(object.PeriodEnd),
			ProviderEventAt:    requiredTimestamp(event.ProviderTime),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidStore
	}
	if err != nil {
		return fmt.Errorf("upsert Commerce subscription projection: %w", err)
	}
	var paymentID uuid.UUID
	if len(object.InvoicePayments) > 0 &&
		(object.InvoiceStatus != "paid" || object.Currency != offer.Currency) {
		return ErrInvalidProviderState
	}
	seenInvoicePayments := make(map[string]struct{}, len(object.InvoicePayments))
	terminalReason := ""
	for _, invoicePayment := range object.InvoicePayments {
		if !validPolledProviderID(object.InvoiceID, "in_") ||
			!validPolledProviderID(invoicePayment.ID, "inpay_") ||
			!validPolledProviderID(invoicePayment.PaymentIntentID, "pi_") ||
			invoicePayment.Amount < 1 {
			return ErrInvalidProviderState
		}
		if _, duplicate := seenInvoicePayments[invoicePayment.ID]; duplicate {
			return ErrInvalidProviderState
		}
		seenInvoicePayments[invoicePayment.ID] = struct{}{}
		paidAt := invoicePayment.PaidAt
		if paidAt == nil {
			paidAt = object.PaidAt
		}
		payment, paymentErr := queries.UpsertCommerceInvoicePaymentProjection(
			ctx,
			generated.UpsertCommerceInvoicePaymentProjectionParams{
				CompanyID: account.CompanyID, SiteID: offer.SiteID,
				CustomerID: customer.ID, OfferID: offer.ID, Environment: offer.Environment,
				SubscriptionID:           subscription.ID,
				ProviderPaymentIntentID:  invoicePayment.PaymentIntentID,
				ProviderInvoicePaymentID: invoicePayment.ID,
				ProviderInvoiceID:        object.InvoiceID,
				Status:                   "paid",
				Amount:                   invoicePayment.Amount, Currency: object.Currency,
				PaidAt:          optionalTimestamp(paidAt),
				ProviderEventAt: requiredTimestamp(event.ProviderTime),
			},
		)
		if errors.Is(paymentErr, pgx.ErrNoRows) {
			return ErrInvalidStore
		}
		if paymentErr != nil {
			return fmt.Errorf("upsert Commerce invoice payment projection: %w", paymentErr)
		}
		if len(object.InvoicePayments) == 1 {
			paymentID = payment.ID
		}
		switch payment.Status {
		case "disputed":
			terminalReason = "dispute_lost"
		case "refunded":
			if terminalReason == "" {
				terminalReason = "refunded"
			}
		}
	}
	switch subscriptionEntitlementDecision(object.Status, object.InvoiceStatus) {
	case entitlementPreserve:
		return nil
	case entitlementActivate:
		if len(object.InvoicePayments) > 0 {
			state, stateErr := queries.GetCommerceSubscriptionInvoiceRevocationState(
				ctx,
				generated.GetCommerceSubscriptionInvoiceRevocationStateParams{
					ProviderInvoiceID: object.InvoiceID,
					SubscriptionID:    subscription.ID,
					CompanyID:         account.CompanyID,
					SiteID:            offer.SiteID,
					CustomerID:        customer.ID,
					OfferID:           offer.ID,
				},
			)
			if errors.Is(stateErr, pgx.ErrNoRows) {
				return ErrInvalidStore
			}
			if stateErr != nil {
				return fmt.Errorf("read Commerce Invoice revocation state: %w", stateErr)
			}
			if !state.IsLatestInvoice {
				return nil
			}
			if !state.HasRemainingPayments {
				if terminalReason == "" {
					return ErrInvalidStore
				}
				return upsertWebhookEntitlement(
					ctx,
					queries,
					account.CompanyID,
					offer,
					customer.ID,
					paymentID,
					subscription.ID,
					"subscription",
					"denied",
					object.PeriodStart,
					object.PeriodEnd,
					terminalReason,
					event.ProviderTime.UTC(),
				)
			}
		}
		if _, err := queries.DenySupersededCommerceSubscriptionEntitlements(
			ctx,
			generated.DenySupersededCommerceSubscriptionEntitlementsParams{
				ProviderEventAt: requiredTimestamp(event.ProviderTime),
				CompanyID:       account.CompanyID,
				SiteID:          offer.SiteID,
				CustomerID:      customer.ID,
				SubscriptionID:  subscription.ID,
				ActiveOfferID:   offer.ID,
			},
		); err != nil {
			return fmt.Errorf("deny superseded Commerce subscription entitlements: %w", err)
		}
		return upsertWebhookEntitlement(
			ctx,
			queries,
			account.CompanyID,
			offer,
			customer.ID,
			paymentID,
			subscription.ID,
			"subscription",
			"active",
			object.PeriodStart,
			object.PeriodEnd,
			"",
			event.ProviderTime.UTC(),
		)
	case entitlementDeny:
		return upsertWebhookEntitlement(
			ctx,
			queries,
			account.CompanyID,
			offer,
			customer.ID,
			paymentID,
			subscription.ID,
			"subscription",
			"denied",
			object.PeriodStart,
			object.PeriodEnd,
			"subscription_ended",
			event.ProviderTime.UTC(),
		)
	default:
		return ErrInvalidProviderState
	}
}

func applyRefundProjection(
	ctx context.Context,
	queries *generated.Queries,
	account generated.CommerceAccount,
	event WebhookEvent,
) error {
	object := event.Object
	if object.PaymentIntentID == "" || object.CustomerID == "" || object.Amount < 1 ||
		object.AmountRefunded < 0 || object.AmountRefunded > object.Amount ||
		!currencyPattern.MatchString(object.Currency) {
		return ErrInvalidProviderState
	}
	if !refundRevokesEntitlement(object.Amount, object.AmountRefunded) {
		return nil
	}
	return revokePaymentEntitlement(
		ctx,
		queries,
		account,
		object.PaymentIntentID,
		object.CustomerID,
		object.Amount,
		object.Currency,
		"refunded",
		"refunded",
		event.ProviderTime.UTC(),
	)
}

func applyDisputeProjection(
	ctx context.Context,
	queries *generated.Queries,
	account generated.CommerceAccount,
	event WebhookEvent,
) error {
	object := event.Object
	if object.PaymentIntentID == "" || object.Amount < 1 ||
		!currencyPattern.MatchString(object.Currency) {
		return ErrInvalidProviderState
	}
	if !disputeRevokesEntitlement(object.Status) {
		return nil
	}
	return revokePaymentEntitlement(
		ctx,
		queries,
		account,
		object.PaymentIntentID,
		"",
		0,
		object.Currency,
		"disputed",
		"dispute_lost",
		event.ProviderTime.UTC(),
	)
}

func revokePaymentEntitlement(
	ctx context.Context,
	queries *generated.Queries,
	account generated.CommerceAccount,
	paymentIntentID string,
	providerCustomerID string,
	amount int64,
	currency string,
	paymentStatus string,
	reason string,
	providerEventAt time.Time,
) error {
	payments, err := queries.ListCommercePaymentsByProviderIntent(
		ctx,
		generated.ListCommercePaymentsByProviderIntentParams{
			Environment: account.Environment, ProviderPaymentIntentID: paymentIntentID,
		},
	)
	if err != nil {
		return fmt.Errorf("list Commerce payments for revocation: %w", err)
	}
	if len(payments) == 0 {
		return errWebhookDependency
	}

	var providerCustomerIDValue uuid.UUID
	if providerCustomerID != "" {
		customer, customerErr := queries.GetCommerceCustomerByProviderID(
			ctx,
			generated.GetCommerceCustomerByProviderIDParams{
				Environment: account.Environment, ProviderCustomerID: providerCustomerID,
			},
		)
		if errors.Is(customerErr, pgx.ErrNoRows) {
			return errWebhookDependency
		}
		if customerErr != nil {
			return fmt.Errorf("get Commerce payment customer: %w", customerErr)
		}
		if customer.CompanyID != account.CompanyID {
			return ErrInvalidProviderState
		}
		providerCustomerIDValue = customer.ID
	}

	revocations := make([]paymentRevocation, 0, len(payments))
	oneTimePaymentCount := 0
	for _, payment := range payments {
		amountMismatch := amount > 0 && payment.Amount != amount
		if payment.SubscriptionID.Valid {
			// A Stripe InvoicePayment stores only the amount allocated to this
			// Invoice. Its PaymentIntent/Charge can be larger when one payment funds
			// multiple allocations, but it cannot be smaller than this projection.
			amountMismatch = amount > 0 && payment.Amount > amount
		} else {
			oneTimePaymentCount++
		}
		customerMismatch := providerCustomerID != "" && payment.CustomerID != providerCustomerIDValue
		if payment.CompanyID != account.CompanyID ||
			payment.Currency != currency ||
			amountMismatch ||
			customerMismatch {
			return ErrInvalidProviderState
		}
		offer, offerErr := queries.GetCommerceOfferByID(
			ctx,
			generated.GetCommerceOfferByIDParams{
				ID: payment.OfferID, CompanyID: payment.CompanyID, SiteID: payment.SiteID,
			},
		)
		if offerErr != nil {
			return fmt.Errorf("get Commerce payment Offer: %w", offerErr)
		}
		revocations = append(revocations, paymentRevocation{
			offer:   offer,
			payment: payment,
		})
	}
	if oneTimePaymentCount > 0 && len(revocations) != 1 {
		return ErrInvalidProviderState
	}

	subscriptionInvoices := make(map[subscriptionInvoiceKey]paymentRevocation)
	for _, revocation := range revocations {
		payment, updateErr := queries.UpdateCommercePaymentTerminalProjection(
			ctx,
			generated.UpdateCommercePaymentTerminalProjectionParams{
				Status:                  paymentStatus,
				ID:                      revocation.payment.ID,
				CompanyID:               account.CompanyID,
				Environment:             account.Environment,
				ProviderPaymentIntentID: paymentIntentID,
				ProviderEventAt: requiredTimestamp(
					providerEventAt,
				),
				ExpectedVersion: revocation.payment.Version,
			},
		)
		if errors.Is(updateErr, pgx.ErrNoRows) {
			return ErrWebhookBusy
		}
		if updateErr != nil {
			return fmt.Errorf("update Commerce payment revocation: %w", updateErr)
		}
		if !payment.SubscriptionID.Valid {
			return upsertWebhookEntitlement(
				ctx,
				queries,
				payment.CompanyID,
				revocation.offer,
				payment.CustomerID,
				payment.ID,
				uuid.Nil,
				"one_time",
				"denied",
				manifestUTCTimePointer(payment.PaidAt),
				nil,
				reason,
				providerEventAt,
			)
		}
		key := subscriptionInvoiceKey{
			invoiceID:      payment.ProviderInvoiceID.String,
			subscriptionID: payment.SubscriptionID.UUID,
		}
		if previous, exists := subscriptionInvoices[key]; exists &&
			(previous.payment.CompanyID != payment.CompanyID ||
				previous.payment.SiteID != payment.SiteID ||
				previous.payment.CustomerID != payment.CustomerID ||
				previous.payment.OfferID != payment.OfferID) {
			return ErrInvalidProviderState
		}
		subscriptionInvoices[key] = paymentRevocation{
			offer:   revocation.offer,
			payment: payment,
		}
	}
	for _, revocation := range subscriptionInvoices {
		if err := denySubscriptionEntitlementForInvoice(
			ctx,
			queries,
			revocation.payment,
			revocation.offer,
			reason,
			providerEventAt,
		); err != nil {
			return err
		}
	}
	return nil
}

type paymentRevocation struct {
	offer   generated.CommerceOffer
	payment generated.CommercePayment
}

type subscriptionInvoiceKey struct {
	invoiceID      string
	subscriptionID uuid.UUID
}

func denySubscriptionEntitlementForInvoice(
	ctx context.Context,
	queries *generated.Queries,
	payment generated.CommercePayment,
	offer generated.CommerceOffer,
	reason string,
	providerEventAt time.Time,
) error {
	subscriptionID := payment.SubscriptionID.UUID
	state, err := queries.GetCommerceSubscriptionInvoiceRevocationState(
		ctx,
		generated.GetCommerceSubscriptionInvoiceRevocationStateParams{
			ProviderInvoiceID: payment.ProviderInvoiceID.String,
			SubscriptionID:    subscriptionID,
			CompanyID:         payment.CompanyID,
			SiteID:            payment.SiteID,
			CustomerID:        payment.CustomerID,
			OfferID:           payment.OfferID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidStore
	}
	if err != nil {
		return fmt.Errorf("read Commerce Invoice revocation state: %w", err)
	}
	if !state.IsLatestInvoice || state.HasRemainingPayments {
		return nil
	}
	entitlement, err := queries.GetCommerceEntitlementGrant(
		ctx,
		generated.GetCommerceEntitlementGrantParams{
			CompanyID: payment.CompanyID, SiteID: payment.SiteID,
			CustomerID: payment.CustomerID, SourceType: "subscription",
			GrantID: subscriptionID, EntitlementKey: offer.EntitlementKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return errWebhookDependency
	}
	if err != nil {
		return fmt.Errorf("get Commerce subscription entitlement: %w", err)
	}
	entitlementPaymentID := uuid.Nil
	if state.PaymentCount == 1 {
		entitlementPaymentID = payment.ID
	}
	return upsertWebhookEntitlement(
		ctx,
		queries,
		payment.CompanyID,
		offer,
		payment.CustomerID,
		entitlementPaymentID,
		subscriptionID,
		"subscription",
		"denied",
		manifestUTCTimePointer(entitlement.StartsAt),
		manifestUTCTimePointer(entitlement.ExpiresAt),
		reason,
		providerEventAt,
	)
}

func resolveWebhookAuthority(
	ctx context.Context,
	queries *generated.Queries,
	account generated.CommerceAccount,
	priceID string,
	customerID string,
) (generated.CommerceOffer, generated.CommerceCustomer, error) {
	offer, err := queries.GetCommerceOfferByProviderPrice(
		ctx,
		generated.GetCommerceOfferByProviderPriceParams{
			Environment: account.Environment, ProviderPriceID: priceID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceOffer{}, generated.CommerceCustomer{}, errWebhookDependency
	}
	if err != nil {
		return generated.CommerceOffer{}, generated.CommerceCustomer{}, fmt.Errorf(
			"resolve Commerce webhook Offer: %w",
			err,
		)
	}
	customer, err := queries.GetCommerceCustomerByProviderID(
		ctx,
		generated.GetCommerceCustomerByProviderIDParams{
			Environment: account.Environment, ProviderCustomerID: customerID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceOffer{}, generated.CommerceCustomer{}, errWebhookDependency
	}
	if err != nil {
		return generated.CommerceOffer{}, generated.CommerceCustomer{}, fmt.Errorf(
			"resolve Commerce webhook customer: %w",
			err,
		)
	}
	if offer.CompanyID != account.CompanyID || customer.CompanyID != account.CompanyID ||
		offer.SiteID != customer.SiteID || offer.Environment != account.Environment ||
		customer.Environment != account.Environment ||
		(offer.Status != "active" && offer.Status != "disabled") {
		return generated.CommerceOffer{}, generated.CommerceCustomer{}, ErrInvalidProviderState
	}
	site, err := queries.GetCommerceSiteForUpdate(
		ctx,
		generated.GetCommerceSiteForUpdateParams{
			CompanyID: account.CompanyID, SiteID: offer.SiteID,
		},
	)
	if err != nil {
		return generated.CommerceOffer{}, generated.CommerceCustomer{}, fmt.Errorf(
			"resolve Commerce webhook Site: %w",
			err,
		)
	}
	if site.CommerceAccountID != account.ID || site.Environment != account.Environment {
		return generated.CommerceOffer{}, generated.CommerceCustomer{}, ErrInvalidProviderState
	}
	return offer, customer, nil
}

func upsertWebhookEntitlement(
	ctx context.Context,
	queries *generated.Queries,
	companyID uuid.UUID,
	offer generated.CommerceOffer,
	customerID uuid.UUID,
	paymentID uuid.UUID,
	subscriptionID uuid.UUID,
	sourceType string,
	status string,
	startsAt *time.Time,
	expiresAt *time.Time,
	revokedReason string,
	providerEventAt time.Time,
) error {
	grantID, err := entitlementGrantID(paymentID, subscriptionID, sourceType)
	if err != nil {
		return err
	}
	_, err = queries.UpsertCommerceEntitlementProjection(
		ctx,
		generated.UpsertCommerceEntitlementProjectionParams{
			CompanyID: companyID, SiteID: offer.SiteID,
			CustomerID: customerID, OfferID: offer.ID,
			PaymentID: nullableUUID(paymentID), SubscriptionID: nullableUUID(subscriptionID),
			GrantID:        grantID,
			EntitlementKey: offer.EntitlementKey, SourceType: sourceType, Status: status,
			StartsAt: optionalTimestamp(startsAt), ExpiresAt: optionalTimestamp(expiresAt),
			RevokedReason: nullableText(revokedReason), ProviderEventAt: requiredTimestamp(providerEventAt),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrInvalidStore
	}
	if err != nil {
		return fmt.Errorf("upsert Commerce entitlement projection: %w", err)
	}
	return nil
}

func entitlementGrantID(
	paymentID uuid.UUID,
	subscriptionID uuid.UUID,
	sourceType string,
) (uuid.UUID, error) {
	switch sourceType {
	case "one_time":
		if paymentID == uuid.Nil || subscriptionID != uuid.Nil {
			return uuid.Nil, ErrInvalidStore
		}
		return paymentID, nil
	case "subscription":
		if subscriptionID == uuid.Nil {
			return uuid.Nil, ErrInvalidStore
		}
		return subscriptionID, nil
	default:
		return uuid.Nil, ErrInvalidStore
	}
}

func sameWebhookReceipt(
	receipt generated.CommerceWebhookReceipt,
	companyID uuid.UUID,
	environment Environment,
	event WebhookEvent,
) bool {
	// Stripe Event IDs are immutable. A local Events-API poll and a later
	// signed webhook can serialize the same Event differently, so dedupe on
	// its authoritative envelope. Projection never trusts either snapshot: it
	// re-reads the current object through ReconcileWebhookEvent.
	return receipt.CompanyID == companyID && receipt.Environment == string(environment) &&
		receipt.ProviderEventID == event.ID && receipt.ProviderAccountID == event.AccountID &&
		receipt.EventType == event.Type && receipt.ProviderObjectID == nullableText(event.ObjectID) &&
		receipt.Livemode == event.LiveMode &&
		databaseTimesEqual(receipt.ProviderCreatedAt, event.ProviderTime) &&
		len(receipt.PayloadSha256) == sha256.Size
}

func subscriptionEntitlementDecision(status, invoiceStatus string) entitlementDecision {
	switch status {
	case "canceled", "incomplete_expired", "unpaid", "paused":
		return entitlementDeny
	case "active":
		if invoiceStatus == "paid" {
			return entitlementActivate
		}
		return entitlementPreserve
	default:
		return entitlementPreserve
	}
}

func refundRevokesEntitlement(amount, amountRefunded int64) bool {
	return amount > 0 && amountRefunded >= amount
}

func disputeRevokesEntitlement(status string) bool {
	return status == "lost"
}

func webhookErrorCode(err error) string {
	switch {
	case errors.Is(err, errWebhookAccountBusy):
		return "account_busy"
	case errors.Is(err, errWebhookDependency):
		return "dependency_not_ready"
	case errors.Is(err, ErrInvalidProviderState), errors.Is(err, ErrInvalidStore):
		return "invalid_provider_state"
	case errors.Is(err, ErrWebhookConflict):
		return "receipt_conflict"
	default:
		return "projection_failed"
	}
}
