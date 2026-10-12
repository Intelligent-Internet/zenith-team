package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"boardly.dev/boardly/server/internal/daemon/execenv"
	"boardly.dev/boardly/server/internal/daemon/workspace"
	"boardly.dev/boardly/server/internal/runchannel"
	"boardly.dev/boardly/server/pkg/agent"
	"boardly.dev/boardly/server/pkg/protocol"
)

// Credential isolation (Access TECH B3d, B3e, B3f, B7b, D31). On macOS the
// daemon starts every process that runs Agent-written code, a Run's provider
// and an app preview's commands, inside a Seatbelt sandbox. It can neither
// read nor write a Zenith Team sign-in on this computer nor another Run's
// isolated home (its run.json opens that Run's channel). It cannot start
// launchctl, open or osascript (nor crontab or at, which are setuid, and
// macOS starts no setuid program in a sandbox), nor write the person's
// launchd agents folder or shell start-up files: the usual ways to have macOS
// start a program later, outside the sandbox. Under the workspaces root it
// writes only its own folders, so it cannot leave code there for another Run
// to run. A Run tagged in a channel or group DM also cannot open the
// workspaces root or a local folder the Company bound, apart from its own
// folders: a channel answer never draws on the tagger's local folders, bound
// folders or private Memory, which no Project access governs. Each runtime
// reports whether that holds: enforced only when this round's probe proved it
// and no launch started without the sandbox is still running, not_enforced
// otherwise. A launch is wrapped exactly when the probe passed, so a probe
// that stops passing lowers the wrapper and the report at once, and one that
// passes again raises the report only once the unwrapped launches have
// ended.

// credentialIsolationEnv is the owner-named test switch. Its one value,
// not_enforced, lowers the report and drops the wrapper (LoadConfig refuses
// any other value).
const credentialIsolationEnv = "BOARDLY_DAEMON_CREDENTIAL_ISOLATION"

// credentialIsolationVerdict is one probe round's answer for this computer.
type credentialIsolationVerdict struct {
	// Value is protocol.CredentialIsolationEnforced or NotEnforced.
	Value string
	// Reason says why it is not enforced, for the daemon log.
	Reason string
}

func credentialIsolationNotEnforced(reason string) credentialIsolationVerdict {
	return credentialIsolationVerdict{Value: protocol.CredentialIsolationNotEnforced, Reason: reason}
}

// credentialIsolation is the report every registration and heartbeat
// carries: the last round's verdict, not_enforced before the first and
// while a launch started without the sandbox is still running.
func (d *Daemon) credentialIsolation() string {
	d.isolationMu.Lock()
	defer d.isolationMu.Unlock()
	if d.unwrappedLaunches > 0 || d.isolation == nil {
		return protocol.CredentialIsolationNotEnforced
	}
	return d.isolation.Value
}

// startLaunchIsolation decides whether one launch (a Run's provider or an
// app preview's commands) starts in the sandbox: exactly when the last
// round's verdict is enforced. A launch that starts without it holds the
// report at not_enforced until done is called, after its processes have
// ended. Deciding and counting under one lock means no report can say
// enforced between the two.
func (d *Daemon) startLaunchIsolation() (sandboxed bool, done func()) {
	d.isolationMu.Lock()
	defer d.isolationMu.Unlock()
	if d.isolation != nil && d.isolation.Value == protocol.CredentialIsolationEnforced {
		return true, func() {}
	}
	return false, d.holdNotEnforcedLocked()
}

// holdNotEnforced counts one launch that starts without the sandbox, which
// holds the report at not_enforced until release is called.
func (d *Daemon) holdNotEnforced() (release func()) {
	d.isolationMu.Lock()
	defer d.isolationMu.Unlock()
	return d.holdNotEnforcedLocked()
}

func (d *Daemon) holdNotEnforcedLocked() func() {
	d.unwrappedLaunches++
	var once sync.Once
	return func() {
		once.Do(func() {
			d.isolationMu.Lock()
			d.unwrappedLaunches--
			d.isolationMu.Unlock()
		})
	}
}

// recordCredentialIsolation keeps a round's verdict and logs a change.
func (d *Daemon) recordCredentialIsolation(verdict credentialIsolationVerdict) {
	d.isolationMu.Lock()
	previous := d.isolation
	d.isolation = &verdict
	d.isolationMu.Unlock()
	if previous != nil && *previous == verdict {
		return
	}
	if verdict.Value == protocol.CredentialIsolationEnforced {
		d.logger.Info("credential isolation enforced: provider processes start in the macOS sandbox")
		return
	}
	d.logger.Warn("credential isolation not enforced: providers start without the sandbox",
		"reason", verdict.Reason)
}

