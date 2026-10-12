package attachment

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// IA-FR4: a completion the uploading Run proved names that Run's Agent, the
// member it works for and the Run (D31); without one it names the member.
func TestCompletedEffectNamesTheRunThatUploaded(t *testing.T) {
	t.Parallel()
	uploader := uuid.Must(uuid.NewV7())
	byRun := events.RunActor(uuid.Must(uuid.NewV7()).String(), uuid.Must(uuid.NewV7()).String(), uploader.String())
	for name, test := range map[string]struct {
		run  events.Actor
		want events.Actor
	}{
		"uploaded by the member": {want: events.UserActor(uploader.String())},
		"uploaded by their Run":  {run: byRun, want: byRun},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var pending []events.Event
			writeCompletedEffect(&pending, LifecycleEffect{
				AttachmentID: uuid.Must(uuid.NewV7()), CompanyID: uuid.Must(uuid.NewV7()),
				UploadedByUserID: uploader, CompletedAttachment: &Response{}, Revision: 2,
				ActorKind: "user", ActorID: &uploader, RunActor: test.run,
			})
			if len(pending) != 1 || pending[0].Actor != test.want {
				t.Fatalf("attachment.completed = %+v, want actor %+v", pending, test.want)
			}
		})
	}
}

// Link visibility is now a fact on the payload rather than an audience on the
// event, so this pins what the frame carries for each of the three link
// shapes; the consumer that delivers turns those facts into a route.
func TestAttachmentDeletedPayloadFollowsLinkVisibility(t *testing.T) {
	t.Parallel()
	companyID := uuid.Must(uuid.NewV7())
	uploaderID := uuid.Must(uuid.NewV7())
	directCreatorID := uuid.Must(uuid.NewV7())
	messageID := uuid.Must(uuid.NewV7())
	conversationID := uuid.Must(uuid.NewV7())
	effect := LifecycleEffect{
		AttachmentID:     uuid.Must(uuid.NewV7()),
		CompanyID:        companyID,
		UploadedByUserID: uploaderID,
		Revision:         3,
		ActorKind:        "system",
	}
	linked := func(kind string, creatorID uuid.NullUUID) generated.GetAttachmentRealtimeRouteRow {
		return generated.GetAttachmentRealtimeRouteRow{
			MessageID:                 uuid.NullUUID{UUID: messageID, Valid: true},
			ConversationID:            uuid.NullUUID{UUID: conversationID, Valid: true},
			ConversationKind:          pgtype.Text{String: kind, Valid: true},
			ConversationCreatorUserID: creatorID,
			UploadedByUserID:          uploaderID,
		}
	}
	tests := map[string]struct {
		route         generated.GetAttachmentRealtimeRouteRow
		wantLinked    bool
		wantKind      string
		wantCreatorID uuid.UUID
	}{
		"unlinked attachment is uploader-private": {
			route: generated.GetAttachmentRealtimeRouteRow{UploadedByUserID: uploaderID},
		},
		"task-linked attachment is company-visible": {
			route:      linked("task", uuid.NullUUID{}),
			wantLinked: true,
			wantKind:   "task",
		},
		"direct-linked attachment is creator-private": {
			route: linked(
				"direct",
				uuid.NullUUID{UUID: directCreatorID, Valid: true},
			),
			wantLinked:    true,
			wantKind:      "direct",
			wantCreatorID: directCreatorID,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := attachmentDeletedPayload(effect, test.route)
			if got.AttachmentID != effect.AttachmentID || got.Revision != effect.Revision ||
				got.CompanyID != companyID || got.UploadedByUserID != uploaderID {
				t.Fatalf("attachmentDeletedPayload() = %+v", got)
			}
			if got.ConversationKind != test.wantKind ||
				got.ConversationCreatorUserID != test.wantCreatorID {
				t.Fatalf(
					"attachmentDeletedPayload() route = %q/%s, want %q/%s",
					got.ConversationKind, got.ConversationCreatorUserID,
					test.wantKind, test.wantCreatorID,
				)
			}
			if !test.wantLinked {
				if got.MessageID != nil || got.ConversationID != nil {
					t.Fatalf("unlinked attachmentDeletedPayload() = %+v", got)
				}
				return
			}
			if got.MessageID == nil || *got.MessageID != messageID ||
				got.ConversationID == nil || *got.ConversationID != conversationID {
				t.Fatalf("linked attachmentDeletedPayload() = %+v", got)
			}
		})
	}
}
