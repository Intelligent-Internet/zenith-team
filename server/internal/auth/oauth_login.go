package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// createOAuthLoginFlow persists one pending login flow and returns the
// database-generated identity. The schema and sqlc types own stored-row
// invariants; provider and callback documents are validated at their input
// boundaries instead.
func createOAuthLoginFlow(
	ctx context.Context,
	q *generated.Queries,
	flow NewOAuthLoginFlow,
) (uuid.UUID, error) {
	clientMetadata, err := encodeOAuthClientMetadata(flow.Client, flow.ClientMetadata)
	if err != nil {
		return uuid.Nil, err
	}
	created, err := q.CreateOAuthLoginFlow(ctx, generated.CreateOAuthLoginFlowParams{
		Provider:           flow.Provider,
		Client:             string(flow.Client),
		StateHash:          flow.StateHash,
		NonceHash:          flow.NonceHash,
		BrowserBindingHash: flow.BrowserBindingHash,
		RedirectUri:        flow.RedirectURI,
		ReturnPath:         util.PtrToText(flow.ReturnPath),
		CodeChallenge:      util.PtrToText(flow.CodeChallenge),
		ClientMetadata:     clientMetadata,
		ExpiresAt:          pgtype.Timestamptz{Time: flow.ExpiresAt, Valid: true},
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("create OAuth login flow: %w", err)
	}
	return created.ID, nil
}

func consumeWebOAuthLoginFlow(
	ctx context.Context,
	q *generated.Queries,
	match WebOAuthFlowConsumption,
) (OAuthLoginFlow, error) {
	flow, err := q.ConsumeWebOAuthLoginFlow(ctx, generated.ConsumeWebOAuthLoginFlowParams{
		Provider:           match.Provider,
		StateHash:          match.StateHash,
		BrowserBindingHash: match.BrowserBindingHash,
		RedirectUri:        match.RedirectURI,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OAuthLoginFlow{}, ErrOAuthFlowInvalid
		}
		return OAuthLoginFlow{}, fmt.Errorf("consume web OAuth login flow: %w", err)
	}
	return oauthLoginFlowFromDB(flow)
}

func consumeDesktopOAuthLoginFlow(
	ctx context.Context,
	q *generated.Queries,
	match DesktopOAuthFlowConsumption,
) (OAuthLoginFlow, error) {
	flow, err := q.ConsumeDesktopOAuthLoginFlow(ctx, generated.ConsumeDesktopOAuthLoginFlowParams{
		ID:            match.ID,
		Provider:      match.Provider,
		StateHash:     match.StateHash,
		RedirectUri:   match.RedirectURI,
		CodeChallenge: match.CodeChallenge,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return OAuthLoginFlow{}, ErrOAuthFlowInvalid
		}
		return OAuthLoginFlow{}, fmt.Errorf("consume desktop OAuth login flow: %w", err)
	}
	return oauthLoginFlowFromDB(flow)
}

func loginWithOAuthAndCreateSession(
	ctx context.Context,
	store *db.Store,
	input OAuthLoginInput,
) (User, uuid.UUID, error) {
	profile, err := json.Marshal(input.Identity.Profile)
	if err != nil {
		return User{}, uuid.Nil, fmt.Errorf("encode OAuth profile snapshot: %w", err)
	}
	sessionMetadata, err := encodeOAuthClientMetadata(input.Session.Client, input.Session.ClientMetadata)
	if err != nil {
		return User{}, uuid.Nil, err
	}
	session := NewSession{
		TokenPrefix:    input.Session.TokenPrefix,
		TokenHash:      input.Session.TokenHash,
		ExpiresAt:      input.Session.ExpiresAt,
		ClientMetadata: sessionMetadata,
	}

	var (
		authenticated User
		sessionID     uuid.UUID
	)
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		createSessionForUser := func(userID uuid.UUID) error {
			session.UserID = userID
			createdSession, err := queries.CreateAuthSession(ctx, createAuthSessionParams(session))
			if err != nil {
				return err
			}
			sessionID = createdSession.ID
			authenticated, err = getUserByID(ctx, queries, userID)
			return err
		}
		if _, err := queries.LockOAuthIdentity(ctx, generated.LockOAuthIdentityParams{
			Provider: input.Identity.Provider,
			Subject:  input.Identity.Subject,
		}); err != nil {
			return err
		}

		account, err := queries.GetOAuthAccountByIdentity(ctx, generated.GetOAuthAccountByIdentityParams{
			Provider: input.Identity.Provider,
			Subject:  input.Identity.Subject,
		})
		switch {
		case err == nil:
			user, err := queries.GetOAuthUserForUpdate(ctx, account.UserID)
			if err != nil {
				return err
			}
			if user.DisabledAt.Valid {
				return ErrAccountDisabled
			}
			verifiedAt := time.Now().UTC()
			user, err = queries.MarkUserEmailVerified(ctx, generated.MarkUserEmailVerifiedParams{
				ID: user.ID, Email: user.Email,
				VerifiedAt: pgtype.Timestamptz{Time: verifiedAt, Valid: true},
			})
			if err != nil {
				return err
			}
			if _, err := queries.UpdateOAuthAccountSnapshot(ctx, generated.UpdateOAuthAccountSnapshotParams{
				Email:   input.Identity.Email,
				Profile: profile,
				ID:      account.ID,
			}); err != nil {
				return err
			}
			return createSessionForUser(user.ID)
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		existing, err := queries.GetUserByEmail(ctx, input.Identity.Email)
		switch {
		case err == nil:
			user, err := queries.GetOAuthUserForUpdate(ctx, existing.ID)
			if err != nil {
				return err
			}
			if user.DisabledAt.Valid {
				return ErrAccountDisabled
			}
			verifiedAt := time.Now().UTC()
			user, err = queries.MarkUserEmailVerified(ctx, generated.MarkUserEmailVerifiedParams{
				ID: user.ID, Email: user.Email,
				VerifiedAt: pgtype.Timestamptz{Time: verifiedAt, Valid: true},
			})
			if err != nil {
				return err
			}
			if _, err := queries.CreateOAuthAccount(ctx, generated.CreateOAuthAccountParams{
				UserID:   user.ID,
				Provider: input.Identity.Provider,
				Subject:  input.Identity.Subject,
				Email:    input.Identity.Email,
				Profile:  profile,
			}); err != nil {
				return err
			}
			return createSessionForUser(user.ID)
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}

		user, err := queries.CreateOAuthUser(ctx, generated.CreateOAuthUserParams{
			Email:       input.Identity.Email,
			DisplayName: input.InitialDisplayName,
			AvatarUrl:   util.PtrToText(input.Identity.Profile.PictureURL),
		})
		if err != nil {
			return err
		}
		if _, err := queries.CreateOAuthAccount(ctx, generated.CreateOAuthAccountParams{
			UserID:   user.ID,
			Provider: input.Identity.Provider,
			Subject:  input.Identity.Subject,
			Email:    input.Identity.Email,
			Profile:  profile,
		}); err != nil {
			return err
		}
		return createSessionForUser(user.ID)
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrOAuthAccountConflict):
			return User{}, uuid.Nil, ErrOAuthAccountConflict
		case errors.Is(err, ErrAccountDisabled):
			return User{}, uuid.Nil, ErrAccountDisabled
		case isUniqueViolation(err, "users_email_ci_key"),
			isUniqueViolation(err, "oauth_accounts_user_id_provider_key"),
			isUniqueViolation(err, "oauth_accounts_provider_subject_key"):
			return User{}, uuid.Nil, ErrOAuthAccountConflict
		default:
			return User{}, uuid.Nil, fmt.Errorf("login with OAuth identity and create session: %w", err)
		}
	}
	return authenticated, sessionID, nil
}

