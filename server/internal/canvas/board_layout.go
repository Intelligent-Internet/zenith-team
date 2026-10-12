package canvas

import (
	"math"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
)

// The editor's text metrics: notes and text boxes at 17px with a 1.5 line
// height inside 20px padding, shape labels at 13px inside 15% padding. The
// server measures with the bundled Noto Sans and adds slack for the
// browser's system font, so text an Agent writes fits the object it gets.
const (
	boardNoteFont     = 17.0
	boardNoteLine     = 25.5
	boardNotePadding  = 20.0
	boardLabelFont    = 13.0
	boardLabelLine    = 18.0
	boardMeasureSlack = 1.15
)

var (
	boardFontOnce   sync.Once
	boardFontParsed *sfnt.Font
	boardFontErr    error
)

// boardParsedFont parses the bundled font once; a parsed font is safe for
// concurrent use, each face built from it is not.
func boardParsedFont() (*sfnt.Font, error) {
	boardFontOnce.Do(func() { boardFontParsed, boardFontErr = opentype.Parse(boardFont) })
	return boardFontParsed, boardFontErr
}

// boardMetrics measures text for one compose call.
type boardMetrics struct {
	note, label font.Face
}

func newBoardMetrics() (*boardMetrics, error) {
	parsed, err := boardParsedFont()
	if err != nil {
		return nil, err
	}
	face := func(size float64) (font.Face, error) {
		return opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	}
	note, err := face(boardNoteFont)
	if err != nil {
		return nil, err
	}
	label, err := face(boardLabelFont)
	if err != nil {
		return nil, err
	}
	return &boardMetrics{note: note, label: label}, nil
}

func (m *boardMetrics) width(face font.Face, text string) float64 {
	return float64(font.MeasureString(face, text)) / 64 * boardMeasureSlack
}

// lines counts the lines text wraps to at width: words wrap at spaces, a
// word wider than a line breaks anywhere, and newlines always break.
func (m *boardMetrics) lines(face font.Face, text string, width float64) int {
	total := 0
	for _, paragraph := range strings.Split(text, "\n") {
		count, used := 1, 0.0
		space := m.width(face, " ")
		for _, word := range strings.Fields(paragraph) {
			w := m.width(face, word)
			switch {
			case used == 0 && w <= width:
				used = w
			case used+space+w <= width:
				used += space + w
			default:
				if used > 0 {
					count++
				}
				for w > width {
					count++
					w -= width
				}
				used = w
			}
		}
		total += count
	}
	return max(1, total)
}

// noteSize fits a note's text: 240 x 190 like the editor's, widening and
// then growing taller for long text.
func (m *boardMetrics) noteSize(text string, minWidth, minHeight float64) (float64, float64) {
	width := minWidth
	for {
		lines := m.lines(m.note, text, width-2*boardNotePadding)
		height := float64(lines)*boardNoteLine + 2*boardNotePadding
		if height <= 1.4*width || width >= 480 {
			return width, math.Max(minHeight, math.Ceil(height))
		}
		width += 80
	}
}

// labelSize fits a shape's label inside the share of the shape the label
// may use: 70% of a rectangle, less of an ellipse, a diamond or a triangle.
func (m *boardMetrics) labelSize(shape, label string) (float64, float64) {
	usable := map[string]float64{"ellipse": 0.55, "diamond": 0.45, "triangle": 0.45}[shape]
	if usable == 0 {
		usable = 0.7
	}
	// A long label wraps rather than widening its box, so a chain of steps
	// stays compact.
	minWidth, minHeight, maxWidth := 160.0, 80.0, 240.0
	if shape != "rectangle" {
		minWidth, minHeight, maxWidth = 180, 110, 300
	}
	width := math.Min(maxWidth, math.Max(minWidth, m.width(m.label, label)/usable+8))
	lines := m.lines(m.label, label, width*usable)
	height := math.Max(minHeight, float64(lines)*boardLabelLine/usable+8)
	return math.Ceil(width), math.Ceil(height)
}

