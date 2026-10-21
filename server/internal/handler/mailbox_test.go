package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/infra/email"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mailbox"
)

func TestMailboxRoutesRequireOwnerForPeopleAndAgents(t *testing.T) {
	t.Parallel()
	for _, role := range []auth.CompanyRole{auth.CompanyRoleAdmin, auth.CompanyRoleMember} {
		for _, runProof := range []bool{false, true} {
			t.Run(
				string(role)+map[bool]string{false: "/person", true: "/agent"}[runProof],
				func(t *testing.T) {
					t.Parallel()
					fixture := newMailboxHandlerFixture()
					fixture.principal.Role = role
					h := New(Handler{
						AuthService: authenticatedAuthStub(fixture.userID),
						CompanyService: &mailboxPermissionCompanyStub{
							principal: fixture.principal,
						},
						MailboxService:      &mailboxServiceStub{},
						MailboxDraftService: &mailboxDraftServiceStub{},
					})
					base := "/api/v1/companies/" + fixture.companyID.String()
					chat := "/conversations/" + uuid.NewString()
					id := uuid.NewString()
					// A Run proof on a person route is refused by RunProofPolicy before
					// the owner check; only the Run route that
					// prepares drafts reaches it with a proof.
					for _, route := range []struct {
						method, path string
						run          bool
					}{
						{http.MethodGet, "/mailbox", false},
						{http.MethodPost, "/mailbox", false},
						{http.MethodGet, "/mailbox/messages", false},
						{http.MethodGet, "/mailbox/messages/" + id, false},
						{http.MethodPost, "/mailbox/messages/" + id + "/read", false},
						{http.MethodGet, "/mailbox/messages/" + id + "/attachments/" + id, false},
						{http.MethodGet, chat + "/mail-draft-groups", false},
						{http.MethodPost, chat + "/mail-draft-groups", true},
						{http.MethodPost, chat + "/mail-draft-groups/" + id + "/send", false},
						{http.MethodPut, chat + "/mail-drafts/" + id, false},
						{http.MethodDelete, chat + "/mail-drafts/" + id, false},
						{http.MethodPost, chat + "/mail-drafts/" + id + "/send", false},
					} {
						request := httptestRequest(route.method, base+route.path, nil)
						request.Header.Set("Authorization", "Bearer session")
						if runProof {
							request.Header.Set(
								runTokenHeader,
								"brt_"+strings.Repeat("a", 64),
							)
						}
						want := "company_permission_required"
						if runProof && !route.run {
							want = "human_actor_required"
						}
						response := serveRequest(h, request)
						if response.Code != http.StatusForbidden ||
							!strings.Contains(response.Body.String(), want) {
							t.Errorf(
								"%s %s = %d %s, want 403 %s",
								route.method,
								route.path,
								response.Code,
								response.Body.String(),
								want,
							)
						}
					}
				},
			)
		}
	}
}

type mailboxPermissionCompanyStub struct {
	companyServiceStub
	principal auth.UserPrincipal
}

func (s *mailboxPermissionCompanyStub) Authorize(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) (auth.UserPrincipal, error) {
	return s.principal, nil
}

func (s *mailboxPermissionCompanyStub) RequirePermission(
	_ context.Context,
	_ auth.UserPrincipal,
	_ uuid.UUID,
	permission auth.Permission,
) (auth.UserPrincipal, error) {
	if !s.principal.Can(permission) {
		return auth.UserPrincipal{}, company.ErrForbidden
	}
	return s.principal, nil
}

