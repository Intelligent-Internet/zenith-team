package commerce

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type developmentEventAccountListerStub struct {
	accounts []generated.CommerceAccount
	pages    map[uuid.UUID][]generated.CommerceAccount
	err      error
	inputs   []generated.ListSandboxCommerceAccountsForEventPollingParams
}

func (stub *developmentEventAccountListerStub) ListSandboxCommerceAccountsForEventPolling(
	_ context.Context,
	input generated.ListSandboxCommerceAccountsForEventPollingParams,
) ([]generated.CommerceAccount, error) {
	stub.inputs = append(stub.inputs, input)
	if stub.pages != nil {
		return stub.pages[input.AfterID], stub.err
	}
	return stub.accounts, stub.err
}

type developmentEventProviderStub struct {
	results         map[string]DevelopmentEventListResult
	resultsByCursor map[string]DevelopmentEventListResult
	errors          map[string]error
	inputs          []DevelopmentEventListInput
}

func (stub *developmentEventProviderStub) ListEvents(
	_ context.Context,
	input DevelopmentEventListInput,
) (DevelopmentEventListResult, error) {
	stub.inputs = append(stub.inputs, input)
	if stub.resultsByCursor != nil {
		return stub.resultsByCursor[input.StartingAfter], stub.errors[input.AccountID]
	}
	return stub.results[input.AccountID], stub.errors[input.AccountID]
}

type developmentEventIngestorStub struct {
	events []WebhookEvent
	err    error
}

func (stub *developmentEventIngestorStub) ingestPolledSandboxEvent(
	_ context.Context,
	event WebhookEvent,
) (WebhookReceiptResult, error) {
	stub.events = append(stub.events, event)
	return WebhookReceiptResult{Processed: stub.err == nil}, stub.err
}

func TestDevelopmentEventPollerUsesRecentBoundedPagesAndIngestsChronologically(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	accounts := &developmentEventAccountListerStub{accounts: []generated.CommerceAccount{
		{
			ID: uuid.New(), Environment: string(EnvironmentSandbox),
			ProviderAccountID: pgtype.Text{String: "acct_BoardlyConnected123", Valid: true},
		},
	}}
	provider := &developmentEventProviderStub{results: map[string]DevelopmentEventListResult{
		"acct_BoardlyConnected123": {
			HasMore:    true,
			NextCursor: "evt_BoardlyOlder123",
			Events: []WebhookEvent{
				{ID: "evt_BoardlyNewer123", AccountID: "acct_BoardlyConnected123"},
				{ID: "evt_BoardlyOlder123", AccountID: "acct_BoardlyConnected123"},
			},
		},
	}}
	ingestor := &developmentEventIngestorStub{}
	issues := make([]DevelopmentEventPollingIssue, 0, 1)
	poller := &DevelopmentEventPoller{
		accounts: accounts, provider: provider, ingestor: ingestor,
		options: DevelopmentEventPollerOptions{
			PollInterval: time.Second, Lookback: 10 * time.Minute,
			AccountBatchSize: 25, EventBatchSize: 75, Now: func() time.Time { return now },
			OnIssue: func(_ context.Context, issue DevelopmentEventPollingIssue) {
				issues = append(issues, issue)
			},
		},
	}
	result, err := poller.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll() error = %v", err)
	}
	if len(accounts.inputs) != 1 || accounts.inputs[0].AfterID != uuid.Nil ||
		accounts.inputs[0].BatchSize != 25 || result.Accounts != 1 || result.Events != 2 ||
		result.Truncated != 1 {
		t.Fatalf("Poll() = %+v, account inputs=%+v", result, accounts.inputs)
	}
	if len(provider.inputs) != 1 || provider.inputs[0].Limit != 75 ||
		provider.inputs[0].StartingAfter != "" ||
		!provider.inputs[0].CreatedAtOrAfter.Equal(now.Add(-10*time.Minute)) ||
		len(provider.inputs[0].Types) != len(sandboxCommerceEventTypes) {
		t.Fatalf("ListEvents input = %+v", provider.inputs)
	}
	if len(ingestor.events) != 2 || ingestor.events[0].ID != "evt_BoardlyOlder123" ||
		ingestor.events[1].ID != "evt_BoardlyNewer123" {
		t.Fatalf("ingested event order = %+v", ingestor.events)
	}
	if len(issues) != 1 || issues[0] != DevelopmentEventPollingIssuePageTruncated {
		t.Fatalf("issues = %v", issues)
	}
}

