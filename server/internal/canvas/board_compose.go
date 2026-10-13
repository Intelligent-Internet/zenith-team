package canvas

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// An Agent composes Board content by intent: what to add (notes, a text, a
// shape, a diagram, a sketch, connectors) or how to arrange what is there,
// never coordinates. The server measures and lays the content out, places it
// where the person the Run works for is looking, and applies it as one
// ordinary batch through the same kernel, receipts and fences as every edit.

type BoardComposeInput struct {
	RequestKey uuid.UUID `json:"requestKey" binding:"required"`
	// Near places new content beside an object; Into fills a frame.
	Near *uuid.UUID `json:"near"`
	Into *uuid.UUID `json:"into"`
	// Revisions are the geometry revisions the Agent read of the objects an
	// arrange op moves, so a collaborator's newer move conflicts.
	Revisions []BoardComposeRevision `json:"revisions" binding:"max=100,dive"`
	Ops       []BoardComposeOp       `json:"ops"       binding:"required,min=1,max=20,dive"`
	// Preview asks for an image of the placed content; it does not change it.
	Preview bool `json:"preview"`
}

type BoardComposeRevision struct {
	ID       uuid.UUID `json:"id"       binding:"required"`
	Revision int64     `json:"revision" binding:"min=1"`
}

// BoardComposeOp is one intent. Type selects the fields that apply:
// notes (items, layout), text (text, color), shape (shape, text, width,
// height, fill, color, sections), diagram (nodes, edges, direction),
// sequence (nodes as participants, edges as messages in order, fragments),
// usecase (nodes as actors and cases, edges as links, frame as the system),
// sketch (width, height, viewBox, paths), connect (from, to, label, style)
// and arrange (refs, align, distribute, gap). Frame wraps notes, a diagram
// or a sketch in a titled frame. Key names a text or shape so later ops
// connect to it. A class shape takes its members as attributes and methods,
// an entity as columns, one per entry, in place of sections (see
// boardComposeCompartments).
type BoardComposeOp struct {
	Type       string                 `json:"type"                 binding:"required,oneof=notes text shape diagram sketch connect arrange sequence usecase"`
	Key        string                 `json:"key,omitempty"        binding:"max=64"`
	Items      []BoardComposeNote     `json:"items,omitempty"      binding:"max=60,dive"`
	Layout     string                 `json:"layout,omitempty"     binding:"omitempty,oneof=grid row column"`
	Text       string                 `json:"text,omitempty"       binding:"max=4000"`
	Shape      string                 `json:"shape,omitempty"      binding:"omitempty,oneof=rectangle ellipse diamond triangle rounded terminal actor database queue cloud component document note lifeline class entity fragment"`
	Sections   []string               `json:"sections,omitempty"   binding:"max=4,dive,max=2000"`
	Attributes []string               `json:"attributes,omitempty" binding:"max=40,dive,max=500"`
	Methods    []string               `json:"methods,omitempty"    binding:"max=40,dive,max=500"`
	Columns    []string               `json:"columns,omitempty"    binding:"max=60,dive,max=500"`
	Direction  string                 `json:"direction,omitempty"  binding:"omitempty,oneof=right down"`
	Style      string                 `json:"style,omitempty"      binding:"max=20"`
	Fragments  []BoardComposeFragment `json:"fragments,omitempty"  binding:"max=20,dive"`
	Fill       string                 `json:"fill,omitempty"       binding:"max=16"`
	Color      string                 `json:"color,omitempty"      binding:"max=16"`
	Width      float64                `json:"width,omitempty"      binding:"gte=0,lte=4000"`
	Height     float64                `json:"height,omitempty"     binding:"gte=0,lte=4000"`
	Nodes      []BoardComposeNode     `json:"nodes,omitempty"      binding:"max=100,dive"`
	Edges      []BoardComposeEdge     `json:"edges,omitempty"      binding:"max=200,dive"`
	ViewBox    string                 `json:"viewBox,omitempty"    binding:"max=100"`
	Paths      []BoardComposePath     `json:"paths,omitempty"      binding:"max=64,dive"`
	From       string                 `json:"from,omitempty"       binding:"max=64"`
	To         string                 `json:"to,omitempty"         binding:"max=64"`
	Label      string                 `json:"label,omitempty"      binding:"max=500"`
	Refs       []uuid.UUID            `json:"refs,omitempty"       binding:"max=100"`
	Align      string                 `json:"align,omitempty"      binding:"omitempty,oneof=left center right top middle bottom"`
	Distribute string                 `json:"distribute,omitempty" binding:"omitempty,oneof=horizontal vertical"`
	Gap        float64                `json:"gap,omitempty"        binding:"gte=0,lte=1000"`
	Frame      string                 `json:"frame,omitempty"      binding:"max=200"`
}

type BoardComposeNote struct {
	Key   string `json:"key,omitempty"   binding:"max=64"`
	Text  string `json:"text"            binding:"max=4000"`
	Color string `json:"color,omitempty" binding:"max=16"`
}

type BoardComposeNode struct {
	Key   string `json:"key"             binding:"required,max=64"`
	Text  string `json:"text"            binding:"max=1000"`
	Shape string `json:"shape,omitempty" binding:"omitempty,oneof=rectangle ellipse diamond triangle rounded terminal actor database queue cloud component document note lifeline class entity fragment sticky"`
	// Sections are a class's or entity's compartments; Attributes, Methods
	// and Columns its members, as on a shape op.
	Sections   []string `json:"sections,omitempty"   binding:"max=4,dive,max=2000"`
	Attributes []string `json:"attributes,omitempty" binding:"max=40,dive,max=500"`
	Methods    []string `json:"methods,omitempty"    binding:"max=40,dive,max=500"`
	Columns    []string `json:"columns,omitempty"    binding:"max=60,dive,max=500"`
	// Fill is the box's fill; Color its outline and text, as on a shape.
	Fill  string `json:"fill,omitempty"  binding:"max=16"`
	Color string `json:"color,omitempty" binding:"max=16"`
}

