//go:build integration

package canvas

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/testutil/dbtest"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestBoardSharingIncludesMemberAvatars(t *testing.T) {
	t.Parallel()
	s, pool, owner, member := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, owner, member)
	for _, actor := range []struct {
		id  uuid.UUID
		url string
	}{
		{owner.UserID, "https://example.com/owner.png"},
		{member.UserID, "https://example.com/member.png"},
	} {
		dbtest.MustExec(t, ctx, pool, `UPDATE users SET avatar_url=$2 WHERE id=$1`, actor.id, actor.url)
	}
	access, err := s.Access(ctx, member, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	if access.Owner.AvatarURL == nil || *access.Owner.AvatarURL != "https://example.com/owner.png" {
		t.Fatalf("owner avatar = %v, want owner profile image", access.Owner.AvatarURL)
	}
	if len(access.Grants) != 1 || access.Grants[0].Actor.AvatarURL == nil ||
		*access.Grants[0].Actor.AvatarURL != "https://example.com/member.png" {
		t.Fatalf("grants = %+v, want member profile image", access.Grants)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE users SET deleted_at=now() WHERE id=$1`, member.UserID)
	access, err = s.Access(ctx, owner, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Grants) != 1 || access.Grants[0].Actor.AvatarURL != nil {
		t.Fatalf("grants = %+v, want no deleted member avatar", access.Grants)
	}
}

func TestBoardSharingPrivateCompanyPublicAndRevocation(t *testing.T) {
	t.Parallel()
	s, pool, owner, member := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, owner)
	if board.Visibility != "private" || !board.CanShare {
		t.Fatalf("new Board is not private and owner-managed: %+v", board)
	}
	scope := BoardScope{BoardID: board.ID}
	if _, err := s.Snapshot(ctx, member, scope, 0); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("unshared private Board: %v", err)
	}
	list, err := s.List(ctx, member, BoardListInput{})
	if err != nil || len(list.Boards) != 0 {
		t.Fatalf("private library leaked: %+v, %v", list, err)
	}
	access, err := s.Access(ctx, owner, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant := BoardAccessInput{
		ExpectedRevision: access.Revision,
		Actor:            protocol.ActorRef{ID: member.UserID, Kind: protocol.ActorKindUser},
		Access:           "read",
	}
	if err := s.SetAccess(ctx, owner, board.ID, grant); err != nil {
		t.Fatal(err)
	}
	read, err := s.Snapshot(ctx, member, scope, 0)
	if err != nil || read.Board.CanEdit || read.Board.CanShare {
		t.Fatalf("read grant: %+v, %v", read, err)
	}
	batch := BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{stickyCommand("note", "Shared content")}}
	if _, err := s.Mutate(ctx, member, scope, batch); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("reader edited: %v", err)
	}
	access, _ = s.Access(ctx, owner, board.ID)
	grant.ExpectedRevision, grant.Access = access.Revision, "edit"
	if err := s.SetAccess(ctx, owner, board.ID, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(ctx, member, scope, batch); err != nil {
		t.Fatal(err)
	}
	access, _ = s.Access(ctx, owner, board.ID)
	grant.ExpectedRevision, grant.Access = access.Revision, "none"
	if err := s.SetAccess(ctx, owner, board.ID, grant); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(ctx, member, scope, batch); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("revoked receipt replay: %v", err)
	}
	access, _ = s.Access(ctx, owner, board.ID)
	if err := s.SetVisibility(
		ctx,
		owner,
		board.ID,
		BoardVisibilityInput{ExpectedRevision: access.Revision, Visibility: "company", CompanyAccess: "read"},
	); err != nil {
		t.Fatal(err)
	}
	read, err = s.Snapshot(ctx, member, scope, 0)
	if err != nil || read.Board.CanEdit {
		t.Fatalf("Company read: %+v, %v", read, err)
	}
	access, _ = s.Access(ctx, owner, board.ID)
	if err := s.SetVisibility(
		ctx,
		owner,
		board.ID,
		BoardVisibilityInput{ExpectedRevision: access.Revision, Visibility: "public", CompanyAccess: "read"},
	); err != nil {
		t.Fatal(err)
	}
	access, _ = s.Access(ctx, owner, board.ID)
	if access.PublicToken == nil {
		t.Fatal("no public token")
	}
	token := *access.PublicToken
	public, err := s.PublicSnapshot(ctx, token)
	if err != nil || public.Board.CanEdit || public.Board.CanShare || len(public.Items) != 1 {
		t.Fatalf("public view: %+v, %v", public, err)
	}
	hydrated, err := s.PublicHydrate(ctx, token, BoardHydrateInput{ItemIDs: []uuid.UUID{public.Items[0].ID}})
	if err != nil || len(hydrated.Items) != 1 || hydrated.Items[0].Text != "Shared content" ||
		len(hydrated.Items[0].State) != 0 {
		t.Fatalf("public hydration must contain rendered text only: %+v, %v", hydrated, err)
	}
	if _, err := s.PublicSnapshot(ctx, uuid.New()); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("guessed token: %v", err)
	}
	access, _ = s.Access(ctx, owner, board.ID)
	if err := s.SetVisibility(
		ctx,
		owner,
		board.ID,
		BoardVisibilityInput{ExpectedRevision: access.Revision, Visibility: "private", CompanyAccess: "read"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PublicSnapshot(ctx, token); !errors.Is(err, ErrBoardNotFound) {
		t.Fatalf("revoked public link: %v", err)
	}
	if _, err := s.PublicHydrate(
		ctx,
		token,
		BoardHydrateInput{ItemIDs: []uuid.UUID{public.Items[0].ID}},
	); !errors.Is(
		err,
		ErrBoardNotFound,
	) {
		t.Fatalf("revoked public hydration: %v", err)
	}
	access, _ = s.Access(ctx, owner, board.ID)
	if err := s.SetVisibility(
		ctx,
		owner,
		board.ID,
		BoardVisibilityInput{ExpectedRevision: access.Revision, Visibility: "public", CompanyAccess: "read"},
	); err != nil {
		t.Fatal(err)
	}
	access, _ = s.Access(ctx, owner, board.ID)
	if access.PublicToken == nil || *access.PublicToken == token {
		t.Fatal("republishing revived revoked token")
	}
	// Agent invitations cannot exceed the execution authorizer's current Board access.
	agent, run := boardTestRun(t, pool, member)
	access, _ = s.Access(ctx, owner, board.ID)
	if err := s.SetAccess(
		ctx,
		owner,
		board.ID,
		BoardAccessInput{
			ExpectedRevision: access.Revision,
			Actor:            protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent},
			Access:           "edit",
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(
		ctx,
		member,
		BoardScope{BoardID: board.ID, RunID: run},
		batch,
	); !errors.Is(
		err,
		ErrBoardForbidden,
	) {
		t.Fatalf("Agent exceeded member read authority: %v", err)
	}
}

// An archived Board is read-only, but whoever manages it can still narrow who
// reaches it. Widening, or editing its details, waits for recovery.
func TestBoardArchivedBoardsOnlyNarrowSharing(t *testing.T) {
	t.Parallel()
	s, _, owner, member := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, owner, member)
	revision := func() int64 {
		t.Helper()
		access, err := s.Access(ctx, owner, board.ID)
		if err != nil {
			t.Fatal(err)
		}
		return access.Revision
	}
	if err := s.SetVisibility(ctx, owner, board.ID, BoardVisibilityInput{
		ExpectedRevision: revision(), Visibility: "public", CompanyAccess: "read",
	}); err != nil {
		t.Fatal(err)
	}
	archive, recover := true, false
	archived, err := s.Update(ctx, owner, board.ID, UpdateBoardInput{ExpectedRevision: revision(), Archived: &archive})
	if err != nil {
		t.Fatal(err)
	}
	title := "Renamed"
	if _, err := s.Update(ctx, owner, board.ID, UpdateBoardInput{ExpectedRevision: archived.Revision, Title: &title}); !errors.Is(err, ErrBoardArchived) {
		t.Fatalf("renaming an archived Board = %v, want ErrBoardArchived", err)
	}
	memberRef := protocol.ActorRef{ID: member.UserID, Kind: protocol.ActorKindUser}
	if err := s.SetAccess(ctx, owner, board.ID, BoardAccessInput{
		ExpectedRevision: revision(), Actor: memberRef, Access: "manage",
	}); !errors.Is(err, ErrBoardArchived) {
		t.Fatalf("widening a grant on an archived Board = %v", err)
	}
	if err := s.SetAccess(ctx, owner, board.ID, BoardAccessInput{
		ExpectedRevision: revision(), Actor: memberRef, Access: "read",
	}); err != nil {
		t.Fatalf("narrowing a grant on an archived Board = %v", err)
	}
	if err := s.SetAccess(ctx, owner, board.ID, BoardAccessInput{
		ExpectedRevision: revision(), Actor: memberRef, Access: "none",
	}); err != nil {
		t.Fatalf("removing a grant on an archived Board = %v", err)
	}
	if err := s.SetVisibility(ctx, owner, board.ID, BoardVisibilityInput{
		ExpectedRevision: revision(), Visibility: "private", CompanyAccess: "read",
	}); err != nil {
		t.Fatalf("unpublishing an archived Board = %v", err)
	}
	if err := s.SetVisibility(ctx, owner, board.ID, BoardVisibilityInput{
		ExpectedRevision: revision(), Visibility: "company", CompanyAccess: "read",
	}); !errors.Is(err, ErrBoardArchived) {
		t.Fatalf("widening visibility on an archived Board = %v", err)
	}
	recovered, err := s.Update(ctx, owner, board.ID, UpdateBoardInput{ExpectedRevision: revision(), Archived: &recover, Title: &title})
	if err != nil || recovered.Title != title || recovered.ArchivedAt != nil {
		t.Fatalf("recovering with a new title = %+v, %v", recovered, err)
	}
}

// When nobody active manages a Board any longer, a Company Whiteboard manager
// can find it and take it over, so a public link never outlives its owner.
func TestBoardUnownedBoardsCanBeClaimed(t *testing.T) {
	t.Parallel()
	s, pool, owner, member := boardFixture(t)
	ctx := t.Context()
	board := createTestBoard(t, s, member)
	access, err := s.Access(ctx, member, board.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetVisibility(ctx, member, board.ID, BoardVisibilityInput{
		ExpectedRevision: access.Revision, Visibility: "public", CompanyAccess: "read",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.List(ctx, member, BoardListInput{Unowned: true}); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("unowned listing without whiteboards.manage = %v", err)
	}
	list, err := s.List(ctx, owner, BoardListInput{Unowned: true})
	if err != nil || len(list.Boards) != 0 {
		t.Fatalf("unowned while its creator is active = %+v, %v", list, err)
	}
	dbtest.MustExec(t, ctx, pool, `UPDATE company_members SET status='revoked' WHERE company_id=$1 AND user_id=$2`,
		member.CompanyID, member.UserID)
	list, err = s.List(ctx, owner, BoardListInput{Unowned: true})
	if err != nil || len(list.Boards) != 1 || list.Boards[0].ID != board.ID {
		t.Fatalf("unowned after the creator left = %+v, %v", list, err)
	}
	if err = s.SetVisibility(ctx, owner, board.ID, BoardVisibilityInput{
		ExpectedRevision: list.Boards[0].Revision, Visibility: "private", CompanyAccess: "read",
	}); !errors.Is(err, ErrBoardForbidden) {
		t.Fatalf("unpublishing before claiming = %v", err)
	}
	claimed, err := s.Claim(ctx, owner, board.ID, list.Boards[0].Revision)
	if err != nil || !claimed.CanShare {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	if err = s.SetVisibility(ctx, owner, board.ID, BoardVisibilityInput{
		ExpectedRevision: claimed.Revision, Visibility: "private", CompanyAccess: "read",
	}); err != nil {
		t.Fatalf("unpublishing after claiming = %v", err)
	}
	if _, err = s.Claim(ctx, owner, board.ID, claimed.Revision+1); !errors.Is(err, ErrBoardConflict) {
		t.Fatalf("claiming a managed Board = %v, want ErrBoardConflict", err)
	}
}
