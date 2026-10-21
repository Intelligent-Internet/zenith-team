package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	// Registers the GIF, JPEG, and PNG decoders that image.DecodeConfig
	// needs to size a workspace asset preview.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	pathpkg "path"

	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon/workspace"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

const (
	localWorkspaceFileResolvePath  = "/v1/workspace-files/resolve"
	localWorkspaceFilePreviewPath  = "/v1/workspace-files/preview"
	localWorkspaceFileExportPath   = "/v1/workspace-files/export"
	localWorkspaceAssetPreviewPath = "/v1/workspace-assets/preview"
	localWorkspaceAssetExportPath  = "/v1/workspace-assets/export"
	localTaskReviewPreparePath     = "/v1/task-review/prepare"
	localTaskReviewAcceptPath      = "/v1/task-review/accept"

	maximumLocalWorkspaceRequestBytes = 16 << 10
	maximumLocalWorkspacePreviewBytes = 1 << 20
	maximumLocalWorkspaceExportBytes  = 100 << 20
	maximumLocalWorkspaceImageBytes   = 4 << 20
	maximumLocalWorkspacePathBytes    = 4096
	maximumLocalWorkspaceImageSide    = 8192
	maximumLocalWorkspaceImagePixels  = 40_000_000
)

type localWorkspaceFileRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
	Path          string `json:"path"`
}

type localWorkspaceFilePreviewResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	Format        string `json:"format"`
	Content       string `json:"content"`
	SizeBytes     int64  `json:"sizeBytes"`
	Truncated     bool   `json:"truncated"`
}

type localWorkspaceFileResolveResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Path          string `json:"path"`
}

type localWorkspaceAssetRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	CompanyID     string `json:"companyId"`
	Scope         string `json:"scope"`
	Path          string `json:"path"`
}

type localWorkspaceAssetPreviewResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	Format        string `json:"format"`
	MIMEType      string `json:"mimeType"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"contentBase64,omitempty"`
	SizeBytes     int64  `json:"sizeBytes"`
	Truncated     bool   `json:"truncated"`
}

type localTaskReviewRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
}

type localTaskReviewAcceptRequest struct {
	SchemaVersion               int    `json:"schemaVersion"`
	CompanyID                   string `json:"companyId"`
	TaskID                      string `json:"taskId"`
	RunID                       string `json:"runId"`
	RuntimeID                   string `json:"runtimeId"`
	ExpectedRevision            int64  `json:"expectedRevision"`
	ExpectedExecutionGeneration int64  `json:"expectedExecutionGeneration"`
}

type localTaskReviewResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	RunID         string `json:"runId"`
	Status        string `json:"status"`
	BaseRef       string `json:"baseRef"`
	HeadRef       string `json:"headRef"`
	Diff          string `json:"diff"`
	Message       string `json:"message"`
}

type localWorkspaceErrorResponse struct {
	SchemaVersion int    `json:"schemaVersion"`
	Code          string `json:"code"`
	Reason        string `json:"reason"`
}

type localWorkspaceOpenError struct {
	status int
	code   string
	reason string
}

func (e *localWorkspaceOpenError) Error() string { return e.reason }

type localOpenedWorkspaceFile struct {
	file *os.File
	info fs.FileInfo
}

// resolveLocalRunWorkDir resolves a run's work directory for generated-file
// access: a retained managed workspace, or the bound directory a signed
// in-place receipt vouches for.
func (d *Daemon) resolveLocalRunWorkDir(ctx context.Context, runID uuid.UUID) (string, error) {
	return d.workspaces.ResolveRunWorkDir(ctx, runID)
}

