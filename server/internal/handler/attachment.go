package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/middleware"
)

const (
	maxAttachmentMetadataBytes = 8 << 10
	maxAttachmentContentBytes  = int64(100 << 20)
	maxAttachmentRequestBytes  = maxAttachmentContentBytes + (int64(64 << 10))
)

type AttachmentService interface {
	Initiate(context.Context, attachment.InitiateInput, io.Reader) (attachment.InitiateResult, error)
	Complete(context.Context, attachment.CompleteInput) (attachment.CompleteResult, error)
	Download(context.Context, attachment.DownloadInput) (attachment.DownloadResult, error)
	DownloadResolved(
		context.Context,
		attachment.ResolvedDownloadInput,
	) (attachment.DownloadResult, error)
	DownloadForRun(context.Context, attachment.RunDownloadInput) (attachment.DownloadResult, error)
}

type attachmentMetadataRequest struct {
	RequestKey  string `json:"requestKey"`
	CompanyID   string `json:"companyId"`
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	SizeBytes   int64  `json:"sizeBytes"`
}

type completeAttachmentRequest struct {
	CompanyID string `json:"companyId"`
}

func (h *Handler) registerAttachmentRoutes(group *gin.RouterGroup) {
	if h.AuthService == nil || h.AttachmentService == nil {
		return
	}
	routes := group.Group("/attachments", middleware.NoStore(), middleware.RequireUserAuth(h.AuthService))
	routes.POST("/initiate", h.initiateAttachment)
	routes.POST("/:attachmentId/complete", h.completeAttachment)
	routes.GET("/:attachmentId/download", h.downloadAttachment)
}

func (h *Handler) initiateAttachment(c *gin.Context) {
	principal, ok := attachmentUserPrincipal(c)
	if !ok {
		return
	}
	if c.Request.ContentLength > maxAttachmentRequestBytes {
		writeBoundaryError(c, boundaryErrorRequestBodyTooLarge)
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAttachmentRequestBytes)
	if !validAttachmentMultipartContentType(c.Request.Header.Values("Content-Type")) {
		writeBoundaryError(c, boundaryErrorMultipartContentTypeRequired)
		return
	}
	reader, err := c.Request.MultipartReader()
	if err != nil {
		writeBoundaryError(c, boundaryErrorMultipartContentTypeRequired)
		return
	}

	metadataPart, err := reader.NextRawPart()
	if err != nil {
		writeAttachmentMultipartError(c, err)
		return
	}
	if !validAttachmentMetadataPart(metadataPart) {
		if validAttachmentPartHeaders(metadataPart, true) && metadataPart.FormName() == "metadata" &&
			!partHasExactJSONMediaType(metadataPart) {
			writeBoundaryError(c, boundaryErrorAttachmentMetadataType)
		} else {
			writeInvalidAttachmentMultipart(c)
		}
		return
	}
	metadataBody, err := io.ReadAll(io.LimitReader(metadataPart, maxAttachmentMetadataBytes+1))
	if err != nil {
		writeAttachmentMultipartError(c, err)
		return
	}
	if len(metadataBody) > maxAttachmentMetadataBytes {
		writeBoundaryError(c, boundaryErrorAttachmentMetadataTooLarge)
		return
	}
	if err := metadataPart.Close(); err != nil {
		writeAttachmentMultipartError(c, err)
		return
	}
	var metadata attachmentMetadataRequest
	if err := util.BindStrictJSON(bytes.NewReader(metadataBody), &metadata); err != nil {
		writeBoundaryError(c, boundaryErrorInvalidRequestBody)
		return
	}
	requestKey, err := util.ParseGoUUID(metadata.RequestKey)
	if err != nil {
		writeAttachmentError(c, attachment.ErrInvalidInput)
		return
	}
	companyID, err := util.ParseGoUUID(metadata.CompanyID)
	if err != nil {
		writeAttachmentError(c, attachment.ErrInvalidInput)
		return
	}
	// Starting an upload records no Activity; only its completion does.
	if _, ok := h.runUploadAdmitted(c, principal, companyID); !ok {
		return
	}

	filePart, err := reader.NextRawPart()
	if err != nil {
		writeAttachmentMultipartError(c, err)
		return
	}
	if !validAttachmentFilePart(filePart) {
		writeInvalidAttachmentMultipart(c)
		return
	}
	result, serviceErr := h.AttachmentService.Initiate(c.Request.Context(), attachment.InitiateInput{
		RequestKey: requestKey, CompanyID: companyID, UploadedByUserID: principal.UserID,
		Filename: metadata.Filename, ContentType: metadata.ContentType,
		SizeBytes: metadata.SizeBytes,
	}, filePart)
	if serviceErr != nil {
		writeAttachmentError(c, serviceErr)
		return
	}
	if err := filePart.Close(); err != nil {
		writeAttachmentMultipartError(c, err)
		return
	}
	trailing, err := reader.NextRawPart()
	if trailing != nil {
		writeInvalidAttachmentMultipart(c)
		return
	}
	if !errors.Is(err, io.EOF) {
		writeAttachmentMultipartError(c, err)
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, result.Attachment)
}

