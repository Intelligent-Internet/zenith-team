package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/agent"
	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/calendar"
	"boardly.dev/boardly/server/internal/canvas"
	"boardly.dev/boardly/server/internal/company"
	"boardly.dev/boardly/server/internal/conversation"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/execution"
	"boardly.dev/boardly/server/internal/inbox"
	"boardly.dev/boardly/server/internal/mailbox"
	"boardly.dev/boardly/server/internal/realtime"
	"boardly.dev/boardly/server/internal/schedule"
	"boardly.dev/boardly/server/internal/task"
	"boardly.dev/boardly/server/internal/wiki"
	"boardly.dev/boardly/server/pkg/protocol"
)

// registerRealtimeRouting is the browser plane's consumer: it subscribes to
// the domains whose facts reach a browser and hands each frame to the
// broadcaster addressed to whoever may see it.
//
// The two sides stay apart on purpose. The bus is pub/sub and knows nothing
// about rooms; the Broadcaster knows rooms and nothing about domains; this
// file is the only place that knows both, and it is the whole answer to "who
// sees what".
//
// Every subscription is written out by hand. A topic missing from this file
// never reaches a browser — that is what makes the daemon wakeup facts
// internal — and the list is exhaustive rather than defaulted, because
// forgetting a line drops frames where defaulting one would broadcast them.
// whiteboardHintRouting decides who hears about a Board commit. viewers names
// the people with a Board open (presence sessions); without it every hint
// reaches the whole audience at once.
type whiteboardHintRouting struct {
	viewers interface {
		Sessions(context.Context, string) ([]realtime.CanvasSession, error)
	}
	// libraryDelay gathers the rest of the audience's hints; it defaults to
	// three seconds.
	libraryDelay time.Duration
	// runs takes an ended Run's Agent sessions off every Board it was on.
	runs interface {
		RemoveRun(context.Context, uuid.UUID) error
	}
}

// whiteboardLibraryHints defers the hints of people who only see a Board in
// their library: one hint per Board per delay, carrying its latest sequence,
// delivered off the request path.
type whiteboardLibraryHints struct {
	mu      sync.Mutex
	pending map[uuid.UUID]events.Event
	delay   time.Duration
	send    func(events.Event)
}

func (h *whiteboardLibraryHints) add(board uuid.UUID, event events.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, waiting := h.pending[board]
	h.pending[board] = event
	if waiting {
		return
	}
	time.AfterFunc(h.delay, func() {
		h.mu.Lock()
		latest := h.pending[board]
		delete(h.pending, board)
		h.mu.Unlock()
		h.send(latest)
	})
}

