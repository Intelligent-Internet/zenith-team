package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/execution"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
)

func TestArtifactSharingMutationsRequireHumanActor(t *testing.T) {
	t.Parallel()
	principal := auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), Role: auth.CompanyRoleOwner}
	resolve := func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
		return principal, nil
	}
	handler := New(
		Handler{
			AuthService:        authenticatedAuthStub(principal.UserID),
			CompanyService:     &companyServiceStub{authorize: resolve, requirePermission: resolve},
			DeliverableService: &deliverableDownloadStub{},
			RunProofResolver:   runProofResolverStub{},
		},
	)
	for _, route := range []struct{ method, path string }{
		{http.MethodPut, "/artifacts/" + uuid.NewString() + "/visibility"},
		{http.MethodPost, "/artifacts/" + uuid.NewString() + "/claim"},
		{http.MethodDelete, "/artifacts/" + uuid.NewString() + "/conversations/" + uuid.NewString()},
		{http.MethodPut, "/artifacts/" + uuid.NewString() + "/grants/" + uuid.NewString()},
		{http.MethodDelete, "/artifacts/" + uuid.NewString() + "/grants/" + uuid.NewString()},
	} {
		t.Run(route.method+route.path, func(t *testing.T) {
			request := httptest.NewRequest(
				route.method,
				"/api/v1/companies/"+principal.CompanyID.String()+route.path,
				strings.NewReader(`{}`),
			)
			request.Header.Set("Authorization", "Bearer access-token")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Boardly-Run-Token", "run-proof")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden ||
				!strings.Contains(response.Body.String(), `"human_actor_required"`) {
				t.Fatalf("agent sharing mutation = %d %s", response.Code, response.Body)
			}
		})
	}
}

type publicArtifactDownloadStub struct {
	deliverableDownloadStub
	resolvePublic func(uuid.UUID, int) (uuid.UUID, uuid.UUID, error)
}

func (s *publicArtifactDownloadStub) ResolvePublicArtifactFile(
	_ context.Context,
	token uuid.UUID,
	ordinal int,
) (uuid.UUID, uuid.UUID, error) {
	return s.resolvePublic(token, ordinal)
}

func TestPublicArtifactDownloadStreamsOnlyAfterTokenAuthorization(t *testing.T) {
	t.Parallel()
	token, companyID, attachmentID := uuid.New(), uuid.New(), uuid.New()
	for _, revoked := range []bool{false, true} {
		t.Run(fmt.Sprintf("revoked=%v", revoked), func(t *testing.T) {
			downloaded := false
			h := Handler{
				DeliverableService: &publicArtifactDownloadStub{
					resolvePublic: func(got uuid.UUID, ordinal int) (uuid.UUID, uuid.UUID, error) {
						if got != token || ordinal != 0 {
							t.Fatalf("token resolution = %s %d", got, ordinal)
						}
						if revoked {
							return uuid.Nil, uuid.Nil, execution.ErrDeliverableNotFound
						}
						return companyID, attachmentID, nil
					},
				},
				AttachmentService: &attachmentServiceStub{
					downloadResolved: func(_ context.Context, input attachment.ResolvedDownloadInput) (attachment.DownloadResult, error) {
						downloaded = true
						if input.CompanyID != companyID || input.ID != attachmentID ||
							!input.OutputAccessResolved {
							t.Fatalf("download authority = %+v", input)
						}
						return attachment.DownloadResult{
							Attachment: attachment.Response{
								Filename:    "final.txt",
								ContentType: "text/plain",
								SizeBytes:   5,
							},
							Body: io.NopCloser(strings.NewReader("hello")),
						}, nil
					},
				},
			}
			response := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(response)
			c.Request = httptest.NewRequest(
				http.MethodGet,
				"/public/artifacts/"+token.String()+"/items/0/download",
				nil,
			)
			c.Params = gin.Params{{Key: "token", Value: token.String()}, {Key: "ordinal", Value: "0"}}
			h.downloadPublicArtifact(c)
			if revoked {
				if response.Code != http.StatusNotFound || downloaded {
					t.Fatalf("revoked download = %d, fetched=%v", response.Code, downloaded)
				}
			} else if response.Code != http.StatusOK || response.Body.String() != "hello" || !downloaded || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatalf("public download = %d %s %v", response.Code, response.Body, response.Header())
			}
		})
	}
}

func TestArtifactVisibilityRejectsRemovedPrivateMutation(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`{"visibility":"private","expectedRevision":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), auth.UserPrincipal{UserID: uuid.New(), CompanyID: uuid.New(), SessionID: uuid.New(), AuthKind: auth.TokenKindSession}))
	c.Params = gin.Params{{Key: "artifactId", Value: uuid.NewString()}}
	h := Handler{}
	h.setArtifactVisibility(c)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusUnprocessableEntity, response.Body)
	}
}
