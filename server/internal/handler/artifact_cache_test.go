package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
)

func TestArtifactDownloadRevalidatesBeforeUsingCache(t *testing.T) {
	t.Parallel()
	attachmentID, companyID, artifactID, runID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, route := range []string{"run", "artifact"} {
		for _, scenario := range []string{"cold", "unchanged", "changed", "other attachment", "denied", "web"} {
			t.Run(route+"/"+scenario, func(t *testing.T) {
				etag := `"attachment:` + attachmentID.String() + `:2"`
				cachedTag := etag
				if scenario == "changed" {
					cachedTag = `"attachment:` + attachmentID.String() + `:1"`
				}
				if scenario == "other attachment" {
					cachedTag = `"attachment:` + uuid.NewString() + `:2"`
				}
				if scenario == "cold" {
					cachedTag = ""
				}
				resolved := false
				deliverables := &artifactCacheDeliverableStub{
					runID: runID,
					deliverableDownloadStub: deliverableDownloadStub{
						resolve: func(context.Context, auth.UserPrincipal, uuid.UUID, uuid.UUID, int) (uuid.UUID, error) {
							resolved = true
							if scenario == "denied" {
								return uuid.Nil, execution.ErrDeliverableNotFound
							}
							return attachmentID, nil
						},
					},
				}
				attachments := &attachmentServiceStub{
					downloadResolved: func(_ context.Context, input attachment.ResolvedDownloadInput) (attachment.DownloadResult, error) {
						if !resolved || !input.OutputAccessResolved ||
							input.ID != attachmentID ||
							input.CompanyID != companyID {
							t.Fatal("file was read without resolved authority")
						}
						wantRevision := int64(0)
						if scenario == "unchanged" {
							wantRevision = 2
						}
						if scenario == "changed" {
							wantRevision = 1
						}
						if input.KnownRevision != wantRevision {
							t.Fatalf(
								"known revision = %d, want %d",
								input.KnownRevision,
								wantRevision,
							)
						}
						result := attachment.DownloadResult{Attachment: attachment.Response{
							ID:          attachmentID,
							Revision:    2,
							Filename:    "file.txt",
							ContentType: "text/plain",
							SizeBytes:   4,
						}, NotModified: input.KnownRevision == 2}
						if !result.NotModified {
							result.Body = io.NopCloser(bytes.NewBufferString("file"))
						}
						return result, nil
					},
				}
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Params = gin.Params{
					{Key: "runId", Value: runID.String()},
					{Key: "artifactId", Value: artifactID.String()},
					{Key: "ordinal", Value: "0"},
				}
				request := httptest.NewRequest(http.MethodGet, "/download", nil)
				request.Header.Set("If-None-Match", cachedTag)
				if scenario != "web" {
					request.Header.Set("X-Zenith-Team-Artifact-Cache", "revalidate")
				}
				c.Request = request.WithContext(
					auth.WithUserPrincipal(request.Context(), auth.UserPrincipal{
						UserID:    uuid.New(),
						SessionID: uuid.New(),
						CompanyID: companyID,
						AuthKind:  auth.TokenKindBearer,
						Role:      auth.CompanyRoleMember,
					}),
				)
				h := &Handler{DeliverableService: deliverables, AttachmentService: attachments}
				if route == "run" {
					h.downloadArtifactItem(c)
				} else {
					h.downloadArtifactFile(c)
				}
				c.Writer.WriteHeaderNow()
				wantStatus := http.StatusOK
				if scenario == "unchanged" {
					wantStatus = http.StatusNotModified
				}
				if scenario == "denied" {
					wantStatus = http.StatusNotFound
				}
				if recorder.Code != wantStatus {
					t.Fatalf(
						"status = %d, want %d: %s",
						recorder.Code,
						wantStatus,
						recorder.Body.String(),
					)
				}
				if scenario == "denied" {
					return
				}
				if scenario == "unchanged" && recorder.Body.Len() != 0 {
					t.Fatal("304 must not contain bytes")
				}
				if wantStatus == http.StatusOK && recorder.Body.String() != "file" {
					t.Fatal("missing download body")
				}
				if scenario == "web" {
					if recorder.Header().Get("Cache-Control") != "private, no-store" {
						t.Fatal("web caching changed")
					}
				} else if recorder.Header().Get("ETag") != etag || recorder.Header().Get("Cache-Control") != "private, no-cache" {
					t.Fatalf("missing revalidation headers: %v", recorder.Header())
				}
			})
		}
	}
}

type artifactCacheDeliverableStub struct {
	deliverableDownloadStub
	runID uuid.UUID
}

func (s *artifactCacheDeliverableStub) GetArtifact(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) (execution.ArtifactDetail, error) {
	return execution.ArtifactDetail{RunID: &s.runID}, nil
}
