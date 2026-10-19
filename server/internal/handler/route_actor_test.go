package handler

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/middleware"
	"boardly.dev/boardly/server/internal/task"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// routeActorProof is a well-formed Run proof; the resolvers below decide
// what it proves.
const routeActorProof = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

var (
	routeActorUser     = uuid.MustParse("0198bb00-0000-7000-8000-00000000000a")
	routeActorCompany  = uuid.MustParse("0198bb00-0000-7000-8000-00000000000b")
	routeActorResource = uuid.MustParse("0198bb00-0000-7000-8000-00000000000c")
)

// countingRunProofResolver records every proof a handler resolves and
// answers with origin, or with err when it is set.
type countingRunProofResolver struct {
	mu     sync.Mutex
	calls  int
	origin execution.RunOrigin
	err    error
}

func (r *countingRunProofResolver) ResolveRunOrigin(context.Context, string) (execution.RunOrigin, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return execution.RunOrigin{}, r.err
	}
	return r.origin, nil
}

func (r *countingRunProofResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// routeActorPath fills a route's path parameters: the Company with
// routeActorCompany, every other one with routeActorResource.
func routeActorPath(route string) (string, string) {
	method, pattern, _ := strings.Cut(route, " ")
	segments := strings.Split(pattern, "/")
	for index, segment := range segments {
		switch {
		case segment == ":companyId":
			segments[index] = routeActorCompany.String()
		case strings.HasPrefix(segment, ":"):
			segments[index] = routeActorResource.String()
		}
	}
	return method, strings.Join(segments, "/")
}

func routeActorMember() auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID:    routeActorUser,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: routeActorCompany,
		Role:      auth.CompanyRoleOwner,
	}
}

func routeActorCompanyService() *companyServiceStub {
	member := func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
		return routeActorMember(), nil
	}
	return &companyServiceStub{authorize: member, requirePermission: member}
}

// IA-FR1, IA-AC2: every mounted route says whether it accepts a Run, once,
// and the table names no route that is not mounted.
func TestEveryRouteDeclaresItsActorType(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool, len(routeActorTypes))
	for _, route := range slices.Concat(runActorRoutes, anyActorRoutes, personActorRoutes) {
		if seen[route] {
			t.Errorf("route %q is listed twice in route_actor.go", route)
		}
		seen[route] = true
	}
	mounted := make(map[string]bool)
	for _, route := range New(fullyWiredHandler()).router.Routes() {
		key := route.Method + " " + route.Path
		mounted[key] = true
		if _, ok := routeActorTypes[key]; !ok {
			t.Errorf("mounted route %q declares no actor type; add it to route_actor.go", key)
		}
	}
	for key := range routeActorTypes {
		if !mounted[key] {
			t.Errorf("actor type entry %q names no mounted route", key)
		}
	}
}

// IA-FR1, IA-FR2, IA-AC1: a Run proof on a person route is refused before
// authentication, so it reaches no service and no proof is resolved.
func TestPersonRoutesRefuseARunProof(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	authenticated := 0
	resolver := &countingRunProofResolver{}
	wired := fullyWiredHandler()
	wired.AuthService = &authServiceStub{
		authenticate: func(context.Context, string, auth.TokenKind) (auth.UserPrincipal, error) {
			mu.Lock()
			defer mu.Unlock()
			authenticated++
			return routeActorMember(), nil
		},
	}
	wired.RunProofResolver = resolver
	handler := New(wired)

	for _, route := range personActorRoutes {
		t.Run(route, func(t *testing.T) {
			method, path := routeActorPath(route)
			var request *http.Request
			if method == http.MethodGet {
				request = httptest.NewRequest(method, path, nil)
			} else {
				request = httptest.NewRequest(method, path, strings.NewReader("{}"))
				request.Header.Set("Content-Type", "application/json")
			}
			request.Header.Set("Authorization", "Bearer access-token")
			request.Header.Set(runTokenHeader, routeActorProof)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			assertErrorResponse(t, response, http.StatusForbidden, "human_actor_required")
		})
	}
	if authenticated != 0 || resolver.count() != 0 {
		t.Fatalf("a refused Run proof reached authentication %d times and the resolver %d times",
			authenticated, resolver.count())
	}
}

