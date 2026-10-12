package canvas

import (
	"cmp"
	"context"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// BoardFocus is where the person a Run works for is looking, read from their
// presence by the HTTP layer. It only steers what an Agent reads and where it
// places work; it grants nothing.
type BoardFocus struct {
	// Present is a live browser session on the Board; Recent is the view they
	// left within the last few minutes.
	Present      bool
	Recent       bool
	Name         string
	View         BoardVisualBounds
	SelectionIDs []uuid.UUID
}

func (f BoardFocus) known() bool { return f.Present || f.Recent }

// BoardAgentViewInput asks for the compact Agent reading of a Board: the area
// the requester looks at (the default when known), the whole Board, or
// selected objects by their short refs.
type BoardAgentViewInput struct {
	Area     string   `json:"area"     binding:"omitempty,oneof=view all"`
	ItemRefs []string `json:"itemRefs" binding:"max=100,dive,min=4,max=36"`
	Image    bool     `json:"image"`
}

// BoardAgentView is that reading: Text is what the model reads, one line per
// object with a short ref; Refs carries each listed object's ID and revisions
// for boardly-mcp, which fills edit revisions so the model never copies them.
type BoardAgentView struct {
	Sequence int64           `json:"sequence"`
	Text     string          `json:"text"`
	Refs     []BoardAgentRef `json:"refs"`
	// Area is the Board rectangle the reading covers, for an image of it.
	Area  *BoardVisualBounds `json:"area"`
	Image *BoardVisualRegion `json:"image,omitempty"`
	// ImageUnavailable explains a requested image that could not be rendered.
	ImageUnavailable string `json:"imageUnavailable,omitempty"`
}

type BoardAgentRef struct {
	Ref               string    `json:"ref"`
	ID                uuid.UUID `json:"id"`
	Type              string    `json:"type"`
	GeometryRevision  int64     `json:"geometryRevision,omitempty"`
	ContentRevision   int64     `json:"contentRevision,omitempty"`
	TextRevision      int64     `json:"textRevision,omitempty"`
	LifecycleRevision int64     `json:"lifecycleRevision,omitempty"`
	Revision          int64     `json:"revision,omitempty"`
	// An item's geometry and properties as read: the operations replace
	// both whole, so a client changing a few fields keeps the rest from here.
	Geometry   *BoardGeometry   `json:"geometry,omitempty"`
	Properties *BoardProperties `json:"properties,omitempty"`
}

// Agent view bounds: the reading stays near 4k tokens however large the
// Board.
const (
	boardAgentViewBudget    = 16 << 10
	boardAgentViewTextRunes = 160
)

// The editor's swatches, so the reading names colors instead of hex codes.
var (
	boardInkNames = map[string]string{
		"#25252d": "graphite", "#4c6fff": "blue", "#2d9470": "green", "#facc15": "yellow", "#e85d75": "rose",
	}
	boardFillNames = map[string]string{
		"#00000000": "none", "#fff2a8": "yellow", "#dff0fc": "blue", "#fce1eb": "pink", "#dff3e3": "green",
	}
)

func (s *DefaultBoardService) AgentView(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	input BoardAgentViewInput,
	focus BoardFocus,
) (BoardAgentView, error) {
	var result BoardAgentView
	if len(input.ItemRefs) > boardMaxSelection {
		return result, ErrBoardInvalid
	}
	var run *BoardViewer
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		board, viewer, err := boardScopeLock(ctx, q, p, scope, false)
		if err != nil {
			return err
		}
		if run, err = runViewer(ctx, q, p, scope, viewer, nil); err != nil {
			return err
		}
		snapshot, err := boardSnapshotInTx(ctx, q, p, board, 0)
		if err != nil {
			return err
		}
		in := boardAgentProjectionInput{
			board:   snapshot.Board,
			canEdit: viewer.CanEdit,
			agent:   viewer.Actor.ID,
			items:   snapshot.Items,
			edges:   snapshot.Edges,
			focus:   focus,
			area:    input.Area,
			refs:    input.ItemRefs,
		}
		if len(input.ItemRefs) > 0 {
			// Selected notes are read in full; the snapshot keeps a preview.
			var notes []uuid.UUID
			resolved := boardResolveRefs(
				boardShortRefs(boardAgentIDs(snapshot.Items, snapshot.Edges)),
				input.ItemRefs,
			)
			for _, item := range snapshot.Items {
				if slices.Contains(resolved.ids, item.ID) &&
					(item.Type == "sticky" || item.Type == "text") {
					notes = append(notes, item.ID)
				}
			}
			if len(notes) > 0 {
				texts, err := q.ListBoardNoteTexts(
					ctx,
					generated.ListBoardNoteTextsParams{
						CompanyID: p.CompanyID,
						BoardID:   board.ID,
						ItemIds:   notes,
					},
				)
				if err != nil {
					return err
				}
				in.texts = make(map[uuid.UUID]string, len(texts))
				for _, text := range texts {
					in.texts[text.ItemID] = text.PlainText
				}
			}
		}
		result = boardAgentProjection(in)
		return nil
	})
	if err == nil {
		s.keepRunViewer(p, scope, run)
	}
	return result, boardError(err)
}

