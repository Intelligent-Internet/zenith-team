package canvas

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/google/uuid"
)

func samePath(a, b [][2]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i][0]-b[i][0]) > 1e-6 || math.Abs(a[i][1]-b[i][1]) > 1e-6 {
			return false
		}
	}
	return true
}

// Every route has one geometry, which the editor, the previews and the
// layouts share: flow from the right side to the left side, straight between
// the boxes' outlines, elbow between their facing sides, and a message
// across at its row (looping back to its own lifeline).
func TestBoardEdgeRouteGeometry(t *testing.T) {
	t.Parallel()
	style := func(route BoardRouteType, anchor ...float64) boardEdgeStyle {
		s := boardDefaultEdgeStyle()
		s.Route = route
		if len(anchor) > 0 {
			s.AnchorY = &anchor[0]
		}
		return s
	}
	rect := BoardShapeTypeRectangle
	for name, tt := range map[string]struct {
		a, b         boardRect
		aKind, bKind BoardShapeType
		style        boardEdgeStyle
		want         [][2]float64
	}{
		"flow": {
			boardRect{0, 0, 100, 80},
			boardRect{240, 0, 200, 80},
			rect, rect, style(BoardRouteTypeFlow),
			[][2]float64{{100, 40}, {240, 40}},
		},
		"straight across": {
			boardRect{0, 0, 100, 100},
			boardRect{300, 0, 100, 100},
			rect, BoardShapeTypeEllipse,
			style(BoardRouteTypeStraight),
			[][2]float64{{100, 50}, {300, 50}},
		},
		"straight to a corner": {
			boardRect{0, 0, 100, 100},
			boardRect{200, 200, 100, 100},
			rect, rect, style(BoardRouteTypeStraight),
			[][2]float64{{100, 100}, {200, 200}},
		},
		"straight to a diamond": {
			boardRect{0, 0, 100, 100},
			boardRect{200, 200, 100, 100},
			rect, BoardShapeTypeDiamond,
			style(BoardRouteTypeStraight),
			[][2]float64{{100, 100}, {225, 225}},
		},
		"elbow down": {
			boardRect{0, 0, 100, 60},
			boardRect{200, 200, 100, 60},
			rect, rect, style(BoardRouteTypeElbow),
			[][2]float64{{50, 60}, {50, 130}, {250, 130}, {250, 200}},
		},
		"elbow straight down": {
			boardRect{0, 0, 100, 60},
			boardRect{0, 200, 100, 60},
			rect, rect, style(BoardRouteTypeElbow),
			[][2]float64{{50, 60}, {50, 200}},
		},
		"elbow across": {
			boardRect{0, 0, 100, 60},
			boardRect{300, 10, 100, 60},
			rect, rect, style(BoardRouteTypeElbow),
			[][2]float64{{100, 30}, {200, 30}, {200, 40}, {300, 40}},
		},
		"elbow up": {
			boardRect{200, 200, 100, 60},
			boardRect{0, 0, 100, 60},
			rect, rect, style(BoardRouteTypeElbow),
			[][2]float64{{250, 200}, {250, 130}, {50, 130}, {50, 60}},
		},
		"message": {
			boardRect{0, 0, 120, 400},
			boardRect{300, 0, 120, 400},
			BoardShapeTypeLifeline, BoardShapeTypeLifeline,
			style(BoardRouteTypeMessage, 100),
			[][2]float64{{60, 100}, {360, 100}},
		},
		"reply": {
			boardRect{300, 0, 120, 400},
			boardRect{0, 0, 120, 400},
			BoardShapeTypeLifeline, BoardShapeTypeLifeline,
			style(BoardRouteTypeMessage, 150),
			[][2]float64{{360, 150}, {60, 150}},
		},
	} {
		if got := boardEdgePoints(tt.a, tt.b, tt.aKind, tt.bKind, tt.style, false); !samePath(got, tt.want) {
			t.Errorf("%s = %v, want %v", name, got, tt.want)
		}
	}
	self := boardEdgePoints(
		boardRect{0, 0, 120, 400}, boardRect{0, 0, 120, 400}, BoardShapeTypeLifeline, BoardShapeTypeLifeline,
		style(BoardRouteTypeMessage, 100), true,
	)
	if want := [][2]float64{{60, 100}, {100, 100}, {100, 124}, {60, 124}}; !samePath(self, want) {
		t.Errorf("self message = %v, want %v", self, want)
	}
}