// appPreviewIsolation decides the sandbox for one launch of an app preview's
// commands (Access TECH B3e). They are code an Agent wrote, so they start in
// the same sandbox as a Run's provider, with no Run home of their own, and
// hide what the Run that presented the app hid (hidden, from the preview's
// manifest; B7b). A preview that cannot be wrapped, because the computer is
// not enforced or its folders lie in a denied path, still starts, and holds
// the report at not_enforced until release, which the supervisor calls once
// every process it started has stopped.
func (d *Daemon) appPreviewIsolation(workingDirectories, hidden []string) (*agent.Sandbox, func()) {
	sandboxed, release := d.startLaunchIsolation()
	if !sandboxed {
		return nil, release
	}
	// Under the workspaces root a preview writes only its own working
	// folders (Access TECH B3f).
	sandbox, err := d.launchSandbox(execenv.SeatbeltOptions{
		RunDirs:  workingDirectories,
		Writable: workingDirectories,
		Hidden:   hidden,
	})
	if err != nil {
		d.logger.Warn("app preview starts without the sandbox", "error", err)
		return nil, d.holdNotEnforced()
	}
	return sandbox, release
}

// launchSandbox builds the sandbox one launch starts in. The launch names its
// own folders: RunHome (a Run's isolated home, empty for an app preview and
// the probe), RunDirs (the folders it works in), Writable (its own folders
// under the workspaces root), Hidden (for a tagged Run, the folders it may
// not open, runHiddenFolders) and WritableRoots (when set, a fence on every
// write). This computer adds the rest: the sign-in paths, the launch points,
// and the workspaces root and the repository cache beside it, which every
// launch reads and writes only in its own folders (Access TECH B3d, B3e,
// B3f, B7b). No Run writes the cache; the daemon's own Git does.
func (d *Daemon) launchSandbox(launch execenv.SeatbeltOptions) (*agent.Sandbox, error) {
	launch.Protected = sandboxProtectedPaths()
	launch.NoWrite = sandboxNoWritePaths()
	launch.Shared = append(launch.Shared, d.cfg.WorkspacesRoot, repositoryCacheRoot(d.cfg.WorkspacesRoot))
	return execenv.Seatbelt(launch)
}

// runHiddenFolders is what a Run may neither read nor write on this computer,
// apart from its own folders there (Access TECH B7b). A channel answer never
// draws on the tagger's local folders, bound folders or private Memory, which
// no Project access governs, so a Run tagged in a channel or group DM hides
// the workspaces root, which holds the Company main with its memory and
// every other Run's folders, the repository cache beside it, and every local
// folder the Company bound. That includes a folder bound to another daemon,
// which may run on this computer too; one that does not exist here hides
// nothing.
// Every other Run hides nothing.
func (d *Daemon) runHiddenFolders(doc RunContext) []string {
	if !isTaggedRun(doc) {
		return nil
	}
	hidden := []string{d.cfg.WorkspacesRoot, repositoryCacheRoot(d.cfg.WorkspacesRoot)}
	for _, resource := range doc.Resources {
		if resource.ResourceType == "local_directory" && filepath.IsAbs(resource.LocalPath) {
			hidden = append(hidden, filepath.Clean(resource.LocalPath))
		}
	}
	return hidden
}

// sandboxProtectedPaths is what the sandbox denies: every path that can hold
// a sign-in (protectedCredentialPaths), and local-preview.json, which holds
// the desktop's loopback capability. It is named on its own so a home that
// moves cannot drop it.
func sandboxProtectedPaths() []string {
	paths := protectedCredentialPaths()
	if home, err := BoardlyHome(); err == nil {
		if absolute, err := filepath.Abs(home); err == nil {
			paths = append(paths, filepath.Join(absolute, "local-preview.json"))
		}
	}
	return paths
}

// errSandboxDeniedRead refuses a file the daemon was asked to read for a Run
// that the Run's own sandbox denies it.
var errSandboxDeniedRead = errors.New(
	"this Run may not read this file: it holds a sign-in, another Run's channel, " +
		"or private work a Run tagged in a chat may not open",
)

