package livekit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAgentDispatchTimeoutRetryObservesExistingJob(t *testing.T) {
	for _, field := range []string{"agentDispatches", "agent_dispatches"} {
		t.Run(field, func(t *testing.T) {
			id := uuid.New()
			creates := 0
			exists := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/twirp/livekit.AgentDispatchService/ListDispatch":
					rows := []map[string]string{}
					if exists {
						rows = append(rows, map[string]string{"id": "dispatch", "metadata": id.String()})
					}
					if err := json.NewEncoder(w).Encode(map[string]any{field: rows}); err != nil {
						t.Error(err)
					}
				case "/twirp/livekit.AgentDispatchService/CreateDispatch":
					creates++
					exists = true
					var payload map[string]string
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if payload["metadata"] != id.String() || payload["agentName"] != "zenith-helia" {
						t.Errorf("unexpected dispatch payload: %+v", payload)
					}
					http.Error(w, "ambiguous provider timeout", http.StatusGatewayTimeout)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			issuer, err := NewTokenIssuer(TokenOptions{APIKey: "key", APISecret: "secret", TTL: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			provider, err := NewHTTPRoomProvider(server.URL, issuer, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if err = provider.DispatchHuddleAI(context.Background(), "room", id); err == nil {
				t.Fatal("expected ambiguous error")
			}
			if err = provider.DispatchHuddleAI(context.Background(), "room", id); err != nil {
				t.Fatal(err)
			}
			if creates != 1 {
				t.Fatalf("created %d dispatches", creates)
			}
		})
	}
}

func TestRemoveHuddleAIDisconnectsOnlyItsParticipant(t *testing.T) {
	t.Parallel()
	for _, dispatchExists := range []bool{true, false} {
		name := "dispatch exists"
		if !dispatchExists {
			name = "dispatch already gone"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			id := uuid.New()
			identity := "helia:" + id.String()
			removed := false
			deleted := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var response any = map[string]any{}
				switch r.URL.Path {
				case "/twirp/livekit.RoomService/ListRooms":
					response = map[string]any{"rooms": []map[string]string{{"name": "room"}}}
				case "/twirp/livekit.AgentDispatchService/ListDispatch":
					rows := []map[string]string{{"id": "other", "metadata": uuid.NewString()}}
					if dispatchExists && !deleted {
						rows = append(rows, map[string]string{"id": "target", "metadata": id.String()})
					}
					response = map[string]any{"agentDispatches": rows}
				case "/twirp/livekit.AgentDispatchService/DeleteDispatch":
					var payload map[string]string
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if payload["dispatchId"] != "target" || payload["room"] != "room" {
						t.Errorf("unexpected delete: %v", payload)
					}
					deleted = true
				case "/twirp/livekit.RoomService/ListParticipants":
					rows := []map[string]string{{"identity": "human"}, {"identity": "helia:another-attempt"}}
					if !removed {
						rows = append(rows, map[string]string{"identity": identity})
					}
					response = map[string]any{"participants": rows}
				case "/twirp/livekit.RoomService/RemoveParticipant":
					var payload map[string]string
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Error(err)
					}
					if payload["identity"] != identity || payload["room"] != "room" || removed {
						t.Errorf("unexpected removal: %v, already removed: %v", payload, removed)
					}
					removed = true
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			issuer, err := NewTokenIssuer(TokenOptions{APIKey: "key", APISecret: "secret", TTL: time.Minute})
			if err != nil {
				t.Fatal(err)
			}
			provider, err := NewHTTPRoomProvider(server.URL, issuer, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := provider.RemoveHuddleAI(t.Context(), "room", id); err != nil {
					t.Fatal(err)
				}
			}
			if !removed {
				t.Fatal("agent participant still connected after removal")
			}
		})
	}
}
