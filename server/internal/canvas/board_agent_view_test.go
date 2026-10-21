package canvas

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/protocol"
)

func agentViewItem(id, kind string, x, y, w, h float64) BoardItem {
	return BoardItem{
		ID:                uuid.MustParse(id),
		Type:              kind,
		BoardGeometry:     BoardGeometry{X: x, Y: y, Width: w, Height: h},
		GeometryRevision:  2,
		ContentRevision:   3,
		TextRevision:      4,
		LifecycleRevision: 5,
	}
}

func TestBoardShortRefsGrowOnlyWhereIDsCollide(t *testing.T) {
	t.Parallel()
	a := uuid.MustParse("a3f9c2e1-0000-4000-8000-000000000001")
	b := uuid.MustParse("a3f9c2e1-7000-4000-8000-000000000002")
	c := uuid.MustParse("77de0a12-0000-4000-8000-000000000003")
	refs := boardShortRefs([]uuid.UUID{a, b, c})
	if refs[c] != "77de0a12" {
		t.Fatalf("ref of a unique ID = %q, want its first 8 hex digits", refs[c])
	}
	if refs[a] == refs[b] || len(refs[a]) <= 8 || !strings.HasPrefix(
		strings.ReplaceAll(a.String(), "-", ""),
		refs[a],
	) {
		t.Fatalf("colliding refs = %q and %q; want longer distinct prefixes", refs[a], refs[b])
	}
	resolved := boardResolveRefs(refs, []string{"77DE0A12", "a3f9c2e1", refs[b], "ffffffff"})
	if resolved.ids[0] != c || resolved.ids[1] != b {
		t.Fatalf("resolved = %+v; want the unique ID and the longer colliding ref", resolved.ids)
	}
	if len(resolved.ambiguous) != 1 || resolved.ambiguous[0] != "a3f9c2e1" ||
		len(resolved.missing) != 1 || resolved.missing[0] != "ffffffff" {
		t.Fatalf("unresolved = %+v / %+v", resolved.ambiguous, resolved.missing)
	}
}

// Resolving an Agent's refs on a crowded Board costs about one pass over the
// Board, not one per ref: an AgentView resolves up to 100 refs among 10,000
// objects inside its transaction.
func TestBoardResolveRefsStaysLinearOnACrowdedBoard(t *testing.T) {
	ids := make([]uuid.UUID, 10000)
	for i := range ids {
		ids[i] = uuid.New()
	}
	refs := boardShortRefs(ids)
	wanted := make([]string, 100)
	for i := range wanted {
		wanted[i] = refs[ids[i*100]]
	}
	var resolved boardResolvedRefs
	allocations := testing.AllocsPerRun(3, func() { resolved = boardResolveRefs(refs, wanted) })
	if len(resolved.ids) != 100 || resolved.ids[7] != ids[700] {
		t.Fatalf("resolved %d refs; want all 100 in order", len(resolved.ids))
	}
	if allocations > 50000 {
		t.Fatalf("resolving 100 refs among 10,000 objects allocated %.0f times", allocations)
	}
}

