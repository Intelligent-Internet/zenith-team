package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/middleware"
)

var (
	attachmentHTTPID         = uuid.MustParse("0195eb55-3f66-74d1-9e51-8f1b8c4f6a01")
	attachmentHTTPRequestKey = uuid.MustParse("0195eb55-3f66-7e62-8888-7f3e2d1c0b9a")
	attachmentHTTPCompany    = uuid.MustParse("0195eb55-3f66-7a2e-99d4-31a72edcc859")
	attachmentHTTPUser       = uuid.MustParse("0195eb55-3f66-7d5a-9d35-0ff2a4a8cfad")
)

func TestAttachmentInitiateStreamsStrictMultipartAndReturnsPublicResponse(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		created    bool
		wantStatus int
	}{
		{name: "created", created: true, wantStatus: http.StatusCreated},
		{name: "exact replay", created: false, wantStatus: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			content := []byte("Boardly attachment content")
			service := &attachmentServiceStub{
				initiate: func(_ context.Context, input attachment.InitiateInput, source io.Reader) (attachment.InitiateResult, error) {
					if input.RequestKey != attachmentHTTPRequestKey ||
						input.CompanyID != attachmentHTTPCompany ||
						input.UploadedByUserID != attachmentHTTPUser ||
						input.Filename != "architecture.pdf" ||
						input.ContentType != "application/pdf" ||
						input.SizeBytes != int64(len(content)) {
						t.Fatalf("Initiate input = %#v", input)
					}
					got, err := io.ReadAll(source)
					if err != nil || !bytes.Equal(got, content) {
						t.Fatalf("streamed source = %q, %v", got, err)
					}
					return attachment.InitiateResult{
						Attachment: pendingAttachmentHTTPResponse(), Created: test.created,
					}, nil
				},
			}
			handler := attachmentHTTPHandler(service)
			request := newAttachmentInitiateRequest(
				t,
				validAttachmentMetadataJSON(int64(len(content))),
				content,
				false,
			)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
			}
			if !bytes.Contains(response.Body.Bytes(), []byte(`"id":"`+attachmentHTTPID.String()+`"`)) ||
				bytes.Contains(response.Body.Bytes(), []byte(`"attachment"`)) ||
				bytes.Contains(response.Body.Bytes(), []byte(`"created"`)) {
				t.Fatalf("response must be the public Attachment payload: %s", response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
		})
	}
}