func TestDevelopmentEventPollerAdvancesEventCursorAcrossBoundedPasses(t *testing.T) {
	t.Parallel()
	accountID := uuid.New()
	accounts := &developmentEventAccountListerStub{accounts: []generated.CommerceAccount{{
		ID: accountID,
		ProviderAccountID: pgtype.Text{
			String: "acct_BoardlyConnected123",
			Valid:  true,
		},
	}}}
	provider := &developmentEventProviderStub{resultsByCursor: map[string]DevelopmentEventListResult{
		"": {
			Events:     []WebhookEvent{{ID: "evt_BoardlyNewest123"}},
			HasMore:    true,
			NextCursor: "evt_BoardlyNewest123",
		},
		"evt_BoardlyNewest123": {
			Events:     []WebhookEvent{{ID: "evt_BoardlyMiddle123"}},
			HasMore:    true,
			NextCursor: "evt_BoardlyMiddle123",
		},
		"evt_BoardlyMiddle123": {
			Events: []WebhookEvent{{ID: "evt_BoardlyOldest123"}},
		},
	}}
	poller := &DevelopmentEventPoller{
		accounts: accounts,
		provider: provider,
		ingestor: &developmentEventIngestorStub{},
		options: DevelopmentEventPollerOptions{
			PollInterval: time.Second, Lookback: time.Minute,
			AccountBatchSize: 10, EventBatchSize: 1, Now: time.Now,
		},
	}
	for range 4 {
		if _, err := poller.Poll(context.Background()); err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
	}
	if len(provider.inputs) != 4 || provider.inputs[0].StartingAfter != "" ||
		provider.inputs[1].StartingAfter != "evt_BoardlyNewest123" ||
		provider.inputs[2].StartingAfter != "evt_BoardlyMiddle123" ||
		provider.inputs[3].StartingAfter != "" {
		t.Fatalf("event cursor inputs = %+v", provider.inputs)
	}
}

func TestDevelopmentEventPollerRotatesBoundedAccountBatches(t *testing.T) {
	t.Parallel()
	firstID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca62101")
	secondID := uuid.MustParse("01991d2f-cd80-7901-9f96-b6b88ca62102")
	accounts := &developmentEventAccountListerStub{pages: map[uuid.UUID][]generated.CommerceAccount{
		uuid.Nil: {
			{
				ID: firstID,
				ProviderAccountID: pgtype.Text{
					String: "acct_BoardlyFirst123",
					Valid:  true,
				},
			},
		},
		firstID: {
			{
				ID: secondID,
				ProviderAccountID: pgtype.Text{
					String: "acct_BoardlySecond123",
					Valid:  true,
				},
			},
		},
		secondID: nil,
	}}
	poller := &DevelopmentEventPoller{
		accounts: accounts,
		provider: &developmentEventProviderStub{
			results: map[string]DevelopmentEventListResult{},
		},
		ingestor: &developmentEventIngestorStub{},
		options: DevelopmentEventPollerOptions{
			PollInterval:     time.Second,
			Lookback:         time.Minute,
			AccountBatchSize: 1,
			EventBatchSize:   10,
			Now:              time.Now,
		},
	}
	for range 4 {
		if _, err := poller.Poll(context.Background()); err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
	}
	if len(accounts.inputs) != 4 || accounts.inputs[0].AfterID != uuid.Nil ||
		accounts.inputs[1].AfterID != firstID || accounts.inputs[2].AfterID != secondID ||
		accounts.inputs[3].AfterID != uuid.Nil {
		t.Fatalf("account cursor inputs = %+v", accounts.inputs)
	}
}

