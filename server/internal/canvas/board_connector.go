package canvas

import (
	"cmp"
	"math"
	"slices"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// BoardLineType draws a connector solid or dashed.
type BoardLineType string

const (
	BoardLineTypeSolid  BoardLineType = "solid"
	BoardLineTypeDashed BoardLineType = "dashed"
)

// BoardMarkerType is what a connector shows at one end: an arrow, a UML
// triangle or diamond, or an entity relationship's cardinality.
type BoardMarkerType string

const (
	BoardMarkerTypeNone BoardMarkerType = "none"
	// BoardMarkerTypeArrow is a filled arrowhead, the Board's usual end.
	BoardMarkerTypeArrow BoardMarkerType = "arrow"
	// BoardMarkerTypeOpen is an open V, for dependencies and async calls.
	BoardMarkerTypeOpen BoardMarkerType = "open"
	// BoardMarkerTypeTriangle is a hollow triangle: inheritance.
	BoardMarkerTypeTriangle BoardMarkerType = "triangle"
	// BoardMarkerTypeDiamond is a hollow diamond: aggregation.
	BoardMarkerTypeDiamond BoardMarkerType = "diamond"
	// BoardMarkerTypeDiamondFilled is a filled diamond: composition.
	BoardMarkerTypeDiamondFilled BoardMarkerType = "diamond_filled"
	// The crow's-foot cardinalities: exactly one, zero or one, one or more,
	// zero or more.
	BoardMarkerTypeOne      BoardMarkerType = "one"
	BoardMarkerTypeZeroOne  BoardMarkerType = "zero_one"
	BoardMarkerTypeMany     BoardMarkerType = "many"
	BoardMarkerTypeZeroMany BoardMarkerType = "zero_many"
)

// BoardRouteType is the path a connector takes between its ends.
type BoardRouteType string

const (
	// BoardRouteTypeFlow leaves the source's right side for the target's left
	// side, as the Board always drew connectors.
	BoardRouteTypeFlow BoardRouteType = "flow"
	// BoardRouteTypeStraight joins the two boxes along the line between their
	// centers.
	BoardRouteTypeStraight BoardRouteType = "straight"
	// BoardRouteTypeElbow turns once between the facing sides of the boxes.
	BoardRouteTypeElbow BoardRouteType = "elbow"
	// BoardRouteTypeMessage runs straight across at AnchorY below the source's
	// top: a sequence diagram's message, and the one route that may return to
	// its source.
	BoardRouteTypeMessage BoardRouteType = "message"
)

var (
	boardLineTypes   = []BoardLineType{BoardLineTypeSolid, BoardLineTypeDashed}
	boardMarkerTypes = []BoardMarkerType{
		BoardMarkerTypeNone, BoardMarkerTypeArrow, BoardMarkerTypeOpen, BoardMarkerTypeTriangle,
		BoardMarkerTypeDiamond, BoardMarkerTypeDiamondFilled, BoardMarkerTypeOne, BoardMarkerTypeZeroOne,
		BoardMarkerTypeMany, BoardMarkerTypeZeroMany,
	}
	boardRouteTypes = []BoardRouteType{
		BoardRouteTypeFlow, BoardRouteTypeStraight, BoardRouteTypeElbow, BoardRouteTypeMessage,
	}
)

// boardEdgeMaxAnchor bounds a message's row, which lies within an item.
const boardEdgeMaxAnchor = 1e6

// boardEdgeStyle is a connector's notation, apart from its label.
type boardEdgeStyle struct {
	Line        BoardLineType
	Start, End  BoardMarkerType
	Route       BoardRouteType
	AnchorY     *float64
	SourceLabel string
	TargetLabel string
}

// boardDefaultEdgeStyle is how the Board draws a connector nobody styled.
func boardDefaultEdgeStyle() boardEdgeStyle {
	return boardEdgeStyle{
		Line:  BoardLineTypeSolid,
		Start: BoardMarkerTypeNone,
		End:   BoardMarkerTypeArrow,
		Route: BoardRouteTypeFlow,
	}
}

func boardEdgeStyleOf(row generated.BoardEdge) boardEdgeStyle {
	style := boardEdgeStyle{
		Line: BoardLineType(
			row.Line,
		),
		Start:       BoardMarkerType(row.StartMarker),
		End:         BoardMarkerType(row.EndMarker),
		Route:       BoardRouteType(row.Route),
		SourceLabel: row.SourceLabel,
		TargetLabel: row.TargetLabel,
	}
	style.AnchorY = boardAnchorValue(row.AnchorY)
	return style
}

// with applies the notation a command names; what it leaves out stays.
func (s boardEdgeStyle) with(c BoardCommand) boardEdgeStyle {
	if c.Line != "" {
		s.Line = c.Line
	}
	if c.StartMarker != "" {
		s.Start = c.StartMarker
	}
	if c.EndMarker != "" {
		s.End = c.EndMarker
	}
	if c.Route != "" {
		s.Route = c.Route
	}
	if c.AnchorY != nil {
		anchor := *c.AnchorY
		s.AnchorY = &anchor
	}
	if c.SourceLabel != nil {
		s.SourceLabel = *c.SourceLabel
	}
	if c.TargetLabel != nil {
		s.TargetLabel = *c.TargetLabel
	}
	return s
}

// check settles a connector's route: a message needs its row and only a
// message may return to its source; other routes keep no row.
func (s boardEdgeStyle) check(self bool) (boardEdgeStyle, error) {
	if s.Route != BoardRouteTypeMessage {
		s.AnchorY = nil
		if self {
			return s, ErrBoardInvalid
		}
		return s, nil
	}
	if s.AnchorY == nil {
		return s, ErrBoardInvalid
	}
	return s, nil
}

// normalizeBoardEdgeNotation checks the notation a command names.
func normalizeBoardEdgeNotation(c BoardCommand) error {
	if c.Line != "" && !slices.Contains(boardLineTypes, c.Line) {
		return boardInvalid("line is %s", boardJoin(boardLineTypes))
	}
	if c.StartMarker != "" && !slices.Contains(boardMarkerTypes, c.StartMarker) ||
		c.EndMarker != "" && !slices.Contains(boardMarkerTypes, c.EndMarker) {
		return boardInvalid("a marker is %s", boardJoin(boardMarkerTypes))
	}
	if c.Route != "" && !slices.Contains(boardRouteTypes, c.Route) {
		return boardInvalid("route is %s", boardJoin(boardRouteTypes))
	}
	if a := c.AnchorY; a != nil && (math.IsNaN(*a) || *a < 0 || *a > boardEdgeMaxAnchor) {
		return boardInvalid("anchorY is 0 to %g", float64(boardEdgeMaxAnchor))
	}
	for _, label := range []*string{c.SourceLabel, c.TargetLabel} {
		// An end label is a multiplicity or a role.
		if label != nil && utf8.RuneCountInString(*label) > 60 {
			return boardInvalid("an end label is at most 60 characters")
		}
	}
	return nil
}

func boardAnchorParam(anchor *float64) pgtype.Float8 {
	if anchor == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *anchor, Valid: true}
}

