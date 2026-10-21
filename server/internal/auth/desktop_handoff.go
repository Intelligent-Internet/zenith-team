package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

// NewDesktopHandoffCode is the input to create a single-use, 60-second
// desktop login handoff code minted by the web app.
type NewDesktopHandoffCode struct {
	UserID    uuid.UUID
	CodeHash  []byte
	ExpiresAt time.Time
}

// DesktopHandoffCode is a stored desktop login handoff code row.
type DesktopHandoffCode struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	CodeHash  []byte
	ExpiresAt time.Time
	CreatedAt time.Time
}

// defaultDesktopHandoffTTL is the lifetime of a minted handoff code when
// DesktopHandoffOptions.TTL is left unset.
const defaultDesktopHandoffTTL = 60 * time.Second

type DesktopHandoffOptions struct {
	Store  *db.Store
	Tokens *TokenManager
	// TTL is the lifetime of a minted handoff code. Defaults to
	// defaultDesktopHandoffTTL (60s) when zero.
	TTL    time.Duration
	Now    func() time.Time
	Random io.Reader
}

// DesktopHandoffService mints and redeems the single-use, short-lived code a
// cookie-authenticated Web session uses to hand a login off to the desktop
// app: Start is called from the Web app, Complete is called unauthenticated
// by the desktop app once the user has approved the handoff in the browser.
type DesktopHandoffService interface {
	Start(context.Context, uuid.UUID) (DesktopHandoffStartResult, error)
	Complete(context.Context, DesktopHandoffCompleteInput) (Result, error)
}

var _ DesktopHandoffService = (*DefaultDesktopHandoffService)(nil)

type DefaultDesktopHandoffService struct {
	store  *db.Store
	tokens *TokenManager
	ttl    time.Duration
	now    func() time.Time
	random io.Reader
}

// DesktopHandoffStartResult is returned to the cookie-authenticated caller
// of Start. Code is the one-time value the desktop app must present to
// Complete; only its SHA-256 digest is ever persisted.
type DesktopHandoffStartResult struct {
	Code      string
	ExpiresAt time.Time
}

// DesktopHandoffCompleteInput is presented by the unauthenticated desktop
// app to redeem a handoff code for a bearer session. ClientMetadata reuses
// the OAuth client metadata allowlist so the desktop app cannot smuggle
// arbitrary JSON into the resulting session row.
type DesktopHandoffCompleteInput struct {
	Code           string
	ClientMetadata OAuthClientMetadata
}