// compartmentSize fits a class or entity box: its name and every member on
// one line, between 160 and 360 wide, and a band per section.
func (m *boardMetrics) compartmentSize(title string, sections []string) (float64, float64) {
	width := m.width(m.label, title)
	height := boardCompartmentTitle
	for _, section := range sections {
		for _, line := range strings.Split(section, "\n") {
			width = math.Max(width, m.width(m.label, line))
		}
		height += boardCompartmentHeight(section)
	}
	return math.Ceil(math.Min(360, math.Max(160, width+20))), height
}

// boardKindMinimums are the smallest boxes the diagram kinds read well at.
var boardKindMinimums = map[BoardShapeType][2]float64{
	BoardShapeTypeRounded: {160, 64}, BoardShapeTypeTerminal: {160, 56}, BoardShapeTypeDatabase: {120, 110},
	BoardShapeTypeQueue: {180, 72}, BoardShapeTypeCloud: {180, 110}, BoardShapeTypeComponent: {160, 80},
	BoardShapeTypeDocument: {160, 90}, BoardShapeTypeNote: {160, 80},
}

// kindSize fits a node of a kind around its label: compartments for classes
// and entities, the name under an actor's figure, and room for a cylinder's
// rims, a cloud's bumps, a component's tabs or a document's wave.
func (m *boardMetrics) kindSize(kind BoardShapeType, label string, sections []string) (float64, float64) {
	switch kind {
	case BoardShapeTypeClass, BoardShapeTypeEntity:
		return m.compartmentSize(label, sections)
	case BoardShapeTypeActor:
		return math.Ceil(math.Max(80, math.Min(160, m.width(m.label, label)+16))), 108
	case "", BoardShapeTypeRectangle, BoardShapeTypeEllipse, BoardShapeTypeDiamond, BoardShapeTypeTriangle:
		return m.labelSize(string(kind), label)
	}
	w, h := m.labelSize("rectangle", label)
	switch kind {
	case BoardShapeTypeDatabase:
		h += 30
	case BoardShapeTypeCloud:
		w, h = w*1.3, h*1.3
	case BoardShapeTypeComponent:
		w += 20
	case BoardShapeTypeDocument:
		h += 20
	}
	minimum := boardKindMinimums[kind]
	return math.Ceil(math.Max(w, minimum[0])), math.Ceil(math.Max(h, minimum[1]))
}

// boardLaidOut is a node placed relative to its layout's origin.
type boardLaidOut struct {
	x, y, width, height float64
}

// boardGridLayout lays equal cells out in rows, a single row or a column;
// rows hold no more cells than fit the room's width (zero for no limit),
// unless a single row was asked for.
func boardGridLayout(sizes [][2]float64, layout string, room float64) ([]boardLaidOut, float64, float64) {
	cellW, cellH := 0.0, 0.0
	for _, size := range sizes {
		cellW, cellH = math.Max(cellW, size[0]), math.Max(cellH, size[1])
	}
	columns := int(math.Min(6, math.Ceil(math.Sqrt(float64(len(sizes))))))
	switch {
	case layout == "row" || layout == "" && len(sizes) <= 3:
		columns = len(sizes)
	case layout == "column":
		columns = 1
	}
	const gap = 30.0
	for layout != "row" && room > 0 && columns > 1 && float64(columns)*(cellW+gap)-gap > room {
		columns--
	}
	out := make([]boardLaidOut, len(sizes))
	for i := range sizes {
		out[i] = boardLaidOut{
			x: float64(i%columns) * (cellW + gap), y: float64(i/columns) * (cellH + gap),
			width: cellW, height: cellH,
		}
	}
	rows := (len(sizes) + columns - 1) / columns
	return out, float64(columns)*(cellW+gap) - gap, float64(rows)*(cellH+gap) - gap
}