func (d *Daemon) localWorkspaceFileResolveHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		var request localWorkspaceFileRequest
		if !decodeLocalWorkspaceJSON(w, r, &request) || request.SchemaVersion != 1 ||
			len(request.Path) > maximumLocalWorkspacePathBytes || !utf8.ValidString(request.Path) ||
			!filepath.IsAbs(request.Path) || filepath.Clean(request.Path) != request.Path {
			writeLocalWorkspaceError(
				w,
				http.StatusBadRequest,
				"invalid_request",
				"Workspace file link is invalid.",
			)
			return
		}
		runID, ok := parseLocalWorkspaceRunID(request.RunID)
		if !ok {
			writeLocalWorkspaceError(
				w,
				http.StatusBadRequest,
				"invalid_request",
				"Workspace file link is invalid.",
			)
			return
		}
		if d.workspaces == nil {
			writeLocalWorkspaceError(
				w,
				http.StatusServiceUnavailable,
				"workspace_unavailable",
				"Generated file access is unavailable.",
			)
			return
		}
		rootPath, err := d.resolveLocalRunWorkDir(r.Context(), runID)
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusNotFound,
				"workspace_unavailable",
				"The workspace for this generated file is no longer available.",
			)
			return
		}
		relativePath, err := filepath.Rel(rootPath, request.Path)
		relativePath = filepath.ToSlash(relativePath)
		if err != nil || !canonicalLocalWorkspacePath(relativePath) {
			writeLocalWorkspaceError(
				w,
				http.StatusNotFound,
				"file_unavailable",
				"This generated file is no longer available.",
			)
			return
		}
		opened, openErr := openLocalWorkspaceFile(rootPath, relativePath, false)
		if openErr != nil {
			writeLocalWorkspaceOpenError(w, openErr)
			return
		}
		_ = opened.file.Close()
		writeLocalWorkspaceJSON(w, http.StatusOK, localWorkspaceFileResolveResponse{
			SchemaVersion: 1, Path: relativePath,
		})
	}
}

func (d *Daemon) localWorkspaceFilePreviewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		request, runID, ok := decodeLocalWorkspaceFileRequest(w, r)
		if !ok {
			return
		}
		if d.workspaces == nil {
			writeLocalWorkspaceError(
				w,
				http.StatusServiceUnavailable,
				"workspace_unavailable",
				"Generated file access is unavailable.",
			)
			return
		}
		rootPath, err := d.resolveLocalRunWorkDir(r.Context(), runID)
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusNotFound,
				"workspace_unavailable",
				"The workspace for this generated file is no longer available.",
			)
			return
		}
		opened, openErr := openLocalWorkspaceFile(rootPath, request.Path, false)
		if openErr != nil {
			writeLocalWorkspaceOpenError(w, openErr)
			return
		}
		defer opened.file.Close()

		raw, err := io.ReadAll(io.LimitReader(opened.file, maximumLocalWorkspacePreviewBytes+utf8.UTFMax))
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusInternalServerError,
				"read_failed",
				"Unable to read this file.",
			)
			return
		}
		truncated := opened.info.Size() > maximumLocalWorkspacePreviewBytes ||
			len(raw) > maximumLocalWorkspacePreviewBytes
		if len(raw) > maximumLocalWorkspacePreviewBytes {
			raw = truncateLocalWorkspaceUTF8(raw, maximumLocalWorkspacePreviewBytes)
		}
		if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
			writeLocalWorkspaceError(
				w,
				http.StatusUnsupportedMediaType,
				"unsupported_file",
				"Only Markdown and UTF-8 text files can be previewed.",
			)
			return
		}
		format := "text"
		if extension := strings.ToLower(
			pathpkg.Ext(request.Path),
		); extension == ".md" ||
			extension == ".markdown" {
			format = "markdown"
		}
		writeLocalWorkspaceJSON(w, http.StatusOK, localWorkspaceFilePreviewResponse{
			SchemaVersion: 1,
			Path:          request.Path,
			Filename:      pathpkg.Base(request.Path),
			Format:        format,
			Content:       string(raw),
			SizeBytes:     opened.info.Size(),
			Truncated:     truncated,
		})
	}
}

func (d *Daemon) localWorkspaceFileExportHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		request, runID, ok := decodeLocalWorkspaceFileRequest(w, r)
		if !ok {
			return
		}
		if d.workspaces == nil {
			writeLocalWorkspaceError(
				w,
				http.StatusServiceUnavailable,
				"workspace_unavailable",
				"Generated file access is unavailable.",
			)
			return
		}
		rootPath, err := d.resolveLocalRunWorkDir(r.Context(), runID)
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusNotFound,
				"workspace_unavailable",
				"The workspace for this generated file is no longer available.",
			)
			return
		}
		opened, openErr := openLocalWorkspaceFile(rootPath, request.Path, false)
		if openErr != nil {
			writeLocalWorkspaceOpenError(w, openErr)
			return
		}
		defer opened.file.Close()
		if opened.info.Size() > maximumLocalWorkspaceExportBytes {
			writeLocalWorkspaceError(
				w,
				http.StatusRequestEntityTooLarge,
				"file_too_large",
				"Files larger than 100 MiB cannot be exported.",
			)
			return
		}
		writeLocalWorkspaceStream(w, opened, pathpkg.Base(request.Path))
	}
}

func (d *Daemon) localWorkspaceAssetPreviewHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		request, ok := decodeLocalWorkspaceAssetRequest(w, r)
		if !ok {
			return
		}
		opened, openErr := d.openLocalWorkspaceAsset(r.Context(), request)
		if openErr != nil {
			writeLocalWorkspaceOpenError(w, openErr)
			return
		}
		defer opened.file.Close()

		format, mimeType := classifyLocalWorkspaceAsset(request.Path)
		switch format {
		case "markdown", "text":
			raw, err := io.ReadAll(
				io.LimitReader(opened.file, maximumLocalWorkspacePreviewBytes+utf8.UTFMax),
			)
			if err != nil {
				writeLocalWorkspaceError(
					w,
					http.StatusInternalServerError,
					"read_failed",
					"Unable to read this workspace asset.",
				)
				return
			}
			truncated := opened.info.Size() > maximumLocalWorkspacePreviewBytes ||
				len(raw) > maximumLocalWorkspacePreviewBytes
			if len(raw) > maximumLocalWorkspacePreviewBytes {
				raw = truncateLocalWorkspaceUTF8(raw, maximumLocalWorkspacePreviewBytes)
			}
			if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
				writeLocalWorkspaceError(
					w,
					http.StatusUnsupportedMediaType,
					"unsupported_file",
					"This file does not contain previewable UTF-8 text.",
				)
				return
			}
			writeLocalWorkspaceJSON(w, http.StatusOK, localWorkspaceAssetPreviewResponse{
				SchemaVersion: 1,
				Path:          request.Path,
				Filename:      pathpkg.Base(request.Path),
				Format:        format,
				MIMEType:      mimeType,
				Content:       string(raw),
				SizeBytes:     opened.info.Size(),
				Truncated:     truncated,
			})
		case "image":
			raw, err := io.ReadAll(io.LimitReader(opened.file, maximumLocalWorkspaceImageBytes+1))
			if err != nil {
				writeLocalWorkspaceError(
					w,
					http.StatusInternalServerError,
					"read_failed",
					"Unable to read this workspace asset.",
				)
				return
			}
			if len(raw) > maximumLocalWorkspaceImageBytes {
				writeLocalWorkspaceError(
					w,
					http.StatusRequestEntityTooLarge,
					"file_too_large",
					"This image is too large to preview.",
				)
				return
			}
			detected := http.DetectContentType(raw)
			if !validLocalWorkspaceRaster(raw, detected) {
				writeLocalWorkspaceError(
					w,
					http.StatusUnsupportedMediaType,
					"unsupported_file",
					"This image format cannot be previewed safely.",
				)
				return
			}
			writeLocalWorkspaceJSON(w, http.StatusOK, localWorkspaceAssetPreviewResponse{
				SchemaVersion: 1,
				Path:          request.Path,
				Filename:      pathpkg.Base(request.Path),
				Format:        "image",
				MIMEType:      detected,
				ContentBase64: base64.StdEncoding.EncodeToString(raw),
				SizeBytes:     opened.info.Size(),
			})
		default:
			writeLocalWorkspaceError(
				w,
				http.StatusUnsupportedMediaType,
				"preview_unavailable",
				"Preview is unavailable for this workspace asset.",
			)
		}
	}
}

