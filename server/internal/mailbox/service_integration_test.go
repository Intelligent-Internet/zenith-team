//go:build integration

package mailbox_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/infra/email"
	"boardly.dev/boardly/server/internal/mailbox"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

const mailboxDomain = "mail.example.com"

func TestMailboxCreationUsesOwnerAndDatabaseUniquenessAuthority(t *testing.T) {
	test := newMailboxTest(t)

	state, err := test.service.GetState(test.ctx, test.ownerA)
	if err != nil || state.Status != mailbox.StatusUnconfigured || !state.CanCreate {
		t.Fatalf("unconfigured owner state = %#v, %v", state, err)
	}

	start := make(chan struct{})
	type result struct {
		state mailbox.State
		err   error
	}
	results := make(chan result, 2)
	for _, principal := range []auth.UserPrincipal{test.ownerA, test.ownerB} {
		go func() {
			<-start
			created, createErr := test.service.Create(test.ctx, principal, "Shared.Name")
			results <- result{state: created, err: createErr}
		}()
	}
	close(start)

	succeeded := 0
	conflicted := 0
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			succeeded++
			if result.state.Mailbox == nil || result.state.Mailbox.Address != "shared.name@"+mailboxDomain {
				t.Fatalf("created state = %#v", result.state)
			}
		case errors.Is(result.err, mailbox.ErrAddressUnavailable):
			conflicted++
		default:
			t.Fatalf("concurrent Create() error = %v", result.err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("creation results = %d success, %d conflict", succeeded, conflicted)
	}

	winningPrincipal := test.ownerA
	winningState, err := test.service.GetState(test.ctx, winningPrincipal)
	if err != nil {
		t.Fatalf("GetState(owner A) error = %v", err)
	}
	if winningState.Status != mailbox.StatusActive {
		winningPrincipal = test.ownerB
	}
	if _, err := test.service.Create(
		test.ctx,
		winningPrincipal,
		"another-name",
	); !errors.Is(
		err,
		mailbox.ErrAlreadyConfigured,
	) {
		t.Fatalf("second Create() error = %v, want already configured", err)
	}
}

