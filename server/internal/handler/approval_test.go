package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/company"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/social"
)

func TestRunTokenRaisesCompanyPlanApprovalAndQuestion(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	runID := uuid.MustParse("00000000-0000-4000-8000-000000004711")
	agentID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	requestKey := uuid.Must(uuid.NewV7())
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	origin := execution.RunOrigin{
		RunID: runID, CompanyID: companyID, AgentID: agentID,
		ConversationID: conversationID, OriginUserID: &userID,
		AuthorizedByUserID: &userID,
	}
	approvalID := uuid.Must(uuid.NewV7())
	questionID := uuid.Must(uuid.NewV7())
	approvalService := &approvalServiceStub{
		requestCompanyPlanApply: func(
			_ context.Context,
			gotPrincipal auth.UserPrincipal,
			gotOrigin execution.RunOrigin,
			gotKey uuid.UUID,
			payload json.RawMessage,
		) (approval.Approval, bool, error) {
			if gotPrincipal.UserID != principal.UserID || gotPrincipal.CompanyID != principal.CompanyID ||
				gotPrincipal.Role != principal.Role || gotOrigin.RunID != origin.RunID || gotKey != requestKey {
				t.Fatalf("request Approval scope = %+v / %+v / %s", gotPrincipal, gotOrigin, gotKey)
			}
			var artifact company.PlanArtifact
			if err := json.Unmarshal(payload, &artifact); err != nil || artifact.ArtifactVersion != 1 {
				t.Fatalf("request Approval payload = %s, %v", payload, err)
			}
			return approval.Approval{ID: approvalID}, true, nil
		},
	}
	questionService := &questionServiceStub{
		ask: func(
			_ context.Context,
			gotPrincipal auth.UserPrincipal,
			gotOrigin execution.RunOrigin,
			gotKey uuid.UUID,
			input approval.QuestionAskInput,
		) (approval.Question, bool, error) {
			if gotPrincipal.UserID != principal.UserID || gotPrincipal.CompanyID != principal.CompanyID ||
				gotPrincipal.Role != principal.Role || gotOrigin.RunID != origin.RunID || gotKey != requestKey ||
				input.Title != "Pick one" || len(input.Prompts) != 1 {
				t.Fatalf(
					"ask Question input = %+v / %+v / %s / %+v",
					gotPrincipal,
					gotOrigin,
					gotKey,
					input,
				)
			}
			return approval.Question{ID: questionID}, true, nil
		},
	}
	handler := hitlTestHandler(t, principal, origin, approvalService, questionService)

	planResponse := serveHITLJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/company-plan/apply",
		requestKey,
		[]byte(
			`{"artifactVersion":1,"companyId":"`+companyID.String()+`","companySlug":"acme","createdAt":"2026-08-19T00:00:00Z","base":{"tasks":[],"dependencies":[]},"desired":{"formatVersion":3,"company":"acme","tasks":[]}}`,
		),
	)
	if planResponse.Code != http.StatusAccepted {
		t.Fatalf("raise Approval = %d %s", planResponse.Code, planResponse.Body.String())
	}
	// The 202 body is the bare durable Approval — no {approval, created}
	// wrapper; created-ness is not signaled on this endpoint.
	var planReceipt approval.Approval
	if err := json.Unmarshal(planResponse.Body.Bytes(), &planReceipt); err != nil ||
		planReceipt.ID != approvalID {
		t.Fatalf("raise Approval receipt = %+v, %v", planReceipt, err)
	}
	var planWire map[string]json.RawMessage
	if err := json.Unmarshal(planResponse.Body.Bytes(), &planWire); err != nil {
		t.Fatalf("decode raise Approval wire object: %v", err)
	}
	for _, retired := range []string{"approval", "created"} {
		if _, exists := planWire[retired]; exists {
			t.Fatalf("raise Approval body carries retired key %q: %s", retired, planResponse.Body.String())
		}
	}

	questionResponse := serveHITLJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/questions",
		requestKey,
		[]byte(
			`{"title":"Pick one","prompts":[{"id":"target","prompt":"Which target?","selectionMode":"single","required":true,"options":[{"id":"a","label":"A"},{"id":"b","label":"B"}]}]}`,
		),
	)
	// First creation answers 201 with the bare durable Question; the status
	// code is the whole created-signal.
	if questionResponse.Code != http.StatusCreated {
		t.Fatalf("ask Question = %d %s", questionResponse.Code, questionResponse.Body.String())
	}
	var questionReceipt approval.Question
	if err := json.Unmarshal(questionResponse.Body.Bytes(), &questionReceipt); err != nil ||
		questionReceipt.ID != questionID {
		t.Fatalf("ask Question receipt = %+v, %v", questionReceipt, err)
	}
}

