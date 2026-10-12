package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
	"github.com/redis/go-redis/v9"
)

// Stream / registry key naming. Centralized so tests can introspect.

// ShardedStreamKey isolates UUID Run frames from retained numeric Run frames.
// The coordinated cutover reconnects clients and refetches durable state.
func ShardedStreamKey(shard int) string {
	return fmt.Sprintf("ws:relay:run-uuid-v1:shard:%d", shard)
}

func HeartbeatKey(nodeID string) string {
	return fmt.Sprintf("ws:node:%s:heartbeat", nodeID)
}

const (
	heartbeatTTL    = 90 * time.Second
	heartbeatPeriod = 30 * time.Second
)

// envelope is what we serialize into each XADD message. It is opaque to the
// hub: the relay decodes payloadJson before fanning out.
type envelope struct {
	EventID     string `json:"eventId"`
	EventType   string `json:"eventType"`
	Scope       string `json:"scope"`
	ScopeID     string `json:"scopeId"`
	CompanyID   string `json:"companyId"`
	ActorID     string `json:"actorId"`
	CreatedAt   string `json:"createdAt"`
	NodeID      string `json:"nodeId"`
	PayloadJSON string `json:"payloadJson"` // raw JSON of the original ws frame
}

func newEnvelope(nodeID, scopeType, scopeID, companyID string, frame []byte, id string) envelope {
	ev := envelope{
		EventID:     id,
		Scope:       scopeType,
		ScopeID:     scopeID,
		NodeID:      nodeID,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		PayloadJSON: string(frame),
	}
	if companyID != "" {
		ev.CompanyID = companyID
	}
	if t, a := peekTypeActor(frame); t != "" {
		ev.EventType = t
		ev.ActorID = a
	}
	return ev
}

func envelopeRedisValues(ev envelope) map[string]any {
	return map[string]any{
		"eventId":     ev.EventID,
		"eventType":   ev.EventType,
		"scope":       ev.Scope,
		"scopeId":     ev.ScopeID,
		"companyId":   ev.CompanyID,
		"actorId":     ev.ActorID,
		"createdAt":   ev.CreatedAt,
		"nodeId":      ev.NodeID,
		"payloadJson": ev.PayloadJSON,
	}
}

func envelopeFromXMessage(msg redis.XMessage) (envelope, bool) {
	ev := envelope{
		EventID:     redisString(msg.Values["eventId"]),
		EventType:   redisString(msg.Values["eventType"]),
		Scope:       redisString(msg.Values["scope"]),
		ScopeID:     redisString(msg.Values["scopeId"]),
		CompanyID:   redisString(msg.Values["companyId"]),
		ActorID:     redisString(msg.Values["actorId"]),
		CreatedAt:   redisString(msg.Values["createdAt"]),
		NodeID:      redisString(msg.Values["nodeId"]),
		PayloadJSON: redisString(msg.Values["payloadJson"]),
	}
	return ev, ev.PayloadJSON != ""
}

func redisString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return ""
	}
}

func deliverEnvelope(hub *Hub, daemonRuntime DaemonRuntimeDeliverer, ev envelope) {
	if ev.PayloadJSON == "" || ev.EventID == "" {
		return
	}
	// Daemon control frames have no eventId field in their schema: dedup rides
	// beside the frame as the envelope id, so the frame passes through
	// byte-for-byte. Only the browser plane gets the authoritative id injected.
	if ev.Scope == ScopeDaemonRuntime {
		if daemonRuntime != nil {
			daemonRuntime.DeliverDaemonRuntime(ev.ScopeID, []byte(ev.PayloadJSON), ev.EventID)
		}
		return
	}
	frame := injectEventID([]byte(ev.PayloadJSON), ev.EventID)
	switch ev.Scope {
	case ScopeDisconnectCompanyUser:
		if hub != nil {
			hub.disconnectCompanyUser(ev.CompanyID, ev.ScopeID, frame, ev.EventID)
		}
	case ScopeCompany:
		if hub != nil {
			hub.Deliver(audience{typ: audienceCompany, companyID: ev.ScopeID}, frame, ev.EventID)
		}
	case ScopeUser:
		if hub != nil {
			hub.Deliver(audience{typ: audienceUser, userID: ev.ScopeID}, frame, ev.EventID)
		}
	case ScopeCompanyUser:
		if hub != nil {
			hub.Deliver(
				audience{typ: audienceCompanyUser, companyID: ev.CompanyID, userID: ev.ScopeID},
				frame,
				ev.EventID,
			)
		}
	case ScopeCompanyOrUser:
		if hub != nil {
			hub.Deliver(
				audience{typ: audienceCompanyOrUser, companyID: ev.CompanyID, userID: ev.ScopeID},
				frame,
				ev.EventID,
			)
		}
	}
}