// runFileGuard guards every file the daemon reads for one Run, outside the
// sandbox: a presented output, a Studio canvas, a provider's generated image.
// launch is the Run's own folders and, for a tagged Run, the folders it may
// not open, as its sandbox gets them. The guard refuses, by real path, what
// that sandbox denies the Run itself: the sign-in files, every Run's isolated
// home and, for a tagged Run, everything in its hidden folders but its own
// folders, so no Run can have the daemon carry them into a chat (Access TECH
// B3e, B7b). It holds on every computer, sandboxed or not.
func runFileGuard(launch execenv.SeatbeltOptions) func(realPath string) error {
	return func(realPath string) error {
		opts := launch
		opts.Protected = sandboxProtectedPaths()
		if execenv.SandboxDeniesRead(opts, realPath) {
			return errSandboxDeniedRead
		}
		return nil
	}
}

// openRunFile opens a file a Run's provider named, for the daemon to read
// outside the sandbox. The Run's guard (runFileGuard) sees the file's real
// path, and a path that changed between the check and the open (a link
// swapped in) is refused.
func openRunFile(path string, launch execenv.SeatbeltOptions) (*os.File, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	if err := runFileGuard(launch)(resolved); err != nil {
		return nil, err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	opened, statErr := file.Stat()
	current, lstatErr := os.Lstat(resolved)
	again, resolveErr := filepath.EvalSymlinks(path)
	if statErr != nil || lstatErr != nil || resolveErr != nil || !opened.Mode().IsRegular() ||
		!os.SameFile(opened, current) || again != resolved {
		_ = file.Close()
		return nil, errors.New("the file changed while it was opened")
	}
	return file, nil
}

// sandboxNoWritePaths is what the sandbox lets a launch read but not write:
// the person's launchd agents folder and the files zsh, bash and sh run when
// a shell starts or ends. A program placed there would start later, outside
// the sandbox. A provider's shell still reads the start-up files.
func sandboxNoWritePaths() []string {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return nil
	}
	names := []string{
		filepath.Join("Library", "LaunchAgents"),
		".zshenv", ".zprofile", ".zshrc", ".zlogin", ".zlogout",
		".profile", ".bashrc", ".bash_profile", ".bash_login", ".bash_logout",
	}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(home, name))
	}
	return paths
}

