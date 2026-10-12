package canvas

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"boardly.dev/boardly/server/pkg/protocol"

	"github.com/google/uuid"
)

var (
	ErrBoardNotFound  = errors.New("whiteboard not found")
	ErrBoardForbidden = errors.New("whiteboard permission required")
	ErrBoardInvalid   = errors.New("invalid whiteboard operation")
	ErrBoardConflict  = errors.New("whiteboard revision conflict")
	ErrBoardLimit     = errors.New("whiteboard limit reached")
	ErrBoardArchived  = errors.New("whiteboard is archived")
	ErrBoardLocked    = errors.New("whiteboard object is locked")
	// ErrBoardItemNotFound is a command naming an item or connector the Board
	// no longer has; the Board itself is still there.
	ErrBoardItemNotFound = errors.New("whiteboard object not found")
	// ErrBoardRequestKeyReused is one request key sent with a different request.
	ErrBoardRequestKeyReused = errors.New("whiteboard request key reused")
)

// BoardInvalidError is an invalid operation with a reason its caller can act
// on, such as the values a field takes; it is ErrBoardInvalid.
type BoardInvalidError struct{ Reason string }

func (e BoardInvalidError) Error() string { return "invalid whiteboard operation: " + e.Reason }

func (e BoardInvalidError) Unwrap() error { return ErrBoardInvalid }

func boardInvalid(format string, args ...any) error {
	return BoardInvalidError{Reason: fmt.Sprintf(format, args...)}
}

// boardJoin lists the values a field takes, for a refusal's reason.
func boardJoin[T ~string](values []T) string {
	names := make([]string, len(values))
	for i, value := range values {
		names[i] = string(value)
	}
	return strings.Join(names, ", ")
}

// Board limits (TECH.md "Initial limits"). Request binding tags repeat the
// batch and selection limits because tags cannot name constants.
const (
	boardMaxItems         = 5000             // live items per Board
	boardMaxEdges         = 5000             // live connectors per Board
	boardMaxCommands      = 1000             // commands in one atomic batch
	boardMaxBatchBytes    = 4 << 20          // encoded batch
	boardMaxSelection     = 100              // items per hydration or selection
	boardMaxStrokePoints  = 4096             // points per stroke
	boardMaxNoteText      = 64 << 10         // note plain text
	boardMaxTextUpdate    = 256 << 10        // one CRDT text update
	boardMaxNoteState     = 1 << 20          // accepted note CRDT state
	boardMaxStateVector   = 64 << 10         // client Yjs state vector
	boardMaxHydration     = 8 << 20          // one hydration response
	boardMaxDelta         = 5000             // change records one delta or replay reads
	boardChangeFeed       = 1000             // change records always retained per Board
	boardChangeFeedCap    = 20000            // change records retained under continuous typing
	boardChangeFeedWindow = 10 * time.Minute // recent commits a reconnecting client catches up on
	boardListPage         = 100              // Boards per Agent list page
	boardMaxTitle         = 200              // runes in a Board title
	boardMaxPurpose       = 2000             // runes in a Board purpose
	boardMaxContextItems  = 500              // items one context or image read may select
)

