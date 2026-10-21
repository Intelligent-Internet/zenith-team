package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type runtimeServiceStub struct {
	list       func(context.Context, auth.UserPrincipal, agent.RuntimeListOptions) (agent.RuntimePage, error)
	getRuntime func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Runtime, error)
	rename     func(context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRenameInput) (agent.Runtime, error)
	remove     func(context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRemovalInput) (agent.RuntimeRemoval, error)
	initiate   func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.ModelDiscoveryRequest, error)
	refresh    func(context.Context, auth.UserPrincipal, uuid.UUID) error
	get        func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID) (agent.ModelDiscoveryRequest, error)
	claim      func(context.Context, uuid.UUID) (*agent.ModelDiscoveryRequest, error)
	finish     func(context.Context, uuid.UUID, uuid.UUID, agent.ModelDiscoveryResult) (agent.ModelDiscoveryRequest, error)
	heartbeat  func(context.Context, auth.UserPrincipal, uuid.UUID, string) error
	deregister func(context.Context, auth.UserPrincipal, agent.RuntimeDeregistration) error
}

func (s runtimeServiceStub) InitiateModelDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
) (agent.ModelDiscoveryRequest, error) {
	return s.initiate(ctx, principal, runtimeID)
}

func (s runtimeServiceStub) RequestRefresh(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
) error {
	if s.refresh == nil {
		return nil
	}
	return s.refresh(ctx, principal, runtimeID)
}

func (s runtimeServiceStub) GetModelDiscovery(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	requestID uuid.UUID,
) (agent.ModelDiscoveryRequest, error) {
	return s.get(ctx, principal, runtimeID, requestID)
}

func (s runtimeServiceStub) ClaimModelDiscovery(
	ctx context.Context,
	runtimeID uuid.UUID,
) (*agent.ModelDiscoveryRequest, error) {
	if s.claim == nil {
		return nil, nil
	}
	return s.claim(ctx, runtimeID)
}

func (s runtimeServiceStub) FinishModelDiscovery(
	ctx context.Context,
	runtimeID uuid.UUID,
	requestID uuid.UUID,
	result agent.ModelDiscoveryResult,
) (agent.ModelDiscoveryRequest, error) {
	return s.finish(ctx, runtimeID, requestID, result)
}

func (s runtimeServiceStub) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options agent.RuntimeListOptions,
) (agent.RuntimePage, error) {
	return s.list(ctx, principal, options)
}

func (s runtimeServiceStub) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
) (agent.Runtime, error) {
	return s.getRuntime(ctx, principal, runtimeID)
}

func (s runtimeServiceStub) Rename(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	input agent.RuntimeRenameInput,
) (agent.Runtime, error) {
	return s.rename(ctx, principal, runtimeID, input)
}

func (s runtimeServiceStub) Remove(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	input agent.RuntimeRemovalInput,
) (agent.RuntimeRemoval, error) {
	return s.remove(ctx, principal, runtimeID, input)
}

func (s runtimeServiceStub) RegisterRuntimes(
	context.Context,
	agent.RuntimeRegistration,
) ([]agent.Runtime, error) {
	return nil, nil
}

func (s runtimeServiceStub) DeregisterRuntimes(
	ctx context.Context,
	principal auth.UserPrincipal,
	input agent.RuntimeDeregistration,
) error {
	if s.deregister == nil {
		return nil
	}
	return s.deregister(ctx, principal, input)
}

func (s runtimeServiceStub) Heartbeat(
	ctx context.Context,
	principal auth.UserPrincipal,
	runtimeID uuid.UUID,
	daemonID string,
) error {
	if s.heartbeat == nil {
		return nil
	}
	return s.heartbeat(ctx, principal, runtimeID, daemonID)
}

func TestParseRuntimeListOptionsAcceptsDocumentedFilters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		rawQuery     string
		wantStatus   string
		wantProvider string
	}{
		{name: "no filters"},
		{name: "status", rawQuery: "status=online", wantStatus: "online"},
		{name: "provider", rawQuery: "provider=claude", wantProvider: "claude"},
		{
			name: "combined", rawQuery: "status=offline&provider=codex",
			wantStatus: "offline", wantProvider: "codex",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRuntimeListOptions(test.rawQuery)
			if err != nil {
				t.Fatalf("parseRuntimeListOptions() error = %v", err)
			}
			if optionalString(got.Status) != test.wantStatus ||
				optionalString(got.Provider) != test.wantProvider {
				t.Fatalf("parseRuntimeListOptions() = %+v", got)
			}
		})
	}
}

