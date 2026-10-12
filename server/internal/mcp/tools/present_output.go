package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/mcp/apiclient"
)

const (
	maxPresentedWorkspaceFileBytes = int64(100 << 20)
	maxPresentedOutputItems        = 32
	maxPresentedOutputTitleRunes   = 160
	maxPresentedOutputLabelRunes   = 120
	maxPresentedOutputDetailRunes  = 4000
	maxPresentedAppCommands        = 8
	maxPresentedAppArguments       = 64
)

type PresentOutputInput struct {
	Page             *PresentOutputPage  `json:"page,omitempty"             jsonschema:"Native Page delivery: exact page_preview identity, sequence, fingerprint and optional changeId. Mutually exclusive with file/app/studio inputs. Preserves suggestions without applying them."`
	StudioCanvasPath string              `json:"studioCanvasPath,omitempty" jsonschema:"Workspace-relative JSON file produced by zenith-studio canvas export. Read and attach the CLI source graph automatically; use this instead of copying nodes into studio. Mutually exclusive with studio."`
	Type             string              `json:"type,omitempty"             jsonschema:"Artifact type: app, media, text, or file (default). Use app for a local web application, media for image/audio/video work, text for plans and readable documents, and file for general downloads."`
	App              *PresentOutputApp   `json:"app,omitempty"              jsonschema:"Required for type=app and forbidden for all other types."`
	Title            string              `json:"title,omitempty"            jsonschema:"Short, specific artifact name. Required for file, media, text and app; native Page deliveries use the Page title."`
	Summary          string              `json:"summary"                    jsonschema:"What the artifact contains and what the user should know."`
	Items            []PresentOutputItem `json:"items,omitempty"            jsonschema:"For media, text, and file artifacts: complete ordered contents with exactly one primary item. Omit for app."`
	SubmissionNotes  string              `json:"submissionNotes,omitempty"  jsonschema:"Optional checks or known limitations for the presented artifact."`
	Studio           *StudioCanvas       `json:"studio,omitempty"           jsonschema:"For Zenith Studio media: include the source canvas returned by the CLI, with real node positions and dependencies. The tool saves it as a supporting source artifact automatically. Omit if the CLI cannot expose the graph; never fabricate it. Up to 31 media items when supplied."`
}

type PresentOutputPage struct {
	PageID      string `json:"pageId"`
	ChangeID    string `json:"changeId,omitempty"`
	Sequence    int64  `json:"sequence"`
	Fingerprint string `json:"fingerprint"`
}

type PresentOutputApp struct {
	Commands []PresentOutputCommand `json:"commands" jsonschema:"One to eight structured development commands. The first command is the primary preview; every command declares the HTTP port it serves."`
}

type PresentOutputCommand struct {
	Command          string   `json:"command"                    jsonschema:"Executable name or workspace-relative executable path. Do not include shell syntax or arguments."`
	Args             []string `json:"args,omitempty"             jsonschema:"Ordered arguments passed directly to the executable without a shell."`
	WorkingDirectory string   `json:"workingDirectory,omitempty" jsonschema:"Workspace-relative working directory. Defaults to the workspace root."`
	Port             int      `json:"port"                       jsonschema:"Loopback HTTP port served by this command (1024-65535). Ports must be unique."`
}

type PresentOutputItem struct {
	Role   string              `json:"role"   jsonschema:"primary: the lead final result (exactly one); final: each additional completed deliverable requested by the user; supporting: source or intermediate files, without a final-result badge. For three requested images use primary, final, final; for one assembled video use primary for the video and supporting for source clips."`
	Label  string              `json:"label"  jsonschema:"Product-facing label such as Source, Report, or Data appendix."`
	Target PresentOutputTarget `json:"target"`
}

type PresentOutputTarget struct {
	Kind                  string `json:"kind"                  jsonschema:"workspace_file"`
	WorkspaceRelativePath string `json:"workspaceRelativePath" jsonschema:"File path relative to the root of this Run workspace, regardless of the shell current directory. Absolute paths, parent traversal, and symlinks are not allowed."`
}

type PresentOutputResult struct {
	OutputID string `json:"outputId"`
}

