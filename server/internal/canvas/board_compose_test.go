package canvas

import (
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/google/uuid"
)

func composeTestItem(x, y, w, h float64) BoardItem {
	return BoardItem{
		ID: uuid.New(), Type: "sticky", BoardGeometry: BoardGeometry{X: x, Y: y, Width: w, Height: h},
		GeometryRevision: 1, ContentRevision: 1, TextRevision: 1, LifecycleRevision: 1,
	}
}

func overlaps(a, b BoardVisualBounds) bool {
	return a.X < b.X+b.Width && b.X < a.X+a.Width && a.Y < b.Y+b.Height && b.Y < a.Y+a.Height
}

func inside(inner, outer BoardVisualBounds) bool {
	return inner.X >= outer.X && inner.Y >= outer.Y &&
		inner.X+inner.Width <= outer.X+outer.Width && inner.Y+inner.Height <= outer.Y+outer.Height
}

var composeTestView = BoardFocus{Present: true, View: BoardVisualBounds{X: 1000, Y: 500, Width: 1600, Height: 900}}

func TestBoardPlacePutsWorkWhereTheRequesterLooks(t *testing.T) {
	t.Parallel()
	far := composeTestItem(-5000, -5000, 240, 190)
	p := newBoardPlacement([]BoardItem{far}, composeTestView, uuid.New(), uuid.Nil)
	got, _ := p.place(600, 300, nil, nil)
	comfort, _ := p.comfort()
	if !inside(got, comfort) {
		t.Fatalf("placed %+v; want inside the requester's view %+v", got, comfort)
	}
	if cx, cy := got.X+got.Width/2, got.Y+got.Height/2; cx < 1700 || cx > 1900 || cy < 850 || cy > 1050 {
		t.Fatalf("placed %+v; want it near the middle of the view", got)
	}
}

func TestBoardPlaceGoesToTheNearestFreeSpaceWhenTheViewIsFull(t *testing.T) {
	t.Parallel()
	var items []BoardItem
	for x := 900.0; x < 2700; x += 260 {
		for y := 400.0; y < 1500; y += 210 {
			items = append(items, composeTestItem(x, y, 240, 190))
		}
	}
	// A rotated object's whole turned extent is in the way, not its frame.
	turned := composeTestItem(2900, 800, 400, 40)
	turned.Rotation = 90
	items = append(items, turned)
	p := newBoardPlacement(items, composeTestView, uuid.New(), uuid.Nil)
	got, _ := p.place(500, 300, nil, nil)
	for _, item := range items {
		if overlaps(got, boardVisualItemBounds(item)) {
			t.Fatalf("placed %+v over %+v", got, item.BoardGeometry)
		}
	}
	view := composeTestView.View
	if dx := max(view.X-(got.X+got.Width), got.X-(view.X+view.Width), 0); dx > 600 {
		t.Fatalf("placed %+v; want the free space nearest the full view", got)
	}
}

func TestBoardPlaceKeepsOneRequestTogether(t *testing.T) {
	t.Parallel()
	run := uuid.New()
	earlier := composeTestItem(1200, 700, 400, 300)
	earlier.CreatedByRunID = &run
	p := newBoardPlacement([]BoardItem{earlier}, composeTestView, run, uuid.Nil)
	got, _ := p.place(300, 200, nil, nil)
	if got.X < earlier.X+earlier.Width || got.X > earlier.X+earlier.Width+200 || got.Y < 600 || got.Y > 800 {
		t.Fatalf("placed %+v; want it right after this Run's earlier work %+v", got, earlier.BoardGeometry)
	}
	p.placed(got)
	again, _ := p.place(300, 200, nil, nil)
	if again.X < got.X+got.Width {
		t.Fatalf("placed %+v; want it after %+v, the Run's latest work", again, got)
	}
}