// The deciding, editing and Project-making routes IA-AC1 names are person
// routes by name, so a later edit of the table cannot quietly open one.
func TestDecidingRoutesArePersonRoutes(t *testing.T) {
	t.Parallel()

	for _, route := range []string{
		"POST /api/v1/companies/:companyId/approvals/:approvalId/approve",
		"POST /api/v1/companies/:companyId/approvals/:approvalId/reject",
		"POST /api/v1/companies/:companyId/approvals/:approvalId/cancel",
		"POST /api/v1/companies/:companyId/questions/:questionId/answer",
		"POST /api/v1/companies/:companyId/questions/:questionId/withdraw",
		"PATCH /api/v1/companies/:companyId/tasks/:taskId",
		"POST /api/v1/companies/:companyId/tasks/:taskId/runs",
		"GET /api/v1/companies/:companyId/task-dependencies",
		"POST /api/v1/companies/:companyId/projects",
	} {
		if got := routeActorTypes[route]; got != middleware.RouteActorTypePerson {
			t.Errorf("%s accepts actor %q, want person", route, got)
		}
	}
}

// runRouteProbe is a request that passes everything a run route checks
// before it resolves the proof. Most run routes need nothing more than the
// proof, so a route missing from runRouteProbes is probed with the zero
// value: no body on a GET, an empty JSON object otherwise.
type runRouteProbe struct {
	body      string
	keyed     bool
	multipart bool
}

func runRouteProbes() map[string]runRouteProbe {
	draft := `{"drafts":[{"subject":"Hello","text":"Body",` +
		`"deliveries":[{"to":["lead@example.com"],"cc":[]}]}]}`
	return map[string]runRouteProbe{
		"POST /api/v1/companies/:companyId/tasks": {
			body: `{"projectId":"` + routeActorResource.String() +
				`","milestoneType":"product","title":"Follow up"}`,
		},
		"POST /api/v1/companies/:companyId/questions":          {keyed: true},
		"POST /api/v1/companies/:companyId/company-plan/apply": {keyed: true},
		"POST /api/v1/companies/:companyId/task-failures":      {body: `{"reason":"Blocked"}`, keyed: true},
		"POST /api/v1/companies/:companyId/conversations/:conversationId/mail-draft-groups": {
			body: draft, keyed: true,
		},
		"POST /api/v1/runs/:runId/output": {keyed: true},
		"POST /api/v1/companies/:companyId/tasks/:taskId/results": {
			body:  `{"expectedRevision":1,"outputIds":["` + routeActorResource.String() + `"]}`,
			keyed: true,
		},
		"POST /api/v1/companies/:companyId/milestone-attestations": {
			body: `{"milestones":["launch"],"note":"Shipped in 2021."}`,
		},
		"POST /api/v1/attachments/initiate": {multipart: true},
		"POST /api/v1/attachments/:attachmentId/complete": {
			body: `{"companyId":"` + routeActorCompany.String() + `"}`,
		},
	}
}

func (p runRouteProbe) request(t *testing.T, route string) *http.Request {
	t.Helper()
	method, path := routeActorPath(route)
	var request *http.Request
	switch {
	case p.multipart:
		request = multipartRequest(t, func(writer *multipart.Writer) {
			writeMetadataPart(t, writer, []byte(`{"requestKey":"`+routeActorResource.String()+
				`","companyId":"`+routeActorCompany.String()+
				`","filename":"output.txt","contentType":"text/plain","sizeBytes":2}`))
			file, err := writer.CreateFormFile("file", "output.txt")
			if err != nil {
				t.Fatalf("create file part: %v", err)
			}
			_, _ = file.Write([]byte("ok"))
		})
		request.URL.Path = path
	case p.body != "":
		request = httptest.NewRequest(method, path, strings.NewReader(p.body))
		request.Header.Set("Content-Type", "application/json")
	case method == http.MethodGet:
		request = httptest.NewRequest(method, path, nil)
	default:
		request = httptest.NewRequest(method, path, strings.NewReader("{}"))
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer access-token")
	request.Header.Set(runTokenHeader, routeActorProof)
	if p.keyed {
		request.Header.Set(idempotencyHeader, uuid.NewString())
	}
	return request
}

// IA-FR1: a run route is one whose handler reads the proof. Sent a proof,
// every run route resolves it, so none acts as the plain member.
func TestRunRoutesResolveTheProof(t *testing.T) {
	t.Parallel()

	probes := runRouteProbes()
	for route := range probes {
		if routeActorTypes[route] != middleware.RouteActorTypeRun {
			t.Errorf("probe %q is not a run route", route)
		}
	}

	resolver := &countingRunProofResolver{err: execution.ErrRunProofInvalid}
	wired := fullyWiredHandler()
	wired.AuthService = authenticatedAuthStub(routeActorUser)
	wired.CompanyService = routeActorCompanyService()
	wired.ProjectService = &projectServiceStub{authorize: allProjectPermissions}
	wired.Queries = generated.New(&daemonRuntimeAccessDBTX{member: true})
	wired.RunProofResolver = resolver
	handler := New(wired)

	for _, route := range runActorRoutes {
		probe := probes[route]
		t.Run(route, func(t *testing.T) {
			before := resolver.count()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, probe.request(t, route))
			if resolver.count() == before {
				t.Fatalf(
					"the proof was never resolved; status %d, body %s",
					response.Code,
					response.Body.String(),
				)
			}
			if response.Code < 400 {
				t.Fatalf("a refused proof answered %d; body %s", response.Code, response.Body.String())
			}
		})
	}
}