func (d *Daemon) localWorkspaceAssetExportHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		request, ok := decodeLocalWorkspaceAssetRequest(w, r)
		if !ok {
			return
		}
		opened, openErr := d.openLocalWorkspaceAsset(r.Context(), request)
		if openErr != nil {
			writeLocalWorkspaceOpenError(w, openErr)
			return
		}
		defer opened.file.Close()
		if opened.info.Size() > maximumLocalWorkspaceExportBytes {
			writeLocalWorkspaceError(
				w,
				http.StatusRequestEntityTooLarge,
				"file_too_large",
				"Files larger than 100 MiB cannot be exported.",
			)
			return
		}
		writeLocalWorkspaceStream(w, opened, pathpkg.Base(request.Path))
	}
}

func (d *Daemon) localTaskReviewPrepareHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		request, runID, ok := decodeLocalTaskReviewRequest(w, r)
		if !ok {
			return
		}
		if d.workspaces == nil {
			writeLocalWorkspaceError(
				w,
				http.StatusServiceUnavailable,
				"workspace_unavailable",
				"Task review is unavailable.",
			)
			return
		}
		review, err := d.workspaces.ReviewTask(r.Context(), runID)
		if err != nil {
			writeLocalTaskReviewError(w, err, "")
			return
		}
		writeLocalWorkspaceJSON(w, http.StatusOK, localTaskReviewResponse{
			SchemaVersion: 1,
			RunID:         request.RunID,
			Status:        string(review.Status),
			BaseRef:       review.BaseRef,
			HeadRef:       review.HeadRef,
			Diff:          review.Diff,
			Message:       review.Message,
		})
	}
}

func (d *Daemon) localTaskReviewAcceptHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		request, runID, ok := decodeLocalTaskReviewAcceptRequest(w, r)
		if !ok {
			return
		}
		if d.workspaces == nil || d.client == nil {
			writeLocalWorkspaceError(
				w,
				http.StatusServiceUnavailable,
				"workspace_unavailable",
				"Task review is unavailable.",
			)
			return
		}
		claim, err := d.client.ClaimManualTaskMerge(
			r.Context(),
			d.cfg.DaemonID,
			request.RuntimeID,
			manualTaskMergeClaimRequest{
				CompanyID: request.CompanyID, TaskID: request.TaskID, RunID: runID,
				ExpectedRevision:            request.ExpectedRevision,
				ExpectedExecutionGeneration: request.ExpectedExecutionGeneration,
			},
		)
		if isProjectPermissionRequiredError(err) {
			writeLocalWorkspaceError(
				w,
				http.StatusForbidden,
				"project_permission_required",
				"You can't run tasks here.",
			)
			return
		}
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusConflict,
				"task_merge_conflict",
				"Task review is stale or another merge is already running.",
			)
			return
		}
		result, err := d.completeTaskMergeClaim(r.Context(), request.RuntimeID, claim)
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusConflict,
				"task_merge_conflict",
				"Task merge authority was lost before completion.",
			)
			return
		}
		if result.Status != TaskMergeStatusMerged || result.ResultCode != "" {
			writeLocalTaskReviewError(w, errors.New("task merge blocked"), result.ResultCode)
			return
		}
		writeLocalWorkspaceJSON(w, http.StatusOK, localTaskReviewResponse{
			SchemaVersion: 1,
			RunID:         request.RunID,
			Status:        string(workspace.TaskReviewMerged),
			BaseRef:       "main",
			Message:       "Task changes were merged into main.",
		})
	}
}

