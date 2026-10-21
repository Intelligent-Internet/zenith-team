package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
)

func TestMeProfileAvatarIsOptionalAndBoundToCaller(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, field string
		wantStatus  int
		wantAvatar  bool
	}{
		{"omitted", "", http.StatusOK, false},
		{"image", `,"avatarDataUrl":"data:image/png;base64,aGVsbG8="`, http.StatusOK, true},
		{"null", `,"avatarDataUrl":null`, http.StatusBadRequest, false},
		{"number", `,"avatarDataUrl":42`, http.StatusBadRequest, false},
		{"large body", `,"avatarDataUrl":"` + strings.Repeat("A", 145<<10) + `"`, http.StatusRequestEntityTooLarge, false},
		{"another user", `,"userId":"another-user"`, http.StatusBadRequest, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			userID := uuid.New()
			service := authenticatedAuthStub(userID)
			calls := 0
			service.updateProfile = func(_ context.Context, gotID uuid.UUID, input auth.UpdateProfileInput) (auth.User, error) {
				calls++
				if gotID != userID || (input.AvatarDataURL != nil) != test.wantAvatar {
					t.Fatal("avatar write lost caller or optional field")
				}
				return auth.User{ID: userID, AvatarURL: input.AvatarDataURL}, nil
			}
			body := `{"expectedRevision":1,"displayName":"Member","biography":"","phoneCountryIso2":null,"phoneNumber":null` + test.field + `}`
			response := serveBearerJSON(
				New(Handler{AuthService: service}),
				http.MethodPatch,
				"/api/v1/me/profile",
				[]byte(body),
			)
			if response.Code != test.wantStatus {
				t.Fatalf(
					"status = %d, want %d: %s",
					response.Code,
					test.wantStatus,
					response.Body.String(),
				)
			}
			if test.wantStatus != http.StatusOK && calls != 0 {
				t.Fatal("invalid request reached service")
			}
		})
	}
}

