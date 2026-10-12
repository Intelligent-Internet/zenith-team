package realtime

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"boardly.dev/boardly/server/pkg/protocol"
)

// CanvasPresence keeps short-lived pointer/camera sessions out of PostgreSQL.
// Redis shares sessions between replicas; the bounded map is for single-replica deployments.
type CanvasPresence struct {
	client *redis.Client
	mu     sync.Mutex
	rooms  map[string]map[string]CanvasSession
	// runs names the rooms each Agent Run joined, so its sessions leave when
	// the Run ends instead of lingering until they expire.
	runs map[uuid.UUID]map[string]bool
	// views keeps each person's last view per room after their session left.
	views map[string]canvasLastView
}

// CanvasRect is an area in Board coordinates.
type CanvasRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type canvasLastView struct {
	view CanvasRect
	at   int64
}

// canvasLastViewTTL is how long, in milliseconds, a person's last view stays
// known after their session left, e.g. while they read another tab.
const canvasLastViewTTL = 600000

type CanvasSession struct {
	Actor        protocol.ActorRef  `json:"actor"`
	RunID        *uuid.UUID         `json:"runId,omitempty"`
	SelectionIDs []uuid.UUID        `json:"selectionIds,omitempty"`
	DragPreviews []BoardDragPreview `json:"dragPreviews,omitempty"`
	Cursor       *DocumentCursor    `json:"cursor,omitempty"`
	SessionID    string             `json:"sessionId"`
	UserID       string             `json:"userId"`
	Name         string             `json:"name"`
	X            float64            `json:"x"`
	Y            float64            `json:"y"`
	CameraX      float64            `json:"cameraX"`
	CameraY      float64            `json:"cameraY"`
	Zoom         float64            `json:"zoom"`
	Pointing     bool               `json:"pointing"`
	Presenting   bool               `json:"presenting"`
	UpdatedAt    int64              `json:"updatedAt"`
	// View is the area of the Board a browser shows and Focused a browser
	// window with the keyboard focus; WorkArea is where an Agent is working.
	View     *CanvasRect `json:"view,omitempty"`
	WorkArea *CanvasRect `json:"workArea,omitempty"`
	Focused  bool        `json:"focused,omitempty"`
}

type BoardDragPreview struct {
	ID uuid.UUID `json:"id" binding:"required"`
	X  float64   `json:"x"  binding:"gte=-1000000,lte=1000000"`
	Y  float64   `json:"y"  binding:"gte=-1000000,lte=1000000"`
}

// DocumentCursor carries encoded Yjs relative positions, never document text.
type DocumentCursor struct {
	ClientID uint64 `json:"clientId"`
	Anchor   []byte `json:"anchor"`
	Head     []byte `json:"head"`
}

// ttl is how long a session stays without a heartbeat, in milliseconds. An
// idle browser heartbeats every five seconds; an Agent only between tool calls,
// and its cursor goes five seconds after its last one.
func (s CanvasSession) ttl() int64 {
	if s.RunID != nil {
		return 5000
	}
	return 15000
}

// ErrCanvasPresenceFull is a room already showing as many sessions as it can.
var ErrCanvasPresenceFull = errors.New("canvas presence room is full")

// Room bounds. One person's extra tabs replace their oldest session instead of
// crowding everyone else out of the room. An Agent working for them is not one
// of their tabs.
const (
	canvasPresenceRoomSessions    = 128
	canvasPresenceSessionsPerUser = 4
)

