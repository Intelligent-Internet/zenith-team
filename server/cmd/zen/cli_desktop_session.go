package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

// desktopSession is the session as this command prints it. It never carries
// the refresh token: that token stays in daemon-auth.json, where only the
// daemon redeems it, so whoever reads the output cannot keep the session
// alive on their own.
type desktopSession struct {
	APIBaseURL       string     `json:"apiBaseUrl"`
	AccessToken      string     `json:"accessToken"`
	AccessExpiresAt  *time.Time `json:"accessExpiresAt"`
	RefreshExpiresAt *time.Time `json:"refreshExpiresAt"`
}

type desktopSessionInput struct {
	APIBaseURL       string  `json:"apiBaseUrl"`
	AccessToken      string  `json:"accessToken"`
	AccessExpiresAt  *string `json:"accessExpiresAt"`
	RefreshToken     string  `json:"refreshToken"`
	RefreshExpiresAt *string `json:"refreshExpiresAt"`
}

// newDesktopSessionCmd is the desktop app's door to the daemon's own
// session. The app keeps its own session in its own store; after
// sign-in it starts the daemon's session (POST /auth/daemon-session) and
// installs it here, and on sign-out it clears it. It is an operator
// command: inside a Run it refuses before it touches the file.
func newDesktopSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "desktop-session",
		Short:  "Manage the daemon session the desktop app gives its daemon",
		Hidden: true,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Install the daemon's own bearer session from JSON on stdin",
			Args:  cobra.NoArgs,
			RunE:  runDesktopSessionInstall,
		},
		&cobra.Command{
			Use:   "clear",
			Short: "Sign the daemon's session out and delete it",
			Args:  cobra.NoArgs,
			RunE:  runDesktopSessionClear,
		},
	)
	return operatorOnly(cmd)
}

func runDesktopSessionInstall(cmd *cobra.Command, _ []string) error {
	input, err := decodeDesktopSessionInput(cmd.InOrStdin())
	if err != nil {
		return err
	}
	credential, err := input.credential()
	if err != nil {
		return err
	}
	path, err := daemon.OwnCredentialPath()
	if err != nil {
		return err
	}
	if err := authfile.WithLock(cmd.Context(), path, func() error {
		return authfile.Save(path, credential)
	}); err != nil {
		return fmt.Errorf("install the daemon session: %w", err)
	}
	return writeDesktopSession(cmd, &credential)
}

// runDesktopSessionClear ends the daemon's session on the server and deletes
// daemon-auth.json. The file goes even when the server cannot be reached;
// the command then fails, so the app can say the sign-out did not finish.
func runDesktopSessionClear(cmd *cobra.Command, _ []string) error {
	if err := forgetDaemonSession(cmd.Context()); err != nil {
		return fmt.Errorf("clear the daemon session: %w", err)
	}
	return writeDesktopSession(cmd, nil)
}

func decodeDesktopSessionInput(reader io.Reader) (desktopSessionInput, error) {
	var input desktopSessionInput
	decoder := json.NewDecoder(io.LimitReader(reader, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return desktopSessionInput{}, fmt.Errorf("decode desktop session: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return desktopSessionInput{}, errors.New("decode desktop session: expected one JSON object")
	}
	return input, nil
}

func (input desktopSessionInput) credential() (authfile.Credential, error) {
	apiBaseURL := strings.TrimSpace(input.APIBaseURL)
	if _, err := authfile.SessionURL(apiBaseURL, "/daemon/config"); err != nil {
		return authfile.Credential{}, fmt.Errorf("desktop session API URL: %w", err)
	}
	accessExpiresAt, err := parseDesktopSessionTime(input.AccessExpiresAt)
	if err != nil {
		return authfile.Credential{}, fmt.Errorf("desktop session access expiry: %w", err)
	}
	refreshExpiresAt, err := parseDesktopSessionTime(input.RefreshExpiresAt)
	if err != nil {
		return authfile.Credential{}, fmt.Errorf("desktop session refresh expiry: %w", err)
	}
	return authfile.Credential{
		Version:          authfile.CurrentVersion,
		APIBaseURL:       apiBaseURL,
		AccessToken:      strings.TrimSpace(input.AccessToken),
		AccessExpiresAt:  accessExpiresAt,
		RefreshToken:     strings.TrimSpace(input.RefreshToken),
		RefreshExpiresAt: refreshExpiresAt,
	}, nil
}

func parseDesktopSessionTime(value *string) (*time.Time, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*value))
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func writeDesktopSession(cmd *cobra.Command, credential *authfile.Credential) error {
	response := struct {
		OK      bool            `json:"ok"`
		Session *desktopSession `json:"session"`
	}{OK: true}
	if credential != nil {
		response.Session = &desktopSession{
			APIBaseURL:       credential.APIBaseURL,
			AccessToken:      credential.AccessToken,
			AccessExpiresAt:  credential.AccessExpiresAt,
			RefreshExpiresAt: credential.RefreshExpiresAt,
		}
	}
	if err := json.NewEncoder(cmd.OutOrStdout()).Encode(response); err != nil {
		return fmt.Errorf("encode desktop session: %w", err)
	}
	return nil
}
