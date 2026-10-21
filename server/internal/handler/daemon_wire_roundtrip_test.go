package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/agent"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
	provideragent "github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

// The daemon plane is two binaries sharing one wire contract with no shared
// type: internal/daemon builds the request bodies, internal/handler declares
// the structs that bind them, and neither side references the other. A JSON
// tag renamed on one side alone compiles cleanly and fails at runtime as a
// zero value, so these tests drive the real daemon.Client against a recorder
// and replay both directions through the real server-side types.

// daemonWireCase is one request/response pair on the daemon control plane.
type daemonWireCase struct {
	name string
	// path the client must hit. A route rename cannot quietly pass the body
	// assertions because the recorder fails the case first.
	path string
	// call drives the real daemon.Client method and returns whatever it
	// decoded from reply (nil when the method discards the body).
	call func(context.Context, *daemon.Client) (any, error)
	// serverRequest is a zero server-side binding struct. The recorded body
	// decodes into it with unknown fields rejected, so a daemon-side key the
	// server does not declare fails here instead of in production. nil marks
	// a bodyless call (a GET), which must record no body at all.
	serverRequest any
	// wantRequest is the same type carrying the values the client was asked
	// to send. A server-side tag rename leaves those fields zero.
	wantRequest any
	// reply is the server-side response value the recorder marshals back.
	reply any
	// wantReply is the daemon-side decode of reply. Response structs are
	// deliberate subsets, so this compares values rather than whole shapes.
	wantReply any
}