func TestParseRuntimeListOptionsRejectsUnknownRepeatedEmptyAndInvalidFilters(t *testing.T) {
	t.Parallel()

	for _, rawQuery := range []string{
		"unknown=value",
		"limit=25",
		"cursor=retired",
		"status=",
		"status=starting",
		"status=online&status=offline",
		"provider=",
		"provider=other",
		"provider=claude&provider=codex",
		"status=%ZZ",
	} {
		t.Run(rawQuery, func(t *testing.T) {
			t.Parallel()
			if _, err := parseRuntimeListOptions(rawQuery); err == nil {
				t.Fatalf("parseRuntimeListOptions(%q) error = nil", rawQuery)
			}
		})
	}
}

func TestRuntimeListRouteRejectsInvalidFiltersBeforeQuery(t *testing.T) {
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	companies := &companyServiceStub{
		authorize: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotCompanyID uuid.UUID,
		) (auth.UserPrincipal, error) {
			if principal.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("Authorize() = %+v / %s", principal, gotCompanyID)
			}
			principal.CompanyID = companyID
			principal.Role = auth.CompanyRoleMember
			return principal, nil
		},
	}
	handler := New(Handler{
		AuthService:    authenticatedAuthStub(userID),
		CompanyService: companies,
		RuntimeService: runtimeServiceStub{list: func(
			context.Context, auth.UserPrincipal, agent.RuntimeListOptions,
		) (agent.RuntimePage, error) {
			t.Fatal("RuntimeService.List() called for invalid query")
			return agent.RuntimePage{}, nil
		}},
	})
	base := "/api/v1/companies/" + companyID.String() + "/runtimes"
	for _, query := range []string{
		"?unknown=value",
		"?limit=25",
		"?status=",
		"?status=starting",
		"?status=online&status=offline",
		"?provider=",
		"?provider=other",
		"?provider=claude&provider=codex",
	} {
		response := serveBearerRequest(handler, http.MethodGet, base+query, nil)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
}