func TestAttachmentInitiateSupportsCookieAuthenticationWithBoundCSRF(t *testing.T) {
	t.Parallel()

	protector, err := auth.NewCSRF([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("NewCSRF() error = %v", err)
	}
	const accessToken = "web-access-token"
	authService := &authServiceStub{
		authenticate: func(_ context.Context, token string, kind auth.TokenKind) (auth.UserPrincipal, error) {
			if token != accessToken || kind != auth.TokenKindSession {
				t.Fatalf("Authenticate(%q, %q)", token, kind)
			}
			return auth.UserPrincipal{
				UserID:    attachmentHTTPUser,
				SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
				AuthKind:  kind,
			}, nil
		},
	}
	attachmentService := &attachmentServiceStub{
		initiate: func(_ context.Context, input attachment.InitiateInput, source io.Reader) (attachment.InitiateResult, error) {
			if input.UploadedByUserID != attachmentHTTPUser {
				t.Fatalf("uploader = %s", input.UploadedByUserID)
			}
			_, _ = io.Copy(io.Discard, source)
			return attachment.InitiateResult{
				Attachment: pendingAttachmentHTTPResponse(),
				Created:    true,
			}, nil
		},
	}
	request := newAttachmentInitiateRequest(t, validAttachmentMetadataJSON(3), []byte("abc"), false)
	request.Header.Del("Authorization")
	csrfToken := protector.Token(accessToken)
	request.AddCookie(&http.Cookie{Name: middleware.AuthCookieName, Value: accessToken})
	request.AddCookie(&http.Cookie{Name: middleware.CSRFCookieName, Value: csrfToken})
	request.Header.Set(middleware.CSRFHeaderName, csrfToken)
	response := httptest.NewRecorder()
	New(
		Handler{AuthService: authService, AttachmentService: attachmentService, CSRF: protector},
	).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
}

func TestAttachmentInitiateRejectsTrailingPartAfterTheObjectWasAccepted(t *testing.T) {
	t.Parallel()

	called := false
	service := &attachmentServiceStub{
		initiate: func(_ context.Context, _ attachment.InitiateInput, source io.Reader) (attachment.InitiateResult, error) {
			called = true
			if _, err := io.Copy(io.Discard, source); err != nil {
				t.Fatalf("consume source: %v", err)
			}
			return attachment.InitiateResult{
				Attachment: pendingAttachmentHTTPResponse(),
				Created:    true,
			}, nil
		},
	}
	request := newAttachmentInitiateRequest(t, validAttachmentMetadataJSON(3), []byte("abc"), true)
	response := httptest.NewRecorder()
	attachmentHTTPHandler(service).ServeHTTP(response, request)

	if !called {
		t.Fatal("Initiate was not called before validating the trailing boundary")
	}
	assertHTTPErrorCode(t, response, http.StatusUnprocessableEntity, "invalid_multipart")
}

func TestAttachmentInitiateRejectsMalformedTrailingPartAfterTheObjectWasAccepted(t *testing.T) {
	t.Parallel()

	const boundary = "boardly-boundary"
	raw := fmt.Sprintf(
		"--%[1]s\r\nContent-Disposition: form-data; name=\"metadata\"\r\nContent-Type: application/json\r\n\r\n%s\r\n"+
			"--%[1]s\r\nContent-Disposition: form-data; name=\"file\"; filename=\"ignored.bin\"\r\nContent-Type: application/octet-stream\r\n\r\nabc\r\n"+
			"--%[1]s\r\nthis-is-not-a-header\r\n\r\n",
		boundary,
		validAttachmentMetadataJSON(3),
	)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/attachments/initiate", strings.NewReader(raw))
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	called := false
	service := &attachmentServiceStub{
		initiate: func(_ context.Context, _ attachment.InitiateInput, source io.Reader) (attachment.InitiateResult, error) {
			called = true
			content, err := io.ReadAll(source)
			if err != nil || string(content) != "abc" {
				t.Fatalf("file stream = %q, %v", content, err)
			}
			return attachment.InitiateResult{
				Attachment: pendingAttachmentHTTPResponse(),
				Created:    true,
			}, nil
		},
	}
	response := httptest.NewRecorder()
	attachmentHTTPHandler(service).ServeHTTP(response, request)
	if !called {
		t.Fatal("Initiate was not called")
	}
	assertHTTPErrorCode(t, response, http.StatusUnprocessableEntity, "invalid_multipart")
}

func TestAttachmentInitiateEnforcesMetadataAndPartShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		build      func(*testing.T) *http.Request
		wantStatus int
		wantCode   string
	}{
		{
			name: "missing request key",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				return newAttachmentInitiateRequest(
					t,
					[]byte(
						`{"companyId":"`+attachmentHTTPCompany.String()+`","filename":"architecture.pdf","contentType":"application/pdf","sizeBytes":3}`,
					),
					[]byte("abc"),
					false,
				)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "validation_failed",
		},
		{
			name: "malformed request key",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				return newAttachmentInitiateRequest(
					t,
					[]byte(
						`{"requestKey":"not-a-uuid","companyId":"`+attachmentHTTPCompany.String()+`","filename":"architecture.pdf","contentType":"application/pdf","sizeBytes":3}`,
					),
					[]byte("abc"),
					false,
				)
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "validation_failed",
		},
		{
			name: "outer media type",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				request := httptest.NewRequest(
					http.MethodPost,
					"/api/v1/attachments/initiate",
					strings.NewReader("not multipart"),
				)
				request.Header.Set("Authorization", "Bearer access-token")
				request.Header.Set("Content-Type", "application/json")
				return request
			},
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "unsupported_media_type",
		},
		{
			name: "outer media type has extra parameter",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				request := newAttachmentInitiateRequest(
					t,
					validAttachmentMetadataJSON(3),
					[]byte("abc"),
					false,
				)
				request.Header.Set("Content-Type", request.Header.Get("Content-Type")+"; charset=utf-8")
				return request
			},
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "unsupported_media_type",
		},
		{
			name: "file before metadata",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				return multipartRequest(t, func(writer *multipart.Writer) {
					part, err := writer.CreateFormFile("file", "ignored.bin")
					if err != nil {
						t.Fatal(err)
					}
					_, _ = part.Write([]byte("abc"))
				})
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_multipart",
		},
		{
			name: "metadata content type",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				return multipartRequest(t, func(writer *multipart.Writer) {
					header := textproto.MIMEHeader{}
					header.Set("Content-Disposition", `form-data; name="metadata"`)
					header.Set("Content-Type", "text/plain")
					part, _ := writer.CreatePart(header)
					_, _ = part.Write(validAttachmentMetadataJSON(3))
				})
			},
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   "unsupported_media_type",
		},
		{
			name: "metadata transfer encoding",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				return multipartRequest(t, func(writer *multipart.Writer) {
					header := textproto.MIMEHeader{}
					header.Set("Content-Disposition", `form-data; name="metadata"`)
					header.Set("Content-Type", "application/json")
					header.Set("Content-Transfer-Encoding", "quoted-printable")
					part, _ := writer.CreatePart(header)
					_, _ = part.Write(validAttachmentMetadataJSON(3))
				})
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_multipart",
		},
		{
			name: "metadata unknown JSON field",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				metadata := bytes.TrimSuffix(validAttachmentMetadataJSON(3), []byte("}"))
				metadata = append(metadata, []byte(`,"private":"must-not-be-accepted"}`)...)
				return newAttachmentInitiateRequest(t, metadata, []byte("abc"), false)
			},
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name: "metadata over eight KiB",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				return multipartRequest(t, func(writer *multipart.Writer) {
					writeMetadataPart(
						t,
						writer,
						bytes.Repeat([]byte("x"), maxAttachmentMetadataBytes+1),
					)
				})
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "request_too_large",
		},
		{
			name: "advertised request over whole-body limit",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				request := newAttachmentInitiateRequest(
					t,
					validAttachmentMetadataJSON(3),
					[]byte("abc"),
					false,
				)
				request.ContentLength = maxAttachmentRequestBytes + 1
				return request
			},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "request_too_large",
		},
		{
			name: "missing file",
			build: func(t *testing.T) *http.Request {
				t.Helper()
				return multipartRequest(t, func(writer *multipart.Writer) {
					writeMetadataPart(t, writer, validAttachmentMetadataJSON(3))
				})
			},
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   "invalid_multipart",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := &attachmentServiceStub{
				initiate: func(context.Context, attachment.InitiateInput, io.Reader) (attachment.InitiateResult, error) {
					t.Fatal("Initiate called for an invalid multipart request")
					return attachment.InitiateResult{}, nil
				},
			}
			response := httptest.NewRecorder()
			attachmentHTTPHandler(service).ServeHTTP(response, test.build(t))
			assertHTTPErrorCode(t, response, test.wantStatus, test.wantCode)
		})
	}
}

