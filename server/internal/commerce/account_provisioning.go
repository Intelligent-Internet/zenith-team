package commerce

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

const (
	accountProvisioningOperationType = "account_provisioning"
	accountMetadataSchemaValue       = "commerce_account_v1"
	accountMetadataSchemaVersion     = int32(1)

	accountProvisioningLeaseDuration  = 2 * time.Minute
	accountProviderRetryHorizon       = 23 * time.Hour
	accountRecoveryClockSkew          = 5 * time.Minute
	accountFinalizationTimeout        = 15 * time.Second
	accountRecoveryPageSize           = int64(100)
	maxAccountProviderAttempts        = int32(8)
	defaultAccountProviderCallTimeout = 30 * time.Second
	minAccountProviderCallTimeout     = time.Second
	maxAccountProviderCallTimeout     = 30 * time.Second

	operationStatePending           = "pending"
	operationStateLeased            = "leased"
	operationStateProviderConfirmed = "provider_confirmed"
	operationStateSucceeded         = "succeeded"
	operationStateRecoveryRequired  = "recovery_required"
)

type accountProvisioningIntent struct {
	operationID uuid.UUID
	companyID   uuid.UUID
	accountID   uuid.UUID
	environment Environment
	createdBy   uuid.UUID
	createdAt   time.Time
	requestSHA  [sha256.Size]byte
}

func newAccountProvisioningIntent(
	operationID uuid.UUID,
	companyID uuid.UUID,
	accountID uuid.UUID,
	environment Environment,
	createdBy uuid.UUID,
	createdAt time.Time,
) accountProvisioningIntent {
	intent := accountProvisioningIntent{
		operationID: operationID,
		companyID:   companyID,
		accountID:   accountID,
		environment: environment,
		createdBy:   createdBy,
		createdAt:   databaseTime(createdAt),
	}
	intent.requestSHA = accountProvisioningRequestSHA(intent)
	return intent
}

func newAccountProvisioningRequest(
	operationID uuid.UUID,
	companyID uuid.UUID,
	accountID uuid.UUID,
	environment Environment,
	createdBy uuid.UUID,
) accountProvisioningIntent {
	return newAccountProvisioningIntent(
		operationID,
		companyID,
		accountID,
		environment,
		createdBy,
		time.Time{},
	)
}

func accountProvisioningRequestSHA(intent accountProvisioningIntent) [sha256.Size]byte {
	canonical := strings.Join([]string{
		"boardly-commerce-account-provisioning-v1",
		"operation_id=" + intent.operationID.String(),
		"company_id=" + intent.companyID.String(),
		"commerce_account_id=" + intent.accountID.String(),
		"environment=" + string(intent.environment),
		"metadata_schema_version=1",
		"controller_template_version=1",
		"controller_losses_payments=stripe",
		"controller_fees_payer=account",
		"controller_requirement_collection=stripe",
		"controller_dashboard_type=full",
	}, "\n")
	return sha256.Sum256([]byte(canonical))
}

func (intent accountProvisioningIntent) idempotencyKey() string {
	return "boardly:connect-account:v1:" + intent.operationID.String()
}

func (intent accountProvisioningIntent) setupRetryIdempotencyKey(
	providerAttemptCount int32,
) string {
	return intent.idempotencyKey() + ":setup-retry:" + strconv.FormatInt(
		int64(providerAttemptCount),
		10,
	)
}

func (intent accountProvisioningIntent) validIdempotencyKey(
	key string,
	providerAttemptCount int32,
) bool {
	baseKey := intent.idempotencyKey()
	if key == baseKey {
		return true
	}
	retryText, ok := strings.CutPrefix(key, baseKey+":setup-retry:")
	if !ok {
		return false
	}
	retryAttempt, err := strconv.ParseInt(retryText, 10, 32)
	return err == nil && retryText == strconv.FormatInt(retryAttempt, 10) && retryAttempt >= 1 &&
		retryAttempt <= int64(providerAttemptCount) &&
		retryAttempt <= int64(maxAccountProviderAttempts)
}

func (intent accountProvisioningIntent) metadata() map[string]string {
	return map[string]string{
		"boardly_metadata_schema":     accountMetadataSchemaValue,
		"boardly_operation_type":      accountProvisioningOperationType,
		"boardly_operation_id":        intent.operationID.String(),
		"boardly_commerce_account_id": intent.accountID.String(),
		"boardly_request_sha256":      hex.EncodeToString(intent.requestSHA[:]),
	}
}

func (s *DefaultCommerceService) ensureAccount(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment Environment,
) (generated.CommerceAccount, EnvironmentOptions, error) {
	if err := validatePrincipalPermission(principal, auth.PermissionCommerceManage); err != nil {
		return generated.CommerceAccount{}, EnvironmentOptions{}, err
	}
	configuration, ok := s.environments[environment]
	if !ok || !environment.valid() {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrInvalidInput
	}
	if !configuration.Configured || configuration.Provider == nil {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrProviderUnavailable
	}
	accepted, err := s.currentTermsAccepted(ctx, principal)
	if err != nil {
		return generated.CommerceAccount{}, EnvironmentOptions{}, err
	}
	if !accepted {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrTermsOutdated
	}
	account, operation, err := s.ensureProvisioningIntent(ctx, principal, environment)
	if err != nil {
		return generated.CommerceAccount{}, EnvironmentOptions{}, err
	}
	if account.Status == string(StatusDraining) || account.Status == string(StatusDisconnected) {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrAccountConflict
	}
	intent := accountProvisioningIntentFromOperation(operation)

	if account.ProviderAccountID.Valid {
		if operation.State != operationStateSucceeded || !operation.ProviderResourceID.Valid ||
			operation.ProviderResourceID.String != account.ProviderAccountID.String {
			return generated.CommerceAccount{}, EnvironmentOptions{}, ErrInvalidStore
		}
		providerAccount, providerErr := configuration.Provider.RetrieveAccount(
			ctx,
			account.ProviderAccountID.String,
		)
		if providerErr != nil {
			return generated.CommerceAccount{}, EnvironmentOptions{}, providerError(
				"retrieve Stripe Connected Account",
				providerErr,
			)
		}
		if err := validateProviderAccountBinding(providerAccount, intent); err != nil {
			return generated.CommerceAccount{}, EnvironmentOptions{}, err
		}
		account, err = s.persistReconciledProjection(
			ctx,
			principal,
			account,
			providerAccount,
		)
		return account, configuration, err
	}
	if operation.State == operationStateProviderConfirmed {
		if !operation.ProviderResourceID.Valid {
			return generated.CommerceAccount{}, EnvironmentOptions{}, ErrInvalidStore
		}
		providerAccount, providerErr := configuration.Provider.RetrieveAccount(
			ctx,
			operation.ProviderResourceID.String,
		)
		if providerErr != nil {
			return generated.CommerceAccount{}, EnvironmentOptions{}, providerError(
				"retrieve confirmed Stripe Connected Account",
				providerErr,
			)
		}
		if err := validateProviderAccountBinding(providerAccount, intent); err != nil {
			return generated.CommerceAccount{}, EnvironmentOptions{}, err
		}
		finalizeCtx, cancel := boundedFinalizationContext(ctx)
		defer cancel()
		account, err = s.finalizeProvisioning(
			finalizeCtx,
			account,
			operation,
			providerAccount,
		)
		return account, configuration, err
	}
	if operation.State == operationStateSucceeded {
		return generated.CommerceAccount{}, EnvironmentOptions{}, ErrInvalidStore
	}

	account, err = s.provisionAccount(ctx, principal, account, operation, configuration.Provider)
	return account, configuration, err
}