func TestDevelopmentEventPollerIsBestEffortAcrossAccounts(t *testing.T) {
	t.Parallel()
	accounts := &developmentEventAccountListerStub{accounts: []generated.CommerceAccount{
		{ProviderAccountID: pgtype.Text{String: "acct_BoardlyFirst123", Valid: true}},
		{ProviderAccountID: pgtype.Text{String: "acct_BoardlySecond123", Valid: true}},
	}}
	provider := &developmentEventProviderStub{
		results: map[string]DevelopmentEventListResult{
			"acct_BoardlySecond123": {Events: []WebhookEvent{{ID: "evt_BoardlySecond123"}}},
		},
		errors: map[string]error{"acct_BoardlyFirst123": errors.New("Stripe unavailable")},
	}
	ingestor := &developmentEventIngestorStub{}
	issues := make([]DevelopmentEventPollingIssue, 0, 1)
	poller := &DevelopmentEventPoller{
		accounts: accounts, provider: provider, ingestor: ingestor,
		options: DevelopmentEventPollerOptions{
			PollInterval: time.Second, Lookback: time.Minute,
			AccountBatchSize: 10, EventBatchSize: 10, Now: time.Now,
			OnIssue: func(_ context.Context, issue DevelopmentEventPollingIssue) {
				issues = append(issues, issue)
			},
		},
	}
	result, err := poller.Poll(context.Background())
	if err == nil || result.Accounts != 1 || result.Events != 1 || len(ingestor.events) != 1 {
		t.Fatalf("Poll() = %+v, %v; events=%+v", result, err, ingestor.events)
	}
	if len(issues) != 1 || issues[0] != DevelopmentEventPollingIssueProviderFailed {
		t.Fatalf("issues = %v", issues)
	}
}

func TestDevelopmentEventPollerBacksOffGloballyWhenProviderPermissionIsMissing(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	accounts := &developmentEventAccountListerStub{accounts: []generated.CommerceAccount{
		{
			ID: uuid.New(),
			ProviderAccountID: pgtype.Text{
				String: "acct_BoardlyFirst123",
				Valid:  true,
			},
		},
		{
			ID: uuid.New(),
			ProviderAccountID: pgtype.Text{
				String: "acct_BoardlySecond123",
				Valid:  true,
			},
		},
	}}
	provider := &developmentEventProviderStub{
		results: map[string]DevelopmentEventListResult{},
		errors: map[string]error{
			"acct_BoardlyFirst123":  ErrDevelopmentEventProviderPermission,
			"acct_BoardlySecond123": ErrDevelopmentEventProviderPermission,
		},
	}
	issues := make([]DevelopmentEventPollingIssue, 0, 2)
	poller := &DevelopmentEventPoller{
		accounts: accounts,
		provider: provider,
		ingestor: &developmentEventIngestorStub{},
		options: DevelopmentEventPollerOptions{
			PollInterval:     time.Second,
			Lookback:         time.Minute,
			AccountBatchSize: 10,
			EventBatchSize:   10,
			Now:              func() time.Time { return now },
			OnIssue: func(_ context.Context, issue DevelopmentEventPollingIssue) {
				issues = append(issues, issue)
			},
		},
	}
	if result, err := poller.Poll(context.Background()); err != nil || result.Accounts != 0 {
		t.Fatalf("first Poll() = %+v, %v", result, err)
	}
	if len(accounts.inputs) != 1 || len(provider.inputs) != 1 || len(issues) != 1 ||
		issues[0] != DevelopmentEventPollingIssueProviderPermissionMissing {
		t.Fatalf(
			"first permission failure calls = accounts %d, provider %d, issues %v",
			len(accounts.inputs), len(provider.inputs), issues,
		)
	}
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("Poll(during backoff) error = %v", err)
	}
	if len(accounts.inputs) != 1 || len(provider.inputs) != 1 || len(issues) != 1 {
		t.Fatalf(
			"backoff calls = accounts %d, provider %d, issues %v",
			len(accounts.inputs), len(provider.inputs), issues,
		)
	}
	now = now.Add(time.Minute)
	if _, err := poller.Poll(context.Background()); err != nil {
		t.Fatalf("Poll(after backoff) error = %v", err)
	}
	if len(accounts.inputs) != 2 || len(provider.inputs) != 2 || len(issues) != 2 {
		t.Fatalf(
			"retry calls = accounts %d, provider %d, issues %v",
			len(accounts.inputs), len(provider.inputs), issues,
		)
	}
}

