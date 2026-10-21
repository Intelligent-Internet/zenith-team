package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
)

func TestCompanyPlanApplyWithoutRunTokenPreservesOwnerMutation(t *testing.T) {
	t.Parallel()

	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principal := auth.UserPrincipal{
		UserID:    userID,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID,
		Role:      auth.CompanyRoleOwner,
	}
	service := &companyPlanServiceStub{
		applyCompanyPlan: func(
			_ context.Context,
			gotPrincipal auth.UserPrincipal,
			artifact company.PlanArtifact,
		) (company.PlanApplyResult, error) {
			if gotPrincipal != principal || artifact.ArtifactVersion != 1 ||
				artifact.CompanyID != companyID.String() {
				t.Fatalf("ApplyCompanyPlan input = %+v / %+v", gotPrincipal, artifact)
			}
			return company.PlanApplyResult{
				Created: 2,
				Updated: 1,
				Deleted: 3,
				Skipped: []company.PlanSkip{},
			}, nil
		},
	}
	handler := companyPlanTestHandler(t, principal, service)
	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/company-plan/apply",
		[]byte(`{"artifactVersion":1,"companyId":"`+companyID.String()+`"}`),
	)
	if response.Code != http.StatusOK ||
		response.Body.String() != `{"created":2,"updated":1,"deleted":3,"skipped":[]}` {
		t.Fatalf("apply Company Plan = %d %s", response.Code, response.Body.String())
	}
}

// The permission answers before the body is read: a member learns nothing
// about how the artifact parses, and the service is never reached.
func TestCompanyPlanApplyRefusesAMemberBeforeReadingTheBody(t *testing.T) {
	t.Parallel()

	principal := phase4Principal(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	service := &companyPlanServiceStub{
		applyCompanyPlan: func(
			context.Context, auth.UserPrincipal, company.PlanArtifact,
		) (company.PlanApplyResult, error) {
			t.Fatal("a member reached ApplyCompanyPlan")
			return company.PlanApplyResult{}, nil
		},
	}
	handler := companyPlanTestHandler(t, principal, service)
	for _, body := range []string{`not json at all`, `{}`} {
		response := serveBearerJSON(
			handler,
			http.MethodPost,
			"/api/v1/companies/"+principal.CompanyID.String()+"/company-plan/apply",
			[]byte(body),
		)
		if response.Code != http.StatusForbidden ||
			!strings.Contains(response.Body.String(), `"code":"company_permission_required"`) {
			t.Fatalf("member apply with %q = %d %s", body, response.Code, response.Body.String())
		}
	}
}

func TestGoalRoutesAreRemoved(t *testing.T) {
	t.Parallel()
	principal := phase4Principal(uuid.New(), uuid.New())
	handler := companyPlanTestHandler(t, principal, &companyPlanServiceStub{})
	path := "/api/v1/companies/" + principal.CompanyID.String() + "/goals"
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, path},
		{http.MethodPost, path},
		{http.MethodGet, path + "/" + uuid.NewString()},
		{http.MethodPatch, path + "/" + uuid.NewString()},
		{http.MethodDelete, path + "/" + uuid.NewString()},
	} {
		response := serveBearerRequest(handler, request.method, request.path, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", request.method, request.path, response.Code)
		}
	}
}

type companyPlanServiceStub struct {
	applyCompanyPlan func(context.Context, auth.UserPrincipal, company.PlanArtifact) (company.PlanApplyResult, error)
}

func (s *companyPlanServiceStub) ApplyCompanyPlan(
	ctx context.Context,
	principal auth.UserPrincipal,
	artifact company.PlanArtifact,
) (company.PlanApplyResult, error) {
	if s.applyCompanyPlan == nil {
		return company.PlanApplyResult{}, errors.New("unexpected ApplyCompanyPlan")
	}
	return s.applyCompanyPlan(ctx, principal, artifact)
}

func companyPlanTestHandler(t *testing.T, principal auth.UserPrincipal, service CompanyPlanService) http.Handler {
	t.Helper()
	companies := &companyServiceStub{
		authorize: func(_ context.Context, user auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
			if user.UserID != principal.UserID || companyID != principal.CompanyID {
				return auth.UserPrincipal{}, company.ErrNotFound
			}
			return principal, nil
		},
		requirePermission: func(_ context.Context, user auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
			if user.UserID != principal.UserID || companyID != principal.CompanyID {
				return auth.UserPrincipal{}, company.ErrNotFound
			}
			return principal, nil
		},
	}
	return New(
		Handler{
			AuthService:        authenticatedAuthStub(principal.UserID),
			CompanyService:     companies,
			CompanyPlanService: service,
		},
	)
}