func (h *Handler) completeAttachment(c *gin.Context) {
	principal, ok := attachmentUserPrincipal(c)
	if !ok {
		return
	}
	attachmentID, err := util.ParseGoUUID(c.Param("attachmentId"))
	if err != nil {
		writeAttachmentError(c, attachment.ErrNotFound)
		return
	}
	request, err := bindRequest[completeAttachmentRequest](c, maxAttachmentMetadataBytes)
	if err != nil {
		writeRequestError(c, err)
		return
	}
	companyID, err := util.ParseGoUUID(request.CompanyID)
	if err != nil {
		writeAttachmentError(c, attachment.ErrInvalidInput)
		return
	}
	run, ok := h.runUploadAdmitted(c, principal, companyID)
	if !ok {
		return
	}
	input := attachment.CompleteInput{ID: attachmentID, CompanyID: companyID, UploadedByUserID: principal.UserID}
	if run != nil {
		input.RunActor = run.Actor()
	}
	result, err := h.AttachmentService.Complete(c.Request.Context(), input)
	if err != nil {
		writeAttachmentError(c, err)
		return
	}
	c.JSON(http.StatusOK, result.Attachment)
}

func (h *Handler) downloadAttachment(c *gin.Context) {
	principal, ok := attachmentUserPrincipal(c)
	if !ok {
		return
	}
	attachmentID, err := util.ParseGoUUID(c.Param("attachmentId"))
	if err != nil {
		writeAttachmentError(c, attachment.ErrNotFound)
		return
	}
	companyID, err := parseAttachmentDownloadCompany(c.Request.URL.RawQuery)
	if err != nil {
		writeBoundaryError(c, boundaryErrorValidationFailed)
		return
	}
	result, err := h.AttachmentService.Download(c.Request.Context(), attachment.DownloadInput{
		ID: attachmentID, CompanyID: companyID, RequestedByUserID: principal.UserID,
	})
	if err != nil {
		writeAttachmentError(c, err)
		return
	}
	streamAttachmentDownload(c, result)
}

func streamAttachmentDownload(c *gin.Context, result attachment.DownloadResult) {
	streamAttachmentDownloadWithPolicy(c, result, "private, no-store")
}

func streamAttachmentDownloadWithPolicy(c *gin.Context, result attachment.DownloadResult, cachePolicy string) {
	if nilHTTPReadCloser(result.Body) {
		closeHTTPReadCloser(result.Body)
		writeAttachmentError(c, attachment.ErrUnavailable)
		return
	}
	disposition, ok := attachmentContentDisposition(result.Attachment.Filename)
	if !ok {
		closeHTTPReadCloser(result.Body)
		writeAttachmentError(c, attachment.ErrUnavailable)
		return
	}
	defer closeHTTPReadCloser(result.Body)
	c.Header("Cache-Control", cachePolicy)
	c.Header("Content-Type", result.Attachment.ContentType)
	c.Header("Content-Length", strconv.FormatInt(result.Attachment.SizeBytes, 10))
	c.Header("Content-Disposition", disposition)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Status(http.StatusOK)
	// Once response bytes are visible, a JSON error would corrupt the file. The
	// transport terminates the stream on read failure and leaves retry policy to
	// the client.
	if _, err := io.Copy(c.Writer, result.Body); err != nil {
		slog.ErrorContext(c.Request.Context(), "Attachment download stream failed",
			"error_code", "attachment_stream_failed")
	}
}

func attachmentUserPrincipal(c *gin.Context) (auth.UserPrincipal, bool) {
	principal, ok := auth.UserPrincipalFromContext(c.Request.Context())
	if !ok {
		writeBoundaryError(c, boundaryErrorAuthenticationRequired)
		return auth.UserPrincipal{}, false
	}
	return principal, true
}

func validAttachmentMetadataPart(part *multipart.Part) bool {
	return validAttachmentPartHeaders(part, true) && part.FormName() == "metadata" &&
		partHasExactJSONMediaType(part)
}

func validAttachmentMultipartContentType(values []string) bool {
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || mediaType != "multipart/form-data" || len(parameters) != 1 {
		return false
	}
	boundary, exists := parameters["boundary"]
	return exists && len(boundary) >= 1 && len(boundary) <= 70
}