func TestHITLRaiseRequiresProofAndIdempotencyKey(t *testing.T) {
	t.Parallel()

	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	handler := hitlTestHandler(
		t,
		principal,
		execution.RunOrigin{CompanyID: companyID},
		&approvalServiceStub{},
		&questionServiceStub{},
	)
	path := "/api/v1/companies/" + companyID.String() + "/questions"
	body := []byte(`{"title":"Pick one","prompts":[]}`)

	missingRequest := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	missingRequest.Header.Set("Authorization", "Bearer access-token")
	missingRequest.Header.Set("Content-Type", "application/json")
	missingRequest.Header.Set(
		"X-Zenith-Team-Run-Token",
		"brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	)
	missingKey := httptest.NewRecorder()
	handler.ServeHTTP(missingKey, missingRequest)
	assertErrorResponse(t, missingKey, http.StatusUnprocessableEntity, "validation_failed")

	request := httptest.NewRequest(http.MethodPost, path, nil)
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("X-Zenith-Team-Run-Token", "not-a-run-token")
	request.Header.Set("Idempotency-Key", uuid.Must(uuid.NewV7()).String())
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	assertErrorResponse(t, response, http.StatusUnauthorized, "run_token_invalid")
}

// The CLI's `question get`, and the pre-read `answer` and `withdraw` do,
// read one Question by id through the same visibility rule as the list; the
// route answers the bare Question and `404 question_not_found` for an id
// the caller cannot see or that is not a UUID.
func TestGetQuestionReadsOneQuestionByID(t *testing.T) {
	t.Parallel()

	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	questionID := uuid.Must(uuid.NewV7())
	hidden := uuid.Must(uuid.NewV7())
	questionService := &questionServiceStub{
		get: func(_ context.Context, gotPrincipal auth.UserPrincipal, id uuid.UUID) (approval.Question, error) {
			if gotPrincipal.UserID != principal.UserID || gotPrincipal.CompanyID != principal.CompanyID {
				t.Fatalf("get Question principal = %+v", gotPrincipal)
			}
			if id != questionID {
				return approval.Question{}, approval.ErrQuestionNotFound
			}
			return approval.Question{
				ID: questionID, CompanyID: companyID, Title: "Pick one", Revision: 2,
			}, nil
		},
	}
	handler := hitlTestHandler(
		t,
		principal,
		execution.RunOrigin{CompanyID: companyID},
		&approvalServiceStub{},
		questionService,
	)
	base := "/api/v1/companies/" + companyID.String() + "/questions/"

	request := httptest.NewRequest(http.MethodGet, base+questionID.String(), nil)
	request.Header.Set("Authorization", "Bearer access-token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("get Question = %d %s", response.Code, response.Body.String())
	}
	var got approval.Question
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || got.ID != questionID || got.Revision != 2 {
		t.Fatalf("get Question body = %s, %v", response.Body.String(), err)
	}

	for _, id := range []string{hidden.String(), "not-a-uuid"} {
		request := httptest.NewRequest(http.MethodGet, base+id, nil)
		request.Header.Set("Authorization", "Bearer access-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		assertErrorResponse(t, response, http.StatusNotFound, "question_not_found")
	}
}

func serveHITLJSON(
	handler http.Handler,
	method string,
	path string,
	requestKey uuid.UUID,
	body []byte,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(
		"X-Zenith-Team-Run-Token",
		"brt_"+"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	)
	request.Header.Set("Idempotency-Key", requestKey.String())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

type runProofResolverStub struct {
	origin execution.RunOrigin
}

func (s runProofResolverStub) ResolveRunOrigin(_ context.Context, token string) (execution.RunOrigin, error) {
	if s.origin.CompanyID == uuid.Nil ||
		token != "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		return execution.RunOrigin{}, execution.ErrRunProofInvalid
	}
	return s.origin, nil
}

type approvalServiceStub struct {
	requestCompanyPlanApply func(
		context.Context,
		auth.UserPrincipal,
		execution.RunOrigin,
		uuid.UUID,
		json.RawMessage,
	) (approval.Approval, bool, error)
}

func (s *approvalServiceStub) RequestCompanyPlanApply(
	ctx context.Context,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	key uuid.UUID,
	payload json.RawMessage,
) (approval.Approval, bool, error) {
	if s.requestCompanyPlanApply == nil {
		return approval.Approval{}, false, errors.New("unexpected RequestCompanyPlanApply")
	}
	return s.requestCompanyPlanApply(ctx, principal, origin, key, payload)
}

func (*approvalServiceStub) List(
	context.Context,
	auth.UserPrincipal,
	approval.ApprovalListOptions,
) (approval.ApprovalPage, error) {
	return approval.ApprovalPage{}, errors.New("unexpected List")
}

func (*approvalServiceStub) Get(context.Context, auth.UserPrincipal, uuid.UUID) (approval.Approval, error) {
	return approval.Approval{}, errors.New("unexpected Get")
}

func (*approvalServiceStub) Stats(
	context.Context,
	auth.UserPrincipal,
	approval.ApprovalStatsOptions,
) (approval.ApprovalStats, error) {
	return approval.ApprovalStats{}, errors.New("unexpected Stats")
}

func (*approvalServiceStub) Approve(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	approval.ApprovalDecisionInput,
) (approval.Approval, error) {
	return approval.Approval{}, errors.New("unexpected Approve")
}

func (*approvalServiceStub) Reject(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	approval.ApprovalDecisionInput,
) (approval.Approval, error) {
	return approval.Approval{}, errors.New("unexpected Reject")
}

func (*approvalServiceStub) Cancel(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	approval.ApprovalDecisionInput,
) (approval.Approval, error) {
	return approval.Approval{}, errors.New("unexpected Cancel")
}

type questionServiceStub struct {
	ask func(context.Context, auth.UserPrincipal, execution.RunOrigin, uuid.UUID, approval.QuestionAskInput) (approval.Question, bool, error)
	get func(context.Context, auth.UserPrincipal, uuid.UUID) (approval.Question, error)
}

func (s *questionServiceStub) Ask(
	ctx context.Context,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	key uuid.UUID,
	input approval.QuestionAskInput,
) (approval.Question, bool, error) {
	if s.ask == nil {
		return approval.Question{}, false, errors.New("unexpected Ask")
	}
	return s.ask(ctx, principal, origin, key, input)
}

func (*questionServiceStub) List(
	context.Context,
	auth.UserPrincipal,
	approval.QuestionListOptions,
) (approval.QuestionPage, error) {
	return approval.QuestionPage{}, errors.New("unexpected List")
}

func (s *questionServiceStub) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
) (approval.Question, error) {
	if s.get == nil {
		return approval.Question{}, errors.New("unexpected Get")
	}
	return s.get(ctx, principal, id)
}

func (*questionServiceStub) Answer(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	approval.QuestionAnswerInput,
) (approval.Question, error) {
	return approval.Question{}, errors.New("unexpected Answer")
}

func (*questionServiceStub) Withdraw(context.Context, auth.UserPrincipal, uuid.UUID, int64) (approval.Question, error) {
	return approval.Question{}, errors.New("unexpected Withdraw")
}

func hitlTestHandler(
	t *testing.T,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	approvals approval.ApprovalService,
	questions approval.QuestionService,
) http.Handler {
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
	return New(Handler{
		AuthService:        authenticatedAuthStub(principal.UserID),
		CompanyService:     companies,
		CompanyPlanService: &companyPlanServiceStub{},
		ApprovalService:    approvals,
		QuestionService:    questions,
		RunProofResolver:   runProofResolverStub{origin: origin},
	})
}

func TestAgentCannotAnswerOwnQuestion(t *testing.T) {
	t.Parallel()
	p := auth.UserPrincipal{
		UserID:    uuid.New(),
		SessionID: uuid.New(),
		CompanyID: uuid.New(),
		AuthKind:  auth.TokenKindBearer,
	}
	h := hitlTestHandler(t, p, execution.RunOrigin{}, &approvalServiceStub{}, &questionServiceStub{})
	response := serveHITLJSON(
		h,
		http.MethodPost,
		"/api/v1/companies/"+p.CompanyID.String()+"/questions/"+uuid.NewString()+"/answer",
		uuid.New(),
		[]byte(`{"expectedRevision":1,"answers":[]}`),
	)
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "human_actor_required") {
		t.Fatalf(
			"answer with Run proof = %d %s, want human actor required",
			response.Code,
			response.Body.String(),
		)
	}
}

type questionSocialPreviewStub struct{ social.SocialService }

func (questionSocialPreviewStub) PreviewForRun(
	_ context.Context,
	_ auth.UserPrincipal,
	_ social.RunContext,
	input social.PostInput,
) (approval.QuestionSocialPost, error) {
	return approval.QuestionSocialPost{
		ConnectionID: input.ConnectionID, TargetID: input.TargetID, Text: input.Text,
		Platform: "x", TargetName: "Verified account", ConnectionRevision: 7,
	}, nil
}

func TestAskSocialQuestionUsesResolvedPreview(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"direct", "task", "schedule"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			p := auth.UserPrincipal{
				UserID:    uuid.New(),
				CompanyID: uuid.New(),
				SessionID: uuid.New(),
				AuthKind:  auth.TokenKindBearer,
			}
			origin := execution.RunOrigin{
				RunID:              uuid.New(),
				CompanyID:          p.CompanyID,
				AgentID:            uuid.New(),
				ConversationID:     uuid.New(),
				ConversationKind:   kind,
				OriginUserID:       &p.UserID,
				AuthorizedByUserID: &p.UserID,
			}
			stored := false
			h := &Handler{
				SocialService:    questionSocialPreviewStub{},
				RunProofResolver: runProofResolverStub{origin: origin},
				QuestionService: &questionServiceStub{
					ask: func(_ context.Context, _ auth.UserPrincipal, _ execution.RunOrigin, _ uuid.UUID, input approval.QuestionAskInput) (approval.Question, bool, error) {
						stored = true
						preview := input.Prompts[0].SocialPost
						if preview.Platform != "x" ||
							preview.TargetName != "Verified account" ||
							preview.ConnectionRevision != 7 ||
							preview.Text != "Reviewed text" {
							t.Fatalf(
								"stored preview = %+v, want authoritative account with exact draft",
								preview,
							)
						}
						return approval.Question{
							ID:      uuid.New(),
							Prompts: input.Prompts,
						}, true, nil
					},
				},
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"title":"Review","prompts":[{"id":"post","prompt":"Publish?","required":true,"selectionMode":"single","socialPost":{"connectionId":"`+uuid.NewString()+`","targetId":"`+uuid.NewString()+`","text":"Reviewed text","targetName":"Forged account","platform":"instagram","connectionRevision":999}}]}`)).
				WithContext(auth.WithUserPrincipal(t.Context(), p))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set(
				runTokenHeader,
				"brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			)
			c.Request.Header.Set(idempotencyHeader, uuid.NewString())
			h.askQuestion(c)
			want := http.StatusUnprocessableEntity
			if kind == "direct" {
				want = http.StatusCreated
			}
			if w.Code != want || stored != (kind == "direct") {
				t.Fatalf("ask = %d %s, stored=%v", w.Code, w.Body.String(), stored)
			}
		})
	}
}
