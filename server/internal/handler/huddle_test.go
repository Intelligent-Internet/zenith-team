package handler

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
)

type huddleHTTPStub struct {
	start func(auth.UserPrincipal, uuid.UUID, conversation.StartHuddleRequest) (conversation.StartHuddleResult, error)
	join  func(auth.UserPrincipal, uuid.UUID, conversation.JoinHuddleRequest) (conversation.JoinHuddleResult, error)
}

func (s huddleHTTPStub) Start(
	_ context.Context,
	principal auth.UserPrincipal,
	sourceID uuid.UUID,
	request conversation.StartHuddleRequest,
) (conversation.StartHuddleResult, error) {
	return s.start(principal, sourceID, request)
}

func (s huddleHTTPStub) Join(
	_ context.Context,
	principal auth.UserPrincipal,
	huddleID uuid.UUID,
	request conversation.JoinHuddleRequest,
) (conversation.JoinHuddleResult, error) {
	return s.join(principal, huddleID, request)
}

func (s huddleHTTPStub) ConfirmConnected(context.Context, uuid.UUID, uuid.UUID) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}
func (s huddleHTTPStub) ConfirmConnectedByIdentity(context.Context, uuid.UUID, string, string) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}
func (s huddleHTTPStub) ConfirmConnectedByIdentityAnyCompany(context.Context, string, string) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}
func (s huddleHTTPStub) EndFromMedia(context.Context, uuid.UUID, string) error { return nil }
func (s huddleHTTPStub) EndFromMediaAnyCompany(context.Context, string) error  { return nil }
func (s huddleHTTPStub) ReconcileMediaRooms(context.Context) (int, error)      { return 0, nil }
func (s huddleHTTPStub) EndParticipantFromMedia(context.Context, uuid.UUID, string, string) error {
	return nil
}
func (s huddleHTTPStub) EndParticipantFromMediaAnyCompany(context.Context, string, string) error {
	return nil
}
func (s huddleHTTPStub) Invite(context.Context, auth.UserPrincipal, uuid.UUID, conversation.InviteHuddleRequest) (conversation.HuddleInvitation, error) {
	return conversation.HuddleInvitation{}, nil
}
func (s huddleHTTPStub) RespondInvitation(context.Context, auth.UserPrincipal, uuid.UUID, conversation.RespondHuddleInvitationRequest) (conversation.HuddleInvitation, error) {
	return conversation.HuddleInvitation{}, nil
}

func (s huddleHTTPStub) Leave(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (conversation.JoinHuddleResult, error) {
	return conversation.JoinHuddleResult{}, nil
}

func (s huddleHTTPStub) SweepExpiredReservations(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (s huddleHTTPStub) SweepExpiredInvitations(context.Context, time.Time) (int, error) {
	return 0, nil
}
func (s huddleHTTPStub) ListPendingInvitations(context.Context, auth.UserPrincipal) ([]conversation.HuddleInvitation, error) {
	return nil, nil
}
func (s huddleHTTPStub) ListInvitationHistory(context.Context, auth.UserPrincipal) ([]conversation.HuddleInvitation, error) {
	return nil, nil
}
func (s huddleHTTPStub) DeliverMissedInvitations(context.Context) (int, error) { return 0, nil }
func (s huddleHTTPStub) ListConnections(context.Context, auth.UserPrincipal, uuid.UUID) ([]conversation.HuddleConnection, error) {
	return nil, nil
}

func TestStartHuddleHTTP(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	sourceID, requestKey, huddleID, chatID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	deadline := time.Now().UTC().Add(time.Hour)
	service := huddleHTTPStub{start: func(
		got auth.UserPrincipal,
		gotSourceID uuid.UUID,
		request conversation.StartHuddleRequest,
	) (conversation.StartHuddleResult, error) {
		if got.UserID != principal.UserID || got.CompanyID != principal.CompanyID || got.Role != principal.Role ||
			gotSourceID != sourceID || request.RequestKey != requestKey {
			t.Fatalf("Start() input = %+v %v %+v", got, gotSourceID, request)
		}
		return conversation.StartHuddleResult{
			Created: true,
			Huddle: conversation.Huddle{
				ID:                   huddleID,
				CompanyID:            principal.CompanyID,
				SourceConversationID: sourceID,
				ChatConversationID:   chatID,
				StartedByUserID:      principal.UserID,
				State:                conversation.HuddleStateStarting,
				Revision:             1,
				DeadlineAt:           deadline,
			},
		}, nil
	}}
	handler := huddleHTTPHandler(principal, service)
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/conversations/" + sourceID.String() + "/huddles"
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		path,
		[]byte(`{"requestKey":"`+requestKey.String()+`"}`),
	)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `"id":"`+huddleID.String()+`"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

func TestStartHuddleHTTPRejectsInvalidBodyAndMapsErrors(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	sourceID := uuid.New()
	called := false
	service := huddleHTTPStub{start: func(
		auth.UserPrincipal,
		uuid.UUID,
		conversation.StartHuddleRequest,
	) (conversation.StartHuddleResult, error) {
		called = true
		return conversation.StartHuddleResult{}, conversation.ErrHuddleRequestConflict
	}}
	handler := huddleHTTPHandler(principal, service)
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/conversations/" + sourceID.String() + "/huddles"
	for _, body := range []string{`{}`, `{"requestKey":null}`, `{"requestKey":"bad"}`, `{"requestKey":"` + uuid.NewString() + `","extra":true}`} {
		response := serveBearerJSON(handler, http.MethodPost, path, []byte(body))
		if response.Code != http.StatusUnprocessableEntity && response.Code != http.StatusBadRequest {
			t.Fatalf("body %s response = %d %s", body, response.Code, response.Body)
		}
	}
	if called {
		t.Fatal("service called for invalid body")
	}
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		path,
		[]byte(`{"requestKey":"`+uuid.NewString()+`"}`),
	)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"huddle_request_conflict"`) {
		t.Fatalf("mapped response = %d %s", response.Code, response.Body)
	}
}