func TestRuntimeListRouteReturnsOwningDomainProjection(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	daemonID := "daemon-a"
	createdAt := time.Date(2026, 8, 19, 9, 10, 11, 123000000, time.UTC)
	service := runtimeServiceStub{list: func(
		_ context.Context,
		principal auth.UserPrincipal,
		options agent.RuntimeListOptions,
	) (agent.RuntimePage, error) {
		if principal.UserID != userID || principal.CompanyID != companyID ||
			optionalString(options.Status) != "online" || optionalString(options.Provider) != "codex" {
			t.Fatalf("List() principal/options = %+v / %+v", principal, options)
		}
		return agent.RuntimePage{Items: []agent.Runtime{{
			ID: runtimeID, CompanyID: companyID, DaemonID: daemonID,
			// An unusable runtime is structurally offline (the CHECK forbids
			// the pair online + reason), so the stub row is offline too.
			Name: "Alex Mac", Provider: "codex", Status: "offline",
			DeviceInfo: "macOS arm64",
			// The domain projection rides the user plane unchanged, so a
			// stored unusable reason reaches the roster as its object.
			UnusableReason: &protocol.RuntimeUnusableReason{
				Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
				Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
			},
			CreatedAt: createdAt, UpdatedAt: createdAt,
		}}}, nil
	}}
	companies := &companyServiceStub{authorize: func(
		_ context.Context,
		principal auth.UserPrincipal,
		gotCompanyID uuid.UUID,
	) (auth.UserPrincipal, error) {
		principal.CompanyID = gotCompanyID
		principal.Role = auth.CompanyRoleMember
		return principal, nil
	}}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID), CompanyService: companies, RuntimeService: service,
	})
	response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/runtimes?status=online&provider=codex",
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		Items []agent.Runtime `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].ID != runtimeID ||
		body.Items[0].Status != "offline" || body.Items[0].DaemonID != daemonID {
		t.Fatalf("response = %+v", body)
	}
	if reason := body.Items[0].UnusableReason; reason == nil ||
		reason.Code != protocol.RuntimeUnusableCodeBelowMinimumVersion ||
		reason.Message != "Codex CLI 0.90.0 is below the minimum 0.100.0" {
		t.Fatalf("response unusableReason = %+v, want the stubbed reason on the wire", reason)
	}
	if !strings.Contains(response.Body.String(), `"unusableReason":{"code":"below_minimum_version"`) {
		t.Fatalf("response body %s does not carry the camelCase unusableReason object", response.Body.String())
	}
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func memberCompanyStub(t *testing.T, userID, companyID uuid.UUID) *companyServiceStub {
	t.Helper()
	return &companyServiceStub{authorize: func(
		_ context.Context,
		principal auth.UserPrincipal,
		gotCompanyID uuid.UUID,
	) (auth.UserPrincipal, error) {
		if principal.UserID != userID || gotCompanyID != companyID {
			t.Fatalf("Authorize() = %+v / %s", principal, gotCompanyID)
		}
		principal.CompanyID = companyID
		principal.Role = auth.CompanyRoleMember
		return principal, nil
	}}
}

func TestRuntimeGetRouteReturnsOwningDomainProjection(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	ownerID := uuid.Must(uuid.NewV7())
	lastSeenAt := time.Date(2026, 8, 19, 9, 10, 11, 123000000, time.UTC)
	service := runtimeServiceStub{getRuntime: func(
		_ context.Context,
		principal auth.UserPrincipal,
		gotRuntimeID uuid.UUID,
	) (agent.Runtime, error) {
		if principal.UserID != userID || principal.CompanyID != companyID || gotRuntimeID != runtimeID {
			t.Fatalf("Get() principal/runtime = %+v / %s", principal, gotRuntimeID)
		}
		return agent.Runtime{
			ID: runtimeID, CompanyID: companyID, DaemonID: "daemon-a",
			Name: "Claude (alex-macbook)", Provider: "claude", Status: "online",
			Owner:           &agent.OwnerRef{ID: ownerID, Kind: "user", DisplayName: "Alex Rivera"},
			DeviceInfo:      "alex-macbook · 2.1.4",
			ProviderVersion: "2.1.4", CLIVersion: "0.9.3", LaunchedBy: agent.RuntimeLaunchedByDesktop,
			LastSeenAt: &lastSeenAt, CreatedAt: lastSeenAt, UpdatedAt: lastSeenAt,
		}, nil
	}}
	handler := New(Handler{
		AuthService:    authenticatedAuthStub(userID),
		CompanyService: memberCompanyStub(t, userID, companyID),
		RuntimeService: service,
	})
	response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/runtimes/"+runtimeID.String(),
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	owner, _ := body["owner"].(map[string]any)
	if body["id"] != runtimeID.String() || body["companyId"] != companyID.String() ||
		body["daemonId"] != "daemon-a" || body["name"] != "Claude (alex-macbook)" ||
		body["provider"] != "claude" || body["status"] != "online" ||
		owner["name"] != "Alex Rivera" || body["deviceInfo"] != "alex-macbook · 2.1.4" ||
		body["providerVersion"] != "2.1.4" || body["cliVersion"] != "0.9.3" ||
		body["launchedBy"] != "desktop" || body["lastSeenAt"] != "2026-08-19T09:10:11.123Z" ||
		body["createdAt"] == nil || body["updatedAt"] == nil {
		t.Fatalf("response = %s", response.Body.String())
	}
}

func TestRuntimeGetRouteAnswersNotFoundForMalformedAndUnknownIds(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	base := "/api/v1/companies/" + companyID.String() + "/runtimes/"

	tests := []struct {
		name       string
		path       string
		getRuntime func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Runtime, error)
		wantStatus int
		wantCode   string
	}{
		{
			name: "malformed id short-circuits",
			path: base + "not-a-uuid",
			getRuntime: func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Runtime, error) {
				t.Fatal("RuntimeService.Get() called for a malformed id")
				return agent.Runtime{}, nil
			},
			wantStatus: http.StatusNotFound,
			wantCode:   "runtime_not_found",
		},
		{
			name: "unknown or foreign id",
			path: base + uuid.Must(uuid.NewV7()).String(),
			getRuntime: func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Runtime, error) {
				return agent.Runtime{}, agent.ErrRuntimeNotFound
			},
			wantStatus: http.StatusNotFound,
			wantCode:   "runtime_not_found",
		},
		{
			name: "service failure",
			path: base + uuid.Must(uuid.NewV7()).String(),
			getRuntime: func(context.Context, auth.UserPrincipal, uuid.UUID) (agent.Runtime, error) {
				return agent.Runtime{}, errors.New("boom")
			},
			wantStatus: http.StatusInternalServerError,
			wantCode:   "internal_error",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := New(Handler{
				AuthService:    authenticatedAuthStub(userID),
				CompanyService: memberCompanyStub(t, userID, companyID),
				RuntimeService: runtimeServiceStub{getRuntime: test.getRuntime},
			})
			response := serveBearerRequest(handler, http.MethodGet, test.path, nil)
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
		})
	}
}

// The model-discovery routes are member-gated at the middleware (ADR 0013):
// whether the caller may use the machine is the service's decision, judged on
// the runtime's consent. The handler scopes the call exactly and refuses a
// non-member before any service call.
func TestRuntimeModelDiscoveryRoutesAreMemberGatedAndExactScoped(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	requestID := uuid.Must(uuid.NewV7())
	now := time.Date(2026, 8, 19, 13, 0, 0, 0, time.UTC)

	serviceCalls := 0
	service := runtimeServiceStub{
		initiate: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotRuntimeID uuid.UUID,
		) (agent.ModelDiscoveryRequest, error) {
			serviceCalls++
			if principal.CompanyID != companyID || principal.Role != auth.CompanyRoleMember ||
				gotRuntimeID != runtimeID {
				t.Fatalf("InitiateModelDiscovery() scope = %+v / %s", principal, gotRuntimeID)
			}
			return agent.ModelDiscoveryRequest{
				ID: requestID, RuntimeID: runtimeID, Status: agent.ModelDiscoveryPending,
				Models: []provideragent.RuntimeModel{}, CreatedAt: now, UpdatedAt: now,
			}, nil
		},
		get: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotRuntimeID uuid.UUID,
			gotRequestID uuid.UUID,
		) (agent.ModelDiscoveryRequest, error) {
			serviceCalls++
			if principal.CompanyID != companyID || principal.Role != auth.CompanyRoleMember ||
				gotRuntimeID != runtimeID || gotRequestID != requestID {
				t.Fatalf(
					"GetModelDiscovery() scope = %+v / %s / %s",
					principal,
					gotRuntimeID,
					gotRequestID,
				)
			}
			return agent.ModelDiscoveryRequest{
				ID: requestID, RuntimeID: runtimeID, Status: agent.ModelDiscoveryPending,
				Models: []provideragent.RuntimeModel{}, CreatedAt: now, UpdatedAt: now,
			}, nil
		},
	}
	memberHandler := New(Handler{
		AuthService: authenticatedAuthStub(
			userID,
		),
		CompanyService: memberCompanyStub(t, userID, companyID),
		RuntimeService: service,
	})
	base := "/api/v1/companies/" + companyID.String() + "/runtimes/" + runtimeID.String() + "/models"
	response := serveBearerRequest(memberHandler, http.MethodPost, base, nil)
	if response.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d, body = %s", response.Code, response.Body.String())
	}
	response = serveBearerRequest(memberHandler, http.MethodGet, base+"/"+requestID.String(), nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", response.Code, response.Body.String())
	}
	if serviceCalls != 2 {
		t.Fatalf("service calls = %d, want 2", serviceCalls)
	}

	outsiderHandler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{authorize: func(
			context.Context, auth.UserPrincipal, uuid.UUID,
		) (auth.UserPrincipal, error) {
			return auth.UserPrincipal{}, company.ErrNotFound
		}},
		RuntimeService: service,
	})
	response = serveBearerRequest(outsiderHandler, http.MethodPost, base, nil)
	assertErrorResponse(t, response, http.StatusNotFound, "company_not_found")
	if serviceCalls != 2 {
		t.Fatalf("service called for a non-member; calls = %d", serviceCalls)
	}
}

// The rename route is member-gated at the middleware — the machine's owner
// may be a plain member — and the owner-or-machine-owner decision belongs
// to the service. The handler's job is the boundary: bind the body, refuse
// what the binder can refuse before any service call, and echo the
// projection the service returns.
func TestRuntimeRenameRouteScopesAndBindsTheBody(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	path := "/api/v1/companies/" + companyID.String() + "/runtimes/" + runtimeID.String()
	now := time.Date(2026, 9, 9, 8, 0, 0, 0, time.UTC)
	newHandler := func(service runtimeServiceStub) http.Handler {
		return New(Handler{
			AuthService:    authenticatedAuthStub(userID),
			CompanyService: memberCompanyStub(t, userID, companyID),
			RuntimeService: service,
		})
	}
	renamed := func(customName *string) agent.Runtime {
		return agent.Runtime{
			ID: runtimeID, CompanyID: companyID, DaemonID: "daemon-a",
			Name: "Claude (alex-macbook)", CustomName: customName, Provider: "claude", Status: "online",
			CreatedAt: now, UpdatedAt: now,
		}
	}

	t.Run("a member's rename reaches the service and echoes the projection", func(t *testing.T) {
		t.Parallel()
		calls := 0
		handler := newHandler(runtimeServiceStub{rename: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotRuntimeID uuid.UUID,
			input agent.RuntimeRenameInput,
		) (agent.Runtime, error) {
			calls++
			if principal.UserID != userID || principal.CompanyID != companyID ||
				principal.Role != auth.CompanyRoleMember || gotRuntimeID != runtimeID ||
				input.CustomName != "Office Mac" || !input.ApplyToMachine {
				t.Fatalf(
					"Rename() principal/runtime/input = %+v / %s / %+v",
					principal,
					gotRuntimeID,
					input,
				)
			}
			customName := "Office Mac"
			return renamed(&customName), nil
		}})
		response := serveBearerJSON(handler, http.MethodPatch, path,
			[]byte(`{"customName":"Office Mac","applyToMachine":true}`))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if body["id"] != runtimeID.String() || body["name"] != "Claude (alex-macbook)" ||
			body["customName"] != "Office Mac" || calls != 1 {
			t.Fatalf("response = %s (service calls %d)", response.Body.String(), calls)
		}
	})

	t.Run("an empty name clears and applyToMachine defaults to false", func(t *testing.T) {
		t.Parallel()
		handler := newHandler(runtimeServiceStub{rename: func(
			_ context.Context,
			_ auth.UserPrincipal,
			_ uuid.UUID,
			input agent.RuntimeRenameInput,
		) (agent.Runtime, error) {
			if input.CustomName != "" || input.ApplyToMachine {
				t.Fatalf("Rename() input = %+v, want an empty name for one row", input)
			}
			return renamed(nil), nil
		}})
		response := serveBearerJSON(handler, http.MethodPatch, path, []byte(`{"customName":""}`))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"customName":null`) {
			t.Fatalf(
				"status = %d, body = %s; want 200 with customName null",
				response.Code,
				response.Body.String(),
			)
		}
	})

	t.Run("service refusals map through the runtime error table", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name       string
			err        error
			wantStatus int
			wantCode   string
		}{
			// Another member's machine is invisible to the caller (ADR 0014),
			// so it answers exactly like an unknown id.
			{
				"unknown, foreign or another member's",
				agent.ErrRuntimeNotFound,
				http.StatusNotFound,
				"runtime_not_found",
			},
			{
				"too long after trimming",
				agent.ErrRuntimeInvalidInput,
				http.StatusUnprocessableEntity,
				"validation_failed",
			},
			{"failure", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				t.Parallel()
				handler := newHandler(runtimeServiceStub{rename: func(
					context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRenameInput,
				) (agent.Runtime, error) {
					return agent.Runtime{}, test.err
				}})
				response := serveBearerJSON(
					handler,
					http.MethodPatch,
					path,
					[]byte(`{"customName":"Office Mac"}`),
				)
				assertErrorResponse(t, response, test.wantStatus, test.wantCode)
			})
		}
	})

	t.Run("bodies the binder refuses never reach the service", func(t *testing.T) {
		t.Parallel()
		tests := []struct {
			name       string
			path       string
			body       []byte
			wantStatus int
			wantCode   string
		}{
			{
				name: "customName missing", path: path,
				body: []byte(`{"applyToMachine":true}`), wantStatus: http.StatusUnprocessableEntity,
				wantCode: "validation_failed",
			},
			{
				name: "customName null", path: path,
				body: []byte(`{"customName":null}`), wantStatus: http.StatusUnprocessableEntity,
				wantCode: "validation_failed",
			},
			{
				name: "customName past 100 runes", path: path,
				body:       []byte(`{"customName":"` + strings.Repeat("a", 101) + `"}`),
				wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
			},
			{
				name: "unknown field",
				path: path,
				body: []byte(
					`{"customName":"Office Mac","revision":1}`,
				),
				wantStatus: http.StatusBadRequest,
				wantCode:   "invalid_request",
			},
			{
				name:       "malformed runtime id",
				path:       "/api/v1/companies/" + companyID.String() + "/runtimes/not-a-uuid",
				body:       []byte(`{"customName":"Office Mac"}`),
				wantStatus: http.StatusNotFound,
				wantCode:   "runtime_not_found",
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				t.Parallel()
				handler := newHandler(runtimeServiceStub{rename: func(
					context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRenameInput,
				) (agent.Runtime, error) {
					t.Fatal("RuntimeService.Rename() called for a body the binder refuses")
					return agent.Runtime{}, nil
				}})
				response := serveBearerJSON(handler, http.MethodPatch, test.path, test.body)
				assertErrorResponse(t, response, test.wantStatus, test.wantCode)
			})
		}
	})
}

