package canvas

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type composedDrafts struct {
	items map[string]BoardCommand
	edges []BoardCommand
}

// composeOnEmpty lays a request out and places it on an empty Board, in an
// empty view large enough for it.
func composeOnEmpty(t *testing.T, ops ...BoardComposeOp) composedDrafts {
	t.Helper()
	in := BoardComposeInput{RequestKey: uuid.New(), Ops: ops}
	if err := normalizeBoardComposeInput(in); err != nil {
		t.Fatalf("normalize = %v", err)
	}
	focus := BoardFocus{Present: true, View: BoardVisualBounds{X: 0, Y: 0, Width: 4000, Height: 3000}}
	plan, err := boardComposeLayout(in, focus)
	if err != nil {
		t.Fatal(err)
	}
	composed, err := boardComposeBuild(plan, in, nil, focus, uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	if err := normalizeBoardBatch(composed.batch); err != nil {
		t.Fatalf("the composed batch is not valid: %v", err)
	}
	out := composedDrafts{items: map[string]BoardCommand{}}
	for _, c := range composed.batch.Commands {
		if c.Type == "create_edge" {
			out.edges = append(out.edges, c)
		} else {
			out.items[c.DraftID] = c
		}
	}
	return out
}

func (d composedDrafts) edge(t *testing.T, from, to string) BoardCommand {
	t.Helper()
	for _, e := range d.edges {
		if e.SourceRef == from && e.TargetRef == to {
			return e
		}
	}
	t.Fatalf("no connector %s -> %s in %+v", from, to, d.edges)
	return BoardCommand{}
}

// A class hierarchy drawn down puts each parent above its children, joined by
// elbows ending in hollow triangles; classes carry their compartments.
func TestBoardComposeClassHierarchyFlowsDown(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(t, BoardComposeOp{
		Type: "diagram", Direction: "down",
		Nodes: []BoardComposeNode{
			{Key: "shape", Text: "Shape", Shape: "class", Sections: []string{"", "+ area(): double"}},
			{
				Key:      "circle",
				Text:     "Circle",
				Shape:    "class",
				Sections: []string{"- r: double", "+ area(): double"},
			},
			{Key: "rect", Text: "Rectangle", Shape: "class"},
			{Key: "drawing", Text: "Drawing", Shape: "class"},
		},
		Edges: []BoardComposeEdge{
			{From: "circle", To: "shape", Style: "inherits"},
			{From: "rect", To: "shape", Style: "inherits"},
			{From: "drawing", To: "shape", Style: "composes", FromEnd: "1", ToEnd: "0..*"},
		},
	})
	parent := d.items["shape"].Geometry
	for _, child := range []string{"circle", "rect"} {
		if g := d.items[child].Geometry; g.Y <= parent.Y+parent.Height {
			t.Errorf("%s at y=%v is not below its parent ending at %v", child, g.Y, parent.Y+parent.Height)
		}
		e := d.edge(t, child, "shape")
		if e.Route != BoardRouteTypeElbow || e.EndMarker != BoardMarkerTypeTriangle ||
			e.Relation != "inherits" {
			t.Errorf("%s inherits as %+v, want an elbow to a hollow triangle", child, e)
		}
	}
	if circle := d.items["circle"].Properties; circle.Shape != BoardShapeTypeClass ||
		len(circle.Sections) != 2 || circle.Sections[0] != "- r: double" {
		t.Errorf("circle = %+v, want a class with its compartments", circle)
	}
	if want := boardCompartmentTitle + boardCompartmentHeight(
		"- r: double",
	) + boardCompartmentHeight(
		"+ area(): double",
	); d.items["circle"].Geometry.Height < want {
		t.Errorf(
			"circle is %v tall, want room for its compartments (%v)",
			d.items["circle"].Geometry.Height,
			want,
		)
	}
	if composes := d.edge(t, "drawing", "shape"); composes.StartMarker != BoardMarkerTypeDiamondFilled ||
		composes.SourceLabel == nil || *composes.SourceLabel != "1" || composes.TargetLabel == nil ||
		*composes.TargetLabel != "0..*" {
		t.Errorf("composition = %+v, want a filled diamond at the whole and multiplicities", composes)
	}
}

// Between entities, the ends of a relationship are cardinalities: crow's feet.
func TestBoardComposeEntitiesDrawCrowsFeet(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(t, BoardComposeOp{
		Type: "diagram",
		Nodes: []BoardComposeNode{
			{
				Key:      "customers",
				Text:     "customers",
				Shape:    "entity",
				Sections: []string{"PK id uuid\nemail text"},
			},
			{
				Key:      "orders",
				Text:     "orders",
				Shape:    "entity",
				Sections: []string{"PK id uuid\nFK customer_id uuid"},
			},
		},
		Edges: []BoardComposeEdge{{From: "customers", To: "orders", FromEnd: "1", ToEnd: "0..*"}},
	})
	e := d.edge(t, "customers", "orders")
	if e.StartMarker != BoardMarkerTypeOne || e.EndMarker != BoardMarkerTypeZeroMany || e.SourceLabel != nil {
		t.Fatalf("relationship = %+v, want one to zero-or-many crow's feet", e)
	}
	if g := d.items["orders"].Geometry; g.Height < boardCompartmentTitle+boardCompartmentHeight("a\nb") {
		t.Fatalf("orders is %v tall, want room for two columns", g.Height)
	}
}

// A sequence lays its participants side by side with lifelines of one
// height, and its messages top to bottom in order at rows that leave room
// for their labels; a fragment encloses its messages' rows, split at its
// else.
func TestBoardComposeSequenceLaysMessagesTopToBottom(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(t, BoardComposeOp{
		Type: "sequence",
		Nodes: []BoardComposeNode{
			{
				Key:  "browser",
				Text: "Browser",
			},
			{Key: "api", Text: "API"},
			{Key: "auth", Text: "Auth service"},
		},
		Edges: []BoardComposeEdge{
			{From: "browser", To: "api", Label: "POST /login"},
			{From: "api", To: "auth", Label: "verify(credentials)"},
			{From: "auth", To: "api", Label: "token", Style: "reply"},
			{From: "api", To: "api", Label: "audit()"},
			{From: "api", To: "browser", Label: "401", Style: "reply"},
		},
		Fragments: []BoardComposeFragment{
			{
				Type:  "alt",
				Label: "valid",
				First: 3,
				Last:  5,
				Else:  []BoardComposeBranch{{At: 5, Label: "else"}},
			},
		},
	})
	var height float64
	for i, key := range []string{"browser", "api", "auth"} {
		g := d.items[key].Geometry
		if d.items[key].Properties.Shape != BoardShapeTypeLifeline || (i > 0 && g.Height != height) {
			t.Fatalf("%s = %+v, want lifelines of one height", key, d.items[key])
		}
		height = g.Height
	}
	if !(d.items["browser"].Geometry.X < d.items["api"].Geometry.X && d.items["api"].Geometry.X < d.items["auth"].Geometry.X) {
		t.Fatal("participants are not side by side in order")
	}
	previous := 0.0
	for i, e := range d.edges {
		if e.Route != BoardRouteTypeMessage || e.AnchorY == nil || *e.AnchorY <= previous {
			t.Fatalf("message %d = %+v, want rows top to bottom", i+1, e)
		}
		previous = *e.AnchorY
	}
	if self := d.edges[3]; self.SourceRef != "api" || self.TargetRef != "api" {
		t.Fatalf("the self message = %+v", self)
	}
	if reply := d.edges[2]; reply.Line != BoardLineTypeDashed || reply.EndMarker != BoardMarkerTypeOpen {
		t.Fatalf("a reply = %+v, want a dashed open arrow", reply)
	}
	var fragment BoardCommand
	for _, c := range d.items {
		if c.Properties != nil && c.Properties.Shape == BoardShapeTypeFragment {
			fragment = c
		}
	}
	if fragment.Properties == nil || fragment.Properties.Title != "alt" ||
		strings.Join(
			fragment.Properties.Sections,
			"|",
		) != "[valid]|[else]" || len(fragment.Properties.Dividers) != 1 {
		t.Fatalf("fragment = %+v", fragment.Properties)
	}
	top, bottom := fragment.Geometry.Y, fragment.Geometry.Y+fragment.Geometry.Height
	lifelineTop := d.items["api"].Geometry.Y
	for _, e := range d.edges[2:] {
		if row := lifelineTop + *e.AnchorY; row <= top || row >= bottom {
			t.Fatalf("message at row %v is outside its fragment %v..%v", row, top, bottom)
		}
	}
	if divider := top + fragment.Properties.Dividers[0]; divider <= lifelineTop+*d.edges[3].AnchorY ||
		divider >= lifelineTop+*d.edges[4].AnchorY {
		t.Fatalf("else divider at %v is not between messages 4 and 5", divider)
	}
}

// A class named and filled in Vietnamese composes as far as the request
// allows: every limit counts characters, not bytes.
func TestBoardComposeCountsCharactersNotBytes(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(t,
		BoardComposeOp{Type: "shape", Shape: "rounded", Text: strings.Repeat("ệ", 1000)},
		BoardComposeOp{Type: "diagram", Nodes: []BoardComposeNode{{
			Key: "order", Text: strings.TrimSpace(strings.Repeat("Đơn hàng ", 100)),
			Shape: "class", Sections: []string{strings.Repeat("ệ", 2000), "+ lưu()"},
		}}},
	)
	if len(d.items) != 2 {
		t.Fatalf("composed %d objects, want the shape and the class", len(d.items))
	}
}

// A class takes its attributes and methods, and an entity its columns, one
// member per entry; the Board draws them as compartments. A class sent one
// member per section, as Agents often do, is regrouped into attributes and
// methods instead of a compartment per member.
func TestBoardComposeGroupsClassMembersIntoCompartments(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(
		t,
		BoardComposeOp{Type: "diagram", Nodes: []BoardComposeNode{
			{
				Key:   "order",
				Text:  "Order",
				Shape: "class",
				Attributes: []string{
					"- id: UUID",
					"- total: Money",
				},
				Methods: []string{"+ place(): void"},
			},
			{Key: "row", Text: "orders", Shape: "entity", Columns: []string{"PK id uuid", "total numeric"}},
			{
				Key: "customer", Text: "Customer", Shape: "class",
				Sections: []string{"+ id: int", "+ name: text", "+ phone: text", "+ order(): Order"},
			},
			{
				Key:      "drink",
				Text:     "Drink",
				Shape:    "class",
				Sections: []string{"- size: Size", "+ price(): Money"},
			},
			{
				Key:      "split",
				Text:     "Split",
				Shape:    "class",
				Sections: []string{"- a\n- b", "+ c()", "«note»"},
			},
		}},
		BoardComposeOp{
			Type:    "shape",
			Key:     "line",
			Shape:   "class",
			Text:    "Line",
			Methods: []string{"+ cost(): Money"},
		},
	)
	for key, want := range map[string][]string{
		"order":    {"- id: UUID\n- total: Money", "+ place(): void"},
		"row":      {"PK id uuid\ntotal numeric"},
		"customer": {"+ id: int\n+ name: text\n+ phone: text", "+ order(): Order"},
		"drink":    {"- size: Size", "+ price(): Money"},
		"split":    {"- a\n- b", "+ c()", "«note»"},
		"line":     {"", "+ cost(): Money"},
	} {
		if got := d.items[key].Properties.Sections; strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s sections = %q, want %q", key, got, want)
		}
	}
}