func validAttachmentFilePart(part *multipart.Part) bool {
	return validAttachmentPartHeaders(part, false) && part.FormName() == "file"
}

func validAttachmentPartHeaders(part *multipart.Part, metadata bool) bool {
	if part == nil {
		return false
	}
	for name, values := range part.Header {
		if name != "Content-Disposition" && name != "Content-Type" || len(values) != 1 {
			return false
		}
	}
	dispositions := part.Header.Values("Content-Disposition")
	if len(dispositions) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(dispositions[0])
	if err != nil || mediaType != "form-data" {
		return false
	}
	name, exists := parameters["name"]
	if !exists {
		return false
	}
	if metadata {
		if name != "metadata" || len(parameters) > 2 || len(part.Header.Values("Content-Type")) != 1 {
			return false
		}
		for key := range parameters {
			if key != "name" && key != "filename" {
				return false
			}
		}
		return true
	}
	if name != "file" || len(parameters) > 2 {
		return false
	}
	for key := range parameters {
		if key != "name" && key != "filename" {
			return false
		}
	}
	return len(part.Header.Values("Content-Type")) <= 1
}

func partHasExactJSONMediaType(part *multipart.Part) bool {
	if part == nil {
		return false
	}
	values := part.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	return err == nil && mediaType == "application/json" && len(parameters) == 0
}

func parseAttachmentDownloadCompany(rawQuery string) (uuid.UUID, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil || len(query) != 1 {
		return uuid.Nil, attachment.ErrInvalidInput
	}
	values, exists := query["companyId"]
	if !exists || len(values) != 1 || values[0] == "" {
		return uuid.Nil, attachment.ErrInvalidInput
	}
	companyID, err := util.ParseGoUUID(values[0])
	if err != nil {
		return uuid.Nil, attachment.ErrInvalidInput
	}
	return companyID, nil
}

func attachmentContentDisposition(filename string) (string, bool) {
	if filename == "" || !utf8.ValidString(filename) || strings.ContainsAny(filename, "\r\n") {
		return "", false
	}
	var encoded strings.Builder
	encoded.Grow(len(filename))
	const hexadecimal = "0123456789ABCDEF"
	for _, value := range []byte(filename) {
		if attachmentDispositionAttrChar(value) {
			encoded.WriteByte(value)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hexadecimal[value>>4])
		encoded.WriteByte(hexadecimal[value&0x0f])
	}
	return "attachment; filename=\"download\"; filename*=UTF-8''" + encoded.String(), true
}

func attachmentDispositionAttrChar(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' ||
		strings.ContainsRune("!#$&+-.^_`|~", rune(value))
}

func nilHTTPReadCloser(reader io.ReadCloser) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func closeHTTPReadCloser(reader io.ReadCloser) {
	if !nilHTTPReadCloser(reader) {
		_ = reader.Close()
	}
}

func writeInvalidAttachmentMultipart(c *gin.Context) {
	writeBoundaryError(c, boundaryErrorMultipartPartOrder)
}

func writeAttachmentMultipartError(c *gin.Context, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) || errors.Is(err, multipart.ErrMessageTooLarge) {
		writeBoundaryError(c, boundaryErrorRequestBodyTooLarge)
		return
	}
	writeInvalidAttachmentMultipart(c)
}

var attachmentErrorMappings = []errorMapping{
	{attachment.ErrInvalidInput, http.StatusUnprocessableEntity, "request validation failed", "validation_failed"},
	{attachment.ErrConflict, http.StatusConflict, "Attachment conflicts with current state", "attachment_conflict"},
	{attachment.ErrNotFound, http.StatusNotFound, "Attachment not found", "attachment_not_found"},
	{
		attachment.ErrUploadIncomplete,
		http.StatusConflict,
		"Attachment upload is incomplete",
		"attachment_upload_incomplete",
	},
	{attachment.ErrUploadExpired, http.StatusGone, "Attachment upload expired", "attachment_upload_expired"},
	{
		attachment.ErrIntegrityMismatch,
		http.StatusUnprocessableEntity,
		"Attachment content does not match its metadata",
		"attachment_integrity_mismatch",
	},
	{
		attachment.ErrStorageUnavailable,
		http.StatusServiceUnavailable,
		"Attachment storage is unavailable",
		"attachment_storage_unavailable",
	},
	{
		attachment.ErrUnavailable,
		http.StatusServiceUnavailable,
		"Attachment service is unavailable",
		"attachment_unavailable",
	},
}

func writeAttachmentError(c *gin.Context, err error) {
	writeMappedError(c, err, attachmentErrorMappings, internalErrorFallback)
}

var _ AttachmentService = (*attachment.DefaultAttachmentService)(nil)
