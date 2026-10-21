package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
	daemonpkg "github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

// The daemon's own session lives in daemon-auth.json beside the
// person's auth.json. The server accepts it only on the daemon plane and
// beside a live Run proof of the same person, so the daemon never runs on
// the person's session. These helpers mint it, check it and end it.

// ensureDaemonSession gives the daemon its own session before it starts.
// A daemon-auth.json the server still accepts is kept (and renewed when
// due). One that expired or that the server ended is deleted, and the
// person's auth.json (from `zen auth login`) mints a new one with
// POST /auth/daemon-session, stored 0600 under the file's lock. When the
// desktop app installed a new session in the meantime, that one is kept.
func ensureDaemonSession(ctx context.Context) error {
	daemonPath, err := daemonpkg.OwnCredentialPath()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	current, err := authfile.Load(daemonPath)
	ended := false
	switch {
	case err == nil && current.State(time.Now()) != authfile.StateExpired:
		_, err := authfile.Ensure(ctx, client, daemonPath, time.Now(), authfile.EnsureOptions{})
		if err == nil {
			return nil
		}
		if !errors.Is(err, authfile.ErrSessionRejected) {
			return fmt.Errorf("renew the daemon session: %w", err)
		}
		ended = true
	case err == nil:
		ended = true
	case !errors.Is(err, authfile.ErrNotFound):
		return fmt.Errorf("read the daemon session: %w", err)
	}
	if ended {
		removed, err := removeEndedDaemonSession(ctx, daemonPath, current.RefreshToken)
		if err != nil || !removed {
			return err
		}
	}
	if err := requireSession(); err != nil {
		return err
	}
	personPath, err := daemonpkg.CredentialPath()
	if err != nil {
		return err
	}
	person, err := authfile.Ensure(ctx, client, personPath, time.Now(), authfile.EnsureOptions{})
	if err != nil {
		return fmt.Errorf("Zenith Team session cannot be renewed (%w); run `zen auth login`", err)
	}
	minted, err := authfile.MintDaemonSession(ctx, client, person)
	if err != nil {
		return fmt.Errorf("could not start the daemon's own session: %w", err)
	}
	if err := authfile.WithLock(ctx, daemonPath, func() error {
		return authfile.Save(daemonPath, minted)
	}); err != nil {
		return fmt.Errorf("store the daemon session: %w", err)
	}
	return nil
}

// forgetDaemonSession ends the daemon's own session on the server and
// deletes daemon-auth.json. The file is always deleted; an error says the
// server could not be told, so the session may live on until it expires.
func forgetDaemonSession(ctx context.Context) error {
	path, err := daemonpkg.OwnCredentialPath()
	if err != nil {
		return err
	}
	credential, err := authfile.Load(path)
	if errors.Is(err, authfile.ErrNotFound) {
		return nil
	}
	var signOutErr error
	if err == nil {
		signOutErr = authfile.Logout(ctx, &http.Client{Timeout: 15 * time.Second}, credential)
	}
	if err := removeCredentialFile(ctx, path); err != nil {
		return err
	}
	if signOutErr != nil {
		return fmt.Errorf("the server could not be told to end the daemon session: %w", signOutErr)
	}
	return nil
}

// removeCredentialFile deletes a credential file under its lock; a missing
// file is already gone.
func removeCredentialFile(ctx context.Context, path string) error {
	if err := authfile.WithLock(ctx, path, func() error {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}); err != nil {
		return fmt.Errorf("delete %s: %w", path, err)
	}
	return nil
}

// removeEndedDaemonSession deletes daemon-auth.json under its lock only while
// it still holds the ended session's refresh token. False means another
// process (the desktop app installing a new session, or the daemon renewing
// it) replaced the file since it was read, so the new one stays.
func removeEndedDaemonSession(ctx context.Context, path, endedRefreshToken string) (bool, error) {
	removed := false
	if err := authfile.WithLock(ctx, path, func() error {
		current, err := authfile.Load(path)
		if errors.Is(err, authfile.ErrNotFound) {
			removed = true
			return nil
		}
		if err == nil && current.RefreshToken != endedRefreshToken {
			return nil
		}
		err = os.Remove(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		removed = true
		return nil
	}); err != nil {
		return false, fmt.Errorf("delete %s: %w", path, err)
	}
	return removed, nil
}

// forgetDaemonSessionOfAnotherPerson ends a daemon session that belongs to
// someone other than the person who just signed in, so the next
// `zen daemon start` mints one for the new person instead of running Agents
// for the old one. A session of the same person is kept.
func forgetDaemonSessionOfAnotherPerson(ctx context.Context, warn io.Writer, person authfile.Credential) {
	path, err := daemonpkg.OwnCredentialPath()
	if err != nil {
		return
	}
	current, err := authfile.Load(path)
	if errors.Is(err, authfile.ErrNotFound) {
		return
	}
	if err == nil && current.APIBaseURL == person.APIBaseURL &&
		accessTokenSubject(current.AccessToken) != "" &&
		accessTokenSubject(current.AccessToken) == accessTokenSubject(person.AccessToken) {
		return
	}
	if err := forgetDaemonSession(ctx); err != nil {
		fmt.Fprintf(warn, "warning: %v\n", err)
	}
}

// accessTokenSubject reads the person an access token names (its `sub`
// claim) without checking its signature: it only tells two local sessions
// apart, and the server checks every token it is sent.
func accessTokenSubject(token string) string {
	segments := strings.Split(token, ".")
	if len(segments) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Subject
}

// verifyDaemonSession proves the daemon's own session works before a
// restart touches the running daemon: it is present (minted if needed),
// renewable, and the server accepts it on the daemon plane
// (GET /daemon/config). GET /me would refuse it, because a daemon session
// is never its person.
func verifyDaemonSession(ctx context.Context) error {
	if err := ensureDaemonSession(ctx); err != nil {
		return err
	}
	path, err := daemonpkg.OwnCredentialPath()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	credential, err := authfile.Ensure(ctx, client, path, time.Now(), authfile.EnsureOptions{})
	if err != nil {
		return fmt.Errorf("the daemon session cannot be renewed (%w); run `zen auth login`", err)
	}
	endpoint, err := authfile.SessionURL(credential.APIBaseURL, "/daemon/config")
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("cannot reach the Zenith Team server at %s: %w", credential.APIBaseURL, err)
	}
	defer response.Body.Close()
	// Only the status matters; the drained bytes are discarded.
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return errors.New("the server rejected the daemon session (expired or revoked); run `zen auth login`")
	case response.StatusCode >= http.StatusBadRequest:
		return fmt.Errorf("the server answered %d to the daemon session check", response.StatusCode)
	}
	return nil
}