type presentOutputRequest struct {
	Type            string                     `json:"type,omitempty"`
	App             *PresentOutputApp          `json:"app,omitempty"`
	Title           string                     `json:"title"`
	Summary         string                     `json:"summary"`
	SubmissionNotes *string                    `json:"submissionNotes,omitempty"`
	Items           []presentOutputRequestItem `json:"items,omitempty"`
}

type presentOutputRequestItem struct {
	Role         string `json:"role"`
	Label        string `json:"label"`
	AttachmentID string `json:"attachmentId"`
}

type presentOutputResponse struct {
	OutputID string `json:"outputId"`
}

type outputAPI interface {
	UploadAttachment(context.Context, apiclient.AttachmentUpload, io.ReadSeeker) (string, error)
	PostRunJSON(context.Context, string, string, any, any) error
}

type AppPreviewRegistrar interface {
	RegisterAppPreview(context.Context, string, []PresentOutputCommand) error
}

// FileGuard refuses a workspace file, given its real path, that the service
// must not read for the Run. The daemon reads outside the Run's sandbox, so
// its guard refuses what the sandbox denies the Run: the sign-in files,
// every Run's isolated home (Access TECH B3e) and, for a Run tagged in a
// chat, the folders its sandbox hides (B7b). Nil refuses nothing.
type FileGuard func(realPath string) error

// PresentOutputService is the complete exported output-presentation surface.
type PresentOutputService interface {
	PresentOutput(context.Context, PresentOutputInput) (PresentOutputResult, error)
}

var _ PresentOutputService = (*DefaultPresentOutputService)(nil)

type DefaultPresentOutputService struct {
	api           outputAPI
	runContext    RunContext
	workspaceRoot string
	guard         FileGuard
	appPreviews   AppPreviewRegistrar
}

func NewPresentOutputService(
	api outputAPI,
	runContext RunContext,
	workspaceRoot string,
	guard FileGuard,
	registrars ...AppPreviewRegistrar,
) (*DefaultPresentOutputService, error) {
	if api == nil || !validPresentOutputContext(runContext, workspaceRoot) {
		return nil, errors.New("Run-bound Zenith Team output presentation is unavailable")
	}
	var appPreviews AppPreviewRegistrar
	if len(registrars) > 0 {
		appPreviews = registrars[0]
	}
	return &DefaultPresentOutputService{
		api: api, runContext: runContext, workspaceRoot: workspaceRoot,
		guard: guard, appPreviews: appPreviews,
	}, nil
}

