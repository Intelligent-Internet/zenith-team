package social

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRefreshPreservesAndRotatesCredentials(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"x", "instagram", "linkedin"} {
		t.Run(platform, func(t *testing.T) {
			t.Parallel()
			client := NewClient(
				platform,
				Credentials{ClientID: "client", ClientSecret: "secret"},
				&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
					if platform == "instagram" {
						if r.URL.Query().Get("grant_type") != "ig_refresh_token" {
							t.Error("missing Instagram refresh grant")
						}
					} else {
						_ = r.ParseForm()
						if r.Form.Get("refresh_token") != "previous-refresh" ||
							r.Form.Get("grant_type") != "refresh_token" {
							t.Error("missing refresh grant")
						}
					}
					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(
							strings.NewReader(
								`{"access_token":"new-access","refresh_token":"rotated-refresh","expires_in":7200}`,
							),
						),
						Header: make(http.Header),
					}, nil
				})},
			)
			got, err := client.Refresh(
				context.Background(),
				Token{
					AccessToken:  "old-access",
					RefreshToken: "previous-refresh",
					Scopes:       "existing.scope",
				},
			)
			if err != nil || got.AccessToken != "new-access" || got.RefreshToken != "rotated-refresh" ||
				got.Scopes != "existing.scope" ||
				!got.ExpiresAt.After(time.Now()) {
				t.Fatalf("refresh = %+v, %v", got, err)
			}
		})
	}
}

func TestRefreshClassifiesProviderFailuresWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status    int
		body      string
		reconnect bool
	}{
		{400, `{"error":"invalid_grant","error_description":"private-token"}`, true},
		{403, `{"title":"Forbidden","detail":"This account cannot publish this content"}`, false},
		{429, `{"error":"rate_limited"}`, false},
		{503, `{"error":"private-token"}`, false},
	} {
		client := NewClient(
			"x",
			Credentials{},
			&http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: tc.status,
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Header:     make(http.Header),
				}, nil
			})},
		)
		_, err := client.Refresh(context.Background(), Token{RefreshToken: "refresh"})
		if err == nil || strings.Contains(err.Error(), "private-token") ||
			RequiresReconnect(err) != tc.reconnect {
			t.Fatalf("error = %v, reconnect = %v", err, RequiresReconnect(err))
		}
	}
}
