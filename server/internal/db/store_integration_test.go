//go:build integration

package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/testutil/dbtest"

	"github.com/jackc/pgx/v5"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

func TestNewStoreRejectsNilPool(t *testing.T) {
	if _, err := NewStore(nil); err == nil {
		t.Fatal("NewStore(nil) error = nil, want error")
	}
}

func TestStoreTransactionsTenantScopeAndReplay(t *testing.T) {
	pool, _ := dbtest.StartPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := MigrateUp(ctx, pool); err != nil {
		t.Fatalf("MigrateUp() error = %v", err)
	}
	store, err := NewStore(pool)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.WithinTx(ctx, pgx.TxOptions{}, nil); err == nil {
		t.Fatal("WithinTx(nil callback) error = nil, want error")
	}

	var rollbackUserID uuid.UUID
	rollbackErr := errors.New("roll back this transaction")
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		if queries == nil {
			t.Fatal("WithinTx() supplied nil queries")
		}
		user, err := queries.CreateUser(ctx, generated.CreateUserParams{
			Email:       "rollback@example.com",
			DisplayName: "Rollback",
		})
		if err != nil {
			return err
		}
		rollbackUserID = user.ID
		return rollbackErr
	})
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("WithinTx() error = %v, want %v", err, rollbackErr)
	}
	if _, err := store.Queries().GetUserByID(ctx, rollbackUserID); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("rolled-back user lookup error = %v, want pgx.ErrNoRows", err)
	}

	var companyID, memberID, outsiderID uuid.UUID
	err = store.WithinTx(ctx, pgx.TxOptions{}, func(queries *generated.Queries) error {
		for index, params := range []generated.CreateUserParams{
			{Email: "member@example.com", DisplayName: "Member"},
			{Email: "outsider@example.com", DisplayName: "Outsider"},
		} {
			user, err := queries.CreateUser(ctx, params)
			if err != nil {
				return err
			}
			if index == 0 {
				memberID = user.ID
			} else {
				outsiderID = user.ID
			}
		}
		company, err := queries.CreateCompany(ctx, generated.CreateCompanyParams{
			Name:            "Example Company",
			Slug:            "example-company",
			TaskPrefix:      "EX",
			BusinessContext: "",
		})
		if err != nil {
			return err
		}
		companyID = company.ID
		_, err = queries.CreateCompanyMember(ctx, generated.CreateCompanyMemberParams{
			CompanyID: companyID,
			UserID:    memberID,
			Role:      "owner",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed transaction error = %v", err)
	}
	if companyID == uuid.Nil {
		t.Fatal("CreateCompany() returned a nil database-generated ID")
	}

	company, err := store.Queries().GetCompanyForMember(ctx, generated.GetCompanyForMemberParams{
		CompanyID: companyID,
		UserID:    memberID,
	})
	if err != nil {
		t.Fatalf("GetCompanyForMember(member) error = %v", err)
	}
	if company.ID != companyID {
		t.Fatalf("GetCompanyForMember(member).ID = %s, want %s", company.ID, companyID)
	}
	if _, err := store.Queries().GetCompanyForMember(ctx, generated.GetCompanyForMemberParams{
		CompanyID: companyID,
		UserID:    outsiderID,
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("GetCompanyForMember(outsider) error = %v, want pgx.ErrNoRows", err)
	}
	memberParams := generated.CreateCompanyMemberParams{
		CompanyID: companyID,
		UserID:    memberID,
		Role:      "owner",
	}
	if _, err := store.Queries().CreateCompanyMember(ctx, memberParams); err != nil {
		t.Fatalf("exact CreateCompanyMember replay error = %v", err)
	}
	memberParams.Role = "member"
	if _, err := store.Queries().CreateCompanyMember(ctx, memberParams); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("conflicting CreateCompanyMember replay error = %v, want pgx.ErrNoRows", err)
	}
}