type BoardComposeEdge struct {
	From  string `json:"from"            binding:"required,max=64"`
	To    string `json:"to"              binding:"required,max=64"`
	Label string `json:"label,omitempty" binding:"max=500"`
	// Style is a connector preset (see boardConnectorPresets); FromEnd and
	// ToEnd are crow's-foot cardinalities between entities, multiplicities
	// otherwise, or markers replacing the style's at that end.
	Style   string `json:"style,omitempty"   binding:"max=20"`
	FromEnd string `json:"fromEnd,omitempty" binding:"max=20"`
	ToEnd   string `json:"toEnd,omitempty"   binding:"max=20"`
}

// BoardComposeFragment is a sequence's combined fragment around messages
// First through Last (1-based), split into operands at each Else.
type BoardComposeFragment struct {
	Type  string               `json:"type"            binding:"required,oneof=alt opt loop par break critical"`
	Label string               `json:"label,omitempty" binding:"max=200"`
	First int                  `json:"first"           binding:"min=1"`
	Last  int                  `json:"last"            binding:"min=1"`
	Else  []BoardComposeBranch `json:"else,omitempty"  binding:"max=3,dive"`
}

// BoardComposeBranch starts another operand at message At.
type BoardComposeBranch struct {
	At    int    `json:"at"              binding:"min=1"`
	Label string `json:"label,omitempty" binding:"max=200"`
}

type BoardComposePath struct {
	D     string  `json:"d"               binding:"required,max=20000"`
	Color string  `json:"color,omitempty" binding:"max=16"`
	Width float64 `json:"width,omitempty" binding:"gte=0,lte=100"`
}

// BoardComposeResult is the committed batch and where it landed.
type BoardComposeResult struct {
	Mutation BoardMutation `json:"mutation"`
	// Bounds covers everything placed; nil when the call only connected or
	// arranged existing objects.
	Bounds *BoardVisualBounds `json:"bounds"`
	// Placement says why the content landed where it did; empty on a replay.
	Placement BoardPlacementType `json:"placement"`
	Preview   *BoardVisualRegion `json:"preview,omitempty"`
}

// BoardPlacementType says where composed content landed for the person the
// Run works for, so the Agent can tell them.
type BoardPlacementType string

const (
	// BoardPlacementTypeView is inside the area they are looking at.
	BoardPlacementTypeView BoardPlacementType = "view"
	// BoardPlacementTypePartly starts in their view but is larger than its
	// free part.
	BoardPlacementTypePartly BoardPlacementType = "partly"
	// BoardPlacementTypeNearby is the nearest free space beside a full view.
	BoardPlacementTypeNearby BoardPlacementType = "nearby"
	// BoardPlacementTypeFrameFull is beside a frame that had no room.
	BoardPlacementTypeFrameFull BoardPlacementType = "frame_full"
	// BoardPlacementTypeUnwatched is beside the content: nobody is looking.
	BoardPlacementTypeUnwatched BoardPlacementType = "unwatched"
)

var boardComposeKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// normalizeBoardComposeInput checks what binding tags cannot: each op has
// what its type needs, keys are unique, references name a key or an ID,
// and colors are the editor's names or hex values.
func normalizeBoardComposeInput(in BoardComposeInput) error {
	keys := map[string]bool{}
	key := func(k string) error {
		if k == "" {
			return nil
		}
		if !boardComposeKey.MatchString(k) {
			return boardInvalid("key %q is 1 to 64 letters, digits, - or _", k)
		}
		if keys[k] {
			return boardInvalid("key %q is used twice; each key names one new object", k)
		}
		keys[k] = true
		return nil
	}
	created := 0
	for _, op := range in.Ops {
		if err := key(op.Key); err != nil {
			return err
		}
		// A note's color is its fill; elsewhere color is ink.
		fills, inks := []string{op.Fill}, []string{op.Color}
		if len(op.Items) > 0 && op.Type != "notes" {
			return boardInvalid("items belong to a notes op; a %s op takes nodes", op.Type)
		}
		switch op.Type {
		case "notes":
			if len(op.Items) == 0 {
				return boardInvalid("a notes op needs items, one per sticky note")
			}
			for _, item := range op.Items {
				if err := key(item.Key); err != nil {
					return err
				}
				fills = append(fills, item.Color)
			}
			created += len(op.Items)
		case "text":
			if strings.TrimSpace(op.Text) == "" {
				return boardInvalid("a text op needs text")
			}
			created++
		case "shape":
			// A shape's text is its title.
			if utf8.RuneCountInString(op.Text) > 1000 {
				return boardInvalid(
					"a shape's text is at most 1000 characters; put longer text in a text or notes op",
				)
			}
			kind := BoardShapeType(cmp.Or(op.Shape, string(BoardShapeTypeRectangle)))
			if err := normalizeBoardComposeMembers(
				kind, op.Sections, op.Attributes, op.Methods, op.Columns,
			); err != nil {
				return err
			}
			created++
		case "diagram", "sequence", "usecase":
			if len(op.Nodes) == 0 {
				return boardInvalid("a %s op needs nodes, each with a key and text", op.Type)
			}
			for _, node := range op.Nodes {
				if node.Key == "" {
					return boardInvalid("node %q needs a key its edges can name", node.Text)
				}
				if err := key(node.Key); err != nil {
					return err
				}
				if err := normalizeBoardComposeNode(op.Type, node); err != nil {
					return err
				}
				fills, inks = append(fills, node.Fill), append(inks, node.Color)
			}
			if err := normalizeBoardComposeLinks(op); err != nil {
				return err
			}
			created += len(op.Nodes) + len(op.Edges) + len(op.Fragments)
			if op.Type == "usecase" {
				created++
			}
		case "sketch":
			if len(op.Paths) == 0 {
				return boardInvalid("a sketch op needs paths of SVG path data")
			}
			for _, path := range op.Paths {
				inks = append(inks, path.Color)
			}
			created += len(op.Paths)
		case "connect":
			if op.From == "" || op.To == "" || op.From == op.To {
				return boardInvalid("a connect op needs from and to, two different objects")
			}
			if err := boardComposeStyle(op.Style); err != nil {
				return err
			}
			created++
		case "arrange":
			if len(op.Refs) < 2 || op.Align == "" && op.Distribute == "" {
				return boardInvalid("an arrange op needs at least two refs and align or distribute")
			}
		}
		if op.Frame != "" {
			created++
		}
		for _, fill := range fills {
			if fill != "" && boardComposeFill(fill, "") == "" {
				return boardInvalid("fill %q is not a Board fill", fill)
			}
		}
		for _, ink := range inks {
			if ink != "" && boardComposeInk(ink, "") == "" {
				return boardInvalid("color %q is not a Board color", ink)
			}
		}
	}
	// Connectors name keys from this call or existing objects by ID.
	for _, op := range in.Ops {
		ends := [][2]string{{op.From, op.To}}
		if op.Type != "connect" {
			ends = nil
		}
		for _, edge := range op.Edges {
			ends = append(ends, [2]string{edge.From, edge.To})
		}
		for _, end := range ends {
			for _, ref := range end {
				if _, err := uuid.Parse(ref); err != nil && !keys[ref] {
					return boardInvalid("%q names no key in this call and no object", ref)
				}
			}
		}
	}
	// Beyond the per-op binding, one call creates at most 300 objects.
	if created > 300 {
		return ErrBoardLimit
	}
	return nil
}

