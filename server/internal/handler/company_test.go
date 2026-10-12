package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
)

func TestCompanyRoutesUseSlimPublicContract(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	wantCompany := company.Response{
		ID: companyID, Name: "Northwind Robotics", Slug: "northwind-robotics",
		TaskPrefix: "NWR", BusinessContext: "Warehouse automation",
		Role: auth.CompanyRoleOwner, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	service := &companyServiceStub{
		create: func(_ context.Context, principal auth.UserPrincipal, input company.CreateCompanyInput) (company.Response, error) {
			if principal.UserID != userID || input.BusinessContext != "Warehouse automation" {
				t.Fatalf("Create input = %+v / %+v", principal, input)
			}
			return wantCompany, nil
		},
		list: func(_ context.Context, principal auth.UserPrincipal) ([]company.Response, error) {
			if principal.UserID != userID {
				t.Fatalf("List principal = %+v", principal)
			}
			return []company.Response{wantCompany}, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})

	created := serveBearerJSON(handler, http.MethodPost, "/api/v1/companies", []byte(`{
		"name":"Northwind Robotics",
		"slug":"northwind-robotics",
		"taskPrefix":"NWR",
		"businessContext":"Warehouse automation"
	}`))
	if created.Code != http.StatusCreated {
		t.Fatalf(
			"POST Company status = %d, want %d; body = %s",
			created.Code,
			http.StatusCreated,
			created.Body.String(),
		)
	}
	assertCompanyResponseKeys(t, created.Body.Bytes())

	listed := serveBearerRequest(handler, http.MethodGet, "/api/v1/companies", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf(
			"GET Companies status = %d, want %d; body = %s",
			listed.Code,
			http.StatusOK,
			listed.Body.String(),
		)
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode Company page: %v", err)
	}
	if _, ok := page["items"]; !ok || len(page) != 1 {
		t.Fatalf("Company collection = %s, want only items", listed.Body.String())
	}
	for _, query := range []string{"?limit=1", "?cursor=retired", "?unknown=true"} {
		response := serveBearerRequest(handler, http.MethodGet, "/api/v1/companies"+query, nil)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
}

// TestCompanyReadRejectsAnyQueryParameter pins the read to the convention
// every sibling already follows: the route takes no query string, so an
// unknown, repeated or empty parameter is 422 validation_failed before the
// service is reached — never silently ignored.
func TestCompanyReadRejectsAnyQueryParameter(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	wantCompany := company.Response{
		ID: companyID, Name: "Northwind Robotics", Slug: "northwind-robotics",
		TaskPrefix: "NWR", Role: auth.CompanyRoleOwner, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	serviceCalls := 0
	service := &companyServiceStub{
		get: func(_ context.Context, principal auth.UserPrincipal, gotCompanyID uuid.UUID) (company.Response, error) {
			serviceCalls++
			if principal.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("Get input = %+v / %s", principal, gotCompanyID)
			}
			return wantCompany, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})
	path := "/api/v1/companies/" + companyID.String()

	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET Company = %d %s", response.Code, response.Body.String())
	}
	assertCompanyResponseKeys(t, response.Body.Bytes())
	if serviceCalls != 1 {
		t.Fatalf("Get calls = %d, want 1", serviceCalls)
	}

	for _, query := range []string{"?unknown=true", "?x=", "?limit=1&limit=2"} {
		got := serveBearerRequest(handler, http.MethodGet, path+query, nil)
		assertErrorResponse(t, got, http.StatusUnprocessableEntity, "validation_failed")
	}
	if serviceCalls != 1 {
		t.Fatalf("Get calls after rejected queries = %d, want 1", serviceCalls)
	}
}

func TestDeleteCompanyUsesOwnerScopeAndRevision(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: userID,
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	deleteCalls := 0
	service := &companyServiceStub{
		requirePermission: func(_ context.Context, got auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
			if got.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("RequireOwner input = %+v / %s", got, gotCompanyID)
			}
			return principal, nil
		},
		deleteCompany: func(_ context.Context, got auth.UserPrincipal, expectedRevision int64) error {
			deleteCalls++
			if got != principal || expectedRevision != 7 {
				t.Fatalf("Delete input = %+v / %d", got, expectedRevision)
			}
			return nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})
	path := "/api/v1/companies/" + companyID.String()

	response := serveBearerJSON(
		handler,
		http.MethodDelete,
		path,
		[]byte(`{"expectedRevision":7}`),
	)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("DELETE Company = %d %s, want empty 204", response.Code, response.Body.String())
	}
	if deleteCalls != 1 {
		t.Fatalf("Delete calls = %d, want 1", deleteCalls)
	}

	invalid := serveBearerJSON(
		handler,
		http.MethodDelete,
		path,
		[]byte(`{"expectedRevision":0}`),
	)
	assertErrorResponse(t, invalid, http.StatusUnprocessableEntity, "validation_failed")
	if deleteCalls != 1 {
		t.Fatalf("Delete calls after invalid revision = %d, want 1", deleteCalls)
	}
}

