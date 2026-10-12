package commerce

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	defaultDevelopmentEventPollInterval = 2 * time.Second
	minimumDevelopmentEventPollInterval = 250 * time.Millisecond
	maximumDevelopmentEventPollInterval = time.Minute
	defaultDevelopmentEventLookback     = 24 * time.Hour
	minimumDevelopmentEventLookback     = time.Minute
	maximumDevelopmentEventLookback     = 24 * time.Hour
	defaultDevelopmentAccountBatch      = int32(25)
	defaultDevelopmentEventBatch        = int64(100)
	developmentEventPermissionRetry     = time.Minute
)

var sandboxCommerceEventTypes = [...]string{
	"checkout.session.completed",
	"checkout.session.async_payment_succeeded",
	"checkout.session.async_payment_failed",
	"checkout.session.expired",
	"customer.subscription.created",
	"customer.subscription.updated",
	"customer.subscription.deleted",
	"invoice.paid",
	"invoice.payment_failed",
	"charge.refunded",
	"charge.dispute.created",
	"charge.dispute.closed",
}

// ErrDevelopmentEventPollerConfiguration reports invalid local fallback wiring.
var ErrDevelopmentEventPollerConfiguration = errors.New(
	"invalid Commerce development Event poller configuration",
)

// ErrDevelopmentEventProviderPermission reports a platform-key permission
// failure shared by every connected sandbox account.
var ErrDevelopmentEventProviderPermission = errors.New(
	"development event provider permission is unavailable",
)

// DevelopmentEventListInput bounds one local sandbox Event-list request.
type DevelopmentEventListInput struct {
	AccountID        string
	CreatedAtOrAfter time.Time
	StartingAfter    string
	Limit            int64
	Types            []string
}

// DevelopmentEventListResult is one bounded provider page.
type DevelopmentEventListResult struct {
	Events     []WebhookEvent
	HasMore    bool
	NextCursor string
}

// DevelopmentEventProvider is the test-mode Event-list boundary used only by
// the local development fallback. Live Commerce remains webhook-only.
type DevelopmentEventProvider interface {
	ListEvents(context.Context, DevelopmentEventListInput) (DevelopmentEventListResult, error)
}

// DevelopmentEventPollingIssue is a low-cardinality local fallback failure.
type DevelopmentEventPollingIssue string

const (
	// DevelopmentEventPollingIssueAccountScanFailed reports a database scan failure.
	DevelopmentEventPollingIssueAccountScanFailed DevelopmentEventPollingIssue = "account_scan_failed"
	// DevelopmentEventPollingIssueProviderFailed reports a Stripe Events API failure.
	DevelopmentEventPollingIssueProviderFailed DevelopmentEventPollingIssue = "provider_failed"
	// DevelopmentEventPollingIssueProviderPermissionMissing reports missing Events Read permission.
	DevelopmentEventPollingIssueProviderPermissionMissing DevelopmentEventPollingIssue = "provider_permission_missing"
	// DevelopmentEventPollingIssueIngestFailed reports a durable receipt ingestion failure.
	DevelopmentEventPollingIssueIngestFailed DevelopmentEventPollingIssue = "ingest_failed"
	// DevelopmentEventPollingIssuePageTruncated reports more than one bounded provider page.
	DevelopmentEventPollingIssuePageTruncated DevelopmentEventPollingIssue = "page_truncated"
)

// DevelopmentEventPollerOptions controls the bounded local fallback loop.
type DevelopmentEventPollerOptions struct {
	PollInterval     time.Duration
	Lookback         time.Duration
	AccountBatchSize int32
	EventBatchSize   int64
	Now              func() time.Time
	OnIssue          func(context.Context, DevelopmentEventPollingIssue)
}

// DevelopmentEventPollingResult summarizes one bounded local polling pass.
type DevelopmentEventPollingResult struct {
	Accounts   int
	Events     int
	Duplicates int
	Truncated  int
}

type developmentEventAccountLister interface {
	ListSandboxCommerceAccountsForEventPolling(
		context.Context,
		generated.ListSandboxCommerceAccountsForEventPollingParams,
	) ([]generated.CommerceAccount, error)
}