func TestBoardPlaceFillsAFrameThenGoesBesideIt(t *testing.T) {
	t.Parallel()
	frame := composeTestItem(0, 0, 900, 500)
	frame.Type = "frame"
	child := composeTestItem(24, 56, 240, 190)
	child.ParentFrameID = &frame.ID
	p := newBoardPlacement([]BoardItem{frame, child}, BoardFocus{}, uuid.New(), frame.ID)
	bounds := BoardVisualBounds{X: 0, Y: 0, Width: 900, Height: 500}
	got, in := p.place(240, 190, nil, &bounds)
	if !in || !inside(got, bounds) || overlaps(got, boardVisualItemBounds(child)) || got.Y > 100 {
		t.Fatalf("placed %+v (inside %v); want beside the child on the frame's first row", got, in)
	}
	big, in := p.place(2000, 1000, nil, &bounds)
	if in || overlaps(big, bounds) {
		t.Fatalf("placed %+v (inside %v); want it beside a frame it does not fit", big, in)
	}
}

func TestBoardPlaceWithoutAViewerGoesBesideTheContent(t *testing.T) {
	t.Parallel()
	got, _ := newBoardPlacement(nil, BoardFocus{}, uuid.Nil, uuid.Nil).place(300, 200, nil, nil)
	if got.X != 0 || got.Y != 0 {
		t.Fatalf("on an empty Board placed %+v; want the origin", got)
	}
	item := composeTestItem(0, 0, 240, 190)
	got, _ = newBoardPlacement([]BoardItem{item}, BoardFocus{}, uuid.Nil, uuid.Nil).place(300, 200, nil, nil)
	if got.X < 240 || overlaps(got, boardVisualItemBounds(item)) {
		t.Fatalf("placed %+v; want it to the right of the content", got)
	}
}

func TestBoardLayeredLayoutFlowsLeftToRight(t *testing.T) {
	t.Parallel()
	sizes := [][2]float64{{160, 80}, {160, 80}, {160, 80}, {200, 120}}
	// a→b→c, a→d, and c→a closes a cycle.
	nodes, w, h := boardLayeredLayout(
		sizes,
		[][2]int{{0, 1}, {1, 2}, {0, 3}, {2, 0}},
		[]float64{0, 0, 300, 0},
		[2]float64{},
	)
	if !(nodes[0].x < nodes[1].x && nodes[1].x < nodes[2].x) || nodes[3].x != nodes[1].x+(160-200)/2 {
		t.Fatalf("layers = %+v; want a, then b and d, then c", nodes)
	}
	// The labeled a→d edge widens the first gap.
	if nodes[1].x-(nodes[0].x+160) < 300 {
		t.Fatalf("gap = %v; want room for the label", nodes[1].x-(nodes[0].x+160))
	}
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			a := BoardVisualBounds{
				X:      nodes[i].x,
				Y:      nodes[i].y,
				Width:  nodes[i].width,
				Height: nodes[i].height,
			}
			b := BoardVisualBounds{
				X:      nodes[j].x,
				Y:      nodes[j].y,
				Width:  nodes[j].width,
				Height: nodes[j].height,
			}
			if overlaps(a, b) {
				t.Fatalf("nodes %d and %d overlap: %+v", i, j, nodes)
			}
		}
		if nodes[i].x+nodes[i].width > w+1e-9 || nodes[i].y+nodes[i].height > h+1e-9 {
			t.Fatalf("node %d %+v is outside %vx%v", i, nodes[i], w, h)
		}
	}
}

