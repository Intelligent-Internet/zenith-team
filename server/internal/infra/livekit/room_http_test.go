package livekit

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPRoomProviderCallsRoomService(t *testing.T) {
	issuer, err := NewTokenIssuer(TokenOptions{APIKey: "key", APISecret: "secret", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/twirp/livekit.RoomService/CreateRoom" || r.Header.Get("Authorization") == "" {
			t.Fatalf("request=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"name":"huddle-room"}`))
	}))
	defer server.Close()
	provider, err := NewHTTPRoomProvider(server.URL, issuer, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.EnsureRoom(
		context.Background(),
		Room{Name: "huddle-room", ParticipantLimit: 9},
	); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRoomProviderReadyRoomsBatchesListRooms(t *testing.T) {
	issuer, err := NewTokenIssuer(TokenOptions{APIKey: "key", APISecret: "secret", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var batches []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/twirp/livekit.RoomService/ListRooms" {
			t.Fatalf("request=%s", r.URL.Path)
		}
		var request struct {
			Names []string `json:"names"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		batches = append(batches, len(request.Names))
		var rooms []map[string]string
		for _, name := range request.Names {
			if name != "room-7" {
				rooms = append(rooms, map[string]string{"name": name})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"rooms": rooms})
	}))
	defer server.Close()
	provider, err := NewHTTPRoomProvider(server.URL, issuer, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, listRoomsBatch+5)
	for i := range names {
		names[i] = fmt.Sprintf("room-%d", i)
	}
	ready, err := provider.ReadyRooms(context.Background(), names)
	if err != nil {
		t.Fatal(err)
	}
	if len(batches) != 2 || batches[0] != listRoomsBatch || batches[1] != 5 {
		t.Fatalf("batches = %v, want [%d 5]", batches, listRoomsBatch)
	}
	if len(ready) != len(names)-1 || ready["room-7"] || !ready["room-0"] {
		t.Fatalf("ready = %d rooms, room-7=%v room-0=%v", len(ready), ready["room-7"], ready["room-0"])
	}
}

func TestHTTPRoomProviderRejectsProviderErrors(t *testing.T) {
	issuer, _ := NewTokenIssuer(TokenOptions{APIKey: "key", APISecret: "secret", TTL: time.Minute})
	server := httptest.NewServer(
		http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "bad room", http.StatusBadRequest) },
		),
	)
	defer server.Close()
	provider, _ := NewHTTPRoomProvider(server.URL, issuer, server.Client())
	err := provider.DeleteRoom(context.Background(), "room")
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("err=%v", err)
	}
}
