package canvas

import (
	"cmp"
	"fmt"
	"math"
	"slices"
)

// Use case layout, in Board units.
const (
	boardUseCaseColumnGap = 40.0
	boardUseCaseRowGap    = 30.0
	boardUseCaseActorGap  = 100.0 // between the actors and the system
	boardUseCaseActorStep = 24.0  // between actors on one side
)

// boardComposeUseCase lays a use case diagram out: its cases inside the
// system's boundary, a frame titled by op.Frame, and its actors outside it,
// each level with the cases it links to. Base cases stand in the first
// column and the cases they include, or that extend them, in the next, each
// beside its base; actors linked to a base case stand on the left when they
// start any link, the others (only ever reached, or taking part only in
// included cases) on the right. Links then reach their cases without
// crossing the others.
func boardComposeUseCase(measure *boardMetrics, op BoardComposeOp, index int) (boardComposeBlock, []BoardCommand) {
	isActor := func(node BoardComposeNode) bool { return node.Shape == string(BoardShapeTypeActor) }
	position := map[string]int{}
	for j, node := range op.Nodes {
		position[node.Key] = j
	}
	// A case depends on the case that includes it, or that it extends.
	bases := map[int][]int{}
	linked := map[int][]int{}
	starts := map[string]bool{}
	for _, edge := range op.Edges {
		starts[edge.From] = true
		from, to := position[edge.From], position[edge.To]
		switch {
		case isActor(op.Nodes[from]) == isActor(op.Nodes[to]):
			if isActor(op.Nodes[from]) {
				continue
			}
			if edge.Style == "extends" {
				from, to = to, from
			}
			bases[to] = append(bases[to], from)
		case isActor(op.Nodes[from]):
			linked[from] = append(linked[from], to)
		default:
			linked[to] = append(linked[to], from)
		}
	}
	depth := map[int]int{}
	var walk func(j int) int
	walk = func(j int) int {
		if d, ok := depth[j]; ok {
			return d
		}
		depth[j] = 0 // a cycle of cases stands with its first
		d := 0
		for k, base := range bases[j] {
			if b := walk(base) + 1; k == 0 || b < d {
				d = b
			}
		}
		depth[j] = d
		return d
	}
	var cases, left, right []int
	for j, node := range op.Nodes {
		if !isActor(node) {
			cases = append(cases, j)
			walk(j)
		}
	}
	for j, node := range op.Nodes {
		if !isActor(node) {
			continue
		}
		base := len(linked[j]) == 0 || slices.ContainsFunc(linked[j], func(c int) bool { return depth[c] == 0 })
		if base && (starts[node.Key] || len(linked[j]) == 0) {
			left = append(left, j)
		} else {
			right = append(right, j)
		}
	}
	sizes := make([][2]float64, len(op.Nodes))
	cellW, cellH := 0.0, 0.0
	for j, node := range op.Nodes {
		kind := BoardShapeTypeEllipse
		if isActor(node) {
			kind = BoardShapeTypeActor
		}
		w, h := measure.kindSize(kind, node.Text, nil)
		sizes[j] = [2]float64{w, h}
		if !isActor(node) {
			cellW, cellH = math.Max(cellW, w), math.Max(cellH, h)
		}
	}
	// A case only the right actors reach stands in the last column, beside
	// them.
	rank := map[int]int{}
	for r, j := range left {
		rank[j] = r - len(left)
	}
	for r, j := range right {
		rank[j] = len(op.Nodes) + r
	}
	score := func(c int) int {
		best := 0
		for j, list := range linked {
			if r := rank[j]; slices.Contains(list, c) && (best == 0 || r < best) {
				best = r
			}
		}
		return best
	}
	columns := 1
	for _, c := range cases {
		columns = max(columns, depth[c]+1)
	}
	column := map[int]int{}
	for _, c := range cases {
		column[c] = depth[c]
		if depth[c] == 0 && score(c) >= len(op.Nodes) && len(bases[c]) == 0 &&
			!slices.ContainsFunc(cases, func(o int) bool { return slices.Contains(bases[o], c) }) {
			column[c] = columns - 1
		}
	}
	slices.SortStableFunc(cases, func(a, b int) int { return score(a) - score(b) })
	systemX := 0.0
	actorWidth := func(side []int) float64 {
		width := 0.0
		for _, j := range side {
			width = math.Max(width, sizes[j][0])
		}
		return width
	}
	if len(left) > 0 {
		systemX = actorWidth(left) + boardUseCaseActorGap
	}
	// The first column stacks its cases by their actors; each later one
	// stands its cases level with their bases, as near as they fit.
	centers := map[int]float64{}
	systemH := 0.0
	type slot struct {
		c int
		y float64
	}
	for col := range columns {
		var slots []slot
		next := 0.0
		for _, c := range cases {
			if column[c] != col {
				continue
			}
			y := next
			if col > 0 {
				sum, count := 0.0, 0
				for _, b := range bases[c] {
					if center, ok := centers[b]; ok {
						sum += center
						count++
					}
				}
				if count > 0 {
					y = sum/float64(count) - cellH/2
				}
			}
			slots = append(slots, slot{c, y})
			next += cellH + boardUseCaseRowGap
		}
		if col > 0 {
			slices.SortStableFunc(slots, func(a, b slot) int { return cmp.Compare(a.y, b.y) })
		}
		top := 0.0
		for _, w := range slots {
			y := math.Max(w.y, top)
			centers[w.c] = y + cellH/2
			top = y + cellH + boardUseCaseRowGap
			systemH = math.Max(systemH, y+cellH)
		}
	}
	systemW := float64(columns)*cellW + float64(columns-1)*boardUseCaseColumnGap + 2*boardComposeFrameSide
	systemH += boardComposeFrameTop + boardComposeFrameSide
	system := fmt.Sprintf("o%d.system", index)
	block := boardComposeBlock{items: []boardComposeItem{{
		draft: system, itemType: "frame",
		geometry: BoardVisualBounds{X: systemX, Width: systemW, Height: systemH},
		props:    BoardProperties{Title: op.Frame, Color: "#25252d", Fill: "#00000000"},
	}}}
	geometry := map[string]BoardVisualBounds{}
	for _, j := range cases {
		node := op.Nodes[j]
		cx := systemX + boardComposeFrameSide + float64(column[j])*(cellW+boardUseCaseColumnGap) + cellW/2
		cy := boardComposeFrameTop + centers[j]
		g := BoardVisualBounds{
			X:      cx - sizes[j][0]/2,
			Y:      cy - sizes[j][1]/2,
			Width:  sizes[j][0],
			Height: sizes[j][1],
		}
		geometry[node.Key] = g
		props := boardComposeShape(string(BoardShapeTypeEllipse), node.Text, node.Fill, node.Color)
		block.items = append(block.items, boardComposeItem{
			draft: node.Key, parent: system, itemType: "shape", geometry: g, props: props,
		})
	}
	// Each actor stands level with the middle of the cases it links to, and
	// actors on one side keep apart.
	place := func(side []int, x func(width float64) float64) float64 {
		type want struct {
			j int
			y float64
		}
		var wants []want
		for _, j := range side {
			key, sum, count := op.Nodes[j].Key, 0.0, 0
			for _, edge := range op.Edges {
				other := ""
				switch key {
				case edge.From:
					other = edge.To
				case edge.To:
					other = edge.From
				}
				if g, ok := geometry[other]; ok {
					sum += g.Y + g.Height/2
					count++
				}
			}
			y := systemH / 2
			if count > 0 {
				y = sum / float64(count)
			}
			wants = append(wants, want{j, y - sizes[j][1]/2})
		}
		slices.SortStableFunc(wants, func(a, b want) int { return cmp.Compare(a.y, b.y) })
		next, bottom := 0.0, 0.0
		for _, w := range wants {
			y := math.Max(w.y, next)
			node := op.Nodes[w.j]
			g := BoardVisualBounds{X: x(sizes[w.j][0]), Y: y, Width: sizes[w.j][0], Height: sizes[w.j][1]}
			props := boardComposeShape(
				string(BoardShapeTypeActor),
				node.Text,
				cmp.Or(node.Fill, "none"),
				node.Color,
			)
			block.items = append(
				block.items,
				boardComposeItem{draft: node.Key, itemType: "shape", geometry: g, props: props},
			)
			next = y + sizes[w.j][1] + boardUseCaseActorStep
			bottom = next - boardUseCaseActorStep
		}
		return bottom
	}
	leftWidth := actorWidth(left)
	leftBottom := place(left, func(width float64) float64 { return (leftWidth - width) / 2 })
	rightX := systemX + systemW + boardUseCaseActorGap
	rightBottom := place(right, func(float64) float64 { return rightX })
	block.width = systemX + systemW
	if len(right) > 0 {
		block.width = rightX + actorWidth(right)
	}
	block.height = math.Max(systemH, math.Max(leftBottom, rightBottom))

	edges := make([]BoardCommand, 0, len(op.Edges))
	for k, edge := range op.Edges {
		edges = append(edges, boardComposeConnector(
			fmt.Sprintf("o%d.e%d", index, k+1), edge.From, edge.To, edge.Label, edge.Style, "", "",
			"line", BoardRouteTypeStraight, false,
		))
	}
	return block, edges
}
