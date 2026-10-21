//go:build integration

package execution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/contentbundle"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestRunSkillSnapshotUsesCurrentAgentWithoutChangingOldContext(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := context.Background()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	q := generated.New(pool)
	preview, err := q.CreateSkillImport(
		ctx,
		generated.CreateSkillImportParams{
			CompanyID:       fixture.CompanyA,
			AgentID:         fixture.AgentA,
			CreatedByUserID: fixture.UserA,
			SourceType:      "markdown",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	version, err := q.CreateSkillVersion(
		ctx,
		generated.CreateSkillVersionParams{
			CompanyID:   fixture.CompanyA,
			ImportID:    preview.ID,
			Name:        "review-work",
			Description: "Review work.",
			SourcePath:  "SKILL.md",
			ContentHash: strings.Repeat("a", 64),
			SizeBytes:   20,
			FileCount:   1,
			Manifest:    []byte("[]"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := q.InsertAgentSkill(
		ctx,
		generated.InsertAgentSkillParams{
			CompanyID: fixture.CompanyA,
			AgentID:   fixture.AgentA,
			VersionID: version.ID,
			Name:      version.Name,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := runAgentSkillRefs(ctx, q, fixture.CompanyA, fixture.AgentA)
	if err != nil || len(refs) != 1 || refs[0].ID != version.ID.String() {
		t.Fatalf("refs = %+v, %v", refs, err)
	}
	old, _ := json.Marshal(refs)
	_, err = q.UpdateAgentSkill(
		ctx,
		generated.UpdateAgentSkillParams{
			CompanyID: fixture.CompanyA,
			AgentID:   fixture.AgentA,
			ID:        installed.ID,
			VersionID: version.ID,
			Revision:  1,
			Enabled:   false,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	newRefs, err := runAgentSkillRefs(ctx, q, fixture.CompanyA, fixture.AgentA)
	if err != nil || len(newRefs) != 0 {
		t.Fatalf("new refs = %+v, %v", newRefs, err)
	}
	if contentbundle.SkillSetHash(refs) == contentbundle.SkillSetHash(newRefs) {
		t.Fatal("removal did not change resume identity")
	}
	if !strings.Contains(string(old), version.ID.String()) {
		t.Fatal("old snapshot changed")
	}
	other, err := runAgentSkillRefs(ctx, q, fixture.CompanyB, fixture.AgentB)
	if err != nil || len(other) != 0 {
		t.Fatalf("other Agent inherited skill: %+v, %v", other, err)
	}
}

func TestExistingChatAndTaskTakeNextSkillSetWhileRetryKeepsFrozenVersion(t *testing.T) {
	ctx, pool, fixture, q := startSnapshotFixture(t)
	preview, err := q.CreateSkillImport(
		ctx,
		generated.CreateSkillImportParams{
			CompanyID:       fixture.CompanyA,
			AgentID:         fixture.AgentA,
			CreatedByUserID: fixture.UserA,
			SourceType:      "markdown",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	version, err := q.CreateSkillVersion(
		ctx,
		generated.CreateSkillVersionParams{
			CompanyID:   fixture.CompanyA,
			ImportID:    preview.ID,
			Name:        "review-work",
			Description: "Review work.",
			ContentHash: strings.Repeat("b", 64),
			SizeBytes:   100,
			FileCount:   1,
			Manifest:    []byte("[]"),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.InsertAgentSkill(
		ctx,
		generated.InsertAgentSkillParams{
			CompanyID: fixture.CompanyA,
			AgentID:   fixture.AgentA,
			VersionID: version.ID,
			Name:      version.Name,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var pending []events.Event
	direct, err := EnqueueRunInTx(
		ctx,
		q,
		&pending,
		RunTrigger{
			CompanyID:          fixture.CompanyA,
			ConversationID:     fixture.DirectConversationA,
			AgentID:            fixture.AgentA,
			AuthorizedByUserID: fixture.UserA,
			ActorUserID:        fixture.UserA,
			Source:             RunSourceMessage,
			TriggerPrompt:      "use the new procedure",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(direct.Context), version.ID.String()) {
		t.Fatal("existing direct chat did not receive custom skill")
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE tasks SET status='in_progress' WHERE id=$1`, fixture.TaskA)
	task := enqueueRetryFenceTaskRun(t, ctx, q, fixture)
	if !strings.Contains(string(task.Context), version.ID.String()) {
		t.Fatal("existing Task did not receive custom skill")
	}
	dbtest.MustExec(t, ctx, pool, `DELETE FROM agent_skills WHERE agent_id=$1`, fixture.AgentA)
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='failed' WHERE id=$1`, task.ID)
	retry, err := q.CreateRetryRun(ctx, generated.CreateRetryRunParams{ParentRunID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if string(retry.Context) != string(task.Context) {
		t.Fatal("retry changed frozen context")
	}
	if _, err := q.GetFrozenRunSkill(
		ctx,
		generated.GetFrozenRunSkillParams{CompanyID: fixture.CompanyA, RunID: retry.ID, VersionID: version.ID},
	); err != nil {
		t.Fatalf("retry lost skill reference: %v", err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE runs SET status='completed' WHERE id=$1`, direct.ID)
	next, err := EnqueueRunInTx(
		ctx,
		q,
		&pending,
		RunTrigger{
			CompanyID:          fixture.CompanyA,
			ConversationID:     fixture.DirectConversationA,
			AgentID:            fixture.AgentA,
			AuthorizedByUserID: fixture.UserA,
			ActorUserID:        fixture.UserA,
			Source:             RunSourceMessage,
			TriggerPrompt:      "continue",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(next.Context), version.ID.String()) {
		t.Fatal("next Run retained removed skill")
	}
	if _, err := q.GetFrozenRunSkill(
		ctx,
		generated.GetFrozenRunSkillParams{CompanyID: fixture.CompanyA, RunID: direct.ID, VersionID: version.ID},
	); err != nil {
		t.Fatalf("old Run lost version: %v", err)
	}
}