func TestMailboxCreateFencesConcurrentMembershipRevocationAtCompany(t *testing.T) {
	test := newMailboxTest(t)

	holder, err := test.pool.Begin(test.ctx)
	if err != nil {
		t.Fatalf("begin Company lock holder: %v", err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(
		test.ctx,
		`SELECT id FROM companies WHERE id = $1 FOR UPDATE`,
		test.fixture.CompanyA,
	); err != nil {
		t.Fatalf("hold Company lock: %v", err)
	}

	createResult := make(chan error, 1)
	go func() {
		_, createErr := test.service.Create(test.ctx, test.ownerA, "account-race")
		createResult <- createErr
	}()
	waitForBlockedMailboxQuery(t, test.ctx, test.pool, "LockCompanyForMembershipMutation")

	if _, err := holder.Exec(test.ctx, `
		UPDATE company_members
		SET status = 'revoked', revoked_at = clock_timestamp(),
			updated_at = clock_timestamp(), version = version + 1
		WHERE company_id = $1 AND user_id = $2 AND status = 'active'`,
		test.fixture.CompanyA,
		test.fixture.UserA,
	); err != nil {
		t.Fatalf("revoke mailbox creator membership: %v", err)
	}
	if err := holder.Commit(test.ctx); err != nil {
		t.Fatalf("commit membership revocation: %v", err)
	}

	select {
	case err := <-createResult:
		if !errors.Is(err, mailbox.ErrForbidden) {
			t.Fatalf("Create() error = %v, want ErrForbidden after revocation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Create() did not resume after Company lock release")
	}
	var mailboxCount int
	if err := test.pool.QueryRow(test.ctx, `
		SELECT count(*) FROM company_mailboxes WHERE company_id = $1`,
		test.fixture.CompanyA,
	).Scan(&mailboxCount); err != nil {
		t.Fatalf("count post-revocation mailboxes: %v", err)
	}
	if mailboxCount != 0 {
		t.Fatalf("post-revocation mailbox count = %d, want 0", mailboxCount)
	}
}

func TestMailboxWebhookFansOutAndDeduplicatesWithoutProviderFetch(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "sales")
	mustCreateMailbox(t, test, test.ownerB, "support")

	event := receivedEvent(
		"event-1",
		"provider-email-1",
		[]string{
			"Sales <sales@mail.example.com>",
			"support@mail.example.com",
			"unknown@mail.example.com",
			"sales@mail.example.com",
		},
		test.now,
	)
	result, err := test.service.Ingest(test.ctx, event)
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if result.Duplicate || result.Delivered != 2 {
		t.Fatalf("Ingest() = %#v", result)
	}
	if test.provider.emailCalls() != 0 {
		t.Fatalf("webhook ingress made %d provider calls", test.provider.emailCalls())
	}

	for _, principal := range []auth.UserPrincipal{test.ownerA, test.ownerB} {
		page, listErr := test.service.List(test.ctx, principal, mailbox.ListOptions{})
		if listErr != nil {
			t.Fatalf("List() error = %v", listErr)
		}
		if len(page.Messages) != 1 || page.Messages[0].Subject != "Product question" {
			t.Fatalf("messages = %#v", page.Messages)
		}
	}

	for _, replay := range []email.ReceivedEvent{
		event,
		receivedEvent("event-2", "provider-email-1", event.To, test.now),
	} {
		result, err := test.service.Ingest(test.ctx, replay)
		if err != nil {
			t.Fatalf("Ingest(replay) error = %v", err)
		}
		if !result.Duplicate || result.Delivered != 0 {
			t.Fatalf("Ingest(replay) = %#v", result)
		}
	}
}

func TestMailboxUnreadCountIsCompanyScopedAndShared(t *testing.T) {
	test := newMailboxTest(t)
	dbtest.MustExec(
		t,
		test.ctx,
		test.pool,
		`UPDATE company_members SET role='owner' WHERE company_id=$1 AND user_id=$2`,
		test.fixture.CompanyA,
		test.fixture.UserB,
	)
	secondOwner := test.memberA
	secondOwner.Role = auth.CompanyRoleOwner
	mustCreateMailbox(t, test, test.ownerA, "sales")
	mustCreateMailbox(t, test, test.ownerB, "support")

	for _, event := range []email.ReceivedEvent{
		receivedEvent(
			"event-unread-shared",
			"provider-email-unread-shared",
			[]string{"sales@mail.example.com", "support@mail.example.com"},
			test.now,
		),
		receivedEvent(
			"event-unread-sales",
			"provider-email-unread-sales",
			[]string{"sales@mail.example.com"},
			test.now.Add(time.Second),
		),
	} {
		if _, err := test.service.Ingest(test.ctx, event); err != nil {
			t.Fatalf("Ingest() error = %v", err)
		}
	}

	assertMailboxUnreadCount(t, test, test.ownerA, 2)
	assertMailboxUnreadCount(t, test, secondOwner, 2)
	assertMailboxUnreadCount(t, test, test.ownerB, 1)

	page, err := test.service.List(test.ctx, secondOwner, mailbox.ListOptions{})
	if err != nil || len(page.Messages) != 2 {
		t.Fatalf("List(owner A) = %#v, %v", page, err)
	}
	if _, err := test.service.MarkRead(test.ctx, secondOwner, page.Messages[0].ID); err != nil {
		t.Fatalf("MarkRead(owner A) error = %v", err)
	}

	assertMailboxUnreadCount(t, test, test.ownerA, 1)
	assertMailboxUnreadCount(t, test, secondOwner, 1)
	assertMailboxUnreadCount(t, test, test.ownerB, 1)

	dbtest.MustExec(t, test.ctx, test.pool, `
		UPDATE mailbox_messages
		SET expires_at = clock_timestamp() - interval '1 second'
		WHERE id = $1`, page.Messages[1].ID)
	assertMailboxUnreadCount(t, test, test.ownerA, 0)
	assertMailboxUnreadCount(t, test, secondOwner, 0)
	assertMailboxUnreadCount(t, test, test.ownerB, 1)
}

func TestMailboxPublishesCompanyChangesAfterCommittedMutations(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "sales")
	configured := test.mailboxEvents.take(t, 1)[0]
	assertMailboxChange(
		t,
		configured,
		test.ownerA.CompanyID,
		mailbox.ChangeReasonConfigured,
		uuid.Nil,
	)

	event := receivedEvent(
		"event-realtime",
		"provider-email-realtime",
		[]string{"sales@mail.example.com"},
		test.now,
	)
	if _, err := test.service.Ingest(test.ctx, event); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	page, err := test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("List() = %#v, %v", page, err)
	}
	messageID := page.Messages[0].ID
	received := test.mailboxEvents.take(t, 1)[0]
	assertMailboxChange(
		t,
		received,
		test.ownerA.CompanyID,
		mailbox.ChangeReasonMessageReceived,
		messageID,
	)

	if _, err := test.service.MarkRead(test.ctx, test.ownerA, messageID); err != nil {
		t.Fatalf("MarkRead() error = %v", err)
	}
	read := test.mailboxEvents.take(t, 1)[0]
	assertMailboxChange(
		t,
		read,
		test.ownerA.CompanyID,
		mailbox.ChangeReasonMessageRead,
		messageID,
	)

	test.provider.messageErrors[event.EmailID] = email.ErrReceivedEmailNotFound
	if _, err := test.service.GetDetail(
		test.ctx,
		test.ownerA,
		messageID,
	); !errors.Is(err, mailbox.ErrMessageExpired) {
		t.Fatalf("GetDetail() error = %v, want expired", err)
	}
	removed := test.mailboxEvents.take(t, 1)[0]
	assertMailboxChange(
		t,
		removed,
		test.ownerA.CompanyID,
		mailbox.ChangeReasonMessageRemoved,
		messageID,
	)
}

