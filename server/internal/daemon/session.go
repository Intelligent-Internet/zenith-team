package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
)

// ErrSignedOut is what every request gets once Zenith Team has ended the
// daemon's session: a refresh answered 401, or the refresh token expired.
// Retrying cannot cure it, only a new daemon-auth.json can, so the session
// sends nothing more until the file is replaced.
var ErrSignedOut = errors.New("the daemon is signed out: its Zenith Team session ended; " +
	"sign in to the desktop app or run `zen daemon start`")

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
	// SignedOut reports that the server ended the session and no new one
	// has been stored since. The background loops pause on it.
	SignedOut() bool
}

// fileSession reads and renews the credential written by `zen auth login`.
type fileSession struct {
	path   string
	doer   authfile.Doer
	now    func() time.Time
	logger *slog.Logger

	// mu guards signedOut and ended: the refresh token of the session the
	// server ended. While the file still holds it the session is signed
	// out; any other token is a new sign-in.
	mu        sync.Mutex
	signedOut bool
	ended     string
}

// NewFileSession returns a Session backed by the credential file at path.
// Its refreshes carry client's identity headers like every other daemon
// request, and logger records when the session ends and when a new one
// replaces it.
func NewFileSession(path string, client *Client, logger *slog.Logger) Session {
	return &fileSession{
		path:   path,
		doer:   identifiedDoer{client: client, http: &http.Client{Timeout: 30 * time.Second}},
		now:    time.Now,
		logger: logger,
	}
}

func (s *fileSession) AccessToken(ctx context.Context) (string, error) {
	return s.ensure(ctx, authfile.EnsureOptions{})
}

func (s *fileSession) Renew(ctx context.Context, rejected string) (string, error) {
	return s.ensure(ctx, authfile.EnsureOptions{Force: true, RejectedAccessToken: rejected})
}

func (s *fileSession) ensure(ctx context.Context, options authfile.EnsureOptions) (string, error) {
	if s.SignedOut() {
		return "", ErrSignedOut
	}
	// Read before Ensure so a refusal is pinned to the session that was
	// refused: if the desktop app stores a new one meanwhile, the next call
	// sees a different token and tries it instead of staying signed out.
	stored, err := authfile.Load(s.path)
	if err != nil {
		return "", err
	}
	credential, err := authfile.Ensure(ctx, s.doer, s.path, s.now(), options)
	if errors.Is(err, authfile.ErrSessionRejected) || errors.Is(err, authfile.ErrSessionExpired) {
		s.signOut(stored.RefreshToken, err)
		return "", fmt.Errorf("%w: %w", ErrSignedOut, err)
	}
	if err != nil {
		return "", err
	}
	return credential.AccessToken, nil
}

func (s *fileSession) signOut(refreshToken string, cause error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signedOut && s.ended == refreshToken {
		return
	}
	s.signedOut, s.ended = true, refreshToken
	s.logger.Error("daemon signed out: Zenith Team ended its session, so it stops calling the "+
		"server until a new session is stored; sign in to the desktop app or run `zen daemon start`",
		"error", cause)
}

// SignedOut rereads the file while signed out, so storing a new session
// is all it takes to resume.
func (s *fileSession) SignedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.signedOut {
		return false
	}
	stored, err := authfile.Load(s.path)
	if err != nil || stored.RefreshToken == s.ended {
		return true
	}
	s.signedOut, s.ended = false, ""
	s.logger.Info("daemon session replaced; resuming")
	return false
}

// identifiedDoer sends the session's own refresh with the daemon's client
// metadata and daemon id, so the server's request log can name the daemon
// behind a refused refresh as it does for every other daemon request.
type identifiedDoer struct {
	client *Client
	http   *http.Client
}

func (d identifiedDoer) Do(request *http.Request) (*http.Response, error) {
	d.client.setIdentityHeaders(request)
	return d.http.Do(request)
}

// staticSession carries a fixed token. Tests use it; production always reads
// the file so rotation is observed.
type staticSession string

func (s staticSession) AccessToken(context.Context) (string, error) { return string(s), nil }

func (s staticSession) Renew(context.Context, string) (string, error) { return string(s), nil }

func (staticSession) SignedOut() bool { return false }
