package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/task"
)

func TestTaskRoutesCreateAndUpdateStrictContracts(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	taskID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	runID := uuid.MustParse("00000000-0000-4000-8000-000000004711")
	principal := phase4Principal(userID, companyID)
	want := phase4TaskResponse(taskID, companyID, conversationID, agentID, userID)
	service := &taskServiceStub{}
	var createKeys []string
	service.create = func(_ context.Context, gotPrincipal auth.UserPrincipal, input task.CreateInput) (task.MutationResult, error) {
		createKeys = append(createKeys, input.Key)
		// An omitted assignee reaches the service zero: the service settles
		// it to the caller. executedBy names the Agent (ADR 0014).
		if gotPrincipal != principal || (input.Key != "ship_release" && input.Key != "") ||
			input.MilestoneType != task.MilestoneTypeLaunch ||
			input.Assignee != (task.ActorRef{}) ||
			input.ExecutedBy == nil || *input.ExecutedBy != (task.ActorRef{Kind: task.ActorAgent, ID: agentID}) {
			t.Fatalf("Create input = %+v / %+v", gotPrincipal, input)
		}
		return task.MutationResult{Task: want, RunID: runIDPointer(runID)}, nil
	}
	service.update = func(_ context.Context, gotPrincipal auth.UserPrincipal, gotTaskID uuid.UUID, input task.UpdateInput) (task.MutationResult, error) {
		if gotPrincipal != principal || gotTaskID != taskID || input.ExpectedRevision != 1 ||
			input.Status == nil || *input.Status != task.StatusInProgress ||
			input.Assignee == nil || input.Assignee.ID != userID || input.Assignee.Kind != task.ActorUser ||
			input.ExecutedBy == nil || input.ExecutedBy.ID != agentID || !input.ConfirmHandoff {
			t.Fatalf("Update input = %+v / %s / %+v", gotPrincipal, gotTaskID, input)
		}
		updated := want
		updated.Status = task.StatusInProgress
		updated.Revision = 2
		updated.UpdatedAt = updated.UpdatedAt.Add(time.Second)
		return task.MutationResult{
			Task:  updated,
			RunID: runIDPointer(runID),
		}, nil
	}
	handler := phase4TaskHandler(t, principal, service)

	createBody := []byte(`{"projectId":"0198aa00-0000-4000-8000-000000000001","key":"ship_release",` +
		`"milestoneType":"launch","title":"Ship release","description":"Inspect tests",` +
		`"status":"todo","priority":"high","executedBy":{"kind":"agent","id":"` + agentID.String() +
		`"}}`)
	created := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/tasks",
		createBody,
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d; body=%s", created.Code, created.Body.String())
	}
	var createdResponse task.Response
	if err := json.Unmarshal(created.Body.Bytes(), &createdResponse); err != nil || createdResponse.ID != taskID {
		t.Fatalf("create response = %+v, %v", createdResponse, err)
	}
	// The New task dialog and the zen CLI send no key: the body binds and
	// reaches the service with an empty key for it to mint.
	unkeyed := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/tasks",
		[]byte(strings.Replace(string(createBody), `"key":"ship_release",`, "", 1)),
	)
	if unkeyed.Code != http.StatusCreated || len(createKeys) != 2 || createKeys[1] != "" {
		t.Fatalf(
			"unkeyed create status = %d, keys seen %q; body=%s",
			unkeyed.Code,
			createKeys,
			unkeyed.Body.String(),
		)
	}

	updateBody := []byte(`{"expectedRevision":1,"status":"in_progress",` +
		`"assignee":{"kind":"user","id":"` + userID.String() + `"},` +
		`"executedBy":{"kind":"agent","id":"` + agentID.String() + `"},"confirmHandoff":true}`)
	updated := serveBearerJSON(handler, http.MethodPatch,
		"/api/v1/companies/"+companyID.String()+"/tasks/"+taskID.String(), updateBody)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d; body=%s", updated.Code, updated.Body.String())
	}
	var updateResponse map[string]any
	if err := json.Unmarshal(updated.Body.Bytes(), &updateResponse); err != nil ||
		updateResponse["id"] != taskID.String() || updateResponse["runId"] != runID.String() ||
		updateResponse["task"] != nil {
		t.Fatalf("update response = %#v, %v", updateResponse, err)
	}

	unknown := serveBearerJSON(handler, http.MethodPost, "/api/v1/companies/"+companyID.String()+"/tasks",
		append(createBody[:len(createBody)-1], []byte(`,"path":"/private"}`)...))
	assertErrorResponse(t, unknown, http.StatusBadRequest, "invalid_request")
	keyUpdate := serveBearerJSON(handler, http.MethodPatch,
		"/api/v1/companies/"+companyID.String()+"/tasks/"+taskID.String(),
		[]byte(`{"expectedRevision":1,"key":"renamed"}`))
	assertErrorResponse(t, keyUpdate, http.StatusBadRequest, "invalid_request")
	// The assignee is always a member (ADR 0014): an Agent assignee is
	// refused at the boundary, on create and on update alike.
	for _, request := range []struct {
		method string
		path   string
		body   string
	}{
		{
			http.MethodPost, "/api/v1/companies/" + companyID.String() + "/tasks",
			`{"projectId":"0198aa00-0000-4000-8000-000000000001","milestoneType":"launch","title":"Ship release",` +
				`"assignee":{"kind":"agent","id":"` + agentID.String() + `"}}`,
		},
		{
			http.MethodPatch, "/api/v1/companies/" + companyID.String() + "/tasks/" + taskID.String(),
			`{"expectedRevision":1,"assignee":{"kind":"agent","id":"` + agentID.String() + `"}}`,
		},
	} {
		agentAssignee := serveBearerJSON(handler, request.method, request.path, []byte(request.body))
		assertErrorResponse(t, agentAssignee, http.StatusUnprocessableEntity, "validation_failed")
	}
}