func TestMailboxDetailIsTenantScopedSanitizedCachedAndSingleflight(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "sales")
	mustCreateMailbox(t, test, test.ownerB, "support")
	event := receivedEvent(
		"event-detail",
		"provider-email-detail",
		[]string{"sales@mail.example.com"},
		test.now,
	)
	if _, err := test.service.Ingest(test.ctx, event); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	page, err := test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("List() = %#v, %v", page, err)
	}
	messageID := page.Messages[0].ID

	if _, err := test.service.GetDetail(
		test.ctx,
		test.ownerB,
		messageID,
	); !errors.Is(
		err,
		mailbox.ErrMessageNotFound,
	) {
		t.Fatalf("cross-tenant GetDetail() error = %v", err)
	}
	if test.provider.emailCalls() != 0 {
		t.Fatal("cross-tenant lookup reached provider")
	}

	release := make(chan struct{})
	test.provider.blockEmail = release
	test.provider.messages["provider-email-detail"] = email.ReceivedEmail{
		ID:         "provider-email-detail",
		From:       event.From,
		To:         event.To,
		Subject:    event.Subject,
		Text:       "Hello",
		HTML:       `<p onclick="steal()">Hello <strong>team</strong></p><script>steal()</script><img src="https://tracker.test/pixel">`,
		ReceivedAt: test.now,
	}

	const readers = 8
	results := make(chan mailbox.Detail, readers)
	errorsChannel := make(chan error, readers)
	for range readers {
		go func() {
			detail, detailErr := test.service.GetDetail(test.ctx, test.ownerA, messageID)
			results <- detail
			errorsChannel <- detailErr
		}()
	}
	waitForProviderCalls(t, test.provider, 1)
	close(release)
	for range readers {
		if detailErr := <-errorsChannel; detailErr != nil {
			t.Fatalf("GetDetail() error = %v", detailErr)
		}
		detail := <-results
		if detail.HTML != "<p>Hello <strong>team</strong></p>" || detail.Text != "Hello" {
			t.Fatalf("sanitized detail = %#v", detail)
		}
	}
	if test.provider.emailCalls() != 1 {
		t.Fatalf("singleflight provider calls = %d, want 1", test.provider.emailCalls())
	}
	if _, err := test.service.GetDetail(test.ctx, test.ownerA, messageID); err != nil {
		t.Fatalf("cached GetDetail() error = %v", err)
	}
	if test.provider.emailCalls() != 1 {
		t.Fatalf("cached provider calls = %d, want 1", test.provider.emailCalls())
	}

	read, err := test.service.MarkRead(test.ctx, test.ownerA, messageID)
	if err != nil || read.ReadAt == nil {
		t.Fatalf("MarkRead() = %#v, %v", read, err)
	}
	readAgain, err := test.service.MarkRead(test.ctx, test.ownerA, messageID)
	if err != nil || readAgain.Revision != read.Revision {
		t.Fatalf("idempotent MarkRead() = %#v, %v; first %#v", readAgain, err, read)
	}
}

