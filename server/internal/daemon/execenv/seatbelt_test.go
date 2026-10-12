package execenv

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"boardly.dev/boardly/server/pkg/agent"
)

// paramsNaming returns the profile parameter names whose value is path.
func paramsNaming(params map[string]string, path string) []string {
	var names []string
	for name, value := range params {
		if value == path {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// The profile names every path through a parameter: a folder with
// everything under it, a file with the temporary siblings an atomic write
// leaves, and a path that does not exist yet as a folder. A path with spaces
// reaches sandbox-exec as its own argv entry, unescaped.
func TestSeatbeltDeniesEveryProtectedPathThroughParameters(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Application Support", "Zenith Team")
	home := filepath.Join(root, "boardly-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	store := filepath.Join(root, "desktop-session.v1.bin")
	if err := os.WriteFile(store, []byte("sealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "not-yet", ".boardly")

	sandbox, err := Seatbelt(SeatbeltOptions{Protected: []string{home, store, missing}})
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	if !strings.HasPrefix(sandbox.Profile, "(version 1)\n(allow default)\n(deny file-read* file-write*") {
		t.Fatalf("profile does not start by allowing and then denying reads and writes:\n%s", sandbox.Profile)
	}
	if strings.Contains(sandbox.Profile, "Application Support") {
		t.Fatalf("profile names a path inline instead of through a parameter:\n%s", sandbox.Profile)
	}
	for _, spelling := range sandboxSpellings(home) {
		for _, name := range paramsNaming(sandbox.Params, spelling) {
			if !strings.Contains(sandbox.Profile, `(subpath (param "`+name+`"))`) {
				t.Errorf("folder %s is not denied as a subpath:\n%s", spelling, sandbox.Profile)
			}
		}
		if len(paramsNaming(sandbox.Params, spelling)) == 0 {
			t.Errorf("folder %s has no parameter; params = %v", spelling, sandbox.Params)
		}
	}
	for _, spelling := range sandboxSpellings(store) {
		literal := paramsNaming(sandbox.Params, spelling)
		siblings := paramsNaming(sandbox.Params, spelling+".")
		if len(literal) != 1 || len(siblings) != 1 {
			t.Fatalf(
				"file %s params = %v / %v, want one literal and one sibling prefix",
				spelling,
				literal,
				siblings,
			)
		}
		if !strings.Contains(sandbox.Profile, `(literal (param "`+literal[0]+`"))`) ||
			!strings.Contains(sandbox.Profile, `(prefix (param "`+siblings[0]+`"))`) {
			t.Errorf("file %s is not denied with its temporary siblings:\n%s", spelling, sandbox.Profile)
		}
	}
	for _, spelling := range sandboxSpellings(missing) {
		names := paramsNaming(sandbox.Params, spelling)
		siblings := paramsNaming(sandbox.Params, spelling+".")
		if len(names) != 1 || !strings.Contains(sandbox.Profile, `(subpath (param "`+names[0]+`"))`) ||
			len(siblings) != 1 || !strings.Contains(sandbox.Profile, `(prefix (param "`+siblings[0]+`"))`) {
			t.Errorf(
				"missing path %s is not denied as a folder with its siblings:\n%s",
				spelling,
				sandbox.Profile,
			)
		}
	}
	_, parents, found := strings.Cut(sandbox.Profile, ")\n(deny file-write*\n")
	if !found {
		t.Fatalf("profile does not keep the protected paths' parents in place:\n%s", sandbox.Profile)
	}
	for _, path := range []string{home, store, missing} {
		for _, spelling := range sandboxSpellings(path) {
			for parent := filepath.Dir(spelling); ; parent = filepath.Dir(parent) {
				named := false
				for _, name := range paramsNaming(sandbox.Params, parent) {
					named = named || strings.Contains(parents, `(literal (param "`+name+`"))`)
				}
				if !named {
					t.Errorf(
						"parent %s of %s can be renamed:\n%s",
						parent,
						spelling,
						sandbox.Profile,
					)
				}
				if parent == filepath.Dir(parent) {
					break
				}
			}
		}
	}
	if strings.Contains(sandbox.Profile, "require-not") {
		t.Fatalf("profile fences writes without writable roots:\n%s", sandbox.Profile)
	}

	name, argv := sandbox.Command("/usr/local/bin/claude", []string{"--print"})
	if name != "/usr/bin/sandbox-exec" {
		t.Fatalf("launcher = %q, want /usr/bin/sandbox-exec", name)
	}
	if !slices.Contains(argv, home) && !slices.ContainsFunc(argv, func(arg string) bool {
		return strings.HasSuffix(arg, "="+home)
	}) {
		t.Fatalf("argv %q does not carry the home with spaces as one -D value", argv)
	}
}

// Seatbelt matches a file by its real path, so a path behind a symlink is
// denied under both spellings.
func TestSeatbeltNamesTheRealPathOfAProtectedPath(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real-home")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "linked-home")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	sandbox, err := Seatbelt(SeatbeltOptions{Protected: []string{link, filepath.Join(link, "later")}})
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{link, resolved, filepath.Join(resolved, "later")} {
		if len(paramsNaming(sandbox.Params, want)) == 0 {
			t.Errorf("no parameter names %s; params = %v", want, sandbox.Params)
		}
	}
}

// Seatbelt matches paths, so a process that could rename a folder above a
// protected path would carry the files out from under the rule and read
// them at the new path. On this Mac, under the real sandbox-exec, the
// parents stay in place while work inside them still goes on.
func TestSeatbeltKeepsTheParentsOfAProtectedPathInPlace(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS only")
	}
	base := t.TempDir()
	support := filepath.Join(base, "Application Support")
	app := filepath.Join(support, "Zenith Team")
	home := filepath.Join(app, "boardly-home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	session := filepath.Join(home, "daemon-auth.json")
	store := filepath.Join(app, "desktop-session.v1.bin")
	for _, path := range []string{session, store} {
		if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sandbox, err := Seatbelt(SeatbeltOptions{Protected: []string{home, store}})
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	run := func(script string) error {
		name, argv := sandbox.Command("/bin/sh", []string{"-c", script, "sh", app, support, base})
		return exec.CommandContext(t.Context(), name, argv...).Run()
	}
	for name, script := range map[string]string{
		"rename the app folder":      `mv "$1" "$3/moved"`,
		"rename Application Support": `mv "$2" "$3/moved"`,
		"rename the temp folder":     `mv "$3" "$3.moved"`,
		"read the session":           `cat "$1/boardly-home/daemon-auth.json"`,
	} {
		if err := run(script); err == nil {
			t.Errorf("%s: allowed inside the sandbox", name)
		}
	}
	work := `mkdir "$1/cache" && echo kept > "$1/cache/x" && mv "$1/cache" "$1/cache2" && ls "$2" >/dev/null`
	if err := run(work); err != nil {
		t.Fatalf("work inside the parents was refused: %v", err)
	}
	if _, err := os.Stat(session); err != nil {
		t.Fatalf("the session moved: %v", err)
	}
}

func TestSeatbeltRefusesALayoutThatWouldDenyTheRunItsWork(t *testing.T) {
	home := t.TempDir()
	// Every provider keeps its own files in the person's home folder and the
	// temporary folder, so no hidden folder may hold either (B7b).
	person := filepath.Join(t.TempDir(), "person")
	if err := os.MkdirAll(person, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", person)
	for name, options := range map[string]SeatbeltOptions{
		"relative hidden folder": {Protected: []string{home}, Hidden: []string{"workspaces"}},
		"hidden folder is the person's home": {
			Protected: []string{home},
			Hidden:    []string{person},
		},
		"hidden folder holds the person's home": {
			Protected: []string{home},
			Hidden:    []string{filepath.Dir(person)},
		},
		"hidden folder holds the temporary folder": {
			Protected: []string{home},
			Hidden:    []string{filepath.Dir(filepath.Clean(os.TempDir()))},
		},
		"run folder inside the home": {
			Protected: []string{home},
			RunDirs:   []string{filepath.Join(home, "workspaces", "run")},
		},
		"run folder is the home": {Protected: []string{home}, RunDirs: []string{home}},
		"run home inside the home": {
			Protected: []string{home},
			RunHome:   filepath.Join(home, "workspaces", RunHomePrefix+"1"),
		},
		"nothing to protect":     {RunDirs: []string{home}},
		"relative protected":     {Protected: []string{"relative/.boardly"}},
		"relative no-write path": {Protected: []string{home}, NoWrite: []string{".zshrc"}},
		"relative run home":      {Protected: []string{home}, RunHome: RunHomePrefix + "1"},
		"relative writable root": {Protected: []string{home}, WritableRoots: []string{"scratch"}},
		"relative shared folder": {Protected: []string{home}, Shared: []string{"workspaces"}},
		"relative own folder": {
			Protected: []string{home},
			Shared:    []string{filepath.Join(t.TempDir(), "workspaces")},
			Writable:  []string{"workspaces/run"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if sandbox, err := Seatbelt(options); err == nil {
				t.Fatalf("seatbelt = %+v, want a refusal", sandbox)
			}
		})
	}
	// The home inside the Run's folder is fine: the deny still holds there.
	if _, err := Seatbelt(SeatbeltOptions{
		Protected: []string{filepath.Join(home, "inner-home")},
		RunDirs:   []string{home},
	}); err != nil {
		t.Fatalf("seatbelt with the home under the Run's folder: %v", err)
	}
	// So is a hidden folder inside the person's home.
	if _, err := Seatbelt(SeatbeltOptions{
		Protected: []string{home},
		Hidden:    []string{filepath.Join(person, "boardly_workspaces")},
	}); err != nil {
		t.Fatalf("seatbelt with a hidden folder in the person's home: %v", err)
	}
}

// Every Run's isolated home is denied by its name and the Run's own is opened
// again after that rule, because in a Seatbelt profile the last rule that
// matches decides. The sign-in paths are denied after both, so the Run's own
// home can never reopen them. A launch that is no Run's opens no home.
func TestSeatbeltDeniesEveryRunHomeButTheRunsOwn(t *testing.T) {
	protected := t.TempDir()
	own := filepath.Join(t.TempDir(), RunHomePrefix+"own")
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatal(err)
	}
	sandbox, err := Seatbelt(SeatbeltOptions{Protected: []string{protected}, RunHome: own})
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	everyHome := `(deny file-read* file-write* (regex #"/\.boardly-home-[^/]*(/|$)"))`
	denyAt := strings.Index(sandbox.Profile, everyHome)
	allowAt := strings.Index(sandbox.Profile, "(allow file-read* file-write*")
	protectedAt := strings.Index(sandbox.Profile, "(deny file-read* file-write*\n")
	if denyAt < 0 || allowAt < denyAt || protectedAt < allowAt {
		t.Fatalf("profile does not deny every Run home, open the Run's own, then deny the sign-in paths:\n%s",
			sandbox.Profile)
	}
	opened := sandbox.Profile[allowAt:protectedAt]
	for _, spelling := range sandboxSpellings(own) {
		names := paramsNaming(sandbox.Params, spelling)
		if len(names) != 1 || !strings.Contains(opened, `(subpath (param "`+names[0]+`"))`) {
			t.Errorf("the Run's own home %s is not opened again:\n%s", spelling, sandbox.Profile)
		}
	}

	preview, err := Seatbelt(SeatbeltOptions{Protected: []string{protected}})
	if err != nil {
		t.Fatalf("seatbelt without a Run home: %v", err)
	}
	if !strings.Contains(preview.Profile, everyHome) || strings.Contains(preview.Profile, "(allow file-read*") {
		t.Fatalf("a launch with no Run home must open none:\n%s", preview.Profile)
	}
}

// The no-write paths are denied writes and not reads (a provider's shell
// runs the start-up files), and every program that asks macOS to start
// something later is denied by its path.
func TestSeatbeltDeniesLaunchPointsAndThePrograms(t *testing.T) {
	person := t.TempDir()
	zshrc := filepath.Join(person, ".zshrc")
	if err := os.WriteFile(zshrc, []byte("# the person's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sandbox, err := Seatbelt(SeatbeltOptions{
		Protected: []string{filepath.Join(person, ".boardly")},
		NoWrite:   []string{zshrc},
	})
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	for _, spelling := range sandboxSpellings(zshrc) {
		names := paramsNaming(sandbox.Params, spelling)
		if len(names) != 1 {
			t.Fatalf("%s params = %v, want one", spelling, names)
		}
		filter := `(literal (param "` + names[0] + `"))`
		at := strings.Index(sandbox.Profile, filter)
		rule := strings.LastIndex(sandbox.Profile[:max(at, 0)], "(deny ")
		if at < 0 || rule < 0 || !strings.HasPrefix(sandbox.Profile[rule:], "(deny file-write*\n") {
			t.Errorf("%s is not denied writes alone:\n%s", spelling, sandbox.Profile)
		}
	}
	_, programs, found := strings.Cut(sandbox.Profile, "(deny process-exec*")
	if !found {
		t.Fatalf("profile denies no program:\n%s", sandbox.Profile)
	}
	for _, program := range sandboxDeniedPrograms {
		names := paramsNaming(sandbox.Params, program)
		if len(names) != 1 || !strings.Contains(programs, `(literal (param "`+names[0]+`"))`) {
			t.Errorf("program %s is not denied:\n%s", program, sandbox.Profile)
		}
	}
}

// runHomeLayout is a workspaces root with this Run's isolated home, another
// Run's home in another task's folder, and a folder of this Run's work. Each
// home holds a run.json; the other also its Commerce file.
func runHomeLayout(t *testing.T) (own, other, work string) {
	t.Helper()
	workspaces := t.TempDir()
	own = filepath.Join(workspaces, ".local-scratch", "run-a", RunHomePrefix+"1111")
	other = filepath.Join(workspaces, "company", "task-b", RunHomePrefix+"2222")
	work = filepath.Join(workspaces, "company", "task-a", "work")
	for _, dir := range []string{own, other, work} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(own, "run.json"):            `{"capability":"own"}`,
		filepath.Join(other, "run.json"):          `{"capability":"other"}`,
		filepath.Join(other, "commerce-dev.json"): `{"capability":"other-commerce"}`,
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return own, other, work
}

// On this Mac, under the real sandbox-exec: a Run reads and writes its own
// isolated home and cannot read, list, write, rename, link or copy another
// Run's, whatever path it makes for it (a renamed parent, a symlink, a hard
// link). Each refused step works without the sandbox, so the refusal is the
// sandbox's. A launch with no home of its own (an app preview) reads no
// Run's home.
func TestSeatbeltKeepsARunOutOfAnotherRunsHome(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS only")
	}
	shell := func(sandbox *agent.Sandbox, script, own, other, work string) error {
		name, argv := sandbox.Command("/bin/sh", []string{"-c", script, "sh", own, other, work})
		return exec.CommandContext(t.Context(), name, argv...).Run()
	}
	runSandbox := func(t *testing.T, own, work string) *agent.Sandbox {
		t.Helper()
		sandbox, err := Seatbelt(SeatbeltOptions{
			Protected: []string{filepath.Join(t.TempDir(), ".boardly")},
			RunHome:   own,
			RunDirs:   []string{filepath.Dir(own), work},
		})
		if err != nil {
			t.Fatalf("seatbelt: %v", err)
		}
		return sandbox
	}

	for _, step := range []struct{ name, script string }{
		{"read its own run.json", `cat "$1/run.json"`},
		{"write and list its own home", `echo note > "$1/note" && cat "$1/note" && ls "$1"`},
		{"work outside every home", `echo x > "$3/x" && cat "$3/x"`},
	} {
		own, other, work := runHomeLayout(t)
		if err := shell(runSandbox(t, own, work), step.script, own, other, work); err != nil {
			t.Errorf("%s: refused inside the sandbox: %v", step.name, err)
		}
	}

	// moved, when set, is where the step leaves another Run's home, under
	// the work folder; every step must leave its run.json as it was.
	for _, step := range []struct{ name, script, moved string }{
		{"read another Run's run.json", `cat "$2/run.json"`, ""},
		{"read another Run's Commerce file", `cat "$2/commerce-dev.json"`, ""},
		{"list another Run's home", `ls "$2"`, ""},
		{"write another Run's run.json", `echo planted > "$2/run.json"`, ""},
		{"plant a file in another Run's home", `echo planted > "$2/planted"`, ""},
		{"rename another Run's home", `mv "$2" "$3/stolen"`, ""},
		{
			// The parent is no Run's home, so it moves; the home keeps its
			// name, and with it the rule, at the new path.
			"read it after renaming its parent",
			`mv "$(dirname "$2")" "$3/moved" && cat "$3/moved/$(basename "$2")/run.json"`,
			filepath.Join("moved", RunHomePrefix+"2222"),
		},
		{"read it through a symlink", `ln -s "$2" "$3/link" && cat "$3/link/run.json"`, ""},
		{"hard-link its run.json", `ln "$2/run.json" "$3/hard" && cat "$3/hard"`, ""},
		{"copy its run.json", `cp "$2/run.json" "$3/copy" && cat "$3/copy"`, ""},
	} {
		own, other, work := runHomeLayout(t)
		if err := shell(nil, step.script, own, other, work); err != nil {
			t.Fatalf("%s: fails even without the sandbox: %v", step.name, err)
		}
		own, other, work = runHomeLayout(t)
		if err := shell(runSandbox(t, own, work), step.script, own, other, work); err == nil {
			t.Errorf("%s: allowed inside the sandbox", step.name)
		}
		home := other
		if step.moved != "" {
			home = filepath.Join(work, step.moved)
		}
		content, err := os.ReadFile(filepath.Join(home, "run.json"))
		if err != nil || string(content) != `{"capability":"other"}` {
			t.Errorf("%s: another Run's run.json is now %q, %v", step.name, content, err)
		}
	}

	own, other, work := runHomeLayout(t)
	preview, err := Seatbelt(SeatbeltOptions{
		Protected: []string{filepath.Join(t.TempDir(), ".boardly")},
		RunDirs:   []string{work},
	})
	if err != nil {
		t.Fatalf("seatbelt without a Run home: %v", err)
	}
	if err := shell(preview, `cat "$1/run.json" || cat "$2/run.json"`, own, other, work); err == nil {
		t.Error("a launch with no Run home read a Run's run.json")
	}
}

// launchPointLayout is a person's home with a launchd agents folder, a
// .zshrc, a .zprofile that is a symlink into a dotfiles folder, and no
// .bashrc yet. It returns the home and the no-write paths in it.
func launchPointLayout(t *testing.T) (string, []string) {
	t.Helper()
	person := t.TempDir()
	agents := filepath.Join(person, "Library", "LaunchAgents")
	dotfiles := filepath.Join(person, "dotfiles")
	for _, dir := range []string{agents, dotfiles} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(person, ".zshrc"):     "# the person's zshrc\n",
		filepath.Join(dotfiles, "zprofile"): "# the person's zprofile\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dotfiles, "zprofile"), filepath.Join(person, ".zprofile")); err != nil {
		t.Fatal(err)
	}
	noWrite := []string{agents}
	for _, name := range []string{".zshrc", ".zprofile", ".bashrc"} {
		noWrite = append(noWrite, filepath.Join(person, name))
	}
	return person, noWrite
}

// On this Mac, under the real sandbox-exec: a launch cannot start the
// programs that ask macOS to start something later, cannot write the
// person's launchd agents folder or shell start-up files (nor the real file
// behind a symlinked one, nor move the folder above them), and still reads
// the start-up files and works elsewhere in the home.
func TestSeatbeltStopsALaunchFromStartingProgramsLater(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS only")
	}
	launchSandbox := func(t *testing.T, person string, noWrite []string) *agent.Sandbox {
		t.Helper()
		sandbox, err := Seatbelt(SeatbeltOptions{
			Protected: []string{filepath.Join(person, ".boardly")},
			NoWrite:   noWrite,
		})
		if err != nil {
			t.Fatalf("seatbelt: %v", err)
		}
		return sandbox
	}
	shell := func(sandbox *agent.Sandbox, script, person string) ([]byte, error) {
		name, argv := sandbox.Command("/bin/sh", []string{"-c", script, "sh", person})
		return exec.CommandContext(t.Context(), name, argv...).CombinedOutput()
	}

	// The shell answers 126 and "Operation not permitted" only when it
	// could not start the program at all; a program that ran and failed
	// answers otherwise. None of them runs here, so none needs running
	// outside the sandbox either. crontab and atq have no rule of their
	// own: they are setuid, and macOS starts no setuid program inside a
	// Seatbelt. They stay here so a change in that is seen.
	for _, start := range []string{
		"/bin/launchctl version",
		"/usr/bin/open -h",
		"/usr/bin/osascript -e 'return 1'",
		"/usr/bin/crontab -l",
		"/usr/bin/atq",
	} {
		person, noWrite := launchPointLayout(t)
		output, err := shell(launchSandbox(t, person, noWrite), start+"; exit $?", person)
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 126 ||
			!strings.Contains(string(output), "Operation not permitted") {
			t.Errorf("%s inside the sandbox = %v %q, want the start refused", start, err, output)
		}
	}

	for _, step := range []struct{ name, script string }{
		{"add a launchd agent", `echo plist > "$1/Library/LaunchAgents/dev.zenith.test.plist"`},
		{"remove the launchd agents folder", `rmdir "$1/Library/LaunchAgents"`},
		{"move the folder above it", `mv "$1/Library" "$1/Library-moved"`},
		{"append to .zshrc", `echo planted >> "$1/.zshrc"`},
		{"replace .zshrc", `echo planted > "$1/zshrc.next" && mv "$1/zshrc.next" "$1/.zshrc"`},
		{"create .bashrc", `echo planted > "$1/.bashrc"`},
		{"write the file behind .zprofile", `echo planted >> "$1/dotfiles/zprofile"`},
	} {
		outside, _ := launchPointLayout(t)
		if _, err := shell(nil, step.script, outside); err != nil {
			t.Fatalf("%s: fails even without the sandbox: %v", step.name, err)
		}
		person, noWrite := launchPointLayout(t)
		if output, err := shell(launchSandbox(t, person, noWrite), step.script, person); err == nil {
			t.Errorf("%s: allowed inside the sandbox (%q)", step.name, output)
		}
		if content, err := os.ReadFile(filepath.Join(person, ".zshrc")); err != nil ||
			strings.Contains(string(content), "planted") {
			t.Errorf("%s: .zshrc is now %q, %v", step.name, content, err)
		}
	}

	person, noWrite := launchPointLayout(t)
	work := `cat "$1/.zshrc" "$1/.zprofile" >/dev/null && echo x > "$1/notes.txt" && ` +
		`mkdir -p "$1/Library/Caches/app" && echo x > "$1/Library/Caches/app/x" && /usr/bin/true`
	if output, err := shell(launchSandbox(t, person, noWrite), work, person); err != nil {
		t.Fatalf("reading the start-up files and working in the home was refused: %v %q", err, output)
	}
}

// The daemon reads some files for a Run outside the sandbox, and asks
// SandboxDeniesRead first. It must deny at least what the profile denies:
// every Run's home wherever it lies, a protected folder and everything in
// it, a protected file and its temporary siblings, a protected path that
// does not exist yet, under the real path as well; and for a launch with
// hidden folders (a tagged Run, Access TECH B7b) everything in them but the
// launch's own folders, a link put in place of an own folder included. On
// macOS each answer is checked against the real sandbox-exec: whatever a
// launch cannot read, the daemon must not read for it.
func TestSandboxDeniesReadCoversWhatTheProfileDenies(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	protectedDir := filepath.Join(root, "home")
	protectedFile := filepath.Join(root, "store", "session.bin")
	notYet := filepath.Join(root, "later", "session.bin")
	workspaces := filepath.Join(root, "ws")
	own := filepath.Join(workspaces, "run-a", "workdir")
	memory := filepath.Join(workspaces, "company", "main", ".memory", "team.md")
	bound := filepath.Join(root, "bound")
	for path, content := range map[string]string{
		filepath.Join(protectedDir, "daemon-auth.json"): "session",
		protectedFile:                                                             "store",
		protectedFile + ".123.tmp":                                                "copy",
		filepath.Join(root, "work", "notes.txt"):                                  "notes",
		filepath.Join(root, "work", "session.bin.txt"):                            "notes",
		filepath.Join(workspaces, "run-b", RunHomePrefix+"2222", "run.json"):      "capability",
		filepath.Join(workspaces, "run-b", RunHomePrefix+"2222", "deep", "x.txt"): "capability",
		filepath.Join(workspaces, "run-b", "boardly-home-2222.txt"):               "notes",
		filepath.Join(own, "answer.md"):                                           "mine",
		memory:                                                                    "private",
		filepath.Join(bound, "secret.md"):                                         "private",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(notYet), 0o700); err != nil {
		t.Fatal(err)
	}
	// An own folder an earlier launch replaced with a link to the Company
	// main opens nothing.
	swapped := filepath.Join(workspaces, "run-c")
	if err := os.Symlink(filepath.Join(workspaces, "company", "main"), swapped); err != nil {
		t.Fatal(err)
	}
	protected := []string{protectedDir, protectedFile, notYet}
	tagged := SeatbeltOptions{
		Protected: protected,
		RunDirs:   []string{filepath.Join(root, "work"), own, swapped},
		Writable:  []string{own, swapped},
		Hidden:    []string{workspaces, bound},
	}
	for path, want := range map[string]bool{
		filepath.Join(protectedDir, "daemon-auth.json"): true,
		protectedDir:                    true,
		protectedFile:                   true,
		protectedFile + ".123.tmp":      true,
		notYet:                          true,
		filepath.Join(notYet, "inside"): true,
		filepath.Join(workspaces, "run-b", RunHomePrefix+"2222", "run.json"):      true,
		filepath.Join(workspaces, "run-b", RunHomePrefix+"2222", "deep", "x.txt"): true,
		filepath.Join(workspaces, "run-b", RunHomePrefix+"2222"):                  true,
		filepath.Join(workspaces, "run-b", "boardly-home-2222.txt"):               true,
		filepath.Join(root, "work", "notes.txt"):                                  false,
		filepath.Join(root, "work", "session.bin.txt"):                            false,
		filepath.Join(own, "answer.md"):                                           false,
		own:                                                                       false,
		memory:                                                                    true,
		filepath.Join(swapped, ".memory", "team.md"):                              true,
		filepath.Join(bound, "secret.md"):                                         true,
		bound:                                                                     true,
		workspaces:                                                                true,
		filepath.Dir(own):                                                         true,
	} {
		if got := SandboxDeniesRead(tagged, path); got != want {
			t.Errorf("SandboxDeniesRead(tagged, %s) = %t, want %t", path, got, want)
		}
	}
	// A launch with no hidden folder reads them, as before.
	untagged := SeatbeltOptions{Protected: protected, RunDirs: tagged.RunDirs, Writable: tagged.Writable}
	for _, path := range []string{memory, filepath.Join(bound, "secret.md"), filepath.Join(workspaces, "run-b", "boardly-home-2222.txt")} {
		if SandboxDeniesRead(untagged, path) {
			t.Errorf("SandboxDeniesRead(untagged, %s) = true, want false", path)
		}
	}

	if runtime.GOOS != "darwin" {
		return
	}
	// The daemon may say no where the sandbox says yes, never the reverse.
	for name, opts := range map[string]SeatbeltOptions{"tagged": tagged, "untagged": untagged} {
		sandbox, err := Seatbelt(opts)
		if err != nil {
			t.Fatalf("seatbelt (%s): %v", name, err)
		}
		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || !entry.Type().IsRegular() {
				return err
			}
			command, argv := sandbox.Command("/bin/cat", []string{path})
			if exec.CommandContext(t.Context(), command, argv...).Run() != nil &&
				!SandboxDeniesRead(opts, path) {
				t.Errorf("the %s sandbox denies %s but the daemon would read it for a Run", name, path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSeatbeltFencesWritesToTheWritableRoots(t *testing.T) {
	home := t.TempDir()
	scratch := t.TempDir()
	sandbox, err := Seatbelt(SeatbeltOptions{
		Protected:     []string{home},
		RunDirs:       []string{scratch},
		WritableRoots: []string{scratch},
	})
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	_, fence, found := strings.Cut(sandbox.Profile, "(deny file-write*\n")
	if !found {
		t.Fatalf("profile has no write fence:\n%s", sandbox.Profile)
	}
	if !strings.Contains(fence, `(require-not (subpath "/dev"))`) {
		t.Fatalf("write fence does not keep /dev writable:\n%s", fence)
	}
	for _, spelling := range sandboxSpellings(scratch) {
		names := paramsNaming(sandbox.Params, spelling)
		if len(names) != 1 || !strings.Contains(fence, `(require-not (subpath (param "`+names[0]+`")))`) {
			t.Errorf("write fence does not let the Run write %s:\n%s", spelling, fence)
		}
	}
}

// Under the workspaces root a launch writes only its own folders (Access
// TECH B3f). The profile closes every shared folder to writes first and
// opens the launch's own folders there after that, before the Run-home and
// sign-in rules, which therefore still win inside them; the folders above a
// shared folder cannot be renamed. A folder outside the shared folders needs
// no rule. One that holds a shared folder, or a link put in place of an own
// folder, gets none, so it stays read-only.
func TestSeatbeltOpensOnlyTheLaunchsOwnFoldersInTheSharedFolders(t *testing.T) {
	base := t.TempDir()
	workspaces := filepath.Join(base, "workspaces")
	envRoot := filepath.Join(workspaces, "company", "task-a")
	workDir := filepath.Join(envRoot, "workdir")
	other := filepath.Join(workspaces, "company", "task-b")
	notYet := filepath.Join(workspaces, ".tools", "accounts", "agent-a")
	swapped := filepath.Join(workspaces, "company", "task-c")
	outside := t.TempDir()
	for _, dir := range []string{workDir, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(other, swapped); err != nil {
		t.Fatal(err)
	}
	sandbox, err := Seatbelt(SeatbeltOptions{
		Protected: []string{filepath.Join(base, ".boardly")},
		RunHome:   filepath.Join(envRoot, RunHomePrefix+"1"),
		RunDirs:   []string{envRoot, workDir},
		Shared:    []string{workspaces},
		Writable:  []string{envRoot, workDir, notYet, swapped, outside, base, workspaces},
	})
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	profile := sandbox.Profile
	if !strings.HasPrefix(profile, "(version 1)\n(allow default)\n(deny file-write*\n") {
		t.Fatalf("profile does not close the shared folders to writes first:\n%s", profile)
	}
	openAt := strings.Index(profile, "(allow file-write*\n")
	homesAt := strings.Index(profile, `(deny file-read* file-write* (regex`)
	if openAt < 0 || homesAt < openAt {
		t.Fatalf("profile does not open the launch's own folders before the Run-home rules:\n%s", profile)
	}
	closed, opened := profile[:openAt], profile[openAt:homesAt]
	for _, spelling := range sandboxSpellings(workspaces) {
		named := false
		for _, name := range paramsNaming(sandbox.Params, spelling) {
			named = named || strings.Contains(closed, `(subpath (param "`+name+`"))`)
		}
		if !named {
			t.Errorf("shared folder %s is not closed to writes:\n%s", spelling, profile)
		}
		for parent := filepath.Dir(spelling); ; parent = filepath.Dir(parent) {
			kept := false
			for _, name := range paramsNaming(sandbox.Params, parent) {
				kept = kept || strings.Contains(profile[homesAt:], `(literal (param "`+name+`"))`)
			}
			if !kept {
				t.Errorf("parent %s of the shared folder can be renamed:\n%s", parent, profile)
			}
			if parent == filepath.Dir(parent) {
				break
			}
		}
	}
	own := map[string]bool{}
	for _, folder := range []string{envRoot, workDir, notYet} {
		for _, spelling := range sandboxSpellings(folder) {
			own[spelling] = true
			named := false
			for _, name := range paramsNaming(sandbox.Params, spelling) {
				named = named || strings.Contains(opened, `(subpath (param "`+name+`"))`)
			}
			if !named {
				t.Errorf("own folder %s is not opened to writes:\n%s", spelling, profile)
			}
		}
	}
	for name, value := range sandbox.Params {
		if strings.Contains(opened, `(param "`+name+`")`) && !own[value] {
			t.Errorf(
				"the profile opens %s to writes, which is no folder of the launch's own:\n%s",
				value,
				profile,
			)
		}
	}

	// A launch with no shared folder keeps B3e's profile: no wall.
	plain, err := Seatbelt(
		SeatbeltOptions{Protected: []string{filepath.Join(base, ".boardly")}, Writable: []string{envRoot}},
	)
	if err != nil {
		t.Fatalf("seatbelt without a shared folder: %v", err)
	}
	if strings.Contains(plain.Profile, "(allow file-write*") ||
		!strings.HasPrefix(plain.Profile, "(version 1)\n(allow default)\n(deny file-read* file-write*") {
		t.Fatalf("a launch with no shared folder got a write wall:\n%s", plain.Profile)
	}
}

// otherRunsFolders is a workspaces root with a folder of the launch's own,
// another Run's folder holding a note and a program, and a link put in place
// of a folder of the launch's own that names the other Run's folder. The
// launch's own folder also holds a Run home and a sign-in folder, which stay
// closed inside it.
type otherRunsFolders struct {
	workspaces, own, other, swapped, outside string
}

// args are the folders as the scripts get them: $1 the launch's own
// folder, $2 another Run's folder, $3 the workspaces root, $4 a folder
// outside it, $5 the link in place of a folder of the launch's own.
func (f otherRunsFolders) args() []string {
	return []string{f.own, f.other, f.workspaces, f.outside, f.swapped}
}

// untouched are the files no refused step may change, with their content.
func (f otherRunsFolders) untouched() map[string]string {
	return map[string]string{
		filepath.Join(f.other, "notes.md"):                            "theirs\n",
		filepath.Join(f.other, ".boardly-bin-1", "zen"):               "#!/bin/sh\n",
		filepath.Join(f.workspaces, "company", "README.md"):           "shared\n",
		filepath.Join(f.own, "nested", RunHomePrefix+"9", "run.json"): "capability\n",
		filepath.Join(f.own, "sign-in", "daemon-auth.json"):           "secret\n",
	}
}

// otherRunsLayout makes a fresh otherRunsFolders.
func otherRunsLayout(t *testing.T) otherRunsFolders {
	t.Helper()
	workspaces := filepath.Join(t.TempDir(), "workspaces")
	f := otherRunsFolders{
		workspaces: workspaces,
		own:        filepath.Join(workspaces, ".local-scratch", "run-a"),
		other:      filepath.Join(workspaces, "company", "projects", "p1", "worktrees", "task-b", "workdir"),
		swapped:    filepath.Join(workspaces, ".local-scratch", "run-c"),
		outside:    t.TempDir(),
	}
	for path, content := range f.untouched() {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Dir(f.other), f.swapped); err != nil {
		t.Fatal(err)
	}
	return f
}

// On this Mac, under the real sandbox-exec: a launch writes its own folder in
// the workspaces root and nothing else there (Access TECH B3f). It cannot
// create, change, remove or rename anything in another Run's folder: not
// through a link it makes, not by renaming the workspaces root or a folder
// above it, and not through a link that an earlier launch put in place of a
// folder of its own. It still reads everything there and writes outside the
// workspaces root, and inside its own folder another Run's home and a
// sign-in folder stay closed. Each refused step works without the sandbox.
func TestSeatbeltKeepsALaunchOutOfOtherRunsFolders(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS only")
	}
	shell := func(sandbox *agent.Sandbox, script string, f otherRunsFolders) error {
		name, argv := sandbox.Command("/bin/sh", append([]string{"-c", script, "sh"}, f.args()...))
		return exec.CommandContext(t.Context(), name, argv...).Run()
	}
	launch := func(t *testing.T, f otherRunsFolders) *agent.Sandbox {
		t.Helper()
		sandbox, err := Seatbelt(SeatbeltOptions{
			Protected: []string{filepath.Join(t.TempDir(), ".boardly"), filepath.Join(f.own, "sign-in")},
			RunDirs:   []string{f.own},
			Shared:    []string{f.workspaces},
			Writable:  []string{f.own, f.swapped},
		})
		if err != nil {
			t.Fatalf("seatbelt: %v", err)
		}
		return sandbox
	}

	for _, step := range []struct{ name, script string }{
		{"work in its own folder", `echo mine > "$1/mine.txt" && mkdir "$1/sub" && mv "$1/sub" "$1/sub2" && rm -r "$1/sub2"`},
		{"read another Run's folder", `cat "$2/notes.md" "$3/company/README.md" >/dev/null && ls "$2" >/dev/null`},
		{"write outside the workspaces root", `echo x > "$4/outside.txt"`},
	} {
		f := otherRunsLayout(t)
		if err := shell(launch(t, f), step.script, f); err != nil {
			t.Errorf("%s: refused inside the sandbox: %v", step.name, err)
		}
	}

	for _, step := range []struct{ name, script string }{
		{"write a Run home inside its own folder", `echo planted > "$1/nested/.boardly-home-9/run.json"`},
		{"read a sign-in file inside its own folder", `cat "$1/sign-in/daemon-auth.json"`},
		{"create a file in another Run's folder", `echo planted > "$2/planted.md"`},
		{"change another Run's note", `echo planted >> "$2/notes.md"`},
		{"replace another Run's program", `echo planted > "$1/zen" && mv "$1/zen" "$2/.boardly-bin-1/zen"`},
		{"remove another Run's note", `rm "$2/notes.md"`},
		{"take another Run's folder", `mv "$2" "$1/stolen"`},
		{"write through a link it makes", `ln -s "$2" "$1/link" && echo planted > "$1/link/planted.md"`},
		{"write through a hard link it makes", `ln "$2/notes.md" "$1/hard" && echo planted >> "$1/hard"`},
		{"write through a link in place of its folder", `echo planted > "$5/workdir/planted.md"`},
		{"write a shared file", `echo planted >> "$3/company/README.md"`},
		{"create a folder beside the Runs", `mkdir "$3/company/task-c"`},
		{"rename the workspaces root", `mv "$3" "$3.moved" && echo planted > "$3.moved/company/README.md"`},
		{
			"rename the folder above the workspaces root",
			`mv "$(dirname "$3")" "$(dirname "$3").moved" && echo planted > "$(dirname "$3").moved/workspaces/company/README.md"`,
		},
	} {
		if err := shell(nil, step.script, otherRunsLayout(t)); err != nil {
			t.Fatalf("%s: fails even without the sandbox: %v", step.name, err)
		}
		f := otherRunsLayout(t)
		if err := shell(launch(t, f), step.script, f); err == nil {
			t.Errorf("%s: allowed inside the sandbox", step.name)
		}
		for path, want := range f.untouched() {
			if content, err := os.ReadFile(path); err != nil || string(content) != want {
				t.Errorf("%s: %s is now %q, %v", step.name, path, content, err)
			}
		}
		if _, err := os.Stat(filepath.Join(f.other, "planted.md")); !os.IsNotExist(err) {
			t.Errorf("%s: a file was planted in another Run's folder (%v)", step.name, err)
		}
	}
}

// taggedLayout is what a Run tagged in a chat finds on its person's computer
// (Access TECH B7b): a workspaces root holding its own scratch (a workdir and
// an isolated home beside it, as the desktop places them), the Company main
// with its Memory and its business context folder, another Run's workdir and
// a reviewed Tool installation; the repository cache beside the root; a
// local folder the Company bound, outside it; a folder outside all of them;
// and a link in place of a folder of the launch's own that names the Company
// main.
type taggedLayout struct {
	workspaces, envRoot, workDir, runHome, companyMain, businessContext string
	otherRun, bound, cache, tool, outside, swapped                      string
}

// args are the folders as the scripts get them: $1 its workdir, $2 the
// workspaces root, $3 the Company main, $4 another Run's workdir, $5 the bound
// folder, $6 the repository cache, $7 the Tool installation, $8 the business
// context folder, $9 its isolated home, ${10} a folder outside them all and
// ${11} the link in place of a folder of its own.
func (l taggedLayout) args() []string {
	return []string{
		l.workDir, l.workspaces, l.companyMain, l.otherRun, l.bound, l.cache,
		l.tool, l.businessContext, l.runHome, l.outside, l.swapped,
	}
}

// untouched are the files no refused step may change, with their content.
func (l taggedLayout) untouched() map[string]string {
	return map[string]string{
		filepath.Join(l.companyMain, ".memory", "team.md"):      "private memory\n",
		filepath.Join(l.companyMain, "notes.md"):                "private notes\n",
		filepath.Join(l.businessContext, "business_context.md"): "# Business\n",
		filepath.Join(l.otherRun, "notes.md"):                   "theirs\n",
		filepath.Join(l.bound, "secret.md"):                     "bound secret\n",
		filepath.Join(l.cache, "repo", "HEAD"):                  "ref: refs/heads/main\n",
		filepath.Join(l.tool, "bin", "demo"):                    "#!/bin/sh\necho tool ran\n",
	}
}

// hidden are the folders a tagged Run hides in this layout: the root, the
// cache and the bound folder.
func (l taggedLayout) hidden() []string {
	return []string{l.workspaces, l.cache, l.bound}
}

// launch is the sandbox a Run gets in this layout with the given hidden
// folders: its own folders are its env root and workdir (and the link in
// their place), and it reads the business context folder and the Tool.
func (l taggedLayout) launch(t *testing.T, hidden []string) *agent.Sandbox {
	t.Helper()
	opts := SeatbeltOptions{
		Protected: []string{filepath.Join(filepath.Dir(l.workspaces), ".boardly")},
		RunHome:   l.runHome,
		RunDirs:   []string{l.envRoot, l.workDir, l.businessContext, l.tool, l.swapped},
		Shared:    []string{l.workspaces},
		Writable:  []string{l.envRoot, l.workDir, l.swapped},
		Hidden:    hidden,
	}
	sandbox, err := Seatbelt(opts)
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	return sandbox
}

// newTaggedLayout makes a fresh taggedLayout.
func newTaggedLayout(t *testing.T) taggedLayout {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspaces := filepath.Join(base, "boardly_workspaces")
	envRoot := filepath.Join(workspaces, "0198aa00-0000-7000-8000-0000000000c6")
	companyMain := filepath.Join(workspaces, "company", "main")
	l := taggedLayout{
		workspaces:      workspaces,
		envRoot:         envRoot,
		workDir:         filepath.Join(envRoot, "workdir"),
		runHome:         filepath.Join(envRoot, RunHomePrefix+"1111"),
		companyMain:     companyMain,
		businessContext: filepath.Join(companyMain, "assets", "general"),
		otherRun:        filepath.Join(workspaces, "0198aa00-0000-7000-8000-0000000000c7", "workdir"),
		bound:           filepath.Join(base, "projects", "acme"),
		cache:           workspaces + "-repos",
		tool:            filepath.Join(workspaces, ".tools", "demo", "1.0.0"),
		outside:         filepath.Join(base, "outside"),
		swapped:         filepath.Join(workspaces, "0198aa00-0000-7000-8000-0000000000c8"),
	}
	for _, dir := range []string{l.workDir, l.runHome, l.outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range l.untouched() {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(l.runHome, "run.json"), []byte("own capability\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(companyMain, l.swapped); err != nil {
		t.Fatal(err)
	}
	// A compiled program in another Run's folder: this test binary, which
	// macOS starts outside the sandbox (a copy of a system program it would
	// kill). A hard link is enough and costs nothing.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Link(executable, filepath.Join(l.otherRun, "program")); err != nil {
		content, readErr := os.ReadFile(executable)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(l.otherRun, "program"), content, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

// A tagged Run's launch hides the folders its answer must not draw on (Access
// TECH B7b). The profile denies reads, writes and starting programs in every
// hidden folder first, lets the folders on the way to the launch's own
// folders answer a lookup, opens its own folders for reading and starting
// programs and its writable ones for writing, and only then applies the B3f write wall, the Run-home rule and
// the sign-in paths, which therefore still win inside them. A link put in
// place of an own folder, and a folder outside every hidden one, get no rule.
// The folders above a hidden folder cannot be renamed. A launch with no
// hidden folder keeps the B3f profile.
func TestSeatbeltHidesTheHiddenFoldersButTheLaunchsOwn(t *testing.T) {
	l := newTaggedLayout(t)
	sandbox := l.launch(t, l.hidden())
	profile := sandbox.Profile
	hide := "(version 1)\n(allow default)\n(deny file-read* file-write* process-exec*\n"
	if !strings.HasPrefix(profile, hide) {
		t.Fatalf("profile does not hide the hidden folders first:\n%s", profile)
	}
	metadataAt := strings.Index(profile, "(allow file-read-metadata\n")
	readAt := strings.Index(profile, "(allow file-read* process-exec*\n")
	writeAt := strings.Index(profile, "(allow file-write*\n")
	wallAt := strings.Index(profile, ")\n(deny file-write*\n")
	homesAt := strings.Index(profile, `(deny file-read* file-write* (regex`)
	if metadataAt < 0 || readAt < metadataAt || writeAt < readAt || wallAt < writeAt || homesAt < wallAt {
		t.Fatalf("profile rules are out of order (hide, look up, read, write, wall, homes):\n%s", profile)
	}
	named := func(rule string) map[string]bool {
		_, body, _ := strings.Cut(profile, rule)
		body, _, _ = strings.Cut(body, ")\n(")
		folders := map[string]bool{}
		for name, value := range sandbox.Params {
			if strings.Contains(body, `(param "`+name+`")`) {
				folders[value] = true
			}
		}
		return folders
	}
	want := func(rule string, folders ...string) {
		t.Helper()
		got := named(rule)
		if len(got) != len(folders) {
			t.Errorf("%s names %v, want %v:\n%s", strings.TrimSpace(rule), got, folders, profile)
		}
		for _, folder := range folders {
			if !got[folder] {
				t.Errorf("%s does not name %s:\n%s", strings.TrimSpace(rule), folder, profile)
			}
		}
	}
	want(hide, l.workspaces, l.cache, l.bound)
	want(
		"(allow file-read-metadata\n",
		l.workspaces, l.envRoot, filepath.Join(l.workspaces, "company"), l.companyMain,
		filepath.Dir(l.businessContext), filepath.Join(l.workspaces, ".tools"),
		filepath.Join(l.workspaces, ".tools", "demo"),
	)
	want("(allow file-read* process-exec*\n", l.envRoot, l.workDir, l.runHome, l.businessContext, l.tool)
	want("(allow file-write*\n", l.envRoot, l.workDir)
	for parent := filepath.Dir(l.bound); ; parent = filepath.Dir(parent) {
		kept := false
		for _, name := range paramsNaming(sandbox.Params, parent) {
			kept = kept || strings.Contains(profile[homesAt:], `(literal (param "`+name+`"))`)
		}
		if !kept {
			t.Errorf("parent %s of the bound folder can be renamed:\n%s", parent, profile)
		}
		if parent == filepath.Dir(parent) {
			break
		}
	}

	plain := l.launch(t, nil)
	if strings.Contains(plain.Profile, "file-read-metadata") ||
		!strings.HasPrefix(plain.Profile, "(version 1)\n(allow default)\n(deny file-write*\n") {
		t.Fatalf("a launch with no hidden folder got a hidden rule:\n%s", plain.Profile)
	}
}

// A hidden folder may be named through a link (a workspaces root under a
// linked folder, say) while the launch names its own folder there by its
// real path, as an app preview's registration does (Access TECH B7b). The own
// folder still opens, the daemon still reads it for the launch, and the rest
// of the hidden folder stays closed. On macOS the launch works there under the
// real sandbox-exec.
func TestSeatbeltOpensAnOwnFolderSpelledByItsRealPath(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	realRoot := filepath.Join(base, "real")
	own := filepath.Join(realRoot, "run", "workdir")
	other := filepath.Join(realRoot, "other", "notes.md")
	for _, dir := range []string{own, filepath.Dir(other)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(other, []byte("theirs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(base, "linked")
	if err := os.Symlink(realRoot, linked); err != nil {
		t.Fatal(err)
	}
	opts := SeatbeltOptions{
		Protected: []string{filepath.Join(base, ".boardly")},
		RunDirs:   []string{own},
		Shared:    []string{linked},
		Writable:  []string{own},
		Hidden:    []string{linked},
	}
	sandbox, err := Seatbelt(opts)
	if err != nil {
		t.Fatalf("seatbelt: %v", err)
	}
	_, seen, _ := strings.Cut(sandbox.Profile, "(allow file-read* process-exec*\n")
	seen, _, _ = strings.Cut(seen, ")\n(")
	opened := false
	for _, name := range paramsNaming(sandbox.Params, own) {
		opened = opened || strings.Contains(seen, `(param "`+name+`")`)
	}
	if !opened {
		t.Fatalf("the launch's own folder %s is not opened:\n%s", own, sandbox.Profile)
	}
	if SandboxDeniesRead(opts, filepath.Join(own, "page.html")) || !SandboxDeniesRead(opts, other) {
		t.Fatalf("the guard does not follow the profile for a folder spelled by its real path")
	}
	if runtime.GOOS != "darwin" {
		return
	}
	name, argv := sandbox.Command("/bin/sh", []string{
		"-c", `cd "$1" && echo mine > mine.txt && cat mine.txt && ! cat "$2"`, "sh", own, other,
	})
	if output, err := exec.CommandContext(t.Context(), name, argv...).CombinedOutput(); err != nil {
		t.Fatalf("the launch could not work in its own folder, or read another: %v %q", err, output)
	}
}

// On this Mac, under the real sandbox-exec: a tagged Run's launch works in its
// own folders, reads its isolated home, the business context folder and the
// reviewed Tool it starts, and writes outside the workspaces root; it cannot
// read, list, write, link, copy, rename or start anything else under the
// workspaces root (the Company main, its Memory, another Run's folder), in the
// repository cache or in a bound folder, also not through a link it makes,
// not by walking up from its own folder, not through a link in place of a
// folder of its own, and not by renaming a folder above them. Each refused
// step works without the sandbox, and a launch with no hidden folder (a
// direct chat's or a Task's Run) still reads them (Access TECH B7b).
func TestSeatbeltKeepsATaggedLaunchOutOfItsHiddenFolders(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sandbox-exec is macOS only")
	}
	shell := func(sandbox *agent.Sandbox, script string, l taggedLayout) ([]byte, error) {
		name, argv := sandbox.Command("/bin/sh", append([]string{"-c", script, "sh"}, l.args()...))
		return exec.CommandContext(t.Context(), name, argv...).CombinedOutput()
	}

	allowed := []struct{ name, script string }{
		{
			"work in its own folder",
			`cd "$1" && echo mine > mine.txt && cat mine.txt && ls >/dev/null && mkdir sub && mv sub sub2 && ` +
				`[ "$(pwd -P)" = "$1" ] && ls .. >/dev/null`,
		},
		{"read and write its own home", `echo note > "$9/note" && cat "$9/run.json" "$9/note" >/dev/null`},
		{"read the business context", `cat "$8/business_context.md" && ls "$8" >/dev/null`},
		{"start the reviewed Tool", `[ "$("$7/bin/demo")" = "tool ran" ]`},
		{"write outside the workspaces root", `echo x > "${10}/x" && cat "${10}/x"`},
		{"write the temporary folder", `f=$(mktemp) && echo x > "$f" && rm "$f"`},
	}
	if _, err := exec.LookPath("git"); err == nil {
		allowed = append(allowed, struct{ name, script string }{
			"keep a Git repository in its own folder",
			`cd "$1" && git init -q && echo x > x && git add x && ` +
				`git -c user.name=a -c user.email=a@a commit -q -m x && git status --short`,
		})
	}
	if node, err := exec.LookPath("node"); err == nil {
		allowed = append(allowed, struct{ name, script string }{
			"start a reviewed Node Tool",
			`[ "$(` + shellWord(node) + ` "$7/lib/node_modules/demo/index.js")" = "demo ran" ]`,
		})
	}
	for _, step := range allowed {
		l := newTaggedLayout(t)
		writeNodeTool(t, l.tool)
		if output, err := shell(l.launch(t, l.hidden()), step.script, l); err != nil {
			t.Errorf("%s: refused inside the sandbox: %v %q", step.name, err, output)
		}
	}

	for _, step := range []struct{ name, script string }{
		{"read the Company Memory", `cat "$3/.memory/team.md"`},
		{"list the Company main", `ls "$3"`},
		{"list the workspaces root", `ls "$2"`},
		{"read another Run's folder", `cat "$4/notes.md"`},
		{"start a program in another Run's folder", `"$4/program" -test.run='^$' >/dev/null`},
		{"read a bound folder", `cat "$5/secret.md"`},
		{"list a bound folder", `ls "$5"`},
		{"read the repository cache", `cat "$6/repo/HEAD"`},
		{"write in the Company main", `echo planted > "$3/planted.md"`},
		{"change the business context", `echo planted >> "$8/business_context.md"`},
		{"change the Tool", `echo planted >> "$7/bin/demo"`},
		{"read through a symlink it makes", `ln -s "$3/.memory" "$1/memory" && cat "$1/memory/team.md"`},
		{"hard-link a Memory file", `ln "$3/.memory/team.md" "$1/hard" && cat "$1/hard"`},
		{"copy the Company Memory", `cp -R "$3/.memory" "$1/copy" && cat "$1/copy/team.md"`},
		{"walk up from its own folder", `cd "$1" && cat ../../company/main/.memory/team.md`},
		{"read through a link in place of its folder", `cat "${11}/.memory/team.md"`},
		{
			"rename the workspaces root",
			`mv "$2" "$2.moved" && cat "$2.moved/company/main/.memory/team.md"`,
		},
		{
			"rename the folder above the bound folder",
			`mv "$(dirname "$5")" "$(dirname "$5").moved" && cat "$(dirname "$5").moved/acme/secret.md"`,
		},
	} {
		if output, err := shell(nil, step.script, newTaggedLayout(t)); err != nil {
			t.Fatalf("%s: fails even without the sandbox: %v %q", step.name, err, output)
		}
		l := newTaggedLayout(t)
		if output, err := shell(l.launch(t, l.hidden()), step.script, l); err == nil {
			t.Errorf("%s: allowed inside the sandbox (%q)", step.name, output)
		}
		for path, want := range l.untouched() {
			if content, err := os.ReadFile(path); err != nil || string(content) != want {
				t.Errorf("%s: %s is now %q, %v", step.name, path, content, err)
			}
		}
		if _, err := os.Lstat(filepath.Join(l.companyMain, "planted.md")); !os.IsNotExist(err) {
			t.Errorf("%s: a file was planted in the Company main (%v)", step.name, err)
		}
	}

	l := newTaggedLayout(t)
	read := `cat "$3/.memory/team.md" "$4/notes.md" "$5/secret.md" "$6/repo/HEAD" >/dev/null && ls "$2" "$3" >/dev/null`
	if output, err := shell(l.launch(t, nil), read, l); err != nil {
		t.Fatalf("a launch with no hidden folder cannot read them: %v %q", err, output)
	}

	// A bound folder may hold the workspaces root. The launch still enters
	// its own folder through both, and opens nothing else in either.
	l = newTaggedLayout(t)
	nested := append(l.hidden(), filepath.Dir(l.workspaces))
	work := `cd "$1" && [ "$(pwd -P)" = "$1" ] && echo x > y && cat y && cat "$9/run.json" >/dev/null`
	if output, err := shell(l.launch(t, nested), work, l); err != nil {
		t.Errorf("work in its own folder inside nested hidden folders was refused: %v %q", err, output)
	}
	for _, script := range []string{`cat "$3/.memory/team.md"`, `ls "$(dirname "$2")"`, `cat "${10}/../projects/acme/secret.md"`} {
		if output, err := shell(l.launch(t, nested), script, l); err == nil {
			t.Errorf("%s: allowed inside nested hidden folders (%q)", script, output)
		}
	}
}

// shellWord writes value as one single-quoted POSIX shell word.
func shellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// writeNodeTool installs a stand-in Node package under the Tool's prefix, as
// npm lays one out, whose entry point loads a module of its own, its
// package.json and a dependency installed beside it, so Node resolves
// modules inside the hidden root.
func writeNodeTool(t *testing.T, prefix string) {
	t.Helper()
	modules := filepath.Join(prefix, "lib", "node_modules")
	for path, content := range map[string]string{
		filepath.Join("demo", "package.json"): `{"name":"demo","version":"1.0.0","main":"index.js"}`,
		filepath.Join("demo", "index.js"): "const lib = require('./lib');\nconst { name } = require('./package.json');\n" +
			"console.log(name + ' ' + require('dep')(lib.word));\n",
		filepath.Join("demo", "lib.js"):      "module.exports = { word: 'ran' };\n",
		filepath.Join("dep", "package.json"): `{"name":"dep","version":"1.0.0","main":"index.js"}`,
		filepath.Join("dep", "index.js"):     "module.exports = (word) => word;\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(modules, path)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(modules, path), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A Run in the daemon's sandbox tells each provider not to start its own
// Seatbelt, which cannot start inside it: Codex runs danger-full-access and
// Claude's run settings turn its sandbox off. A connector-only Codex Run
// also gets the writable roots that stand in for its read-only mode.
func TestPrepareInTheSandboxTurnsTheProvidersOwnSandboxOff(t *testing.T) {
	shared := withFakeCodexHome(t)
	for _, connectorOnly := range []bool{false, true} {
		envRoot := t.TempDir()
		workDir := filepath.Join(envRoot, "workdir")
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatal(err)
		}
		env, err := Prepare(Options{
			EnvRoot:       envRoot,
			WorkDir:       workDir,
			Provider:      "codex",
			ConnectorOnly: connectorOnly,
			Sandboxed:     true,
		}, testLogger())
		if err != nil {
			t.Fatalf("prepare codex (connector-only %t): %v", connectorOnly, err)
		}
		config, err := os.ReadFile(filepath.Join(env.CodexHome, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(config), "danger-full-access") ||
			strings.Contains(string(config), "read-only") {
			t.Fatalf("sandboxed codex config (connector-only %t):\n%s", connectorOnly, config)
		}
		wantRoots := []string(nil)
		if connectorOnly {
			wantRoots = []string{envRoot, shared}
		}
		if !slices.Equal(env.SandboxWritableRoots, wantRoots) {
			t.Fatalf("writable roots (connector-only %t) = %v, want %v",
				connectorOnly, env.SandboxWritableRoots, wantRoots)
		}
	}

	envRoot := t.TempDir()
	workDir := filepath.Join(envRoot, "workdir")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	env, err := Prepare(
		Options{EnvRoot: envRoot, WorkDir: workDir, Provider: "claude", Sandboxed: true},
		testLogger(),
	)
	if err != nil {
		t.Fatalf("prepare claude: %v", err)
	}
	if env.ClaudeSettingsPath == "" {
		t.Fatal("a sandboxed Claude Run has no run settings to turn its own sandbox off")
	}
	raw, err := os.ReadFile(env.ClaudeSettingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Sandbox struct {
			Enabled *bool `json:"enabled"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Sandbox.Enabled == nil || *settings.Sandbox.Enabled {
		t.Fatalf("claude settings = %s, want sandbox.enabled false", raw)
	}

	// Outside the sandbox nothing changes: no settings file is written for
	// a Run with nothing to deny.
	plain, err := Prepare(Options{EnvRoot: t.TempDir(), WorkDir: workDir, Provider: "claude"}, testLogger())
	if err != nil {
		t.Fatalf("prepare claude outside the sandbox: %v", err)
	}
	if plain.ClaudeSettingsPath != "" {
		t.Fatalf("unsandboxed Claude Run got settings %s", plain.ClaudeSettingsPath)
	}
}
