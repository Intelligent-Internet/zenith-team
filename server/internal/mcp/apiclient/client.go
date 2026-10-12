// Package apiclient is how Run-bound code reaches the Zenith Team API.
// boardly-mcp uses RunChannel: every call goes over the daemon's Run channel
// and boardly-mcp holds no credential (Access TECH B3b). Client, which reads
// the person's session from auth.json, serves only the daemon's own output
// presenter, which runs in the daemon and never in the Agent; every call it
// makes carries the Run proof.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"boardly.dev/boardly/server/internal/authfile"
)

// Client calls the API with the session in auth.json plus the Run proof. Only
// the daemon's output presenter holds one.
type Client struct {
	authPath         string
	httpClient       *http.Client
	attachmentClient *http.Client
	now              func() time.Time
	runToken         string
}

const minimumAttachmentUploadBytesPerSecond = int64(128 << 10)

type errorResponse struct {
	Code      string `json:"code"`
	Detail    string `json:"detail"`
	RequestID string `json:"requestId"`
}

type AttachmentUpload struct {
	RequestKey  string
	CompanyID   string
	Filename    string
	ContentType string
	SizeBytes   int64
}

type attachmentResponse struct {
	ID string `json:"id"`
}

// APIError is the canonical Boardly HTTP error envelope plus its status.
// Callers branch on Code, never on human-readable Detail.
type APIError struct {
	StatusCode int
	Code       string
	Detail     string
	RequestID  string
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return fmt.Sprintf("Zenith Team API returned %d: %s (%s)", e.StatusCode, e.Detail, e.Code)
	}
	return fmt.Sprintf("Zenith Team API returned %d: %s", e.StatusCode, e.Detail)
}

// New binds a client to the session stored at authPath and one Run's proof.
func New(authPath, runToken string) *Client {
	httpClient := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("Zenith Team API redirects are not allowed")
		},
	}
	return newClient(authPath, runToken, httpClient, time.Now)
}

func newClient(authPath, runToken string, httpClient *http.Client, now func() time.Time) *Client {
	attachmentClient := *httpClient
	attachmentClient.Timeout = 0
	return &Client{
		authPath: authPath, httpClient: httpClient, attachmentClient: &attachmentClient,
		now: now, runToken: runToken,
	}
}

// PostRunJSON sends the session's bearer plus the Run proof. The request key
// makes a retried call converge.
func (c *Client) PostRunJSON(
	ctx context.Context,
	path, requestKey string,
	source, destination any,
) error {
	if c == nil || c.runToken == "" {
		return errors.New("Zenith Team run proof is unavailable")
	}
	if requestKey == "" {
		return errors.New("Zenith Team idempotency key is required")
	}
	return c.doJSON(ctx, http.MethodPost, path, source, destination, requestProof{
		runToken: c.runToken, requestKey: requestKey,
	})
}