func TestMailboxConcurrentProviderExpiryDeletesEveryFanoutIndex(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "sales")
	mustCreateMailbox(t, test, test.ownerB, "support")
	event := receivedEvent(
		"event-expired-fanout",
		"provider-email-expired-fanout",
		[]string{"sales@mail.example.com", "support@mail.example.com"},
		test.now,
	)
	if _, err := test.service.Ingest(test.ctx, event); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	pageA, err := test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
	if err != nil || len(pageA.Messages) != 1 {
		t.Fatalf("List(owner A) = %#v, %v", pageA, err)
	}
	pageB, err := test.service.List(test.ctx, test.ownerB, mailbox.ListOptions{})
	if err != nil || len(pageB.Messages) != 1 {
		t.Fatalf("List(owner B) = %#v, %v", pageB, err)
	}

	release := make(chan struct{})
	test.provider.blockEmail = release
	test.provider.messageErrors[event.EmailID] = email.ErrReceivedEmailNotFound
	start := make(chan struct{})
	ready := make(chan struct{}, 2)
	errorsChannel := make(chan error, 2)
	for _, request := range []struct {
		principal auth.UserPrincipal
		messageID uuid.UUID
	}{
		{principal: test.ownerA, messageID: pageA.Messages[0].ID},
		{principal: test.ownerB, messageID: pageB.Messages[0].ID},
	} {
		go func() {
			ready <- struct{}{}
			<-start
			_, detailErr := test.service.GetDetail(
				test.ctx,
				request.principal,
				request.messageID,
			)
			errorsChannel <- detailErr
		}()
	}
	for range 2 {
		<-ready
	}
	close(start)
	waitForProviderCalls(t, test.provider, 1)
	close(release)
	expiredResults := 0
	for range 2 {
		detailErr := <-errorsChannel
		if errors.Is(detailErr, mailbox.ErrMessageExpired) {
			expiredResults++
		} else if !errors.Is(detailErr, mailbox.ErrMessageNotFound) {
			t.Fatalf("GetDetail() error = %v, want expired or concurrently removed", detailErr)
		}
	}
	if expiredResults == 0 {
		t.Fatal("concurrent provider caller did not receive the expired result")
	}
	for _, principal := range []auth.UserPrincipal{test.ownerA, test.ownerB} {
		page, listErr := test.service.List(test.ctx, principal, mailbox.ListOptions{})
		if listErr != nil || len(page.Messages) != 0 {
			t.Fatalf("List(after provider expiry) = %#v, %v", page, listErr)
		}
	}
}

func TestMailboxProviderExpiryDeletesFanoutBeforeLateReader(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "sales")
	mustCreateMailbox(t, test, test.ownerB, "support")
	event := receivedEvent(
		"event-expired-late-reader",
		"provider-email-expired-late-reader",
		[]string{"sales@mail.example.com", "support@mail.example.com"},
		test.now,
	)
	if _, err := test.service.Ingest(test.ctx, event); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	pageA, err := test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
	if err != nil || len(pageA.Messages) != 1 {
		t.Fatalf("List(owner A) = %#v, %v", pageA, err)
	}
	pageB, err := test.service.List(test.ctx, test.ownerB, mailbox.ListOptions{})
	if err != nil || len(pageB.Messages) != 1 {
		t.Fatalf("List(owner B) = %#v, %v", pageB, err)
	}
	test.provider.messageErrors[event.EmailID] = email.ErrReceivedEmailNotFound

	if _, err := test.service.GetDetail(
		test.ctx,
		test.ownerA,
		pageA.Messages[0].ID,
	); !errors.Is(err, mailbox.ErrMessageExpired) {
		t.Fatalf("GetDetail(owner A) error = %v, want expired", err)
	}
	if test.provider.emailCalls() != 1 {
		t.Fatalf("provider calls after first expiry = %d, want 1", test.provider.emailCalls())
	}
	remainingB, err := test.service.List(test.ctx, test.ownerB, mailbox.ListOptions{})
	if err != nil || len(remainingB.Messages) != 0 {
		t.Fatalf("List(owner B after shared expiry) = %#v, %v", remainingB, err)
	}
	if _, err := test.service.GetDetail(
		test.ctx,
		test.ownerB,
		pageB.Messages[0].ID,
	); !errors.Is(err, mailbox.ErrMessageNotFound) {
		t.Fatalf("late GetDetail(owner B) error = %v, want not found", err)
	}
	if test.provider.emailCalls() != 1 {
		t.Fatalf("provider calls after late reader = %d, want 1", test.provider.emailCalls())
	}
}