func registerRealtimeRouting(bus *events.Bus, b realtime.Broadcaster, hints whiteboardHintRouting) error {
	if hints.libraryDelay == 0 {
		hints.libraryDelay = 3 * time.Second
	}
	library := &whiteboardLibraryHints{
		pending: map[uuid.UUID]events.Event{},
		delay:   hints.libraryDelay,
		send: func(e events.Event) {
			change, err := events.PayloadOf[canvas.BoardChanged](e)
			if err == nil {
				err = toCompanyUsers(context.Background(), b, e, change.RecipientUserIDs)
			}
			if err != nil {
				slog.Warn("Whiteboard library hint failed", "error", err)
			}
		},
	}
	// toCompany is the whole handler for a fact the whole company may see.
	// The envelope already names the company, so there is no routing
	// decision and nothing to read off the payload.
	toCompany := func(_ context.Context, e events.Event) error {
		return b.BroadcastToCompany(e.CompanyID.String(), realtime.NewBroadcastEvent(e))
	}
	// toProject is the whole handler for a fact about a Project or the work
	// inside it: the producer decided who may see it in its transaction
	// (ADR 0017), and the frame goes to exactly them.
	toProject := func(ctx context.Context, e events.Event) error {
		return byProject(ctx, b, e, projectAudienceOf(e))
	}

	return errors.Join(
		// ---- the readers of the Task's Project ----
		bus.Subscribe(task.Topic, toProject),

		// ---- company-wide: every member may see these ----
		bus.Subscribe(agent.TopicConnector, toCompany),

		// ---- the feed row's own audience ----
		bus.Subscribe(activity.Topic, func(ctx context.Context, e events.Event) error {
			entry, err := events.PayloadOf[activity.Entry](e)
			if err != nil {
				return err
			}
			// A row about a Project reaches whoever reads it in the feed.
			if entry.Audience != nil {
				return byProject(ctx, b, e, entry.Audience)
			}
			if entry.VisibilityUserID.Valid {
				return b.BroadcastToCompanyUser(
					e.CompanyID.String(),
					entry.VisibilityUserID.UUID.String(),
					realtime.NewBroadcastEvent(e),
				)
			}
			return b.BroadcastToCompany(e.CompanyID.String(), realtime.NewBroadcastEvent(e))
		}),

		// ---- company-wide: every member may see these ----
		// Agent lifecycle is the Company's; a member's binding and the
		// availability it yields are theirs alone (ADR 0014), so those two
		// frames follow the binder and never name another member's machine
		// to anyone else.
		bus.Subscribe(agent.Topic, func(ctx context.Context, e events.Event) error {
			switch e.Name {
			case agent.EventAgentBindingChanged:
				change, err := events.PayloadOf[agent.BindingChange](e)
				if err != nil {
					return err
				}
				return b.BroadcastToCompanyUser(
					e.CompanyID.String(), change.UserID.String(), realtime.NewBroadcastEvent(e),
				)
			case agent.EventAgentStatusChanged:
				change, err := events.PayloadOf[agent.AvailabilityChange](e)
				if err != nil {
					return err
				}
				return b.BroadcastToCompanyUser(
					e.CompanyID.String(), change.UserID.String(), realtime.NewBroadcastEvent(e),
				)
			default:
				return toCompany(ctx, e)
			}
		}),
		// The roster is the registrant's; the sweeps and the collector name
		// no machine and stay a Company-wide refetch signal.
		bus.Subscribe(agent.TopicDaemon, func(ctx context.Context, e events.Event) error {
			roster, err := events.PayloadOf[agent.DaemonRoster](e)
			if err != nil {
				return err
			}
			if roster.UserID == uuid.Nil {
				return toCompany(ctx, e)
			}
			return b.BroadcastToCompanyUser(
				e.CompanyID.String(), roster.UserID.String(), realtime.NewBroadcastEvent(e),
			)
		}),
		bus.Subscribe(agent.TopicRuntime, func(_ context.Context, e events.Event) error {
			if e.Name != agent.EventRuntimeCapabilitiesChanged {
				return nil
			}
			change, err := events.PayloadOf[agent.CapabilitiesChanged](e)
			if err != nil {
				return err
			}
			return b.BroadcastToCompanyUser(
				e.CompanyID.String(),
				change.UserID.String(),
				realtime.NewBroadcastEvent(e),
			)
		}),
		bus.Subscribe(conversation.TopicHuddle, func(_ context.Context, e events.Event) error {
			if e.Name == conversation.EventHuddleRosterChanged || e.Name == conversation.EventHuddleStatusChanged {
				// Opaque refetch signals for the people in the call, addressed
				// by the producer when the connection or status changed.
				var recipients []uuid.UUID
				if e.Name == conversation.EventHuddleRosterChanged {
					roster, err := events.PayloadOf[conversation.HuddleRosterChangedEvent](e)
					if err != nil {
						return err
					}
					recipients = roster.RecipientUserIDs
				} else {
					status, err := events.PayloadOf[conversation.HuddleStatusChangedEvent](e)
					if err != nil {
						return err
					}
					recipients = status.RecipientUserIDs
				}
				var errs []error
				for _, userID := range recipients {
					errs = append(errs, b.BroadcastToCompanyUser(
						e.CompanyID.String(), userID.String(), realtime.NewBroadcastEvent(e),
					))
				}
				return errors.Join(errs...)
			}
			invitation, err := events.PayloadOf[conversation.HuddleInvitationEvent](e)
			if err != nil {
				return err
			}
			return b.BroadcastToCompanyUser(
				e.CompanyID.String(),
				invitation.RecipientUserID.String(),
				realtime.NewBroadcastEvent(e),
			)
		}),
		bus.Subscribe(company.TopicResource, toCompany),
		// Every active member reads a Company's Groups, as they read the Team
		// page; the frames name a Group and its revision, nothing more.
		bus.Subscribe(company.TopicGroup, toCompany),
		// Project lifecycle frames reach the Project's readers and the
		// projects.manage holders, decided with the commit (ADR 0017).
		bus.Subscribe(company.TopicProject, func(ctx context.Context, e events.Event) error {
			if e.Name != company.EventProjectAccessChanged {
				return toProject(ctx, e)
			}
			// Who may do what on a Project changed (ADR 0017). The frame is
			// opaque and reaches exactly the people the change concerned,
			// decided in its transaction: the readers before and after, the
			// projects.manage holders and the actor. A Project that was or
			// became Company-visible had every member among its readers.
			change, err := events.PayloadOf[company.ProjectAccessChanged](e)
			if err != nil {
				return err
			}
			return byProject(ctx, b, e, &auth.ProjectAudience{
				CompanyWide: change.CompanyWide, UserIDs: change.RecipientUserIDs,
			})
		}),
		bus.Subscribe(canvas.Topic, toProject),
		bus.Subscribe(execution.Topic, func(ctx context.Context, e events.Event) error {
			if hints.runs == nil ||
				(e.Name != execution.EventRunCompleted && e.Name != execution.EventRunFailed &&
					e.Name != execution.EventRunCancelled) {
				return nil
			}
			run, err := events.PayloadOf[execution.RunSnapshot](e)
			if err != nil {
				return err
			}
			return hints.runs.RemoveRun(ctx, run.ID)
		}),
		bus.Subscribe(canvas.TopicBoard, func(ctx context.Context, e events.Event) error {
			change, err := events.PayloadOf[canvas.BoardChanged](e)
			if err != nil {
				return err
			}
			// Who may reach the Board changed: everyone concerned hears now.
			if change.Access || hints.viewers == nil {
				return toCompanyUsers(ctx, b, e, change.RecipientUserIDs)
			}
			sessions, err := hints.viewers.Sessions(ctx, e.CompanyID.String()+":whiteboard:"+change.BoardID.String())
			if err != nil {
				return toCompanyUsers(ctx, b, e, change.RecipientUserIDs)
			}
			open := make(map[string]bool, len(sessions))
			for _, session := range sessions {
				open[session.UserID] = true
			}
			var now, later []uuid.UUID
			for _, user := range change.RecipientUserIDs {
				if open[user.String()] {
					now = append(now, user)
				} else {
					later = append(later, user)
				}
			}
			if len(later) > 0 {
				deferred := e
				libraryChange := change
				libraryChange.RecipientUserIDs = later
				deferred.Payload = libraryChange
				library.add(change.BoardID, deferred)
			}
			return toCompanyUsers(ctx, b, e, now)
		}),
		bus.Subscribe(wiki.TopicWiki, func(ctx context.Context, e events.Event) error {
			if e.Name != wiki.EventRunReadsChanged {
				return toCompany(ctx, e)
			}
			change, err := events.PayloadOf[wiki.RunReadsChanged](e)
			if err != nil {
				return err
			}
			// A Task Run's evidence is work inside a Project: it reaches the
			// Project's readers, decided by the producer (ADR 0017).
			if change.ConversationKind == "task" {
				return toProject(ctx, e)
			}
			if change.ConversationCreatedBy == nil ||
				(change.ConversationKind != "direct" && change.ConversationKind != "schedule") {
				return nil
			}
			return b.BroadcastToCompanyUser(
				e.CompanyID.String(),
				change.ConversationCreatedBy.String(),
				realtime.NewBroadcastEvent(e),
			)
		}),
		bus.Subscribe(schedule.Topic, func(_ context.Context, e events.Event) error {
			change, err := events.PayloadOf[schedule.Changed](e)
			if err != nil {
				return err
			}
			return b.BroadcastToCompanyUser(
				e.CompanyID.String(),
				change.CreatorUserID.String(),
				realtime.NewBroadcastEvent(e),
			)
		}),
		bus.Subscribe(calendar.Topic, func(ctx context.Context, e events.Event) error {
			change, err := events.PayloadOf[calendar.Changed](e)
			if err != nil {
				return err
			}
			e.Payload = struct{}{}
			if change.CompanyWide {
				return toCompany(ctx, e)
			}
			// Personal calendars follow the account through all Company tabs.
			e.CompanyID = uuid.Nil
			return b.BroadcastToUser(change.UserID.String(), realtime.NewBroadcastEvent(e))
		}),
		bus.Subscribe(execution.TopicDeliverable, func(_ context.Context, e events.Event) error {
			change, err := events.PayloadOf[execution.DeliverableEvent](e)
			if err != nil {
				return err
			}
			// A grant can be revoked between commit and delivery. Only send an
			// opaque invalidation; the subsequent read checks current access.
			e.Actor = events.SystemActor()
			e.Payload = struct{}{}
			frame := realtime.NewBroadcastEvent(e)
			delivered := make(map[uuid.UUID]bool, len(change.RecipientUserIDs))
			var errs []error
			for _, recipient := range change.RecipientUserIDs {
				if recipient == uuid.Nil || delivered[recipient] {
					continue
				}
				delivered[recipient] = true
				errs = append(errs, b.BroadcastToCompanyUser(
					e.CompanyID.String(), recipient.String(), frame))
			}
			return errors.Join(errs...)
		}),
		bus.Subscribe(execution.TopicArtifact, func(_ context.Context, e events.Event) error {
			var recipients []uuid.UUID
			switch e.Name {
			case execution.EventArtifactAccessChanged:
				change, err := events.PayloadOf[execution.ArtifactAccessChanged](e)
				if err != nil {
					return err
				}
				recipients = change.RecipientUserIDs
			case execution.EventArtifactChanged:
				change, err := events.PayloadOf[execution.ArtifactChanged](e)
				if err != nil {
					return err
				}
				recipients = change.RecipientUserIDs
			default:
				return nil
			}
			e.Actor = events.SystemActor()
			e.Payload = struct{}{}
			frame := realtime.NewBroadcastEvent(e)
			delivered := make(map[uuid.UUID]bool, len(recipients))
			var errs []error
			for _, recipient := range recipients {
				if recipient == uuid.Nil || delivered[recipient] {
					continue
				}
				delivered[recipient] = true
				errs = append(errs, b.BroadcastToCompanyUser(
					e.CompanyID.String(), recipient.String(), frame))
			}
			return errors.Join(errs...)
		}),

		// ---- one user, wherever they are signed in ----
		//
		// These three follow a person rather than a tenant, so the envelope's
		// company is uuid.Nil and the user has to come off the payload.
		bus.Subscribe(auth.TopicUser, func(_ context.Context, e events.Event) error {
			user, err := events.PayloadOf[auth.UserResponse](e)
			if err != nil {
				return err
			}
			return b.BroadcastToUser(user.ID.String(), realtime.NewBroadcastEvent(e))
		}),
		bus.Subscribe(auth.Topic, func(_ context.Context, e events.Event) error {
			// Session and Company-access facts deliberately share the auth
			// topic. Their names select the payload before the routing fact is
			// read, so one family is never decoded as another.
			switch e.Name {
			case auth.EventSessionRevoked:
				revoked, err := events.PayloadOf[auth.RevokedSession](e)
				if err != nil {
					return err
				}
				return b.BroadcastToUser(revoked.UserID.String(), realtime.NewBroadcastEvent(e))
			case company.EventAuthCompanyAccessRevoked:
				receipt, err := events.PayloadOf[company.AccessRevokedReceipt](e)
				if err != nil {
					return err
				}
				// One fact, two paths: the user's other sessions learn of it,
				// and the Company sockets get it as their goodbye. The shared
				// id keeps a socket in both audiences from seeing it twice.
				frame := realtime.NewBroadcastEvent(e)
				frame.EventID = realtime.NewEventID()
				return errors.Join(
					b.BroadcastToUser(receipt.UserID.String(), frame),
					b.DisconnectCompanyUser(
						receipt.CompanyID.String(),
						receipt.UserID.String(),
						frame,
					),
				)
			case company.EventAuthCompanyAccessChanged:
				receipt, err := events.PayloadOf[company.AccessChangedReceipt](e)
				if err != nil {
					return err
				}
				return b.BroadcastToUser(receipt.UserID.String(), realtime.NewBroadcastEvent(e))
			default:
				return nil
			}
		}),

		// ---- private inbox events follow the account across companies ----
		bus.Subscribe(inbox.Topic, func(_ context.Context, e events.Event) error {
			item, err := events.PayloadOf[inbox.ItemWithCounts](e)
			if err != nil {
				return err
			}
			if item.EntityType == protocol.EntityDeliverable {
				return nil
			}
			return b.BroadcastToUser(
				item.RecipientUserID.String(), realtime.NewBroadcastEvent(e),
			)
		}),
		bus.Subscribe(inbox.TopicNotification, func(_ context.Context, e events.Event) error {
			hint, err := events.PayloadOf[inbox.ToastHint](e)
			if err != nil {
				return err
			}
			if hint.EntityType == protocol.EntityDeliverable {
				return nil
			}
			return b.BroadcastToUser(
				hint.RecipientUserID.String(), realtime.NewBroadcastEvent(e),
			)
		}),

		// ---- Mailbox permission holders who may read the projection ----
		bus.Subscribe(mailbox.Topic, func(ctx context.Context, e events.Event) error {
			switch e.Name {
			case mailbox.EventChanged:
				payload, err := events.PayloadOf[mailbox.ChangedPayload](e)
				if err != nil {
					return err
				}
				return byProject(ctx, b, e, &auth.ProjectAudience{UserIDs: payload.RecipientUserIDs})
			case mailbox.EventDraftChanged:
				payload, err := events.PayloadOf[mailbox.DraftChangedPayload](e)
				if err != nil {
					return err
				}
				if payload.Audience == nil || payload.Audience.CompanyWide {
					return errors.New("missing Mailbox permission audience")
				}
				return byProject(ctx, b, e, payload.Audience)
			default:
				return nil
			}
		}),

		// ---- a company, plus someone not connected to it yet ----
		bus.Subscribe(company.Topic, func(ctx context.Context, e events.Event) error {
			// company.membership_changed is an internal wakeup fact that shares
			// this topic. It belongs to a user rather than a company, so it has
			// no company room to reach even if one wanted it to.
			if e.Name == company.EventCompanyMembershipChanged {
				return nil
			}
			if e.Name != company.EventCompanyCreated {
				return toCompany(ctx, e)
			}
			// The company room is empty at creation — nobody has joined a
			// company that did not exist a moment ago — so the frame also has
			// to reach the founder's own room.
			value, err := events.PayloadOf[company.Response](e)
			if err != nil {
				return err
			}
			return b.BroadcastToCompanyOrUser(
				e.CompanyID.String(), value.FounderUserID.String(), realtime.NewBroadcastEvent(e),
			)
		}),
		bus.Subscribe(company.TopicMember, func(ctx context.Context, e events.Event) error {
			if e.Name != company.EventMemberAdded {
				return toCompany(ctx, e)
			}
			// Like company.created: the new member may not be in the company
			// room yet.
			member, err := events.PayloadOf[company.ActiveMemberResponse](e)
			if err != nil {
				return err
			}
			return b.BroadcastToCompanyOrUser(
				e.CompanyID.String(), member.Actor.ID.String(), realtime.NewBroadcastEvent(e),
			)
		}),

		// ---- the members who may see the invitation surface ----
		// The payload names its recipients: the active members whose role
		// held members.invite when the change committed. Each gets the frame
		// on their sockets in this company; a plain member's socket sees
		// nothing, because the roster they see has no invitations on it.
		bus.Subscribe(company.TopicInvitation, func(_ context.Context, e events.Event) error {
			var recipients []uuid.UUID
			switch e.Name {
			case company.EventInvitationChanged:
				payload, err := events.PayloadOf[company.InvitationChanged](e)
				if err != nil {
					return err
				}
				recipients = payload.RecipientUserIDs
			case company.EventShareLinkChanged:
				payload, err := events.PayloadOf[company.ShareLinkChanged](e)
				if err != nil {
					return err
				}
				recipients = payload.RecipientUserIDs
			default:
				return nil
			}
			frame := realtime.NewBroadcastEvent(e)
			var errs []error
			for _, recipient := range recipients {
				errs = append(
					errs,
					b.BroadcastToCompanyUser(e.CompanyID.String(), recipient.String(), frame),
				)
			}
			return errors.Join(errs...)
		}),

		// ---- whoever may see the conversation it belongs to ----
		bus.Subscribe(execution.Topic, func(ctx context.Context, e events.Event) error {
			if e.Name == execution.EventRunMessageCreated {
				item, err := events.PayloadOf[execution.TranscriptItem](e)
				if err != nil {
					return err
				}
				return byConversation(ctx, b, e, item.ConversationKind, item.ConversationCreatedBy)
			}
			run, err := events.PayloadOf[execution.RunSnapshot](e)
			if err != nil {
				return err
			}
			return byConversation(ctx, b, e, run.ConversationKind, run.ConversationCreatedBy)
		}),
		bus.Subscribe(conversation.Topic, func(ctx context.Context, e events.Event) error {
			switch e.Name {
			case conversation.EventDiscussionChanged,
				conversation.EventDiscussionHuddleChanged,
				conversation.EventDiscussionStateChanged,
				conversation.EventDiscussionAccessRevoked,
				conversation.EventDiscussionResync,
				conversation.EventChannelAgentsChanged:
				return nil
			}
			if e.Name == conversation.EventConversationRead {
				// One member's own watermark, not the conversation's.
				state, err := events.PayloadOf[conversation.UserState](e)
				if err != nil {
					return err
				}
				return b.BroadcastToCompanyUser(
					e.CompanyID.String(), state.UserID.String(), realtime.NewBroadcastEvent(e),
				)
			}
			snapshot, err := events.PayloadOf[conversation.Snapshot](e)
			if err != nil {
				return err
			}
			value := snapshot.Conversation
			return byConversation(ctx, b, e, string(value.Kind), value.CreatedByUserID)
		}),
		bus.Subscribe(conversation.TopicMessage, func(ctx context.Context, e events.Event) error {
			if e.Name == conversation.EventMessageUpdated {
				update, err := events.PayloadOf[conversation.MessageUpdated](e)
				if err != nil {
					return err
				}
				// Channel and human direct messages reach their members
				// through the discussion dispatcher, which reads the current
				// members at delivery.
				if isDiscussionKind(update.ConversationKind) {
					return nil
				}
				return byConversation(ctx, b, e,
					string(update.ConversationKind), update.ConversationCreatedByUserID)
			}
			message, err := events.PayloadOf[conversation.PostedMessage](e)
			if err != nil {
				return err
			}
			return byConversation(ctx, b, e,
				string(message.ConversationKind), message.ConversationCreatedByUserID)
		}),
		bus.Subscribe(attachment.Topic, func(ctx context.Context, e events.Event) error {
			if e.Name == attachment.EventDeleted {
				removal, err := events.PayloadOf[attachment.Removal](e)
				if err != nil {
					return err
				}
				// An attachment on no message is nobody's but its uploader's,
				// which is the shape of a conversation with one member.
				member := removal.ConversationCreatorUserID
				if removal.ConversationKind == "" {
					member = removal.UploadedByUserID
				}
				return byConversation(ctx, b, e, removal.ConversationKind, &member)
			}
			value, err := events.PayloadOf[attachment.Response](e)
			if err != nil {
				return err
			}
			return b.BroadcastToCompanyUser(
				e.CompanyID.String(), value.UploadedByUserID.String(), realtime.NewBroadcastEvent(e),
			)
		}),

		// ---- the scope the row was raised in ----
		bus.Subscribe(approval.Topic, func(ctx context.Context, e events.Event) error {
			value, err := events.PayloadOf[approval.Approval](e)
			if err != nil {
				return err
			}
			return byOrigin(ctx, b, e, value.TaskID, value.OriginUserID)
		}),
		bus.Subscribe(approval.TopicQuestion, func(ctx context.Context, e events.Event) error {
			value, err := events.PayloadOf[approval.Question](e)
			if err != nil {
				return err
			}
			return byOrigin(ctx, b, e, value.TaskID, value.OriginUserID)
		}),
	)
}

