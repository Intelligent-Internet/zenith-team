//go:build integration

package company

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
)

func projectIDs(projects []ProjectResponse) []uuid.UUID {
	ids := make([]uuid.UUID, len(projects))
	for i, project := range projects {
		ids[i] = project.ID
	}
	return ids
}

// A Run reads Projects only as its authorizer's reader (ADR 0017): the
// metadata view a projects.manage holder gets of a Project they cannot read
// is management, and a Run never manages, so that Project is not found. A
// Task Run reads only its Task chat's Project. Any other Run, in its
// person's direct chat or tagged in a channel, reads and lists what its
// Authorizer reads, and a channel member outside a Project narrows nothing
// (D31, revised 2026-10-06). The bounds are the delegate rule, read from
// each Run's own row.
func TestProjectReadsForARunAreReaderOnlyAndBoundedByTheAuthorizer(t *testing.T) {
	ctx, w := newProjectAccessWorld(t)
	taskRunID := dbtest.SeedRun(t, ctx, w.pool, dbtest.RunSeed{
		CompanyID: w.f.CompanyA, AgentID: w.f.AgentA, RuntimeID: w.f.RuntimeA,
		ConversationID: w.f.ConversationA, TaskID: w.f.TaskA, AuthorizerID: w.f.UserA,
	})
	taskRun := &task.RunScope{AgentID: w.f.AgentA, UserID: w.f.UserA, RunID: taskRunID, ProjectID: &w.f.ProjectA}

	if _, err := w.projects.GetForRun(ctx, w.owner, w.f.ProjectA, taskRun); err != nil {
		t.Fatalf("GetForRun(readable Project) = %v", err)
	}
	listed, err := w.projects.ListForRun(ctx, w.owner, false, taskRun)
	if err != nil || !slices.Contains(projectIDs(listed), w.f.ProjectA) {
		t.Fatalf("ListForRun() = %v, %v; want the readable Project", projectIDs(listed), err)
	}
	// A Task Run answers into its Task chat, read by Project A's readers
	// only: another Project the member reads stays out of its reach.
	other, err := w.projects.Create(ctx, w.owner, ProjectCreateInput{Key: "secret", Title: "Secret"})
	if err != nil {
		t.Fatalf("Create(second Project) = %v", err)
	}
	if _, err := w.projects.GetForRun(ctx, w.owner, other.ID, taskRun); !errors.Is(err, task.ErrOutsideRunScope) {
		t.Fatalf("GetForRun(another Project) from a Task Run = %v, want ErrOutsideRunScope", err)
	}
	listed, err = w.projects.ListForRun(ctx, w.owner, false, taskRun)
	if err != nil || slices.Contains(projectIDs(listed), other.ID) || len(listed) != 1 {
		t.Fatalf("ListForRun() from a Task Run = %v, %v; want its own Project only", projectIDs(listed), err)
	}
	// A Run in its person's direct chat answers only that person, so it
	// lists both Projects they read.
	directRunID := dbtest.SeedRun(t, ctx, w.pool, dbtest.RunSeed{
		CompanyID: w.f.CompanyA, AgentID: w.f.AgentA, RuntimeID: w.f.RuntimeA,
		ConversationID: w.f.DirectConversationA, AuthorizerID: w.f.UserA,
	})
	directRun := &task.RunScope{AgentID: w.f.AgentA, UserID: w.f.UserA, RunID: directRunID}
	listed, err = w.projects.ListForRun(ctx, w.owner, false, directRun)
	if err != nil || !slices.Contains(projectIDs(listed), other.ID) ||
		!slices.Contains(projectIDs(listed), w.f.ProjectA) {
		t.Fatalf("ListForRun() from a direct chat Run = %v, %v; want both Projects", projectIDs(listed), err)
	}

	// The owner holds the management bridge; without project.read a person
	// sees the metadata, but a Run does not.
	dbtest.MustExec(t, ctx, w.pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		w.f.ProjectA, w.f.UserA)
	if _, err := w.projects.Get(ctx, w.owner, w.f.ProjectA); err != nil {
		t.Fatalf("Get() by the bridge holder = %v, want the metadata view", err)
	}
	if _, err := w.projects.GetForRun(ctx, w.owner, w.f.ProjectA, taskRun); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("GetForRun() through the bridge = %v, want ErrProjectNotFound", err)
	}
	listed, err = w.projects.ListForRun(ctx, w.owner, false, taskRun)
	if err != nil || slices.Contains(projectIDs(listed), w.f.ProjectA) {
		t.Fatalf("ListForRun() through the bridge = %v, %v; want the Project left out", projectIDs(listed), err)
	}
	dbtest.GrantProject(t, ctx, w.pool, w.f.CompanyA, w.f.ProjectA, w.f.UserA, "project.read")

	channel := dbtest.SeedDiscussionChannel(t, ctx, w.pool, w.f.CompanyA, w.f.UserA, nil)
	dbtest.AddDiscussionMember(t, ctx, w.pool, w.f.CompanyA, channel, w.member.UserID)
	taggedRunID := dbtest.SeedTaggedRun(t, ctx, w.pool, w.f.CompanyA, channel, w.f.AgentA, w.f.RuntimeA, w.f.UserA)
	tagged := &task.RunScope{AgentID: w.f.AgentA, UserID: w.f.UserA, RunID: taggedRunID}
	if _, err := w.projects.GetForRun(ctx, w.owner, w.f.ProjectA, tagged); err != nil {
		t.Fatalf("GetForRun() with a member outside the Project = %v, want the Authorizer's read", err)
	}
	listed, err = w.projects.ListForRun(ctx, w.owner, false, tagged)
	if err != nil || !slices.Contains(projectIDs(listed), w.f.ProjectA) ||
		!slices.Contains(projectIDs(listed), other.ID) {
		t.Fatalf(
			"ListForRun() with a member outside the Project = %v, %v; want both Projects",
			projectIDs(listed),
			err,
		)
	}
	// A tagger who cannot read the Project: the Run cannot either.
	dbtest.MustExec(t, ctx, w.pool, `DELETE FROM project_grants WHERE project_id = $1 AND user_id = $2`,
		w.f.ProjectA, w.f.UserA)
	if _, err := w.projects.GetForRun(ctx, w.owner, w.f.ProjectA, tagged); !errors.Is(err, ErrProjectNotFound) {
		t.Fatalf("GetForRun() for a tagger without the Project = %v, want ErrProjectNotFound", err)
	}
	listed, err = w.projects.ListForRun(ctx, w.owner, false, tagged)
	if err != nil || slices.Contains(projectIDs(listed), w.f.ProjectA) {
		t.Fatalf("ListForRun() for a tagger without the Project = %v, %v", projectIDs(listed), err)
	}

	dbtest.MustExec(t, ctx, w.pool, `UPDATE projects SET visibility = 'company' WHERE id = $1`, w.f.ProjectA)
	if _, err := w.projects.GetForRun(ctx, w.owner, w.f.ProjectA, tagged); err != nil {
		t.Fatalf("GetForRun() of a Company-visible Project = %v", err)
	}
	listed, err = w.projects.ListForRun(ctx, w.owner, false, tagged)
	if err != nil || !slices.Contains(projectIDs(listed), w.f.ProjectA) {
		t.Fatalf("ListForRun() of a Company-visible Project = %v, %v", projectIDs(listed), err)
	}
}
