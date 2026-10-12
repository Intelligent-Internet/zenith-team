package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/runchannel"
)

const sampleTask = `{"id":"` + testRunTaskID + `","key":"ENG-7","companyId":"` + testCompanyID + `",` +
	`"milestoneType":"product",` +
	`"conversationId":"` + testRunConvID + `","number":7,"title":"Ship the thing","description":"Do it well",` +
	`"status":"in_progress","priority":"high","labels":["launch"],"startDate":null,"dueDate":null,` +
	`"assignee":{"kind":"user","id":"0198aa00-0000-7000-8000-00000000aaaa","name":"Founder"},` +
	`"executedBy":{"kind":"agent","id":"` + testRunAgentID + `","name":"Scout"},` +
	`"creator":{"kind":"user","id":"u1","name":"Founder"},"blockedByCount":0,"dependenciesResolved":true,` +
	`"createdAt":"2026-09-15T00:00:00Z","updatedAt":"2026-09-15T00:00:00Z","completedAt":null,"revision":3}`

func TestTaskListAndGetInOperatorMode(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		if r.Header.Get("Authorization") != "Bearer access-cli" || r.Header.Get("X-Client-Platform") != "cli" {
			t.Errorf("headers = %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			_, _ = w.Write([]byte(`{"items":[` + sampleTask + `]}`))
		case strings.HasSuffix(r.URL.Path, "/tasks/"+testRunTaskID),
			strings.HasSuffix(r.URL.Path, "/tasks/ENG-7"):
			_, _ = w.Write([]byte(sampleTask))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")

	out, err := executeCLI(t, newRootCmd, "task", "list", "--status", "in_progress,todo")
	if err != nil {
		t.Fatalf("task list: %v", err)
	}
	if !strings.Contains(out, "ENG-7") || !strings.Contains(out, "Ship the thing") ||
		!strings.Contains(out, "Scout") {
		t.Fatalf("task list table = %q", out)
	}
	if paths[0] != "GET /api/v1/companies/"+testCompanyID+"/tasks?status=in_progress,todo" {
		t.Fatalf("task list requested %q", paths[0])
	}
	// A key or label goes to the server as given, which looks it up; a
	// UUID goes straight to the object. Neither reads the list first.
	paths = nil
	out, err = executeCLI(t, newRootCmd, "task", "get", "ENG-7", "--output", "table")
	if err != nil || !strings.Contains(out, "dependencies resolved") || !strings.Contains(out, "Do it well") {
		t.Fatalf("task get by key = %q (err %v)", out, err)
	}
	if len(paths) != 1 || paths[0] != "GET /api/v1/companies/"+testCompanyID+"/tasks/ENG-7" {
		t.Fatalf("task get by key requested %q", paths)
	}
	out, err = executeCLI(t, newRootCmd, "task", "get", testRunTaskID)
	if err != nil {
		t.Fatalf("task get by id: %v", err)
	}
	var printed map[string]any
	if err := json.Unmarshal([]byte(out), &printed); err != nil || printed["key"] != "ENG-7" {
		t.Fatalf("task get JSON = %q", out)
	}
	if paths[len(paths)-1] != "GET /api/v1/companies/"+testCompanyID+"/tasks/"+testRunTaskID {
		t.Fatalf("task get requested %q", paths[len(paths)-1])
	}
}

// The dependency set is `{blockedBy, blocks}` with the other task embedded
// in each edge (docs/api/tasks.md); there is no `items` envelope. The table
// lists both directions and derives RESOLVED from the prerequisite's status.
func TestTaskDependenciesRendersBothDirections(t *testing.T) {
	const dependencies = `{"blockedBy":[` +
		`{"task":{"projectId":"0198aa00-0000-7000-8000-00000000c001","id":"0198aa00-0000-7000-8000-00000000f006",` +
		`"key":"ENG-6","title":"Add the cursor codec","status":"done",` +
		`"assignee":{"kind":"user","id":"0198aa00-0000-7000-8000-00000000aaaa","name":"Founder"},` +
		`"executedBy":{"kind":"agent","id":"` + testRunAgentID + `","name":"Scout"},"revision":11},` +
		`"createdAt":"2026-09-12T08:20:41Z","revision":1},` +
		`{"task":{"projectId":"0198aa00-0000-7000-8000-00000000c001","id":"0198aa00-0000-7000-8000-00000000f005",` +
		`"key":"ENG-5","title":"Write the schema","status":"backlog",` +
		`"assignee":{"kind":"user","id":"0198aa00-0000-7000-8000-00000000aaaa","name":"Founder"},` +
		`"executedBy":{"kind":"agent","id":"` + testRunAgentID + `","name":"Scout"},"revision":2},` +
		`"createdAt":"2026-09-12T08:20:41Z","revision":1}],` +
		`"blocks":[` +
		`{"task":{"projectId":"0198aa00-0000-7000-8000-00000000c001","id":"0198aa00-0000-7000-8000-00000000f017",` +
		`"key":"ENG-17","title":"Browser verification","status":"todo",` +
		`"assignee":{"kind":"user","id":"0198aa00-0000-7000-8000-00000000aaaa","name":"Founder"},` +
		`"executedBy":{"kind":"agent","id":"` + testRunAgentID + `","name":"Scout"},"revision":3},` +
		`"createdAt":"2026-09-13T08:20:41Z","revision":1}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v1/companies/"+testCompanyID+"/tasks/"+testRunTaskID+"/dependencies" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(dependencies))
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")

	out, err := executeCLI(t, newRootCmd, "task", "dependencies", testRunTaskID)
	if err != nil {
		t.Fatalf("task dependencies: %v", err)
	}
	for _, want := range []string{"DIRECTION", "blocked by", "blocks", "ENG-6", "ENG-5", "ENG-17", "Add the cursor codec"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dependencies table %q is missing %q", out, want)
		}
	}
	var doneRow, backlogRow string
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.Contains(line, "ENG-6"):
			doneRow = line
		case strings.Contains(line, "ENG-5"):
			backlogRow = line
		}
	}
	if !strings.Contains(doneRow, "true") || !strings.Contains(backlogRow, "false") {
		t.Fatalf("RESOLVED should follow the prerequisite's status: done row %q, backlog row %q",
			doneRow, backlogRow)
	}
}

func TestTaskCreateLeavesTheKeyToTheServerAndNamesBothHalvesOfTheAssignment(t *testing.T) {
	var created map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/projects"):
			_, _ = w.Write(
				[]byte(
					`{"items":[{"id":"0198aa00-0000-7000-8000-00000000c001","key":"work","title":"Work","revision":1}]}`,
				),
			)
		case r.URL.Path == "/api/v1/companies/"+testCompanyID && r.Method == http.MethodGet:
			_, _ = w.Write(
				[]byte(
					`{"id":"` + testCompanyID + `","name":"Acme","slug":"acme","taskPrefix":"ENG","role":"owner","revision":1}`,
				),
			)
		case r.URL.Path == "/api/v1/me":
			t.Error("task create asked /me; the server already defaults the assignee to the caller")
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/tasks") && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"items":[` + sampleTask + `]}`))
		case strings.HasSuffix(r.URL.Path, "/tasks") && r.Method == http.MethodPost:
			// Decoding into a live map merges keys; read each request alone.
			created = nil
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Errorf("decode create: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(strings.Replace(sampleTask, `"key":"ENG-7"`, `"key":"ENG-8"`, 1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")

	// A Task always belongs to a member and names the Agent that does the
	// work (ADR 0014). Unnamed, the member is the caller: the server's
	// default, so the CLI sends no assignee of its own.
	out, err := executeCLI(t, newRootCmd, "task", "create", "--project", "work",
		"--title", "Write the launch post", "--executed-by-agent", testRunAgentID,
		"--milestone", "launch", "--labels", "launch, copy", "--priority", "high")
	if err != nil {
		t.Fatalf("task create: %v", err)
	}
	// The server mints {prefix}-{number} from the number it allocates; the
	// CLI sends no guess of its own.
	if _, sent := created["key"]; sent || created["title"] != "Write the launch post" ||
		created["milestoneType"] != "launch" {
		t.Fatalf("create body = %v", created)
	}
	if _, sent := created["assignee"]; sent {
		t.Fatalf("assignee = %v, want it left to the server (the caller)", created["assignee"])
	}
	executedBy, _ := created["executedBy"].(map[string]any)
	if executedBy["kind"] != "agent" || executedBy["id"] != testRunAgentID {
		t.Fatalf("executedBy = %v, want the named Agent", executedBy)
	}
	if labels, _ := created["labels"].([]any); len(labels) != 2 || labels[1] != "copy" {
		t.Fatalf("labels = %v", created["labels"])
	}
	if !strings.Contains(out, `"key": "ENG-8"`) {
		t.Fatalf("create printed %q", out)
	}

	// Without an Agent the assignee does the work themselves — again the
	// server's default; an explicit key is sent as given.
	for _, self := range [][]string{nil, {"--executed-by-self"}} {
		args := append([]string{
			"task", "create", "--project", "work",
			"--title", "Sign the contract", "--milestone", "launch", "--key", "sign_contract",
		}, self...)
		if _, err := executeCLI(t, newRootCmd, args...); err != nil {
			t.Fatalf("user task create %v: %v", self, err)
		}
		_, assigneeSent := created["assignee"]
		_, executedBySent := created["executedBy"]
		if assigneeSent || executedBySent || created["key"] != "sign_contract" {
			t.Fatalf("user create body %v = %v, want the caller doing it themselves", self, created)
		}
	}

	// Handing it to a colleague names them as the assignee; the server is
	// what decides whether the caller may.
	const colleagueID = "0198aa00-0000-7000-8000-00000000bbbb"
	if _, err := executeCLI(t, newRootCmd, "task", "create", "--project", "work",
		"--title", "Draft the brief", "--milestone", "launch",
		"--assignee-user", colleagueID, "--executed-by-agent", testRunAgentID); err != nil {
		t.Fatalf("colleague task create: %v", err)
	}
	assignee, _ := created["assignee"].(map[string]any)
	executedBy, _ = created["executedBy"].(map[string]any)
	if assignee["kind"] != "user" || assignee["id"] != colleagueID || executedBy["id"] != testRunAgentID {
		t.Fatalf("colleague create body = %v", created)
	}

	// Outside a Task Run nothing names a Project for the caller.
	if _, err := executeCLI(t, newRootCmd, "task", "create",
		"--title", "Nowhere", "--milestone", "launch"); err == nil ||
		!strings.Contains(err.Error(), "--project") || exitCodeFor(err) != exitValidation {
		t.Fatalf("create without --project = %v (exit %d)", err, exitCodeFor(err))
	}

	// The two executedBy flags are mutually exclusive.
	if _, err := executeCLI(t, newRootCmd, "task", "create", "--project", "work",
		"--title", "Both", "--milestone", "launch",
		"--executed-by-agent", testRunAgentID, "--executed-by-self"); err == nil {
		t.Fatal("task create accepted both --executed-by-agent and --executed-by-self")
	}
}