// UploadAttachment stores one file for the Run. The upload start and its
// completion both carry the Run proof, so the server checks the session is
// the Run's authorizer and records the upload as the Run's.
func (c *Client) UploadAttachment(
	ctx context.Context,
	input AttachmentUpload,
	source io.ReadSeeker,
) (string, error) {
	if c == nil || c.runToken == "" {
		return "", errors.New("Zenith Team run proof is unavailable")
	}
	if source == nil || input.RequestKey == "" || input.CompanyID == "" || input.Filename == "" ||
		input.ContentType == "" || input.SizeBytes < 0 || input.SizeBytes > int64(100<<20) {
		return "", errors.New("attachment upload is invalid")
	}
	uploadCtx, cancel := context.WithTimeout(ctx, attachmentUploadTimeout(input.SizeBytes))
	defer cancel()
	credential, err := c.usableCredential(uploadCtx, false, "")
	if err != nil {
		return "", err
	}
	status, attachmentID, err := c.uploadAttachmentWithCredential(
		uploadCtx,
		credential,
		input,
		source,
	)
	if err != nil && status == http.StatusUnauthorized {
		credential, err = c.usableCredential(uploadCtx, true, credential.AccessToken)
		if err != nil {
			return "", err
		}
		if _, seekErr := source.Seek(0, io.SeekStart); seekErr != nil {
			return "", fmt.Errorf("rewind attachment upload: %w", seekErr)
		}
		_, attachmentID, err = c.uploadAttachmentWithCredential(
			uploadCtx,
			credential,
			input,
			source,
		)
	}
	if err != nil {
		return "", err
	}
	completionCtx, cancelCompletion := context.WithTimeout(
		ctx,
		attachmentUploadTimeout(input.SizeBytes),
	)
	defer cancelCompletion()
	var completed attachmentResponse
	completionClient := *c
	completionClient.httpClient = c.attachmentClient
	if err := completionClient.doJSON(
		completionCtx,
		http.MethodPost,
		"/attachments/"+url.PathEscape(attachmentID)+"/complete",
		map[string]string{"companyId": input.CompanyID},
		&completed,
		requestProof{runToken: c.runToken},
	); err != nil {
		return "", fmt.Errorf("complete Zenith Team attachment: %w", err)
	}
	if completed.ID != attachmentID {
		return "", errors.New("Zenith Team attachment completion returned a different identity")
	}
	return attachmentID, nil
}