func boardAnchorValue(anchor pgtype.Float8) *float64 {
	if !anchor.Valid {
		return nil
	}
	value := anchor.Float64
	return &value
}

// boardConnectorPreset is a named connector notation Agents ask for: the
// line and markers it draws, the relation it records and, for use case
// links, the stereotype it shows when it has no label.
type boardConnectorPreset struct {
	line       BoardLineType
	start, end BoardMarkerType
	relation   string
	label      string
}

var boardConnectorPresets = map[string]boardConnectorPreset{
	"arrow":      {BoardLineTypeSolid, BoardMarkerTypeNone, BoardMarkerTypeArrow, "", ""},
	"line":       {BoardLineTypeSolid, BoardMarkerTypeNone, BoardMarkerTypeNone, "", ""},
	"both":       {BoardLineTypeSolid, BoardMarkerTypeArrow, BoardMarkerTypeArrow, "", ""},
	"dashed":     {BoardLineTypeDashed, BoardMarkerTypeNone, BoardMarkerTypeArrow, "", ""},
	"inherits":   {BoardLineTypeSolid, BoardMarkerTypeNone, BoardMarkerTypeTriangle, "inherits", ""},
	"implements": {BoardLineTypeDashed, BoardMarkerTypeNone, BoardMarkerTypeTriangle, "implements", ""},
	"composes":   {BoardLineTypeSolid, BoardMarkerTypeDiamondFilled, BoardMarkerTypeNone, "composes", ""},
	"aggregates": {BoardLineTypeSolid, BoardMarkerTypeDiamond, BoardMarkerTypeNone, "aggregates", ""},
	"depends":    {BoardLineTypeDashed, BoardMarkerTypeNone, BoardMarkerTypeOpen, "depends_on", ""},
	"associates": {BoardLineTypeSolid, BoardMarkerTypeNone, BoardMarkerTypeOpen, "associates", ""},
	"includes":   {BoardLineTypeDashed, BoardMarkerTypeNone, BoardMarkerTypeOpen, "includes", "«include»"},
	"extends":    {BoardLineTypeDashed, BoardMarkerTypeNone, BoardMarkerTypeOpen, "extends", "«extend»"},
	"sync":       {BoardLineTypeSolid, BoardMarkerTypeNone, BoardMarkerTypeArrow, "", ""},
	"async":      {BoardLineTypeSolid, BoardMarkerTypeNone, BoardMarkerTypeOpen, "", ""},
	"reply":      {BoardLineTypeDashed, BoardMarkerTypeNone, BoardMarkerTypeOpen, "", ""},
}

