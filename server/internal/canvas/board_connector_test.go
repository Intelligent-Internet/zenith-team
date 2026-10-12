package canvas

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A connector's notation starts as the Board always drew it, and a command
// changes only the parts it names: relabeling never drops a UML arrowhead.
func TestBoardEdgeStyleMergesWhatACommandNames(t *testing.T) {
	t.Parallel()
	base := boardDefaultEdgeStyle()
	if base.Line != BoardLineTypeSolid || base.Start != BoardMarkerTypeNone || base.End != BoardMarkerTypeArrow ||
		base.Route != BoardRouteTypeFlow || base.AnchorY != nil {
		t.Fatalf("default style = %+v", base)
	}
	at, source := 40.0, "1"
	uml := base.with(BoardCommand{
		Line:        BoardLineTypeDashed,
		EndMarker:   BoardMarkerTypeTriangle,
		Route:       BoardRouteTypeElbow,
		SourceLabel: &source,
	})
	relabeled := uml.with(BoardCommand{Label: "parent"})
	if relabeled != uml || uml.Line != BoardLineTypeDashed || uml.End != BoardMarkerTypeTriangle ||
		uml.Route != BoardRouteTypeElbow || uml.SourceLabel != "1" {
		t.Fatalf("relabel changed the notation: %+v -> %+v", uml, relabeled)
	}
	message, err := base.with(BoardCommand{Route: BoardRouteTypeMessage, AnchorY: &at}).check(true)
	if err != nil || message.AnchorY == nil || *message.AnchorY != 40 {
		t.Fatalf("a self message at 40 = %+v, %v", message, err)
	}
	if _, err := base.with(BoardCommand{Route: BoardRouteTypeMessage}).
		check(false); !errors.Is(
		err,
		ErrBoardInvalid,
	) {
		t.Fatalf("a message without a row = %v, want invalid", err)
	}
	if _, err := base.check(true); !errors.Is(err, ErrBoardInvalid) {
		t.Fatalf("a self connector that is not a message = %v, want invalid", err)
	}
	straight, err := message.with(BoardCommand{Route: BoardRouteTypeStraight}).check(false)
	if err != nil || straight.AnchorY != nil {
		t.Fatalf("leaving the message route kept its row: %+v, %v", straight, err)
	}
}

func TestBoardBatchValidatesConnectorNotation(t *testing.T) {
	t.Parallel()
	far, long := 2e6, strings.Repeat("x", 61)
	for name, c := range map[string]BoardCommand{
		"unknown line":   {Line: "dotted"},
		"unknown marker": {EndMarker: "star"},
		"unknown route":  {Route: "curved"},
		"far row":        {Route: BoardRouteTypeMessage, AnchorY: &far},
		"long end label": {TargetLabel: &long},
	} {
		c.Type, c.DraftID, c.SourceRef, c.TargetRef = "create_edge", "e", uuid.NewString(), uuid.NewString()
		if err := normalizeBoardBatch(
			BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{c}},
		); !errors.Is(
			err,
			ErrBoardInvalid,
		) {
			t.Errorf("%s = %v, want invalid", name, err)
		}
	}
}

// An end may name a marker instead of a multiplicity, as an Agent reaching
// for a hollow triangle does: the marker replaces the style's at that end
// and no label is written there.
func TestBoardComposeConnectorTakesMarkerNamedEnds(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		style, fromEnd, toEnd string
		start, end            BoardMarkerType
		line                  BoardLineType
		labels                [2]string
	}{
		"implements as a dashed triangle": {"dashed", "", "triangle", BoardMarkerTypeNone, BoardMarkerTypeTriangle, BoardLineTypeDashed, [2]string{}},
		"inherits naming its own marker":  {"inherits", "", "triangle", BoardMarkerTypeNone, BoardMarkerTypeTriangle, BoardLineTypeSolid, [2]string{}},
		"composes naming its diamond":     {"composes", "diamond_filled", "", BoardMarkerTypeDiamondFilled, BoardMarkerTypeNone, BoardLineTypeSolid, [2]string{}},
		"a marker beside a multiplicity":  {"composes", "diamond_filled", "0..*", BoardMarkerTypeDiamondFilled, BoardMarkerTypeNone, BoardLineTypeSolid, [2]string{"", "0..*"}},
		"multiplicities stay labels":      {"associates", "1", "0..*", BoardMarkerTypeNone, BoardMarkerTypeOpen, BoardLineTypeSolid, [2]string{"1", "0..*"}},
	} {
		c := boardComposeConnector(
			"e",
			"a",
			"b",
			"",
			tt.style,
			tt.fromEnd,
			tt.toEnd,
			"arrow",
			BoardRouteTypeElbow,
			false,
		)
		labels := [2]string{}
		if c.SourceLabel != nil {
			labels[0] = *c.SourceLabel
		}
		if c.TargetLabel != nil {
			labels[1] = *c.TargetLabel
		}
		if c.StartMarker != tt.start || c.EndMarker != tt.end || c.Line != tt.line || labels != tt.labels {
			t.Errorf("%s: %s %s→%s labels %q, want %s %s→%s labels %q",
				name, c.Line, c.StartMarker, c.EndMarker, labels, tt.line, tt.start, tt.end, tt.labels)
		}
	}
}

// Between entities a label that is only a cardinality is the target end's
// crow's foot, as an Agent writing "0..*" on the line means.
func TestBoardComposeConnectorReadsACardinalityLabelBetweenEntities(t *testing.T) {
	t.Parallel()
	c := boardComposeConnector("e", "a", "b", "0..*", "", "", "", "line", BoardRouteTypeFlow, true)
	if c.EndMarker != BoardMarkerTypeZeroMany || c.Label != "" || c.TargetLabel != nil {
		t.Fatalf(
			"got %s→%s label %q, want a zero-or-many foot and no label",
			c.StartMarker,
			c.EndMarker,
			c.Label,
		)
	}
	c = boardComposeConnector("e", "a", "b", "places", "", "", "", "line", BoardRouteTypeFlow, true)
	if c.Label != "places" || c.EndMarker != BoardMarkerTypeNone {
		t.Fatalf("a relationship name stays its label, got %q with %s", c.Label, c.EndMarker)
	}
}