// A long flow wraps into bands that fit the requester's view, as a person
// drawing on a narrow screen would: each band reads left to right below the
// one before, and the connector from a band's end back to the next band's
// start runs through the gap between them, clear of every node.
func TestBoardLayeredLayoutWrapsToTheRoom(t *testing.T) {
	t.Parallel()
	sizes := make([][2]float64, 9)
	var edges [][2]int
	for i := range sizes {
		sizes[i] = [2]float64{180, 90}
		if i > 0 && i < 8 {
			edges = append(edges, [2]int{i - 1, i})
		}
	}
	// Step 3 is a decision: its "no" side goes to a fix that loops back.
	edges = append(edges, [2]int{3, 8}, [2]int{8, 3})
	labels := make([]float64, len(edges))
	_, wide, _ := boardLayeredLayout(sizes, edges, labels, [2]float64{})
	room := [2]float64{830, 700}
	nodes, w, h := boardLayeredLayout(sizes, edges, labels, room)
	if wide <= room[0] || w > room[0] || h > room[1] {
		t.Fatalf("laid out %vx%v (%v in one row); want it wrapped into %v", w, h, wide, room)
	}
	box := func(n boardLaidOut) BoardVisualBounds {
		return BoardVisualBounds{X: n.x, Y: n.y, Width: n.width, Height: n.height}
	}
	for i := range nodes {
		for j := i + 1; j < len(nodes); j++ {
			if overlaps(box(nodes[i]), box(nodes[j])) {
				t.Fatalf("nodes %d and %d overlap: %+v", i, j, nodes)
			}
		}
		if nodes[i].x < 0 || nodes[i].y < 0 || nodes[i].x+nodes[i].width > w || nodes[i].y+nodes[i].height > h {
			t.Fatalf("node %d %+v is outside %vx%v", i, nodes[i], w, h)
		}
	}
	wrapped := 0
	for _, e := range edges[:7] {
		s, d := nodes[e[0]], nodes[e[1]]
		if d.x > s.x+s.width {
			continue
		}
		wrapped++
		// The next band starts below this one, and the Board's connector
		// turns back at the height halfway between the two ends.
		if d.y < s.y+s.height {
			t.Fatalf("step %d → %d goes back without moving down: %+v → %+v", e[0], e[1], s, d)
		}
		turn := (s.y + s.height/2 + d.y + d.height/2) / 2
		for i, n := range nodes {
			if turn >= n.y && turn <= n.y+n.height {
				t.Fatalf(
					"the connector %d → %d turns back at y=%v, through node %d %+v",
					e[0],
					e[1],
					turn,
					i,
					n,
				)
			}
		}
	}
	if wrapped == 0 {
		t.Fatalf("no step moved to a new band: %+v", nodes)
	}
}

func TestBoardGridLayoutFitsTheRoom(t *testing.T) {
	t.Parallel()
	sizes := make([][2]float64, 16)
	for i := range sizes {
		sizes[i] = [2]float64{240, 190}
	}
	if _, w, _ := boardGridLayout(sizes, "", 0); w <= 800 {
		t.Fatalf("grid without a room is %v wide; want its usual columns", w)
	}
	if _, w, _ := boardGridLayout(sizes, "", 800); w > 800 {
		t.Fatalf("grid in an 800 wide room is %v wide", w)
	}
	if _, w, _ := boardGridLayout(sizes, "row", 800); w <= 800 {
		t.Fatalf("an asked-for row is %v wide; want it kept in one row", w)
	}
}

func TestBoardMetricsFitText(t *testing.T) {
	t.Parallel()
	m, err := newBoardMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if w, h := m.noteSize("Short", 240, 190); w != 240 || h != 190 {
		t.Fatalf("short note = %vx%v; want the editor's 240x190", w, h)
	}
	long := ""
	for range 60 {
		long += "Tiếng Việt có dấu "
	}
	w, h := m.noteSize(long, 240, 190)
	if w <= 240 || h <= 190 || h > 1.4*w+boardNoteLine {
		t.Fatalf("long note = %vx%v; want it wider and taller, not a tall strip", w, h)
	}
	rw, rh := m.labelSize("rectangle", "Approve the budget")
	dw, dh := m.labelSize("diamond", "Approve the budget")
	if dw <= rw || dh <= rh {
		t.Fatalf("diamond %vx%v vs rectangle %vx%v; a diamond needs more room for its label", dw, dh, rw, rh)
	}
}