func TestReserveHuddleConnectionHTTP(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	huddleID, requestKey, connectionID := uuid.New(), uuid.New(), uuid.New()
	service := huddleHTTPStub{join: func(got auth.UserPrincipal, gotHuddleID uuid.UUID, request conversation.JoinHuddleRequest) (conversation.JoinHuddleResult, error) {
		if got.UserID != principal.UserID || gotHuddleID != huddleID || request.RequestKey != requestKey {
			t.Fatalf("Join() input = %+v %v %+v", got, gotHuddleID, request)
		}
		return conversation.JoinHuddleResult{
			Huddle:     conversation.Huddle{ID: huddleID, CompanyID: principal.CompanyID},
			Connection: conversation.HuddleConnection{ID: connectionID, HuddleID: huddleID, UserID: principal.UserID, Status: "reserved", Revision: 1},
		}, nil
	}}
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/huddles/" + huddleID.String() + "/connections"
	response := serveBearerJSON(huddleHTTPHandler(principal, service), http.MethodPost, path, []byte(`{"requestKey":"`+requestKey.String()+`"}`))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"reserved"`) ||
		!strings.Contains(response.Body.String(), connectionID.String()) {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

func TestReserveHuddleConnectionHTTPReportsAlreadyConnected(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	huddleID := uuid.New()
	service := huddleHTTPStub{join: func(auth.UserPrincipal, uuid.UUID, conversation.JoinHuddleRequest) (conversation.JoinHuddleResult, error) {
		return conversation.JoinHuddleResult{}, conversation.ErrHuddleAlreadyConnected
	}}
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/huddles/" + huddleID.String() + "/connections"
	response := serveBearerJSON(huddleHTTPHandler(principal, service), http.MethodPost, path, []byte(`{"requestKey":"`+uuid.NewString()+`"}`))
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"huddle_already_connected"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body)
	}
}

func huddleHTTPHandler(principal auth.UserPrincipal, service conversation.HuddleService) *Handler {
	resolve := func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID) (auth.UserPrincipal, error) {
		return principal, nil
	}
	return New(Handler{
		AuthService:              authenticatedAuthStub(principal.UserID),
		CompanyService:           &companyServiceStub{authorize: resolve, requirePermission: resolve},
		HumanConversationService: &conversation.DefaultConversationService{},
		DiscussionService:        &conversation.DefaultDiscussionService{},
		HuddleService:            service,
		HuddleCallsEnabled:       true,
	})
}

func TestHuddleCallsNeedLiveKit(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleMember}
	service := huddleHTTPStub{
		start: func(auth.UserPrincipal, uuid.UUID, conversation.StartHuddleRequest) (conversation.StartHuddleResult, error) {
			t.Fatal("Start() reached without LiveKit")
			return conversation.StartHuddleResult{}, nil
		},
		join: func(auth.UserPrincipal, uuid.UUID, conversation.JoinHuddleRequest) (conversation.JoinHuddleResult, error) {
			t.Fatal("Join() reached without LiveKit")
			return conversation.JoinHuddleResult{}, nil
		},
	}
	handler := huddleHTTPHandler(principal, service)
	handler.HuddleCallsEnabled = false
	base := "/api/v1/companies/" + principal.CompanyID.String()
	body := []byte(`{"requestKey":"` + uuid.NewString() + `"}`)
	for _, path := range []string{
		base + "/conversations/" + uuid.NewString() + "/huddles",
		base + "/huddles/" + uuid.NewString() + "/connections",
	} {
		response := serveBearerJSON(handler, http.MethodPost, path, body)
		if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), `"huddle_not_configured"`) {
			t.Fatalf("%s response = %d %s", path, response.Code, response.Body)
		}
	}
	response := serveBearerJSON(handler, http.MethodGet, base+"/huddles/capabilities", nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"calls":false`) {
		t.Fatalf("capabilities response = %d %s", response.Code, response.Body)
	}
}