func TestMailboxProviderFailuresMapAndExpireIndex(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "expired", err: email.ErrReceivedEmailNotFound, want: mailbox.ErrMessageExpired},
		{
			name: "rate limited",
			err:  &email.RateLimitError{RetryAfter: 7 * time.Second},
			want: mailbox.ErrProviderRateLimited,
		},
		{name: "unavailable", err: email.ErrUnavailable, want: mailbox.ErrProviderUnavailable},
	}
	for _, scenario := range tests {
		t.Run(scenario.name, func(t *testing.T) {
			test := newMailboxTest(t)
			mustCreateMailbox(t, test, test.ownerA, "sales")
			event := receivedEvent("event-"+scenario.name, "email-"+scenario.name,
				[]string{"sales@mail.example.com"}, test.now)
			if _, err := test.service.Ingest(test.ctx, event); err != nil {
				t.Fatalf("Ingest() error = %v", err)
			}
			page, err := test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
			if err != nil || len(page.Messages) != 1 {
				t.Fatalf("List() = %#v, %v", page, err)
			}
			test.provider.messageErrors[event.EmailID] = scenario.err
			_, err = test.service.GetDetail(test.ctx, test.ownerA, page.Messages[0].ID)
			if !errors.Is(err, scenario.want) {
				t.Fatalf("GetDetail() error = %v, want %v", err, scenario.want)
			}
			if errors.Is(scenario.want, mailbox.ErrProviderRateLimited) {
				var rateLimitErr *mailbox.ProviderRateLimitError
				if !errors.As(err, &rateLimitErr) || rateLimitErr.RetryAfter != 7*time.Second {
					t.Fatalf("rate limit = %#v", rateLimitErr)
				}
			}
			if errors.Is(scenario.want, mailbox.ErrMessageExpired) {
				page, err = test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
				if err != nil || len(page.Messages) != 0 {
					t.Fatalf("expired List() = %#v, %v", page, err)
				}
			}
		})
	}
}

func TestMailboxAttachmentAuthorizationAndRetentionPurge(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "sales")
	mustCreateMailbox(t, test, test.ownerB, "support")
	event := receivedEvent("event-attachment", "email-attachment",
		[]string{"sales@mail.example.com"}, test.now)
	event.Attachments = []email.ReceivedAttachment{{
		ID: "provider-attachment", Filename: "brief.pdf",
		ContentType: "application/pdf", SizeBytes: int64Pointer(42), Disposition: "attachment",
	}}
	if _, err := test.service.Ingest(test.ctx, event); err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	page, err := test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("List() = %#v, %v", page, err)
	}
	detail := mustDetail(t, test, page.Messages[0].ID)
	if len(detail.Attachments) != 1 {
		t.Fatalf("attachments = %#v", detail.Attachments)
	}
	attachmentID := detail.Attachments[0].ID
	test.provider.downloads["email-attachment/provider-attachment"] = email.AttachmentDownload{
		URL: "https://downloads.example.test/signed", ExpiresAt: test.now.Add(time.Hour),
	}
	if _, err := test.service.GetAttachment(
		test.ctx,
		test.ownerB,
		page.Messages[0].ID,
		attachmentID,
	); !errors.Is(
		err,
		mailbox.ErrMessageNotFound,
	) {
		t.Fatalf("cross-tenant GetAttachment() error = %v", err)
	}
	download, err := test.service.GetAttachment(test.ctx, test.ownerA, page.Messages[0].ID, attachmentID)
	if err != nil || download.URL != "https://downloads.example.test/signed" {
		t.Fatalf("GetAttachment() = %#v, %v", download, err)
	}

	test.now = test.now.Add(31 * 24 * time.Hour)
	test.clock.set(test.now)
	sweep, err := test.service.PurgeExpired(test.ctx, 100)
	if err != nil {
		t.Fatalf("PurgeExpired() error = %v", err)
	}
	if sweep.Messages != 1 || sweep.Attachments != 1 || sweep.Receipts != 1 {
		t.Fatalf("sweep = %#v", sweep)
	}
	page, err = test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("post-purge List() = %#v, %v", page, err)
	}
	state, err := test.service.GetState(test.ctx, test.ownerA)
	if err != nil || state.Status != mailbox.StatusActive {
		t.Fatalf("post-purge state = %#v, %v", state, err)
	}
}