// canvasPresenceScript expires stale sessions, makes room for the caller's
// session within their own limit, stores it, remembers a person's view and
// answers the live sessions in one round trip. Agent sessions (with a Run)
// expire five seconds after their last call and stay outside the personal
// limit.
var canvasPresenceScript = redis.NewScript(fmt.Sprintf(`
local values = redis.call('HGETALL', KEYS[1])
local live = {}
local mine = {}
for i = 1, #values, 2 do
  local ok, v = pcall(cjson.decode, values[i + 1])
  local ttl = 15000
  if ok and type(v.runId) == 'string' then ttl = 5000 end
  if not ok or v.updatedAt < tonumber(ARGV[3]) - ttl then
    redis.call('HDEL', KEYS[1], values[i])
  else
    live[values[i]] = values[i + 1]
    if v.userId == ARGV[4] and values[i] ~= ARGV[1] and type(v.runId) ~= 'string' then
      table.insert(mine, {values[i], v.updatedAt})
    end
  end
end
if live[ARGV[1]] == nil then
  if ARGV[5] ~= '1' and #mine >= %d then
    table.sort(mine, function(a, b) return a[2] < b[2] end)
    for j = 1, #mine - %d + 1 do
      redis.call('HDEL', KEYS[1], mine[j][1])
      live[mine[j][1]] = nil
    end
  end
  local count = 0
  for _ in pairs(live) do count = count + 1 end
  if count >= %d then return false end
end
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('PEXPIRE', KEYS[1], 130000)
if ARGV[6] ~= '' then redis.call('SET', KEYS[2], ARGV[6], 'PX', %d) end
live[ARGV[1]] = ARGV[2]
local result = {}
for _, value in pairs(live) do table.insert(result, value) end
return result`,
	canvasPresenceSessionsPerUser, canvasPresenceSessionsPerUser, canvasPresenceRoomSessions, canvasLastViewTTL,
))

func NewCanvasPresence(client *redis.Client) *CanvasPresence {
	return &CanvasPresence{
		client: client,
		rooms:  map[string]map[string]CanvasSession{},
		runs:   map[uuid.UUID]map[string]bool{},
		views:  map[string]canvasLastView{},
	}
}

