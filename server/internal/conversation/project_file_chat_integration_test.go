//go:build integration

package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// frameLog keeps every frame payload a topic published, marshaled as the
// wire would carry it.
type frameLog struct {
	mu     sync.Mutex
	frames []string
}

func recordFrames(t *testing.T, bus *events.Bus, topics ...string) *frameLog {
	t.Helper()
	log := &frameLog{}
	for _, topic := range topics {
		if err := bus.Subscribe(topic, func(_ context.Context, event events.Event) error {
			encoded, err := json.Marshal(event.Payload)
			if err != nil {
				return err
			}
			log.mu.Lock()
			defer log.mu.Unlock()
			log.frames = append(log.frames, event.Name+" "+string(encoded))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	return log
}

func (l *frameLog) mentioning(text string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []string
	for _, frame := range l.frames {
		if strings.Contains(frame, text) {
			found = append(found, frame)
		}
	}
	return found
}

// The owner's chat-file rule (ADR 0017): a file that is an active Project
// file shows only to that Project's readers, wherever it was shared. A
// channel member who cannot read the Project finds the message without the
// file (not a refused download), and no frame of the send names the file. A
// reader sees it. Once the file leaves every Project it is a plain chat upload
// again and follows the channel's members.
func TestChannelProjectFileShowsOnlyToProjectReaders(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	const filename = "q3-roadmap-confidential.txt"
	frames := recordFrames(t, f.bus, Topic, TopicMessage)

	channel, err := f.conversations.CreateChannel(f.ctx, f.a, CreateChannelInput{
		RequestKey: uuid.New(), Name: "Launch", MemberUserIDs: []uuid.UUID{f.b.UserID, f.c.UserID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var file uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		VALUES ($1, $2, gen_random_uuid(), $3, 'text/plain', 4, 'ready') RETURNING id`,
		f.a.CompanyID, f.a.UserID, filename).Scan(&file); err != nil {
		t.Fatal(err)
	}
	// ProjectA is restricted; the fixture owner UserA (f.a) reads it, b and c
	// hold no grant.
	dbtest.MustExec(t, f.ctx, f.pool, `INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
		VALUES ($1, $2, $3, 'document')`, f.a.CompanyID, f.seed.ProjectA, file)
	sent, err := f.discussion.Send(f.ctx, f.a, channel.Conversation.ID, DiscussionSendInput{
		RequestKey:    uuid.New(),
		Body:          DiscussionBody{Version: 1, Markdown: "The plan"},
		AttachmentIDs: []uuid.UUID{file},
	})
	if err != nil {
		t.Fatal(err)
	}
	if leaked := frames.mentioning(filename); len(leaked) > 0 {
		t.Fatalf("a frame of the send names the Project file: %v", leaked)
	}

	files := func(reader, message uuid.UUID) []string {
		t.Helper()
		principal := f.b
		switch reader {
		case f.a.UserID:
			principal = f.a
		case f.c.UserID:
			principal = f.c
		}
		page, err := f.discussion.ListMessages(
			f.ctx,
			principal,
			channel.Conversation.ID,
			DiscussionPageOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.ID != message {
				continue
			}
			names := []string{}
			for _, attached := range item.Attachments {
				names = append(names, attached.Filename)
			}
			return names
		}
		t.Fatalf("message %s missing from %s's page", message, reader)
		return nil
	}
	// withheld is the message's attachmentsWithheld for the reader: a file
	// is there that they may not open.
	withheld := func(reader uuid.UUID) bool {
		t.Helper()
		principal := map[uuid.UUID]auth.UserPrincipal{f.a.UserID: f.a, f.b.UserID: f.b, f.c.UserID: f.c}[reader]
		page, err := f.discussion.ListMessages(
			f.ctx,
			principal,
			channel.Conversation.ID,
			DiscussionPageOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			if item.ID == sent.Message.ID {
				return item.AttachmentsWithheld
			}
		}
		t.Fatalf("message missing from %s's page", reader)
		return false
	}
	opens := func(reader uuid.UUID) bool {
		t.Helper()
		_, err := generated.New(f.pool).
			AuthorizeAttachmentDownload(f.ctx, generated.AuthorizeAttachmentDownloadParams{
				CompanyID: f.a.CompanyID, AttachmentID: file, ActorUserID: reader,
			})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		return err == nil
	}

	if got := files(f.a.UserID, sent.Message.ID); len(got) != 1 || got[0] != filename || !opens(f.a.UserID) ||
		withheld(f.a.UserID) {
		t.Fatalf("Project reader sees %v (opens %t, withheld %t), want the file", got, opens(f.a.UserID),
			withheld(f.a.UserID))
	}
	for _, outsider := range []uuid.UUID{f.b.UserID, f.c.UserID} {
		if got := files(outsider, sent.Message.ID); len(got) != 0 || opens(outsider) || !withheld(outsider) {
			t.Fatalf(
				"non-reader channel member %s sees %v (opens %t, withheld %t), want no file and the flag",
				outsider,
				got,
				opens(outsider),
				withheld(outsider),
			)
		}
	}
	// A grant opens it to b; c still cannot.
	dbtest.GrantProject(t, f.ctx, f.pool, f.a.CompanyID, f.seed.ProjectA, f.b.UserID, "project.read")
	if got := files(f.b.UserID, sent.Message.ID); len(got) != 1 || !opens(f.b.UserID) || withheld(f.b.UserID) {
		t.Fatalf("new Project reader sees %v, want the file and no flag", got)
	}
	if got := files(f.c.UserID, sent.Message.ID); len(got) != 0 || opens(f.c.UserID) || !withheld(f.c.UserID) {
		t.Fatalf("non-reader sees %v after someone else's grant", got)
	}
	// A deleted message never says a file was withheld: the flag on the
	// tombstone would tell c that the removed message held a Project file.
	var removedFile uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		VALUES ($1, $2, gen_random_uuid(), 'removed.txt', 'text/plain', 4, 'ready') RETURNING id`,
		f.a.CompanyID, f.a.UserID).Scan(&removedFile); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(t, f.ctx, f.pool, `INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
		VALUES ($1, $2, $3, 'document')`, f.a.CompanyID, f.seed.ProjectA, removedFile)
	removed, err := f.discussion.Send(f.ctx, f.a, channel.Conversation.ID, DiscussionSendInput{
		RequestKey: uuid.New(), Body: DiscussionBody{Version: 1, Markdown: "Gone soon"},
		AttachmentIDs: []uuid.UUID{removedFile},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.discussion.DeleteMessage(f.ctx, f.a, DiscussionMessageTarget{
		ConversationID: channel.Conversation.ID, MessageID: removed.Message.ID,
	}, removed.Message.Revision); err != nil {
		t.Fatal(err)
	}
	tombstones, err := f.discussion.ListMessages(f.ctx, f.c, channel.Conversation.ID, DiscussionPageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range tombstones.Items {
		if item.ID == removed.Message.ID && item.AttachmentsWithheld {
			t.Fatal("the deleted message says a file was withheld from a non-reader")
		}
	}
	// Retire-then-read: once the file leaves the Project, c reads it as a
	// channel member (the widening ADR 0017 records).
	dbtest.MustExec(t, f.ctx, f.pool, `UPDATE project_files SET removed_at = clock_timestamp()
		WHERE project_id = $1 AND attachment_id = $2`, f.seed.ProjectA, file)
	if got := files(f.c.UserID, sent.Message.ID); len(got) != 1 || got[0] != filename || !opens(f.c.UserID) ||
		withheld(f.c.UserID) {
		t.Fatalf("channel member after the file was retired sees %v, want the file and no flag", got)
	}
}

// message.created carries a message's files to the chat's audience, which is
// not a Project file's audience: when any file on the message is an active
// Project file the frame carries none and says so, even for a sender who may
// open them all. Retired from the Project, the file rides the frame again.
func TestMessageCreatedFrameWithholdsProjectFiles(t *testing.T) {
	t.Parallel()
	f := newDiscussionFixture(t)
	const filename = "pricing-model.txt"
	q := generated.New(f.pool)
	var file, message uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO attachments
		(company_id, uploaded_by_user_id, request_key, filename, content_type, size_bytes, status)
		VALUES ($1, $2, gen_random_uuid(), $3, 'text/plain', 4, 'ready') RETURNING id`,
		f.a.CompanyID, f.a.UserID, filename).Scan(&file); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO conversation_items
		(company_id, conversation_id, run_id, seq, type, author_kind, author_id, content)
		VALUES ($1, $2, gen_random_uuid(), 2, 'user_message', 'user', $3, 'See the model') RETURNING id`,
		f.a.CompanyID, f.seed.ConversationA, f.a.UserID).Scan(&message); err != nil {
		t.Fatal(err)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`INSERT INTO message_attachments (company_id, message_id, attachment_id, position)
		VALUES ($1, $2, $3, 0)`,
		f.a.CompanyID,
		message,
		file,
	)
	// A second Project in the Company holds the file; the Task chat is
	// ProjectA's, so the chat's audience is not the file's.
	var other uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO projects (company_id, key, title) VALUES ($1, 'pricing', 'Pricing')
		RETURNING id`, f.a.CompanyID).
		Scan(&other); err != nil {
		t.Fatal(err)
	}
	dbtest.GrantProject(t, f.ctx, f.pool, f.a.CompanyID, other, f.a.UserID, "project.read")
	dbtest.MustExec(t, f.ctx, f.pool, `INSERT INTO project_files (company_id, project_id, attachment_id, file_type)
		VALUES ($1, $2, $3, 'document')`, f.a.CompanyID, other, file)

	frame := func() string {
		t.Helper()
		view, err := messageViewWithAttachments(f.ctx, q, f.a.UserID, f.a.CompanyID, MessageResponse{
			ID: message, ConversationID: f.seed.ConversationA, Type: ItemTypeUserMessage,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(view.Attachments) != 1 {
			t.Fatalf("sender's own view = %d files, want 1", len(view.Attachments))
		}
		encoded, err := json.Marshal(
			NewMemberMessageCreatedEvent(f.a.UserID, f.a.CompanyID, KindTask, nil, view).Payload,
		)
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	if got := frame(); strings.Contains(got, filename) || !strings.Contains(got, `"attachmentsWithheld":true`) {
		t.Fatalf("message.created with a Project file = %s", got)
	}
	// The chat page: b reads the Task chat's Project, not the file's, so the
	// page carries the message without the file and says one is withheld;
	// the sender, who reads both, gets the file and no flag.
	dbtest.GrantProject(t, f.ctx, f.pool, f.a.CompanyID, f.seed.ProjectA, f.b.UserID, "project.read")
	page := func(reader uuid.UUID) MessageView {
		t.Helper()
		views, err := buildMessageViews(f.ctx, q, reader, f.a.CompanyID, []generated.ConversationItem{
			{
				ID:             message,
				CompanyID:      f.a.CompanyID,
				ConversationID: f.seed.ConversationA,
				Type:           string(ItemTypeUserMessage),
			},
		})
		if err != nil || len(views) != 1 {
			t.Fatalf("chat page = %v, %v", views, err)
		}
		return views[0]
	}
	if view := page(f.b.UserID); len(view.Attachments) != 0 || !view.AttachmentsWithheld {
		t.Fatalf(
			"chat page for a reader outside the file's Project = %d files, withheld %t; want none and the flag",
			len(view.Attachments),
			view.AttachmentsWithheld,
		)
	}
	if view := page(f.a.UserID); len(view.Attachments) != 1 || view.AttachmentsWithheld {
		t.Fatalf("chat page for the sender = %d files, withheld %t; want the file and no flag",
			len(view.Attachments), view.AttachmentsWithheld)
	}
	dbtest.MustExec(
		t,
		f.ctx,
		f.pool,
		`UPDATE project_files SET removed_at = clock_timestamp() WHERE attachment_id = $1`,
		file,
	)
	if got := frame(); !strings.Contains(got, filename) || !strings.Contains(got, `"attachmentsWithheld":false`) {
		t.Fatalf("message.created after the file was retired = %s", got)
	}
}
