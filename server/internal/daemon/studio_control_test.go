package daemon

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/pkg/protocol"
)

func TestStudioEditorsOnOneRuntimeUseDifferentAgentProfiles(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	tools := newLocalToolService(t.TempDir())
	release := studioRelease()
	entry := filepath.Join(tools.root, "bridge.cjs")
	// This child exercises production pipe routing, not upstream sign-in.
	script := `const fs=require('node:fs'),path=require('node:path'),crypto=require('node:crypto'),readline=require('node:readline');
 const account=fs.readFileSync(path.join(process.env.ZENITH_STUDIO_CLI_HOME,'account'),'utf8');let hello;
 readline.createInterface({input:process.stdin}).on('line',line=>{const req=JSON.parse(line);let data;
 if(req.operation==='handshake'){hello=req;data={processId:crypto.randomUUID(),runtimeId:req.runtimeId,daemonId:req.daemonId,connectionGeneration:req.connectionGeneration,origin:process.env.ZENITH_STUDIO_CLI_API_URL,studioAccountId:account,editorSchemaVersion:2,requestLimit:16384,responseLimit:8388608,maxInFlight:1};}
 else data={probeId:req.request.probeId,runtimeId:hello.runtimeId,daemonId:hello.daemonId,connectionGeneration:hello.connectionGeneration,origin:process.env.ZENITH_STUDIO_CLI_API_URL,studioAccountId:account,verifiedAt:new Date().toISOString(),creationProtocol:'receiptV1',editorSchemaVersion:2};
 console.log(JSON.stringify({protocolVersion:1,id:req.id,ok:true,data}));});`
	if err = os.WriteFile(entry, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	tools.installed[release.ID()] = toolInstallation{Node: node, Entrypoint: entry}
	runtime := Runtime{ID: uuid.NewString(), CompanyID: uuid.NewString(), Provider: "codex"}
	d := &Daemon{
		tools:        tools,
		cfg:          Config{DaemonID: "fixture"},
		runtimeIndex: map[string]Runtime{runtime.ID: runtime},
	}
	control := &studioControl{daemon: d}
	defer control.close()
	accounts := make(map[uuid.UUID]uuid.UUID)
	for range 2 {
		agent, account := uuid.New(), uuid.New()
		accounts[agent] = account
		home := mustToolAuthHome(t, tools, toolAuthScope{runtime.CompanyID, agent.String()}, release)
		if err = os.MkdirAll(home, 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(home, "account"), []byte(account.String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		for agent, want := range accounts {
			request, _ := json.Marshal(
				protocol.StudioConnectionProbeRequest{
					RuntimeID: uuid.MustParse(runtime.ID),
					ProbeID:   uuid.New(),
				},
			)
			scoped, _ := json.Marshal(protocol.StudioScopedControl{AgentID: agent, Request: request})
			done := make(chan []byte, 1)
			control.accept(
				t.Context(),
				protocol.EventStudioConnectionProbe,
				scoped,
				func(raw []byte) { done <- raw },
			)
			select {
			case raw := <-done:
				var frame protocol.Message
				var result protocol.StudioControlResponse
				if json.Unmarshal(raw, &frame) != nil ||
					json.Unmarshal(frame.Payload, &result) != nil ||
					result.ErrorCode != "" ||
					result.Probe == nil ||
					result.Probe.StudioAccountID != want {
					t.Fatalf("wrong editor account: %s", raw)
				}
			// accept bounds a probe at 15 seconds. Allow that response plus
			// scheduling headroom when CI starts Node under parallel race tests;
			// a production timeout still fails the response assertions above.
			case <-time.After(30 * time.Second):
				t.Fatalf("editor for agent %s did not answer", agent)
			}
		}
	}
	control.mu.Lock()
	count := len(control.runtimes)
	control.mu.Unlock()
	if count != 2 {
		t.Fatalf("editor profiles=%d, want 2", count)
	}
}