type mailboxTest struct {
	ctx           context.Context
	pool          *pgxpool.Pool
	store         *db.Store
	fixture       dbtest.Fixture
	bus           *events.Bus
	service       *mailbox.DefaultMailboxService
	provider      *fakeReceivedProvider
	ownerA        auth.UserPrincipal
	ownerB        auth.UserPrincipal
	memberA       auth.UserPrincipal
	now           time.Time
	clock         *testClock
	mailboxEvents *mailboxEventRecorder
}

func newMailboxTest(t *testing.T) *mailboxTest {
	t.Helper()
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("db.MigrateUp() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `
		INSERT INTO company_members (company_id, user_id, role)
		VALUES ($1, $2, 'member')`, fixture.CompanyA, fixture.UserB)
	// Project access alone must not grant Mailbox access.
	dbtest.GrantProject(t, ctx, pool, fixture.CompanyA, fixture.ProjectA, fixture.UserB)
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("db.NewStore() error = %v", err)
	}
	// The service clock is the test's to move, but retention reads are
	// database-clocked (expires_at > clock_timestamp()), so a fixed calendar
	// date silently expires every seeded Message once the wall clock passes
	// it plus the retention window. Anchor the fixture to now instead.
	now := time.Now().UTC().Truncate(time.Second)
	clock := &testClock{now: now}
	provider := newFakeReceivedProvider()
	cache := newMemoryDetailCache()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	recorder := &mailboxEventRecorder{}
	if err := bus.Subscribe(mailbox.Topic, recorder.record); err != nil {
		t.Fatalf("subscribe Mailbox events: %v", err)
	}
	service, err := mailbox.NewMailboxService(mailbox.ServiceOptions{
		Store: store, Provider: provider, Cache: cache, ExpiryCache: cache, Bus: bus,
		Domain: mailboxDomain, Now: clock.read,
	})
	if err != nil {
		t.Fatalf("mailbox.NewMailboxService() error = %v", err)
	}
	return &mailboxTest{
		ctx: ctx, pool: pool, store: store, fixture: fixture, bus: bus,
		service: service, provider: provider,
		ownerA:  companyPrincipal(fixture.UserA, fixture.CompanyA, auth.CompanyRoleOwner),
		ownerB:  companyPrincipal(fixture.UserB, fixture.CompanyB, auth.CompanyRoleOwner),
		memberA: companyPrincipal(fixture.UserB, fixture.CompanyA, auth.CompanyRoleMember),
		now:     now, clock: clock, mailboxEvents: recorder,
	}
}

func companyPrincipal(userID, companyID uuid.UUID, role auth.CompanyRole) auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindSession,
		CompanyID: companyID, Role: role,
	}
}

func receivedEvent(eventID, emailID string, to []string, receivedAt time.Time) email.ReceivedEvent {
	return email.ReceivedEvent{
		EventID: eventID, EmailID: emailID, From: "Customer <customer@example.com>",
		To: to, Subject: "Product question", ReceivedAt: receivedAt,
	}
}

func int64Pointer(value int64) *int64 {
	return &value
}

func mustCreateMailbox(t *testing.T, test *mailboxTest, principal auth.UserPrincipal, localPart string) {
	t.Helper()
	if _, err := test.service.Create(test.ctx, principal, localPart); err != nil {
		t.Fatalf("Create(%q) error = %v", localPart, err)
	}
}

func assertMailboxUnreadCount(
	t *testing.T,
	test *mailboxTest,
	principal auth.UserPrincipal,
	want int64,
) {
	t.Helper()
	state, err := test.service.GetState(test.ctx, principal)
	if err != nil {
		t.Fatalf("GetState() error = %v", err)
	}
	if state.UnreadCount != want {
		t.Fatalf("GetState().UnreadCount = %d, want %d", state.UnreadCount, want)
	}
}

func mustDetail(t *testing.T, test *mailboxTest, messageID uuid.UUID) mailbox.Detail {
	t.Helper()
	test.provider.messages["email-attachment"] = email.ReceivedEmail{
		ID: "email-attachment", From: "customer@example.com",
		To: []string{"sales@mail.example.com"}, Subject: "Product question",
		HTML: "<p>Attached</p>", Text: "Attached", ReceivedAt: test.now,
	}
	detail, err := test.service.GetDetail(test.ctx, test.ownerA, messageID)
	if err != nil {
		t.Fatalf("GetDetail() error = %v", err)
	}
	return detail
}

func waitForProviderCalls(t *testing.T, provider *fakeReceivedProvider, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if provider.emailCalls() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("provider calls = %d, want at least %d", provider.emailCalls(), want)
}

func waitForBlockedMailboxQuery(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	queryName string,
) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_stat_activity
				WHERE datname = current_database()
				  AND pid <> pg_backend_pid()
				  AND wait_event_type = 'Lock'
				  AND query LIKE '%' || $1 || '%'
			)`, queryName).Scan(&waiting); err != nil {
			t.Fatalf("observe %s lock wait: %v", queryName, err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for %s lock: %v", queryName, ctx.Err())
		case <-timeout.C:
			t.Fatalf("%s never waited for its lock", queryName)
		case <-ticker.C:
		}
	}
}