func (s *DefaultCommerceService) ensureProvisioningIntent(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment Environment,
) (generated.CommerceAccount, generated.CommerceOperation, error) {
	var account generated.CommerceAccount
	var operation generated.CommerceOperation
	var err error
	created := false
	transactionErr := s.store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			if err := lockManagingMember(ctx, queries, principal); err != nil {
				return err
			}
			account, err = queries.GetCommerceAccountForUpdate(
				ctx,
				generated.GetCommerceAccountForUpdateParams{
					CompanyID: principal.CompanyID, Environment: string(environment),
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				account, err = queries.InsertCommerceAccountProvisioning(
					ctx,
					generated.InsertCommerceAccountProvisioningParams{
						CompanyID: principal.CompanyID, Environment: string(environment),
						CreatedByUserID: principal.UserID,
					},
				)
				if errors.Is(err, pgx.ErrNoRows) {
					account, err = queries.GetCommerceAccountForUpdate(
						ctx,
						generated.GetCommerceAccountForUpdateParams{
							CompanyID:   principal.CompanyID,
							Environment: string(environment),
						},
					)
				} else if err == nil {
					created = true
				}
			}
			if err != nil {
				return fmt.Errorf("ensure Commerce account provisioning row: %w", err)
			}
			if created {
				operation, err = queries.InsertCommerceAccountProvisioningOperation(
					ctx,
					generated.InsertCommerceAccountProvisioningOperationParams{
						CompanyID:             principal.CompanyID,
						AccountID:             account.ID,
						Environment:           string(environment),
						MetadataSchemaVersion: accountMetadataSchemaVersion,
						CreatedByUserID:       principal.UserID,
					},
				)
				if err != nil {
					return fmt.Errorf("insert Commerce account provisioning operation: %w", err)
				}
				return nil
			}
			operation, err = queries.GetCommerceAccountProvisioningOperationForUpdate(
				ctx,
				generated.GetCommerceAccountProvisioningOperationForUpdateParams{
					CompanyID: principal.CompanyID, AccountID: account.ID,
					Environment: string(environment),
				},
			)
			if err != nil {
				return fmt.Errorf("get Commerce account provisioning operation: %w", err)
			}
			return nil
		},
	)
	intent := newAccountProvisioningIntent(
		operation.ID,
		principal.CompanyID,
		account.ID,
		environment,
		principal.UserID,
		operation.CreatedAt.Time,
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		recoveryCtx, cancel := boundedFinalizationContext(ctx)
		defer cancel()
		account, operation, created, err = s.resolveProvisioningIntentCommit(
			recoveryCtx,
			principal,
			environment,
			intent,
		)
		if err != nil {
			return generated.CommerceAccount{}, generated.CommerceOperation{},
				errors.Join(transactionErr, err)
		}
	} else if transactionErr != nil {
		return generated.CommerceAccount{}, generated.CommerceOperation{}, transactionErr
	}
	if created {
		if !bytes.Equal(operation.RequestSha256, intent.requestSHA[:]) ||
			operation.IdempotencyKey != intent.idempotencyKey() {
			return generated.CommerceAccount{}, generated.CommerceOperation{}, ErrAccountConflict
		}
	}
	return account, operation, nil
}

