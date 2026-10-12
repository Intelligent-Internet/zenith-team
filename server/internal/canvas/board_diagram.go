package canvas

import (
	"image"
	"image/color"
	"math"
	"strings"
	"sync"

	"golang.org/x/image/vector"
)

// Diagram geometry shared by the previews, the layouts and the editor (see
// frontend board-edge.tsx and board-shape.tsx, which follow the same rules):
// the path each connector route takes, the outline each shape kind draws,
// and where each kind's text goes. Sizes are Board units at scale 1.

type boardRect struct{ X, Y, W, H float64 }

func boardItemRect(g BoardGeometry) boardRect { return boardRect{g.X, g.Y, g.Width, g.Height} }

func (r boardRect) center() (float64, float64) { return r.X + r.W/2, r.Y + r.H/2 }

// notation is how a connector is drawn; a connector read before its notation
// existed draws as the Board's usual arrow.
func (e BoardEdge) notation() boardEdgeStyle {
	style := boardDefaultEdgeStyle()
	if e.Line != "" {
		style.Line = e.Line
	}
	if e.StartMarker != "" {
		style.Start = e.StartMarker
	}
	if e.EndMarker != "" {
		style.End = e.EndMarker
	}
	if e.Route != "" {
		style.Route = e.Route
	}
	style.AnchorY, style.SourceLabel, style.TargetLabel = e.AnchorY, e.SourceLabel, e.TargetLabel
	return style
}

// Connector geometry: a message's row defaults to its source's middle, and a
// self message loops this far right and down.
const (
	boardSelfMessageWidth  = 40.0
	boardSelfMessageHeight = 24.0
)

// boardEdgePoints is the path a connector takes from a to b, in Board units:
// flow from a's right middle to b's left middle; straight along the line
// between the centers, from outline to outline (centers when the boxes
// overlap); elbow out of the facing sides with one turn halfway across the
// larger gap; message straight across at its row, or a loop when self.
func boardEdgePoints(a, b boardRect, aKind, bKind BoardShapeType, style boardEdgeStyle, self bool) [][2]float64 {
	ax, ay := a.center()
	bx, by := b.center()
	switch style.Route {
	case BoardRouteTypeStraight:
		dx, dy := bx-ax, by-ay
		if dx == 0 && dy == 0 {
			return [][2]float64{{ax, ay}, {bx, by}}
		}
		ta, tb := boardOutlineReach(a, aKind, dx, dy), boardOutlineReach(b, bKind, dx, dy)
		if ta+tb >= 1 {
			return [][2]float64{{ax, ay}, {bx, by}}
		}
		return [][2]float64{{ax + ta*dx, ay + ta*dy}, {bx - tb*dx, by - tb*dy}}
	case BoardRouteTypeElbow:
		gapX := math.Max(b.X-(a.X+a.W), a.X-(b.X+b.W))
		gapY := math.Max(b.Y-(a.Y+a.H), a.Y-(b.Y+b.H))
		switch {
		case gapY >= gapX && gapY > 0:
			from, to := [2]float64{ax, a.Y + a.H}, [2]float64{bx, b.Y}
			if b.Y < a.Y {
				from, to = [2]float64{ax, a.Y}, [2]float64{bx, b.Y + b.H}
			}
			middle := (from[1] + to[1]) / 2
			return boardDistinctPoints([][2]float64{from, {from[0], middle}, {to[0], middle}, to})
		case gapX > 0:
			from, to := [2]float64{a.X + a.W, ay}, [2]float64{b.X, by}
			if b.X < a.X {
				from, to = [2]float64{a.X, ay}, [2]float64{b.X + b.W, by}
			}
			middle := (from[0] + to[0]) / 2
			return boardDistinctPoints([][2]float64{from, {middle, from[1]}, {middle, to[1]}, to})
		}
		return boardEdgePoints(a, b, aKind, bKind, boardEdgeStyle{Route: BoardRouteTypeStraight}, self)
	case BoardRouteTypeMessage:
		row := a.H / 2
		if style.AnchorY != nil {
			row = *style.AnchorY
		}
		y := a.Y + row
		if self {
			return [][2]float64{
				{ax, y},
				{ax + boardSelfMessageWidth, y},
				{ax + boardSelfMessageWidth, y + boardSelfMessageHeight},
				{ax, y + boardSelfMessageHeight},
			}
		}
		return [][2]float64{{ax, y}, {bx, y}}
	}
	return [][2]float64{{a.X + a.W, a.Y + a.H/2}, {b.X, b.Y + b.H/2}}
}

