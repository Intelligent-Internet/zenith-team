package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

type UpdateProfileInput struct {
	ExpectedRevision int64
	DisplayName      string
	Biography        string
	PhoneCountryISO2 *string
	PhoneNumber      *string
	AvatarDataURL    *string
}

// ProfileRevisionConflictError rejects a profile write whose expectedRevision
// is stale. Current carries the committed snapshot so the boundary can return
// it alongside the conflict.
type ProfileRevisionConflictError struct {
	Current User
}

func (e *ProfileRevisionConflictError) Error() string { return "profile revision conflict" }

type canonicalProfileInput struct {
	expectedRevision int64
	displayName      string
	biography        string
	phoneCountryISO2 *string
	phoneNumber      *string
	avatarDataURL    *string
}

func (s *DefaultAuthService) UpdateProfile(
	ctx context.Context,
	userID uuid.UUID,
	input UpdateProfileInput,
) (User, error) {
	canonical, err := normalizeProfileInput(input)
	if err != nil {
		return User{}, err
	}
	if s == nil || s.store == nil || userID == uuid.Nil {
		return User{}, errors.New("profile service is not initialized")
	}

	var user User
	var pending []events.Event
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		// conflictError carries the same projection the success paths return;
		// the transaction holds no writes when it aborts on this error.
		conflictError := func() error {
			current, err := getUserByID(ctx, queries, userID)
			if err != nil {
				return err
			}
			return &ProfileRevisionConflictError{Current: current}
		}
		stored, err := queries.LockUserForProfileUpdate(ctx, userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock User profile: %w", err)
		}
		current := userFromDB(stored)
		if current.DisabledAt != nil || current.DeletedAt != nil {
			return ErrAccountDisabled
		}

		// expectedRevision is checked first, on every call: a stale revision
		// is a conflict even when the submitted values match what is stored,
		// so a stale writer always hears about it. A same-revision no-change
		// write returns the current User and stages no event.
		if current.Version != canonical.expectedRevision {
			return conflictError()
		}
		if !profileChanged(current, canonical) {
			user, err = getUserByID(ctx, queries, userID)
			return err
		}

		avatarURL := current.AvatarURL
		if canonical.avatarDataURL != nil {
			avatarURL = canonical.avatarDataURL
		}
		_, err = queries.UpdateUserProfile(ctx, generated.UpdateUserProfileParams{
			DisplayName:      canonical.displayName,
			Biography:        canonical.biography,
			PhoneCountryIso2: util.PtrToText(canonical.phoneCountryISO2),
			PhoneNumber:      util.PtrToText(canonical.phoneNumber),
			AvatarUrl:        util.PtrToText(avatarURL),
			ID:               userID,
			ExpectedVersion:  canonical.expectedRevision,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return conflictError()
		}
		if err != nil {
			return fmt.Errorf("update User profile: %w", err)
		}
		user, err = getUserByID(ctx, queries, userID)
		if err != nil {
			return err
		}
		pending = append(pending, profileUpdatedEvent(user))
		return nil
	})
	if err != nil {
		return User{}, err
	}
	for _, group := range events.GroupByTopic(pending) {
		s.bus.Publish(ctx, group.Topic, group.Events...)
	}
	return user, nil
}

func normalizeProfileInput(input UpdateProfileInput) (canonicalProfileInput, error) {
	if input.AvatarDataURL != nil {
		if err := validateProfileAvatarDataURL(*input.AvatarDataURL); err != nil {
			return canonicalProfileInput{}, err
		}
	}
	displayName := protocol.TrimContractWhitespace(input.DisplayName)
	if !utf8.ValidString(displayName) || displayName == "" ||
		utf8.RuneCountInString(displayName) > 120 ||
		len(displayName) > 480 {
		return canonicalProfileInput{}, fmt.Errorf(
			"%w: display name must contain 1–120 characters and at most 480 UTF-8 bytes",
			ErrInvalidInput,
		)
	}
	biography := strings.ReplaceAll(input.Biography, "\r\n", "\n")
	biography = strings.ReplaceAll(biography, "\r", "\n")
	biography = protocol.TrimContractWhitespace(biography)
	if !utf8.ValidString(biography) || strings.ContainsRune(biography, '\x00') ||
		utf8.RuneCountInString(biography) > 1200 ||
		len(biography) > 4800 ||
		(biography != "" && strings.Count(biography, "\n")+1 > 12) {
		return canonicalProfileInput{}, fmt.Errorf(
			"%w: biography must contain at most 1,200 characters, 4,800 UTF-8 bytes, and 12 lines without NUL",
			ErrInvalidInput,
		)
	}
	phoneCountryISO2, phoneNumber, err := normalizeProfilePhone(
		input.PhoneCountryISO2,
		input.PhoneNumber,
	)
	if err != nil {
		return canonicalProfileInput{}, err
	}
	return canonicalProfileInput{
		expectedRevision: input.ExpectedRevision,
		displayName:      displayName,
		biography:        biography,
		phoneCountryISO2: phoneCountryISO2,
		phoneNumber:      phoneNumber,
		avatarDataURL:    input.AvatarDataURL,
	}, nil
}

func normalizeProfilePhone(countryInput, numberInput *string) (*string, *string, error) {
	country, number, err := protocol.NormalizePhone(countryInput, numberInput)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	return country, number, nil
}

func profileChanged(current User, target canonicalProfileInput) bool {
	return current.DisplayName != target.displayName ||
		current.Biography != target.biography ||
		!equalOptionalProfileText(current.PhoneCountryISO2, target.phoneCountryISO2) ||
		!equalOptionalProfileText(current.PhoneNumber, target.phoneNumber) ||
		(target.avatarDataURL != nil && !equalOptionalProfileText(current.AvatarURL, target.avatarDataURL))
}

func equalOptionalProfileText(left, right *string) bool {
	return left == nil && right == nil ||
		left != nil && right != nil && *left == *right
}

func profileUpdatedEvent(user User) events.Event {
	return events.Event{
		EntityType: TopicUser,
		Name:       EventUserUpdated,
		CreatedAt:  time.Now().UTC(),
		Actor:      events.UserActor(user.ID.String()),
		Payload:    ProjectUser(user),
	}
}
