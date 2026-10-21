package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/apiclient"
)

const presentOutputTestID = "01990d25-3d7a-7000-8000-000000000901"

func TestPresentOutputUploadsWorkspaceFileForBoundRun(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	reportPath := filepath.Join(root, "reports", "qa.txt")
	if err := os.MkdirAll(filepath.Dir(reportPath), 0o700); err != nil {
		t.Fatal(err)
	}
	contents := []byte("all checks passed\n")
	if err := os.WriteFile(reportPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	api := &presentAPIStub{}
	service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.PresentOutput(t.Context(), PresentOutputInput{
		Type: "text", Title: "QA result", Summary: "Validated the release.",
		Items: []PresentOutputItem{{
			Role: "primary", Label: "QA report",
			Target: PresentOutputTarget{
				Kind: "workspace_file", WorkspaceRelativePath: "reports/qa.txt",
			},
		}},
	})
	if err != nil {
		t.Fatalf("PresentOutput() error = %v", err)
	}
	if result.OutputID != presentOutputTestID || len(api.output.Items) != 1 ||
		api.output.Items[0].AttachmentID == "" || api.upload.Filename != "qa.txt" ||
		api.upload.CompanyID != testPresentRunContext().CompanyID ||
		api.upload.SizeBytes != int64(len(contents)) || api.upload.RequestKey == "" ||
		api.postPath != "/runs/00000000-0000-4000-8000-000000000071/output" || api.postRequestKey == "" {
		t.Fatalf("result = %+v upload = %+v output = %+v", result, api.upload, api.output)
	}
}

func TestPresentOutputUploadsImmutableSnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	firstPath := filepath.Join(root, "first.txt")
	secondPath := filepath.Join(root, "second.txt")
	if err := os.WriteFile(firstPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalSecond := []byte("before")
	if err := os.WriteFile(secondPath, originalSecond, 0o600); err != nil {
		t.Fatal(err)
	}
	api := &presentAPIStub{
		beforeUpload: func(call int) {
			if call == 0 {
				if err := os.WriteFile(secondPath, []byte("after!"), 0o600); err != nil {
					t.Errorf("mutate second workspace file: %v", err)
				}
			}
		},
	}
	service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.PresentOutput(t.Context(), PresentOutputInput{
		Type: "file", Title: "Snapshot result", Summary: "Two immutable files.",
		Items: []PresentOutputItem{
			{
				Role: "primary", Label: "First",
				Target: PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: "first.txt"},
			},
			{
				Role:  "final",
				Label: "Second",
				Target: PresentOutputTarget{
					Kind:                  "workspace_file",
					WorkspaceRelativePath: "second.txt",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("PresentOutput() error = %v", err)
	}
	if len(api.output.Items) != 2 || api.output.Items[1].Role != "final" {
		t.Fatalf("output items = %+v, want additional final result", api.output.Items)
	}
	if len(api.uploadedContents) != 2 || string(api.uploadedContents[1]) != string(originalSecond) {
		t.Fatalf(
			"second uploaded content = %q, want immutable snapshot %q",
			api.uploadedContents,
			originalSecond,
		)
	}
	digest := sha256.Sum256(originalSecond)
	wantRequestKey := deterministicAttachmentRequestKey(
		"00000000-0000-4000-8000-000000000071",
		"second.txt",
		digest[:],
	).String()
	if api.upload.RequestKey != wantRequestKey {
		t.Fatalf(
			"second upload request key = %q, want digest-derived %q",
			api.upload.RequestKey,
			wantRequestKey,
		)
	}
}

func TestPresentOutputContractUsesCamelCase(t *testing.T) {
	t.Parallel()

	input, err := json.Marshal(PresentOutputInput{
		Title: "Result", Summary: "Summary", SubmissionNotes: "Notes",
		Items: []PresentOutputItem{{
			Role: "primary", Label: "File",
			Target: PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: "result.txt"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	appInput, err := json.Marshal(PresentOutputInput{
		Type: "app", Title: "App", Summary: "Ready.",
		App: &PresentOutputApp{Commands: []PresentOutputCommand{{
			Command: "npm", Args: []string{"run", "dev"}, WorkingDirectory: "app", Port: 5173,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(PresentOutputResult{OutputID: presentOutputTestID})
	if err != nil {
		t.Fatal(err)
	}
	contract := string(input) + string(appInput) + string(result)
	for _, field := range []string{
		`"app"`, `"commands"`, `"command"`, `"args"`, `"workingDirectory"`, `"port"`,
		`"submissionNotes"`, `"workspaceRelativePath"`, `"outputId"`,
	} {
		if !strings.Contains(contract, field) {
			t.Fatalf("MCP contract = %s, want %s", contract, field)
		}
	}
	for _, field := range []string{"submission_notes", "workspace_relative_path", "output_id"} {
		if strings.Contains(contract, field) {
			t.Fatalf("MCP contract contains snake_case field %q: %s", field, contract)
		}
	}
}

func TestPresentOutputRejectsWorkspaceEscapeAndSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.txt")); err != nil {
		t.Fatal(err)
	}
	service, err := NewPresentOutputService(&presentAPIStub{}, testPresentRunContext(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../secret.txt", outside, "linked.txt"} {
		_, err := service.PresentOutput(t.Context(), PresentOutputInput{
			Title: "Result", Summary: "Summary",
			Items: []PresentOutputItem{{
				Role: "primary", Label: "File",
				Target: PresentOutputTarget{
					Kind: "workspace_file", WorkspaceRelativePath: path,
				},
			}},
		})
		if err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Fatalf("PresentOutput(%q) error = %v, want workspace rejection", path, err)
		}
	}
}

// The guard sees each file's real path before anything is read or uploaded,
// for a listed item and for a Studio canvas, and its refusal stops the whole
// presentation.
func TestPresentOutputAsksTheGuardBeforeReadingAFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"result.txt", "guarded.txt", "canvas.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	refused := errors.New("the Run's sandbox denies this file")
	var asked []string
	guard := func(path string) error {
		asked = append(asked, path)
		if filepath.Base(path) != "result.txt" {
			return refused
		}
		return nil
	}
	item := func(role, path string) PresentOutputItem {
		return PresentOutputItem{
			Role: role, Label: "File",
			Target: PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: path},
		}
	}
	for name, input := range map[string]PresentOutputInput{
		"item": {
			Title: "Result", Summary: "Summary",
			Items: []PresentOutputItem{item("primary", "result.txt"), item("supporting", "guarded.txt")},
		},
		"studio canvas": {
			Type: "media", Title: "Result", Summary: "Summary", StudioCanvasPath: "canvas.json",
			Items: []PresentOutputItem{item("primary", "result.txt")},
		},
	} {
		asked = nil
		api := &presentAPIStub{}
		service, err := NewPresentOutputService(api, testPresentRunContext(), root, guard)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.PresentOutput(t.Context(), input); !errors.Is(err, refused) {
			t.Fatalf("%s: PresentOutput() error = %v, want the guard's refusal", name, err)
		}
		if api.uploadCalls != 0 || api.postPath != "" {
			t.Fatalf("%s: a refused presentation reached the API", name)
		}
		if len(asked) == 0 || filepath.Dir(asked[len(asked)-1]) != realRoot {
			t.Fatalf("%s: guard asked about %v, want real paths under %s", name, asked, realRoot)
		}
	}
}

func TestPresentOutputValidatesEveryItemBeforeUploading(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{"first.txt", "second.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	api := &presentAPIStub{}
	service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.PresentOutput(t.Context(), PresentOutputInput{
		Title: "Invalid result", Summary: "Contains two primary items.",
		Items: []PresentOutputItem{
			{
				Role: "primary", Label: "First",
				Target: PresentOutputTarget{
					Kind: "workspace_file", WorkspaceRelativePath: "first.txt",
				},
			},
			{
				Role: "primary", Label: "Second",
				Target: PresentOutputTarget{
					Kind: "workspace_file", WorkspaceRelativePath: "second.txt",
				},
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one primary") {
		t.Fatalf("PresentOutput() error = %v, want primary-cardinality rejection", err)
	}
	if api.uploadCalls != 0 {
		t.Fatalf("UploadAttachment() calls = %d, want 0 before validation succeeds", api.uploadCalls)
	}
}

func TestPresentOutputRejectsArtifactTypeThatDoesNotMatchPrimaryFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.md"), []byte("# Report"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &presentAPIStub{}
	service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.PresentOutput(t.Context(), PresentOutputInput{
		Type: "media", Title: "Report", Summary: "A text report.",
		Items: []PresentOutputItem{{
			Role: "primary", Label: "Report",
			Target: PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: "report.md"},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "media artifact") {
		t.Fatalf("PresentOutput() error = %v, want media mismatch", err)
	}
	if api.uploadCalls != 0 || api.postPath != "" {
		t.Fatal("mismatched artifact reached the API")
	}
}

func TestPresentOutputPresentsAudioAsMedia(t *testing.T) {
	t.Parallel()
	for _, filename := range []string{
		"music.mp3", "MUSIC.MP3", "music.wav", "music.ogg", "music.m4a", "music.flac", "music.aac",
	} {
		t.Run(filename, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, filename), []byte("audio"), 0o600); err != nil {
				t.Fatal(err)
			}
			api := &presentAPIStub{}
			service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.PresentOutput(t.Context(), PresentOutputInput{
				Type: "media", Title: "Music", Summary: "Generated music.",
				Items: []PresentOutputItem{
					{
						Role:  "primary",
						Label: "Music",
						Target: PresentOutputTarget{
							Kind:                  "workspace_file",
							WorkspaceRelativePath: filename,
						},
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.OutputID != presentOutputTestID || api.output.Type != "media" ||
				api.uploadCalls != 1 || !strings.HasPrefix(api.upload.ContentType, "audio/") ||
				len(api.output.Items) != 1 || api.output.Items[0].Role != "primary" {
				t.Fatalf(
					"result = %+v, output = %+v, upload = %+v, want primary audio media",
					result, api.output, api.upload,
				)
			}
		})
	}
}

func TestPresentOutputAllowsGenericContentTypeForTextArtifact(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report"), []byte("Report\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &presentAPIStub{}
	service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.PresentOutput(t.Context(), PresentOutputInput{
		Type: "text", Title: "Report", Summary: "A readable text report.",
		Items: []PresentOutputItem{{
			Role: "primary", Label: "Report",
			Target: PresentOutputTarget{Kind: "workspace_file", WorkspaceRelativePath: "report"},
		}},
	})
	if err != nil {
		t.Fatalf("PresentOutput() error = %v", err)
	}
	if api.upload.ContentType != "application/octet-stream" || api.uploadCalls != 1 || api.postPath == "" {
		t.Fatalf(
			"upload = %+v, calls = %d, post path = %q",
			api.upload,
			api.uploadCalls,
			api.postPath,
		)
	}
}

func TestPresentOutputValidatesAllMetadataBeforeUploading(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "result.txt"), []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseInput := func() PresentOutputInput {
		return PresentOutputInput{
			Title: "Result", Summary: "Complete result.",
			Items: []PresentOutputItem{{
				Role: "primary", Label: "Result",
				Target: PresentOutputTarget{
					Kind: "workspace_file", WorkspaceRelativePath: "result.txt",
				},
			}},
		}
	}
	for _, test := range []struct {
		name string
		edit func(*PresentOutputInput)
	}{
		{name: "title over 160 runes", edit: func(input *PresentOutputInput) {
			input.Title = strings.Repeat("界", 161)
		}},
		{name: "summary over 4000 runes", edit: func(input *PresentOutputInput) {
			input.Summary = strings.Repeat("界", 4001)
		}},
		{name: "submission notes over 4000 runes", edit: func(input *PresentOutputInput) {
			input.SubmissionNotes = strings.Repeat("界", 4001)
		}},
		{name: "label over 120 runes", edit: func(input *PresentOutputInput) {
			input.Items[0].Label = strings.Repeat("界", 121)
		}},
		{name: "invalid UTF-8 title", edit: func(input *PresentOutputInput) {
			input.Title = string([]byte{0xff})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := baseInput()
			test.edit(&input)
			api := &presentAPIStub{}
			service, err := NewPresentOutputService(api, testPresentRunContext(), root, nil)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := service.PresentOutput(t.Context(), input); err == nil {
				t.Fatal("PresentOutput() error = nil, want metadata rejection")
			}
			if api.uploadCalls != 0 || api.postPath != "" {
				t.Fatalf(
					"preflight API calls = uploads %d, post %q; want none",
					api.uploadCalls,
					api.postPath,
				)
			}
		})
	}
}

func testPresentRunContext() RunContext {
	return RunContext{
		RunID: "00000000-0000-4000-8000-000000000071", CompanyID: "01990d25-3d7a-7000-8000-000000000902",
		ConversationID: "01990d25-3d7a-7000-8000-000000000903",
		AgentID:        "01990d25-3d7a-7000-8000-000000000904",
	}
}

type presentAPIStub struct {
	upload           apiclient.AttachmentUpload
	uploadedContents [][]byte
	output           presentOutputRequest
	uploadCalls      int
	postPath         string
	postRequestKey   string
	beforeUpload     func(int)
}

func (s *presentAPIStub) PostRunJSON(
	_ context.Context,
	path string,
	requestKey string,
	source, destination any,
) error {
	request, ok := source.(presentOutputRequest)
	if !ok || !strings.Contains(path, "/output") {
		return fmt.Errorf("unexpected POST %s", path)
	}
	s.output = request
	s.postPath = path
	s.postRequestKey = requestKey
	stubResult := destination.(*presentOutputResponse)
	stubResult.OutputID = presentOutputTestID
	return nil
}

func (s *presentAPIStub) UploadAttachment(
	_ context.Context,
	input apiclient.AttachmentUpload,
	source io.ReadSeeker,
) (string, error) {
	if s.beforeUpload != nil {
		s.beforeUpload(s.uploadCalls)
	}
	s.uploadCalls++
	content, err := io.ReadAll(source)
	if err != nil {
		return "", err
	}
	s.uploadedContents = append(s.uploadedContents, content)
	s.upload = input
	return uuid.Must(uuid.NewV7()).String(), nil
}

// The files Agents present are mostly text the platform's mime table does
// not know on every OS (Markdown above all): the viewer must not receive
// them as application/octet-stream, which downloads instead of rendering.
// The OS table wins when it has an answer (Linux knows .go, macOS does not),
// so the test pins the family, not the exact type.
func TestPresentedWorkspaceFilesCarryTheirTextContentType(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for name, want := range map[string]string{
		"notes.md":  "text/",
		"data.csv":  "text/",
		"plan.yaml": "yaml",
		"main.go":   "text/",
		"page.html": "text/html",
		"blob.bin":  "application/octet-stream",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		file, upload, err := openPresentedWorkspaceFile(
			root, nil, testPresentRunContext().RunID, testPresentRunContext().CompanyID, name,
		)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		_ = file.Close()
		if !strings.Contains(upload.ContentType, want) {
			t.Errorf("%s content type = %q, want it to contain %q", name, upload.ContentType, want)
		}
	}
}

type nativePageOutputAPI struct {
	pageAPIStub
	uploads int
}

func (s *nativePageOutputAPI) UploadAttachment(
	context.Context,
	apiclient.AttachmentUpload,
	io.ReadSeeker,
) (string, error) {
	s.uploads++
	return "", fmt.Errorf("native Page delivery must use its canonical export")
}

func TestPresentOutputDeliversNativeProposal(t *testing.T) {
	t.Parallel()
	api := new(nativePageOutputAPI)
	service, err := NewPresentOutputService(api, testPresentRunContext(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	input := PresentOutputInput{
		Summary: "Proposed finding, awaiting acceptance",
		Page: &PresentOutputPage{
			PageID:      uuid.NewString(),
			ChangeID:    uuid.NewString(),
			Sequence:    7,
			Fingerprint: strings.Repeat("a", 64),
		},
	}
	for range 2 {
		result, err := service.PresentOutput(t.Context(), input)
		if err != nil || result.OutputID != "artifact" {
			t.Fatalf("native delivery = %+v, %v", result, err)
		}
	}
	var capture map[string]any
	if err := json.Unmarshal(api.requests[0], &capture); err != nil {
		t.Fatal(err)
	}
	if capture["changeId"] != input.Page.ChangeID || api.uploads != 0 || api.keys[0] != api.keys[2] ||
		api.keys[1] != api.keys[3] {
		t.Fatalf("proposal capture = %+v, uploads = %d, keys = %v", capture, api.uploads, api.keys)
	}
	input.Items = []PresentOutputItem{{Role: "primary"}}
	if _, err := service.PresentOutput(t.Context(), input); err == nil || len(api.requests) != 4 {
		t.Fatalf("mixed Page/file delivery = %v, requests = %d", err, len(api.requests))
	}
}