func TestMeProfileRouteReturnsAcknowledgedUser(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2d")
	createdAt := time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	service := authenticatedAuthStub(userID)
	service.updateProfile = func(_ context.Context, gotUserID uuid.UUID, input auth.UpdateProfileInput) (auth.User, error) {
		if gotUserID != userID || input.ExpectedRevision != 3 ||
			input.DisplayName != "Sam Lee" || input.Biography != "Preferred context" ||
			input.PhoneCountryISO2 == nil || *input.PhoneCountryISO2 != "VN" ||
			input.PhoneNumber == nil || *input.PhoneNumber != "912345678" {
			t.Errorf("UpdateProfile(%s, %#v)", gotUserID, input)
		}
		return auth.User{
			ID: userID, Email: "founder@zenith-team.test", DisplayName: "Sam Lee",
			Biography:        "Preferred context",
			PhoneCountryISO2: stringPointer("VN"), PhoneNumber: stringPointer("912345678"),
			CreatedAt: createdAt, UpdatedAt: updatedAt, Version: 4,
		}, nil
	}
	response := serveBearerJSON(
		New(Handler{AuthService: service}),
		http.MethodPatch,
		"/api/v1/me/profile",
		[]byte(
			`{"expectedRevision":3,"displayName":"Sam Lee","biography":"Preferred context","phoneCountryIso2":"VN","phoneNumber":"912345678"}`,
		),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := map[string]any{
		"id":               userID.String(),
		"email":            "founder@zenith-team.test",
		"emailVerifiedAt":  nil,
		"hasPassword":      false,
		"linkedProviders":  []any{},
		"displayName":      "Sam Lee",
		"avatarUrl":        nil,
		"biography":        "Preferred context",
		"phoneCountryIso2": "VN",
		"phoneNumber":      "912345678",
		"revision":         float64(4),
		"createdAt":        "2026-07-14T09:00:00Z",
		"updatedAt":        "2026-07-14T09:01:00Z",
	}
	if !reflect.DeepEqual(body, want) {
		t.Fatalf("response = %#v, want %#v", body, want)
	}
}

func TestMeRouteReturnsTheBareUser(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2d")
	now := time.Date(2026, time.July, 14, 9, 0, 0, 0, time.UTC)
	service := authenticatedAuthStub(userID)
	service.currentUser = func(_ context.Context, gotUserID uuid.UUID) (auth.User, error) {
		if gotUserID != userID {
			t.Fatalf("CurrentUser user ID = %s", gotUserID)
		}
		return auth.User{
			ID: userID, Email: "founder@zenith-team.test", DisplayName: "Founder",
			CreatedAt: now, UpdatedAt: now, Version: 3,
		}, nil
	}

	response := serveBearerRequest(New(Handler{AuthService: service}), http.MethodGet, "/api/v1/me", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["id"] != userID.String() || body["displayName"] != "Founder" {
		t.Fatalf("response = %#v, want bare User", body)
	}
	if _, wrapped := body["user"]; wrapped {
		t.Fatalf("response = %#v, want no user wrapper", body)
	}
}

func TestMeProfileRouteReturnsCurrentProjectionOnConflict(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Second)
	service := authenticatedAuthStub(userID)
	service.updateProfile = func(context.Context, uuid.UUID, auth.UpdateProfileInput) (auth.User, error) {
		return auth.User{}, &auth.ProfileRevisionConflictError{
			Current: auth.User{
				ID: userID, Email: "founder@zenith-team.test", DisplayName: "Latest",
				Biography:        "Latest biography",
				PhoneCountryISO2: stringPointer("US"), PhoneNumber: stringPointer("4155552671"),
				CreatedAt: now, UpdatedAt: now, Version: 7,
			},
		}
	}
	response := serveBearerJSON(
		New(Handler{AuthService: service}),
		http.MethodPatch,
		"/api/v1/me/profile",
		[]byte(
			`{"expectedRevision":3,"displayName":"Draft","biography":"Draft biography","phoneCountryIso2":"VN","phoneNumber":"912345678"}`,
		),
	)
	var body struct {
		Code        string       `json:"code"`
		Detail      string       `json:"detail"`
		RequestID   string       `json:"requestId"`
		CurrentUser userResponse `json:"currentUser"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode conflict response: %v", err)
	}
	if response.Code != http.StatusConflict || body.Code != "profile_revision_conflict" ||
		body.Detail == "" || body.RequestID == "" ||
		body.CurrentUser.ID != userID || body.CurrentUser.DisplayName != "Latest" ||
		body.CurrentUser.Biography != "Latest biography" ||
		body.CurrentUser.PhoneCountryISO2 == nil || *body.CurrentUser.PhoneCountryISO2 != "US" ||
		body.CurrentUser.PhoneNumber == nil || *body.CurrentUser.PhoneNumber != "4155552671" ||
		body.CurrentUser.Revision != 7 {
		t.Fatalf("conflict response = %+v", body)
	}
}

func TestMeProfileRouteRejectsUnauthorizedMalformedAndInvalidRequests(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	unauthorized := &authServiceStub{
		authenticate: func(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error) {
			return auth.UserPrincipal{}, auth.ErrInvalidSession
		},
	}
	response := serveBearerJSON(
		New(Handler{AuthService: unauthorized}),
		http.MethodPatch,
		"/api/v1/me/profile",
		[]byte(
			`{"expectedRevision":1,"displayName":"Sam","biography":"","phoneCountryIso2":null,"phoneNumber":null}`,
		),
	)
	assertErrorResponse(t, response, http.StatusUnauthorized, "session_invalid")

	service := authenticatedAuthStub(userID)
	calls := 0
	service.updateProfile = func(context.Context, uuid.UUID, auth.UpdateProfileInput) (auth.User, error) {
		calls++
		return auth.User{}, auth.ErrInvalidInput
	}
	handler := New(Handler{AuthService: service})
	for _, body := range []string{
		`{"expectedRevision":1,"displayName":"Sam","biography":"","phoneCountryIso2":null,"phoneNumber":null,"phoneExtension":"123"}`,
		`{"expectedRevision":1,"displayName":"Sam","phoneCountryIso2":null,"phoneNumber":null}`,
		`{"expectedRevision":1,"displayName":"Sam","biography":""}`,
		`{"expectedRevision":1,"expectedRevision":1,"displayName":"Sam","biography":""}`,
		`{"expectedRevision":"one","displayName":"Sam","biography":""}`,
	} {
		response = serveBearerJSON(handler, http.MethodPatch, "/api/v1/me/profile", []byte(body))
		assertErrorResponse(t, response, http.StatusBadRequest, "invalid_request")
	}
	if calls != 0 {
		t.Fatalf("malformed requests reached service %d times", calls)
	}

	response = serveBearerJSON(
		handler,
		http.MethodPatch,
		"/api/v1/me/profile",
		[]byte(
			`{"expectedRevision":1,"displayName":" ","biography":"","phoneCountryIso2":null,"phoneNumber":null}`,
		),
	)
	assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	if calls != 1 {
		t.Fatalf("validly-shaped request calls = %d, want 1", calls)
	}
}

func TestMeProfileCookieMutationRequiresBoundCSRF(t *testing.T) {
	t.Parallel()

	protector, err := auth.NewCSRF([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("NewCSRF() error = %v", err)
	}
	const accessToken = "web-access"
	userID := uuid.Must(uuid.NewV7())
	service := &authServiceStub{
		authenticate: func(_ context.Context, token string, kind auth.TokenKind) (auth.UserPrincipal, error) {
			if token != accessToken || kind != auth.TokenKindSession {
				t.Errorf("Authenticate(%q, %q)", token, kind)
			}
			return auth.UserPrincipal{
				UserID:    userID,
				SessionID: uuid.Must(uuid.NewV7()),
				AuthKind:  kind,
			}, nil
		},
		updateProfile: func(context.Context, uuid.UUID, auth.UpdateProfileInput) (auth.User, error) {
			t.Fatal("UpdateProfile ran without a valid CSRF token")
			return auth.User{}, nil
		},
	}
	request := httptest.NewRequest(
		http.MethodPatch,
		"/api/v1/me/profile",
		strings.NewReader(
			`{"expectedRevision":1,"displayName":"Sam","biography":"","phoneCountryIso2":null,"phoneNumber":null}`,
		),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://app.zenith-team.test")
	request.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: accessToken})
	response := httptest.NewRecorder()
	New(Handler{
		AuthService: service, CSRF: protector, AllowedOrigins: []string{"https://app.zenith-team.test"},
	}).ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusForbidden, "csrf_invalid")
}

func stringPointer(value string) *string {
	return &value
}