// The any routes are the five Company metadata reads, and nothing else.
func TestAnyRoutesAreTheListedReads(t *testing.T) {
	t.Parallel()

	want := []string{
		"GET /api/v1/companies",
		"GET /api/v1/companies/:companyId",
		"GET /api/v1/companies/:companyId/agents",
		"GET /api/v1/companies/:companyId/agents/:agentId",
	}
	var got []string
	for route, actor := range routeActorTypes {
		if actor == middleware.RouteActorTypeAny {
			got = append(got, route)
		}
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("any routes = %q, want %q", got, want)
	}
}

// runServiceGetStub serves GET /runs/:runId and records each read.
type runServiceGetStub struct {
	execution.RunService
	reads *int
}

func (s runServiceGetStub) Get(context.Context, auth.UserPrincipal, uuid.UUID) (execution.RunView, error) {
	*s.reads++
	return execution.RunView{}, nil
}

// IA-FR3, IA-AC3: with a proof, a Run's own reads must name its own Run or
// chat. A Run tagged in a channel reads its own Run record, as the Run
// channel offers every Run `zen run get`, but none of the chat reads.
// Without a proof the read is the member's own and is untouched.
func TestRunOwnReadsRefuseAnotherRunOrChat(t *testing.T) {
	t.Parallel()

	member := routeActorMember()
	colleague := uuid.MustParse("0198bb00-0000-7000-8000-0000000000aa")
	runID := uuid.MustParse("0198bb00-0000-7000-8000-0000000000b1")
	chatID := uuid.MustParse("0198bb00-0000-7000-8000-0000000000c1")
	otherID := uuid.MustParse("0198bb00-0000-7000-8000-0000000000d1")
	channel := uuid.MustParse("0198bb00-0000-7000-8000-0000000000e1")
	own := execution.RunOrigin{
		RunID: runID, CompanyID: routeActorCompany, AgentID: uuid.New(),
		ConversationID: chatID, ConversationKind: "direct", AuthorizedByUserID: &member.UserID,
	}
	tagged := own
	tagged.InvocationChannelID = &channel
	borrowed := own
	borrowed.AuthorizedByUserID = &colleague
	company := "/api/v1/companies/" + routeActorCompany.String()

	routes := []struct {
		name        string
		path        func(id uuid.UUID) string
		ownID       uuid.UUID
		readCount   func(*routeOwnReadCounts) int
		taggedReads bool
	}{
		{
			"run",
			func(id uuid.UUID) string { return company + "/runs/" + id.String() },
			runID, func(c *routeOwnReadCounts) int { return c.run }, true,
		},
		{
			"conversation",
			func(id uuid.UUID) string { return company + "/conversations/" + id.String() },
			chatID, func(c *routeOwnReadCounts) int { return c.detail }, false,
		},
		{
			"transcript",
			func(id uuid.UUID) string { return company + "/conversations/" + id.String() + "/transcript" },
			chatID, func(c *routeOwnReadCounts) int { return c.transcript }, false,
		},
		{
			"messages",
			func(id uuid.UUID) string { return "/api/v1/conversations/" + id.String() + "/messages" },
			chatID, func(c *routeOwnReadCounts) int { return c.messages }, false,
		},
	}
	cases := []struct {
		name     string
		origin   *execution.RunOrigin
		other    bool
		status   int
		code     string
		wantRead bool
	}{
		{name: "the Run's own", origin: &own, status: http.StatusOK, wantRead: true},
		{
			name: "another Run or chat", origin: &own, other: true, status: http.StatusForbidden,
			code: "task_outside_run_scope",
		},
		{
			name: "a tagged Run's own", origin: &tagged, status: http.StatusForbidden,
			code: "task_outside_run_scope",
		},
		{
			name: "a proof borne by another member", origin: &borrowed, status: http.StatusUnauthorized,
			code: "run_token_invalid",
		},
		{name: "no proof", other: true, status: http.StatusOK, wantRead: true},
	}
	for _, route := range routes {
		for _, test := range cases {
			if test.origin == &tagged && route.taggedReads {
				test.status, test.code, test.wantRead = http.StatusOK, "", true
			}
			t.Run(route.name+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				counts, handler := routeOwnReadHandler(test.origin)
				id := route.ownID
				if test.other {
					id = otherID
				}
				request := httptest.NewRequest(http.MethodGet, route.path(id), nil)
				request.Header.Set("Authorization", "Bearer access-token")
				if test.origin != nil {
					request.Header.Set(runTokenHeader, routeActorProof)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if test.code != "" {
					assertErrorResponse(t, response, test.status, test.code)
				} else if response.Code != test.status {
					t.Fatalf(
						"status = %d, want %d; body %s",
						response.Code,
						test.status,
						response.Body.String(),
					)
				}
				if read := route.readCount(counts) == 1; read != test.wantRead {
					t.Fatalf("service read = %t, want %t", read, test.wantRead)
				}
			})
		}
	}
}

