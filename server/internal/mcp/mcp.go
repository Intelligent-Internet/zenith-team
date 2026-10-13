// Package mcp composes boardly-mcp, the MCP stdio server the desktop app
// ships beside the daemon: it opens the Run channel named by run.json in the
// Run's isolated BOARDLY_HOME, wires that channel into the Run-scoped tools,
// and serves them over stdio. It holds no credential: every server call is a
// channel request the daemon forwards with its own session and the Run proof
// (Access TECH B3b). cmd/mcp/main.go is a thin shell over Run. Beyond its own
// tree it imports only the Run channel wire internal/runchannel, the shared
// credential store internal/authfile (linked through apiclient for the
// daemon's output presenter, never called here), the pinned platform content
// internal/platformbundle (which imports internal/mcp/tools and the
// asset-free internal/contentbundle verifier), and the vocabularies
// internal/task/milestone and pkg/studiocanvas (mcp-sealed).
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"boardly.dev/boardly/server/internal/mcp/apiclient"
	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/platformbundle"
	"boardly.dev/boardly/server/internal/runchannel"
)

// Run serves the Boardly MCP tools over stdio until ctx is canceled or the
// transport closes. It runs only inside a Run: without run.json in its
// BOARDLY_HOME it has no way to reach the server and refuses to start.
func Run(ctx context.Context, getenv func(string) string) error {
	if getenv == nil {
		return errors.New("Zenith Team run environment reader is unavailable")
	}
	control, err := runchannel.LoadRunControlConfig(strings.TrimSpace(getenv("BOARDLY_HOME")))
	if err != nil {
		return fmt.Errorf("open the Zenith Team Run channel: %w", err)
	}
	api := apiclient.NewRunChannel(control)
	companyID := strings.TrimSpace(getenv("BOARDLY_COMPANY_ID"))
	planRoot, err := resolveCompanyPlanRoot(getenv)
	if err != nil {
		return fmt.Errorf("resolve Zenith Team MCP workspace: %w", err)
	}
	service, err := tools.NewCompanyPlanServiceForWorkspace(api, planRoot)
	if err != nil {
		return fmt.Errorf("initialize Company Plan tools: %w", err)
	}
	questionService := tools.NewQuestionService(api, companyID)
	allowedTools := allowedToolsFromEnvironment(getenv)
	socialEnabled := toolExplicitlyAllowed(allowedTools, "social_read") ||
		toolExplicitlyAllowed(allowedTools, "social_publish")
	calendarEnabled := toolExplicitlyAllowed(allowedTools, "calendar_read") ||
		toolExplicitlyAllowed(allowedTools, "calendar_write")
	wikiEnabled := toolExplicitlyAllowed(allowedTools, "wiki_search") ||
		toolExplicitlyAllowed(allowedTools, "wiki_read") ||
		toolExplicitlyAllowed(allowedTools, "wiki_write")
	pagesEnabled := toolExplicitlyAllowed(allowedTools, "project_read") ||
		toolExplicitlyAllowed(allowedTools, "source_read") ||
		toolExplicitlyAllowed(allowedTools, "page_preview") ||
		toolExplicitlyAllowed(allowedTools, "page_create") ||
		toolExplicitlyAllowed(allowedTools, "page_read") ||
		toolExplicitlyAllowed(allowedTools, "page_edit")
	milestonesEnabled := toolExplicitlyAllowed(allowedTools, tools.AttestMilestonesToolName)
	artifactsEnabled := toolExplicitlyAllowed(allowedTools, "artifact_inspect") ||
		toolExplicitlyAllowed(allowedTools, "artifact_fetch") ||
		toolExplicitlyAllowed(allowedTools, "artifact_edit")
	// A Whiteboard is read and changed through the Artifact tools.
	whiteboardsEnabled := toolExplicitlyAllowed(allowedTools, "artifact_inspect") ||
		toolExplicitlyAllowed(allowedTools, "artifact_edit")
	outputEnabled := toolExplicitlyAllowed(allowedTools, "present_output")
	var runContext tools.RunContext
	commerceEnabled := toolExplicitlyAllowed(allowedTools, "commerce_account_status") ||
		toolExplicitlyAllowed(allowedTools, "commerce_manifest_submit") ||
		toolExplicitlyAllowed(allowedTools, "commerce_manifest_status")
	if toolExplicitlyAllowed(allowedTools, "create_mail_drafts") ||
		toolExplicitlyAllowed(
			allowedTools,
			"mark_task_failed",
		) || toolExplicitlyAllowed(allowedTools, "finalize_task_results") || commerceEnabled || calendarEnabled || socialEnabled || wikiEnabled || pagesEnabled ||
		milestonesEnabled || outputEnabled || artifactsEnabled || whiteboardsEnabled {
		runContext, err = resolveRunContext(getenv)
		if err != nil {
			return err
		}
	}
	// present_output, artifact_inspect and artifact_fetch are the calls the
	// daemon completes itself instead of forwarding (it packages workspace
	// files, supervises app commands, and reads saved Artifacts with the Run
	// proof into the workspace). They ride the same Run channel, named by
	// run.json, never an endpoint or capability handed over in the
	// environment (Access TECH B3b).
	var artifactService tools.ArtifactChannelService
	if artifactsEnabled {
		artifactService, err = tools.NewArtifactChannelService(
			control.Endpoint,
			control.Capability,
			control.RunID,
		)
		if err != nil {
			return fmt.Errorf("initialize artifact tools: %w", err)
		}
	}
	var outputService tools.OutputChannelService
	if outputEnabled {
		outputService, err = tools.NewOutputChannelService(control.Endpoint, control.Capability, control.RunID)
		if err != nil {
			return fmt.Errorf("initialize output tool: %w", err)
		}
	}
	// A social call waits on the provider; the daemon gives the social rows a
	// longer forward timeout, so the tools share the one channel.
	var socialService tools.SocialToolService
	if socialEnabled {
		socialService, err = tools.NewSocialToolService(api, runContext)
		if err != nil {
			return fmt.Errorf("initialize Social tools: %w", err)
		}
	}
	var calendarService tools.CalendarToolService
	if calendarEnabled {
		calendarService, err = tools.NewCalendarToolService(api, runContext)
		if err != nil {
			return fmt.Errorf("initialize Calendar tools: %w", err)
		}
	}
	var whiteboards tools.WhiteboardToolService
	if whiteboardsEnabled {
		whiteboards, err = tools.NewWhiteboardToolService(api, runContext)
		if err != nil {
			return err
		}
	}
	var wikiService tools.WikiToolService
	var pageService tools.PageToolService
	if pagesEnabled {
		pageService, err = tools.NewPageToolService(api, runContext)
		if err != nil {
			return err
		}
	}
	if wikiEnabled {
		wikiService, err = tools.NewWikiToolService(api, runContext)
		if err != nil {
			return fmt.Errorf("initialize Wiki tools: %w", err)
		}
	}
	var mailService *tools.DefaultMailService
	if toolExplicitlyAllowed(allowedTools, "create_mail_drafts") {
		mailService, err = tools.NewMailService(api, runContext)
		if err != nil {
			return fmt.Errorf("initialize Agent Mail tools: %w", err)
		}
	}
	var taskResultService tools.TaskResultToolService
	if toolExplicitlyAllowed(allowedTools, "finalize_task_results") {
		taskResultService, err = tools.NewTaskResultToolService(api, runContext)
		if err != nil {
			return fmt.Errorf("initialize Task result tool: %w", err)
		}
	}
	var taskFailureService *tools.DefaultTaskFailureService
	if toolExplicitlyAllowed(allowedTools, "mark_task_failed") {
		taskFailureService, err = tools.NewTaskFailureService(api, runContext)
		if err != nil {
			return fmt.Errorf("initialize Task failure tool: %w", err)
		}
	}
	var commerceService *tools.DefaultCommerceToolService
	if commerceEnabled {
		commerceService, err = tools.NewCommerceToolService(api, runContext)
		if err != nil {
			return fmt.Errorf("initialize Commerce tools: %w", err)
		}
	}
	var milestoneService *tools.DefaultMilestoneToolService
	if milestonesEnabled {
		milestoneService, err = tools.NewMilestoneToolService(api, runContext)
		if err != nil {
			return fmt.Errorf("initialize milestone tool: %w", err)
		}
	}
	var skillService *tools.DefaultSkillService
	snapshotDir, hasSnapshot := resolveSkillSnapshotDir(getenv)
	if commerceEnabled || taskPlatformSkillsAvailable(allowedTools, getenv) {
		loaded, openErr := openTaskSkills(snapshotDir, os.Stderr)
		if openErr != nil {
			return fmt.Errorf("configure Task platform Skills: %w", openErr)
		}
		skillService = loaded
	} else if hasSnapshot {
		loaded, openErr := tools.NewSkillService(snapshotDir)
		if openErr != nil {
			return fmt.Errorf("configure Run skills: %w", openErr)
		}
		skillService = loaded
	}
	runErr := newServer(
		service,
		questionService,
		skillService,
		allowedTools,
		optionalTools{
			output:      outputService,
			artifacts:   artifactService,
			mail:        mailService,
			calendar:    calendarService,
			wiki:        wikiService,
			whiteboards: whiteboards,
			pages:       pageService,
			taskResults: taskResultService,
			social:      socialService,
			taskFailure: taskFailureService,
			commerce:    commerceService,
			milestones:  milestoneService,
		},
	).Run(ctx, &mcpsdk.StdioTransport{})
	if skillService == nil {
		return runErr
	}
	return errors.Join(runErr, skillService.Close())
}

