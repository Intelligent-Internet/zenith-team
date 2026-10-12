package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPendingToolLoginDoesNotBlockStatus(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name           string
		inflightStatus bool
	}{
		{"status queued during login", false},
		{"status already in flight", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testToolLoginStatus(t, test.inflightStatus)
		})
	}
}

func testToolLoginStatus(t *testing.T, inflightStatus bool) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable")
	}
	loginStarted := make(chan struct{})
	statusStarted := make(chan struct{})
	var signalStatus sync.Once
	releaseStatus := make(chan struct{})
	statusChecked := make(chan string, 2)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			close(loginStarted)
			<-r.Context().Done()
			return
		}
		companyID := strings.Split(filepath.ToSlash(r.URL.Query().Get("home")), "/companies/")[1][:36]
		if companyID == "22222222-2222-4222-8222-222222222222" && inflightStatus {
			signalStatus.Do(func() { close(statusStarted) })
			select {
			case <-releaseStatus:
			case <-r.Context().Done():
				return
			}
		} else {
			statusChecked <- companyID
		}
		_, _ = w.Write([]byte(`{"ok":true,"data":{"authenticated":false}}`))
	}))
	defer remote.Close()
	root := t.TempDir()
	entry := filepath.Join(root, "cli.cjs")
	script := fmt.Sprintf(
		`fetch(%q + '/' + process.argv.at(-1) + '?home=' + encodeURIComponent(process.env.ZENITH_STUDIO_CLI_HOME)).then(r=>r.text()).then(body=>console.log(body));`,
		remote.URL,
	)
	if err := os.WriteFile(entry, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newLocalToolService(root)
	studio, _ := findToolRelease("zenith-studio")
	service.installed[studio.ID()] = toolInstallation{Node: node, Entrypoint: entry}
	service.authorizeLogin = func(_ context.Context, _ toolAuthScope) bool { return true }
	stop := service.startJobWorkers(t.Context())
	defer stop()
	schedule := func(companyID, action string) {
		t.Helper()
		if err := service.schedule(
			toolJob{CompanyID: companyID, AgentID: testToolAgentID, Key: studio.Key, Action: action},
		); err != nil {
			t.Fatal(err)
		}
	}
	if inflightStatus {
		schedule("22222222-2222-4222-8222-222222222222", "status")
		select {
		case <-statusStarted:
		case <-time.After(10 * time.Second):
			t.Fatal("status did not start")
		}
	}
	schedule("22222222-2222-4222-8222-222222222222", "connect")
	select {
	case <-loginStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("login did not start")
	}
	if inflightStatus {
		close(releaseStatus)
	} else {
		schedule("22222222-2222-4222-8222-222222222222", "status")
	}
	schedule("33333333-3333-4333-8333-333333333333", "status")
	select {
	case companyID := <-statusChecked:
		if companyID != "33333333-3333-4333-8333-333333333333" {
			t.Fatalf("status probed %s while its login was pending", companyID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("status waited for another Company's browser login")
	}
	if got := service.observations("22222222-2222-4222-8222-222222222222", testToolAgentID)[0].AuthState; got != "connecting" {
		t.Fatalf("auth state = %s, want connecting while browser login is pending", got)
	}
	service.cancelLogin(toolAuthScope{"22222222-2222-4222-8222-222222222222", testToolAgentID}, studio.Key)
}

// A successful Desktop login must be the account used by the same Agent's provider Runs,
// including when the daemon inherits an unrelated ambient Studio home.
func TestConnectedStudioAccountIsUsedByRun(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	root := t.TempDir()
	entry := filepath.Join(root, "cli.cjs")
	script := `const fs = require('node:fs');
 const path = require('node:path');
 const home = process.env.ZENITH_STUDIO_CLI_HOME;
 const marker = path.join(home, 'test-connected');
 const args = process.argv.slice(2);
 if (args.includes('login')) fs.writeFileSync(marker, 'connected');
 console.log(JSON.stringify({ok:true,data:{authenticated:fs.existsSync(marker),home,api:process.env.ZENITH_STUDIO_CLI_API_URL}}));`
	if err := os.WriteFile(entry, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newLocalToolService(root)
	service.authorizeLogin = func(context.Context, toolAuthScope) bool { return true }
	for _, release := range reviewedToolReleases() {
		service.installed[release.ID()] = toolInstallation{Node: node, Entrypoint: entry}
		service.setState(release, "installed", "")
	}
	stop := service.startJobWorkers(t.Context())
	defer stop()
	companyID := uuid.NewString()
	if err := service.schedule(
		toolJob{CompanyID: companyID, AgentID: testToolAgentID, Key: "zenith-studio", Action: "connect"},
	); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for service.observations(companyID, testToolAgentID)[0].AuthState != "authenticated" {
		select {
		case <-deadline:
			t.Fatal("login did not complete")
		case <-service.changed:
		}
	}
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			d := &Daemon{tools: service}
			run := Run{
				ID:        uuid.New(),
				CompanyID: companyID,
				AgentID:   testToolAgentID,
				AuthToken: testChannelRunToken,
			}
			environment, err := d.prepareStudioEnvironment(
				t.Context(),
				studioRunInput{Enabled: true, EnvRoot: t.TempDir(), Run: run},
			)
			if err != nil {
				t.Fatal(err)
			}
			output, err := runToolProcess(t.Context(), toolProcess{
				Executable: filepath.Join(
					environment.BinDir,
					"zenith-studio",
				),
				Args: []string{"--json", "auth", "status"},
				Env: map[string]string{
					"ZENITH_STUDIO_CLI_HOME":    t.TempDir(),
					"ZENITH_STUDIO_CLI_API_URL": "https://wrong.invalid",
				},
			})
			var result struct {
				Data struct {
					Authenticated bool
					Home          string
					API           string
				}
			}
			if err != nil || json.Unmarshal(output, &result) != nil || !result.Data.Authenticated ||
				result.Data.Home != mustToolAuthHome(
					t,
					service,
					toolAuthScope{companyID, testToolAgentID},
					studioRelease(),
				) ||
				result.Data.API != "https://api-agent.ii.inc" {
				t.Fatalf("wrong Run account: %s, %v", output, err)
			}
			if _, err := runToolProcess(
				t.Context(),
				toolProcess{
					Executable: filepath.Join(environment.BinDir, "zenith-studio"),
					Args:       []string{"--api-url=https://wrong.invalid", "auth", "status"},
				},
			); err == nil {
				t.Fatal("Run changed managed API origin")
			}
		})
	}
	d := &Daemon{tools: service}
	_, err = d.prepareStudioEnvironment(
		t.Context(),
		studioRunInput{
			Enabled: true,
			EnvRoot: t.TempDir(),
			Run: Run{
				ID:        uuid.New(),
				CompanyID: uuid.NewString(),
				AgentID:   testToolAgentID,
				AuthToken: testChannelRunToken,
			},
		},
	)
	if err != nil {
		t.Fatalf("missing auth blocked unrelated work during preparation: %v", err)
	}
}