func (p *CanvasPresence) Update(
	ctx context.Context,
	room string,
	session CanvasSession,
	leave bool,
) ([]CanvasSession, error) {
	now := time.Now().UnixMilli()
	session.UpdatedAt = now
	field := session.UserID + ":" + session.SessionID
	if p.client != nil {
		key := "canvas:presence:" + room
		if leave {
			if err := p.client.HDel(ctx, key, field).Err(); err != nil {
				return nil, err
			}
			return p.Sessions(ctx, room)
		}
		raw, err := json.Marshal(session)
		if err != nil {
			return nil, err
		}
		agent, view := "0", []byte{}
		if session.RunID != nil {
			agent = "1"
		} else if session.View != nil {
			if view, err = json.Marshal(session.View); err != nil {
				return nil, err
			}
		}
		keys := []string{key, canvasLastViewKey(room, session.UserID)}
		values, err := canvasPresenceScript.Run(ctx, p.client, keys, field, raw, now, session.UserID, agent, view).
			StringSlice()
		if errors.Is(err, redis.Nil) {
			return nil, ErrCanvasPresenceFull
		}
		if err != nil {
			return nil, err
		}
		if session.RunID != nil {
			index := canvasPresenceRunKey(*session.RunID)
			pipe := p.client.TxPipeline()
			pipe.SAdd(ctx, index, room+"\n"+field)
			pipe.PExpire(ctx, index, 130*time.Second)
			if _, err = pipe.Exec(ctx); err != nil {
				return nil, err
			}
		}
		return canvasPresenceSessions(values, now), nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(now)
	if len(p.rooms) >= 256 && p.rooms[room] == nil {
		return nil, ErrCanvasPresenceFull
	}
	if p.rooms[room] == nil {
		p.rooms[room] = map[string]CanvasSession{}
	}
	items := p.rooms[room]
	if leave {
		delete(items, field)
	} else {
		if _, exists := items[field]; !exists {
			var mine []string
			for key, item := range items {
				if item.UserID == session.UserID && item.RunID == nil {
					mine = append(mine, key)
				}
			}
			slices.SortFunc(
				mine,
				func(a, b string) int { return cmp.Compare(items[a].UpdatedAt, items[b].UpdatedAt) },
			)
			for session.RunID == nil && len(mine) >= canvasPresenceSessionsPerUser {
				delete(items, mine[0])
				mine = mine[1:]
			}
			if len(items) >= canvasPresenceRoomSessions {
				return nil, ErrCanvasPresenceFull
			}
		}
		items[field] = session
		if session.RunID == nil && session.View != nil {
			p.views[room+"\n"+session.UserID] = canvasLastView{view: *session.View, at: now}
		}
		if session.RunID != nil {
			if p.runs[*session.RunID] == nil {
				p.runs[*session.RunID] = map[string]bool{}
			}
			p.runs[*session.RunID][room+"\n"+field] = true
		}
	}
	result := make([]CanvasSession, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result, nil
}

func canvasPresenceRunKey(run uuid.UUID) string {
	return "canvas:presence:run:" + run.String()
}

func canvasLastViewKey(room, userID string) string {
	return "canvas:presence:view:" + room + ":" + userID
}

// LastView answers the area a person last looked at in a room, for a while
// after their session left; nil when it is unknown or too old.
func (p *CanvasPresence) LastView(ctx context.Context, room, userID string) (*CanvasRect, error) {
	if p.client != nil {
		raw, err := p.client.Get(ctx, canvasLastViewKey(room, userID)).Bytes()
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var view CanvasRect
		if err = json.Unmarshal(raw, &view); err != nil {
			return nil, err
		}
		return &view, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(time.Now().UnixMilli())
	if last, ok := p.views[room+"\n"+userID]; ok {
		return &last.view, nil
	}
	return nil, nil
}

// RemoveRun takes an ended Run's sessions out of every room it joined.
func (p *CanvasPresence) RemoveRun(ctx context.Context, run uuid.UUID) error {
	if p.client != nil {
		index := canvasPresenceRunKey(run)
		entries, err := p.client.SMembers(ctx, index).Result()
		if err != nil {
			return err
		}
		pipe := p.client.TxPipeline()
		for _, entry := range entries {
			room, field, _ := strings.Cut(entry, "\n")
			pipe.HDel(ctx, "canvas:presence:"+room, field)
		}
		pipe.Del(ctx, index)
		_, err = pipe.Exec(ctx)
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for entry := range p.runs[run] {
		room, field, _ := strings.Cut(entry, "\n")
		delete(p.rooms[room], field)
	}
	delete(p.runs, run)
	return nil
}

// Sessions lists a room's live sessions without joining it.
func (p *CanvasPresence) Sessions(ctx context.Context, room string) ([]CanvasSession, error) {
	now := time.Now().UnixMilli()
	if p.client != nil {
		values, err := p.client.HVals(ctx, "canvas:presence:"+room).Result()
		if err != nil {
			return nil, err
		}
		return canvasPresenceSessions(values, now), nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expireLocked(now)
	result := make([]CanvasSession, 0, len(p.rooms[room]))
	for _, item := range p.rooms[room] {
		result = append(result, item)
	}
	return result, nil
}

func (p *CanvasPresence) expireLocked(now int64) {
	for key, items := range p.rooms {
		for id, item := range items {
			if item.UpdatedAt < now-item.ttl() {
				delete(items, id)
			}
		}
		if len(items) == 0 {
			delete(p.rooms, key)
		}
	}
	for run, entries := range p.runs {
		for entry := range entries {
			room, field, _ := strings.Cut(entry, "\n")
			if _, live := p.rooms[room][field]; !live {
				delete(entries, entry)
			}
		}
		if len(entries) == 0 {
			delete(p.runs, run)
		}
	}
	for key, last := range p.views {
		if last.at < now-canvasLastViewTTL {
			delete(p.views, key)
		}
	}
}

func canvasPresenceSessions(values []string, now int64) []CanvasSession {
	result := make([]CanvasSession, 0, len(values))
	for _, value := range values {
		var item CanvasSession
		if json.Unmarshal([]byte(value), &item) != nil || item.UpdatedAt < now-item.ttl() {
			continue
		}
		result = append(result, item)
	}
	return result
}
