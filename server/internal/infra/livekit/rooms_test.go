package livekit

import (
	"context"
	"testing"
)

func TestMemoryRoomProviderIsIdempotent(t *testing.T) {
	p := NewMemoryRoomProvider()
	room := Room{Name: "huddle-room", ParticipantLimit: 9}
	if err := p.EnsureRoom(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	if err := p.EnsureRoom(context.Background(), room); err != nil {
		t.Fatal(err)
	}
	ready, err := p.RoomReady(context.Background(), room.Name)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	if err := p.DeleteRoom(context.Background(), room.Name); err != nil {
		t.Fatal(err)
	}
	ready, err = p.RoomReady(context.Background(), room.Name)
	if err != nil || ready {
		t.Fatalf("after delete ready=%v err=%v", ready, err)
	}
}