func TestBoardAgentProjectionShowsWhatTheRequesterSees(t *testing.T) {
	t.Parallel()
	agent := uuid.New()
	frame := agentViewItem("f7c1d2e0-0000-4000-8000-000000000001", "frame", 1200, -400, 900, 600)
	frame.Properties.Title = "Ideas"
	note := agentViewItem("a3f9c2e1-0000-4000-8000-000000000002", "sticky", 1240, -380, 240, 190)
	note.ParentFrameID = &frame.ID
	note.Text = "Onboarding: cut\n10 steps to 4"
	shape := agentViewItem("b2c4d6e8-0000-4000-8000-000000000003", "shape", 1600, -100, 200, 120)
	shape.Properties = BoardProperties{Shape: "diamond", Title: "Ship?", Fill: "#dff0fc"}
	shape.CreatedBy = protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent}
	ink := agentViewItem("c0ffee00-0000-4000-8000-000000000004", "stroke", 1300, 0, 300, 40)
	ink.Properties.Color = "#4c6fff"
	far := agentViewItem("dddddddd-0000-4000-8000-000000000005", "sticky", 9000, 9000, 240, 190)
	far.Text = "Far away"
	edge := BoardEdge{
		ID:       uuid.MustParse("9b01c3aa-0000-4000-8000-000000000006"),
		SourceID: note.ID,
		TargetID: shape.ID,
		Label:    "leads to",
		Revision: 7,
	}
	view := boardAgentProjection(boardAgentProjectionInput{
		board:   Board{Title: "Sprint Q4", Sequence: 812},
		canEdit: true,
		agent:   agent,
		items:   []BoardItem{far, ink, shape, note, frame},
		edges:   []BoardEdge{edge},
		focus: BoardFocus{
			Present:      true,
			Name:         "Tuấn",
			View:         BoardVisualBounds{X: 1180, Y: -420, Width: 1600, Height: 900},
			SelectionIDs: []uuid.UUID{note.ID},
		},
	})
	for _, want := range []string{
		`Board "Sprint Q4", you can edit, sequence 812.`,
		"Tuấn is looking at 1180,-420 1600x900",
		"Tuấn selected: a3f9c2e1",
		`f7c1d2e0 frame 1200,-400 900x600 "Ideas"`,
		`a3f9c2e1 sticky 1240,-380 240x190 in f7c1d2e0 "Onboarding: cut\n10 steps to 4"`,
		`b2c4d6e8 shape:diamond 1600,-100 200x120 fill=blue by you "Ship?"`,
		"c0ffee00 stroke 1300,0 300x40 color=blue",
		`9b01c3aa edge a3f9c2e1->b2c4d6e8 "leads to"`,
		"1 drawing or image here; set image=true to see it.",
	} {
		if !strings.Contains(view.Text, want) {
			t.Errorf("view text lacks %q:\n%s", want, view.Text)
		}
	}
	if strings.Contains(view.Text, "Far away") || strings.Contains(view.Text, "dddddddd") {
		t.Fatalf("view lists an object outside the requester's view:\n%s", view.Text)
	}
	if strings.Index(view.Text, "f7c1d2e0 frame") > strings.Index(view.Text, "a3f9c2e1 sticky") {
		t.Fatalf("frames should come before their content:\n%s", view.Text)
	}
	revisions := map[string]BoardAgentRef{}
	for _, ref := range view.Refs {
		revisions[ref.Ref] = ref
	}
	if got := revisions["a3f9c2e1"]; got.ID != note.ID || got.GeometryRevision != 2 || got.TextRevision != 4 ||
		got.LifecycleRevision != 5 || revisions["9b01c3aa"].Revision != 7 || len(view.Refs) != 5 {
		t.Fatalf("refs = %+v; want every listed object with its revisions", view.Refs)
	}
	// An edit naming a few fields keeps the rest from what was read.
	if got := revisions["a3f9c2e1"]; got.Geometry == nil || *got.Geometry != note.BoardGeometry ||
		revisions["b2c4d6e8"].Properties == nil || revisions["b2c4d6e8"].Properties.Shape != "diamond" ||
		revisions["9b01c3aa"].Geometry != nil {
		t.Fatalf("refs = %+v; want each item's geometry and properties as read", view.Refs)
	}
	if view.Area == nil || view.Area.X >= 1180 || view.Area.Width <= 1600 {
		t.Fatalf("area = %+v; want the requester's view with a margin", view.Area)
	}
}

func TestBoardAgentProjectionFallsBackToTheWholeBoard(t *testing.T) {
	t.Parallel()
	note := agentViewItem("a3f9c2e1-0000-4000-8000-000000000002", "sticky", 0, 0, 240, 190)
	view := boardAgentProjection(boardAgentProjectionInput{
		board:   Board{Title: "Empty-ish", Sequence: 3},
		items:   []BoardItem{note},
		focus:   BoardFocus{},
		canEdit: false,
	})
	for _, want := range []string{
		`Board "Empty-ish", read only, sequence 3.`,
		"The person you work for is not viewing this Board; showing the whole Board",
		"a3f9c2e1 sticky 0,0 240x190",
	} {
		if !strings.Contains(view.Text, want) {
			t.Errorf("view text lacks %q:\n%s", want, view.Text)
		}
	}
}