// A sequence's participant may be an actor: a lifeline headed by the actor's
// figure, and the first message starts below the tallest head.
func TestBoardComposeSequenceTakesActorParticipants(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(t, BoardComposeOp{
		Type:  "sequence",
		Nodes: []BoardComposeNode{{Key: "user", Text: "User", Shape: "actor"}, {Key: "api", Text: "API"}},
		Edges: []BoardComposeEdge{{From: "user", To: "api", Label: "sign in"}},
	})
	user, api := d.items["user"], d.items["api"]
	if user.Properties.Shape != BoardShapeTypeLifeline || user.Properties.Head != BoardHeadTypeActor ||
		api.Properties.Head != "" || user.Geometry.Height != api.Geometry.Height {
		t.Fatalf("participants = %+v, %+v; want an actor lifeline", user.Properties, api.Properties)
	}
	if row := *d.edges[0].AnchorY; row <= boardLifelineActorHeader {
		t.Fatalf("first message at row %v; want it below the actor's head", row)
	}
}

// A fragment draws as many operands as a fragment shape holds: a guard and
// three else branches.
func TestBoardComposeSequenceDrawsAFragmentOfFourOperands(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(t, BoardComposeOp{
		Type:  "sequence",
		Nodes: []BoardComposeNode{{Key: "a", Text: "A"}, {Key: "b", Text: "B"}},
		Edges: []BoardComposeEdge{
			{From: "a", To: "b", Label: "one"},
			{From: "a", To: "b", Label: "two"},
			{From: "a", To: "b", Label: "three"},
			{From: "a", To: "b", Label: "four"},
		},
		Fragments: []BoardComposeFragment{{
			Type: "alt", Label: "1", First: 1, Last: 4,
			Else: []BoardComposeBranch{{At: 2, Label: "2"}, {At: 3, Label: "3"}, {At: 4, Label: "4"}},
		}},
	})
	for _, c := range d.items {
		if c.Properties != nil && c.Properties.Shape == BoardShapeTypeFragment {
			if len(c.Properties.Sections) != 4 || len(c.Properties.Dividers) != 3 {
				t.Fatalf("fragment = %+v, want four operands", c.Properties)
			}
			return
		}
	}
	t.Fatal("no fragment drawn")
}

