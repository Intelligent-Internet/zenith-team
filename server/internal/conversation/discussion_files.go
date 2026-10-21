package conversation

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// The caller has authorized the current Conversation membership in its read
// transaction. Artifact references additionally obey their current grants.
func loadDiscussionFiles(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	messages []*DiscussionMessage,
) error {
	if len(messages) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, 0, len(messages))
	byID := make(map[uuid.UUID]*DiscussionMessage, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
		byID[message.ID] = message
	}
	attachments, err := q.ListDiscussionAttachments(
		ctx,
		generated.ListDiscussionAttachmentsParams{
			CompanyID:   p.CompanyID,
			MessageIds:  ids,
			ActorUserID: p.UserID,
		},
	)
	if err != nil {
		return err
	}
	for _, row := range attachments {
		message := byID[row.MessageID]
		message.Attachments = append(message.Attachments, attachmentFromRow(row.Attachment))
	}
	withheld, err := q.ListMessagesWithWithheldAttachments(
		ctx,
		generated.ListMessagesWithWithheldAttachmentsParams{
			CompanyID:   p.CompanyID,
			MessageIds:  ids,
			ActorUserID: p.UserID,
		},
	)
	if err != nil {
		return err
	}
	for _, id := range withheld {
		byID[id].AttachmentsWithheld = true
	}
	artifacts, err := q.ListDiscussionArtifacts(
		ctx,
		generated.ListDiscussionArtifactsParams{
			CompanyID:   p.CompanyID,
			MessageIds:  ids,
			ActorUserID: p.UserID,
		},
	)
	if err != nil {
		return err
	}
	for _, row := range artifacts {
		message := byID[row.MessageID]
		var files []DiscussionArtifactFile
		if err := json.Unmarshal(row.Files, &files); err != nil {
			return fmt.Errorf("decode discussion artifact files: %w", err)
		}
		message.Artifacts = append(message.Artifacts, DiscussionArtifact{
			ID: row.ID, Title: row.Title, Type: row.Type, Files: files,
			FileCount: row.FileCount, FileName: row.FileName, ContentType: row.ContentType,
		})
	}
	return nil
}