// An operator names a Task as a person reads it off the board: its key, or
// the {prefix}-{number} label. The server looks the name up, as it does for
// a Run, so the CLI sends it as given and never reads the Task list first.
// A name that is no Task therefore fails with the server's own code and
// request id (exit 4). A command that acts on the Task by id reads it by
// name first and then uses the id the server answered with.
func TestTaskNamesGoToTheServerInOperatorMode(t *testing.T) {
	const notFound = `{"detail":"Task not found","code":"task_not_found","requestId":"req-404"}`
	tasks := "/api/v1/companies/" + testCompanyID + "/tasks/"
	var paths []string
	var lastURI string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		lastURI = r.RequestURI
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case tasks + "ship_the_thing", tasks + "eng-7", tasks + "déploiement_v2":
			_, _ = w.Write([]byte(sampleTask))
		case tasks + "ship_the_thing/dependencies":
			_, _ = w.Write([]byte(`{"blockedBy":[],"blocks":[]}`))
		case tasks + testRunTaskID + "/workspace":
			_, _ = w.Write([]byte(`{"task":` + sampleTask + `,"messages":[]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(notFound))
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")

	for _, test := range []struct {
		args      []string
		wantPaths []string
	}{
		{[]string{"task", "get", "ship_the_thing"}, []string{"GET " + tasks + "ship_the_thing"}},
		{
			[]string{"task", "dependencies", "ship_the_thing"},
			[]string{"GET " + tasks + "ship_the_thing/dependencies"},
		},
		{
			[]string{"task", "workspace", "eng-7"},
			[]string{"GET " + tasks + "eng-7", "GET " + tasks + testRunTaskID + "/workspace"},
		},
	} {
		paths = nil
		if _, err := executeCLI(t, newRootCmd, test.args...); err != nil {
			t.Fatalf("zen %v: %v", test.args, err)
		}
		if strings.Join(paths, "\n") != strings.Join(test.wantPaths, "\n") {
			t.Fatalf("zen %v requested %q, want %q", test.args, paths, test.wantPaths)
		}
	}

	// A name that is no Task is the server's 404, request id included.
	for _, command := range []string{"get", "dependencies", "workspace", "messages"} {
		paths = nil
		_, err := executeCLI(t, newRootCmd, "task", command, "ENG-99")
		if err == nil || exitCodeFor(err) != exitNotFound || len(paths) != 1 ||
			!strings.HasPrefix(paths[0], "GET "+tasks+"ENG-99") {
			t.Fatalf("zen task %s ENG-99 = %v (exit %d), requests %q",
				command, err, exitCodeFor(err), paths)
		}
		var printed commandErrorBody
		encoded := formatCommandError(err, false, outputJSON)
		if decodeErr := json.Unmarshal([]byte(encoded), &printed); decodeErr != nil {
			t.Fatalf("zen task %s JSON error does not decode: %v", command, decodeErr)
		}
		if got := printed.Error; got.Code != "task_not_found" || got.Status != 404 ||
			got.RequestID != "req-404" || got.ExitCode != exitNotFound ||
			!strings.HasSuffix(got.Message, "Task not found") {
			t.Fatalf("zen task %s JSON error = %+v, want the server's", command, got)
		}
	}

	// Any other key the server allows travels escaped as one path segment
	// and arrives as given: a letter like é, a space, a percent sign or a
	// query mark never lands on another route.
	paths = nil
	if _, err := executeCLI(t, newRootCmd, "task", "workspace", "déploiement_v2"); err != nil {
		t.Fatalf("zen task workspace déploiement_v2: %v", err)
	}
	want := []string{"GET " + tasks + "déploiement_v2", "GET " + tasks + testRunTaskID + "/workspace"}
	if strings.Join(paths, "\n") != strings.Join(want, "\n") {
		t.Fatalf("zen task workspace déploiement_v2 requested %q, want %q", paths, want)
	}
	for _, name := range []string{"déploiement_v2", "a b", "ENG-7%2Fworkspace", "ENG-7?x=1", "ENG-7#top"} {
		paths = nil
		_, err := executeCLI(t, newRootCmd, "task", "get", name)
		if len(paths) != 1 || paths[0] != "GET "+tasks+name ||
			lastURI != tasks+url.PathEscape(name) {
			t.Fatalf("zen task get %q requested %q (%s), want one read of that name", name, paths, lastURI)
		}
		if name != "déploiement_v2" && (err == nil || exitCodeFor(err) != exitNotFound) {
			t.Fatalf("zen task get %q = %v (exit %d), want the server's 404", name, err, exitCodeFor(err))
		}
	}

	// A slash or a dot segment would land on another route, so such a name
	// is refused before it is sent (exit 5).
	for _, name := range []string{testRunTaskID + "/workspace", "ENG-7/", "..", "."} {
		for _, command := range []string{"get", "dependencies", "workspace"} {
			paths = nil
			_, err := executeCLI(t, newRootCmd, "task", command, name)
			if err == nil || exitCodeFor(err) != exitValidation || len(paths) != 0 {
				t.Fatalf("zen task %s %q = %v (exit %d), requests %q; want a usage error",
					command, name, err, exitCodeFor(err), paths)
			}
		}
	}
}

func TestTaskGetAndFailInsideARunUseTheChannel(t *testing.T) {
	channel := newFakeRunChannel(t, func(request runchannel.Request) (int, string) {
		switch {
		case request.Method == http.MethodGet && request.Path == "/companies/"+testCompanyID+"/tasks/"+testRunTaskID:
			return 200, sampleTask
		case request.Method == http.MethodPost && request.Path == "/companies/"+testCompanyID+"/task-failures":
			return 200, `{"status":"failed","task":` + sampleTask + `}`
		}
		return 403, `{"detail":"This Run's scope does not admit it","code":"run_scope_denied"}`
	})
	seedAgentShell(t, channel, testRunTaskID, []string{"zen task get", "zen task fail"})

	// No id: the Run's own Task, with no list read.
	out, err := executeCLI(t, newRootCmd, "task", "get")
	if err != nil || !strings.Contains(out, `"key": "ENG-7"`) {
		t.Fatalf("task get in agent mode = %q (err %v)", out, err)
	}
	// The key of the own Task resolves without a list read too.
	if _, err := executeCLI(t, newRootCmd, "task", "get", "ENG-7"); err != nil {
		t.Fatalf("task get by own key: %v", err)
	}
	if len(channel.requests) != 2 || channel.requests[0].RequestKey != "" {
		t.Fatalf("channel requests = %+v, want two keyless reads", channel.requests)
	}

	out, err = executeCLI(t, newRootCmd, "task", "fail", "--reason", "The supplier discontinued the part.")
	if err != nil || !strings.Contains(out, `"status": "failed"`) {
		t.Fatalf("task fail = %q (err %v)", out, err)
	}
	failure := channel.requests[2]
	var body map[string]string
	if err := json.Unmarshal(
		failure.Body,
		&body,
	); err != nil ||
		body["reason"] != "The supplier discontinued the part." {
		t.Fatalf("failure body = %s", failure.Body)
	}
	if failure.RequestKey == "" {
		t.Fatal("a mutation over the channel carried no request key")
	}
	// The same command again converges on the same key.
	if _, err := executeCLI(
		t,
		newRootCmd,
		"task",
		"fail",
		"--reason",
		"The supplier discontinued the part.",
	); err != nil {
		t.Fatal(err)
	}
	if channel.requests[3].RequestKey != failure.RequestKey {
		t.Fatalf("retried request key %q differs from %q", channel.requests[3].RequestKey, failure.RequestKey)
	}
	// Out of scope: the daemon's refusal is an ordinary API error with the
	// authorization exit code, never a fallback to another credential.
	_, err = executeCLI(t, newRootCmd, "task", "list")
	if err == nil || !strings.Contains(err.Error(), "run_scope_denied") || exitCodeFor(err) != exitAuth {
		t.Fatalf("out-of-scope list = %v (exit %d)", err, exitCodeFor(err))
	}
	// Failing a Task is a Run's act; an operator shell cannot do it.
	seedSession(t, "http://127.0.0.1:1")
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")
	if _, err := executeCLI(t, newRootCmd, "task", "fail", "--reason", "x"); err == nil ||
		!strings.Contains(err.Error(), "only works in Agent mode") {
		t.Fatalf("task fail as operator = %v", err)
	}
}

// A Run reads only its own Tasks. Inside one, `zen task get BSC-8` cannot
// find a colleague's Task in its own list, so the CLI hands the server the
// name the Agent was given; the server refuses it with one fixed sentence,
// and the CLI prints that sentence as it stands, with no request line in
// front. It is an authorization failure (exit 3); a Task that does not exist
// stays exit 4.
func TestTaskReadsInsideARunRelayTheRefusalOfAnotherMembersTask(t *testing.T) {
	const refusal = "Permission denied. You can only access your own Tasks."
	tasks := "/companies/" + testCompanyID + "/tasks/"
	channel := newFakeRunChannel(t, func(request runchannel.Request) (int, string) {
		switch request.Path {
		case tasks + "BSC-8", tasks + "BSC-8/dependencies":
			return 403, `{"detail":"` + refusal + `","code":"task_outside_run_scope","requestId":"req-8"}`
		case tasks + "BSC-99":
			return 404, `{"detail":"Task not found","code":"task_not_found","requestId":"req-99"}`
		case tasks + "ENG-7":
			return 200, sampleTask
		}
		return 403, `{"detail":"This Run's scope does not admit it","code":"run_scope_denied"}`
	})
	// A channel Run: it has no Task of its own and reads Company Tasks.
	seedAgentShell(t, channel, "", []string{"zen task list", "zen task get <id>", "zen task dependencies <id>"})

	for _, command := range [][]string{{"task", "get", "BSC-8"}, {"task", "dependencies", "BSC-8"}} {
		channel.requests = nil
		_, err := executeCLI(t, newRootCmd, command...)
		if err == nil || exitCodeFor(err) != exitAuth {
			t.Fatalf("zen %v = %v (exit %d), want the refusal with exit %d",
				command, err, exitCodeFor(err), exitAuth)
		}
		want := "zen: " + refusal + " (code: task_outside_run_scope) [request req-8]"
		if message := formatCommandError(err, false, ""); message != want {
			t.Fatalf("zen %v printed %q, want %q", command, message, want)
		}
		// With --output json the same refusal is one object an Agent can
		// branch on: the code, the sentence, and the exit code.
		var printed commandErrorBody
		if decodeErr := json.Unmarshal(
			[]byte(formatCommandError(err, false, outputJSON)),
			&printed,
		); decodeErr != nil {
			t.Fatalf("zen %v JSON error does not decode: %v", command, decodeErr)
		}
		if want := (commandErrorDetail{
			Code: "task_outside_run_scope", Message: refusal, Status: 403, ExitCode: exitAuth, RequestID: "req-8",
		}); printed.Error != want {
			t.Fatalf("zen %v JSON error = %+v, want %+v", command, printed.Error, want)
		}
		// The name goes to the server as given: no list read first.
		if len(channel.requests) != 1 || channel.requests[0].Method != http.MethodGet {
			t.Fatalf("zen %v channel requests = %+v", command, channel.requests)
		}
	}

	// Its own Task reads by name like any other; a name that is nobody's
	// Task is not found.
	out, err := executeCLI(t, newRootCmd, "task", "get", "ENG-7")
	if err != nil || !strings.Contains(out, `"key": "ENG-7"`) {
		t.Fatalf("own Task by key = %q (err %v)", out, err)
	}
	if _, err := executeCLI(t, newRootCmd, "task", "get", "BSC-99"); err == nil ||
		!strings.Contains(err.Error(), "task_not_found") || exitCodeFor(err) != exitNotFound {
		t.Fatalf("unknown Task = %v (exit %d)", err, exitCodeFor(err))
	}
}

// A failure that never reached the server still prints one JSON object
// under --output json: no code, the CLI's own message, and its exit code.
func TestJSONCommandErrorForALocalFailure(t *testing.T) {
	var printed commandErrorBody
	if err := json.Unmarshal(
		[]byte(formatCommandError(usagef("a task id or key is required"), false, outputJSON)),
		&printed,
	); err != nil {
		t.Fatalf("JSON error does not decode: %v", err)
	}
	if want := (commandErrorDetail{Message: "a task id or key is required", ExitCode: exitValidation}); printed.Error != want {
		t.Fatalf("JSON error = %+v, want %+v", printed.Error, want)
	}
}

// In Agent mode a Task's name is sent to the server as one path segment. A
// name that cannot travel as one segment unchanged — a slash, a percent
// sign, a query or fragment mark, a dot segment, a letter a URL escapes —
// could make the request land on another route (tasks/<id>%2Fworkspace
// reads a whole Task), and the daemon refuses any percent sign, so the CLI
// refuses it as a usage error and never sends it.
func TestTaskReadsInsideARunRefuseANameThatIsNotOnePathSegment(t *testing.T) {
	channel := newFakeRunChannel(t, func(runchannel.Request) (int, string) {
		return 200, sampleTask
	})
	seedAgentShell(t, channel, "", []string{"zen task list", "zen task get <id>", "zen task dependencies <id>"})

	for _, name := range []string{
		testRunTaskID + "/workspace",
		testRunTaskID + "%2Fworkspace",
		"BSC-8?x=1",
		"BSC-8#top",
		"..",
		".",
		"a b",
		`a\b`,
		"déploiement_v2",
	} {
		for _, command := range []string{"get", "dependencies"} {
			channel.requests = nil
			_, err := executeCLI(t, newRootCmd, "task", command, name)
			if err == nil || exitCodeFor(err) != exitValidation {
				t.Fatalf(
					"zen task %s %q = %v (exit %d), want a usage error",
					command,
					name,
					err,
					exitCodeFor(err),
				)
			}
			if len(channel.requests) != 0 {
				t.Fatalf("zen task %s %q reached the channel: %+v", command, name, channel.requests)
			}
		}
	}
}

// Inside a Run, `zen task create` files work for the member the Run works
// for (ADR 0014 D6). The daemon forwards it with that member's session, so
// the server makes them the assignee and the CLI needs no /me; naming
// another assignee is refused. A Task Run's new Task lands in the Run's own
// Project, named or not, and this Run's Agent does the work unless the
// command names another or the member.
func TestTaskCreateInsideARunFilesWorkForTheRunsMember(t *testing.T) {
	const lyraID = "0198aa00-0000-7000-8000-00000000f0b2"
	projectPath := "/companies/" + testCompanyID + "/projects/" + testRunProjectID
	channel := newFakeRunChannel(t, func(request runchannel.Request) (int, string) {
		switch {
		case request.Method == http.MethodGet && request.Path == projectPath:
			return 200, `{"id":"` + testRunProjectID + `","key":"work","title":"Work","revision":1}`
		case request.Method == http.MethodPost && request.Path == "/companies/"+testCompanyID+"/tasks":
			return 201, strings.Replace(sampleTask, `"key":"ENG-7"`, `"key":"ENG-8"`, 1)
		}
		return 403, `{"detail":"This Run's scope does not admit it","code":"run_scope_denied"}`
	})
	seedAgentShell(t, channel, testRunTaskID, []string{"zen task create"})

	for _, test := range []struct {
		name           string
		args           []string
		wantExecutedBy map[string]any
	}{
		{"the Run's Project and Agent", nil, map[string]any{"kind": "agent", "id": testRunAgentID}},
		{
			"the Run's Project by key",
			[]string{"--project", "work"},
			map[string]any{"kind": "agent", "id": testRunAgentID},
		},
		{
			"another Agent",
			[]string{"--project", testRunProjectID, "--executed-by-agent", lyraID},
			map[string]any{"kind": "agent", "id": lyraID},
		},
		{"the member themself", []string{"--executed-by-self"}, nil},
	} {
		channel.requests = nil
		args := append(
			[]string{"task", "create", "--title", "Follow up", "--milestone", "launch"},
			test.args...,
		)
		out, err := executeCLI(t, newRootCmd, args...)
		if err != nil || !strings.Contains(out, `"key": "ENG-8"`) {
			t.Fatalf("%s: task create = %q (err %v)", test.name, out, err)
		}
		// The Project lookup and the create — never /me or a list read.
		if len(channel.requests) != 2 ||
			channel.requests[0].Method != http.MethodGet || channel.requests[0].Path != projectPath ||
			channel.requests[1].Method != http.MethodPost || channel.requests[1].RequestKey == "" {
			t.Fatalf("%s: channel requests = %+v", test.name, channel.requests)
		}
		var body map[string]any
		if err := json.Unmarshal(channel.requests[1].Body, &body); err != nil {
			t.Fatal(err)
		}
		if _, sent := body["assignee"]; sent || body["projectId"] != testRunProjectID {
			t.Fatalf("%s: create body = %v, want the Run's Project and no assignee", test.name, body)
		}
		executedBy, sent := body["executedBy"].(map[string]any)
		if test.wantExecutedBy == nil && sent ||
			test.wantExecutedBy != nil && (executedBy["kind"] != test.wantExecutedBy["kind"] ||
				executedBy["id"] != test.wantExecutedBy["id"]) {
			t.Fatalf("%s: executedBy = %v, want %v", test.name, body["executedBy"], test.wantExecutedBy)
		}
	}

	// The Task belongs to the member the Run works for: a Run cannot file
	// work in a colleague's name, even when that member could.
	channel.requests = nil
	_, err := executeCLI(t, newRootCmd,
		"task", "create", "--title", "Follow up", "--milestone", "launch",
		"--assignee-user", "0198aa00-0000-7000-8000-00000000f0c3")
	if err == nil || !strings.Contains(err.Error(), "--assignee-user") || exitCodeFor(err) != exitValidation {
		t.Fatalf("Run create for another member = %v (exit %d)", err, exitCodeFor(err))
	}
	if len(channel.requests) != 0 {
		t.Fatalf("a refused create reached the channel: %+v", channel.requests)
	}

	// A direct Run has no Project of its own to fall back on.
	seedAgentShell(t, channel, "", []string{"zen task create"})
	channel.requests = nil
	_, err = executeCLI(t, newRootCmd, "task", "create", "--title", "Follow up", "--milestone", "launch")
	if err == nil || !strings.Contains(err.Error(), "--project") || exitCodeFor(err) != exitValidation {
		t.Fatalf("direct Run create without --project = %v (exit %d)", err, exitCodeFor(err))
	}
	if len(channel.requests) != 0 {
		t.Fatalf("a refused create reached the channel: %+v", channel.requests)
	}
}

const testMessagesConversationID = "0198aa00-0000-7000-8000-0000000000d7"

// Messages live on the Task and the Conversation themselves, not under the
// company: the server serves /tasks/{id}/messages and
// /conversations/{id}/messages and nothing else, so the CLI must ask there.
func TestTaskAndConversationMessagesUseTheServersRoutes(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/tasks/"+testRunTaskID+"/messages" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"items":[],"nextCursor":null}`))
		case r.URL.Path == "/api/v1/tasks/"+testRunTaskID+"/messages" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(`{"disposition":"message","runId":"0198aa00-0000-7000-8000-0000000000e1"}`),
			)
		case r.URL.Path == "/api/v1/conversations/"+testMessagesConversationID+"/messages" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(
				[]byte(`{"disposition":"message","runId":"0198aa00-0000-7000-8000-0000000000e2"}`),
			)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	seedSession(t, srv.URL)
	t.Setenv("BOARDLY_RUN_ID", "")
	t.Setenv("BOARDLY_RUN_TOKEN", "")

	if _, err := executeCLI(t, newRootCmd, "task", "messages", testRunTaskID); err != nil {
		t.Fatalf("task messages: %v", err)
	}
	if _, err := executeCLI(t, newRootCmd, "task", "send", testRunTaskID, "--content", "ping"); err != nil {
		t.Fatalf("task send: %v", err)
	}
	if _, err := executeCLI(
		t,
		newRootCmd,
		"conversation",
		"send",
		testMessagesConversationID,
		"--content",
		"ping",
	); err != nil {
		t.Fatalf("conversation send: %v", err)
	}
	want := []string{
		"GET /api/v1/tasks/" + testRunTaskID + "/messages",
		"POST /api/v1/tasks/" + testRunTaskID + "/messages",
		"POST /api/v1/conversations/" + testMessagesConversationID + "/messages",
	}
	for i, path := range want {
		if i >= len(paths) || paths[i] != path {
			t.Fatalf("request %d = %v, want %q", i, paths, path)
		}
	}
}

