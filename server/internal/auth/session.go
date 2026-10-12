package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/db"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// createUserWithSession creates the user and its first auth session in one
// transaction, so a signup never commits a user without the session that
// authenticates it.
func createUserWithSession(
	ctx context.Context,
	store *db.Store,
	newUser NewUser,
	newSession NewSession,
) (User, uuid.UUID, error) {
	var (
		created   User
		sessionID uuid.UUID
	)
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		user, err := queries.CreateUser(ctx, generated.CreateUserParams{
			Email:           newUser.Email,
			PasswordHash:    pgtype.Text{String: newUser.PasswordHash, Valid: true},
			DisplayName:     newUser.DisplayName,
			EmailVerifiedAt: nullableTimestamptz(newUser.EmailVerifiedAt),
		})
		if err != nil {
			return err
		}
		newSession.UserID = user.ID
		session, err := queries.CreateAuthSession(ctx, createAuthSessionParams(newSession))
		if err != nil {
			return err
		}
		sessionID = session.ID
		created, err = getUserByID(ctx, queries, user.ID)
		return err
	})
	if err != nil {
		if isUniqueViolation(err, "users_email_ci_key") {
			return User{}, uuid.Nil, ErrEmailExists
		}
		return User{}, uuid.Nil, fmt.Errorf("create user and auth session: %w", err)
	}
	return created, sessionID, nil
}

func findUserByEmail(ctx context.Context, q *generated.Queries, email string) (User, error) {
	user, err := q.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("get user by email: %w", err)
	}
	return userFromDB(user), nil
}

// createSession stores one auth session for a user that is still enabled and
// not deleted; the guarded insert is what turns a login that raced an account
// deletion into ErrAccountDisabled instead of a live session.
func createSession(ctx context.Context, q *generated.Queries, session NewSession) (uuid.UUID, error) {
	params := createAuthSessionParams(session)
	created, err := q.CreateAuthSessionForEnabledUser(
		ctx,
		generated.CreateAuthSessionForEnabledUserParams{
			UserID: params.UserID, TokenPrefix: params.TokenPrefix,
			TokenHash: params.TokenHash, ExpiresAt: params.ExpiresAt,
			RotatedFromSessionID: params.RotatedFromSessionID, RotationID: params.RotationID,
			ClientMetadata: params.ClientMetadata, Client: params.Client,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrAccountDisabled
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("create auth session: %w", err)
	}
	return created.ID, nil
}

func createSessionWithUserProjection(
	ctx context.Context,
	store *db.Store,
	session NewSession,
) (User, uuid.UUID, error) {
	var (
		user      User
		sessionID uuid.UUID
	)
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		var err error
		sessionID, err = createSession(ctx, queries, session)
		if err != nil {
			return err
		}
		user, err = getUserByID(ctx, queries, session.UserID)
		return err
	})
	if err != nil {
		return User{}, uuid.Nil, err
	}
	return user, sessionID, nil
}

