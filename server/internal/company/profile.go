package company

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/internal/util"
	generated "boardly.dev/boardly/server/pkg/db/generated"
	"boardly.dev/boardly/server/pkg/protocol"
)

type UpdateProfileInput struct {
	ExpectedRevision int64
	Name             string
	ContactEmail     *string
	PhoneCountryISO2 *string
	PhoneNumber      *string
	// Nil preserves the avatar; a null UUID removes it.
	AvatarAttachmentID *uuid.NullUUID
}

// ProfileRevisionConflictError reports a stale ExpectedRevision. It carries
// the current snapshot so the transport can hand the caller the state to
// rebase onto.
type ProfileRevisionConflictError struct {
	Current Response
}

func (e *ProfileRevisionConflictError) Error() string {
	return "company profile revision conflict"
}

type canonicalProfileInput struct {
	expectedRevision   int64
	name               string
	contactEmail       *string
	phoneCountryISO2   *string
	phoneNumber        *string
	avatarAttachmentID *uuid.NullUUID
}

func (s *DefaultCompanyService) UpdateProfile(
	ctx context.Context,
	principal auth.UserPrincipal,
	input UpdateProfileInput,
) (Response, error) {
	canonical, err := normalizeCompanyProfileInput(input)
	if err != nil {
		return Response{}, err
	}

	var snapshot Response
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(tx *generated.Queries) error {
		pending = pending[:0]
		if _, err := tx.LockCompanyForProfileUpdate(
			ctx, principal.CompanyID,
		); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("lock Company profile: %w", err)
		}
		if _, err := authorizeMembershipActor(
			ctx, tx, principal.CompanyID, principal.UserID, auth.PermissionCompanyProfileUpdate,
		); err != nil {
			return err
		}
		currentRow, err := companyViewRow(ctx, tx, principal.CompanyID, principal.UserID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("load Company profile: %w", err)
		}
		current := companyDTOFromGetRow(currentRow)
		// The revision gate runs before the no-change gate, deliberately: a
		// stale writer must hear about the conflict even when its payload
		// matches the current row.
		if current.Revision != canonical.expectedRevision {
			return &ProfileRevisionConflictError{Current: current}
		}
		if !companyProfileChanged(current, canonical) {
			snapshot = current
			return nil
		}
		avatar := util.NullUUIDFromPtr(current.AvatarAttachmentID)
		if canonical.avatarAttachmentID != nil && *canonical.avatarAttachmentID != avatar {
			avatar = *canonical.avatarAttachmentID
			if avatar.Valid {
				upload, err := tx.LockCompanyAvatarUpload(ctx, generated.LockCompanyAvatarUploadParams{
					CompanyID: principal.CompanyID, ActorUserID: principal.UserID, AttachmentID: avatar.UUID,
				})
				if errors.Is(err, pgx.ErrNoRows) {
					return ErrInvalidInput
				}
				if err != nil {
					return fmt.Errorf("lock company avatar upload: %w", err)
				}
				if !validProfileImage(upload.ContentType, upload.SizeBytes) {
					return fmt.Errorf("%w: avatar must be a PNG, JPEG or WebP image up to 5 MB", ErrInvalidInput)
				}
			}
		}
		if _, err := tx.UpdateCompanyProfile(ctx, generated.UpdateCompanyProfileParams{
			Name:               canonical.name,
			ContactEmail:       util.PtrToText(canonical.contactEmail),
			PhoneCountryIso2:   util.PtrToText(canonical.phoneCountryISO2),
			PhoneNumber:        util.PtrToText(canonical.phoneNumber),
			AvatarAttachmentID: avatar,
			ID:                 principal.CompanyID,
			ExpectedVersion:    canonical.expectedRevision,
		}); errors.Is(err, pgx.ErrNoRows) {
			return &ProfileRevisionConflictError{Current: current}
		} else if err != nil {
			return fmt.Errorf("update Company profile: %w", err)
		}
		updatedRow, err := companyViewRow(ctx, tx, principal.CompanyID, principal.UserID)
		if err != nil {
			return fmt.Errorf("load updated Company profile: %w", err)
		}
		snapshot = companyDTOFromGetRow(updatedRow)
		pending = append(pending, companyUpdatedEvent(principal.UserID.String(), snapshot))
		return nil
	})
	if err != nil {
		return Response{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return snapshot, nil
}

// companyUpdatedEvent stages company.updated. The Company this fact belongs
// to is the one it describes, so the projection's own id scopes the envelope.
// The frame reaches every member, so the actor's own role and permissions
// are stripped: those belong to each viewer, who keeps the ones their last
// Company read resolved, and only auth.company_access_changed moves them.
func companyUpdatedEvent(actorID string, value Response) events.Event {
	value.Role = ""
	value.Permissions = nil
	return events.Event{
		EntityType: Topic,
		Name:       EventCompanyUpdated,
		CompanyID:  value.ID,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.UserActor(actorID),
		Payload:    value,
	}
}

// normalizeCompanyProfileInput holds the rules the request binding cannot
// state. The revision is no longer one of them: `binding:"revision"` reads the
// same bound off protocol.MaxJSONSafeInteger, and reads it through the
// optional[int64] wrapper, so it is stated once at the boundary. Name and
// contact details arrive as json.RawMessage and are still this function's.
func normalizeCompanyProfileInput(input UpdateProfileInput) (canonicalProfileInput, error) {
	name := protocol.TrimContractWhitespace(input.Name)
	if name == "" || !utf8.ValidString(name) ||
		utf8.RuneCountInString(name) > 120 ||
		len(name) > 480 {
		return canonicalProfileInput{}, fmt.Errorf(
			"%w: Company name must contain 1–120 characters and at most 480 UTF-8 bytes",
			ErrInvalidInput,
		)
	}
	contactEmail, err := normalizeCompanyContactEmail(input.ContactEmail)
	if err != nil {
		return canonicalProfileInput{}, err
	}
	phoneCountryISO2, phoneNumber, err := protocol.NormalizePhone(
		input.PhoneCountryISO2, input.PhoneNumber,
	)
	if err != nil {
		return canonicalProfileInput{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return canonicalProfileInput{
		expectedRevision:   input.ExpectedRevision,
		name:               name,
		contactEmail:       contactEmail,
		phoneCountryISO2:   phoneCountryISO2,
		phoneNumber:        phoneNumber,
		avatarAttachmentID: input.AvatarAttachmentID,
	}, nil
}

func normalizeCompanyContactEmail(input *string) (*string, error) {
	if input == nil {
		return nil, nil
	}
	value := strings.ToLower(protocol.TrimContractWhitespace(*input))
	if value == "" {
		return nil, nil
	}
	if !utf8.ValidString(value) || len(value) > 320 {
		return nil, fmt.Errorf("%w: Company contact email is invalid", ErrInvalidInput)
	}
	parsed, err := mail.ParseAddress(value)
	at := strings.LastIndexByte(value, '@')
	if err != nil || parsed.Address != value || at <= 0 {
		return nil, fmt.Errorf("%w: Company contact email is invalid", ErrInvalidInput)
	}
	domain := value[at+1:]
	dot := strings.LastIndexByte(domain, '.')
	if dot <= 0 || dot == len(domain)-1 {
		return nil, fmt.Errorf("%w: Company contact email is invalid", ErrInvalidInput)
	}
	return &value, nil
}

func companyProfileChanged(current Response, target canonicalProfileInput) bool {
	return current.Name != target.name ||
		!equalOptionalText(current.ContactEmail, target.contactEmail) ||
		!equalOptionalText(current.PhoneCountryISO2, target.phoneCountryISO2) ||
		!equalOptionalText(current.PhoneNumber, target.phoneNumber) ||
		(target.avatarAttachmentID != nil && *target.avatarAttachmentID != util.NullUUIDFromPtr(current.AvatarAttachmentID))
}

func equalOptionalText(left, right *string) bool {
	return left == nil && right == nil ||
		left != nil && right != nil && *left == *right
}

func validProfileImage(contentType string, size int64) bool {
	return size > 0 && size <= 5<<20 &&
		(contentType == "image/png" || contentType == "image/jpeg" || contentType == "image/webp")
}