// A Task whose assignee may not run tasks in its Project reads as not ready,
// in words that name neither the Project nor a computer (ADR 0017).
func TestTaskReadinessSaysTheAssigneeMayNotRunTasksThere(t *testing.T) {
	t.Parallel()
	var item taskView
	reason := "assignee_cannot_run"
	item.Execution.UnreadyReason = &reason
	if got, want := taskReadiness(item), "no — the assignee may not run tasks in this work"; got != want {
		t.Fatalf("readiness = %q, want %q", got, want)
	}
}

func TestTaskCreateScheduledAssignmentFromAgent(t *testing.T) {
	const recipient = "0198aa00-0000-7000-8000-00000000aaaa"
	var body map[string]any
	channel := newFakeRunChannel(t, func(request runchannel.Request) (int, string) {
		if request.Method == http.MethodGet {
			return 200, `{"id":"` + testRunProjectID + `","key":"work","title":"Work","revision":1}`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		return 201, sampleTask
	})
	seedAgentShell(t, channel, testRunTaskID, []string{"zen task create"})
	_, err := executeCLI(
		t,
		newRootCmd,
		"task",
		"create",
		"--title",
		"Scheduled report",
		"--milestone",
		"product",
		"--assignee-user",
		recipient,
		"--run-at",
		"2026-12-01T09:00:00Z",
	)
	if err != nil {
		t.Fatal(err)
	}
	trigger, ok := body["schedule"].(map[string]any)
	if !ok || trigger["type"] != "once" || trigger["runAt"] != "2026-12-01T09:00:00Z" {
		t.Fatalf("schedule = %#v", body["schedule"])
	}
}
