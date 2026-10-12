package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/pkg/studiocanvas"
)

func TestPresentRunOutputResolvesCompanyFromProof(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"authorizer", "other member", "missing authorizer", "revoked membership"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			userID, companyID, runID := uuid.New(), uuid.New(), uuid.New()
			principal := auth.UserPrincipal{UserID: userID, SessionID: uuid.New(), AuthKind: auth.TokenKindBearer}
			origin := execution.RunOrigin{RunID: runID, CompanyID: companyID, AgentID: uuid.New(), AuthorizedByUserID: &userID}
			want := http.StatusCreated
			switch scenario {
			case "other member":
				other := uuid.New()
				origin.AuthorizedByUserID, want = &other, http.StatusForbidden
			case "missing authorizer":
				origin.AuthorizedByUserID, want = nil, http.StatusForbidden
			case "revoked membership":
				want = http.StatusNotFound
			}
			presented := false
			h := &Handler{
				AuthService: &authServiceStub{authenticate: func(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error) {
					return principal, nil
				}},
				CompanyService: &companyServiceStub{authorize: func(_ context.Context, got auth.UserPrincipal, id uuid.UUID) (auth.UserPrincipal, error) {
					if scenario == "other member" || scenario == "missing authorizer" {
						t.Fatal("foreign Run proof reached Company authorization")
					}
					if got != principal || id != companyID {
						t.Fatalf("Company authorization = %+v, %s", got, id)
					}
					if scenario == "revoked membership" {
						return auth.UserPrincipal{}, company.ErrNotFound
					}
					got.CompanyID, got.Role = companyID, auth.CompanyRoleMember
					return got, nil
				}},
				RunProofResolver: runProofResolverStub{origin: origin},
				DeliverableService: &deliverableDownloadStub{present: func(_ context.Context, got auth.UserPrincipal, proof execution.RunOrigin, _ execution.DeliverablePresentRequest) (execution.DeliverablePresentResult, error) {
					presented = true
					if got.UserID != userID || got.CompanyID != companyID || got.Role != auth.CompanyRoleMember || proof.RunID != runID {
						t.Fatalf("presentation scope = %+v / %+v", got, proof)
					}
					return execution.DeliverablePresentResult{OutputID: uuid.New(), Created: true}, nil
				}},
			}
			router := gin.New()
			h.registerDeliverableRoutes(router.Group("/api/v1"))
			body := []byte(`{"type":"text","title":"Review","summary":"Review findings","items":[{"role":"primary","label":"Report","assetVersionId":"00000000-0000-4000-8000-000000000071"}]}`)
			response := serveHITLJSON(router, http.MethodPost, "/api/v1/runs/"+runID.String()+"/output", uuid.New(), body)
			if response.Code != want || presented != (want == http.StatusCreated) {
				t.Fatalf("presentation = %d, called %v, body %s; want %d", response.Code, presented, response.Body.String(), want)
			}
		})
	}
}

func TestDownloadArtifactItemPreservesResolvedAuthority(t *testing.T) {
	t.Parallel()
	attachmentID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000911")
	companyID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000912")
	userID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000913")
	payload := []byte("reviewable output")
	artifactID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000914")
	deliverables := &deliverableDownloadStub{
		resolve: func(
			_ context.Context,
			principal auth.UserPrincipal,
			runID, gotArtifactID uuid.UUID,
			ordinal int,
		) (uuid.UUID, error) {
			if principal.UserID != userID || principal.CompanyID != companyID ||
				runID != uuid.MustParse("00000000-0000-4000-8000-000000000071") ||
				gotArtifactID != artifactID ||
				ordinal != 0 {
				t.Fatalf(
					"ResolveArtifactFileAttachment() = principal %+v, run %s, artifact %s, ordinal %d",
					principal,
					runID,
					gotArtifactID,
					ordinal,
				)
			}
			return attachmentID, nil
		},
	}
	attachments := &attachmentServiceStub{
		download: func(context.Context, attachment.DownloadInput) (attachment.DownloadResult, error) {
			t.Fatal("result download repeated generic attachment authorization")
			return attachment.DownloadResult{}, nil
		},
		downloadResolved: func(_ context.Context, input attachment.ResolvedDownloadInput) (attachment.DownloadResult, error) {
			if input.ID != attachmentID || input.CompanyID != companyID {
				t.Fatalf("DownloadResolved() input = %+v", input)
			}
			return attachment.DownloadResult{
				Attachment: attachment.Response{
					ID:               attachmentID,
					CompanyID:        companyID,
					UploadedByUserID: uuid.New(),
					Filename:         "result.txt",
					ContentType:      "text/plain",
					SizeBytes:        int64(len(payload)),
					Status:           attachment.PublicStatusReady,
					CreatedAt:        time.Now(),
					UpdatedAt:        time.Now(),
					Revision:         2,
				},
				Body: io.NopCloser(bytes.NewReader(payload)),
			}, nil
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{
		{Key: "runId", Value: "00000000-0000-4000-8000-000000000071"},
		{Key: "artifactId", Value: artifactID.String()},
		{Key: "ordinal", Value: "0"},
	}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/companies/company/runs/00000000-0000-4000-8000-000000000071/artifacts/"+
			artifactID.String()+"/items/0/download",
		nil,
	)
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.New(), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	c.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), principal))

	(&Handler{DeliverableService: deliverables, AttachmentService: attachments}).downloadArtifactItem(c)

	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), payload) {
		t.Fatalf("download response = %d / %q", recorder.Code, recorder.Body.Bytes())
	}
}

