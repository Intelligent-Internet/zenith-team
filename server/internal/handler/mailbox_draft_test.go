package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/mailbox"
)

func TestAgentMailCreateUsesTrustedPathAndAuthenticatedPrincipal(t *testing.T) {
	t.Parallel()
	fixture := newMailboxHandlerFixture()
	conversationID := uuid.Must(uuid.NewV7())
	runID := uuid.MustParse("00000000-0000-4000-8000-000000000042")
	agentID := uuid.Must(uuid.NewV7())
	requestKey := uuid.Must(uuid.NewV7())
	groupID := uuid.Must(uuid.NewV7())
	drafts := &mailboxDraftServiceStub{
		create: func(
			_ context.Context,
			principal auth.UserPrincipal,
			trusted mailbox.TrustedDraftContext,
			input []mailbox.DraftInput,
		) (mailbox.DraftCreateResult, error) {
			if principal != fixture.principal || trusted.CompanyID != fixture.companyID ||
				trusted.ConversationID != conversationID || trusted.RunID != runID ||
				trusted.AgentID != agentID || trusted.RequestKey != requestKey || len(input) != 1 {
				t.Fatalf(
					"Create() principal = %#v, trusted = %#v, drafts = %#v",
					principal,
					trusted,
					input,
				)
			}
			return mailbox.DraftCreateResult{
				DraftGroup: mailbox.DraftGroup{ID: groupID, Drafts: []mailbox.Draft{}}, Created: true,
			}, nil
		},
	}
	handler := fixture.draftHandler(drafts)
	handler.RunProofResolver = runProofResolverStub{origin: execution.RunOrigin{
		RunID: runID, CompanyID: fixture.companyID, AgentID: agentID,
		ConversationID: conversationID, ConversationKind: "direct",
		AuthorizedByUserID: &fixture.userID,
	}}
	body := []byte(
		`{"drafts":[{"subject":"Hello","text":"Body","deliveries":[{"to":["lead@example.com"],"cc":[]}]}]}`,
	)
	response := serveHITLJSON(
		handler, http.MethodPost,
		"/api/v1/companies/"+fixture.companyID.String()+"/conversations/"+
			conversationID.String()+"/mail-draft-groups",
		requestKey,
		body,
	)
	if response.Code != http.StatusCreated ||
		!bytes.Contains(response.Body.Bytes(), []byte(groupID.String())) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

func TestAgentMailQueueReturnsAcceptedAndStableConflict(t *testing.T) {
	t.Parallel()
	fixture := newMailboxHandlerFixture()
	conversationID := uuid.Must(uuid.NewV7())
	draftID := uuid.Must(uuid.NewV7())
	drafts := &mailboxDraftServiceStub{
		queueDraft: func(
			_ context.Context,
			_ auth.UserPrincipal,
			gotConversationID uuid.UUID,
			gotDraftID uuid.UUID,
			version int64,
		) (mailbox.Draft, error) {
			if gotConversationID != conversationID || gotDraftID != draftID || version != 3 {
				t.Fatalf("QueueDraft() = %s %s %d", gotConversationID, gotDraftID, version)
			}
			return mailbox.Draft{}, mailbox.ErrAgentMailDraftConflict
		},
	}
	handler := fixture.draftHandler(drafts)
	response := serveBearerJSON(
		handler, http.MethodPost,
		"/api/v1/companies/"+fixture.companyID.String()+"/conversations/"+
			conversationID.String()+"/mail-drafts/"+draftID.String()+"/send",
		[]byte(`{"expectedRevision":3}`),
	)
	assertErrorResponse(t, response, http.StatusConflict, "agent_mail_draft_conflict")
}

func TestAgentMailListRequiresBoundedSourceRuns(t *testing.T) {
	t.Parallel()
	fixture := newMailboxHandlerFixture()
	conversationID := uuid.Must(uuid.NewV7())
	firstRunID := uuid.MustParse("00000000-0000-4000-8000-000000000041")
	secondRunID := uuid.MustParse("00000000-0000-4000-8000-000000000042")
	groupID := uuid.Must(uuid.NewV7())
	listCalls := 0
	drafts := &mailboxDraftServiceStub{
		listGroups: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotConversationID uuid.UUID,
			sourceRunIDs []uuid.UUID,
		) ([]mailbox.DraftGroup, error) {
			listCalls++
			if principal != fixture.principal || gotConversationID != conversationID ||
				len(sourceRunIDs) != 2 || sourceRunIDs[0] != firstRunID ||
				sourceRunIDs[1] != secondRunID {
				t.Fatalf("ListGroups() = %#v, %s, %#v", principal, gotConversationID, sourceRunIDs)
			}
			return []mailbox.DraftGroup{{ID: groupID}}, nil
		},
	}
	handler := fixture.draftHandler(drafts)
	path := "/api/v1/companies/" + fixture.companyID.String() + "/conversations/" +
		conversationID.String() + "/mail-draft-groups"

	missing := serveBearerJSON(handler, http.MethodGet, path, nil)
	assertErrorResponse(t, missing, http.StatusUnprocessableEntity, "validation_failed")
	if listCalls != 0 {
		t.Fatalf("ListGroups() calls without source Runs = %d", listCalls)
	}
	tooManyIDs := make([]string, maxMailboxDraftSourceRunIDs+1)
	for index := range tooManyIDs {
		tooManyIDs[index] = strconv.Itoa(index + 1)
	}
	overLimit := serveBearerJSON(
		handler, http.MethodGet, path+"?source_run_ids="+strings.Join(tooManyIDs, ","), nil,
	)
	assertErrorResponse(t, overLimit, http.StatusUnprocessableEntity, "validation_failed")
	if listCalls != 0 {
		t.Fatalf("ListGroups() calls above source Run limit = %d", listCalls)
	}

	response := serveBearerJSON(
		handler, http.MethodGet,
		path+"?source_run_ids="+firstRunID.String()+","+
			secondRunID.String(), nil,
	)
	if response.Code != http.StatusOK || listCalls != 1 ||
		!bytes.Contains(response.Body.Bytes(), []byte(groupID.String())) {
		t.Fatalf("response = %d %s; ListGroups calls = %d", response.Code, response.Body.String(), listCalls)
	}
}

