package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/approval"
	"github.com/Intelligent-Internet/zenith-team/server/internal/conversation"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	"github.com/Intelligent-Internet/zenith-team/server/internal/realtime"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type discussionBatchBroadcaster interface {
	BroadcastToCompany(string, realtime.BroadcastEvent) error
	BroadcastToCompanyUsers(
		context.Context,
		string,
		[]string,
		realtime.BroadcastEvent,
	) ([]realtime.RelayBatchResult, error)
}
type discussionDispatchKey struct {
	companyID, conversationID, userID uuid.UUID
	// messageID keeps message.updated hints for different messages apart.
	messageID uuid.UUID
	eventType string
}
type discussionDispatchJob struct {
	event                   events.Event
	userID                  uuid.UUID
	messageID               uuid.UUID
	revision                int64
	executionConversationID uuid.UUID
	retryAt                 time.Time
	failures                int
}

// isDiscussionKind reports whether a conversation follows its members: a
// channel or a human direct message. Their frames go through this dispatcher.
func isDiscussionKind(kind conversation.Kind) bool {
	return kind == "channel" || kind == "human_direct"
}

type discussionDispatcher struct {
	queries     *generated.Queries
	broadcaster discussionBatchBroadcaster
	mu          sync.Mutex
	changes     map[discussionDispatchKey]discussionDispatchJob
	revokes     map[discussionDispatchKey]discussionDispatchJob
	resync      map[discussionDispatchKey]discussionDispatchJob
	capacity    int
}

func newDiscussionDispatcher(q *generated.Queries, b discussionBatchBroadcaster) *discussionDispatcher {
	return &discussionDispatcher{
		queries:     q,
		broadcaster: b,
		changes:     make(map[discussionDispatchKey]discussionDispatchJob),
		revokes:     make(map[discussionDispatchKey]discussionDispatchJob),
		resync:      make(map[discussionDispatchKey]discussionDispatchJob),
		capacity:    4096,
	}
}

// enqueue performs no I/O. Receipts acknowledge PostgreSQL independently of
// hint delivery; overflow coalesces into a Company-level recovery hint without
// naming the private conversation that could not be queued.
func (d *discussionDispatcher) enqueue(_ context.Context, event events.Event) error {
	job := discussionDispatchJob{event: event}
	// Raw execution payloads never enter the discussion delivery queue. Only
	// the parent channel is resolved at delivery, under its current grants.
	switch event.EntityType {
	case execution.Topic:
		var kind string
		if event.Name == execution.EventRunMessageCreated {
			item, err := events.PayloadOf[execution.TranscriptItem](event)
			if err != nil {
				return err
			}
			kind = item.ConversationKind
		} else {
			run, err := events.PayloadOf[execution.RunSnapshot](event)
			if err != nil {
				return err
			}
			kind = run.ConversationKind
		}
		if kind != "channel_agent" {
			return nil
		}
		job.executionConversationID = event.ConversationID
	case approval.Topic, approval.TopicQuestion:
		// Questions and approvals can change while the Run remains running.
		// Non-channel origins have no parent and are skipped during delivery.
		job.executionConversationID = event.ConversationID
	}
	if job.executionConversationID != uuid.Nil {
		job.event = events.Event{
			EntityType:     conversation.Topic,
			Name:           conversation.EventChannelAgentsChanged,
			CompanyID:      event.CompanyID,
			ConversationID: event.ConversationID,
			CreatedAt:      event.CreatedAt,
			Actor:          events.SystemActor(),
			Payload: conversation.ChannelAgentsChanged{
				ActivityOnly: event.Name == execution.EventRunMessageCreated,
			},
		}
		event = job.event
	}
	switch event.Name {
	case conversation.EventChannelAgentsChanged:
		job.revision = event.CreatedAt.UnixNano()
	case conversation.EventDiscussionHuddleChanged:
		payload, err := events.PayloadOf[conversation.DiscussionHuddleChanged](event)
		if err != nil {
			return err
		}
		job.revision = payload.Revision
	case conversation.EventDiscussionChanged:
		payload, err := events.PayloadOf[conversation.DiscussionChanged](event)
		if err != nil {
			return err
		}
		job.revision = payload.Revision
	case conversation.EventDiscussionStateChanged:
		payload, err := events.PayloadOf[conversation.DiscussionStateChanged](event)
		if err != nil {
			return err
		}
		job.revision = payload.Revision
		job.userID = payload.UserID
	case conversation.EventDiscussionAccessRevoked:
		payload, err := events.PayloadOf[conversation.DiscussionAccessRevoked](event)
		if err != nil {
			return err
		}
		job.revision = payload.GrantGeneration
		job.userID = payload.UserID
	case conversation.EventMessageUpdated:
		payload, err := events.PayloadOf[conversation.MessageUpdated](event)
		if err != nil {
			return err
		}
		if !isDiscussionKind(payload.ConversationKind) {
			return nil
		}
		job.messageID = payload.MessageID
	default:
		return nil
	}
	return d.queue(job)
}

