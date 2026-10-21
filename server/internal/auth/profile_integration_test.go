//go:build integration

package auth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
	"github.com/Intelligent-Internet/zenith-team/server/internal/testutil/dbtest"
)

func TestUpdateProfileAvatarPersistsWithoutCompanyAndHonorsRevision(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx := t.Context()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatal(err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(t, ctx, pool, `DELETE FROM company_members WHERE user_id=$1`, fixture.UserA)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE users SET avatar_url='https://example.com/original.png' WHERE id=$1`,
		fixture.UserA,
	)
	bus := authTestBus(t)
	eventLog := recordTopic[UserResponse](t, bus, TopicUser)
	service := &DefaultAuthService{store: store, bus: bus}
	input := UpdateProfileInput{ExpectedRevision: 1, DisplayName: "Member"}
	preserved, err := service.UpdateProfile(ctx, fixture.UserA, input)
	if err != nil || preserved.AvatarURL == nil || *preserved.AvatarURL != "https://example.com/original.png" {
		t.Fatalf("omitted avatar did not preserve original: %v", err)
	}
	avatar := profileAvatarFixture(t, "png", 128)
	input.ExpectedRevision = preserved.Version
	input.AvatarDataURL = &avatar
	updated, err := service.UpdateProfile(ctx, fixture.UserA, input)
	if err != nil || updated.AvatarURL == nil || *updated.AvatarURL != avatar ||
		updated.Version != preserved.Version+1 {
		t.Fatalf("avatar update did not persist: %v", err)
	}
	fresh, err := service.CurrentUser(ctx, fixture.UserA)
	if err != nil || fresh.AvatarURL == nil || *fresh.AvatarURL != avatar {
		t.Fatalf("avatar read = %v", err)
	}
	published := eventLog.snapshot()
	if len(published) != 2 || published[1].Payload.AvatarURL == nil || *published[1].Payload.AvatarURL != avatar {
		t.Fatal("avatar missing from user.updated")
	}
	input.ExpectedRevision = updated.Version
	noOp, err := service.UpdateProfile(ctx, fixture.UserA, input)
	if err != nil || noOp.Version != updated.Version || len(eventLog.snapshot()) != 2 {
		t.Fatalf("avatar no-op = %v", err)
	}
	replacement := profileAvatarFixture(t, "jpeg", 128)
	input.AvatarDataURL = &replacement
	input.ExpectedRevision--
	_, err = service.UpdateProfile(ctx, fixture.UserA, input)
	var conflict *ProfileRevisionConflictError
	if !errors.As(err, &conflict) || conflict.Current.AvatarURL == nil || *conflict.Current.AvatarURL != avatar {
		t.Fatalf("stale avatar update = %v", err)
	}
	input.ExpectedRevision = updated.Version
	replaced, err := service.UpdateProfile(ctx, fixture.UserA, input)
	if err != nil || replaced.AvatarURL == nil || *replaced.AvatarURL != replacement {
		t.Fatalf("replacement = %v", err)
	}
	other, err := service.CurrentUser(ctx, fixture.UserB)
	if err != nil || other.AvatarURL != nil {
		t.Fatalf("another user's avatar changed: %v", err)
	}
}

func TestUpdateProfilePersistsCanonicalValuesAndEmitsUserProjection(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	userA := fixture.UserA
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE users
		 SET password_hash = 'encoded-password', email_verified_at = clock_timestamp()
		 WHERE id = $1`,
		userA,
	)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`INSERT INTO oauth_accounts (id, user_id, provider, subject, email, profile)
		 VALUES ($1, $2, 'google', 'profile-user-a', 'schema-a@example.com', '{}')`,
		uuid.Must(uuid.NewV7()),
		userA,
	)
	bus := authTestBus(t)
	eventLog := recordTopic[UserResponse](t, bus, TopicUser)
	service := &DefaultAuthService{store: store, bus: bus}

	user, err := service.UpdateProfile(ctx, userA, UpdateProfileInput{
		ExpectedRevision: 1,
		DisplayName:      "\uFEFF  Sam Lee  \u3000",
		Biography:        "  Preferred context\r\nSecond line\r  ",
		PhoneCountryISO2: stringPointer(" vn "),
		PhoneNumber:      stringPointer("0912345678"),
	})
	if err != nil {
		t.Fatalf("UpdateProfile() error = %v", err)
	}
	if user.DisplayName != "Sam Lee" ||
		user.Biography != "Preferred context\nSecond line" ||
		user.PhoneCountryISO2 == nil || *user.PhoneCountryISO2 != "VN" ||
		user.PhoneNumber == nil || *user.PhoneNumber != "0912345678" ||
		user.Version != 2 {
		t.Fatalf("UpdateProfile() = %+v", user)
	}

	published := eventLog.snapshot()
	if len(published) != 1 {
		t.Fatalf("published profile events = %#v, want exactly the event event", published)
	}
	event := published[0]
	if event.Name != "user.updated" || event.Actor != events.UserActor(userA.String()) {
		t.Fatalf("profile event envelope = %#v", event)
	}
	payload := event.Payload
	if payload.ID != userA || payload.Revision != 2 ||
		payload.DisplayName != "Sam Lee" || payload.Biography != "Preferred context\nSecond line" ||
		payload.PhoneNumber == nil || *payload.PhoneNumber != "0912345678" ||
		payload.EmailVerifiedAt == nil || !payload.HasPassword ||
		!reflect.DeepEqual(payload.LinkedProviders, []string{"google"}) {
		t.Fatalf("profile event payload = %#v", event.Payload)
	}
}

