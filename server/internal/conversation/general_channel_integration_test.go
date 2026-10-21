//go:build integration

package conversation

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestGeneralChannelMembershipAndProtectedLifecycle(t *testing.T) {
	f := newDiscussionFixture(t)
	dbtest.MustExec(t, f.ctx, f.pool, `WITH people AS (
 INSERT INTO users(email,display_name) SELECT 'general-'||n||'@example.test','Member '||n FROM generate_series(1,300) n RETURNING id
 ) INSERT INTO company_members(company_id,user_id,role) SELECT $1,id,'member' FROM people`, f.a.CompanyID)
	join := func() {
		t.Helper()
		err := f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			if _, err := q.LockCompanyForMembershipMutation(f.ctx, f.a.CompanyID); err != nil {
				return err
			}
			pending := []events.Event{}
			return JoinGeneralChannelInTx(
				f.ctx,
				q,
				&pending,
				GeneralChannelMembership{CompanyID: f.a.CompanyID, UserID: f.b.UserID},
			)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE users SET avatar_url='https://example.test/'||repeat('a',2000) WHERE id IN (SELECT user_id FROM company_members WHERE company_id=$1)`,
		f.a.CompanyID,
	)
	join()
	row, err := f.store.Queries().GetGeneralChannelForUpdate(f.ctx, f.a.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.conversations.FetchChannel(f.ctx, f.b, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Conversation.IsGeneral || len(detail.Members) != 304 || len(detail.Agents) != 0 {
		t.Fatalf(
			"General=%v humans=%d agents=%d",
			detail.Conversation.IsGeneral,
			len(detail.Members),
			len(detail.Agents),
		)
	}
	management, err := f.conversations.ListChannelManagement(f.ctx, f.a, DiscussionListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(management.Items) != 1 || len(management.Items[0].Members) != 304 {
		t.Fatal("large General management roster was truncated")
	}
	name := "renamed"
	_, err = f.conversations.UpdateChannel(
		f.ctx,
		f.a,
		row.ID,
		ChannelUpdateInput{ExpectedRevision: *detail.Conversation.ConfigurationRevision, Name: &name},
	)
	if !errors.Is(err, ErrDiscussionForbidden) {
		t.Fatalf("rename: %v", err)
	}
	_, err = f.conversations.SetChannelArchived(
		f.ctx,
		f.a,
		row.ID,
		ChannelArchiveInput{ExpectedRevision: *detail.Conversation.ConfigurationRevision, Archived: true},
	)
	if !errors.Is(err, ErrDiscussionForbidden) {
		t.Fatalf("archive: %v", err)
	}
	err = f.conversations.LeaveChannel(f.ctx, f.b, row.ID, *detail.Conversation.ConfigurationRevision)
	if !errors.Is(err, ErrDiscussionForbidden) {
		t.Fatalf("leave: %v", err)
	}
	_, err = f.conversations.SetChannelMember(
		f.ctx,
		f.a,
		row.ID,
		ChannelMemberInput{
			ExpectedRevision: *detail.Conversation.ConfigurationRevision,
			UserID:           f.b.UserID,
			Active:           false,
		},
	)
	if !errors.Is(err, ErrDiscussionForbidden) {
		t.Fatalf("remove human: %v", err)
	}
	meta, err := f.conversations.SetChannelAgent(
		f.ctx,
		f.a,
		row.ID,
		ChannelAgentInput{
			ExpectedRevision: *detail.Conversation.ConfigurationRevision,
			AgentID:          f.seed.AgentA,
			Active:           true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta.Agents) != 1 {
		t.Fatal("explicit agent add missing")
	}
	join()
	after, err := f.conversations.FetchChannelManagement(f.ctx, f.a, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.ConfigurationRevision != meta.ConfigurationRevision || len(after.Agents) != 1 {
		t.Fatal("idempotent join changed channel or removed agent")
	}
	_, err = f.conversations.SetChannelAgent(
		f.ctx,
		f.a,
		row.ID,
		ChannelAgentInput{ExpectedRevision: after.ConfigurationRevision, AgentID: f.seed.AgentA, Active: false},
	)
	if err != nil {
		t.Fatal(err)
	}
}