type storedOAuthClientMetadata struct {
	Client ClientKind `json:"client"`
	OAuthClientMetadata
}

func encodeOAuthClientMetadata(client ClientKind, metadata OAuthClientMetadata) (json.RawMessage, error) {
	if client != ClientWeb && client != ClientDesktop {
		return nil, fmt.Errorf("%w: client must be web or desktop", ErrInvalidInput)
	}
	encoded, err := json.Marshal(storedOAuthClientMetadata{
		Client:              client,
		OAuthClientMetadata: metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: OAuth client metadata is invalid", ErrInvalidInput)
	}
	if len(encoded) > maxClientMetadataBytes {
		return nil, fmt.Errorf("%w: OAuth client metadata is too large", ErrInvalidInput)
	}
	return encoded, nil
}

func decodeOAuthClientMetadata(client ClientKind, encoded json.RawMessage) (OAuthClientMetadata, error) {
	var stored storedOAuthClientMetadata
	if err := util.BindStrictJSON(bytes.NewReader(encoded), &stored); err != nil {
		return OAuthClientMetadata{}, fmt.Errorf("decode OAuth client metadata: %w", err)
	}
	if stored.Client != client {
		return OAuthClientMetadata{}, errors.New("OAuth client metadata does not match flow client")
	}
	return stored.OAuthClientMetadata, nil
}

func oauthLoginFlowFromDB(flow generated.OauthLoginFlow) (OAuthLoginFlow, error) {
	clientMetadata, err := decodeOAuthClientMetadata(ClientKind(flow.Client), flow.ClientMetadata)
	if err != nil {
		return OAuthLoginFlow{}, err
	}
	return OAuthLoginFlow{
		ID:                 flow.ID,
		Provider:           flow.Provider,
		Client:             ClientKind(flow.Client),
		StateHash:          flow.StateHash,
		NonceHash:          flow.NonceHash,
		BrowserBindingHash: flow.BrowserBindingHash,
		RedirectURI:        flow.RedirectUri,
		ReturnPath:         util.TextToPtr(flow.ReturnPath),
		CodeChallenge:      util.TextToPtr(flow.CodeChallenge),
		ClientMetadata:     clientMetadata,
		ExpiresAt:          flow.ExpiresAt.Time,
		CreatedAt:          flow.CreatedAt.Time,
	}, nil
}