// peekTypeActor parses the WS frame just enough to lift eventType / actorId
// for the envelope. Failures yield empty strings — the envelope still works.
func peekTypeActor(frame []byte) (string, string) {
	var probe struct {
		Type    string `json:"type"`
		ActorID string `json:"actorId"`
	}
	_ = json.Unmarshal(frame, &probe)
	return probe.Type, probe.ActorID
}

// injectEventID makes the relay envelope's eventId authoritative in an
// existing JSON object frame. The frame must be a JSON object. Browser-plane
// frames only — daemon control frames pass through deliverEnvelope untouched.
func injectEventID(frame []byte, eventID string) []byte {
	if eventID == "" || len(frame) == 0 || frame[0] != '{' {
		return frame
	}
	var canonical struct {
		EventID string `json:"eventId"`
	}
	if err := json.Unmarshal(frame, &canonical); err != nil {
		return frame
	}
	if canonical.EventID == eventID {
		return frame
	}
	// Decode-encode round-trip is simplest and avoids edge cases with
	// trailing whitespace / nested escapes when the envelope must correct a
	// missing or conflicting frame id.
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(frame, &obj); err != nil {
		return frame
	}
	idJSON, _ := json.Marshal(eventID)
	obj["eventId"] = idJSON
	out, err := json.Marshal(obj)
	if err != nil {
		return frame
	}
	return out
}

// ShardedStreamRelayConfig controls the fixed-reader Redis Stream relay.
type ShardedStreamRelayConfig struct {
	Shards       int
	StreamMaxLen int64
	ReadCount    int64
	ReadBlock    time.Duration
	// ReplayGrace is the lookback window on startup: the shard reader starts
	// consuming from (now - ReplayGrace) rather than "$" so that any events
	// published while this pod was down are replayed. Events are bounded by
	// the stream's MAXLEN, and downstream consumers must be idempotent.
	ReplayGrace time.Duration
}

// DefaultShardedStreamRelayConfig returns production-safe defaults: a small
// fixed number of blocking readers per pod, bounded stream retention, and
// batched reads.
func DefaultShardedStreamRelayConfig() ShardedStreamRelayConfig {
	return ShardedStreamRelayConfig{
		Shards:       8,
		StreamMaxLen: 100000,
		ReadCount:    128,
		ReadBlock:    5 * time.Second,
		ReplayGrace:  5 * time.Minute,
	}
}

func (c ShardedStreamRelayConfig) withDefaults() ShardedStreamRelayConfig {
	def := DefaultShardedStreamRelayConfig()
	if c.Shards <= 0 {
		c.Shards = def.Shards
	}
	if c.StreamMaxLen <= 0 {
		c.StreamMaxLen = def.StreamMaxLen
	}
	if c.ReadCount <= 0 {
		c.ReadCount = def.ReadCount
	}
	if c.ReadBlock <= 0 {
		c.ReadBlock = def.ReadBlock
	}
	if c.ReplayGrace <= 0 {
		c.ReplayGrace = def.ReplayGrace
	}
	return c
}