// normalizeBoardComposeNode checks a node's kind for its op: a flow draws
// any box, a sequence lifelines, a use case diagram actors and ellipses.
func normalizeBoardComposeNode(opType string, node BoardComposeNode) error {
	kind := BoardShapeType(node.Shape)
	if kind != "" && node.Shape != "sticky" && !slices.Contains(boardShapeTypes, kind) {
		names := make([]string, len(boardShapeTypes))
		for i, t := range boardShapeTypes {
			names[i] = string(t)
		}
		return boardInvalid(
			"node %q: shape %q is not one of %s or sticky",
			node.Key,
			node.Shape,
			strings.Join(names, ", "),
		)
	}
	switch opType {
	case "sequence":
		if kind != "" && kind != BoardShapeTypeLifeline && kind != BoardShapeTypeActor {
			return boardInvalid("node %q: a sequence's participants are lifelines or actors", node.Key)
		}
	case "usecase":
		if kind != "" && kind != BoardShapeTypeActor && kind != BoardShapeTypeEllipse {
			return boardInvalid("node %q: a use case diagram draws actors and ellipses", node.Key)
		}
	default:
		if kind == BoardShapeTypeLifeline || kind == BoardShapeTypeFragment {
			return boardInvalid("node %q: %s belongs in a sequence op", node.Key, kind)
		}
	}
	if len(node.Sections) > 0 && kind != BoardShapeTypeClass && kind != BoardShapeTypeEntity {
		return boardInvalid("node %q: sections belong to class or entity nodes", node.Key)
	}
	if err := normalizeBoardComposeMembers(
		kind, node.Sections, node.Attributes, node.Methods, node.Columns,
	); err != nil {
		var invalid BoardInvalidError
		if errors.As(err, &invalid) {
			return boardInvalid("node %q: %s", node.Key, invalid.Reason)
		}
		return err
	}
	return nil
}

// normalizeBoardComposeMembers checks a class's or entity's members: a
// class takes attributes and methods, an entity columns, either in place of
// sections, and their compartments fit a shape's.
func normalizeBoardComposeMembers(kind BoardShapeType, sections, attributes, methods, columns []string) error {
	members := len(attributes)+len(methods)+len(columns) > 0
	switch {
	case len(attributes)+len(methods) > 0 && kind != BoardShapeTypeClass:
		return boardInvalid("attributes and methods belong to a class")
	case len(columns) > 0 && kind != BoardShapeTypeEntity:
		return boardInvalid("columns belong to an entity")
	case members && len(sections) > 0:
		return boardInvalid("give sections or members (attributes and methods, or columns), not both")
	case len(sections) == 0 && !members:
		return nil
	}
	return normalizeBoardShape(BoardProperties{
		Shape: kind, Sections: boardComposeCompartments(kind, sections, attributes, methods, columns),
	})
}

// boardComposeStyle checks a connector's style names a preset.
func boardComposeStyle(style string) error {
	if _, ok := boardConnectorPresets[style]; style != "" && !ok {
		return boardInvalid(
			"style %q is not one of %s",
			style,
			strings.Join(slices.Sorted(maps.Keys(boardConnectorPresets)), ", "),
		)
	}
	return nil
}