type mailboxDraftServiceStub struct {
	create func(
		context.Context, auth.UserPrincipal, mailbox.TrustedDraftContext, []mailbox.DraftInput,
	) (mailbox.DraftCreateResult, error)
	queueDraft func(
		context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64,
	) (mailbox.Draft, error)
	listGroups func(
		context.Context, auth.UserPrincipal, uuid.UUID, []uuid.UUID,
	) ([]mailbox.DraftGroup, error)
}

func (s *mailboxDraftServiceStub) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	trusted mailbox.TrustedDraftContext,
	drafts []mailbox.DraftInput,
) (mailbox.DraftCreateResult, error) {
	if s.create == nil {
		return mailbox.DraftCreateResult{}, errors.New("unexpected Create call")
	}
	return s.create(ctx, principal, trusted, drafts)
}

func (s *mailboxDraftServiceStub) ListGroups(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	sourceRunIDs []uuid.UUID,
) ([]mailbox.DraftGroup, error) {
	if s.listGroups == nil {
		return nil, errors.New("unexpected ListGroups call")
	}
	return s.listGroups(ctx, principal, conversationID, sourceRunIDs)
}

func (*mailboxDraftServiceStub) UpdateDraft(
	context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, mailbox.UpdateDraftInput,
) (mailbox.Draft, error) {
	return mailbox.Draft{}, errors.New("unexpected UpdateDraft call")
}

func (*mailboxDraftServiceStub) DeleteDraft(
	context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int64,
) error {
	return errors.New("unexpected DeleteDraft call")
}

func (s *mailboxDraftServiceStub) QueueDraft(
	ctx context.Context,
	principal auth.UserPrincipal,
	conversationID uuid.UUID,
	draftID uuid.UUID,
	version int64,
) (mailbox.Draft, error) {
	if s.queueDraft == nil {
		return mailbox.Draft{}, errors.New("unexpected QueueDraft call")
	}
	return s.queueDraft(ctx, principal, conversationID, draftID, version)
}

func (*mailboxDraftServiceStub) QueueGroup(
	context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, []mailbox.DraftRevision,
) (mailbox.DraftGroup, error) {
	return mailbox.DraftGroup{}, errors.New("unexpected QueueGroup call")
}

func (f mailboxHandlerFixture) draftHandler(service MailboxDraftService) *Handler {
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
		MailboxService: &mailboxServiceStub{}, MailboxDraftService: service,
	})
}