// byConversation sends anything a conversation owns. A Task chat's frame
// reaches the readers of its Project (byProject); any other belongs to the
// member who opened it.
//
// Kind is authoritative rather than a task id: a plan apply that deletes a
// task nulls the run's taskId while the conversation survives carrying kind
// 'task' and its Project, so routing on the task would lose the Project at
// exactly that moment.
func byConversation(
	ctx context.Context,
	b realtime.Broadcaster,
	event events.Event,
	kind string,
	member *uuid.UUID,
) error {
	// Channel Agent transcripts are fetched through the live manager/channel
	// access check. Never route them by the execution conversation's creator.
	if kind == "channel_agent" {
		return nil
	}
	if kind == string(conversation.KindTask) {
		return byProject(ctx, b, event, projectAudienceOf(event))
	}
	// A conversation with no member addresses nobody; the broadcaster refuses
	// it rather than widening to the company.
	var recipient string
	if member != nil {
		recipient = member.String()
	}
	return b.BroadcastToCompanyUser(event.CompanyID.String(), recipient, realtime.NewBroadcastEvent(event))
}

// byOrigin sends a raised approval or question to the scope it was raised
// in, which the row froze and nothing later moves: the readers of the Project
// when it was raised in a Task chat or names a Task, else its origin user.
func byOrigin(
	ctx context.Context,
	b realtime.Broadcaster,
	event events.Event,
	taskID *uuid.UUID,
	originUserID uuid.UUID,
) error {
	if audience := projectAudienceOf(event); audience != nil || taskID != nil {
		return byProject(ctx, b, event, audience)
	}
	return b.BroadcastToCompanyUser(
		event.CompanyID.String(), originUserID.String(), realtime.NewBroadcastEvent(event),
	)
}