func (s *DefaultCommerceService) resolveProvisioningIntentCommit(
	ctx context.Context,
	principal auth.UserPrincipal,
	environment Environment,
	proposed accountProvisioningIntent,
) (generated.CommerceAccount, generated.CommerceOperation, bool, error) {
	if err := authorizeRead(ctx, s.store.Queries(), principal, auth.PermissionCommerceManage); err != nil {
		return generated.CommerceAccount{}, generated.CommerceOperation{}, false, err
	}
	account, err := s.store.Queries().GetCommerceAccountByCompanyEnvironment(
		ctx,
		generated.GetCommerceAccountByCompanyEnvironmentParams{
			CompanyID: principal.CompanyID, Environment: string(environment),
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return generated.CommerceAccount{}, generated.CommerceOperation{}, false,
			ErrAccountRecovery
	}
	if err != nil {
		return generated.CommerceAccount{}, generated.CommerceOperation{}, false,
			fmt.Errorf("resolve Commerce provisioning account commit: %w", err)
	}
	operation, err := s.store.Queries().GetCommerceAccountProvisioningOperation(
		ctx,
		generated.GetCommerceAccountProvisioningOperationParams{
			CompanyID: principal.CompanyID, AccountID: account.ID,
			Environment: string(environment),
		},
	)
	if err != nil {
		return generated.CommerceAccount{}, generated.CommerceOperation{}, false,
			fmt.Errorf("resolve Commerce provisioning operation commit: %w", err)
	}
	created := account.ID == proposed.accountID
	if created && (operation.ID != proposed.operationID ||
		!bytes.Equal(operation.RequestSha256, proposed.requestSHA[:]) ||
		operation.IdempotencyKey != proposed.idempotencyKey()) {
		return generated.CommerceAccount{}, generated.CommerceOperation{}, false,
			ErrAccountConflict
	}
	return account, operation, created, nil
}

func (s *DefaultCommerceService) provisionAccount(
	ctx context.Context,
	principal auth.UserPrincipal,
	account generated.CommerceAccount,
	operation generated.CommerceOperation,
	provider Provider,
) (generated.CommerceAccount, error) {
	claimed, leaseID, err := s.claimProvisioningOperation(ctx, principal, account, operation)
	if err != nil {
		return generated.CommerceAccount{}, err
	}
	intent := accountProvisioningIntentFromOperation(claimed)
	lookup, err := provider.FindAccount(ctx, FindAccountInput{
		Metadata: intent.metadata(), CreatedAfter: claimed.CreatedAt.Time.Add(-accountRecoveryClockSkew),
		CreatedBefore: claimed.ProviderRetryDeadlineAt.Time.Add(accountRecoveryClockSkew),
		StartingAfter: textValue(claimed.RecoveryCursor), Limit: accountRecoveryPageSize,
	})
	if err != nil {
		releaseErr := s.releaseProvisioningLease(ctx, claimed, leaseID, "provider_unavailable")
		return generated.CommerceAccount{}, errors.Join(
			providerError("find Stripe Account for recovery", err),
			releaseErr,
		)
	}

	candidateID := textValue(claimed.RecoveryCandidateProviderID)
	if lookup.Found {
		if err := validateProviderAccountBinding(lookup.Account, intent); err != nil {
			releaseErr := s.releaseProvisioningLease(ctx, claimed, leaseID, "provider_state_invalid")
			return generated.CommerceAccount{}, errors.Join(err, releaseErr)
		}
		if candidateID != "" && candidateID != lookup.Account.ID {
			releaseErr := s.releaseProvisioningLease(ctx, claimed, leaseID, "duplicate_provider_accounts")
			return generated.CommerceAccount{}, errors.Join(ErrInvalidProviderState, releaseErr)
		}
		candidateID = lookup.Account.ID
	}
	if lookup.HasMore {
		if !providerIDPattern.MatchString(lookup.NextCursor) {
			releaseErr := s.releaseProvisioningLease(ctx, claimed, leaseID, "provider_state_invalid")
			return generated.CommerceAccount{}, errors.Join(ErrInvalidProviderState, releaseErr)
		}
		if err := s.advanceRecoveryScan(ctx, claimed, leaseID, lookup.NextCursor, candidateID); err != nil {
			return generated.CommerceAccount{}, err
		}
		return generated.CommerceAccount{}, ErrAccountRecovery
	}
	if candidateID != "" {
		providerAccount := lookup.Account
		if !lookup.Found {
			providerAccount, err = provider.RetrieveAccount(ctx, candidateID)
			if err != nil {
				releaseErr := s.releaseProvisioningLease(ctx, claimed, leaseID, "provider_unavailable")
				return generated.CommerceAccount{}, errors.Join(
					providerError("retrieve recovered Stripe Connected Account", err),
					releaseErr,
				)
			}
		}
		if err := validateProviderAccountBinding(providerAccount, intent); err != nil {
			releaseErr := s.releaseProvisioningLease(ctx, claimed, leaseID, "provider_state_invalid")
			return generated.CommerceAccount{}, errors.Join(err, releaseErr)
		}
		return s.confirmAndFinalizeProvisioning(ctx, account, claimed, leaseID, providerAccount)
	}

	providerCallCtx, cancelProviderCall := context.WithTimeout(ctx, s.providerCallTimeout)
	defer cancelProviderCall()
	attempted, err := s.recordProviderAttempt(providerCallCtx, principal, claimed, leaseID)
	if err != nil {
		if errors.Is(err, ErrAccountRecovery) {
			releaseErr := s.releaseProvisioningLease(
				ctx,
				claimed,
				leaseID,
				"provider_retry_expired",
			)
			return generated.CommerceAccount{}, errors.Join(err, releaseErr)
		}
		return generated.CommerceAccount{}, err
	}
	claimed = attempted
	if err := providerCallCtx.Err(); err != nil {
		releaseErr := s.releaseProvisioningLease(
			ctx,
			claimed,
			leaseID,
			"provider_call_window_expired",
		)
		return generated.CommerceAccount{}, errors.Join(
			providerError("start Stripe Connected Account creation", err),
			releaseErr,
		)
	}
	providerAccount, err := provider.CreateAccount(providerCallCtx, CreateAccountInput{
		IdempotencyKey: claimed.IdempotencyKey, Metadata: intent.metadata(),
		ControllerTemplateVersion: controllerTemplateVersion,
	})
	if err != nil {
		if errors.Is(err, ErrProviderSetupRequired) {
			rotationErr := s.rotateProvisioningIdempotency(ctx, claimed, leaseID)
			if rotationErr != nil {
				return generated.CommerceAccount{}, errors.Join(
					ErrAccountRecovery,
					fmt.Errorf("create Stripe Connected Account: %w", err),
					rotationErr,
				)
			}
			return generated.CommerceAccount{}, fmt.Errorf(
				"create Stripe Connected Account: %w",
				err,
			)
		}
		releaseErr := s.releaseProvisioningLease(ctx, claimed, leaseID, "provider_create_ambiguous")
		return generated.CommerceAccount{}, errors.Join(
			providerError("create Stripe Connected Account", err),
			releaseErr,
		)
	}
	if err := validateProviderAccountBinding(providerAccount, intent); err != nil {
		releaseErr := s.releaseProvisioningLease(ctx, claimed, leaseID, "provider_state_invalid")
		return generated.CommerceAccount{}, errors.Join(err, releaseErr)
	}
	return s.confirmAndFinalizeProvisioning(ctx, account, claimed, leaseID, providerAccount)
}

func (s *DefaultCommerceService) rotateProvisioningIdempotency(
	ctx context.Context,
	operation generated.CommerceOperation,
	leaseID uuid.UUID,
) error {
	updateCtx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	var updated generated.CommerceOperation
	var beforeRotation generated.CommerceOperation
	var retryKey string
	transactionErr := s.store.WithinTx(
		updateCtx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			var err error
			beforeRotation, err = queries.GetCommerceAccountProvisioningOperationForUpdate(
				updateCtx,
				generated.GetCommerceAccountProvisioningOperationForUpdateParams{
					CompanyID: operation.CompanyID, AccountID: operation.CommerceAccountID,
					Environment: operation.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock Commerce operation for idempotency rotation: %w", err)
			}
			if !operationLeaseMatches(beforeRotation, operation, leaseID) {
				return ErrAccountRecovery
			}
			intent := accountProvisioningIntentFromOperation(beforeRotation)
			retryKey = intent.setupRetryIdempotencyKey(beforeRotation.ProviderAttemptCount)
			if retryKey == beforeRotation.IdempotencyKey {
				return ErrInvalidStore
			}
			updated, err = queries.RotateCommerceAccountProvisioningIdempotency(
				updateCtx,
				generated.RotateCommerceAccountProvisioningIdempotencyParams{
					NewIdempotencyKey: retryKey,
					OperationID:       beforeRotation.ID,
					CompanyID:         beforeRotation.CompanyID,
					AccountID:         beforeRotation.CommerceAccountID,
					Environment:       beforeRotation.Environment,
					RequestSha256:     beforeRotation.RequestSha256,
					IdempotencyKey:    beforeRotation.IdempotencyKey,
					LeaseID:           leaseID,
					ExpectedVersion:   beforeRotation.Version,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountRecovery
			}
			if err != nil {
				return fmt.Errorf("rotate Commerce provisioning idempotency: %w", err)
			}
			return nil
		},
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		verified, err := s.store.Queries().GetCommerceOperationByIdentity(
			updateCtx,
			generated.GetCommerceOperationByIdentityParams{
				OperationID: operation.ID, CompanyID: operation.CompanyID,
				AccountID: operation.CommerceAccountID, Environment: operation.Environment,
			},
		)
		if err != nil || !rotatedIdempotencyOperationMatches(
			verified,
			beforeRotation,
			retryKey,
		) {
			return errors.Join(transactionErr, err)
		}
		updated = verified
	} else if transactionErr != nil {
		return transactionErr
	}
	if !rotatedIdempotencyOperationMatches(updated, beforeRotation, retryKey) {
		return ErrInvalidStore
	}
	return nil
}

func (s *DefaultCommerceService) claimProvisioningOperation(
	ctx context.Context,
	principal auth.UserPrincipal,
	account generated.CommerceAccount,
	operation generated.CommerceOperation,
) (generated.CommerceOperation, uuid.UUID, error) {
	leaseID, err := uuid.NewV7()
	if err != nil {
		return generated.CommerceOperation{}, uuid.Nil, errors.New("generate Commerce operation lease ID")
	}
	var claimed generated.CommerceOperation
	var beforeClaim generated.CommerceOperation
	transactionErr := s.store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			if err := lockManagingMember(ctx, queries, principal); err != nil {
				return err
			}
			currentAccount, err := queries.GetCommerceAccountForUpdate(
				ctx,
				generated.GetCommerceAccountForUpdateParams{
					CompanyID: principal.CompanyID, Environment: account.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock Commerce account for provisioning: %w", err)
			}
			if currentAccount.ID != account.ID {
				return ErrAccountConflict
			}
			if currentAccount.ProviderAccountID.Valid {
				return ErrAccountRecovery
			}
			if currentAccount.Status != string(StatusProvisioning) {
				return ErrAccountConflict
			}
			beforeClaim, err = queries.GetCommerceAccountProvisioningOperationForUpdate(
				ctx,
				generated.GetCommerceAccountProvisioningOperationForUpdateParams{
					CompanyID: principal.CompanyID, AccountID: account.ID,
					Environment: account.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock Commerce provisioning operation: %w", err)
			}
			if beforeClaim.ID != operation.ID {
				return ErrAccountConflict
			}
			claimed, err = queries.ClaimCommerceAccountProvisioningOperation(
				ctx,
				generated.ClaimCommerceAccountProvisioningOperationParams{
					LeaseID:                   leaseID,
					LeaseDurationMilliseconds: accountProvisioningLeaseDuration.Milliseconds(),
					OperationID:               beforeClaim.ID,
					CompanyID:                 beforeClaim.CompanyID,
					AccountID:                 beforeClaim.CommerceAccountID,
					Environment:               beforeClaim.Environment,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountRecovery
			}
			if err != nil {
				return fmt.Errorf("claim Commerce provisioning operation: %w", err)
			}
			return nil
		},
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		recoveryCtx, cancel := boundedFinalizationContext(ctx)
		defer cancel()
		verified, err := s.store.Queries().GetCommerceOperationByIdentity(
			recoveryCtx,
			generated.GetCommerceOperationByIdentityParams{
				OperationID: operation.ID, CompanyID: operation.CompanyID,
				AccountID: operation.CommerceAccountID, Environment: operation.Environment,
			},
		)
		if err != nil || !claimedOperationMatches(
			verified,
			beforeClaim,
			leaseID,
		) {
			return generated.CommerceOperation{}, uuid.Nil, errors.Join(transactionErr, err)
		}
		claimed = verified
	} else if transactionErr != nil {
		return generated.CommerceOperation{}, uuid.Nil, transactionErr
	}
	if !claimedOperationMatches(claimed, beforeClaim, leaseID) {
		return generated.CommerceOperation{}, uuid.Nil, fmt.Errorf(
			"validate claimed Commerce operation: %w",
			ErrInvalidStore,
		)
	}
	return claimed, leaseID, nil
}

func (s *DefaultCommerceService) advanceRecoveryScan(
	ctx context.Context,
	operation generated.CommerceOperation,
	leaseID uuid.UUID,
	cursor string,
	candidateID string,
) error {
	updateCtx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	var updated generated.CommerceOperation
	var beforeAdvance generated.CommerceOperation
	transactionErr := s.store.WithinTx(
		updateCtx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			var err error
			beforeAdvance, err = queries.GetCommerceAccountProvisioningOperationForUpdate(
				updateCtx,
				generated.GetCommerceAccountProvisioningOperationForUpdateParams{
					CompanyID: operation.CompanyID, AccountID: operation.CommerceAccountID,
					Environment: operation.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock Commerce operation for recovery scan: %w", err)
			}
			if !operationLeaseMatches(beforeAdvance, operation, leaseID) {
				return ErrAccountRecovery
			}
			updated, err = queries.AdvanceCommerceAccountRecoveryScan(
				updateCtx,
				generated.AdvanceCommerceAccountRecoveryScanParams{
					RecoveryCursor:              optionalText(cursor),
					RecoveryCandidateProviderID: optionalText(candidateID),
					OperationID:                 beforeAdvance.ID,
					CompanyID:                   beforeAdvance.CompanyID,
					AccountID:                   beforeAdvance.CommerceAccountID,
					Environment:                 beforeAdvance.Environment,
					RequestSha256:               beforeAdvance.RequestSha256,
					IdempotencyKey:              beforeAdvance.IdempotencyKey,
					LeaseID:                     leaseID, ExpectedVersion: beforeAdvance.Version,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountRecovery
			}
			if err != nil {
				return fmt.Errorf("advance Commerce account recovery scan: %w", err)
			}
			return nil
		},
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		verified, err := s.store.Queries().GetCommerceOperationByIdentity(
			updateCtx,
			generated.GetCommerceOperationByIdentityParams{
				OperationID: operation.ID, CompanyID: operation.CompanyID,
				AccountID: operation.CommerceAccountID, Environment: operation.Environment,
			},
		)
		if err != nil || !advancedRecoveryOperationMatches(
			verified,
			beforeAdvance,
			cursor,
			candidateID,
		) {
			return errors.Join(transactionErr, err)
		}
		updated = verified
	} else if transactionErr != nil {
		return transactionErr
	}
	if !advancedRecoveryOperationMatches(
		updated,
		beforeAdvance,
		cursor,
		candidateID,
	) {
		return ErrInvalidStore
	}
	return nil
}

func (s *DefaultCommerceService) recordProviderAttempt(
	ctx context.Context,
	principal auth.UserPrincipal,
	operation generated.CommerceOperation,
	leaseID uuid.UUID,
) (generated.CommerceOperation, error) {
	var updated generated.CommerceOperation
	var beforeAttempt generated.CommerceOperation
	transactionErr := s.store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			if err := lockManagingMember(ctx, queries, principal); err != nil {
				return err
			}
			var err error
			beforeAttempt, err = queries.GetCommerceAccountProvisioningOperationForUpdate(
				ctx,
				generated.GetCommerceAccountProvisioningOperationForUpdateParams{
					CompanyID: operation.CompanyID, AccountID: operation.CommerceAccountID,
					Environment: operation.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock Commerce operation for provider attempt: %w", err)
			}
			if !operationLeaseMatches(beforeAttempt, operation, leaseID) {
				return ErrAccountRecovery
			}
			updated, err = queries.RecordCommerceAccountProviderAttempt(
				ctx,
				generated.RecordCommerceAccountProviderAttemptParams{
					ProviderCallBudgetMilliseconds: s.providerCallTimeout.Milliseconds(),
					OperationID:                    beforeAttempt.ID,
					CompanyID:                      beforeAttempt.CompanyID,
					AccountID:                      beforeAttempt.CommerceAccountID,
					Environment:                    beforeAttempt.Environment,
					RequestSha256:                  beforeAttempt.RequestSha256,
					IdempotencyKey:                 beforeAttempt.IdempotencyKey,
					LeaseID:                        leaseID,
					ExpectedVersion:                beforeAttempt.Version,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountRecovery
			}
			if err != nil {
				return fmt.Errorf("record Commerce provider attempt: %w", err)
			}
			return nil
		},
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		recoveryCtx, cancel := boundedFinalizationContext(ctx)
		defer cancel()
		verified, err := s.store.Queries().GetCommerceOperationByIdentity(
			recoveryCtx,
			generated.GetCommerceOperationByIdentityParams{
				OperationID: operation.ID, CompanyID: operation.CompanyID,
				AccountID: operation.CommerceAccountID, Environment: operation.Environment,
			},
		)
		if err != nil || !providerAttemptOperationMatches(
			verified,
			beforeAttempt,
			leaseID,
			s.providerCallTimeout,
		) {
			return generated.CommerceOperation{}, errors.Join(transactionErr, err)
		}
		updated = verified
	} else if transactionErr != nil {
		return generated.CommerceOperation{}, transactionErr
	}
	if !providerAttemptOperationMatches(
		updated,
		beforeAttempt,
		leaseID,
		s.providerCallTimeout,
	) {
		return generated.CommerceOperation{}, fmt.Errorf(
			"validate Commerce provider attempt: %w",
			ErrInvalidStore,
		)
	}
	return updated, nil
}

func (s *DefaultCommerceService) releaseProvisioningLease(
	ctx context.Context,
	operation generated.CommerceOperation,
	leaseID uuid.UUID,
	errorCode string,
) error {
	updateCtx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	var updated generated.CommerceOperation
	var beforeRelease generated.CommerceOperation
	transactionErr := s.store.WithinTx(
		updateCtx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			var err error
			beforeRelease, err = queries.GetCommerceAccountProvisioningOperationForUpdate(
				updateCtx,
				generated.GetCommerceAccountProvisioningOperationForUpdateParams{
					CompanyID: operation.CompanyID, AccountID: operation.CommerceAccountID,
					Environment: operation.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock Commerce operation for lease release: %w", err)
			}
			if !operationLeaseMatches(beforeRelease, operation, leaseID) {
				return ErrAccountRecovery
			}
			updated, err = queries.ReleaseCommerceAccountProvisioningLease(
				updateCtx,
				generated.ReleaseCommerceAccountProvisioningLeaseParams{
					LastErrorCode: errorCode, OperationID: beforeRelease.ID,
					CompanyID:      beforeRelease.CompanyID,
					AccountID:      beforeRelease.CommerceAccountID,
					Environment:    beforeRelease.Environment,
					RequestSha256:  beforeRelease.RequestSha256,
					IdempotencyKey: beforeRelease.IdempotencyKey, LeaseID: leaseID,
					ExpectedVersion: beforeRelease.Version,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountRecovery
			}
			if err != nil {
				return fmt.Errorf("release Commerce provisioning lease: %w", err)
			}
			return nil
		},
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		verified, err := s.store.Queries().GetCommerceOperationByIdentity(
			updateCtx,
			generated.GetCommerceOperationByIdentityParams{
				OperationID: operation.ID, CompanyID: operation.CompanyID,
				AccountID: operation.CommerceAccountID, Environment: operation.Environment,
			},
		)
		if err != nil || !releasedOperationMatches(
			verified,
			beforeRelease,
			errorCode,
		) {
			return errors.Join(transactionErr, err)
		}
		updated = verified
	} else if transactionErr != nil {
		return transactionErr
	}
	if !releasedOperationMatches(
		updated,
		beforeRelease,
		errorCode,
	) {
		return ErrInvalidStore
	}
	return nil
}

func (s *DefaultCommerceService) confirmAndFinalizeProvisioning(
	ctx context.Context,
	account generated.CommerceAccount,
	operation generated.CommerceOperation,
	leaseID uuid.UUID,
	providerAccount ProviderAccount,
) (generated.CommerceAccount, error) {
	finalizeCtx, cancel := boundedFinalizationContext(ctx)
	defer cancel()
	confirmed, err := s.recordProviderConfirmation(
		finalizeCtx,
		operation,
		leaseID,
		providerAccount.ID,
	)
	if err != nil {
		return generated.CommerceAccount{}, err
	}
	return s.finalizeProvisioning(finalizeCtx, account, confirmed, providerAccount)
}

func (s *DefaultCommerceService) recordProviderConfirmation(
	ctx context.Context,
	operation generated.CommerceOperation,
	leaseID uuid.UUID,
	providerAccountID string,
) (generated.CommerceOperation, error) {
	var confirmed generated.CommerceOperation
	var beforeConfirmation generated.CommerceOperation
	transactionErr := s.store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			var err error
			beforeConfirmation, err = queries.GetCommerceAccountProvisioningOperationForUpdate(
				ctx,
				generated.GetCommerceAccountProvisioningOperationForUpdateParams{
					CompanyID: operation.CompanyID, AccountID: operation.CommerceAccountID,
					Environment: operation.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock Commerce operation for provider confirmation: %w", err)
			}
			if !operationLeaseMatches(beforeConfirmation, operation, leaseID) {
				return ErrAccountRecovery
			}
			confirmed, err = queries.RecordCommerceAccountProviderConfirmation(
				ctx,
				generated.RecordCommerceAccountProviderConfirmationParams{
					ProviderResourceID: providerAccountID,
					OperationID:        beforeConfirmation.ID,
					CompanyID:          beforeConfirmation.CompanyID,
					AccountID:          beforeConfirmation.CommerceAccountID,
					Environment:        beforeConfirmation.Environment,
					RequestSha256:      beforeConfirmation.RequestSha256,
					IdempotencyKey:     beforeConfirmation.IdempotencyKey,
					LeaseID:            leaseID,
					ExpectedVersion:    beforeConfirmation.Version,
				},
			)
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrAccountRecovery
			}
			if err != nil {
				return fmt.Errorf("record Commerce provider confirmation: %w", err)
			}
			return nil
		},
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		verified, err := s.store.Queries().GetCommerceOperationByIdentity(
			ctx,
			generated.GetCommerceOperationByIdentityParams{
				OperationID: operation.ID, CompanyID: operation.CompanyID,
				AccountID: operation.CommerceAccountID, Environment: operation.Environment,
			},
		)
		if err != nil || !providerConfirmationMatches(
			verified,
			beforeConfirmation,
			providerAccountID,
		) {
			return generated.CommerceOperation{}, errors.Join(transactionErr, err)
		}
		confirmed = verified
	} else if transactionErr != nil {
		return generated.CommerceOperation{}, transactionErr
	}
	if !providerConfirmationMatches(confirmed, beforeConfirmation, providerAccountID) {
		return generated.CommerceOperation{}, fmt.Errorf(
			"validate Commerce provider confirmation: %w",
			ErrInvalidStore,
		)
	}
	return confirmed, nil
}

func (s *DefaultCommerceService) finalizeProvisioning(
	ctx context.Context,
	account generated.CommerceAccount,
	operation generated.CommerceOperation,
	providerAccount ProviderAccount,
) (generated.CommerceAccount, error) {
	intent := accountProvisioningIntentFromOperation(operation)
	if err := validateProviderAccountBinding(providerAccount, intent); err != nil {
		return generated.CommerceAccount{}, err
	}
	projection := projectionParams(providerAccount, s.allowedCountries)
	var currentAccount generated.CommerceAccount
	var currentOperation generated.CommerceOperation
	var updatedAccount generated.CommerceAccount
	var completedOperation generated.CommerceOperation
	var err error
	alreadyCompleted := false
	transactionErr := s.store.WithinTx(
		ctx,
		pgx.TxOptions{},
		func(queries *generated.Queries) error {
			currentAccount, err = queries.GetCommerceAccountForUpdate(
				ctx,
				generated.GetCommerceAccountForUpdateParams{
					CompanyID: operation.CompanyID, Environment: operation.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock confirmed Commerce account: %w", err)
			}
			currentOperation, err = queries.GetCommerceAccountProvisioningOperationForUpdate(
				ctx,
				generated.GetCommerceAccountProvisioningOperationForUpdateParams{
					CompanyID: operation.CompanyID, AccountID: operation.CommerceAccountID,
					Environment: operation.Environment,
				},
			)
			if err != nil {
				return fmt.Errorf("lock confirmed Commerce operation: %w", err)
			}
			if currentOperation.ID != operation.ID || currentAccount.ID != account.ID ||
				currentOperation.CommerceAccountID != currentAccount.ID ||
				!currentOperation.ProviderResourceID.Valid ||
				currentOperation.ProviderResourceID.String != providerAccount.ID {
				return ErrAccountConflict
			}
			if currentOperation.State == operationStateSucceeded {
				if !currentAccount.ProviderAccountID.Valid ||
					currentAccount.ProviderAccountID.String != providerAccount.ID {
					return ErrAccountConflict
				}
				updatedAccount = currentAccount
				completedOperation = currentOperation
				alreadyCompleted = true
				return nil
			}
			if currentOperation.State != operationStateProviderConfirmed ||
				currentAccount.ProviderAccountID.Valid {
				return ErrAccountConflict
			}
			updatedAccount, err = queries.FinalizeCommerceAccountProvisioning(
				ctx,
				finalizeAccountParams(currentAccount, providerAccount, projection),
			)
			if err != nil {
				return fmt.Errorf("finalize Commerce account projection: %w", err)
			}
			completedOperation, err = queries.CompleteCommerceAccountProvisioningOperation(
				ctx,
				generated.CompleteCommerceAccountProvisioningOperationParams{
					OperationID:        currentOperation.ID,
					CompanyID:          currentOperation.CompanyID,
					AccountID:          currentOperation.CommerceAccountID,
					Environment:        currentOperation.Environment,
					RequestSha256:      currentOperation.RequestSha256,
					IdempotencyKey:     currentOperation.IdempotencyKey,
					ProviderResourceID: providerAccount.ID,
					ExpectedVersion:    currentOperation.Version,
				},
			)
			if err != nil {
				return fmt.Errorf("complete Commerce account provisioning operation: %w", err)
			}
			return nil
		},
	)
	if errors.Is(transactionErr, db.ErrCommitUnknown) {
		verifiedAccount, accountErr := s.store.Queries().GetCommerceAccountByIdentity(
			ctx,
			generated.GetCommerceAccountByIdentityParams{
				CompanyID: operation.CompanyID, AccountID: operation.CommerceAccountID,
				Environment: operation.Environment,
			},
		)
		verifiedOperation, operationErr := s.store.Queries().GetCommerceOperationByIdentity(
			ctx,
			generated.GetCommerceOperationByIdentityParams{
				OperationID: operation.ID, CompanyID: operation.CompanyID,
				AccountID: operation.CommerceAccountID, Environment: operation.Environment,
			},
		)
		matches := false
		if accountErr == nil && operationErr == nil {
			if alreadyCompleted {
				matches = observedCompletedRowsMatch(
					verifiedAccount,
					verifiedOperation,
					currentAccount,
					currentOperation,
					providerAccount.ID,
				)
			} else {
				matches = exactProviderProjectionMatches(
					verifiedAccount,
					currentAccount,
					providerAccount,
					projection,
				) && completedOperationMatches(
					verifiedOperation,
					currentOperation,
					providerAccount.ID,
				)
			}
		}
		if !matches {
			return generated.CommerceAccount{}, errors.Join(
				transactionErr,
				accountErr,
				operationErr,
			)
		}
		updatedAccount = verifiedAccount
		completedOperation = verifiedOperation
	} else if transactionErr != nil {
		return generated.CommerceAccount{}, transactionErr
	}
	if !alreadyCompleted && !completedOperationMatches(
		completedOperation,
		currentOperation,
		providerAccount.ID,
	) {
		return generated.CommerceAccount{}, fmt.Errorf(
			"validate completed Commerce operation: %w",
			ErrInvalidStore,
		)
	}
	if completedOperation.State != operationStateSucceeded {
		return generated.CommerceAccount{}, ErrInvalidStore
	}
	return updatedAccount, nil
}

func finalizeAccountParams(
	current generated.CommerceAccount,
	account ProviderAccount,
	projection providerProjection,
) generated.FinalizeCommerceAccountProvisioningParams {
	return generated.FinalizeCommerceAccountProvisioningParams{
		ProviderAccountID:              account.ID,
		Status:                         string(projection.status),
		Country:                        projection.country,
		DefaultCurrency:                projection.defaultCurrency,
		DetailsSubmitted:               account.DetailsSubmitted,
		ChargesEnabled:                 account.ChargesEnabled,
		PayoutsEnabled:                 account.PayoutsEnabled,
		CardPaymentsStatus:             projection.cardPaymentsStatus,
		TransfersStatus:                projection.transfersStatus,
		RequirementsCurrentlyDueCount:  int32(account.Requirements.CurrentlyDue),
		RequirementsEventuallyDueCount: int32(account.Requirements.EventuallyDue),
		RequirementsPastDueCount:       int32(account.Requirements.PastDue),
		RequirementsDeadline:           optionalTimestamp(projection.deadline),
		DisabledReason:                 projection.disabledReason,
		CompanyID:                      current.CompanyID,
		AccountID:                      current.ID,
		Environment:                    current.Environment,
		ExpectedVersion:                current.Version,
	}
}

func accountProvisioningIntentFromOperation(
	operation generated.CommerceOperation,
) accountProvisioningIntent {
	intent := newAccountProvisioningIntent(
		operation.ID,
		operation.CompanyID,
		operation.CommerceAccountID,
		Environment(operation.Environment),
		operation.CreatedByUserID,
		operation.CreatedAt.Time,
	)
	return intent
}

func validateProviderAccountBinding(
	account ProviderAccount,
	intent accountProvisioningIntent,
) error {
	if err := validateProviderAccount(account); err != nil {
		return err
	}
	for key, value := range intent.metadata() {
		if account.Metadata[key] != value {
			return ErrInvalidProviderState
		}
	}
	return nil
}

func recoveryStateAt(
	operation generated.CommerceOperation,
	databaseNow time.Time,
) (string, *time.Time) {
	if databaseNow.Before(operation.ProviderRetryDeadlineAt.Time) &&
		operation.ProviderAttemptCount < maxAccountProviderAttempts {
		return operationStatePending, nil
	}
	if operation.RecoveryRequiredAt.Valid {
		return operationStateRecoveryRequired, &operation.RecoveryRequiredAt.Time
	}
	recoveryRequiredAt := databaseNow
	return operationStateRecoveryRequired, &recoveryRequiredAt
}

func claimedOperationMatches(
	actual generated.CommerceOperation,
	before generated.CommerceOperation,
	leaseID uuid.UUID,
) bool {
	return operationStaticFieldsMatch(actual, before) && actual.State == operationStateLeased &&
		actual.LeaseID.Valid && actual.LeaseID.UUID == leaseID &&
		actual.LeaseExpiresAt.Valid && databaseTimesEqual(
		actual.LeaseExpiresAt,
		actual.UpdatedAt.Time.Add(accountProvisioningLeaseDuration),
	) &&
		actual.AttemptCount == before.AttemptCount+1 &&
		providerAttemptHistoryMatches(actual, before) &&
		textsEqual(actual.RecoveryCursor, before.RecoveryCursor) &&
		textsEqual(actual.RecoveryCandidateProviderID, before.RecoveryCandidateProviderID) &&
		textsEqual(actual.ProviderResourceID, before.ProviderResourceID) &&
		timePointersEqual(actual.ProviderConfirmedAt, before.ProviderConfirmedAt) &&
		timePointersEqual(actual.RecoveryRequiredAt, before.RecoveryRequiredAt) &&
		timePointersEqual(actual.CompletedAt, before.CompletedAt) && !actual.LastErrorCode.Valid &&
		actual.Version == before.Version+1
}

func advancedRecoveryOperationMatches(
	actual generated.CommerceOperation,
	before generated.CommerceOperation,
	cursor string,
	candidateID string,
) bool {
	nextState, recoveryRequiredAt := recoveryStateAt(before, actual.UpdatedAt.Time)
	return operationStaticFieldsMatch(actual, before) && actual.State == nextState &&
		!actual.LeaseID.Valid && !actual.LeaseExpiresAt.Valid &&
		actual.AttemptCount == before.AttemptCount &&
		providerAttemptHistoryMatches(actual, before) &&
		textValue(actual.RecoveryCursor) == cursor &&
		textValue(actual.RecoveryCandidateProviderID) == candidateID &&
		textsEqual(actual.ProviderResourceID, before.ProviderResourceID) &&
		timePointersEqual(actual.ProviderConfirmedAt, before.ProviderConfirmedAt) &&
		timePointersEqual(actual.RecoveryRequiredAt, recoveryRequiredAt) &&
		timePointersEqual(actual.CompletedAt, before.CompletedAt) &&
		exactText(actual.LastErrorCode, "recovery_scan_incomplete") &&
		actual.Version == before.Version+1
}

func providerAttemptOperationMatches(
	actual generated.CommerceOperation,
	before generated.CommerceOperation,
	leaseID uuid.UUID,
	providerCallTimeout time.Duration,
) bool {
	expectedFirstAttemptedAt := before.ProviderFirstAttemptedAt
	if !expectedFirstAttemptedAt.Valid {
		expectedFirstAttemptedAt = actual.UpdatedAt
	}
	return operationStaticFieldsMatch(actual, before) && actual.State == operationStateLeased &&
		actual.LeaseID.Valid && actual.LeaseID.UUID == leaseID &&
		actual.LeaseID == before.LeaseID && actual.LeaseExpiresAt.Valid &&
		databaseTimesEqual(
			actual.LeaseExpiresAt,
			actual.UpdatedAt.Time.Add(providerCallTimeout),
		) &&
		actual.AttemptCount == before.AttemptCount &&
		actual.ProviderAttemptCount == before.ProviderAttemptCount+1 &&
		before.ProviderAttemptCount < maxAccountProviderAttempts &&
		actual.UpdatedAt.Time.Add(providerCallTimeout).Before(actual.ProviderRetryDeadlineAt.Time) &&
		actual.ProviderFirstAttemptedAt.Valid && actual.ProviderLastAttemptedAt.Valid &&
		timePointersEqual(actual.ProviderFirstAttemptedAt, expectedFirstAttemptedAt) &&
		databaseTimesEqual(actual.ProviderLastAttemptedAt, actual.UpdatedAt) &&
		!actual.RecoveryCursor.Valid && !actual.RecoveryCandidateProviderID.Valid &&
		textsEqual(actual.ProviderResourceID, before.ProviderResourceID) &&
		timePointersEqual(actual.ProviderConfirmedAt, before.ProviderConfirmedAt) &&
		timePointersEqual(actual.RecoveryRequiredAt, before.RecoveryRequiredAt) &&
		timePointersEqual(actual.CompletedAt, before.CompletedAt) && !actual.LastErrorCode.Valid &&
		actual.Version == before.Version+1
}

func releasedOperationMatches(
	actual generated.CommerceOperation,
	before generated.CommerceOperation,
	errorCode string,
) bool {
	nextState, recoveryRequiredAt := recoveryStateAt(before, actual.UpdatedAt.Time)
	return operationStaticFieldsMatch(actual, before) && actual.State == nextState &&
		!actual.LeaseID.Valid && !actual.LeaseExpiresAt.Valid &&
		actual.AttemptCount == before.AttemptCount &&
		providerAttemptHistoryMatches(actual, before) &&
		!actual.RecoveryCursor.Valid && !actual.RecoveryCandidateProviderID.Valid &&
		textsEqual(actual.ProviderResourceID, before.ProviderResourceID) &&
		timePointersEqual(actual.ProviderConfirmedAt, before.ProviderConfirmedAt) &&
		timePointersEqual(actual.RecoveryRequiredAt, recoveryRequiredAt) &&
		timePointersEqual(actual.CompletedAt, before.CompletedAt) &&
		exactText(actual.LastErrorCode, errorCode) && actual.Version == before.Version+1 &&
		!actual.UpdatedAt.Time.IsZero()
}

func rotatedIdempotencyOperationMatches(
	actual generated.CommerceOperation,
	before generated.CommerceOperation,
	retryKey string,
) bool {
	nextState, recoveryRequiredAt := recoveryStateAt(before, actual.UpdatedAt.Time)
	return operationFixedFieldsMatch(actual, before) && actual.IdempotencyKey == retryKey &&
		actual.State == nextState && !actual.LeaseID.Valid && !actual.LeaseExpiresAt.Valid &&
		actual.AttemptCount == before.AttemptCount &&
		providerAttemptHistoryMatches(actual, before) &&
		!actual.RecoveryCursor.Valid && !actual.RecoveryCandidateProviderID.Valid &&
		textsEqual(actual.ProviderResourceID, before.ProviderResourceID) &&
		timePointersEqual(actual.ProviderConfirmedAt, before.ProviderConfirmedAt) &&
		timePointersEqual(actual.RecoveryRequiredAt, recoveryRequiredAt) &&
		timePointersEqual(actual.CompletedAt, before.CompletedAt) &&
		exactText(actual.LastErrorCode, "provider_setup_required") &&
		actual.Version == before.Version+1 && !actual.UpdatedAt.Time.IsZero()
}

func providerConfirmationMatches(
	actual generated.CommerceOperation,
	before generated.CommerceOperation,
	providerAccountID string,
) bool {
	return operationStaticFieldsMatch(actual, before) &&
		actual.State == operationStateProviderConfirmed &&
		!actual.LeaseID.Valid && !actual.LeaseExpiresAt.Valid &&
		actual.AttemptCount == before.AttemptCount &&
		providerAttemptHistoryMatches(actual, before) &&
		!actual.RecoveryCursor.Valid && !actual.RecoveryCandidateProviderID.Valid &&
		actual.ProviderResourceID.Valid &&
		actual.ProviderResourceID.String == providerAccountID && actual.ProviderConfirmedAt.Valid &&
		databaseTimesEqual(actual.ProviderConfirmedAt, actual.UpdatedAt) &&
		!actual.RecoveryRequiredAt.Valid && timePointersEqual(actual.CompletedAt, before.CompletedAt) &&
		!actual.LastErrorCode.Valid &&
		actual.Version == before.Version+1
}

func completedOperationMatches(
	actual generated.CommerceOperation,
	before generated.CommerceOperation,
	providerAccountID string,
) bool {
	return operationStaticFieldsMatch(actual, before) && actual.State == operationStateSucceeded &&
		!actual.LeaseID.Valid && !actual.LeaseExpiresAt.Valid &&
		actual.AttemptCount == before.AttemptCount &&
		providerAttemptHistoryMatches(actual, before) &&
		!actual.RecoveryCursor.Valid && !actual.RecoveryCandidateProviderID.Valid &&
		actual.ProviderResourceID.Valid && actual.ProviderResourceID.String == providerAccountID &&
		timePointersEqual(actual.ProviderConfirmedAt, before.ProviderConfirmedAt) &&
		!actual.RecoveryRequiredAt.Valid && !actual.LastErrorCode.Valid &&
		actual.CompletedAt.Valid && databaseTimesEqual(actual.CompletedAt, actual.UpdatedAt) &&
		actual.Version == before.Version+1
}

func observedCompletedRowsMatch(
	actualAccount generated.CommerceAccount,
	actualOperation generated.CommerceOperation,
	observedAccount generated.CommerceAccount,
	observedOperation generated.CommerceOperation,
	providerAccountID string,
) bool {
	return actualAccount.ID == observedAccount.ID &&
		actualAccount.CompanyID == observedAccount.CompanyID &&
		actualAccount.Environment == observedAccount.Environment &&
		actualAccount.ProviderAccountID.Valid &&
		actualAccount.ProviderAccountID.String == providerAccountID &&
		actualAccount.CreatedByUserID == observedAccount.CreatedByUserID &&
		databaseTimesEqual(actualAccount.CreatedAt, observedAccount.CreatedAt) &&
		actualAccount.Version == observedAccount.Version &&
		databaseTimesEqual(actualAccount.UpdatedAt, observedAccount.UpdatedAt) &&
		operationStaticFieldsMatch(actualOperation, observedOperation) &&
		actualOperation.State == operationStateSucceeded &&
		actualOperation.ProviderResourceID.Valid &&
		actualOperation.ProviderResourceID.String == providerAccountID &&
		actualOperation.ProviderConfirmedAt.Valid && actualOperation.CompletedAt.Valid &&
		actualOperation.Version == observedOperation.Version &&
		databaseTimesEqual(actualOperation.UpdatedAt, observedOperation.UpdatedAt)
}

func providerAttemptHistoryMatches(
	actual generated.CommerceOperation,
	expected generated.CommerceOperation,
) bool {
	return actual.ProviderAttemptCount == expected.ProviderAttemptCount &&
		timePointersEqual(actual.ProviderFirstAttemptedAt, expected.ProviderFirstAttemptedAt) &&
		timePointersEqual(actual.ProviderLastAttemptedAt, expected.ProviderLastAttemptedAt)
}

func operationLeaseMatches(
	actual generated.CommerceOperation,
	expected generated.CommerceOperation,
	leaseID uuid.UUID,
) bool {
	return operationStaticFieldsMatch(actual, expected) &&
		actual.State == operationStateLeased && expected.State == operationStateLeased &&
		actual.LeaseID.Valid && expected.LeaseID.Valid &&
		actual.LeaseID.UUID == leaseID &&
		expected.LeaseID.UUID == leaseID &&
		timePointersEqual(actual.LeaseExpiresAt, expected.LeaseExpiresAt) &&
		actual.AttemptCount == expected.AttemptCount &&
		providerAttemptHistoryMatches(actual, expected) &&
		textsEqual(actual.RecoveryCursor, expected.RecoveryCursor) &&
		textsEqual(
			actual.RecoveryCandidateProviderID,
			expected.RecoveryCandidateProviderID,
		) &&
		textsEqual(actual.ProviderResourceID, expected.ProviderResourceID) &&
		timePointersEqual(actual.ProviderConfirmedAt, expected.ProviderConfirmedAt) &&
		timePointersEqual(actual.RecoveryRequiredAt, expected.RecoveryRequiredAt) &&
		timePointersEqual(actual.CompletedAt, expected.CompletedAt) &&
		textsEqual(actual.LastErrorCode, expected.LastErrorCode) &&
		databaseTimesEqual(actual.UpdatedAt, expected.UpdatedAt) &&
		actual.Version == expected.Version
}

func operationStaticFieldsMatch(
	actual generated.CommerceOperation,
	expected generated.CommerceOperation,
) bool {
	return operationFixedFieldsMatch(actual, expected) &&
		actual.IdempotencyKey == expected.IdempotencyKey
}

func operationFixedFieldsMatch(
	actual generated.CommerceOperation,
	expected generated.CommerceOperation,
) bool {
	return actual.ID == expected.ID && actual.CompanyID == expected.CompanyID &&
		actual.CommerceAccountID == expected.CommerceAccountID &&
		actual.Environment == expected.Environment &&
		actual.OperationType == expected.OperationType &&
		actual.MetadataSchemaVersion == expected.MetadataSchemaVersion &&
		bytes.Equal(actual.RequestSha256, expected.RequestSha256) &&
		databaseTimesEqual(actual.ProviderRetryDeadlineAt, expected.ProviderRetryDeadlineAt) &&
		actual.CreatedByUserID == expected.CreatedByUserID &&
		databaseTimesEqual(actual.CreatedAt, expected.CreatedAt)
}

func exactProviderProjectionMatches(
	actual generated.CommerceAccount,
	before generated.CommerceAccount,
	account ProviderAccount,
	projection providerProjection,
) bool {
	return actual.ID == before.ID && actual.CompanyID == before.CompanyID &&
		actual.Environment == before.Environment && actual.ProviderAccountID.Valid &&
		actual.ProviderAccountID.String == account.ID &&
		storedProviderProjectionMatches(actual, account, projection) &&
		actual.Version == before.Version+1 &&
		actual.ReconciliationGeneration == before.ReconciliationGeneration+1
}

func storedProviderProjectionMatches(
	row generated.CommerceAccount,
	account ProviderAccount,
	projection providerProjection,
) bool {
	return row.Status == string(projection.status) && row.Country == projection.country &&
		row.DefaultCurrency == projection.defaultCurrency &&
		row.DetailsSubmitted == account.DetailsSubmitted &&
		row.ChargesEnabled == account.ChargesEnabled && row.PayoutsEnabled == account.PayoutsEnabled &&
		row.CardPaymentsStatus == projection.cardPaymentsStatus &&
		row.TransfersStatus == projection.transfersStatus &&
		row.RequirementsCurrentlyDueCount == int32(account.Requirements.CurrentlyDue) &&
		row.RequirementsEventuallyDueCount == int32(account.Requirements.EventuallyDue) &&
		row.RequirementsPastDueCount == int32(account.Requirements.PastDue) &&
		timePointersEqual(row.RequirementsDeadline, projection.deadline) &&
		row.DisabledReason == projection.disabledReason &&
		exactText(row.ControllerLossesPayments, "stripe") &&
		exactText(row.ControllerFeesPayer, "account") &&
		exactText(row.ControllerRequirementCollection, "stripe") &&
		exactText(row.ControllerDashboardType, "full") &&
		row.ControllerTemplateVersion.Valid &&
		row.ControllerTemplateVersion.Int32 == controllerTemplateVersion &&
		row.LastReconciledAt.Valid &&
		databaseTimesEqual(row.LastReconciledAt, row.UpdatedAt)
}

func boundedFinalizationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), accountFinalizationTimeout)
}

func databaseTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func databaseTimesEqual(left, right any) bool {
	leftTime, leftValid := timestampValue(left)
	rightTime, rightValid := timestampValue(right)
	return leftValid == rightValid && (!leftValid || databaseTime(leftTime).Equal(databaseTime(rightTime)))
}

func timePointersEqual(left, right any) bool {
	return databaseTimesEqual(left, right)
}

func timestampValue(value any) (time.Time, bool) {
	switch typed := value.(type) {
	case time.Time:
		return typed, true
	case *time.Time:
		if typed == nil {
			return time.Time{}, false
		}
		return *typed, true
	case pgtype.Timestamptz:
		return typed.Time, typed.Valid
	default:
		return time.Time{}, false
	}
}

func optionalTimestamp(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func requiredTimestamp(value time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: value.UTC(), Valid: true}
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func textsEqual(left, right pgtype.Text) bool {
	return left.Valid == right.Valid && (!left.Valid || left.String == right.String)
}
