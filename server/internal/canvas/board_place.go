package canvas

import (
	"math"

	"github.com/google/uuid"
)

// An Agent's work goes where the person it works for can see it: the area
// they are looking at when it has room, the free space nearest to it when it
// does not, next to what the same Run already placed so one request stays
// together, and beside the Board's content when nobody is looking.

const (
	// boardPlaceMargin keeps new work this far from everything else.
	boardPlaceMargin = 32.0
	boardPlaceStep   = 40.0
	// boardPlaceRings bounds the search around an anchor, in steps.
	boardPlaceRings = 250
)

// The search runs under the Board's lock, so its cost is bounded however
// large the Board's objects or the requester's view: an obstacle costs a
// window's index the cells along its edges, never every cell it covers, and
// a very large area is tried on a coarser step.
type boardPlacement struct {
	obstacles []BoardVisualBounds
	// index answers free for the window being searched; nil when obstacles
	// changed since it was built.
	index *boardPlaceIndex
	focus BoardFocus
	// work is what this Run has placed on the Board, content everything.
	work    *BoardVisualBounds
	content *BoardVisualBounds
}

// newBoardPlacement takes every live object as an obstacle except the frame
// new work goes into.
func newBoardPlacement(items []BoardItem, focus BoardFocus, run, into uuid.UUID) *boardPlacement {
	p := &boardPlacement{focus: focus}
	for _, item := range items {
		if item.Deleted {
			continue
		}
		bounds := boardVisualItemBounds(item)
		p.content = boardUnionPtr(p.content, bounds)
		if run != uuid.Nil && item.CreatedByRunID != nil && *item.CreatedByRunID == run {
			p.work = boardUnionPtr(p.work, bounds)
		}
		if item.ID != into {
			p.obstacle(bounds)
		}
	}
	return p
}

func boardUnionPtr(current *BoardVisualBounds, bounds BoardVisualBounds) *BoardVisualBounds {
	if current == nil {
		return &bounds
	}
	union := boardVisualUnion(*current, bounds)
	return &union
}

func (p *boardPlacement) obstacle(bounds BoardVisualBounds) {
	p.obstacles = append(p.obstacles, BoardVisualBounds{
		X: bounds.X - boardPlaceMargin, Y: bounds.Y - boardPlaceMargin,
		Width: bounds.Width + 2*boardPlaceMargin, Height: bounds.Height + 2*boardPlaceMargin,
	})
	p.index = nil
}

// placed records new work: an obstacle for what follows and part of the
// Run's work area.
func (p *boardPlacement) placed(bounds BoardVisualBounds) {
	p.obstacle(bounds)
	p.work = boardUnionPtr(p.work, bounds)
	p.content = boardUnionPtr(p.content, bounds)
}

// search indexes the obstacles for the rectangles tried next, all of which
// lie within window.
func (p *boardPlacement) search(window BoardVisualBounds) {
	p.index = newBoardPlaceIndex(BoardVisualBounds{
		X: window.X - 1, Y: window.Y - 1, Width: window.Width + 2, Height: window.Height + 2,
	}, p.obstacles)
}

func (p *boardPlacement) free(r BoardVisualBounds) bool {
	if w := p.index; w != nil && r.X >= w.window.X && r.Y >= w.window.Y &&
		r.X+r.Width <= w.window.X+w.window.Width && r.Y+r.Height <= w.window.Y+w.window.Height {
		return w.clear(r, p.obstacles)
	}
	for _, o := range p.obstacles {
		if boardPlaceOverlaps(r, o) {
			return false
		}
	}
	return true
}

func boardPlaceOverlaps(r, o BoardVisualBounds) bool {
	return r.X < o.X+o.Width && o.X < r.X+r.Width && r.Y < o.Y+o.Height && o.Y < r.Y+r.Height
}

// boardPlaceStepFor is the step that tries at most about 40,000 positions
// across a span: the usual step, doubled for an area so large that a finer
// search would hold the Board's lock too long.
func boardPlaceStepFor(width, height float64) float64 {
	step := boardPlaceStep
	for (math.Max(width, 0)/step+1)*(math.Max(height, 0)/step+1) > 40000 {
		step *= 2
	}
	return step
}