func TestTaskRouteStartsAssignedAgentExecution(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	taskID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	runID := uuid.MustParse("00000000-0000-4000-8000-000000004711")
	principal := phase4Principal(userID, companyID)
	want := phase4TaskResponse(taskID, companyID, conversationID, agentID, userID)
	want.Status = task.StatusInProgress
	want.Revision = 4
	service := &taskServiceStub{}
	service.startExecution = func(
		_ context.Context,
		gotPrincipal auth.UserPrincipal,
		gotTaskID uuid.UUID,
		input task.StartExecutionInput,
	) (task.MutationResult, error) {
		if gotPrincipal != principal || gotTaskID != taskID || input.ExpectedRevision != 3 {
			t.Fatalf("StartExecution input = %+v / %s / %+v", gotPrincipal, gotTaskID, input)
		}
		return task.MutationResult{Task: want, RunID: runIDPointer(runID)}, nil
	}
	handler := phase4TaskHandler(t, principal, service)

	response := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/tasks/"+taskID.String()+"/runs",
		[]byte(`{"expectedRevision":3}`),
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("start execution status = %d; body=%s", response.Code, response.Body.String())
	}
	// The run-start receipt is the Task flat with runId beside it — the same
	// shape PATCH returns, so one decode serves both writes.
	var result task.MutationResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil ||
		result.ID != taskID || result.RunID == nil || *result.RunID != runID {
		t.Fatalf("start execution response = %+v, %v", result, err)
	}

	unknown := serveBearerJSON(
		handler,
		http.MethodPost,
		"/api/v1/companies/"+companyID.String()+"/tasks/"+taskID.String()+"/runs",
		[]byte(`{"expectedRevision":3,"mode":"parallel"}`),
	)
	assertErrorResponse(t, unknown, http.StatusBadRequest, "invalid_request")
}

func TestTaskRoutesListUsesStrictFiltersAndNonDisclosingErrors(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	service := &taskServiceStub{}
	service.list = func(
		_ context.Context,
		got auth.UserPrincipal,
		options task.ListOptions,
		run *task.RunScope,
	) (task.Page, error) {
		// A member's own list carries no Run proof and is not scoped.
		if got != principal || run != nil || len(options.Statuses) != 2 ||
			options.Statuses[0] != task.StatusTodo || options.Statuses[1] != task.StatusBlocked ||
			len(options.Priorities) != 2 || options.Priorities[0] != task.PriorityHigh ||
			options.Priorities[1] != task.PriorityUrgent ||
			options.AssigneeUserID == nil || *options.AssigneeUserID != userID ||
			options.ExecutedByAgentID == nil || *options.ExecutedByAgentID != agentID ||
			options.DependenciesResolved == nil ||
			!*options.DependenciesResolved {
			t.Fatalf("List input = %+v / %+v", got, options)
		}
		return task.Page{Tasks: []task.Response{}}, nil
	}
	service.get = func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, run *task.RunScope) (task.Response, error) {
		if run != nil {
			t.Fatalf("a member's own read was scoped to %+v", run)
		}
		return task.Response{}, task.ErrNotFound
	}
	handler := phase4TaskHandler(t, principal, service)

	response := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/tasks?status=todo,blocked&priority=high,urgent"+
			"&assignee_id="+userID.String()+"&executed_by_agent_id="+agentID.String()+
			"&dependencies_resolved=true", nil)
	if response.Code != http.StatusOK || response.Body.String() != `{"items":[]}` {
		t.Fatalf("list response = %d %s", response.Code, response.Body.String())
	}
	for _, query := range []string{
		"?limit=25", "?department_key=engineering",
		// The kind went with the Agent assignee: the filter is the member.
		"?assignee_kind=agent&assignee_id=" + agentID.String(),
		"?assignee_id=not-a-uuid", "?executed_by_agent_id=not-a-uuid",
	} {
		invalid := serveBearerRequest(handler, http.MethodGet,
			"/api/v1/companies/"+companyID.String()+"/tasks"+query, nil)
		assertErrorResponse(t, invalid, http.StatusUnprocessableEntity, "validation_failed")
	}
	retiredFilter := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/tasks?goal_id="+uuid.NewString(), nil)
	assertErrorResponse(t, retiredFilter, http.StatusUnprocessableEntity, "validation_failed")
	notFound := serveBearerRequest(handler, http.MethodGet,
		"/api/v1/companies/"+companyID.String()+"/tasks/"+uuid.Must(uuid.NewV7()).String(), nil)
	assertErrorResponse(t, notFound, http.StatusNotFound, "task_not_found")
	if response := serveBearerRequest(
		handler,
		http.MethodGet,
		"/api/v1/companies/"+uuid.Must(uuid.NewV7()).
			String()+
			"/tasks",
		nil,
	); response.Code != http.StatusNotFound {
		t.Fatalf("cross-Company status = %d; body=%s", response.Code, response.Body.String())
	}
}

func TestTaskCreateTransportCarriesMaximumContractDescription(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	taskID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	description := strings.Repeat("d", 64*1024)
	service := &taskServiceStub{}
	service.create = func(_ context.Context, _ auth.UserPrincipal, input task.CreateInput) (task.MutationResult, error) {
		if input.Description != description ||
			input.MilestoneType != task.MilestoneTypeProduct {
			t.Fatalf(
				"Create input = %+v; description bytes = %d, want %d",
				input,
				len(input.Description),
				len(description),
			)
		}
		now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
		return task.MutationResult{Task: task.Response{
			ID:             taskID,
			Key:            "maximum_description",
			CompanyID:      companyID,
			ConversationID: conversationID,

			MilestoneType: task.MilestoneTypeProduct,
			Number:        1,
			Title:         "Maximum description",
			Description:   description,
			Status:        task.StatusBacklog,
			Priority:      task.PriorityMedium,
			Assignee: task.ActorRef{
				Kind: task.ActorUser,
				ID:   userID,
			},
			ExecutedBy: task.ActorRef{Kind: task.ActorUser, ID: userID},
			Execution:  task.Execution{AssigneeReady: true},
			Creator:    task.ActorRef{Kind: task.ActorUser, ID: userID},
			CreatedAt:  now,
			UpdatedAt:  now,
		}}, nil
	}
	handler := phase4TaskHandler(t, principal, service)
	body := []byte(`{"projectId":"0198aa00-0000-4000-8000-000000000001","key":"maximum_description",` +
		`"milestoneType":"product","title":"Maximum description","description":"` +
		description + `","assignee":{"kind":"user","id":"` + userID.String() + `"}}`)
	response := serveBearerJSON(handler, http.MethodPost, "/api/v1/companies/"+companyID.String()+"/tasks", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("maximum description status = %d; body=%s", response.Code, response.Body.String())
	}
}

