//go:build darwin

package daemon

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/daemon/execenv"
	"boardly.dev/boardly/server/internal/daemon/workspace"
	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

// shellQuote writes value as one single-quoted POSIX shell word.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// writeFakeProvider writes a provider stand-in script.
func writeFakeProvider(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// desktopLayout gives the daemon the desktop app's layout, spaces and all:
// its home and the app's session store under "Application Support". It
// returns the home and the store.
func desktopLayout(t *testing.T) (string, string) {
	t.Helper()
	support := filepath.Join(t.TempDir(), "Application Support", "Zenith Team Dev")
	home := filepath.Join(support, "boardly-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOARDLY_HOME", home)
	store := filepath.Join(support, "desktop-session.v1.bin")
	t.Setenv(DesktopSessionPathEnv, store)
	for path, content := range map[string]string{
		filepath.Join(home, "auth.json"):          `{"refreshToken":"person-secret"}`,
		filepath.Join(home, "local-preview.json"): `{"capability":"preview-secret"}`,
		store:                             "sealed-desktop-session",
		store + ".42.abc.tmp":             "sealed-desktop-session-being-written",
		filepath.Join(support, "Cookies"): "not a sign-in",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeDaemonSession(t)
	return home, store
}

// The probe passes only when the sandbox really hides the daemon's session
// file and another Run's run.json, keeps a launch's writes to its own folder
// in a stand-in workspaces root, and hides that root but for its own folder
// from a tagged Run (it makes the stand-ins and removes them), and refuses a
// layout whose workspaces root lies in the home.
func TestProbeEnforcesOnlyWhenTheSandboxHidesTheSessionFile(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	home, _ := desktopLayout(t)
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	if verdict := d.probeCredentialIsolation(t.Context()); verdict.Value != protocol.CredentialIsolationEnforced {
		t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
	}
	if left, err := os.ReadDir(temp); err != nil || len(left) != 0 {
		t.Fatalf("the probe left %v in the temporary folder (%v)", left, err)
	}

	inside := d.cfg
	inside.WorkspacesRoot = filepath.Join(home, "workspaces")
	d.cfg = inside
	if verdict := d.probeCredentialIsolation(
		t.Context(),
	); verdict.Value != protocol.CredentialIsolationNotEnforced {
		t.Fatalf("verdict with the workspaces root in the home = %+v, want not_enforced", verdict)
	}

	t.Setenv("BOARDLY_HOME", t.TempDir())
	d.cfg.WorkspacesRoot = t.TempDir()
	if verdict := d.probeCredentialIsolation(
		t.Context(),
	); verdict.Value != protocol.CredentialIsolationNotEnforced {
		t.Fatalf("verdict with no session file to test = %+v, want not_enforced", verdict)
	}
}

// Access TECH B3d, IA-AC5: a whole Run on this Mac, through the real Claude
// launch path. The Agent's shell cannot read or write the daemon's session,
// the person's auth.json, local-preview.json or the desktop app's session
// store (nor its half-written copy), cannot list the home, and cannot move
// the folders above it to read the files elsewhere; it can still read and
// write its own workspace and its own isolated home.
func TestAgentShellCannotReadTheSignInFilesOnMacOS(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.txt")
	fakeClaude := filepath.Join(t.TempDir(), "claude")
	var home, store string
	d := handleOneTaskRun(t, "claude", fakeClaude, func(d *Daemon) {
		home, store = desktopLayout(t)
		verdict := d.probeCredentialIsolation(t.Context())
		if verdict.Value != protocol.CredentialIsolationEnforced {
			t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
		}
		d.recordCredentialIsolation(verdict)
		denied := []string{
			filepath.Join(home, "daemon-auth.json"),
			filepath.Join(home, "auth.json"),
			filepath.Join(home, "local-preview.json"),
			store,
			store + ".42.abc.tmp",
		}
		var script strings.Builder
		script.WriteString("#!/bin/sh\nIFS= read -r _\nreport=" + shellQuote(report) + "\n")
		script.WriteString(": > \"$report\"\n")
		for _, path := range denied {
			script.WriteString("if /bin/sh -c 'cat \"$1\"' sh " + shellQuote(path) +
				" >/dev/null 2>&1; then echo \"read " + shellQuote(path) + "\" >> \"$report\"; fi\n")
			script.WriteString("if /bin/sh -c 'echo planted >> \"$1\"' sh " + shellQuote(path) +
				" 2>/dev/null; then echo \"wrote " + shellQuote(path) + "\" >> \"$report\"; fi\n")
		}
		script.WriteString(
			"if /bin/ls " + shellQuote(
				home,
			) + " >/dev/null 2>&1; then echo 'listed home' >> \"$report\"; fi\n",
		)
		// Moving a folder above the home would carry the files out from
		// under the profile's paths.
		for _, parent := range []string{filepath.Dir(home), filepath.Dir(filepath.Dir(home))} {
			script.WriteString("if /bin/mv " + shellQuote(parent) + " " + shellQuote(parent+".moved") +
				" 2>/dev/null; then echo \"moved " + shellQuote(parent) + "\" >> \"$report\"; fi\n")
		}
		script.WriteString(
			"if ! /bin/sh -c 'echo mine > workspace-probe.txt && cat workspace-probe.txt' >/dev/null 2>&1; " +
				"then echo 'workspace refused' >> \"$report\"; fi\n",
		)
		script.WriteString(
			"if ! /bin/sh -c 'echo mine > \"$BOARDLY_HOME/probe\" && cat \"$BOARDLY_HOME/probe\"' >/dev/null 2>&1; " +
				"then echo 'isolated home refused' >> \"$report\"; fi\n",
		)
		script.WriteString("echo done >> \"$report\"\n")
		script.WriteString(
			`echo '{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"checked"}]}}'` + "\n",
		)
		script.WriteString(
			`echo '{"type":"result","subtype":"success","is_error":false,"result":"checked","session_id":"s1"}'` + "\n",
		)
		writeFakeProvider(t, fakeClaude, script.String())
	})
	if d.credentialIsolation() != protocol.CredentialIsolationEnforced {
		t.Fatal("the daemon stopped reporting enforced during the Run")
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("the Agent never ran: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "done" {
		t.Fatalf(
			"Agent shell report:\n%s\nwant only \"done\": every sign-in file refused, its own folders open",
			got,
		)
	}
	for _, path := range []string{filepath.Join(home, "daemon-auth.json"), store} {
		content, err := os.ReadFile(path)
		if err != nil || strings.Contains(string(content), "planted") {
			t.Fatalf("%s after the Run = %q, %v; want it untouched", path, content, err)
		}
	}
}

// A connector-only Codex launch runs under the profile with Codex told
// danger-full-access, because its own read-only Seatbelt cannot start inside
// the daemon's. The profile's write fence takes its place: Codex writes its
// per-run home and its shared home (where it renews its sign-in), and
// nothing else; the sign-in files stay hidden.
func TestConnectorOnlyCodexLaunchRunsUnderTheWriteFence(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	home, _ := desktopLayout(t)
	if verdict := d.probeCredentialIsolation(t.Context()); verdict.Value != protocol.CredentialIsolationEnforced {
		t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
	}
	shared := filepath.Join(t.TempDir(), "codex-shared")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", shared)
	envRoot := filepath.Join(d.cfg.WorkspacesRoot, "scheduled-run")
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	env, err := execenv.Prepare(execenv.Options{
		EnvRoot: envRoot, WorkDir: workDir, Provider: "codex", ConnectorOnly: true, Sandboxed: true,
	}, testLogger())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	sandbox, err := d.launchSandbox(execenv.SeatbeltOptions{
		RunDirs:       []string{envRoot, workDir},
		Writable:      []string{envRoot},
		WritableRoots: env.SandboxWritableRoots,
	})
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}

	outside := t.TempDir()
	fakeCodex := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\n" +
		`if [ "$1" = "--version" ]; then echo "codex-cli 0.0.0-test"; exit 0; fi` + "\n" +
		"read line\n" +
		`echo '{"jsonrpc":"2.0","id":1,"result":{}}'` + "\n" +
		"read line\nread line\n" +
		`case "$line" in *'"sandbox":"danger-full-access"'*) ;; *) echo "thread asked for a Seatbelt" >&2; exit 21;; esac` + "\n" +
		`echo state > "$CODEX_HOME/state.json" || { echo "per-run home refused" >&2; exit 22; }` + "\n" +
		"echo renewed > " + shellQuote(filepath.Join(shared, "auth.json")) + ` || { echo "shared home refused" >&2; exit 23; }` + "\n" +
		"if echo escape > " + shellQuote(filepath.Join(outside, "escape")) + ` 2>/dev/null; then echo "wrote outside" >&2; exit 24; fi` + "\n" +
		"if cat " + shellQuote(filepath.Join(home, "daemon-auth.json")) + ` >/dev/null 2>&1; then echo "read the session" >&2; exit 25; fi` + "\n" +
		`echo '{"jsonrpc":"2.0","id":2,"result":{"thread":{"id":"thr-fenced"}}}'` + "\n" +
		"read line\n" +
		`case "$line" in *'"dangerFullAccess"'*) ;; *) echo "turn asked for a Seatbelt" >&2; exit 26;; esac` + "\n" +
		`echo '{"jsonrpc":"2.0","id":3,"result":{}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"turn/started","params":{"threadId":"thr-fenced","turn":{"id":"turn-1"}}}'` + "\n" +
		`echo '{"jsonrpc":"2.0","method":"turn/completed","params":{"threadId":"thr-fenced","turn":{"id":"turn-1","status":"completed"}}}'` + "\n"
	writeFakeProvider(t, fakeCodex, script)

	provider, err := agent.New("codex", agent.Config{
		ExecutablePath: fakeCodex,
		Env:            map[string]string{"CODEX_HOME": env.CodexHome},
		Logger:         testLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	execution, err := provider.Execute(ctx, "brief", agent.ExecOptions{
		Cwd:                       workDir,
		ToolPolicy:                agent.ToolPolicyConnectorOnly,
		Timeout:                   20 * time.Second,
		SemanticInactivityTimeout: 20 * time.Second,
		Sandbox:                   sandbox,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var result agent.Result
	for event := range execution.Events {
		switch event := event.(type) {
		case agent.CompletedItem:
			event.Acknowledge()
		case agent.Finished:
			result = event.Result
		}
	}
	if result.Status != agent.ResultCompleted {
		t.Fatalf("connector-only Codex under the fence = %q (%s), want completed", result.Status, result.Error)
	}
	if _, err := os.Stat(filepath.Join(outside, "escape")); !os.IsNotExist(err) {
		t.Fatalf("the launch wrote outside its roots: %v", err)
	}
}

// personLayout points HOME at a stand-in for the person's home with a
// launchd agents folder and a .zshrc, so a sandbox that failed would change
// these and not the real ones. It returns the home.
func personLayout(t *testing.T) string {
	t.Helper()
	person := t.TempDir()
	t.Setenv("HOME", person)
	if err := os.MkdirAll(filepath.Join(person, "Library", "LaunchAgents"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(person, ".zshrc"), []byte("# the person's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return person
}

// otherRunHome makes another Run's isolated home under the workspaces root,
// as the daemon makes one for every Run, with its run.json and Commerce file.
func otherRunHome(t *testing.T, workspacesRoot string) string {
	t.Helper()
	home := filepath.Join(
		workspacesRoot, workspace.LocalScratchDirectoryName, uuid.NewString(), execenv.RunHomePrefix+"424242",
	)
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"run.json":          `{"capability":"another-run"}`,
		"commerce-dev.json": `{"capability":"another-run-commerce"}`,
	} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// Access TECH B3e: a whole Run on this Mac, through the real Claude launch
// path, with its Run channel open. The Agent's shell reads its own run.json
// but cannot read or list another Run's isolated home (its run.json and
// Commerce file), so it cannot call through that Run's channel. It cannot
// start launchctl, add a launchd agent or change the person's .zshrc, which
// it still reads.
func TestAgentShellCannotReachAnotherRunsChannelOnMacOS(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.txt")
	fakeClaude := filepath.Join(t.TempDir(), "claude")
	var other, person string
	d := handleOneTaskRun(t, "claude", fakeClaude, func(d *Daemon) {
		desktopLayout(t)
		person = personLayout(t)
		d.localPlaneEndpoint = "http://127.0.0.1:19713"
		other = otherRunHome(t, d.cfg.WorkspacesRoot)
		verdict := d.probeCredentialIsolation(t.Context())
		if verdict.Value != protocol.CredentialIsolationEnforced {
			t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
		}
		d.recordCredentialIsolation(verdict)
		checks := []struct{ ran, script string }{
			{"read another Run's run.json", `cat "$1/run.json"`},
			{"read another Run's Commerce file", `cat "$1/commerce-dev.json"`},
			{"listed another Run's home", `ls "$1"`},
			{"started launchctl", `/bin/launchctl version`},
			{"added a launchd agent", `echo plist > "$2/Library/LaunchAgents/dev.zenith.test.plist"`},
			{"changed .zshrc", `echo planted >> "$2/.zshrc"`},
		}
		var script strings.Builder
		script.WriteString("#!/bin/sh\nIFS= read -r _\nreport=" + shellQuote(report) + "\n: > \"$report\"\n")
		arguments := " sh " + shellQuote(other) + " " + shellQuote(person) + " >/dev/null 2>&1"
		for _, check := range checks {
			script.WriteString("if /bin/sh -c " + shellQuote(check.script) + arguments)
			script.WriteString("; then echo " + shellQuote(check.ran) + " >> \"$report\"; fi\n")
		}
		script.WriteString(
			"if ! /bin/sh -c 'grep -q capability \"$BOARDLY_HOME/run.json\"' >/dev/null 2>&1; " +
				"then echo 'own run.json refused' >> \"$report\"; fi\n",
		)
		script.WriteString(
			"if ! /bin/sh -c 'cat \"$1/.zshrc\"' sh " + shellQuote(person) + " >/dev/null 2>&1; " +
				"then echo '.zshrc unreadable' >> \"$report\"; fi\n",
		)
		script.WriteString("echo done >> \"$report\"\n")
		script.WriteString(
			`echo '{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"checked"}]}}'` + "\n",
		)
		script.WriteString(
			`echo '{"type":"result","subtype":"success","is_error":false,"result":"checked","session_id":"s1"}'` + "\n",
		)
		writeFakeProvider(t, fakeClaude, script.String())
	})
	if d.credentialIsolation() != protocol.CredentialIsolationEnforced {
		t.Fatal("the daemon stopped reporting enforced during the Run")
	}
	raw, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("the Agent never ran: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "done" {
		t.Fatalf("Agent shell report:\n%s\nwant only \"done\": another Run's home refused, its own open", got)
	}
	if content, err := os.ReadFile(filepath.Join(other, "run.json")); err != nil ||
		string(content) != `{"capability":"another-run"}` {
		t.Fatalf("another Run's run.json after the Run = %q, %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(person, ".zshrc")); err != nil ||
		string(content) != "# the person's\n" {
		t.Fatalf(".zshrc after the Run = %q, %v", content, err)
	}
	agents, err := os.ReadDir(filepath.Join(person, "Library", "LaunchAgents"))
	if err != nil || len(agents) != 0 {
		t.Fatalf("launchd agents after the Run = %v, %v", agents, err)
	}
}

// shellCheck is one step an Agent's shell tries: what to report and the
// script, which gets the folders of otherRunsUnderTheRoot as $1 to $6.
type shellCheck struct{ report, script string }

// otherRunsUnderTheRoot is what the workspaces root holds besides the Run
// under test: another Task's worktree in the same Project (with a note of
// its own), another Run's scratch (with its zen and Codex settings), the
// Company main (with its Memory), a Tool installation, and the Project main
// with one committed file, README.md, which the Run's worktree branches
// from.
type otherRunsUnderTheRoot struct {
	otherWorktree, otherScratch, companyMain, projectMain, tool string
}

func (o otherRunsUnderTheRoot) args() []string {
	return []string{o.otherWorktree, o.otherScratch, o.companyMain, o.projectMain, o.tool}
}

func makeOtherRunsUnderTheRoot(t *testing.T, d *Daemon) otherRunsUnderTheRoot {
	t.Helper()
	ctx := t.Context()
	scope := "projects/" + taskMergeTaskID
	other, err := d.workspaces.AcquireScopedScratch(
		ctx, oneRunCompanyID, scope, uuid.NewString(), uuid.New(), 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.workspaces.MarkCompleted(other.Root); err != nil {
		t.Fatal(err)
	}
	projectMain, err := d.workspaces.ResolveMain(ctx, oneRunCompanyID, scope)
	if err != nil {
		t.Fatal(err)
	}
	companyMain, err := d.workspaces.EnsureMain(ctx, oneRunCompanyID, companyWorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	layout := otherRunsUnderTheRoot{
		otherWorktree: other.WorkDir,
		otherScratch: filepath.Join(
			d.cfg.WorkspacesRoot, workspace.LocalScratchDirectoryName, uuid.NewString(),
		),
		companyMain: companyMain,
		projectMain: projectMain,
		tool:        filepath.Join(d.cfg.WorkspacesRoot, ".tools", "npm", "tool", "index.js"),
	}
	for path, content := range map[string]string{
		filepath.Join(layout.otherWorktree, "notes.md"):                 "theirs\n",
		filepath.Join(layout.otherScratch, ".boardly-bin-1", "zen"):     "#!/bin/sh\n",
		filepath.Join(layout.otherScratch, "codex-home", "config.toml"): "model = \"theirs\"\n",
		filepath.Join(layout.companyMain, ".memory", "team.md"):         "# Team\n",
		filepath.Join(layout.projectMain, "README.md"):                  "# Project\n",
		layout.tool: "// reviewed\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"add", "README.md"},
		{"-c", "user.name=Person", "-c", "user.email=person@example.com", "commit", "-q", "-m", "Add README"},
	} {
		command := exec.CommandContext(ctx, "git", args...)
		command.Dir = layout.projectMain
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	return layout
}

// requireUntouched fails when a step of the Agent's shell changed anything
// of another Run's, the Project main's or the Company main's.
func (o otherRunsUnderTheRoot) requireUntouched(t *testing.T, root string) {
	t.Helper()
	for path, want := range map[string]string{
		filepath.Join(o.otherWorktree, "notes.md"):                 "theirs\n",
		filepath.Join(o.otherScratch, ".boardly-bin-1", "zen"):     "#!/bin/sh\n",
		filepath.Join(o.otherScratch, "codex-home", "config.toml"): "model = \"theirs\"\n",
		filepath.Join(o.companyMain, ".memory", "team.md"):         "# Team\n",
		o.tool: "// reviewed\n",
	} {
		if content, err := os.ReadFile(path); err != nil || string(content) != want {
			t.Errorf("%s after the Run = %q, %v; want it untouched", path, content, err)
		}
	}
	for _, path := range []string{
		filepath.Join(o.otherWorktree, "planted.md"),
		filepath.Join(o.companyMain, ".memory", "planted.md"),
		filepath.Join(o.projectMain, "planted.md"),
		filepath.Join(o.projectMain, ".git", "hooks", "post-checkout"),
		filepath.Join(root, "planted.md"),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s was planted (%v)", path, err)
		}
	}
	config, err := os.ReadFile(filepath.Join(o.projectMain, ".git", "config"))
	if err != nil || strings.Contains(string(config), "hooksPath") {
		t.Errorf("the Project main's Git config is now %q, %v", config, err)
	}
	command := exec.CommandContext(t.Context(), "git", "show-ref", "--verify", "--quiet", "refs/heads/planted")
	command.Dir = o.projectMain
	if command.Run() == nil {
		t.Error("a branch was planted in the Project main's repository")
	}
}

// writeShellChecksProvider writes a stand-in Claude whose shell runs every
// allowed step, reporting any refused, and every denied step, reporting any
// that worked, then "done", and answers once.
func writeShellChecksProvider(
	t *testing.T, path, report string, args []string, allowed, denied []shellCheck,
) {
	t.Helper()
	var quoted strings.Builder
	for _, arg := range args {
		quoted.WriteString(" " + shellQuote(arg))
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\nIFS= read -r _\nreport=" + shellQuote(report) + "\n: > \"$report\"\n")
	for _, check := range allowed {
		script.WriteString("if ! /bin/sh -c " + shellQuote(check.script) + " sh" + quoted.String() +
			" >/dev/null 2>&1; then echo " + shellQuote("refused: "+check.report) + " >> \"$report\"; fi\n")
	}
	for _, check := range denied {
		script.WriteString("if /bin/sh -c " + shellQuote(check.script) + " sh" + quoted.String() +
			" >/dev/null 2>&1; then echo " + shellQuote("allowed: "+check.report) + " >> \"$report\"; fi\n")
	}
	script.WriteString("echo done >> \"$report\"\n")
	script.WriteString(
		`echo '{"type":"assistant","message":{"id":"m1","role":"assistant","content":[{"type":"text","text":"checked"}]}}'` + "\n",
	)
	script.WriteString(
		`echo '{"type":"result","subtype":"success","is_error":false,"result":"checked","session_id":"s1"}'` + "\n",
	)
	writeFakeProvider(t, path, script.String())
}

// The steps no Run may take under the workspaces root. $1 is another Task's
// worktree, $2 another Run's scratch, $3 the Company main, $4 the Project
// main, $5 a Tool installation, $6 the workspaces root.
var plantingSteps = []shellCheck{
	{"a file in another Task's worktree", `echo planted > "$1/planted.md"`},
	{"another Task's note changed", `echo planted >> "$1/notes.md"`},
	{"another Run's zen changed", `echo planted >> "$2/.boardly-bin-1/zen"`},
	{"another Run's Codex settings changed", `echo planted >> "$2/codex-home/config.toml"`},
	{"another Run's scratch removed", `rm -rf "$2"`},
	{"a Git hook in the Project main", `echo planted > "$4/.git/hooks/post-checkout"`},
	{"a file in the Project main", `echo planted > "$4/planted.md"`},
	{"a Tool installation changed", `echo planted >> "$5"`},
	{"a file at the top of the workspaces root", `echo planted > "$6/planted.md"`},
	{
		"a file through a link to another Task's worktree",
		`ln -s "$1" link-out && echo planted > link-out/planted.md`,
	},
}

// Access TECH B3f: a whole Run on this Mac, through the real Claude launch
// path. Under the workspaces root the Agent's shell writes only its own
// folders. A Task Run writes its worktree (Memory included), its env root and
// isolated home, and Git restores a file there through its worktree's own
// folder in the Project main's repository. It cannot write another Task's
// worktree, another Run's scratch (its zen, its Codex settings), the Project
// main or its repository (a hook, the config, a commit, a branch), the
// Company main's Memory, a Tool installation or the root itself, nor go
// through a link it makes; it still reads them. A direct chat's Run works in
// the Company main and commits there, as before, and is kept out of every
// Task's folders the same way.
func TestAgentShellWritesOnlyItsOwnFoldersOnMacOS(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	t.Run("task run", func(t *testing.T) {
		report := filepath.Join(t.TempDir(), "report.txt")
		fakeClaude := filepath.Join(t.TempDir(), "claude")
		var layout otherRunsUnderTheRoot
		d := handleOneTaskRun(t, "claude", fakeClaude, func(d *Daemon) {
			desktopLayout(t)
			personLayout(t)
			d.localPlaneEndpoint = "http://127.0.0.1:19713"
			layout = makeOtherRunsUnderTheRoot(t, d)
			verdict := d.probeCredentialIsolation(t.Context())
			if verdict.Value != protocol.CredentialIsolationEnforced {
				t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
			}
			d.recordCredentialIsolation(verdict)
			allowed := []shellCheck{
				{"its workdir", `echo mine > workspace-probe.txt && cat workspace-probe.txt`},
				{"its Memory", `echo note > .memory/b3f.md`},
				{"its env root", `echo mine > "$(dirname "$PWD")/env-probe.txt"`},
				{"its isolated home", `echo mine > "$BOARDLY_HOME/probe"`},
				{"git status", `git status --short`},
				{
					"git restores a file",
					`echo changed >> README.md && git checkout -- README.md && grep -qx '# Project' README.md`,
				},
				{"reading another Task's worktree", `cat "$1/notes.md"`},
				{"reading the Company Memory", `cat "$3/.memory/team.md"`},
			}
			denied := append([]shellCheck{
				{"the Company Memory changed", `echo planted > "$3/.memory/planted.md"`},
				{"the Project main's Git config changed", `git config core.hooksPath /tmp`},
				{
					"a commit in the Project main's repository",
					`git -c user.name=a -c user.email=a@a commit -q --allow-empty -m planted`,
				},
				{"a branch in the Project main's repository", `git branch planted`},
			}, plantingSteps...)
			writeShellChecksProvider(t, fakeClaude, report,
				append(layout.args(), d.cfg.WorkspacesRoot), allowed, denied)
		})
		if d.credentialIsolation() != protocol.CredentialIsolationEnforced {
			t.Fatal("the daemon stopped reporting enforced during the Run")
		}
		raw, err := os.ReadFile(report)
		if err != nil {
			t.Fatalf("the Agent never ran: %v", err)
		}
		if got := strings.TrimSpace(string(raw)); got != "done" {
			t.Fatalf(
				"Agent shell report:\n%s\nwant only \"done\": its own folders open, every other refused",
				got,
			)
		}
		layout.requireUntouched(t, d.cfg.WorkspacesRoot)
	})

	t.Run("direct chat run", func(t *testing.T) {
		report := filepath.Join(t.TempDir(), "report.txt")
		fakeClaude := filepath.Join(t.TempDir(), "claude")
		var layout otherRunsUnderTheRoot
		direct := RunContext{ExecutionProfile: RunExecutionProfileContext{Source: "message"}}
		d := handleOneRun(t, "claude", fakeClaude, direct, func(d *Daemon) {
			desktopLayout(t)
			personLayout(t)
			d.localPlaneEndpoint = "http://127.0.0.1:19713"
			layout = makeOtherRunsUnderTheRoot(t, d)
			verdict := d.probeCredentialIsolation(t.Context())
			if verdict.Value != protocol.CredentialIsolationEnforced {
				t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
			}
			d.recordCredentialIsolation(verdict)
			allowed := []shellCheck{
				{
					"working in the Company main",
					`[ "$PWD" = "$3" ] || [ "$(pwd -P)" = "$(cd "$3" && pwd -P)" ]`,
				},
				{
					"its Memory, committed",
					`echo note > .memory/direct.md && git add .memory/direct.md && ` +
						`git -c user.name=a -c user.email=a@a commit -q -m memory`,
				},
				{"its isolated home", `echo mine > "$BOARDLY_HOME/probe"`},
			}
			writeShellChecksProvider(t, fakeClaude, report,
				append(layout.args(), d.cfg.WorkspacesRoot), allowed, plantingSteps)
		})
		raw, err := os.ReadFile(report)
		if err != nil {
			t.Fatalf("the Agent never ran: %v", err)
		}
		if got := strings.TrimSpace(string(raw)); got != "done" {
			t.Fatalf(
				"Agent shell report:\n%s\nwant only \"done\": the Company main open, every Task's folder refused",
				got,
			)
		}
		layout.requireUntouched(t, d.cfg.WorkspacesRoot)
		if _, err := os.Stat(filepath.Join(layout.companyMain, ".memory", "direct.md")); err != nil {
			t.Fatalf("the direct chat's Memory note is missing: %v", err)
		}
	})
}

// privateFolders is what a person's computer holds that a Run tagged in a
// chat must not open (Access TECH B7b): the Company main with its Memory, its
// notes and its business context, another Run's workdir and the repository
// cache, beside the local folder the person bound.
type privateFolders struct {
	companyMain, otherRun, cache, bound, root string
}

// args are the folders as the scripts get them: $1 the Company main, $2 the
// bound folder, $3 another Run's workdir, $4 the repository cache, $5 the
// workspaces root and $6 the Company main relative to the workspaces root.
func (p privateFolders) args(t *testing.T) []string {
	t.Helper()
	relative, err := filepath.Rel(p.root, p.companyMain)
	if err != nil {
		t.Fatal(err)
	}
	return []string{p.companyMain, p.bound, p.otherRun, p.cache, p.root, relative}
}

// untouched are the private files no step may change, with their content.
func (p privateFolders) untouched() map[string]string {
	return map[string]string{
		filepath.Join(p.companyMain, ".memory", "team.md"):                       "private memory\n",
		filepath.Join(p.companyMain, "notes.md"):                                 "private notes\n",
		filepath.Join(p.companyMain, "assets", "general", "business_context.md"): "# Business\n",
		filepath.Join(p.otherRun, "notes.md"):                                    "theirs\n",
		filepath.Join(p.cache, "probe", "HEAD"):                                  "ref: refs/heads/main\n",
		filepath.Join(p.bound, "secret.md"):                                      "bound secret\n",
	}
}

func makePrivateFolders(t *testing.T, d *Daemon, bound string) privateFolders {
	t.Helper()
	companyMain, err := d.workspaces.EnsureMain(t.Context(), oneRunCompanyID, companyWorkspaceScope)
	if err != nil {
		t.Fatal(err)
	}
	p := privateFolders{
		companyMain: companyMain,
		otherRun:    filepath.Join(d.cfg.WorkspacesRoot, uuid.NewString(), "workdir"),
		cache:       repositoryCacheRoot(d.cfg.WorkspacesRoot),
		bound:       bound,
		root:        d.cfg.WorkspacesRoot,
	}
	for path, content := range p.untouched() {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

// Access TECH B7b, IA-AC29: a Run tagged in a channel, whole, on this Mac,
// through the real Claude launch path, on a computer whose person bound a
// local folder and whose Company main holds Memory. The Agent's shell works
// in its own scratch, <root>/<id>/workdir with its isolated home beside it,
// as the desktop places it; it writes there and in its env root, reads its
// own run.json and the business context, keeps a Git repository of its own
// and writes the temporary folder. It cannot read or list the Company main,
// its Memory or its notes, the bound folder, another Run's folder, the
// workspaces root or the repository cache, nor reach them through a link, a
// copy or by walking up from its folder. A direct chat's Run in the same
// layout, in the bound folder, still reads all of them.
func TestATaggedRunsShellCannotOpenItsPersonsPrivateFoldersOnMacOS(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	bound, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	resources := []RunResourceContext{
		{ResourceType: "local_directory", LocalDaemonID: "daemon-1", LocalPath: bound},
	}
	shellRun := func(t *testing.T, doc RunContext, allowed, denied []shellCheck) privateFolders {
		t.Helper()
		report := filepath.Join(t.TempDir(), "report.txt")
		fakeClaude := filepath.Join(t.TempDir(), "claude")
		var private privateFolders
		d := handleOneRun(t, "claude", fakeClaude, doc, func(d *Daemon) {
			desktopLayout(t)
			personLayout(t)
			d.localPlaneEndpoint = "http://127.0.0.1:19713"
			private = makePrivateFolders(t, d, bound)
			verdict := d.probeCredentialIsolation(t.Context())
			if verdict.Value != protocol.CredentialIsolationEnforced {
				t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
			}
			d.recordCredentialIsolation(verdict)
			writeShellChecksProvider(t, fakeClaude, report, private.args(t), allowed, denied)
		})
		if d.credentialIsolation() != protocol.CredentialIsolationEnforced {
			t.Fatal("the daemon stopped reporting enforced during the Run")
		}
		raw, err := os.ReadFile(report)
		if err != nil {
			t.Fatalf("the Agent never ran: %v", err)
		}
		if got := strings.TrimSpace(string(raw)); got != "done" {
			t.Fatalf("Agent shell report:\n%s\nwant only \"done\"", got)
		}
		for path, want := range private.untouched() {
			if content, err := os.ReadFile(path); err != nil || string(content) != want {
				t.Errorf("%s after the Run = %q, %v; want it untouched", path, content, err)
			}
		}
		return private
	}

	t.Run("tagged run", func(t *testing.T) {
		private := shellRun(t, taggedRunContext(resources...), []shellCheck{
			{
				"working in <root>/<id>/workdir beside its home",
				`[ "$(basename "$(pwd -P)")" = workdir ] && ` +
					`[ "$(dirname "$(pwd -P)")" = "$(cd "$(dirname "$BOARDLY_HOME")" && pwd -P)" ] && ` +
					`[ "$(dirname "$(dirname "$(pwd -P)")")" = "$5" ]`,
			},
			{"its workdir", `echo mine > answer.md && cat answer.md && ls >/dev/null`},
			{"its env root", `echo mine > ../env-probe.txt && ls .. >/dev/null`},
			{
				"its isolated home",
				`echo mine > "$BOARDLY_HOME/probe" && grep -q capability "$BOARDLY_HOME/run.json"`,
			},
			{"the business context", `cat "$1/assets/general/business_context.md"`},
			{"the temporary folder", `f=$(mktemp) && echo x > "$f" && rm "$f"`},
			{
				"a Git repository of its own",
				`git init -q && git add answer.md && git -c user.name=a -c user.email=a@a commit -q -m answer`,
			},
		}, []shellCheck{
			{"read the Company Memory", `cat "$1/.memory/team.md"`},
			{"read the Company main's notes", `cat "$1/notes.md"`},
			{"listed the Company main", `ls "$1"`},
			{"read the bound folder", `cat "$2/secret.md"`},
			{"listed the bound folder", `ls "$2"`},
			{"read another Run's folder", `cat "$3/notes.md"`},
			{"read the repository cache", `cat "$4/probe/HEAD"`},
			{"listed the workspaces root", `ls "$5"`},
			{"wrote the Company Memory", `echo planted > "$1/.memory/planted.md"`},
			{"read the Memory through a link", `ln -s "$1/.memory" memory-link && cat memory-link/team.md`},
			{"copied the Memory", `cp -R "$1/.memory" memory-copy && cat memory-copy/team.md`},
			{"walked up to the Company Memory", `cat "../../$6/.memory/team.md"`},
		})
		if _, err := os.Lstat(
			filepath.Join(private.companyMain, ".memory", "planted.md"),
		); !os.IsNotExist(
			err,
		) {
			t.Fatalf("the tagged Run planted a Memory file (%v)", err)
		}
	})

	t.Run("direct chat run", func(t *testing.T) {
		shellRun(t, RunContext{Resources: resources}, []shellCheck{
			{"working in the bound folder", `[ "$(pwd -P)" = "$2" ] && cat secret.md`},
			{"reading the Company Memory", `cat "$1/.memory/team.md" "$1/notes.md" && ls "$1" >/dev/null`},
			{"reading another Run's folder", `cat "$3/notes.md" && ls "$5" >/dev/null`},
		}, nil)
	})
}

// Access TECH B3e: an app preview's commands, which an Agent wrote, run in
// the Runs' sandbox on this Mac, through the real supervisor. They cannot
// read the daemon's session or another Run's run.json, nor start launchctl,
// nor write another Run's folder under the workspaces root (B3f), and still
// work in their own folder there and serve the preview. The computer keeps
// reporting enforced while the preview runs, and the preview stops cleanly.
func TestAppPreviewCommandsRunInTheSandboxOnMacOS(t *testing.T) {
	d := newTestDaemon(t, "http://127.0.0.1:1")
	home, _ := desktopLayout(t)
	personLayout(t)
	other := otherRunHome(t, d.cfg.WorkspacesRoot)
	verdict := d.probeCredentialIsolation(t.Context())
	if verdict.Value != protocol.CredentialIsolationEnforced {
		t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
	}
	d.recordCredentialIsolation(verdict)

	settings := testAppPreviewSettings()
	settings.startupDeadline = 10 * time.Second
	settings.healthInterval = 50 * time.Millisecond
	settings.cleanupTimeout = 5 * time.Second
	service, err := newAppPreviewServiceWithBackend(
		t.TempDir(), testLogger(), newPlatformAppPreviewBackend(), d.appPreviewIsolation, settings,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	port := availableAppPreviewPort(t)
	manifest := testAppPreviewManifest(t, port)
	// The preview's folder is a Run's workdir under the workspaces root,
	// which it writes; another Run's folder there it may not (B3f).
	manifest.WorkDir = filepath.Join(d.cfg.WorkspacesRoot, uuid.NewString(), "workdir")
	if err := os.MkdirAll(manifest.WorkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// One line: a preview argument may not hold a newline.
	script := `: > report.txt; ` +
		`if cat "$1" >/dev/null 2>&1; then echo "read the session" >> report.txt; fi; ` +
		`if cat "$2" >/dev/null 2>&1; then echo "read another Run's run.json" >> report.txt; fi; ` +
		`if /bin/launchctl version >/dev/null 2>&1; then echo "started launchctl" >> report.txt; fi; ` +
		`if echo planted > "$5/planted.md" 2>/dev/null; then echo "wrote another Run's folder" >> report.txt; fi; ` +
		`echo done >> report.txt; ` +
		`exec "$3" -test.run=TestAppPreviewHelperProcess -- serve "$4"`
	manifest.Commands[0] = appPreviewCommand{
		Command: "/bin/sh",
		Args: []string{
			"-c", script, "sh",
			filepath.Join(home, "daemon-auth.json"), filepath.Join(other, "run.json"),
			os.Args[0], strconv.Itoa(port), filepath.Dir(other),
		},
		Port: port,
	}
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	if got := d.credentialIsolation(); got != protocol.CredentialIsolationEnforced {
		t.Fatalf("report while the sandboxed preview runs = %q, want enforced", got)
	}
	raw, err := os.ReadFile(filepath.Join(manifest.WorkDir, "report.txt"))
	if err != nil {
		t.Fatalf("the preview command never ran: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "done" {
		t.Fatalf("preview command report:\n%s\nwant only \"done\"", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(other), "planted.md")); !os.IsNotExist(err) {
		t.Fatalf("the preview planted a file in another Run's folder (%v)", err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if connection, err := net.DialTimeout(
		"tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond,
	); err == nil {
		_ = connection.Close()
		t.Fatal("the sandboxed preview survived its cleanup")
	}
}

// Access TECH B7b: the app a tagged Run presented, through the real
// supervisor on this Mac. Its commands, which the Agent wrote, hide what the
// Run hid: they cannot read the Company Memory, the bound folder or another
// Run's folder, nor start a program there, and still work in the preview's
// own folder and serve it.
func TestATaggedRunsAppPreviewCannotOpenItsPersonsPrivateFoldersOnMacOS(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is unavailable")
	}
	d := newTestDaemon(t, "http://127.0.0.1:1")
	desktopLayout(t)
	personLayout(t)
	bound, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	private := makePrivateFolders(t, d, bound)
	verdict := d.probeCredentialIsolation(t.Context())
	if verdict.Value != protocol.CredentialIsolationEnforced {
		t.Fatalf("verdict = %+v, want enforced on this Mac", verdict)
	}
	d.recordCredentialIsolation(verdict)

	settings := testAppPreviewSettings()
	settings.startupDeadline = 10 * time.Second
	settings.healthInterval = 50 * time.Millisecond
	settings.cleanupTimeout = 5 * time.Second
	service, err := newAppPreviewServiceWithBackend(
		t.TempDir(), testLogger(), newPlatformAppPreviewBackend(), d.appPreviewIsolation, settings,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	port := availableAppPreviewPort(t)
	manifest := testAppPreviewManifest(t, port)
	manifest.WorkDir = filepath.Join(d.cfg.WorkspacesRoot, uuid.NewString(), "workdir")
	if err := os.MkdirAll(manifest.WorkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest.Hidden = d.runHiddenFolders(taggedRunContext(
		RunResourceContext{ResourceType: "local_directory", LocalDaemonID: d.cfg.DaemonID, LocalPath: bound},
	))
	// A compiled program in another Run's folder: this test binary, which
	// macOS starts outside the sandbox.
	program := filepath.Join(private.otherRun, "program")
	if err := os.Link(os.Args[0], program); err != nil {
		content, readErr := os.ReadFile(os.Args[0])
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(program, content, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// One line: a preview argument may not hold a newline.
	script := `: > report.txt; ` +
		`if cat "$1/.memory/team.md" >/dev/null 2>&1; then echo "read the Company Memory" >> report.txt; fi; ` +
		`if cat "$2/secret.md" >/dev/null 2>&1; then echo "read the bound folder" >> report.txt; fi; ` +
		`if cat "$3/notes.md" >/dev/null 2>&1; then echo "read another Run's folder" >> report.txt; fi; ` +
		`if "$3/program" -test.run='^$' >/dev/null 2>&1; then echo "started another Run's program" >> report.txt; fi; ` +
		`echo done >> report.txt; ` +
		`exec "$4" -test.run=TestAppPreviewHelperProcess -- serve "$5"`
	manifest.Commands[0] = appPreviewCommand{
		Command: "/bin/sh",
		Args: []string{
			"-c", script, "sh",
			private.companyMain, private.bound, private.otherRun, os.Args[0], strconv.Itoa(port),
		},
		Port: port,
	}
	if err := service.Register(manifest); err != nil {
		t.Fatal(err)
	}
	lease := attachTestAppPreview(t, service, manifest)
	if _, err := service.Ensure(lease.LeaseID, manifest.identity()); err != nil {
		t.Fatal(err)
	}
	waitForAppPreview(t, service, lease.LeaseID, manifest.identity(), appPreviewLifecycleRunning)
	raw, err := os.ReadFile(filepath.Join(manifest.WorkDir, "report.txt"))
	if err != nil {
		t.Fatalf("the preview command never ran: %v", err)
	}
	if got := strings.TrimSpace(string(raw)); got != "done" {
		t.Fatalf("preview command report:\n%s\nwant only \"done\"", got)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
}