type developmentEventIngestor interface {
	ingestPolledSandboxEvent(context.Context, WebhookEvent) (WebhookReceiptResult, error)
}

// DevelopmentEventPoller automatically reconciles local sandbox payments from
// Stripe's Events API. It reads one bounded recent test-mode page per connected
// account, then uses the same durable receipt and provider-authoritative
// projection path as signed webhooks.
type DevelopmentEventPoller struct {
	pollMu            sync.Mutex
	accounts          developmentEventAccountLister
	provider          DevelopmentEventProvider
	ingestor          developmentEventIngestor
	options           DevelopmentEventPollerOptions
	afterAccountID    uuid.UUID
	eventCursors      map[uuid.UUID]string
	permissionRetryAt time.Time
}

// NewDevelopmentEventPoller constructs the local sandbox reconciliation fallback.
func NewDevelopmentEventPoller(
	queries *generated.Queries,
	provider DevelopmentEventProvider,
	service *DefaultCommerceService,
	options DevelopmentEventPollerOptions,
) (*DevelopmentEventPoller, error) {
	if queries == nil || provider == nil || service == nil ||
		!normalizeDevelopmentEventPollerOptions(&options) {
		return nil, ErrDevelopmentEventPollerConfiguration
	}
	return &DevelopmentEventPoller{
		accounts:     queries,
		provider:     provider,
		ingestor:     service,
		options:      options,
		eventCursors: make(map[uuid.UUID]string),
	}, nil
}

// Run polls until ctx ends. Each pass is synchronous, so ticks never overlap.
func (poller *DevelopmentEventPoller) Run(ctx context.Context) error {
	if !poller.isInitialized() || ctx == nil {
		return ErrDevelopmentEventPollerConfiguration
	}
	ticker := time.NewTicker(poller.options.PollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := poller.Poll(ctx)
		if errors.Is(err, ErrDevelopmentEventPollerConfiguration) {
			return err
		}
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Poll runs one bounded pass across connected local sandbox accounts.
func (poller *DevelopmentEventPoller) Poll(
	ctx context.Context,
) (DevelopmentEventPollingResult, error) {
	if !poller.isInitialized() || ctx == nil {
		return DevelopmentEventPollingResult{}, ErrDevelopmentEventPollerConfiguration
	}
	poller.pollMu.Lock()
	defer poller.pollMu.Unlock()
	if !normalizeDevelopmentEventPollerOptions(&poller.options) {
		return DevelopmentEventPollingResult{}, ErrDevelopmentEventPollerConfiguration
	}
	now := poller.options.Now().UTC()
	if !poller.permissionRetryAt.IsZero() && now.Before(poller.permissionRetryAt) {
		return DevelopmentEventPollingResult{}, nil
	}
	accounts, err := poller.accounts.ListSandboxCommerceAccountsForEventPolling(
		ctx,
		generated.ListSandboxCommerceAccountsForEventPollingParams{
			AfterID:   poller.afterAccountID,
			BatchSize: poller.options.AccountBatchSize,
		},
	)
	if err != nil {
		poller.observeIssue(ctx, DevelopmentEventPollingIssueAccountScanFailed)
		return DevelopmentEventPollingResult{}, fmt.Errorf(
			"list Commerce development polling accounts: %w",
			err,
		)
	}
	if len(accounts) == 0 {
		poller.afterAccountID = uuid.Nil
		return DevelopmentEventPollingResult{}, nil
	}
	cutoff := now.Add(-poller.options.Lookback)
	result := DevelopmentEventPollingResult{}
	var joined error
	if poller.eventCursors == nil {
		poller.eventCursors = make(map[uuid.UUID]string)
	}
	for _, account := range accounts {
		startingAfter := poller.eventCursors[account.ID]
		page, listErr := poller.provider.ListEvents(ctx, DevelopmentEventListInput{
			AccountID:        account.ProviderAccountID.String,
			CreatedAtOrAfter: cutoff,
			StartingAfter:    startingAfter,
			Limit:            poller.options.EventBatchSize,
			Types:            append([]string(nil), sandboxCommerceEventTypes[:]...),
		})
		if listErr != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return result, contextErr
			}
			if errors.Is(listErr, ErrDevelopmentEventProviderPermission) {
				poller.permissionRetryAt = now.Add(developmentEventPermissionRetry)
				poller.observeIssue(
					ctx,
					DevelopmentEventPollingIssueProviderPermissionMissing,
				)
				return result, nil
			}
			poller.observeIssue(ctx, DevelopmentEventPollingIssueProviderFailed)
			joined = errors.Join(joined, fmt.Errorf("list Stripe development Events: %w", listErr))
			poller.afterAccountID = account.ID
			continue
		}
		poller.permissionRetryAt = time.Time{}
		result.Accounts++
		if page.HasMore {
			if page.NextCursor == "" || page.NextCursor == startingAfter || len(page.Events) == 0 {
				poller.observeIssue(ctx, DevelopmentEventPollingIssueProviderFailed)
				joined = errors.Join(joined, errors.New(
					"Stripe development Event page did not advance",
				))
				poller.afterAccountID = account.ID
				continue
			}
			result.Truncated++
			poller.observeIssue(ctx, DevelopmentEventPollingIssuePageTruncated)
		}
		// Stripe lists newest first. Apply a bounded page oldest first so
		// normal local runs preserve provider chronology.
		pageDurable := true
		for index := len(page.Events) - 1; index >= 0; index-- {
			receipt, ingestErr := poller.ingestor.ingestPolledSandboxEvent(
				ctx,
				page.Events[index],
			)
			if ingestErr != nil {
				if contextErr := ctx.Err(); contextErr != nil {
					return result, contextErr
				}
				if errors.Is(ingestErr, ErrWebhookBusy) {
					pageDurable = false
					continue
				}
				pageDurable = false
				poller.observeIssue(ctx, DevelopmentEventPollingIssueIngestFailed)
				joined = errors.Join(joined, fmt.Errorf(
					"ingest Stripe development Event: %w",
					ingestErr,
				))
				continue
			}
			result.Events++
			if receipt.Duplicate {
				result.Duplicates++
			}
		}
		if pageDurable {
			if page.HasMore {
				poller.eventCursors[account.ID] = page.NextCursor
			} else {
				delete(poller.eventCursors, account.ID)
			}
		}
		poller.afterAccountID = account.ID
	}
	if len(accounts) < int(poller.options.AccountBatchSize) {
		poller.afterAccountID = uuid.Nil
	}
	return result, joined
}

