package daemon

import (
	"context"
	"net/http"
	"time"

	"boardly.dev/boardly/server/internal/authfile"
)

// Session supplies the bearer access token the daemon sends to the server.
//
// A daemon outlives its access token by orders of magnitude, so the token
// cannot be read once at startup: the source re-reads the stored credential
// and renews it before expiry. Renewal rotates the refresh token, so the
// authoritative copy is always the file on disk — never a value cached in
// memory here.
type Session interface {
	// AccessToken returns a token that is valid now, refreshing if needed.
	AccessToken(ctx context.Context) (string, error)
	// Renew forces a refresh after the server rejected rejected. It returns
	// the replacement token, which may be one another process already stored.
	Renew(ctx context.Context, rejected string) (string, error)
}

// fileSession reads and renews the credential written by `zen auth login`.
type fileSession struct {
	path string
	doer authfile.Doer
	now  func() time.Time
}

// NewFileSession returns a Session backed by the credential file at path.
func NewFileSession(path string) Session {
	return &fileSession{
		path: path,
		doer: &http.Client{Timeout: 30 * time.Second},
		now:  time.Now,
	}
}

func (s *fileSession) AccessToken(ctx context.Context) (string, error) {
	credential, err := authfile.Ensure(ctx, s.doer, s.path, s.now(), authfile.EnsureOptions{})
	if err != nil {
		return "", err
	}
	return credential.AccessToken, nil
}

func (s *fileSession) Renew(ctx context.Context, rejected string) (string, error) {
	credential, err := authfile.Ensure(ctx, s.doer, s.path, s.now(), authfile.EnsureOptions{
		Force:               true,
		RejectedAccessToken: rejected,
	})
	if err != nil {
		return "", err
	}
	return credential.AccessToken, nil
}

// staticSession carries a fixed token. Tests use it; production always reads
// the file so rotation is observed.
type staticSession string

func (s staticSession) AccessToken(context.Context) (string, error) { return string(s), nil }

func (s staticSession) Renew(context.Context, string) (string, error) { return string(s), nil }