// ShardedStreamRelay publishes all realtime events into a fixed set of Redis
// Streams. Every API node runs one XREAD BLOCK loop per shard and locally
// filters events through authenticated identity rooms. This keeps blocked
// Redis connections bounded by pod_count * shard_count.
type ShardedStreamRelay struct {
	hub      *Hub
	writeRDB *redis.Client
	readRDB  *redis.Client
	nodeID   string
	config   ShardedStreamRelayConfig

	mu       sync.Mutex
	stopping bool
	wg       sync.WaitGroup

	daemonRuntime DaemonRuntimeDeliverer
}

func NewShardedStreamRelay(
	hub *Hub,
	writeRDB, readRDB *redis.Client,
	config ShardedStreamRelayConfig,
) *ShardedStreamRelay {
	if readRDB == nil {
		readRDB = writeRDB
	}
	return &ShardedStreamRelay{
		hub:      hub,
		writeRDB: writeRDB,
		readRDB:  readRDB,
		nodeID:   ulid.Make().String(),
		config:   config.withDefaults(),
	}
}

func (r *ShardedStreamRelay) NodeID() string { return r.nodeID }

func (r *ShardedStreamRelay) SetDaemonRuntimeDeliverer(d DaemonRuntimeDeliverer) {
	r.daemonRuntime = d
}

func (r *ShardedStreamRelay) Start(ctx context.Context) {
	M.NodeID.Store(r.nodeID)
	if err := r.writeRDB.Ping(ctx).Err(); err != nil {
		slog.Error("realtime/sharded-redis: initial ping failed", "error", err)
		M.RedisConnected.Store(false)
		M.SetRedisLastError(err.Error())
	} else if r.readRDB != r.writeRDB {
		if err := r.readRDB.Ping(ctx).Err(); err != nil {
			slog.Error("realtime/sharded-redis: initial read-client ping failed", "error", err)
			M.RedisConnected.Store(false)
			M.SetRedisLastError(err.Error())
		} else {
			M.RedisConnected.Store(true)
		}
	} else {
		M.RedisConnected.Store(true)
	}

	r.wg.Add(1 + r.config.Shards)
	go func() {
		defer r.wg.Done()
		r.heartbeatLoop(ctx)
	}()
	for shard := range r.config.Shards {
		go func() {
			defer r.wg.Done()
			r.readShard(ctx, shard)
		}()
	}
}

func (r *ShardedStreamRelay) Stop() {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
}

func (r *ShardedStreamRelay) Wait() {
	r.wg.Wait()
}

func (r *ShardedStreamRelay) PublishWithID(
	scopeType, scopeID, companyID string,
	frame []byte,
	id string,
) error {
	if !validRelayRoute(scopeType, scopeID, companyID) {
		return errors.New("realtime relay route is invalid")
	}
	if len(frame) == 0 || id == "" {
		return errors.New("realtime relay frame and event id are required")
	}
	if r == nil || r.writeRDB == nil {
		return errors.New("realtime relay Redis client is required")
	}
	ev := newEnvelope(r.nodeID, scopeType, scopeID, companyID, frame, id)
	stream := ShardedStreamKey(r.shardFor(scopeType, scopeID))
	args := &redis.XAddArgs{
		Stream: stream,
		MaxLen: r.config.StreamMaxLen,
		Approx: true,
		Values: envelopeRedisValues(ev),
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.writeRDB.XAdd(ctx, args).Err(); err != nil {
		M.RedisXAddErrors.Add(1)
		M.SetRedisLastError(err.Error())
		slog.Warn(
			"realtime/sharded-redis: XADD failed",
			"error",
			err,
			"scope",
			scopeType,
			"scope_id",
			scopeID,
			"stream",
			stream,
		)
		return err
	}
	M.RedisXAddTotal.Add(1)
	M.RedisLastXAddLagMicros.Store(time.Since(start).Microseconds())
	return nil
}

func validRelayRoute(scopeType, scopeID, companyID string) bool {
	if scopeID == "" {
		return false
	}
	switch scopeType {
	case ScopeCompany, ScopeUser, ScopeDaemonRuntime:
		return companyID == ""
	case ScopeCompanyUser, ScopeCompanyOrUser, ScopeDisconnectCompanyUser:
		return companyID != ""
	default:
		return false
	}
}

func (r *ShardedStreamRelay) shardFor(scopeType, scopeID string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(scopeType))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(scopeID))
	return int(h.Sum32() % uint32(r.config.Shards))
}