func (poller *DevelopmentEventPoller) isInitialized() bool {
	return poller != nil && poller.accounts != nil && poller.provider != nil &&
		poller.ingestor != nil
}

func normalizeDevelopmentEventPollerOptions(options *DevelopmentEventPollerOptions) bool {
	if options == nil {
		return false
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaultDevelopmentEventPollInterval
	}
	if options.Lookback == 0 {
		options.Lookback = defaultDevelopmentEventLookback
	}
	if options.AccountBatchSize == 0 {
		options.AccountBatchSize = defaultDevelopmentAccountBatch
	}
	if options.EventBatchSize == 0 {
		options.EventBatchSize = defaultDevelopmentEventBatch
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	isPollIntervalValid := options.PollInterval >= minimumDevelopmentEventPollInterval &&
		options.PollInterval <= maximumDevelopmentEventPollInterval &&
		options.PollInterval%time.Millisecond == 0
	isLookbackValid := options.Lookback >= minimumDevelopmentEventLookback &&
		options.Lookback <= maximumDevelopmentEventLookback &&
		options.Lookback%time.Second == 0
	isAccountBatchValid := options.AccountBatchSize >= 1 && options.AccountBatchSize <= 100
	isEventBatchValid := options.EventBatchSize >= 1 && options.EventBatchSize <= 100
	return isPollIntervalValid && isLookbackValid && isAccountBatchValid && isEventBatchValid
}

func (poller *DevelopmentEventPoller) observeIssue(
	ctx context.Context,
	issue DevelopmentEventPollingIssue,
) {
	if poller.options.OnIssue == nil {
		return
	}
	func() {
		defer func() { _ = recover() }()
		poller.options.OnIssue(ctx, issue)
	}()
}