// normalizeBoardComposeLinks checks a diagram's, sequence's or use case
// diagram's connectors and a sequence's fragments: styles are presets,
// only messages return to their source, sequences and use case diagrams
// link their own nodes, and fragments cover messages, nested or apart.
func normalizeBoardComposeLinks(op BoardComposeOp) error {
	local := map[string]bool{}
	actors := 0
	for _, node := range op.Nodes {
		local[node.Key] = true
		if node.Shape == string(BoardShapeTypeActor) {
			actors++
		}
	}
	switch op.Type {
	case "sequence":
		if len(op.Nodes) > 20 || len(op.Edges) > 100 {
			return ErrBoardLimit
		}
	case "usecase":
		if actors == len(op.Nodes) {
			return boardInvalid("a use case diagram needs a use case besides its actors")
		}
	}
	for _, edge := range op.Edges {
		if err := boardComposeStyle(edge.Style); err != nil {
			return err
		}
		if op.Type == "sequence" && !boardSequenceStyles[edge.Style] {
			return boardInvalid(
				"message %s→%s: a message's style is sync, async or reply",
				edge.From,
				edge.To,
			)
		}
		if op.Type != "diagram" && (!local[edge.From] || !local[edge.To]) {
			return boardInvalid("edge %s→%s: a %s links its own nodes", edge.From, edge.To, op.Type)
		}
		if edge.From == edge.To && op.Type != "sequence" {
			return boardInvalid(
				"edge %s→%s: only a sequence message returns to its own node",
				edge.From,
				edge.To,
			)
		}
	}
	if len(op.Fragments) > 0 && op.Type != "sequence" {
		return boardInvalid("fragments belong to a sequence")
	}
	for i, f := range op.Fragments {
		if f.First < 1 || f.Last < f.First || f.Last > len(op.Edges) {
			return boardInvalid(
				"fragment %d: first and last number its messages, from 1 to %d",
				i+1,
				len(op.Edges),
			)
		}
		if len(f.Else) > 0 && f.Type != "alt" && f.Type != "par" {
			return boardInvalid("fragment %d: only alt and par take else", i+1)
		}
		// Each operand's guard is one of the fragment shape's sections.
		if len(f.Else)+1 > boardShapeMaxSections {
			return boardInvalid(
				"fragment %d: at most %d else; a fragment draws %d operands",
				i+1,
				boardShapeMaxSections-1,
				boardShapeMaxSections,
			)
		}
		previous := f.First
		for _, branch := range f.Else {
			if branch.At <= previous || branch.At > f.Last {
				return boardInvalid(
					"fragment %d: each else starts at a message after the operand before it, at most last (%d)",
					i+1,
					f.Last,
				)
			}
			previous = branch.At
		}
		for _, other := range op.Fragments[:i] {
			apart := f.Last < other.First || other.Last < f.First
			nested := f.First >= other.First && f.Last <= other.Last ||
				other.First >= f.First && other.Last <= f.Last
			if !apart && !nested {
				return boardInvalid("fragment %d: fragments nest inside each other or stand apart", i+1)
			}
		}
	}
	return nil
}

// Synonyms for each palette's swatches.
var (
	boardInkAliases = map[string]string{
		"red":   "rose",
		"pink":  "rose",
		"black": "graphite",
		"gray":  "graphite",
		"grey":  "graphite",
	}
	boardFillAliases = map[string]string{"red": "pink", "rose": "pink", "transparent": "none"}
)

// boardComposeInk resolves an outline or text color: an ink swatch's name or
// a hex value; "" when it is neither, fallback when it is empty.
func boardComposeInk(value, fallback string) string {
	return boardComposeColor(value, boardInkNames, boardInkAliases, fallback)
}

// boardComposeFill resolves a fill the same way from the fill swatches.
func boardComposeFill(value, fallback string) string {
	return boardComposeColor(value, boardFillNames, boardFillAliases, fallback)
}

func boardComposeColor(value string, names, aliases map[string]string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	if alias, ok := aliases[value]; ok {
		value = alias
	}
	for hex, name := range names {
		if name == value {
			return hex
		}
	}
	if boardColorPattern.MatchString(value) {
		return value
	}
	return ""
}

// boardComposeBlock is content laid out relative to its own origin, placed
// on the Board as one rectangle.
type boardComposeBlock struct {
	width, height float64
	// frame wraps the block's items when the op asked for one.
	frame string
	items []boardComposeItem
}

type boardComposeItem struct {
	draft string
	// parent is a frame earlier in the same block that holds this item, such
	// as a use case diagram's system boundary.
	parent   string
	itemType string
	geometry BoardVisualBounds
	props    BoardProperties
	text     string
	points   []BoardPoint
}

// boardComposePlan is the relative layout of a compose call, computed
// before the Board lock: measuring and layout cost nothing under it.
type boardComposePlan struct {
	blocks   []boardComposeBlock
	edges    []BoardCommand
	arranges []BoardComposeOp
	// autoRoute names connect ops whose route follows where their ends land:
	// flow when the source is left of the target, straight otherwise.
	autoRoute map[string]bool
}