// boardSequenceStyles are the messages a sequence draws.
var boardSequenceStyles = map[string]bool{"": true, "sync": true, "async": true, "reply": true}

// boardCardinalities are an entity relationship end's crow's feet.
var boardCardinalities = map[string]BoardMarkerType{
	"1": BoardMarkerTypeOne, "0..1": BoardMarkerTypeZeroOne, "1..*": BoardMarkerTypeMany,
	"0..*": BoardMarkerTypeZeroMany, "*": BoardMarkerTypeZeroMany,
}

// boardComposeConnector is the create_edge command for a composed connector:
// its preset's notation (the default when style is empty), and its ends as
// crow's feet between entities or as multiplicity labels otherwise.
func boardComposeConnector(
	draft, from, to, label, style, fromEnd, toEnd string,
	fallback string,
	route BoardRouteType,
	entities bool,
) BoardCommand {
	if style == "" {
		style = fallback
	}
	preset := boardConnectorPresets[style]
	c := BoardCommand{
		Type:        "create_edge",
		DraftID:     draft,
		SourceRef:   from,
		TargetRef:   to,
		Label:       label,
		Relation:    preset.relation,
		Line:        preset.line,
		StartMarker: preset.start,
		EndMarker:   preset.end,
		Route:       route,
	}
	if c.Label == "" {
		c.Label = preset.label
	}
	// An end naming a marker (triangle, diamond_filled…) draws it there in
	// place of the style's; any other end is a multiplicity.
	if marker := BoardMarkerType(fromEnd); slices.Contains(boardMarkerTypes, marker) {
		c.StartMarker, fromEnd = marker, ""
	}
	if marker := BoardMarkerType(toEnd); slices.Contains(boardMarkerTypes, marker) {
		c.EndMarker, toEnd = marker, ""
	}
	if entities {
		// A label that is only a cardinality is the target's foot.
		if _, ok := boardCardinalities[label]; ok && fromEnd == "" && toEnd == "" {
			toEnd, c.Label = label, ""
		}
		start, startOK := boardCardinalities[fromEnd]
		end, endOK := boardCardinalities[toEnd]
		if (startOK || fromEnd == "") && (endOK || toEnd == "") && (startOK || endOK) {
			c.StartMarker, c.EndMarker = cmp.Or(
				start,
				BoardMarkerTypeNone,
			), cmp.Or(
				end,
				BoardMarkerTypeNone,
			)
			return c
		}
	}
	if fromEnd != "" {
		c.SourceLabel = &fromEnd
	}
	if toEnd != "" {
		c.TargetLabel = &toEnd
	}
	return c
}