func TestCompanyMembersReturnCompleteItemsCollection(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: userID,
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	service := &companyServiceStub{
		authorize: func(_ context.Context, got auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
			if got.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("RequireOwner input = %+v / %s", got, gotCompanyID)
			}
			return principal, nil
		},
		listMembers: func(_ context.Context, got auth.UserPrincipal) ([]company.ActiveMemberResponse, error) {
			if got != principal {
				t.Fatalf("ListMembers principal = %+v, want %+v", got, principal)
			}
			return []company.ActiveMemberResponse{{
				CompanyID: companyID,
				Actor: company.ActorRef{
					ID: userID, Kind: company.ActorTypeUser, DisplayName: "Owner",
				},
				Email:     "owner@example.com",
				Role:      auth.CompanyRoleOwner,
				CreatedAt: time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC),
				Revision:  1,
			}}, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})
	path := "/api/v1/companies/" + companyID.String() + "/members"

	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET Company members = %d %s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode Company members: %v", err)
	}
	if len(body) != 1 || string(body["items"]) == "null" {
		t.Fatalf("Company member collection = %s, want non-null items only", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "userId") ||
		!strings.Contains(response.Body.String(), `"actor":{"id":"`+userID.String()+`"`) ||
		!strings.Contains(response.Body.String(), `"email":"owner@example.com"`) ||
		!strings.Contains(response.Body.String(), `"accountDisabled":false`) {
		t.Fatalf("Company member projection = %s", response.Body.String())
	}
	for _, query := range []string{"?limit=1", "?cursor=retired"} {
		got := serveBearerRequest(handler, http.MethodGet, path+query, nil)
		assertErrorResponse(t, got, http.StatusUnprocessableEntity, "validation_failed")
	}
}

func TestCreateCompanyRejectsRetiredOnboardingFields(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	service := &companyServiceStub{
		create: func(context.Context, auth.UserPrincipal, company.CreateCompanyInput) (company.Response, error) {
			t.Fatal("Create called for a request with a retired field")
			return company.Response{}, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})
	valid := `{
		"name":"Northwind Robotics",
		"slug":"northwind-robotics",
		"taskPrefix":"NWR",
		"businessContext":""
	}`
	for name, field := range map[string]string{
		"client-supplied company id": `"id":"` + uuid.Must(uuid.NewV7()).String() + `"`,
		"retired first goal":         `"firstGoal":"launch_mvp"`,
	} {
		t.Run(name, func(t *testing.T) {
			body := []byte("{" + field + strings.TrimPrefix(valid, "{"))
			response := serveBearerJSON(handler, http.MethodPost, "/api/v1/companies", body)
			assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
		})
	}
}

func TestCompanyStatisticsRouteReturnsFlatMemberScopedCounts(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: userID,
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	service := &companyServiceStub{
		authorize: func(_ context.Context, got auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
			if got.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("Authorize input = %+v / %s", got, gotCompanyID)
			}
			return principal, nil
		},
		statistics: func(_ context.Context, got auth.UserPrincipal) (company.Statistics, error) {
			if got != principal {
				t.Fatalf("Statistics principal = %+v, want %+v", got, principal)
			}
			return company.Statistics{MemberCount: 4, RuntimeCount: 3, RuntimeOnlineCount: 2}, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})

	response := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/statistics", nil)
	if response.Code != http.StatusOK ||
		response.Body.String() != "{\"memberCount\":4,\"runtimeCount\":3,\"runtimeOnlineCount\":2}" {
		t.Fatalf("GET Company statistics = %d %s", response.Code, response.Body.String())
	}
}