// A use case diagram puts its cases inside the system's boundary and its
// actors outside: the initiating ones on the left, the ones only reached on
// the right; include and extend links are dashed and named.
func TestBoardComposeUseCasePutsActorsOutsideTheSystem(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(t, BoardComposeOp{
		Type: "usecase", Frame: "Online Shop",
		Nodes: []BoardComposeNode{
			{Key: "customer", Text: "Customer", Shape: "actor"},
			{Key: "browse", Text: "Browse catalog"},
			{Key: "order", Text: "Place order"},
			{Key: "pay", Text: "Pay"},
			{Key: "gateway", Text: "Payment Gateway", Shape: "actor"},
		},
		Edges: []BoardComposeEdge{
			{From: "customer", To: "browse"},
			{From: "customer", To: "order"},
			{From: "order", To: "pay", Style: "includes"},
			{From: "pay", To: "gateway"},
		},
	})
	var system BoardCommand
	for _, c := range d.items {
		if c.ItemType == "frame" && c.Properties.Title == "Online Shop" {
			system = c
		}
	}
	if system.Geometry == nil {
		t.Fatal("no system boundary")
	}
	for _, key := range []string{"browse", "order", "pay"} {
		if c := d.items[key]; c.ParentRef != system.DraftID || c.Properties.Shape != BoardShapeTypeEllipse {
			t.Errorf("use case %s = %+v, want an ellipse inside the system", key, c)
		}
	}
	s := system.Geometry
	if g := d.items["customer"].Geometry; g.X+g.Width > s.X {
		t.Errorf("Customer at %v reaches into the system at %v", g.X+g.Width, s.X)
	}
	if g := d.items["gateway"].Geometry; g.X < s.X+s.Width {
		t.Errorf("Payment Gateway at %v is not right of the system ending at %v", g.X, s.X+s.Width)
	}
	if e := d.edge(
		t,
		"order",
		"pay",
	); e.Line != BoardLineTypeDashed || e.Label != "«include»" ||
		e.Route != BoardRouteTypeStraight {
		t.Errorf("include = %+v", e)
	}
	if e := d.edge(t, "customer", "browse"); e.EndMarker != BoardMarkerTypeNone {
		t.Errorf("an association = %+v, want a plain line", e)
	}
}

