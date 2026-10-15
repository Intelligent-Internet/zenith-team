package conversation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf16"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/inbox"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

func validateDiscussionReferences(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	conversationID uuid.UUID,
	body DiscussionBody,
	attachmentIDs []uuid.UUID,
	previous *DiscussionBody,
) ([]discussionReference, error) {
	references, err := decodeDiscussionReferences(body)
	if err != nil {
		return nil, err
	}
	// Preserve only the same target and displayed label, with multiplicity.
	// New or retargeted mentions still require current membership.
	type mentionKey struct {
		Type  string
		ID    uuid.UUID
		Label string
	}
	keyFor := func(ref discussionReference, text []uint16) mentionKey {
		key := mentionKey{Type: ref.Type, Label: string(utf16.Decode(text[ref.Start:ref.End]))}
		if ref.UserID != nil {
			key.ID = *ref.UserID
		}
		if ref.AgentID != nil {
			key.ID = *ref.AgentID
		}
		return key
	}
	historical := make(map[mentionKey]int)
	if previous != nil {
		oldRefs, err := decodeDiscussionReferences(*previous)
		if err != nil {
			return nil, err
		}
		oldText := utf16.Encode([]rune(previous.Markdown))
		for _, ref := range oldRefs {
			if ref.Type == "user" || ref.Type == "agent" {
				historical[keyFor(ref, oldText)]++
			}
		}
	}
	currentText := utf16.Encode([]rune(body.Markdown))
	allowedUploads := make(map[uuid.UUID]struct{}, len(attachmentIDs))
	for _, id := range attachmentIDs {
		allowedUploads[id] = struct{}{}
	}
	for _, reference := range references {
		if reference.Type == "user" || reference.Type == "agent" {
			key := keyFor(reference, currentText)
			if historical[key] > 0 {
				historical[key]--
				continue
			}
		}
		switch reference.Type {
		case "user":
			member, lookupErr := q.GetDiscussionMember(ctx, generated.GetDiscussionMemberParams{
				ConversationID: conversationID, UserID: *reference.UserID,
			})
			if lookupErr != nil || member.Status != "active" {
				return nil, ErrDiscussionInvalid
			}
			if _, err := q.GetDiscussionEligibleUser(
				ctx,
				generated.GetDiscussionEligibleUserParams{
					CompanyID: p.CompanyID,
					UserID:    *reference.UserID,
				},
			); err != nil {
				return nil, ErrDiscussionInvalid
			}
		case "agent":
			if previous != nil {
				return nil, ErrDiscussionInvalid
			}
			// A tag needs the Agent in the conversation. The refusal says so
			// rather than calling the body malformed.
			if _, lookupErr := q.GetDiscussionAgentHistoryGrant(
				ctx,
				generated.GetDiscussionAgentHistoryGrantParams{
					CompanyID:      p.CompanyID,
					ConversationID: conversationID,
					AgentID:        *reference.AgentID,
				},
			); errors.Is(
				lookupErr,
				pgx.ErrNoRows,
			) {
				return nil, ErrDiscussionAgentRequired
			} else if lookupErr != nil {
				return nil, lookupErr
			}
		case "attachment":
			if _, ok := allowedUploads[*reference.AttachmentID]; ok {
				continue
			}
			if _, lookupErr := q.GetDiscussionAttachmentReference(
				ctx,
				generated.GetDiscussionAttachmentReferenceParams{
					AttachmentID:   *reference.AttachmentID,
					CompanyID:      p.CompanyID,
					ConversationID: conversationID,
				},
			); lookupErr != nil {
				return nil, ErrDiscussionInvalid
			}
		case "connector":
			if previous != nil {
				oldRefs, err := decodeDiscussionReferences(*previous)
				if err != nil {
					return nil, err
				}
				found := false
				for _, old := range oldRefs {
					if old.Type == "connector" && *old.ConnectorID == *reference.ConnectorID &&
						string(
							utf16.Decode(
								utf16.Encode([]rune(previous.Markdown))[old.Start:old.End],
							),
						) == string(
							utf16.Decode(currentText[reference.Start:reference.End]),
						) {
						found = true
						break
					}
				}
				if !found {
					return nil, ErrDiscussionInvalid
				}
			}
		case "skill":
			if previous != nil {
				oldRefs, err := decodeDiscussionReferences(*previous)
				if err != nil {
					return nil, err
				}
				found := false
				for _, old := range oldRefs {
					if old.Type == "skill" && *old.SkillID == *reference.SkillID &&
						*old.VersionID == *reference.VersionID {
						found = true
						break
					}
				}
				if !found {
					return nil, ErrDiscussionInvalid
				}
			}
		case "asset_version":
			// Asset-version references are reserved for the Company resource ACL
			// surface. Rejecting them here avoids leaking a guessed resource ID.
			return nil, ErrDiscussionInvalid
		case "broadcast":
			access, lookupErr := currentDiscussion(ctx, q, p, conversationID, false)
			if lookupErr != nil || access.Kind != "channel" {
				return nil, ErrDiscussionInvalid
			}
		}
	}
	return references, nil
}