func decodeLocalTaskReviewAcceptRequest(
	w http.ResponseWriter,
	r *http.Request,
) (localTaskReviewAcceptRequest, uuid.UUID, bool) {
	var request localTaskReviewAcceptRequest
	if !decodeLocalWorkspaceJSON(w, r, &request) || request.SchemaVersion != 1 ||
		!validLocalWorkspaceCompanyID(request.CompanyID) ||
		!validLocalWorkspaceCompanyID(request.TaskID) ||
		!validLocalWorkspaceCompanyID(request.RuntimeID) ||
		request.ExpectedRevision < 1 || request.ExpectedRevision > 9_007_199_254_740_991 ||
		request.ExpectedExecutionGeneration < 1 ||
		request.ExpectedExecutionGeneration > 9_007_199_254_740_991 {
		writeLocalWorkspaceError(w, http.StatusBadRequest, "invalid_request", "Task review request is invalid.")
		return localTaskReviewAcceptRequest{}, uuid.Nil, false
	}
	runID, ok := parseLocalWorkspaceRunID(request.RunID)
	if !ok {
		writeLocalWorkspaceError(w, http.StatusBadRequest, "invalid_request", "Task review request is invalid.")
		return localTaskReviewAcceptRequest{}, uuid.Nil, false
	}
	return request, runID, true
}

func (d *Daemon) authorizeLocalWorkspaceRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeLocalWorkspaceError(
			w,
			http.StatusMethodNotAllowed,
			"method_not_allowed",
			"Request method is not allowed.",
		)
		return false
	}
	if !localLoopbackRemote(r.RemoteAddr) || !localLoopbackHost(r.Host) || r.Header.Get("Origin") != "" {
		writeLocalWorkspaceError(w, http.StatusForbidden, "forbidden", "Local workspace access is forbidden.")
		return false
	}
	if d.localPreviewToken == "" ||
		!validLocalPreviewAuthorization(r.Header.Get("Authorization"), d.localPreviewToken) {
		writeLocalWorkspaceError(
			w,
			http.StatusUnauthorized,
			"unauthorized",
			"Local workspace access is unauthorized.",
		)
		return false
	}
	return true
}

func decodeLocalWorkspaceFileRequest(
	w http.ResponseWriter,
	r *http.Request,
) (localWorkspaceFileRequest, uuid.UUID, bool) {
	var request localWorkspaceFileRequest
	if !decodeLocalWorkspaceJSON(w, r, &request) || request.SchemaVersion != 1 ||
		!canonicalLocalWorkspacePath(request.Path) {
		writeLocalWorkspaceError(
			w,
			http.StatusBadRequest,
			"invalid_request",
			"Workspace file request is invalid.",
		)
		return localWorkspaceFileRequest{}, uuid.Nil, false
	}
	runID, ok := parseLocalWorkspaceRunID(request.RunID)
	if !ok {
		writeLocalWorkspaceError(
			w,
			http.StatusBadRequest,
			"invalid_request",
			"Workspace file request is invalid.",
		)
		return localWorkspaceFileRequest{}, uuid.Nil, false
	}
	return request, runID, true
}

func decodeLocalWorkspaceAssetRequest(
	w http.ResponseWriter,
	r *http.Request,
) (localWorkspaceAssetRequest, bool) {
	var request localWorkspaceAssetRequest
	if !decodeLocalWorkspaceJSON(w, r, &request) || request.SchemaVersion != 1 ||
		!validLocalWorkspaceCompanyID(request.CompanyID) ||
		!validLocalWorkspaceScope(request.Scope) ||
		!canonicalLocalWorkspacePath(request.Path) {
		writeLocalWorkspaceError(
			w,
			http.StatusBadRequest,
			"invalid_request",
			"Workspace asset request is invalid.",
		)
		return localWorkspaceAssetRequest{}, false
	}
	return request, true
}

