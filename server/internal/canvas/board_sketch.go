package canvas

import (
	"errors"
	"math"
	"strconv"
	"strings"
)

// An Agent sketches the way it already knows how to draw: SVG path data in a
// view box of its choosing. The server flattens the curves, fits the drawing
// into the box it places on the Board, and stores each subpath as a pen
// stroke, so a sketch is ordinary ink a person can move, restyle or erase.

type boardSketchPoint struct{ x, y float64 }

// boardSketchStroke is one stroke ready to create: its geometry and its
// points relative to it, exactly as the Board's own pen stores them.
type boardSketchStroke struct {
	x, y, width, height float64
	points              []BoardPoint
	color               string
	strokeWidth         float64
}

// boardSketchMaxPathBytes bounds one path's data.
const boardSketchMaxPathBytes = 20000

var errBoardSketch = errors.New("invalid sketch path")

// boardSketchStrokes turns a sketch op into strokes inside a width x height
// box at the origin, keeping the view box's proportions and centering it.
func boardSketchStrokes(op BoardComposeOp, width, height float64) ([]boardSketchStroke, error) {
	vb, err := boardSketchViewBox(op.ViewBox, width, height)
	if err != nil {
		return nil, err
	}
	scale := math.Min(width/vb[2], height/vb[3])
	offsetX := (width-vb[2]*scale)/2 - vb[0]*scale
	offsetY := (height-vb[3]*scale)/2 - vb[1]*scale
	var strokes []boardSketchStroke
	for _, path := range op.Paths {
		// Flatten to a quarter of a Board unit at the final scale.
		lines, err := boardSketchPath(path.D, 0.25/math.Max(scale, 1e-6))
		if err != nil {
			return nil, err
		}
		color := boardComposeInk(path.Color, "#25252d")
		strokeWidth := path.Width
		if strokeWidth == 0 {
			strokeWidth = 3
		}
		for _, line := range lines {
			board := make([]boardSketchPoint, len(line))
			for i, p := range line {
				board[i] = boardSketchPoint{p.x*scale + offsetX, p.y*scale + offsetY}
			}
			for _, part := range boardSketchSimplify(board) {
				stroke := boardSketchNormalize(part)
				stroke.color, stroke.strokeWidth = color, strokeWidth
				strokes = append(strokes, stroke)
			}
		}
	}
	return strokes, nil
}

// boardSketchViewBox parses "minX minY width height"; empty means the box
// itself.
func boardSketchViewBox(raw string, width, height float64) ([4]float64, error) {
	if strings.TrimSpace(raw) == "" {
		return [4]float64{0, 0, width, height}, nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ' ' || r == ',' || r == '\t' || r == '\n' })
	var vb [4]float64
	if len(fields) != 4 {
		return vb, errBoardSketch
	}
	for i, field := range fields {
		value, err := strconv.ParseFloat(field, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 1e6 {
			return vb, errBoardSketch
		}
		vb[i] = value
	}
	if vb[2] <= 0 || vb[3] <= 0 {
		return vb, errBoardSketch
	}
	return vb, nil
}

// boardSketchSimplify drops points within half a Board unit of the line
// through their neighbors (Ramer–Douglas–Peucker, as the pen does). A line
// still longer than the Board's point limit continues as further strokes
// that share their joining point, so no detail is lost.
func boardSketchSimplify(points []boardSketchPoint) [][]boardSketchPoint {
	kept := boardSketchRDP(points, 0.5)
	var parts [][]boardSketchPoint
	for len(kept) > boardMaxStrokePoints {
		parts = append(parts, kept[:boardMaxStrokePoints])
		kept = kept[boardMaxStrokePoints-1:]
	}
	return append(parts, kept)
}

func boardSketchRDP(points []boardSketchPoint, tolerance float64) []boardSketchPoint {
	if len(points) < 3 {
		return points
	}
	keep := make([]bool, len(points))
	keep[0], keep[len(points)-1] = true, true
	ranges := [][2]int{{0, len(points) - 1}}
	for len(ranges) > 0 {
		r := ranges[len(ranges)-1]
		ranges = ranges[:len(ranges)-1]
		a, b := points[r[0]], points[r[1]]
		dx, dy := b.x-a.x, b.y-a.y
		length := math.Hypot(dx, dy)
		farthest, distance := -1, tolerance
		for i := r[0] + 1; i < r[1]; i++ {
			p := points[i]
			d := math.Hypot(p.x-a.x, p.y-a.y)
			if length > 0 {
				d = math.Abs(dy*(p.x-a.x)-dx*(p.y-a.y)) / length
			}
			if d > distance {
				farthest, distance = i, d
			}
		}
		if farthest >= 0 {
			keep[farthest] = true
			ranges = append(ranges, [2]int{r[0], farthest}, [2]int{farthest, r[1]})
		}
	}
	kept := make([]boardSketchPoint, 0, len(points))
	for i, p := range points {
		if keep[i] {
			kept = append(kept, p)
		}
	}
	return kept
}