func boardComposeLayout(in BoardComposeInput, focus BoardFocus) (boardComposePlan, error) {
	var plan boardComposePlan
	measure, err := newBoardMetrics()
	if err != nil {
		return plan, err
	}
	// Notes and diagrams wrap to the part of the requester's view the
	// placement prefers, so the work can show whole where they look.
	room := [2]float64{2400, 1600}
	if focus.known() {
		room = [2]float64{focus.View.Width * 0.88, focus.View.Height * 0.88}
	}
	inner := func(frame string) [2]float64 {
		if frame == "" {
			return room
		}
		return [2]float64{
			room[0] - 2*boardComposeFrameSide,
			room[1] - boardComposeFrameTop - boardComposeFrameSide,
		}
	}
	for i, op := range in.Ops {
		draft := func(key string, n int) string {
			if key != "" {
				return key
			}
			return fmt.Sprintf("o%d.%d", i+1, n)
		}
		switch op.Type {
		case "notes":
			sizes := make([][2]float64, len(op.Items))
			for j, item := range op.Items {
				w, h := measure.noteSize(item.Text, 240, 190)
				sizes[j] = [2]float64{w, h}
			}
			cells, w, h := boardGridLayout(sizes, op.Layout, inner(op.Frame)[0])
			block := boardComposeBlock{width: w, height: h, frame: op.Frame}
			for j, item := range op.Items {
				block.items = append(block.items, boardComposeItem{
					draft:    draft(item.Key, j+1),
					itemType: "sticky",
					text:     item.Text,
					geometry: BoardVisualBounds{
						X:      cells[j].x,
						Y:      cells[j].y,
						Width:  cells[j].width,
						Height: cells[j].height,
					},
					props: BoardProperties{
						Color: "#25252d",
						Fill:  boardComposeFill(item.Color, "#fff2a8"),
					},
				})
			}
			plan.blocks = append(plan.blocks, block)
		case "text":
			w, h := measure.noteSize(op.Text, 260, 70)
			// A one-line text is as wide as its words.
			single := measure.width(measure.note, op.Text) + 2*boardNotePadding
			if !strings.Contains(op.Text, "\n") && single < w {
				w = math.Max(120, math.Ceil(single))
			}
			item := boardComposeItem{
				draft: draft(op.Key, 1), itemType: "text", text: op.Text,
				geometry: BoardVisualBounds{Width: w, Height: h},
				props: BoardProperties{
					Color: boardComposeInk(op.Color, "#25252d"),
					Fill:  boardComposeFill(op.Fill, "#00000000"),
				},
			}
			plan.blocks = append(
				plan.blocks,
				boardComposeBlock{width: w, height: h, items: []boardComposeItem{item}},
			)
		case "shape":
			shape := op.Shape
			if shape == "" {
				shape = "rectangle"
			}
			sections := boardComposeSections(
				BoardShapeType(shape),
				boardComposeCompartments(
					BoardShapeType(shape),
					op.Sections,
					op.Attributes,
					op.Methods,
					op.Columns,
				),
			)
			w, h := measure.kindSize(BoardShapeType(shape), op.Text, sections)
			if op.Width > 0 {
				w = op.Width
			}
			if op.Height > 0 {
				h = op.Height
			}
			item := boardComposeItem{
				draft: draft(op.Key, 1), itemType: "shape",
				geometry: BoardVisualBounds{Width: w, Height: h},
				props:    boardComposeShape(shape, op.Text, op.Fill, op.Color),
			}
			item.props.Sections = sections
			plan.blocks = append(
				plan.blocks,
				boardComposeBlock{width: w, height: h, items: []boardComposeItem{item}},
			)
		case "diagram":
			block, edges := boardComposeDiagram(measure, op, inner(op.Frame), i+1)
			plan.blocks, plan.edges = append(plan.blocks, block), append(plan.edges, edges...)
		case "sequence":
			block, edges := boardComposeSequence(measure, op, i+1)
			plan.blocks, plan.edges = append(plan.blocks, block), append(plan.edges, edges...)
		case "usecase":
			block, edges := boardComposeUseCase(measure, op, i+1)
			plan.blocks, plan.edges = append(plan.blocks, block), append(plan.edges, edges...)
		case "sketch":
			w, h := op.Width, op.Height
			if w == 0 {
				w = 400
			}
			if h == 0 {
				h = 300
			}
			strokes, err := boardSketchStrokes(op, w, h)
			if err != nil {
				return plan, boardInvalid(
					"op %d: a sketch takes SVG path data (M, L, H, V, C, S, Q, T, A, Z) and a viewBox of four numbers",
					i+1,
				)
			}
			block := boardComposeBlock{width: w, height: h, frame: op.Frame}
			for j, stroke := range strokes {
				block.items = append(block.items, boardComposeItem{
					draft:    draft("", j+1),
					itemType: "stroke",
					geometry: BoardVisualBounds{
						X:      stroke.x,
						Y:      stroke.y,
						Width:  stroke.width,
						Height: stroke.height,
					},
					props: BoardProperties{
						Color:       stroke.color,
						Fill:        "#00000000",
						StrokeWidth: stroke.strokeWidth,
						Brush:       "pen",
					},
					points: stroke.points,
				})
			}
			plan.blocks = append(plan.blocks, block)
		case "connect":
			c := boardComposeConnector(
				draft(op.Key, 1),
				op.From,
				op.To,
				op.Label,
				op.Style,
				"",
				"",
				"arrow",
				"",
				false,
			)
			plan.edges = append(plan.edges, c)
			if plan.autoRoute == nil {
				plan.autoRoute = map[string]bool{}
			}
			plan.autoRoute[c.DraftID] = true
		case "arrange":
			plan.arranges = append(plan.arranges, op)
		}
	}
	return plan, nil
}

func boardComposeShape(shape, label, fill, color string) BoardProperties {
	return BoardProperties{
		Shape:       BoardShapeType(shape),
		Title:       label,
		Color:       boardComposeInk(color, "#25252d"),
		Fill:        boardComposeFill(fill, "#00000000"),
		StrokeWidth: 2,
	}
}

// boardComposeCompartments turns a class's attributes and methods, or an
// entity's columns, into its compartments, a member per line. A class sent
// as three or four one-line sections is one member per section, as Agents
// often send it, not as many compartments: its members are regrouped into
// attributes and methods (the lines that call, with a parenthesis).
func boardComposeCompartments(kind BoardShapeType, sections, attributes, methods, columns []string) []string {
	switch {
	case kind == BoardShapeTypeClass && len(attributes)+len(methods) > 0:
		return []string{strings.Join(attributes, "\n"), strings.Join(methods, "\n")}
	case kind == BoardShapeTypeEntity && len(columns) > 0:
		return []string{strings.Join(columns, "\n")}
	case kind == BoardShapeTypeClass && len(sections) > 2 &&
		!slices.ContainsFunc(sections, func(section string) bool { return strings.Contains(section, "\n") }):
		var regrouped [2][]string
		for _, member := range sections {
			if strings.Contains(member, "(") {
				regrouped[1] = append(regrouped[1], member)
			} else {
				regrouped[0] = append(regrouped[0], member)
			}
		}
		return []string{strings.Join(regrouped[0], "\n"), strings.Join(regrouped[1], "\n")}
	}
	return sections
}

// boardComposeSections gives a class its two compartments and an entity its
// columns when the request names none.
func boardComposeSections(kind BoardShapeType, sections []string) []string {
	switch {
	case len(sections) > 0:
		return sections
	case kind == BoardShapeTypeClass:
		return []string{"", ""}
	case kind == BoardShapeTypeEntity:
		return []string{""}
	}
	return nil
}