func (s *DefaultPresentOutputService) PresentOutput(
	ctx context.Context,
	input PresentOutputInput,
) (PresentOutputResult, error) {
	if s == nil || s.api == nil || !validPresentOutputContext(s.runContext, s.workspaceRoot) {
		return PresentOutputResult{}, errors.New("Run-bound Zenith Team output presentation is unavailable")
	}
	if input.Page != nil {
		page := input.Page
		if input.App != nil || len(input.Items) > 0 || input.Studio != nil || input.StudioCanvasPath != "" ||
			(input.Type != "" && input.Type != "text") ||
			strings.TrimSpace(input.Summary) == "" ||
			utf8.RuneCountInString(input.Summary) > maxPresentedOutputDetailRunes ||
			utf8.RuneCountInString(input.SubmissionNotes) > maxPresentedOutputDetailRunes ||
			page.Sequence < 0 ||
			len(page.Fingerprint) != 64 {
			return PresentOutputResult{}, errors.New("invalid native Page delivery")
		}
		if _, err := uuid.Parse(page.PageID); err != nil {
			return PresentOutputResult{}, errors.New("invalid Page identity")
		}
		return presentNativePage(
			ctx,
			s.api,
			s.runContext,
			PagePresentInput{
				PageID:          page.PageID,
				ChangeID:        page.ChangeID,
				Sequence:        page.Sequence,
				Fingerprint:     page.Fingerprint,
				Summary:         input.Summary,
				SubmissionNotes: input.SubmissionNotes,
			},
		)
	}
	if input.StudioCanvasPath != "" {
		if input.Studio != nil {
			return PresentOutputResult{}, errors.New("supply studioCanvasPath or studio, not both")
		}
		canvas, err := s.readStudioCanvasFile(input.StudioCanvasPath)
		if err != nil {
			return PresentOutputResult{}, err
		}
		input.Studio = canvas
	}
	normalized, err := normalizePresentOutputInput(input)
	if err != nil {
		return PresentOutputResult{}, err
	}
	request := presentOutputRequest{
		Type:  normalized.Type,
		Title: normalized.Title, Summary: normalized.Summary,
		Items: make([]presentOutputRequestItem, len(normalized.Items)),
	}
	if normalized.Type == "app" {
		if s.appPreviews == nil {
			return PresentOutputResult{}, errors.New("local app preview registration is unavailable")
		}
		request.App = normalized.App
	}
	if normalized.SubmissionNotes != "" {
		request.SubmissionNotes = &normalized.SubmissionNotes
	}
	files := make([]*presentedWorkspaceSnapshot, len(normalized.Items))
	uploads := make([]apiclient.AttachmentUpload, len(normalized.Items))
	for index, item := range normalized.Items {
		request.Items[index] = presentOutputRequestItem{Role: item.Role, Label: item.Label}
		file, upload, err := openPresentedWorkspaceFile(
			s.workspaceRoot, s.guard, s.runContext.RunID, s.runContext.CompanyID,
			item.Target.WorkspaceRelativePath,
		)
		if err != nil {
			var pathErr *os.PathError
			if errors.As(err, &pathErr) {
				err = fmt.Errorf("workspace output could not be packaged: %w", pathErr.Err)
			}
			return PresentOutputResult{}, errors.Join(err, closePresentedFiles(files))
		}
		if item.Role == "primary" && !presentedArtifactContentMatches(normalized.Type, upload.ContentType) {
			return PresentOutputResult{}, errors.Join(
				fmt.Errorf(
					"%s artifact primary item has unsupported content type %s",
					normalized.Type,
					upload.ContentType,
				),
				file.Close(),
				closePresentedFiles(files),
			)
		}
		files[index], uploads[index] = file, upload
	}
	for index, file := range files {
		attachmentID, uploadErr := s.api.UploadAttachment(ctx, uploads[index], file)
		closeErr := file.Close()
		files[index] = nil
		if uploadErr != nil || closeErr != nil {
			return PresentOutputResult{}, fmt.Errorf(
				"upload workspace output: %w",
				errors.Join(uploadErr, closeErr, closePresentedFiles(files)),
			)
		}
		request.Items[index].AttachmentID = attachmentID
	}
	if normalized.Studio != nil {
		canvasItem, err := s.uploadStudioCanvas(ctx, normalized.Studio)
		if err != nil {
			return PresentOutputResult{}, err
		}
		request.Items = append(request.Items, canvasItem)
	}
	requestKey, err := deterministicOutputRequestKey(s.runContext.RunID, request)
	if err != nil {
		return PresentOutputResult{}, err
	}
	var response presentOutputResponse
	if err := s.api.PostRunJSON(
		ctx,
		"/runs/"+s.runContext.RunID+"/output",
		requestKey.String(),
		request,
		&response,
	); err != nil {
		return PresentOutputResult{}, fmt.Errorf("present Run output: %w", err)
	}
	outputID, err := uuid.Parse(response.OutputID)
	if err != nil || outputID == uuid.Nil {
		return PresentOutputResult{}, errors.New("server returned an invalid Output identity")
	}
	if normalized.Type == "app" {
		if err := s.appPreviews.RegisterAppPreview(
			ctx, outputID.String(), normalized.App.Commands,
		); err != nil {
			return PresentOutputResult{}, fmt.Errorf("register local app preview: %w", err)
		}
	}
	return PresentOutputResult{OutputID: outputID.String()}, nil
}