// `zen task create` reaches this route through a Run's channel, which sends
// the daemon member's session plus that Run's proof. Cancelling a Run (a
// handoff, a user cancel) deletes its tokens so its next call fails: a proof
// that no longer resolves to a live Run in this Company, or names no member
// the Run works for, refuses the create before anything is written. A live
// proof becomes the Run's scope, which the service holds the create to: the
// Run's member as assignee and, for a Task Run, its Task's Project. Its
// refusal is the Run-scope sentence. A request with no proof is a member's
// own create and is unchanged.
func TestTaskCreateThroughARunNeedsItsLiveProof(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	agentID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	liveTaskID := uuid.Must(uuid.NewV7())
	liveProjectID := uuid.Must(uuid.NewV7())
	live := execution.RunOrigin{
		RunID: uuid.Must(uuid.NewV7()), CompanyID: companyID, AgentID: agentID,
		TaskID: &liveTaskID, ConversationID: uuid.Must(uuid.NewV7()), ConversationKind: "task",
		AuthorizedByUserID: &userID, ProjectID: &liveProjectID,
	}
	liveScope := &task.RunScope{
		AgentID: agentID, UserID: userID, RunID: live.RunID, TaskID: &liveTaskID, ProjectID: &liveProjectID,
	}
	foreign := live
	foreign.CompanyID = uuid.Must(uuid.NewV7())
	unauthorized := live
	unauthorized.AuthorizedByUserID = nil
	colleague := uuid.Must(uuid.NewV7())
	othersRun := live
	othersRun.AuthorizedByUserID = &colleague
	projectless := live
	projectless.ProjectID = nil
	const token = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	body := []byte(`{"projectId":"0198aa00-0000-4000-8000-000000000001","milestoneType":"launch",` +
		`"title":"Follow up","executedBy":{"kind":"agent","id":"` + agentID.String() + `"}}`)

	for _, test := range []struct {
		name       string
		origin     execution.RunOrigin
		token      string
		refusal    error
		wantRun    *task.RunScope
		wantStatus int
		wantCode   string
	}{
		{name: "live Run", origin: live, token: token, wantRun: liveScope, wantStatus: http.StatusCreated},
		{
			name: "a create outside the Run's scope", origin: live, token: token, wantRun: liveScope,
			refusal:    fmt.Errorf("create Task: %w", task.ErrOutsideRunScope),
			wantStatus: http.StatusForbidden, wantCode: "task_outside_run_scope",
		},
		// The stub resolves nothing: the Run was cancelled and its tokens deleted.
		{name: "cancelled Run", token: token, wantStatus: http.StatusUnauthorized, wantCode: "run_token_invalid"},
		{name: "malformed proof", origin: live, token: "brt_stale", wantStatus: http.StatusUnauthorized, wantCode: "run_token_invalid"},
		{name: "another Company's Run", origin: foreign, token: token, wantStatus: http.StatusUnauthorized, wantCode: "run_token_invalid"},
		{name: "Run with no member", origin: unauthorized, token: token, wantStatus: http.StatusUnauthorized, wantCode: "run_token_invalid"},
		// The session carrying the proof must be the Run's authorizer.
		{name: "another member's Run", origin: othersRun, token: token, wantStatus: http.StatusUnauthorized, wantCode: "run_token_invalid"},
		// A Task chat with no Project admits no Run, so its proof names none.
		{name: "Task chat with no Project", origin: projectless, token: token, wantStatus: http.StatusUnauthorized, wantCode: "run_token_invalid"},
		{name: "member session alone", wantStatus: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			created := 0
			service := &taskServiceStub{
				create: func(_ context.Context, _ auth.UserPrincipal, input task.CreateInput) (task.MutationResult, error) {
					if !reflect.DeepEqual(input.Run, test.wantRun) {
						t.Errorf("create scope = %+v, want %+v", input.Run, test.wantRun)
					}
					if test.refusal != nil {
						return task.MutationResult{}, test.refusal
					}
					created++
					return task.MutationResult{
						Task: phase4TaskResponse(
							uuid.Must(uuid.NewV7()),
							companyID,
							uuid.Must(uuid.NewV7()),
							agentID,
							userID,
						),
					}, nil
				},
			}
			handler := New(Handler{
				AuthService: authenticatedAuthStub(userID),
				CompanyService: &companyServiceStub{
					authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
						return principal, nil
					},
				},
				TaskService:      service,
				RunProofResolver: runProofResolverStub{origin: test.origin},
			})
			request := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/companies/"+companyID.String()+"/tasks",
				bytes.NewReader(body),
			)
			request.Header.Set("Authorization", "Bearer access-token")
			request.Header.Set("Content-Type", "application/json")
			if test.token != "" {
				request.Header.Set(runTokenHeader, test.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if test.wantCode != "" {
				assertErrorResponse(t, response, test.wantStatus, test.wantCode)
				if created != 0 {
					t.Fatalf("a refused proof still created %d Task(s)", created)
				}
				if test.refusal != nil && !strings.Contains(response.Body.String(),
					`"detail":"Permission denied. You can only access your own Tasks."`) {
					t.Fatalf("refusal = %s", response.Body.String())
				}
				return
			}
			if response.Code != test.wantStatus || created != 1 {
				t.Fatalf(
					"status = %d, creates = %d; body=%s",
					response.Code,
					created,
					response.Body.String(),
				)
			}
		})
	}
}