// boardComposeDiagram lays a diagram out as a layered flow, wrapped to the
// room: left to right with flow connectors, or down with elbow connectors,
// where a hierarchy's parents sit above their children. Nodes are shapes of
// any kind with their text as the label, or sticky notes.
func boardComposeDiagram(
	measure *boardMetrics,
	op BoardComposeOp,
	room [2]float64,
	index int,
) (boardComposeBlock, []BoardCommand) {
	down := op.Direction == "down"
	position := map[string]int{}
	sizes := make([][2]float64, len(op.Nodes))
	kinds := make([]BoardShapeType, len(op.Nodes))
	sections := make([][]string, len(op.Nodes))
	for j, node := range op.Nodes {
		position[node.Key] = j
		if node.Shape == "sticky" {
			w, h := measure.noteSize(node.Text, 200, 120)
			sizes[j] = [2]float64{w, h}
			continue
		}
		kinds[j] = BoardShapeType(cmp.Or(node.Shape, string(BoardShapeTypeRectangle)))
		sections[j] = boardComposeSections(
			kinds[j],
			boardComposeCompartments(kinds[j], node.Sections, node.Attributes, node.Methods, node.Columns),
		)
		w, h := measure.kindSize(kinds[j], node.Text, sections[j])
		sizes[j] = [2]float64{w, h}
	}
	var links [][2]int
	var labels []float64
	for _, edge := range op.Edges {
		from, fromOK := position[edge.From]
		to, toOK := position[edge.To]
		if !fromOK || !toOK {
			continue
		}
		if down && (edge.Style == "inherits" || edge.Style == "implements") {
			// A child points to its parent; the parent goes above it.
			from, to = to, from
		}
		links = append(links, [2]int{from, to})
		label := measure.width(measure.label, edge.Label)
		if down {
			label = 0
			if edge.Label != "" {
				label = boardLabelLine
			}
		}
		labels = append(labels, label)
	}
	layoutSizes, layoutRoom := sizes, room
	if down {
		layoutSizes = make([][2]float64, len(sizes))
		for j, size := range sizes {
			layoutSizes[j] = [2]float64{size[1], size[0]}
		}
		layoutRoom = [2]float64{room[1], room[0]}
	}
	nodes, w, h := boardLayeredLayout(layoutSizes, links, labels, layoutRoom)
	if down {
		for j := range nodes {
			nodes[j] = boardLaidOut{
				x:      nodes[j].y,
				y:      nodes[j].x,
				width:  nodes[j].height,
				height: nodes[j].width,
			}
		}
		w, h = h, w
	}
	block := boardComposeBlock{width: w, height: h, frame: op.Frame}
	for j, node := range op.Nodes {
		item := boardComposeItem{
			draft: node.Key,
			geometry: BoardVisualBounds{
				X:      nodes[j].x,
				Y:      nodes[j].y,
				Width:  nodes[j].width,
				Height: nodes[j].height,
			},
		}
		if node.Shape == "sticky" {
			item.itemType, item.text = "sticky", node.Text
			item.props = BoardProperties{
				Color: boardComposeInk(node.Color, "#25252d"),
				Fill:  boardComposeFill(node.Fill, "#fff2a8"),
			}
		} else {
			item.itemType = "shape"
			item.props = boardComposeShape(string(kinds[j]), node.Text, node.Fill, node.Color)
			item.props.Sections = sections[j]
		}
		block.items = append(block.items, item)
	}
	route := BoardRouteTypeFlow
	if down {
		route = BoardRouteTypeElbow
	}
	edges := make([]BoardCommand, 0, len(op.Edges))
	for j, edge := range op.Edges {
		entities := false
		if from, ok := position[edge.From]; ok {
			if to, ok := position[edge.To]; ok {
				entities = kinds[from] == BoardShapeTypeEntity && kinds[to] == BoardShapeTypeEntity
			}
		}
		edges = append(edges, boardComposeConnector(
			fmt.Sprintf(
				"o%d.e%d",
				index,
				j+1,
			),
			edge.From,
			edge.To,
			edge.Label,
			edge.Style,
			edge.FromEnd,
			edge.ToEnd,
			"arrow",
			route,
			entities,
		))
	}
	return block, edges
}

// boardComposeFramePadding is the room a wrapping frame leaves around its
// content, with its title above.
const (
	boardComposeFrameSide = 40.0
	boardComposeFrameTop  = 64.0
)

// boardComposed is a placed plan: the batch to apply and where it lands.
type boardComposed struct {
	batch     BoardBatch
	bounds    *BoardVisualBounds
	placement BoardPlacementType
}

