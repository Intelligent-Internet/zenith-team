package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/daemon/execenv"
	"boardly.dev/boardly/server/internal/mcp/apiclient"
	"boardly.dev/boardly/server/internal/mcp/tools"
	"boardly.dev/boardly/server/internal/runchannel"
)

// The Run channel is how the zen CLI and boardly-mcp work from inside an
// agent process.
//
// A Run's processes hold no Zenith Team credential: its BOARDLY_HOME is an
// empty directory the daemon creates for that Run alone. What it does hold is
// run.json, written there by the daemon, which names the daemon's loopback
// endpoint and a Run-bound local capability. The CLI in Agent mode and
// boardly-mcp read that file and send every product request to the daemon
// instead of the server (internal/runchannel is the wire). The daemon checks
// the capability, checks the request against the Run's frozen policy, and
// only then forwards it to the server with its own session plus the
// X-Boardly-Run-Token proof. The agent therefore never sees a bearer token,
// cannot widen its scope by editing flags or environment, and loses the
// channel the moment its Run ends. See
// docs/adr/0009-run-scoped-cli-channel.md and Access TECH B3b.

const (
	// runChannelMaxRequestBytes takes the largest body the server takes on a
	// channel row (a Company Plan apply, 16 MiB) plus the request document
	// around it.
	runChannelMaxRequestBytes = 16<<20 + 64<<10
	// runChannelForwardTimeout bounds a forward whose row names no timeout
	// of its own.
	runChannelForwardTimeout = 60 * time.Second
	// runChannelSocialTimeout covers a social read or publish, which waits
	// on the provider; boardly-mcp gave its social client the same bound
	// before it moved onto the channel. It is the longest row, so it is
	// runchannel.MaxForwardTimeout.
	runChannelSocialTimeout = 130 * time.Second
)

// runCLIOperation is one (method, path pattern) the policy admits. A pattern
// segment of "*" matches exactly one path segment; every other segment must
// match exactly. Query strings never take part in matching — the server
// validates them.
type runCLIOperation struct {
	Method  string
	Pattern string
	// Timeout bounds the forward of this operation; zero means
	// runChannelForwardTimeout. It never exceeds
	// runchannel.MaxForwardTimeout, which the client waits for.
	Timeout time.Duration
	// Local marks an operation the daemon completes itself instead of
	// forwarding it: the present_output MCP tool, which packages the Run's
	// local files, and artifact_inspect and artifact_fetch, which read a saved
	// Artifact with the Run proof and fetch its file into the Run's
	// workspace. Native Page captures are packaged by the daemon too;
	// Agents cannot forward a prebuilt presentation directly.
	Local bool
}

// errRunChannelResponseTooLarge is a server answer over
// runchannel.MaxResponseBytes, which the channel refuses whole.
var errRunChannelResponseTooLarge = errors.New("server response exceeds the Run channel limit")

func (o runCLIOperation) forwardTimeout() time.Duration {
	if o.Timeout == 0 {
		return runChannelForwardTimeout
	}
	return o.Timeout
}

// runCLIPolicy is the closed set of API operations one Run may perform over
// the channel. It is derived from the frozen execution profile, so the prompt
// and enforced channel policy cannot drift.
type runCLIPolicy struct {
	operations []runCLIOperation
}

// admit returns the operation that admits the call: a local one when local
// is set, a forwarded one otherwise.
func (p runCLIPolicy) admit(method, path string, local bool) (runCLIOperation, bool) {
	for _, operation := range p.operations {
		if operation.Method == method && operation.Local == local &&
			runCLIPatternMatches(operation.Pattern, path) {
			return operation, true
		}
	}
	return runCLIOperation{}, false
}