func TestDaemonWireContractRoundTrips(t *testing.T) {
	int64Pointer := func(value int64) *int64 { return &value }
	zeroCost := int64(0)
	sampleRun := runResponse{
		ID:                     uuid.MustParse("00000000-0000-4000-8000-000000004711"),
		CompanyID:              "019fa834-a515-79b2-b1bd-19e6bd4f275a",
		AgentID:                "019fa834-a515-79b2-b1bd-19e6bd4f275b",
		TaskID:                 "019fa834-a515-79b2-b1bd-19e6bd4f275c",
		ConversationID:         "019fa834-a515-79b2-b1bd-19e6bd4f275d",
		RuntimeID:              "019fa834-a515-79b2-b1bd-19e6bd4f275e",
		Context:                json.RawMessage(`{"prompt":"ship it"}`),
		ForceFreshSession:      true,
		AuthToken:              "brt_sentinel",
		PriorSessionID:         "session-prior",
		PriorSessionThroughSeq: 12,
	}

	sampleRuntime := runtimeResponse{
		ID:        "019fa834-a515-79b2-b1bd-19e6bd4f2762",
		CompanyID: "019fa834-a515-79b2-b1bd-19e6bd4f275a",
		Name:      "Claude (Device-1)",
		Provider:  "claude",
		Status:    "online",
	}

	heartbeatAck := &protocol.DaemonHeartbeatAckPayload{
		RuntimeID: "019fa834-a515-79b2-b1bd-19e6bd4f275e",
		Status:    "ok",
		ServerCapabilities: []string{
			protocol.DaemonCapabilityRPCV1,
			protocol.DaemonCapabilityModelDiscoveryV1,
		},
		PendingModelList: &protocol.DaemonHeartbeatPendingModelList{
			RequestID: "019fa834-a515-79b2-b1bd-19e6bd4f2761",
		},
	}

	cases := []daemonWireCase{
		{
			name: "register",
			path: "/api/v1/daemon/register",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return c.Register(ctx, daemon.RegisterRequest{
					CompanyID:  "019fa834-a515-79b2-b1bd-19e6bd4f275a",
					DaemonID:   "daemon-1",
					DeviceName: "Device-1",
					CLIVersion: "1.2.3",
					LaunchedBy: "cli",
					Runtimes: []daemon.RegisterRuntime{
						{
							Name:                "Claude (Device-1)",
							Provider:            "claude",
							Version:             "2.0.14",
							Status:              "online",
							CredentialIsolation: protocol.CredentialIsolationEnforced,
						},
						// An unusable provider registers offline with its
						// reason; the nested object binds field for field.
						{
							Name:     "Codex (Device-1)",
							Provider: "codex",
							Version:  "0.90.0",
							Status:   "offline",
							UnusableReason: &protocol.RuntimeUnusableReason{
								Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
								Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
							},
							CredentialIsolation: protocol.CredentialIsolationNotEnforced,
						},
					},
				})
			},
			serverRequest: &daemonRegisterRequest{},
			wantRequest: &daemonRegisterRequest{
				CompanyID:  "019fa834-a515-79b2-b1bd-19e6bd4f275a",
				DaemonID:   "daemon-1",
				DeviceName: "Device-1",
				CLIVersion: "1.2.3",
				LaunchedBy: "cli",
				Runtimes: []daemonRegisterRuntime{
					{
						Name:                "Claude (Device-1)",
						Provider:            "claude",
						Version:             "2.0.14",
						Status:              "online",
						CredentialIsolation: protocol.CredentialIsolationEnforced,
					},
					{
						Name:     "Codex (Device-1)",
						Provider: "codex",
						Version:  "0.90.0",
						Status:   "offline",
						UnusableReason: &daemonRuntimeUnusableReason{
							Code:    protocol.RuntimeUnusableCodeBelowMinimumVersion,
							Message: "Codex CLI 0.90.0 is below the minimum 0.100.0",
						},
						CredentialIsolation: protocol.CredentialIsolationNotEnforced,
					},
				},
			},
			reply: daemonRuntimeCollectionResponse{Items: []runtimeResponse{sampleRuntime}},
			wantReply: &daemon.RegisterResponse{Runtimes: []daemon.Runtime{{
				ID:        sampleRuntime.ID,
				CompanyID: sampleRuntime.CompanyID,
				Name:      sampleRuntime.Name,
				Provider:  sampleRuntime.Provider,
				Status:    sampleRuntime.Status,
			}}},
		},
		{
			// A GET: the contract here is the reply direction only.
			name: "companies",
			path: "/api/v1/daemon/companies",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return c.ListCompanies(ctx)
			},
			reply: daemonCompanyCollectionResponse{Items: []daemonCompanyResponse{{
				ID:   "019fa834-a515-79b2-b1bd-19e6bd4f275a",
				Name: "Acme",
				Slug: "acme",
			}}},
			wantReply: []daemon.CompanyInfo{{
				ID:   "019fa834-a515-79b2-b1bd-19e6bd4f275a",
				Name: "Acme",
			}},
		},
		{
			// The daemon holds a full SkillRefData; the endpoint binds exactly
			// {id, source} and rejects any other key, so the client must send
			// the pair and nothing else.
			name: "skill bundle resolve",
			path: "/api/v1/daemon/runtimes/runtime-1/runs/00000000-0000-4000-8000-000000004711/skill-bundles/resolve",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return c.ResolveSkillBundle(
					ctx,
					"runtime-1",
					uuid.MustParse("00000000-0000-4000-8000-000000004711"),
					daemon.SkillRefData{
						ID:          "skill-1",
						Source:      "company",
						Name:        "Deploy",
						Description: "how to ship",
						Hash:        "abc123",
						SizeBytes:   42,
						FileCount:   2,
					},
				)
			},
			serverRequest: &daemonResolveSkillBundlesRequest{},
			wantRequest: &daemonResolveSkillBundlesRequest{
				ContractVersion: 1,
				Skills:          []daemonSkillRef{{ID: "skill-1", Source: "company"}},
			},
			reply: daemonSkillBundleCollectionResponse{Items: []json.RawMessage{
				json.RawMessage(
					`{"id":"skill-1","source":"company","name":"Deploy","hash":"abc123","sizeBytes":42,"content":"# Deploy"}`,
				),
			}},
			wantReply: daemon.SkillData{
				ID:        "skill-1",
				Source:    "company",
				Name:      "Deploy",
				Hash:      "abc123",
				SizeBytes: 42,
				Content:   "# Deploy",
			},
		},
		{
			// A GET: the daemon's mid-flight cancellation poll.
			name: "run status",
			path: "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/status",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return c.GetRunStatus(ctx, uuid.MustParse("00000000-0000-4000-8000-000000004711"))
			},
			reply:     daemonRunStatusResponse{Status: "cancelled", Reason: "task_reassigned"},
			wantReply: daemon.RunStatus{Status: "cancelled", Reason: "task_reassigned"},
		},
		{
			name: "pin session",
			path: "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/session",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return nil, c.PinRunSession(
					ctx,
					uuid.MustParse("00000000-0000-4000-8000-000000004711"),
					"session-current",
				)
			},
			serverRequest: &pinRunSessionRequest{},
			wantRequest:   &pinRunSessionRequest{SessionID: "session-current"},
			reply:         daemonStatusResponse{Status: "ok"},
		},
		{
			name: "model discovery result",
			path: "/api/v1/daemon/runtimes/runtime-1/model-discoveries/request-1/result",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return nil, c.ReportRuntimeModelDiscovery(
					ctx,
					"runtime-1",
					"request-1",
					daemon.ModelDiscoveryResult{
						Status: "completed",
						Models: []provideragent.RuntimeModel{{
							ID: "gpt-visible", Label: "GPT Visible", Default: true,
						}},
					},
				)
			},
			serverRequest: &daemonModelDiscoveryResultRequest{},
			wantRequest: &daemonModelDiscoveryResultRequest{
				Status: agent.ModelDiscoveryCompleted,
				Models: []provideragent.RuntimeModel{{
					ID: "gpt-visible", Label: "GPT Visible", Default: true,
				}},
			},
			reply: map[string]string{"status": "ok"},
		},
		{
			name: "claim",
			path: "/api/v1/daemon/runs/claim",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return c.ClaimRuns(ctx, "daemon-1", []string{"runtime-1", "runtime-2"}, 4)
			},
			serverRequest: &daemonClaimRunsBatchRequest{},
			wantRequest: &daemonClaimRunsBatchRequest{
				DaemonID:   "daemon-1",
				RuntimeIDs: []string{"runtime-1", "runtime-2"},
				MaxRuns:    4,
			},
			reply: claimBatchReply{Items: []runResponse{sampleRun}},
			wantReply: []*daemon.Run{{
				ID:                     sampleRun.ID,
				CompanyID:              sampleRun.CompanyID,
				AgentID:                sampleRun.AgentID,
				TaskID:                 sampleRun.TaskID,
				ConversationID:         sampleRun.ConversationID,
				RuntimeID:              sampleRun.RuntimeID,
				Context:                sampleRun.Context,
				ForceFreshSession:      sampleRun.ForceFreshSession,
				AuthToken:              sampleRun.AuthToken,
				PriorSessionID:         sampleRun.PriorSessionID,
				PriorSessionThroughSeq: sampleRun.PriorSessionThroughSeq,
			}},
		},
		{
			// Start carries the runtime the daemon holds the claim under, and
			// the server fences the transition on it (bug R2). A daemon-side
			// rename of runtimeId would leave the server's field zero here.
			name: "start",
			path: "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/start",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return nil, c.StartRun(
					ctx,
					"019fa834-a515-79b2-b1bd-19e6bd4f275e",
					uuid.MustParse("00000000-0000-4000-8000-000000004711"),
				)
			},
			serverRequest: &daemonStartRunRequest{},
			wantRequest: &daemonStartRunRequest{
				RuntimeID: "019fa834-a515-79b2-b1bd-19e6bd4f275e",
			},
			reply: sampleRun,
		},
		{
			name: "usage distinguishes exact zero cost from unreported cost",
			path: "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/usage",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return nil, c.ReportRunUsage(
					ctx,
					uuid.MustParse("00000000-0000-4000-8000-000000004711"),
					[]daemon.RunUsageEntry{
						{
							Provider:     "claude",
							Model:        "free-model",
							InputTokens:  5,
							CostUSDTicks: &zeroCost,
							RawJSON:      json.RawMessage(`{"input_tokens":5}`),
						},
						{
							Provider: "codex", Model: "unpriced-model", OutputTokens: 3,
							RawJSON: json.RawMessage(`{"output_tokens":3}`),
						},
					},
				)
			},
			serverRequest: &runUsageBatchRequest{},
			wantRequest: &runUsageBatchRequest{Usage: []runUsagePayload{
				{
					Provider: "claude", Model: "free-model",
					InputTokens: int64Pointer(5), OutputTokens: int64Pointer(0),
					CacheReadTokens: int64Pointer(0), CacheWriteTokens: int64Pointer(0),
					CostUSDTicks: &zeroCost, RawJSON: json.RawMessage(`{"input_tokens":5}`),
				},
				{
					Provider: "codex", Model: "unpriced-model",
					InputTokens: int64Pointer(0), OutputTokens: int64Pointer(3),
					CacheReadTokens: int64Pointer(0), CacheWriteTokens: int64Pointer(0),
					RawJSON: json.RawMessage(`{"output_tokens":3}`),
				},
			}},
			reply: map[string]string{"status": "ok"},
		},
		{
			name: "complete",
			path: "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/complete",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return nil, c.CompleteRun(
					ctx,
					uuid.MustParse("00000000-0000-4000-8000-000000004711"),
					daemon.RunTranscriptProof{
						ThroughSeq:   7,
						FinalItemKey: "provider-item-7",
					},
					"session-current",
					true,
					"session-retired",
				)
			},
			serverRequest: &runCompleteRequest{},
			wantRequest: &runCompleteRequest{
				ThroughSeq:            int64Pointer(7),
				FinalItemKey:          "provider-item-7",
				SessionID:             "session-current",
				SessionRolloutMissing: true,
				RetiredSessionID:      "session-retired",
			},
			reply: sampleRun,
		},
		{
			// A successful turn with no final agent text: the client omits
			// finalItemKey entirely and the binding accepts its absence.
			name: "complete without final text",
			path: "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/complete",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return nil, c.CompleteRun(
					ctx,
					uuid.MustParse("00000000-0000-4000-8000-000000004711"),
					daemon.RunTranscriptProof{ThroughSeq: 4},
					"session-current",
					false,
					"",
				)
			},
			serverRequest: &runCompleteRequest{},
			wantRequest: &runCompleteRequest{
				ThroughSeq: int64Pointer(4),
				SessionID:  "session-current",
			},
			reply: sampleRun,
		},
		{
			name: "fail",
			path: "/api/v1/daemon/runs/00000000-0000-4000-8000-000000004711/fail",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return nil, c.FailRun(
					ctx,
					uuid.MustParse("00000000-0000-4000-8000-000000004711"),
					daemon.RunTranscriptProof{
						ThroughSeq:   7,
						FinalItemKey: "provider-item-7",
					},
					"agent exploded",
					"session-current",
					"agent_error",
					true,
					"session-retired",
				)
			},
			serverRequest: &runFailRequest{},
			wantRequest: &runFailRequest{
				ThroughSeq:            int64Pointer(7),
				FinalItemKey:          "provider-item-7",
				Error:                 "agent exploded",
				SessionID:             "session-current",
				FailureReason:         "agent_error",
				SessionRolloutMissing: true,
				RetiredSessionID:      "session-retired",
			},
			reply: sampleRun,
		},
		{
			name: "deregister",
			path: "/api/v1/daemon/deregister",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return nil, c.Deregister(ctx, []string{"019fa834-a515-79b2-b1bd-19e6bd4f275e"})
			},
			serverRequest: &daemonDeregisterRequest{},
			wantRequest: &daemonDeregisterRequest{
				RuntimeIDs: []string{"019fa834-a515-79b2-b1bd-19e6bd4f275e"},
			},
			reply: daemonStatusResponse{Status: "ok"},
		},
		{
			name: "heartbeat",
			path: "/api/v1/daemon/heartbeat",
			call: func(ctx context.Context, c *daemon.Client) (any, error) {
				return c.SendHeartbeat(
					ctx,
					"019fa834-a515-79b2-b1bd-19e6bd4f275e",
					protocol.CredentialIsolationEnforced,
				)
			},
			serverRequest: &daemonHeartbeatRequest{},
			wantRequest: &daemonHeartbeatRequest{
				RuntimeID:           "019fa834-a515-79b2-b1bd-19e6bd4f275e",
				CredentialIsolation: protocol.CredentialIsolationEnforced,
			},
			// The exact body the HTTP handler writes, not a copy of it.
			reply: daemonHeartbeatResponse(heartbeatAck),
			wantReply: &daemon.HeartbeatResponse{
				// The HTTP reply now carries the runtime id the WS ack always
				// carried, so one decoded shape serves both transports.
				RuntimeID:          heartbeatAck.RuntimeID,
				Status:             heartbeatAck.Status,
				ServerCapabilities: heartbeatAck.ServerCapabilities,
				PendingModelList:   heartbeatAck.PendingModelList,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var recorded []byte
			var gotDaemonID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Errorf("client posted to %s, want %s", r.URL.Path, tc.path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				gotDaemonID = r.Header.Get("X-Zenith-Team-Daemon-ID")
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				recorded = body
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(tc.reply); err != nil {
					t.Errorf("encode reply: %v", err)
				}
			}))
			defer server.Close()

			client := daemon.NewClient(server.URL)
			client.SetToken("sentinel-daemon-token")
			client.SetDaemonID("daemon-1")
			got, err := tc.call(t.Context(), client)
			if err != nil {
				t.Fatalf("client call failed: %v", err)
			}
			// Every request on the plane — POSTs and the companies GET —
			// names the machine, which is what the server-side fence reads.
			if gotDaemonID != "daemon-1" {
				t.Errorf("X-Zenith-Team-Daemon-ID = %q, want daemon-1", gotDaemonID)
			}

			if tc.serverRequest == nil {
				if len(recorded) != 0 {
					t.Errorf("bodyless call sent a body: %s", recorded)
				}
			} else {
				assertRequestBinds(t, recorded, tc.serverRequest, tc.wantRequest)
			}
			if tc.wantReply != nil && !reflect.DeepEqual(got, tc.wantReply) {
				t.Errorf("daemon decoded reply as %s, want %s", asJSON(t, got), asJSON(t, tc.wantReply))
			}
		})
	}
}

