package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/contentbundle"
	db "boardly.dev/boardly/server/pkg/db/generated"
)

func TestSkillErrorsUseDocumentedEnvelope(t *testing.T) {
	t.Parallel()
	for _, test := range []mappedErrorCase{
		{"upgrade", agent.ErrSkillDaemonUpgrade, 409, "Update the Zenith Team daemon to load custom skills", "agent_skill_daemon_upgrade"},
		{"invalid", agent.ErrSkillInvalid, http.StatusUnprocessableEntity, "Skill package is invalid; check portable SKILL.md frontmatter, file paths, references and size limits", "agent_skill_invalid"},
		{"frontmatter", agent.ErrSkillFrontmatterInvalid, 422, "SKILL.md needs valid YAML frontmatter with supported fields; compatibility must be at most 500 characters", "agent_skill_frontmatter_invalid"},
		{"name", agent.ErrSkillNameRequired, 422, "Add a non-empty name field to SKILL.md frontmatter", "agent_skill_name_required"},
		{"description", agent.ErrSkillDescriptionInvalid, 422, "Add a non-empty plain-text description of at most 1024 characters to SKILL.md frontmatter", "agent_skill_description_invalid"},
		{"reference", agent.ErrSkillReferenceInvalid, 422, "Correct local links and keep their targets inside the skill folder", "agent_skill_reference_invalid"},
		{"missing reference", agent.ErrSkillReferenceMissing, 422, "Include every linked local file in the skill folder; upload a ZIP for a skill with supporting files", "agent_skill_reference_missing"},
		{"archive", agent.ErrSkillArchiveInvalid, 422, "The ZIP could not be read; export a fresh ZIP and try again", "agent_skill_archive_invalid"},
		{"files", agent.ErrSkillFilesInvalid, 422, "Use unique relative file paths and remove hidden files, provider configuration, shortcuts and special files", "agent_skill_files_invalid"},
		{"package limit", agent.ErrSkillPackageLimit, 422, "Reduce the skill package to fit the documented file-count and size limits", "agent_skill_package_limit"},
		{"instructions", agent.ErrSkillInstructionsInvalid, 422, "Save SKILL.md as non-empty UTF-8 text without NUL bytes", "agent_skill_instructions_invalid"},
		{"skill count", agent.ErrSkillCountInvalid, 422, "Include 1 to 32 skill folders, each with a file named SKILL.md", "agent_skill_count_invalid"},
		{"missing", agent.ErrSkillNotFound, http.StatusNotFound, "Agent skill or import not found", "agent_skill_not_found"},
		{"conflict", agent.ErrSkillConflict, http.StatusConflict, "Skill name or revision conflicts with the installed skill", "agent_skill_conflict"},
		{"unavailable", agent.ErrSkillUnavailable, http.StatusServiceUnavailable, "Skill storage or public GitHub source is unavailable", "agent_skill_unavailable"},
		{"limit", agent.ErrSkillLimit, http.StatusUnprocessableEntity, "An Agent supports 32 custom skills, 128 MB total, and 20 imports per day", "agent_skill_limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertMappedErrorResponse(
				t,
				func(c *gin.Context) { writeSkillError(c, fmt.Errorf("private-source/token-secret: %w", test.err)) },
				test.status,
				test.detail,
				test.code,
			)
		})
	}
}

func TestSkillErrorsKeepUnknownFailuresPrivate(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		err  error
		code string
		want int
	}{
		{"unclassified validation", fmt.Errorf("%w: private-source/token-secret", agent.ErrSkillInvalid), "agent_skill_invalid", 422},
		{"unknown failure", errors.New("private-source/token-secret"), "internal_error", 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			writeSkillError(c, test.err)
			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != test.want || body["code"] != test.code || len(body) != 3 {
				t.Fatalf("response = %d %v, want %d %q and canonical envelope", recorder.Code, body, test.want, test.code)
			}
			if strings.Contains(recorder.Body.String(), "private-source") || strings.Contains(recorder.Body.String(), "token-secret") {
				t.Fatal("response exposed private error context")
			}
		})
	}
}

type skillGitHubPreviewStub struct {
	agent.SkillService
	preview func(agent.SkillGitHubInput) (agent.SkillPreview, error)
}

func (s *skillGitHubPreviewStub) PreviewGitHub(
	_ context.Context,
	_ auth.UserPrincipal,
	_ uuid.UUID,
	input agent.SkillGitHubInput,
) (agent.SkillPreview, error) {
	return s.preview(input)
}