// The base use cases stand in the first column and the ones they include, or
// that extend them, beside them to the right; an actor taking part only in
// those stands right of the system. No link then passes through a use case
// it does not join.
func TestBoardComposeUseCaseLinksPassNoOtherCase(t *testing.T) {
	t.Parallel()
	d := composeOnEmpty(t, BoardComposeOp{
		Type: "usecase", Frame: "Online Shop",
		Nodes: []BoardComposeNode{
			{Key: "customer", Text: "Customer", Shape: "actor"},
			{Key: "admin", Text: "Admin", Shape: "actor"},
			{Key: "gateway", Text: "Payment Gateway", Shape: "actor"},
			{Key: "view", Text: "View catalog"},
			{Key: "order", Text: "Place order"},
			{Key: "track", Text: "Track order"},
			{Key: "pay", Text: "Make payment"},
			{Key: "coupon", Text: "Apply coupon"},
			{Key: "manage", Text: "Manage catalog"},
		},
		Edges: []BoardComposeEdge{
			{From: "customer", To: "view"},
			{From: "customer", To: "order"},
			{From: "customer", To: "track"},
			{From: "order", To: "pay", Style: "includes"},
			{From: "coupon", To: "order", Style: "extends"},
			{From: "gateway", To: "pay"},
			{From: "admin", To: "manage"},
		},
	})
	cases := []string{"view", "order", "track", "pay", "coupon", "manage"}
	center := func(key string) (float64, float64) {
		g := d.items[key].Geometry
		return g.X + g.Width/2, g.Y + g.Height/2
	}
	for _, e := range d.edges {
		x0, y0 := center(e.SourceRef)
		x1, y1 := center(e.TargetRef)
		for _, key := range cases {
			if key == e.SourceRef || key == e.TargetRef {
				continue
			}
			// The segment against the case's ellipse, slightly inset, in the
			// ellipse's unit circle.
			g := d.items[key].Geometry
			cx, cy, a, b := g.X+g.Width/2, g.Y+g.Height/2, g.Width/2-2, g.Height/2-2
			px, py := (x0-cx)/a, (y0-cy)/b
			dx, dy := (x1-x0)/a, (y1-y0)/b
			t0 := math.Max(0, math.Min(1, -(px*dx+py*dy)/(dx*dx+dy*dy)))
			if qx, qy := px+t0*dx, py+t0*dy; qx*qx+qy*qy < 1 {
				t.Errorf("the link %s → %s passes through %q", e.SourceRef, e.TargetRef, key)
			}
		}
	}
	system := d.items["customer"].Geometry
	for _, c := range d.items {
		if c.ItemType == "frame" {
			system = c.Geometry
		}
	}
	if g := d.items["gateway"].Geometry; g.X < system.X+system.Width {
		t.Errorf("Payment Gateway at x=%v, want right of the system ending at %v", g.X, system.X+system.Width)
	}
	for _, pair := range [][2]string{{"order", "pay"}, {"order", "coupon"}} {
		if base, dependent := d.items[pair[0]].Geometry, d.items[pair[1]].Geometry; dependent.X <= base.X+base.Width {
			t.Errorf(
				"%s at x=%v, want right of its base %s ending at %v",
				pair[1],
				dependent.X,
				pair[0],
				base.X+base.Width,
			)
		}
	}
}