func TestUpdateProfileNoOpAndOptimisticConflict(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	userA := fixture.UserA
	bus := authTestBus(t)
	eventLog := recordTopic[UserResponse](t, bus, TopicUser)
	service := &DefaultAuthService{store: store, bus: bus}

	updated, err := service.UpdateProfile(ctx, userA, UpdateProfileInput{
		ExpectedRevision: 1,
		DisplayName:      "Schema A",
		Biography:        "Context",
		PhoneCountryISO2: stringPointer("VN"),
		PhoneNumber:      stringPointer("912345678"),
	})
	if err != nil || updated.Version != 2 {
		t.Fatalf("first UpdateProfile() = %+v, %v", updated, err)
	}
	// expectedRevision is checked first, on every call: an identical-values
	// replay that carries the stale revision is a conflict, not a silent
	// success, and the conflict carries the committed snapshot.
	_, err = service.UpdateProfile(ctx, userA, UpdateProfileInput{
		ExpectedRevision: 1,
		DisplayName:      " Schema A ",
		Biography:        " Context ",
		PhoneCountryISO2: stringPointer(" vn "),
		PhoneNumber:      stringPointer("912345678"),
	})
	var staleReplay *ProfileRevisionConflictError
	if !errors.As(err, &staleReplay) || staleReplay.Current.Version != 2 {
		t.Fatalf("stale identical-values UpdateProfile() error = %v, want a revision-2 conflict", err)
	}
	// The same replay at the current revision is a no-op: the current User
	// comes back unchanged and no event is staged.
	noOp, err := service.UpdateProfile(ctx, userA, UpdateProfileInput{
		ExpectedRevision: 2,
		DisplayName:      " Schema A ",
		Biography:        " Context ",
		PhoneCountryISO2: stringPointer(" vn "),
		PhoneNumber:      stringPointer("912345678"),
	})
	if err != nil {
		t.Fatalf("no-op UpdateProfile() error = %v", err)
	}
	if noOp.Version != 2 {
		t.Fatalf("no-op UpdateProfile() = %+v", noOp)
	}
	_, err = service.UpdateProfile(ctx, userA, UpdateProfileInput{
		ExpectedRevision: 1,
		DisplayName:      "Conflicting name",
		Biography:        "Context",
		PhoneCountryISO2: stringPointer("VN"),
		PhoneNumber:      stringPointer("912345678"),
	})
	var conflict *ProfileRevisionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("conflicting UpdateProfile() error = %v, want *ProfileRevisionConflictError", err)
	}
	if conflict.Current.DisplayName != "Schema A" ||
		conflict.Current.Biography != "Context" ||
		conflict.Current.PhoneCountryISO2 == nil || *conflict.Current.PhoneCountryISO2 != "VN" ||
		conflict.Current.PhoneNumber == nil || *conflict.Current.PhoneNumber != "912345678" ||
		conflict.Current.Version != 2 {
		t.Fatalf("conflicting UpdateProfile() snapshot = %+v", conflict.Current)
	}
	if published := eventLog.snapshot(); len(published) != 1 {
		t.Fatalf("published profile events = %d, want the first update only", len(published))
	}
}

