package company

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gopkg.in/yaml.v3"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
)

// A Run reads a Project only as a reader (ADR 0017): the metadata view a
// projects.manage holder gets of a Project they cannot read is management,
// and a Run never manages, so it answers like a missing Project.
func TestRunReadsProjectOnlyAsAReader(t *testing.T) {
	t.Parallel()
	view := func(management bool, permissions ...auth.ProjectPermission) ProjectResponse {
		return ProjectResponse{Access: ProjectAccessView{Permissions: permissions, Management: management}}
	}
	for name, test := range map[string]struct {
		value ProjectResponse
		want  bool
	}{
		"reader":                  {view(false, auth.ProjectPermissionRead), true},
		"reader who also manages": {view(true, auth.ProjectPermissionRead), true},
		"manager who cannot read": {view(true), false},
		"no permission":           {view(false), false},
	} {
		if got := runReadsProject(test.value); got != test.want {
			t.Errorf("%s: runReadsProject() = %t, want %t", name, got, test.want)
		}
	}
}

func TestProjectWorkLostOnlyWhenReadOrExecuteGoes(t *testing.T) {
	t.Parallel()
	holding := func(permissions ...auth.ProjectPermission) auth.ProjectAccess {
		return auth.ProjectAccess{Permissions: permissions}
	}
	read, write, execute := auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite, auth.ProjectPermissionTasksExecute
	for _, test := range []struct {
		name          string
		before, after auth.ProjectAccess
		want          bool
	}{
		{"removed", holding(read, execute), holding(), true},
		{"execute taken", holding(read, write, execute), holding(read, write), true},
		{"read taken, bridge kept", holding(read, auth.ProjectPermissionArchive), holding(auth.ProjectPermissionArchive), true},
		{"write taken", holding(read, write, execute), holding(read, execute), false},
		{"widened", holding(read), holding(read, execute), false},
		{"never held", holding(), holding(), false},
		{"deciding taken", holding(read, auth.ProjectPermissionApprovalsResolve), holding(read), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := projectWorkLost(test.before, test.after); got != test.want {
				t.Fatalf("projectWorkLost() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestProjectPostingLostOnlyWhenReadOrWriteGoes(t *testing.T) {
	t.Parallel()
	holding := func(permissions ...auth.ProjectPermission) auth.ProjectAccess {
		return auth.ProjectAccess{Permissions: permissions}
	}
	read, write, execute := auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite, auth.ProjectPermissionTasksExecute
	for _, test := range []struct {
		name          string
		before, after auth.ProjectAccess
		want          bool
	}{
		{"removed", holding(read, write), holding(), true},
		{"write taken", holding(read, write, execute), holding(read, execute), true},
		{"execute taken", holding(read, write, execute), holding(read, write), false},
		{"read only before", holding(read), holding(), true},
		{"widened", holding(read), holding(read, write), false},
		{"never held", holding(), holding(), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := projectPostingLost(test.before, test.after); got != test.want {
				t.Fatalf("projectPostingLost() = %t, want %t", got, test.want)
			}
		})
	}
}

// On a Company-visible Project a stored set adds on top of the default set,
// so only removing a person is refused there.
func TestProjectFloorRefusesOnlyRemovalOnCompanyProjects(t *testing.T) {
	t.Parallel()
	if err := checkProjectFloor(auth.ProjectVisibilityRestricted); err != nil {
		t.Fatalf("restricted removal = %v", err)
	}
	if err := checkProjectFloor(auth.ProjectVisibilityCompany); !errors.Is(err, ErrProjectVisibilityFloor) {
		t.Fatalf("Company removal = %v, want the floor refusal", err)
	}
}

func TestGrantChangeRecordedOnlyWhenTheStoredSetMoved(t *testing.T) {
	t.Parallel()
	granted := func(permissions ...auth.ProjectPermission) auth.ProjectAccess {
		if permissions == nil {
			permissions = []auth.ProjectPermission{}
		}
		return auth.ProjectAccess{Granted: permissions}
	}
	read, write := auth.ProjectPermissionRead, auth.ProjectPermissionTasksWrite
	for _, test := range []struct {
		name          string
		before, after auth.ProjectAccess
		written       bool
		want          bool
	}{
		{"granted", granted(), granted(read), true, true},
		{"widened", granted(read), granted(read, write), true, true},
		{"narrowed", granted(read, write), granted(read), true, true},
		{"removed", granted(read), granted(), true, true},
		{"nothing written", granted(read), granted(read), false, false},
		{"written but the same", granted(read), granted(read), true, false},
		{"removing nobody", granted(), granted(), true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := grantChangeRecorded(test.before, test.after, test.written); got != test.want {
				t.Fatalf("grantChangeRecorded() = %t, want %t", got, test.want)
			}
		})
	}
}

// The self-grant cap: someone who manages access only through their own
// grant gives only what they hold and cannot change a grant that holds more;
// a projects.manage holder is not capped.
func TestGrantWithinOwnCapsAGrantManagerToTheirOwnSet(t *testing.T) {
	t.Parallel()
	read, grants := auth.ProjectPermissionRead, auth.ProjectPermissionGrantsManage
	approve := auth.ProjectPermissionApprovalsResolve
	set := func(permissions ...auth.ProjectPermission) []auth.ProjectPermission { return permissions }
	work := set(read, auth.ProjectPermissionTasksExecute, auth.ProjectPermissionTasksWrite)
	// Leo manages Payroll's access through a grant of {read, grants.manage}.
	leo := auth.ProjectAccess{
		Via: auth.ProjectAccessViaGrant, Permissions: set(grants, read), Granted: set(grants, read),
	}
	// On a Project open to the Company he also holds the default set.
	leoOnOpen := auth.ProjectAccess{
		Visibility: auth.ProjectVisibilityCompany, Via: auth.ProjectAccessViaGrant,
		Permissions: append(set(grants), work...), Granted: set(grants, read),
	}
	// An admin manages through the bridge, with no grant of their own.
	admin := auth.ProjectAccess{Management: true, Permissions: auth.ManagementProjectPermissions()}
	full := auth.ProjectPermissions()
	for _, test := range []struct {
		name              string
		caller            auth.ProjectAccess
		target, requested []auth.ProjectPermission
		want              bool
	}{
		{"gives Can view tasks to someone new", leo, nil, set(read), true},
		{"gives what he holds", leo, set(read), set(grants, read), true},
		{"gives Can approve tasks", leo, nil, set(approve, read), false},
		{"gives Can edit and run tasks on a restricted Project", leo, set(read), work, false},
		{"gives Can edit and run tasks on a Project open to the Company", leoOnOpen, set(read), work, true},
		{"lowers someone who holds Can manage project", leo, full, set(read), false},
		{"removes someone who holds Can manage project", leo, full, nil, false},
		{"removes someone who holds Can view tasks", leo, set(read), nil, true},
		{"removes nobody", leo, set(), nil, true},
		{"saves someone's own set again, above his own", leo, set(approve, read), set(approve, read), false},
		{"lowers himself", leo, set(grants, read), set(read), true},
		{"an admin gives Can approve tasks", admin, nil, set(approve, read), true},
		{"an admin lowers someone who holds Can manage project", admin, full, set(read), true},
		{"an admin who joined is still not capped", auth.ProjectAccess{
			Management: true, Via: auth.ProjectAccessViaGrant, Granted: set(read),
			Permissions: append(auth.ManagementProjectPermissions(), read),
		}, full, set(approve, read), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := grantWithinOwn(test.caller, test.target, test.requested); got != test.want {
				t.Fatalf("grantWithinOwn() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestSameProjectFilesComparesTheSelection(t *testing.T) {
	t.Parallel()
	logo, first, second := uuid.New(), uuid.New(), uuid.New()
	current := ProjectFiles{Logo: &ProjectFile{ID: logo}, Documents: []ProjectFile{{ID: first}, {ID: second}}}
	for _, test := range []struct {
		name  string
		input ProjectFilesInput
		want  bool
	}{
		{"same, any order", ProjectFilesInput{LogoID: &logo, DocumentIDs: []uuid.UUID{second, first}}, true},
		{"logo dropped", ProjectFilesInput{DocumentIDs: []uuid.UUID{first, second}}, false},
		{"other logo", ProjectFilesInput{LogoID: &first, DocumentIDs: []uuid.UUID{first, second}}, false},
		{"document dropped", ProjectFilesInput{LogoID: &logo, DocumentIDs: []uuid.UUID{first}}, false},
		{"document swapped", ProjectFilesInput{LogoID: &logo, DocumentIDs: []uuid.UUID{first, logo}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := sameProjectFiles(current, test.input); got != test.want {
				t.Fatalf("sameProjectFiles() = %t, want %t", got, test.want)
			}
		})
	}
	if _, err := projectFileIDs(ProjectFilesInput{DocumentIDs: []uuid.UUID{first, first}}); !errors.Is(err, ErrProjectInvalidInput) {
		t.Fatalf("repeated document = %v", err)
	}
	if _, err := projectFileIDs(ProjectFilesInput{LogoID: &first, DocumentIDs: []uuid.UUID{first}}); !errors.Is(err, ErrProjectInvalidInput) {
		t.Fatalf("logo repeated as a document = %v", err)
	}
}

func TestProjectAccessChangedFrameSortsItsRecipients(t *testing.T) {
	t.Parallel()
	a := uuid.MustParse("00000000-0000-4000-8000-00000000000a")
	b := uuid.MustParse("00000000-0000-4000-8000-00000000000b")
	event := projectAccessChangedEvent(uuid.New(), events.SystemActor(), uuid.New(), 3, []uuid.UUID{b, a, b}, false)
	change, ok := event.Payload.(ProjectAccessChanged)
	if !ok || len(change.RecipientUserIDs) != 2 || change.RecipientUserIDs[0] != a || change.RecipientUserIDs[1] != b {
		t.Fatalf("recipients = %+v", event.Payload)
	}
	if !event.ActivityRecorded || event.Name != EventProjectAccessChanged {
		t.Fatalf("event = %+v", event)
	}
}

// The Activity rows this domain writes must be filterable in the feed and
// declared in the contract's entity vocabulary.
func TestProjectActivityVocabularyIsFilterableAndDeclared(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../docs/api/spec/_common.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Schemas map[string]struct {
			Enum []string `yaml:"enum"`
		} `yaml:"schemas"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	declared := document.Schemas["EntityType"].Enum
	for _, action := range []string{
		projectGrantChanged, projectGrantJoined, projectGrantVisibilityChanged,
		EventProjectCreated, EventProjectUpdated, EventProjectArchived,
		groupCreated, groupUpdated, groupDeleted, groupMemberAdded, groupMemberRemoved,
		projectGrantGroupChanged, projectGrantGroupMemberAdded, projectGrantGroupMemberRemoved,
	} {
		entityType, _, _ := strings.Cut(action, ".")
		if !activity.ValidActionFilter(action) || !activity.ValidEntityTypeFilter(entityType) {
			t.Errorf("%s is not filterable in the Activity feed", action)
		}
		if !slices.Contains(declared, entityType) {
			t.Errorf("_common.yaml EntityType %v misses %q", declared, entityType)
		}
	}
}