// loadPlatformBundles is the pinned platform content Task Runs load; a
// variable so tests can stand in a pin that fails verification.
var loadPlatformBundles = platformbundle.PlatformSkills

// openTaskSkills opens the Task skill service over the compiled and pinned
// platform skills and the Run's frozen snapshot. A pinned bundle that fails
// verification or loading is dropped and reported on diagnostics (the MCP
// server's stderr) rather than taking down every skill tool: zenith-platform
// and the Commerce procedure stay served, and the dropped key stays reserved.
func openTaskSkills(snapshotDir string, diagnostics io.Writer) (*tools.DefaultSkillService, error) {
	bundles, locks, err := loadPlatformBundles()
	if err != nil {
		fmt.Fprintf(diagnostics, "boardly-mcp: dropped pinned platform content: %s\n", oneLine(err))
	}
	for _, lock := range locks {
		fmt.Fprintf(diagnostics, "boardly-mcp: platform bundle %s %s (%s) sha256 %s\n",
			lock.Plugin, lock.Version, lock.Component, lock.Artifact.SHA256)
	}
	return tools.OpenTaskSkillService(snapshotDir, bundles, func(key string, err error) {
		fmt.Fprintf(diagnostics, "boardly-mcp: dropped platform skill %s: %s\n", key, oneLine(err))
	})
}

