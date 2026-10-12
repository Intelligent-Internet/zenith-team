// This file holds the topic, the event names, and the payloads that exist
// only to be published. A payload the REST surface also serves lives with the
// rest of the domain's types; the event carries that projection, it does not
// own it.
//
// Who may see an event lives in cmd/server/realtime_routing.go — a comment
// here says what fact an event records, never who receives it, because a rule
// written in two places is a rule that drifts.

package inbox

import (
	"github.com/google/uuid"

	"boardly.dev/boardly/server/pkg/protocol"
)

// Topic carries an item and the recipient's fresh counts, so the badge moves
// exactly when an item does and there is no counts-only event.
const Topic = "inbox"

const (
	// EventItemCreated records a new item landing in someone's inbox.
	EventItemCreated = "inbox.created"

	// EventItemUpdated records a change to an item already there.
	EventItemUpdated = "inbox.updated"
)

// ItemWithCounts is one item plus the recipient's resulting counts. Its
// routing facts are Item's own CompanyID and RecipientUserID.
type ItemWithCounts struct {
	Item
	Counts Counts `json:"counts"`
}

// TopicNotification is the transient presentation hint that rides alongside a
// freshly created item. Losing one loses no product state.
const TopicNotification = "notification"

// EventNotificationIssued records that an item is worth surfacing right now.
const EventNotificationIssued = "notification.issued"

// ToastHint is the presentation hint: enough to render a toast,
// and nothing that would let it be mistaken for durable state.
type ToastHint struct {
	NotificationID uuid.UUID           `json:"notificationId"`
	Type           Type                `json:"kind"`
	EntityType     protocol.EntityType `json:"-"`
	Title          string              `json:"title"`
	Body           string              `json:"body"`
	DeepLink       *string             `json:"deepLink,omitempty"`

	// CompanyID and RecipientUserID are the routing facts: this hint is for
	// one person, in one company. The bridge reads them off the Go value and
	// they stay off the wire, exactly as the item frames' recipient does.
	CompanyID       uuid.UUID `json:"-"`
	RecipientUserID uuid.UUID `json:"-"`
}
