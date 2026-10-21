// Package livekit contains the provider boundary for Huddle media. Domain
// services depend on the shape of a token request, not on LiveKit SDK types.
package livekit

import (
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type TokenOptions struct {
	APIKey    string
	APISecret string
	TTL       time.Duration
}

type ParticipantTokenRequest struct {
	RoomName       string
	Identity       string
	Name           string
	CanPublish     bool
	PublishSources []string
}

type TokenIssuer struct {
	apiKey    string
	apiSecret []byte
	ttl       time.Duration
}

func NewTokenIssuer(options TokenOptions) (*TokenIssuer, error) {
	if strings.TrimSpace(options.APIKey) == "" || strings.TrimSpace(options.APISecret) == "" {
		return nil, errors.New("LiveKit API credentials are required")
	}
	if options.TTL <= 0 || options.TTL > 24*time.Hour {
		return nil, errors.New("LiveKit token TTL must be between 1 second and 24 hours")
	}
	return &TokenIssuer{apiKey: options.APIKey, apiSecret: []byte(options.APISecret), ttl: options.TTL}, nil
}

func (i *TokenIssuer) IssueParticipantToken(now time.Time, request ParticipantTokenRequest) (string, error) {
	if i == nil || len(i.apiSecret) == 0 || strings.TrimSpace(request.RoomName) == "" ||
		strings.TrimSpace(request.Identity) == "" {
		return "", errors.New("LiveKit token request is invalid")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	video := map[string]any{
		"roomJoin":       true,
		"room":           request.RoomName,
		"canPublish":     request.CanPublish,
		"canSubscribe":   true,
		"canPublishData": true,
	}
	if len(request.PublishSources) > 0 {
		video["canPublishSources"] = request.PublishSources
	}
	claims := jwt.MapClaims{
		"iss":   i.apiKey,
		"sub":   request.Identity,
		"iat":   now.Unix(),
		"exp":   now.Add(i.ttl).Unix(),
		"video": video,
	}
	if request.Name != "" {
		claims["name"] = request.Name
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(i.apiSecret)
}

func (i *TokenIssuer) IssueRoomAdminToken(now time.Time) (string, error) {
	return i.IssueRoomAdminTokenForRoom(now, "")
}

func (i *TokenIssuer) IssueRoomAdminTokenForRoom(now time.Time, room string) (string, error) {
	if i == nil || len(i.apiSecret) == 0 {
		return "", errors.New("LiveKit token issuer is invalid")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	claims := jwt.MapClaims{
		"iss": i.apiKey, "sub": i.apiKey, "iat": now.Unix(), "exp": now.Add(i.ttl).Unix(),
		"video": map[string]any{"roomCreate": true, "roomList": true, "roomAdmin": true, "room": room},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.apiSecret)
}

func ParticipantIdentity(huddleID, userID uuid.UUID) string {
	return "huddle:" + huddleID.String() + ":user:" + userID.String()
}
