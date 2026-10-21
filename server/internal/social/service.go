// Package social owns user-authorized social connections, credential maintenance,
// OAuth transactions and immediate publication from live direct Runs.
// These cloud credentials are independent of daemon-local Agent credentials.
package social

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/attachment"
	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/config"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	adapter "github.com/Intelligent-Internet/zenith-team/server/internal/infra/social"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

var (
	ErrUnavailable      = errors.New("social connection is not available")
	ErrNotFound         = errors.New("social connection not found")
	ErrInvalidFlow      = errors.New("social authorization expired or does not match this browser")
	ErrProvider         = errors.New("social authorization could not be completed")
	ErrReconnect        = errors.New("social account needs reconnect")
	ErrInvalid          = errors.New("invalid social post")
	ErrConflict         = errors.New("social connection changed")
	ErrApprovalRequired = errors.New("social post needs human confirmation")
)

type Platform struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	Available            bool   `json:"available"`
	ComingSoon           bool   `json:"comingSoon"`
	SupportsRemoteRevoke bool   `json:"supportsRemoteRevoke"`
}
type Target struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
	Type     string `json:"type"`
}
type Connection struct {
	ID          string     `json:"id"`
	Platform    string     `json:"platform"`
	DisplayName string     `json:"displayName"`
	Username    string     `json:"username"`
	AvatarURL   string     `json:"avatarUrl"`
	ConnectedAt time.Time  `json:"connectedAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Status      string     `json:"status"`
	CanPublish  bool       `json:"canPublish"`
	Revision    int64      `json:"revision"`
	Targets     []Target   `json:"targets"`
}
type (
	StartResult   struct{ AuthorizationURL, Binding string }
	CompleteInput struct{ Platform, State, Binding, Code, ProviderError string }
)

type SocialService interface {
	Platforms() []Platform
	MaintainCredentials(context.Context) error
	ReadForRun(context.Context, auth.UserPrincipal, RunContext) (ReadResult, error)
	PreviewForRun(context.Context, auth.UserPrincipal, RunContext, PostInput) (approval.QuestionSocialPost, error)
	PublishForRun(context.Context, auth.UserPrincipal, RunContext, uuid.UUID) (Post, error)
	List(context.Context, auth.UserPrincipal) ([]Connection, error)
	Start(context.Context, auth.UserPrincipal, string) (StartResult, error)
	StartDesktop(context.Context, auth.UserPrincipal, string, DesktopInput) (StartResult, error)
	LaunchDesktop(context.Context, string, string, string) (StartResult, error)
	Complete(context.Context, CompleteInput) (string, error)
	Disconnect(context.Context, auth.UserPrincipal, string, int64) error
}

var _ SocialService = (*DefaultSocialService)(nil)

type DefaultSocialService struct {
	store        *db.Store
	config       config.SocialConfig
	vault        *vault
	providerHTTP *http.Client
	attachments  attachment.AttachmentService
}

func NewSocialService(
	store *db.Store,
	cfg config.SocialConfig,
	attachments attachment.AttachmentService,
) (*DefaultSocialService, error) {
	s := &DefaultSocialService{store: store, config: cfg, attachments: attachments}
	if cfg.TokenEncryptionKey.Reveal() != "" {
		key, err := base64.StdEncoding.DecodeString(cfg.TokenEncryptionKey.Reveal())
		if err != nil {
			return nil, errors.New("invalid social encryption key")
		}
		s.vault, err = newVault(key)
		if err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *DefaultSocialService) Platforms() []Platform {
	result := []Platform{}
	for _, item := range []struct{ id, name string }{{"x", "X"}, {"instagram", "Instagram"}, {"facebook", "Facebook"}, {"linkedin", "LinkedIn"}, {"tiktok", "TikTok"}} {
		cfg := s.config.Platforms[item.id]
		result = append(
			result,
			Platform{
				ID:                   item.id,
				Name:                 item.name,
				ComingSoon:           !cfg.Enabled,
				Available:            cfg.Enabled && cfg.ClientID != "" && s.vault != nil,
				SupportsRemoteRevoke: item.id != "linkedin" && item.id != "tiktok",
			},
		)
	}
	return result
}

func (s *DefaultSocialService) connector(platform string, requireEnabled bool) (*adapter.Client, error) {
	cfg, exists := s.config.Platforms[platform]
	if !exists || (requireEnabled && !cfg.Enabled) || cfg.ClientID == "" || s.vault == nil {
		return nil, ErrUnavailable
	}
	return adapter.NewClient(
		platform,
		adapter.Credentials{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret.Reveal(),
			GraphVersion: s.config.GraphVersion,
		},
		s.providerHTTP,
	), nil
}

func (s *DefaultSocialService) List(ctx context.Context, principal auth.UserPrincipal) ([]Connection, error) {
	return s.list(ctx, s.store.Queries(), principal.UserID)
}

func (s *DefaultSocialService) list(ctx context.Context, q *generated.Queries, userID uuid.UUID) ([]Connection, error) {
	rows, err := q.ListSocialConnections(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list social connections: %w", err)
	}
	targets, err := q.ListSocialTargets(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list social targets: %w", err)
	}
	result := make([]Connection, 0, len(rows))
	for _, row := range rows {
		item := Connection{
			ID:          row.ID.String(),
			Platform:    row.Platform,
			DisplayName: row.DisplayName,
			Username:    row.Username,
			AvatarURL:   safeImageURL(row.AvatarUrl),
			ConnectedAt: row.ConnectedAt.Time,
			Revision:    row.Version,
			Status:      "connected",
			Targets:     []Target{},
		}
		if row.ExpiresAt.Valid {
			item.ExpiresAt = &row.ExpiresAt.Time
		}
		if token, err := s.token(row); err == nil {
			if client, err := s.connector(row.Platform, true); err == nil {
				item.CanPublish = client.CanPublish(token) && !row.ReconnectRequired &&
					(token.ExpiresAt.IsZero() || token.ExpiresAt.After(time.Now()) || client.CanRefresh(token))
			}
		}
		if !item.CanPublish {
			item.Status = "needs_reconnect"
		}
		for _, target := range targets {
			if target.ConnectionID == row.ID {
				item.Targets = append(
					item.Targets,
					Target{
						ID:       target.ID.String(),
						Name:     target.DisplayName,
						Username: target.Username,
						Type:     target.Type,
					},
				)
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *DefaultSocialService) Start(
	ctx context.Context,
	principal auth.UserPrincipal,
	platform string,
) (StartResult, error) {
	return s.start(ctx, principal, platform, DesktopInput{})
}

func (s *DefaultSocialService) start(
	ctx context.Context,
	principal auth.UserPrincipal,
	platform string,
	desktop DesktopInput,
) (StartResult, error) {
	client, err := s.connector(platform, true)
	if err != nil {
		return StartResult{}, err
	}
	state, err := randomSecret()
	if err != nil {
		return StartResult{}, err
	}
	binding, err := randomSecret()
	if err != nil {
		return StartResult{}, err
	}
	verifier, err := randomSecret()
	if err != nil {
		return StartResult{}, err
	}
	encrypted, err := s.vault.seal([]byte(verifier), principal.UserID.String()+":"+platform)
	if err != nil {
		return StartResult{}, err
	}
	stateHash, bindingHash, challenge := sha256.Sum256(
		[]byte(state),
	), sha256.Sum256(
		[]byte(binding),
	), sha256.Sum256(
		[]byte(verifier),
	)
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockEnabledUserForMembershipMutation(ctx, principal.UserID); err != nil {
			return socialQueryError(err)
		}
		if err := q.DeleteExpiredSocialOAuthFlows(ctx); err != nil {
			return err
		}
		if err := q.InsertSocialOAuthFlow(
			ctx,
			generated.InsertSocialOAuthFlowParams{
				UserID:      principal.UserID,
				SessionID:   principal.SessionID,
				Platform:    platform,
				StateHash:   stateHash[:],
				BindingHash: bindingHash[:],
				Verifier:    encrypted,
			},
		); err != nil {
			return err
		}
		return q.SetSocialDesktopFlow(
			ctx,
			generated.SetSocialDesktopFlowParams{
				StateHash:          stateHash[:],
				DesktopRedirectUri: desktop.RedirectURI,
				DesktopState:       desktop.State,
			},
		)
	})
	if err != nil {
		return StartResult{}, fmt.Errorf("start social authorization: %w", err)
	}
	address := client.AuthorizationURL(
		state,
		base64.RawURLEncoding.EncodeToString(challenge[:]),
		s.callback(platform),
	)
	if desktop.RedirectURI != "" {
		address = s.config.CallbackOrigin + "/api/v1/social/oauth/" + platform + "/launch?" + url.Values{
			"state":  {state},
			"ticket": {binding},
		}.Encode()
	}
	return StartResult{AuthorizationURL: address, Binding: binding}, nil
}

func (s *DefaultSocialService) Complete(ctx context.Context, input CompleteInput) (string, error) {
	client, err := s.connector(input.Platform, true)
	if err != nil {
		return "", err
	}
	if len(input.State) != 43 || len(input.Binding) != 43 || len(input.Code) > 4096 {
		return "", ErrInvalidFlow
	}
	stateHash, bindingHash := sha256.Sum256([]byte(input.State)), sha256.Sum256([]byte(input.Binding))
	var flow generated.SocialOauthFlow
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var err error
		flow, err = q.ConsumeSocialOAuthFlow(
			ctx,
			generated.ConsumeSocialOAuthFlowParams{
				StateHash:   stateHash[:],
				BindingHash: bindingHash[:],
				Platform:    input.Platform,
			},
		)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrInvalidFlow
	}
	if err != nil {
		return "", fmt.Errorf("consume social authorization: %w", err)
	}
	returnURI := desktopReturn(flow.DesktopRedirectUri, flow.DesktopState)
	if input.ProviderError != "" || input.Code == "" {
		return returnURI, ErrProvider
	}
	owner := flow.UserID.String() + ":" + input.Platform
	verifier, err := s.vault.open(flow.Verifier, owner)
	if err != nil {
		return "", ErrInvalidFlow
	}
	account, err := client.Exchange(ctx, input.Code, string(verifier), s.callback(input.Platform))
	if err != nil {
		return returnURI, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	return returnURI, s.saveAccount(ctx, flow.UserID, flow.SessionID, input.Platform, account)
}

func (s *DefaultSocialService) saveAccount(
	ctx context.Context,
	userID, sessionID uuid.UUID,
	platform string,
	account adapter.Account,
) error {
	owner := userID.String() + ":" + platform
	encoded, err := json.Marshal(account.Token)
	if err != nil {
		return err
	}
	encrypted, err := s.vault.seal(encoded, owner)
	if err != nil {
		return err
	}
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockEnabledUserForMembershipMutation(ctx, userID); err != nil {
			return socialQueryError(err)
		}
		if _, err := q.LockSocialAuthorizationSession(
			ctx,
			generated.LockSocialAuthorizationSessionParams{SessionID: sessionID, UserID: userID},
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidFlow
			}
			return err
		}
		row, err := q.UpsertSocialConnection(
			ctx,
			generated.UpsertSocialConnectionParams{
				UserID:      userID,
				Platform:    platform,
				ExternalID:  account.ExternalID,
				DisplayName: account.DisplayName,
				Username:    account.Username,
				AvatarUrl:   safeImageURL(account.AvatarURL),
				Credentials: encrypted,
				ExpiresAt: pgtype.Timestamptz{
					Time:  account.Token.ExpiresAt,
					Valid: !account.Token.ExpiresAt.IsZero(),
				},
			},
		)
		if err != nil {
			return err
		}
		if err = q.DeleteSocialTargets(ctx, row.ID); err != nil {
			return err
		}
		for _, target := range account.Targets {
			encoded, err := json.Marshal(target.Token)
			if err != nil {
				return err
			}
			token, err := s.vault.seal(encoded, owner+":"+target.ExternalID)
			if err != nil {
				return err
			}
			if err = q.InsertSocialTarget(
				ctx,
				generated.InsertSocialTargetParams{
					ConnectionID: row.ID,
					ExternalID:   target.ExternalID,
					DisplayName:  target.DisplayName,
					Username:     target.Username,
					Type:         target.Type,
					Credentials:  token,
				},
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *DefaultSocialService) Disconnect(
	ctx context.Context,
	principal auth.UserPrincipal,
	id string,
	revision int64,
) error {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return ErrNotFound
	}
	row, err := s.store.Queries().
		GetSocialConnection(ctx, generated.GetSocialConnectionParams{UserID: principal.UserID, ID: parsed})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if row.Version != revision {
		return ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	row, err = s.claim(ctx, principal.UserID, parsed)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		_ = s.release(cleanup, row)
	}()
	if row.Version != revision {
		return ErrConflict
	}
	client, err := s.connector(row.Platform, false)
	if err != nil {
		return err
	}
	token, err := s.token(row)
	if err != nil {
		return ErrProvider
	}
	if err := client.Revoke(ctx, token); err != nil {
		return fmt.Errorf("%w: %w", ErrProvider, err)
	}
	// A completed remote revocation must survive the caller closing the request.
	finish, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	return s.store.WithinTx(finish, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockEnabledUserForMembershipMutation(finish, principal.UserID); err != nil {
			return socialQueryError(err)
		}
		count, err := q.DeleteSocialConnection(
			finish,
			generated.DeleteSocialConnectionParams{
				ID:             parsed,
				UserID:         principal.UserID,
				Version:        revision,
				OperationToken: row.OperationToken,
			},
		)
		if err != nil {
			return err
		}
		if count == 0 {
			return ErrConflict
		}
		return nil
	})
}

func (s *DefaultSocialService) callback(platform string) string {
	return s.config.CallbackOrigin + "/api/v1/social/oauth/" + platform + "/callback"
}

func randomSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func safeImageURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || strings.TrimSpace(parsed.Host) == "" {
		return ""
	}
	return value
}