func TestBoardMetricsFitCompartments(t *testing.T) {
	t.Parallel()
	m, err := newBoardMetrics()
	if err != nil {
		t.Fatal(err)
	}
	sections := []string{"- id: UUID\n- placedAt: Instant", "+ place(): void"}
	w, h := m.compartmentSize("Order", sections)
	if want := boardCompartmentTitle + boardCompartmentHeight(
		sections[0],
	) + boardCompartmentHeight(
		sections[1],
	); h != want {
		t.Fatalf("height = %v, want %v", h, want)
	}
	if w < 160 || w > 360 {
		t.Fatalf("width = %v, want between 160 and 360", w)
	}
	long, _ := m.compartmentSize("Order", []string{strings.Repeat("x", 400)})
	if long != 360 {
		t.Fatalf("a long member widens the box to %v, want the 360 cap", long)
	}
}

func TestNormalizeBoardComposeDiagramsRejectsWhatCannotBeDrawn(t *testing.T) {
	t.Parallel()
	nodes := []BoardComposeNode{{Key: "a", Text: "A"}, {Key: "b", Text: "B"}}
	for name, op := range map[string]BoardComposeOp{
		"unknown style":            {Type: "diagram", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: "b", Style: "wavy"}}},
		"a message to an outsider": {Type: "sequence", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: uuid.NewString()}}},
		"a fragment past the end": {
			Type: "sequence", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: "b"}},
			Fragments: []BoardComposeFragment{{Type: "loop", First: 1, Last: 2}},
		},
		"an else outside its fragment": {
			Type: "sequence", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: "b"}, {From: "b", To: "a"}},
			Fragments: []BoardComposeFragment{{Type: "alt", First: 1, Last: 1, Else: []BoardComposeBranch{{At: 2}}}},
		},
		"crossing fragments": {
			Type: "sequence", Nodes: nodes,
			Edges:     []BoardComposeEdge{{From: "a", To: "b"}, {From: "b", To: "a"}, {From: "a", To: "b"}},
			Fragments: []BoardComposeFragment{{Type: "loop", First: 1, Last: 2}, {Type: "opt", First: 2, Last: 3}},
		},
		"a use case diagram of actors":   {Type: "usecase", Nodes: []BoardComposeNode{{Key: "a", Text: "A", Shape: "actor"}}},
		"a self link outside a sequence": {Type: "diagram", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: "a"}}},
		"a lifeline in a flow":           {Type: "diagram", Nodes: []BoardComposeNode{{Key: "a", Text: "A", Shape: "lifeline"}}},
		"sections on a database":         {Type: "shape", Shape: "database", Sections: []string{"x"}},
	} {
		in := BoardComposeInput{RequestKey: uuid.New(), Ops: []BoardComposeOp{op}}
		if err := normalizeBoardComposeInput(in); !errors.Is(err, ErrBoardInvalid) {
			t.Errorf("%s = %v, want invalid", name, err)
		}
	}
	many := BoardComposeOp{Type: "sequence"}
	for i := range 21 {
		many.Nodes = append(many.Nodes, BoardComposeNode{Key: fmt.Sprintf("p%d", i), Text: "P"})
	}
	if err := normalizeBoardComposeInput(
		BoardComposeInput{RequestKey: uuid.New(), Ops: []BoardComposeOp{many}},
	); err == nil {
		t.Error("a sequence of 21 participants was accepted")
	}
}