type fakeReceivedProvider struct {
	mu             sync.Mutex
	messages       map[string]email.ReceivedEmail
	messageErrors  map[string]error
	downloads      map[string]email.AttachmentDownload
	downloadErrors map[string]error
	blockEmail     <-chan struct{}
	calls          int
}

func newFakeReceivedProvider() *fakeReceivedProvider {
	return &fakeReceivedProvider{
		messages: map[string]email.ReceivedEmail{}, messageErrors: map[string]error{},
		downloads: map[string]email.AttachmentDownload{}, downloadErrors: map[string]error{},
	}
}

func (p *fakeReceivedProvider) RetrieveReceivedEmail(_ context.Context, emailID string) (email.ReceivedEmail, error) {
	p.mu.Lock()
	p.calls++
	message, err := p.messages[emailID], p.messageErrors[emailID]
	block := p.blockEmail
	p.mu.Unlock()
	if block != nil {
		<-block
	}
	return message, err
}

func (p *fakeReceivedProvider) RetrieveReceivedAttachment(
	_ context.Context,
	emailID string,
	attachmentID string,
) (email.AttachmentDownload, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := emailID + "/" + attachmentID
	return p.downloads[key], p.downloadErrors[key]
}

func (p *fakeReceivedProvider) emailCalls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

type memoryDetailCache struct {
	mu          sync.Mutex
	values      map[string]mailbox.DetailContent
	expirations map[string]time.Time
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

func newMemoryDetailCache() *memoryDetailCache {
	return &memoryDetailCache{
		values: map[string]mailbox.DetailContent{}, expirations: map[string]time.Time{},
	}
}

func (c *memoryDetailCache) Get(_ context.Context, key string) (mailbox.DetailContent, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	value, found := c.values[key]
	return value, found, nil
}

func (c *memoryDetailCache) Set(
	_ context.Context,
	key string,
	value mailbox.DetailContent,
	_ time.Duration,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = value
	return nil
}

func (c *memoryDetailCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.values, key)
	return nil
}

func (c *memoryDetailCache) IsExpired(_ context.Context, key string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	expiresAt, found := c.expirations[key]
	return found && expiresAt.After(time.Now()), nil
}

func (c *memoryDetailCache) MarkExpired(
	_ context.Context,
	key string,
	ttl time.Duration,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expirations[key] = time.Now().Add(ttl)
	return nil
}

type mailboxEventRecorder struct {
	mu     sync.Mutex
	events []events.Event
}

func (r *mailboxEventRecorder) record(_ context.Context, event events.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
	return nil
}

func (r *mailboxEventRecorder) take(t *testing.T, want int) []events.Event {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != want {
		t.Fatalf("recorded Mailbox events = %d, want %d", len(r.events), want)
	}
	result := append([]events.Event(nil), r.events...)
	r.events = nil
	return result
}

func assertMailboxChange(
	t *testing.T,
	event events.Event,
	companyID uuid.UUID,
	reason mailbox.ChangeReason,
	messageID uuid.UUID,
) {
	t.Helper()
	if event.Name != mailbox.EventChanged || event.EntityType != mailbox.Topic ||
		event.CompanyID != companyID {
		t.Fatalf("Mailbox event envelope = %#v", event)
	}
	payload, err := events.PayloadOf[mailbox.ChangedPayload](event)
	if err != nil {
		t.Fatalf("Mailbox change payload: %v", err)
	}
	if payload.Reason != reason {
		t.Fatalf("Mailbox change reason = %q, want %q", payload.Reason, reason)
	}
	if messageID == uuid.Nil {
		if payload.MessageID != nil {
			t.Fatalf("Mailbox change message ID = %v, want omitted", payload.MessageID)
		}
		return
	}
	if payload.MessageID == nil || *payload.MessageID != messageID {
		t.Fatalf("Mailbox change message ID = %v, want %s", payload.MessageID, messageID)
	}
}

