//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestHumanDirectGroupPrivacyAndIdentity(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	dbtest.AssertStatementRejected(
		t,
		f.ctx,
		f.pool,
		"23514",
		`INSERT INTO conversation_members(company_id,conversation_id,user_id,source_type) VALUES($1,$2,$3,'human_direct')`,
		f.a.CompanyID,
		id,
		f.owner.UserID,
	)
	dbtest.AssertStatementRejected(t, f.ctx, f.pool, "23514",
		`DELETE FROM conversation_members WHERE conversation_id=$1 AND user_id=$2`, id, f.c.UserID)
	dbtest.AssertStatementRejected(
		t,
		f.ctx,
		f.pool,
		"23514",
		`UPDATE conversation_human_directs SET participant_ids=ARRAY[$2::uuid] WHERE conversation_id=$1`,
		id,
		f.a.UserID,
	)
	if !created.Created || created.Conversation.MemberCount != 3 || created.Conversation.Peer != nil ||
		created.Conversation.Title == "" {
		t.Fatalf("group = %+v", created)
	}
	for _, caller := range []struct {
		principal auth.UserPrincipal
		peers     []uuid.UUID
	}{
		{f.a, []uuid.UUID{f.c.UserID, f.b.UserID}},
		{f.b, []uuid.UUID{f.c.UserID, f.a.UserID}},
		{f.c, []uuid.UUID{f.b.UserID, f.a.UserID}},
	} {
		reopened, err := f.conversations.CreateHumanDirect(
			f.ctx,
			caller.principal,
			CreateHumanDirectInput{PeerUserIDs: caller.peers},
		)
		if err != nil || reopened.Created || reopened.Conversation.ID != id {
			t.Fatalf("reopened = %+v, %v", reopened, err)
		}
		detail, err := f.conversations.FetchHumanDirect(f.ctx, caller.principal, id)
		if err != nil || len(detail.Members) != 3 {
			t.Fatalf("detail = %+v, %v", detail, err)
		}
		if _, err = f.discussion.Send(
			f.ctx,
			caller.principal,
			id,
			textSend("Private group message"),
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.conversations.FetchHumanDirect(f.ctx, f.owner, id); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("owner access = %v", err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.owner,
		id,
		textSend("Intrusion"),
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("owner send = %v", err)
	}
	pair, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil || pair.Conversation.ID == id || pair.Conversation.Peer == nil {
		t.Fatalf("pair = %+v, %v", pair, err)
	}
	page, err := f.discussion.ListMessages(f.ctx, f.c, id, DiscussionPageOptions{})
	if err != nil || len(page.Items) != 3 {
		t.Fatalf("messages = %+v, %v", page, err)
	}
}

func TestHumanDirectInvalidRecipients(t *testing.T) {
	f := newDiscussionFixture(t)
	tooMany := make([]uuid.UUID, 50)
	for i := range tooMany {
		tooMany[i] = uuid.New()
	}
	for _, peers := range [][]uuid.UUID{tooMany, nil, {uuid.Nil}, {f.a.UserID}, {f.b.UserID, f.b.UserID}, {f.b.UserID, uuid.New()}} {
		if _, err := f.conversations.CreateHumanDirect(
			f.ctx,
			f.a,
			CreateHumanDirectInput{PeerUserIDs: peers},
		); !errors.Is(
			err,
			ErrDiscussionInvalid,
		) {
			t.Fatalf("peers %v: error = %v", peers, err)
		}
	}
}

func TestHumanDirectGroupMemberRevocation(t *testing.T) {
	f := newDiscussionFixture(t)
	group, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	id := group.Conversation.ID
	revoke := func(userID uuid.UUID) {
		t.Helper()
		err := f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			if _, err := q.LockCompanyForMembershipMutation(f.ctx, f.a.CompanyID); err != nil {
				return err
			}
			if _, err := q.RevokeCompanyMember(
				f.ctx,
				generated.RevokeCompanyMemberParams{
					CompanyID:       f.a.CompanyID,
					UserID:          userID,
					ActorUserID:     f.owner.UserID,
					ExpectedVersion: 1,
				},
			); err != nil {
				return err
			}
			var pending []events.Event
			return RevokeDiscussionMembersInTx(
				f.ctx,
				q,
				&pending,
				DiscussionUserRevocation{
					CompanyID:   f.a.CompanyID,
					UserID:      userID,
					ActorUserID: f.owner.UserID,
				},
			)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	revoke(f.c.UserID)
	if _, err = f.discussion.Send(f.ctx, f.a, id, textSend("Still chatting")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.conversations.FetchHumanDirect(f.ctx, f.c, id); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("revoked read = %v", err)
	}
	revoke(f.b.UserID)
	if _, err = f.discussion.Send(
		f.ctx,
		f.a,
		id,
		textSend("Nobody else active"),
	); !errors.Is(
		err,
		ErrDiscussionReadOnly,
	) {
		t.Fatalf("send = %v", err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE company_members SET status='active',version=version+1 WHERE company_id=$1 AND user_id=$2`,
		f.a.CompanyID,
		f.b.UserID,
	)
	reopened, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.c.UserID, f.b.UserID}},
	)
	if err != nil || reopened.Created || reopened.Conversation.ID != id {
		t.Fatalf("reopen = %+v, %v", reopened, err)
	}
	if _, err = f.discussion.Send(f.ctx, f.b, id, textSend("Returned member")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.conversations.FetchHumanDirect(f.ctx, f.c, id); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("ineligible regrant = %v", err)
	}
}

func TestHumanDirectMixedParticipants(t *testing.T) {
	f := newDiscussionFixture(t)
	input := CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}, AgentIDs: []uuid.UUID{f.seed.AgentA}}
	created, err := f.conversations.CreateHumanDirect(f.ctx, f.a, input)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	if created.Conversation.MemberCount != 2 || created.Conversation.AgentCount != 1 ||
		created.Conversation.Peer != nil {
		t.Fatalf("mixed conversation = %+v", created.Conversation)
	}
	detail, err := f.conversations.FetchHumanDirect(f.ctx, f.b, id)
	if err != nil || len(detail.Members) != 2 || len(detail.Agents) != 1 ||
		detail.Agents[0].AgentID != f.seed.AgentA {
		t.Fatalf("detail = %+v, %v", detail, err)
	}
	reopened, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.b,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.a.UserID}, AgentIDs: input.AgentIDs},
	)
	if err != nil || reopened.Created || reopened.Conversation.ID != id {
		t.Fatalf("reopen = %+v, %v", reopened, err)
	}
	humans, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: input.PeerUserIDs},
	)
	if err != nil || humans.Conversation.ID == id {
		t.Fatalf("human-only = %+v, %v", humans, err)
	}
	if _, err = f.discussion.Send(f.ctx, f.b, id, textSend("Hello team")); err != nil {
		t.Fatal(err)
	}
	if _, err = f.conversations.FetchHumanDirect(f.ctx, f.owner, id); !errors.Is(err, ErrDiscussionNotFound) {
		t.Fatalf("nonparticipant owner = %v", err)
	}
	if _, err = f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{AgentIDs: []uuid.UUID{f.seed.AgentB}},
	); !errors.Is(
		err,
		ErrDiscussionInvalid,
	) {
		t.Fatalf("foreign agent = %v", err)
	}
}

func TestHumanDirectAgentsOnlyAndArchive(t *testing.T) {
	f := newDiscussionFixture(t)
	input := CreateHumanDirectInput{AgentIDs: []uuid.UUID{f.seed.AgentA}}
	created, err := f.conversations.CreateHumanDirect(f.ctx, f.a, input)
	if err != nil {
		t.Fatal(err)
	}
	if !created.Conversation.Capabilities.Send || created.Conversation.AgentCount != 1 ||
		created.Conversation.Title == "" {
		t.Fatalf("conversation = %+v", created.Conversation)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE agents SET archived_at=now() WHERE id=$1`, f.seed.AgentA)
	err = f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockCompanyForMembershipMutation(f.ctx, f.a.CompanyID); err != nil {
			return err
		}
		var pending []events.Event
		return RevokeDiscussionAgentsInTx(
			f.ctx,
			q,
			&pending,
			DiscussionAgentRevocation{CompanyID: f.a.CompanyID, AgentID: f.seed.AgentA},
		)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.discussion.Send(
		f.ctx,
		f.a,
		created.Conversation.ID,
		textSend("Inactive group"),
	); !errors.Is(
		err,
		ErrDiscussionReadOnly,
	) {
		t.Fatalf("send after revocation = %v", err)
	}
	archived, err := f.conversations.CreateHumanDirect(f.ctx, f.a, input)
	if err != nil || archived.Created || archived.Conversation.Capabilities.Send {
		t.Fatalf("archived group = %+v, %v", archived, err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE agents SET archived_at=NULL WHERE id=$1`, f.seed.AgentA)
	reopened, err := f.conversations.CreateHumanDirect(f.ctx, f.a, input)
	if err != nil || reopened.Created || reopened.Conversation.ID != created.Conversation.ID ||
		!reopened.Conversation.Capabilities.Send {
		t.Fatalf("reopen = %+v, %v", reopened, err)
	}
}

func TestHumanDirectMultipleAgents(t *testing.T) {
	f := newDiscussionFixture(t)
	var second uuid.UUID
	// An Agent template UserA created and set up on their own runtime, like
	// AgentA (ADR 0014). Tagging it in the group starts a tag, whose
	// Conversation belongs to the Agent's creator.
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO agents(company_id,name,title,reports_to_agent_id,provider,model,created_by_user_id) VALUES($1,'Assistant Two','Engineer',$2,'codex','test',$3) RETURNING id`, f.a.CompanyID, f.seed.AgentA, f.a.UserID).
		Scan(&second); err != nil {
		t.Fatal(err)
	}
	dbtest.SeedAgentBinding(t, f.ctx, f.pool, f.a.CompanyID, second, f.a.UserID, f.seed.RuntimeA, "codex")
	input := CreateHumanDirectInput{AgentIDs: []uuid.UUID{f.seed.AgentA, second}}
	created, err := f.conversations.CreateHumanDirect(f.ctx, f.a, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Conversation.MemberCount != 1 || created.Conversation.AgentCount != 2 ||
		!created.Conversation.Capabilities.Send {
		t.Fatalf("group = %+v", created)
	}
	reopened, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{AgentIDs: []uuid.UUID{second, f.seed.AgentA}},
	)
	if err != nil || reopened.Created || reopened.Conversation.ID != created.Conversation.ID {
		t.Fatalf("reopen = %+v, %v", reopened, err)
	}
	single, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{AgentIDs: []uuid.UUID{second}},
	)
	if err != nil || single.Conversation.ID == created.Conversation.ID {
		t.Fatalf("different set = %+v, %v", single, err)
	}
	mention := textSend("@A")
	mention.Body.References = []json.RawMessage{
		json.RawMessage(`{"type":"agent","agentId":"` + second.String() + `","start":0,"end":2}`),
	}
	if _, err = f.discussion.Send(f.ctx, f.a, created.Conversation.ID, mention); err != nil {
		t.Fatalf("agent mention = %v", err)
	}
	if _, err = generated.New(f.pool).
		GetDiscussionAgentHistoryGrant(f.ctx, generated.GetDiscussionAgentHistoryGrantParams{CompanyID: f.a.CompanyID, ConversationID: created.Conversation.ID, AgentID: second}); err != nil {
		t.Fatalf("participant history grant = %v", err)
	}
	if _, err = f.conversations.FetchHumanDirect(
		f.ctx,
		f.owner,
		created.Conversation.ID,
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("owner access = %v", err)
	}
}

func TestHumanDirectInviteMembers(t *testing.T) {
	f := newDiscussionFixture(t)
	original := CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID}}
	created, err := f.conversations.CreateHumanDirect(f.ctx, f.a, original)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	if !created.Conversation.Capabilities.ManageMembers {
		t.Fatal("group cannot invite")
	}
	if _, err = f.discussion.Send(f.ctx, f.a, id, textSend("Before invitation")); err != nil {
		t.Fatal(err)
	}
	input := InviteHumanDirectInput{
		MemberUserIDs: []uuid.UUID{f.owner.UserID, f.c.UserID},
		AgentIDs:      []uuid.UUID{f.seed.AgentA},
	}
	if _, err = f.conversations.InviteHumanDirectMembers(
		f.ctx,
		f.owner,
		id,
		input,
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("nonparticipant invite = %v", err)
	}
	// A separate group with the destination membership must keep its own history.
	other, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{
			PeerUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID, f.owner.UserID},
			AgentIDs:    input.AgentIDs,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	invited, err := f.conversations.InviteHumanDirectMembers(f.ctx, f.b, id, input)
	if err != nil {
		t.Fatal(err)
	}
	if invited.Conversation.ID != id || len(invited.Members) != 4 || len(invited.Agents) != 1 {
		t.Fatalf("invited = %+v", invited)
	}
	again, err := f.conversations.InviteHumanDirectMembers(f.ctx, f.b, id, input)
	if err != nil || again.Conversation.ConversationRevision != invited.Conversation.ConversationRevision {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	page, err := f.discussion.ListMessages(f.ctx, f.owner, id, DiscussionPageOptions{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("retained history = %+v, %v", page, err)
	}
	var notice *DiscussionSystem
	for _, item := range page.Items {
		if item.System != nil {
			notice = item.System
		}
	}
	if notice == nil || notice.Action != "members_added" || notice.ActorUserID == nil ||
		*notice.ActorUserID != f.b.UserID ||
		len(notice.SubjectUserIDs) != 1 ||
		notice.SubjectUserIDs[0] != f.owner.UserID ||
		len(notice.SubjectAgentIDs) != 1 ||
		notice.SubjectAgentIDs[0] != f.seed.AgentA {
		t.Fatalf("invitation notice = %+v", notice)
	}
	if _, err = generated.New(f.pool).
		GetDiscussionAgentHistoryGrant(f.ctx, generated.GetDiscussionAgentHistoryGrantParams{CompanyID: f.a.CompanyID, ConversationID: id, AgentID: f.seed.AgentA}); err != nil {
		t.Fatal(err)
	}
	page, err = f.discussion.ListMessages(f.ctx, f.a, other.Conversation.ID, DiscussionPageOptions{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("separate history = %+v, %v", page, err)
	}
	reopened, err := f.conversations.CreateHumanDirect(f.ctx, f.a, original)
	if err != nil || !reopened.Created || reopened.Conversation.ID == id {
		t.Fatalf("old recipients reused expanded DM = %+v, %v", reopened, err)
	}
	dbtest.AssertStatementRejected(
		t,
		f.ctx,
		f.pool,
		"23514",
		`UPDATE conversation_human_directs SET membership_expanded=false WHERE conversation_id=$1`,
		id,
	)
	dbtest.AssertStatementRejected(
		t,
		f.ctx,
		f.pool,
		"23514",
		`UPDATE conversation_human_directs SET participant_ids=array_remove(participant_ids,$2) WHERE conversation_id=$1`,
		id,
		f.owner.UserID,
	)
}

func TestHumanDirectInviteValidation(t *testing.T) {
	f := newDiscussionFixture(t)
	pair, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if pair.Conversation.Capabilities.ManageMembers {
		t.Fatal("pair allows invitation")
	}
	input := InviteHumanDirectInput{MemberUserIDs: []uuid.UUID{f.c.UserID}}
	if _, err = f.conversations.InviteHumanDirectMembers(
		f.ctx,
		f.a,
		pair.Conversation.ID,
		input,
	); !errors.Is(
		err,
		ErrDiscussionForbidden,
	) {
		t.Fatalf("pair invite = %v", err)
	}
	group, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []InviteHumanDirectInput{{}, {MemberUserIDs: []uuid.UUID{uuid.Nil}}, {MemberUserIDs: []uuid.UUID{f.owner.UserID, f.owner.UserID}}, {MemberUserIDs: []uuid.UUID{f.owner.UserID}, AgentIDs: []uuid.UUID{uuid.New()}}} {
		if _, err = f.conversations.InviteHumanDirectMembers(
			f.ctx,
			f.a,
			group.Conversation.ID,
			invalid,
		); !errors.Is(
			err,
			ErrDiscussionInvalid,
		) {
			t.Fatalf("invalid invite = %v", err)
		}
	}
	if _, err = f.conversations.FetchHumanDirect(
		f.ctx,
		f.owner,
		group.Conversation.ID,
	); !errors.Is(
		err,
		ErrDiscussionNotFound,
	) {
		t.Fatalf("partial grant = %v", err)
	}
}

func TestHumanDirectInviteConcurrentOpen(t *testing.T) {
	f := newDiscussionFixture(t)
	input := CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID}}
	created, err := f.conversations.CreateHumanDirect(f.ctx, f.a, input)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Conversation.ID
	locked, release := make(chan struct{}), make(chan struct{})
	lockResult := make(chan error, 1)
	go func() {
		lockResult <- f.store.WithinTx(f.ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
			if err := lockDiscussion(f.ctx, q, f.a, id); err != nil {
				return err
			}
			close(locked)
			select {
			case <-release:
				return nil
			case <-f.ctx.Done():
				return f.ctx.Err()
			}
		})
	}()
	select {
	case <-locked:
	case err := <-lockResult:
		t.Fatal(err)
	}
	// Release the database fence even when a lock-wait assertion fails.
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	invited := make(chan error, 1)
	go func() {
		_, err := f.conversations.InviteHumanDirectMembers(
			f.ctx,
			f.b,
			id,
			InviteHumanDirectInput{MemberUserIDs: []uuid.UUID{f.owner.UserID}},
		)
		invited <- err
	}()
	waitForDirectLocks(t, f, 1)
	type outcome struct {
		result HumanDirectResult
		err    error
	}
	opened := make(chan outcome, 1)
	go func() {
		result, err := f.conversations.CreateHumanDirect(f.ctx, f.a, input)
		opened <- outcome{result, err}
	}()
	waitForDirectLocks(t, f, 2)
	close(release)
	if err = <-lockResult; err != nil {
		t.Fatal(err)
	}
	if err = <-invited; err != nil {
		t.Fatal(err)
	}
	got := <-opened
	if got.err != nil || !got.result.Created || got.result.Conversation.ID == id ||
		got.result.Conversation.MemberCount != 3 {
		t.Fatalf("concurrent open = %+v, %v", got.result, got.err)
	}
}

func waitForDirectLocks(t *testing.T, f discussionFixture, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 5*time.Second)
	defer cancel()
	for {
		var waiting int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%LockDiscussionConversation%'`).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= count {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("operations did not reach conversation fence")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestHumanDirectConcurrentInvitations(t *testing.T) {
	f := newDiscussionFixture(t)
	created, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	// Each worker produces one result; buffering allows both to exit on assertion failure.
	results := make(chan error, 2)
	for _, input := range []InviteHumanDirectInput{{MemberUserIDs: []uuid.UUID{f.owner.UserID}}, {AgentIDs: []uuid.UUID{f.seed.AgentA}}} {
		go func() {
			<-start
			_, err := f.conversations.InviteHumanDirectMembers(f.ctx, f.b, created.Conversation.ID, input)
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	detail, err := f.conversations.FetchHumanDirect(f.ctx, f.a, created.Conversation.ID)
	if err != nil || len(detail.Members) != 4 || len(detail.Agents) != 1 {
		t.Fatalf("concurrent invitations = %+v, %v", detail, err)
	}
}

func TestHumanDirectGroupTitleTracksMembers(t *testing.T) {
	f := newDiscussionFixture(t)
	bName, cName, ownerName := strings.Repeat("B", 100), strings.Repeat("C", 100), strings.Repeat("O", 100)
	for id, name := range map[uuid.UUID]string{f.b.UserID: bName, f.c.UserID: cName, f.owner.UserID: ownerName} {
		dbtest.MustExec(t, f.ctx, f.pool, `UPDATE users SET display_name=$2 WHERE id=$1`, id, name)
	}
	group, err := f.conversations.CreateHumanDirect(
		f.ctx,
		f.a,
		CreateHumanDirectInput{PeerUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID}},
	)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := f.conversations.InviteHumanDirectMembers(
		f.ctx,
		f.a,
		group.Conversation.ID,
		InviteHumanDirectInput{
			MemberUserIDs: []uuid.UUID{f.owner.UserID},
			AgentIDs:      []uuid.UUID{f.seed.AgentA},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{bName, cName, ownerName, detail.Agents[0].Name} {
		if !strings.Contains(detail.Conversation.Title, name) {
			t.Fatalf("missing complete name %q in %q", name, detail.Conversation.Title)
		}
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE conversation_members SET status='revoked',revoked_at=now() WHERE conversation_id=$1 AND user_id=$2`,
		group.Conversation.ID,
		f.c.UserID,
	)
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE agents SET archived_at=now() WHERE id=$1`, f.seed.AgentA)
	updated, err := f.conversations.FetchHumanDirect(f.ctx, f.a, group.Conversation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(updated.Conversation.Title, cName) ||
		strings.Contains(updated.Conversation.Title, detail.Agents[0].Name) ||
		!strings.Contains(updated.Conversation.Title, ownerName) {
		t.Fatalf("stale title = %q", updated.Conversation.Title)
	}
}