func TestBoardComposeBuildsOneNativeBatch(t *testing.T) {
	t.Parallel()
	existing := composeTestItem(5000, 5000, 240, 190)
	in := BoardComposeInput{
		RequestKey: uuid.New(),
		Ops: []BoardComposeOp{
			{
				Type:  "diagram",
				Frame: "Onboarding",
				Nodes: []BoardComposeNode{
					{Key: "signup", Text: "Sign up"},
					{Key: "verify", Text: "Verified?", Shape: "diamond"},
					{Key: "done", Text: "Welcome", Shape: "sticky", Fill: "green"},
				},
				Edges: []BoardComposeEdge{
					{From: "signup", To: "verify"},
					{From: "verify", To: "done", Label: "yes"},
				},
			},
			{
				Type: "notes",
				Items: []BoardComposeNote{
					{Text: "A"},
					{Text: "B", Color: "pink"},
					{Text: "C"},
					{Text: "D"},
				},
			},
			{Type: "text", Key: "title", Text: "Q4 plan"},
			{Type: "shape", Shape: "ellipse", Text: "Goal", Fill: "blue"},
			{
				Type:    "sketch",
				Width:   200,
				Height:  200,
				ViewBox: "0 0 10 10",
				Paths:   []BoardComposePath{{D: "M1 1 L9 9"}},
			},
			{Type: "connect", From: "title", To: existing.ID.String(), Label: "see"},
		},
	}
	if err := normalizeBoardComposeInput(in); err != nil {
		t.Fatal(err)
	}
	plan, err := boardComposeLayout(in, BoardFocus{})
	if err != nil {
		t.Fatal(err)
	}
	composed, err := boardComposeBuild(plan, in, []BoardItem{existing}, composeTestView, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := normalizeBoardBatch(composed.batch); err != nil {
		t.Fatalf("compiled batch is not a valid native batch: %v", err)
	}
	commands := composed.batch.Commands
	if commands[0].ItemType != "frame" || commands[0].Properties.Title != "Onboarding" {
		t.Fatalf("first command = %+v; want the diagram's frame", commands[0])
	}
	drafts := map[string]BoardCommand{}
	for _, command := range commands {
		drafts[command.DraftID] = command
	}
	if drafts["signup"].ParentRef != commands[0].DraftID || drafts["verify"].Properties.Shape != "diamond" ||
		drafts["verify"].Properties.Title != "Verified?" || drafts["done"].ItemType != "sticky" ||
		drafts["done"].Properties.Fill != "#dff3e3" {
		t.Fatalf("diagram nodes = %+v / %+v / %+v", drafts["signup"], drafts["verify"], drafts["done"])
	}
	if drafts["signup"].Geometry.X >= drafts["verify"].Geometry.X {
		t.Fatal("the diagram should flow left to right")
	}
	var edges, strokes int
	for _, command := range commands {
		switch {
		case command.Type == "create_edge":
			edges++
		case command.ItemType == "stroke":
			strokes++
			if command.Properties.Brush != "pen" || len(command.Points) < 2 {
				t.Fatalf("sketch stroke = %+v", command)
			}
		case command.Type == "create_item" && command.ItemType != "frame":
			g := command.Geometry
			placed := BoardVisualBounds{X: g.X, Y: g.Y, Width: g.Width, Height: g.Height}
			if !inside(placed, *composed.bounds) || overlaps(placed, boardVisualItemBounds(existing)) {
				t.Fatalf(
					"%s at %+v is outside the placed bounds %+v",
					command.DraftID,
					placed,
					composed.bounds,
				)
			}
		}
	}
	if edges != 3 || strokes != 1 || drafts["o6.1"].TargetRef != existing.ID.String() {
		t.Fatalf("edges = %d, strokes = %d, connect = %+v", edges, strokes, drafts["o6.1"])
	}
	if composed.placement != BoardPlacementTypeView {
		t.Fatalf("content at %+v should fit the empty view %+v", composed.bounds, composeTestView.View)
	}
}

// Each color names a swatch of its own palette: a fill from the fills, an
// outline or text color from the inks, with the obvious synonyms of each.
func TestBoardComposeColorsResolveInTheirOwnPalette(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]string{
		"rose": "#fce1eb", "red": "#fce1eb", "pink": "#fce1eb", "Blue": "#dff0fc", "none": "#00000000",
		"transparent": "#00000000", "#123456": "#123456", "graphite": "", "black": "",
	} {
		if got := boardComposeFill(value, ""); got != want {
			t.Errorf("fill %q = %q, want %q", value, got, want)
		}
	}
	for value, want := range map[string]string{
		"red": "#e85d75", "pink": "#e85d75", "black": "#25252d", "grey": "#25252d", "green": "#2d9470",
		"none": "",
	} {
		if got := boardComposeInk(value, ""); got != want {
			t.Errorf("ink %q = %q, want %q", value, got, want)
		}
	}
	plan, err := boardComposeLayout(BoardComposeInput{RequestKey: uuid.New(), Ops: []BoardComposeOp{{
		Type:  "diagram",
		Nodes: []BoardComposeNode{{Key: "a", Text: "A", Fill: "rose", Color: "blue"}},
	}}}, BoardFocus{})
	if err != nil {
		t.Fatal(err)
	}
	if node := plan.blocks[0].items[0].props; node.Fill != "#fce1eb" || node.Color != "#4c6fff" {
		t.Fatalf("a diagram node's fill and outline = %q, %q; want pink and blue", node.Fill, node.Color)
	}
}