func TestShareArtifactItemPreservesResolvedAuthority(t *testing.T) {
	t.Parallel()
	attachmentID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000921")
	companyID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000922")
	userID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000923")
	artifactID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000924")
	runID := uuid.MustParse("00000000-0000-4000-8000-000000000072")
	deliverables := &deliverableDownloadStub{
		resolve: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotRunID, gotArtifactID uuid.UUID,
			ordinal int,
		) (uuid.UUID, error) {
			if principal.UserID != userID || principal.CompanyID != companyID ||
				gotRunID != runID || gotArtifactID != artifactID || ordinal != 2 {
				t.Fatalf(
					"ResolveArtifactFileAttachment() = principal %+v, run %s, artifact %s, ordinal %d",
					principal,
					gotRunID,
					gotArtifactID,
					ordinal,
				)
			}
			return attachmentID, nil
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{
		{Key: "runId", Value: runID.String()},
		{Key: "artifactId", Value: artifactID.String()},
		{Key: "ordinal", Value: "2"},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/companies/company/runs/"+runID.String()+"/artifacts/"+
			artifactID.String()+"/items/2/share",
		nil,
	)
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.New(), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleMember,
	}
	c.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), principal))

	(&Handler{DeliverableService: deliverables, OAuthWebAppOrigin: "https://zenith.test"}).shareArtifactItem(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("share response = %d / %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
	var response artifactShareResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode share response: %v", err)
	}
	if response.URL != "https://zenith.test/companies/"+companyID.String()+
		"/artifacts/"+artifactID.String()+"?item=2" {
		t.Fatalf("share response = %+v", response)
	}
}

func TestFetchRunResultSelectsRequestedArtifact(t *testing.T) {
	t.Parallel()
	runID := uuid.MustParse("00000000-0000-4000-8000-000000000071")
	artifactID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000914")
	companyID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000912")
	userID := uuid.MustParse("01990d25-3d7a-7000-8000-000000000913")
	deliverables := &deliverableDownloadStub{
		fetchArtifact: func(
			_ context.Context,
			principal auth.UserPrincipal,
			gotRunID uuid.UUID,
			gotArtifactID uuid.UUID,
		) (execution.DeliverableRunResult, error) {
			if principal.UserID != userID || principal.CompanyID != companyID ||
				gotRunID != runID || gotArtifactID != artifactID {
				t.Fatalf(
					"FetchRunArtifactResult() = principal %+v, run %s, artifact %s",
					principal,
					gotRunID,
					gotArtifactID,
				)
			}
			return execution.DeliverableRunResult{RunStatus: "completed"}, nil
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Params = gin.Params{{Key: "runId", Value: runID.String()}}
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/companies/company/runs/"+runID.String()+
			"/result?artifactId="+artifactID.String(),
		nil,
	)
	principal := auth.UserPrincipal{
		UserID: userID, SessionID: uuid.New(), AuthKind: auth.TokenKindBearer,
		CompanyID: companyID, Role: auth.CompanyRoleOwner,
	}
	c.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), principal))

	(&Handler{DeliverableService: deliverables}).fetchRunResult(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("fetch result response = %d / %q", recorder.Code, recorder.Body.String())
	}
}