// projectAudienceOf reads the audience a producer stamped on the payload
// (auth.ProjectFrame), or nil when there is none.
func projectAudienceOf(event events.Event) *auth.ProjectAudience {
	framed, ok := event.Payload.(auth.ProjectAudienceCarrier)
	if !ok {
		return nil
	}
	return framed.ProjectAudience()
}

// byProject sends a frame about a Project, or the work inside it, to the
// audience its producer decided in the transaction that committed it (ADR
// 0017): the whole Company in one frame when the Project is Company-visible,
// otherwise each listed member, in bounded batches. A Project frame that
// arrives without an audience is refused, never widened to the Company, and
// counted in ProjectFramesDroppedTotal like a recipient the relay could not
// reach.
//
// The batch runs on the bus handler's context without its cancellation: the
// fact has committed, and the request that published it closing its
// connection must not take the other readers' frames with it.
func byProject(
	ctx context.Context,
	b realtime.Broadcaster,
	event events.Event,
	audience *auth.ProjectAudience,
) error {
	if audience == nil {
		realtime.M.ProjectFramesDroppedTotal.Add(1)
		return fmt.Errorf("realtime: %s is about a Project but carries no audience", event.Name)
	}
	if audience.CompanyWide {
		return b.BroadcastToCompany(event.CompanyID.String(), realtime.NewBroadcastEvent(event))
	}
	return toCompanyUsers(ctx, b, event, audience.UserIDs)
}