func decodeLocalTaskReviewRequest(
	w http.ResponseWriter,
	r *http.Request,
) (localTaskReviewRequest, uuid.UUID, bool) {
	var request localTaskReviewRequest
	if !decodeLocalWorkspaceJSON(w, r, &request) || request.SchemaVersion != 1 {
		writeLocalWorkspaceError(w, http.StatusBadRequest, "invalid_request", "Task review request is invalid.")
		return localTaskReviewRequest{}, uuid.Nil, false
	}
	runID, ok := parseLocalWorkspaceRunID(request.RunID)
	if !ok {
		writeLocalWorkspaceError(w, http.StatusBadRequest, "invalid_request", "Task review request is invalid.")
		return localTaskReviewRequest{}, uuid.Nil, false
	}
	return request, runID, true
}

func decodeLocalWorkspaceJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maximumLocalWorkspaceRequestBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(destination) != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func parseLocalWorkspaceRunID(value string) (uuid.UUID, bool) {
	runID, err := uuid.Parse(value)
	return runID, err == nil && runID != uuid.Nil && runID.String() == value
}

func validLocalWorkspaceCompanyID(value string) bool {
	_, err := util.ParseGoUUID(value)
	return err == nil
}

func validLocalWorkspaceScope(value string) bool {
	if strings.HasPrefix(value, "projects/") {
		return validLocalWorkspaceCompanyID(strings.TrimPrefix(value, "projects/"))
	}
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func canonicalLocalWorkspacePath(value string) bool {
	if value == "" || len(value) > maximumLocalWorkspacePathBytes || !utf8.ValidString(value) ||
		strings.Contains(value, "\\") || value == "." || !fs.ValidPath(value) || pathpkg.Clean(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func openLocalWorkspaceFile(
	rootPath string,
	relativePath string,
	asset bool,
) (*localOpenedWorkspaceFile, *localWorkspaceOpenError) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, localWorkspaceUnavailableError(asset)
	}
	defer root.Close()

	parts := strings.Split(relativePath, "/")
	current := ""
	var info fs.FileInfo
	for index, part := range parts {
		// Git internals are rejected on every plane: a Run never generates a
		// deliverable under .git, and a managed worktree keeps its gitdir
		// file there. The asset plane additionally hides every dotfile.
		if part == ".git" || asset && hiddenLocalWorkspaceAssetName(part) {
			return nil, localWorkspaceFileUnavailableError(asset)
		}
		if current == "" {
			current = part
		} else {
			current += "/" + part
		}
		info, err = root.Lstat(filepath.FromSlash(current))
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, localWorkspaceFileUnavailableError(asset)
		}
		if index < len(parts)-1 {
			if !info.IsDir() || asset && ignoredLocalWorkspaceAssetDirectory(part) {
				return nil, localWorkspaceFileUnavailableError(asset)
			}
			if asset {
				codeProject, projectErr := localWorkspaceCodeProjectDirectory(root.FS(), current)
				if projectErr != nil || codeProject {
					return nil, localWorkspaceFileUnavailableError(asset)
				}
			}
			continue
		}
		if !info.Mode().IsRegular() || asset && sourceCodeLocalWorkspaceAsset(part) {
			return nil, localWorkspaceFileUnavailableError(asset)
		}
	}

	file, err := root.Open(filepath.FromSlash(relativePath))
	if err != nil {
		return nil, localWorkspaceFileUnavailableError(asset)
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, localWorkspaceFileUnavailableError(asset)
	}
	return &localOpenedWorkspaceFile{file: file, info: openedInfo}, nil
}