// boardLayeredLayout lays a directed graph out left to right: cycles are
// broken where a depth-first walk finds them, each node goes one layer
// past its longest incoming path, and four barycenter sweeps order each
// layer to uncross edges. Connectors leave a node's right side and enter
// the next node's left side, as the Board draws them. A flow too wide for
// the room (width, height; zero for no limit) wraps into bands.
func boardLayeredLayout(
	sizes [][2]float64,
	edges [][2]int,
	labels []float64,
	room [2]float64,
) ([]boardLaidOut, float64, float64) {
	n := len(sizes)
	out := make([]boardLaidOut, n)
	if n == 0 {
		return out, 0, 0
	}
	next := make([][]int, n)
	for _, e := range edges {
		if e[0] != e[1] {
			next[e[0]] = append(next[e[0]], e[1])
		}
	}
	// Edges a depth-first walk sees going back up its path close a cycle;
	// layering ignores them.
	state := make([]int, n)
	back := map[[2]int]bool{}
	var walk func(int)
	walk = func(v int) {
		state[v] = 1
		for _, w := range next[v] {
			switch state[w] {
			case 0:
				walk(w)
			case 1:
				back[[2]int{v, w}] = true
			}
		}
		state[v] = 2
	}
	for v := range n {
		if state[v] == 0 {
			walk(v)
		}
	}
	// Longest-path layers over the remaining DAG, in topological order.
	indegree := make([]int, n)
	for v := range n {
		for _, w := range next[v] {
			if !back[[2]int{v, w}] {
				indegree[w]++
			}
		}
	}
	layer := make([]int, n)
	var queue []int
	for v := range n {
		if indegree[v] == 0 {
			queue = append(queue, v)
		}
	}
	for len(queue) > 0 {
		v := queue[0]
		queue = queue[1:]
		for _, w := range next[v] {
			if back[[2]int{v, w}] {
				continue
			}
			layer[w] = max(layer[w], layer[v]+1)
			if indegree[w]--; indegree[w] == 0 {
				queue = append(queue, w)
			}
		}
	}
	layers := 0
	for _, l := range layer {
		layers = max(layers, l+1)
	}
	rows := make([][]int, layers)
	for v := range n {
		rows[layer[v]] = append(rows[layer[v]], v)
	}
	// Barycenter sweeps: order each layer by the mean position of its
	// neighbors in the layer before (then after) it.
	position := make([]float64, n)
	reindex := func() {
		for _, row := range rows {
			for i, v := range row {
				position[v] = float64(i)
			}
		}
	}
	reindex()
	prev := make([][]int, n)
	for v := range n {
		for _, w := range next[v] {
			prev[w] = append(prev[w], v)
		}
	}
	for sweep := range 4 {
		order := make([]int, layers)
		for i := range order {
			order[i] = i
		}
		neighbors := prev
		if sweep%2 == 1 {
			for i, j := 0, len(order)-1; i < j; i, j = i+1, j-1 {
				order[i], order[j] = order[j], order[i]
			}
			neighbors = next
		}
		for _, l := range order {
			row := rows[l]
			center := make(map[int]float64, len(row))
			for _, v := range row {
				sum, count := 0.0, 0
				for _, w := range neighbors[v] {
					if layer[w] != layer[v] {
						sum += position[w]
						count++
					}
				}
				center[v] = position[v]
				if count > 0 {
					center[v] = sum / float64(count)
				}
			}
			sort.SliceStable(row, func(a, b int) bool { return center[row[a]] < center[row[b]] })
			reindex()
		}
	}
	// Columns as wide as their widest node, separated by enough room for the
	// widest label crossing that gap.
	c := boardColumns{
		sizes: sizes, edges: edges, rows: rows,
		widths: make([]float64, layers), heights: make([]float64, layers), gaps: make([]float64, layers),
	}
	for i := range c.gaps {
		c.gaps[i] = 120
	}
	for i, e := range edges {
		if i < len(labels) && layer[e[1]] == layer[e[0]]+1 {
			c.gaps[layer[e[0]]] = math.Max(c.gaps[layer[e[0]]], labels[i]+60)
		}
	}
	for l, row := range rows {
		for i, v := range row {
			if i > 0 {
				c.heights[l] += boardLayerNodeGap
			}
			c.heights[l] += sizes[v][1]
			c.widths[l] = math.Max(c.widths[l], sizes[v][0])
		}
	}
	return c.wrap(room)
}

