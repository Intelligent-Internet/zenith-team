package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/mcp/companyplan"
)

const (
	CompanyPlanApplyToolName   = "company_plan_transaction_apply"
	companyPlanActionsFilename = "company-plan-actions.yaml"
	companyPlanExportFilename  = "company-plan-export.yaml"
)

type API interface {
	companyplan.API
	companyplan.ApplyAPI
}

// CompanyPlanService owns the Boardly tools served over MCP: the enabled
// Agent roster and the Company Plan export, preview, and apply flow.
type CompanyPlanService interface {
	ListAgents(context.Context, ListAgentsInput) (ListAgentsResult, error)
	Export(context.Context, ExportInput) (ExportResult, error)
	Preview(context.Context, PreviewInput) (PreviewResult, error)
	Apply(context.Context, ApplyInput) (ApplyResult, error)
}

var _ CompanyPlanService = (*DefaultCompanyPlanService)(nil)

type DefaultCompanyPlanService struct {
	api           API
	now           func() time.Time
	workspaceRoot string
	mu            sync.Mutex
	bindings      map[string]*previewBinding
	pathLocks     map[string]*sync.Mutex
}

type previewBinding struct {
	artifact companyplan.Artifact
	digest   string
	applying bool
}

func NewCompanyPlanService(api API) *DefaultCompanyPlanService {
	return NewCompanyPlanServiceWithClock(api, time.Now)
}

func NewCompanyPlanServiceWithClock(api API, now func() time.Time) *DefaultCompanyPlanService {
	return &DefaultCompanyPlanService{
		api: api, now: now, bindings: map[string]*previewBinding{}, pathLocks: map[string]*sync.Mutex{},
	}
}

func NewCompanyPlanServiceForWorkspace(
	api API,
	workspaceRoot string,
) (*DefaultCompanyPlanService, error) {
	root, err := canonicalWorkspaceRoot(workspaceRoot)
	if err != nil {
		return nil, err
	}
	service := NewCompanyPlanService(api)
	service.workspaceRoot = root
	return service, nil
}

type ExportInput struct {
	OutputPath string `json:"outputPath,omitempty" jsonschema:"Optional. Omit it: the export is written to the Run's canonical Company Plan export file. When given it must name that same file."`
	Overwrite  bool   `json:"overwrite,omitempty"  jsonschema:"Replace an existing output file. Defaults to false."`
}

type ExportResult struct {
	OutputPath string `json:"outputPath"`
	Tasks      int    `json:"tasks"`
}

func (s *DefaultCompanyPlanService) Export(ctx context.Context, input ExportInput) (ExportResult, error) {
	if s == nil || s.api == nil {
		return ExportResult{}, errors.New("Zenith Team API client is unavailable")
	}
	path, err := s.canonicalExportPath(input.OutputPath)
	if err != nil {
		return ExportResult{}, err
	}
	contents, err := companyplan.ExportOnlyCompany(ctx, s.api)
	if err != nil {
		return ExportResult{}, err
	}
	_, summary, err := companyplan.ParseAndValidate(contents)
	if err != nil {
		return ExportResult{}, fmt.Errorf("validate exported Company Plan: %w", err)
	}
	if err := writeOutputFile(path, contents, input.Overwrite); err != nil {
		return ExportResult{}, err
	}
	return ExportResult{
		OutputPath: path,
		Tasks:      summary.Tasks,
	}, nil
}

type PreviewInput struct {
	ActionsPath string `json:"actionsPath" jsonschema:"Absolute path to the Company Plan transaction actions YAML file."`
}

type PreviewResult struct {
	Ready        bool                 `json:"ready"`
	Preview      string               `json:"preview"`
	Digest       string               `json:"draftDigest"`
	TotalChanges int                  `json:"totalChanges"`
	Changes      []companyplan.Change `json:"changes"`
}

