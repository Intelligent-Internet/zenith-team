package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/activity"
	"boardly.dev/boardly/server/internal/util"
)

// Activity v4 binds the position to Company, reader, normalized filters and
// ordering. The digest prevents accidental cursor reuse, not authorization;
// every database read independently checks membership and retained visibility.
func activityFilterDigest(c *gin.Context, options activity.ListOptions) [32]byte {
	filters, _ := json.Marshal([]any{
		c.Param("companyId"), options.UserID, options.EntityType, options.EntityID,
		options.ActorType, options.ActorID, options.Action, options.From, options.To,
		options.Revision, options.BeforeRevision,
	})
	return sha256.Sum256(filters)
}

func encodeActivityCursor(c *gin.Context, options activity.ListOptions, cursor *activity.Cursor) string {
	digest := activityFilterDigest(c, options)
	record := make([]byte, 49)
	record[0] = 4
	copy(record[1:17], digest[:16])
	binary.BigEndian.PutUint64(record[17:25], uint64(cursor.CreatedAt.UnixMicro()))
	copy(record[25:41], cursor.ID[:])
	binary.BigEndian.PutUint64(record[41:49], uint64(cursor.Revision))
	return base64.RawURLEncoding.EncodeToString(record)
}

func decodeActivityCursor(c *gin.Context, options activity.ListOptions, raw string) (*activity.Cursor, bool) {
	if len(raw) > 100 {
		return nil, false
	}
	record, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(record) != 49 || record[0] != 4 {
		return nil, false
	}
	digest := activityFilterDigest(c, options)
	if !bytes.Equal(record[1:17], digest[:16]) {
		return nil, false
	}
	micros, revision := binary.BigEndian.Uint64(record[17:25]), binary.BigEndian.Uint64(record[41:49])
	if micros == 0 || micros > ^uint64(0)>>1 || revision > 9007199254740991 {
		return nil, false
	}
	if (options.Revision != nil || options.BeforeRevision != nil) && revision == 0 {
		return nil, false
	}
	var id uuid.UUID
	copy(id[:], record[25:41])
	if !util.ValidUUID(id) {
		return nil, false
	}
	return &activity.Cursor{ID: id, CreatedAt: time.UnixMicro(int64(micros)).UTC(), Revision: int64(revision)}, true
}
