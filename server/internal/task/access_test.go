package task

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// A Task read carries the reader's permissions on its Project, merged from
// the facts the projection returned exactly as a Project read merges them
// (ADR 0017): the stored rows, the Company-visible floor, and the management
// names for a projects.manage role.
func TestTaskAccessMergesLikeProjectAccess(t *testing.T) {
	t.Parallel()

	read := string(auth.ProjectPermissionRead)
	write := string(auth.ProjectPermissionTasksWrite)
	cases := []struct {
		name       string
		visibility string
		via        string
		management bool
		granted    []string
		want       []auth.ProjectPermission
	}{
		{
			name:       "restricted, read only",
			visibility: string(auth.ProjectVisibilityRestricted),
			via:        string(auth.ProjectAccessViaGrant),
			granted:    []string{read},
			want:       []auth.ProjectPermission{auth.ProjectPermissionRead},
		},
		{
			name:       "restricted, reader and writer",
			visibility: string(auth.ProjectVisibilityRestricted),
			via:        string(auth.ProjectAccessViaGrant),
			granted:    []string{read, write},
			want:       []auth.ProjectPermission{auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite},
		},
		{
			name:       "company-visible floor with no rows",
			visibility: string(auth.ProjectVisibilityCompany),
			via:        string(auth.ProjectAccessViaCompany),
			granted:    []string{},
			want:       sortedPermissions(auth.CompanyProjectDefaultPermissions()),
		},
		{
			name:       "reader who also manages",
			visibility: string(auth.ProjectVisibilityRestricted),
			via:        string(auth.ProjectAccessViaGrant),
			management: true,
			granted:    []string{read},
			want: sortedPermissions(append(
				[]auth.ProjectPermission{auth.ProjectPermissionRead},
				auth.ManagementProjectPermissions()...,
			)),
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			row := generated.ListTaskProjectionsForActiveMemberRow{
				Task: generated.Task{
					ID: uuid.New(), CompanyID: uuid.New(), ProjectID: uuid.New(),
					AssigneeUserID: uuid.New(), Status: string(StatusTodo), Priority: string(PriorityMedium),
				},
				ProjectVisibility:    test.visibility,
				ProjectAccessVersion: 3,
				AccessVia:            test.via,
				AccessManagement:     test.management,
				GrantedPermissions:   test.granted,
			}
			value := responseFromProjectionRow(row)
			if value.Access == nil {
				t.Fatal("a Task read for a person carries no access")
			}
			if !slices.Equal(value.Access.Permissions, test.want) {
				t.Fatalf("access.permissions = %v, want %v", value.Access.Permissions, test.want)
			}
		})
	}
}

// assigneeReady is read off unready_reason alone, on a person's read and on
// the frames' system read, so the two can never disagree.
func TestTaskReadinessFollowsTheUnreadyReason(t *testing.T) {
	t.Parallel()

	task := generated.Task{
		ID: uuid.New(), CompanyID: uuid.New(), ProjectID: uuid.New(),
		AssigneeUserID: uuid.New(), Status: string(StatusTodo), Priority: string(PriorityMedium),
	}
	for _, reason := range []string{"", "assignee_cannot_run", "assignee_inactive", "not_bound"} {
		reads := map[string]Response{
			"person": responseFromProjectionRow(generated.ListTaskProjectionsForActiveMemberRow{
				Task: task, ProjectVisibility: string(auth.ProjectVisibilityRestricted),
				AccessVia: string(auth.ProjectAccessViaGrant), GrantedPermissions: []string{},
				UnreadyReason: reason,
			}),
			"system": responseFromSystemProjectionRow(generated.GetTaskProjectionForSystemRow{
				Task: task, UnreadyReason: reason,
			}),
		}
		for who, value := range reads {
			execution := value.Execution
			if reason == "" {
				if !execution.AssigneeReady || execution.UnreadyReason != nil {
					t.Errorf("%s read, no reason: execution = %+v; want ready with a null reason", who, execution)
				}
				continue
			}
			if execution.AssigneeReady || execution.UnreadyReason == nil || *execution.UnreadyReason != reason {
				t.Errorf("%s read, reason %q: execution = %+v; want not ready with that reason", who, reason, execution)
			}
		}
	}
}

