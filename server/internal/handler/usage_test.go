package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/usage"
)

type usageServiceStub struct {
	report      func(context.Context, uuid.UUID, []usage.RunUsageInput) error
	get         func(context.Context, auth.UserPrincipal, uuid.UUID, usage.Period) (usage.AgentUsageReport, error)
	getActivity func(context.Context, auth.UserPrincipal) (usage.ActivityInsight, error)
	getCompany  func(context.Context, auth.UserPrincipal, usage.CompanyPeriod) (usage.CompanyUsageReport, error)
}

func (s *usageServiceStub) ReportRunUsage(
	ctx context.Context,
	runID uuid.UUID,
	inputs []usage.RunUsageInput,
) error {
	if s.report == nil {
		return nil
	}
	return s.report(ctx, runID, inputs)
}

func (s *usageServiceStub) GetActivityInsight(
	ctx context.Context,
	principal auth.UserPrincipal,
) (usage.ActivityInsight, error) {
	return s.getActivity(ctx, principal)
}

func (s *usageServiceStub) GetCompany(
	ctx context.Context,
	principal auth.UserPrincipal,
	period usage.CompanyPeriod,
) (usage.CompanyUsageReport, error) {
	return s.getCompany(ctx, principal, period)
}

func TestActivityInsightRouteUsesMemberPrincipalAndReturnsNoStoreReport(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae41")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae42")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	mostUsedProvider := "codex"
	mostUsedModel := "gpt-5.6-sol"
	lastActiveAt := time.Date(2026, 8, 16, 8, 41, 19, 220417000, time.UTC)
	report := usage.ActivityInsight{
		MostUsedProvider: &mostUsedProvider,
		MostUsedModel:    &mostUsedModel,
		SuccessfulTasks:  "87",
		TotalSessions:    "214",
		CompletedRuns:    "192",
		FailedRuns:       "17",
		TotalRuns:        "214",
		LastActiveAt:     &lastActiveAt,
	}
	service := &usageServiceStub{
		getActivity: func(_ context.Context, got auth.UserPrincipal) (usage.ActivityInsight, error) {
			if got != principal {
				t.Fatalf("GetActivityInsight() principal = %+v, want %+v", got, principal)
			}
			return report, nil
		},
	}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			authorize: func(_ context.Context, got auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
				if got.UserID != userID || gotCompanyID != companyID {
					t.Fatalf("Authorize() = %+v / %s", got, gotCompanyID)
				}
				return principal, nil
			},
		},
		UsageService: service,
	})
	path := "/api/v1/companies/" + companyID.String() + "/me/activity-insight"
	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q, want %q", got, "private, no-store")
	}
	var decoded usage.ActivityInsight
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.MostUsedProvider == nil || *decoded.MostUsedProvider != "codex" ||
		decoded.MostUsedModel == nil || *decoded.MostUsedModel != "gpt-5.6-sol" ||
		decoded.SuccessfulTasks != "87" || decoded.TotalSessions != "214" ||
		decoded.CompletedRuns != "192" || decoded.FailedRuns != "17" ||
		decoded.TotalRuns != "214" || decoded.LastActiveAt == nil ||
		!decoded.LastActiveAt.Equal(*report.LastActiveAt) {
		t.Fatalf("response = %+v", decoded)
	}
}

func TestActivityInsightRouteRejectsQueryParametersBeforeService(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae45")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae46")
	calls := 0
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			authorize: func(_ context.Context, principal auth.UserPrincipal, id uuid.UUID) (auth.UserPrincipal, error) {
				principal.CompanyID = id
				principal.Role = auth.CompanyRoleMember
				return principal, nil
			},
		},
		UsageService: &usageServiceStub{
			getActivity: func(context.Context, auth.UserPrincipal) (usage.ActivityInsight, error) {
				calls++
				return usage.ActivityInsight{}, nil
			},
		},
	})
	base := "/api/v1/companies/" + companyID.String() + "/me/activity-insight"
	for _, query := range []string{"?period=all", "?extra=1", "?timeZone=UTC"} {
		response := serveBearerRequest(handler, http.MethodGet, base+query, nil)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
	if calls != 0 {
		t.Fatalf("UsageService.GetActivityInsight() calls = %d, want 0", calls)
	}
}