// comfort is the part of the requester's view clear of the editor's
// toolbars.
func (p *boardPlacement) comfort() (BoardVisualBounds, bool) {
	if !p.focus.known() {
		return BoardVisualBounds{}, false
	}
	v := p.focus.View
	return BoardVisualBounds{
		X: v.X + v.Width*0.06, Y: v.Y + v.Height*0.06, Width: v.Width * 0.88, Height: v.Height * 0.88,
	}, true
}

// place finds a free width x height rectangle: inside the frame when one
// is given and it has room (reported by inside), else near the anchor.
func (p *boardPlacement) place(width, height float64, near, frame *BoardVisualBounds) (BoardVisualBounds, bool) {
	if frame != nil {
		inner := BoardVisualBounds{
			X: frame.X + 24, Y: frame.Y + 56, Width: frame.Width - 48, Height: frame.Height - 80,
		}
		// Fill a frame in reading order: rows top to bottom.
		p.search(inner)
		if r, ok := p.scan(inner, width, height, func(r BoardVisualBounds) float64 {
			return (r.Y-inner.Y)*1e4 + (r.X - inner.X)
		}); ok {
			return r, true
		}
		near = frame
	}
	var anchor [2]float64
	view, viewing := p.comfort()
	switch {
	case near != nil:
		anchor = [2]float64{near.X + near.Width + 60 + width/2, near.Y + height/2}
	case p.work != nil:
		anchor = [2]float64{p.work.X + p.work.Width + 60 + width/2, p.work.Y + height/2}
	case viewing:
		anchor = [2]float64{view.X + view.Width/2, view.Y + view.Height/2}
	case p.content != nil:
		anchor = [2]float64{p.content.X + p.content.Width + 160 + width/2, p.content.Y + height/2}
	default:
		anchor = [2]float64{width / 2, height / 2}
	}
	distance := func(r BoardVisualBounds) float64 {
		dx, dy := r.X+r.Width/2-anchor[0], r.Y+r.Height/2-anchor[1]
		// Ties prefer below, then to the right: how a person keeps writing.
		return math.Hypot(dx, dy) + 1e-3*(r.Y-anchor[1]) + 1e-6*(r.X-anchor[0])
	}
	// Content asked for beside an object goes there even when the person
	// looks elsewhere; otherwise it lands in their view when it has room.
	if viewing && near == nil {
		p.search(view)
		if r, ok := p.scan(view, width, height, distance); ok {
			return r, false
		}
		// Too big for what is left of the view: start inside it where the
		// person sees the most of it, rather than straddling its edges.
		p.search(
			BoardVisualBounds{
				X:      view.X,
				Y:      view.Y,
				Width:  view.Width + width,
				Height: view.Height + height,
			},
		)
		if r, ok := p.overflow(view, width, height, distance); ok {
			return r, false
		}
	}
	// Rings of candidates around the anchor; the first ring with room wins.
	originX, originY := anchor[0]-width/2, anchor[1]-height/2
	reach := boardPlaceRings * boardPlaceStep
	p.search(BoardVisualBounds{
		X: originX - reach, Y: originY - reach, Width: width + 2*reach, Height: height + 2*reach,
	})
	for ring := range boardPlaceRings + 1 {
		best, found, score := BoardVisualBounds{}, false, math.Inf(1)
		for dx := -ring; dx <= ring; dx++ {
			for dy := -ring; dy <= ring; dy++ {
				if max(abs(dx), abs(dy)) != ring {
					continue
				}
				r := BoardVisualBounds{
					X: originX + float64(
						dx,
					)*boardPlaceStep,
					Y:      originY + float64(dy)*boardPlaceStep,
					Width:  width,
					Height: height,
				}
				if s := distance(r); s < score && p.free(r) {
					best, found, score = r, true, s
				}
			}
		}
		if found {
			return best, false
		}
	}
	// Nothing near is free: beside everything is.
	x := 0.0
	if p.content != nil {
		x = p.content.X + p.content.Width + 160
	}
	return BoardVisualBounds{X: x, Y: anchor[1] - height/2, Width: width, Height: height}, false
}

