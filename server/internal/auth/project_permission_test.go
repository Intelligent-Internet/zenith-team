package auth

import (
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"boardly.dev/boardly/server/migrations"
)

func TestProjectPermissionCatalogIsSortedAndValid(t *testing.T) {
	t.Parallel()

	all := ProjectPermissions()
	if len(all) != 9 {
		t.Fatalf("ProjectPermissions() has %d entries, want 9: %v", len(all), all)
	}
	if !slices.IsSorted(all) {
		t.Errorf("ProjectPermissions() = %v, want sorted", all)
	}
	if len(slices.Compact(slices.Clone(all))) != len(all) {
		t.Errorf("ProjectPermissions() = %v, want no duplicates", all)
	}
	for _, permission := range all {
		if !permission.Valid() {
			t.Errorf("%q is in the catalog but not Valid()", permission)
		}
	}
	for _, name := range []ProjectPermission{
		"", "project", "project.write", "projects.manage", "PROJECT.READ", "project.results.review",
	} {
		if name.Valid() {
			t.Errorf("%q Valid() = true, want false", name)
		}
	}
}

// The projects.manage bridge reaches Projects its holder cannot read, so it
// must never carry a content or deciding permission (ADR 0017 decision 4).
func TestManagementProjectPermissionsNeverOpenContent(t *testing.T) {
	t.Parallel()

	management := ManagementProjectPermissions()
	if !slices.IsSorted(management) {
		t.Errorf("ManagementProjectPermissions() = %v, want sorted", management)
	}
	want := []ProjectPermission{ProjectPermissionArchive, ProjectPermissionGrantsManage, ProjectPermissionSettingsUpdate}
	if !slices.Equal(management, want) {
		t.Errorf("ManagementProjectPermissions() = %v, want %v", management, want)
	}
	for _, content := range []ProjectPermission{
		ProjectPermissionRead,
		ProjectPermissionTasksWrite,
		ProjectPermissionTasksExecute,
		ProjectPermissionFilesManage,
		ProjectPermissionApprovalsResolve,
		ProjectPermissionVisibilityUpdate,
	} {
		if slices.Contains(management, content) {
			t.Errorf("management set names content or deciding permission %q", content)
		}
	}
}

func TestCompanyProjectDefaultPermissionsAreInTheCatalog(t *testing.T) {
	t.Parallel()

	defaults := CompanyProjectDefaultPermissions()
	if !slices.IsSorted(defaults) {
		t.Errorf("CompanyProjectDefaultPermissions() = %v, want sorted", defaults)
	}
	if !slices.Contains(defaults, ProjectPermissionRead) {
		t.Errorf("CompanyProjectDefaultPermissions() = %v, want project.read", defaults)
	}
	for _, permission := range defaults {
		if !permission.Valid() {
			t.Errorf("default %q is not in the catalog", permission)
		}
		if slices.Contains(ManagementProjectPermissions(), permission) {
			t.Errorf("default %q is a management permission", permission)
		}
	}
}

func TestProjectPermissionStringsNeverNil(t *testing.T) {
	t.Parallel()

	if got := ProjectPermissionStrings(nil); got == nil || len(got) != 0 {
		t.Errorf("ProjectPermissionStrings(nil) = %#v, want empty non-nil", got)
	}
	got := ProjectPermissionStrings([]ProjectPermission{ProjectPermissionRead, ProjectPermissionArchive})
	if !slices.Equal(got, []string{"project.read", "project.archive"}) {
		t.Errorf("ProjectPermissionStrings() = %v", got)
	}
}

func TestNormalizeProjectGrant(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		input []string
		want  []ProjectPermission
	}{
		{name: "empty stays empty", input: nil, want: []ProjectPermission{}},
		{name: "read alone", input: []string{"project.read"}, want: []ProjectPermission{ProjectPermissionRead}},
		{
			name:  "read is added",
			input: []string{"project.tasks.write"},
			want:  []ProjectPermission{ProjectPermissionRead, ProjectPermissionTasksWrite},
		},
		{
			name:  "duplicates collapse and the result is sorted",
			input: []string{"project.tasks.write", "project.archive", "project.tasks.write", "project.read"},
			want:  []ProjectPermission{ProjectPermissionArchive, ProjectPermissionRead, ProjectPermissionTasksWrite},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeProjectGrant(test.input)
			if err != nil {
				t.Fatalf("NormalizeProjectGrant(%v) error = %v", test.input, err)
			}
			if got == nil || !slices.Equal(got, test.want) {
				t.Errorf("NormalizeProjectGrant(%v) = %#v, want %v", test.input, got, test.want)
			}
		})
	}
	for _, unknown := range []string{
		"projects.manage",
		// Removed by migration 000102: a grant naming it is refused.
		"project.results.review",
	} {
		_, err := NormalizeProjectGrant([]string{"project.read", unknown})
		if !errors.Is(err, ErrProjectPermissionUnknown) {
			t.Errorf("NormalizeProjectGrant(%q) error = %v, want ErrProjectPermissionUnknown", unknown, err)
		}
		// The refusal names the permission, so the answer can.
		var named *ProjectPermissionUnknownError
		if !errors.As(err, &named) || named.Name != unknown {
			t.Errorf("NormalizeProjectGrant(%q) error = %#v, want it to name %q", unknown, err, unknown)
		}
	}
}