func TestMailboxAccessRequiresOwnerIncludingStalePrincipals(t *testing.T) {
	test := newMailboxTest(t)
	mustCreateMailbox(t, test, test.ownerA, "sensitive")
	event := receivedEvent("owner-access", "email-attachment", []string{"sensitive@" + mailboxDomain}, test.now)
	event.Attachments = []email.ReceivedAttachment{
		{ID: "private-file", Filename: "private.pdf", ContentType: "application/pdf"},
	}
	if _, err := test.service.Ingest(test.ctx, event); err != nil {
		t.Fatal(err)
	}
	page, err := test.service.List(test.ctx, test.ownerA, mailbox.ListOptions{})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("List(owner) = %#v, %v", page, err)
	}
	messageID := page.Messages[0].ID
	detail := mustDetail(t, test, messageID)
	attachmentID := detail.Attachments[0].ID
	for _, change := range test.mailboxEvents.take(t, 2) {
		payload, err := events.PayloadOf[mailbox.ChangedPayload](change)
		if err != nil || !slices.Equal(payload.RecipientUserIDs, []uuid.UUID{test.ownerA.UserID}) {
			t.Fatalf("Mailbox audience = %v, %v, want owner alone", payload.RecipientUserIDs, err)
		}
	}
	providerCalls := test.provider.emailCalls()
	for _, role := range []auth.CompanyRole{auth.CompanyRoleAdmin, auth.CompanyRoleMember} {
		dbtest.MustExec(
			t,
			test.ctx,
			test.pool,
			`UPDATE company_members SET role=$3 WHERE company_id=$1 AND user_id=$2`,
			test.fixture.CompanyA,
			test.fixture.UserA,
			string(role),
		)
		for _, stale := range []bool{false, true} {
			principal := test.ownerA
			if !stale {
				principal.Role = role
			}
			if _, err := test.service.GetState(test.ctx, principal); !errors.Is(err, mailbox.ErrForbidden) {
				t.Errorf("GetState(%s, stale=%t) error = %v, want forbidden", role, stale, err)
			}
			if _, err := test.service.Create(
				test.ctx,
				principal,
				"another",
			); !errors.Is(
				err,
				mailbox.ErrForbidden,
			) {
				t.Errorf("Create(%s, stale=%t) error = %v, want forbidden", role, stale, err)
			}
			listed, err := test.service.List(test.ctx, principal, mailbox.ListOptions{})
			var want error
			if !stale {
				want = mailbox.ErrForbidden
			}
			if len(listed.Messages) != 0 || !errors.Is(err, want) {
				t.Errorf("List(%s, stale=%t) = %#v, %v", role, stale, listed, err)
			}
			for name, read := range map[string]func() error{
				"detail":    func() error { _, err := test.service.GetDetail(test.ctx, principal, messageID); return err },
				"mark read": func() error { _, err := test.service.MarkRead(test.ctx, principal, messageID); return err },
				"attachment": func() error {
					_, err := test.service.GetAttachment(test.ctx, principal, messageID, attachmentID)
					return err
				},
			} {
				if err := read(); !errors.Is(err, mailbox.ErrForbidden) &&
					!errors.Is(err, mailbox.ErrMessageNotFound) {
					t.Errorf(
						"%s(%s, stale=%t) error = %v, want forbidden or not found",
						name,
						role,
						stale,
						err,
					)
				}
			}
		}
	}
	if got := test.provider.emailCalls(); got != providerCalls {
		t.Fatalf("provider calls = %d, want %d", got, providerCalls)
	}
	var read bool
	if err := test.pool.QueryRow(test.ctx, `SELECT read_at IS NOT NULL FROM mailbox_messages WHERE id=$1`, messageID).
		Scan(&read); err != nil ||
		read {
		t.Fatalf("denied request marked message read = %t, %v", read, err)
	}
}
