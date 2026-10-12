package authfile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/util"
)

const (
	// CurrentVersion is the credential file format version. Load and Save
	// refuse anything else so a downgraded binary never rewrites a newer file.
	CurrentVersion = 1
)

var ErrNotFound = errors.New("Zenith Team auth file was not found")

type State string

const (
	StateAuthenticated   State = "authenticated"
	StateRefreshRequired State = "refresh_required"
	StateExpired         State = "expired"
)

type Credential struct {
	Version           int
	APIBaseURL        string
	AccessToken       string
	AccessExpiresAt   *time.Time
	RefreshToken      string
	RefreshExpiresAt  *time.Time
	PendingRotationID *uuid.UUID
}

type credentialJSON struct {
	Version           int     `json:"version"`
	APIBaseURL        string  `json:"apiBaseUrl"`
	AccessToken       string  `json:"accessToken"`
	AccessExpiresAt   *string `json:"accessExpiresAt"`
	RefreshToken      string  `json:"refreshToken"`
	RefreshExpiresAt  *string `json:"refreshExpiresAt"`
	PendingRotationID *string `json:"pendingRotationId,omitempty"`
}

type lockFile struct {
	ID  string `json:"id"`
	PID int    `json:"pid"`
}

func Load(path string) (Credential, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, fmt.Errorf("open Zenith Team auth file: %w", err)
	}
	defer file.Close()

	if err := validateFilePermissions(file); err != nil {
		return Credential{}, err
	}

	var raw credentialJSON
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	if err := decoder.Decode(&raw); err != nil {
		return Credential{}, fmt.Errorf("decode Zenith Team auth file: %w", err)
	}
	if raw.Version != CurrentVersion {
		return Credential{}, fmt.Errorf("unsupported Zenith Team auth file version %d", raw.Version)
	}

	credential := Credential{
		Version:      raw.Version,
		APIBaseURL:   strings.TrimSpace(raw.APIBaseURL),
		AccessToken:  strings.TrimSpace(raw.AccessToken),
		RefreshToken: strings.TrimSpace(raw.RefreshToken),
	}
	if credential.AccessToken == "" {
		return Credential{}, errors.New("Zenith Team auth file accessToken is missing")
	}
	if credential.RefreshToken == "" {
		return Credential{}, errors.New("Zenith Team auth file refreshToken is missing")
	}

	credential.AccessExpiresAt, err = parseOptionalTimestamp(raw.AccessExpiresAt)
	if err != nil {
		return Credential{}, fmt.Errorf("Zenith Team auth file accessExpiresAt: %w", err)
	}
	credential.RefreshExpiresAt, err = parseOptionalTimestamp(raw.RefreshExpiresAt)
	if err != nil {
		return Credential{}, fmt.Errorf("Zenith Team auth file refreshExpiresAt: %w", err)
	}
	if raw.PendingRotationID != nil {
		rotationID, err := util.ParseGoUUID(strings.TrimSpace(*raw.PendingRotationID))
		if err != nil {
			return Credential{}, errors.New("Zenith Team auth file pendingRotationId is invalid")
		}
		credential.PendingRotationID = &rotationID
	}
	return credential, nil
}

func Save(path string, credential Credential) error {
	if strings.TrimSpace(credential.AccessToken) == "" {
		return errors.New("Zenith Team auth file accessToken is missing")
	}
	if strings.TrimSpace(credential.RefreshToken) == "" {
		return errors.New("Zenith Team auth file refreshToken is missing")
	}
	if credential.Version == 0 {
		credential.Version = CurrentVersion
	}
	if credential.Version != CurrentVersion {
		return fmt.Errorf("unsupported Zenith Team auth file version %d", credential.Version)
	}
	if credential.PendingRotationID != nil && !util.ValidUUID(*credential.PendingRotationID) {
		return errors.New("Zenith Team auth file pendingRotationId is invalid")
	}

	directory := filepath.Dir(path)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	raw := credentialJSON{
		Version:          CurrentVersion,
		APIBaseURL:       strings.TrimSpace(credential.APIBaseURL),
		AccessToken:      strings.TrimSpace(credential.AccessToken),
		AccessExpiresAt:  formatOptionalTimestamp(credential.AccessExpiresAt),
		RefreshToken:     strings.TrimSpace(credential.RefreshToken),
		RefreshExpiresAt: formatOptionalTimestamp(credential.RefreshExpiresAt),
	}
	if credential.PendingRotationID != nil {
		rotationID := credential.PendingRotationID.String()
		raw.PendingRotationID = &rotationID
	}
	encoded, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Zenith Team auth file: %w", err)
	}
	encoded = append(encoded, '\n')
	temporaryPath := fmt.Sprintf("%s.%d.%s.tmp", path, os.Getpid(), randomID())
	if err := os.WriteFile(temporaryPath, encoded, 0o600); err != nil {
		return fmt.Errorf("write temporary Zenith Team auth file: %w", err)
	}
	defer os.Remove(temporaryPath)
	if runtime.GOOS != "windows" {
		if err := os.Chmod(temporaryPath, 0o600); err != nil {
			return fmt.Errorf("secure temporary Zenith Team auth file: %w", err)
		}
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace Zenith Team auth file: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o600); err != nil {
			return fmt.Errorf("secure Zenith Team auth file: %w", err)
		}
	}
	return nil
}

