package handler

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestTaskFailureUsesOnlyPrincipalRunProofAndReason(t *testing.T) {
	t.Parallel()

	principal := phase4Principal(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	taskID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	origin := execution.RunOrigin{
		RunID: uuid.MustParse(
			"00000000-0000-4000-8000-000000000041",
		),
		CompanyID:          principal.CompanyID,
		AgentID:            agentID,
		TaskID:             &taskID,
		ConversationID:     uuid.Must(uuid.NewV7()),
		ConversationKind:   "task",
		AuthorizedByUserID: &principal.UserID,
	}
	requestKey := uuid.Must(uuid.NewV7())
	want := phase4TaskResponse(taskID, principal.CompanyID, origin.ConversationID, agentID, principal.UserID)
	want.Status = task.StatusFailed
	service := &taskServiceStub{
		markFailed: func(
			_ context.Context,
			gotPrincipal auth.UserPrincipal,
			gotOrigin execution.RunOrigin,
			input task.FailureInput,
		) (task.Response, error) {
			if gotPrincipal != principal || gotOrigin != origin || input.RequestKey != requestKey ||
				input.Reason != "Supplier policy forbids delivery" {
				t.Fatalf("MarkFailed input = %+v / %+v / %+v", gotPrincipal, gotOrigin, input)
			}
			return want, nil
		},
	}
	handler := &Handler{
		TaskService:      service,
		RunProofResolver: runProofResolverStub{origin: origin},
	}
	router := taskFailureTestRouter(principal, handler)
	request := httptest.NewRequest(
		http.MethodPost,
		"/companies/"+principal.CompanyID.String()+"/task-failures",
		bytes.NewBufferString(`{"reason":"Supplier policy forbids delivery"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(runTokenHeader, "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	request.Header.Set(idempotencyHeader, requestKey.String())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var got taskFailureResponse
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil ||
		got.Status != taskFailureStatusFailed || got.Task.ID != taskID {
		t.Fatalf("response = %+v, %v", got, err)
	}
}

func TestTaskFailureRejectsUnprovedOrMalformedRequests(t *testing.T) {
	t.Parallel()

	principal := phase4Principal(uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()))
	taskID := uuid.Must(uuid.NewV7())
	origin := execution.RunOrigin{
		RunID: uuid.MustParse(
			"00000000-0000-4000-8000-000000000042",
		),
		CompanyID:          principal.CompanyID,
		AgentID:            uuid.Must(uuid.NewV7()),
		TaskID:             &taskID,
		ConversationID:     uuid.Must(uuid.NewV7()),
		ConversationKind:   "task",
		AuthorizedByUserID: &principal.UserID,
	}
	service := &taskServiceStub{
		markFailed: func(
			context.Context,
			auth.UserPrincipal,
			execution.RunOrigin,
			task.FailureInput,
		) (task.Response, error) {
			return task.Response{}, task.ErrInvalidInput
		},
	}

	tests := []struct {
		name       string
		origin     execution.RunOrigin
		token      string
		requestKey string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing run token",
			origin:     origin,
			requestKey: uuid.Must(uuid.NewV7()).String(),
			body:       `{"reason":"blocked"}`,
			wantStatus: http.StatusUnauthorized,
			wantCode:   "run_token_invalid",
		},
		{
			name:       "missing idempotency key",
			origin:     origin,
			token:      "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			body:       `{"reason":"blocked"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "validation_failed",
		},
		{
			name: "foreign company proof", origin: execution.RunOrigin{
				RunID: origin.RunID, CompanyID: uuid.Must(uuid.NewV7()), AgentID: origin.AgentID,
				TaskID: origin.TaskID, ConversationID: origin.ConversationID, ConversationKind: "task",
				AuthorizedByUserID: origin.AuthorizedByUserID,
			},
			token:      "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			requestKey: uuid.Must(uuid.NewV7()).String(), body: `{"reason":"blocked"}`,
			wantStatus: http.StatusUnauthorized, wantCode: "run_token_invalid",
		},
		{
			name:   "unknown body field",
			origin: origin,
			token:  "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			requestKey: uuid.Must(uuid.NewV7()).
				String(),
			body:       `{"reason":"blocked","taskId":"` + taskID.String() + `"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name: "blank reason", origin: origin,
			token:      "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			requestKey: uuid.Must(uuid.NewV7()).String(), body: `{"reason":"   "}`,
			wantStatus: http.StatusUnprocessableEntity, wantCode: "validation_failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := &Handler{
				TaskService:      service,
				RunProofResolver: runProofResolverStub{origin: test.origin},
			}
			router := taskFailureTestRouter(principal, handler)
			request := httptest.NewRequest(
				http.MethodPost,
				"/companies/"+principal.CompanyID.String()+"/task-failures",
				bytes.NewBufferString(test.body),
			)
			request.Header.Set("Content-Type", "application/json")
			if test.token != "" {
				request.Header.Set(runTokenHeader, test.token)
			}
			if test.requestKey != "" {
				request.Header.Set(idempotencyHeader, test.requestKey)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertErrorResponse(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func taskFailureTestRouter(principal auth.UserPrincipal, handler *Handler) http.Handler {
	router := gin.New()
	router.Use(middleware.RequestID(), func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithUserPrincipal(c.Request.Context(), principal))
		c.Next()
	})
	router.POST("/companies/:companyId/task-failures", handler.markTaskFailed)
	return router
}