func TestAttachmentCompleteUsesAuthenticatedUserAndStrictJSON(t *testing.T) {
	t.Parallel()

	service := &attachmentServiceStub{
		complete: func(_ context.Context, input attachment.CompleteInput) (attachment.CompleteResult, error) {
			if input.ID != attachmentHTTPID || input.CompanyID != attachmentHTTPCompany ||
				input.UploadedByUserID != attachmentHTTPUser {
				t.Fatalf("Complete input = %#v", input)
			}
			return attachment.CompleteResult{Attachment: readyAttachmentHTTPResponse(), Changed: true}, nil
		},
	}
	response := serveBearerJSON(
		attachmentHTTPHandler(service), http.MethodPost,
		"/api/v1/attachments/"+attachmentHTTPID.String()+"/complete",
		[]byte(`{"companyId":"`+attachmentHTTPCompany.String()+`"}`),
	)
	if response.Code != http.StatusOK || bytes.Contains(response.Body.Bytes(), []byte(`"changed"`)) {
		t.Fatalf("status/body = %d / %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}

	invalid := serveBearerJSON(
		attachmentHTTPHandler(&attachmentServiceStub{}), http.MethodPost,
		"/api/v1/attachments/"+attachmentHTTPID.String()+"/complete",
		[]byte(`{"companyId":"`+attachmentHTTPCompany.String()+`","unknown":true}`),
	)
	assertHTTPErrorCode(t, invalid, http.StatusBadRequest, "invalid_request")
}

func TestAttachmentDownloadUsesExactQueryAndStreamsCanonicalHeaders(t *testing.T) {
	t.Parallel()

	body := &trackingReadCloser{Reader: strings.NewReader("download body")}
	service := &attachmentServiceStub{
		download: func(_ context.Context, input attachment.DownloadInput) (attachment.DownloadResult, error) {
			if input.ID != attachmentHTTPID || input.CompanyID != attachmentHTTPCompany ||
				input.RequestedByUserID != attachmentHTTPUser {
				t.Fatalf("Download input = %#v", input)
			}
			return attachment.DownloadResult{Attachment: func() attachment.Response {
				response := readyAttachmentHTTPResponse()
				response.Filename = "Boardly kế hoạch.pdf"
				response.ContentType = "application/pdf"
				response.SizeBytes = int64(len("download body"))
				return response
			}(), Body: body}, nil
		},
	}
	response := serveBearerRequest(
		attachmentHTTPHandler(service),
		http.MethodGet,
		"/api/v1/attachments/"+attachmentHTTPID.String()+"/download?companyId="+attachmentHTTPCompany.String(),
		nil,
	)
	if response.Code != http.StatusOK || response.Body.String() != "download body" {
		t.Fatalf("status/body = %d / %q", response.Code, response.Body.String())
	}
	if !body.closed {
		t.Fatal("download body was not closed")
	}
	for name, want := range map[string]string{
		"Cache-Control":          "private, no-store",
		"Content-Type":           "application/pdf",
		"Content-Length":         fmt.Sprint(len("download body")),
		"Content-Disposition":    "attachment; filename=\"download\"; filename*=UTF-8''Boardly%20k%E1%BA%BF%20ho%E1%BA%A1ch.pdf",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := response.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// A malformed attachment id in the path is 404 attachment_not_found — the
// resource cannot exist, so the id shape is not a validation problem.
func TestAttachmentMalformedPathIDIs404(t *testing.T) {
	t.Parallel()

	handler := attachmentHTTPHandler(&attachmentServiceStub{})
	complete := serveBearerJSON(
		handler, http.MethodPost,
		"/api/v1/attachments/not-a-uuid/complete",
		[]byte(`{"companyId":"`+attachmentHTTPCompany.String()+`"}`),
	)
	assertHTTPErrorCode(t, complete, http.StatusNotFound, "attachment_not_found")

	download := serveBearerRequest(
		handler, http.MethodGet,
		"/api/v1/attachments/not-a-uuid/download?companyId="+attachmentHTTPCompany.String(),
		nil,
	)
	assertHTTPErrorCode(t, download, http.StatusNotFound, "attachment_not_found")
}

func TestAttachmentDownloadRejectsNonExactQueryBeforeService(t *testing.T) {
	t.Parallel()

	for _, suffix := range []string{
		"",
		"?companyId=" + attachmentHTTPCompany.String() + "&companyId=" + attachmentHTTPCompany.String(),
		"?companyId=" + attachmentHTTPCompany.String() + "&extra=true",
	} {
		service := &attachmentServiceStub{
			download: func(context.Context, attachment.DownloadInput) (attachment.DownloadResult, error) {
				t.Fatal("Download called for invalid query")
				return attachment.DownloadResult{}, nil
			},
		}
		response := serveBearerRequest(
			attachmentHTTPHandler(service), http.MethodGet,
			"/api/v1/attachments/"+attachmentHTTPID.String()+"/download"+suffix, nil,
		)
		assertHTTPErrorCode(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
}

func TestAttachmentDownloadDoesNotAppendJSONAfterStreamingFailure(t *testing.T) {
	t.Parallel()

	body := &trackingReadCloser{Reader: &failingReader{content: []byte("partial")}}
	service := &attachmentServiceStub{
		download: func(context.Context, attachment.DownloadInput) (attachment.DownloadResult, error) {
			response := readyAttachmentHTTPResponse()
			response.SizeBytes = 12
			return attachment.DownloadResult{Attachment: response, Body: body}, nil
		},
	}
	response := serveBearerRequest(
		attachmentHTTPHandler(service),
		http.MethodGet,
		"/api/v1/attachments/"+attachmentHTTPID.String()+"/download?companyId="+attachmentHTTPCompany.String(),
		nil,
	)
	if response.Code != http.StatusOK || response.Body.String() != "partial" ||
		bytes.Contains(response.Body.Bytes(), []byte(`"code"`)) {
		t.Fatalf("status/body = %d / %q", response.Code, response.Body.String())
	}
	if !body.closed {
		t.Fatal("failed stream body was not closed")
	}
}

func TestAttachmentDomainErrorsHaveStableHTTPMappings(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		err        error
		wantStatus int
		wantCode   string
	}{
		{attachment.ErrInvalidInput, http.StatusUnprocessableEntity, "validation_failed"},
		{attachment.ErrConflict, http.StatusConflict, "attachment_conflict"},
		{attachment.ErrNotFound, http.StatusNotFound, "attachment_not_found"},
		{attachment.ErrUploadIncomplete, http.StatusConflict, "attachment_upload_incomplete"},
		{attachment.ErrUploadExpired, http.StatusGone, "attachment_upload_expired"},
		{attachment.ErrIntegrityMismatch, http.StatusUnprocessableEntity, "attachment_integrity_mismatch"},
		{attachment.ErrStorageUnavailable, http.StatusServiceUnavailable, "attachment_storage_unavailable"},
		{attachment.ErrUnavailable, http.StatusServiceUnavailable, "attachment_unavailable"},
		{errors.New("private database detail"), http.StatusInternalServerError, "internal_error"},
	} {
		service := &attachmentServiceStub{
			complete: func(context.Context, attachment.CompleteInput) (attachment.CompleteResult, error) {
				return attachment.CompleteResult{}, test.err
			},
		}
		response := serveBearerJSON(
			attachmentHTTPHandler(service), http.MethodPost,
			"/api/v1/attachments/"+attachmentHTTPID.String()+"/complete",
			[]byte(`{"companyId":"`+attachmentHTTPCompany.String()+`"}`),
		)
		assertHTTPErrorCode(t, response, test.wantStatus, test.wantCode)
		if bytes.Contains(response.Body.Bytes(), []byte("private database detail")) {
			t.Fatalf("private error leaked: %s", response.Body.String())
		}
	}
}

type attachmentServiceStub struct {
	initiate         func(context.Context, attachment.InitiateInput, io.Reader) (attachment.InitiateResult, error)
	complete         func(context.Context, attachment.CompleteInput) (attachment.CompleteResult, error)
	download         func(context.Context, attachment.DownloadInput) (attachment.DownloadResult, error)
	downloadResolved func(context.Context, attachment.ResolvedDownloadInput) (attachment.DownloadResult, error)
	downloadForRun   func(context.Context, attachment.RunDownloadInput) (attachment.DownloadResult, error)
}

func (s *attachmentServiceStub) Initiate(
	ctx context.Context,
	input attachment.InitiateInput,
	source io.Reader,
) (attachment.InitiateResult, error) {
	if s.initiate == nil {
		return attachment.InitiateResult{}, errors.New("unexpected Initiate call")
	}
	return s.initiate(ctx, input, source)
}

func (s *attachmentServiceStub) Complete(
	ctx context.Context,
	input attachment.CompleteInput,
) (attachment.CompleteResult, error) {
	if s.complete == nil {
		return attachment.CompleteResult{}, errors.New("unexpected Complete call")
	}
	return s.complete(ctx, input)
}

func (s *attachmentServiceStub) Download(
	ctx context.Context,
	input attachment.DownloadInput,
) (attachment.DownloadResult, error) {
	if s.download == nil {
		return attachment.DownloadResult{}, errors.New("unexpected Download call")
	}
	return s.download(ctx, input)
}

func (s *attachmentServiceStub) DownloadResolved(
	ctx context.Context,
	input attachment.ResolvedDownloadInput,
) (attachment.DownloadResult, error) {
	if s.downloadResolved == nil {
		return attachment.DownloadResult{}, errors.New("unexpected DownloadResolved call")
	}
	return s.downloadResolved(ctx, input)
}

func (s *attachmentServiceStub) DownloadForRun(
	ctx context.Context,
	input attachment.RunDownloadInput,
) (attachment.DownloadResult, error) {
	if s.downloadForRun == nil {
		return attachment.DownloadResult{}, errors.New("unexpected DownloadForRun call")
	}
	return s.downloadForRun(ctx, input)
}

func attachmentHTTPHandler(service AttachmentService) http.Handler {
	return New(Handler{AuthService: authenticatedAuthStub(attachmentHTTPUser), AttachmentService: service})
}

func validAttachmentMetadataJSON(size int64) []byte {
	return []byte(fmt.Sprintf(
		`{"requestKey":"%s","companyId":"%s","filename":"architecture.pdf","contentType":"application/pdf","sizeBytes":%d}`,
		attachmentHTTPRequestKey,
		attachmentHTTPCompany,
		size,
	))
}

func newAttachmentInitiateRequest(t *testing.T, metadata, content []byte, trailing bool) *http.Request {
	t.Helper()
	return multipartRequest(t, func(writer *multipart.Writer) {
		writeMetadataPart(t, writer, metadata)
		file, err := writer.CreateFormFile("file", "ignored-by-server.txt")
		if err != nil {
			t.Fatalf("create file part: %v", err)
		}
		if _, err := file.Write(content); err != nil {
			t.Fatalf("write file part: %v", err)
		}
		if trailing {
			extra, err := writer.CreateFormField("extra")
			if err != nil {
				t.Fatalf("create extra part: %v", err)
			}
			_, _ = extra.Write([]byte("forbidden"))
		}
	})
}

func multipartRequest(t *testing.T, write func(*multipart.Writer)) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	write(writer)
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/attachments/initiate", &body)
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	return request
}

func writeMetadataPart(t *testing.T, writer *multipart.Writer, metadata []byte) {
	t.Helper()
	header := textproto.MIMEHeader{}
	// Browser FormData supplies a harmless Blob filename. The server ignores it
	// and derives every authoritative field from the JSON metadata.
	header.Set("Content-Disposition", `form-data; name="metadata"; filename="blob"`)
	header.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create metadata part: %v", err)
	}
	if _, err := part.Write(metadata); err != nil {
		t.Fatalf("write metadata: %v", err)
	}
}

func pendingAttachmentHTTPResponse() attachment.Response {
	createdAt := time.Date(2026, 7, 12, 1, 2, 3, 0, time.UTC)
	return attachment.Response{
		ID: attachmentHTTPID, CompanyID: attachmentHTTPCompany, UploadedByUserID: attachmentHTTPUser,
		Filename: "architecture.pdf", ContentType: "application/pdf", SizeBytes: 3,
		Status:    attachment.PublicStatusPending,
		CreatedAt: createdAt, UpdatedAt: createdAt, Revision: 1,
	}
}

func readyAttachmentHTTPResponse() attachment.Response {
	response := pendingAttachmentHTTPResponse()
	response.Status = attachment.PublicStatusReady
	response.Revision = 2
	response.UpdatedAt = response.CreatedAt.Add(time.Minute)
	return response
}

func assertHTTPErrorCode(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status || !bytes.Contains(response.Body.Bytes(), []byte(`"code":"`+code+`"`)) {
		t.Fatalf("status/body = %d / %s, want %d/%s", response.Code, response.Body.String(), status, code)
	}
}

type trackingReadCloser struct {
	io.Reader
	closed bool
}

func (r *trackingReadCloser) Close() error {
	r.closed = true
	return nil
}

type failingReader struct {
	content []byte
	done    bool
}

func (r *failingReader) Read(destination []byte) (int, error) {
	if r.done {
		return 0, errors.New("stream failed")
	}
	r.done = true
	return copy(destination, r.content), nil
}

var _ AttachmentService = (*attachmentServiceStub)(nil)
