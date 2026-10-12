package account

import "errors"

var (
	ErrUnauthenticated     = errors.New("account authentication is required")
	ErrInvalidConfirmation = errors.New("account deletion confirmation is invalid")
	ErrConflict            = errors.New("account lifecycle conflicts with current state")
	ErrNotFound            = errors.New("account was not found")
)