const (
	boardLayerNodeGap = 50.0
	// boardBandGap is the least room between bands, enough for a label on a
	// connector turning back through it; boardBandClear keeps that turn off
	// the nodes above and below.
	boardBandGap   = 80.0
	boardBandClear = 24.0
)

// boardColumns is a layered graph ready to place: each layer is a column of
// nodes, top to bottom, with the gap after it.
type boardColumns struct {
	sizes                 [][2]float64
	edges                 [][2]int
	rows                  [][]int
	widths, heights, gaps []float64
}

// wrap lays the columns out in the fewest bands that fit the room, or, when
// none fit, in the count that needs the least zooming out to see whole.
func (c boardColumns) wrap(room [2]float64) ([]boardLaidOut, float64, float64) {
	layers := len(c.widths)
	out, w, h := c.stack([][2]int{{0, layers}})
	if room[0] <= 0 || room[1] <= 0 || w <= room[0] && h <= room[1] {
		return out, w, h
	}
	best, widest, count := math.Max(w/room[0], h/room[1]), 0.0, 1
	for _, width := range c.widths {
		widest = math.Max(widest, width)
	}
	total := w
	for k := 2; k <= layers; k++ {
		limit := math.Max(total/float64(k), widest)
		bands := c.pack(limit)
		for len(bands) > k {
			limit += 10
			bands = c.pack(limit)
		}
		if len(bands) <= count {
			continue
		}
		count = len(bands)
		laid, bw, bh := c.stack(bands)
		if zoom := math.Max(bw/room[0], bh/room[1]); zoom < best {
			out, w, h, best = laid, bw, bh, zoom
		}
		if best <= 1 {
			break
		}
	}
	return out, w, h
}

// pack splits the layers, in order, into bands no wider than limit.
func (c boardColumns) pack(limit float64) [][2]int {
	var bands [][2]int
	start, x := 0, 0.0
	for l, width := range c.widths {
		if l > start && x+c.gaps[l-1]+width > limit {
			bands = append(bands, [2]int{start, l})
			start, x = l, 0
		}
		if l > start {
			x += c.gaps[l-1]
		}
		x += width
	}
	return append(bands, [2]int{start, len(c.widths)})
}

// stack places each band's columns left to right, centered vertically in
// the band, and each band below the one before. A connector from a band
// to the next one that turns back left runs halfway between its ends, as
// the Board draws it, so the gap grows until that run clears both bands.
func (c boardColumns) stack(bands [][2]int) ([]boardLaidOut, float64, float64) {
	out := make([]boardLaidOut, len(c.sizes))
	band := make([]int, len(c.sizes))
	for v := range band {
		band[v] = -1
	}
	width, bottom := 0.0, 0.0
	for b, span := range bands {
		tallest, x := 0.0, 0.0
		for l := span[0]; l < span[1]; l++ {
			tallest = math.Max(tallest, c.heights[l])
		}
		for l := span[0]; l < span[1]; l++ {
			y := (tallest - c.heights[l]) / 2
			for _, v := range c.rows[l] {
				out[v] = boardLaidOut{
					x:      x + (c.widths[l]-c.sizes[v][0])/2,
					y:      y,
					width:  c.sizes[v][0],
					height: c.sizes[v][1],
				}
				band[v] = b
				y += c.sizes[v][1] + boardLayerNodeGap
			}
			x += c.widths[l]
			if l < span[1]-1 {
				x += c.gaps[l]
			}
		}
		width = math.Max(width, x)
		top := 0.0
		if b > 0 {
			top = bottom + boardBandGap
			for _, e := range c.edges {
				s, d := out[e[0]], out[e[1]]
				if band[e[0]] != b-1 || band[e[1]] != b || s.x+s.width+40 < d.x {
					continue
				}
				// Halfway between the ends, with the next band at top.
				from, to := s.y+s.height/2, d.y+d.height/2
				top = math.Max(
					top,
					math.Max(2*bottom+2*boardBandClear-from-to, from+to+2*boardBandClear),
				)
			}
			top = math.Ceil(top)
		}
		for l := span[0]; l < span[1]; l++ {
			for _, v := range c.rows[l] {
				out[v].y += top
			}
		}
		bottom = top + tallest
	}
	return out, width, bottom
}