// The permissions of whoever read the Task stay off the frame other people
// receive.
func TestTaskFrameCarriesNoReaderAccess(t *testing.T) {
	t.Parallel()

	value := Response{ID: uuid.New(), Access: &Access{Permissions: []auth.ProjectPermission{auth.ProjectPermissionRead}}}
	// The audience is already decided, so staging reads no database here;
	// TestStampProjectAudiencesDecidesEachBranch covers deciding it.
	value.Audience = &auth.ProjectAudience{CompanyWide: true}
	// Every Task frame constructor, and a frame built by hand and staged:
	// stageTaskEvent is the one place every Task frame passes.
	runActor := events.RunActor(uuid.NewString(), uuid.NewString(), uuid.NewString())
	for name, event := range map[string]events.Event{
		"newTaskEvent":             newTaskEvent(uuid.New(), EventUpdated, value),
		"newTaskEventForActor":     newTaskEventForActor(events.UserActor(uuid.NewString()), EventCreated, value),
		"newAgentTaskUpdatedEvent": newAgentTaskUpdatedEvent(uuid.New(), runActor, value),
		"hand-built and staged":    {EntityType: Topic, Name: EventUpdated, Payload: value},
	} {
		var pending []events.Event
		if err := stageTaskEvent(context.Background(), nil, &pending, event); err != nil {
			t.Fatal(err)
		}
		payload, ok := pending[0].Payload.(Response)
		if !ok {
			t.Fatalf("%s: payload = %T, want Response", name, pending[0].Payload)
		}
		if payload.Access != nil {
			t.Errorf("%s: the staged frame carries access %v", name, payload.Access)
		}
		if name != "hand-built and staged" && event.Payload.(Response).Access != nil {
			t.Errorf("%s: the constructor left access on the frame", name)
		}
	}
	if value.Access == nil {
		t.Fatal("building the frames cleared the caller's own value")
	}
}

// A person's write binds the fence with their id, the permission and the
// three sets the function needs; a system writer binds NULL, and the
// statement skips the fence.
func TestProjectWriteFenceParams(t *testing.T) {
	t.Parallel()

	userID, permission, sets := projectWriteFence{}.params()
	if userID.Valid || permission != "" {
		t.Fatalf("system fence = (%v, %q), want NULL", userID, permission)
	}
	if sets.CompanyDefaultPermissions == nil || sets.ManagementPermissions == nil || sets.ManagementRoles == nil {
		t.Fatalf("system fence sets = %+v, want empty arrays, not NULL", sets)
	}
	person := uuid.New()
	userID, permission, sets = personWriteFence(person, auth.ProjectPermissionTasksWrite).params()
	if !userID.Valid || userID.UUID != person || permission != string(auth.ProjectPermissionTasksWrite) {
		t.Fatalf("person fence = (%v, %q)", userID, permission)
	}
	want := auth.ProjectPermissionHeldSets()
	if !slices.Equal(sets.CompanyDefaultPermissions, want.CompanyDefaultPermissions) ||
		!slices.Equal(sets.ManagementPermissions, want.ManagementPermissions) ||
		!slices.Equal(sets.ManagementRoles, want.ManagementRoles) {
		t.Fatalf("person fence sets = %+v, want %+v", sets, want)
	}
}

// A write receipt always lists the Tasks whose assignee is outside the
// Project, as [] rather than null when there are none.
func TestMutationResponseListsOutsideAssigneesNeverNull(t *testing.T) {
	t.Parallel()

	outside := uuid.New()
	for name, test := range map[string]struct {
		result MutationResult
		want   string
	}{
		"none": {MutationResult{}, `[]`},
		"one":  {MutationResult{AssigneeOutsideProject: []uuid.UUID{outside}}, `["` + outside.String() + `"]`},
	} {
		body, err := json.Marshal(NewMutationResponse(test.result))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		if got := string(fields["assigneeOutsideProjectTaskIds"]); got != test.want {
			t.Errorf("%s: assigneeOutsideProjectTaskIds = %s, want %s", name, got, test.want)
		}
	}
}