func TestDevelopmentEventPollerTreatsBusyDurableReceiptAsExpected(t *testing.T) {
	t.Parallel()
	accounts := &developmentEventAccountListerStub{accounts: []generated.CommerceAccount{
		{ProviderAccountID: pgtype.Text{String: "acct_BoardlyConnected123", Valid: true}},
	}}
	provider := &developmentEventProviderStub{results: map[string]DevelopmentEventListResult{
		"acct_BoardlyConnected123": {Events: []WebhookEvent{{ID: "evt_BoardlyPending123"}}},
	}}
	ingestor := &developmentEventIngestorStub{err: ErrWebhookBusy}
	issues := make([]DevelopmentEventPollingIssue, 0, 1)
	poller := &DevelopmentEventPoller{
		accounts: accounts,
		provider: provider,
		ingestor: ingestor,
		options: DevelopmentEventPollerOptions{
			PollInterval:     time.Second,
			Lookback:         time.Minute,
			AccountBatchSize: 10,
			EventBatchSize:   10,
			Now:              time.Now,
			OnIssue: func(_ context.Context, issue DevelopmentEventPollingIssue) {
				issues = append(issues, issue)
			},
		},
	}
	result, err := poller.Poll(context.Background())
	if err != nil || result.Accounts != 1 || result.Events != 0 || len(issues) != 0 {
		t.Fatalf("Poll() = %+v, %v; issues=%v", result, err, issues)
	}
}

func TestDevelopmentEventPollerRetainsCursorUntilWholePageIsDurable(t *testing.T) {
	t.Parallel()
	accountID := uuid.New()
	accounts := &developmentEventAccountListerStub{accounts: []generated.CommerceAccount{{
		ID: accountID,
		ProviderAccountID: pgtype.Text{
			String: "acct_BoardlyConnected123",
			Valid:  true,
		},
	}}}
	provider := &developmentEventProviderStub{resultsByCursor: map[string]DevelopmentEventListResult{
		"evt_BoardlyCurrentCursor123": {
			Events:     []WebhookEvent{{ID: "evt_BoardlyRetry123"}},
			HasMore:    true,
			NextCursor: "evt_BoardlyNextCursor123",
		},
	}}
	poller := &DevelopmentEventPoller{
		accounts: accounts,
		provider: provider,
		ingestor: &developmentEventIngestorStub{err: ErrWebhookBusy},
		options: DevelopmentEventPollerOptions{
			PollInterval: time.Second, Lookback: time.Minute,
			AccountBatchSize: 10, EventBatchSize: 10, Now: time.Now,
		},
		eventCursors: map[uuid.UUID]string{
			accountID: "evt_BoardlyCurrentCursor123",
		},
	}
	for range 2 {
		if _, err := poller.Poll(context.Background()); err != nil {
			t.Fatalf("Poll() error = %v", err)
		}
	}
	if len(provider.inputs) != 2 ||
		provider.inputs[0].StartingAfter != "evt_BoardlyCurrentCursor123" ||
		provider.inputs[1].StartingAfter != "evt_BoardlyCurrentCursor123" {
		t.Fatalf("event cursor advanced before durable ingestion: %+v", provider.inputs)
	}
}

func TestDevelopmentEventPollerOptionsAreStrictAndBounded(t *testing.T) {
	t.Parallel()
	options := DevelopmentEventPollerOptions{}
	if !normalizeDevelopmentEventPollerOptions(&options) || options.PollInterval == 0 ||
		options.Lookback == 0 || options.AccountBatchSize == 0 || options.EventBatchSize == 0 ||
		options.Now == nil {
		t.Fatalf("normalized options = %+v", options)
	}
	for _, options := range []DevelopmentEventPollerOptions{
		{PollInterval: time.Millisecond},
		{Lookback: 31 * 24 * time.Hour},
		{AccountBatchSize: 101},
		{EventBatchSize: 101},
	} {
		if normalizeDevelopmentEventPollerOptions(&options) {
			t.Fatalf("accepted invalid options %+v", options)
		}
	}
}