func NewDesktopHandoffService(options DesktopHandoffOptions) (*DefaultDesktopHandoffService, error) {
	if options.Store == nil || options.Tokens == nil {
		return nil, errors.New("desktop handoff store and token manager are required")
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.TTL == 0 {
		options.TTL = defaultDesktopHandoffTTL
	}
	if options.TTL < 0 {
		return nil, errors.New("desktop handoff code TTL must be positive")
	}
	return &DefaultDesktopHandoffService{
		store:  options.Store,
		tokens: options.Tokens,
		ttl:    options.TTL,
		now:    options.Now,
		random: options.Random,
	}, nil
}

// Start mints a new single-use handoff code for userID, deleting any code
// the user already holds (createDesktopHandoffCode does this
// transactionally), and returns the raw code to hand to the desktop app out
// of band. Only the code's SHA-256 digest is persisted.
func (s *DefaultDesktopHandoffService) Start(ctx context.Context, userID uuid.UUID) (DesktopHandoffStartResult, error) {
	code, err := s.randomValue()
	if err != nil {
		return DesktopHandoffStartResult{}, fmt.Errorf("generate desktop handoff code: %w", err)
	}
	hash := sha256.Sum256([]byte(code))
	expiresAt := s.now().UTC().Add(s.ttl)
	if err := createDesktopHandoffCode(ctx, s.store, NewDesktopHandoffCode{
		UserID:    userID,
		CodeHash:  hash[:],
		ExpiresAt: expiresAt,
	}); err != nil {
		return DesktopHandoffStartResult{}, err
	}
	return DesktopHandoffStartResult{Code: code, ExpiresAt: expiresAt}, nil
}

// Complete redeems a handoff code for a bearer session, mirroring
// AuthService.Login's session-issuance shape (createOAuthSession, service.go's
// Login). The code is consumed unconditionally by the delete-returning query
// before expiry is checked here, so a code can never be redeemed twice even
// if it is already expired.
func (s *DefaultDesktopHandoffService) Complete(
	ctx context.Context,
	input DesktopHandoffCompleteInput,
) (Result, error) {
	if !validOAuthRandomValue(input.Code) {
		return Result{}, ErrDesktopHandoffCodeInvalid
	}
	if err := validateOAuthClientMetadata(ClientDesktop, input.ClientMetadata); err != nil {
		return Result{}, err
	}
	hash := sha256.Sum256([]byte(input.Code))
	// The delete is unconditional: it does not filter on expires_at, so a row
	// that comes back already expired is distinguishable from a code that was
	// never issued (ErrDesktopHandoffCodeInvalid).
	stored, err := s.store.Queries().ConsumeDesktopHandoffCode(ctx, hash[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Result{}, ErrDesktopHandoffCodeInvalid
		}
		return Result{}, fmt.Errorf("consume desktop handoff code: %w", err)
	}
	row := desktopHandoffCodeFromDB(stored)
	if !s.now().UTC().Before(row.ExpiresAt) {
		return Result{}, ErrDesktopHandoffCodeExpired
	}
	refresh, err := s.tokens.IssueRefresh()
	if err != nil {
		return Result{}, err
	}
	metadata, err := encodeClientMetadata(ClientDesktop, desktopHandoffClientMetadataDetails(input.ClientMetadata))
	if err != nil {
		return Result{}, err
	}
	user, sessionID, err := createSessionWithUserProjection(ctx, s.store, NewSession{
		UserID:         row.UserID,
		TokenPrefix:    refresh.Prefix,
		TokenHash:      refresh.Hash,
		ExpiresAt:      refresh.ExpiresAt,
		ClientMetadata: metadata,
	})
	if err != nil {
		return Result{}, err
	}
	issued, err := issueSessionTokens(s.tokens, user.ID, sessionID, refresh)
	if err != nil {
		return Result{}, err
	}
	return Result{User: user, Client: ClientDesktop, Tokens: issued}, nil
}

func (s *DefaultDesktopHandoffService) randomValue() (string, error) {
	return randomOpaqueValue(s.random)
}

// desktopHandoffClientMetadataDetails narrows an OAuthClientMetadata value
// down to its non-empty fields so it can be passed to encodeClientMetadata,
// producing the same {"client":"desktop","details":{...}} envelope
// Login/Signup/Refresh already persist for regular sessions.
func desktopHandoffClientMetadataDetails(metadata OAuthClientMetadata) map[string]any {
	details := make(map[string]any, 2)
	if metadata.AppVersion != "" {
		details["appVersion"] = metadata.AppVersion
	}
	if metadata.Platform != "" {
		details["platform"] = metadata.Platform
	}
	return details
}

// createDesktopHandoffCode stores a new desktop login handoff code for the
// user, first deleting any code the user already holds so at most one code
// is ever live per user.
func createDesktopHandoffCode(
	ctx context.Context,
	store *db.Store,
	newCode NewDesktopHandoffCode,
) error {
	err := store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if err := queries.DeleteDesktopHandoffCodesForUser(ctx, newCode.UserID); err != nil {
			return err
		}
		return queries.CreateDesktopHandoffCode(ctx, generated.CreateDesktopHandoffCodeParams{
			UserID:    newCode.UserID,
			CodeHash:  newCode.CodeHash,
			ExpiresAt: pgtype.Timestamptz{Time: newCode.ExpiresAt, Valid: true},
		})
	})
	if err != nil {
		return fmt.Errorf("create desktop handoff code: %w", err)
	}
	return nil
}

func desktopHandoffCodeFromDB(row generated.DesktopHandoffCode) DesktopHandoffCode {
	return DesktopHandoffCode{
		ID:        row.ID,
		UserID:    row.UserID,
		CodeHash:  append([]byte(nil), row.CodeHash...),
		ExpiresAt: row.ExpiresAt.Time,
		CreatedAt: row.CreatedAt.Time,
	}
}
