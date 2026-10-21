package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/activity"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"

	"github.com/google/uuid"
)

type authServiceStub struct {
	signup        func(context.Context, auth.SignupInput) (auth.SignupResult, error)
	verifyEmail   func(context.Context, auth.VerifyEmailInput) (auth.Result, error)
	resendEmail   func(context.Context, auth.ResendEmailVerificationInput) (auth.PendingEmailVerification, error)
	emailStatus   func(context.Context, auth.EmailVerificationStatusInput) (auth.EmailVerificationStatusResult, error)
	login         func(context.Context, auth.LoginInput) (auth.LoginResult, error)
	requestReset  func(context.Context, auth.PasswordResetRequestInput) error
	resetPassword func(context.Context, auth.PasswordResetInput) error
	refresh       func(context.Context, auth.RefreshInput) (auth.RefreshResult, error)
	logout        func(context.Context, string) (auth.SessionMutationResult, error)
	authenticate  func(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error)
	mintDaemon    func(context.Context, auth.UserPrincipal) (auth.Result, error)
	currentUser   func(context.Context, uuid.UUID) (auth.User, error)
	updateProfile func(context.Context, uuid.UUID, auth.UpdateProfileInput) (auth.User, error)
}

func (s *authServiceStub) Signup(ctx context.Context, input auth.SignupInput) (auth.SignupResult, error) {
	return s.signup(ctx, input)
}

func (s *authServiceStub) VerifyEmail(ctx context.Context, input auth.VerifyEmailInput) (auth.Result, error) {
	return s.verifyEmail(ctx, input)
}

func (s *authServiceStub) ResendEmailVerification(
	ctx context.Context,
	input auth.ResendEmailVerificationInput,
) (auth.PendingEmailVerification, error) {
	return s.resendEmail(ctx, input)
}

func (s *authServiceStub) EmailVerificationStatus(
	ctx context.Context,
	input auth.EmailVerificationStatusInput,
) (auth.EmailVerificationStatusResult, error) {
	return s.emailStatus(ctx, input)
}

func (s *authServiceStub) Login(ctx context.Context, input auth.LoginInput) (auth.LoginResult, error) {
	return s.login(ctx, input)
}

func (s *authServiceStub) RequestPasswordReset(ctx context.Context, input auth.PasswordResetRequestInput) error {
	return s.requestReset(ctx, input)
}

func (s *authServiceStub) ResetPassword(ctx context.Context, input auth.PasswordResetInput) error {
	return s.resetPassword(ctx, input)
}

func (s *authServiceStub) Refresh(ctx context.Context, input auth.RefreshInput) (auth.RefreshResult, error) {
	return s.refresh(ctx, input)
}

func (s *authServiceStub) Logout(ctx context.Context, refreshToken string) (auth.SessionMutationResult, error) {
	return s.logout(ctx, refreshToken)
}

func (s *authServiceStub) Authenticate(
	ctx context.Context,
	accessToken string,
	kind auth.TokenKind,
) (auth.UserPrincipal, error) {
	return s.authenticate(ctx, accessToken, kind)
}

func (s *authServiceStub) MintDaemonSession(
	ctx context.Context,
	principal auth.UserPrincipal,
) (auth.Result, error) {
	return s.mintDaemon(ctx, principal)
}

func (s *authServiceStub) CurrentUser(ctx context.Context, userID uuid.UUID) (auth.User, error) {
	return s.currentUser(ctx, userID)
}

func (s *authServiceStub) UpdateProfile(
	ctx context.Context,
	userID uuid.UUID,
	input auth.UpdateProfileInput,
) (auth.User, error) {
	return s.updateProfile(ctx, userID, input)
}

type companyServiceStub struct {
	removeMember      func(context.Context, auth.UserPrincipal, company.MembershipMutation) error
	leave             func(context.Context, auth.UserPrincipal, int64) error
	deleteCompany     func(context.Context, auth.UserPrincipal, int64) error
	create            func(context.Context, auth.UserPrincipal, company.CreateCompanyInput) (company.Response, error)
	bootstrap         func(context.Context, auth.UserPrincipal) (company.Bootstrap, error)
	get               func(context.Context, auth.UserPrincipal, uuid.UUID) (company.Response, error)
	list              func(context.Context, auth.UserPrincipal) ([]company.Response, error)
	authorize         func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error)
	requirePermission func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error)
	changeRole        func(context.Context, auth.UserPrincipal, company.ChangeRoleInput) (company.ActiveMemberResponse, error)
	transferOwnership func(context.Context, auth.UserPrincipal, company.MembershipMutation) ([]company.ActiveMemberResponse, error)
	updateProfile     func(context.Context, auth.UserPrincipal, company.UpdateProfileInput) (company.Response, error)
	listMembers       func(context.Context, auth.UserPrincipal) ([]company.ActiveMemberResponse, error)
	statistics        func(context.Context, auth.UserPrincipal) (company.Statistics, error)
}

