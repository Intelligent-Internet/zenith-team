package livekit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type HTTPRoomProvider struct {
	baseURL string
	wsURL   string
	issuer  *TokenIssuer
	client  *http.Client
}

type MediaToken struct {
	URL   string
	Token string
}

func NewHTTPRoomProvider(baseURL string, issuer *TokenIssuer, client *http.Client) (*HTTPRoomProvider, error) {
	if strings.TrimSpace(baseURL) == "" || issuer == nil {
		return nil, errors.New("LiveKit room provider configuration is required")
	}
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	trimmed := strings.TrimRight(baseURL, "/")
	wsURL := strings.Replace(trimmed, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	return &HTTPRoomProvider{baseURL: trimmed, wsURL: wsURL, issuer: issuer, client: client}, nil
}

func (p *HTTPRoomProvider) IssueParticipantToken(_ context.Context, roomName, identity string) (MediaToken, error) {
	token, err := p.issuer.IssueParticipantToken(time.Now().UTC(), ParticipantTokenRequest{
		RoomName: roomName, Identity: identity, CanPublish: true,
		PublishSources: []string{"microphone", "camera", "screen_share", "screen_share_audio"},
	})
	if err != nil {
		return MediaToken{}, err
	}
	return MediaToken{URL: p.wsURL, Token: token}, nil
}

func (p *HTTPRoomProvider) EnsureRoom(ctx context.Context, room Room) error {
	_, err := p.call(
		ctx,
		"CreateRoom",
		map[string]any{"name": room.Name, "max_participants": room.ParticipantLimit},
	)
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}

func (p *HTTPRoomProvider) DeleteRoom(ctx context.Context, name string) error {
	_, err := p.call(ctx, "DeleteRoom", map[string]any{"room": name})
	return err
}

func (p *HTTPRoomProvider) RoomReady(ctx context.Context, name string) (bool, error) {
	ready, err := p.ReadyRooms(ctx, []string{name})
	return ready[name], err
}

// listRoomsBatch bounds the names sent in one ListRooms request.
const listRoomsBatch = 100

// ReadyRooms reports which of the named rooms exist, with one ListRooms call
// per batch instead of one per room. Missing names are absent from the map.
func (p *HTTPRoomProvider) ReadyRooms(ctx context.Context, names []string) (map[string]bool, error) {
	ready := make(map[string]bool, len(names))
	for start := 0; start < len(names); start += listRoomsBatch {
		batch := names[start:min(start+listRoomsBatch, len(names))]
		body, err := p.call(ctx, "ListRooms", map[string]any{"names": batch})
		if err != nil {
			return nil, err
		}
		var response struct {
			Rooms []struct {
				Name string `json:"name"`
			} `json:"rooms"`
		}
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, err
		}
		for _, room := range response.Rooms {
			ready[room.Name] = true
		}
	}
	return ready, nil
}

func (p *HTTPRoomProvider) call(ctx context.Context, method string, payload any) ([]byte, error) {
	return p.callService(ctx, "RoomService", method, "", payload)
}

func (p *HTTPRoomProvider) callService(ctx context.Context, service, method, room string, payload any) ([]byte, error) {
	token, err := p.issuer.IssueRoomAdminTokenForRoom(time.Now().UTC(), room)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		p.baseURL+"/twirp/livekit."+service+"/"+method,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf(
			"LiveKit %s failed with status %d: %s",
			method,
			resp.StatusCode,
			strings.TrimSpace(string(responseBody)),
		)
	}
	return responseBody, nil
}
