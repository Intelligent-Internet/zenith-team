package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/redis/go-redis/v9"
)

// EphemeralRelay delivers replaceable gestures without writing the durable
// event stream. Redis Pub/Sub loss is harmless: the next presence heartbeat
// replaces it and HTTP reads recover the current bounded, expiring sessions.
type EphemeralRelay struct {
	hub       *Hub
	client    *redis.Client
	sub       *redis.PubSub
	source    string
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

type ephemeralFrame struct {
	Source     string         `json:"source"`
	CompanyID  string         `json:"companyId"`
	Event      BroadcastEvent `json:"event"`
	Recipients *[]uuid.UUID   `json:"recipients,omitempty"`
}

func NewEphemeralRelay(ctx context.Context, hub *Hub, client *redis.Client) (*EphemeralRelay, error) {
	if hub == nil {
		return nil, errors.New("ephemeral relay requires a hub")
	}
	runCtx, cancel := context.WithCancel(ctx)
	r := &EphemeralRelay{hub: hub, client: client, source: NewEventID(), cancel: cancel, done: make(chan struct{})}
	if client == nil {
		close(r.done)
		return r, nil
	}
	r.sub = client.Subscribe(runCtx, "zenith:ephemeral")
	if _, err := r.sub.Receive(runCtx); err != nil {
		cancel()
		_ = r.sub.Close()
		return nil, err
	}
	go r.receive(runCtx)
	return r, nil
}

func (r *EphemeralRelay) receive(ctx context.Context) {
	defer close(r.done)
	messages := r.sub.Channel(redis.WithChannelSize(64))
	for {
		select {
		case <-ctx.Done():
			return
		case message, ok := <-messages:
			if !ok {
				return
			}
			if len(message.Payload) > 32<<10 {
				continue
			}
			var frame ephemeralFrame
			if json.Unmarshal([]byte(message.Payload), &frame) != nil || frame.Source == r.source ||
				!addressable(frame.CompanyID) {
				continue
			}
			if frame.Event.EventID == "" || frame.Event.CompanyID == nil ||
				*frame.Event.CompanyID != frame.CompanyID {
				continue
			}
			raw, err := json.Marshal(frame.Event)
			if err != nil {
				continue
			}
			r.deliver(frame, raw)
		}
	}
}

func (r *EphemeralRelay) Publish(ctx context.Context, company string, event BroadcastEvent) error {
	return r.publish(ctx, ephemeralFrame{CompanyID: company, Event: event})
}

// A non-nil recipient list is a restricted audience, including an empty list.
func (r *EphemeralRelay) PublishToCompanyUsers(
	ctx context.Context,
	company string,
	recipients []uuid.UUID,
	event BroadcastEvent,
) error {
	for chunk := range slices.Chunk(recipients, 256) {
		if err := r.publish(
			ctx,
			ephemeralFrame{CompanyID: company, Event: event, Recipients: &chunk},
		); err != nil {
			return err
		}
	}
	return nil
}

func (r *EphemeralRelay) deliver(frame ephemeralFrame, raw []byte) {
	if frame.Recipients == nil {
		r.hub.Deliver(audience{typ: audienceCompany, companyID: frame.CompanyID}, raw, frame.Event.EventID)
		return
	}
	for _, user := range *frame.Recipients {
		r.hub.Deliver(
			audience{typ: audienceCompanyUser, companyID: frame.CompanyID, userID: user.String()},
			raw,
			frame.Event.EventID,
		)
	}
}

func (r *EphemeralRelay) publish(ctx context.Context, frame ephemeralFrame) error {
	event := frame.Event
	if !addressable(frame.CompanyID) || event.CompanyID == nil || *event.CompanyID != frame.CompanyID ||
		event.Type == "" ||
		event.CreatedAt.IsZero() {
		return errors.New("invalid ephemeral event")
	}
	frame.Source = r.source
	frame.Event.EventID = NewEventID()
	encoded, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	if len(encoded) > 32<<10 {
		return errors.New("ephemeral event exceeds limit")
	}
	raw, err := json.Marshal(frame.Event)
	if err != nil {
		return err
	}
	r.deliver(frame, raw)
	if r.client != nil {
		return r.client.Publish(ctx, "zenith:ephemeral", encoded).Err()
	}
	return nil
}

func (r *EphemeralRelay) Close() {
	r.closeOnce.Do(func() {
		r.cancel()
		if r.sub != nil {
			_ = r.sub.Close()
		}
		<-r.done
	})
}