// boardSketchNormalize stores a stroke the way the Board's pen does
// (normalizeDrawing with a padding of 6 and a 12 x 12 minimum), so the
// renderers' width / (max x + 6) scale is exactly 1.
func boardSketchNormalize(points []boardSketchPoint) boardSketchStroke {
	const padding = 6
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, p := range points {
		minX, minY = math.Min(minX, p.x), math.Min(minY, p.y)
		maxX, maxY = math.Max(maxX, p.x), math.Max(maxY, p.y)
	}
	stroke := boardSketchStroke{
		x:      minX - padding,
		y:      minY - padding,
		width:  math.Max(12, maxX-minX+2*padding),
		height: math.Max(12, maxY-minY+2*padding),
		points: make([]BoardPoint, len(points)),
	}
	for i, p := range points {
		stroke.points[i] = BoardPoint{
			X: math.Round((p.x-minX+padding)*10) / 10,
			Y: math.Round((p.y-minY+padding)*10) / 10,
		}
	}
	return stroke
}

// boardSketchPath flattens SVG path data into one polyline per subpath, in
// the path's own coordinates, no further than tolerance from its curves.
func boardSketchPath(d string, tolerance float64) ([][]boardSketchPoint, error) {
	if len(d) > boardSketchMaxPathBytes {
		return nil, errBoardSketch
	}
	tokens := boardSketchTokens{src: d}
	var lines [][]boardSketchPoint
	var line []boardSketchPoint
	var current, start, control boardSketchPoint
	var command, previous byte
	moved, total := false, 0
	emit := func(p boardSketchPoint) {
		line = append(line, p)
		total++
	}
	finish := func() {
		if len(line) >= 2 {
			lines = append(lines, line)
		}
		line = nil
	}
parse:
	for {
		next, ok, err := tokens.command()
		if err != nil {
			return nil, err
		}
		switch {
		case ok:
			command = next
		case tokens.done():
			break parse
		case command == 0:
			return nil, errBoardSketch
		}
		relative := command >= 'a'
		base := boardSketchPoint{}
		if relative {
			base = current
		}
		upper := command &^ 0x20
		if upper != 'M' && upper != 'Z' {
			// Drawing starts at a moveto, or after a closepath at its start.
			if !moved {
				return nil, errBoardSketch
			}
			if line == nil {
				emit(current)
			}
		}
		switch upper {
		case 'Z':
			if len(line) > 0 {
				emit(start)
			}
			current = start
			finish()
			previous, command = 'Z', 0
			continue
		case 'M':
			p, err := tokens.point()
			if err != nil {
				return nil, err
			}
			finish()
			current = boardSketchPoint{base.x + p.x, base.y + p.y}
			start, moved = current, true
			emit(current)
			// Further pairs after a moveto are linetos.
			command = 'L' | (command & 0x20)
		case 'L':
			p, err := tokens.point()
			if err != nil {
				return nil, err
			}
			current = boardSketchPoint{base.x + p.x, base.y + p.y}
			emit(current)
		case 'H':
			x, err := tokens.number()
			if err != nil {
				return nil, err
			}
			current.x = base.x + x
			if !relative {
				current.x = x
			}
			emit(current)
		case 'V':
			y, err := tokens.number()
			if err != nil {
				return nil, err
			}
			current.y = base.y + y
			if !relative {
				current.y = y
			}
			emit(current)
		case 'C', 'S':
			var c1 boardSketchPoint
			if upper == 'C' {
				p, err := tokens.point()
				if err != nil {
					return nil, err
				}
				c1 = boardSketchPoint{base.x + p.x, base.y + p.y}
			} else {
				c1 = current
				if previous == 'C' || previous == 'S' {
					c1 = boardSketchPoint{2*current.x - control.x, 2*current.y - control.y}
				}
			}
			p2, err := tokens.point()
			if err != nil {
				return nil, err
			}
			p3, err := tokens.point()
			if err != nil {
				return nil, err
			}
			c2 := boardSketchPoint{base.x + p2.x, base.y + p2.y}
			end := boardSketchPoint{base.x + p3.x, base.y + p3.y}
			boardSketchCubic(current, c1, c2, end, tolerance, 0, emit)
			current, control = end, c2
		case 'Q', 'T':
			var q boardSketchPoint
			if upper == 'Q' {
				p, err := tokens.point()
				if err != nil {
					return nil, err
				}
				q = boardSketchPoint{base.x + p.x, base.y + p.y}
			} else {
				q = current
				if previous == 'Q' || previous == 'T' {
					q = boardSketchPoint{2*current.x - control.x, 2*current.y - control.y}
				}
			}
			p, err := tokens.point()
			if err != nil {
				return nil, err
			}
			end := boardSketchPoint{base.x + p.x, base.y + p.y}
			// A quadratic is the cubic with its control two thirds along.
			c1 := boardSketchPoint{current.x + 2*(q.x-current.x)/3, current.y + 2*(q.y-current.y)/3}
			c2 := boardSketchPoint{end.x + 2*(q.x-end.x)/3, end.y + 2*(q.y-end.y)/3}
			boardSketchCubic(current, c1, c2, end, tolerance, 0, emit)
			current, control = end, q
		case 'A':
			var values [3]float64
			for i := range values {
				value, err := tokens.number()
				if err != nil {
					return nil, err
				}
				values[i] = value
			}
			large, err := tokens.flag()
			if err != nil {
				return nil, err
			}
			sweep, err := tokens.flag()
			if err != nil {
				return nil, err
			}
			p, err := tokens.point()
			if err != nil {
				return nil, err
			}
			end := boardSketchPoint{base.x + p.x, base.y + p.y}
			boardSketchArc(current, end, values[0], values[1], values[2], large, sweep, tolerance, emit)
			current = end
		default:
			return nil, errBoardSketch
		}
		previous = upper
		// The parser emits at most 50,000 points before simplification
		// brings each stroke within the Board's point limit.
		if total > 50000 {
			return nil, errBoardSketch
		}
	}
	finish()
	if len(lines) == 0 {
		return nil, errBoardSketch
	}
	return lines, nil
}