// boardComposeBuild places the plan on the locked scene and writes the
// batch: frames first, then their content, then connectors, then arranged
// objects.
func boardComposeBuild(
	plan boardComposePlan,
	in BoardComposeInput,
	items []BoardItem,
	focus BoardFocus,
	run uuid.UUID,
) (boardComposed, error) {
	composed := boardComposed{batch: BoardBatch{RequestKey: in.RequestKey}}
	batch := &composed.batch
	byID := make(map[uuid.UUID]BoardItem, len(items))
	top := int64(0)
	for _, item := range items {
		if !item.Deleted {
			byID[item.ID] = item
			top = max(top, item.StackOrder)
		}
	}
	var near, frame *BoardVisualBounds
	var into uuid.UUID
	if in.Near != nil {
		item, ok := byID[*in.Near]
		if !ok {
			return composed, ErrBoardItemNotFound
		}
		bounds := boardVisualItemBounds(item)
		near = &bounds
	}
	if in.Into != nil {
		item, ok := byID[*in.Into]
		if !ok {
			return composed, ErrBoardItemNotFound
		}
		if item.Type != "frame" {
			return composed, boardInvalid("into names a %s; it takes a frame", item.Type)
		}
		into = item.ID
		frame = &BoardVisualBounds{X: item.X, Y: item.Y, Width: item.Width, Height: item.Height}
	}
	placement := newBoardPlacement(items, focus, run, into)
	// One request lands as one group: its blocks are packed in rows no wider
	// than the space they go into, and the group is placed once, so the
	// first block never splits the free space the others need.
	rowWidth := 2400.0
	if view, ok := placement.comfort(); ok {
		rowWidth = view.Width
	}
	if frame != nil {
		rowWidth = frame.Width - 48
	}
	type packed struct{ x, y, width, height, offsetX, offsetY float64 }
	boxes := make([]packed, len(plan.blocks))
	const blockGap = 80.0
	x, y, rowHeight, groupWidth := 0.0, 0.0, 0.0, 0.0
	for i, block := range plan.blocks {
		box := packed{width: block.width, height: block.height}
		if block.frame != "" {
			box.width, box.height = box.width+2*boardComposeFrameSide, box.height+boardComposeFrameTop+boardComposeFrameSide
			box.offsetX, box.offsetY = boardComposeFrameSide, boardComposeFrameTop
		}
		if x > 0 && x+box.width > rowWidth {
			x, y, rowHeight = 0, y+rowHeight+blockGap, 0
		}
		box.x, box.y = x, y
		x += box.width + blockGap
		rowHeight = math.Max(rowHeight, box.height)
		groupWidth = math.Max(groupWidth, box.x+box.width)
		boxes[i] = box
	}
	frameFull := false
	placed := map[string]BoardGeometry{}
	if len(plan.blocks) > 0 {
		at, inside := placement.place(groupWidth, y+rowHeight, near, frame)
		frameFull = frame != nil && !inside
		placement.placed(at)
		composed.bounds = &at
		for i, block := range plan.blocks {
			box := boxes[i]
			originX, originY := at.X+box.x, at.Y+box.y
			parentID := (*uuid.UUID)(nil)
			parentRef := ""
			if inside {
				parentID = &into
			}
			if block.frame != "" {
				draft := fmt.Sprintf("b%d.frame", i+1)
				batch.Commands = append(batch.Commands, BoardCommand{
					Type:     "create_item",
					DraftID:  draft,
					ItemType: "frame",
					Geometry: &BoardGeometry{
						X:             originX,
						Y:             originY,
						Width:         box.width,
						Height:        box.height,
						ParentFrameID: parentID,
					},
					Properties: &BoardProperties{
						Title: block.frame,
						Color: "#25252d",
						Fill:  "#00000000",
					},
				})
				parentID, parentRef = nil, draft
			}
			for _, item := range block.items {
				top++
				props := item.props
				geometry := BoardGeometry{
					X:             originX + box.offsetX + item.geometry.X,
					Y:             originY + box.offsetY + item.geometry.Y,
					Width:         item.geometry.Width,
					Height:        item.geometry.Height,
					StackOrder:    top,
					ParentFrameID: parentID,
				}
				itemParent := parentRef
				if item.parent != "" {
					geometry.ParentFrameID, itemParent = nil, item.parent
				}
				placed[item.draft] = geometry
				batch.Commands = append(batch.Commands, BoardCommand{
					Type:       "create_item",
					DraftID:    item.draft,
					ItemType:   item.itemType,
					Text:       item.text,
					Geometry:   &geometry,
					Properties: &props,
					Points:     item.points,
					ParentRef:  itemParent,
				})
			}
		}
	}
	for _, edge := range plan.edges {
		if plan.autoRoute[edge.DraftID] {
			edge.Route = boardComposeAutoRoute(edge, placed, byID)
		}
		batch.Commands = append(batch.Commands, edge)
	}
	revisions := map[uuid.UUID]int64{}
	for _, revision := range in.Revisions {
		revisions[revision.ID] = revision.Revision
	}
	moves, err := boardComposeArrange(plan.arranges, byID, revisions)
	if err != nil {
		return composed, err
	}
	batch.Commands = append(batch.Commands, moves...)
	if b := composed.bounds; b != nil {
		v := focus.View
		switch {
		case frameFull:
			composed.placement = BoardPlacementTypeFrameFull
		case !focus.known():
			composed.placement = BoardPlacementTypeUnwatched
		case b.X >= v.X && b.Y >= v.Y && b.X+b.Width <= v.X+v.Width && b.Y+b.Height <= v.Y+v.Height:
			composed.placement = BoardPlacementTypeView
		case boardVisualIntersects(*b, v):
			composed.placement = BoardPlacementTypePartly
		default:
			composed.placement = BoardPlacementTypeNearby
		}
	}
	return composed, nil
}

// boardComposeAutoRoute routes a connect op by where its ends are: the usual
// flow when the source lies left of the target, straight otherwise, so it
// never doubles back around its ends.
func boardComposeAutoRoute(
	edge BoardCommand,
	placed map[string]BoardGeometry,
	byID map[uuid.UUID]BoardItem,
) BoardRouteType {
	at := func(ref string) (BoardGeometry, bool) {
		if g, ok := placed[ref]; ok {
			return g, true
		}
		if id, err := uuid.Parse(ref); err == nil {
			if item, ok := byID[id]; ok {
				return item.BoardGeometry, true
			}
		}
		return BoardGeometry{}, false
	}
	source, sourceOK := at(edge.SourceRef)
	target, targetOK := at(edge.TargetRef)
	if !sourceOK || !targetOK || source.X+source.Width <= target.X {
		return BoardRouteTypeFlow
	}
	return BoardRouteTypeStraight
}