// A rejected drawing says what to change, so an Agent fixes it in one retry
// rather than guessing: the reason names the value and what it may be.
func TestNormalizeBoardComposeExplainsWhatItRejects(t *testing.T) {
	t.Parallel()
	nodes := []BoardComposeNode{{Key: "a", Text: "A"}, {Key: "b", Text: "B"}}
	for name, tt := range map[string]struct {
		op   BoardComposeOp
		hint string
	}{
		"an unknown style": {
			BoardComposeOp{Type: "diagram", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: "b", Style: "solid"}}},
			`"solid"`,
		},
		"a class style on a message": {
			BoardComposeOp{Type: "sequence", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: "b", Style: "inherits"}}},
			"sync, async or reply",
		},
		"an unknown shape": {
			BoardComposeOp{Type: "diagram", Nodes: []BoardComposeNode{{Key: "a", Text: "A", Shape: "server"}}},
			`"server"`,
		},
		"sections on a box": {
			BoardComposeOp{Type: "diagram", Nodes: []BoardComposeNode{{Key: "a", Text: "A", Sections: []string{"x"}}}},
			"class or entity",
		},
		"an end naming nothing": {
			BoardComposeOp{Type: "diagram", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: "c"}}},
			`"c"`,
		},
		"an else outside its fragment": {
			BoardComposeOp{
				Type: "sequence", Nodes: nodes, Edges: []BoardComposeEdge{{From: "a", To: "b"}, {From: "b", To: "a"}},
				Fragments: []BoardComposeFragment{{Type: "alt", First: 1, Last: 1, Else: []BoardComposeBranch{{At: 2}}}},
			},
			"else",
		},
		"a shape label longer than a title": {
			BoardComposeOp{Type: "shape", Shape: "rounded", Text: strings.Repeat("x", 1001)},
			"1000 characters",
		},
		// An Agent once sent a sequence's participants as items, a notes op's
		// field, and retried three times against a bare whiteboard_invalid.
		"a sequence's participants as items": {
			BoardComposeOp{Type: "sequence", Items: []BoardComposeNote{{Key: "a", Text: "A"}}},
			"nodes",
		},
		"a sequence with no participants": {BoardComposeOp{Type: "sequence"}, "nodes"},
		"notes with no items":             {BoardComposeOp{Type: "notes"}, "items"},
		"an empty text":                   {BoardComposeOp{Type: "text", Text: " "}, "text"},
		"a node without a key": {
			BoardComposeOp{Type: "diagram", Nodes: []BoardComposeNode{{Text: "A"}}},
			"key",
		},
		"a key used twice": {
			BoardComposeOp{Type: "diagram", Nodes: []BoardComposeNode{{Key: "a", Text: "A"}, {Key: "a", Text: "B"}}},
			`"a"`,
		},
		"a sketch with no paths": {BoardComposeOp{Type: "sketch"}, "paths"},
		"a connector to itself":  {BoardComposeOp{Type: "connect", From: "x", To: "x"}, "from and to"},
		"an arrange of one":      {BoardComposeOp{Type: "arrange", Refs: []uuid.UUID{uuid.New()}, Align: "left"}, "two"},
		"attributes on a box": {
			BoardComposeOp{Type: "diagram", Nodes: []BoardComposeNode{{Key: "a", Text: "A", Attributes: []string{"x"}}}},
			"attributes and methods belong to a class",
		},
		"columns on a class": {
			BoardComposeOp{Type: "diagram", Nodes: []BoardComposeNode{{Key: "a", Text: "A", Shape: "class", Columns: []string{"x"}}}},
			"columns belong to an entity",
		},
		"sections and attributes together": {
			BoardComposeOp{Type: "shape", Shape: "class", Sections: []string{"- a"}, Attributes: []string{"- b"}},
			"not both",
		},
		"members past a compartment": {
			BoardComposeOp{Type: "shape", Shape: "class", Attributes: []string{
				strings.Repeat("x", 500), strings.Repeat("x", 500), strings.Repeat("x", 500),
				strings.Repeat("x", 500), strings.Repeat("x", 500),
			}},
			"2000 characters",
		},
		"four else branches": {
			BoardComposeOp{
				Type: "sequence", Nodes: nodes,
				Edges: []BoardComposeEdge{
					{From: "a", To: "b"},
					{From: "b", To: "a"},
					{From: "a", To: "b"},
					{From: "b", To: "a"},
					{From: "a", To: "b"},
				},
				Fragments: []BoardComposeFragment{{
					Type: "alt", First: 1, Last: 5,
					Else: []BoardComposeBranch{{At: 2}, {At: 3}, {At: 4}, {At: 5}},
				}},
			},
			"at most 3 else",
		},
	} {
		err := normalizeBoardComposeInput(
			BoardComposeInput{RequestKey: uuid.New(), Ops: []BoardComposeOp{tt.op}},
		)
		var invalid BoardInvalidError
		if !errors.Is(err, ErrBoardInvalid) || !errors.As(err, &invalid) ||
			!strings.Contains(invalid.Reason, tt.hint) {
			t.Errorf("%s: err = %v, want a reason mentioning %s", name, err, tt.hint)
		}
	}
}
