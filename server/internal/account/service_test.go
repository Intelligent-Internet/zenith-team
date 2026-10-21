package account

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
)

func TestServiceRejectsInvalidDependenciesAndConfirmation(t *testing.T) {
	sessionInvalidator := auth.SessionInvalidatorFunc(func(context.Context, []auth.SessionRevocation) {})
	if _, err := NewAccountService(ServiceOptions{}); err == nil {
		t.Fatal("NewAccountService() accepted missing dependencies")
	}
	if _, err := NewAccountService(ServiceOptions{
		Store: &db.Store{}, SessionInvalidator: sessionInvalidator,
	}); err == nil {
		t.Fatal("NewAccountService() accepted a Store without queries")
	}

	service := &DefaultAccountService{
		store:              &db.Store{},
		sessionInvalidator: sessionInvalidator,
	}
	principal := auth.UserPrincipal{
		UserID: uuid.Must(uuid.NewV7()), SessionID: uuid.Must(uuid.NewV7()), AuthKind: auth.TokenKindSession,
	}
	if _, err := service.Delete(
		context.Background(),
		principal,
		"delete",
	); !errors.Is(
		err,
		ErrInvalidConfirmation,
	) {
		t.Fatalf("Delete(invalid confirmation) error = %v, want ErrInvalidConfirmation", err)
	}
}