// The CHECKs are the database's copies of the catalog, one per grant table
// (a person's grants, 000086, and a Group's, 000101); a permission added on
// one side only would be refused at write time or never granted. A later
// migration may redefine a CHECK (000102 removed project.results.review), so
// the newest definition is the one compared.
func TestProjectGrantsCheckMatchesCatalog(t *testing.T) {
	t.Parallel()

	want := ProjectPermissionStrings(ProjectPermissions())
	slices.Sort(want)
	// Permissions a later migration removed from the catalog, its rows and
	// both CHECKs (000102). Older migrations still name them.
	retired := []string{"project.results.review"}
	read := string(ProjectPermissionRead)
	readColumn := "read_permission text GENERATED ALWAYS AS ('" + read + "') STORED NOT NULL"
	ups, err := fs.Glob(migrations.Files, "*.up.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	slices.Sort(ups)
	for _, table := range []struct{ created, check string }{
		{"000086_project_access.up.sql", "project_grants_permission_check"},
		{"000101_groups.up.sql", "project_group_grants_permission_check"},
	} {
		definition := regexp.MustCompile(`(?s)CONSTRAINT ` + table.check + ` CHECK \(permission IN \((.*?)\)\)`)
		newest, newestFile := "", ""
		for _, file := range ups {
			raw, err := migrations.Files.ReadFile(file)
			if err != nil {
				t.Fatalf("read migration: %v", err)
			}
			for _, match := range definition.FindAllStringSubmatch(string(raw), -1) {
				newest, newestFile = match[1], file
			}
		}
		if newestFile == "" {
			t.Fatalf("no migration declares %s", table.check)
		}
		got := []string{}
		for _, literal := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(newest, -1) {
			got = append(got, literal[1])
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s in %s = %v, want catalog %v", table.check, newestFile, got, want)
		}
		raw, err := migrations.Files.ReadFile(table.created)
		if err != nil {
			t.Fatalf("read migration: %v", err)
		}
		// Every grant row points at the grantee's read row through this column.
		if !strings.Contains(string(raw), readColumn) {
			t.Errorf("%s does not declare %q", table.created, readColumn)
		}
	}

	// Beyond the CHECKs, the access migrations name a permission only where
	// the catalog has it, or a project_permission_held call would match
	// nobody, or where they deal with a retired one.
	for _, file := range []string{
		"000086_project_access.up.sql", "000101_groups.up.sql", "000102_remove_results_review.up.sql",
	} {
		raw, err := migrations.Files.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration: %v", err)
		}
		source := string(raw)
		if strings.Contains(source, "'projects.manage'") {
			t.Errorf("%s names a Company permission; Go binds the management roles", file)
		}
		for _, literal := range regexp.MustCompile(`'(project\.[a-z.]+)'`).FindAllStringSubmatch(source, -1) {
			if !ProjectPermission(literal[1]).Valid() && !slices.Contains(retired, literal[1]) {
				t.Errorf("%s names %q, which is not in the Project catalog", file, literal[1])
			}
		}
	}

	// The owner backfill (000086) wrote the catalog of its day: today's
	// catalog and the permissions retired since.
	raw, err := migrations.Files.ReadFile("000086_project_access.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	backfill := regexp.MustCompile(`(?s)CROSS JOIN \(VALUES(.*?)\) AS catalog`).FindStringSubmatch(string(raw))
	if backfill == nil {
		t.Fatal("migration declares no owner backfill catalog")
	}
	backfilled := []string{}
	for _, literal := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(backfill[1], -1) {
		backfilled = append(backfilled, literal[1])
	}
	slices.Sort(backfilled)
	then := slices.Concat(want, retired)
	slices.Sort(then)
	if !slices.Equal(backfilled, then) {
		t.Errorf("owner backfill = %v, want catalog and retired %v", backfilled, then)
	}
}

// The three sets every project_permission_held call binds come from the Go
// catalogs, never from literals at a call site.
func TestProjectPermissionHeldSetsBindTheCatalogs(t *testing.T) {
	t.Parallel()

	sets := ProjectPermissionHeldSets()
	if !slices.Equal(sets.CompanyDefaultPermissions, ProjectPermissionStrings(CompanyProjectDefaultPermissions())) {
		t.Errorf("CompanyDefaultPermissions = %v", sets.CompanyDefaultPermissions)
	}
	if !slices.Equal(sets.ManagementPermissions, ProjectPermissionStrings(ManagementProjectPermissions())) {
		t.Errorf("ManagementPermissions = %v", sets.ManagementPermissions)
	}
	if !slices.Equal(sets.ManagementRoles, RolesWith(PermissionProjectsManage)) {
		t.Errorf("ManagementRoles = %v", sets.ManagementRoles)
	}
}
