package calendar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/inbox"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// RunContext comes from the verified run proof, never from model-authored input.
// Personal authority is limited to a direct conversation owned by the caller.
type RunContext struct {
	RunID          uuid.UUID
	ConversationID uuid.UUID
	AgentID        uuid.UUID
	RequestKey     uuid.UUID
}

type ReadResult struct {
	Catalog Catalog      `json:"catalog"`
	Items   []Occurrence `json:"items"`
}

func lockRun(ctx context.Context, q *generated.Queries, p auth.UserPrincipal, run RunContext) error {
	if p.CompanyID == uuid.Nil {
		return ErrNotFound
	}
	if err := lockActor(ctx, q, p); err != nil {
		return err
	}
	_, err := q.LockCalendarConversation(
		ctx,
		generated.LockCalendarConversationParams{
			ConversationID: run.ConversationID,
			CompanyID:      p.CompanyID,
			UserID:         uuid.NullUUID{UUID: p.UserID, Valid: true},
		},
	)
	if err != nil {
		return calendarError(err)
	}
	_, err = q.LockCalendarRun(
		ctx,
		generated.LockCalendarRunParams{
			RunID:          run.RunID,
			CompanyID:      p.CompanyID,
			ConversationID: run.ConversationID,
			AgentID:        run.AgentID,
		},
	)
	if err != nil {
		return calendarError(err)
	}
	return nil
}

func (s *DefaultCalendarService) ReadForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run RunContext,
	from, to time.Time,
) (ReadResult, error) {
	var result ReadResult
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockRun(ctx, q, p, run); err != nil {
			return err
		}
		if err := ensureCalendars(ctx, q, p); err != nil {
			return err
		}
		var err error
		result.Catalog, err = catalog(ctx, q, p)
		if err != nil {
			return err
		}
		result.Items, err = list(ctx, q, p, from, to)
		return err
	})
	return result, err
}

func (s *DefaultCalendarService) WriteForRun(
	ctx context.Context,
	p auth.UserPrincipal,
	run RunContext,
	command Command,
) (CommandResult, error) {
	if run.RequestKey == uuid.Nil {
		return CommandResult{}, ErrInvalid
	}
	payload, err := json.Marshal(command)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode calendar command: %w", err)
	}
	digest := sha256.Sum256(payload)
	var result CommandResult
	m := mutation{principal: p, actor: inbox.Actor{Kind: "agent", ID: &run.AgentID}}
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		if err := lockRun(ctx, q, p, run); err != nil {
			return err
		}
		receipt, err := q.GetCalendarToolReceipt(
			ctx,
			generated.GetCalendarToolReceiptParams{RunID: run.RunID, RequestKey: run.RequestKey},
		)
		if err == nil {
			if !bytes.Equal(receipt.PayloadHash, digest[:]) {
				return ErrConflict
			}
			return json.Unmarshal(receipt.Response, &result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result, err = m.writeInTx(ctx, q, command)
		if err != nil {
			return err
		}
		response, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return q.CreateCalendarToolReceipt(
			ctx,
			generated.CreateCalendarToolReceiptParams{
				RunID:       run.RunID,
				RequestKey:  run.RequestKey,
				PayloadHash: digest[:],
				Response:    response,
			},
		)
	})
	if err == nil {
		s.publishMutation(ctx, m)
	}
	return result, err
}
