package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/util"
)

func uuidToString(u uuid.UUID) string { return u.String() }

// parseUUIDOrBadRequest validates a UUID string sourced from user input
// (URL params, request bodies) and writes a 422 when it is malformed. Use
// this anywhere a malformed UUID would otherwise reach a write query — the
// silent zero-UUID behavior is exactly the data-loss bug it exists to prevent.
//
// It parses through util.ParseGoUUID rather than util.ParseUUID because pgtype
// accepts 00000000-0000-0000-0000-000000000000 and a malformed RFC-4122
// variant. An all-zero id is not a value any caller means, and it reads as a
// real one for the rest of the request.
func parseUUIDOrBadRequest(c *gin.Context, s, fieldName string) (uuid.UUID, bool) {
	id, err := util.ParseGoUUID(s)
	if err != nil {
		writeBoundaryErrorDetail(c, boundaryErrorValidationFailed, fieldName+" is not a valid uuid")
		return uuid.Nil, false
	}
	return id, true
}