func TestNormalizeBoardComposeInputRejectsWhatCannotBeBuilt(t *testing.T) {
	t.Parallel()
	for name, op := range map[string]BoardComposeOp{
		"empty notes":       {Type: "notes"},
		"empty text":        {Type: "text", Text: "  "},
		"unknown color":     {Type: "shape", Fill: "chartreuse"},
		"ink as a fill":     {Type: "shape", Fill: "graphite"},
		"fill as ink":       {Type: "text", Text: "x", Color: "none"},
		"node ink as fill":  {Type: "diagram", Nodes: []BoardComposeNode{{Key: "a", Fill: "graphite"}}},
		"dangling edge":     {Type: "diagram", Nodes: []BoardComposeNode{{Key: "a"}}, Edges: []BoardComposeEdge{{From: "a", To: "b"}}},
		"duplicate key":     {Type: "diagram", Nodes: []BoardComposeNode{{Key: "a"}, {Key: "a"}}},
		"bad key":           {Type: "text", Key: "a b", Text: "x"},
		"lonely arrange":    {Type: "arrange", Refs: []uuid.UUID{uuid.New()}, Align: "top"},
		"aimless arrange":   {Type: "arrange", Refs: []uuid.UUID{uuid.New(), uuid.New()}},
		"connect to nobody": {Type: "connect", From: "x", To: "y"},
	} {
		if err := normalizeBoardComposeInput(
			BoardComposeInput{RequestKey: uuid.New(), Ops: []BoardComposeOp{op}},
		); !errors.Is(
			err,
			ErrBoardInvalid,
		) {
			t.Errorf("%s: err = %v, want ErrBoardInvalid", name, err)
		}
	}
}

func TestBoardComposeArrangesWhatTheAgentRead(t *testing.T) {
	t.Parallel()
	a, b, c := composeTestItem(
		0,
		40,
		200,
		100,
	), composeTestItem(
		500,
		0,
		200,
		100,
	), composeTestItem(
		250,
		90,
		100,
		100,
	)
	byID := map[uuid.UUID]BoardItem{a.ID: a, b.ID: b, c.ID: c}
	op := BoardComposeOp{
		Type:       "arrange",
		Refs:       []uuid.UUID{a.ID, b.ID, c.ID},
		Align:      "top",
		Distribute: "horizontal",
		Gap:        20,
	}
	moves, err := boardComposeArrange([]BoardComposeOp{op}, byID, map[uuid.UUID]int64{a.ID: 3, b.ID: 4, c.ID: 5})
	if err != nil {
		t.Fatal(err)
	}
	at := map[uuid.UUID]BoardGeometry{}
	for _, move := range moves {
		at[*move.ID] = *move.Geometry
		if move.Type != "geometry" ||
			move.ExpectedRevision != map[uuid.UUID]int64{a.ID: 3, b.ID: 4, c.ID: 5}[*move.ID] {
			t.Fatalf("move = %+v; want a geometry command guarded by the revision read", move)
		}
	}
	if at[a.ID].X != 0 || at[c.ID].X != 220 || at[b.ID].X != 340 || at[a.ID].Y != 0 || at[c.ID].Y != 0 {
		t.Fatalf("arranged = %+v; want left to right, 20 apart, top aligned", at)
	}
	unread := map[uuid.UUID]int64{a.ID: 3}
	if _, err := boardComposeArrange([]BoardComposeOp{op}, byID, unread); !errors.Is(err, ErrBoardInvalid) {
		t.Fatalf("arranging unread objects = %v, want refused", err)
	}
}