func (c *Client) uploadAttachmentWithCredential(
	ctx context.Context,
	credential authfile.Credential,
	input AttachmentUpload,
	source io.Reader,
) (int, string, error) {
	endpoint, err := endpointURL(credential.APIBaseURL, "/attachments/initiate")
	if err != nil {
		return 0, "", err
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	writeDone := make(chan error, 1)
	go func() {
		metadataHeader := make(textproto.MIMEHeader)
		metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
		metadataHeader.Set("Content-Type", "application/json")
		metadataPart, partErr := multipartWriter.CreatePart(metadataHeader)
		if partErr == nil {
			partErr = json.NewEncoder(metadataPart).Encode(map[string]any{
				"requestKey": input.RequestKey, "companyId": input.CompanyID,
				"filename": input.Filename, "contentType": input.ContentType,
				"sizeBytes": input.SizeBytes,
			})
		}
		fileHeader := make(textproto.MIMEHeader)
		fileHeader.Set("Content-Disposition", `form-data; name="file"; filename="upload"`)
		fileHeader.Set("Content-Type", "application/octet-stream")
		var filePart io.Writer
		if partErr == nil {
			filePart, partErr = multipartWriter.CreatePart(fileHeader)
		}
		if partErr == nil {
			_, partErr = io.Copy(filePart, source)
		}
		closeErr := multipartWriter.Close()
		pipeErr := writer.CloseWithError(errors.Join(partErr, closeErr))
		writeDone <- errors.Join(partErr, closeErr, pipeErr)
	}()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		_ = reader.CloseWithError(err)
		return 0, "", fmt.Errorf("create Zenith Team attachment request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("X-Boardly-Run-Token", c.runToken)
	request.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	response, err := c.attachmentClient.Do(request)
	if err != nil {
		_ = reader.CloseWithError(err)
		<-writeDone
		return 0, "", fmt.Errorf("upload Zenith Team attachment: %w", err)
	}
	defer response.Body.Close()
	if writeErr := <-writeDone; writeErr != nil {
		return response.StatusCode, "", fmt.Errorf("stream Zenith Team attachment: %w", writeErr)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, "", decodeAPIError(response)
	}
	var initiated attachmentResponse
	if err := decodeJSON(response.Body, &initiated); err != nil || initiated.ID == "" {
		return response.StatusCode, "", errors.New("Zenith Team attachment initiation response is invalid")
	}
	return response.StatusCode, initiated.ID, nil
}

func attachmentUploadTimeout(sizeBytes int64) time.Duration {
	uploadSeconds := (sizeBytes + minimumAttachmentUploadBytesPerSecond - 1) /
		minimumAttachmentUploadBytesPerSecond
	return 2*time.Minute + time.Duration(uploadSeconds)*time.Second
}

type requestProof struct {
	runToken   string
	requestKey string
}

func (c *Client) doJSON(
	ctx context.Context,
	method, path string,
	source, destination any,
	proof requestProof,
) error {
	credential, err := c.usableCredential(ctx, false, "")
	if err != nil {
		return err
	}
	status, err := c.doJSONWithCredential(
		ctx, credential, method, path, source, destination, proof,
	)
	if err == nil || status != http.StatusUnauthorized {
		return err
	}

	credential, err = c.usableCredential(ctx, true, credential.AccessToken)
	if err != nil {
		return err
	}
	_, err = c.doJSONWithCredential(ctx, credential, method, path, source, destination, proof)
	return err
}

func (c *Client) doJSONWithCredential(
	ctx context.Context,
	credential authfile.Credential,
	method string,
	path string,
	source any,
	destination any,
	proof requestProof,
) (int, error) {
	endpoint, err := endpointURL(credential.APIBaseURL, path)
	if err != nil {
		return 0, err
	}
	var body io.Reader
	if source != nil {
		encoded, encodeErr := json.Marshal(source)
		if encodeErr != nil {
			return 0, fmt.Errorf("encode Zenith Team API request: %w", encodeErr)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, fmt.Errorf("create Zenith Team API request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	if proof.runToken != "" {
		request.Header.Set("X-Boardly-Run-Token", proof.runToken)
		if proof.requestKey != "" {
			request.Header.Set("Idempotency-Key", proof.requestKey)
		}
	}
	if source != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("call Zenith Team API: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, decodeAPIError(response)
	}
	if destination != nil {
		if err := decodeJSON(response.Body, destination); err != nil {
			return response.StatusCode, fmt.Errorf("decode Zenith Team API response: %w", err)
		}
	}
	return response.StatusCode, nil
}

func (c *Client) usableCredential(
	ctx context.Context,
	forceRefresh bool,
	rejectedAccessToken string,
) (authfile.Credential, error) {
	return authfile.Ensure(ctx, c.httpClient, c.authPath, c.now(), authfile.EnsureOptions{
		Force:               forceRefresh,
		RejectedAccessToken: rejectedAccessToken,
	})
}

func endpointURL(baseURL, path string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme != "http" && base.Scheme != "https" || base.User != nil || base.RawQuery != "" ||
		base.Fragment != "" ||
		base.Path != "/api/v1" {
		return "", errors.New("Zenith Team API URL in auth.json is invalid")
	}
	if base.Scheme == "http" && !isLoopback(base.Hostname()) {
		return "", errors.New("Zenith Team API URL must use HTTPS unless it is loopback")
	}
	if !strings.HasPrefix(path, "/") {
		return "", errors.New("Zenith Team API path is invalid")
	}
	return strings.TrimSuffix(base.String(), "/") + path, nil
}

func isLoopback(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") || strings.HasSuffix(strings.ToLower(hostname), ".localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func decodeJSON(reader io.Reader, destination any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 16<<20))
	return decoder.Decode(destination)
}

func decodeAPIError(response *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	return apiErrorFromBody(response.StatusCode, body)
}

// apiErrorFromBody reads the server's {detail, code, requestId} envelope.
func apiErrorFromBody(status int, body []byte) *APIError {
	var payload errorResponse
	_ = json.Unmarshal(body, &payload)
	detail := strings.TrimSpace(payload.Detail)
	if detail == "" {
		detail = http.StatusText(status)
	}
	return &APIError{
		StatusCode: status,
		Code:       payload.Code,
		Detail:     detail,
		RequestID:  payload.RequestID,
	}
}