func TestGitHubSkillPreviewAcceptsOnlyURLInput(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body string
		want int
		url  string
	}{
		{
			name: "repository link",
			body: `{"url":"https://github.com/acme/skills"}`,
			want: http.StatusCreated,
			url:  "https://github.com/acme/skills",
		},
		{
			name: "folder link",
			body: `{"url":"https://github.com/acme/skills/tree/main/skills/review"}`,
			want: http.StatusCreated,
			url:  "https://github.com/acme/skills/tree/main/skills/review",
		},
		{
			name: "file link",
			body: `{"url":"https://github.com/acme/skills/blob/main/skills/review/SKILL.md"}`,
			want: http.StatusCreated,
			url:  "https://github.com/acme/skills/blob/main/skills/review/SKILL.md",
		},
		{
			name: "separate ref rejected",
			body: `{"url":"https://github.com/acme/skills","ref":"main"}`,
			want: http.StatusBadRequest,
		},
		{
			name: "separate path rejected",
			body: `{"url":"https://github.com/acme/skills","path":"skills/review"}`,
			want: http.StatusBadRequest,
		},
		{
			name: "empty ref rejected",
			body: `{"url":"https://github.com/acme/skills","ref":""}`,
			want: http.StatusBadRequest,
		},
		{
			name: "empty path rejected",
			body: `{"url":"https://github.com/acme/skills","path":""}`,
			want: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			called := false
			h := &Handler{SkillService: &skillGitHubPreviewStub{
				preview: func(input agent.SkillGitHubInput) (agent.SkillPreview, error) {
					called = true
					if test.want == http.StatusCreated && input.URL != test.url {
						t.Fatalf("URL = %q, want %q", input.URL, test.url)
					}
					return agent.SkillPreview{SourceURL: input.URL}, nil
				},
			}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Params = gin.Params{{Key: "agentId", Value: uuid.NewString()}}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			c.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), daemonTestPrincipal()))
			h.previewAgentSkillGitHub(c)
			if recorder.Code != test.want || called != (test.want == http.StatusCreated) {
				t.Fatalf("status = %d, called = %v, body = %s", recorder.Code, called, recorder.Body.String())
			}
			if test.want == http.StatusBadRequest {
				var envelope struct {
					Code string `json:"code"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil || envelope.Code != "invalid_request" {
					t.Fatalf("error envelope = %s, error = %v", recorder.Body.String(), err)
				}
			}
		})
	}
}

type skillInstallStub struct {
	agent.SkillService
	install func(agent.SkillInstallInput) ([]agent.SkillView, error)
}

func (s *skillInstallStub) Install(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, input agent.SkillInstallInput) ([]agent.SkillView, error) {
	return s.install(input)
}

func TestSkillInstallBindsExplicitReplacementChoices(t *testing.T) {
	t.Parallel()
	importID, versionID, skillID := uuid.New(), uuid.New(), uuid.New()
	for _, test := range []struct {
		name        string
		replacement map[string]any
		want        int
	}{
		{"reviewed update", map[string]any{"versionId": versionID.String(), "skillId": skillID.String(), "expectedRevision": 3}, http.StatusOK},
		{"invalid skill id", map[string]any{"versionId": versionID.String(), "skillId": "invalid", "expectedRevision": 3}, http.StatusUnprocessableEntity},
		{"missing revision", map[string]any{"versionId": versionID.String(), "skillId": skillID.String()}, http.StatusUnprocessableEntity},
		{"unknown nested field", map[string]any{"versionId": versionID.String(), "skillId": skillID.String(), "expectedRevision": 3, "overwrite": true}, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body, err := json.Marshal(map[string]any{"importId": importID.String(), "versionIds": []string{versionID.String()}, "replacements": []any{test.replacement}})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			h := &Handler{SkillService: &skillInstallStub{install: func(input agent.SkillInstallInput) ([]agent.SkillView, error) {
				called = true
				if input.ImportID != importID || len(input.Replacements) != 1 || input.Replacements[0].VersionID != versionID || input.Replacements[0].SkillID != skillID || input.Replacements[0].ExpectedRevision != 3 {
					t.Fatalf("install input = %+v", input)
				}
				return []agent.SkillView{}, nil
			}}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Params = gin.Params{{Key: "agentId", Value: uuid.NewString()}}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
			request.Header.Set("Content-Type", "application/json")
			c.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), daemonTestPrincipal()))
			h.installAgentSkills(c)
			if recorder.Code != test.want || called != (test.want == http.StatusOK) {
				t.Fatalf("status = %d, called = %v, body = %s", recorder.Code, called, recorder.Body.String())
			}
		})
	}
}

type skillResolverStub struct {
	agent.SkillService
	resolve func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (contentbundle.Skill, error)
}

type skillFileEditStub struct {
	agent.SkillService
	edit func(agent.SkillEditInput) (agent.SkillView, error)
}

func (s *skillFileEditStub) EditFiles(_ context.Context, _ auth.UserPrincipal, _, _ uuid.UUID, input agent.SkillEditInput) (agent.SkillView, error) {
	return s.edit(input)
}

func TestSkillUpdateBindsFileEditsWithoutMixingEnablement(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, body string
		want       int
	}{
		{"edit", `{"expectedRevision":2,"changes":[{"path":"scripts/check.py","content":"edited"}],"deletePaths":["assets/icon.bin"]}`, http.StatusOK},
		{"empty file", `{"expectedRevision":2,"changes":[{"path":"scripts/check.py","content":""}]}`, http.StatusOK},
		{"mixed modes", `{"expectedRevision":2,"enabled":false,"deletePaths":["assets/icon.bin"]}`, http.StatusUnprocessableEntity},
		{"empty request", `{"expectedRevision":2}`, http.StatusUnprocessableEntity},
		{"missing content", `{"expectedRevision":2,"changes":[{"path":"scripts/check.py"}]}`, http.StatusUnprocessableEntity},
		{"missing revision", `{"deletePaths":["assets/icon.bin"]}`, http.StatusUnprocessableEntity},
		{"unknown field", `{"expectedRevision":2,"deletePaths":["assets/icon.bin"],"force":true}`, http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			called := false
			h := &Handler{SkillService: &skillFileEditStub{edit: func(input agent.SkillEditInput) (agent.SkillView, error) {
				called = true
				if input.ExpectedRevision != 2 {
					t.Fatalf("revision = %d, want 2", input.ExpectedRevision)
				}
				return agent.SkillView{}, nil
			}}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Params = gin.Params{{Key: "agentId", Value: uuid.NewString()}, {Key: "skillId", Value: uuid.NewString()}}
			request := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			c.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), daemonTestPrincipal()))
			h.updateAgentSkill(c)
			if recorder.Code != test.want || called != (test.want == http.StatusOK) {
				t.Fatalf("status = %d, called = %v, body = %s", recorder.Code, called, recorder.Body.String())
			}
		})
	}
}

type skillReferenceStub struct {
	agent.SkillService
	read func(agent.SkillReadInput) (agent.SkillReferenceView, error)
}

func (s *skillReferenceStub) ReadReference(_ context.Context, _ auth.UserPrincipal, input agent.SkillReadInput) (agent.SkillReferenceView, error) {
	return s.read(input)
}

func TestSkillReferenceValidatesVersionBeforeReading(t *testing.T) {
	t.Parallel()
	versionID := uuid.New()
	for _, test := range []struct {
		name, query string
		want        int
		version     uuid.UUID
	}{
		{"current file", "?path=SKILL.md", http.StatusOK, uuid.Nil},
		{"versioned file", "?path=SKILL.md&versionId=" + versionID.String(), http.StatusOK, versionID},
		{"unknown query", "?path=SKILL.md&force=true", http.StatusUnprocessableEntity, uuid.Nil},
		{"invalid version", "?path=SKILL.md&versionId=invalid", http.StatusUnprocessableEntity, uuid.Nil},
		{"empty version", "?path=SKILL.md&versionId=", http.StatusUnprocessableEntity, uuid.Nil},
		{"repeated version", "?path=SKILL.md&versionId=" + versionID.String() + "&versionId=" + versionID.String(), http.StatusUnprocessableEntity, uuid.Nil},
		{"repeated path", "?path=SKILL.md&path=other.md", http.StatusUnprocessableEntity, uuid.Nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			called := false
			h := &Handler{SkillService: &skillReferenceStub{read: func(input agent.SkillReadInput) (agent.SkillReferenceView, error) {
				called = true
				if input.VersionID != test.version || input.Path != "SKILL.md" {
					t.Fatalf("read input = %+v", input)
				}
				return agent.SkillReferenceView{}, nil
			}}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Params = gin.Params{{Key: "agentId", Value: uuid.NewString()}, {Key: "skillId", Value: uuid.NewString()}}
			request := httptest.NewRequest(http.MethodGet, "/"+test.query, nil)
			c.Request = request.WithContext(auth.WithUserPrincipal(request.Context(), daemonTestPrincipal()))
			h.readAgentSkillReference(c)
			if recorder.Code != test.want || called != (test.want == http.StatusOK) {
				t.Fatalf("status = %d, called = %v, body = %s", recorder.Code, called, recorder.Body.String())
			}
		})
	}
}

func (s *skillResolverStub) ResolveForRun(
	ctx context.Context,
	companyID, runID, versionID uuid.UUID,
) (contentbundle.Skill, error) {
	return s.resolve(ctx, companyID, runID, versionID)
}

func TestDaemonSkillsRequireCurrentContractAndExactClaim(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name           string
		contract       int
		source         string
		status         string
		foreignRuntime bool
		foreignVersion bool
		want           int
		resolved       bool
	}{
		{name: "exact frozen version", contract: 1, source: "company", status: "dispatched", want: http.StatusOK, resolved: true},
		{name: "old daemon", source: "company", status: "dispatched", want: http.StatusConflict},
		{name: "untrusted source", contract: 1, source: "runtime", status: "dispatched", want: http.StatusNotFound},
		{name: "unfrozen version", contract: 1, source: "company", status: "dispatched", foreignVersion: true, want: http.StatusNotFound, resolved: true},
		{name: "running claim", contract: 1, source: "company", status: "running", want: http.StatusConflict},
		{name: "another runtime", contract: 1, source: "company", status: "dispatched", foreignRuntime: true, want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			companyID, runtimeID, runID, versionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			database := &daemonRuntimeAccessDBTX{
				member: true,
				runtime: db.Runtime{
					ID:          runtimeID,
					CompanyID:   companyID,
					DaemonID:    "test-daemon",
					OwnerUserID: uuid.NullUUID{UUID: daemonTestPrincipal().UserID, Valid: true},
				},
				run: db.Run{
					ID:        runID,
					CompanyID: companyID,
					RuntimeID: runtimeID,
					Status:    test.status,
				},
			}
			if test.foreignRuntime {
				database.run.RuntimeID = uuid.New()
			}
			bundle, err := contentbundle.BuildSkill(
				"review-work",
				"Review work.",
				map[string][]byte{"SKILL.md": []byte("instructions"), "assets/example.bin": {0, 255}},
			)
			if err != nil {
				t.Fatal(err)
			}
			bundle.ID, bundle.Source = versionID.String(), "company"
			called := false
			h := &Handler{Queries: db.New(database), SkillService: &skillResolverStub{
				resolve: func(_ context.Context, company, run, version uuid.UUID) (contentbundle.Skill, error) {
					called = true
					if company != companyID || run != runID || version != versionID {
						t.Fatal("resolver received a different claim")
					}
					if test.foreignVersion {
						return contentbundle.Skill{}, agent.ErrSkillNotFound
					}
					return bundle, nil
				},
			}}
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Params = gin.Params{
				{Key: "runtimeId", Value: runtimeID.String()},
				{Key: "runId", Value: runID.String()},
			}
			body, err := json.Marshal(
				daemonResolveSkillBundlesRequest{
					ContractVersion: test.contract,
					Skills: []daemonSkillRef{
						{ID: versionID.String(), Source: test.source},
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
			request.Header.Set("X-Boardly-Daemon-ID", "test-daemon")
			request.Header.Set("Content-Type", "application/json")
			c.Request = request.WithContext(
				auth.WithUserPrincipal(request.Context(), daemonTestPrincipal()),
			)
			h.daemonResolveSkillBundles(c)
			if recorder.Code != test.want || called != test.resolved {
				t.Fatalf("status=%d resolved=%v body=%s", recorder.Code, called, recorder.Body.String())
			}
			if recorder.Code == http.StatusOK {
				var result struct {
					Items []contentbundle.Skill `json:"items"`
				}
				if err := json.Unmarshal(
					recorder.Body.Bytes(),
					&result,
				); err != nil ||
					len(result.Items) != 1 {
					t.Fatalf("response=%s, %v", recorder.Body.String(), err)
				}
				if err := contentbundle.ValidateSkill(
					contentbundle.SkillReference(bundle),
					result.Items[0],
				); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