type boardAgentProjectionInput struct {
	board   Board
	canEdit bool
	// agent is the reading Agent, so its own earlier work reads "by you".
	agent uuid.UUID
	items []BoardItem
	edges []BoardEdge
	focus BoardFocus
	area  string
	// refs are requested objects, read in full with texts.
	refs  []string
	texts map[uuid.UUID]string
}

// boardAgentProjection renders the compact reading. It is pure, so the
// format and its budget are pinned without a database.
func boardAgentProjection(in boardAgentProjectionInput) BoardAgentView {
	result := BoardAgentView{Sequence: in.board.Sequence, Refs: []BoardAgentRef{}}
	live := slices.DeleteFunc(slices.Clone(in.items), func(item BoardItem) bool { return item.Deleted })
	refs := boardShortRefs(boardAgentIDs(live, in.edges))
	var text strings.Builder
	access := "read only"
	if in.canEdit {
		access = "you can edit"
	}
	fmt.Fprintf(
		&text,
		"Board %s, %s, sequence %d.\n",
		boardAgentQuote(in.board.Title, 200),
		access,
		in.board.Sequence,
	)
	if purpose := strings.TrimSpace(in.board.Purpose); purpose != "" {
		fmt.Fprintf(&text, "Purpose: %s\n", boardAgentClip(purpose, 300))
	}
	person := in.focus.Name
	if person == "" {
		person = "The person you work for"
	}
	byID := make(map[uuid.UUID]BoardItem, len(live))
	for _, item := range live {
		byID[item.ID] = item
	}
	listed := map[uuid.UUID]bool{}
	note := func(id uuid.UUID, kind string) {
		if listed[id] {
			return
		}
		listed[id] = true
		ref := BoardAgentRef{Ref: refs[id], ID: id, Type: kind}
		if item, ok := byID[id]; ok {
			ref.GeometryRevision, ref.ContentRevision = item.GeometryRevision, item.ContentRevision
			ref.TextRevision, ref.LifecycleRevision = item.TextRevision, item.LifecycleRevision
			ref.Geometry, ref.Properties = &item.BoardGeometry, &item.Properties
		}
		result.Refs = append(result.Refs, ref)
	}
	if len(in.refs) > 0 {
		resolved := boardResolveRefs(refs, in.refs)
		for _, id := range resolved.ids {
			if item, ok := byID[id]; ok {
				if full, ok := in.texts[id]; ok {
					item.Text, item.TextTruncated = full, false
				}
				text.WriteString(boardAgentLine(item, refs, in.agent, -1))
				fmt.Fprintf(
					&text,
					"  created by %s, updated by %s\n",
					boardAgentActor(item.CreatedBy.DisplayName),
					boardAgentActor(item.UpdatedBy.DisplayName),
				)
				note(id, item.Type)
				continue
			}
			for _, edge := range in.edges {
				if edge.ID == id && !edge.Deleted {
					text.WriteString(boardAgentEdgeLine(edge, refs))
					note(id, "edge")
					edgeRef := &result.Refs[len(result.Refs)-1]
					edgeRef.Revision = edge.Revision
				}
			}
		}
		if len(resolved.ambiguous) > 0 {
			fmt.Fprintf(
				&text,
				"Ambiguous refs, give more digits: %s\n",
				strings.Join(resolved.ambiguous, ", "),
			)
		}
		if len(resolved.missing) > 0 {
			fmt.Fprintf(&text, "Not on this Board: %s\n", strings.Join(resolved.missing, ", "))
		}
		result.Text = text.String()
		return result
	}
	var area BoardVisualBounds
	switch {
	case in.focus.known() && in.area != "all":
		view := in.focus.View
		area = BoardVisualBounds{
			X:      view.X - view.Width/8,
			Y:      view.Y - view.Height/8,
			Width:  view.Width * 1.25,
			Height: view.Height * 1.25,
		}
		verb := "is looking at"
		if !in.focus.Present {
			verb = "left the Board a moment ago; their last view was"
		}
		fmt.Fprintf(&text, "%s %s %s", person, verb, boardAgentRect(view))
	default:
		if len(live) == 0 {
			if in.area != "all" && !in.focus.known() {
				fmt.Fprintf(&text, "%s is not viewing this Board. ", person)
			}
			text.WriteString("The Board is empty.\n")
			result.Text = text.String()
			return result
		}
		area = boardVisualItemBounds(live[0])
		for _, item := range live[1:] {
			area = boardVisualUnion(area, boardVisualItemBounds(item))
		}
		if in.area != "all" {
			fmt.Fprintf(&text, "%s is not viewing this Board; showing the whole Board", person)
		} else {
			text.WriteString("Whole Board")
		}
		fmt.Fprintf(&text, " %s", boardAgentRect(area))
	}
	result.Area = &area
	var shown []BoardItem
	for _, item := range live {
		if boardVisualIntersects(boardVisualItemBounds(item), area) {
			shown = append(shown, item)
		}
	}
	fmt.Fprintf(&text, "; %d objects there.\n", len(shown))
	if len(in.focus.SelectionIDs) > 0 && in.focus.Present {
		var selected []string
		for _, id := range in.focus.SelectionIDs {
			if item, ok := byID[id]; ok {
				selected = append(selected, refs[id])
				if !boardVisualIntersects(boardVisualItemBounds(item), area) {
					shown = append(shown, item)
				}
			}
		}
		if len(selected) > 0 {
			fmt.Fprintf(&text, "%s selected: %s\n", person, strings.Join(selected, ", "))
		}
	}
	// Nearby strokes are one drawing: a line per drawing keeps a sketch of
	// many strokes short while still naming every stroke.
	entries := boardAgentEntries(shown)
	// Frames first, then content in reading order: rows top to bottom.
	slices.SortStableFunc(entries, func(a, b boardAgentEntry) int {
		if a.frame != b.frame {
			if a.frame {
				return -1
			}
			return 1
		}
		return cmp.Or(
			cmp.Compare(math.Floor(a.bounds.Y/40), math.Floor(b.bounds.Y/40)),
			cmp.Compare(a.bounds.X, b.bounds.X),
		)
	})
	visual, omitted := 0, 0
	for _, entry := range entries {
		var line string
		if len(entry.ink) > 1 {
			line = boardAgentInkLine(entry, refs)
		} else {
			line = boardAgentLine(entry.item, refs, in.agent, boardAgentViewTextRunes)
		}
		if entry.item.Type == "stroke" || entry.item.Type == "image" {
			visual++
		}
		if text.Len()+len(line) > boardAgentViewBudget {
			omitted += max(1, len(entry.ink))
			continue
		}
		text.WriteString(line)
		if len(entry.ink) > 1 {
			for _, stroke := range entry.ink {
				note(stroke.ID, stroke.Type)
			}
		} else {
			note(entry.item.ID, entry.item.Type)
		}
	}
	for _, edge := range boardAgentEdgeOrder(in.edges, in.items) {
		if edge.Deleted || !listed[edge.SourceID] && !listed[edge.TargetID] {
			continue
		}
		line := boardAgentEdgeLine(edge, refs)
		if text.Len()+len(line) > boardAgentViewBudget {
			omitted++
			continue
		}
		text.WriteString(line)
		note(edge.ID, "edge")
		result.Refs[len(result.Refs)-1].Revision = edge.Revision
	}
	if omitted > 0 {
		fmt.Fprintf(&text, "… %d more objects not listed; read a part with itemRefs.\n", omitted)
	}
	if visual > 0 {
		noun, pronoun := "drawings or images", "them"
		if visual == 1 {
			noun, pronoun = "drawing or image", "it"
		}
		fmt.Fprintf(&text, "%d %s here; set image=true to see %s.\n", visual, noun, pronoun)
	}
	result.Text = text.String()
	return result
}

