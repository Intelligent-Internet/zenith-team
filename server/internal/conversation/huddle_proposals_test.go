package conversation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestNormalizeHuddleProposal(t *testing.T) {
	t.Parallel()
	valid := HuddleProposalInput{
		RequestKey: "draft-1",
		ProjectID:  uuid.New(),
		Title:      " Fix login ",
		Brief:      " Verify the login flow ",
	}
	for _, tt := range []struct {
		name   string
		change func(*HuddleProposalInput)
		want   error
	}{
		{name: "trim content", change: func(*HuddleProposalInput) {}},
		{name: "empty title", change: func(in *HuddleProposalInput) { in.Title = "  " }, want: ErrHuddleInvalid},
		{name: "long brief", change: func(in *HuddleProposalInput) { in.Brief = strings.Repeat("a", 4001) }, want: ErrHuddleInvalid},
		{name: "missing project", change: func(in *HuddleProposalInput) { in.ProjectID = uuid.Nil }, want: ErrHuddleInvalid},
		{name: "missing key", change: func(in *HuddleProposalInput) { in.RequestKey = "" }, want: ErrHuddleInvalid},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := valid
			tt.change(&in)
			got, err := normalizeHuddleProposalInput(in)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got = %v, want %v", err, tt.want)
			}
			if err == nil && got.Title != "Fix login" {
				t.Fatalf("title = %q", got.Title)
			}
		})
	}
}

func TestHuddleProposalProjectionExpiresWithoutDeletingDraft(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, callState   string
		expired, disabled bool
		revision          int64
		want              string
	}{
		{name: "pending", callState: "open", revision: 2, want: "pending"},
		{name: "deadline", callState: "open", expired: true, revision: 2, want: "expired"},
		{name: "call ended", callState: "ended", revision: 2, want: "expired"},
		{name: "invitation removed", callState: "open", disabled: true, revision: 2, want: "pending"},
		{name: "agent replaced", callState: "open", revision: 3, want: "pending"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			until := time.Now().Add(time.Hour)
			if tt.expired {
				until = time.Now().Add(-time.Second)
			}
			row := generated.HuddleWorkProposal{
				Title:              "Keep my draft",
				State:              "pending",
				InvitationRevision: 2,
				ExpiresAt:          pgtype.Timestamptz{Time: until, Valid: true},
			}
			h := generated.Huddle{
				State:      tt.callState,
				DeadlineAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
			}
			got := huddleProposalProjection(row, h, tt.revision, tt.disabled, true)
			if got.State != tt.want || got.Title != row.Title || got.CanEdit != (tt.want == "pending") {
				t.Fatalf("got = %+v, want state %s", got, tt.want)
			}
		})
	}
}