func removalRefusingService(t *testing.T) runtimeServiceStub {
	t.Helper()
	return runtimeServiceStub{remove: func(
		context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRemovalInput,
	) (agent.RuntimeRemoval, error) {
		t.Fatal("RuntimeService.Remove() called for a request the boundary refuses")
		return agent.RuntimeRemoval{}, nil
	}}
}

// Both removal routes are member-gated (ADR 0014): a runtime is its
// registrant's alone, whatever their role, so a non-member never reaches
// the service and a plain member does.
func TestRuntimeRemoveRoutesAreMemberGated(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{authorize: func(
			context.Context, auth.UserPrincipal, uuid.UUID,
		) (auth.UserPrincipal, error) {
			return auth.UserPrincipal{}, company.ErrNotFound
		}},
		RuntimeService: removalRefusingService(t),
	})
	path := "/api/v1/companies/" + companyID.String() + "/runtimes/" + runtimeID.String()
	response := serveBearerRequest(handler, http.MethodDelete, path, nil)
	assertErrorResponse(t, response, http.StatusNotFound, "company_not_found")
	response = serveBearerJSON(handler, http.MethodPost, path+"/remove",
		[]byte(`{"targetRuntimeId":"`+uuid.Must(uuid.NewV7()).String()+`"}`))
	assertErrorResponse(t, response, http.StatusNotFound, "company_not_found")
}