func TestBoardAgentProjectionStaysWithinItsBudget(t *testing.T) {
	t.Parallel()
	items := make([]BoardItem, 0, 1000)
	for i := range 1000 {
		item := agentViewItem(uuid.NewString(), "sticky", float64(i%40)*260, float64(i/40)*210, 240, 190)
		item.Text = strings.Repeat("Unicode note ✓ ", 7)
		items = append(items, item)
	}
	for i := range 300 {
		ink := agentViewItem(uuid.NewString(), "stroke", float64(i)*10, -500, 80, 40)
		items = append(items, ink)
	}
	view := boardAgentProjection(boardAgentProjectionInput{
		board: Board{Title: "Fixture", Sequence: 1},
		items: items,
		focus: BoardFocus{Present: true, View: BoardVisualBounds{X: -600, Y: -600, Width: 12000, Height: 6000}},
	})
	if len(view.Text) > boardAgentViewBudget+512 {
		t.Fatalf("view text is %d bytes, want at most about %d", len(view.Text), boardAgentViewBudget)
	}
	if !strings.Contains(view.Text, "more objects not listed") {
		t.Fatalf("a truncated view must say so:\n%s", view.Text[len(view.Text)-400:])
	}
	if strings.Contains(view.Text, " stroke ") || !strings.Contains(view.Text, ", 300 strokes:") {
		t.Fatalf("touching strokes must read as one drawing:\n%s", view.Text[:600])
	}
}

func TestBoardAgentProjectionReadsSelectedObjectsInFull(t *testing.T) {
	t.Parallel()
	person := protocol.ActorRef{ID: uuid.New(), Kind: protocol.ActorKindUser, DisplayName: "Lan"}
	note := agentViewItem("a3f9c2e1-0000-4000-8000-000000000002", "sticky", 0, 0, 240, 190)
	note.Text, note.TextTruncated = strings.Repeat("x", 256), true
	note.CreatedBy, note.UpdatedBy = person, person
	long := strings.Repeat("long text ", 100)
	view := boardAgentProjection(boardAgentProjectionInput{
		board: Board{Title: "Detail", Sequence: 9},
		items: []BoardItem{note},
		refs:  []string{"a3f9c2e1", "deadbeef"},
		texts: map[uuid.UUID]string{note.ID: long},
	})
	for _, want := range []string{
		fmt.Sprintf("%q", long)[1:20],
		"created by Lan, updated by Lan",
		"Not on this Board: deadbeef",
	} {
		if !strings.Contains(view.Text, want) {
			t.Errorf("detail text lacks %q:\n%s", want, view.Text)
		}
	}
}

func TestBoardAgentProjectionReadsTouchingStrokesAsOneDrawing(t *testing.T) {
	t.Parallel()
	var items []BoardItem
	for i := range 5 {
		ink := agentViewItem(uuid.NewString(), "stroke", float64(i)*20, 0, 40, 40)
		ink.Properties.Color = "#e85d75"
		items = append(items, ink)
	}
	for i := range 3 {
		items = append(items, agentViewItem(uuid.NewString(), "stroke", 2000+float64(i)*20, 0, 40, 40))
	}
	lone := agentViewItem("c0ffee00-0000-4000-8000-000000000004", "stroke", 5000, 0, 40, 40)
	items = append(items, lone)
	view := boardAgentProjection(boardAgentProjectionInput{
		board: Board{Title: "Ink", Sequence: 1},
		items: items,
		area:  "all",
	})
	if got := strings.Count(view.Text, "drawing "); got != 2 {
		t.Fatalf("drawings = %d, want 2:\n%s", got, view.Text)
	}
	for _, want := range []string{
		", 5 strokes color=rose:", ", 3 strokes:", "c0ffee00 stroke 5000,0 40x40",
		"3 drawings or images here",
	} {
		if !strings.Contains(view.Text, want) {
			t.Errorf("view text lacks %q:\n%s", want, view.Text)
		}
	}
	if len(view.Refs) != 9 {
		t.Fatalf("refs = %d, want every stroke named", len(view.Refs))
	}
}

