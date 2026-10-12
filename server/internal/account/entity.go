package account

import (
	"time"

	"github.com/google/uuid"
)

const Confirmation = "DELETE"

type DeleteResult struct {
	DeletedAt time.Time
}

type DeletionAuthority struct {
	UserID  uuid.UUID
	Version int64
}