// DELETE is the removal with no target: no body, an empty input, no content
// back; the plain member reaches the service with their own principal.
func TestDeleteRuntimeRouteForwardsAnEmptyInputAndAnswersNoContent(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	calls := 0
	handler := New(Handler{
		AuthService:    authenticatedAuthStub(userID),
		CompanyService: memberCompanyStub(t, userID, companyID),
		RuntimeService: runtimeServiceStub{remove: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotRuntimeID uuid.UUID,
			input agent.RuntimeRemovalInput,
		) (agent.RuntimeRemoval, error) {
			calls++
			if principal.UserID != userID || principal.CompanyID != companyID ||
				principal.Role != auth.CompanyRoleMember || gotRuntimeID != runtimeID ||
				input.TargetRuntimeID != nil {
				t.Fatalf(
					"Remove() principal/runtime/input = %+v / %s / %+v",
					principal,
					gotRuntimeID,
					input,
				)
			}
			return agent.RuntimeRemoval{RemovedRuntimeID: runtimeID}, nil
		}},
	})
	path := "/api/v1/companies/" + companyID.String() + "/runtimes/" + runtimeID.String()

	response := serveBearerRequest(handler, http.MethodDelete, path, nil)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("DELETE status = %d, body = %q; want 204 with no body", response.Code, response.Body.String())
	}
	if calls != 1 {
		t.Fatalf("service calls = %d, want 1", calls)
	}
	// A body is not a destination: the route refuses one that carries state.
	response = serveBearerJSON(handler, http.MethodDelete, path,
		[]byte(`{"targetRuntimeId":"`+uuid.Must(uuid.NewV7()).String()+`"}`))
	assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
	// A malformed id is answered like an unknown one, before any service call.
	response = serveBearerRequest(handler, http.MethodDelete,
		"/api/v1/companies/"+companyID.String()+"/runtimes/not-a-uuid", nil)
	assertErrorResponse(t, response, http.StatusNotFound, "runtime_not_found")
	if calls != 1 {
		t.Fatalf("service calls after refused requests = %d, want still 1", calls)
	}
}