// A Run reads only its own Tasks (owner decision, 2026-09-26): the ones its
// Agent does for the member the Run works for, its authorizer (ADR 0014).
// Tagged in a channel and asked to "run BSC-8", Lyra read a Task Eli does
// for Bob and acted out its question on Alice's computer. The reads a Run
// sends through its channel carry the Run's proof; the handler turns that
// proof into the scope the service reads under, refuses a proof that is not
// live, and serves a Task outside the scope as a 403 with one fixed
// sentence — never its content, and no name of the member or Agent it
// belongs to. A member's own read carries no proof and is unscoped.
func TestTaskReadsThroughARunAreScopedToItsOwnTasks(t *testing.T) {
	t.Parallel()

	alice, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	lyraID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(alice, companyID)
	ownID, bobsID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	live := execution.RunOrigin{
		RunID: uuid.Must(uuid.NewV7()), CompanyID: companyID, AgentID: lyraID,
		ConversationID: uuid.Must(uuid.NewV7()), ConversationKind: "channel_agent",
		AuthorizedByUserID: &alice,
	}
	unauthorized := live
	unauthorized.AuthorizedByUserID = nil
	foreign := live
	foreign.CompanyID = uuid.Must(uuid.NewV7())
	// Bob's Run proof sent with Alice's session: the reads would be made
	// with Alice's Project access for a Run that works for Bob.
	bob := uuid.Must(uuid.NewV7())
	othersRun := live
	othersRun.AuthorizedByUserID = &bob
	wantScope := task.RunScope{AgentID: lyraID, UserID: alice, RunID: live.RunID}
	own := phase4TaskResponse(ownID, companyID, uuid.Must(uuid.NewV7()), lyraID, alice)
	// Even an error that mentions the Task, its member and its Agent is
	// served as the one fixed sentence: the body never carries them.
	refusal := fmt.Errorf("BSC-8, which Eli does for Bob Member: %w", task.ErrOutsideRunScope)
	const wantDetail = "Permission denied. You can only access your own Tasks."
	const token = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	var scopes []*task.RunScope
	record := func(run *task.RunScope) {
		if run != nil {
			copied := *run
			run = &copied
		}
		scopes = append(scopes, run)
	}
	service := &taskServiceStub{
		list: func(_ context.Context, got auth.UserPrincipal, _ task.ListOptions, run *task.RunScope) (task.Page, error) {
			if got != principal {
				t.Fatalf("List principal = %+v", got)
			}
			record(run)
			return task.Page{Tasks: []task.Response{own}}, nil
		},
		get: func(_ context.Context, _ auth.UserPrincipal, id uuid.UUID, run *task.RunScope) (task.Response, error) {
			record(run)
			switch id {
			case ownID:
				return own, nil
			case bobsID:
				if run == nil {
					return phase4TaskResponse(
						bobsID,
						companyID,
						uuid.Must(uuid.NewV7()),
						lyraID,
						alice,
					), nil
				}
				return task.Response{}, fmt.Errorf("get Task: %w", refusal)
			}
			return task.Response{}, task.ErrNotFound
		},
		listDependencies: func(_ context.Context, _ auth.UserPrincipal, id uuid.UUID, run *task.RunScope) (task.DependencySet, error) {
			record(run)
			if id == bobsID && run != nil {
				return task.DependencySet{}, refusal
			}
			return task.DependencySet{}, nil
		},
		// A Run names Tasks the way people do. It cannot list a Task that is
		// not its own to find the id, so the two reads resolve the name.
		resolveReference: func(_ context.Context, got auth.UserPrincipal, reference string) (uuid.UUID, error) {
			if got != principal {
				t.Fatalf("ResolveReference principal = %+v", got)
			}
			if reference == "BSC-8" {
				return bobsID, nil
			}
			return uuid.Nil, task.ErrNotFound
		},
	}
	serve := func(origin execution.RunOrigin, path, proof string) *httptest.ResponseRecorder {
		t.Helper()
		handler := New(Handler{
			AuthService: authenticatedAuthStub(alice),
			CompanyService: &companyServiceStub{
				authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
					return principal, nil
				},
			},
			TaskService:      service,
			RunProofResolver: runProofResolverStub{origin: origin},
		})
		request := httptest.NewRequest(http.MethodGet, "/api/v1/companies/"+companyID.String()+path, nil)
		request.Header.Set("Authorization", "Bearer access-token")
		if proof != "" {
			request.Header.Set(runTokenHeader, proof)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	assertScoped := func(name string, want *task.RunScope) {
		t.Helper()
		if len(scopes) != 1 || (want == nil) != (scopes[0] == nil) || want != nil && *scopes[0] != *want {
			t.Fatalf("%s: service read under %+v, want %+v", name, scopes, want)
		}
		scopes = nil
	}

	// The list is the Run's own Tasks: the service reads it under the
	// Run's Agent and member, whatever filters the caller sent.
	listed := serve(live, "/tasks?assignee_id="+uuid.NewString(), token)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), ownID.String()) {
		t.Fatalf("scoped list = %d %s", listed.Code, listed.Body.String())
	}
	assertScoped("list", &wantScope)

	// Its own Task reads as ever.
	got := serve(live, "/tasks/"+ownID.String(), token)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"id":"`+ownID.String()+`"`) {
		t.Fatalf("own Task = %d %s", got.Code, got.Body.String())
	}
	assertScoped("get own", &wantScope)

	// Another member's Task, by id or by the label the Agent was given, is
	// refused with the one fixed sentence, and nothing of it: no content, no
	// label, no name of its member or its Agent.
	for _, reference := range []string{bobsID.String(), "BSC-8"} {
		for _, path := range []string{"/tasks/" + reference, "/tasks/" + reference + "/dependencies"} {
			refused := serve(live, path, token)
			assertErrorResponse(t, refused, http.StatusForbidden, "task_outside_run_scope")
			var body map[string]any
			if err := json.Unmarshal(
				refused.Body.Bytes(),
				&body,
			); err != nil ||
				body["detail"] != wantDetail ||
				len(body) != 3 {
				t.Fatalf("GET %s refusal = %s", path, refused.Body.String())
			}
			assertRefusalNamesNoOne(t, refused)
			assertScoped("refused "+path, &wantScope)
		}
	}

	// A Task that does not exist is still a plain 404, by id or by name.
	for _, path := range []string{"/tasks/" + uuid.NewString(), "/tasks/BSC-99", "/tasks/BSC-99/dependencies"} {
		assertErrorResponse(t, serve(live, path, token), http.StatusNotFound, "task_not_found")
		scopes = nil
	}

	// A proof that is not live in this Company, or proves no member the Run
	// works for, is refused before anything is read.
	for name, test := range map[string]struct {
		origin execution.RunOrigin
		proof  string
	}{
		"finished Run":          {proof: token},
		"malformed proof":       {origin: live, proof: "brt_stale"},
		"another Company's Run": {origin: foreign, proof: token},
		"Run with no member":    {origin: unauthorized, proof: token},
		"another member's Run":  {origin: othersRun, proof: token},
	} {
		for _, path := range []string{"/tasks", "/tasks/" + ownID.String(), "/tasks/" + ownID.String() + "/dependencies"} {
			assertErrorResponse(
				t,
				serve(test.origin, path, test.proof),
				http.StatusUnauthorized,
				"run_token_invalid",
			)
			if len(scopes) != 0 {
				t.Fatalf("%s: GET %s read Tasks under a refused proof: %+v", name, path, scopes)
			}
		}
	}

	// A member's own session carries no proof: every read is unscoped, and
	// a colleague's Task reads in full.
	for _, path := range []string{"/tasks", "/tasks/" + bobsID.String(), "/tasks/BSC-8/dependencies"} {
		if response := serve(live, path, ""); response.Code != http.StatusOK {
			t.Fatalf("member GET %s = %d %s", path, response.Code, response.Body.String())
		}
		assertScoped("member "+path, nil)
	}
}

// A Task's workspace and results carry the whole Task — its description,
// messages and Run output — so they answer a Run's proof the way the Task
// read does. The Run channel does not forward them today; this is the fence
// behind the channel's own, in case a path ever reaches them again.
func TestTaskWorkspaceAndResultsThroughARunAreScopedToItsOwnTasks(t *testing.T) {
	t.Parallel()

	alice, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	lyraID := uuid.Must(uuid.NewV7())
	principal := phase4Principal(alice, companyID)
	ownID, bobsID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	live := execution.RunOrigin{
		RunID: uuid.Must(uuid.NewV7()), CompanyID: companyID, AgentID: lyraID,
		ConversationID: uuid.Must(uuid.NewV7()), ConversationKind: "channel_agent",
		AuthorizedByUserID: &alice,
	}
	unauthorized := live
	unauthorized.AuthorizedByUserID = nil
	wantScope := task.RunScope{AgentID: lyraID, UserID: alice, RunID: live.RunID}
	// Even an error that mentions the Task, its member and its Agent is
	// served as the one fixed sentence: the body never carries them.
	refusal := fmt.Errorf("BSC-8, which Eli does for Bob Member: %w", task.ErrOutsideRunScope)
	const wantDetail = "Permission denied. You can only access your own Tasks."
	const token = "brt_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	var scopes []*task.RunScope
	record := func(run *task.RunScope) {
		if run != nil {
			copied := *run
			run = &copied
		}
		scopes = append(scopes, run)
	}
	resultReads := 0
	service := &taskServiceStub{
		workspace: func(
			_ context.Context,
			got auth.UserPrincipal,
			id uuid.UUID,
			_ task.WorkspaceOptions,
			run *task.RunScope,
		) (task.Workspace, error) {
			if got != principal {
				t.Fatalf("Workspace principal = %+v", got)
			}
			record(run)
			if id == bobsID && run != nil {
				return task.Workspace{}, fmt.Errorf("read Task workspace: %w", refusal)
			}
			return task.Workspace{
				Task: task.Response{ID: id, CompanyID: companyID},
				Conversation: task.WorkspaceConversation{
					Response: conversation.Response{
						ID: uuid.Must(
							uuid.NewV7(),
						),
						CompanyID: companyID,
						Kind:      conversation.KindTask,
					},
					TaskID: id,
				},
			}, nil
		},
		get: func(_ context.Context, _ auth.UserPrincipal, id uuid.UUID, run *task.RunScope) (task.Response, error) {
			record(run)
			if id == bobsID && run != nil {
				return task.Response{}, fmt.Errorf("get Task: %w", refusal)
			}
			return phase4TaskResponse(id, companyID, uuid.Must(uuid.NewV7()), lyraID, alice), nil
		},
	}
	deliverables := &taskResultsStub{
		deliverableDownloadStub: &deliverableDownloadStub{},
		list: func(_ context.Context, got auth.UserPrincipal, _ uuid.UUID) (execution.TaskResultPage, error) {
			if got != principal {
				t.Fatalf("ListTaskResults principal = %+v", got)
			}
			resultReads++
			return execution.TaskResultPage{}, nil
		},
	}
	serve := func(origin execution.RunOrigin, path, proof string) *httptest.ResponseRecorder {
		t.Helper()
		handler := New(Handler{
			AuthService: authenticatedAuthStub(alice),
			CompanyService: &companyServiceStub{
				authorize: func(context.Context, auth.UserPrincipal, uuid.UUID) (auth.UserPrincipal, error) {
					return principal, nil
				},
			},
			TaskService:        service,
			DeliverableService: deliverables,
			RunProofResolver:   runProofResolverStub{origin: origin},
		})
		request := httptest.NewRequest(http.MethodGet, "/api/v1/companies/"+companyID.String()+path, nil)
		request.Header.Set("Authorization", "Bearer access-token")
		if proof != "" {
			request.Header.Set(runTokenHeader, proof)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}
	reset := func() { scopes, resultReads = nil, 0 }

	// The Run's own Task: its workspace and results read under the scope.
	for _, path := range []string{"/tasks/" + ownID.String() + "/workspace", "/tasks/" + ownID.String() + "/results"} {
		if response := serve(live, path, token); response.Code != http.StatusOK {
			t.Fatalf("own GET %s = %d %s", path, response.Code, response.Body.String())
		}
		if len(scopes) != 1 || scopes[0] == nil || *scopes[0] != wantScope {
			t.Fatalf("own GET %s read under %+v, want %+v", path, scopes, wantScope)
		}
		reset()
	}

	// Another member's Task: refused with the one fixed sentence, and its
	// results are never read.
	for _, path := range []string{"/tasks/" + bobsID.String() + "/workspace", "/tasks/" + bobsID.String() + "/results"} {
		refused := serve(live, path, token)
		assertErrorResponse(t, refused, http.StatusForbidden, "task_outside_run_scope")
		var body map[string]any
		if err := json.Unmarshal(
			refused.Body.Bytes(),
			&body,
		); err != nil || body["detail"] != wantDetail ||
			len(body) != 3 {
			t.Fatalf("GET %s refusal = %s", path, refused.Body.String())
		}
		assertRefusalNamesNoOne(t, refused)
		if resultReads != 0 {
			t.Fatalf("GET %s read the results of a Task outside the Run", path)
		}
		reset()
	}

	// A proof that is not live, or proves no member, is refused before
	// anything is read.
	for name, origin := range map[string]execution.RunOrigin{
		"finished Run":       {},
		"Run with no member": unauthorized,
	} {
		for _, path := range []string{"/tasks/" + ownID.String() + "/workspace", "/tasks/" + ownID.String() + "/results"} {
			assertErrorResponse(t, serve(origin, path, token), http.StatusUnauthorized, "run_token_invalid")
			if len(scopes) != 0 || resultReads != 0 {
				t.Fatalf("%s: GET %s read under a refused proof", name, path)
			}
		}
	}

	// A member's own session is unscoped: the workspace reads with no scope
	// and the results need no Task read first.
	for _, path := range []string{"/tasks/" + bobsID.String() + "/workspace", "/tasks/" + bobsID.String() + "/results"} {
		if response := serve(live, path, ""); response.Code != http.StatusOK {
			t.Fatalf("member GET %s = %d %s", path, response.Code, response.Body.String())
		}
		if strings.HasSuffix(path, "/workspace") && (len(scopes) != 1 || scopes[0] != nil) ||
			strings.HasSuffix(path, "/results") && (len(scopes) != 0 || resultReads != 1) {
			t.Fatalf("member GET %s: scopes %+v, result reads %d", path, scopes, resultReads)
		}
		reset()
	}
}

// assertRefusalNamesNoOne fails when a Run's refused Task read carries the
// refused Task's label or the name of its member or its Agent.
func assertRefusalNamesNoOne(t *testing.T, refused *httptest.ResponseRecorder) {
	t.Helper()
	for _, name := range []string{"BSC-8", "Bob Member", "Eli"} {
		if strings.Contains(refused.Body.String(), name) {
			t.Fatalf("refusal names %q: %s", name, refused.Body.String())
		}
	}
}

// taskResultsStub serves a Task's results; every other deliverable call is
// unexpected.
type taskResultsStub struct {
	*deliverableDownloadStub
	list func(context.Context, auth.UserPrincipal, uuid.UUID) (execution.TaskResultPage, error)
}

func (s *taskResultsStub) ListTaskResults(
	ctx context.Context,
	principal auth.UserPrincipal,
	taskID uuid.UUID,
	_ execution.TaskResultListOptions,
) (execution.TaskResultPage, error) {
	return s.list(ctx, principal, taskID)
}

func TestTaskDependencyRouteIsReadOnly(t *testing.T) {
	t.Parallel()
	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	dependentID, prerequisiteID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	now := time.Date(2026, time.August, 4, 1, 2, 3, 0, time.UTC)
	dependency := task.DependencyResponse{
		Task: task.DependencyTask{
			ID: prerequisiteID, Key: "approve_spec", Title: "Approve spec",
			Status: task.StatusDone,
			Assignee: task.ActorRef{
				Kind: task.ActorUser, ID: userID, DisplayName: "Quy",
			},
			ExecutedBy: task.ActorRef{
				Kind: task.ActorAgent, ID: uuid.Must(uuid.NewV7()), DisplayName: "Ada",
			},
			Revision: 3,
		},
		CreatedAt: now,
		Revision:  1,
	}
	service := &taskServiceStub{
		listDependencies: func(
			_ context.Context,
			got auth.UserPrincipal,
			gotTaskID uuid.UUID,
			run *task.RunScope,
		) (task.DependencySet, error) {
			if got != principal || gotTaskID != dependentID || run != nil {
				t.Fatalf("ListDependencies() = %+v / %s / %+v", got, gotTaskID, run)
			}
			return task.DependencySet{BlockedBy: []task.DependencyResponse{dependency}}, nil
		},
	}
	handler := phase4TaskHandler(t, principal, service)
	base := "/api/v1/companies/" + companyID.String() + "/tasks/" + dependentID.String() + "/dependencies"

	listed := serveBearerRequest(handler, http.MethodGet, base, nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"blockedBy"`) ||
		!strings.Contains(listed.Body.String(), `"blocks":[]`) ||
		!strings.Contains(listed.Body.String(), `"id":"`+prerequisiteID.String()+`"`) ||
		strings.Contains(listed.Body.String(), `"companyId"`) ||
		strings.Contains(listed.Body.String(), `"dependentTaskId"`) ||
		strings.Contains(listed.Body.String(), `"prerequisiteTaskId"`) {
		t.Fatalf("list dependencies response = %d %s", listed.Code, listed.Body.String())
	}

	for _, request := range []struct {
		method, path string
	}{
		{http.MethodPost, base},
		{http.MethodDelete, base + "/" + prerequisiteID.String()},
	} {
		response := serveBearerRequest(handler, request.method, request.path, nil)
		if response.Code != http.StatusNotFound {
			t.Fatalf(
				"%s %s status = %d, want %d",
				request.method,
				request.path,
				response.Code,
				http.StatusNotFound,
			)
		}
	}
}