func (s *companyServiceStub) Delete(
	ctx context.Context,
	principal auth.UserPrincipal,
	expectedRevision int64,
) error {
	if s.deleteCompany == nil {
		return errors.New("unexpected Delete call")
	}
	return s.deleteCompany(ctx, principal, expectedRevision)
}

func (s *companyServiceStub) Bootstrap(
	ctx context.Context,
	principal auth.UserPrincipal,
) (company.Bootstrap, error) {
	if s.bootstrap == nil {
		return company.Bootstrap{}, errors.New("unexpected Bootstrap call")
	}
	return s.bootstrap(ctx, principal)
}

func (s *companyServiceStub) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input company.CreateCompanyInput,
) (company.Response, error) {
	if s.create == nil {
		return company.Response{}, errors.New("unexpected Create call")
	}
	return s.create(ctx, principal, input)
}

func (s *companyServiceStub) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
) (company.Response, error) {
	if s.get == nil {
		return company.Response{}, errors.New("unexpected Get call")
	}
	return s.get(ctx, principal, companyID)
}

func (s *companyServiceStub) List(
	ctx context.Context,
	principal auth.UserPrincipal,
) ([]company.Response, error) {
	if s.list == nil {
		return nil, errors.New("unexpected List call")
	}
	return s.list(ctx, principal)
}

func (s *companyServiceStub) Authorize(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
) (auth.UserPrincipal, error) {
	if s.authorize == nil {
		return auth.UserPrincipal{}, errors.New("unexpected Authorize call")
	}
	return s.authorize(ctx, principal, companyID)
}

// RequirePermission mirrors the real service: the stub answers for the
// resolved company, and tests that care about the permission read it from
// the route they mount rather than from this seam.
func (s *companyServiceStub) RequirePermission(
	ctx context.Context,
	principal auth.UserPrincipal,
	companyID uuid.UUID,
	_ auth.Permission,
) (auth.UserPrincipal, error) {
	if s.requirePermission == nil {
		return auth.UserPrincipal{}, errors.New("unexpected RequirePermission call")
	}
	return s.requirePermission(ctx, principal, companyID)
}

func (s *companyServiceStub) ChangeRole(
	ctx context.Context,
	principal auth.UserPrincipal,
	input company.ChangeRoleInput,
) (company.ActiveMemberResponse, error) {
	if s.changeRole == nil {
		return company.ActiveMemberResponse{}, errors.New("unexpected ChangeRole call")
	}
	return s.changeRole(ctx, principal, input)
}

func (s *companyServiceStub) TransferOwnership(
	ctx context.Context,
	principal auth.UserPrincipal,
	input company.MembershipMutation,
) ([]company.ActiveMemberResponse, error) {
	if s.transferOwnership == nil {
		return nil, errors.New("unexpected TransferOwnership call")
	}
	return s.transferOwnership(ctx, principal, input)
}

func (s *companyServiceStub) UpdateProfile(
	ctx context.Context,
	principal auth.UserPrincipal,
	input company.UpdateProfileInput,
) (company.Response, error) {
	if s.updateProfile == nil {
		return company.Response{}, errors.New("unexpected UpdateProfile call")
	}
	return s.updateProfile(ctx, principal, input)
}

func (s *companyServiceStub) ListMembers(
	ctx context.Context,
	principal auth.UserPrincipal,
) ([]company.ActiveMemberResponse, error) {
	if s.listMembers == nil {
		return nil, errors.New("unexpected ListMembers call")
	}
	return s.listMembers(ctx, principal)
}

func (s *companyServiceStub) Statistics(
	ctx context.Context,
	principal auth.UserPrincipal,
) (company.Statistics, error) {
	if s.statistics == nil {
		return company.Statistics{}, errors.New("unexpected Statistics call")
	}
	return s.statistics(ctx, principal)
}

func authenticatedAuthStub(userID uuid.UUID) *authServiceStub {
	return &authServiceStub{
		authenticate: func(_ context.Context, _ string, kind auth.TokenKind) (auth.UserPrincipal, error) {
			return auth.UserPrincipal{
				UserID:    userID,
				SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
				AuthKind:  kind,
			}, nil
		},
	}
}

