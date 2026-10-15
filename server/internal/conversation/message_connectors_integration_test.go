//go:build integration

package conversation

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/agent/connectorselection"
	"boardly.dev/boardly/server/internal/testutil/dbtest"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func seedMessageConnector(t *testing.T, f channelInvocationFixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	digest := strings.Repeat("a", 64)
	generation := uuid.New()
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO connector_catalog_items
 (company_id,provider,source,native_id,name,status,action,can_execute,contract_digest,tool_namespace,observed_at)
 VALUES($1,'codex','codex_app','gmail','Gmail','connected','none',true,$2,'[{"name":"search","enabled":true,"readOnly":true}]',clock_timestamp()) RETURNING id`, f.a.CompanyID, digest).Scan(&id); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO connector_policies(connector_id,company_id,desired_enabled,reviewed_contract_digest,review_required) VALUES($1,$2,true,$3,false)`,
		id,
		f.a.CompanyID,
		digest,
	)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO connector_discovery_generations(runtime_id,company_id,provider,generation,manifest_digest,observed_at) VALUES($1,$2,'codex',$3,$4,clock_timestamp())`,
		f.seed.RuntimeA,
		f.a.CompanyID,
		generation,
		digest,
	)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO connector_runtime_observations(runtime_id,connector_id,company_id,provider,generation,name,status,action,can_execute,contract_digest,tool_namespace,observed_at) VALUES($1,$2,$3,'codex',$4,'Gmail','connected','none',true,$5,'[{"name":"search","enabled":true,"readOnly":true}]',clock_timestamp())`,
		f.seed.RuntimeA,
		id,
		f.a.CompanyID,
		generation,
		digest,
	)
	return id
}

func addMessageConnector(t *testing.T, input *DiscussionSendInput, id uuid.UUID) {
	t.Helper()
	start := utf16Len(input.Body.Markdown)
	input.Body.Markdown += "$Gmail"
	raw, err := json.Marshal(discussionReference{Type: "connector", ConnectorID: &id, Start: start, End: start + 6})
	if err != nil {
		t.Fatal(err)
	}
	input.Body.References = append(input.Body.References, raw)
}