func TestCompanyBootstrapIsOwnerOnlyAndReturnsExplicitCollections(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	principal := auth.UserPrincipal{
		UserID:    userID,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	service := &companyServiceStub{
		requirePermission: func(_ context.Context, got auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
			if got.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("RequireOwner() = %+v / %s", got, gotCompanyID)
			}
			return principal, nil
		},
		bootstrap: func(_ context.Context, got auth.UserPrincipal) (company.Bootstrap, error) {
			if got != principal {
				t.Fatalf("Bootstrap() principal = %+v, want %+v", got, principal)
			}
			return company.Bootstrap{
				Company: company.Response{ID: companyID, Role: auth.CompanyRoleOwner},
			}, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})
	path := "/api/v1/companies/" + companyID.String() + "/bootstrap"
	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET bootstrap = %d %s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	if _, hasDepartments := body["departments"]; hasDepartments ||
		string(body["activeTaskCount"]) != "0" || string(body["queuedRunCount"]) != "0" ||
		string(body["activeRunCount"]) != "0" {
		t.Fatalf("bootstrap body = %s", response.Body.String())
	}
	for _, query := range []string{"?unknown=true", "?limit=1"} {
		got := serveBearerRequest(handler, http.MethodGet, path+query, nil)
		assertErrorResponse(t, got, http.StatusUnprocessableEntity, "validation_failed")
	}

	service.requirePermission = func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
		return auth.UserPrincipal{}, company.ErrForbidden
	}
	denied := serveBearerRequest(handler, http.MethodGet, path, nil)
	assertErrorResponse(t, denied, http.StatusForbidden, "company_permission_required")
}