type boardAgentEntry struct {
	item   BoardItem
	ink    []BoardItem
	bounds BoardVisualBounds
	frame  bool
}

// boardAgentEntries groups strokes within a short gap of each other into one
// drawing; every other object is its own entry.
func boardAgentEntries(items []BoardItem) []boardAgentEntry {
	var entries []boardAgentEntry
	var strokes []BoardItem
	for _, item := range items {
		if item.Type == "stroke" {
			strokes = append(strokes, item)
			continue
		}
		entries = append(entries, boardAgentEntry{
			item: item, bounds: boardVisualItemBounds(item), frame: item.Type == "frame",
		})
	}
	parent := make([]int, len(strokes))
	for i := range parent {
		parent[i] = i
	}
	find := func(i int) int {
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	bounds := make([]BoardVisualBounds, len(strokes))
	for i, stroke := range strokes {
		b := boardVisualItemBounds(stroke)
		bounds[i] = BoardVisualBounds{X: b.X - 30, Y: b.Y - 30, Width: b.Width + 60, Height: b.Height + 60}
	}
	// Sweep left to right: only strokes whose extents overlap on x can touch.
	byX := make([]int, len(strokes))
	for i := range byX {
		byX[i] = i
	}
	slices.SortFunc(byX, func(a, b int) int { return cmp.Compare(bounds[a].X, bounds[b].X) })
	for k, i := range byX {
		for _, j := range byX[k+1:] {
			if bounds[j].X > bounds[i].X+bounds[i].Width {
				break
			}
			if boardVisualIntersects(bounds[i], bounds[j]) {
				parent[find(i)] = find(j)
			}
		}
	}
	groups := map[int]*boardAgentEntry{}
	var order []int
	for i, stroke := range strokes {
		root := find(i)
		group, ok := groups[root]
		if !ok {
			group = &boardAgentEntry{item: stroke, bounds: boardVisualItemBounds(stroke)}
			groups[root] = group
			order = append(order, root)
		} else {
			group.bounds = boardVisualUnion(group.bounds, boardVisualItemBounds(stroke))
		}
		group.ink = append(group.ink, stroke)
	}
	for _, root := range order {
		entries = append(entries, *groups[root])
	}
	return entries
}

// boardAgentInkLine is one drawing of several strokes: its extent, its color
// when they share one, and every stroke's ref, so it can be moved or erased.
func boardAgentInkLine(entry boardAgentEntry, refs map[uuid.UUID]string) string {
	var line strings.Builder
	fmt.Fprintf(&line, "drawing %s, %d strokes", boardAgentRect(entry.bounds), len(entry.ink))
	color := strings.ToLower(entry.ink[0].Properties.Color)
	for _, stroke := range entry.ink[1:] {
		if strings.ToLower(stroke.Properties.Color) != color {
			color = ""
			break
		}
	}
	if color != "" {
		line.WriteString(" color=" + boardAgentColor(color, boardInkNames))
	}
	line.WriteString(":")
	for _, stroke := range entry.ink {
		line.WriteString(" " + refs[stroke.ID])
	}
	line.WriteByte('\n')
	return line.String()
}

// boardAgentLine is one object: ref, type, position and size, what sets it
// apart, then its text. maxRunes < 0 keeps the whole text.
func boardAgentLine(item BoardItem, refs map[uuid.UUID]string, agent uuid.UUID, maxRunes int) string {
	var line strings.Builder
	line.WriteString(refs[item.ID])
	line.WriteByte(' ')
	line.WriteString(item.Type)
	if item.Type == "shape" && item.Properties.Shape != "" {
		line.WriteString(":" + string(item.Properties.Shape))
	}
	fmt.Fprintf(&line, " %s", boardAgentRect(BoardVisualBounds{
		X: item.X, Y: item.Y, Width: item.Width, Height: item.Height,
	}))
	if item.Properties.Head != "" {
		line.WriteString(" head=" + string(item.Properties.Head))
	}
	if item.Rotation != 0 {
		fmt.Fprintf(&line, " rot=%d", int(math.Round(item.Rotation)))
	}
	// A sticky is yellow and anything else unfilled unless it says otherwise.
	fill, plain := strings.ToLower(item.Properties.Fill), "#00000000"
	if item.Type == "sticky" {
		plain = "#fff2a8"
	}
	if fill != "" && fill != plain {
		line.WriteString(" fill=" + boardAgentColor(fill, boardFillNames))
	}
	if color := strings.ToLower(item.Properties.Color); color != "" &&
		(color != "#25252d" || item.Type == "stroke") {
		line.WriteString(" color=" + boardAgentColor(color, boardInkNames))
	}
	if item.Properties.Brush == "highlighter" {
		line.WriteString(" highlighter")
	}
	if item.ParentFrameID != nil {
		if parent, ok := refs[*item.ParentFrameID]; ok {
			line.WriteString(" in " + parent)
		}
	}
	if item.Properties.Locked {
		line.WriteString(" locked")
	}
	if agent != uuid.Nil && item.CreatedBy.ID == agent {
		line.WriteString(" by you")
	}
	label := item.Text
	switch item.Type {
	case "frame", "shape":
		label = item.Properties.Title
	case "image", "file":
		if item.File != nil {
			label = item.File.Filename
		}
	}
	if label != "" {
		if maxRunes < 0 {
			line.WriteString(" " + boardAgentQuote(label, -1))
		} else {
			line.WriteString(" " + boardAgentQuote(label, maxRunes))
		}
	} else if item.TextTruncated {
		line.WriteString(" …")
	}
	// A class's, entity's or fragment's compartments follow its name.
	for i, section := range item.Properties.Sections {
		if i == 0 {
			line.WriteString(" sections: ")
		} else {
			line.WriteString(" | ")
		}
		line.WriteString(boardAgentQuote(section, maxRunes))
	}
	line.WriteByte('\n')
	return line.String()
}

// boardAgentEdgeLine reads a connector, with its notation where it differs
// from the Board's usual arrow: its route (a message with its row), a dashed
// line, its markers, its end labels and its relation.
func boardAgentEdgeLine(edge BoardEdge, refs map[uuid.UUID]string) string {
	var line strings.Builder
	fmt.Fprintf(
		&line,
		"%s edge %s->%s",
		refs[edge.ID],
		boardAgentRef(refs, edge.SourceID),
		boardAgentRef(refs, edge.TargetID),
	)
	style := edge.notation()
	if style.Route != BoardRouteTypeFlow {
		line.WriteString(" " + string(style.Route))
		if style.Route == BoardRouteTypeMessage && style.AnchorY != nil {
			fmt.Fprintf(&line, " at=%d", int(math.Round(*style.AnchorY)))
		}
	}
	if style.Line == BoardLineTypeDashed {
		line.WriteString(" dashed")
	}
	if style.Start != BoardMarkerTypeNone {
		line.WriteString(" start=" + string(style.Start))
	}
	if style.End != BoardMarkerTypeArrow {
		line.WriteString(" end=" + string(style.End))
	}
	if style.SourceLabel != "" || style.TargetLabel != "" {
		fmt.Fprintf(&line, " ends=%q,%q", style.SourceLabel, style.TargetLabel)
	}
	if edge.Relation != "" && edge.Relation != "related" {
		line.WriteString(" rel=" + edge.Relation)
	}
	if edge.Label != "" {
		line.WriteString(" " + boardAgentQuote(edge.Label, boardAgentViewTextRunes))
	}
	line.WriteByte('\n')
	return line.String()
}

// boardAgentEdgeOrder lists connectors as they are, except that messages
// follow, in their order down the lifelines.
func boardAgentEdgeOrder(edges []BoardEdge, items []BoardItem) []BoardEdge {
	top := make(map[uuid.UUID]float64, len(items))
	for _, item := range items {
		top[item.ID] = item.Y
	}
	row := func(edge BoardEdge) float64 { return top[edge.SourceID] + *edge.AnchorY }
	out := make([]BoardEdge, 0, len(edges))
	var messages []BoardEdge
	for _, edge := range edges {
		if edge.Route == BoardRouteTypeMessage && edge.AnchorY != nil {
			messages = append(messages, edge)
		} else {
			out = append(out, edge)
		}
	}
	slices.SortStableFunc(messages, func(a, b BoardEdge) int { return cmp.Compare(row(a), row(b)) })
	return append(out, messages...)
}

func boardAgentRef(refs map[uuid.UUID]string, id uuid.UUID) string {
	if ref, ok := refs[id]; ok {
		return ref
	}
	return strings.ReplaceAll(id.String(), "-", "")[:8]
}

func boardAgentRect(r BoardVisualBounds) string {
	return fmt.Sprintf("%d,%d %dx%d",
		int(math.Round(r.X)), int(math.Round(r.Y)), int(math.Round(r.Width)), int(math.Round(r.Height)))
}

func boardAgentColor(value string, names map[string]string) string {
	if name, ok := names[value]; ok {
		return name
	}
	return value
}

func boardAgentActor(name string) string {
	if name == "" {
		return "someone"
	}
	return name
}

// boardAgentQuote quotes text on one line, keeping Unicode readable and
// newlines visible; maxRunes < 0 keeps it whole.
func boardAgentQuote(value string, maxRunes int) string {
	clipped := value
	if maxRunes >= 0 {
		clipped = boardAgentClip(value, maxRunes)
	}
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", "", "\t", " ")
	return `"` + replacer.Replace(clipped) + `"`
}

func boardAgentClip(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes]) + "…"
}