func TestMessageConnectorsScopeFanoutAndNoInvocation(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	id := seedMessageConnector(t, f)
	scope := connectorselection.MessageConnectorScope{ConversationID: f.channelID}
	own, err := connectorselection.ReadMessageConnectors(f.ctx, f.store.Queries(), f.a, scope)
	if err != nil || len(own) != 1 || own[0].ID != id {
		t.Fatalf("own catalog = %+v, %v", own, err)
	}
	other, err := connectorselection.ReadMessageConnectors(f.ctx, f.store.Queries(), f.b, scope)
	if err != nil || len(other) != 0 {
		t.Fatalf("other member catalog = %+v, %v", other, err)
	}
	only := textSend("")
	addMessageConnector(t, &only, id)
	if _, err := f.discussion.Send(f.ctx, f.b, f.channelID, only); !errors.Is(err, ErrMessageConnectorSelection) {
		t.Fatalf("other member send = %v", err)
	}
	message := f.send(t, only)
	var count int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM channel_invocations WHERE trigger_message_id=$1`, message.ID).
		Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf("plugin-only invocations = %d, %v", count, err)
	}
	var second uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO agents(company_id,name,title,reports_to_agent_id,provider,model,created_by_user_id) VALUES($1,'Second','Engineer',$2,'codex','test',$3) RETURNING id`, f.a.CompanyID, f.seed.AgentA, f.a.UserID).
		Scan(&second); err != nil {
		t.Fatal(err)
	}
	dbtest.SeedAgentBinding(t, f.ctx, f.pool, f.a.CompanyID, second, f.a.UserID, f.seed.RuntimeA, "codex")
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{
			RequestKey: uuid.New(),
			Name:       "Plugin fanout",
			AgentIDs:   []uuid.UUID{f.seed.AgentA, second},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	f.channelID = channel.Conversation.ID
	input := taggedSend(t, f.seed.AgentA, second)
	addMessageConnector(t, &input, id)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE agents SET settings=jsonb_build_object('capabilities',jsonb_build_object('disabledMcpServers',jsonb_build_array($2::text))) WHERE id=$1`,
		second,
		id.String(),
	)
	if _, err := f.discussion.Send(f.ctx, f.a, f.channelID, input); !errors.Is(err, ErrMessageConnectorSelection) {
		t.Fatalf("disabled Agent send = %v", err)
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM conversation_items WHERE request_key=$1`, input.RequestKey).
		Scan(&count); err != nil ||
		count != 0 {
		t.Fatalf("rejected send left %d messages: %v", count, err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE agents SET settings='{}' WHERE id=$1`, second)
	message = f.send(t, input)
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM runs r JOIN channel_invocations i ON i.id=r.invocation_id JOIN run_connector_selections s ON s.run_id=r.id WHERE i.trigger_message_id=$1 AND s.connector_id=$2 AND r.connector_selection_mode='exact'`, message.ID, id).
		Scan(&count); err != nil ||
		count != 2 {
		t.Fatalf("exact fanout = %d, %v", count, err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE connector_runtime_observations SET status='unavailable',can_execute=false,contract_digest=NULL,tool_namespace=NULL WHERE connector_id=$1`,
		id,
	)
	if _, err := f.discussion.Send(f.ctx, f.a, f.channelID, input); err != nil {
		t.Fatalf("idempotent replay = %v", err)
	}
}

func TestDirectMessageConnectorsReplayHistoryQueueAndRetry(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	id := seedMessageConnector(t, f)
	principal := f.a
	principal.CompanyID = uuid.Nil
	target := MessageTarget{Kind: TargetConversation, ID: f.seed.DirectConversationA}
	input := SendInput{
		RequestKey:          uuid.New(),
		Content:             "  $Gmail search  ",
		ConnectorReferences: []ConnectorReference{{ConnectorID: id, Start: 2, End: 8}},
	}
	result, err := f.conversations.Send(f.ctx, principal, target, input)
	if err != nil || result.RunID == nil || len(result.View.Message.ConnectorReferences) != 1 ||
		result.View.Message.ConnectorReferences[0].Start != 0 {
		t.Fatalf("send = %+v, %v", result, err)
	}
	queuedInput := input
	queuedInput.RequestKey = uuid.New()
	queued, err := f.conversations.Send(f.ctx, principal, target, queuedInput)
	if err != nil || queued.QueueItem == nil {
		t.Fatalf("queue = %+v, %v", queued, err)
	}
	for _, original := range []SendInput{input, queuedInput} {
		if _, err := f.conversations.Send(f.ctx, principal, target, original); err != nil {
			t.Fatal(err)
		}
	}
	input.ConnectorReferences = nil
	if _, err := f.conversations.Send(f.ctx, principal, target, input); err == nil {
		t.Fatal("replay lost identity")
	}
	page, err := f.conversations.ListMessages(f.ctx, principal, target, MessageListOptions{Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range page.Items {
		if item.Message.ID == result.View.Message.ID {
			found = len(item.Message.ConnectorReferences) == 1 &&
				item.Message.ConnectorReferences[0].ConnectorID == id
		}
	}
	if !found {
		t.Fatal("history lost connector")
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE runs SET status='failed',finished_at=clock_timestamp() WHERE id=$1`,
		*result.RunID,
	)
	retry, err := f.store.Queries().
		CreateRetryRun(f.ctx, generated.CreateRetryRunParams{ParentRunID: *result.RunID})
	if err != nil {
		t.Fatal(err)
	}
	refs, err := f.store.Queries().ListSelectedRunConnectorIDs(f.ctx, retry.ID)
	if err != nil || len(refs) != 1 || refs[0] != id || retry.ConnectorSelectionMode != "exact" {
		t.Fatalf("retry selection = %+v, %v", refs, err)
	}
}

func TestMessageConnectorCatalogTracksRosterAndDiscovery(t *testing.T) {
	t.Parallel()
	f := newChannelInvocationFixture(t)
	id := seedMessageConnector(t, f)
	channel, err := f.conversations.CreateChannel(
		f.ctx,
		f.a,
		CreateChannelInput{RequestKey: uuid.New(), Name: "Dynamic plugin catalog"},
	)
	if err != nil {
		t.Fatal(err)
	}
	scope := connectorselection.MessageConnectorScope{ConversationID: channel.Conversation.ID}
	check := func(want int) {
		t.Helper()
		items, err := connectorselection.ReadMessageConnectors(f.ctx, f.store.Queries(), f.a, scope)
		if err != nil || len(items) != want {
			t.Fatalf("catalog = %+v, want %d, error %v", items, want, err)
		}
		if want == 1 && items[0].ID != id {
			t.Fatalf("connector = %s, want %s", items[0].ID, id)
		}
	}
	check(0)
	added, err := f.conversations.SetChannelAgent(
		f.ctx,
		f.a,
		channel.Conversation.ID,
		ChannelAgentInput{
			ExpectedRevision: *channel.Conversation.ConfigurationRevision,
			AgentID:          f.seed.AgentA,
			Active:           true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	check(1)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE connector_discovery_generations SET observed_at=clock_timestamp()-interval '1 hour' WHERE runtime_id=$1`,
		f.seed.RuntimeA,
	)
	check(0)
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE connector_discovery_generations SET observed_at=clock_timestamp() WHERE runtime_id=$1`,
		f.seed.RuntimeA,
	)
	check(1)
	if _, err := f.conversations.SetChannelAgent(
		f.ctx,
		f.a,
		channel.Conversation.ID,
		ChannelAgentInput{ExpectedRevision: added.ConfigurationRevision, AgentID: f.seed.AgentA, Active: false},
	); err != nil {
		t.Fatal(err)
	}
	check(0)
	scope = connectorselection.MessageConnectorScope{AgentID: f.seed.AgentA}
	check(1)
	scope = connectorselection.MessageConnectorScope{ConversationID: f.seed.DirectConversationA}
	check(1)
	others, err := connectorselection.ReadMessageConnectors(f.ctx, f.store.Queries(), f.b, scope)
	if err != nil || len(others) != 0 {
		t.Fatalf("another user's direct chat = %+v, %v", others, err)
	}
	scope = connectorselection.MessageConnectorScope{AgentID: f.seed.AgentB}
	check(0)
}
