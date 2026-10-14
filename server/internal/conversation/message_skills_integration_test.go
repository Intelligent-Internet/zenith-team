//go:build integration

package conversation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"github.com/google/uuid"
)

func seedMessageSkill(t *testing.T, f discussionFixture) (uuid.UUID, uuid.UUID) {
	t.Helper()
	q := f.store.Queries()
	preview, err := q.CreateSkillImport(f.ctx, generated.CreateSkillImportParams{CompanyID: f.a.CompanyID, AgentID: f.seed.AgentA, CreatedByUserID: f.a.UserID, SourceType: "markdown"})
	if err != nil {
		t.Fatal(err)
	}
	version, err := q.CreateSkillVersion(f.ctx, generated.CreateSkillVersionParams{CompanyID: f.a.CompanyID, ImportID: preview.ID, Name: "message-review", Description: "Review this message", SourcePath: "SKILL.md", ContentHash: strings.Repeat("a", 64), SizeBytes: 30, FileCount: 1, Manifest: []byte("[]")})
	if err != nil {
		t.Fatal(err)
	}
	skill, err := q.InsertAgentSkill(f.ctx, generated.InsertAgentSkillParams{CompanyID: f.a.CompanyID, AgentID: f.seed.AgentA, VersionID: version.ID, Name: version.Name})
	if err != nil {
		t.Fatal(err)
	}
	return skill.ID, version.ID
}

func addMessageSkill(t *testing.T, input *DiscussionSendInput, skillID, versionID uuid.UUID) {
	t.Helper()
	start := utf16Len(input.Body.Markdown)
	input.Body.Markdown += "$message-review"
	ref, err := json.Marshal(discussionReference{Type: "skill", Start: start, End: start + 15, SkillID: &skillID, VersionID: &versionID})
	if err != nil {
		t.Fatal(err)
	}
	input.Body.References = append(input.Body.References, ref)
}

func TestMessageSkillsFanOutWithoutInstallingAndPinQueuedVersion(t *testing.T) {
	f := newChannelInvocationFixture(t)
	var second uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO agents(company_id,name,title,reports_to_agent_id,provider,model,created_by_user_id) VALUES($1,'Second','Engineer',$2,'codex','test',$3) RETURNING id`, f.a.CompanyID, f.seed.AgentA, f.a.UserID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	dbtest.SeedAgentBinding(t, f.ctx, f.pool, f.a.CompanyID, second, f.a.UserID, f.seed.RuntimeA, "codex")
	channel, err := f.conversations.CreateChannel(f.ctx, f.a, CreateChannelInput{RequestKey: uuid.New(), Name: "Skill fanout", AgentIDs: []uuid.UUID{f.seed.AgentA, second}})
	if err != nil {
		t.Fatal(err)
	}
	f.channelID = channel.Conversation.ID
	skillID, versionID := seedMessageSkill(t, f.discussionFixture)
	// Keep the first Agent busy. The second will start immediately.
	busy := f.send(t, taggedSend(t, f.seed.AgentA))
	busyRun := f.run(t, f.invocation(t, busy.ID).ID)
	input := taggedSend(t, f.seed.AgentA, second)
	addMessageSkill(t, &input, skillID, versionID)
	message := f.send(t, input)
	var immediate uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `SELECT r.id FROM runs r JOIN channel_invocations i ON i.id=r.invocation_id WHERE i.trigger_message_id=$1 AND r.agent_id=$2`, message.ID, second).Scan(&immediate); err != nil {
		t.Fatal(err)
	}
	assertSelection := func(runID uuid.UUID) {
		t.Helper()
		run, err := f.store.Queries().GetRun(f.ctx, runID)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Agent struct {
				SelectedSkillKeys []string `json:"selectedSkillKeys"`
				SkillRefs         []struct {
					ID string `json:"id"`
				} `json:"skillRefs"`
			} `json:"agent"`
		}
		if err := json.Unmarshal(run.Context, &doc); err != nil {
			t.Fatal(err)
		}
		if len(doc.Agent.SelectedSkillKeys) != 1 || doc.Agent.SelectedSkillKeys[0] != "message-review" || len(doc.Agent.SkillRefs) != 1 || doc.Agent.SkillRefs[0].ID != versionID.String() {
			t.Fatalf("selection = %+v", doc.Agent)
		}
	}
	assertSelection(immediate)
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM agent_skills WHERE id=$1`, skillID)
	promotion := f.finish(t, busyRun, "completed", "done", true)
	if promotion.RunID == nil {
		t.Fatal("queued invocation was not promoted")
	}
	assertSelection(*promotion.RunID)
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM agent_skills WHERE agent_id=$1`, second).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("message selection installed a skill")
	}
	if _, err := f.discussion.Send(f.ctx, f.a, f.channelID, input); err != nil {
		t.Fatalf("replay after source removal: %v", err)
	}
	// Retention includes a message selection even before any Run consumes it.
	unused, err := f.store.Queries().SkillVersionUnused(f.ctx, versionID)
	if err != nil || unused.Bool {
		t.Fatalf("selected version unused = %v, %v", unused, err)
	}
}

func TestMessageSkillAloneDoesNotInvokeAndRejectsUnavailableVersion(t *testing.T) {
	f := newChannelInvocationFixture(t)
	skillID, versionID := seedMessageSkill(t, f.discussionFixture)
	input := textSend("")
	addMessageSkill(t, &input, skillID, versionID)
	message := f.send(t, input)
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM channel_invocations WHERE trigger_message_id=$1`, message.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("skill alone invoked an Agent")
	}
	bad := taggedSend(t, f.seed.AgentA)
	addMessageSkill(t, &bad, skillID, uuid.New())
	if _, err := f.discussion.Send(f.ctx, f.a, f.channelID, bad); err == nil {
		t.Fatal("unavailable version accepted")
	}
}