func (s *DefaultCompanyPlanService) Preview(ctx context.Context, input PreviewInput) (PreviewResult, error) {
	if s == nil || s.api == nil || s.now == nil {
		return PreviewResult{}, errors.New("Zenith Team Company Plan service is unavailable")
	}
	path, err := s.canonicalActionsPath(input.ActionsPath)
	if err != nil {
		return PreviewResult{}, err
	}
	pathLock := s.lockForPath(path)
	pathLock.Lock()
	defer pathLock.Unlock()
	contents, err := readRegularFile(path, companyplan.MaxActionBytes, "Company Plan transaction YAML")
	if err != nil {
		return PreviewResult{}, err
	}
	transaction, digest, err := companyplan.ParseActions(contents)
	if err != nil {
		return PreviewResult{}, err
	}
	artifact, err := companyplan.PlanActions(ctx, s.api, transaction, s.now())
	if err != nil {
		return PreviewResult{}, err
	}
	changes := presentChanges(artifact.Changes, transaction)
	preview := formatPreview(changes)
	s.mu.Lock()
	s.bindings[path] = &previewBinding{
		artifact: artifact, digest: digest,
	}
	s.mu.Unlock()
	return PreviewResult{
		Ready:        true,
		Preview:      preview,
		Digest:       digest,
		TotalChanges: len(changes),
		Changes:      changes,
	}, nil
}

type ApplyInput struct {
	ActionsPath string `json:"actionsPath" jsonschema:"Absolute path to the previously previewed Company Plan transaction actions YAML file."`
}

type ApplyResult struct {
	ApprovalID     string `json:"approvalId"`
	ApprovalStatus string `json:"approvalStatus"`
	Instruction    string `json:"instruction"`
}

func (s *DefaultCompanyPlanService) Apply(ctx context.Context, input ApplyInput) (ApplyResult, error) {
	if s == nil || s.api == nil {
		return ApplyResult{}, errors.New("Zenith Team API client is unavailable")
	}
	path, err := s.canonicalActionsPath(input.ActionsPath)
	if err != nil {
		return ApplyResult{}, err
	}
	pathLock := s.lockForPath(path)
	pathLock.Lock()
	defer pathLock.Unlock()
	contents, err := readRegularFile(path, companyplan.MaxActionBytes, "Company Plan transaction YAML")
	if err != nil {
		return ApplyResult{}, err
	}
	_, digest, err := companyplan.ParseActions(contents)
	if err != nil {
		return ApplyResult{}, err
	}
	s.mu.Lock()
	binding := s.bindings[path]
	if binding == nil || binding.digest != digest {
		s.mu.Unlock()
		return ApplyResult{}, errors.New("preview_required: preview this unchanged action YAML before apply")
	}
	if binding.applying {
		s.mu.Unlock()
		return ApplyResult{}, errors.New("Company Plan transaction apply is already in progress")
	}
	binding.applying = true
	artifact := binding.artifact
	s.mu.Unlock()
	requestKey := uuid.NewSHA1(
		uuid.NameSpaceURL,
		[]byte("boardly:company-plan-apply:"+digest),
	).String()
	backendResult, err := companyplan.Apply(ctx, s.api, artifact, requestKey)
	s.mu.Lock()
	binding.applying = false
	s.mu.Unlock()
	if err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{
		ApprovalID:     backendResult.Approval.ID,
		ApprovalStatus: backendResult.Approval.Status,
		Instruction:    companyPlanApplyInstruction(backendResult.Approval.Status),
	}, nil
}

func companyPlanApplyInstruction(status string) string {
	switch status {
	case "pending":
		return "The Company Plan approval is pending human review. End this run without retrying or applying the plan again."
	case "approved":
		return "The Company Plan is approved, and the server has already handled the action. End this run without retrying or applying the plan again."
	default:
		return "The Company Plan approval is " + status + ". End this run without retrying or applying the plan again."
	}
}

func formatPreview(changes []companyplan.Change) string {
	if len(changes) == 0 {
		return "Company Plan transaction preview\n\nNo changes."
	}
	var builder strings.Builder
	builder.WriteString("Company Plan transaction preview\n")
	counts := map[string]int{}
	for _, change := range changes {
		counts[change.Operation]++
		symbol := map[string]string{"create": "+", "update": "~", "cancel": "×", "archive": "−"}[change.Operation]
		fmt.Fprintf(&builder, "\n%s %s %s %s\n", symbol, change.Operation, change.EntityType, change.Key)
		for _, field := range change.Fields {
			fmt.Fprintf(&builder, "    %s: %q -> %q\n", field.Field, field.Before, field.After)
		}
	}
	fmt.Fprintf(
		&builder,
		"\nSummary: %d create, %d update, %d cancel, %d archive",
		counts["create"],
		counts["update"],
		counts["cancel"],
		counts["archive"],
	)
	return builder.String()
}

