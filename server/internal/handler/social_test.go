package handler

import (
	"errors"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Intelligent-Internet/zenith-team/server/internal/social"
)

func TestSocialErrorContracts(t *testing.T) {
	cases := []struct {
		name         string
		write        func(*gin.Context, error)
		sentinel     error
		status       int
		detail, code string
	}{
		{
			"social review required",
			writeSocialError,
			social.ErrApprovalRequired,
			409,
			"Review the post with ask_human and confirm Publish now before publishing",
			"social_approval_required",
		},
		{
			"social unavailable",
			writeSocialError,
			social.ErrUnavailable,
			503,
			"this social connection is not available yet",
			"social_unavailable",
		},
		{
			"social not found",
			writeSocialError,
			social.ErrNotFound,
			404,
			"social connection not found",
			"social_not_found",
		},
		{
			"social flow",
			writeSocialError,
			social.ErrInvalidFlow,
			422,
			"authorization expired; start connecting again",
			"social_invalid_flow",
		},
		{
			"social provider",
			writeSocialError,
			social.ErrProvider,
			502,
			"the social platform could not complete the request; try again",
			"social_provider_failed",
		},
		{
			"social conflict",
			writeSocialError,
			social.ErrConflict,
			409,
			"this connection changed; refresh before trying again",
			"social_conflict",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assertMappedErrorResponse(
				t,
				func(c *gin.Context) { tt.write(c, errors.Join(errors.New("private provider detail"), tt.sentinel)) },
				tt.status,
				tt.detail,
				tt.code,
			)
		})
	}
}