func TestUpdateProfileValidatesDisplayNameAndBiography(t *testing.T) {
	service := &DefaultAuthService{}
	// A revision outside 1..protocol.MaxJSONSafeInteger is not among these: it
	// is the `revision` binding rule on the handler package's
	// updateProfileRequest, exercised there by
	// TestRevisionBindingRejectsRevisionsOutsideTheRange.
	for name, input := range map[string]UpdateProfileInput{
		"empty name":      {ExpectedRevision: 1, DisplayName: " \u3000", Biography: ""},
		"name characters": {ExpectedRevision: 1, DisplayName: strings.Repeat("a", 121), Biography: ""},
		"bio characters":  {ExpectedRevision: 1, DisplayName: "Sam", Biography: strings.Repeat("a", 1201)},
		"bio lines":       {ExpectedRevision: 1, DisplayName: "Sam", Biography: strings.Repeat("x\n", 12) + "x"},
		"bio NUL":         {ExpectedRevision: 1, DisplayName: "Sam", Biography: "before\x00after"},
		"invalid UTF-8":   {ExpectedRevision: 1, DisplayName: "Sam", Biography: string([]byte{0xff})},
		"country only": {
			ExpectedRevision: 1, DisplayName: "Sam", PhoneCountryISO2: stringPointer("VN"),
		},
		"number only": {
			ExpectedRevision: 1, DisplayName: "Sam", PhoneNumber: stringPointer("912345678"),
		},
		"country shape": {
			ExpectedRevision: 1, DisplayName: "Sam",
			PhoneCountryISO2: stringPointer("VNM"), PhoneNumber: stringPointer("912345678"),
		},
		"unsupported country": {
			ExpectedRevision: 1, DisplayName: "Sam",
			PhoneCountryISO2: stringPointer("ZZ"), PhoneNumber: stringPointer("912345678"),
		},
		"phone characters": {
			ExpectedRevision: 1, DisplayName: "Sam",
			PhoneCountryISO2: stringPointer("VN"), PhoneNumber: stringPointer("912-345-678"),
		},
		"phone length": {
			ExpectedRevision: 1, DisplayName: "Sam",
			PhoneCountryISO2: stringPointer("VN"), PhoneNumber: stringPointer(strings.Repeat("1", 16)),
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.UpdateProfile(context.Background(), uuid.Nil, input)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("UpdateProfile() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestUpdateProfileDoesNotMutateADeletedUser(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := db.MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := db.NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	fixture := dbtest.SeedFixture(t, ctx, pool)
	dbtest.MustExec(
		t,
		ctx,
		pool,
		`UPDATE users SET disabled_at = now(), deleted_at = now() WHERE id = $1`,
		fixture.UserA,
	)
	service := &DefaultAuthService{store: store}
	_, err = service.UpdateProfile(ctx, fixture.UserA, UpdateProfileInput{
		ExpectedRevision: 1, DisplayName: "Changed", Biography: "",
	})
	if !errors.Is(err, ErrAccountDisabled) {
		t.Fatalf("UpdateProfile() error = %v, want ErrAccountDisabled", err)
	}
}

func stringPointer(value string) *string {
	return &value
}