// toCompanyUsers delivers one frame to resolved Company members in bounded
// relay batches instead of one relay publish per person.
func toCompanyUsers(ctx context.Context, b realtime.Broadcaster, event events.Event, userIDs []uuid.UUID) error {
	frame := realtime.NewBroadcastEvent(event)
	companyID := event.CompanyID.String()
	recipients := make([]string, 0, len(userIDs))
	seen := make(map[uuid.UUID]bool, len(userIDs))
	for _, userID := range userIDs {
		if userID == uuid.Nil || seen[userID] {
			continue
		}
		seen[userID] = true
		recipients = append(recipients, userID.String())
	}
	ctx = context.WithoutCancel(ctx)
	var errs []error
	// 50 is the most users one BroadcastToCompanyUsers call takes.
	for batch := range slices.Chunk(recipients, 50) {
		results, err := b.BroadcastToCompanyUsers(ctx, companyID, batch, frame)
		if err != nil {
			realtime.M.ProjectFramesDroppedTotal.Add(int64(len(batch)))
			errs = append(errs, fmt.Errorf("realtime: deliver %s: %w", event.Name, err))
			continue
		}
		for _, result := range results {
			if result.Err != nil {
				realtime.M.ProjectFramesDroppedTotal.Add(1)
			}
		}
	}
	return errors.Join(errs...)
}
