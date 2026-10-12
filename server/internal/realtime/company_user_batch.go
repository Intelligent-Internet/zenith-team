package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// CompanyUserRelayFrame is one already-addressed frame. The batch is a bounded
// execution form of the existing Company-user audience, never a new audience.
type CompanyUserRelayFrame struct {
	CompanyID, UserID, EventID string
	Frame                      []byte
}
type RelayBatchResult struct {
	EventID string
	Err     error
}
type CompanyUserBatchRelay interface {
	PublishCompanyUserBatch(context.Context, []CompanyUserRelayFrame) []RelayBatchResult
}

// BroadcastToCompanyUsers prepares each identity once and preserves its result
// even when another XADD fails. The discussion dispatcher and the Project
// frames (cmd/server byProject, ADR 0017) call this bounded port.
func (b *EventBroadcaster) BroadcastToCompanyUsers(
	ctx context.Context,
	companyID string,
	userIDs []string,
	event BroadcastEvent,
) ([]RelayBatchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b == nil || b.local == nil || !addressable(companyID) || len(userIDs) > 50 || event.Type == "" ||
		event.CreatedAt.IsZero() {
		return nil, errors.New("invalid realtime identity batch")
	}
	var relay CompanyUserBatchRelay
	if b.relay != nil {
		var ok bool
		relay, ok = b.relay.(CompanyUserBatchRelay)
		if !ok {
			return nil, errors.New("realtime relay has no bounded batch port")
		}
	}
	frames := make([]CompanyUserRelayFrame, len(userIDs))
	seen := make(map[string]bool, len(userIDs))
	for i, userID := range userIDs {
		if !addressable(userID) || seen[userID] {
			return nil, errors.New("invalid realtime batch recipient")
		}
		seen[userID] = true
		target := event
		target.EventID = NewEventID()
		target.CompanyID = &companyID
		target.CreatedAt = target.CreatedAt.UTC()
		raw, err := json.Marshal(target)
		if err != nil {
			return nil, fmt.Errorf("encode realtime identity batch: %w", err)
		}
		frames[i] = CompanyUserRelayFrame{
			CompanyID: companyID,
			UserID:    userID,
			EventID:   target.EventID,
			Frame:     raw,
		}
	}
	results := make([]RelayBatchResult, len(frames))
	for i, frame := range frames {
		b.local.Deliver(
			audience{typ: audienceCompanyUser, companyID: companyID, userID: frame.UserID},
			frame.Frame,
			frame.EventID,
		)
		M.RecordEvent(event.Type)
		results[i].EventID = frame.EventID
	}
	if relay == nil || len(frames) == 0 {
		return results, nil
	}
	batchCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return relay.PublishCompanyUserBatch(batchCtx, frames), nil
}

func (r *ShardedStreamRelay) PublishCompanyUserBatch(
	ctx context.Context,
	frames []CompanyUserRelayFrame,
) []RelayBatchResult {
	results := make([]RelayBatchResult, len(frames))
	reject := func(err error) []RelayBatchResult {
		for i, f := range frames {
			results[i] = RelayBatchResult{EventID: f.EventID, Err: err}
		}
		return results
	}
	if r == nil || r.writeRDB == nil || len(frames) > 50 {
		return reject(errors.New("invalid relay batch"))
	}
	if err := ctx.Err(); err != nil {
		return reject(err)
	}
	for _, f := range frames {
		if !addressable(f.CompanyID) || !addressable(f.UserID) || f.EventID == "" || len(f.Frame) == 0 {
			return reject(errors.New("invalid relay batch frame"))
		}
	}
	// WithTimeout also bounds clients whose context timeout option is disabled
	// (for example an embedded caller); production Redis enables context deadlines.
	client := r.writeRDB.WithTimeout(time.Second)
	pipeline := client.Pipeline()
	commands := make([]*redis.StringCmd, len(frames))
	for i, f := range frames {
		ev := newEnvelope(r.nodeID, ScopeCompanyUser, f.UserID, f.CompanyID, f.Frame, f.EventID)
		commands[i] = pipeline.XAdd(
			ctx,
			&redis.XAddArgs{
				Stream: ShardedStreamKey(r.shardFor(ScopeCompanyUser, f.UserID)),
				MaxLen: r.config.StreamMaxLen,
				Approx: true,
				Values: envelopeRedisValues(ev),
			},
		)
	}
	_, batchErr := pipeline.Exec(ctx)
	for i, cmd := range commands {
		err := cmd.Err()
		if err == nil && cmd.Val() == "" {
			err = batchErr
		}
		results[i] = RelayBatchResult{EventID: frames[i].EventID, Err: err}
		if err != nil {
			M.RedisXAddErrors.Add(1)
		} else {
			M.RedisXAddTotal.Add(1)
		}
	}
	return results
}

var _ CompanyUserBatchRelay = (*ShardedStreamRelay)(nil)
