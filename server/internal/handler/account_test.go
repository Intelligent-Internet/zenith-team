package handler

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/account"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
)

func TestDeleteAccountRequiresExactConfirmationAndClearsWebCredentials(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	sessionID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19")
	service := &accountServiceStub{
		delete: func(_ context.Context, principal auth.UserPrincipal, confirmation string) (account.DeleteResult, error) {
			if principal.UserID != userID || principal.SessionID != sessionID ||
				principal.AuthKind != auth.TokenKindSession {
				t.Fatalf("Delete() principal = %#v", principal)
			}
			if confirmation != account.Confirmation {
				t.Fatalf("Delete() confirmation = %q", confirmation)
			}
			return account.DeleteResult{}, nil
		},
	}
	authService := authenticatedAuthStub(userID)
	protector, err := auth.NewCSRF([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("NewCSRF() error = %v", err)
	}
	accessToken := "signed-web-access-token"
	csrfToken := protector.Token(accessToken)
	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/me",
		bytes.NewBufferString(`{"confirmation":"DELETE"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(middleware.CSRFHeaderName, csrfToken)
	request.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: accessToken})
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: csrfToken})
	response := httptest.NewRecorder()

	New(Handler{
		AuthService:    authService,
		AccountService: service,
		CSRF:           protector,
		AuthCookies:    CookieOptions{Secure: true},
	}).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status/body = %d / %q", response.Code, response.Body.String())
	}
	setCookies := response.Header().Values("Set-Cookie")
	for _, name := range []string{middleware.AuthCookieName, middleware.RefreshCookieName, middleware.CSRFCookieName, middleware.RefreshCSRFCookieName} {
		if !containsClearedCookie(setCookies, name) {
			t.Errorf("Set-Cookie does not clear %s: %#v", name, setCookies)
		}
	}
}

func TestDeleteAccountRejectsWrongConfirmation(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	service := &accountServiceStub{
		delete: func(_ context.Context, _ auth.UserPrincipal, confirmation string) (account.DeleteResult, error) {
			if confirmation != "delete" {
				t.Fatalf("Delete() confirmation = %q", confirmation)
			}
			return account.DeleteResult{}, account.ErrInvalidConfirmation
		},
	}
	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/v1/me",
		bytes.NewBufferString(`{"confirmation":"delete"}`),
	)
	request.Header.Set("Authorization", "Bearer desktop-access-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	New(Handler{AuthService: authenticatedAuthStub(userID), AccountService: service}).ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(response.Body.String(), `"code":"validation_failed"`) ||
		!strings.Contains(response.Body.String(), `"detail":"request validation failed"`) {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
}

func TestDeleteAccountRepeatedRequestConvergesToLoggedOut(t *testing.T) {
	t.Parallel()

	authService := &authServiceStub{
		authenticate: func(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error) {
			return auth.UserPrincipal{}, auth.ErrInvalidSession
		},
	}
	service := &accountServiceStub{
		delete: func(context.Context, auth.UserPrincipal, string) (account.DeleteResult, error) {
			t.Fatal("Delete() called for a revoked session")
			return account.DeleteResult{}, nil
		},
	}
	response := serveBearerJSON(
		New(Handler{AuthService: authService, AccountService: service}),
		http.MethodDelete,
		"/api/v1/me",
		[]byte(`{"confirmation":"DELETE"}`),
	)

	if response.Code != http.StatusUnauthorized ||
		!strings.Contains(response.Body.String(), `"code":"session_invalid"`) {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
}

func TestDeleteAccountMapsTransactionConflicts(t *testing.T) {
	t.Parallel()

	userID := uuid.MustParse("0197f590-10b8-7cc0-a6c6-160ed7a18b2c")
	service := &accountServiceStub{
		delete: func(context.Context, auth.UserPrincipal, string) (account.DeleteResult, error) {
			return account.DeleteResult{}, account.ErrConflict
		},
	}
	response := serveBearerJSON(
		New(Handler{AuthService: authenticatedAuthStub(userID), AccountService: service}),
		http.MethodDelete,
		"/api/v1/me",
		[]byte(`{"confirmation":"DELETE"}`),
	)

	if response.Code != http.StatusConflict ||
		!strings.Contains(response.Body.String(), `"code":"account_conflict"`) {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
}

type accountServiceStub struct {
	delete func(context.Context, auth.UserPrincipal, string) (account.DeleteResult, error)
}

func (s *accountServiceStub) Delete(
	ctx context.Context,
	principal auth.UserPrincipal,
	confirmation string,
) (account.DeleteResult, error) {
	if s.delete == nil {
		return account.DeleteResult{}, errors.New("unexpected Delete call")
	}
	return s.delete(ctx, principal, confirmation)
}

func containsClearedCookie(values []string, name string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, name+"=") && strings.Contains(value, "Max-Age=0") {
			return true
		}
	}
	return false
}