func normalizePresentOutputInput(input PresentOutputInput) (PresentOutputInput, error) {
	if input.Type == "" {
		if input.Studio != nil {
			input.Type = "media"
		} else {
			input.Type = "file"
		}
	}
	if input.Studio != nil {
		if input.Type == "app" {
			return PresentOutputInput{}, errors.New("studio canvas is not supported for app artifacts")
		}
		if err := validateStudioCanvas(input.Studio, len(input.Items)); err != nil {
			return PresentOutputInput{}, err
		}
	}
	if input.Type == "app" {
		if len(input.Items) != 0 || input.App == nil {
			return PresentOutputInput{}, errors.New("app artifact requires development commands, not items")
		}
		commands, err := normalizePresentOutputCommands(input.App.Commands)
		if err != nil {
			return PresentOutputInput{}, err
		}
		input.App = &PresentOutputApp{Commands: commands}
	} else if input.Type != "media" && input.Type != "text" && input.Type != "file" || input.App != nil {
		return PresentOutputInput{}, errors.New(
			"artifact type must be app, media, text, or file; commands are only for app",
		)
	}
	input.Title = strings.TrimSpace(input.Title)
	input.Summary = strings.TrimSpace(input.Summary)
	input.SubmissionNotes = strings.TrimSpace(input.SubmissionNotes)
	invalidRequiredText := !validPresentedOutputText(input.Title, maxPresentedOutputTitleRunes) ||
		!validPresentedOutputText(input.Summary, maxPresentedOutputDetailRunes)
	invalidNotes := input.SubmissionNotes != "" &&
		!validPresentedOutputText(input.SubmissionNotes, maxPresentedOutputDetailRunes)
	invalidCardinality := input.Type != "app" &&
		(len(input.Items) < 1 || len(input.Items) > maxPresentedOutputItems)
	if invalidRequiredText || invalidNotes || invalidCardinality {
		return PresentOutputInput{}, errors.New("output presentation is invalid")
	}
	items := make([]PresentOutputItem, len(input.Items))
	primaryCount := 0
	for index, item := range input.Items {
		switch item.Role {
		case "primary":
			primaryCount++
		case "final", "supporting":
		default:
			return PresentOutputInput{}, errors.New(
				"output item role must be primary, final, or supporting",
			)
		}
		item.Label = strings.TrimSpace(item.Label)
		if !validPresentedOutputText(item.Label, maxPresentedOutputLabelRunes) {
			return PresentOutputInput{}, errors.New("output item label is invalid")
		}
		if input.Type != "app" && item.Target.Kind != "workspace_file" {
			return PresentOutputInput{}, errors.New("output target kind is unsupported")
		}
		if input.Type != "app" && !validWorkspaceRelativePath(item.Target.WorkspaceRelativePath) {
			return PresentOutputInput{}, errors.New("workspace output target is invalid")
		}
		if input.Studio != nil && isStudioCanvasPath(filepath.ToSlash(item.Target.WorkspaceRelativePath)) {
			return PresentOutputInput{}, errors.New(
				"studio canvas is attached automatically; omit it from items",
			)
		}
		items[index] = item
	}
	if input.Type != "app" && primaryCount != 1 {
		return PresentOutputInput{}, errors.New("output must have exactly one primary item")
	}
	input.Items = items
	return input, nil
}

func normalizePresentOutputCommands(commands []PresentOutputCommand) ([]PresentOutputCommand, error) {
	if len(commands) < 1 || len(commands) > maxPresentedAppCommands {
		return nil, errors.New("app artifact requires one to eight development commands")
	}
	normalized := make([]PresentOutputCommand, len(commands))
	ports := make(map[int]struct{}, len(commands))
	for index, command := range commands {
		if !validPresentedCommandValue(command.Command) || command.Port < 1024 ||
			command.Port > 65535 || len(command.Args) > maxPresentedAppArguments {
			return nil, errors.New("app development command is invalid")
		}
		if _, duplicate := ports[command.Port]; duplicate {
			return nil, errors.New("app development command ports must be unique")
		}
		ports[command.Port] = struct{}{}
		for _, argument := range command.Args {
			if !utf8.ValidString(argument) || len(argument) > 4096 ||
				strings.ContainsAny(argument, "\x00\r\n") {
				return nil, errors.New("app development command argument is invalid")
			}
		}
		workingDirectory := command.WorkingDirectory
		if workingDirectory == "" {
			workingDirectory = "."
		}
		if filepath.IsAbs(workingDirectory) || filepath.Clean(workingDirectory) != workingDirectory ||
			workingDirectory == ".." || strings.HasPrefix(workingDirectory, ".."+string(filepath.Separator)) {
			return nil, errors.New("app development command working directory is invalid")
		}
		command.WorkingDirectory = workingDirectory
		command.Args = append([]string(nil), command.Args...)
		normalized[index] = command
	}
	return normalized, nil
}