func (d *Daemon) openLocalWorkspaceAsset(
	ctx context.Context,
	request localWorkspaceAssetRequest,
) (*localOpenedWorkspaceFile, *localWorkspaceOpenError) {
	if d.workspaces == nil {
		return nil, localWorkspaceUnavailableError(true)
	}
	rootPath, err := d.workspaces.ResolveMain(ctx, request.CompanyID, request.Scope)
	if err != nil {
		return nil, localWorkspaceUnavailableError(true)
	}
	return openLocalWorkspaceFile(rootPath, request.Path, true)
}

func localWorkspaceUnavailableError(asset bool) *localWorkspaceOpenError {
	reason := "The workspace for this generated file is no longer available."
	if asset {
		reason = "This main workspace is unavailable."
	}
	return &localWorkspaceOpenError{status: http.StatusNotFound, code: "workspace_unavailable", reason: reason}
}

func localWorkspaceFileUnavailableError(asset bool) *localWorkspaceOpenError {
	reason := "This generated file is no longer available."
	if asset {
		reason = "This workspace asset is unavailable."
	}
	return &localWorkspaceOpenError{status: http.StatusNotFound, code: "file_unavailable", reason: reason}
}

func classifyLocalWorkspaceAsset(path string) (string, string) {
	_, mimeType, previewKind := classifyLocalWorkspaceCatalogAsset(path)
	return previewKind, mimeType
}

func validLocalWorkspaceRaster(raw []byte, mimeType string) bool {
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/gif" {
		return false
	}
	config, format, err := decodeLocalWorkspaceImageConfig(raw)
	if err != nil || config.Width <= 0 || config.Height <= 0 ||
		config.Width > maximumLocalWorkspaceImageSide || config.Height > maximumLocalWorkspaceImageSide ||
		config.Width > maximumLocalWorkspaceImagePixels/config.Height {
		return false
	}
	return format == "png" && mimeType == "image/png" ||
		format == "jpeg" && mimeType == "image/jpeg" ||
		format == "gif" && mimeType == "image/gif"
}

func decodeLocalWorkspaceImageConfig(raw []byte) (config image.Config, format string, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("image config decode failed")
		}
	}()
	return image.DecodeConfig(bytes.NewReader(raw))
}

func hiddenLocalWorkspaceAssetName(name string) bool {
	return name == "" || strings.HasPrefix(name, ".")
}

func ignoredLocalWorkspaceAssetDirectory(name string) bool {
	switch strings.ToLower(name) {
	case "node_modules", "vendor", "dist", "build", "out", "coverage", "target",
		"__pycache__", "venv", "pods", "deriveddata", "cache", "caches", "bin", "obj":
		return true
	default:
		return false
	}
}

func sourceCodeLocalWorkspaceAsset(name string) bool {
	lower := strings.ToLower(name)
	if localWorkspaceCodeProjectMarker(lower) {
		return true
	}
	switch lower {
	case "dockerfile", "makefile", "procfile", "package.json", "package-lock.json",
		"pnpm-lock.yaml", "yarn.lock", "go.mod", "go.sum", "go.work", "cargo.toml",
		"cargo.lock", "pyproject.toml", "pipfile", "gemfile", "composer.json",
		"cmakelists.txt":
		return true
	}
	switch strings.ToLower(pathpkg.Ext(lower)) {
	case ".asm", ".c", ".cc", ".clj", ".cljs", ".cpp", ".cs", ".css", ".dart",
		".elm", ".erl", ".ex", ".exs", ".fs", ".fsx", ".go", ".gradle", ".graphql",
		".groovy", ".h", ".hcl", ".hpp", ".hrl", ".hs", ".ipynb",
		".java", ".jl", ".js", ".jsx", ".kt", ".kts", ".lua", ".m", ".mm", ".php",
		".pl", ".proto", ".ps1", ".py", ".r", ".rb", ".rkt", ".rs", ".s", ".scala",
		".scss", ".sh", ".sol", ".sql", ".swift", ".tf", ".ts", ".tsx", ".vb",
		".vue", ".wasm", ".zig":
		return true
	default:
		return false
	}
}

