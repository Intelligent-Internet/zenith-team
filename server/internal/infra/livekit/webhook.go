package livekit

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type WebhookEvent struct {
	Event string `json:"event"`
	Room  *struct {
		Name string `json:"name"`
	} `json:"room,omitempty"`
	Participant *struct {
		Identity string `json:"identity"`
	} `json:"participant,omitempty"`
}

// VerifyWebhook validates the LiveKit Authorization JWT and binds it to the
// exact raw request body. The caller must dispatch only the returned event.
func VerifyWebhook(rawBody []byte, authorization, apiKey, apiSecret string) (WebhookEvent, error) {
	if len(rawBody) == 0 || strings.TrimSpace(apiKey) == "" || strings.TrimSpace(apiSecret) == "" {
		return WebhookEvent{}, errors.New("LiveKit webhook input is invalid")
	}
	// LiveKit sends the signed JWT directly, without a Bearer scheme.
	token, err := jwt.Parse(authorization, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("LiveKit webhook signing method is invalid")
		}
		return []byte(apiSecret), nil
	}, jwt.WithIssuer(apiKey))
	if err != nil || !token.Valid {
		return WebhookEvent{}, errors.New("LiveKit webhook signature is invalid")
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return WebhookEvent{}, errors.New("LiveKit webhook claims are invalid")
	}
	expected := sha256.Sum256(rawBody)
	want := base64.StdEncoding.EncodeToString(expected[:])
	got, ok := claims["sha256"].(string)
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return WebhookEvent{}, errors.New("LiveKit webhook body hash is invalid")
	}
	var event WebhookEvent
	if err := json.Unmarshal(rawBody, &event); err != nil {
		return WebhookEvent{}, fmt.Errorf("decode LiveKit webhook: %w", err)
	}
	if event.Event == "" {
		return WebhookEvent{}, errors.New("LiveKit webhook event is missing")
	}
	return event, nil
}