// POST …/remove forwards the destination — a target runtime, or an explicit
// null that means the DELETE — and returns the receipt's four counts.
func TestRemoveRuntimeRouteForwardsTheTargetAndReturnsTheReceipt(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	target := uuid.Must(uuid.NewV7())
	var gotInputs []agent.RuntimeRemovalInput
	handler := New(Handler{
		AuthService:    authenticatedAuthStub(userID),
		CompanyService: memberCompanyStub(t, userID, companyID),
		RuntimeService: runtimeServiceStub{remove: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotRuntimeID uuid.UUID,
			input agent.RuntimeRemovalInput,
		) (agent.RuntimeRemoval, error) {
			if principal.UserID != userID || principal.CompanyID != companyID || gotRuntimeID != runtimeID {
				t.Fatalf("Remove() principal/runtime = %+v / %s", principal, gotRuntimeID)
			}
			gotInputs = append(gotInputs, input)
			return agent.RuntimeRemoval{
				RemovedRuntimeID:   runtimeID,
				MovedBindingCount:  2,
				RetargetedRunCount: 3,
				CancelledRunCount:  0,
			}, nil
		}},
	})
	path := "/api/v1/companies/" + companyID.String() + "/runtimes/" + runtimeID.String() + "/remove"

	response := serveBearerJSON(handler, http.MethodPost, path, []byte(`{"targetRuntimeId":"`+target.String()+`"}`))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		RemovedRuntimeID   string `json:"removedRuntimeId"`
		MovedBindingCount  int64  `json:"movedBindingCount"`
		RetargetedRunCount int64  `json:"retargetedRunCount"`
		CancelledRunCount  *int64 `json:"cancelledRunCount"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if body.RemovedRuntimeID != runtimeID.String() || body.MovedBindingCount != 2 ||
		body.RetargetedRunCount != 3 || body.CancelledRunCount == nil || *body.CancelledRunCount != 0 {
		t.Fatalf("receipt = %s, want every count present", response.Body.String())
	}
	response = serveBearerJSON(handler, http.MethodPost, path, []byte(`{"targetRuntimeId":null}`))
	if response.Code != http.StatusOK {
		t.Fatalf("null target status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(gotInputs) != 2 || gotInputs[0].TargetRuntimeID == nil || *gotInputs[0].TargetRuntimeID != target ||
		gotInputs[1].TargetRuntimeID != nil {
		t.Fatalf("Remove() inputs = %+v, want the target then nil", gotInputs)
	}
}

func TestRemoveRuntimeRouteRejectsMalformedBodies(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	target := uuid.Must(uuid.NewV7()).String()
	handler := New(Handler{
		AuthService:    authenticatedAuthStub(userID),
		CompanyService: memberCompanyStub(t, userID, companyID),
		RuntimeService: removalRefusingService(t),
	})
	path := "/api/v1/companies/" + companyID.String() + "/runtimes/" + runtimeID.String() + "/remove"

	// The three target refusals each say which rule the body broke, like
	// rename's "customName is required", instead of a bare validation
	// failure.
	tests := []struct {
		name       string
		path       string
		body       string
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{
			name:       "targetRuntimeId missing",
			path:       path,
			body:       `{}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "validation_failed",
			wantDetail: "targetRuntimeId is required: another of your runtimes to move your agents to," +
				" or null to remove the agents set up there",
		},
		{
			name: "a target that is not a uuid", path: path,
			body:       `{"targetRuntimeId":"not-a-uuid"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
			wantDetail: "targetRuntimeId must be a runtime id or null",
		},
		{
			name: "a target that is not a string", path: path,
			body:       `{"targetRuntimeId":7}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
			wantDetail: "targetRuntimeId must be a runtime id or null",
		},
		{
			name: "the target is the runtime being removed", path: path,
			body:       `{"targetRuntimeId":"` + runtimeID.String() + `"}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
			wantDetail: "targetRuntimeId must be another runtime, not the one being removed",
		},
		{
			name: "unknown field", path: path,
			body:       `{"targetRuntimeId":"` + target + `","expectedAgentIds":[]}`,
			wantStatus: http.StatusBadRequest, wantCode: "invalid_request",
		},
		{
			name:       "malformed runtime id",
			path:       "/api/v1/companies/" + companyID.String() + "/runtimes/not-a-uuid/remove",
			body:       `{"targetRuntimeId":"` + target + `"}`,
			wantStatus: http.StatusNotFound, wantCode: "runtime_not_found",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response := serveBearerJSON(handler, http.MethodPost, test.path, []byte(test.body))
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
			if test.wantDetail == "" {
				return
			}
			var envelope struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if envelope.Detail != test.wantDetail {
				t.Fatalf("detail = %q, want %q", envelope.Detail, test.wantDetail)
			}
		})
	}
	t.Run("a body without a JSON content type", func(t *testing.T) {
		t.Parallel()
		response := serveBearerRequest(handler, http.MethodPost, path,
			bytes.NewReader([]byte(`{"targetRuntimeId":"`+target+`"}`)))
		assertErrorResponse(t, response, http.StatusUnsupportedMediaType, "unsupported_media_type")
	})
}

// Service refusals map through the runtime error table on both routes.
func TestRemoveRuntimeRoutesMapDomainConflicts(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	target := uuid.Must(uuid.NewV7()).String()
	path := "/api/v1/companies/" + companyID.String() + "/runtimes/" + runtimeID.String()
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantDetail string
	}{
		{
			"online", agent.ErrRuntimeRemoveBlockedOnline, http.StatusConflict,
			"runtime_remove_blocked",
			"Quit Zenith Team on that computer first, or stop its zen daemon, then try again",
		},
		{
			"target of another provider",
			agent.ErrBindingProviderMismatch,
			http.StatusUnprocessableEntity,
			"agent_binding_provider_mismatch",
			"the target runtime runs a different provider than the agents bound here",
		},
		{
			"unknown, foreign, removed or another member's",
			agent.ErrRuntimeNotFound,
			http.StatusNotFound,
			"runtime_not_found",
			"Runtime not found",
		},
		{
			"failure",
			errors.New("boom"),
			http.StatusInternalServerError,
			"internal_error",
			"internal server error",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := New(Handler{
				AuthService:    authenticatedAuthStub(userID),
				CompanyService: memberCompanyStub(t, userID, companyID),
				RuntimeService: runtimeServiceStub{remove: func(
					context.Context, auth.UserPrincipal, uuid.UUID, agent.RuntimeRemovalInput,
				) (agent.RuntimeRemoval, error) {
					return agent.RuntimeRemoval{}, test.err
				}},
			})
			for _, request := range []struct {
				method string
				path   string
				body   []byte
			}{
				{http.MethodDelete, path, nil},
				{http.MethodPost, path + "/remove", []byte(`{"targetRuntimeId":"` + target + `"}`)},
			} {
				response := serveBearerRequest(handler, request.method, request.path, nil)
				if request.body != nil {
					response = serveBearerJSON(handler, request.method, request.path, request.body)
				}
				assertErrorResponse(t, response, test.wantStatus, test.wantCode)
				var envelope struct {
					Detail string `json:"detail"`
				}
				if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
					t.Fatalf("%s: decode error envelope: %v", request.method, err)
				}
				if envelope.Detail != test.wantDetail {
					t.Fatalf(
						"%s detail = %q, want %q",
						request.method,
						envelope.Detail,
						test.wantDetail,
					)
				}
			}
		})
	}
}

// The refresh route asks a machine to probe its provider setup again. It is
// member-gated — the registrant may be a plain member — and the runtime row
// decides inside the service, so the route proves exact company scope, the
// 202-with-no-body answer, and that every service verdict maps to its code.
func TestRuntimeRefreshRouteIsMemberGatedAndAnswers202(t *testing.T) {
	t.Parallel()
	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runtimeID := uuid.Must(uuid.NewV7())
	path := "/api/v1/companies/" + companyID.String() + "/runtimes/" + runtimeID.String() + "/refresh"

	serviceCalls := 0
	registrant := New(Handler{
		AuthService:    authenticatedAuthStub(userID),
		CompanyService: memberCompanyStub(t, userID, companyID),
		RuntimeService: runtimeServiceStub{
			refresh: func(_ context.Context, principal auth.UserPrincipal, gotRuntimeID uuid.UUID) error {
				serviceCalls++
				if principal.CompanyID != companyID || principal.UserID != userID ||
					gotRuntimeID != runtimeID {
					t.Fatalf("RequestRefresh() scope = %+v / %s", principal, gotRuntimeID)
				}
				return nil
			},
		},
	})
	response := serveBearerRequest(registrant, http.MethodPost, path, nil)
	if response.Code != http.StatusAccepted || response.Body.Len() != 0 || serviceCalls != 1 {
		t.Fatalf("registrant refresh = %d %q (calls %d), want 202 with no body after one service call",
			response.Code, response.Body.String(), serviceCalls)
	}

	verdicts := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"offline", agent.ErrRuntimeOffline, http.StatusConflict, "runtime_offline"},
		// Another member's machine, like an unknown one, is invisible.
		{"hidden or another member's", agent.ErrRuntimeNotFound, http.StatusNotFound, "runtime_not_found"},
	}
	for _, verdict := range verdicts {
		t.Run(verdict.name, func(t *testing.T) {
			t.Parallel()
			handler := New(Handler{
				AuthService:    authenticatedAuthStub(userID),
				CompanyService: memberCompanyStub(t, userID, companyID),
				RuntimeService: runtimeServiceStub{
					refresh: func(context.Context, auth.UserPrincipal, uuid.UUID) error { return verdict.err },
				},
			})
			assertErrorResponse(t, serveBearerRequest(handler, http.MethodPost, path, nil),
				verdict.wantStatus, verdict.wantCode)
		})
	}
}

func (runtimeServiceStub) ReportTools(context.Context, auth.UserPrincipal, agent.ToolObservationReport) error {
	return nil
}
