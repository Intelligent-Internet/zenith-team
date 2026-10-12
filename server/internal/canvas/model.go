package canvas

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

var (
	ErrNotFound          = errors.New("canvas target not found")
	ErrForbidden         = errors.New("canvas operation forbidden")
	ErrInvalid           = errors.New("invalid canvas operation")
	ErrPageInvalidInsert = errors.New("invalid page insertion")
	ErrConflict          = errors.New("canvas revision conflicts with current state")
	ErrLimit             = errors.New("canvas limit reached")
)

type Command struct {
	WritingTarget    *DocumentWritingTarget `json:"writingTarget,omitempty"`
	AgentIDs         []uuid.UUID            `json:"agentIds,omitempty" binding:"omitempty,max=5,dive,required"`
	PageID           *uuid.UUID             `json:"pageId,omitempty"`
	Content          json.RawMessage        `json:"content,omitempty"`
	RequestKey       uuid.UUID              `json:"requestKey"                 binding:"required"`
	Type             string                 `json:"type"                       binding:"required"`
	ID               *uuid.UUID             `json:"id,omitempty"`
	ArtifactID       *uuid.UUID             `json:"artifactId,omitempty"`
	TaskID           *uuid.UUID             `json:"taskId,omitempty"`
	VersionID        *uuid.UUID             `json:"versionId,omitempty"`
	ThreadID         *uuid.UUID             `json:"threadId,omitempty"`
	AttachmentID     *uuid.UUID             `json:"attachmentId,omitempty"`
	OutputID         *uuid.UUID             `json:"outputId,omitempty"`
	OutputItemID     *uuid.UUID             `json:"outputItemId,omitempty"`
	WikiPageID       *uuid.UUID             `json:"wikiPageId,omitempty"`
	WikiRevisionID   *uuid.UUID             `json:"wikiRevisionId,omitempty"`
	ParentFrameID    *uuid.UUID             `json:"parentFrameId,omitempty"`
	SourceID         *uuid.UUID             `json:"sourceId,omitempty"`
	TargetID         *uuid.UUID             `json:"targetId,omitempty"`
	ExpectedRevision int64                  `json:"expectedRevision,omitempty"`
	Title            string                 `json:"title,omitempty"`
	Body             string                 `json:"body,omitempty"`
	Action           string                 `json:"action,omitempty"`
	Representation   string                 `json:"representation,omitempty"`
	VersionPolicy    string                 `json:"versionPolicy,omitempty"`
	X                float64                `json:"x,omitempty"`
	Y                float64                `json:"y,omitempty"`
	Width            float64                `json:"width,omitempty"`
	Height           float64                `json:"height,omitempty"`
	Moves            []Move                 `json:"moves,omitempty"`
	ViewConfig       json.RawMessage        `json:"viewConfig,omitempty"`
	Anchor           json.RawMessage        `json:"anchor,omitempty"`
}
type Move struct {
	ID               uuid.UUID `json:"id"`
	ExpectedRevision int64     `json:"expectedRevision"`
	X                float64   `json:"x"`
	Y                float64   `json:"y"`
	Width            float64   `json:"width"`
	Height           float64   `json:"height"`
}
type Result struct {
	ID       uuid.UUID `json:"id"`
	Sequence int64     `json:"sequence"`
}
type Changes struct {
	Items            []uuid.UUID `json:"items"`
	Artifacts        []uuid.UUID `json:"artifacts"`
	Edges            []uuid.UUID `json:"edges"`
	Removed          []uuid.UUID `json:"removed"`
	ReviewVersionIDs []uuid.UUID `json:"reviewVersionIds"`
}
type Item struct {
	Version          *Version        `json:"version"`
	ID               uuid.UUID       `json:"id"`
	Type             string          `json:"type"`
	ArtifactID       *uuid.UUID      `json:"artifactId"`
	PinnedVersionID  *uuid.UUID      `json:"pinnedVersionId"`
	VersionPolicy    string          `json:"versionPolicy"`
	ParentFrameID    *uuid.UUID      `json:"parentFrameId"`
	Title            string          `json:"title"`
	X                float64         `json:"x"`
	Y                float64         `json:"y"`
	Width            float64         `json:"width"`
	Height           float64         `json:"height"`
	StackOrder       int64           `json:"stackOrder"`
	Representation   string          `json:"representation"`
	ViewConfig       json.RawMessage `json:"viewConfig"`
	GeometryRevision int64           `json:"geometryRevision"`
	ContentRevision  int64           `json:"contentRevision"`
}
type Version struct {
	OutputItemID   *uuid.UUID `json:"outputItemId,omitempty"`
	BranchTitle    string     `json:"branchTitle"`
	OpenComments   int64      `json:"openComments"`
	ID             uuid.UUID  `json:"id"`
	ArtifactID     uuid.UUID  `json:"artifactId"`
	Number         int64      `json:"number"`
	SourceType     string     `json:"sourceType"`
	WikiPageID     *uuid.UUID `json:"wikiPageId"`
	WikiRevisionID *uuid.UUID `json:"wikiRevisionId"`
	AssetVersionID *uuid.UUID `json:"assetVersionId"`
	OutputID       *uuid.UUID `json:"outputId"`
	SourceOutputID *uuid.UUID `json:"sourceOutputId"`
	RunID          *uuid.UUID `json:"runId"`
	AttachmentID   *uuid.UUID `json:"attachmentId"`
	Filename       *string    `json:"filename"`
	ContentType    *string    `json:"contentType"`
	SizeBytes      *int64     `json:"sizeBytes"`
	Excerpt        string     `json:"excerpt"`
	CreatedAt      time.Time  `json:"createdAt"`
	BaseVersionID  *uuid.UUID `json:"baseVersionId"`
}
type Artifact struct {
	PageID            *uuid.UUID `json:"pageId"`
	TaskID            *uuid.UUID `json:"taskId"`
	CreatorName       string     `json:"creatorName"`
	ReviewState       string     `json:"reviewState"`
	ResultStatus      *string    `json:"resultStatus,omitempty"`
	ID                uuid.UUID  `json:"id"`
	Title             string     `json:"title"`
	Revision          int64      `json:"revision"`
	AcceptedVersionID *uuid.UUID `json:"acceptedVersionId"`
	LatestVersionID   uuid.UUID  `json:"latestVersionId"`
	LatestVersion     Version    `json:"latestVersion"`
	OpenComments      int64      `json:"openComments"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}
type Edge struct {
	ID       uuid.UUID `json:"id"`
	Source   uuid.UUID `json:"source"`
	Target   uuid.UUID `json:"target"`
	Label    string    `json:"label"`
	Revision int64     `json:"revision"`
}
type Snapshot struct {
	ArtifactIDs      []uuid.UUID `json:"artifactIds"`
	ReviewVersionIDs []uuid.UUID `json:"reviewVersionIds"`
	CanvasID         uuid.UUID   `json:"canvasId"`
	Sequence         int64       `json:"sequence"`
	Full             bool        `json:"full"`
	Items            []Item      `json:"items"`
	Artifacts        []Artifact  `json:"artifacts"`
	Edges            []Edge      `json:"edges"`
	Removed          []uuid.UUID `json:"removed"`
	CanEdit          bool        `json:"canEdit"`
	CanManageFiles   bool        `json:"canManageFiles"`
	CanEditDocuments bool        `json:"canEditDocuments"`
}
type Comment struct {
	AuthorAgentID *uuid.UUID `json:"authorAgentId,omitempty"`
	RunID         *uuid.UUID `json:"runId,omitempty"`
	ID            uuid.UUID  `json:"id"`
	Body          string     `json:"body"`
	Revision      int64      `json:"revision"`
	AuthorID      uuid.UUID  `json:"authorId"`
	AuthorName    string     `json:"authorName"`
	CreatedAt     time.Time  `json:"createdAt"`
	EditedAt      *time.Time `json:"editedAt"`
	Deleted       bool       `json:"deleted"`
}
type DocumentAgentRun struct {
	AgentID   uuid.UUID `json:"agentId"`
	AgentName string    `json:"agentName"`
	RunID     uuid.UUID `json:"runId"`
	Status    string    `json:"status"`
}

type Thread struct {
	AgentRuns []DocumentAgentRun `json:"agentRuns,omitempty"`
	PageID    *uuid.UUID         `json:"pageId,omitempty"`
	ID        uuid.UUID          `json:"id"`
	VersionID uuid.UUID          `json:"versionId"`
	Anchor    json.RawMessage    `json:"anchor"`
	Status    string             `json:"status"`
	Revision  int64              `json:"revision"`
	CreatedAt time.Time          `json:"createdAt"`
	Comments  []Comment          `json:"comments"`
}
type Decision struct {
	ID        uuid.UUID `json:"id"`
	VersionID uuid.UUID `json:"versionId"`
	Action    string    `json:"action"`
	Feedback  string    `json:"feedback"`
	ActorName string    `json:"actorName"`
	CreatedAt time.Time `json:"createdAt"`
}
type History struct {
	Versions  []Version  `json:"versions"`
	Decisions []Decision `json:"decisions"`
}
type Document struct {
	SchemaVersion     int             `json:"schemaVersion"`
	Content           json.RawMessage `json:"content,omitempty"`
	PageID            uuid.UUID       `json:"pageId"`
	Sequence          int64           `json:"sequence"`
	StructureSequence int64           `json:"structureSequence"`
	StateVector       []byte          `json:"stateVector,omitempty"`
	State             []byte          `json:"state"`
	Markdown          string          `json:"markdown"`
	CanEdit           bool            `json:"canEdit"`
}

type TimelineEntry struct {
	ID            uuid.UUID `json:"id"`
	ArtifactID    uuid.UUID `json:"artifactId"`
	VersionID     uuid.UUID `json:"versionId"`
	VersionNumber int64     `json:"versionNumber"`
	Title         string    `json:"title"`
	Action        string    `json:"action"`
	Feedback      string    `json:"feedback"`
	ActorName     string    `json:"actorName"`
	CreatedAt     time.Time `json:"createdAt"`
}
type TimelinePage struct {
	Items      []TimelineEntry `json:"items"`
	NextCursor *string         `json:"nextCursor"`
}