func TestActivityInsightRouteMapsDomainErrors(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae48")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae49")
	principal := auth.UserPrincipal{
		UserID:    userID,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	service := &usageServiceStub{}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
		},
		UsageService: service,
	})
	path := "/api/v1/companies/" + companyID.String() + "/me/activity-insight"
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "forbidden", err: usage.ErrActivityInsightForbidden, status: http.StatusForbidden, code: "company_access_required"},
		{name: "store", err: usage.ErrStore, status: http.StatusInternalServerError, code: "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service.getActivity = func(context.Context, auth.UserPrincipal) (usage.ActivityInsight, error) {
				return usage.ActivityInsight{}, test.err
			}
			response := serveBearerRequest(handler, http.MethodGet, path, nil)
			assertErrorResponse(t, response, test.status, test.code)
		})
	}
}

func TestCompanyUsageRouteUsesOwnerPrincipalAndReturnsNoStoreReport(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae01")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae02")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	report := usage.CompanyUsageReport{
		Totals: usage.CompanyTotals{
			Tokens: usage.Tokens{
				InputTokens:  "9007199254740993",
				OutputTokens: "7",
			},
			RunCount: "4",
		},
		ComparisonTotals: usage.CompanyTotals{
			Tokens:   usage.Tokens{InputTokens: "0", OutputTokens: "0"},
			RunCount: "0",
		},
		Agents: []usage.AgentUsage{},
	}
	service := &usageServiceStub{
		getCompany: func(_ context.Context, got auth.UserPrincipal, gotPeriod usage.CompanyPeriod) (usage.CompanyUsageReport, error) {
			if got != principal || gotPeriod.Type != usage.CompanyPeriodMonth ||
				gotPeriod.TimeZone != "Asia/Ho_Chi_Minh" {
				t.Fatalf("GetCompany() = %+v / %+v", got, gotPeriod)
			}
			return report, nil
		},
	}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			requirePermission: func(_ context.Context, got auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
				if got.UserID != userID || gotCompanyID != companyID {
					t.Fatalf("RequireOwner() = %+v / %s", got, gotCompanyID)
				}
				return principal, nil
			},
		},
		UsageService: service,
	})
	path := "/api/v1/companies/" + companyID.String() + "/usage?period=month&time_zone=Asia%2FHo_Chi_Minh"
	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var decoded usage.CompanyUsageReport
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Totals.InputTokens != "9007199254740993" || decoded.Agents == nil || decoded.Models == nil {
		t.Fatalf("response = %+v", decoded)
	}
}

func TestCompanyUsageRouteRejectsInvalidQueriesBeforeService(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae05")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae06")
	calls := 0
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			requirePermission: func(_ context.Context, principal auth.UserPrincipal, id uuid.UUID) (auth.UserPrincipal, error) {
				principal.CompanyID = id
				principal.Role = auth.CompanyRoleOwner
				return principal, nil
			},
		},
		UsageService: &usageServiceStub{
			getCompany: func(context.Context, auth.UserPrincipal, usage.CompanyPeriod) (usage.CompanyUsageReport, error) {
				calls++
				return usage.CompanyUsageReport{}, nil
			},
		},
	})
	base := "/api/v1/companies/" + companyID.String() + "/usage"
	for _, query := range []string{
		"",
		"?period=month&time_zone=UTC&extra=1",
		"?period=month&period=week&time_zone=UTC",
		"?period=quarter&time_zone=UTC",
	} {
		response := serveBearerRequest(handler, http.MethodGet, base+query, nil)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
	if calls != 0 {
		t.Fatalf("UsageService.GetCompany() calls = %d", calls)
	}
}

func (s *usageServiceStub) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	agentID uuid.UUID,
	period usage.Period,
) (usage.AgentUsageReport, error) {
	return s.get(ctx, principal, agentID, period)
}