func TestCompanyProfileUsesRevisionAndFlatWriteReceipt(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: userID,
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	now := time.Date(2026, time.August, 17, 8, 0, 0, 0, time.UTC)
	updated := company.Response{
		ID: companyID, Name: "Northwind", Slug: "northwind", TaskPrefix: "NWR",
		BusinessContext: "", Role: auth.CompanyRoleOwner,
		Revision: 2, CreatedAt: now, UpdatedAt: now,
	}
	service := &companyServiceStub{
		requirePermission: func(_ context.Context, got auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
			if got.UserID != userID || gotCompanyID != companyID {
				t.Fatalf("RequireOwner input = %+v / %s", got, gotCompanyID)
			}
			return principal, nil
		},
		updateProfile: func(_ context.Context, got auth.UserPrincipal, input company.UpdateProfileInput) (company.Response, error) {
			if got != principal || input.ExpectedRevision != 1 || input.Name != "Northwind" {
				t.Fatalf("UpdateProfile input = %+v / %+v", got, input)
			}
			return updated, nil
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})

	response := serveBearerJSON(
		handler,
		http.MethodPatch,
		"/api/v1/companies/"+companyID.String()+"/profile",
		[]byte(
			`{"expectedRevision":1,"name":"Northwind","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
		),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("PATCH Company profile = %d %s", response.Code, response.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode Company profile response: %v", err)
	}
	if _, wrapped := body["company"]; wrapped || len(body) != 17 {
		t.Fatalf("Company profile response = %s, want bare Company", response.Body.String())
	}
	if _, hasChanges := body["changes"]; hasChanges {
		t.Fatalf("Company profile response leaked internal changes = %s", response.Body.String())
	}
}

func TestCompanyProfileConflictUsesRevisionCodeAndCurrentCompany(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: userID,
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	current := company.Response{
		ID: companyID, Name: "Current", Slug: "current", TaskPrefix: "CUR",
		Role: auth.CompanyRoleOwner, Revision: 2,
	}
	service := &companyServiceStub{
		requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
			return principal, nil
		},
		updateProfile: func(context.Context, auth.UserPrincipal, company.UpdateProfileInput) (company.Response, error) {
			// Wrapped like the real service, whose transaction helper wraps
			// closure errors; the handler must unwrap with errors.As.
			return company.Response{}, fmt.Errorf(
				"run database transaction: %w",
				&company.ProfileRevisionConflictError{Current: current},
			)
		},
	}
	handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})

	response := serveBearerJSON(
		handler,
		http.MethodPatch,
		"/api/v1/companies/"+companyID.String()+"/profile",
		[]byte(
			`{"expectedRevision":1,"name":"Current","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
		),
	)
	if response.Code != http.StatusConflict {
		t.Fatalf("PATCH Company profile conflict = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Code           string           `json:"code"`
		CurrentCompany company.Response `json:"currentCompany"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode Company profile conflict: %v", err)
	}
	if body.Code != "company_profile_revision_conflict" ||
		body.CurrentCompany.ID != companyID || body.CurrentCompany.Revision != 2 {
		t.Fatalf("Company profile conflict = %+v", body)
	}
}

// TestCompanyProfileRejectsAnUnusableRevisionBeforeTheService pins the guards
// the binding tag now owns.
//
// domainInput dereferences ExpectedRevision without a nil check, which is only
// safe because `binding:"required"` rejects the absent key and the optional
// decoder rejects an explicit null. Both are one edit away from being lost, and
// losing either turns a 400 into a nil-pointer panic.
//
// The range is the tag's now too: `revision` reads the same bound off
// protocol.MaxJSONSafeInteger that company.normalizeCompanyProfileInput used
// to restate, and that guard is gone. The stub fails on contact, so this is
// also what proves the boundary answers before the service is reached.
func TestCompanyProfileRejectsAnUnusableRevisionBeforeTheService(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: userID,
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}

	for name, test := range map[string]struct {
		body string
		want int
	}{
		// An absent key is a field rule that did not hold, which is 422 like
		// every other one. An explicit null is a malformed statement about the
		// field rather than an absent one, and the decoder refuses it as 400.
		"absent": {
			body: `{"name":"Northwind","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
			want: http.StatusUnprocessableEntity,
		},
		"null": {
			body: `{"expectedRevision":null,"name":"Northwind","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
			want: http.StatusBadRequest,
		},
		// A revision is a positive integer a JSON client round-trips exactly.
		// Zero would match a Company nobody has written yet; anything past
		// 2^53-1 has already lost digits on the way in.
		"zero": {
			body: `{"expectedRevision":0,"name":"Northwind","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
			want: http.StatusUnprocessableEntity,
		},
		"past the JSON-safe ceiling": {
			body: `{"expectedRevision":9007199254740992,"name":"Northwind","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
			want: http.StatusUnprocessableEntity,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			service := &companyServiceStub{
				requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
					return principal, nil
				},
				updateProfile: func(
					_ context.Context, _ auth.UserPrincipal, input company.UpdateProfileInput,
				) (company.Response, error) {
					t.Fatalf("a body with no usable revision reached the service as %+v", input)
					return company.Response{}, nil
				},
			}
			handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})

			response := serveBearerJSON(handler, http.MethodPatch,
				"/api/v1/companies/"+companyID.String()+"/profile", []byte(test.body))
			if response.Code != test.want {
				t.Fatalf("PATCH Company profile = %d %s, want %d",
					response.Code, response.Body.String(), test.want)
			}
		})
	}
}

// TestCompanyProfileRejectsAMissingOrMistypedFieldBeforeTheService pins the
// documented answer for the four profile fields: despite the PATCH verb the
// write replaces the profile, so every field must be present, and an absent
// or wrongly-typed one is 422 validation_failed naming the field — the same
// answer `expectedRevision is required` gets, not this route's old private
// 400. Malformed JSON and unknown fields stay 400 invalid_request.
func TestCompanyProfileRejectsAMissingOrMistypedFieldBeforeTheService(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	companyID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: userID,
		AuthKind: auth.TokenKindBearer, CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}

	for name, test := range map[string]struct {
		body   string
		status int
		code   string
		detail string
	}{
		"contactEmail absent": {
			body:   `{"expectedRevision":1,"name":"Northwind","phoneCountryIso2":null,"phoneNumber":null}`,
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
			detail: "contactEmail is required",
		},
		"name absent": {
			body:   `{"expectedRevision":1,"contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
			detail: "name is required",
		},
		"name not a string": {
			body:   `{"expectedRevision":1,"name":42,"contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
			detail: "name must be a string",
		},
		"name null": {
			body:   `{"expectedRevision":1,"name":null,"contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null}`,
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
			detail: "name must be a string",
		},
		"phoneNumber not a string or null": {
			body:   `{"expectedRevision":1,"name":"Northwind","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":4155550142}`,
			status: http.StatusUnprocessableEntity,
			code:   "validation_failed",
			detail: "phoneNumber must be a string or null",
		},
		"unknown field": {
			body:   `{"expectedRevision":1,"name":"Northwind","contactEmail":null,"phoneCountryIso2":null,"phoneNumber":null,"slug":"x"}`,
			status: http.StatusBadRequest,
			code:   "invalid_request",
			detail: "request body is invalid",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			service := &companyServiceStub{
				requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
					return principal, nil
				},
				updateProfile: func(
					_ context.Context, _ auth.UserPrincipal, input company.UpdateProfileInput,
				) (company.Response, error) {
					t.Fatalf("a body with a missing or mistyped field reached the service as %+v",
						input)
					return company.Response{}, nil
				},
			}
			handler := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})

			response := serveBearerJSON(handler, http.MethodPatch,
				"/api/v1/companies/"+companyID.String()+"/profile", []byte(test.body))
			assertErrorResponse(t, response, test.status, test.code)
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode error response: %v", err)
			}
			if body["detail"] != test.detail {
				t.Fatalf("PATCH Company profile detail = %q, want %q", body["detail"], test.detail)
			}
		})
	}
}