func serveBearerRequest(handler http.Handler, method, path string, body *bytes.Reader) *httptest.ResponseRecorder {
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, body)
	}
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func serveBearerJSON(handler http.Handler, method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertErrorResponse(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, status, response.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body["code"] != code || body["detail"] == "" || body["requestId"] == "" {
		t.Fatalf("error body = %#v", body)
	}
}

type desktopHandoffServiceStub struct {
	start    func(context.Context, uuid.UUID) (auth.DesktopHandoffStartResult, error)
	complete func(context.Context, auth.DesktopHandoffCompleteInput) (auth.Result, error)
}

func (s *desktopHandoffServiceStub) Start(
	ctx context.Context,
	userID uuid.UUID,
) (auth.DesktopHandoffStartResult, error) {
	if s == nil || s.start == nil {
		return auth.DesktopHandoffStartResult{}, errors.New("unexpected Start call")
	}
	return s.start(ctx, userID)
}

func (s *desktopHandoffServiceStub) Complete(
	ctx context.Context,
	input auth.DesktopHandoffCompleteInput,
) (auth.Result, error) {
	if s == nil || s.complete == nil {
		return auth.Result{}, errors.New("unexpected Complete call")
	}
	return s.complete(ctx, input)
}

type googleOAuthServiceStub struct {
	enabled         bool
	webEnabled      bool
	desktopEnabled  bool
	startWeb        func(context.Context, auth.WebOAuthStartInput) (auth.WebOAuthStartResult, error)
	completeWeb     func(context.Context, auth.WebOAuthCompleteInput) (auth.WebOAuthCompleteResult, error)
	startDesktop    func(context.Context, auth.DesktopOAuthStartInput) (auth.DesktopOAuthStartResult, error)
	completeDesktop func(context.Context, auth.DesktopOAuthCompleteInput) (auth.Result, error)
}

func (s *googleOAuthServiceStub) ProviderEnabled() bool {
	return s != nil && (s.enabled || s.webEnabled || s.desktopEnabled)
}

func (s *googleOAuthServiceStub) WebEnabled() bool {
	return s != nil && (s.enabled || s.webEnabled)
}

func (s *googleOAuthServiceStub) DesktopEnabled() bool {
	return s != nil && (s.enabled || s.desktopEnabled)
}

func (s *googleOAuthServiceStub) StartWeb(
	ctx context.Context,
	input auth.WebOAuthStartInput,
) (auth.WebOAuthStartResult, error) {
	if s == nil || s.startWeb == nil {
		return auth.WebOAuthStartResult{}, auth.ErrOAuthNotConfigured
	}
	return s.startWeb(ctx, input)
}

func (s *googleOAuthServiceStub) CompleteWeb(
	ctx context.Context,
	input auth.WebOAuthCompleteInput,
) (auth.WebOAuthCompleteResult, error) {
	if s == nil || s.completeWeb == nil {
		return auth.WebOAuthCompleteResult{}, errors.New("unexpected CompleteWeb call")
	}
	return s.completeWeb(ctx, input)
}

func (s *googleOAuthServiceStub) StartDesktop(
	ctx context.Context,
	input auth.DesktopOAuthStartInput,
) (auth.DesktopOAuthStartResult, error) {
	if s == nil || s.startDesktop == nil {
		return auth.DesktopOAuthStartResult{}, auth.ErrOAuthNotConfigured
	}
	return s.startDesktop(ctx, input)
}

func (s *googleOAuthServiceStub) CompleteDesktop(
	ctx context.Context,
	input auth.DesktopOAuthCompleteInput,
) (auth.Result, error) {
	if s == nil || s.completeDesktop == nil {
		return auth.Result{}, errors.New("unexpected CompleteDesktop call")
	}
	return s.completeDesktop(ctx, input)
}

// activityFeedStub satisfies ActivityFeed for route wiring tests.
type activityFeedStub struct{}

func (activityFeedStub) List(
	context.Context,
	uuid.UUID,
	activity.ListOptions,
) ([]activity.Entry, *activity.Cursor, error) {
	return []activity.Entry{}, nil, nil
}

func (activityFeedStub) Calendar(
	context.Context,
	uuid.UUID,
	activity.CalendarOptions,
) (activity.Calendar, error) {
	return activity.Calendar{Days: map[string]int64{}}, nil
}

type milestoneServiceStub struct {
	attest func(
		context.Context,
		auth.UserPrincipal,
		[]task.MilestoneType,
		string,
	) ([]company.MilestoneAttestation, error)
}

func (s *milestoneServiceStub) AttestMilestones(
	ctx context.Context,
	principal auth.UserPrincipal,
	milestones []task.MilestoneType,
	note string,
) ([]company.MilestoneAttestation, error) {
	if s.attest == nil {
		return nil, company.ErrNotFound
	}
	return s.attest(ctx, principal, milestones, note)
}

type referenceServiceStub struct {
	add func(
		context.Context,
		auth.UserPrincipal,
		[]uuid.UUID,
	) ([]company.ReferenceDocument, error)
}

func (s *referenceServiceStub) AddReferenceDocuments(
	ctx context.Context,
	principal auth.UserPrincipal,
	attachmentIDs []uuid.UUID,
) ([]company.ReferenceDocument, error) {
	if s.add == nil {
		return nil, company.ErrNotFound
	}
	return s.add(ctx, principal, attachmentIDs)
}

func (s *companyServiceStub) RemoveMember(
	ctx context.Context,
	p auth.UserPrincipal,
	input company.MembershipMutation,
) error {
	if s.removeMember == nil {
		return errors.New("unexpected RemoveMember")
	}
	return s.removeMember(ctx, p, input)
}

func (s *companyServiceStub) Leave(ctx context.Context, p auth.UserPrincipal, revision int64) error {
	if s.leave == nil {
		return errors.New("unexpected Leave")
	}
	return s.leave(ctx, p, revision)
}
