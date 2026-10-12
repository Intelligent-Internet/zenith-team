package canvas

import (
	"math"
	"testing"
)

func TestBoardSketchPathParsesEveryCommand(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, d   string
		subpaths  int
		end       boardSketchPoint
		closedEnd bool
	}{
		{"absolute lines", "M10 10 L50 10 H90 V60", 1, boardSketchPoint{90, 60}, false},
		{"relative lines", "m10,10 l40,0 h40 v50", 1, boardSketchPoint{90, 60}, false},
		{"implicit lineto after moveto", "M0 0 10 0 10 10", 1, boardSketchPoint{10, 10}, false},
		{"close returns to the start", "M0 0 L10 0 L10 10 Z", 1, boardSketchPoint{0, 0}, true},
		{"subpaths", "M0 0 L10 0 M20 20 L30 20", 2, boardSketchPoint{30, 20}, false},
		{"cubic and smooth", "M0 0 C0 10 10 10 10 0 S20 -10 20 0", 1, boardSketchPoint{20, 0}, false},
		{"quadratic and smooth", "M0 0 Q5 10 10 0 T20 0", 1, boardSketchPoint{20, 0}, false},
		{"compact numbers", "M0-1.5.5.5L1e1,2", 1, boardSketchPoint{10, 2}, false},
		{"drawing after a close", "M0 0 L10 0 Z L0 10", 2, boardSketchPoint{0, 10}, false},
		{"arc with packed flags", "M0 0a5 5 0 1010 0", 1, boardSketchPoint{10, 0}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			lines, err := boardSketchPath(tt.d, 0.25)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != tt.subpaths {
				t.Fatalf("subpaths = %d, want %d: %v", len(lines), tt.subpaths, lines)
			}
			last := lines[len(lines)-1]
			got := last[len(last)-1]
			if math.Abs(got.x-tt.end.x) > 1e-6 || math.Abs(got.y-tt.end.y) > 1e-6 {
				t.Fatalf("path ends at %v, want %v", got, tt.end)
			}
		})
	}
	for _, bad := range []string{"", "L10 10", "M0 0 X5 5", "M0", "M0 0 A5 5 0 2 0 10 0"} {
		if _, err := boardSketchPath(bad, 0.25); err == nil {
			t.Errorf("path %q accepted", bad)
		}
	}
}

func TestBoardSketchCurvesStayCloseToTheCurve(t *testing.T) {
	t.Parallel()
	// A half circle of radius 50 drawn as an arc.
	lines, err := boardSketchPath("M0 50 A50 50 0 0 1 100 50", 0.25)
	if err != nil {
		t.Fatal(err)
	}
	points := lines[0]
	if len(points) < 12 {
		t.Fatalf("a half circle flattened to %d points; want a smooth curve", len(points))
	}
	for _, p := range points {
		if r := math.Hypot(p.x-50, p.y-50); math.Abs(r-50) > 0.5 {
			t.Fatalf("point %v is %v from the center; want 50", p, r)
		}
		if p.y > 50.001 {
			t.Fatalf("sweep flag drew the lower half: %v", p)
		}
	}
}

// A sketch is scaled into its box keeping its proportions, each subpath is
// its own stroke, and stroke geometry matches the board's own pen, so both
// renderers draw it at exactly that size.
func TestBoardSketchStrokesFitTheirBoxLikeThePen(t *testing.T) {
	t.Parallel()
	strokes, err := boardSketchStrokes(
		BoardComposeOp{
			Type:    "sketch",
			ViewBox: "0 0 100 50",
			Paths:   []BoardComposePath{{D: "M0 0 L100 50 M0 50 L100 0", Color: "rose", Width: 4}},
		},
		400, 400,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(strokes) != 2 {
		t.Fatalf("strokes = %d, want one per subpath", len(strokes))
	}
	for _, stroke := range strokes {
		// 100x50 into 400x400 keeps 2:1, so it is 400 wide and 200 tall,
		// centered vertically.
		if math.Abs(stroke.x-(-6)) > 0.01 || math.Abs(stroke.y-(100-6)) > 0.01 ||
			math.Abs(stroke.width-412) > 0.01 || math.Abs(stroke.height-212) > 0.01 {
			t.Fatalf("stroke geometry = %+v", stroke)
		}
		maxX := 0.0
		for _, p := range stroke.points {
			maxX = math.Max(maxX, p.X)
		}
		// Both renderers scale points by width / (max x + 6).
		if scale := stroke.width / math.Max(12, maxX+6); math.Abs(scale-1) > 0.001 {
			t.Fatalf("renderers would scale this stroke by %v, want 1", scale)
		}
		if stroke.color != "#e85d75" || stroke.strokeWidth != 4 {
			t.Fatalf("stroke style = %q %v", stroke.color, stroke.strokeWidth)
		}
	}
}

func TestBoardSketchKeepsLongStrokesWithinThePointLimit(t *testing.T) {
	t.Parallel()
	// A zigzag of about 5,000 points within the path size limit: pairs
	// after a lineto repeat it.
	d := "M0 0l"
	for len(d) < boardSketchMaxPathBytes-8 {
		d += "1 9 1-9 "
	}
	strokes, err := boardSketchStrokes(
		BoardComposeOp{Type: "sketch", ViewBox: "0 -10 5000 20", Paths: []BoardComposePath{{D: d}}},
		4000, 16,
	)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, stroke := range strokes {
		if len(stroke.points) > boardMaxStrokePoints {
			t.Fatalf("a stroke has %d points, want at most %d", len(stroke.points), boardMaxStrokePoints)
		}
		total += len(stroke.points)
	}
	// Every zigzag corner survives: the line continues as a second stroke.
	if len(strokes) != 2 || total < 4900 {
		t.Fatalf("%d strokes with %d points; want the whole zigzag in two strokes", len(strokes), total)
	}
}
