package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/execution"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type runArtifactStub struct {
	deliverableDownloadStub
	reads int
}

func (s *runArtifactStub) ReadArtifactForRun(
	_ context.Context,
	_ auth.UserPrincipal,
	_ execution.RunOrigin,
	id uuid.UUID,
) (execution.DeliverableOutput, error) {
	s.reads++
	return execution.DeliverableOutput{ID: id, Items: []execution.DeliverableOutputItem{}}, nil
}

func TestArtifactRunRequiresLiveBearerBoundProof(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"valid", "missing proof", "wrong run", "other user", "schedule", "revoked"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			userID, runID, companyID := uuid.New(), uuid.New(), uuid.New()
			p := auth.UserPrincipal{
				UserID:    userID,
				CompanyID: companyID,
				SessionID: uuid.New(),
				AuthKind:  auth.TokenKindBearer,
			}
			origin := execution.RunOrigin{
				RunID:              runID,
				CompanyID:          companyID,
				AuthorizedByUserID: &userID,
				ConversationKind:   "direct",
			}
			token := "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
			switch scenario {
			case "missing proof":
				token = ""
			case "wrong run":
				origin.RunID = uuid.New()
			case "other user":
				other := uuid.New()
				origin.AuthorizedByUserID = &other
			case "schedule":
				origin.ConversationKind = "schedule"
			case "revoked":
				token = "expired"
			}
			stub := &runArtifactStub{}
			h := &Handler{RunProofResolver: runProofResolverStub{origin: origin}, DeliverableService: stub}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), p))
			})
			router.GET("/runs/:runId/artifacts/:artifactId", h.inspectArtifactForRun)
			request := httptest.NewRequest(
				http.MethodGet,
				"/runs/"+runID.String()+"/artifacts/"+uuid.NewString(),
				nil,
			)
			request.Header.Set(runTokenHeader, token)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			want := http.StatusUnauthorized
			if scenario == "valid" {
				want = http.StatusOK
			}
			if response.Code != want || (stub.reads > 0) != (scenario == "valid") {
				t.Fatalf(
					"response = %d %s, reads=%d; want %d",
					response.Code,
					response.Body.String(),
					stub.reads,
					want,
				)
			}
		})
	}
}