func TestDirectMessageSkillsNormalizePinReplayAndHistory(t *testing.T) {
	f := newChannelInvocationFixture(t)
	skillID, versionID := seedMessageSkill(t, f.discussionFixture)
	input := SendInput{RequestKey: uuid.New(), Content: "  $message-review do this  ", SkillReferences: []SkillReference{{Start: 2, End: 17, SkillID: skillID, VersionID: versionID}}}
	target := MessageTarget{Kind: TargetConversation, ID: f.seed.DirectConversationA}
	principal := f.a
	principal.CompanyID = uuid.Nil
	result, err := f.conversations.Send(f.ctx, principal, target, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.RunID == nil || len(result.View.Message.SkillReferences) != 1 || result.View.Message.SkillReferences[0].Start != 0 {
		t.Fatalf("send selection = %+v", result)
	}
	var frozen string
	if err := f.pool.QueryRow(f.ctx, `SELECT context->'agent'->'selectedSkillKeys'->>0 FROM runs WHERE id=$1`, *result.RunID).Scan(&frozen); err != nil || frozen != "message-review" {
		t.Fatalf("frozen selection = %q, %v", frozen, err)
	}
	queuedInput := input
	queuedInput.RequestKey = uuid.New()
	queued, err := f.conversations.Send(f.ctx, principal, target, queuedInput)
	if err != nil || queued.QueueItem == nil {
		t.Fatalf("queue = %+v, %v", queued, err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `DELETE FROM agent_skills WHERE id=$1`, skillID)
	for _, original := range []SendInput{input, queuedInput} {
		if _, err := f.conversations.Send(f.ctx, principal, target, original); err != nil {
			t.Fatalf("replay after source removal: %v", err)
		}
	}
	input.SkillReferences = nil
	if _, err := f.conversations.Send(f.ctx, principal, target, input); err == nil {
		t.Fatal("replay changed skill identity")
	}
	page, err := f.conversations.ListMessages(f.ctx, principal, target, MessageListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.Message.ID == result.View.Message.ID {
			found = len(item.Message.SkillReferences) == 1 && item.Message.SkillReferences[0].VersionID == versionID
		}
	}
	if !found {
		t.Fatal("history lost pinned skill reference")
	}
}

func TestMessageSkillsCompanyBoundary(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	ownSkill, ownVersion := seedMessageSkill(t, f)
	foreign := f
	foreign.a.CompanyID = f.seed.CompanyB
	foreign.a.UserID = f.seed.UserB
	foreign.seed.AgentA = f.seed.AgentB
	foreignSkill, foreignVersion := seedMessageSkill(t, foreign)
	channel, err := f.conversations.CreateChannel(f.ctx, f.a, CreateChannelInput{RequestKey: uuid.New(), Name: "Skills access", AgentIDs: []uuid.UUID{f.seed.AgentA}, MemberUserIDs: []uuid.UUID{f.b.UserID}})
	if err != nil {
		t.Fatal(err)
	}
	input := textSend("")
	addMessageSkill(t, &input, ownSkill, ownVersion)
	if _, err := f.discussion.Send(f.ctx, f.b, channel.Conversation.ID, input); err != nil {
		t.Fatalf("company member cannot select another member's skill: %v", err)
	}
	input = textSend("")
	addMessageSkill(t, &input, foreignSkill, foreignVersion)
	if _, err := f.discussion.Send(f.ctx, f.a, channel.Conversation.ID, input); !errors.Is(err, ErrMessageSkillSelection) {
		t.Fatalf("foreign skill error = %v", err)
	}
}

func TestMessageSkillCatalogTracksConversationAgents(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	skillID, versionID := seedMessageSkill(t, f.discussionFixture)
	channel, err := f.conversations.CreateChannel(f.ctx, f.a, CreateChannelInput{RequestKey: uuid.New(), Name: "Dynamic skills"})
	if err != nil {
		t.Fatal(err)
	}
	scope := generated.ListMessageSkillCatalogParams{CompanyID: f.a.CompanyID, ActorUserID: f.a.UserID, ConversationID: channel.Conversation.ID}
	check := func(want int) {
		t.Helper()
		rows, err := f.store.Queries().ListMessageSkillCatalog(f.ctx, scope)
		if err != nil || len(rows) != want {
			t.Fatalf("catalog count = %d, want %d, error = %v", len(rows), want, err)
		}
		if want == 1 && rows[0].ID != skillID {
			t.Fatalf("catalog skill = %s, want %s", rows[0].ID, skillID)
		}
	}
	check(0)
	input := textSend("")
	addMessageSkill(t, &input, skillID, versionID)
	if _, err := f.discussion.Send(f.ctx, f.a, channel.Conversation.ID, input); !errors.Is(err, ErrMessageSkillSelection) {
		t.Fatalf("outside Agent skill error = %v", err)
	}
	added, err := f.conversations.SetChannelAgent(f.ctx, f.a, channel.Conversation.ID, ChannelAgentInput{ExpectedRevision: *channel.Conversation.ConfigurationRevision, AgentID: f.seed.AgentA, Active: true})
	if err != nil {
		t.Fatal(err)
	}
	check(1)
	if _, err := f.discussion.Send(f.ctx, f.a, channel.Conversation.ID, input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.conversations.SetChannelAgent(f.ctx, f.a, channel.Conversation.ID, ChannelAgentInput{ExpectedRevision: added.ConfigurationRevision, AgentID: f.seed.AgentA, Active: false}); err != nil {
		t.Fatal(err)
	}
	check(0)
	input.RequestKey = uuid.New()
	if _, err := f.discussion.Send(f.ctx, f.a, channel.Conversation.ID, input); !errors.Is(err, ErrMessageSkillSelection) {
		t.Fatalf("removed Agent skill error = %v", err)
	}
	scope.ConversationID = f.seed.DirectConversationA
	check(1)
	scope.ActorUserID = f.b.UserID
	check(0)
	scope.ActorUserID = f.a.UserID
	scope.ConversationID = uuid.Nil
	scope.AgentID = f.seed.AgentA
	check(1)
	group, err := f.conversations.CreateHumanDirect(f.ctx, f.a, CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}, AgentIDs: []uuid.UUID{f.seed.AgentA}})
	if err != nil {
		t.Fatal(err)
	}
	scope.AgentID = uuid.Nil
	scope.ConversationID = group.Conversation.ID
	check(1)
	scope.ActorUserID = f.c.UserID
	check(0)
}
