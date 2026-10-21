package handler

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/usage"
)

type huddleBudgetStub struct {
	usage.HuddleBudgetService
	configure func(usage.ConfigureHuddleBudgetInput) (usage.HuddleBudget, error)
}

func (s huddleBudgetStub) Configure(
	_ context.Context,
	_ auth.UserPrincipal,
	input usage.ConfigureHuddleBudgetInput,
) (usage.HuddleBudget, error) {
	return s.configure(input)
}

func TestConfigureHuddleBudget(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"zero disables", `{"monthlyLimitUsdTicks":"0","expectedRevision":0}`, 200},
		{"configured", `{"monthlyLimitUsdTicks":"100000000000","expectedRevision":0}`, 200},
		{"negative", `{"monthlyLimitUsdTicks":"-1","expectedRevision":0}`, 422},
		{"overflow", `{"monthlyLimitUsdTicks":"999999999999999999999","expectedRevision":0}`, 422},
		{"fraction", `{"monthlyLimitUsdTicks":"0.1","expectedRevision":0}`, 422},
		{"signed value", `{"monthlyLimitUsdTicks":"+1","expectedRevision":0}`, 422},
		{"missing limit", `{"expectedRevision":0}`, 422},
		{"missing revision", `{"monthlyLimitUsdTicks":"0"}`, 422},
		{"unknown field", `{"monthlyLimitUsdTicks":"0","expectedRevision":0,"enabled":true}`, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			userID, companyID := uuid.New(), uuid.New()
			h := New(Handler{
				AuthService: authenticatedAuthStub(userID),
				CompanyService: &companyServiceStub{
					requirePermission: func(_ context.Context, p auth.UserPrincipal, _ uuid.UUID) (auth.UserPrincipal, error) {
						p.CompanyID = companyID
						p.Role = auth.CompanyRoleAdmin
						return p, nil
					},
				},
				HuddleBudgetService: huddleBudgetStub{
					configure: func(_ usage.ConfigureHuddleBudgetInput) (usage.HuddleBudget, error) {
						if tc.want != 200 {
							t.Fatal("invalid input reached service")
						}
						return usage.HuddleBudget{Revision: 1}, nil
					},
				},
			})
			response := serveBearerJSON(
				h,
				http.MethodPut,
				"/api/v1/companies/"+companyID.String()+"/huddle-budget",
				[]byte(tc.body),
			)
			if response.Code != tc.want {
				t.Fatalf("status/body = %d/%s, want %d", response.Code, response.Body.String(), tc.want)
			}
		})
	}
}
