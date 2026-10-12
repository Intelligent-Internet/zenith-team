package livekit

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyWebhookBindsSignatureToBody(t *testing.T) {
	t.Parallel()
	body, _ := json.Marshal(WebhookEvent{Event: "participant_joined"})
	hash := sha256.Sum256(body)
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"iss": "key", "sha256": base64.StdEncoding.EncodeToString(hash[:]), "iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix()}).SignedString([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	event, err := VerifyWebhook(body, token, "key", "secret")
	if err != nil || event.Event != "participant_joined" {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	body[0] ^= 1
	if _, err := VerifyWebhook(body, token, "key", "secret"); err == nil {
		t.Fatal("tampered webhook accepted")
	}
}

func TestVerifyWebhookRejectsInvalidAuthorization(t *testing.T) {
	t.Parallel()
	body := []byte(`{"event":"participant_joined"}`)
	hash := sha256.Sum256(body)
	for _, tt := range []struct {
		name   string
		issuer string
		secret string
		expiry time.Time
		method jwt.SigningMethod
		prefix string
	}{
		{
			name: "wrong issuer", issuer: "other", secret: "secret",
			expiry: time.Now().Add(time.Minute), method: jwt.SigningMethodHS256,
		},
		{
			name: "wrong signature", issuer: "key", secret: "other",
			expiry: time.Now().Add(time.Minute), method: jwt.SigningMethodHS256,
		},
		{
			name: "expired", issuer: "key", secret: "secret",
			expiry: time.Now().Add(-time.Minute), method: jwt.SigningMethodHS256,
		},
		{
			name: "wrong algorithm", issuer: "key", secret: "secret",
			expiry: time.Now().Add(time.Minute), method: jwt.SigningMethodHS384,
		},
		{
			name: "unexpected scheme", issuer: "key", secret: "secret", prefix: "Basic ",
			expiry: time.Now().Add(time.Minute), method: jwt.SigningMethodHS256,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			token, err := jwt.NewWithClaims(tt.method, jwt.MapClaims{
				"iss": tt.issuer, "exp": tt.expiry.Unix(),
				"sha256": base64.StdEncoding.EncodeToString(hash[:]),
			}).SignedString([]byte(tt.secret))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := VerifyWebhook(body, tt.prefix+token, "key", "secret"); err == nil {
				t.Fatal("invalid webhook accepted")
			}
		})
	}
}