// boardSketchCubic subdivides a cubic until its control points lie within
// tolerance of the chord, emitting every point after the start.
func boardSketchCubic(
	p0, p1, p2, p3 boardSketchPoint,
	tolerance float64,
	depth int,
	emit func(boardSketchPoint),
) {
	dx, dy := p3.x-p0.x, p3.y-p0.y
	length := math.Hypot(dx, dy)
	far := func(p boardSketchPoint) float64 {
		if length == 0 {
			return math.Hypot(p.x-p0.x, p.y-p0.y)
		}
		return math.Abs(dy*(p.x-p0.x)-dx*(p.y-p0.y)) / length
	}
	if depth >= 12 || math.Max(far(p1), far(p2)) <= tolerance {
		emit(p3)
		return
	}
	mid := func(a, b boardSketchPoint) boardSketchPoint {
		return boardSketchPoint{(a.x + b.x) / 2, (a.y + b.y) / 2}
	}
	p01, p12, p23 := mid(p0, p1), mid(p1, p2), mid(p2, p3)
	p012, p123 := mid(p01, p12), mid(p12, p23)
	split := mid(p012, p123)
	boardSketchCubic(p0, p01, p012, split, tolerance, depth+1, emit)
	boardSketchCubic(split, p123, p23, p3, tolerance, depth+1, emit)
}

