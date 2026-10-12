package telemetry

import "errors"

var (
	ErrConfiguration                = errors.New("invalid crash report service configuration")
	ErrInvalidInput                 = errors.New("crash report input is invalid")
	ErrStore                        = errors.New("crash report store is unavailable")
	ErrRetentionWorkerConfiguration = errors.New("invalid crash report retention worker configuration")
)