func TestMailboxGetStateReturnsUnreadCount(t *testing.T) {
	t.Parallel()

	fixture := newMailboxHandlerFixture()
	service := &mailboxServiceStub{
		getState: func(_ context.Context, principal auth.UserPrincipal) (mailbox.State, error) {
			if principal != fixture.principal {
				t.Fatalf("GetState() principal = %#v", principal)
			}
			return mailbox.State{
				Status:      mailbox.StatusActive,
				Domain:      "mail.example.com",
				UnreadCount: 12,
			}, nil
		},
	}
	handler := fixture.handler(service, nil, nil)
	response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+fixture.companyID.String()+"/mailbox",
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"unreadCount":12`)) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestMailboxGetStateReportsDisabledWhenProviderIsNotConfigured(t *testing.T) {
	t.Parallel()

	fixture := newMailboxHandlerFixture()
	handler := fixture.handler(nil, nil, nil)
	response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+fixture.companyID.String()+"/mailbox",
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	for _, member := range [][]byte{
		[]byte(`"status":"disabled"`),
		[]byte(`"mailbox":null`),
		[]byte(`"canCreate":false`),
		[]byte(`"unreadCount":0`),
	} {
		if !bytes.Contains(response.Body.Bytes(), member) {
			t.Fatalf("body = %s, want member %s", response.Body.String(), member)
		}
	}
}

func TestMailboxCreateUsesOwnerPrincipalAndNormalizedDomainResponse(t *testing.T) {
	t.Parallel()

	fixture := newMailboxHandlerFixture()
	service := &mailboxServiceStub{
		create: func(_ context.Context, principal auth.UserPrincipal, localPart string) (mailbox.State, error) {
			if principal != fixture.principal || localPart != "Product.Team" {
				t.Fatalf("Create() = principal %#v, localPart %q", principal, localPart)
			}
			return mailbox.State{
				Status: mailbox.StatusActive,
				Domain: "mail.example.com",
				Mailbox: &mailbox.Mailbox{
					ID:      uuid.Must(uuid.NewV7()),
					Address: "product.team@mail.example.com",
				},
			}, nil
		},
	}
	handler := fixture.handler(service, nil, nil)
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+fixture.companyID.String()+"/mailbox",
		[]byte(`{"localPart":"Product.Team"}`),
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !bytes.Contains(response.Body.Bytes(), []byte(`"address":"product.team@mail.example.com"`)) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestMailboxCreateRejectsLocalPartLongerThanContract(t *testing.T) {
	t.Parallel()

	fixture := newMailboxHandlerFixture()
	service := &mailboxServiceStub{
		create: func(context.Context, auth.UserPrincipal, string) (mailbox.State, error) {
			t.Fatal("Create() called for a local part longer than the API contract")
			return mailbox.State{}, nil
		},
	}
	handler := fixture.handler(service, nil, nil)
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+fixture.companyID.String()+"/mailbox",
		[]byte(`{"localPart":"`+strings.Repeat("a", 65)+`"}`),
	)
	assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
}

func TestMailboxWebhookVerifiesExactRawBodyBeforeIngest(t *testing.T) {
	t.Parallel()

	fixture := newMailboxHandlerFixture()
	body := []byte(
		`{"type":"email.received","data":{"email_id":"email-1","created_at":"2026-08-18T02:03:04Z","from":"a@example.com","to":["sales@mail.example.com"],"subject":"Hi","attachments":[]}}`,
	)
	verifier := &mailboxWebhookVerifierStub{verify: func(got []byte, headers email.WebhookHeaders) error {
		if !bytes.Equal(got, body) || headers.ID != "event-1" || headers.Timestamp != "1787018584" ||
			headers.Signature != "v1,signature" {
			t.Fatalf("Verify() = body %q, headers %#v", got, headers)
		}
		return nil
	}}
	ingested := 0
	service := &mailboxServiceStub{
		ingest: func(_ context.Context, event email.ReceivedEvent) (mailbox.IngestResult, error) {
			ingested++
			if event.EventID != "event-1" || event.EmailID != "email-1" {
				t.Fatalf("Ingest() event = %#v", event)
			}
			return mailbox.IngestResult{Delivered: 1}, nil
		},
	}
	observer := &mailboxWebhookObserverStub{}
	handler := fixture.handler(service, verifier, observer)
	request := httptestRequest(http.MethodPost, "/api/v1/webhooks/resend/mailbox", body)
	request.Header.Set("svix-id", "event-1")
	request.Header.Set("svix-timestamp", "1787018584")
	request.Header.Set("svix-signature", "v1,signature")
	response := serveRequest(handler, request)
	if response.Code != http.StatusOK || ingested != 1 {
		t.Fatalf("status = %d, ingested = %d, body = %s", response.Code, ingested, response.Body.String())
	}

	verifier.verify = func([]byte, email.WebhookHeaders) error { return email.ErrInvalidWebhook }
	invalidRequest := httptestRequest(http.MethodPost, "/api/v1/webhooks/resend/mailbox", body)
	invalidRequest.Header = request.Header.Clone()
	response = serveRequest(handler, invalidRequest)
	if response.Code != http.StatusBadRequest || ingested != 1 {
		t.Fatalf("invalid signature status = %d, ingested = %d", response.Code, ingested)
	}
	if len(observer.outcomes) != 1 || observer.outcomes[0] != "invalid" {
		t.Fatalf("webhook outcomes = %#v, want one invalid", observer.outcomes)
	}
}

func TestMailboxWebhookRejectsOversizedBodyBeforeVerification(t *testing.T) {
	t.Parallel()

	fixture := newMailboxHandlerFixture()
	verified := false
	verifier := &mailboxWebhookVerifierStub{verify: func([]byte, email.WebhookHeaders) error {
		verified = true
		return nil
	}}
	observer := &mailboxWebhookObserverStub{}
	handler := fixture.handler(&mailboxServiceStub{}, verifier, observer)
	response := serveRequest(handler, httptestRequest(
		http.MethodPost,
		"/api/v1/webhooks/resend/mailbox",
		bytes.Repeat([]byte("x"), maxMailboxWebhookBytes+1),
	))
	if response.Code != http.StatusBadRequest || verified {
		t.Fatalf("status = %d, verified = %t", response.Code, verified)
	}
	if len(observer.outcomes) != 1 || observer.outcomes[0] != "invalid" {
		t.Fatalf("webhook outcomes = %#v, want one invalid", observer.outcomes)
	}
}

func TestMailboxProviderRateLimitReturnsRetryAfter(t *testing.T) {
	t.Parallel()

	fixture := newMailboxHandlerFixture()
	messageID := uuid.Must(uuid.NewV7())
	service := &mailboxServiceStub{getDetail: func(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
	) (mailbox.Detail, error) {
		return mailbox.Detail{}, &mailbox.ProviderRateLimitError{RetryAfter: 1500 * time.Millisecond}
	}}
	handler := fixture.handler(service, nil, nil)
	response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+fixture.companyID.String()+"/mailbox/messages/"+messageID.String(),
		nil,
	)
	assertErrorResponse(t, response, http.StatusTooManyRequests, "mailbox_provider_rate_limited")
	if response.Header().Get("Retry-After") != "2" {
		t.Fatalf("Retry-After = %q, want 2", response.Header().Get("Retry-After"))
	}
}

type mailboxServiceStub struct {
	getState      func(context.Context, auth.UserPrincipal) (mailbox.State, error)
	create        func(context.Context, auth.UserPrincipal, string) (mailbox.State, error)
	list          func(context.Context, auth.UserPrincipal, mailbox.ListOptions) (mailbox.Page, error)
	getDetail     func(context.Context, auth.UserPrincipal, uuid.UUID) (mailbox.Detail, error)
	markRead      func(context.Context, auth.UserPrincipal, uuid.UUID) (mailbox.Message, error)
	getAttachment func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (mailbox.AttachmentDownload, error)
	ingest        func(context.Context, email.ReceivedEvent) (mailbox.IngestResult, error)
}

func (s *mailboxServiceStub) GetState(ctx context.Context, principal auth.UserPrincipal) (mailbox.State, error) {
	if s.getState == nil {
		return mailbox.State{}, errors.New("unexpected GetState call")
	}
	return s.getState(ctx, principal)
}

func (s *mailboxServiceStub) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	localPart string,
) (mailbox.State, error) {
	if s.create == nil {
		return mailbox.State{}, errors.New("unexpected Create call")
	}
	return s.create(ctx, principal, localPart)
}

func (s *mailboxServiceStub) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options mailbox.ListOptions,
) (mailbox.Page, error) {
	if s.list == nil {
		return mailbox.Page{}, errors.New("unexpected List call")
	}
	return s.list(ctx, principal, options)
}

func (s *mailboxServiceStub) GetDetail(
	ctx context.Context,
	principal auth.UserPrincipal,
	messageID uuid.UUID,
) (mailbox.Detail, error) {
	if s.getDetail == nil {
		return mailbox.Detail{}, errors.New("unexpected GetDetail call")
	}
	return s.getDetail(ctx, principal, messageID)
}

func (s *mailboxServiceStub) MarkRead(
	ctx context.Context,
	principal auth.UserPrincipal,
	messageID uuid.UUID,
) (mailbox.Message, error) {
	if s.markRead == nil {
		return mailbox.Message{}, errors.New("unexpected MarkRead call")
	}
	return s.markRead(ctx, principal, messageID)
}

func (s *mailboxServiceStub) GetAttachment(
	ctx context.Context,
	principal auth.UserPrincipal,
	messageID uuid.UUID,
	attachmentID uuid.UUID,
) (mailbox.AttachmentDownload, error) {
	if s.getAttachment == nil {
		return mailbox.AttachmentDownload{}, errors.New("unexpected GetAttachment call")
	}
	return s.getAttachment(ctx, principal, messageID, attachmentID)
}

func (s *mailboxServiceStub) Ingest(
	ctx context.Context,
	event email.ReceivedEvent,
) (mailbox.IngestResult, error) {
	if s.ingest == nil {
		return mailbox.IngestResult{}, errors.New("unexpected Ingest call")
	}
	return s.ingest(ctx, event)
}

type mailboxWebhookVerifierStub struct {
	verify func([]byte, email.WebhookHeaders) error
}

type mailboxWebhookObserverStub struct {
	outcomes []string
}

func (s *mailboxWebhookObserverStub) ObserveMailboxWebhook(outcome string) {
	s.outcomes = append(s.outcomes, outcome)
}

func (s *mailboxWebhookVerifierStub) Verify(body []byte, headers email.WebhookHeaders) error {
	return s.verify(body, headers)
}

type mailboxHandlerFixture struct {
	userID    uuid.UUID
	companyID uuid.UUID
	principal auth.UserPrincipal
}

func newMailboxHandlerFixture() mailboxHandlerFixture {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	return mailboxHandlerFixture{
		userID: userID, companyID: companyID,
		principal: auth.UserPrincipal{
			UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
			AuthKind:  auth.TokenKindBearer,
			CompanyID: companyID, Role: auth.CompanyRoleOwner,
		},
	}
}

func (f mailboxHandlerFixture) handler(
	service MailboxService,
	verifier email.WebhookVerifier,
	observer MailboxWebhookObserver,
) *Handler {
	companies := &companyServiceStub{
		authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
			return f.principal, nil
		},
		requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
			return f.principal, nil
		},
	}
	return New(Handler{
		AuthService: authenticatedAuthStub(f.userID), CompanyService: companies,
		MailboxService: service, MailboxWebhookVerifier: verifier,
		MailboxWebhookObserver: observer,
	})
}

func httptestRequest(method, path string, body []byte) *http.Request {
	return httptest.NewRequest(method, path, bytes.NewReader(body))
}

func serveRequest(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