// createDiscussionMentionInboxItems freezes the recipient set in the same
// transaction as the message. The existing Inbox dedupe key makes retries
// safe, while the message body, private file names and the channel title
// never enter the notice (C0-CONTRACT §5): it names only the sender.
func createDiscussionMentionInboxItems(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	conversationID, messageID uuid.UUID,
	kind string, rootID *uuid.UUID,
	references []discussionReference,
	pending *[]events.Event,
	directOnly bool,
) error {
	recipients := make(map[uuid.UUID]struct{})
	preferences, err := q.ListDiscussionNotificationPreferences(
		ctx,
		generated.ListDiscussionNotificationPreferencesParams{
			CompanyID:      p.CompanyID,
			ConversationID: conversationID,
		},
	)
	if err != nil {
		return err
	}
	levels := make(map[uuid.UUID]string, len(preferences))
	for _, preference := range preferences {
		levels[preference.UserID] = preference.NotificationLevel
		if !directOnly && kind == "channel" && rootID == nil && preference.NotificationLevel == "all" {
			recipients[preference.UserID] = struct{}{}
		}
	}
	if !directOnly && kind == "human_direct" {
		members, err := q.ListDiscussionMembers(
			ctx,
			generated.ListDiscussionMembersParams{CompanyID: p.CompanyID, ConversationID: conversationID},
		)
		if err != nil {
			return err
		}
		for _, member := range members {
			if member.Active {
				recipients[member.UserID] = struct{}{}
			}
		}
	}
	if !directOnly && rootID != nil {
		followers, err := q.ListDiscussionNotificationFollowers(
			ctx,
			generated.ListDiscussionNotificationFollowersParams{
				CompanyID:      p.CompanyID,
				ConversationID: conversationID,
				RootID:         *rootID,
			},
		)
		if err != nil {
			return err
		}
		for _, userID := range followers {
			recipients[userID] = struct{}{}
		}
	}

	var hereCandidates []uuid.UUID
	var members []generated.ListDiscussionMembersRow
	for _, reference := range references {
		if reference.Type == "user" {
			recipients[*reference.UserID] = struct{}{}
		}
		if !directOnly && reference.Type == "broadcast" {
			// Resolve broadcasts against current membership and shared presence.
			if members == nil {
				var err error
				members, err = q.ListDiscussionMembers(
					ctx,
					generated.ListDiscussionMembersParams{
						CompanyID:      p.CompanyID,
						ConversationID: conversationID,
					},
				)
				if err != nil {
					return err
				}
			}
			if reference.Scope == "here" {
				for _, member := range members {
					if member.Active {
						hereCandidates = append(hereCandidates, member.UserID)
					}
				}
			} else {
				for _, member := range members {
					if member.Active {
						recipients[member.UserID] = struct{}{}
					}
				}
			}
		}
	}
	if len(hereCandidates) > 0 {
		rows, err := q.ListCompanyMemberPresence(
			ctx,
			generated.ListCompanyMemberPresenceParams{CompanyID: p.CompanyID, UserIds: hereCandidates},
		)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Online {
				recipients[row.UserID] = struct{}{}
			}
		}
	}

	delete(recipients, p.UserID)
	ids := make([]uuid.UUID, 0, len(recipients))
	for id := range recipients {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	discussionType := "channel"
	if kind == "human_direct" {
		discussionType = kind
	}
	deepLink := fmt.Sprintf(
		"/app?conversationKind=discussion&discussionType=%s&conversationId=%s&messageId=%s",
		discussionType,
		conversationID,
		messageID,
	)
	if rootID != nil {
		deepLink += "&threadRootId=" + rootID.String()
	}
	if len(ids) == 0 {
		return nil
	}
	// The notice says who wrote, never what or in which channel: the stored
	// row outlives a rename and a lost grant. The deep link opens the place.
	// A member the message names is told they were mentioned; everyone else
	// that it is new.
	sender, err := q.GetRunTriggerAuthor(ctx, generated.GetRunTriggerAuthorParams{
		CompanyID: p.CompanyID, UserID: p.UserID,
	})
	if err != nil {
		return fmt.Errorf("read discussion sender name: %w", err)
	}
	where := "a channel"
	title, body := "New channel message", fmt.Sprintf("%s wrote in a channel.", sender.DisplayName)
	if kind == "human_direct" {
		where = "a direct message"
		title, body = "New direct message", fmt.Sprintf("%s sent you a direct message.", sender.DisplayName)
	} else if rootID != nil {
		where = "a thread"
		title, body = "New thread activity", fmt.Sprintf("%s replied in a thread.", sender.DisplayName)
	}
	mentioned := make(map[uuid.UUID]bool)
	for _, reference := range references {
		if reference.Type == "user" {
			mentioned[*reference.UserID] = true
		}
	}
	for _, recipient := range ids {
		itemTitle, itemBody := title, body
		if mentioned[recipient] {
			itemTitle = "New mention in chat"
			itemBody = fmt.Sprintf("%s mentioned you in %s.", sender.DisplayName, where)
		}
		actorID := p.UserID
		result, err := inbox.CreateInTx(ctx, q, pending, inbox.CreateInput{
			CompanyID: p.CompanyID, RecipientUserID: recipient, ConversationID: &conversationID,
			Type:   inbox.TypeDiscussionMention,
			Entity: inbox.Entity{Type: protocol.EntityMessage, ID: messageID.String()},
			Title:  itemTitle, Body: itemBody,
			DeepLink: &deepLink, DedupeKey: "discussion:" + messageID.String() + ":" + recipient.String(),
			Actor: inbox.Actor{Kind: "user", ID: &actorID},
		})
		if err != nil {
			return err
		}
		if result.Created && levels[recipient] != "muted" {
			*pending = append(
				*pending,
				events.Event{
					EntityType: inbox.TopicNotification,
					Name:       inbox.EventNotificationIssued,
					CompanyID:  p.CompanyID,
					CreatedAt:  time.Now().UTC(),
					Actor:      events.SystemActor(),
					Payload: inbox.ToastHint{
						NotificationID:  result.Item.ID,
						Type:            result.Item.Type,
						Title:           result.Item.Title,
						Body:            result.Item.Body,
						DeepLink:        result.Item.DeepLink,
						CompanyID:       p.CompanyID,
						RecipientUserID: recipient,
					},
				},
			)
		}
	}
	return nil
}
