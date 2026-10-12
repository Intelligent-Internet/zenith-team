package runchannel

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const (
	testCapability = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	testRunID      = "0198aa00-0000-7000-8000-0000000000b0"
	testCompanyID  = "0198aa00-0000-7000-8000-0000000000c0"
	testAgentID    = "0198aa00-0000-7000-8000-0000000000a0"
	testChatID     = "0198aa00-0000-7000-8000-0000000000d0"
)

func testControl(endpoint string) RunControlConfig {
	return RunControlConfig{
		SchemaVersion: 1, Endpoint: endpoint, Capability: testCapability,
		RunID: testRunID, CompanyID: testCompanyID, AgentID: testAgentID,
		ConversationID: testChatID, ConversationKind: "direct",
		Commands: []string{"zen run get"},
	}
}

func writeControl(t *testing.T, home string, document any, mode os.FileMode) {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ControlFileName)
	if err := os.WriteFile(path, encoded, mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile keeps an existing file's mode.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRunControlConfigReadsOnlyAPrivateCanonicalRunJSON(t *testing.T) {
	t.Parallel()
	if _, err := LoadRunControlConfig(""); !IsRunControlUnavailable(err) {
		t.Fatalf("no home = %v, want run control unavailable", err)
	}
	if _, err := LoadRunControlConfig(t.TempDir()); !IsRunControlUnavailable(err) {
		t.Fatalf("an operator home = %v, want run control unavailable", err)
	}

	home := t.TempDir()
	writeControl(t, home, testControl("http://127.0.0.1:19713"), 0o600)
	got, err := LoadRunControlConfig(home)
	if err != nil {
		t.Fatalf("LoadRunControlConfig() error = %v", err)
	}
	if got.RunID != testRunID || got.Capability != testCapability || got.ConversationKind != "direct" {
		t.Fatalf("LoadRunControlConfig() = %+v", got)
	}

	for name, tamper := range map[string]func(*RunControlConfig){
		"a remote endpoint":      func(c *RunControlConfig) { c.Endpoint = "http://example.com:19713" },
		"a short capability":     func(c *RunControlConfig) { c.Capability = "c2hvcnQ" },
		"a Run id that is a key": func(c *RunControlConfig) { c.RunID = "ENG-7" },
		"an unknown chat kind":   func(c *RunControlConfig) { c.ConversationKind = "company" },
		"another schema":         func(c *RunControlConfig) { c.SchemaVersion = 2 },
	} {
		control := testControl("http://127.0.0.1:19713")
		tamper(&control)
		writeControl(t, home, control, 0o600)
		if _, err := LoadRunControlConfig(home); err == nil || IsRunControlUnavailable(err) {
			t.Fatalf("%s was accepted or read as an operator home: %v", name, err)
		}
	}
	writeControl(t, home, map[string]any{"schemaVersion": 1, "bearer": "person-token"}, 0o600)
	if _, err := LoadRunControlConfig(home); err == nil {
		t.Fatal("run.json with an unknown field was accepted")
	}
	if runtime.GOOS != "windows" {
		writeControl(t, home, testControl("http://127.0.0.1:19713"), 0o644)
		if _, err := LoadRunControlConfig(home); err == nil {
			t.Fatal("a run.json other users can read was accepted")
		}
	}
}

func TestValidEndpointAcceptsOnlyACanonicalLoopbackURL(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{
		"http://127.0.0.1:19713":       true,
		"http://[::1]:19713":           true,
		"http://localhost:19713":       true,
		"http://run.localhost:19713":   true,
		"http://127.0.0.1":             false,
		"http://127.0.0.1:80":          false,
		"http://127.0.0.1:019713":      false,
		"https://127.0.0.1:19713":      false,
		"http://example.com:19713":     false,
		"http://LOCALHOST:19713":       false,
		"http://127.0.0.1:19713/":      false,
		"http://127.0.0.1:19713?x=1":   false,
		"http://user@127.0.0.1:19713":  false,
		"http://[::ffff:7f00:1]:19713": false,
		"":                             false,
	} {
		if got := ValidEndpoint(value); got != want {
			t.Errorf("ValidEndpoint(%q) = %t, want %t", value, got, want)
		}
	}
}

func TestValidIDAndCapabilityAreCanonical(t *testing.T) {
	t.Parallel()
	if !ValidID(testRunID) || ValidID(strings.ToUpper(testRunID)) ||
		ValidID("00000000-0000-0000-0000-000000000000") || ValidID("*") {
		t.Fatal("ValidID accepts only a canonical, non-nil UUID")
	}
	if !ValidCapability(testCapability) || ValidCapability(testCapability+"=") || ValidCapability("") {
		t.Fatal("ValidCapability accepts only 32 bytes of unpadded base64url")
	}
}

// One call round trips through the channel: the client posts the request
// document with the Run capability, never a bearer or the Run token, and
// returns the server's status and body; a daemon refusal is a *Refusal.
func TestClientRoundTripsOneCallThroughTheChannel(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != Path {
			t.Errorf("channel request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != AuthorizationScheme+" "+testCapability {
			t.Errorf("Authorization = %q, want the Run capability", got)
		}
		if r.Header.Get("X-Boardly-Run-Token") != "" {
			t.Error("the client sent the Run token; only the daemon's forward carries it")
		}
		var request Request
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		switch request.Path {
		case "/companies/" + testCompanyID + "/questions":
			if request.SchemaVersion != 1 || request.Method != http.MethodPost ||
				request.RequestKey != "0198aa00-0000-7000-8000-000000000001" ||
				string(request.Body) != `{"title":"Pick a name"}` {
				t.Errorf("request = %+v", request)
			}
			_ = json.NewEncoder(w).Encode(Response{
				SchemaVersion: 1, Status: http.StatusCreated, Body: json.RawMessage(`{"id":"q1"}`),
			})
		case "/me":
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).
				Encode(ErrorBody{SchemaVersion: 1, Code: "run_scope_denied", Reason: "Not admitted."})
		case "/broken":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream down"))
		default:
			_ = json.NewEncoder(w).Encode(Response{SchemaVersion: 2, Status: http.StatusOK})
		}
	}))
	t.Cleanup(server.Close)
	client := NewClient(testControl(server.URL))

	response, err := client.Do(t.Context(), Request{
		Method: http.MethodPost, Path: "/companies/" + testCompanyID + "/questions",
		RequestKey: "0198aa00-0000-7000-8000-000000000001", Body: json.RawMessage(`{"title":"Pick a name"}`),
	})
	if err != nil || response.Status != http.StatusCreated || string(response.Body) != `{"id":"q1"}` {
		t.Fatalf("Do() = %+v, %v", response, err)
	}

	_, err = client.Do(t.Context(), Request{Method: http.MethodGet, Path: "/me"})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Status != http.StatusForbidden || refusal.Code != "run_scope_denied" {
		t.Fatalf("refused call = %v, want run_scope_denied", err)
	}
	_, err = client.Do(t.Context(), Request{Method: http.MethodGet, Path: "/broken"})
	if !errors.As(err, &refusal) || refusal.Code != "run_channel_error" || refusal.Reason != "Bad Gateway" {
		t.Fatalf("a refusal without a body = %v, want run_channel_error", err)
	}
	if _, err := client.Do(t.Context(), Request{Method: http.MethodGet, Path: "/other"}); err == nil ||
		errors.As(err, &refusal) {
		t.Fatalf("an answer in another schema = %v, want an invalid response", err)
	}
	if NewClient(testControl(server.URL)).client.Timeout <= MaxForwardTimeout {
		t.Fatal("the client must outwait the daemon's longest forward")
	}
}