// WithLock runs operation under the cross-process lock of one credential
// file. Each file has its own lock beside it, named after the file:
// auth.json takes auth.lock, as every earlier binary does, and the daemon's
// daemon-auth.json takes daemon-auth.lock, so renewing one never waits on
// the other.
func WithLock(ctx context.Context, authPath string, operation func() error) error {
	if operation == nil {
		return errors.New("Zenith Team auth lock operation is required")
	}
	directory := filepath.Dir(authPath)
	if err := ensurePrivateDirectory(directory); err != nil {
		return err
	}
	lockPath := filepath.Join(directory, strings.TrimSuffix(filepath.Base(authPath), ".json")+".lock")
	owner := lockFile{ID: randomID(), PID: os.Getpid()}
	deadline := time.Now().Add(10 * time.Second)

	for {
		acquired, err := acquireLock(lockPath, owner)
		if err != nil {
			return err
		}
		if acquired {
			break
		}
		if err := removeStaleLock(lockPath); err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return errors.New("timed out waiting for the Zenith Team auth lock")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	defer releaseOwnedLock(lockPath, owner.ID)
	return operation()
}

// State reports the credential at exact expiry boundaries. It deliberately
// carries no early-renewal skew: State answers "what is stored", while
// NeedsRefresh answers "should a caller renew before using it" and renews 30s
// ahead. A credential can therefore read StateAuthenticated in its final 30s
// while the next request still triggers a refresh.
func (c Credential) State(now time.Time) State {
	if c.RefreshExpiresAt != nil && !now.Before(*c.RefreshExpiresAt) {
		return StateExpired
	}
	if c.AccessExpiresAt != nil && !now.Before(*c.AccessExpiresAt) {
		return StateRefreshRequired
	}
	return StateAuthenticated
}

func ensurePrivateDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create Zenith Team auth directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("secure Zenith Team auth directory: %w", err)
		}
	}
	return nil
}

func validateFilePermissions(file *os.File) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat Zenith Team auth file: %w", err)
	}
	if permissions := info.Mode().Perm(); permissions&0o077 != 0 {
		return fmt.Errorf(
			"Zenith Team auth file permissions %04o are too open; run chmod 600 %q",
			permissions,
			file.Name(),
		)
	}
	return nil
}

func parseOptionalTimestamp(value *string) (*time.Time, error) {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*value))
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func formatOptionalTimestamp(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func acquireLock(path string, owner lockFile) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("create Zenith Team auth lock: %w", err)
	}
	encodeErr := json.NewEncoder(file).Encode(owner)
	closeErr := file.Close()
	if encodeErr != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("write Zenith Team auth lock: %w", encodeErr)
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return false, fmt.Errorf("close Zenith Team auth lock: %w", closeErr)
	}
	return true, nil
}

func removeStaleLock(path string) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat Zenith Team auth lock: %w", err)
	}
	if time.Since(info.ModTime()) > 2*time.Minute {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale Zenith Team auth lock: %w", err)
		}
	}
	return nil
}

func releaseOwnedLock(path, ownerID string) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var owner lockFile
	if json.Unmarshal(contents, &owner) == nil && owner.ID == ownerID {
		_ = os.Remove(path)
	}
}

func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