// BoardScope names the Board and, for machine calls, the Run proved by the transport.
// Membership remains on the canonical UserPrincipal.
type BoardScope struct {
	BoardID uuid.UUID
	RunID   uuid.UUID
}
type BoardAccessInput struct {
	ExpectedRevision int64             `json:"expectedRevision" binding:"required,min=1"`
	Actor            protocol.ActorRef `json:"actor"            binding:"required"`
	Access           string            `json:"access"           binding:"required,oneof=read edit manage none"`
}
type BoardGrant struct {
	Actor  protocol.ActorRef `json:"actor"`
	Access string            `json:"access"`
}
type BoardAccess struct {
	URL           string            `json:"url"`
	Owner         protocol.ActorRef `json:"owner"`
	Permission    string            `json:"permission"`
	Visibility    string            `json:"visibility"`
	CompanyAccess string            `json:"companyAccess"`
	PublicToken   *uuid.UUID        `json:"publicToken"`
	Revision      int64             `json:"revision"`
	Grants        []BoardGrant      `json:"grants"`
}
type BoardClaimInput struct {
	ExpectedRevision int64 `json:"expectedRevision" binding:"required,min=1"`
}
type BoardVisibilityInput struct {
	ExpectedRevision int64  `json:"expectedRevision" binding:"required,min=1"`
	Visibility       string `json:"visibility"       binding:"required,oneof=private company public"`
	CompanyAccess    string `json:"companyAccess"    binding:"required,oneof=read edit"`
}
type BoardViewer struct {
	CanShare         bool              `json:"-"`
	RecipientUserIDs []uuid.UUID       `json:"-"`
	Actor            protocol.ActorRef `json:"actor"`
	RunID            *uuid.UUID        `json:"runId"`
	CanEdit          bool              `json:"-"`
}
type Board struct {
	Visibility string     `json:"visibility"`
	CanShare   bool       `json:"canShare"`
	ID         uuid.UUID  `json:"id"`
	CompanyID  uuid.UUID  `json:"companyId"`
	Title      string     `json:"title"`
	Purpose    string     `json:"purpose"`
	Sequence   int64      `json:"sequence"`
	Revision   int64      `json:"revision"`
	CreatedBy  uuid.UUID  `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	ArchivedAt *time.Time `json:"archivedAt"`
	Favorite   bool       `json:"favorite"`
	CanEdit    bool       `json:"canEdit"`
	CanManage  bool       `json:"canManage"`
}
type BoardListInput struct {
	Search    string
	Archived  bool
	Favorites bool
	// Unowned lists, for Company Whiteboard managers, the Boards no active
	// member manages, so one of them can claim it.
	Unowned bool
	Limit   int32
	Offset  int32
}
type BoardList struct {
	Boards  []Board `json:"boards"`
	HasMore bool    `json:"hasMore"`
}
type CreateBoardInput struct {
	RequestKey uuid.UUID `json:"requestKey" binding:"required"`
	Title      string    `json:"title"      binding:"required,max=200"`
	Purpose    string    `json:"purpose"    binding:"max=2000"`
}
type UpdateBoardInput struct {
	ExpectedRevision int64   `json:"expectedRevision"   binding:"required,min=1"`
	Title            *string `json:"title,omitempty"    binding:"omitempty,max=200"`
	Purpose          *string `json:"purpose,omitempty"  binding:"omitempty,max=2000"`
	Archived         *bool   `json:"archived,omitempty"`
}
type BoardGeometry struct {
	X             float64    `json:"x"`
	Y             float64    `json:"y"`
	Width         float64    `json:"width"`
	Height        float64    `json:"height"`
	Rotation      float64    `json:"rotation"`
	StackOrder    int64      `json:"stackOrder"`
	ParentFrameID *uuid.UUID `json:"parentFrameId"`
	ReadingOrder  int64      `json:"readingOrder"`
}
type BoardProperties struct {
	Title        string         `json:"title,omitempty"`
	Color        string         `json:"color,omitempty"`
	Fill         string         `json:"fill,omitempty"`
	StrokeWidth  float64        `json:"strokeWidth,omitempty"`
	Shape        BoardShapeType `json:"shape,omitempty"`
	Brush        string         `json:"brush,omitempty"`
	Locked       bool           `json:"locked"`
	Role         string         `json:"role,omitempty"`
	Caption      string         `json:"caption,omitempty"`
	Alt          string         `json:"alt,omitempty"`
	AttachmentID *uuid.UUID     `json:"attachmentId,omitempty"`
	// Sections are the compartments below a class's, entity's or fragment's
	// title, one entry per line; Dividers are a fragment's operand lines,
	// measured from its top. See board_shape.go.
	Sections []string  `json:"sections,omitempty"`
	Dividers []float64 `json:"dividers,omitempty"`
	// Head draws a lifeline's head: a box when empty, else an actor's figure
	// over its name.
	Head BoardHeadType `json:"head,omitempty"`
}
type BoardPoint struct {
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Pressure float64 `json:"pressure,omitempty"`
}
type BoardItem struct {
	BoardGeometry
	ID                uuid.UUID         `json:"id"`
	Type              string            `json:"type"`
	Properties        BoardProperties   `json:"properties"`
	GeometryRevision  int64             `json:"geometryRevision"`
	ContentRevision   int64             `json:"contentRevision"`
	TextRevision      int64             `json:"textRevision"`
	LifecycleRevision int64             `json:"lifecycleRevision"`
	Text              string            `json:"text"`
	TextTruncated     bool              `json:"textTruncated"`
	CreatedBy         protocol.ActorRef `json:"createdBy"`
	UpdatedBy         protocol.ActorRef `json:"updatedBy"`
	CreatedByRunID    *uuid.UUID        `json:"createdByRunId,omitempty"`
	UpdatedByRunID    *uuid.UUID        `json:"updatedByRunId,omitempty"`
	Deleted           bool              `json:"deleted"`
	File              *BoardFile        `json:"file,omitempty"`
}
type BoardEdge struct {
	ID       uuid.UUID `json:"id"`
	SourceID uuid.UUID `json:"sourceId"`
	TargetID uuid.UUID `json:"targetId"`
	Label    string    `json:"label"`
	Relation string    `json:"relation"`
	// The connector's notation; see board_connector.go.
	Line        BoardLineType   `json:"line"`
	StartMarker BoardMarkerType `json:"startMarker"`
	EndMarker   BoardMarkerType `json:"endMarker"`
	Route       BoardRouteType  `json:"route"`
	// AnchorY is a message's row below its source's top; nil on other routes.
	AnchorY        *float64          `json:"anchorY"`
	SourceLabel    string            `json:"sourceLabel"`
	TargetLabel    string            `json:"targetLabel"`
	Revision       int64             `json:"revision"`
	CreatedBy      protocol.ActorRef `json:"createdBy"`
	UpdatedBy      protocol.ActorRef `json:"updatedBy"`
	CreatedByRunID *uuid.UUID        `json:"createdByRunId,omitempty"`
	UpdatedByRunID *uuid.UUID        `json:"updatedByRunId,omitempty"`
	Deleted        bool              `json:"deleted"`
}
type BoardSnapshot struct {
	Board    Board       `json:"board"`
	Sequence int64       `json:"sequence"`
	Full     bool        `json:"full"`
	Items    []BoardItem `json:"items"`
	Edges    []BoardEdge `json:"edges"`
	Removed  []uuid.UUID `json:"removed"`
}
type BoardHydrateInput struct {
	ItemIDs []uuid.UUID `json:"itemIds" binding:"required,min=1,max=100,dive,required"`
	// StateVectors names the note state a client already holds, so the answer
	// carries only the missing CRDT update instead of the whole note. Keys are
	// item IDs.
	StateVectors map[string][]byte `json:"stateVectors,omitempty"`
}
type BoardHydratedItem struct {
	LifecycleRevision int64        `json:"lifecycleRevision"`
	ID                uuid.UUID    `json:"id"`
	Text              string       `json:"text"`
	TextRevision      int64        `json:"textRevision"`
	State             []byte       `json:"state,omitempty"`
	Delta             bool         `json:"delta,omitempty"`
	Points            []BoardPoint `json:"points,omitempty"`
}
type BoardHydration struct {
	Sequence int64               `json:"sequence"`
	Items    []BoardHydratedItem `json:"items"`
	// Missing names requested IDs that are no longer live items on the Board.
	Missing []uuid.UUID `json:"missing"`
}
type BoardCommand struct {
	Type                     string           `json:"type"                               binding:"required"`
	DraftID                  string           `json:"draftId,omitempty"`
	ID                       *uuid.UUID       `json:"id,omitempty"`
	ItemType                 string           `json:"itemType,omitempty"`
	ExpectedRevision         int64            `json:"expectedRevision,omitempty"`
	ExpectedGeometryRevision int64            `json:"expectedGeometryRevision,omitempty"`
	ExpectedTextRevision     int64            `json:"expectedTextRevision,omitempty"`
	Geometry                 *BoardGeometry   `json:"geometry,omitempty"`
	Properties               *BoardProperties `json:"properties,omitempty"`
	Text                     string           `json:"text,omitempty"`
	Points                   []BoardPoint     `json:"points,omitempty"`
	ParentRef                string           `json:"parentRef,omitempty"`
	SourceRef                string           `json:"sourceRef,omitempty"`
	TargetRef                string           `json:"targetRef,omitempty"`
	Label                    string           `json:"label,omitempty"`
	Relation                 string           `json:"relation,omitempty"`
	// Connector notation. create_edge starts from the Board's usual arrow;
	// update_edge changes only the parts it names.
	Line        BoardLineType   `json:"line,omitempty"`
	StartMarker BoardMarkerType `json:"startMarker,omitempty"`
	EndMarker   BoardMarkerType `json:"endMarker,omitempty"`
	Route       BoardRouteType  `json:"route,omitempty"`
	AnchorY     *float64        `json:"anchorY,omitempty"`
	SourceLabel *string         `json:"sourceLabel,omitempty"`
	TargetLabel *string         `json:"targetLabel,omitempty"`
}
type BoardBatch struct {
	RequestKey uuid.UUID      `json:"requestKey" binding:"required"`
	Commands   []BoardCommand `json:"commands"   binding:"required,min=1,max=1000,dive"`
}
type BoardMutation struct {
	Sequence int64                `json:"sequence"`
	IDs      map[string]uuid.UUID `json:"ids"`
	Items    []BoardItem          `json:"items"`
	Edges    []BoardEdge          `json:"edges"`
	Removed  []uuid.UUID          `json:"removed"`
	// Replayed answers a request key that was already committed.
	Replayed bool `json:"replayed"`
}
type BoardTextInput struct {
	LifecycleRevision int64     `json:"lifecycleRevision"     binding:"required,min=1"`
	RequestKey        uuid.UUID `json:"requestKey"            binding:"required"`
	ItemID            uuid.UUID `json:"itemId"                binding:"required"`
	Update            []byte    `json:"update"                binding:"required"`
	StateVector       []byte    `json:"stateVector,omitempty"`
}
type BoardTextResult struct {
	Sequence     int64     `json:"sequence"`
	ItemID       uuid.UUID `json:"itemId"`
	TextRevision int64     `json:"textRevision"`
	Text         string    `json:"text"`
	Update       []byte    `json:"update"`
}
type BoardContextInput struct {
	ItemIDs          []uuid.UUID `json:"itemIds,omitempty"`
	MaxItems         int         `json:"maxItems,omitempty"`
	MaxBytes         int         `json:"maxBytes,omitempty"`
	IncludeVisuals   bool        `json:"includeVisuals,omitempty"`
	VisualOffset     int         `json:"visualOffset,omitempty"     binding:"min=0"`
	ExpectedSequence *int64      `json:"expectedSequence,omitempty" binding:"omitempty,min=0"`
	// ExpectedPlan is the first page's planHash; a later page reports whether
	// the region plan moved.
	ExpectedPlan string `json:"expectedPlan,omitempty" binding:"max=64"`
}
type BoardContext struct {
	OmittedCount        int           `json:"omittedCount"`
	OmittedIDsTruncated bool          `json:"omittedIdsTruncated"`
	Board               Board         `json:"board"`
	Sequence            int64         `json:"sequence"`
	Items               []BoardItem   `json:"items"`
	Edges               []BoardEdge   `json:"edges"`
	OmittedIDs          []uuid.UUID   `json:"omittedIds"`
	Truncated           bool          `json:"truncated"`
	Markdown            string        `json:"markdown"`
	Visuals             *BoardVisuals `json:"visuals,omitempty"`
}

// Board change records retain identifiers, not text or gesture payloads. Receipts
// keep created IDs independently, so delta compaction cannot make an
// acknowledged create repeat; a replay reads the touched objects from here.
type boardChanges struct {
	Items   []uuid.UUID `json:"items"`
	Edges   []uuid.UUID `json:"edges"`
	Removed []uuid.UUID `json:"removed"`
}