func TestBindDeliverablePresentRequestSeparatesAppCommandsFromFileItems(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		body    string
		wantErr bool
	}{
		"app commands": {
			body: `{"type":"app","app":{"commands":[{"command":"npm","args":["run","dev"],"port":5173}]},"title":"App","summary":"Ready"}`,
		},
		"app without commands": {
			body:    `{"type":"app","title":"App","summary":"Ready"}`,
			wantErr: true,
		},
		"app with file items": {
			body:    `{"type":"app","app":{"commands":[{"command":"npm","port":5173}]},"title":"App","summary":"Ready","items":[{"role":"primary","label":"App","assetVersionId":"00000000-0000-4000-8000-000000000071"}]}`,
			wantErr: true,
		},
		"file items": {
			body: `{"type":"file","title":"Files","summary":"Ready","items":[{"role":"primary","label":"File","assetVersionId":"00000000-0000-4000-8000-000000000071"}]}`,
		},
		"file with app commands": {
			body:    `{"type":"file","app":{"commands":[{"command":"npm","port":5173}]},"title":"Files","summary":"Ready","items":[{"role":"primary","label":"File","assetVersionId":"00000000-0000-4000-8000-000000000071"}]}`,
			wantErr: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(test.body))
			c.Request.Header.Set("Content-Type", "application/json")

			_, err := bindRequest[deliverablePresentRequest](c, maxDeliverableBodyBytes)
			if (err != nil) != test.wantErr {
				t.Fatalf("bindRequest() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

type deliverableDownloadStub struct {
	present       func(context.Context, auth.UserPrincipal, execution.RunOrigin, execution.DeliverablePresentRequest) (execution.DeliverablePresentResult, error)
	fetchArtifact func(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
	) (execution.DeliverableRunResult, error)
	resolve func(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		uuid.UUID,
		int,
	) (uuid.UUID, error)
}

func (*deliverableDownloadStub) GetArtifact(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) (execution.ArtifactDetail, error) {
	return execution.ArtifactDetail{}, errors.New("unexpected GetArtifact call")
}

func (*deliverableDownloadStub) ListUnownedArtifacts(
	context.Context,
	auth.UserPrincipal,
) ([]execution.UnownedArtifact, error) {
	return nil, errors.New("unexpected ListUnownedArtifacts call")
}

func (*deliverableDownloadStub) ClaimUnownedArtifact(
	context.Context, auth.UserPrincipal, uuid.UUID, int64,
) (execution.ArtifactDetail, error) {
	return execution.ArtifactDetail{}, errors.New("unexpected ClaimUnownedArtifact call")
}

func (*deliverableDownloadStub) ChangeArtifactGrant(
	context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, *execution.ArtifactPermission, int64,
) (execution.ArtifactAccess, error) {
	return execution.ArtifactAccess{}, errors.New("unexpected ChangeArtifactGrant call")
}

func (*deliverableDownloadStub) ListTaskResults(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	execution.TaskResultListOptions,
) (execution.TaskResultPage, error) {
	return execution.TaskResultPage{}, errors.New("unexpected ListTaskResults call")
}

func (*deliverableDownloadStub) ListConversationArtifacts(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	execution.ArtifactListOptions,
) (execution.ConversationArtifactPage, error) {
	return execution.ConversationArtifactPage{}, errors.New("unexpected ListConversationArtifacts call")
}

func (*deliverableDownloadStub) ListArtifacts(
	context.Context,
	auth.UserPrincipal,
	execution.CompanyArtifactListOptions,
) (execution.ArtifactPage, error) {
	return execution.ArtifactPage{}, errors.New("unexpected ListArtifacts call")
}

func (*deliverableDownloadStub) ResolveArtifacts(
	context.Context,
	auth.UserPrincipal,
	[]uuid.UUID,
) ([]execution.ArtifactRunResult, error) {
	return nil, errors.New("unexpected ResolveArtifacts call")
}

func (s *deliverableDownloadStub) Present(
	ctx context.Context,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	request execution.DeliverablePresentRequest,
) (execution.DeliverablePresentResult, error) {
	if s.present != nil {
		return s.present(ctx, principal, origin, request)
	}
	return execution.DeliverablePresentResult{}, errors.New("unexpected Present call")
}

func (*deliverableDownloadStub) FetchRunResult(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) (execution.DeliverableRunResult, error) {
	return execution.DeliverableRunResult{}, errors.New("unexpected FetchRunResult call")
}

func (s *deliverableDownloadStub) FetchRunArtifactResult(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	artifactID uuid.UUID,
) (execution.DeliverableRunResult, error) {
	if s.fetchArtifact == nil {
		return execution.DeliverableRunResult{}, errors.New("unexpected FetchRunArtifactResult call")
	}
	return s.fetchArtifact(ctx, principal, runID, artifactID)
}

func (s *deliverableDownloadStub) ResolveArtifactFileAttachment(
	ctx context.Context,
	principal auth.UserPrincipal,
	runID uuid.UUID,
	artifactID uuid.UUID,
	ordinal int,
) (uuid.UUID, error) {
	return s.resolve(ctx, principal, runID, artifactID, ordinal)
}

func (*deliverableDownloadStub) AttachStudioCanvas(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	studiocanvas.Canvas,
) (execution.SavedStudioCanvas, error) {
	return execution.SavedStudioCanvas{}, errors.New("unexpected AttachStudioCanvas call")
}

func (*deliverableDownloadStub) SetArtifactVisibility(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	execution.ArtifactVisibilityInput,
) (execution.ArtifactAccess, error) {
	return execution.ArtifactAccess{}, execution.ErrDeliverableNotFound
}

func (*deliverableDownloadStub) RevokeArtifactConversation(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
	execution.ArtifactConversationInput,
) (execution.ArtifactAccess, error) {
	return execution.ArtifactAccess{}, execution.ErrDeliverableNotFound
}

func (*deliverableDownloadStub) GetPublicArtifact(context.Context, uuid.UUID) (execution.PublicArtifact, error) {
	return execution.PublicArtifact{}, execution.ErrDeliverableNotFound
}

func (*deliverableDownloadStub) ResolvePublicArtifactFile(
	context.Context,
	uuid.UUID,
	int,
) (uuid.UUID, uuid.UUID, error) {
	return uuid.Nil, uuid.Nil, execution.ErrDeliverableNotFound
}

func (*deliverableDownloadStub) ReadArtifactForRun(context.Context, auth.UserPrincipal, execution.RunOrigin, uuid.UUID) (execution.DeliverableOutput, error) {
	return execution.DeliverableOutput{}, nil
}
