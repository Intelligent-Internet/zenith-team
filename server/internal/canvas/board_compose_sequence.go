package canvas

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
)

// Sequence layout, in Board units: lifelines side by side, messages in rows
// from the top, each row leaving its label room above the line.
const (
	boardSequenceMinGap      = 40.0  // between neighboring headers
	boardSequenceMessageRoom = 48.0  // beside a message's label between its lifelines
	boardSequenceLabelWidth  = 320.0 // the widest a message label spreads
	boardSequenceFirstRow    = 40.0  // below the headers
	boardSequenceAfterRow    = 14.0
	boardSequenceAfterSelf   = 34.0 // a self message's loop and room below it
	boardSequenceOpenRoom    = 30.0 // a fragment's tag above its first row
	boardSequenceElseRoom    = 26.0 // an operand's guard above its first row
	boardSequenceCloseRoom   = 12.0
	boardSequenceTail        = 32.0 // below the last row
)

// boardComposeSequence lays a sequence diagram out: participants as
// lifelines of one height side by side, an actor's headed by its figure, far
// enough apart for the labels of the messages between them, and messages in
// order at rows down the lifelines, each a message connector at its row.
// Fragments enclose their messages' rows and lifelines, inner ones inside
// outer ones, with a divider at each else.
func boardComposeSequence(measure *boardMetrics, op BoardComposeOp, index int) (boardComposeBlock, []BoardCommand) {
	column := map[string]int{}
	widths := make([]float64, len(op.Nodes))
	// Messages start below the tallest head: an actor's figure over its name
	// stands taller than a box.
	header := boardLifelineHeader
	for j, node := range op.Nodes {
		column[node.Key] = j
		widths[j] = math.Ceil(math.Min(200, math.Max(100, measure.width(measure.label, node.Text)+24)))
		if node.Shape == string(BoardShapeTypeActor) {
			header = boardLifelineActorHeader
		}
	}
	labelWidth := func(label string) float64 {
		return math.Min(boardSequenceLabelWidth, measure.width(measure.label, label))
	}
	// Columns left to right, each far enough from every column a message
	// spans to it for that message's label, and past a self message's loop.
	centers := make([]float64, len(op.Nodes))
	reach := make([]float64, len(op.Nodes))
	for j := range op.Nodes {
		if j == 0 {
			centers[j] = widths[0] / 2
		} else {
			centers[j] = centers[j-1] + widths[j-1]/2 + widths[j]/2 + boardSequenceMinGap
			centers[j] = math.Max(centers[j], reach[j-1])
		}
		for _, edge := range op.Edges {
			from, to := column[edge.From], column[edge.To]
			switch {
			case from == j && to == j:
				reach[j] = math.Max(
					reach[j],
					centers[j]+boardSelfMessageWidth+labelWidth(edge.Label)+16,
				)
				if j+1 < len(op.Nodes) {
					reach[j] = math.Max(
						reach[j],
						centers[j]+boardSelfMessageWidth+labelWidth(
							edge.Label,
						)+widths[j+1]/2+16,
					)
				}
			case max(from, to) == j:
				centers[j] = math.Max(
					centers[j],
					centers[min(from, to)]+labelWidth(edge.Label)+boardSequenceMessageRoom,
				)
			}
		}
	}

	type event struct{ opens, closes []int }
	events := make([]event, len(op.Edges)+2)
	for f, fragment := range op.Fragments {
		events[fragment.First].opens = append(events[fragment.First].opens, f)
		events[fragment.Last].closes = append(events[fragment.Last].closes, f)
	}
	for k := range events {
		// Outer fragments open first and close last.
		slices.SortFunc(events[k].opens, func(a, b int) int {
			return op.Fragments[b].Last - op.Fragments[a].Last
		})
		slices.SortFunc(events[k].closes, func(a, b int) int {
			return op.Fragments[b].First - op.Fragments[a].First
		})
	}
	tops := make([]float64, len(op.Fragments))
	bottoms := make([]float64, len(op.Fragments))
	dividers := make([][]float64, len(op.Fragments))
	rows := make([]float64, len(op.Edges))
	y := header + boardSequenceFirstRow
	for k, edge := range op.Edges {
		message := k + 1
		for _, f := range events[message].opens {
			tops[f] = y - 4
			y += boardSequenceOpenRoom
		}
		for f, fragment := range op.Fragments {
			for _, branch := range fragment.Else {
				if branch.At == message {
					dividers[f] = append(dividers[f], y+2)
					y += boardSequenceElseRoom
				}
			}
		}
		lines := 1
		if edge.Label != "" {
			lines = measure.lines(measure.label, edge.Label, boardSequenceLabelWidth)
		}
		y += float64(lines) * boardLabelLine
		rows[k] = y
		if edge.From == edge.To {
			y += boardSequenceAfterSelf
		} else {
			y += boardSequenceAfterRow
		}
		for _, f := range events[message].closes {
			y += boardSequenceCloseRoom
			bottoms[f] = y - 4
		}
	}
	height := y + boardSequenceTail

	block := boardComposeBlock{}
	right := 0.0
	for j, node := range op.Nodes {
		props := boardComposeShape(
			string(BoardShapeTypeLifeline),
			node.Text,
			cmp.Or(node.Fill, "#ffffff"),
			node.Color,
		)
		if node.Shape == string(BoardShapeTypeActor) {
			props.Head = BoardHeadTypeActor
		}
		block.items = append(block.items, boardComposeItem{
			draft: node.Key, itemType: "shape", props: props,
			geometry: BoardVisualBounds{X: centers[j] - widths[j]/2, Width: widths[j], Height: height},
		})
		right = math.Max(right, math.Max(centers[j]+widths[j]/2, reach[j]))
	}
	// Fragments span their messages' lifelines, outer ones a little wider.
	depth := make([]int, len(op.Fragments))
	for f, fragment := range op.Fragments {
		for g, other := range op.Fragments {
			if g != f && other.First <= fragment.First && other.Last >= fragment.Last &&
				(other.First < fragment.First || other.Last > fragment.Last || g < f) {
				depth[f]++
			}
		}
	}
	for f, fragment := range op.Fragments {
		low, high := math.Inf(1), math.Inf(-1)
		for _, edge := range op.Edges[fragment.First-1 : fragment.Last] {
			for _, key := range []string{edge.From, edge.To} {
				low, high = math.Min(low, centers[column[key]]), math.Max(high, centers[column[key]])
			}
			if edge.From == edge.To {
				high = math.Max(
					high,
					centers[column[edge.From]]+boardSelfMessageWidth+labelWidth(edge.Label),
				)
			}
		}
		margin := math.Max(12, 36-10*float64(depth[f]))
		tag := measure.width(measure.label, fragment.Type) + 20
		x, w := low-margin, math.Max(high-low+2*margin, tag+60)
		guards := []string{boardGuard(fragment.Label)}
		var lines []float64
		for b, branch := range fragment.Else {
			guards = append(guards, boardGuard(branch.Label))
			lines = append(lines, dividers[f][b]-tops[f])
		}
		props := boardComposeShape(string(BoardShapeTypeFragment), fragment.Type, "none", "")
		props.Sections, props.Dividers = guards, lines
		block.items = append(block.items, boardComposeItem{
			draft: fmt.Sprintf("o%d.f%d", index, f+1), itemType: "shape", props: props,
			geometry: BoardVisualBounds{X: x, Y: tops[f], Width: w, Height: bottoms[f] - tops[f]},
		})
		right = math.Max(right, x+w)
	}
	block.width, block.height = right, height

	edges := make([]BoardCommand, 0, len(op.Edges))
	for k, edge := range op.Edges {
		c := boardComposeConnector(
			fmt.Sprintf("o%d.m%d", index, k+1), edge.From, edge.To, edge.Label, edge.Style, "", "",
			"sync", BoardRouteTypeMessage, false,
		)
		row := rows[k]
		c.AnchorY = &row
		edges = append(edges, c)
	}
	return block, edges
}

// boardGuard brackets an operand's condition, as UML writes it.
func boardGuard(label string) string {
	label = strings.TrimSpace(label)
	if label == "" || strings.HasPrefix(label, "[") {
		return label
	}
	return "[" + label + "]"
}
