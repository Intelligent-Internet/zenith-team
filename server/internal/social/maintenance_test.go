package social

import (
	"errors"
	"testing"

	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

func TestTokenWithoutConfiguredEncryptionKey(t *testing.T) {
	t.Parallel()
	service := new(DefaultSocialService)
	if _, err := service.token(generated.SocialConnection{}); !errors.Is(err, ErrReconnect) {
		t.Fatalf("missing key = %v, want reconnect instead of panic", err)
	}
}