func presentChanges(changes []companyplan.Change, transaction companyplan.ActionDocument) []companyplan.Change {
	cancelled := map[string]bool{}
	for _, action := range transaction.Actions {
		if action.Operation == "cancel" {
			cancelled[action.Entity+"\x00"+action.Key] = true
		}
	}
	result := make([]companyplan.Change, len(changes))
	copy(result, changes)
	for index := range result {
		if cancelled[result[index].EntityType+"\x00"+result[index].Key] {
			result[index].Operation = "cancel"
		}
	}
	return result
}

func absolutePath(value string) (string, error) {
	if strings.TrimSpace(value) != value || value == "" || !filepath.IsAbs(value) {
		return "", errors.New("file path must be an absolute path without surrounding whitespace")
	}
	return filepath.Clean(value), nil
}

func canonicalInputPath(value string) (string, error) {
	path, err := absolutePath(value)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve input file path: %w", err)
	}
	return filepath.Clean(resolved), nil
}

func canonicalWorkspaceRoot(value string) (string, error) {
	path, err := absolutePath(value)
	if err != nil {
		return "", fmt.Errorf("resolve Company Plan workspace: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve Company Plan workspace: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("inspect Company Plan workspace: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("Company Plan workspace must be a directory")
	}
	return filepath.Clean(resolved), nil
}

func (s *DefaultCompanyPlanService) canonicalActionsPath(value string) (string, error) {
	path, err := canonicalInputPath(value)
	if err != nil {
		return "", err
	}
	expected := filepath.Join(filepath.Dir(path), companyPlanActionsFilename)
	if s.workspaceRoot != "" {
		expected = filepath.Join(s.workspaceRoot, companyPlanActionsFilename)
	}
	if path != expected {
		return "", fmt.Errorf(
			"Company Plan transaction must use the canonical workspace draft %q",
			expected,
		)
	}
	return path, nil
}

// canonicalExportPath is where the export goes. Rooted in a Run environment
// there is exactly one place, beside the actions draft: both are Run-local
// scratch, and letting the model choose a path is how they ended up in the
// repository the Agent works in. An omitted path means that place; a given
// path must name it, resolved through symlinks the way the root was, so the
// prompt's spelling and the daemon's canonical form both pass. Without a
// root (a hand-started process) a path is required and any absolute path is
// accepted.
func (s *DefaultCompanyPlanService) canonicalExportPath(value string) (string, error) {
	if s.workspaceRoot == "" {
		return absolutePath(value)
	}
	expected := filepath.Join(s.workspaceRoot, companyPlanExportFilename)
	if value == "" {
		return expected, nil
	}
	path, err := absolutePath(value)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(resolved, filepath.Base(path))
	}
	if path != expected {
		return "", fmt.Errorf(
			"Company Plan export must be written to the canonical workspace export %q",
			expected,
		)
	}
	return expected, nil
}

func (s *DefaultCompanyPlanService) lockForPath(path string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock := s.pathLocks[path]
	if lock == nil {
		lock = &sync.Mutex{}
		s.pathLocks[path] = lock
	}
	return lock
}

func readRegularFile(path string, maximum int64, label string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", label)
	}
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d MiB", label, maximum>>20)
	}
	return contents, nil
}

func writeOutputFile(path string, contents []byte, overwrite bool) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".boardly-company-plan-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if !overwrite {
		// Linking a same-directory temporary file publishes the completed output
		// atomically without the check-then-rename race that could overwrite a
		// path created concurrently after an existence check.
		if err := os.Link(temporaryPath, path); err != nil {
			if errors.Is(err, os.ErrExist) {
				return fmt.Errorf("output file already exists: %s", path)
			}
			return fmt.Errorf("publish output file: %w", err)
		}
		return nil
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace output file: %w", err)
	}
	return nil
}
