// The topic this domain publishes, its event names, and the payloads that
// exist only to be published.
//
// A payload the REST surface also serves stays with the domain's other types:
// the event carries the domain's projection, it does not own it. activity.created
// carries Entry, the feed read's own projection, so Entry lives in reads.go.
// Who may see an event lives in cmd/server/realtime_routing.go — a comment
// here says what fact an event records, never who receives it, because a rule
// written in two places is a rule that drifts.

package activity

// Topic is the activity family. Every fact this domain publishes travels on
// it, and a consumer subscribes once and switches on the event name.
const Topic = "activity"

// EventCreated is the one activity.* wire name. It carries one appended feed
// row, in the same Entry projection the feed read returns.
const EventCreated = "activity.created"