// replayStartID returns a Redis stream ID anchored to (now - ReplayGrace) so
// that a freshly started shard reader replays only the recent grace window
// rather than the entire retained stream. The "-0" suffix matches any
// sequence number at that millisecond.
func (r *ShardedStreamRelay) replayStartID() string {
	ms := time.Now().Add(-r.config.ReplayGrace).UnixMilli()
	if ms < 0 {
		ms = 0
	}
	return fmt.Sprintf("%d-0", ms)
}

func (r *ShardedStreamRelay) readShard(ctx context.Context, shard int) {
	stream := ShardedStreamKey(shard)
	// Start from a bounded lookback window, not "$", so that events
	// published while this pod was down are replayed. The grace window is
	// short enough that replay volume stays manageable, and downstream
	// consumers (daemon wakeups, client reconnects) are idempotent.
	lastID := r.replayStartID()
	for {
		if ctx.Err() != nil || r.isStopping() {
			return
		}
		if !r.readShardOnce(ctx, shard, stream, &lastID) {
			return
		}
	}
}

// readShardOnce performs a single XREAD iteration for one shard. It returns
// true when the caller should continue reading, false when the context is
// done and the loop should exit. lastID is advanced past any messages read.
func (r *ShardedStreamRelay) readShardOnce(ctx context.Context, shard int, stream string, lastID *string) bool {
	readCtx, cancel := context.WithTimeout(ctx, r.config.ReadBlock+time.Second)
	res, err := r.readRDB.XRead(readCtx, &redis.XReadArgs{
		Streams: []string{stream, *lastID},
		Count:   r.config.ReadCount,
		Block:   r.config.ReadBlock,
	}).Result()
	cancel()

	if errors.Is(err, redis.Nil) ||
		(err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))) {
		return true
	}
	if err != nil {
		M.RedisXReadErrors.Add(1)
		M.SetRedisLastError(err.Error())
		slog.Warn("realtime/sharded-redis: XREAD failed", "error", err, "shard", shard, "stream", stream)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Second):
		}
		return true
	}

	for _, s := range res {
		for _, msg := range s.Messages {
			*lastID = msg.ID
			M.RedisXReadTotal.Add(1)
			r.deliverMessage(msg)
		}
	}
	return true
}

func (r *ShardedStreamRelay) deliverMessage(msg redis.XMessage) {
	ev, ok := envelopeFromXMessage(msg)
	if !ok || ev.Scope == "" || ev.ScopeID == "" {
		return
	}
	deliverEnvelope(r.hub, r.daemonRuntime, ev)
}

func (r *ShardedStreamRelay) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(heartbeatPeriod)
	defer t.Stop()
	for {
		r.heartbeatOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *ShardedStreamRelay) heartbeatOnce(ctx context.Context) {
	hbCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := r.writeRDB.Set(hbCtx, HeartbeatKey(r.nodeID), time.Now().UTC().Format(time.RFC3339Nano), heartbeatTTL).
		Err(); err != nil {
		M.RedisConnected.Store(false)
		M.SetRedisLastError(err.Error())
		return
	}
	M.RedisConnected.Store(true)
}

func (r *ShardedStreamRelay) isStopping() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopping
}

var _ RelayPublisher = (*ShardedStreamRelay)(nil)
