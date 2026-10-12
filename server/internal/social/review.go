package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// PreviewForRun resolves an immutable review snapshot without contacting the
// provider or reserving a publication. Media access is checked again on publish.
func (s *DefaultSocialService) PreviewForRun(
	ctx context.Context, p auth.UserPrincipal, run RunContext, input PostInput,
) (approval.QuestionSocialPost, error) {
	var preview approval.QuestionSocialPost
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockRun(ctx, q, p, run); err != nil {
			return err
		}
		var operation publication
		if err := s.resolvePost(ctx, q, p, run, input, &operation); err != nil {
			return err
		}
		if operation.connection.ReconnectRequired {
			return ErrReconnect
		}
		media := make([]approval.QuestionSocialMedia, 0, len(operation.media))
		ids := make([]string, 0, len(operation.media))
		for _, item := range operation.media {
			ids = append(ids, item.ID.String())
			media = append(media, approval.QuestionSocialMedia{
				ID:          item.ID.String(),
				Filename:    item.Filename,
				ContentType: item.ContentType,
				SizeBytes:   item.SizeBytes,
			})
		}
		preview = approval.QuestionSocialPost{
			ConnectionID: operation.connection.ID.String(), TargetID: operation.target.ID.String(),
			Text: input.Text, AttachmentIDs: ids, ConnectionRevision: operation.connection.Version,
			Platform: operation.connection.Platform, TargetName: operation.target.DisplayName,
			Username: operation.target.Username, AvatarURL: operation.connection.AvatarUrl, Media: media,
		}
		return nil
	})
	return preview, err
}

// approvedPost binds the human answer to this member, Agent and private chat.
// The Question ID is also the publication key, across Runs and HTTP retries.
func approvedPost(
	ctx context.Context, q *generated.Queries, p auth.UserPrincipal, run RunContext, questionID uuid.UUID,
) (approval.QuestionSocialPost, time.Time, error) {
	projection, err := q.GetQuestionProjection(ctx, generated.GetQuestionProjectionParams{
		CompanyID: p.CompanyID, QuestionID: questionID, ActorUserID: p.UserID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return approval.QuestionSocialPost{}, time.Time{}, ErrApprovalRequired
	}
	if err != nil {
		return approval.QuestionSocialPost{}, time.Time{}, err
	}
	row := projection.Question
	if row.Status != "answered" || row.OriginUserID.UUID != p.UserID ||
		row.ResolvedByUserID.UUID != p.UserID || row.ConversationID != run.ConversationID ||
		row.AskedByAgentID != run.AgentID {
		return approval.QuestionSocialPost{}, time.Time{}, ErrApprovalRequired
	}
	var prompts []approval.QuestionPrompt
	var answers []approval.QuestionAnswer
	if err := json.Unmarshal(row.Prompts, &prompts); err != nil {
		return approval.QuestionSocialPost{}, time.Time{}, fmt.Errorf("decode social review: %w", err)
	}
	if err := json.Unmarshal(row.Answers, &answers); err != nil {
		return approval.QuestionSocialPost{}, time.Time{}, fmt.Errorf("decode social decision: %w", err)
	}
	if len(prompts) != 1 || prompts[0].SocialPost == nil || len(answers) != 1 ||
		answers[0].PromptID != prompts[0].ID || answers[0].Text != nil ||
		len(answers[0].OptionIDs) != 1 || answers[0].OptionIDs[0] != "publish" {
		return approval.QuestionSocialPost{}, time.Time{}, ErrApprovalRequired
	}
	return *prompts[0].SocialPost, row.ExpiresAt.Time, nil
}