func TestRunUsageInputFromPayloadKeepsExactZeroDistinctFromMissing(t *testing.T) {
	t.Parallel()
	int64Pointer := func(value int64) *int64 { return &value }
	zeroCost := int64(0)

	reportedZero := runUsageInputFromPayload(runUsagePayload{
		Provider: "CODEX", Model: "gpt-5", CostUSDTicks: &zeroCost,
		InputTokens: int64Pointer(1), OutputTokens: int64Pointer(2),
		CacheReadTokens: int64Pointer(3), CacheWriteTokens: int64Pointer(4),
		RawJSON: json.RawMessage(`{"input_tokens":1}`),
	})
	if reportedZero.CostUSDTicks == nil || *reportedZero.CostUSDTicks != 0 {
		t.Fatalf("reported zero cost = %v, want pointer to 0", reportedZero.CostUSDTicks)
	}

	unreported := runUsageInputFromPayload(runUsagePayload{
		Provider: "codex", Model: "gpt-5-mini",
		InputTokens: int64Pointer(1), OutputTokens: int64Pointer(2),
		CacheReadTokens: int64Pointer(3), CacheWriteTokens: int64Pointer(4),
		RawJSON: json.RawMessage(`{"input_tokens":1}`),
	})
	if unreported.CostUSDTicks != nil {
		t.Fatalf("unreported cost = %v, want nil", unreported.CostUSDTicks)
	}
}