func TestTaskDependencyGraphRouteForwardsDocumentedFilters(t *testing.T) {
	t.Parallel()
	userID, companyID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	dependentID, prerequisiteID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	principal := phase4Principal(userID, companyID)
	now := time.Date(2026, time.August, 5, 1, 2, 3, 0, time.UTC)
	var receivedOptions []task.DependencyGraphOptions
	service := &taskServiceStub{
		listDependencyGraph: func(
			_ context.Context,
			got auth.UserPrincipal,
			options task.DependencyGraphOptions,
		) (task.DependencyGraph, error) {
			if got != principal {
				t.Fatalf("ListDependencyGraph() = %+v", got)
			}
			receivedOptions = append(receivedOptions, options)
			return task.DependencyGraph{
				Dependencies: []task.DependencyGraphEdge{
					{
						CompanyID:          companyID,
						DependentTaskID:    dependentID,
						PrerequisiteTaskID: prerequisiteID,
						CreatedAt:          now,
					},
				},
				Tasks: []task.DependencyGraphTask{
					{
						ID:     dependentID,
						Key:    "build_api",
						Title:  "Build API",
						Status: task.StatusTodo,
					},
					{
						ID:     prerequisiteID,
						Key:    "approve_spec",
						Title:  "Approve spec",
						Status: task.StatusDone,
					},
				},
			}, nil
		},
	}
	handler := phase4TaskHandler(t, principal, service)
	path := "/api/v1/companies/" + companyID.String() + "/task-dependencies"

	response := serveBearerRequest(handler, http.MethodGet, path, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"items"`) ||
		strings.Contains(response.Body.String(), `"dependencies"`) {
		t.Fatalf("dependency graph response = %d %s", response.Code, response.Body.String())
	}

	if len(receivedOptions) != 1 || receivedOptions[0] != (task.DependencyGraphOptions{}) {
		t.Fatalf("ListDependencyGraph() options = %#v", receivedOptions)
	}
	// The graph is Company-wide: there is no narrower scope to ask for.
	for _, query := range []string{"?department_key=engineering", "?agent_id=" + companyID.String()} {
		invalid := serveBearerRequest(handler, http.MethodGet, path+query, nil)
		assertErrorResponse(t, invalid, http.StatusUnprocessableEntity, "validation_failed")
	}
}

func TestTaskWorkspaceRouteParsesBoundedMessageWindow(t *testing.T) {
	t.Parallel()

	userID := uuid.Must(uuid.NewV7())
	companyID := uuid.Must(uuid.NewV7())
	taskID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	messageID := uuid.Must(uuid.NewV7())
	createdAt := time.Date(2026, time.August, 5, 1, 2, 3, 0, time.UTC)
	principal := phase4Principal(userID, companyID)
	limits := []int{}
	service := &taskServiceStub{
		workspace: func(
			_ context.Context,
			got auth.UserPrincipal,
			gotTaskID uuid.UUID,
			options task.WorkspaceOptions,
			run *task.RunScope,
		) (task.Workspace, error) {
			if got != principal || gotTaskID != taskID || run != nil {
				t.Fatalf("Workspace() = %+v / %s / %+v", got, gotTaskID, run)
			}
			limits = append(limits, options.MessageLimit)
			return task.Workspace{
				Task: task.Response{ID: taskID, CompanyID: companyID},
				Conversation: task.WorkspaceConversation{
					Response: conversation.Response{
						ID: conversationID, CompanyID: companyID, Kind: conversation.KindTask,
					},
					TaskID: taskID,
				},
				Messages: []conversation.MessageView{{
					Message: conversation.MessageResponse{
						ID: messageID, ConversationID: conversationID, Seq: 7,
						Type: conversation.ItemTypeUserMessage,
						Author: conversation.ActorRef{
							Kind:        conversation.ActorUser,
							ID:          userID,
							DisplayName: "Dana",
						},
						RunID:   uuid.MustParse("00000000-0000-4000-8000-000000004711"),
						Content: "Ship release", CreatedAt: createdAt,
					},
				}},
			}, nil
		},
	}
	handler := phase4TaskHandler(t, principal, service)
	path := "/api/v1/companies/" + companyID.String() + "/tasks/" + taskID.String() + "/workspace"
	wantMessages := `[{"id":"` + messageID.String() + `","conversationId":"` + conversationID.String() +
		`","seq":7,"type":"user_message","author":{"id":"` + userID.String() +
		`","kind":"user","name":"Dana","avatarUrl":null},"runId":"00000000-0000-4000-8000-000000004711","content":"Ship release","createdAt":"2026-08-05T01:02:03Z","attachments":[],"attachmentsWithheld":false}]`
	for _, suffix := range []string{"", "?message_limit=7"} {
		response := serveBearerRequest(handler, http.MethodGet, path+suffix, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET workspace%s = %d %s", suffix, response.Code, response.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil ||
			string(body["messages"]) == "null" || string(body["runTimelines"]) == "null" ||
			string(body["activeRuns"]) == "null" {
			t.Fatalf("workspace response = %s, error = %v", response.Body.String(), err)
		}
		if got := string(body["messages"]); got != wantMessages {
			t.Fatalf("workspace messages = %s, want %s", got, wantMessages)
		}
		if !strings.Contains(string(body["conversation"]), `"taskId":"`+taskID.String()+`"`) {
			t.Fatalf("workspace Conversation = %s", body["conversation"])
		}
	}
	if len(limits) != 2 || limits[0] != 50 || limits[1] != 7 {
		t.Fatalf("Workspace message limits = %v, want [50 7]", limits)
	}
	for _, query := range []string{
		"?message_limit=0", "?message_limit=101", "?message_limit=one",
		"?message_limit=2&message_limit=3", "?unknown=true",
	} {
		response := serveBearerRequest(handler, http.MethodGet, path+query, nil)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
}

func (s *taskServiceStub) ResultSelection(
	context.Context,
	auth.UserPrincipal,
	uuid.UUID,
) (task.ResultSelection, error) {
	return task.ResultSelection{OutputIDs: []uuid.UUID{}}, nil
}

type taskServiceStub struct {
	TaskService
	create              func(context.Context, auth.UserPrincipal, task.CreateInput) (task.MutationResult, error)
	list                func(context.Context, auth.UserPrincipal, task.ListOptions, *task.RunScope) (task.Page, error)
	get                 func(context.Context, auth.UserPrincipal, uuid.UUID, *task.RunScope) (task.Response, error)
	resolveReference    func(context.Context, auth.UserPrincipal, string) (uuid.UUID, error)
	update              func(context.Context, auth.UserPrincipal, uuid.UUID, task.UpdateInput) (task.MutationResult, error)
	startExecution      func(context.Context, auth.UserPrincipal, uuid.UUID, task.StartExecutionInput) (task.MutationResult, error)
	markFailed          func(context.Context, auth.UserPrincipal, execution.RunOrigin, task.FailureInput) (task.Response, error)
	listDependencies    func(context.Context, auth.UserPrincipal, uuid.UUID, *task.RunScope) (task.DependencySet, error)
	listDependencyGraph func(
		context.Context,
		auth.UserPrincipal,
		task.DependencyGraphOptions,
	) (task.DependencyGraph, error)
	workspace func(
		context.Context,
		auth.UserPrincipal,
		uuid.UUID,
		task.WorkspaceOptions,
		*task.RunScope,
	) (task.Workspace, error)
}

func (s *taskServiceStub) MarkFailed(
	ctx context.Context,
	principal auth.UserPrincipal,
	origin execution.RunOrigin,
	input task.FailureInput,
) (task.Response, error) {
	if s.markFailed == nil {
		return task.Response{}, errors.New("unexpected Task MarkFailed")
	}
	return s.markFailed(ctx, principal, origin, input)
}

func (s *taskServiceStub) Workspace(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	options task.WorkspaceOptions,
	run *task.RunScope,
) (task.Workspace, error) {
	if s.workspace == nil {
		return task.Workspace{}, errors.New("unexpected Task Workspace")
	}
	return s.workspace(ctx, principal, id, options, run)
}

func (s *taskServiceStub) ListDependencies(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	run *task.RunScope,
) (task.DependencySet, error) {
	if s.listDependencies == nil {
		return task.DependencySet{}, errors.New("unexpected Task ListDependencies")
	}
	return s.listDependencies(ctx, principal, id, run)
}

func (s *taskServiceStub) ListDependencyGraph(
	ctx context.Context,
	principal auth.UserPrincipal,
	options task.DependencyGraphOptions,
) (task.DependencyGraph, error) {
	if s.listDependencyGraph == nil {
		return task.DependencyGraph{}, errors.New("unexpected Task ListDependencyGraph")
	}
	return s.listDependencyGraph(ctx, principal, options)
}

func (s *taskServiceStub) Create(
	ctx context.Context,
	principal auth.UserPrincipal,
	input task.CreateInput,
) (task.MutationResult, error) {
	if s.create == nil {
		return task.MutationResult{}, errors.New("unexpected Task Create")
	}
	return s.create(ctx, principal, input)
}

func (s *taskServiceStub) List(
	ctx context.Context,
	principal auth.UserPrincipal,
	options task.ListOptions,
	run *task.RunScope,
) (task.Page, error) {
	if s.list == nil {
		return task.Page{}, errors.New("unexpected Task List")
	}
	return s.list(ctx, principal, options, run)
}

func (s *taskServiceStub) Get(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	run *task.RunScope,
) (task.Response, error) {
	if s.get == nil {
		return task.Response{}, errors.New("unexpected Task Get")
	}
	return s.get(ctx, principal, id, run)
}

func (s *taskServiceStub) ResolveReference(
	ctx context.Context,
	principal auth.UserPrincipal,
	reference string,
) (uuid.UUID, error) {
	if s.resolveReference == nil {
		return uuid.Nil, errors.New("unexpected Task ResolveReference")
	}
	return s.resolveReference(ctx, principal, reference)
}

func (s *taskServiceStub) Update(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	input task.UpdateInput,
) (task.MutationResult, error) {
	if s.update == nil {
		return task.MutationResult{}, errors.New("unexpected Task Update")
	}
	return s.update(ctx, principal, id, input)
}

func (s *taskServiceStub) StartExecution(
	ctx context.Context,
	principal auth.UserPrincipal,
	id uuid.UUID,
	input task.StartExecutionInput,
) (task.MutationResult, error) {
	if s.startExecution == nil {
		return task.MutationResult{}, errors.New("unexpected Task StartExecution")
	}
	return s.startExecution(ctx, principal, id, input)
}

func phase4TaskHandler(t *testing.T, principal auth.UserPrincipal, service TaskService) http.Handler {
	t.Helper()
	companyService := &companyServiceStub{
		authorize: func(_ context.Context, user auth.UserPrincipal, companyID uuid.UUID) (auth.UserPrincipal, error) {
			if user.UserID != principal.UserID || companyID != principal.CompanyID {
				return auth.UserPrincipal{}, company.ErrNotFound
			}
			return principal, nil
		},
	}
	return New(
		Handler{
			AuthService:    authenticatedAuthStub(principal.UserID),
			CompanyService: companyService,
			TaskService:    service,
		},
	)
}

func phase4Principal(userID, companyID uuid.UUID) auth.UserPrincipal {
	return auth.UserPrincipal{
		UserID:    userID,
		SessionID: uuid.MustParse("0197f590-a482-78f4-827b-d1db1598ae19"),
		AuthKind:  auth.TokenKindBearer,
		CompanyID: companyID,
		Role:      auth.CompanyRoleMember,
	}
}

func phase4TaskResponse(taskID, companyID, conversationID, agentID, creatorID uuid.UUID) task.Response {
	now := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	return task.Response{
		ID:             taskID,
		Key:            "ship_release",
		CompanyID:      companyID,
		ConversationID: conversationID,
		Number:         7,
		Title:          "Ship release",
		Description:    "Inspect tests",
		Status:         task.StatusTodo,
		Priority:       task.PriorityHigh,
		Assignee: task.ActorRef{
			Kind: task.ActorUser,
			ID:   creatorID,
		},
		ExecutedBy: task.ActorRef{
			Kind: task.ActorAgent,
			ID:   agentID,
		},
		Execution: task.Execution{AssigneeReady: true},
		Creator:   task.ActorRef{Kind: task.ActorUser, ID: creatorID},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestTaskResultPolicyRequiresExplicitBooleans(t *testing.T) {
	t.Parallel()
	companyID, userID, taskID := uuid.New(), uuid.New(), uuid.New()
	service := &taskServiceStub{}
	service.update = func(_ context.Context, _ auth.UserPrincipal, _ uuid.UUID, input task.UpdateInput) (task.MutationResult, error) {
		if input.ResultPolicy == nil || input.ResultPolicy.ProposalOnly || input.ResultPolicy.ReviewRequired {
			t.Fatalf("explicit false policy = %+v", input.ResultPolicy)
		}
		return task.MutationResult{}, nil
	}
	handler := phase4TaskHandler(t, phase4Principal(userID, companyID), service)
	path := "/api/v1/companies/" + companyID.String() + "/tasks/" + taskID.String()
	for _, policy := range []string{`{}`, `{"proposalOnly":false}`, `{"reviewRequired":false}`, `{"proposalOnly":null,"reviewRequired":false}`} {
		response := serveBearerJSON(
			handler,
			http.MethodPatch,
			path,
			[]byte(`{"expectedRevision":1,"resultPolicy":`+policy+`}`),
		)
		assertErrorResponse(t, response, http.StatusUnprocessableEntity, "validation_failed")
	}
	response := serveBearerJSON(
		handler,
		http.MethodPatch,
		path,
		[]byte(`{"expectedRevision":1,"resultPolicy":{"proposalOnly":false,"reviewRequired":false}}`),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("explicit false policy: %d %s", response.Code, response.Body.String())
	}
}