func TestAgentUsageRouteUsesOwnerPrincipalAndReturnsNoStoreReport(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae11")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae12")
	agentID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae13")
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	startAt := time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC)
	endAt := time.Date(2026, 3, 9, 4, 0, 0, 0, time.UTC)
	report := usage.AgentUsageReport{
		PeriodStartAt: startAt, PeriodEndAt: endAt,
		TimeZone: "America/New_York", Bucket: usage.BucketDay,
		Totals: usage.Totals{
			Tokens: usage.Tokens{
				InputTokens:      "9007199254740993",
				OutputTokens:     "7",
				CacheReadTokens:  "2",
				CacheWriteTokens: "1",
			},
			RunCount: "1",
			CostAttribution: usage.CostAttribution{
				CostCoverageState:     usage.CostCoverageMissing,
				CostReportedFactCount: "0",
				CostMissingFactCount:  "1",
			},
		},
		Series: []usage.SeriesPoint{}, Models: []usage.ModelSlice{},
		TopTasks: []usage.TopTaskItem{}, TopTasksTotalInputOutputTokens: "0",
	}
	service := &usageServiceStub{
		get: func(_ context.Context, got auth.UserPrincipal, gotAgentID uuid.UUID, gotPeriod usage.Period) (usage.AgentUsageReport, error) {
			if got != principal || gotAgentID != agentID {
				t.Fatalf("Get() identity = %+v / %s", got, gotAgentID)
			}
			if !gotPeriod.StartAt.Equal(startAt) || !gotPeriod.EndAt.Equal(endAt) ||
				gotPeriod.TimeZone != "America/New_York" ||
				gotPeriod.Bucket != usage.BucketDay {
				t.Fatalf("Get() period = %+v", gotPeriod)
			}
			return report, nil
		},
	}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			requirePermission: func(_ context.Context, got auth.UserPrincipal, gotCompanyID uuid.UUID) (auth.UserPrincipal, error) {
				if got.UserID != userID || gotCompanyID != companyID {
					t.Fatalf("RequireOwner() = %+v / %s", got, gotCompanyID)
				}
				return principal, nil
			},
		},
		UsageService: service,
	})
	path := "/api/v1/companies/" + companyID.String() + "/agents/" + agentID.String() +
		"/usage?start_at=2026-03-08T00%3A00%3A00-05%3A00&end_at=2026-03-09T00%3A00%3A00-04%3A00&time_zone=America%2FNew_York&bucket=day"
	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	var decoded usage.AgentUsageReport
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Totals.InputTokens != "9007199254740993" || decoded.Totals.ProviderCostUSDTicks != nil ||
		decoded.Series == nil ||
		decoded.Models == nil ||
		decoded.TopTasks == nil {
		t.Fatalf("response = %+v", decoded)
	}
}

func TestAgentUsageRouteRejectsInvalidQueriesBeforeService(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae21")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae22")
	agentID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae23")
	calls := 0
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			requirePermission: func(_ context.Context, principal auth.UserPrincipal, id uuid.UUID) (auth.UserPrincipal, error) {
				principal.CompanyID = id
				principal.Role = auth.CompanyRoleOwner
				return principal, nil
			},
		},
		UsageService: &usageServiceStub{
			get: func(context.Context, auth.UserPrincipal, uuid.UUID, usage.Period) (usage.AgentUsageReport, error) {
				calls++
				return usage.AgentUsageReport{}, nil
			},
		},
	})
	base := "/api/v1/companies/" + companyID.String() + "/agents/" + agentID.String() + "/usage"
	queries := []string{
		"",
		"?start_at=2026-01-01T00%3A00%3A00Z&end_at=2026-01-02T00%3A00%3A00Z&time_zone=UTC&bucket=day&extra=1",
		"?start_at=2026-01-01T00%3A00%3A00Z&start_at=2026-01-01T00%3A00%3A00Z&end_at=2026-01-02T00%3A00%3A00Z&time_zone=UTC&bucket=day",
	}
	for _, query := range queries {
		response := serveBearerRequest(handler, http.MethodGet, base+query, nil)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
	if calls != 0 {
		t.Fatalf("UsageService.Get() calls = %d", calls)
	}
}

func TestAgentUsageRouteMapsDomainErrors(t *testing.T) {
	t.Parallel()
	userID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae31")
	companyID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae32")
	agentID := uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae33")
	principal := auth.UserPrincipal{
		UserID:    userID,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID,
		Role:      auth.CompanyRoleOwner,
	}
	service := &usageServiceStub{}
	handler := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return principal, nil
			},
		},
		UsageService: service,
	})
	path := "/api/v1/companies/" + companyID.String() + "/agents/" + agentID.String() +
		"/usage?start_at=2026-01-01T00%3A00%3A00Z&end_at=2026-01-02T00%3A00%3A00Z&time_zone=UTC&bucket=day"
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{usage.ErrNotFound, http.StatusNotFound, "agent_not_found"},
		{usage.ErrForbidden, http.StatusForbidden, "company_permission_required"},
		{usage.ErrStore, http.StatusInternalServerError, "internal_error"},
	} {
		service.get = func(context.Context, auth.UserPrincipal, uuid.UUID, usage.Period) (usage.AgentUsageReport, error) {
			return usage.AgentUsageReport{}, test.err
		}
		response := serveBearerRequest(handler, http.MethodGet, path, nil)
		assertErrorResponse(t, response, test.status, test.code)
	}

	companyDenied := New(Handler{
		AuthService: authenticatedAuthStub(userID),
		CompanyService: &companyServiceStub{
			requirePermission: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
				return auth.UserPrincipal{}, company.ErrNotFound
			},
		},
		UsageService: service,
	})
	response := serveBearerRequest(companyDenied, http.MethodGet, path, nil)
	assertErrorResponse(t, response, http.StatusNotFound, "company_not_found")
}