// claimBatchReply is the batch claim's reply envelope as the server serves
// it: the {items} list wrapper shared with every daemon-plane list.
type claimBatchReply struct {
	Items []runResponse `json:"items"`
}

// assertRequestBinds decodes the recorded daemon body into the server's
// binding struct with unknown fields rejected, compares the bound values with
// what the client was asked to send, then re-marshals the bound struct and
// requires the same key set — so the contract holds in both directions.
func assertRequestBinds(t *testing.T, recorded []byte, into, want any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(recorded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		t.Fatalf("server struct %T cannot bind the daemon body %s: %v", into, recorded, err)
	}
	if !reflect.DeepEqual(into, want) {
		t.Errorf("server bound %#v, want %#v (body: %s)", into, want, recorded)
	}

	reencoded, err := json.Marshal(into)
	if err != nil {
		t.Fatalf("re-marshal %T: %v", into, err)
	}
	if sent, back := jsonKeys(t, recorded), jsonKeys(t, reencoded); !reflect.DeepEqual(sent, back) {
		t.Errorf("server re-serializes keys %v, daemon sent %v", back, sent)
	}
}

func jsonKeys(t *testing.T, data []byte) []string {
	t.Helper()
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("body is not a JSON object: %v (%s)", err, data)
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// asJSON renders a decoded value for failure output, where the wire shape is
// what a reader needs to see rather than Go pointers.
func asJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %T for diagnostics: %v", value, err)
	}
	return data
}
