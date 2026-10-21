package livekit

import (
	"reflect"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func TestTokenIssuerCreatesScopedParticipantToken(t *testing.T) {
	issuer, err := NewTokenIssuer(TokenOptions{APIKey: "key", APISecret: "secret", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	room := uuid.NewString()
	tokenString, err := issuer.IssueParticipantToken(
		now,
		ParticipantTokenRequest{
			RoomName:       room,
			Identity:       "user-1",
			Name:           "A",
			CanPublish:     true,
			PublishSources: []string{"microphone", "camera", "screen_share"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(tokenString, func(_ *jwt.Token) (any, error) { return []byte("secret"), nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("token valid=%v err=%v", parsed.Valid, err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if claims["iss"] != "key" || claims["sub"] != "user-1" {
		t.Fatalf("claims=%v", claims)
	}
	video := claims["video"].(map[string]any)
	if video["room"] != room || video["roomJoin"] != true || video["canPublish"] != true {
		t.Fatalf("video=%v", video)
	}
	if got, want := video["canPublishSources"], []any{
		"microphone",
		"camera",
		"screen_share",
	}; !reflect.DeepEqual(
		got,
		want,
	) {
		t.Fatalf("canPublishSources = %v, want %v", got, want)
	}
}

func TestTokenIssuerCreatesRoomAdminToken(t *testing.T) {
	t.Parallel()
	for _, room := range []string{"", "huddle-test"} {
		t.Run("room="+room, func(t *testing.T) {
			t.Parallel()
			issuer, err := NewTokenIssuer(
				TokenOptions{APIKey: "key", APISecret: "secret", TTL: time.Minute},
			)
			if err != nil {
				t.Fatal(err)
			}
			tokenString, err := issuer.IssueRoomAdminTokenForRoom(time.Now(), room)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := jwt.Parse(
				tokenString,
				func(*jwt.Token) (any, error) { return []byte("secret"), nil },
				jwt.WithValidMethods([]string{"HS256"}),
			)
			if err != nil {
				t.Fatal(err)
			}
			claims, ok := parsed.Claims.(jwt.MapClaims)
			if !ok || !parsed.Valid {
				t.Fatal("invalid admin token")
			}
			video, ok := claims["video"].(map[string]any)
			if !ok {
				t.Fatal("missing video grants")
			}
			want := map[string]any{"roomCreate": true, "roomList": true, "roomAdmin": true, "room": room}
			if !reflect.DeepEqual(video, want) {
				t.Fatalf("video = %v, want %v", video, want)
			}
		})
	}
}

func TestParticipantIdentityIsStableAndScoped(t *testing.T) {
	huddleID, userID := uuid.New(), uuid.New()
	if got := ParticipantIdentity(huddleID, userID); got != "huddle:"+huddleID.String()+":user:"+userID.String() {
		t.Fatalf("identity=%q", got)
	}
}
