package social

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	adapter "boardly.dev/boardly/server/internal/infra/social"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func (s *DefaultSocialService) claim(ctx context.Context, userID, id uuid.UUID) (generated.SocialConnection, error) {
	var row generated.SocialConnection
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockEnabledUserForMembershipMutation(ctx, userID); err != nil {
			return socialQueryError(err)
		}
		var err error
		row, err = q.ClaimSocialOperation(ctx, generated.ClaimSocialOperationParams{ID: id, UserID: userID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return err
	})
	return row, err
}

func (s *DefaultSocialService) release(ctx context.Context, row generated.SocialConnection) error {
	return s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		return q.ReleaseSocialOperation(
			ctx,
			generated.ReleaseSocialOperationParams{ID: row.ID, OperationToken: row.OperationToken},
		)
	})
}

func (s *DefaultSocialService) token(row generated.SocialConnection) (adapter.Token, error) {
	if s.vault == nil {
		return adapter.Token{}, ErrReconnect
	}
	raw, err := s.vault.open(row.Credentials, row.UserID.String()+":"+row.Platform)
	if err != nil {
		return adapter.Token{}, ErrReconnect
	}
	var token adapter.Token
	if err = json.Unmarshal(raw, &token); err != nil {
		return adapter.Token{}, ErrReconnect
	}
	return token, nil
}

// A database lease spans provider I/O; a bounded call finishes before that lease
// expires. Reconnect changes the lease token so an old refresh cannot overwrite it.
func (s *DefaultSocialService) maintain(ctx context.Context, row generated.SocialConnection) (adapter.Token, error) {
	client, err := s.connector(row.Platform, false)
	if err != nil {
		return adapter.Token{}, err
	}
	token, err := s.token(row)
	if err != nil {
		return token, err
	}
	if row.ReconnectRequired {
		return token, ErrReconnect
	}
	now := time.Now().UTC()
	lead := 5 * time.Minute
	if row.Platform == "instagram" || row.Platform == "linkedin" {
		lead = 7 * 24 * time.Hour
	}
	refresh := !token.ExpiresAt.IsZero() && token.ExpiresAt.Before(now.Add(lead)) && client.CanRefresh(token)
	next := now.Add(24 * time.Hour)
	switch {
	case refresh:
		var refreshed adapter.Token
		refreshed, err = client.Refresh(ctx, token)
		if err == nil {
			token = refreshed
		}
	case !token.ExpiresAt.IsZero() && !token.ExpiresAt.After(now):
		err = &adapter.ProviderError{Code: "invalid_token"}
	case !row.MaintenanceAt.Valid || !row.MaintenanceAt.Time.After(now):
		err = client.Validate(ctx, token)
	}
	reconnect := adapter.RequiresReconnect(err)
	if err != nil {
		next = now.Add(5 * time.Minute)
	} else if !token.ExpiresAt.IsZero() {
		due := token.ExpiresAt
		if client.CanRefresh(token) {
			due = due.Add(-lead)
		}
		if due.Before(next) {
			next = due
		}
		if next.Before(now.Add(time.Minute)) {
			next = now.Add(time.Minute)
		}
	}
	encoded, encodeErr := json.Marshal(token)
	if encodeErr != nil {
		return token, encodeErr
	}
	sealed, sealErr := s.vault.seal(encoded, row.UserID.String()+":"+row.Platform)
	if sealErr != nil {
		return token, sealErr
	}
	saveCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	saveErr := s.store.WithinTx(saveCtx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if _, err := q.LockEnabledUserForMembershipMutation(saveCtx, row.UserID); err != nil {
			return socialQueryError(err)
		}
		count, err := q.SaveSocialCredentials(
			saveCtx,
			generated.SaveSocialCredentialsParams{
				ID:             row.ID,
				OperationToken: row.OperationToken,
				Credentials:    sealed,
				ExpiresAt: pgtype.Timestamptz{
					Time:  token.ExpiresAt,
					Valid: !token.ExpiresAt.IsZero(),
				},
				ReconnectRequired: reconnect,
				MaintenanceAt:     pgtype.Timestamptz{Time: next, Valid: !reconnect},
			},
		)
		if err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		return nil
	})
	if saveErr != nil {
		return token, saveErr
	}
	if reconnect {
		return token, ErrReconnect
	}
	if err != nil {
		return token, fmt.Errorf("%w: %w", ErrProvider, err)
	}
	return token, nil
}

func (s *DefaultSocialService) MaintainCredentials(ctx context.Context) error {
	rows, err := s.store.Queries().ListSocialMaintenance(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, candidate := range rows {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.maintainCandidate(
			ctx,
			candidate,
		); err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrReconnect) &&
			!errors.Is(err, ErrUnavailable) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (s *DefaultSocialService) maintainCandidate(
	ctx context.Context,
	candidate generated.ListSocialMaintenanceRow,
) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	row, err := s.claim(ctx, candidate.UserID, candidate.ID)
	if err != nil {
		return err
	}
	_, err = s.maintain(ctx, row)
	cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stop()
	return errors.Join(err, s.release(cleanup, row))
}