func TestStudioLoginWithMissingInstallationDoesNotStayConnecting(t *testing.T) {
	t.Parallel()
	service := newLocalToolService(t.TempDir())
	stop := service.startJobWorkers(t.Context())
	defer stop()
	if err := service.schedule(
		toolJob{
			CompanyID: "22222222-2222-4222-8222-222222222222",
			AgentID:   testToolAgentID,
			Key:       "zenith-studio",
			Action:    "connect",
		},
	); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for service.observations("22222222-2222-4222-8222-222222222222", testToolAgentID)[0].AuthState != "error" {
		select {
		case <-deadline:
			t.Fatal("missing CLI left sign-in pending")
		case <-service.changed:
		}
	}
}

func TestStudioLoginSharesOneLoginSlot(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	for _, action := range []string{"connect"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			entry := filepath.Join(root, "cli.cjs")
			script := `const fs = require('node:fs'); const path = require('node:path'); const args = process.argv.slice(2); if(args.includes('login')) fs.writeFileSync(path.join(process.env.ZENITH_STUDIO_CLI_HOME,'consent'), String(args.includes('login'))); console.log(JSON.stringify({ok:true,data:{authenticated:true}}));`
			if err := os.WriteFile(entry, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			service := newLocalToolService(root)
			service.authorizeLogin = func(context.Context, toolAuthScope) bool { return true }
			release, _ := findToolRelease("zenith-studio")
			service.installed[release.ID()] = toolInstallation{Node: node, Entrypoint: entry}
			service.setState(release, "installed", "")
			service.started = true
			company := uuid.NewString()
			if err := service.schedule(
				toolJob{CompanyID: company, AgentID: testToolAgentID, Key: release.Key, Action: action},
			); err != nil {
				t.Fatal(err)
			}
			if err := service.schedule(
				toolJob{
					CompanyID: company,
					AgentID:   testToolAgentID,
					Key:       release.Key,
					Action:    "connect",
				},
			); err == nil {
				t.Fatal("overlapping login accepted")
			}
			stop := service.startJobWorkers(t.Context())
			defer stop()
			deadline := time.After(10 * time.Second)
			for service.observations(company, testToolAgentID)[0].AuthState != "authenticated" {
				select {
				case <-deadline:
					t.Fatal("login did not complete")
				case <-service.changed:
				}
			}
			got, err := os.ReadFile(
				filepath.Join(
					mustToolAuthHome(t, service, toolAuthScope{company, testToolAgentID}, release),
					"consent",
				),
			)
			want := "true"
			if err != nil || string(got) != want {
				t.Fatalf("consent=%s err=%v", got, err)
			}
		})
	}
}

