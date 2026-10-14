package conversation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

type resolvedConversationTarget struct {
	DocumentThreadID *uuid.UUID
	CompanyID        uuid.UUID
	ConversationID   uuid.UUID
	ConversationKind string
	TaskID           *uuid.UUID
	AgentID          *uuid.UUID
	AssigneeUserID   *uuid.UUID
	// ProjectID scopes Task chats and document-agent conversations.
	ProjectID *uuid.UUID
}

func resolveMessageTarget(
	ctx context.Context,
	q *generated.Queries,
	actorID uuid.UUID,
	target MessageTarget,
) (resolvedConversationTarget, error) {
	row, err := q.ResolveMessageTargetForActiveUser(ctx, generated.ResolveMessageTargetForActiveUserParams{
		ActorUserID: actorID, TargetKind: string(target.Kind), TargetID: target.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return resolvedConversationTarget{}, ErrNotFound
	}
	if err != nil {
		return resolvedConversationTarget{}, fmt.Errorf("resolve Message target: %w", err)
	}
	return resolvedConversationTarget{
		DocumentThreadID: util.NullUUIDPtr(row.DocumentThreadID),
		CompanyID:        row.CompanyID,
		ConversationID:   row.ConversationID, ConversationKind: row.ConversationKind,
		TaskID: util.NullUUIDPtr(row.TaskID), AgentID: util.NullUUIDPtr(row.EffectiveAgentID),
		AssigneeUserID: util.NullUUIDPtr(row.AssigneeUserID),
		ProjectID:      util.NullUUIDPtr(row.ProjectID),
	}, nil
}

// messageSendReplay returns the already-inserted Message for an exact send
// replay. ErrNotFound is a flow-control sentinel here: Send branches to the
// insert path on it.
func messageSendReplay(
	ctx context.Context,
	q *generated.Queries,
	actorID, requestKey, conversationID, companyID uuid.UUID,
) (MessageResponse, error) {
	row, err := q.GetRequestedUserMessageForReplay(
		ctx,
		generated.GetRequestedUserMessageForReplayParams{
			ActorUserID: actorID, RequestKey: requestKey,
			ConversationID: conversationID, CompanyID: companyID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return MessageResponse{}, ErrNotFound
	}
	if err != nil {
		return MessageResponse{}, fmt.Errorf("look up Message send replay: %w", err)
	}
	return messageResponseWithActor(ctx, q, row)
}

// insertRequestedUserMessage writes the user's words as an item of the Run
// that will answer them. runID is supplied rather than derived because the
// Run row is inserted first in the same transaction — runs.id is a database-generated UUID
// that needs nothing from the stream — and the item is what carries the edge.
func insertRequestedUserMessage(
	ctx context.Context,
	q *generated.Queries,
	runID uuid.UUID,
	actorID, requestKey uuid.UUID,
	content string,
	conversationID, companyID uuid.UUID,
	projectID *uuid.UUID,
) (MessageResponse, error) {
	sets := auth.ProjectPermissionHeldSets()
	row, err := q.InsertRequestedUserMessageAfterConversationLock(
		ctx,
		generated.InsertRequestedUserMessageAfterConversationLockParams{
			RunID:      runID,
			RequestKey: requestKey, Content: content,
			ActorUserID:    actorID,
			ConversationID: conversationID, CompanyID: companyID,
			// The statement's own write fence for a Task chat (ADR 0017).
			WritePermission:           string(auth.ProjectPermissionTasksWrite),
			CompanyDefaultPermissions: sets.CompanyDefaultPermissions,
			ManagementPermissions:     sets.ManagementPermissions,
			ManagementRoles:           sets.ManagementRoles,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) && projectID != nil {
		// A revoke that landed after the service's check: say so, rather
		// than the generic conflict every other zero-row outcome maps to.
		held, checkErr := auth.HoldsProjectPermission(
			ctx, q, companyID, *projectID, actorID, auth.ProjectPermissionTasksWrite,
		)
		if checkErr != nil {
			return MessageResponse{}, checkErr
		}
		if !held {
			return MessageResponse{}, auth.ErrProjectPermissionRequired
		}
	}
	if err != nil {
		return MessageResponse{}, mapConversationMutationError("insert Conversation Message", err)
	}
	if err := setFirstMessageTitle(ctx, q, row); err != nil {
		return MessageResponse{}, err
	}
	return messageResponseWithActor(ctx, q, row)
}

func insertReservedUserMessage(
	ctx context.Context,
	q *generated.Queries,
	runID uuid.UUID,
	actorID, messageID, requestKey uuid.UUID,
	content string,
	conversationID, companyID uuid.UUID,
) (MessageResponse, error) {
	row, err := q.InsertReservedUserMessageAfterConversationLock(
		ctx,
		generated.InsertReservedUserMessageAfterConversationLockParams{
			MessageID: messageID, RunID: runID,
			RequestKey: requestKey, Content: content,
			ActorUserID:    actorID,
			ConversationID: conversationID, CompanyID: companyID,
		},
	)
	if err != nil {
		return MessageResponse{}, mapConversationMutationError("insert reserved Conversation Message", err)
	}
	if err := setFirstMessageTitle(ctx, q, row); err != nil {
		return MessageResponse{}, err
	}
	return messageResponseWithActor(ctx, q, row)
}

func directTitleFromMessage(content string) string {
	title := []rune(strings.Join(strings.Fields(content), " "))
	if len(title) > 120 {
		title = title[:120]
	}
	return strings.TrimSpace(string(title))
}

func setFirstMessageTitle(ctx context.Context, q *generated.Queries, message generated.ConversationItem) error {
	if err := q.SetDirectConversationFirstMessageTitle(ctx, generated.SetDirectConversationFirstMessageTitleParams{
		CompanyID: message.CompanyID, ConversationID: message.ConversationID,
		MessageID: message.ID, Title: directTitleFromMessage(message.Content),
	}); err != nil {
		return fmt.Errorf("set first-message Conversation title: %w", err)
	}
	return nil
}

// linkMessageAttachment links a fresh send's ready Attachment to the inserted
// Message while the send transaction owns both rows.
func linkMessageAttachment(
	ctx context.Context,
	q *generated.Queries,
	position int32,
	messageID, attachmentID, actorID, companyID uuid.UUID,
) error {
	_, err := q.LinkMessageAttachment(ctx, generated.LinkMessageAttachmentParams{
		AttachmentID: attachmentID,
		CompanyID:    companyID,
		ActorUserID:  actorID,
		Position:     position,
		MessageID:    messageID,
	})
	if err != nil {
		return mapConversationMutationError("link Message Attachment", err)
	}
	return nil
}

// conversationMessageWatermark returns errors unwrapped; its caller applies
// its own wrap text.
func conversationMessageWatermark(
	ctx context.Context,
	q *generated.Queries,
	actorID, conversationID, companyID uuid.UUID,
) (int64, error) {
	return q.GetConversationMessageWatermark(ctx, generated.GetConversationMessageWatermarkParams{
		ActorUserID: actorID, ConversationID: conversationID,
		CompanyID: companyID,
	})
}

// newestMessageViews reads the page a chat panel opens on: the newest `limit`
// messages at or below throughSeq, in display order.
//
// The page is a COUNT of messages, which is the whole reason it reads
// backwards. Under one stream seq numbers every item a Run wrote — reasoning,
// tool calls, results — so no seq range picked in advance holds a known number
// of messages, and the obvious range (throughSeq-limit, throughSeq] is empty
// whenever the newest Run was busy. Taking `limit` rows off the tail of the
// timeline index reads only what the chat renders, so nothing invisible can
// shrink the page or blank it.
//
// throughSeq stays the caller's bound over the WHOLE stream, so the page a
// client asks for again does not move when a Run writes items it never sees.
func newestMessageViews(
	ctx context.Context,
	q *generated.Queries,
	actorID, companyID, conversationID uuid.UUID,
	throughSeq int64,
	limit int,
) ([]MessageView, error) {
	rows, err := q.ListConversationMessagesBackward(
		ctx,
		generated.ListConversationMessagesBackwardParams{
			ActorUserID: actorID, ConversationID: conversationID,
			CompanyID:  companyID,
			ThroughSeq: throughSeq, PageSize: int32(limit),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list newest Conversation Messages: %w", err)
	}
	slices.Reverse(rows)
	items, err := buildMessageViews(ctx, q, actorID, companyID, rows)
	if err != nil {
		return nil, fmt.Errorf("build newest Message page: %w", err)
	}
	return items, nil
}

func listMessageViews(
	ctx context.Context,
	q *generated.Queries,
	actorID, companyID, conversationID uuid.UUID,
	afterSeq, throughSeq int64,
	limit int,
) ([]MessageView, error) {
	// One row past the page is read so a full page is recognized as full and
	// trimmed back to limit; nothing downstream needs to be told there is more.
	rows, err := q.ListConversationMessages(ctx, generated.ListConversationMessagesParams{
		ActorUserID: actorID, ConversationID: conversationID,
		CompanyID: companyID,
		AfterSeq:  afterSeq, ThroughSeq: throughSeq, PageSize: int32(limit + 1),
	})
	if err != nil {
		return nil, fmt.Errorf("list Conversation Messages: %w", err)
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	items, err := buildMessageViews(ctx, q, actorID, companyID, rows)
	if err != nil {
		return nil, fmt.Errorf("build Message page: %w", err)
	}
	return items, nil
}

// messagePageSkeleton is the pure assembly seam for a Message history page:
// decoding, duplicate detection, and bulk-row application happen without
// queries, so the fixed-bulk-call property of listMessageViews stays
// structural rather than asserted.
type messagePageSkeleton struct {
	items          []MessageView
	messageIndexes map[uuid.UUID]int
	messageIDs     []uuid.UUID
}

func newMessagePageSkeleton(rows []generated.ConversationItem) *messagePageSkeleton {
	skeleton := &messagePageSkeleton{
		items:          make([]MessageView, len(rows)),
		messageIndexes: make(map[uuid.UUID]int, len(rows)),
		messageIDs:     make([]uuid.UUID, len(rows)),
	}
	for index, row := range rows {
		message := messageFromRow(row)
		skeleton.messageIndexes[message.ID] = index
		skeleton.messageIDs[index] = row.ID
		skeleton.items[index] = MessageView{Message: message, Attachments: []attachment.Response{}}
	}
	return skeleton
}

func (s *messagePageSkeleton) applyAttachments(rows []generated.ListMessageAttachmentsBulkRow) {
	for _, row := range rows {
		index := s.messageIndexes[row.MessageID]
		value := attachmentFromBulkRow(row)
		s.items[index].Attachments = append(s.items[index].Attachments, value)
	}
}

func (s *messagePageSkeleton) applyActorRefs(rows []generated.ListMessageActorRefsBulkRow) {
	for _, row := range rows {
		index := s.messageIndexes[row.MessageID]
		s.items[index].Message.Author = actorRef(
			row.AuthorKind,
			row.AuthorID.UUID,
			row.DisplayName,
			row.AvatarUrl,
		)
	}
}

func buildMessageViews(
	ctx context.Context,
	q *generated.Queries,
	actorID, companyID uuid.UUID,
	rows []generated.ConversationItem,
) ([]MessageView, error) {
	skeleton := newMessagePageSkeleton(rows)
	if len(skeleton.messageIDs) == 0 {
		return skeleton.items, nil
	}
	authors, err := q.ListMessageActorRefsBulk(ctx, generated.ListMessageActorRefsBulkParams{
		MessageIds: skeleton.messageIDs,
		CompanyID:  companyID,
	})
	if err != nil {
		return nil, fmt.Errorf("list Message authors in bulk: %w", err)
	}
	skeleton.applyActorRefs(authors)
	skillRows, err := q.ListMessageSkillReferencesBulk(ctx, generated.ListMessageSkillReferencesBulkParams{CompanyID: companyID, MessageIds: skeleton.messageIDs})
	if err != nil {
		return nil, err
	}
	for _, ref := range skillRows {
		index := skeleton.messageIndexes[ref.MessageID]
		skeleton.items[index].Message.SkillReferences = append(skeleton.items[index].Message.SkillReferences, SkillReference{Start: int(ref.StartOffset), End: int(ref.EndOffset), SkillID: ref.SkillID, VersionID: ref.VersionID})
	}
	attachments, err := q.ListMessageAttachmentsBulk(ctx, generated.ListMessageAttachmentsBulkParams{
		ActorUserID: actorID, MessageIds: skeleton.messageIDs, CompanyID: companyID,
	})
	if err != nil {
		return nil, fmt.Errorf("list Message Attachments in bulk: %w", err)
	}
	skeleton.applyAttachments(attachments)
	withheld, err := q.ListMessagesWithWithheldAttachments(ctx, generated.ListMessagesWithWithheldAttachmentsParams{
		ActorUserID: actorID, MessageIds: skeleton.messageIDs, CompanyID: companyID,
	})
	if err != nil {
		return nil, fmt.Errorf("list Messages with withheld Attachments: %w", err)
	}
	for _, id := range withheld {
		skeleton.items[skeleton.messageIndexes[id]].AttachmentsWithheld = true
	}
	return skeleton.items, nil
}

// messageViewWithAttachments projects one already-mapped Message with the ready
// Attachment DTOs the acting user may open, read in one statement.
// It also notes whether any file on the message is an active Project file, so
// the message.created frame built from it carries none (ADR 0017).
func messageViewWithAttachments(
	ctx context.Context,
	q *generated.Queries,
	actorID, companyID uuid.UUID,
	message MessageResponse,
) (MessageView, error) {
	rows, err := q.ListMessageAttachments(ctx, generated.ListMessageAttachmentsParams{
		MessageID: message.ID, ActorUserID: actorID,
		CompanyID: companyID,
	})
	if err != nil {
		return MessageView{}, fmt.Errorf("list Message Attachments: %w", err)
	}
	attachments := make([]attachment.Response, len(rows))
	for index, row := range rows {
		attachments[index] = attachmentFromRow(row)
	}
	carriesProjectFile, err := q.MessageCarriesProjectFile(ctx, generated.MessageCarriesProjectFileParams{
		CompanyID: companyID, MessageID: message.ID,
	})
	if err != nil {
		return MessageView{}, fmt.Errorf("check Message for Project files: %w", err)
	}
	withheld := false
	if carriesProjectFile {
		ids, err := q.ListMessagesWithWithheldAttachments(ctx, generated.ListMessagesWithWithheldAttachmentsParams{
			ActorUserID: actorID, MessageIds: []uuid.UUID{message.ID}, CompanyID: companyID,
		})
		if err != nil {
			return MessageView{}, fmt.Errorf("list withheld Message Attachments: %w", err)
		}
		withheld = len(ids) > 0
	}
	return MessageView{
		Message: message, Attachments: attachments, AttachmentsWithheld: withheld,
		carriesProjectFile: carriesProjectFile,
	}, nil
}

// messageFromRow projects one chat-surface item. The `type` column travels
// through untranslated: it is the fact the schema stores and the fact the
// reader renders, and inventing a second vocabulary for it here is what let
// the old `kind` column disagree with what a row actually was.
func messageFromRow(row generated.ConversationItem) MessageResponse {
	return MessageResponse{
		ID: row.ID, ConversationID: row.ConversationID, Seq: row.Seq,
		Type:   ItemType(row.Type),
		Author: messageAuthorFromRow(row.AuthorKind, row.AuthorID.UUID),
		// Legacy type allowlists guarantee a Run for both authors: the
		// Run this item belongs to. No branch on author kind survives.
		RunID:     row.RunID.UUID,
		Content:   row.Content,
		CreatedAt: row.CreatedAt.Time.UTC(),
	}
}

func messageResponseWithActor(
	ctx context.Context,
	q *generated.Queries,
	row generated.ConversationItem,
) (MessageResponse, error) {
	author, err := q.GetMessageActorRef(ctx, generated.GetMessageActorRefParams{
		MessageID: row.ID,
		CompanyID: row.CompanyID,
	})
	if err != nil {
		return MessageResponse{}, fmt.Errorf("get Message author: %w", err)
	}
	message := messageFromRow(row)
	refs, err := q.ListMessageSkillReferencesBulk(ctx, generated.ListMessageSkillReferencesBulkParams{CompanyID: row.CompanyID, MessageIds: []uuid.UUID{row.ID}})
	if err != nil {
		return MessageResponse{}, err
	}
	for _, ref := range refs {
		message.SkillReferences = append(message.SkillReferences, SkillReference{Start: int(ref.StartOffset), End: int(ref.EndOffset), SkillID: ref.SkillID, VersionID: ref.VersionID})
	}
	message.Author = actorRef(
		author.AuthorKind,
		author.AuthorID.UUID,
		author.DisplayName,
		author.AvatarUrl,
	)
	return message, nil
}

// MessageViewFromRow projects a trusted item known to have no Attachments,
// as with the execution prompt inserted by the Task domain, and resolves its
// public author projection in the same transaction.
func MessageViewFromRow(
	ctx context.Context,
	q *generated.Queries,
	row generated.ConversationItem,
) (MessageView, error) {
	message, err := messageResponseWithActor(ctx, q, row)
	if err != nil {
		return MessageView{}, err
	}
	return MessageView{Message: message, Attachments: []attachment.Response{}}, nil
}

// MessageViewWithAttachmentsFromRow projects a trusted item another domain
// inserted and linked files to in its own transaction, as with the files a
// Task is created with, carrying those files as actorID may read them.
func MessageViewWithAttachmentsFromRow(
	ctx context.Context,
	q *generated.Queries,
	actorID, companyID uuid.UUID,
	row generated.ConversationItem,
) (MessageView, error) {
	message, err := messageResponseWithActor(ctx, q, row)
	if err != nil {
		return MessageView{}, err
	}
	return messageViewWithAttachments(ctx, q, actorID, companyID, message)
}

// author_kind and author_id are both NOT NULL on an item, so neither of these
// has a missing-author case to answer for any more.
func messageAuthorFromRow(authorKind string, authorID uuid.UUID) ActorRef {
	if authorKind == "user" {
		return ActorRef{Kind: ActorUser, ID: authorID}
	}
	return ActorRef{Kind: ActorAgent, ID: authorID}
}

func actorRef(
	authorKind string,
	authorID uuid.UUID,
	displayName string,
	avatarURL pgtype.Text,
) ActorRef {
	return ActorRef{
		Kind:        ActorKind(authorKind),
		ID:          authorID,
		DisplayName: displayName,
		AvatarURL:   util.TextToPtr(avatarURL),
	}
}

func attachmentFromRow(row generated.Attachment) attachment.Response {
	return attachment.Response{
		ID: row.ID, CompanyID: row.CompanyID,
		UploadedByUserID: row.UploadedByUserID,
		Filename:         row.Filename, ContentType: row.ContentType, SizeBytes: row.SizeBytes,
		Status:    attachment.PublicStatusReady,
		CreatedAt: row.CreatedAt.Time.UTC(),
		UpdatedAt: row.UpdatedAt.Time.UTC(), Revision: row.Version,
	}
}

func attachmentFromBulkRow(row generated.ListMessageAttachmentsBulkRow) attachment.Response {
	return attachmentFromRow(generated.Attachment{
		ID: row.ID, CompanyID: row.CompanyID, UploadedByUserID: row.UploadedByUserID,
		ObjectKey: row.ObjectKey, Filename: row.Filename, ContentType: row.ContentType,
		SizeBytes: row.SizeBytes, Status: row.Status, UploadLeaseExpiresAt: row.UploadLeaseExpiresAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Version: row.Version,
	})
}
