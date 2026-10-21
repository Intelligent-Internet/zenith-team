package handler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
	"github.com/Intelligent-Internet/zenith-team/server/internal/task"
)

type taskResultsServiceStub struct {
	*taskServiceStub
	finalize func(context.Context, auth.UserPrincipal, execution.RunOrigin, task.FinalizeResultsInput) (task.Response, error)
}

func (s *taskResultsServiceStub) FinalizeResults(
	ctx context.Context,
	p auth.UserPrincipal,
	origin execution.RunOrigin,
	in task.FinalizeResultsInput,
) (task.Response, error) {
	return s.finalize(ctx, p, origin, in)
}

func TestTaskResultsFinalizationIsRunBoundAndCannotApprove(t *testing.T) {
	t.Parallel()
	p := phase4Principal(uuid.New(), uuid.New())
	taskID, outputID, key := uuid.New(), uuid.New(), uuid.New()
	origin := execution.RunOrigin{
		RunID:              uuid.New(),
		CompanyID:          p.CompanyID,
		AgentID:            uuid.New(),
		TaskID:             &taskID,
		ConversationID:     uuid.New(),
		ConversationKind:   "task",
		AuthorizedByUserID: &p.UserID,
	}
	calls := 0
	service := &taskResultsServiceStub{
		taskServiceStub: &taskServiceStub{},
		finalize: func(_ context.Context, got auth.UserPrincipal, run execution.RunOrigin, in task.FinalizeResultsInput) (task.Response, error) {
			calls++
			if got != p || run != origin || in.ExpectedRevision != 1 || in.RequestKey != key ||
				len(in.OutputIDs) != 1 ||
				in.OutputIDs[0] != outputID {
				t.Fatalf("unexpected finalization: %+v %+v %+v", got, run, in)
			}
			return task.Response{ID: taskID, Status: task.StatusInProgress}, nil
		},
	}
	h := &Handler{TaskService: service, RunProofResolver: runProofResolverStub{origin: origin}}
	router := gin.New()
	router.Use(middleware.RequestID(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), p))
		c.Next()
	})
	router.POST("/companies/:companyId/tasks/:taskId/results", h.finalizeTaskResults)
	body := `{"expectedRevision":1,"outputIds":["` + outputID.String() + `"]}`
	for _, test := range []struct {
		name, body string
		target     uuid.UUID
		token      bool
		status     int
	}{
		{"valid", body, taskID, true, http.StatusOK},
		{"no proof", body, taskID, false, http.StatusUnauthorized},
		{"different Task", body, uuid.New(), true, http.StatusForbidden},
		{"approval injection", `{"expectedRevision":1,"outputIds":["` + outputID.String() + `"],"status":"done"}`, taskID, true, http.StatusBadRequest},
		{"policy injection", `{"expectedRevision":1,"outputIds":["` + outputID.String() + `"],"resultPolicy":{"reviewRequired":false,"proposalOnly":true}}`, taskID, true, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/companies/"+p.CompanyID.String()+"/tasks/"+test.target.String()+"/results",
				bytes.NewBufferString(test.body),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set(idempotencyHeader, key.String())
			if test.token {
				request.Header.Set(
					runTokenHeader,
					"brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d, body=%s", response.Code, response.Body.String())
			}
		})
	}
	if calls != 1 {
		t.Fatalf("finalization calls=%d", calls)
	}
}