func validPresentedCommandValue(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && utf8.ValidString(value) &&
		len(value) <= 4096 && strings.IndexFunc(value, func(r rune) bool {
		return r == 0 || r <= ' ' || r == 0x7f
	}) < 0
}

func presentedArtifactContentMatches(artifactType, contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	switch artifactType {
	case "media":
		return strings.HasPrefix(contentType, "image/") || strings.HasPrefix(contentType, "video/") ||
			strings.HasPrefix(contentType, "audio/")
	case "text":
		return strings.HasPrefix(contentType, "text/") ||
			contentType == "application/json" || strings.HasSuffix(contentType, "+json") ||
			contentType == "application/xml" || strings.HasSuffix(contentType, "+xml") ||
			contentType == "application/yaml" || contentType == "application/x-yaml" ||
			contentType == "application/octet-stream"
	case "file":
		return true
	default:
		return false
	}
}

func validPresentedOutputText(value string, maximumRunes int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximumRunes
}

type presentedWorkspaceSnapshot struct {
	*os.File
}

func (s *presentedWorkspaceSnapshot) Close() error {
	if s == nil || s.File == nil {
		return nil
	}
	name := s.Name()
	err := errors.Join(s.File.Close(), os.Remove(name))
	s.File = nil
	return err
}

func closePresentedFiles(files []*presentedWorkspaceSnapshot) error {
	var errs []error
	for _, file := range files {
		if file != nil {
			errs = append(errs, file.Close())
		}
	}
	return errors.Join(errs...)
}

func validPresentOutputContext(runContext RunContext, workspaceRoot string) bool {
	runID, err := uuid.Parse(runContext.RunID)
	return err == nil && runID != uuid.Nil && runID.String() == runContext.RunID &&
		runContext.CompanyID != "" && runContext.AgentID != "" && runContext.ConversationID != "" &&
		filepath.IsAbs(workspaceRoot) && filepath.Clean(workspaceRoot) == workspaceRoot
}

