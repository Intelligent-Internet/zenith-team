package livekit

import (
	"context"
	"errors"
	"strings"
	"sync"
)

type Room struct {
	Name             string
	ParticipantLimit int
}

// RoomProvider is the narrow media boundary used by Huddle lifecycle code.
// The production implementation can be backed by LiveKit Cloud; tests use
// MemoryRoomProvider without opening sockets or requiring provider credentials.
type RoomProvider interface {
	EnsureRoom(context.Context, Room) error
	DeleteRoom(context.Context, string) error
	RoomReady(context.Context, string) (bool, error)
}

type MemoryRoomProvider struct {
	mu    sync.Mutex
	rooms map[string]Room
}

func NewMemoryRoomProvider() *MemoryRoomProvider {
	return &MemoryRoomProvider{rooms: make(map[string]Room)}
}

func (p *MemoryRoomProvider) EnsureRoom(_ context.Context, room Room) error {
	if p == nil || strings.TrimSpace(room.Name) == "" || room.ParticipantLimit < 1 {
		return errors.New("room is invalid")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.rooms[room.Name]; ok && existing.ParticipantLimit != room.ParticipantLimit {
		return errors.New("room already exists with different participant limit")
	}
	p.rooms[room.Name] = room
	return nil
}

func (p *MemoryRoomProvider) DeleteRoom(_ context.Context, name string) error {
	if p == nil {
		return errors.New("room provider is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.rooms, name)
	return nil
}

func (p *MemoryRoomProvider) RoomReady(_ context.Context, name string) (bool, error) {
	if p == nil {
		return false, errors.New("room provider is nil")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.rooms[name]
	return ok, nil
}

func (p *MemoryRoomProvider) IssueParticipantToken(_ context.Context, roomName, identity string) (MediaToken, error) {
	if p == nil || strings.TrimSpace(roomName) == "" || strings.TrimSpace(identity) == "" {
		return MediaToken{}, errors.New("media token request is invalid")
	}
	ready, _ := p.RoomReady(context.Background(), roomName)
	if !ready {
		return MediaToken{}, errors.New("room is not ready")
	}
	return MediaToken{URL: "ws://memory", Token: "memory:" + roomName + ":" + identity}, nil
}