const testToolAgentID = "44444444-4444-4444-8444-444444444444"

func studioRelease() toolRelease { release, _ := findToolRelease("zenith-studio"); return release }

func mustToolAuthHome(t *testing.T, service *localToolService, scope toolAuthScope, release toolRelease) string {
	t.Helper()
	home, err := service.authHome(scope, release)
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestToolAuthHomesIsolateCompanyAgentToolAndOrigin(t *testing.T) {
	t.Parallel()
	service := newLocalToolService(t.TempDir())
	scope := toolAuthScope{uuid.NewString(), uuid.NewString()}
	release := studioRelease()
	home := mustToolAuthHome(t, service, scope, release)
	for _, other := range []toolAuthScope{{scope.CompanyID, uuid.NewString()}, {uuid.NewString(), scope.AgentID}} {
		if mustToolAuthHome(t, service, other, release) == home {
			t.Fatal("profiles shared credentials")
		}
	}
	future := release
	future.Key, future.HomeEnv = "future-tool", "FUTURE_TOOL_HOME"
	if mustToolAuthHome(t, service, scope, future) == home {
		t.Fatal("future tool shared Studio credentials")
	}
	futureHome := mustToolAuthHome(t, service, scope, future)
	future.APIURL = "https://another.example"
	if mustToolAuthHome(t, service, scope, future) == futureHome {
		t.Fatal("origins shared credentials")
	}
	for _, invalid := range []toolAuthScope{{scope.CompanyID, ""}, {"", scope.AgentID}, {scope.CompanyID, "../other"}} {
		if _, err := service.authHome(invalid, release); err == nil {
			t.Fatal("accepted unscoped authentication")
		}
	}
}

func TestToolLoginCancellationAndObservationAreAgentScoped(t *testing.T) {
	t.Parallel()
	service := newLocalToolService(t.TempDir())
	service.started = true
	company := uuid.NewString()
	a, b := uuid.NewString(), uuid.NewString()
	for _, agent := range []string{a, b} {
		if err := service.schedule(
			toolJob{CompanyID: company, AgentID: agent, Key: "zenith-studio", Action: "connect"},
		); err != nil {
			// The browser queue is bounded; consume the first job without completing it.
			t.Fatal(err)
		}
		<-service.loginJobs
	}
	service.cancelLogin(toolAuthScope{company, a}, "zenith-studio")
	if service.observations(company, a)[0].AuthState != "unknown" ||
		service.observations(company, b)[0].AuthState != "connecting" {
		t.Fatal("cancel affected another Agent")
	}
	if service.observations(company, "")[0].AuthState != "unknown" {
		t.Fatal("shared runtime advertised an Agent account")
	}
}

func TestRunDoesNotInheritAnotherAgentOrLegacyStudioAccount(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	service := newLocalToolService(t.TempDir())
	release := studioRelease()
	entry := filepath.Join(service.root, "auth.cjs")
	script := `const fs=require('node:fs'),path=require('node:path');const home=process.env.ZENITH_STUDIO_CLI_HOME;const result={ok:true,data:{authenticated:fs.existsSync(path.join(home,'account')),home}};fs.writeFileSync(process.env.PROFILE_RECORD,JSON.stringify(result));console.log(JSON.stringify(result));`
	if err = os.WriteFile(entry, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, installedRelease := range reviewedToolReleases() {
		service.installed[installedRelease.ID()] = toolInstallation{Node: node, Entrypoint: entry}
	}
	owner := toolAuthScope{uuid.NewString(), uuid.NewString()}
	home := mustToolAuthHome(t, service, owner, release)
	legacy := filepath.Join(service.root, "accounts", owner.CompanyID, release.Key)
	for _, dir := range []string{home, legacy} {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "account"), []byte("connected"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		name      string
		scope     toolAuthScope
		connected bool
	}{
		{"same Agent next Run", owner, true},
		{"other Agent", toolAuthScope{owner.CompanyID, uuid.NewString()}, false},
		{"other Company", toolAuthScope{uuid.NewString(), owner.AgentID}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := &Daemon{tools: service}
			env, err := d.prepareStudioEnvironment(
				t.Context(),
				studioRunInput{
					Enabled: true,
					EnvRoot: t.TempDir(),
					Run: Run{
						ID:        uuid.New(),
						CompanyID: test.scope.CompanyID,
						AgentID:   test.scope.AgentID,
						AuthToken: testChannelRunToken,
					},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			record := filepath.Join(t.TempDir(), "probe.json")
			output, err := runToolProcess(
				t.Context(),
				toolProcess{
					Executable: filepath.Join(env.BinDir, "zenith-studio"),
					Args:       []string{"--json", "auth", "status"},
					Env: map[string]string{
						"ZENITH_STUDIO_CLI_HOME": legacy,
						"PROFILE_RECORD":         record,
					},
				},
			)
			var result struct {
				Data struct {
					Authenticated bool
					Home          string
				}
			}
			observed, readErr := os.ReadFile(record)
			if err != nil || readErr != nil ||
				json.Unmarshal(observed, &result) != nil ||
				result.Data.Authenticated != test.connected ||
				result.Data.Home != mustToolAuthHome(t, service, test.scope, release) {
				t.Fatalf("Run account = %s, %v", output, err)
			}
		})
	}
	if _, err = os.Stat(filepath.Join(legacy, "account")); err != nil {
		t.Fatal("legacy credentials were deleted")
	}
}

func TestLocalToolStatusDoesNotRequireAnAuthScope(t *testing.T) {
	t.Parallel()
	service := newLocalToolService(t.TempDir())
	release, _ := findToolRelease("hyperframes")
	service.setState(release, "installed", "")
	// An unavailable installation must be re-observed even with no account scope.
	service.installed[release.ID()] = toolInstallation{Node: "missing-node", Entrypoint: "missing-entry"}
	stop := service.startJobWorkers(t.Context())
	defer stop()
	if err := service.schedule(toolJob{Key: release.Key, Action: "status"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for service.observations("", "")[1].State != "error" {
		select {
		case <-service.changed:
		case <-deadline:
			t.Fatal("local status was not checked")
		}
	}
}

func TestStudioAuthStatusVerifiesRemoteAccess(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	for _, test := range []struct {
		name          string
		authenticated bool
		remoteExit    int
		want          string
	}{
		{name: "signed out", want: "needs_auth"},
		{name: "valid session", authenticated: true, want: "authenticated"},
		{name: "revoked session", authenticated: true, remoteExit: 3, want: "needs_auth"},
		{name: "API unavailable", authenticated: true, remoteExit: 5, want: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := newLocalToolService(t.TempDir())
			release := studioRelease()
			entry := filepath.Join(service.root, "auth.cjs")
			script := fmt.Sprintf(
				`if(process.argv.includes("auth")){console.log(JSON.stringify({ok:true,data:{authenticated:%t}}));}else{process.exit(%d);}`,
				test.authenticated,
				test.remoteExit,
			)
			if err := os.WriteFile(entry, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			service.installed[release.ID()] = toolInstallation{Node: node, Entrypoint: entry}
			scope := toolAuthScope{uuid.NewString(), uuid.NewString()}
			service.probeAuth(
				t.Context(),
				toolJob{
					CompanyID: scope.CompanyID,
					AgentID:   scope.AgentID,
					Key:       release.Key,
					Action:    "status",
				},
				release,
			)
			if got := service.observations(scope.CompanyID, scope.AgentID)[0].AuthState; got != test.want {
				t.Fatalf("remote authentication state = %q, want %q", got, test.want)
			}
		})
	}
}