func boardAgentIDs(items []BoardItem, edges []BoardEdge) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(items)+len(edges))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	for _, edge := range edges {
		if !edge.Deleted {
			ids = append(ids, edge.ID)
		}
	}
	return ids
}

// boardShortRefs names each object by the shortest prefix of its hex ID, at
// least eight digits and growing by four, that no other object on the Board
// shares. Sorted, an ID can only share a prefix with its neighbors.
func boardShortRefs(ids []uuid.UUID) map[uuid.UUID]string {
	hexes := make([]string, len(ids))
	for i, id := range ids {
		hexes[i] = strings.ReplaceAll(id.String(), "-", "")
	}
	order := make([]int, len(ids))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return strings.Compare(hexes[a], hexes[b]) })
	common := func(a, b string) int {
		n := 0
		for n < len(a) && n < len(b) && a[n] == b[n] {
			n++
		}
		return n
	}
	refs := make(map[uuid.UUID]string, len(ids))
	for k, i := range order {
		shared := 0
		if k > 0 {
			shared = common(hexes[i], hexes[order[k-1]])
		}
		if k+1 < len(order) {
			shared = max(shared, common(hexes[i], hexes[order[k+1]]))
		}
		length := 8
		for length <= shared && length < 32 {
			length += 4
		}
		refs[ids[i]] = hexes[i][:length]
	}
	return refs
}