// An Agent reads diagram notation: a class's compartments, and a
// connector's route, line, markers, ends and relation where they differ
// from the Board's usual arrow; messages read in their order down the
// lifelines.
func TestBoardAgentProjectionDescribesDiagrams(t *testing.T) {
	t.Parallel()
	order := agentViewItem("0a0a0a0a-0000-4000-8000-000000000001", "shape", 0, 0, 200, 120)
	order.Properties = BoardProperties{
		Shape:    BoardShapeTypeClass,
		Title:    "Order",
		Sections: []string{"- id: UUID", "+ place()"},
	}
	line := agentViewItem("0b0b0b0b-0000-4000-8000-000000000002", "shape", 0, 300, 200, 100)
	line.Properties = BoardProperties{Shape: BoardShapeTypeClass, Title: "Line"}
	web := agentViewItem("0c0c0c0c-0000-4000-8000-000000000003", "shape", 400, 0, 120, 400)
	web.Properties = BoardProperties{Shape: BoardShapeTypeLifeline, Title: "Web", Head: BoardHeadTypeActor}
	api := agentViewItem("0d0d0d0d-0000-4000-8000-000000000004", "shape", 700, 0, 120, 400)
	api.Properties = BoardProperties{Shape: BoardShapeTypeLifeline, Title: "API"}
	many, first, second := "0..*", 100.0, 200.0
	composes := BoardEdge{
		ID: uuid.MustParse("0e0e0e0e-0000-4000-8000-000000000005"), SourceID: order.ID, TargetID: line.ID,
		Line: BoardLineTypeSolid, StartMarker: BoardMarkerTypeDiamondFilled, EndMarker: BoardMarkerTypeNone,
		Route: BoardRouteTypeElbow, TargetLabel: many, Relation: "composes",
	}
	reply := BoardEdge{
		ID: uuid.MustParse(
			"0f0f0f0f-0000-4000-8000-000000000006",
		),
		SourceID:  api.ID,
		TargetID:  web.ID,
		Label:     "200",
		Line:      BoardLineTypeDashed,
		EndMarker: BoardMarkerTypeOpen,
		Route:     BoardRouteTypeMessage,
		AnchorY:   &second,
	}
	request := BoardEdge{
		ID: uuid.MustParse(
			"1a1a1a1a-0000-4000-8000-000000000007",
		),
		SourceID: web.ID,
		TargetID: api.ID,
		Label:    "GET",
		Route:    BoardRouteTypeMessage,
		AnchorY:  &first,
	}
	view := boardAgentProjection(boardAgentProjectionInput{
		board: Board{Title: "Design"}, canEdit: true,
		items: []BoardItem{order, line, web, api},
		edges: []BoardEdge{composes, reply, request},
		focus: BoardFocus{Present: true, View: BoardVisualBounds{X: -100, Y: -100, Width: 1200, Height: 800}},
	})
	for _, want := range []string{
		`0a0a0a0a shape:class 0,0 200x120 "Order" sections: "- id: UUID" | "+ place()"`,
		`0e0e0e0e edge 0a0a0a0a->0b0b0b0b elbow start=diamond_filled end=none ends="","0..*" rel=composes`,
		`0f0f0f0f edge 0d0d0d0d->0c0c0c0c message at=200 dashed end=open "200"`,
		`0c0c0c0c shape:lifeline 400,0 120x400 head=actor "Web"`,
	} {
		if !strings.Contains(view.Text, want+"\n") {
			t.Errorf("reading lacks %q:\n%s", want, view.Text)
		}
	}
	if strings.Index(view.Text, "1a1a1a1a edge") > strings.Index(view.Text, "0f0f0f0f edge") {
		t.Errorf("messages are not in their order down the lifelines:\n%s", view.Text)
	}
}
