package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	localWorkspaceAssetListPath  = "/v1/workspace-assets/list"
	localWorkspaceMemoryListPath = "/v1/workspace-memory/list"

	maximumLocalWorkspaceAssetQueryBytes  = 256
	maximumLocalWorkspaceAssetCursorBytes = 4096
	maximumLocalWorkspaceAssetFiles       = 20_000
	maximumLocalWorkspaceAssetDepth       = 32
	maximumLocalWorkspaceAssetPageSize    = 100

	maximumLocalWorkspaceMemoryFiles         = 64
	maximumLocalWorkspaceMemoryFileBytes     = 256 << 10
	maximumLocalWorkspaceMemoryResponseBytes = 1 << 20
	localWorkspaceMemoryDirectory            = ".memory"
)

var localWorkspaceMemoryFilenamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}\.md$`)

type localWorkspaceAssetKind string

const (
	localWorkspaceAssetKindArchive  localWorkspaceAssetKind = "archive"
	localWorkspaceAssetKindDocument localWorkspaceAssetKind = "document"
	localWorkspaceAssetKindImage    localWorkspaceAssetKind = "image"
	localWorkspaceAssetKindMedia    localWorkspaceAssetKind = "media"
	localWorkspaceAssetKindOther    localWorkspaceAssetKind = "other"
)

type localWorkspaceAssetSort string

const (
	localWorkspaceAssetSortLatest localWorkspaceAssetSort = "latest"
	localWorkspaceAssetSortName   localWorkspaceAssetSort = "name"
	localWorkspaceAssetSortOldest localWorkspaceAssetSort = "oldest"
)

type localWorkspaceAssetScanLimits struct {
	MaxDepth int
	MaxFiles int
}

type localWorkspaceAssetItem struct {
	Path        string                  `json:"path"`
	Filename    string                  `json:"filename"`
	ParentPath  string                  `json:"parentPath"`
	Kind        localWorkspaceAssetKind `json:"kind"`
	MIMEType    string                  `json:"mimeType"`
	SizeBytes   int64                   `json:"sizeBytes"`
	ModifiedAt  time.Time               `json:"modifiedAt"`
	PreviewKind string                  `json:"previewKind"`
}

type localWorkspaceAssetListRequest struct {
	SchemaVersion int                       `json:"schemaVersion"`
	CompanyID     string                    `json:"companyId"`
	Scope         string                    `json:"scope"`
	Query         string                    `json:"query,omitempty"`
	Kinds         []localWorkspaceAssetKind `json:"kinds,omitempty"`
	Sort          localWorkspaceAssetSort   `json:"sort"`
	Cursor        *string                   `json:"cursor,omitempty"`
	Limit         int                       `json:"limit"`
}

type localWorkspaceAssetListResponse struct {
	SchemaVersion    int                       `json:"schemaVersion"`
	CompanyID        string                    `json:"companyId"`
	Scope            string                    `json:"scope"`
	Items            []localWorkspaceAssetItem `json:"items"`
	TotalCount       int                       `json:"totalCount"`
	LatestModifiedAt *time.Time                `json:"latestModifiedAt"`
	NextCursor       *string                   `json:"nextCursor"`
	Truncated        bool                      `json:"truncated"`
}

type localWorkspaceAssetCursor struct {
	CompanyID          string                    `json:"companyId"`
	Scope              string                    `json:"scope"`
	Sort               localWorkspaceAssetSort   `json:"sort"`
	Query              string                    `json:"query"`
	Kinds              []localWorkspaceAssetKind `json:"kinds,omitempty"`
	Path               string                    `json:"path"`
	ModifiedAtUnixNano int64                     `json:"modifiedAtUnixNano"`
}

type localWorkspaceMemoryListRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	CompanyID     string `json:"companyId"`
	Scope         string `json:"scope"`
}

type localWorkspaceMemoryFile struct {
	Path       string    `json:"path"`
	Filename   string    `json:"filename"`
	Markdown   string    `json:"markdown"`
	SizeBytes  int64     `json:"sizeBytes"`
	ModifiedAt time.Time `json:"modifiedAt"`
}

type localWorkspaceMemoryListResponse struct {
	SchemaVersion int                        `json:"schemaVersion"`
	CompanyID     string                     `json:"companyId"`
	Scope         string                     `json:"scope"`
	Files         []localWorkspaceMemoryFile `json:"files"`
	Truncated     bool                       `json:"truncated"`
}

func (d *Daemon) localWorkspaceAssetListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		var request localWorkspaceAssetListRequest
		if !decodeLocalWorkspaceJSON(w, r, &request) || !validLocalWorkspaceAssetListRequest(request) {
			writeLocalWorkspaceError(
				w,
				http.StatusBadRequest,
				"invalid_request",
				"Workspace asset list request is invalid.",
			)
			return
		}
		if d.workspaces == nil {
			writeLocalWorkspaceError(
				w,
				http.StatusServiceUnavailable,
				"workspace_unavailable",
				"Workspace assets are unavailable.",
			)
			return
		}
		rootPath, err := d.workspaces.ResolveMain(r.Context(), request.CompanyID, request.Scope)
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, fs.ErrNotExist) {
			writeLocalWorkspaceJSON(
				w,
				http.StatusOK,
				emptyLocalWorkspaceAssetListResponse(request.CompanyID, request.Scope),
			)
			return
		}
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusNotFound,
				"workspace_unavailable",
				"This main workspace is unavailable.",
			)
			return
		}
		items, truncated, err := scanLocalWorkspaceAssets(
			r.Context(),
			rootPath,
			localWorkspaceAssetScanLimits{
				MaxDepth: maximumLocalWorkspaceAssetDepth,
				MaxFiles: maximumLocalWorkspaceAssetFiles,
			},
		)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			writeLocalWorkspaceError(
				w,
				http.StatusInternalServerError,
				"scan_failed",
				"Unable to scan this main workspace.",
			)
			return
		}
		response, err := buildLocalWorkspaceAssetListResponse(request, items, truncated)
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusBadRequest,
				"invalid_cursor",
				"Workspace asset cursor is invalid.",
			)
			return
		}
		writeLocalWorkspaceJSON(w, http.StatusOK, response)
	}
}

func (d *Daemon) localWorkspaceMemoryListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.authorizeLocalWorkspaceRequest(w, r) {
			return
		}
		var request localWorkspaceMemoryListRequest
		if !decodeLocalWorkspaceJSON(w, r, &request) || request.SchemaVersion != 1 ||
			!validLocalWorkspaceCompanyID(request.CompanyID) || !validLocalWorkspaceScope(request.Scope) {
			writeLocalWorkspaceError(
				w,
				http.StatusBadRequest,
				"invalid_request",
				"Workspace Memory request is invalid.",
			)
			return
		}
		if d.workspaces == nil {
			writeLocalWorkspaceError(
				w,
				http.StatusServiceUnavailable,
				"workspace_unavailable",
				"Workspace Memory is unavailable.",
			)
			return
		}
		rootPath, err := d.workspaces.ResolveMain(r.Context(), request.CompanyID, request.Scope)
		if r.Context().Err() != nil {
			return
		}
		if errors.Is(err, fs.ErrNotExist) {
			writeLocalWorkspaceJSON(w, http.StatusOK, emptyLocalWorkspaceMemoryListResponse(request))
			return
		}
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusNotFound,
				"workspace_unavailable",
				"This main workspace is unavailable.",
			)
			return
		}
		files, truncated, err := readLocalWorkspaceMemory(rootPath)
		if err != nil {
			writeLocalWorkspaceError(
				w,
				http.StatusInternalServerError,
				"read_failed",
				"Workspace Memory could not be read.",
			)
			return
		}
		writeLocalWorkspaceJSON(w, http.StatusOK, localWorkspaceMemoryListResponse{
			SchemaVersion: 1,
			CompanyID:     request.CompanyID,
			Scope:         request.Scope,
			Files:         files,
			Truncated:     truncated,
		})
	}
}

func scanLocalWorkspaceAssets(
	ctx context.Context,
	rootPath string,
	limits localWorkspaceAssetScanLimits,
) ([]localWorkspaceAssetItem, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if limits.MaxDepth <= 0 || limits.MaxFiles <= 0 {
		return nil, false, errors.New("workspace asset scan limits are invalid")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()
	rootFS := root.FS()
	items := make([]localWorkspaceAssetItem, 0, min(limits.MaxFiles, 256))
	truncated := false
	err = fs.WalkDir(rootFS, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		name := entry.Name()
		depth := strings.Count(path, "/") + 1
		if entry.IsDir() {
			if depth > limits.MaxDepth || hiddenLocalWorkspaceAssetName(name) ||
				ignoredLocalWorkspaceAssetDirectory(name) {
				return fs.SkipDir
			}
			codeProject, err := localWorkspaceCodeProjectDirectory(rootFS, path)
			if err != nil {
				return err
			}
			if codeProject {
				return fs.SkipDir
			}
			return nil
		}
		if depth > limits.MaxDepth || hiddenLocalWorkspaceAssetName(name) ||
			entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || sourceCodeLocalWorkspaceAsset(name) {
			return nil
		}
		if len(items) == limits.MaxFiles {
			truncated = true
			return fs.SkipAll
		}
		items = append(items, localWorkspaceAssetItemFor(path, info))
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	sort.Slice(items, func(left, right int) bool { return items[left].Path < items[right].Path })
	return items, truncated, nil
}

func localWorkspaceAssetItemFor(path string, info fs.FileInfo) localWorkspaceAssetItem {
	kind, mimeType, previewKind := classifyLocalWorkspaceCatalogAsset(path)
	parentPath := pathpkg.Dir(path)
	if parentPath == "." {
		parentPath = ""
	}
	return localWorkspaceAssetItem{
		Path: path, Filename: pathpkg.Base(path), ParentPath: parentPath,
		Kind: kind, MIMEType: mimeType, SizeBytes: info.Size(),
		ModifiedAt: info.ModTime().UTC(), PreviewKind: previewKind,
	}
}

func classifyLocalWorkspaceCatalogAsset(path string) (localWorkspaceAssetKind, string, string) {
	switch strings.ToLower(pathpkg.Ext(path)) {
	case ".png":
		return localWorkspaceAssetKindImage, "image/png", "image"
	case ".jpg", ".jpeg":
		return localWorkspaceAssetKindImage, "image/jpeg", "image"
	case ".gif":
		return localWorkspaceAssetKindImage, "image/gif", "image"
	case ".webp":
		return localWorkspaceAssetKindImage, "image/webp", "none"
	case ".avif":
		return localWorkspaceAssetKindImage, "image/avif", "none"
	case ".bmp":
		return localWorkspaceAssetKindImage, "image/bmp", "none"
	case ".svg":
		return localWorkspaceAssetKindImage, "image/svg+xml", "none"
	case ".md", ".markdown":
		return localWorkspaceAssetKindDocument, "text/markdown", "markdown"
	// A standalone page is a deliverable — a signup form, a one-page site —
	// not source code: code projects are skipped as whole directories above.
	case ".html", ".htm":
		return localWorkspaceAssetKindDocument, "text/html", "text"
	case ".txt", ".log":
		return localWorkspaceAssetKindDocument, "text/plain", "text"
	case ".csv":
		return localWorkspaceAssetKindDocument, "text/csv", "text"
	case ".tsv":
		return localWorkspaceAssetKindDocument, "text/tab-separated-values", "text"
	case ".json":
		return localWorkspaceAssetKindDocument, "application/json", "text"
	case ".yaml", ".yml":
		return localWorkspaceAssetKindDocument, "application/yaml", "text"
	case ".pdf":
		return localWorkspaceAssetKindDocument, "application/pdf", "none"
	case ".doc":
		return localWorkspaceAssetKindDocument, "application/msword", "none"
	case ".docx":
		return localWorkspaceAssetKindDocument, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "none"
	case ".xls":
		return localWorkspaceAssetKindDocument, "application/vnd.ms-excel", "none"
	case ".xlsx":
		return localWorkspaceAssetKindDocument, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "none"
	case ".ppt":
		return localWorkspaceAssetKindDocument, "application/vnd.ms-powerpoint", "none"
	case ".pptx":
		return localWorkspaceAssetKindDocument, "application/vnd.openxmlformats-officedocument.presentationml.presentation", "none"
	case ".mp3":
		return localWorkspaceAssetKindMedia, "audio/mpeg", "none"
	case ".wav":
		return localWorkspaceAssetKindMedia, "audio/wav", "none"
	case ".m4a":
		return localWorkspaceAssetKindMedia, "audio/mp4", "none"
	case ".mp4":
		return localWorkspaceAssetKindMedia, "video/mp4", "none"
	case ".mov":
		return localWorkspaceAssetKindMedia, "video/quicktime", "none"
	case ".webm":
		return localWorkspaceAssetKindMedia, "video/webm", "none"
	case ".zip":
		return localWorkspaceAssetKindArchive, "application/zip", "none"
	case ".tar":
		return localWorkspaceAssetKindArchive, "application/x-tar", "none"
	case ".gz", ".tgz":
		return localWorkspaceAssetKindArchive, "application/gzip", "none"
	case ".7z":
		return localWorkspaceAssetKindArchive, "application/x-7z-compressed", "none"
	default:
		return localWorkspaceAssetKindOther, "application/octet-stream", "none"
	}
}

func buildLocalWorkspaceAssetListResponse(
	request localWorkspaceAssetListRequest,
	items []localWorkspaceAssetItem,
	truncated bool,
) (localWorkspaceAssetListResponse, error) {
	kinds := normalizedLocalWorkspaceAssetKinds(request.Kinds)
	query := strings.ToLower(strings.TrimSpace(request.Query))
	filtered := make([]localWorkspaceAssetItem, 0, len(items))
	var latest *time.Time
	for _, item := range items {
		if query != "" && !strings.Contains(strings.ToLower(item.Path), query) {
			continue
		}
		if len(kinds) > 0 && !containsLocalWorkspaceAssetKind(kinds, item.Kind) {
			continue
		}
		filtered = append(filtered, item)
		if latest == nil || item.ModifiedAt.After(*latest) {
			value := item.ModifiedAt
			latest = &value
		}
	}
	sortLocalWorkspaceAssetItems(filtered, request.Sort)
	start := 0
	if request.Cursor != nil {
		cursor, err := decodeLocalWorkspaceAssetCursor(*request.Cursor)
		if err != nil || cursor.CompanyID != request.CompanyID || cursor.Scope != request.Scope ||
			cursor.Sort != request.Sort || cursor.Query != query ||
			strings.Join(localWorkspaceAssetKindsToStrings(cursor.Kinds), ",") !=
				strings.Join(localWorkspaceAssetKindsToStrings(kinds), ",") {
			return localWorkspaceAssetListResponse{}, errors.New("workspace asset cursor is invalid")
		}
		start = firstLocalWorkspaceAssetAfterCursor(filtered, cursor)
	}
	end := min(start+request.Limit, len(filtered))
	page := make([]localWorkspaceAssetItem, end-start)
	copy(page, filtered[start:end])
	var nextCursor *string
	if end < len(filtered) && len(page) > 0 {
		encoded, err := encodeLocalWorkspaceAssetCursor(localWorkspaceAssetCursor{
			CompanyID: request.CompanyID, Scope: request.Scope,
			Sort: request.Sort, Query: query, Kinds: kinds,
			Path:               page[len(page)-1].Path,
			ModifiedAtUnixNano: page[len(page)-1].ModifiedAt.UnixNano(),
		})
		if err != nil {
			return localWorkspaceAssetListResponse{}, err
		}
		nextCursor = &encoded
	}
	return localWorkspaceAssetListResponse{
		SchemaVersion: 1, CompanyID: request.CompanyID, Scope: request.Scope, Items: page,
		TotalCount: len(filtered), LatestModifiedAt: latest,
		NextCursor: nextCursor, Truncated: truncated,
	}, nil
}

func emptyLocalWorkspaceAssetListResponse(companyID, scope string) localWorkspaceAssetListResponse {
	return localWorkspaceAssetListResponse{
		SchemaVersion: 1,
		CompanyID:     companyID,
		Scope:         scope,
		Items:         []localWorkspaceAssetItem{},
	}
}

func sortLocalWorkspaceAssetItems(items []localWorkspaceAssetItem, order localWorkspaceAssetSort) {
	sort.SliceStable(items, func(left, right int) bool {
		a, b := items[left], items[right]
		switch order {
		case localWorkspaceAssetSortName:
			lowerA, lowerB := strings.ToLower(a.Filename), strings.ToLower(b.Filename)
			if lowerA != lowerB {
				return lowerA < lowerB
			}
			return a.Path < b.Path
		case localWorkspaceAssetSortOldest:
			if !a.ModifiedAt.Equal(b.ModifiedAt) {
				return a.ModifiedAt.Before(b.ModifiedAt)
			}
			return a.Path < b.Path
		default:
			if !a.ModifiedAt.Equal(b.ModifiedAt) {
				return a.ModifiedAt.After(b.ModifiedAt)
			}
			return a.Path < b.Path
		}
	})
}

func firstLocalWorkspaceAssetAfterCursor(
	items []localWorkspaceAssetItem,
	cursor localWorkspaceAssetCursor,
) int {
	for index, item := range items {
		if localWorkspaceAssetItemAfterCursor(item, cursor) {
			return index
		}
	}
	return len(items)
}

func localWorkspaceAssetItemAfterCursor(
	item localWorkspaceAssetItem,
	cursor localWorkspaceAssetCursor,
) bool {
	switch cursor.Sort {
	case localWorkspaceAssetSortName:
		lowerItem := strings.ToLower(item.Filename)
		lowerCursor := strings.ToLower(pathpkg.Base(cursor.Path))
		return lowerItem > lowerCursor || lowerItem == lowerCursor && item.Path > cursor.Path
	case localWorkspaceAssetSortOldest:
		return item.ModifiedAt.UnixNano() > cursor.ModifiedAtUnixNano ||
			item.ModifiedAt.UnixNano() == cursor.ModifiedAtUnixNano && item.Path > cursor.Path
	default:
		return item.ModifiedAt.UnixNano() < cursor.ModifiedAtUnixNano ||
			item.ModifiedAt.UnixNano() == cursor.ModifiedAtUnixNano && item.Path > cursor.Path
	}
}

func encodeLocalWorkspaceAssetCursor(cursor localWorkspaceAssetCursor) (string, error) {
	raw, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeLocalWorkspaceAssetCursor(value string) (localWorkspaceAssetCursor, error) {
	if value == "" || len(value) > maximumLocalWorkspaceAssetCursorBytes {
		return localWorkspaceAssetCursor{}, errors.New("workspace asset cursor is invalid")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return localWorkspaceAssetCursor{}, err
	}
	var cursor localWorkspaceAssetCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return localWorkspaceAssetCursor{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || !canonicalLocalWorkspacePath(cursor.Path) {
		return localWorkspaceAssetCursor{}, errors.New("workspace asset cursor is invalid")
	}
	return cursor, nil
}

func validLocalWorkspaceAssetListRequest(request localWorkspaceAssetListRequest) bool {
	if request.SchemaVersion != 1 {
		return false
	}
	if !validLocalWorkspaceCompanyID(request.CompanyID) || !validLocalWorkspaceScope(request.Scope) {
		return false
	}
	if len(request.Query) > maximumLocalWorkspaceAssetQueryBytes || !utf8.ValidString(request.Query) {
		return false
	}
	if request.Limit < 1 || request.Limit > maximumLocalWorkspaceAssetPageSize {
		return false
	}
	switch request.Sort {
	case localWorkspaceAssetSortLatest, localWorkspaceAssetSortName, localWorkspaceAssetSortOldest:
	default:
		return false
	}
	if len(request.Kinds) > 5 {
		return false
	}
	seen := make(map[localWorkspaceAssetKind]struct{}, len(request.Kinds))
	for _, kind := range request.Kinds {
		if !validLocalWorkspaceAssetKind(kind) {
			return false
		}
		if _, duplicate := seen[kind]; duplicate {
			return false
		}
		seen[kind] = struct{}{}
	}
	return request.Cursor == nil || len(*request.Cursor) <= maximumLocalWorkspaceAssetCursorBytes
}

func validLocalWorkspaceAssetKind(kind localWorkspaceAssetKind) bool {
	switch kind {
	case localWorkspaceAssetKindArchive, localWorkspaceAssetKindDocument,
		localWorkspaceAssetKindImage, localWorkspaceAssetKindMedia, localWorkspaceAssetKindOther:
		return true
	default:
		return false
	}
}

func normalizedLocalWorkspaceAssetKinds(
	kinds []localWorkspaceAssetKind,
) []localWorkspaceAssetKind {
	result := append([]localWorkspaceAssetKind(nil), kinds...)
	sort.Slice(result, func(left, right int) bool { return result[left] < result[right] })
	return result
}

func containsLocalWorkspaceAssetKind(
	kinds []localWorkspaceAssetKind,
	kind localWorkspaceAssetKind,
) bool {
	for _, candidate := range kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func localWorkspaceAssetKindsToStrings(kinds []localWorkspaceAssetKind) []string {
	result := make([]string, len(kinds))
	for index, kind := range kinds {
		result[index] = string(kind)
	}
	return result
}

func emptyLocalWorkspaceMemoryListResponse(
	request localWorkspaceMemoryListRequest,
) localWorkspaceMemoryListResponse {
	return localWorkspaceMemoryListResponse{
		SchemaVersion: 1,
		CompanyID:     request.CompanyID,
		Scope:         request.Scope,
		Files:         []localWorkspaceMemoryFile{},
	}
}

func readLocalWorkspaceMemory(rootPath string) ([]localWorkspaceMemoryFile, bool, error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, false, err
	}
	defer root.Close()

	directoryInfo, err := root.Lstat(localWorkspaceMemoryDirectory)
	if errors.Is(err, fs.ErrNotExist) {
		return []localWorkspaceMemoryFile{}, false, nil
	}
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("workspace memory directory is invalid")
	}
	directory, err := root.Open(localWorkspaceMemoryDirectory)
	if err != nil {
		return nil, false, err
	}
	openedDirectoryInfo, statErr := directory.Stat()
	if statErr != nil || !openedDirectoryInfo.IsDir() || !os.SameFile(directoryInfo, openedDirectoryInfo) {
		closeErr := directory.Close()
		return nil, false, errors.Join(
			errors.New("workspace memory directory changed while opening"),
			statErr,
			closeErr,
		)
	}
	entries, readDirectoryErr := directory.ReadDir(-1)
	closeDirectoryErr := directory.Close()
	if readDirectoryErr != nil || closeDirectoryErr != nil {
		return nil, false, errors.Join(readDirectoryErr, closeDirectoryErr)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })

	files := make([]localWorkspaceMemoryFile, 0, min(len(entries), maximumLocalWorkspaceMemoryFiles))
	totalBytes := 0
	truncated := false
	for _, entry := range entries {
		if !localWorkspaceMemoryFilenamePattern.MatchString(entry.Name()) ||
			entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if len(files) == maximumLocalWorkspaceMemoryFiles {
			truncated = true
			break
		}
		path := filepath.ToSlash(filepath.Join(localWorkspaceMemoryDirectory, entry.Name()))
		pathInfo, err := root.Lstat(filepath.FromSlash(path))
		if err != nil {
			return nil, false, err
		}
		if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() ||
			pathInfo.Size() > maximumLocalWorkspaceMemoryFileBytes {
			continue
		}
		file, err := root.Open(filepath.FromSlash(path))
		if err != nil {
			return nil, false, err
		}
		openedInfo, statErr := file.Stat()
		if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(pathInfo, openedInfo) {
			closeErr := file.Close()
			return nil, false, errors.Join(
				errors.New("workspace memory file changed while opening"),
				statErr,
				closeErr,
			)
		}
		raw, readErr := io.ReadAll(io.LimitReader(file, maximumLocalWorkspaceMemoryFileBytes+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, false, errors.Join(readErr, closeErr)
		}
		if len(raw) > maximumLocalWorkspaceMemoryFileBytes || !utf8.Valid(raw) ||
			bytes.IndexByte(raw, 0) >= 0 {
			continue
		}
		if totalBytes+len(raw) > maximumLocalWorkspaceMemoryResponseBytes {
			truncated = true
			break
		}
		totalBytes += len(raw)
		files = append(files, localWorkspaceMemoryFile{
			Path:       path,
			Filename:   entry.Name(),
			Markdown:   string(raw),
			SizeBytes:  openedInfo.Size(),
			ModifiedAt: openedInfo.ModTime().UTC(),
		})
	}
	return files, truncated, nil
}