// boardOutlineReach is how far along (dx, dy), as a fraction of it, a box's
// outline lies from its center: the ellipse for round kinds, the diamond's
// edges, else the rectangle.
func boardOutlineReach(r boardRect, kind BoardShapeType, dx, dy float64) float64 {
	a, b := r.W/2, r.H/2
	switch kind {
	case BoardShapeTypeEllipse, BoardShapeTypeCloud:
		return 1 / math.Sqrt(dx*dx/(a*a)+dy*dy/(b*b))
	case BoardShapeTypeDiamond:
		return 1 / (math.Abs(dx)/a + math.Abs(dy)/b)
	}
	t := math.Inf(1)
	if dx != 0 {
		t = a / math.Abs(dx)
	}
	if dy != 0 {
		t = math.Min(t, b/math.Abs(dy))
	}
	return t
}

// boardDistinctPoints drops repeated points and the middle of straight runs,
// so an elbow between aligned boxes is one segment.
func boardDistinctPoints(points [][2]float64) [][2]float64 {
	out := [][2]float64{points[0]}
	for _, p := range points[1:] {
		last := out[len(out)-1]
		if math.Abs(last[0]-p[0]) < 1e-9 && math.Abs(last[1]-p[1]) < 1e-9 {
			continue
		}
		if len(out) >= 2 {
			prev := out[len(out)-2]
			if math.Abs((last[0]-prev[0])*(p[1]-last[1])-(last[1]-prev[1])*(p[0]-last[0])) < 1e-9 {
				out[len(out)-1] = p
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// boardPathMiddle is the point halfway along a path, where its label goes.
func boardPathMiddle(points [][2]float64) (float64, float64) {
	total := 0.0
	for i := 1; i < len(points); i++ {
		total += math.Hypot(points[i][0]-points[i-1][0], points[i][1]-points[i-1][1])
	}
	half := total / 2
	for i := 1; i < len(points); i++ {
		length := math.Hypot(points[i][0]-points[i-1][0], points[i][1]-points[i-1][1])
		if length >= half && length > 0 {
			t := half / length
			return points[i-1][0] + t*(points[i][0]-points[i-1][0]), points[i-1][1] + t*(points[i][1]-points[i-1][1])
		}
		half -= length
	}
	return points[0][0], points[0][1]
}

// Shape kind geometry, at scale 1.
const (
	boardActorBand           = 36.0  // the band below an actor's figure for its name
	boardLifelineHeader      = 48.0  // a lifeline's header box
	boardLifelineActorHeader = 100.0 // a lifeline headed by an actor's figure over its name
	boardCompartmentTitle    = 30.0  // a class's or entity's name band
	boardCompartmentLine     = 18.0  // one member or column
	boardCompartmentMargin   = 12.0  // a section's padding
	boardFragmentTag         = 22.0  // a fragment's operator tag height
	boardDashLength          = 8.0
	boardDashGap             = 6.0
)

// boardCompartmentHeight is one section's height: its lines and padding.
func boardCompartmentHeight(section string) float64 {
	lines := 1
	if section != "" {
		lines = strings.Count(section, "\n") + 1
	}
	return float64(lines)*boardCompartmentLine + boardCompartmentMargin
}

// boardCloudPath is the cloud's outline in a 100×100 box, normalized to the
// shape's bounds when drawn.
const boardCloudPath = "M25 80 C8 80 2 62 14 52 C8 36 24 24 38 30 C44 14 68 12 74 30 " +
	"C90 28 100 44 92 56 C102 70 90 82 76 80 Z"

var boardCloudOutline = sync.OnceValue(func() [][2]float64 {
	lines, err := boardSketchPath(boardCloudPath, 0.25)
	if err != nil || len(lines) == 0 {
		return nil
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range lines[0] {
		minX, minY, maxX, maxY = math.Min(
			minX,
			p.x,
		), math.Min(
			minY,
			p.y,
		), math.Max(
			maxX,
			p.x,
		), math.Max(
			maxY,
			p.y,
		)
	}
	out := make([][2]float64, 0, len(lines[0]))
	for _, p := range lines[0] {
		out = append(out, [2]float64{(p.x - minX) / (maxX - minX), (p.y - minY) / (maxY - minY)})
	}
	return out
})

// boardOutline is what a shape kind draws in a box: its body (filled and
// outlined), filled parts drawn over it (headers, tabs, tags), open lines
// (rims, folds, a stick figure, compartment dividers) and dashed lines.
type boardOutline struct {
	body   [][2]float64
	parts  [][][2]float64
	lines  [][][2]float64
	dashed [][][2]float64
	// bodyFill is the body's fill when it differs from the shape's (an
	// entity's body is white under its filled header); clear draws no fill.
	bodyFill *color.RGBA
	clear    bool
}

func boardArc(cx, cy, rx, ry, from, to float64, steps int) [][2]float64 {
	out := make([][2]float64, 0, steps+1)
	for i := 0; i <= steps; i++ {
		a := from + (to-from)*float64(i)/float64(steps)
		out = append(out, [2]float64{cx + rx*math.Cos(a), cy + ry*math.Sin(a)})
	}
	return out
}

func boardRoundedRect(x, y, w, h, r float64) [][2]float64 {
	r = math.Min(r, math.Min(w, h)/2)
	var out [][2]float64
	out = append(out, boardArc(x+w-r, y+r, r, r, -math.Pi/2, 0, 6)...)
	out = append(out, boardArc(x+w-r, y+h-r, r, r, 0, math.Pi/2, 6)...)
	out = append(out, boardArc(x+r, y+h-r, r, r, math.Pi/2, math.Pi, 6)...)
	out = append(out, boardArc(x+r, y+r, r, r, math.Pi, 3*math.Pi/2, 6)...)
	return out
}

func boardBox(x, y, w, h float64) [][2]float64 {
	return [][2]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}
}

// boardShapeOutline is the outline of a kind in the box (x, y, w, h), drawn
// at scale s. tag is a fragment's operator tag width.
func boardShapeOutline(kind BoardShapeType, x, y, w, h, s float64, p BoardProperties, tag float64) boardOutline {
	switch kind {
	case BoardShapeTypeEllipse:
		return boardOutline{body: boardArc(x+w/2, y+h/2, w/2, h/2, 0, 2*math.Pi, 64)}
	case BoardShapeTypeDiamond:
		return boardOutline{body: [][2]float64{{x + w/2, y}, {x + w, y + h/2}, {x + w/2, y + h}, {x, y + h/2}}}
	case BoardShapeTypeTriangle:
		return boardOutline{body: [][2]float64{{x + w/2, y}, {x + w, y + h}, {x, y + h}}}
	case BoardShapeTypeRounded:
		return boardOutline{body: boardRoundedRect(x, y, w, h, math.Min(12*s, math.Min(w, h)/4))}
	case BoardShapeTypeTerminal:
		return boardOutline{body: boardRoundedRect(x, y, w, h, math.Min(w, h)/2)}
	case BoardShapeTypeActor:
		return boardOutline{clear: true, lines: boardActorFigure(x, y, w, h, s)}
	case BoardShapeTypeDatabase:
		ry := math.Min(0.1*h, 12*s)
		body := boardArc(x+w/2, y+ry, w/2, ry, math.Pi, 2*math.Pi, 24)
		body = append(body, boardArc(x+w/2, y+h-ry, w/2, ry, 0, math.Pi, 24)...)
		return boardOutline{body: body, lines: [][][2]float64{boardArc(x+w/2, y+ry, w/2, ry, 0, math.Pi, 24)}}
	case BoardShapeTypeQueue:
		rx := math.Min(0.08*w, 12*s)
		body := boardArc(x+w-rx, y+h/2, rx, h/2, -math.Pi/2, math.Pi/2, 24)
		body = append(body, boardArc(x+rx, y+h/2, rx, h/2, math.Pi/2, 3*math.Pi/2, 24)...)
		return boardOutline{
			body:  body,
			lines: [][][2]float64{boardArc(x+w-rx, y+h/2, rx, h/2, math.Pi/2, 3*math.Pi/2, 24)},
		}
	case BoardShapeTypeCloud:
		var body [][2]float64
		for _, p := range boardCloudOutline() {
			body = append(body, [2]float64{x + p[0]*w, y + p[1]*h})
		}
		return boardOutline{body: body}
	case BoardShapeTypeComponent:
		tab := 10 * s
		return boardOutline{
			body: boardBox(x+tab, y, w-tab, h),
			parts: [][][2]float64{
				boardBox(x, y+h*0.25-tab/2, 2*tab, tab),
				boardBox(x, y+h*0.6-tab/2, 2*tab, tab),
			},
		}
	case BoardShapeTypeDocument:
		a := math.Min(0.1*h, 10*s)
		body := [][2]float64{{x, y}, {x + w, y}}
		for i := range 25 {
			t := float64(i) / 24
			body = append(body, [2]float64{x + w - t*w, y + h - a + a*math.Sin(2*math.Pi*t)})
		}
		return boardOutline{body: body}
	case BoardShapeTypeNote:
		f := math.Min(16*s, math.Min(w, h)/4)
		return boardOutline{
			body:  [][2]float64{{x, y}, {x + w - f, y}, {x + w, y + f}, {x + w, y + h}, {x, y + h}},
			lines: [][][2]float64{{{x + w - f, y}, {x + w - f, y + f}, {x + w, y + f}}},
		}
	case BoardShapeTypeLifeline:
		header := boardLifelineHead(p.Head, h, s)
		axis := [][][2]float64{{{x + w/2, y + header}, {x + w/2, y + h}}}
		if p.Head == BoardHeadTypeActor {
			return boardOutline{clear: true, lines: boardActorFigure(x, y, w, header, s), dashed: axis}
		}
		return boardOutline{clear: true, parts: [][][2]float64{boardBox(x, y, w, header)}, dashed: axis}
	case BoardShapeTypeClass, BoardShapeTypeEntity:
		title := math.Min(h, boardCompartmentTitle*s)
		out := boardOutline{
			body:  boardBox(x, y, w, h),
			lines: [][][2]float64{{{x, y + title}, {x + w, y + title}}},
		}
		if kind == BoardShapeTypeEntity {
			white := color.RGBA{255, 255, 255, 255}
			out.bodyFill, out.parts = &white, [][][2]float64{boardBox(x, y, w, title)}
		}
		at := y + title
		for i, section := range p.Sections {
			if i == len(p.Sections)-1 {
				break
			}
			at += boardCompartmentHeight(section) * s
			if at < y+h {
				out.lines = append(out.lines, [][2]float64{{x, at}, {x + w, at}})
			}
		}
		return out
	case BoardShapeTypeFragment:
		tagHeight := math.Min(h, boardFragmentTag*s)
		out := boardOutline{clear: true, body: boardBox(x, y, w, h), parts: [][][2]float64{
			{
				{
					x,
					y,
				},
				{x + tag, y},
				{x + tag, y + tagHeight - 6*s},
				{x + tag - 8*s, y + tagHeight},
				{x, y + tagHeight},
			},
		}}
		for _, divider := range p.Dividers {
			if dy := y + divider*s; dy < y+h {
				out.dashed = append(out.dashed, [][2]float64{{x, dy}, {x + w, dy}})
			}
		}
		return out
	}
	return boardOutline{body: boardBox(x, y, w, h)}
}

// boardActorFigure is an actor's stick figure in the box (x, y, w, h) at
// scale s, over the band its name takes.
func boardActorFigure(x, y, w, h, s float64) [][][2]float64 {
	band := math.Min(boardActorBand*s, h*0.35)
	figure := h - band
	cx := x + w/2
	r := math.Min(figure*0.14, w*0.18)
	headY := y + r + 1
	neck, hip, arms := headY+r, y+figure*0.62, y+figure*0.36
	reach, stride := w*0.32, w*0.25
	return [][][2]float64{
		boardArc(cx, headY, r, r, 0, 2*math.Pi, 24),
		{{cx, neck}, {cx, hip}},
		{{cx - reach, arms}, {cx + reach, arms}},
		{{cx - stride, y + figure}, {cx, hip}, {cx + stride, y + figure}},
	}
}

// boardLifelineHead is the height of a lifeline's head at scale s: a box, or
// an actor's figure over its name.
func boardLifelineHead(head BoardHeadType, h, s float64) float64 {
	if head == BoardHeadTypeActor {
		return math.Min(h, boardLifelineActorHeader*s)
	}
	return math.Min(h, boardLifelineHeader*s)
}

// boardShapeTextBox is where a kind's label goes inside its box (x, y, w, h)
// at scale s: below an actor's figure, in a lifeline's head, inside a
// database's body, else inset like any object.
func boardShapeTextBox(kind BoardShapeType, head BoardHeadType, x, y, w, h, s float64) image.Rectangle {
	inset := func(left, top, right, bottom float64) image.Rectangle {
		return image.Rect(int(x+left), int(y+top), int(x+w-right), int(y+h-bottom))
	}
	switch kind {
	case BoardShapeTypeActor:
		band := math.Min(boardActorBand*s, h*0.35)
		return image.Rect(int(x), int(y+h-band), int(x+w), int(y+h))
	case BoardShapeTypeLifeline:
		header := boardLifelineHead(head, h, s)
		if head == BoardHeadTypeActor {
			band := math.Min(boardActorBand*s, header*0.35)
			return image.Rect(int(x), int(y+header-band), int(x+w), int(y+header))
		}
		return image.Rect(int(x+6*s), int(y+6*s), int(x+w-6*s), int(y+header-4*s))
	case BoardShapeTypeDatabase:
		ry := math.Min(0.1*h, 12*s)
		return inset(10*s, 2*ry+4*s, 10*s, ry+4*s)
	case BoardShapeTypeComponent:
		return inset(24*s, 8*s, 8*s, 8*s)
	case BoardShapeTypeCloud:
		return inset(0.2*w, 0.3*h, 0.15*w, 0.12*h)
	}
	return inset(12*s, 12*s, 12*s, 12*s)
}

// boardPolygon fills a closed polygon and outlines it, rasterizing only its
// bounds.
func boardPolygon(
	dst *image.RGBA,
	work *boardRenderWork,
	points [][2]float64,
	fill *color.RGBA,
	stroke color.RGBA,
	width float64,
) {
	if len(points) < 2 {
		return
	}
	if fill != nil && fill.A > 0 && len(points) > 2 {
		minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, p := range points {
			minX, minY = math.Min(minX, p[0]), math.Min(minY, p[1])
			maxX, maxY = math.Max(maxX, p[0]), math.Max(maxY, p[1])
		}
		area := image.Rect(int(math.Floor(minX)), int(math.Floor(minY)), int(math.Ceil(maxX)), int(math.Ceil(maxY))).
			Intersect(dst.Bounds())
		if !area.Empty() && work.spend(int64(area.Dx())*int64(area.Dy())) {
			raster := vector.NewRasterizer(area.Dx(), area.Dy())
			ox, oy := float64(area.Min.X), float64(area.Min.Y)
			raster.MoveTo(float32(points[0][0]-ox), float32(points[0][1]-oy))
			for _, p := range points[1:] {
				raster.LineTo(float32(p[0]-ox), float32(p[1]-oy))
			}
			raster.ClosePath()
			raster.Draw(dst, area, image.NewUniform(color.NRGBA(*fill)), image.Point{})
		}
	}
	for i, p := range points {
		q := points[(i+1)%len(points)]
		boardLine(dst, work, p[0], p[1], q[0], q[1], width, stroke)
	}
}

// boardPolyline draws an open path, solid or dashed.
func boardPolyline(
	dst *image.RGBA,
	work *boardRenderWork,
	points [][2]float64,
	width float64,
	c color.RGBA,
	dash, gap float64,
) {
	phase := 0.0
	for i := 1; i < len(points); i++ {
		a, b := points[i-1], points[i]
		if dash <= 0 {
			boardLine(dst, work, a[0], a[1], b[0], b[1], width, c)
			continue
		}
		phase = boardDashedLine(dst, work, a[0], a[1], b[0], b[1], width, c, dash, gap, phase)
	}
}

// boardDashedLine dashes a segment starting phase units into the pattern and
// returns the phase it ends at. Only the part within the raster is dashed,
// so a long line costs what the tile shows of it.
func boardDashedLine(
	dst *image.RGBA,
	work *boardRenderWork,
	x1, y1, x2, y2, width float64,
	c color.RGBA,
	dash, gap, phase float64,
) float64 {
	length := math.Hypot(x2-x1, y2-y1)
	period := dash + gap
	end := math.Mod(phase+length, period)
	if length == 0 {
		return end
	}
	ux, uy := (x2-x1)/length, (y2-y1)/length
	b := dst.Bounds()
	low, high, ok := boardClipSegment(x1, y1, x2, y2, BoardVisualBounds{
		X: float64(b.Min.X) - width, Y: float64(b.Min.Y) - width,
		Width: float64(b.Dx()) + 2*width, Height: float64(b.Dy()) + 2*width,
	})
	if !ok {
		return end
	}
	from, to := low*length, high*length
	// The first dash at or before the visible part.
	start := math.Floor((from+phase)/period)*period - phase
	for at := start; at < to && !work.exhausted(); at += period {
		a, z := math.Max(at, from), math.Min(at+dash, to)
		if z > a {
			boardLine(dst, work, x1+ux*a, y1+uy*a, x1+ux*z, y1+uy*z, width, c)
		}
	}
	return end
}

// boardClipSegment returns the part of a segment within a rectangle as
// fractions of it (Liang–Barsky).
func boardClipSegment(x1, y1, x2, y2 float64, r BoardVisualBounds) (float64, float64, bool) {
	dx, dy := x2-x1, y2-y1
	low, high := 0.0, 1.0
	for _, edge := range [][2]float64{
		{-dx, x1 - r.X}, {dx, r.X + r.Width - x1}, {-dy, y1 - r.Y}, {dy, r.Y + r.Height - y1},
	} {
		p, q := edge[0], edge[1]
		if p == 0 {
			if q < 0 {
				return 0, 0, false
			}
			continue
		}
		t := q / p
		if p < 0 {
			low = math.Max(low, t)
		} else {
			high = math.Min(high, t)
		}
		if low > high {
			return 0, 0, false
		}
	}
	return low, high, true
}

// boardMarker draws a connector's end marker with its tip at (tx, ty),
// pointing along (ux, uy), at scale s. Hollow markers are filled with the
// canvas so the line does not show through them.
func boardMarker(
	dst *image.RGBA,
	work *boardRenderWork,
	marker BoardMarkerType,
	tx, ty, ux, uy, s, width float64,
	c color.RGBA,
) {
	nx, ny := -uy, ux
	at := func(back, side float64) [2]float64 {
		return [2]float64{tx - ux*back*s + nx*side*s, ty - uy*back*s + ny*side*s}
	}
	blank := color.RGBA{255, 255, 255, 255}
	bar := func(back float64) { boardPolyline(dst, work, [][2]float64{at(back, -6), at(back, 6)}, width, c, 0, 0) }
	ring := func(back float64) {
		cx, cy := at(back, 0)[0], at(back, 0)[1]
		boardPolygon(dst, work, boardArc(cx, cy, 4*s, 4*s, 0, 2*math.Pi, 16), &blank, c, width)
	}
	foot := func() {
		boardPolyline(dst, work, [][2]float64{at(12, 0), at(0, 6)}, width, c, 0, 0)
		boardPolyline(dst, work, [][2]float64{at(12, 0), at(0, -6)}, width, c, 0, 0)
	}
	switch marker {
	case BoardMarkerTypeArrow:
		fill := c
		boardPolygon(dst, work, [][2]float64{at(0, 0), at(12, 5), at(12, -5)}, &fill, c, width)
	case BoardMarkerTypeOpen:
		boardPolyline(dst, work, [][2]float64{at(12, 5), at(0, 0), at(12, -5)}, width, c, 0, 0)
	case BoardMarkerTypeTriangle:
		boardPolygon(dst, work, [][2]float64{at(0, 0), at(14, 8), at(14, -8)}, &blank, c, width)
	case BoardMarkerTypeDiamond, BoardMarkerTypeDiamondFilled:
		fill := blank
		if marker == BoardMarkerTypeDiamondFilled {
			fill = c
		}
		boardPolygon(dst, work, [][2]float64{at(0, 0), at(9, 6), at(18, 0), at(9, -6)}, &fill, c, width)
	case BoardMarkerTypeOne:
		bar(8)
		bar(12)
	case BoardMarkerTypeZeroOne:
		bar(8)
		ring(18)
	case BoardMarkerTypeMany:
		foot()
		bar(16)
	case BoardMarkerTypeZeroMany:
		foot()
		ring(20)
	}
}