type boardResolvedRefs struct {
	ids       []uuid.UUID
	ambiguous []string
	missing   []string
}

// boardResolveRefs finds the objects short refs name: a ref matches every ID
// it prefixes, so a shorter ref than the reading gave may still be unique.
// The IDs are sorted once, so each ref is a search, not a pass.
func boardResolveRefs(refs map[uuid.UUID]string, wanted []string) boardResolvedRefs {
	type object struct {
		hex string
		id  uuid.UUID
	}
	sorted := make([]object, 0, len(refs))
	for id := range refs {
		sorted = append(sorted, object{hex.EncodeToString(id[:]), id})
	}
	slices.SortFunc(sorted, func(a, b object) int { return strings.Compare(a.hex, b.hex) })
	var result boardResolvedRefs
	for _, raw := range wanted {
		ref := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), "-", ""))
		first, _ := slices.BinarySearchFunc(sorted, ref, func(o object, ref string) int {
			return strings.Compare(o.hex, ref)
		})
		matches := 0
		for _, o := range sorted[first:] {
			if !strings.HasPrefix(o.hex, ref) || matches > 1 {
				break
			}
			matches++
		}
		switch {
		case matches == 1 && !slices.Contains(result.ids, sorted[first].id):
			result.ids = append(result.ids, sorted[first].id)
		case matches > 1:
			result.ambiguous = append(result.ambiguous, raw)
		case matches == 0:
			result.missing = append(result.missing, raw)
		}
	}
	return result
}