type routeOwnReadCounts struct {
	run, detail, transcript, messages int
}

func routeOwnReadHandler(origin *execution.RunOrigin) (*routeOwnReadCounts, http.Handler) {
	counts := &routeOwnReadCounts{}
	resolver := &countingRunProofResolver{err: execution.ErrRunProofInvalid}
	if origin != nil {
		resolver = &countingRunProofResolver{origin: *origin}
	}
	conversations := &conversationServiceStub{
		getDirectDetail: func(
			context.Context, auth.UserPrincipal, uuid.UUID, conversation.MessageWindow,
		) (conversation.DirectDetail, error) {
			counts.detail++
			return conversation.DirectDetail{}, nil
		},
		transcript: func(
			context.Context, auth.UserPrincipal, uuid.UUID, conversation.TranscriptListOptions,
		) (conversation.TranscriptPage, error) {
			counts.transcript++
			return conversation.TranscriptPage{}, nil
		},
		listMessages: func(
			context.Context, auth.UserPrincipal, conversation.MessageTarget, conversation.MessageListOptions,
		) (conversation.MessagePage, error) {
			counts.messages++
			return conversation.MessagePage{}, nil
		},
	}
	return counts, New(Handler{
		AuthService:         authenticatedAuthStub(routeActorUser),
		CompanyService:      routeActorCompanyService(),
		RunService:          runServiceGetStub{reads: &counts.run},
		ConversationService: conversations,
		RunProofResolver:    resolver,
	})
}

