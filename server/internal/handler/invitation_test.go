package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
)

func TestInvitationErrors(t *testing.T) {
	t.Parallel()
	cases := []mappedErrorCase{
		{
			"removed",
			company.ErrShareLinkMembershipRemoved,
			403,
			"Ask an owner or admin for a new email invitation to rejoin",
			"team_share_link_membership_removed",
		},
		{
			"verification",
			company.ErrShareLinkVerificationRequired,
			403,
			"Verify your email before joining",
			"team_share_link_verification_required",
		},
		{
			"share configuration",
			company.ErrShareLinkUnavailable,
			503,
			"Invite links are not configured",
			"team_share_link_unavailable",
		},
		{"missing", company.ErrInvitationNotFound, 404, "Invitation not found", "team_invitation_not_found"},
		{
			"account",
			company.ErrInvitationAccountMismatch,
			403,
			"Sign in with the verified invited email",
			"team_invitation_account_mismatch",
		},
		{
			"inactive",
			company.ErrInvitationInactive,
			410,
			"This invitation is no longer active",
			"team_invitation_inactive",
		},
		{
			"revision",
			company.ErrInvitationConflict,
			409,
			"Invitation changed; refresh and try again",
			"team_invitation_revision_conflict",
		},
		{
			"limit",
			company.ErrInvitationRateLimited,
			429,
			"Please wait before trying again",
			"team_invitation_rate_limited",
		},
		{
			"unavailable",
			company.ErrInvitationUnavailable,
			503,
			"Invitation email is not configured",
			"team_invitation_unavailable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertMappedErrorResponse(
				t,
				func(c *gin.Context) { writeInvitationError(c, tc.err) },
				tc.status,
				tc.detail,
				tc.code,
			)
		})
	}
}

func TestInvitationPublicRateLimit(t *testing.T) {
	t.Parallel()
	r := gin.New()
	r.Use(invitationRequestLimit())
	r.GET("/invite", func(c *gin.Context) { c.Status(http.StatusOK) })
	for index := range 121 {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/invite", nil)
		r.ServeHTTP(w, req)
		want := 200
		if index == 120 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("request %d status = %d, want %d", index, w.Code, want)
		}
	}
}

type invitationPreviewStub struct {
	company.InvitationService
	previews int
}

func (s *invitationPreviewStub) Preview(context.Context, string) (company.InvitationPreview, error) {
	s.previews++
	return company.InvitationPreview{CompanyName: "Acme", MaskedEmail: "a***@example.com", State: "pending"}, nil
}

func TestInvitationPreviewIsPublicAndUncacheable(t *testing.T) {
	t.Parallel()
	service := &invitationPreviewStub{}
	handler := New(
		Handler{
			InvitationService: service,
			AuthService:       authenticatedAuthStub(uuid.New()),
			CompanyService:    &companyServiceStub{},
		},
	)
	path := "/api/v1/invitations/" + strings.Repeat("A", 43)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK || service.previews != 1 {
		t.Fatalf("preview = %d %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("preview headers = %v", response.Header())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path+"/accept", strings.NewReader("{}")))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous accept = %d", response.Code)
	}
}