// probeCredentialIsolation decides one round's verdict. It refuses enforced
// when the switch is set, off macOS, when the workspaces root or the Run
// scratch root lies inside a denied path (the profile would deny every Run
// its work), and when the sandbox does not deny the daemon's own session
// file or another Run's run.json, lets a launch write another Run's folder
// in a stand-in workspaces root or refuses it its own there, lets a tagged
// Run read another Run's folder there or refuses it its own, or cannot start
// a process.
func (d *Daemon) probeCredentialIsolation(ctx context.Context) credentialIsolationVerdict {
	if d.cfg.CredentialIsolationOff {
		return credentialIsolationNotEnforced("turned off by " + credentialIsolationEnv)
	}
	if runtime.GOOS != "darwin" {
		return credentialIsolationNotEnforced("provider processes have no sandbox on " + runtime.GOOS)
	}
	session, err := OwnCredentialPath()
	if err != nil {
		return credentialIsolationNotEnforced(err.Error())
	}
	session, err = filepath.Abs(session)
	if err != nil {
		return credentialIsolationNotEnforced(err.Error())
	}
	// The probe needs a file the sandbox must hide; reading it here first
	// proves a failed read inside the sandbox is the sandbox's doing.
	if content, err := os.ReadFile(session); err != nil || len(content) == 0 {
		return credentialIsolationNotEnforced("the daemon cannot read its own session file to test the sandbox")
	}
	// A stand-in Run home with its run.json, which the sandbox must hide as
	// it hides every Run's home.
	otherRun, err := os.MkdirTemp("", execenv.RunHomePrefix+"probe-")
	if err != nil {
		return credentialIsolationNotEnforced("the daemon cannot make a stand-in Run home: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(otherRun) }()
	otherChannel := filepath.Join(otherRun, runchannel.ControlFileName)
	if err := os.WriteFile(otherChannel, []byte("{}\n"), 0o600); err != nil {
		return credentialIsolationNotEnforced("the daemon cannot make a stand-in Run home: " + err.Error())
	}
	// A stand-in workspaces root with a folder of the launch's own and one
	// of another Run's: the sandbox must let it write only its own (Access
	// TECH B3f).
	standIn, err := os.MkdirTemp("", "zenith-probe-workspaces-")
	if err != nil {
		return credentialIsolationNotEnforced(
			"the daemon cannot make a stand-in workspaces root: " + err.Error(),
		)
	}
	defer func() { _ = os.RemoveAll(standIn) }()
	ownFolder, otherFolder := filepath.Join(standIn, "own"), filepath.Join(standIn, "other")
	for _, folder := range []string{ownFolder, otherFolder} {
		if err := os.Mkdir(folder, 0o700); err != nil {
			return credentialIsolationNotEnforced(
				"the daemon cannot make a stand-in workspaces root: " + err.Error(),
			)
		}
	}
	otherNote := filepath.Join(otherFolder, "notes.md")
	if err := os.WriteFile(otherNote, []byte("private\n"), 0o600); err != nil {
		return credentialIsolationNotEnforced(
			"the daemon cannot make a stand-in workspaces root: " + err.Error(),
		)
	}

	scratch := filepath.Join(d.cfg.WorkspacesRoot, workspace.LocalScratchDirectoryName)
	runDirs := []string{d.cfg.WorkspacesRoot, scratch}
	sandbox, err := d.launchSandbox(execenv.SeatbeltOptions{
		RunDirs:  runDirs,
		Shared:   []string{standIn},
		Writable: []string{ownFolder},
	})
	if err != nil {
		return credentialIsolationNotEnforced(err.Error())
	}
	// The write fence a connector-only Codex launch adds must compile too.
	fenced, err := d.launchSandbox(execenv.SeatbeltOptions{RunDirs: runDirs, WritableRoots: []string{scratch}})
	if err != nil {
		return credentialIsolationNotEnforced(err.Error())
	}
	// A tagged Run's sandbox hides the stand-in root but for its own folder
	// (Access TECH B7b).
	tagged, err := d.launchSandbox(execenv.SeatbeltOptions{
		RunDirs:  []string{ownFolder},
		Shared:   []string{standIn},
		Writable: []string{ownFolder},
		Hidden:   []string{standIn},
	})
	if err != nil {
		return credentialIsolationNotEnforced(err.Error())
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if leaked, err := runInSandbox(ctx, sandbox, "/bin/cat", session); err == nil || len(leaked) != 0 {
		return credentialIsolationNotEnforced("the sandbox did not deny a read of the daemon's session file")
	}
	if leaked, err := runInSandbox(ctx, sandbox, "/bin/cat", otherChannel); err == nil || len(leaked) != 0 {
		return credentialIsolationNotEnforced("the sandbox did not deny a read of another Run's run.json")
	}
	if _, err := runInSandbox(ctx, sandbox, "/usr/bin/touch", filepath.Join(otherFolder, "planted")); err == nil {
		return credentialIsolationNotEnforced("the sandbox did not deny a write in another Run's folder")
	}
	if _, err := runInSandbox(ctx, sandbox, "/usr/bin/touch", filepath.Join(ownFolder, "written")); err != nil {
		return credentialIsolationNotEnforced(
			"the sandbox denied a write in the launch's own folder: " + err.Error(),
		)
	}
	if leaked, err := runInSandbox(ctx, tagged, "/bin/cat", otherNote); err == nil || len(leaked) != 0 {
		return credentialIsolationNotEnforced("the sandbox did not hide another Run's folder from a tagged Run")
	}
	if _, err := runInSandbox(
		ctx, tagged, "/bin/sh", "-c", `cd "$1" && echo mine > tagged && cat tagged`, "sh", ownFolder,
	); err != nil {
		return credentialIsolationNotEnforced("the sandbox denied a tagged Run its own folder: " + err.Error())
	}
	for _, profile := range []*agent.Sandbox{sandbox, fenced} {
		if _, err := runInSandbox(ctx, profile, "/usr/bin/true"); err != nil {
			return credentialIsolationNotEnforced("the sandbox could not start a process: " + err.Error())
		}
	}
	return credentialIsolationVerdict{Value: protocol.CredentialIsolationEnforced}
}

// runInSandbox runs one probe command through the same wrapper a provider
// launch uses and returns its standard output.
func runInSandbox(ctx context.Context, sandbox *agent.Sandbox, path string, args ...string) ([]byte, error) {
	name, argv := sandbox.Command(path, args)
	return exec.CommandContext(ctx, name, argv...).Output()
}