func assertCompanyResponseKeys(t *testing.T, raw []byte) {
	t.Helper()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode Company response: %v", err)
	}
	want := map[string]bool{
		"id": true, "name": true, "slug": true, "taskPrefix": true, "avatarAttachmentId": true,
		"businessContext": true, "websiteUrl": true, "contactEmail": true, "phoneCountryIso2": true,
		"phoneNumber": true, "role": true, "permissions": true, "revision": true,
		"createdAt": true, "updatedAt": true,
		"agentSetupCompletedAt": true, "firstPlanAppliedAt": true,
	}
	if len(body) != len(want) {
		t.Fatalf("Company response = %s, want %d fields", raw, len(want))
	}
	for key := range body {
		if !want[key] {
			t.Fatalf("Company response has unexpected field %q: %s", key, raw)
		}
	}
}

func TestMembershipMutationRoutesForwardAuthorizedPrincipal(t *testing.T) {
	t.Parallel()
	for _, leave := range []bool{false, true} {
		name := "remove"
		if leave {
			name = "leave"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			userID, companyID, targetID := uuid.New(), uuid.New(), uuid.New()
			principal := auth.UserPrincipal{
				UserID:    userID,
				CompanyID: companyID,
				Role:      auth.CompanyRoleOwner,
			}
			called := false
			authorize := func(_ context.Context, got auth.UserPrincipal, id uuid.UUID) (auth.UserPrincipal, error) {
				if got.UserID != userID || id != companyID {
					t.Fatalf("wrong authorization: %+v %s", got, id)
				}
				return principal, nil
			}
			service := &companyServiceStub{
				authorize:         authorize,
				requirePermission: authorize,
				removeMember: func(_ context.Context, got auth.UserPrincipal, input company.MembershipMutation) error {
					called = true
					if got.UserID != principal.UserID || got.CompanyID != principal.CompanyID ||
						got.Role != principal.Role ||
						input.UserID != targetID ||
						input.ExpectedRevision != 7 {
						t.Fatalf("remove: %+v %+v", got, input)
					}
					return nil
				},
				leave: func(_ context.Context, got auth.UserPrincipal, revision int64) error {
					called = true
					if got.UserID != principal.UserID || got.CompanyID != principal.CompanyID ||
						got.Role != principal.Role ||
						revision != 7 {
						t.Fatalf("leave: %+v %d", got, revision)
					}
					return nil
				},
			}
			h := New(Handler{AuthService: authenticatedAuthStub(userID), CompanyService: service})
			method, path := http.MethodDelete, "/api/v1/companies/"+companyID.String()+"/members/"+targetID.String()
			if leave {
				method, path = http.MethodPost, "/api/v1/companies/"+companyID.String()+"/leave"
			}
			for _, body := range []string{`{}`, `{"expectedRevision":0}`} {
				response := serveBearerJSON(h, method, path, []byte(body))
				assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
				if called {
					t.Fatal("invalid request reached service")
				}
			}
			unknown := serveBearerJSON(h, method, path, []byte(`{"expectedRevision":7,"role":"owner"}`))
			assertErrorResponse(t, unknown, http.StatusBadRequest, "invalid_request")
			if called {
				t.Fatal("unknown field reached service")
			}
			response := serveBearerJSON(h, method, path, []byte(`{"expectedRevision":7}`))
			if response.Code != http.StatusNoContent || !called {
				t.Fatalf("status=%d called=%t body=%s", response.Code, called, response.Body.String())
			}
		})
	}
}