func (d *discussionDispatcher) queue(job discussionDispatchJob) error {
	event := job.event
	key := discussionDispatchKey{
		companyID:      event.CompanyID,
		conversationID: event.ConversationID,
		userID:         job.userID,
		messageID:      job.messageID,
		eventType:      event.Name,
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	lane := d.changes
	switch event.Name {
	case conversation.EventDiscussionAccessRevoked:
		lane = d.revokes
	case conversation.EventDiscussionResync:
		lane = d.resync
	}
	if existing, ok := lane[key]; ok {
		if existing.retryAt.After(job.retryAt) {
			job.retryAt = existing.retryAt
		}
		if event.Name == conversation.EventChannelAgentsChanged {
			incoming, err := events.PayloadOf[conversation.ChannelAgentsChanged](job.event)
			if err != nil {
				return err
			}
			previous, err := events.PayloadOf[conversation.ChannelAgentsChanged](existing.event)
			if err != nil {
				return err
			}
			incoming.ActivityOnly = incoming.ActivityOnly && previous.ActivityOnly
			job.event.Payload = incoming
			lane[key] = job
			return nil
		}
		if event.Name == conversation.EventDiscussionHuddleChanged || job.revision > existing.revision {
			lane[key] = job
		}
		return nil
	}
	if len(lane) >= d.capacity && event.Name != conversation.EventDiscussionResync {
		realtime.M.DiscussionHintsDroppedTotal.Add(1)
		recovery := events.Event{
			EntityType: conversation.Topic,
			Name:       conversation.EventDiscussionResync,
			CompanyID:  event.CompanyID,
			CreatedAt:  time.Now().UTC(),
			Actor:      events.SystemActor(),
			Payload:    struct{}{},
		}
		d.resync[discussionDispatchKey{companyID: event.CompanyID, eventType: recovery.Name}] = discussionDispatchJob{
			event: recovery,
		}
		return nil
	}
	lane[key] = job
	return nil
}

// discussionDispatchWorkers is both the worker count and the most jobs one
// tick takes, so a tick never hands out more work than the workers can start.
const discussionDispatchWorkers = 8

func (d *discussionDispatcher) take() []discussionDispatchJob {
	d.mu.Lock()
	defer d.mu.Unlock()
	jobs := make([]discussionDispatchJob, 0, discussionDispatchWorkers)
	for _, lane := range []map[discussionDispatchKey]discussionDispatchJob{d.revokes, d.resync, d.changes} {
		for key, job := range lane {
			if time.Now().Before(job.retryAt) {
				continue
			}
			if len(jobs) == discussionDispatchWorkers {
				return jobs
			}
			jobs = append(jobs, job)
			delete(lane, key)
		}
	}
	return jobs
}

// run owns eight workers and joins them on shutdown. Jobs are pulled from
// bounded coalescing maps, so slow Redis never creates a goroutine per message.
func (d *discussionDispatcher) run(ctx context.Context) error {
	jobs := make(chan discussionDispatchJob)
	var workers sync.WaitGroup
	for range discussionDispatchWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					d.deliver(ctx, job)
				}
			}
		}()
	}
	defer func() { close(jobs); workers.Wait() }()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for _, job := range d.take() {
				select {
				case <-ctx.Done():
					return nil
				case jobs <- job:
				}
			}
		}
	}
}

func (d *discussionDispatcher) deliver(ctx context.Context, job discussionDispatchJob) {
	if job.event.Name == conversation.EventDiscussionResync {
		if err := d.broadcaster.BroadcastToCompany(
			job.event.CompanyID.String(),
			realtime.NewBroadcastEvent(job.event),
		); err != nil {
			d.retry(job)
		}
		return
	}
	deadline, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if job.executionConversationID != uuid.Nil {
		parent, err := d.queries.GetChannelAgentParent(deadline, generated.GetChannelAgentParentParams{
			CompanyID: job.event.CompanyID, ConversationID: job.executionConversationID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return
		}
		if err != nil {
			d.retry(job)
			return
		}
		job.event.ConversationID = parent
		payload, err := events.PayloadOf[conversation.ChannelAgentsChanged](job.event)
		if err != nil {
			return
		}
		payload.ConversationID = parent
		job.event.Payload = payload
		job.executionConversationID = uuid.Nil
	}
	var users []string
	if job.event.Name == conversation.EventDiscussionAccessRevoked {
		// The removed user is intentionally not resolved through current grants.
		users = []string{job.userID.String()}
	} else {
		recipients, err := d.queries.ListDiscussionRecipients(
			deadline,
			generated.ListDiscussionRecipientsParams{
				CompanyID:      job.event.CompanyID,
				ConversationID: job.event.ConversationID,
			},
		)
		if err != nil {
			d.retry(job)
			return
		}
		for _, user := range recipients {
			if job.userID == uuid.Nil || user == job.userID {
				users = append(users, user.String())
			}
		}
	}
	if len(users) == 0 {
		return
	}
	// General includes the full Company; retain the transport's 50-user bound.
	for batch := range slices.Chunk(users, 50) {
		results, err := d.broadcaster.BroadcastToCompanyUsers(
			deadline,
			job.event.CompanyID.String(),
			batch,
			realtime.NewBroadcastEvent(job.event),
		)
		if err != nil {
			d.retry(job)
			return
		}
		for _, result := range results {
			if result.Err != nil {
				d.retry(job)
				return
			}
		}
	}
}

func (d *discussionDispatcher) retry(job discussionDispatchJob) {
	// Keep the opaque parent hint until recipient lookup and relay succeed.
	// Replays are harmless invalidations; coalescing retains newer revisions.
	job.retryAt = time.Now().Add(min(time.Second<<min(job.failures, 5), 30*time.Second))
	job.failures++
	_ = d.queue(job)
}