// oneLine keeps a joined error on one diagnostics line, so every line
// boardly-mcp writes carries its "boardly-mcp:" prefix.
func oneLine(err error) string {
	return strings.ReplaceAll(err.Error(), "\n", "; ")
}

func taskPlatformSkillsAvailable(
	allowedTools map[string]struct{},
	getenv func(string) string,
) bool {
	if getenv == nil || !toolExplicitlyAllowed(allowedTools, "search_skills") ||
		!toolExplicitlyAllowed(allowedTools, "load_skill_reference") {
		return false
	}
	runID := strings.TrimSpace(getenv("BOARDLY_MCP_RUN_ID"))
	parsed, err := uuid.Parse(runID)
	return err == nil && parsed != uuid.Nil && parsed.String() == runID
}

func resolveRunContext(getenv func(string) string) (tools.RunContext, error) {
	if getenv == nil {
		return tools.RunContext{}, errors.New("Zenith Team run environment reader is unavailable")
	}
	runContext := tools.RunContext{
		CompanyID:      strings.TrimSpace(getenv("BOARDLY_MCP_COMPANY_ID")),
		ConversationID: strings.TrimSpace(getenv("BOARDLY_MCP_CONVERSATION_ID")),
		TaskID:         strings.TrimSpace(getenv("BOARDLY_MCP_TASK_ID")),
		RunID:          strings.TrimSpace(getenv("BOARDLY_MCP_RUN_ID")),
		AgentID:        strings.TrimSpace(getenv("BOARDLY_MCP_AGENT_ID")),
	}
	for name, value := range map[string]string{
		"company":      runContext.CompanyID,
		"conversation": runContext.ConversationID,
		"agent":        runContext.AgentID,
	} {
		parsed, parseErr := uuid.Parse(value)
		if parseErr != nil || parsed == uuid.Nil || parsed.String() != value {
			return tools.RunContext{}, fmt.Errorf("Zenith Team run %s ID is invalid", name)
		}
	}
	runID, parseErr := uuid.Parse(runContext.RunID)
	if parseErr != nil || runID == uuid.Nil || runID.String() != runContext.RunID {
		return tools.RunContext{}, errors.New("Zenith Team run ID is invalid")
	}
	return runContext, nil
}

// resolveCompanyPlanRoot is the directory the Company Plan draft and export
// live in: the per-Run scratch the daemon hands over as BOARDLY_MCP_PLAN_ROOT
// (Runs TECH §11.2 — a Run's temporary artifacts stay out of cwd, so the
// repository the Agent works in never reads dirty because of them). The
// process cwd is only the fallback for a hand-started MCP process. Whether
// the directory is real is NewCompanyPlanServiceForWorkspace's judgment,
// made once for both sources.
func resolveCompanyPlanRoot(getenv func(string) string) (string, error) {
	if getenv != nil {
		if value := strings.TrimSpace(getenv("BOARDLY_MCP_PLAN_ROOT")); value != "" {
			return value, nil
		}
	}
	return os.Getwd()
}

func resolveSkillSnapshotDir(getenv func(string) string) (string, bool) {
	if getenv == nil {
		return "", false
	}
	value := getenv("BOARDLY_SKILL_SNAPSHOT_DIR")
	if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return "", false
	}
	info, err := os.Lstat(value)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	return value, true
}

func allowedToolsFromEnvironment(getenv func(string) string) map[string]struct{} {
	if getenv == nil {
		return nil
	}
	raw := strings.TrimSpace(getenv("BOARDLY_MCP_ALLOWED_TOOLS"))
	if raw == "" {
		return nil
	}
	allowed := map[string]struct{}{}
	for _, value := range strings.Split(raw, ",") {
		name := strings.TrimSpace(value)
		if name != "" {
			allowed[name] = struct{}{}
		}
	}
	return allowed
}