func openPresentedWorkspaceFile(
	workspaceRoot string, guard FileGuard, runID, companyID, relativePath string,
) (*presentedWorkspaceSnapshot, apiclient.AttachmentUpload, error) {
	if !filepath.IsAbs(workspaceRoot) || filepath.Clean(workspaceRoot) != workspaceRoot ||
		!validWorkspaceRelativePath(relativePath) {
		return nil, apiclient.AttachmentUpload{}, errors.New(
			"workspace file path is outside the bound Run workspace",
		)
	}
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return nil, apiclient.AttachmentUpload{}, errors.New("bound Run workspace is unavailable")
	}
	candidate := filepath.Join(root, relativePath)
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || resolved != candidate {
		return nil, apiclient.AttachmentUpload{}, errors.New("workspace file symlinks are not allowed")
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, apiclient.AttachmentUpload{}, errors.New(
			"workspace file path is outside the bound Run workspace",
		)
	}
	// The path has no symbolic link, so the guard sees the real path. A
	// swap after this check is caught by the checks after the open.
	if guard != nil {
		if err := guard(resolved); err != nil {
			return nil, apiclient.AttachmentUpload{}, err
		}
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 ||
		info.Size() > maxPresentedWorkspaceFileBytes {
		return nil, apiclient.AttachmentUpload{}, errors.New(
			"workspace file must be a regular file no larger than 100 MB",
		)
	}
	source, err := os.Open(resolved)
	if err != nil {
		return nil, apiclient.AttachmentUpload{}, errors.New("workspace file is unavailable")
	}
	openedInfo, statErr := source.Stat()
	currentInfo, lstatErr := os.Lstat(resolved)
	currentResolved, resolveErr := filepath.EvalSymlinks(candidate)
	if statErr != nil || lstatErr != nil || resolveErr != nil ||
		!openedInfo.Mode().IsRegular() || !currentInfo.Mode().IsRegular() ||
		!os.SameFile(openedInfo, currentInfo) || currentResolved != candidate {
		_ = source.Close()
		return nil, apiclient.AttachmentUpload{}, errors.New("workspace file changed while it was opened")
	}
	staging, err := os.CreateTemp("", "boardly-present-output-*")
	if err != nil {
		_ = source.Close()
		return nil, apiclient.AttachmentUpload{}, errors.New("workspace file could not be staged")
	}
	stagingPath := staging.Name()
	hasher := sha256.New()
	limited := &io.LimitedReader{R: source, N: maxPresentedWorkspaceFileBytes + 1}
	sizeBytes, copyErr := io.Copy(io.MultiWriter(staging, hasher), limited)
	sourceCloseErr := source.Close()
	if copyErr != nil || sourceCloseErr != nil {
		_ = errors.Join(staging.Close(), os.Remove(stagingPath))
		return nil, apiclient.AttachmentUpload{}, errors.New("workspace file could not be read")
	}
	if sizeBytes > maxPresentedWorkspaceFileBytes {
		_ = errors.Join(staging.Close(), os.Remove(stagingPath))
		return nil, apiclient.AttachmentUpload{}, errors.New(
			"workspace file must be a regular file no larger than 100 MB",
		)
	}
	digest := hasher.Sum(nil)
	if err := errors.Join(staging.Close(), os.Chmod(stagingPath, 0o400)); err != nil {
		_ = os.Remove(stagingPath)
		return nil, apiclient.AttachmentUpload{}, errors.New("workspace file could not be staged")
	}
	stagedFile, err := os.Open(stagingPath)
	if err != nil {
		_ = os.Remove(stagingPath)
		return nil, apiclient.AttachmentUpload{}, errors.New("workspace file could not be staged")
	}
	snapshot := &presentedWorkspaceSnapshot{File: stagedFile}
	requestKey := deterministicAttachmentRequestKey(runID, relativePath, digest)
	contentType := presentedContentType(filepath.Ext(resolved))
	return snapshot, apiclient.AttachmentUpload{
		RequestKey: requestKey.String(), CompanyID: companyID,
		Filename: filepath.Base(resolved), ContentType: contentType, SizeBytes: sizeBytes,
	}, nil
}

func validWorkspaceRelativePath(relativePath string) bool {
	return relativePath != "" && !filepath.IsAbs(relativePath) &&
		filepath.Clean(relativePath) == relativePath && relativePath != "." &&
		!strings.HasPrefix(relativePath, ".."+string(filepath.Separator))
}

func deterministicAttachmentRequestKey(requestScope, relativePath string, digest []byte) uuid.UUID {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(requestScope))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(filepath.ToSlash(relativePath)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write(digest)
	return uuidFromDigest(hasher.Sum(nil))
}

func deterministicOutputRequestKey(runID string, request presentOutputRequest) (uuid.UUID, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return uuid.Nil, errors.New("output presentation could not be encoded")
	}
	digest := sha256.Sum256(append([]byte(runID+"\x00"), encoded...))
	return uuidFromDigest(digest[:]), nil
}

func uuidFromDigest(digest []byte) uuid.UUID {
	var id uuid.UUID
	copy(id[:], digest[:16])
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

// presentedTextTypes names the text files Agents produce that the platform
// mime table does not know on every OS. Markdown is the common case: as
// application/octet-stream the viewer offers a download instead of the
// note the person asked for.
var presentedTextTypes = map[string]string{
	".md": "text/markdown", ".markdown": "text/markdown", ".txt": "text/plain",
	".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".yaml": "application/yaml", ".yml": "application/yaml", ".toml": "text/plain",
	".go": "text/plain", ".py": "text/plain", ".rb": "text/plain", ".rs": "text/plain",
	".ts": "text/plain", ".tsx": "text/plain", ".jsx": "text/plain",
	".sql": "text/plain", ".sh": "text/plain", ".env": "text/plain", ".log": "text/plain",
}

func presentedContentType(extension string) string {
	extension = strings.ToLower(extension)
	contentType := mime.TypeByExtension(extension)
	if separator := strings.IndexByte(contentType, ';'); separator >= 0 {
		contentType = contentType[:separator]
	}
	if contentType == "" {
		contentType = presentedTextTypes[extension]
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return contentType
}