func runCLIPatternMatches(pattern, path string) bool {
	want := strings.Split(strings.Trim(pattern, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for index := range want {
		if want[index] == "*" {
			if got[index] == "" {
				return false
			}
			continue
		}
		if want[index] != got[index] {
			return false
		}
	}
	return true
}

// runCLICommand pairs one zen command, or one boardly-mcp tool, with the
// operation it needs. The catalog is the single place that knows which
// command or tool maps to which server route, so the prompt, run.json and
// the policy cannot drift. A tool row has no Use: the prompt and run.json
// advertise zen commands only.
type runCLICommand struct {
	Use       string
	Tool      string
	Operation runCLIOperation
}

// runCLICommands builds the command catalog for one Run. Identity segments
// are substituted here so a Run's policy admits its own Task, Project,
// Conversation and Run and no other; the wildcard rows only appear for reads
// the Run's frozen product authority grants Company-wide, or that the server
// scopes to the Run itself. The rows of the boardly-mcp tools the Run is
// offered follow (runMCPCommands).
func runCLICommands(run Run, doc RunContext) []runCLICommand {
	if runToolPolicy(doc) != "" {
		return runMCPCommands(run, doc)
	}
	return append(runZenCommands(run, doc), runMCPCommands(run, doc)...)
}

// runZenCommands is the zen half of the catalog.
func runZenCommands(run Run, doc RunContext) []runCLICommand {
	company := "/companies/" + run.CompanyID
	conversation := company + "/conversations/" + run.ConversationID
	zen := func(use, method, pattern string) runCLICommand {
		return runCLICommand{Use: use, Operation: runCLIOperation{Method: method, Pattern: pattern}}
	}
	commands := []runCLICommand{
		zen("zen company get", http.MethodGet, company),
		zen("zen agent list", http.MethodGet, company+"/agents"),
		zen("zen agent get <id>", http.MethodGet, company+"/agents/*"),
		zen("zen run get", http.MethodGet, company+"/runs/"+run.ID.String()),
	}
	if doc.ChannelInvocation == nil {
		commands = append(commands,
			zen("zen conversation get", http.MethodGet, conversation),
			// Messages are served on the conversation itself, not under the
			// company (handler/conversation.go): the server owns that route, so
			// the policy names it as served rather than a path that 404s.
			zen(
				runConversationMessagesUse,
				http.MethodGet,
				"/conversations/"+run.ConversationID+"/messages",
			),
			zen("zen conversation transcript", http.MethodGet, conversation+"/transcript"),
		)
	}

	tools := make(map[string]struct{})
	for _, tool := range boardlyMCPToolsForRun(doc) {
		tools[tool] = struct{}{}
	}
	companyReads := runCompanyReadsAvailable(doc)
	if companyReads {
		commands = append(
			commands,
			zen("zen project list", http.MethodGet, company+"/projects"),
			zen("zen project get <id>", http.MethodGet, company+"/projects/*"),
			zen("zen task list", http.MethodGet, company+"/tasks"),
		)
	}
	// A Task read names any Task, and every Run with a channel may send one,
	// planning or not: the server reads a Run only its own Tasks — the ones
	// its Agent does for the member it works for — and refuses any other
	// with task_outside_run_scope (ADR 0014 rule 14). So every refusal is
	// the server's sentence, never a bare run_scope_denied. A Task Run reads
	// its own Task when it names none.
	taskGet, taskDependencies := "zen task get <id>", "zen task dependencies <id>"
	if doc.ExecutionProfile.Task != nil && run.TaskID != "" {
		taskGet, taskDependencies = "zen task get", "zen task dependencies"
	}
	commands = append(
		commands,
		zen(taskGet, http.MethodGet, company+"/tasks/*"),
		zen(taskDependencies, http.MethodGet, company+"/tasks/*/dependencies"),
	)
	// `zen task create` files work for the member this Run works for: the
	// daemon's session is theirs, so the server makes them the assignee and
	// starts the Task when they have its Agent set up (ADR 0014 D6). It needs
	// a Project. A Task Run gets the lookup of its own; any other Run names
	// one through the Company reads above and without them is not offered it.
	if runTaskCreationAvailable(doc) {
		create := zen(runTaskCreateUse, http.MethodPost, company+"/tasks")
		if project := runTaskProject(doc); project != nil {
			commands = append(commands,
				zen(runTaskCreateUse, http.MethodGet, company+"/projects/"+project.ID),
				create,
			)
		} else if companyReads {
			commands = append(commands, create)
		}
	}
	if _, ok := tools["ask_human"]; ok {
		commands = append(commands, zen("zen question ask", http.MethodPost, company+"/questions"))
	}
	if _, ok := tools["mark_task_failed"]; ok {
		commands = append(commands, zen("zen task fail", http.MethodPost, company+"/task-failures"))
	}
	if _, ok := tools["create_mail_drafts"]; ok {
		commands = append(commands, zen("zen mail draft", http.MethodPost, conversation+"/mail-draft-groups"))
	}
	if _, ok := tools["commerce_account_status"]; ok {
		commands = append(commands,
			zen("zen commerce account-status", http.MethodGet, company+"/commerce/account-status"),
		)
	}
	if _, ok := tools["commerce_manifest_status"]; ok {
		commands = append(commands,
			zen("zen commerce manifest get <id>", http.MethodGet, company+"/commerce/manifests/*"),
		)
	}
	return commands
}

// runMCPCommands are the routes the boardly-mcp tools offered to this Run
// call (boardlyMCPToolsForRun). boardly-mcp holds no credential: every tool
// call is a channel request the daemon forwards with its own session and the
// Run proof (Access TECH B3b), so a tool has exactly these rows and a tool
// the Run is not offered has none. The search_skills and
// load_skill_reference tools read the Run's local snapshot and call nothing.
// TestEveryMCPToolCallIsAdmittedByTheRunChannel calls every tool through a
// real channel, so a tool that changes its route fails there.
func runMCPCommands(run Run, doc RunContext) []runCLICommand {
	company := "/companies/" + run.CompanyID
	conversation := company + "/conversations/" + run.ConversationID
	ownRun := company + "/runs/" + run.ID.String()
	row := func(method, pattern string) runCLIOperation {
		return runCLIOperation{Method: method, Pattern: pattern}
	}
	social := func(method, pattern string) runCLIOperation {
		return runCLIOperation{Method: method, Pattern: pattern, Timeout: runChannelSocialTimeout}
	}
	// The Company Plan export and preview read the Company the member
	// belongs to, its Projects, its Tasks and each Task's prerequisites. With
	// the proof the server shows the root Agent's direct-chat Run every Task
	// its person reads and any other Run its own (Access TECH B6).
	planReads := []runCLIOperation{
		row(http.MethodGet, "/companies"),
		row(http.MethodGet, company+"/projects"),
		row(http.MethodGet, company+"/tasks"),
		row(http.MethodGet, company+"/tasks/*/dependencies"),
	}
	// A Board chat's Run reaches only its own Board.
	boards := ownRun + "/whiteboards/*"
	if board := doc.ExecutionProfile.Whiteboard; board != nil {
		boards = ownRun + "/whiteboards/" + board.ID
	}
	routes := map[string][]runCLIOperation{
		"ask_human":                        {row(http.MethodPost, company+"/questions")},
		"attest_milestones":                {row(http.MethodPost, company+"/milestone-attestations")},
		"calendar_read":                    {row(http.MethodGet, conversation+"/calendar")},
		"calendar_write":                   {row(http.MethodPost, conversation+"/calendar")},
		"commerce_account_status":          {row(http.MethodGet, company+"/commerce/account-status")},
		"commerce_manifest_status":         {row(http.MethodGet, company+"/commerce/manifests/*")},
		"commerce_manifest_submit":         {row(http.MethodPost, company+"/commerce/manifests")},
		"company_plan_export":              planReads,
		"company_plan_transaction_preview": planReads,
		"company_plan_transaction_apply":   {row(http.MethodPost, company+"/company-plan/apply")},
		"create_mail_drafts":               {row(http.MethodPost, conversation+"/mail-draft-groups")},
		"list_agents": {
			row(http.MethodGet, "/companies"),
			row(http.MethodGet, company+"/agents"),
		},
		"mark_task_failed":      {row(http.MethodPost, company+"/task-failures")},
		"finalize_task_results": {row(http.MethodPost, company+"/tasks/"+run.TaskID+"/results")},
		"project_read":          {row(http.MethodGet, ownRun+"/agent-project")},
		"source_read":           {row(http.MethodGet, ownRun+"/agent-sources")},
		"page_create":           {row(http.MethodPost, ownRun+"/agent-pages")},
		"page_read":             {row(http.MethodGet, ownRun+"/agent-pages/*")},
		"page_edit":             {row(http.MethodPost, ownRun+"/agent-pages/*")},
		"page_preview":          {row(http.MethodGet, ownRun+"/agent-pages/*/presentation-preview")},
		// present_output posts its input to the same route; the daemon
		// completes it locally (Local), so it never reaches the server as is.
		"present_output": {{Method: http.MethodPost, Pattern: runOutputPath(run), Local: true}},
		// artifact_inspect and artifact_fetch are local too: the daemon reads
		// the Artifact on the server's Run-proven agent-artifacts routes with
		// its own session and the proof, and writes a fetched file into the
		// Run's workspace (readRunArtifact). No server route has these paths.
		"artifact_inspect": {{Method: http.MethodPost, Pattern: runArtifactInspectPath(run), Local: true}},
		"artifact_fetch":   {{Method: http.MethodPost, Pattern: runArtifactFetchPath(run), Local: true}},
		"social_read":      {social(http.MethodGet, conversation+"/social")},
		"social_publish":   {social(http.MethodPost, conversation+"/social/posts")},
		"whiteboard_list":  {row(http.MethodGet, ownRun+"/whiteboards")},
		"whiteboard_read": {
			row(http.MethodPost, boards+"/view"),
			row(http.MethodPost, boards+"/context"),
		},
		"whiteboard_draw": {
			row(http.MethodPost, boards+"/compose"),
			row(http.MethodPost, boards+"/view"),
		},
		"whiteboard_edit": {
			row(http.MethodPost, boards+"/operations"),
			row(http.MethodPost, boards+"/view"),
		},
		"wiki_search": {row(http.MethodGet, ownRun+"/agent-wiki/search")},
		"wiki_read":   {row(http.MethodGet, ownRun+"/agent-wiki/pages/*")},
	}
	var commands []runCLICommand
	for _, tool := range boardlyMCPToolsForRun(doc) {
		for _, operation := range routes[tool] {
			commands = append(commands, runCLICommand{Tool: tool, Operation: operation})
		}
	}
	return commands
}

// runTaskProject is a Task Run's frozen Project, or nil for any other Run. An
// id that is not a UUID is treated as absent: an empty one or a "*"
// substituted into the lookup pattern would admit every Project.
func runTaskProject(doc RunContext) *RunProjectContext {
	project := doc.ExecutionProfile.Project
	if doc.ExecutionProfile.Task == nil || project == nil || !runchannel.ValidID(project.ID) {
		return nil
	}
	return project
}

func runCLIPolicyFor(commands []runCLICommand) runCLIPolicy {
	policy := runCLIPolicy{operations: make([]runCLIOperation, 0, len(commands))}
	for _, command := range commands {
		policy.operations = append(policy.operations, command.Operation)
	}
	return policy
}

// runCLICommandUses lists the zen commands the catalog admits, once each, for
// run.json and the prompt. A boardly-mcp tool row advertises nothing.
func runCLICommandUses(commands []runCLICommand) []string {
	uses := make([]string, 0, len(commands))
	seen := make(map[string]struct{}, len(commands))
	for _, command := range commands {
		if _, duplicate := seen[command.Use]; duplicate || command.Use == "" {
			continue
		}
		seen[command.Use] = struct{}{}
		uses = append(uses, command.Use)
	}
	return uses
}

type runChannelKey [sha256.Size]byte

func runChannelKeyFor(capability string) runChannelKey {
	return runChannelKey(sha256.Sum256([]byte(capability)))
}

// runChannel is one live Run's channel: its identity, its policy, the proof
// the daemon forwards, and the context that cancels in-flight forwards when
// the Run ends.
type runChannel struct {
	run      Run
	policy   runCLIPolicy
	runToken string
	// artifacts completes artifact_inspect and artifact_fetch, local
	// operations too: a fetched file is written into the Run's workspace.
	// Nil when the Run has no workspace or its policy disables saved work.
	artifacts *runArtifactReader
	// present completes present_output: the channel operation the
	// daemon performs itself instead of forwarding, because file items must
	// become attachments and app commands must enter the local supervisor.
	// Nil when the Run has no workspace to present from.
	present  RunOutputPresenter
	ctx      context.Context
	cancel   context.CancelFunc
	inflight sync.WaitGroup
}

// RunOutputPresenter is the output-presentation surface the CLI channel needs.
type RunOutputPresenter interface {
	PresentOutput(context.Context, tools.PresentOutputInput) (tools.PresentOutputResult, error)
}

// runCLIAppPreviewRegistrar keeps app registration in the daemon. The Run
// identity and workspace stay daemon-owned, and so do the folders the Run's
// sandbox hid, which every launch of its app hides too (Access TECH B7b).
type runCLIAppPreviewRegistrar struct {
	previews *appPreviewService
	run      Run
	workDir  string
	hidden   []string
}

func (r *runCLIAppPreviewRegistrar) RegisterAppPreview(
	ctx context.Context,
	outputID string,
	commands []tools.PresentOutputCommand,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil || r.previews == nil {
		return errAppPreviewUnavailable
	}
	parsedOutputID, err := uuid.Parse(outputID)
	if err != nil || parsedOutputID == uuid.Nil {
		return errAppPreviewInvalid
	}
	companyID, err := uuid.Parse(r.run.CompanyID)
	if err != nil || companyID == uuid.Nil {
		return errAppPreviewInvalid
	}
	runtimeID, err := uuid.Parse(r.run.RuntimeID)
	if err != nil || runtimeID == uuid.Nil {
		return errAppPreviewInvalid
	}
	previewCommands := make([]appPreviewCommand, len(commands))
	for index, command := range commands {
		previewCommands[index] = appPreviewCommand{
			Command: command.Command, Args: append([]string(nil), command.Args...),
			WorkingDirectory: command.WorkingDirectory, Port: command.Port,
		}
	}
	return r.previews.Register(appPreviewManifest{
		SchemaVersion: appPreviewManifestSchemaVersion,
		CompanyID:     companyID,
		OutputID:      parsedOutputID,
		RunID:         r.run.ID,
		RuntimeID:     runtimeID,
		WorkDir:       r.workDir,
		Commands:      previewCommands,
		Hidden:        r.hidden,
	})
}

// runConversationMessagesUse is the command the platform policy names for
// reading earlier chat; the system prompt teaches it only when admitted.
const runConversationMessagesUse = "zen conversation messages"

// runTaskCreateUse is the command that files a Task for the Run's member. Its
// two operations — the Project lookup and the create — share the one Use so
// the catalog advertises it once.
const runTaskCreateUse = "zen task create"

// runOutputPath is the server route a Run presents its output on. The
// present_output MCP tool posts its input there and the daemon completes it
// locally, posting the finished file or native Page presentation itself.
func runOutputPath(run Run) string {
	return "/runs/" + run.ID.String() + "/output"
}

// runArtifactInspectPath and runArtifactFetchPath are the channel paths the
// artifact_inspect and artifact_fetch MCP tools post to. The daemon completes
// both itself (readRunArtifact); neither is a server route.
func runArtifactInspectPath(run Run) string {
	return "/runs/" + run.ID.String() + "/artifact-inspect"
}

func runArtifactFetchPath(run Run) string {
	return "/runs/" + run.ID.String() + "/artifact-fetch"
}

// runArtifactPath reports whether path is one of the Run's artifact paths.
func runArtifactPath(run Run, path string) bool {
	return path == runArtifactInspectPath(run) || path == runArtifactFetchPath(run)
}

// runOutputNamesPresentation reports whether an output body names a Page
// presentation directly. Such a body must not be treated as local tool
// input: no forwarding row admits it. The daemon prepares native captures.
func runOutputNamesPresentation(body json.RawMessage) bool {
	var output struct {
		PresentationID string `json:"presentationId"`
	}
	return json.Unmarshal(body, &output) == nil && output.PresentationID != ""
}

// newRunOutputPresenter binds the shared output-presentation service to
// one Run: the daemon's own credential, the Run proof, and the working
// directory whose files the Run may present. It runs in the daemon, never
// in the Agent, so it is the one Run caller that holds a credential; its
// uploads and its output post carry the proof. It reads outside the Run's
// sandbox, so it refuses a file the sandbox denies the Run (a workdir such as
// the person's home holds the sign-in files and every Run's home; a tagged
// Run's hidden folders hold its person's private work; Access TECH B3e,
// B7b): launch is the Run's own and hidden folders, as its sandbox gets them.
// Nil when the Run's policy disables saved work or the workspace is unknown.
func (d *Daemon) newRunOutputPresenter(
	run Run,
	doc RunContext,
	runToken, workDir string,
	launch execenv.SeatbeltOptions,
) RunOutputPresenter {
	if !runOutputPresentationAvailable(doc) || workDir == "" {
		return nil
	}
	credentialPath, err := OwnCredentialPath()
	if err != nil {
		if d.logger != nil {
			d.logger.Warn("run output presentation unavailable: no credential path", "error", err)
		}
		return nil
	}
	var registrars []tools.AppPreviewRegistrar
	if d.appPreviews != nil {
		registrars = append(registrars, &runCLIAppPreviewRegistrar{
			previews: d.appPreviews,
			run:      run,
			workDir:  workDir,
			hidden:   launch.Hidden,
		})
	}
	presenter, err := tools.NewPresentOutputService(
		apiclient.New(credentialPath, runToken),
		tools.RunContext{
			CompanyID:      run.CompanyID,
			ConversationID: run.ConversationID,
			TaskID:         run.TaskID,
			RunID:          run.ID.String(),
			AgentID:        run.AgentID,
		},
		workDir,
		runFileGuard(launch),
		registrars...,
	)
	if err != nil {
		if d.logger != nil {
			d.logger.Warn("run output presentation unavailable", "run", run.ID, "error", err)
		}
		return nil
	}
	return presenter
}

// runChannelWorkspace is what the channel completes local calls against: the
// presenter for present_output, and the Run's working directory with its
// sandbox guard for artifact_fetch, which writes there from outside the
// sandbox (Access TECH B3e).
type runChannelWorkspace struct {
	present RunOutputPresenter
	path    string
	// guard refuses, by real path, what the Run's own sandbox denies it
	// (runFileGuard). prepareRunCLIEnvironment always sets it; nil, in
	// tests, adds no check beyond the workspace itself.
	guard func(realPath string) error
}

// registerRunChannel opens the channel for one Run and returns the run.json
// document to write into its isolated home plus the revoke function that
// closes it. Revocation cancels forwards still in flight and waits for their
// handlers, so a handler can never read a channel after its Run is gone.
func (d *Daemon) registerRunChannel(
	run Run,
	doc RunContext,
	runToken string,
	workspace runChannelWorkspace,
) (runchannel.RunControlConfig, func() error, error) {
	if run.ID == uuid.Nil || run.CompanyID == "" || run.AgentID == "" || run.ConversationID == "" {
		return runchannel.RunControlConfig{}, nil, errors.New("run channel identity is incomplete")
	}
	if !runchannel.ValidEndpoint(d.localPlaneEndpoint) {
		return runchannel.RunControlConfig{}, nil, errors.New("run channel endpoint is unavailable")
	}
	if !validRunScopedAuthTokenValue(runToken) {
		return runchannel.RunControlConfig{}, nil, errors.New("run channel requires the run-scoped auth token")
	}
	capability, err := newLocalCapability()
	if err != nil {
		return runchannel.RunControlConfig{}, nil, err
	}
	present := workspace.present
	readsArtifacts := runOutputPresentationAvailable(doc) && workspace.path != ""
	commands := runCLICommands(run, doc)
	// The present_output, artifact_inspect and artifact_fetch rows are
	// local: the daemon completes them with the presenter and the artifact
	// reader, so without one its rows go and the call is refused.
	commands = slices.DeleteFunc(commands, func(command runCLICommand) bool {
		if !command.Operation.Local {
			return false
		}
		if command.Operation.Pattern == runOutputPath(run) {
			return present == nil
		}
		return !readsArtifacts
	})
	policy := runCLIPolicyFor(commands)
	ctx, cancel := context.WithCancel(context.Background())
	channel := &runChannel{
		run:      run,
		policy:   policy,
		runToken: runToken,
		present:  present,
		ctx:      ctx,
		cancel:   cancel,
	}
	if readsArtifacts {
		channel.artifacts = newRunArtifactReader(
			&runArtifactRemote{daemon: d, channel: channel},
			workspace.path,
			run.ID.String(),
			workspace.guard,
		)
	}
	key := runChannelKeyFor(capability)
	d.runChannelMu.Lock()
	if d.runChannels == nil {
		d.runChannels = make(map[runChannelKey]*runChannel)
	}
	if _, collision := d.runChannels[key]; collision {
		d.runChannelMu.Unlock()
		cancel()
		return runchannel.RunControlConfig{}, nil, errors.New("run channel capability collision")
	}
	d.runChannels[key] = channel
	d.runChannelMu.Unlock()

	conversationKind := "direct"
	if doc.ChannelInvocation != nil {
		conversationKind = "channel_agent"
	}
	taskKey := ""
	if task := doc.ExecutionProfile.Task; task != nil {
		conversationKind = "task"
		taskKey = strings.TrimSpace(task.Key)
	}
	config := runchannel.RunControlConfig{
		SchemaVersion:    1,
		Endpoint:         d.localPlaneEndpoint,
		Capability:       capability,
		RunID:            run.ID.String(),
		CompanyID:        run.CompanyID,
		AgentID:          run.AgentID,
		AgentName:        strings.TrimSpace(doc.Agent.Name),
		TaskID:           run.TaskID,
		TaskKey:          taskKey,
		ConversationID:   run.ConversationID,
		ConversationKind: conversationKind,
		Commands:         runCLICommandUses(commands),
	}
	if project := runTaskProject(doc); project != nil {
		config.ProjectID = project.ID
		config.ProjectKey = strings.TrimSpace(project.Key)
	}
	var revokeOnce sync.Once
	revoke := func() error {
		revokeOnce.Do(func() {
			d.runChannelMu.Lock()
			if d.runChannels[key] == channel {
				delete(d.runChannels, key)
			}
			d.runChannelMu.Unlock()
			cancel()
			channel.inflight.Wait()
			channel.runToken = ""
		})
		return nil
	}
	return config, revoke, nil
}

// lookupRunChannel resolves the Authorization header to a live channel and
// holds it in flight until release is called.
func (d *Daemon) lookupRunChannel(header string) (*runChannel, func(), bool) {
	capability, ok := parseCommerceDevAuthorization(header, runchannel.AuthorizationScheme)
	if !ok || !runchannel.ValidCapability(capability) {
		return nil, nil, false
	}
	key := runChannelKeyFor(capability)
	d.runChannelMu.RLock()
	channel, ok := d.runChannels[key]
	if ok {
		channel.inflight.Add(1)
	}
	d.runChannelMu.RUnlock()
	if !ok {
		return nil, nil, false
	}
	var releaseOnce sync.Once
	return channel, func() { releaseOnce.Do(channel.inflight.Done) }, true
}

// writeRunControlConfig places run.json in the Run's isolated home with the
// same private-file discipline as the Commerce bootstrap.
func writeRunControlConfig(directory string, config runchannel.RunControlConfig) (string, error) {
	encoded, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode run control configuration: %w", err)
	}
	path := filepath.Join(directory, runchannel.ControlFileName)
	if err := writePrivateFile(path, append(encoded, '\n')); err != nil {
		return "", err
	}
	return path, nil
}

// runChannelHandler serves runchannel.Path.
func (d *Daemon) runChannelHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeRunChannelError(
				w,
				http.StatusMethodNotAllowed,
				"method_not_allowed",
				"Method not allowed.",
			)
			return
		}
		if !localLoopbackRemote(r.RemoteAddr) || !localLoopbackHost(r.Host) || r.Header.Get("Origin") != "" {
			writeRunChannelError(w, http.StatusForbidden, "forbidden", "Run channel access is forbidden.")
			return
		}
		channel, release, ok := d.lookupRunChannel(r.Header.Get("Authorization"))
		if !ok {
			writeRunChannelError(
				w,
				http.StatusUnauthorized,
				"unauthorized",
				"Run capability is invalid or the Run has ended.",
			)
			return
		}
		defer release()
		var request runchannel.Request
		decoder := json.NewDecoder(io.LimitReader(r.Body, runChannelMaxRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil || request.SchemaVersion != 1 {
			writeRunChannelError(w, http.StatusBadRequest, "invalid_request", "Request body is invalid.")
			return
		}
		pathOnly, ok := validRunChannelPath(request.Path)
		if !ok || !validRunChannelMethod(request.Method) {
			writeRunChannelError(
				w,
				http.StatusBadRequest,
				"invalid_request",
				"Request method or path is invalid.",
			)
			return
		}
		// present_output posts its input to the Run's output route and the
		// daemon completes it. Prebuilt presentation bodies have no forwarding
		// row and are refused.
		// artifact_inspect and artifact_fetch have local rows only.
		local := request.Method == http.MethodPost &&
			(pathOnly == runOutputPath(channel.run) && !runOutputNamesPresentation(request.Body) ||
				runArtifactPath(channel.run, pathOnly))
		operation, admitted := channel.policy.admit(request.Method, pathOnly, local)
		if !admitted {
			// The refusal never reaches the server, so without this line only
			// the agent's transcript would say why its command failed.
			if d.logger != nil {
				d.logger.Warn(
					"run channel refused a request outside the Run's scope",
					"code", "run_scope_denied",
					"run", channel.run.ID,
					"method", request.Method,
					"path", pathOnly,
				)
			}
			writeRunChannelError(
				w,
				http.StatusForbidden,
				"run_scope_denied",
				"This Run's scope does not admit "+request.Method+" "+pathOnly+".",
			)
			return
		}
		if request.Method != http.MethodGet {
			if _, err := uuid.Parse(request.RequestKey); err != nil {
				writeRunChannelError(
					w,
					http.StatusBadRequest,
					"request_key_required",
					"Mutations over the Run channel need a UUID requestKey.",
				)
				return
			}
		}
		ctx, cancel := context.WithTimeout(r.Context(), operation.forwardTimeout())
		defer cancel()
		stop := context.AfterFunc(channel.ctx, cancel)
		defer stop()
		var relayed runchannel.Response
		var err error
		switch {
		case !operation.Local:
			relayed, err = d.forwardRunChannelRequest(ctx, channel, request, operation.forwardTimeout())
		case pathOnly == runOutputPath(channel.run):
			// This local row exists only when the channel has a presenter
			// (registerRunChannel drops it otherwise).
			relayed, err = presentRunOutput(ctx, channel.present, request.Body)
		default:
			// The artifact rows exist only when the channel has an artifact
			// reader (registerRunChannel drops them otherwise).
			request.Path = pathOnly
			relayed, err = readRunArtifact(ctx, channel.artifacts, request)
		}
		if err != nil {
			if d.logger != nil {
				d.logger.Warn(
					"run channel forward failed",
					"run",
					channel.run.ID,
					"path",
					pathOnly,
					"error",
					err,
				)
			}
			if errors.Is(err, errRunChannelResponseTooLarge) {
				writeRunChannelError(
					w,
					http.StatusBadGateway,
					"response_too_large",
					"The server's answer is larger than the Run channel carries.",
				)
				return
			}
			writeRunChannelError(
				w,
				http.StatusBadGateway,
				"server_unreachable",
				"The Zenith Team server could not be reached.",
			)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(relayed)
	}
}

// presentRunOutput completes the present_output MCP tool. The presenter packages file
// items or registers app commands and posts the Run's output. A server verdict
// met on the way is relayed as itself, the same envelope the forwarded
// operations return; any other failure is the presentation failing on this
// machine and answers 422.
func presentRunOutput(
	ctx context.Context,
	present RunOutputPresenter,
	body json.RawMessage,
) (runchannel.Response, error) {
	var input tools.PresentOutputInput
	if len(body) != 0 {
		if err := json.Unmarshal(body, &input); err != nil {
			return runChannelVerdict(
				http.StatusBadRequest,
				"invalid_request",
				"Output presentation body is invalid.",
			)
		}
	}
	result, err := present.PresentOutput(ctx, input)
	if err != nil {
		var apiErr *apiclient.APIError
		if errors.As(err, &apiErr) {
			return runChannelVerdict(apiErr.StatusCode, apiErr.Code, apiErr.Detail)
		}
		return runChannelVerdict(http.StatusUnprocessableEntity, "output_presentation_failed", err.Error())
	}
	encoded, err := json.Marshal(map[string]string{"outputId": result.OutputID})
	if err != nil {
		return runchannel.Response{}, err
	}
	return runchannel.Response{SchemaVersion: 1, Status: http.StatusCreated, Body: encoded}, nil
}

func runChannelVerdict(status int, code, detail string) (runchannel.Response, error) {
	encoded, err := json.Marshal(map[string]string{"code": code, "detail": detail})
	if err != nil {
		return runchannel.Response{}, err
	}
	return runchannel.Response{SchemaVersion: 1, Status: status, Body: encoded}, nil
}

// forwardRunChannelRequest sends the call to the server as the daemon's own
// session plus this Run's proof, renewing the session once on a 401 exactly
// as every other daemon request does. timeout is the admitting row's.
func (d *Daemon) forwardRunChannelRequest(
	ctx context.Context,
	channel *runChannel,
	request runchannel.Request,
	timeout time.Duration,
) (runchannel.Response, error) {
	bearer, err := d.client.bearer(ctx)
	if err != nil {
		return runchannel.Response{}, err
	}
	if bearer == "" {
		return runchannel.Response{}, errors.New("daemon session is unavailable")
	}
	endpoint := strings.TrimRight(d.cfg.ServerBaseURL, "/") + "/api/v1" + request.Path
	build := func(token string) (*http.Request, error) {
		var body io.Reader
		if len(request.Body) != 0 {
			body = bytes.NewReader(request.Body)
		}
		httpRequest, err := http.NewRequestWithContext(ctx, request.Method, endpoint, body)
		if err != nil {
			return nil, err
		}
		httpRequest.Header.Set("Authorization", "Bearer "+token)
		httpRequest.Header.Set("X-Boardly-Run-Token", channel.runToken)
		httpRequest.Header.Set("Accept", "application/json")
		if len(request.Body) != 0 {
			httpRequest.Header.Set("Content-Type", "application/json")
		}
		if request.RequestKey != "" {
			httpRequest.Header.Set("Idempotency-Key", request.RequestKey)
		}
		httpRequest.Header.Set("X-Client-Platform", "cli")
		httpRequest.Header.Set("X-Client-Version", d.cfg.CLIVersion)
		httpRequest.Header.Set("X-Client-OS", runtime.GOOS)
		return httpRequest, nil
	}
	forwardClient := &http.Client{
		Transport: d.client.client.Transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	httpRequest, err := build(bearer)
	if err != nil {
		return runchannel.Response{}, err
	}
	response, err := forwardClient.Do(httpRequest)
	if err != nil {
		return runchannel.Response{}, err
	}
	if response.StatusCode == http.StatusUnauthorized {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 16<<10))
		_ = response.Body.Close()
		renewed, renewErr := d.client.renewBearer(ctx, bearer)
		if renewErr != nil {
			return runchannel.Response{}, renewErr
		}
		retry, err := build(renewed)
		if err != nil {
			return runchannel.Response{}, err
		}
		response, err = forwardClient.Do(retry)
		if err != nil {
			return runchannel.Response{}, err
		}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, runchannel.MaxResponseBytes+1))
	if err != nil {
		return runchannel.Response{}, fmt.Errorf("read server response: %w", err)
	}
	// A cut answer is not valid JSON and would be relayed as a bare detail
	// with the server's 200, so a reader would see an empty list. Refuse it.
	if len(body) > runchannel.MaxResponseBytes {
		return runchannel.Response{}, errRunChannelResponseTooLarge
	}
	relayed := runchannel.Response{SchemaVersion: 1, Status: response.StatusCode}
	if len(bytes.TrimSpace(body)) != 0 {
		if json.Valid(body) {
			relayed.Body = body
		} else {
			encoded, _ := json.Marshal(map[string]any{"detail": strings.TrimSpace(string(body))})
			relayed.Body = encoded
		}
	}
	return relayed, nil
}

func validRunChannelMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

// validRunChannelPath accepts a /api/v1-relative path with an optional query
// string and returns the path without it. Anything that could escape the
// API prefix or smuggle a second request is refused.
//
// The path may carry no percent sign. The policy matches the path as sent,
// one segment per "*", while the server routes on the decoded path: an
// encoded slash or dot would pass a "*" row as one segment and reach
// another route (tasks/<id>%2Fworkspace is a whole Task on the server). No
// command the channel admits needs one; the query string, which takes no
// part in routing, keeps its encoding.
func validRunChannelPath(raw string) (string, bool) {
	if raw == "" || raw[0] != '/' || len(raw) > 4096 || strings.ContainsAny(raw, " \r\n\t#") {
		return "", false
	}
	pathOnly, _, _ := strings.Cut(raw, "?")
	if strings.HasPrefix(pathOnly, "//") || strings.ContainsAny(pathOnly, "\\%") {
		return "", false
	}
	for _, segment := range strings.Split(strings.Trim(pathOnly, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", false
		}
	}
	return pathOnly, true
}

func writeRunChannelError(w http.ResponseWriter, status int, code, reason string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(runchannel.ErrorBody{SchemaVersion: 1, Code: code, Reason: reason})
}
