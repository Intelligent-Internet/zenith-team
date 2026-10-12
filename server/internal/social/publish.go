package social

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/approval"
	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	adapter "boardly.dev/boardly/server/internal/infra/social"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// RunContext is supplied by the verified Run proof, never by model input.
type RunContext struct{ RunID, ConversationID, AgentID uuid.UUID }

type PostInput struct {
	ConnectionID  string   `json:"connectionId"`
	TargetID      string   `json:"targetId"`
	Text          string   `json:"text"`
	AttachmentIDs []string `json:"attachmentIds"`
}

type Post struct {
	ID         string    `json:"id"`
	Platform   string    `json:"platform"`
	TargetName string    `json:"targetName"`
	Text       string    `json:"text"`
	Status     string    `json:"status"`
	ExternalID string    `json:"externalId"`
	URL        string    `json:"url"`
	ErrorCode  string    `json:"errorCode"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Media = approval.QuestionSocialMedia

type ReadResult struct {
	Connections []Connection `json:"connections"`
	Posts       []Post       `json:"posts"`
	Images      []Media      `json:"images"`
	Videos      []Media      `json:"videos"`
}

func lockRun(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, run RunContext) error {
	company, err := q.LockCompanyForMembershipRead(ctx, p.CompanyID)
	if err != nil {
		return socialQueryError(err)
	}
	if company.DeletingAt.Valid {
		return ErrNotFound
	}
	member, err := q.GetCompanyMemberForShare(
		ctx,
		generated.GetCompanyMemberForShareParams{CompanyID: p.CompanyID, UserID: p.UserID},
	)
	if err != nil {
		return socialQueryError(err)
	}
	if member.Status != "active" {
		return ErrNotFound
	}
	if _, err := q.LockSocialConversation(
		ctx,
		generated.LockSocialConversationParams{
			ID:              run.ConversationID,
			CompanyID:       p.CompanyID,
			CreatedByUserID: uuid.NullUUID{UUID: p.UserID, Valid: true},
		},
	); err != nil {
		return socialQueryError(err)
	}
	_, err = q.LockSocialRun(
		ctx,
		generated.LockSocialRunParams{
			ID:             run.RunID,
			CompanyID:      p.CompanyID,
			ConversationID: run.ConversationID,
			AgentID:        run.AgentID,
		},
	)
	return socialQueryError(err)
}

func socialQueryError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s *DefaultSocialService) ReadForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run RunContext,
) (ReadResult, error) {
	result := ReadResult{Posts: []Post{}, Images: []Media{}, Videos: []Media{}}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockRun(ctx, q, p, run); err != nil {
			return err
		}
		var err error
		result.Connections, err = s.list(ctx, q, p.UserID)
		if err != nil {
			return err
		}
		posts, err := q.ListSocialPosts(
			ctx,
			generated.ListSocialPostsParams{UserID: p.UserID, CompanyID: p.CompanyID},
		)
		if err != nil {
			return err
		}
		for _, row := range posts {
			result.Posts = append(result.Posts, postDTO(row))
		}
		media, err := q.ListSocialMedia(
			ctx,
			generated.ListSocialMediaParams{
				CompanyID:      p.CompanyID,
				ActorUserID:    p.UserID,
				ConversationID: run.ConversationID,
				MaxVideoBytes:  adapter.MaxVideoBytes,
			},
		)
		if err != nil {
			return err
		}
		for _, row := range media {
			item := Media{
				ID:          row.ID.String(),
				Filename:    row.Filename,
				ContentType: row.ContentType,
				SizeBytes:   row.SizeBytes,
			}
			if row.ContentType == "video/mp4" {
				result.Videos = append(result.Videos, item)
			} else {
				result.Images = append(result.Images, item)
			}
		}
		return nil
	})
	return result, err
}

func postDTO(row generated.SocialPost) Post {
	status, code := row.Status, row.ErrorCode
	if status == "publishing" && time.Since(row.CreatedAt.Time) > 3*time.Minute {
		status, code = "unknown", "outcome_unknown"
	}
	return Post{
		ID:         row.ID.String(),
		Platform:   row.Platform,
		TargetName: row.TargetName,
		Text:       row.Text,
		Status:     status,
		ExternalID: row.ExternalID,
		URL:        row.Url,
		ErrorCode:  code,
		CreatedAt:  row.CreatedAt.Time,
	}
}

type publication struct {
	connection generated.SocialConnection
	target     generated.SocialTarget
	post       generated.SocialPost
	media      []generated.ListSocialMediaRow
	replayed   bool
}

func (s *DefaultSocialService) resolvePost(
	ctx context.Context, q *generated.Queries, p auth.UserPrincipal, run RunContext, input PostInput,
	operation *publication,
) error {
	connectionID, err := uuid.Parse(input.ConnectionID)
	if err != nil {
		return ErrInvalid
	}
	targetID, err := uuid.Parse(input.TargetID)
	if err != nil || len(input.AttachmentIDs) > 4 || len(input.Text) > 256000 {
		return ErrInvalid
	}
	operation.connection, err = q.LockSocialConnection(
		ctx,
		generated.LockSocialConnectionParams{ID: connectionID, UserID: p.UserID},
	)
	if err != nil {
		return socialQueryError(err)
	}
	if _, err = s.connector(operation.connection.Platform, true); err != nil {
		return err
	}
	operation.target, err = q.GetSocialTarget(
		ctx,
		generated.GetSocialTargetParams{ID: targetID, ConnectionID: connectionID},
	)
	if err != nil {
		return socialQueryError(err)
	}
	available, err := q.ListSocialMedia(
		ctx,
		generated.ListSocialMediaParams{
			CompanyID:      p.CompanyID,
			ActorUserID:    p.UserID,
			ConversationID: run.ConversationID,
			MaxVideoBytes:  adapter.MaxVideoBytes,
		},
	)
	if err != nil {
		return err
	}
	check := adapter.PublishInput{Text: input.Text}
	seen := make(map[string]bool)
	for _, id := range input.AttachmentIDs {
		if seen[id] {
			return ErrInvalid
		}
		seen[id] = true
		found := false
		for _, media := range available {
			if media.ID.String() == id {
				operation.media = append(operation.media, media)
				if media.ContentType == "video/mp4" {
					if check.Video != nil {
						return ErrInvalid
					}
					check.Video = &adapter.Video{
						ContentType: media.ContentType,
						SizeBytes:   media.SizeBytes,
					}
				} else {
					check.Images = append(
						check.Images,
						adapter.Image{ContentType: media.ContentType},
					)
				}
				found = true
				break
			}
		}
		if !found {
			return ErrNotFound
		}
	}
	if err := adapter.ValidatePost(operation.connection.Platform, check); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

func (s *DefaultSocialService) reservePost(
	ctx context.Context,
	p auth.UserPrincipal,
	run RunContext,
	questionID uuid.UUID,
) (publication, error) {
	var operation publication
	if questionID == uuid.Nil {
		return operation, ErrInvalid
	}
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockRun(ctx, q, p, run); err != nil {
			return err
		}
		review, expiresAt, err := approvedPost(ctx, q, p, run, questionID)
		if err != nil {
			return err
		}
		input := PostInput{
			ConnectionID:  review.ConnectionID,
			TargetID:      review.TargetID,
			Text:          review.Text,
			AttachmentIDs: review.AttachmentIDs,
		}
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(encoded)
		previous, err := q.GetSocialPostReceipt(
			ctx,
			generated.GetSocialPostReceiptParams{UserID: p.UserID, RequestKey: questionID},
		)
		if err == nil {
			if !bytes.Equal(previous.PayloadHash, hash[:]) ||
				previous.ConversationID != run.ConversationID {
				return ErrConflict
			}
			operation.post, operation.replayed = previous, true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if !time.Now().Before(expiresAt) {
			return ErrApprovalRequired
		}
		if err := s.resolvePost(ctx, q, p, run, input, &operation); err != nil {
			return err
		}
		if operation.connection.Version != review.ConnectionRevision {
			return ErrApprovalRequired
		}
		connectionID := operation.connection.ID
		operation.connection, err = q.ClaimSocialOperation(
			ctx,
			generated.ClaimSocialOperationParams{ID: connectionID, UserID: p.UserID},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		operation.post, err = q.CreateSocialPost(
			ctx,
			generated.CreateSocialPostParams{
				UserID:         p.UserID,
				CompanyID:      p.CompanyID,
				ConversationID: run.ConversationID,
				RunID:          uuid.NullUUID{UUID: run.RunID, Valid: true},
				ConnectionID:   uuid.NullUUID{UUID: connectionID, Valid: true},
				RequestKey:     questionID,
				PayloadHash:    hash[:],
				Platform:       operation.connection.Platform,
				TargetName:     operation.target.DisplayName,
				Text:           input.Text,
			},
		)
		return err
	})
	return operation, err
}

func (s *DefaultSocialService) PublishForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run RunContext,
	questionID uuid.UUID,
) (Post, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	operation, err := s.reservePost(ctx, p, run, questionID)
	if err != nil {
		return Post{}, err
	}
	if operation.replayed {
		return postDTO(operation.post), nil
	}
	result, publishErr := s.publish(ctx, p, run, operation)
	status, code := "published", ""
	if publishErr != nil {
		status, code = "failed", "provider_rejected"
		if errors.Is(publishErr, ErrReconnect) {
			code = "needs_reconnect"
		}
		if errors.Is(publishErr, ErrNotFound) || errors.Is(publishErr, ErrConflict) {
			code = "authorization_changed"
		}
		if adapter.OutcomeUncertain(publishErr) || errors.Is(publishErr, context.Canceled) ||
			errors.Is(publishErr, context.DeadlineExceeded) {
			status, code = "unknown", "outcome_unknown"
		}
	}
	// Persist even if the caller disconnected after the provider accepted the post.
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	var row generated.SocialPost
	err = s.store.WithinTx(finish, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		row, err = q.FinishSocialPost(
			finish,
			generated.FinishSocialPostParams{
				ID:         operation.post.ID,
				Status:     status,
				ErrorCode:  code,
				ExternalID: result.ExternalID,
				Url:        result.URL,
			},
		)
		if err != nil {
			return err
		}
		if errors.Is(publishErr, ErrReconnect) {
			if err := q.MarkSocialReconnect(
				finish,
				generated.MarkSocialReconnectParams{
					ID:             operation.connection.ID,
					OperationToken: operation.connection.OperationToken,
				},
			); err != nil {
				return err
			}
		}
		return q.ReleaseSocialOperation(
			finish,
			generated.ReleaseSocialOperationParams{
				ID:             operation.connection.ID,
				OperationToken: operation.connection.OperationToken,
			},
		)
	})
	if err != nil {
		return Post{}, err
	}
	return postDTO(row), nil
}

func (s *DefaultSocialService) publish(
	ctx context.Context,
	p auth.UserPrincipal,
	run RunContext,
	operation publication,
) (adapter.PublishResult, error) {
	token, err := s.maintain(ctx, operation.connection)
	if err != nil {
		return adapter.PublishResult{}, err
	}
	client, err := s.connector(operation.connection.Platform, true)
	if err != nil {
		return adapter.PublishResult{}, err
	}
	if !client.CanPublish(token) {
		return adapter.PublishResult{}, ErrReconnect
	}
	if operation.connection.Platform == "facebook" {
		raw, err := s.vault.open(
			operation.target.Credentials,
			p.UserID.String()+":facebook:"+operation.target.ExternalID,
		)
		if err != nil {
			return adapter.PublishResult{}, ErrReconnect
		}
		if err = json.Unmarshal(raw, &token); err != nil {
			return adapter.PublishResult{}, ErrReconnect
		}
	}
	input := adapter.PublishInput{TargetID: operation.target.ExternalID, Text: operation.post.Text}
	for _, media := range operation.media {
		if media.ContentType == "video/mp4" {
			if s.attachments == nil {
				return adapter.PublishResult{}, ErrUnavailable
			}
			download, err := s.attachments.DownloadResolved(
				ctx,
				attachment.ResolvedDownloadInput{ID: media.ID, CompanyID: p.CompanyID},
			)
			if err != nil {
				return adapter.PublishResult{}, err
			}
			defer download.Body.Close()
			input.Video = &adapter.Video{
				Content:     download.Body,
				ContentType: media.ContentType,
				SizeBytes:   media.SizeBytes,
			}
			continue
		}
		image, err := s.prepareImage(ctx, p.CompanyID, media, operation.connection.Platform)
		if err != nil {
			return adapter.PublishResult{}, err
		}
		input.Images = append(input.Images, image)
	}
	prepared, err := client.Prepare(ctx, token, input)
	if err != nil {
		if adapter.RequiresReconnect(err) {
			return adapter.PublishResult{}, ErrReconnect
		}
		return adapter.PublishResult{}, err
	}
	// Recheck the Run and connection lease immediately before crossing the publish
	// boundary. Task/Schedule Runs never inherit a user's social authority.
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockRun(ctx, q, p, run); err != nil {
			return err
		}
		current, err := q.LockSocialConnection(
			ctx,
			generated.LockSocialConnectionParams{ID: operation.connection.ID, UserID: p.UserID},
		)
		if err != nil {
			return socialQueryError(err)
		}
		if current.OperationToken != operation.connection.OperationToken ||
			!current.OperationUntil.Time.After(time.Now()) {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return adapter.PublishResult{}, err
	}
	result, err := prepared.Publish(ctx)
	if adapter.RequiresReconnect(err) {
		return result, ErrReconnect
	}
	return result, err
}

func (s *DefaultSocialService) prepareImage(
	ctx context.Context,
	companyID uuid.UUID,
	image generated.ListSocialMediaRow,
	platform string,
) (adapter.Image, error) {
	if s.attachments == nil {
		return adapter.Image{}, ErrUnavailable
	}
	download, err := s.attachments.DownloadResolved(
		ctx,
		attachment.ResolvedDownloadInput{ID: image.ID, CompanyID: companyID},
	)
	if err != nil {
		return adapter.Image{}, err
	}
	defer download.Body.Close()
	content, err := io.ReadAll(io.LimitReader(download.Body, (5<<20)+1))
	if err != nil {
		return adapter.Image{}, err
	}
	if len(content) > 5<<20 {
		return adapter.Image{}, ErrInvalid
	}
	if platform == "x" || platform == "linkedin" {
		return adapter.Image{Content: content, ContentType: image.ContentType}, nil
	}
	share, err := s.attachments.ShareResolved(
		ctx,
		attachment.ResolvedShareInput{ID: image.ID, CompanyID: companyID},
	)
	if err != nil {
		return adapter.Image{}, err
	}
	if !strings.HasPrefix(share.URL, "https://") {
		return adapter.Image{}, errors.New("social image publishing requires public HTTPS object storage")
	}
	return adapter.Image{Content: content, ContentType: image.ContentType, URL: share.URL}, nil
}