// A proof on the daemon's upload must be the member's own live Run in the
// Company the upload names; with no proof the upload is the member's own,
// as before.
func TestUploadsCheckTheRunProofBearer(t *testing.T) {
	t.Parallel()

	member := attachmentHTTPUser
	colleague := uuid.MustParse("0198bb00-0000-7000-8000-0000000000aa")
	own := execution.RunOrigin{
		RunID: uuid.New(), CompanyID: attachmentHTTPCompany, AgentID: uuid.New(),
		ConversationID: uuid.New(), ConversationKind: "task", AuthorizedByUserID: &member,
	}
	borrowed := own
	borrowed.AuthorizedByUserID = &colleague
	elsewhere := own
	elsewhere.CompanyID = uuid.New()

	cases := []struct {
		name     string
		origin   *execution.RunOrigin
		proof    bool
		status   int
		code     string
		wantCall bool
	}{
		{name: "the member's own Run", origin: &own, proof: true, wantCall: true},
		{
			name: "a colleague's Run", origin: &borrowed, proof: true,
			status: http.StatusUnauthorized, code: "run_token_invalid",
		},
		{
			name: "a Run in another Company", origin: &elsewhere, proof: true,
			status: http.StatusUnauthorized, code: "run_token_invalid",
		},
		{
			name: "a proof that is not live", proof: true,
			status: http.StatusUnauthorized, code: "run_token_invalid",
		},
		{name: "no proof", wantCall: true},
	}
	for _, test := range cases {
		for _, step := range []string{"initiate", "complete"} {
			t.Run(step+"/"+test.name, func(t *testing.T) {
				t.Parallel()
				calls := 0
				var completedBy events.Actor
				service := &attachmentServiceStub{
					initiate: func(context.Context, attachment.InitiateInput, io.Reader) (attachment.InitiateResult, error) {
						calls++
						return attachment.InitiateResult{
							Attachment: pendingAttachmentHTTPResponse(), Created: true,
						}, nil
					},
					complete: func(_ context.Context, input attachment.CompleteInput) (attachment.CompleteResult, error) {
						calls++
						completedBy = input.RunActor
						return attachment.CompleteResult{
							Attachment: pendingAttachmentHTTPResponse(),
						}, nil
					},
				}
				resolver := &countingRunProofResolver{err: execution.ErrRunProofInvalid}
				if test.origin != nil {
					resolver = &countingRunProofResolver{origin: *test.origin}
				}
				handler := New(Handler{
					AuthService:       authenticatedAuthStub(attachmentHTTPUser),
					AttachmentService: service,
					RunProofResolver:  resolver,
				})
				var request *http.Request
				if step == "initiate" {
					request = newAttachmentInitiateRequest(
						t, validAttachmentMetadataJSON(2), []byte("ok"), false,
					)
				} else {
					request = httptest.NewRequest(http.MethodPost,
						"/api/v1/attachments/"+attachmentHTTPID.String()+"/complete",
						strings.NewReader(`{"companyId":"`+attachmentHTTPCompany.String()+`"}`))
					request.Header.Set("Authorization", "Bearer access-token")
					request.Header.Set("Content-Type", "application/json")
				}
				if test.proof {
					request.Header.Set(runTokenHeader, routeActorProof)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if test.code != "" {
					assertErrorResponse(t, response, test.status, test.code)
				} else if response.Code >= 400 {
					t.Fatalf("status = %d; body %s", response.Code, response.Body.String())
				}
				if (calls == 1) != test.wantCall {
					t.Fatalf(
						"attachment service called %d times, want call %t",
						calls,
						test.wantCall,
					)
				}
				// IA-FR4: a completion the member's own Run proves is
				// recorded as the Run's; one with no proof is the member's.
				var wantActor events.Actor
				if step == "complete" && test.wantCall && test.proof {
					wantActor = test.origin.Actor()
				}
				if completedBy != wantActor {
					t.Fatalf("completion recorded as %+v, want %+v", completedBy, wantActor)
				}
			})
		}
	}
}

// runWriteActivity says, for every run route that writes, how the Activity
// it records names the Run (IA-FR4, D31). A route either hands the proof's
// Run to the service that records it, and the named integration test reads
// the stored rows back, or it writes no Activity row, and the entry says
// why.
var runWriteActivity = map[string]struct {
	recordedBy string
	noActivity string
}{
	"POST /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/whiteboard/operations": {
		noActivity: "Board edits raise only a whiteboard change hint; board_operations records the acting Run",
	},
	"POST /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/whiteboard/context": {
		noActivity: "context reads Board content without writing Activity",
	},
	"POST /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/whiteboard/compose": {
		noActivity: "composed content is a Board edit: it raises only a whiteboard change hint; board_operations records the acting Run",
	},
	"POST /api/v1/companies/:companyId/runs/:runId/agent-artifacts/:artifactId/whiteboard/view": {
		noActivity: "the Agent view reads Board content without writing Activity",
	},
	"POST /api/v1/companies/:companyId/tasks": {
		recordedBy: "activity.TestTaskCreatedInARunNamesBothCreatorsAndTheRun",
	},
	"POST /api/v1/companies/:companyId/questions": {
		recordedBy: "activity.TestQuestionAskedInARunStoresTheAgentThePersonAndTheRun",
	},
	"POST /api/v1/companies/:companyId/company-plan/apply": {
		recordedBy: "activity.TestCompanyPlanRaisedInARunStoresTheRun and " +
			"activity.TestCompanyPlanAppliedByARunInProactiveModeStoresTheRun",
	},
	"POST /api/v1/companies/:companyId/tasks/:taskId/results": {
		recordedBy: "task.TestAgentFinalizesExplicitTaskResultsWithoutApproving",
	},
	"POST /api/v1/companies/:companyId/task-failures": {
		recordedBy: "activity.TestTaskFailedInARunStoresTheRun",
	},
	"POST /api/v1/companies/:companyId/conversations/:conversationId/mail-draft-groups": {
		recordedBy: "mailbox.TestAgentMailCreateBindsLiveRunReplaysAndPreservesConversationVisibility",
	},
	"POST /api/v1/attachments/:attachmentId/complete": {
		recordedBy: "handler.TestUploadsCheckTheRunProofBearer and " +
			"attachment.TestCompletedEffectNamesTheRunThatUploaded",
	},
	"POST /api/v1/runs/:runId/output": {
		noActivity: "a presentation stores an Output and raises only the canvas change hint (with no " +
			"reviewer the server, not the Run, ends the Run as the system); the " +
			"Deliverable frames raised when the Run completes name the Run " +
			"(execution.TestDeliverablePresentationPromotionReviewAndReplay), and Activity keeps " +
			"no Deliverable rows",
	},
	"POST /api/v1/companies/:companyId/milestone-attestations": {
		noActivity: "an attestation is its own row and raises no event",
	},
	"POST /api/v1/attachments/initiate": {
		noActivity: "starting an upload raises no event; its completion is recorded",
	},
	"POST /api/v1/companies/:companyId/conversations/:conversationId/calendar": {
		noActivity: "a calendar write raises Inbox notices and the calendar.changed hint, neither of them Activity",
	},
	"POST /api/v1/companies/:companyId/conversations/:conversationId/social/posts": {
		noActivity: "a social post raises no event",
	},
	"POST /api/v1/companies/:companyId/runs/:runId/agent-pages": {
		noActivity: "Project page work raises only the canvas change hints, never Activity",
	},
	"POST /api/v1/companies/:companyId/runs/:runId/agent-pages/:pageId": {
		noActivity: "Project page work raises only the canvas change hints, never Activity",
	},
	"POST /api/v1/companies/:companyId/runs/:runId/agent-pages/:pageId/presentations": {
		noActivity: "Project page work raises only the canvas change hints, never Activity",
	},
	"POST /api/v1/companies/:companyId/runs/:runId/agent-wiki/pages": {
		noActivity: "a research Run's Wiki write raises only the Wiki page events; run_wiki_writes is its evidence",
	},
}

// IA-FR4: every run route that writes says how its Activity names the Run,
// so a new one cannot record a Run's write as the member's own without the
// table saying so. The Task create is the one route that turns the proof
// into a scope itself, so that scope must carry the Run.
func TestRunMutationRoutesRecordTheRun(t *testing.T) {
	t.Parallel()

	for _, route := range runActorRoutes {
		method, _, _ := strings.Cut(route, " ")
		entry, listed := runWriteActivity[route]
		if method == http.MethodGet {
			if listed {
				t.Errorf("%s reads; it records no Activity and needs no entry", route)
			}
			continue
		}
		if !listed {
			t.Errorf("run route %s writes but does not say how its Activity names the Run", route)
			continue
		}
		if (entry.recordedBy == "") == (entry.noActivity == "") {
			t.Errorf("%s must name either the test that reads its rows back or why it writes none", route)
		}
	}
	for route := range runWriteActivity {
		if routeActorTypes[route] != middleware.RouteActorTypeRun {
			t.Errorf("%s is not a run route", route)
		}
	}

	member := routeActorUser
	origin := execution.RunOrigin{
		RunID: uuid.New(), CompanyID: routeActorCompany, AgentID: uuid.New(),
		ConversationID: uuid.New(), ConversationKind: "direct", AuthorizedByUserID: &member,
	}
	var created *task.RunScope
	wired := fullyWiredHandler()
	wired.AuthService = authenticatedAuthStub(routeActorUser)
	wired.CompanyService = routeActorCompanyService()
	wired.RunProofResolver = &countingRunProofResolver{origin: origin}
	wired.TaskService = &taskServiceStub{
		create: func(_ context.Context, _ auth.UserPrincipal, input task.CreateInput) (task.MutationResult, error) {
			created = input.Run
			return task.MutationResult{}, nil
		},
	}
	route := "POST /api/v1/companies/:companyId/tasks"
	response := httptest.NewRecorder()
	New(wired).ServeHTTP(response, runRouteProbes()[route].request(t, route))
	if created == nil || created.RunID != origin.RunID || created.AgentID != origin.AgentID ||
		created.UserID != member {
		t.Fatalf(
			"Task create with a proof passed scope %+v (status %d, body %s); want Run %s of Agent %s for %s",
			created,
			response.Code,
			response.Body.String(),
			origin.RunID,
			origin.AgentID,
			member,
		)
	}
}