// boardComposeArrange aligns and spaces existing objects, keeping
// everything but their position. Ops apply in turn, each from where the ones
// before it left the objects, and each object moves once, guarded by the
// revision the Agent read, so a call never conflicts with itself.
func boardComposeArrange(
	ops []BoardComposeOp,
	byID map[uuid.UUID]BoardItem,
	revisions map[uuid.UUID]int64,
) ([]BoardCommand, error) {
	moved := map[uuid.UUID]BoardItem{}
	var order []uuid.UUID
	for _, op := range ops {
		items := make([]BoardItem, 0, len(op.Refs))
		for _, id := range op.Refs {
			if slices.ContainsFunc(items, func(item BoardItem) bool { return item.ID == id }) {
				continue
			}
			item, ok := moved[id]
			if !ok {
				if item, ok = byID[id]; !ok {
					return nil, ErrBoardItemNotFound
				}
				if revisions[id] == 0 {
					return nil, boardInvalid(
						"arrange: read %s before moving it; its revision is missing",
						id,
					)
				}
			}
			items = append(items, item)
		}
		boardComposeArrangeOne(op, items)
		for _, item := range items {
			if _, ok := moved[item.ID]; !ok {
				order = append(order, item.ID)
			}
			moved[item.ID] = item
		}
	}
	moves := make([]BoardCommand, 0, len(order))
	for _, id := range order {
		geometry := moved[id].BoardGeometry
		moves = append(moves, BoardCommand{
			Type: "geometry", ID: &id, ExpectedRevision: revisions[id], Geometry: &geometry,
		})
	}
	return moves, nil
}

// boardComposeArrangeOne aligns and spaces one op's objects in place.
func boardComposeArrangeOne(op BoardComposeOp, items []BoardItem) {
	left, topEdge := math.Inf(1), math.Inf(1)
	right, bottom := math.Inf(-1), math.Inf(-1)
	for _, item := range items {
		left, topEdge = math.Min(left, item.X), math.Min(topEdge, item.Y)
		right, bottom = math.Max(right, item.X+item.Width), math.Max(bottom, item.Y+item.Height)
	}
	gap := op.Gap
	if gap == 0 {
		gap = 40
	}
	switch op.Distribute {
	case "horizontal":
		slices.SortStableFunc(items, func(a, b BoardItem) int { return cmp.Compare(a.X, b.X) })
		x := left
		for i := range items {
			items[i].X, x = x, x+items[i].Width+gap
		}
	case "vertical":
		slices.SortStableFunc(items, func(a, b BoardItem) int { return cmp.Compare(a.Y, b.Y) })
		y := topEdge
		for i := range items {
			items[i].Y, y = y, y+items[i].Height+gap
		}
	}
	for i := range items {
		item := &items[i]
		switch op.Align {
		case "left":
			item.X = left
		case "right":
			item.X = right - item.Width
		case "center":
			item.X = (left+right)/2 - item.Width/2
		case "top":
			item.Y = topEdge
		case "bottom":
			item.Y = bottom - item.Height
		case "middle":
			item.Y = (topEdge+bottom)/2 - item.Height/2
		}
	}
}

func (s *DefaultBoardService) Compose(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	input BoardComposeInput,
	focus BoardFocus,
) (BoardComposeResult, error) {
	var result BoardComposeResult
	if err := normalizeBoardComposeInput(input); err != nil {
		return result, err
	}
	// The intent identifies the request: a retry replays the same objects
	// even after the scene, and so the layout, changed.
	hash, err := boardHash(input)
	if err != nil {
		return result, err
	}
	plan, err := boardComposeLayout(input, focus)
	if err != nil {
		return result, err
	}
	var pending events.Event
	var run *BoardViewer
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		b, viewer, err := boardScopeLock(ctx, q, p, scope, true)
		if err != nil {
			return err
		}
		replay, found, err := boardReceiptReplay(ctx, q, p, b, input.RequestKey, hash, viewer)
		if err != nil {
			return err
		}
		if found {
			result.Mutation = replay
			result.Bounds, err = boardComposeReplayBounds(ctx, q, p, b, replay)
		} else {
			var snapshot BoardSnapshot
			if snapshot, err = boardSnapshotInTx(ctx, q, p, b, 0); err != nil {
				return err
			}
			var composed boardComposed
			composed, err = boardComposeBuild(plan, input, snapshot.Items, focus, scope.RunID)
			if err != nil {
				return err
			}
			result.Bounds, result.Placement = composed.bounds, composed.placement
			if err = normalizeBoardBatch(composed.batch); err != nil {
				return err
			}
			result.Mutation, err = boardApplyCommands(ctx, q, p, b, composed.batch, hash, viewer)
		}
		if err != nil {
			return err
		}
		pending = boardChangeEvent(p, scope.BoardID, result.Mutation.Sequence)
		pending.Actor = boardEventActor(p, viewer)
		if err = stampBoardAudience(ctx, q, &pending); err != nil {
			return err
		}
		run, err = runViewer(ctx, q, p, scope, viewer, boardAudienceOf(pending))
		return err
	})
	if err == nil {
		s.keepRunViewer(p, scope, run)
	}
	// A replay changed nothing, so nobody needs to refetch.
	if err == nil && !result.Mutation.Replayed {
		s.publishEvent(ctx, pending)
	}
	return result, boardError(err)
}

// boardComposeReplayBounds recomputes where a replayed compose's objects are
// now, since a receipt keeps their IDs and not their layout.
func boardComposeReplayBounds(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	b boardRecord,
	replay BoardMutation,
) (*BoardVisualBounds, error) {
	if len(replay.IDs) == 0 {
		return nil, nil
	}
	snapshot, err := boardSnapshotInTx(ctx, q, p, b, 0)
	if err != nil {
		return nil, err
	}
	created := map[uuid.UUID]bool{}
	for _, id := range replay.IDs {
		created[id] = true
	}
	var bounds *BoardVisualBounds
	for _, item := range snapshot.Items {
		if created[item.ID] && !item.Deleted {
			bounds = boardUnionPtr(bounds, boardVisualItemBounds(item))
		}
	}
	return bounds, nil
}