// scan tries the grid positions of a width x height rectangle inside area
// and keeps the free one with the lowest score.
func (p *boardPlacement) scan(
	area BoardVisualBounds,
	width, height float64,
	score func(BoardVisualBounds) float64,
) (BoardVisualBounds, bool) {
	best, found, bestScore := BoardVisualBounds{}, false, math.Inf(1)
	if width > area.Width || height > area.Height {
		return best, false
	}
	step := boardPlaceStepFor(area.Width-width, area.Height-height)
	for x := area.X; x+width <= area.X+area.Width+1e-9; x += step {
		for y := area.Y; y+height <= area.Y+area.Height+1e-9; y += step {
			r := BoardVisualBounds{X: x, Y: y, Width: width, Height: height}
			if s := score(r); s < bestScore && p.free(r) {
				best, found, bestScore = r, true, s
			}
		}
	}
	return best, found
}

// overflow tries the grid positions inside area for the top-left corner of
// a rectangle that may run past it, and keeps the free one that leaves the
// most of it in the requester's view, the nearest to the anchor among equals.
func (p *boardPlacement) overflow(
	area BoardVisualBounds,
	width, height float64,
	distance func(BoardVisualBounds) float64,
) (BoardVisualBounds, bool) {
	view := p.focus.View
	best, found, bestScore := BoardVisualBounds{}, false, math.Inf(1)
	step := boardPlaceStepFor(area.Width, area.Height)
	for x := area.X; x <= area.X+area.Width; x += step {
		for y := area.Y; y <= area.Y+area.Height; y += step {
			r := BoardVisualBounds{X: x, Y: y, Width: width, Height: height}
			visibleW := math.Min(r.X+r.Width, view.X+view.Width) - math.Max(r.X, view.X)
			visibleH := math.Min(r.Y+r.Height, view.Y+view.Height) - math.Max(r.Y, view.Y)
			if visibleW <= 0 || visibleH <= 0 {
				continue
			}
			// Whole rows of a Board unit squared outweigh any distance.
			score := -math.Floor(visibleW*visibleH/1000)*1e4 + distance(r)
			if score < bestScore && p.free(r) {
				best, found, bestScore = r, true, score
			}
		}
	}
	return best, found
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// boardPlaceIndex answers whether a rectangle within one search window is
// clear of every obstacle. The window has at most 256 x 256 cells. An
// obstacle counts once in a summed-area table for the cells it covers whole
// and lists itself only in the cells along its edges, so a frame the size of
// the Board costs about what a note does.
type boardPlaceIndex struct {
	window     BoardVisualBounds
	cell       float64
	cols, rows int
	// covered and listed are summed-area tables over the cells, (cols+1) x
	// (rows+1): cells some obstacle covers whole, and cells that list one.
	covered, listed []int32
	// edges lists, per cell, the obstacles that cover part of it.
	edges [][]int
}

func newBoardPlaceIndex(window BoardVisualBounds, obstacles []BoardVisualBounds) *boardPlaceIndex {
	cell := math.Max(400, math.Max(window.Width, window.Height)/256)
	index := &boardPlaceIndex{
		window: window,
		cell:   cell,
		cols:   max(1, int(math.Ceil(window.Width/cell))),
		rows:   max(1, int(math.Ceil(window.Height/cell))),
	}
	cols, rows := index.cols, index.rows
	index.edges = make([][]int, cols*rows)
	// Whole cells are added as a two-dimensional difference: four corners
	// per obstacle, summed once below.
	whole := make([]int32, (cols+1)*(rows+1))
	for i, o := range obstacles {
		x0, x1, ix0, ix1, inX := index.axis(o.X, o.Width, window.X, cols)
		y0, y1, iy0, iy1, inY := index.axis(o.Y, o.Height, window.Y, rows)
		if !inX || !inY {
			continue
		}
		covers := ix0 <= ix1 && iy0 <= iy1
		if covers {
			whole[iy0*(cols+1)+ix0]++
			whole[iy0*(cols+1)+ix1+1]--
			whole[(iy1+1)*(cols+1)+ix0]--
			whole[(iy1+1)*(cols+1)+ix1+1]++
		}
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				if covers && y >= iy0 && y <= iy1 && x == ix0 {
					x = ix1
					continue
				}
				index.edges[y*cols+x] = append(index.edges[y*cols+x], i)
			}
		}
	}
	counts := make([]int32, cols*rows)
	for y := range rows {
		for x := range cols {
			v := whole[y*(cols+1)+x]
			if x > 0 {
				v += counts[y*cols+x-1]
			}
			if y > 0 {
				v += counts[(y-1)*cols+x]
			}
			if x > 0 && y > 0 {
				v -= counts[(y-1)*cols+x-1]
			}
			counts[y*cols+x] = v
		}
	}
	index.covered = index.summed(func(c int) bool { return counts[c] > 0 })
	index.listed = index.summed(func(c int) bool { return len(index.edges[c]) > 0 })
	return index
}