func rotateSession(
	ctx context.Context,
	store *db.Store,
	bus *events.Bus,
	refreshHash []byte,
	next NewSession,
) (RotateSessionResult, error) {
	result := RotateSessionResult{Mutation: SessionMutationResult{Outcome: SessionMutationInvalid}}
	var pending []events.Event
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		pending = pending[:0]
		current, storedUser, found, err := lockAuthSessionMutation(ctx, queries, refreshHash)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if storedUser.DisabledAt.Valid {
			return nil
		}
		if !sameSessionClient(current.ClientMetadata, next.ClientMetadata) {
			revocations, err := revokeLiveSessionFamily(
				ctx,
				queries,
				current.ID,
				SessionRevocationRefreshTokenReuse,
				uuid.NullUUID{},
			)
			if err != nil {
				return err
			}
			// An expired live session has no revocable rows. Keep natural expiry
			// distinct from refresh-token reuse instead of reporting a mutation.
			if !current.RevokedAt.Valid && len(revocations) == 0 {
				return nil
			}
			stageSessionRevocations(&pending, revocations, events.SystemActor())
			result.Mutation = SessionMutationResult{
				Outcome:     SessionMutationTokenReuse,
				Revocations: revocations,
			}
			return nil
		}
		if current.RevokedAt.Valid {
			if next.RotationID != nil {
				child, err := queries.GetDirectRotationChildByRotationID(
					ctx,
					generated.GetDirectRotationChildByRotationIDParams{
						RootSessionID: current.ID,
						RotationID:    *next.RotationID,
					},
				)
				if err == nil {
					result.User, err = getUserByID(ctx, queries, storedUser.ID)
					if err != nil {
						return err
					}
					result.SessionID = child.ID
					result.RefreshExpiresAt = child.ExpiresAt.Time
					result.Mutation = SessionMutationResult{Outcome: SessionMutationRecovered}
					return nil
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			}
			if _, err := queries.GetRecentDirectRotationChild(
				ctx,
				generated.GetRecentDirectRotationChildParams{
					RootSessionID:  current.ID,
					ClientMetadata: current.ClientMetadata,
				},
			); err == nil {
				result.Mutation = SessionMutationResult{Outcome: SessionMutationAlreadyRotated}
				return nil
			} else if !errors.Is(
				err,
				pgx.ErrNoRows,
			) {
				return err
			}
			revocations, err := revokeLiveSessionFamily(
				ctx,
				queries,
				current.ID,
				SessionRevocationRefreshTokenReuse,
				uuid.NullUUID{},
			)
			if err != nil {
				return err
			}
			stageSessionRevocations(&pending, revocations, events.SystemActor())
			result.Mutation = SessionMutationResult{
				Outcome:     SessionMutationTokenReuse,
				Revocations: revocations,
			}
			return nil
		}
		row, err := queries.RevokeLiveAuthSession(ctx, generated.RevokeLiveAuthSessionParams{
			RevocationReason: string(SessionRevocationRefreshRotated),
			RevokedByUserID:  uuid.NullUUID{UUID: current.UserID, Valid: true},
			ID:               current.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		revocation := sessionRevocationFromRow(row.ID, row.UserID, row.RevokedAt, row.RevocationReason)
		currentID := current.ID
		next.UserID = current.UserID
		next.RotatedFromSessionID = &currentID
		next.ClientMetadata = current.ClientMetadata
		// A session keeps its holder for life: a daemon's refresh stays a
		// daemon session (D31).
		next.Client = SessionClient(current.Client)
		createdSession, err := queries.CreateAuthSession(ctx, createAuthSessionParams(next))
		if err != nil {
			return err
		}
		result.SessionID = createdSession.ID
		result.RefreshExpiresAt = createdSession.ExpiresAt.Time
		stageSessionRevocations(
			&pending,
			[]SessionRevocation{revocation},
			events.UserActor(current.UserID.String()),
		)
		result.User, err = getUserByID(ctx, queries, storedUser.ID)
		if err != nil {
			return err
		}
		result.Mutation = SessionMutationResult{
			Outcome:     SessionMutationRotated,
			Revocations: []SessionRevocation{revocation},
		}
		return nil
	})
	if err != nil {
		return RotateSessionResult{}, fmt.Errorf("rotate auth session: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func sameSessionClient(current, next json.RawMessage) bool {
	var currentMetadata, nextMetadata struct {
		Client ClientKind `json:"client"`
	}
	if json.Unmarshal(current, &currentMetadata) != nil || json.Unmarshal(next, &nextMetadata) != nil {
		return false
	}
	return currentMetadata.Client != "" && currentMetadata.Client == nextMetadata.Client
}

func revokeSession(
	ctx context.Context,
	store *db.Store,
	bus *events.Bus,
	refreshHash []byte,
) (SessionMutationResult, error) {
	result := SessionMutationResult{Outcome: SessionMutationNoChange}
	var pending []events.Event
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		pending = pending[:0]
		session, _, found, err := lockAuthSessionMutation(ctx, queries, refreshHash)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		revocations, err := revokeLiveSessionFamily(
			ctx,
			queries,
			session.ID,
			SessionRevocationLogout,
			uuid.NullUUID{UUID: session.UserID, Valid: true},
		)
		if err != nil {
			return err
		}
		if len(revocations) == 0 {
			return nil
		}
		stageSessionRevocations(
			&pending,
			revocations,
			events.UserActor(session.UserID.String()),
		)
		result = SessionMutationResult{Outcome: SessionMutationLoggedOut, Revocations: revocations}
		return nil
	})
	if err != nil {
		return SessionMutationResult{}, fmt.Errorf("revoke auth session: %w", err)
	}
	for _, group := range events.GroupByTopic(pending) {
		bus.Publish(ctx, group.Topic, group.Events...)
	}
	return result, nil
}

func lockAuthSessionMutation(
	ctx context.Context,
	queries *generated.Queries,
	refreshHash []byte,
) (generated.AuthSession, generated.User, bool, error) {
	ownerID, err := queries.GetAuthSessionOwnerByTokenHash(ctx, refreshHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.AuthSession{}, generated.User{}, false, nil
	}
	if err != nil {
		return generated.AuthSession{}, generated.User{}, false, fmt.Errorf(
			"resolve auth session owner: %w",
			err,
		)
	}
	user, err := queries.LockUserForAuthSessionMutation(ctx, ownerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.AuthSession{}, generated.User{}, false, nil
	}
	if err != nil {
		return generated.AuthSession{}, generated.User{}, false, fmt.Errorf(
			"lock User for auth session mutation: %w",
			err,
		)
	}
	session, err := queries.LockAuthSessionByTokenHash(ctx, refreshHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.AuthSession{}, generated.User{}, false, nil
	}
	if err != nil {
		return generated.AuthSession{}, generated.User{}, false, fmt.Errorf("lock auth session: %w", err)
	}
	return session, user, true, nil
}

func revokeLiveSessionFamily(
	ctx context.Context,
	queries *generated.Queries,
	rootSessionID uuid.UUID,
	reason SessionRevocationReason,
	revokedByUserID uuid.NullUUID,
) ([]SessionRevocation, error) {
	if _, err := queries.LockAuthSessionFamily(ctx, rootSessionID); err != nil {
		return nil, fmt.Errorf("lock auth session family: %w", err)
	}
	rows, err := queries.RevokeLiveAuthSessionFamily(ctx, generated.RevokeLiveAuthSessionFamilyParams{
		RevocationReason: string(reason),
		RevokedByUserID:  revokedByUserID,
		RootSessionID:    rootSessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("revoke live auth session family: %w", err)
	}
	revocations := make([]SessionRevocation, len(rows))
	for index, row := range rows {
		revocations[index] = sessionRevocationFromRow(
			row.ID,
			row.UserID,
			row.RevokedAt,
			row.RevocationReason,
		)
	}
	return revocations, nil
}

func sessionRevocationFromRow(
	sessionID uuid.UUID,
	userID uuid.UUID,
	revokedAt pgtype.Timestamptz,
	reason pgtype.Text,
) SessionRevocation {
	return SessionRevocation{
		SessionID:   sessionID,
		UserID:      userID,
		Reason:      SessionRevocationReason(reason.String),
		EffectiveAt: revokedAt.Time.UTC(),
	}
}

func stageSessionRevocations(
	pending *[]events.Event,
	revocations []SessionRevocation,
	actor events.Actor,
) {
	for _, revocation := range revocations {
		*pending = append(*pending, newAuthSessionRevokedEvent(revocation, actor))
	}
}

func getUserByID(ctx context.Context, q *generated.Queries, userID uuid.UUID) (User, error) {
	row, err := q.GetUserProjection(ctx, userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("get User projection: %w", err)
	}
	return userFromProjection(row), nil
}

func createAuthSessionParams(session NewSession) generated.CreateAuthSessionParams {
	client := session.Client
	if client == "" {
		client = SessionClientPerson
	}
	return generated.CreateAuthSessionParams{
		UserID:               session.UserID,
		TokenPrefix:          session.TokenPrefix,
		TokenHash:            session.TokenHash,
		ExpiresAt:            pgtype.Timestamptz{Time: session.ExpiresAt, Valid: true},
		RotatedFromSessionID: util.NullUUIDFromPtr(session.RotatedFromSessionID),
		RotationID:           util.NullUUIDFromPtr(session.RotationID),
		ClientMetadata:       session.ClientMetadata,
		Client:               string(client),
	}
}

func nullableTimestamptz(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func userFromDB(user generated.User) User {
	passwordHash := ""
	if user.PasswordHash.Valid {
		passwordHash = user.PasswordHash.String
	}
	return User{
		ID:               user.ID,
		Email:            user.Email,
		PasswordHash:     passwordHash,
		DisplayName:      user.DisplayName,
		AvatarURL:        util.TextToPtr(user.AvatarUrl),
		Biography:        user.Biography,
		PhoneCountryISO2: util.TextToPtr(user.PhoneCountryIso2),
		PhoneNumber:      util.TextToPtr(user.PhoneNumber),
		DisabledAt:       util.TimestampToTime(user.DisabledAt),
		DeletedAt:        util.TimestampToTime(user.DeletedAt),
		EmailVerifiedAt:  util.TimestampToTime(user.EmailVerifiedAt),
		CreatedAt:        user.CreatedAt.Time,
		UpdatedAt:        user.UpdatedAt.Time,
		Version:          user.Version,
	}
}

func userFromProjection(row generated.GetUserProjectionRow) User {
	user := userFromDB(row.User)
	user.LinkedProviders = row.LinkedProviders
	return user
}

func isUniqueViolation(err error, constraint string) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505" &&
		postgresError.ConstraintName == constraint
}
