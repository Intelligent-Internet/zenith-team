package attachment

import "errors"

var (
	ErrInvalidInput                = errors.New("invalid attachment input")
	ErrConflict                    = errors.New("attachment conflict")
	ErrNotFound                    = errors.New("attachment not found")
	ErrUploadIncomplete            = errors.New("attachment upload is incomplete")
	ErrUploadExpired               = errors.New("attachment upload expired")
	ErrIntegrityMismatch           = errors.New("attachment content does not match its metadata")
	ErrStorageUnavailable          = errors.New("attachment storage is unavailable")
	ErrUnavailable                 = errors.New("attachment service is unavailable")
	ErrInvalidServiceConfiguration = errors.New("invalid attachment service configuration")
	ErrCleanupWorkerConfiguration  = errors.New("invalid Attachment cleanup worker configuration")
)