func dark(c color.RGBA) bool { return c.R < 140 && c.G < 140 && c.B < 160 }

func white(c color.RGBA) bool { return c.R > 240 && c.G > 240 && c.B > 240 }

func renderTestScene(t *testing.T, scene boardScene, w, h float64) *image.RGBA {
	t.Helper()
	img, err := boardRenderRegion(t.Context(), fullRenderWork(), scene, nil, boardBounds{w: w, h: h}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func shapeScene(kind BoardShapeType, w, h float64, props BoardProperties) boardScene {
	props.Shape, props.StrokeWidth = kind, 2
	if props.Fill == "" {
		props.Fill = "#dff0fc"
	}
	return boardScene{Items: []boardSceneItem{{BoardItem: BoardItem{
		ID: uuid.New(), Type: "shape", BoardGeometry: BoardGeometry{X: 10, Y: 10, Width: w, Height: h},
		Properties: props,
	}}}}
}

// The previews Agents read draw each diagram shape recognizably: a
// database's front rim, an actor's figure on no box, a note's fold, a
// lifeline's header and dashed axis, a class's compartments and a
// fragment's open interior.
func TestBoardExportDrawsDiagramShapes(t *testing.T) {
	t.Parallel()
	for name, tt := range map[string]struct {
		kind      BoardShapeType
		w, h      float64
		props     BoardProperties
		inked     [][2]int
		blank     [][2]int
		minDashes int
	}{
		"database": {
			kind: BoardShapeTypeDatabase, w: 120, h: 110,
			inked: [][2]int{{70, 10 + 22}}, blank: [][2]int{{12, 12}},
		},
		"actor": {
			kind: BoardShapeTypeActor, w: 80, h: 108,
			inked: [][2]int{{50, 10 + 40}}, blank: [][2]int{{14, 10 + 40}},
		},
		"note": {
			kind: BoardShapeTypeNote, w: 160, h: 80,
			inked: [][2]int{{10 + 160 - 16, 10 + 8}}, blank: [][2]int{{10 + 160 - 3, 12}},
		},
		"lifeline": {
			kind: BoardShapeTypeLifeline, w: 120, h: 300,
			blank: [][2]int{{30, 200}}, minDashes: 3,
		},
		// An actor's lifeline: the figure where a box head would be, then
		// the dashed axis below its name.
		"actor lifeline": {
			kind: BoardShapeTypeLifeline, w: 120, h: 300, props: BoardProperties{Head: BoardHeadTypeActor},
			inked: [][2]int{{70, 40}}, blank: [][2]int{{30, 11}}, minDashes: 3,
		},
		"class": {
			kind: BoardShapeTypeClass, w: 160, h: 120, props: BoardProperties{Title: "Order", Sections: []string{"id", "save()"}},
			inked: [][2]int{{90, 10 + 30}},
		},
		"fragment": {
			kind: BoardShapeTypeFragment, w: 200, h: 200, props: BoardProperties{Title: "loop", Sections: []string{"[each]"}},
			blank: [][2]int{{110, 150}},
		},
		"cloud": {
			kind: BoardShapeTypeCloud, w: 180, h: 110,
			blank: [][2]int{{11, 11}, {11 + 178, 11}},
		},
	} {
		img := renderTestScene(t, shapeScene(tt.kind, tt.w, tt.h, tt.props), tt.w+20, tt.h+20)
		for _, p := range tt.inked {
			if c := img.RGBAAt(p[0], p[1]); !dark(c) {
				t.Errorf("%s: (%d,%d) = %v, want ink", name, p[0], p[1], c)
			}
		}
		for _, p := range tt.blank {
			if c := img.RGBAAt(p[0], p[1]); !white(c) {
				t.Errorf("%s: (%d,%d) = %v, want blank", name, p[0], p[1], c)
			}
		}
		if tt.minDashes > 0 {
			// The axis runs down the middle below the header, dashed.
			x, dashes, inDash := 10+int(tt.w/2), 0, false
			for y := 10 + 60; y < 10+int(tt.h)-2; y++ {
				on := dark(img.RGBAAt(x, y))
				if on && !inDash {
					dashes++
				}
				inDash = on
			}
			if dashes < tt.minDashes {
				t.Errorf("%s: axis has %d dashes, want at least %d", name, dashes, tt.minDashes)
			}
		}
	}
}

// Connector markers read as UML: a hollow triangle stays empty, a filled
// diamond is inked, and a dashed line has gaps.
func TestBoardExportDrawsConnectorMarkers(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	box := func(id uuid.UUID, x float64) boardSceneItem {
		return boardSceneItem{BoardItem: BoardItem{
			ID: id, Type: "shape", BoardGeometry: BoardGeometry{X: x, Width: 100, Height: 80},
			Properties: BoardProperties{Fill: "#ffffff"},
		}}
	}
	scene := boardScene{
		Items: []boardSceneItem{box(a, 0), box(b, 240)},
		Edges: []BoardEdge{{
			SourceID: a, TargetID: b, Line: BoardLineTypeDashed,
			StartMarker: BoardMarkerTypeDiamondFilled, EndMarker: BoardMarkerTypeTriangle,
		}},
	}
	img := renderTestScene(t, scene, 360, 80)
	if c := img.RGBAAt(233, 40); !white(c) {
		t.Errorf("inside the hollow triangle = %v, want blank", c)
	}
	if c := img.RGBAAt(226, 36); !dark(c) {
		t.Errorf("the triangle's base at (226,36) = %v, want ink", c)
	}
	if c := img.RGBAAt(109, 40); !dark(c) {
		t.Errorf("inside the filled diamond = %v, want ink", c)
	}
	inked, gaps := 0, 0
	for x := 124; x < 220; x++ {
		if dark(img.RGBAAt(x, 40)) {
			inked++
		} else {
			gaps++
		}
	}
	if inked == 0 || gaps < 20 {
		t.Errorf("dashed line: %d inked and %d blank pixels, want both", inked, gaps)
	}
}

// A dashed connector far longer than the tile dashes only the part the tile
// shows, so it costs no more than a short one.
func TestBoardExportDashedLinesStayWithinBudget(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	scene := boardScene{
		Items: []boardSceneItem{
			{
				BoardItem: BoardItem{
					ID:            a,
					Type:          "shape",
					BoardGeometry: BoardGeometry{X: -500000, Width: 100, Height: 80},
				},
			},
			{
				BoardItem: BoardItem{
					ID:            b,
					Type:          "shape",
					BoardGeometry: BoardGeometry{X: 500000, Width: 100, Height: 80},
				},
			},
		},
		Edges: []BoardEdge{{SourceID: a, TargetID: b, Line: BoardLineTypeDashed}},
	}
	work := &boardRenderWork{left: 5_000_000}
	if _, err := boardRenderRegion(
		t.Context(),
		work,
		scene,
		nil,
		boardBounds{x: 0, y: 0, w: 400, h: 100},
		1,
	); err != nil {
		t.Fatalf("a long dashed connector over a small tile = %v", err)
	}
}

// A page that a message crosses draws the lifelines at its ends, even when
// the flow route between them would miss the page.
func TestBoardVisualsDrawRoutedConnectorsAcrossPages(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	anchor := 100.0
	items := map[uuid.UUID]BoardItem{
		a: {
			ID: a, Type: "shape", BoardGeometry: BoardGeometry{Width: 100, Height: 1000},
			Properties: BoardProperties{Shape: BoardShapeTypeLifeline},
		},
		b: {
			ID: b, Type: "shape", BoardGeometry: BoardGeometry{X: 400, Width: 100, Height: 1000},
			Properties: BoardProperties{Shape: BoardShapeTypeLifeline},
		},
	}
	scene := boardScene{Edges: []BoardEdge{{
		SourceID: a, TargetID: b, Route: BoardRouteTypeMessage, AnchorY: &anchor,
	}}}
	ends := boardVisualConnectorEnds(scene, items, []BoardVisualBounds{{X: 200, Y: 80, Width: 50, Height: 50}})
	if len(ends) != 2 {
		t.Fatalf("a page the message crosses draws %d of its lifelines, want both", len(ends))
	}
}