func TestTaskProjectDecisions(t *testing.T) {
	t.Parallel()

	reader := auth.ProjectAccess{
		Via:         auth.ProjectAccessViaGrant,
		Permissions: []auth.ProjectPermission{auth.ProjectPermissionRead},
	}
	writer := auth.ProjectAccess{
		Via: auth.ProjectAccessViaGrant,
		Permissions: []auth.ProjectPermission{
			auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite,
		},
	}
	// A projects.manage holder who cannot read holds the management names
	// and still sees no Task.
	manager := auth.ProjectAccess{Management: true, Permissions: auth.ManagementProjectPermissions()}
	stranger := auth.ProjectAccess{}

	cases := []struct {
		name       string
		access     auth.ProjectAccess
		permission auth.ProjectPermission
		want       error
	}{
		{"writer writes", writer, auth.ProjectPermissionTasksWrite, nil},
		{"reader cannot write", reader, auth.ProjectPermissionTasksWrite, auth.ErrProjectPermissionRequired},
		{"manager without read sees nothing", manager, auth.ProjectPermissionTasksWrite, ErrNotFound},
		{"manager without read cannot archive a Task either", manager, auth.ProjectPermissionArchive, ErrNotFound},
		{"stranger sees nothing", stranger, auth.ProjectPermissionTasksWrite, ErrNotFound},
	}
	for _, test := range cases {
		if err := requireProjectPermission(test.access, test.permission); !errors.Is(err, test.want) || (test.want == nil && err != nil) {
			t.Errorf("%s: requireProjectPermission = %v, want %v", test.name, err, test.want)
		}
	}

	if err := requireAssigneeReads(reader); err != nil {
		t.Errorf("a reader as assignee = %v, want nil", err)
	}
	for name, access := range map[string]auth.ProjectAccess{"manager": manager, "stranger": stranger} {
		if err := requireAssigneeReads(access); !errors.Is(err, ErrAssigneeOutsideProject) {
			t.Errorf("%s as assignee = %v, want ErrAssigneeOutsideProject", name, err)
		}
	}
}

func sortedPermissions(values []auth.ProjectPermission) []auth.ProjectPermission {
	out := slices.Clone(values)
	slices.Sort(out)
	return slices.Compact(out)
}

// assigneeOutsideProject is mapped from both projection reads, survives into
// the Task frame, serializes under its camelCase key, and is the one source
// of a write receipt's list (ADR 0017).
func TestAssigneeOutsideProjectReachesReadsFramesAndReceipts(t *testing.T) {
	t.Parallel()

	task := generated.Task{
		ID: uuid.New(), CompanyID: uuid.New(), ProjectID: uuid.New(),
		AssigneeUserID: uuid.New(), Status: string(StatusTodo), Priority: string(PriorityMedium),
	}
	for name, value := range map[string]Response{
		"person read": responseFromProjectionRow(generated.ListTaskProjectionsForActiveMemberRow{
			Task: task, ProjectVisibility: string(auth.ProjectVisibilityRestricted),
			AccessVia: string(auth.ProjectAccessViaGrant), GrantedPermissions: []string{},
			AssigneeOutsideProject: true,
		}),
		"system read": responseFromSystemProjectionRow(generated.GetTaskProjectionForSystemRow{
			Task: task, AssigneeOutsideProject: true,
		}),
	} {
		body, err := json.Marshal(framedResponse(value))
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		if got := string(fields["assigneeOutsideProject"]); got != "true" {
			t.Errorf("%s: framed assigneeOutsideProject = %s, want true", name, got)
		}
		if got := assigneeOutsideProject(value); !slices.Equal(got, []uuid.UUID{task.ID}) {
			t.Errorf("%s: receipt lists %v, want the Task", name, got)
		}
	}
	if got := assigneeOutsideProject(Response{ID: task.ID}); got != nil {
		t.Errorf("receipt for a Task whose assignee reads it lists %v, want none", got)
	}
}