func localWorkspaceCodeProjectDirectory(root fs.FS, path string) (bool, error) {
	entries, err := fs.ReadDir(root, path)
	if err != nil {
		return false, err
	}
	pythonSource := false
	requirementsManifest := false
	for _, entry := range entries {
		name := strings.ToLower(entry.Name())
		if localWorkspaceCodeProjectMarker(name) {
			return true, nil
		}
		if !entry.IsDir() && strings.HasSuffix(name, ".py") {
			pythonSource = true
		}
		if !entry.IsDir() && strings.HasPrefix(name, "requirements") && strings.HasSuffix(name, ".txt") {
			requirementsManifest = true
		}
	}
	return pythonSource && requirementsManifest, nil
}

func localWorkspaceCodeProjectMarker(name string) bool {
	switch name {
	case "package.json", "pnpm-workspace.yaml", "turbo.json", "nx.json",
		"go.mod", "go.work", "pyproject.toml", "setup.py", "pipfile",
		"cargo.toml", "pom.xml", "build.gradle", "build.gradle.kts",
		"settings.gradle", "settings.gradle.kts", "package.swift", "pubspec.yaml",
		"composer.json", "gemfile", "cmakelists.txt", "meson.build":
		return true
	}
	return strings.HasPrefix(name, "build.gradle.") ||
		strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".csproj") ||
		strings.HasSuffix(name, ".fsproj") || strings.HasSuffix(name, ".xcodeproj") ||
		strings.HasSuffix(name, ".xcworkspace")
}

func truncateLocalWorkspaceUTF8(raw []byte, maximum int) []byte {
	raw = raw[:maximum]
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return raw
}

func localLoopbackRemote(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func localLoopbackHost(hostHeader string) bool {
	host := hostHeader
	if split, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = split
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func writeLocalTaskReviewError(w http.ResponseWriter, err error, resultCode string) {
	status := http.StatusConflict
	code := resultCode
	reason := "Task changes could not be merged."
	switch {
	case errors.Is(err, workspace.ErrTaskReviewUnavailable), code == "workspace_unavailable":
		status, code, reason = http.StatusNotFound, "workspace_unavailable", "The task workspace is no longer available."
	case errors.Is(err, workspace.ErrTaskReviewTooLarge), code == "diff_too_large":
		status, code, reason = http.StatusUnsupportedMediaType, "diff_too_large", "The task diff is too large to review here."
	case errors.Is(err, workspace.ErrTaskReviewUnsupported):
		status, code, reason = http.StatusUnsupportedMediaType, "unsupported_diff", "The task diff contains unsupported binary content."
	case code == "main_dirty":
		reason = "Main has uncommitted changes."
	case code == "conflict":
		reason = "Task changes conflict with main."
	case code == "invalid_run":
		status, reason = http.StatusBadRequest, "Task review request is invalid."
	case code != "":
		status = http.StatusInternalServerError
	case code == "":
		status, code, reason = http.StatusInternalServerError, "review_failed", "Task review is unavailable."
	}
	writeLocalWorkspaceError(w, status, code, reason)
}

func writeLocalWorkspaceOpenError(w http.ResponseWriter, err *localWorkspaceOpenError) {
	writeLocalWorkspaceError(w, err.status, err.code, err.reason)
}

func writeLocalWorkspaceStream(
	w http.ResponseWriter,
	opened *localOpenedWorkspaceFile,
	filename string,
) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(opened.info.Size(), 10))
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
		"filename": filename,
	}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = io.CopyN(w, opened.file, opened.info.Size())
}

func writeLocalWorkspaceError(w http.ResponseWriter, status int, code, reason string) {
	writeLocalWorkspaceJSON(w, status, localWorkspaceErrorResponse{
		SchemaVersion: 1,
		Code:          code,
		Reason:        reason,
	})
}

func writeLocalWorkspaceJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