// boardSketchArc draws an SVG elliptical arc by its center
// parameterization (SVG 1.1, appendix F.6.5).
func boardSketchArc(
	from, to boardSketchPoint,
	rx, ry, rotation float64,
	large, sweep bool,
	tolerance float64,
	emit func(boardSketchPoint),
) {
	rx, ry = math.Abs(rx), math.Abs(ry)
	if rx == 0 || ry == 0 || from == to {
		emit(to)
		return
	}
	phi := rotation * math.Pi / 180
	cos, sin := math.Cos(phi), math.Sin(phi)
	dx, dy := (from.x-to.x)/2, (from.y-to.y)/2
	x1 := cos*dx + sin*dy
	y1 := -sin*dx + cos*dy
	// Radii too small for the endpoints grow just enough.
	if lambda := x1*x1/(rx*rx) + y1*y1/(ry*ry); lambda > 1 {
		rx, ry = rx*math.Sqrt(lambda), ry*math.Sqrt(lambda)
	}
	numerator := rx*rx*ry*ry - rx*rx*y1*y1 - ry*ry*x1*x1
	factor := math.Sqrt(math.Max(0, numerator/(rx*rx*y1*y1+ry*ry*x1*x1)))
	if large == sweep {
		factor = -factor
	}
	cx1, cy1 := factor*rx*y1/ry, -factor*ry*x1/rx
	cx := cos*cx1 - sin*cy1 + (from.x+to.x)/2
	cy := sin*cx1 + cos*cy1 + (from.y+to.y)/2
	angle := func(ux, uy, vx, vy float64) float64 { return math.Atan2(ux*vy-uy*vx, ux*vx+uy*vy) }
	theta := angle(1, 0, (x1-cx1)/rx, (y1-cy1)/ry)
	delta := angle((x1-cx1)/rx, (y1-cy1)/ry, (-x1-cx1)/rx, (-y1-cy1)/ry)
	if !sweep && delta > 0 {
		delta -= 2 * math.Pi
	} else if sweep && delta < 0 {
		delta += 2 * math.Pi
	}
	// Segments short enough that the arc bulges at most tolerance.
	r := math.Max(rx, ry)
	step := 2 * math.Acos(math.Max(-1, 1-tolerance/r))
	n := int(math.Min(1024, math.Max(2, math.Ceil(math.Abs(delta)/math.Max(step, 1e-3)))))
	for i := 1; i <= n; i++ {
		t := theta + delta*float64(i)/float64(n)
		ex, ey := rx*math.Cos(t), ry*math.Sin(t)
		emit(boardSketchPoint{cos*ex - sin*ey + cx, sin*ex + cos*ey + cy})
	}
}

// boardSketchTokens reads SVG path data: command letters, numbers in any
// of the compact forms SVG allows, and single-digit arc flags.
type boardSketchTokens struct {
	src string
	at  int
}

func (t *boardSketchTokens) skip() {
	for t.at < len(t.src) && strings.IndexByte(" \t\r\n,", t.src[t.at]) >= 0 {
		t.at++
	}
}

func (t *boardSketchTokens) done() bool {
	t.skip()
	return t.at >= len(t.src)
}

func (t *boardSketchTokens) command() (byte, bool, error) {
	t.skip()
	if t.at >= len(t.src) {
		return 0, false, nil
	}
	c := t.src[t.at]
	if strings.IndexByte("MmLlHhVvCcSsQqTtAaZz", c) >= 0 {
		t.at++
		return c, true, nil
	}
	if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' {
		return 0, false, nil
	}
	return 0, false, errBoardSketch
}

func (t *boardSketchTokens) number() (float64, error) {
	t.skip()
	start := t.at
	if t.at < len(t.src) && (t.src[t.at] == '-' || t.src[t.at] == '+') {
		t.at++
	}
	digits, dot := 0, false
	for t.at < len(t.src) {
		c := t.src[t.at]
		switch {
		case c >= '0' && c <= '9':
			digits++
		case c == '.' && !dot:
			dot = true
		default:
			goto exponent
		}
		t.at++
	}
exponent:
	if digits > 0 && t.at < len(t.src) && (t.src[t.at] == 'e' || t.src[t.at] == 'E') {
		mark := t.at
		t.at++
		if t.at < len(t.src) && (t.src[t.at] == '-' || t.src[t.at] == '+') {
			t.at++
		}
		if t.at >= len(t.src) || t.src[t.at] < '0' || t.src[t.at] > '9' {
			t.at = mark
		}
		for t.at < len(t.src) && t.src[t.at] >= '0' && t.src[t.at] <= '9' {
			t.at++
		}
	}
	if digits == 0 {
		return 0, errBoardSketch
	}
	value, err := strconv.ParseFloat(t.src[start:t.at], 64)
	if err != nil || math.IsInf(value, 0) || math.Abs(value) > 1e6 {
		return 0, errBoardSketch
	}
	return value, nil
}

func (t *boardSketchTokens) point() (boardSketchPoint, error) {
	x, err := t.number()
	if err != nil {
		return boardSketchPoint{}, err
	}
	y, err := t.number()
	return boardSketchPoint{x, y}, err
}

func (t *boardSketchTokens) flag() (bool, error) {
	t.skip()
	if t.at < len(t.src) && (t.src[t.at] == '0' || t.src[t.at] == '1') {
		t.at++
		return t.src[t.at-1] == '1', nil
	}
	return false, errBoardSketch
}