// axis is the range of cells along one axis that a span overlaps (first to
// last) and covers whole (innerFirst to innerLast), within the window;
// overlaps is false when it misses the window.
func (index *boardPlaceIndex) axis(
	start, length, origin float64,
	count int,
) (first, last, innerFirst, innerLast int, overlaps bool) {
	a, b := (start-origin)/index.cell, (start+length-origin)/index.cell
	first, last = int(math.Floor(a)), int(math.Ceil(b))-1
	if last < 0 || first >= count || b <= a {
		return 0, -1, 0, -1, false
	}
	innerFirst, innerLast = max(int(math.Ceil(a)), 0), min(int(math.Floor(b))-1, count-1)
	return max(first, 0), min(last, count-1), innerFirst, innerLast, true
}

func (index *boardPlaceIndex) summed(set func(cell int) bool) []int32 {
	cols, rows := index.cols, index.rows
	sums := make([]int32, (cols+1)*(rows+1))
	for y := range rows {
		for x := range cols {
			v := sums[y*(cols+1)+x+1] + sums[(y+1)*(cols+1)+x] - sums[y*(cols+1)+x]
			if set(y*cols + x) {
				v++
			}
			sums[(y+1)*(cols+1)+x+1] = v
		}
	}
	return sums
}

// any reports whether a summed-area table counts a cell in the inclusive
// range.
func (index *boardPlaceIndex) any(sums []int32, x0, x1, y0, y1 int) bool {
	if x0 > x1 || y0 > y1 {
		return false
	}
	w := index.cols + 1
	return sums[(y1+1)*w+x1+1]-sums[y0*w+x1+1]-sums[(y1+1)*w+x0]+sums[y0*w+x0] > 0
}

// clear reports whether r, within the window, overlaps no obstacle: none
// covers a cell r overlaps, none lists itself in a cell r covers whole, and
// none of those listed along r's edges overlaps it.
func (index *boardPlaceIndex) clear(r BoardVisualBounds, obstacles []BoardVisualBounds) bool {
	x0, x1, ix0, ix1, inX := index.axis(r.X, r.Width, index.window.X, index.cols)
	y0, y1, iy0, iy1, inY := index.axis(r.Y, r.Height, index.window.Y, index.rows)
	if !inX || !inY {
		return true
	}
	if index.any(index.covered, x0, x1, y0, y1) || index.any(index.listed, ix0, ix1, iy0, iy1) {
		return false
	}
	inner := ix0 <= ix1 && iy0 <= iy1
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			if inner && y >= iy0 && y <= iy1 && x == ix0 {
				x = ix1
				continue
			}
			for _, i := range index.edges[y*index.cols+x] {
				if boardPlaceOverlaps(r, obstacles[i]) {
					return false
				}
			}
		}
	}
	return true
}