// Aligning and then spacing the same objects, even naming one twice, moves
// each once to where both ops leave it, so the batch never conflicts with
// itself.
func TestBoardComposeArrangeAppliesOpsInTurn(t *testing.T) {
	t.Parallel()
	a, b := composeTestItem(0, 0, 200, 100), composeTestItem(300, 300, 200, 100)
	byID := map[uuid.UUID]BoardItem{a.ID: a, b.ID: b}
	ops := []BoardComposeOp{
		{Type: "arrange", Refs: []uuid.UUID{a.ID, b.ID, a.ID}, Align: "left"},
		{Type: "arrange", Refs: []uuid.UUID{b.ID, a.ID}, Distribute: "vertical", Gap: 10},
	}
	moves, err := boardComposeArrange(ops, byID, map[uuid.UUID]int64{a.ID: 2, b.ID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(moves) != 2 {
		t.Fatalf("moves = %+v; want one per object", moves)
	}
	at := map[uuid.UUID]BoardGeometry{}
	for _, move := range moves {
		at[*move.ID] = *move.Geometry
	}
	if at[a.ID].X != 0 || at[b.ID].X != 0 || at[a.ID].Y != 0 || at[b.ID].Y != 110 {
		t.Fatalf("arranged = %+v; want both at the left, b 10 below a", at)
	}
	in := BoardComposeInput{
		RequestKey: uuid.New(), Ops: ops,
		Revisions: []BoardComposeRevision{{ID: a.ID, Revision: 2}, {ID: b.ID, Revision: 7}},
	}
	composed, err := boardComposeBuild(
		boardComposePlan{arranges: ops},
		in,
		[]BoardItem{a, b},
		BoardFocus{},
		uuid.New(),
	)
	if err != nil || len(composed.batch.Commands) != 2 {
		t.Fatalf("composed %+v, %v; want two geometry commands", composed.batch.Commands, err)
	}
}

// Work larger than the free part of the view starts inside the view where the
// person sees the most of it, rather than straddling its edges.
func TestBoardPlaceShowsAsMuchAsItCanOfLargeWork(t *testing.T) {
	t.Parallel()
	var items []BoardItem
	for i := range 3 {
		items = append(items, composeTestItem(120+float64(i)*280, 80, 240, 190))
	}
	focus := BoardFocus{Present: true, View: BoardVisualBounds{X: 0, Y: 0, Width: 1600, Height: 900}}
	p := newBoardPlacement(items, focus, uuid.New(), uuid.Nil)
	got, _ := p.place(1640, 750, nil, nil)
	for _, item := range items {
		if overlaps(got, boardVisualItemBounds(item)) {
			t.Fatalf("placed %+v over %+v", got, item.BoardGeometry)
		}
	}
	comfort, _ := p.comfort()
	if got.X < comfort.X || got.Y < comfort.Y || got.X > comfort.X+80 || got.Y > 400 {
		t.Fatalf("placed %+v; want it to start inside the view, just below the notes, so most of it shows", got)
	}
}

// The answer says why work landed where it did, so the Agent tells its person
// the right thing: in their view, starting there but larger, beside a full
// view, beside a full frame, or beside the content when nobody looks.
func TestBoardComposeSaysWhyWorkLandedWhereItDid(t *testing.T) {
	t.Parallel()
	notes := func(n int) BoardComposeInput {
		in := BoardComposeInput{RequestKey: uuid.New(), Ops: []BoardComposeOp{{Type: "notes"}}}
		for range n {
			in.Ops[0].Items = append(in.Ops[0].Items, BoardComposeNote{Text: "Idea"})
		}
		in.Ops[0].Layout = "row"
		return in
	}
	flow := BoardComposeInput{RequestKey: uuid.New(), Ops: []BoardComposeOp{{Type: "diagram"}}}
	for i := range 8 {
		key := fmt.Sprintf("s%d", i)
		flow.Ops[0].Nodes = append(flow.Ops[0].Nodes, BoardComposeNode{Key: key, Text: "Step " + key})
		if i > 0 {
			flow.Ops[0].Edges = append(
				flow.Ops[0].Edges,
				BoardComposeEdge{From: fmt.Sprintf("s%d", i-1), To: key},
			)
		}
	}
	small := BoardFocus{Present: true, View: BoardVisualBounds{X: 0, Y: 0, Width: 900, Height: 600}}
	var crowded []BoardItem
	for x := -40.0; x < 960; x += 260 {
		for y := -40.0; y < 660; y += 210 {
			crowded = append(crowded, composeTestItem(x, y, 240, 190))
		}
	}
	frame := composeTestItem(4000, 0, 300, 300)
	frame.Type = "frame"
	full := notes(2)
	full.Into = &frame.ID
	for name, tt := range map[string]struct {
		in    BoardComposeInput
		items []BoardItem
		focus BoardFocus
		want  BoardPlacementType
	}{
		"fits the view":        {notes(2), nil, small, BoardPlacementTypeView},
		"larger than the view": {notes(6), nil, small, BoardPlacementTypePartly},
		"a long flow wraps":    {flow, nil, small, BoardPlacementTypeView},
		"the view is full":     {notes(2), crowded, small, BoardPlacementTypeNearby},
		"the frame is full":    {full, []BoardItem{frame}, small, BoardPlacementTypeFrameFull},
		"nobody looks":         {notes(2), nil, BoardFocus{}, BoardPlacementTypeUnwatched},
	} {
		plan, err := boardComposeLayout(tt.in, tt.focus)
		if err != nil {
			t.Fatal(err)
		}
		composed, err := boardComposeBuild(plan, tt.in, tt.items, tt.focus, uuid.New())
		if err != nil {
			t.Fatal(err)
		}
		if composed.placement != tt.want {
			t.Errorf(
				"%s: placement = %q at %+v, want %q",
				name,
				composed.placement,
				composed.bounds,
				tt.want,
			)
		}
	}
}

// Diagram nodes stay compact: a long label wraps rather than widening its
// box, so a chain of steps does not stretch far past the view.
func TestBoardMetricsWrapLongLabels(t *testing.T) {
	t.Parallel()
	m, err := newBoardMetrics()
	if err != nil {
		t.Fatal(err)
	}
	w, h := m.labelSize("rectangle", "2. Xác nhận thông tin & nhu cầu của khách hàng mới")
	if w > 240 || h <= 80 {
		t.Fatalf("long label = %vx%v; want at most 240 wide and taller instead", w, h)
	}
	if dw, _ := m.labelSize("diamond", "Đã kết nối máy tính?"); dw > 300 {
		t.Fatalf("diamond = %v wide; want at most 300", dw)
	}
}

// Content an Agent puts near an object goes beside that object, even when
// the person it works for looks elsewhere: adding a class to a diagram off
// screen extends that diagram rather than starting a stray one in view.
func TestBoardComposeNearGoesBesideTheObject(t *testing.T) {
	t.Parallel()
	target := composeTestItem(5000, 4000, 160, 90)
	in := BoardComposeInput{
		RequestKey: uuid.New(),
		Near:       &target.ID,
		Ops:        []BoardComposeOp{{Type: "shape", Shape: "class", Text: "Ellipse", Sections: []string{""}}},
	}
	focus := BoardFocus{Present: true, View: BoardVisualBounds{X: 0, Y: 0, Width: 900, Height: 600}}
	plan, err := boardComposeLayout(in, focus)
	if err != nil {
		t.Fatal(err)
	}
	composed, err := boardComposeBuild(plan, in, []BoardItem{target}, focus, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	b := composed.bounds
	if gap := math.Hypot(
		b.X+b.Width/2-(target.X+target.Width/2),
		b.Y+b.Height/2-(target.Y+target.Height/2),
	); gap > 600 {
		t.Fatalf(
			"placed at %+v, %v from the object at (%v, %v); want it beside the object",
			*b,
			gap,
			target.X,
			target.Y,
		)
	}
}
