package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Intelligent-Internet/zenith-team/server/internal/mcp/apiclient"
)

// ArtifactEditInput changes a Whiteboard Artifact: ops add content by intent
// and the server lays it out and places it where the person the Run works
// for is looking; commands change objects the Run read, by ref.
type ArtifactEditInput struct {
	ArtifactID string             `json:"artifactId"           jsonschema:"The Whiteboard Artifact ID."`
	Ops        []WhiteboardDrawOp `json:"ops,omitempty"        jsonschema:"What to add, in order: 1 to 20 ops, placed together as one group."`
	Near       string             `json:"near,omitempty"       jsonschema:"With ops: ref of an object to put the new content beside."`
	Into       string             `json:"into,omitempty"       jsonschema:"With ops: ref of a frame to fill; content goes beside it when the frame is full."`
	Preview    bool               `json:"preview,omitempty"    jsonschema:"With ops: also return an image of the result, to check a sketch or a layout once."`
	Commands   []map[string]any   `json:"commands,omitempty"   jsonschema:"Or change objects you read, as one atomic batch of at most 100 native commands: create_item, geometry, content, text, delete_item, restore_item, create_edge, update_edge, delete_edge, restore_edge. Name objects by their refs; revisions are filled from your reads. New objects use draftId, itemType (sticky,text,shape,stroke,frame) and geometry {x,y,width,height}. Connectors use sourceRef/targetRef (refs or draftIds), label, relation, line (solid|dashed), startMarker/endMarker (none,arrow,open,triangle,diamond,diamond_filled,one,zero_one,many,zero_many), route (flow|straight|elbow|message with anchorY), sourceLabel/targetLabel; update_edge keeps the notation it does not name. geometry moves or resizes (id, geometry naming only what changes, e.g. {x:300}); content restyles (id, properties naming only what changes, e.g. {title:\"Orders\"}; null clears one): color and fill (a swatch name as read, e.g. pink, or #rrggbb), shape, strokeWidth, brush, title, sections, head, locked; shapes and frames show title; class/entity sections are compartments; a lifeline's head is a box or actor. text replaces a note's text (id, text); delete_item removes. Prefer ops to add new content."`
	RequestKey string             `json:"requestKey,omitempty" jsonschema:"Only to finish a call whose outcome was unknown: the key that error named. Its ops or commands may then be left out."`
}

type WhiteboardDrawOp struct {
	Type       string                   `json:"type"                 jsonschema:"notes, text, shape, diagram, sequence, usecase, sketch, connect or arrange"`
	Key        string                   `json:"key,omitempty"        jsonschema:"text or shape: a name (letters, digits, - and _) a connect op in this call can use."`
	Items      []WhiteboardDrawNote     `json:"items,omitempty"      jsonschema:"notes: sticky notes in reading order."`
	Layout     string                   `json:"layout,omitempty"     jsonschema:"notes: grid (default), row or column."`
	Text       string                   `json:"text,omitempty"       jsonschema:"text: the text. shape: its label."`
	Shape      string                   `json:"shape,omitempty"      jsonschema:"shape: rectangle (default), rounded, ellipse, diamond, triangle, terminal, actor, database, queue, cloud, component, document, note, class, entity or fragment."`
	Sections   []string                 `json:"sections,omitempty"   jsonschema:"shape class or entity: its compartments, each ONE string with a member per line. Prefer attributes and methods (or columns), one member per entry."`
	Attributes []string                 `json:"attributes,omitempty" jsonschema:"shape class: its attributes, one per entry, e.g. [\"- id: UUID\", \"- total: Money\"]."`
	Methods    []string                 `json:"methods,omitempty"    jsonschema:"shape class: its methods, one per entry, e.g. [\"+ place(): void\"]."`
	Columns    []string                 `json:"columns,omitempty"    jsonschema:"shape entity: its columns, one per entry, e.g. [\"PK id uuid\", \"email text\"]."`
	Fill       string                   `json:"fill,omitempty"       jsonschema:"Background: yellow, blue, pink, green or none."`
	Color      string                   `json:"color,omitempty"      jsonschema:"Ink or outline: graphite, blue, green, yellow or rose."`
	Width      float64                  `json:"width,omitempty"      jsonschema:"shape or sketch size in Board units; a sketch defaults to 400x300. Sizes otherwise fit the text."`
	Height     float64                  `json:"height,omitempty"`
	Nodes      []WhiteboardDrawNode     `json:"nodes,omitempty"      jsonschema:"diagram: boxes laid out along the edges. sequence: participants, left to right. usecase: actors (shape actor) and use cases."`
	Edges      []WhiteboardDrawEdge     `json:"edges,omitempty"      jsonschema:"diagram: connectors from node key to node key (or an object's ref). sequence: messages in order; from = to is a self message. usecase: links."`
	Direction  string                   `json:"direction,omitempty"  jsonschema:"diagram: right (default) or down, for hierarchies: parents above children."`
	Fragments  []WhiteboardDrawFragment `json:"fragments,omitempty"  jsonschema:"sequence: boxes around messages first..last (1-based)."`
	ViewBox    string                   `json:"viewBox,omitempty"    jsonschema:"sketch: the paths' coordinate box, minX minY width height; scaled to fit width x height."`
	Paths      []WhiteboardDrawPath     `json:"paths,omitempty"      jsonschema:"sketch: SVG path data; every subpath becomes a pen stroke people can edit."`
	From       string                   `json:"from,omitempty"       jsonschema:"connect: a key from this call or an object's ref."`
	To         string                   `json:"to,omitempty"`
	Label      string                   `json:"label,omitempty"      jsonschema:"connect: the arrow's label."`
	Style      string                   `json:"style,omitempty"      jsonschema:"connect: a connector style, as for edges."`
	Refs       []string                 `json:"refs,omitempty"       jsonschema:"arrange: refs of objects you read this turn."`
	Align      string                   `json:"align,omitempty"      jsonschema:"arrange: left, center, right, top, middle or bottom."`
	Distribute string                   `json:"distribute,omitempty" jsonschema:"arrange: horizontal or vertical, gap apart (default 40)."`
	Gap        float64                  `json:"gap,omitempty"`
	Frame      string                   `json:"frame,omitempty"      jsonschema:"notes, diagram or sketch: wrap it in a frame with this title. usecase: the system's name."`
}

type WhiteboardDrawNote struct {
	Key   string `json:"key,omitempty"`
	Text  string `json:"text"`
	Color string `json:"color,omitempty" jsonschema:"yellow (default), blue, pink or green."`
}

type WhiteboardDrawNode struct {
	Key        string   `json:"key"`
	Text       string   `json:"text"`
	Shape      string   `json:"shape,omitempty"      jsonschema:"as for a shape op, or sticky. sequence: lifeline (default) or actor, a person or outside system. usecase: actor or ellipse (default)."`
	Sections   []string `json:"sections,omitempty"   jsonschema:"as for a shape op: each compartment one string, a member per line; prefer attributes, methods or columns."`
	Attributes []string `json:"attributes,omitempty" jsonschema:"class: its attributes, one per entry."`
	Methods    []string `json:"methods,omitempty"    jsonschema:"class: its methods, one per entry."`
	Columns    []string `json:"columns,omitempty"    jsonschema:"entity: its columns, one per entry."`
	Fill       string   `json:"fill,omitempty"       jsonschema:"none, yellow, blue, pink or green."`
	Color      string   `json:"color,omitempty"      jsonschema:"outline and text: graphite, blue, green, yellow or rose."`
}

type WhiteboardDrawEdge struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Label   string `json:"label,omitempty"`
	Style   string `json:"style,omitempty"   jsonschema:"arrow (default), line, both, dashed; class: inherits (child to parent, hollow triangle), implements (dashed triangle), composes (diamond at from), aggregates, depends, associates; usecase: includes, extends; sequence: sync, async, reply."`
	FromEnd string `json:"fromEnd,omitempty" jsonschema:"multiplicity at from (1, 0..*; never in label); between entities 1, 0..1, 1..* or 0..* draw a crow's foot. A marker (triangle, diamond, diamond_filled, open) instead replaces the style's there."`
	ToEnd   string `json:"toEnd,omitempty"   jsonschema:"as fromEnd, at to."`
}

type WhiteboardDrawFragment struct {
	Type  string                 `json:"type"            jsonschema:"alt, opt, loop, par, break or critical."`
	Label string                 `json:"label,omitempty" jsonschema:"the first operand's guard, e.g. valid; type is the operator."`
	First int                    `json:"first"           jsonschema:"first message inside, numbered from 1 in edges order."`
	Last  int                    `json:"last"            jsonschema:"last message inside."`
	Else  []WhiteboardDrawBranch `json:"else,omitempty"  jsonschema:"alt or par: each later operand (at most 3), from its first message at, with its guard as label."`
}

type WhiteboardDrawBranch struct {
	At    int    `json:"at"`
	Label string `json:"label,omitempty"`
}

type WhiteboardDrawPath struct {
	D     string  `json:"d"`
	Color string  `json:"color,omitempty"`
	Width float64 `json:"width,omitempty" jsonschema:"pen width, default 3."`
}

// WhiteboardReading is what a read shows the model: one text and images.
type WhiteboardReading struct {
	Text   string
	Images []WhiteboardImage
}

type WhiteboardImage struct {
	Label    string
	MIMEType string
	Data     []byte
}

type whiteboardAPI interface {
	FetchRunJSON(context.Context, string, any) error
	PostRunJSON(context.Context, string, string, any, any) error
}

type WhiteboardToolService interface {
	Read(context.Context, ArtifactInspectInput) (WhiteboardReading, error)
	Edit(context.Context, ArtifactEditInput) (WhiteboardReading, error)
}

var _ WhiteboardToolService = (*DefaultWhiteboardToolService)(nil)

type DefaultWhiteboardToolService struct {
	api whiteboardAPI
	run RunContext
	mu  sync.Mutex
	// plans keeps each Board's first-page region plan, so later image pages
	// can report when it moved without the Agent carrying it.
	plans map[string]string
	// seen is this Run's read set per Board: every object a read or an edit
	// showed it, by ref, with the revisions it saw. Edits fill their
	// revisions from it, so a collaborator's newer change still conflicts
	// while the model never copies revision numbers. A new Run starts empty,
	// so each turn reads before it overwrites.
	seen map[string]map[string]whiteboardRef
	// pending keeps the exact request of a change whose outcome is unknown,
	// so finishing it needs only its key and sends the same bytes.
	pending map[string]whiteboardBatch
	// drawing does the same for ops.
	drawing map[string]whiteboardCompose
}

type whiteboardCompose struct {
	RequestKey string               `json:"requestKey"`
	Near       *string              `json:"near,omitempty"`
	Into       *string              `json:"into,omitempty"`
	Revisions  []whiteboardRevision `json:"revisions,omitempty"`
	Ops        []WhiteboardDrawOp   `json:"ops"`
	Preview    bool                 `json:"preview"`
}

type whiteboardRevision struct {
	ID       string `json:"id"`
	Revision int64  `json:"revision"`
}

func NewWhiteboardToolService(
	api whiteboardAPI,
	run RunContext,
) (*DefaultWhiteboardToolService, error) {
	if err := validateRunContext(run); err != nil {
		return nil, err
	}
	return &DefaultWhiteboardToolService{
		api:     api,
		run:     run,
		plans:   map[string]string{},
		seen:    map[string]map[string]whiteboardRef{},
		pending: map[string]whiteboardBatch{},
		drawing: map[string]whiteboardCompose{},
	}, nil
}

// whiteboardRef is one object a read or an edit showed: its ref, ID and the
// revisions the Agent saw.
type whiteboardRef struct {
	Ref               string `json:"ref"`
	ID                string `json:"id"`
	Type              string `json:"type"`
	GeometryRevision  int64  `json:"geometryRevision"`
	ContentRevision   int64  `json:"contentRevision"`
	TextRevision      int64  `json:"textRevision"`
	LifecycleRevision int64  `json:"lifecycleRevision"`
	Revision          int64  `json:"revision"`
	// An item as read. The Board replaces geometry and properties whole, so
	// an edit naming a few fields is laid over these.
	Geometry   json.RawMessage `json:"geometry,omitempty"`
	Properties json.RawMessage `json:"properties,omitempty"`
}

type whiteboardRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type whiteboardRegion struct {
	Bounds   whiteboardRect `json:"bounds"`
	MIMEType string         `json:"mimeType"`
	Data     []byte         `json:"data"`
}

type whiteboardView struct {
	Sequence         int64             `json:"sequence"`
	Text             string            `json:"text"`
	Refs             []whiteboardRef   `json:"refs"`
	Area             *whiteboardRect   `json:"area"`
	Image            *whiteboardRegion `json:"image"`
	ImageUnavailable string            `json:"imageUnavailable"`
}

type whiteboardBatch struct {
	RequestKey string           `json:"requestKey"`
	Commands   []map[string]any `json:"commands"`
}

type whiteboardMutation struct {
	Sequence int64                    `json:"sequence"`
	IDs      map[string]string        `json:"ids"`
	Items    []whiteboardMutationItem `json:"items"`
	Edges    []whiteboardMutationEdge `json:"edges"`
	Removed  []string                 `json:"removed"`
	Replayed bool                     `json:"replayed"`
}

type whiteboardMutationItem struct {
	whiteboardRef
	whiteboardGeometry
	Text    string `json:"text"`
	Deleted bool   `json:"deleted"`
}

// whiteboardGeometry is an item's geometry, which a saved item carries
// flattened into itself.
type whiteboardGeometry struct {
	X             float64 `json:"x"`
	Y             float64 `json:"y"`
	Width         float64 `json:"width"`
	Height        float64 `json:"height"`
	Rotation      float64 `json:"rotation"`
	StackOrder    int64   `json:"stackOrder"`
	ParentFrameID *string `json:"parentFrameId"`
	ReadingOrder  int64   `json:"readingOrder"`
}

type whiteboardMutationEdge struct {
	ID       string `json:"id"`
	SourceID string `json:"sourceId"`
	TargetID string `json:"targetId"`
	Revision int64  `json:"revision"`
	Deleted  bool   `json:"deleted"`
}

func (s *DefaultWhiteboardToolService) path(id string) string {
	base := "/companies/" + url.PathEscape(
		s.run.CompanyID,
	) + "/runs/" + url.PathEscape(
		s.run.RunID,
	) + "/agent-artifacts/" + url.PathEscape(id) + "/whiteboard"
	return base
}

func (s *DefaultWhiteboardToolService) Read(ctx context.Context, in ArtifactInspectInput) (WhiteboardReading, error) {
	var reading WhiteboardReading
	if in.ArtifactID == "" || len(in.ItemRefs) > 100 || in.Area != "" && in.Area != "view" && in.Area != "all" {
		return reading, errors.New(
			"artifact_inspect reads a Whiteboard with area view or all and at most 100 itemRefs",
		)
	}
	if in.VisualOffset < 0 || in.ExpectedSequence != nil && *in.ExpectedSequence < 0 ||
		in.VisualOffset > 0 && (in.ExpectedSequence == nil || in.Area != "all" || !in.Image) {
		return reading, errors.New(
			"artifact_inspect continues image pages only with area=all, image=true and the first page's expectedSequence",
		)
	}
	// Whole-Board images come in pages; the text comes with the first one.
	wholeImages := in.Area == "all" && in.Image && len(in.ItemRefs) == 0
	if in.VisualOffset == 0 {
		view, err := s.view(ctx, in.ArtifactID, in.Area, in.ItemRefs, in.Image && !wholeImages)
		if err != nil {
			return reading, err
		}
		reading.Text = view.Text
		if view.Image != nil {
			reading.Images = append(reading.Images, WhiteboardImage{
				Label:    "Image of the area read, " + whiteboardRectText(view.Image.Bounds) + ":",
				MIMEType: view.Image.MIMEType,
				Data:     view.Image.Data,
			})
		}
		if view.ImageUnavailable != "" {
			reading.Text += view.ImageUnavailable + "\n"
		}
		if wholeImages {
			sequence := view.Sequence
			in.ExpectedSequence = &sequence
		}
	}
	if wholeImages {
		if err := s.wholeImages(ctx, in, &reading); err != nil {
			return reading, err
		}
	}
	return reading, nil
}

func (s *DefaultWhiteboardToolService) view(
	ctx context.Context,
	board, area string,
	refs []string,
	image bool,
) (whiteboardView, error) {
	var view whiteboardView
	request := struct {
		Area     string   `json:"area,omitempty"`
		ItemRefs []string `json:"itemRefs,omitempty"`
		Image    bool     `json:"image"`
	}{area, refs, image}
	key, err := runRequestKey("whiteboard_view", s.run, struct {
		Board   string
		Request any
	}{board, request})
	if err != nil {
		return view, err
	}
	if err := s.api.PostRunJSON(ctx, s.path(board)+"/view", key, request, &view); err != nil {
		return view, err
	}
	s.remember(board, view.Refs)
	return view, nil
}

// wholeImages adds one page of whole-Board images: an overview first, then
// up to four detailed regions per page.
func (s *DefaultWhiteboardToolService) wholeImages(
	ctx context.Context,
	in ArtifactInspectInput,
	reading *WhiteboardReading,
) error {
	request := struct {
		MaxItems         int    `json:"maxItems"`
		MaxBytes         int    `json:"maxBytes"`
		IncludeVisuals   bool   `json:"includeVisuals"`
		VisualOffset     int    `json:"visualOffset"`
		ExpectedSequence *int64 `json:"expectedSequence,omitempty"`
		ExpectedPlan     string `json:"expectedPlan,omitempty"`
	}{MaxItems: 1, MaxBytes: 4096, IncludeVisuals: true, VisualOffset: in.VisualOffset, ExpectedSequence: in.ExpectedSequence}
	if in.VisualOffset > 0 {
		s.mu.Lock()
		request.ExpectedPlan = s.plans[in.ArtifactID]
		s.mu.Unlock()
	}
	key, err := runRequestKey("whiteboard_read", s.run, in)
	if err != nil {
		return err
	}
	var result struct {
		Visuals *struct {
			Sequence       int64              `json:"sequence"`
			Regions        []whiteboardRegion `json:"regions"`
			Overview       *whiteboardRegion  `json:"overview"`
			NextOffset     *int               `json:"nextOffset"`
			HasMore        bool               `json:"hasMore"`
			Changed        bool               `json:"changed"`
			PlanHash       string             `json:"planHash"`
			RegionsShifted bool               `json:"regionsShifted"`
			Unavailable    string             `json:"unavailable"`
		} `json:"visuals"`
	}
	if err := s.api.PostRunJSON(ctx, s.path(in.ArtifactID)+"/context", key, request, &result); err != nil {
		return err
	}
	visuals := result.Visuals
	if visuals == nil {
		return nil
	}
	if in.VisualOffset == 0 && visuals.PlanHash != "" {
		s.mu.Lock()
		s.plans[in.ArtifactID] = visuals.PlanHash
		s.mu.Unlock()
	}
	if visuals.Overview != nil {
		reading.Images = append(reading.Images, WhiteboardImage{
			Label: "The whole Board at low resolution, " + whiteboardRectText(
				visuals.Overview.Bounds,
			) + ":",
			MIMEType: visuals.Overview.MIMEType,
			Data:     visuals.Overview.Data,
		})
	}
	for _, region := range visuals.Regions {
		reading.Images = append(reading.Images, WhiteboardImage{
			Label:    "Board region " + whiteboardRectText(region.Bounds) + ":",
			MIMEType: region.MIMEType,
			Data:     region.Data,
		})
	}
	var notes []string
	if visuals.Unavailable != "" {
		notes = append(notes, visuals.Unavailable)
	}
	switch {
	case visuals.RegionsShifted:
		notes = append(
			notes,
			"The Board's regions moved since the first page; read again from visualOffset=0 if every region matters.",
		)
	case visuals.Changed:
		notes = append(notes, "The Board changed since the first page; these images show the latest state.")
	}
	if visuals.HasMore && visuals.NextOffset != nil {
		notes = append(notes, fmt.Sprintf(
			"More regions remain: artifact_inspect area=all image=true visualOffset=%d expectedSequence=%d.",
			*visuals.NextOffset,
			*in.ExpectedSequence,
		))
	}
	if len(notes) > 0 {
		reading.Text += strings.Join(notes, "\n") + "\n"
	}
	return nil
}

// remember records what a read or edit showed in this Run's read set.
func (s *DefaultWhiteboardToolService) remember(board string, refs []whiteboardRef) {
	s.mu.Lock()
	defer s.mu.Unlock()
	known := s.seen[board]
	if known == nil {
		known = map[string]whiteboardRef{}
		s.seen[board] = known
	}
	for _, ref := range refs {
		// Every ref this object went by keeps naming it.
		for key, other := range known {
			if other.ID == ref.ID {
				known[key] = ref
			}
		}
		known[ref.Ref] = ref
	}
}

func (s *DefaultWhiteboardToolService) lookup(board, ref string) (whiteboardRef, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	known := s.seen[board]
	ref = strings.ToLower(strings.ReplaceAll(ref, "-", ""))
	if seen, ok := known[ref]; ok {
		return seen, true
	}
	if len(ref) == 32 {
		for _, seen := range known {
			if strings.ReplaceAll(seen.ID, "-", "") == ref {
				return seen, true
			}
		}
	}
	return whiteboardRef{}, false
}

var whiteboardHexRef = regexp.MustCompile(`^[0-9a-fA-F-]{4,36}$`)

// whiteboardOverwrites are the commands that change an existing object, and
// which revision guards them.
var whiteboardOverwrites = map[string]func(whiteboardRef, map[string]any){
	"geometry":     func(r whiteboardRef, c map[string]any) { c["expectedRevision"] = r.GeometryRevision },
	"content":      func(r whiteboardRef, c map[string]any) { c["expectedRevision"] = r.ContentRevision },
	"delete_item":  func(r whiteboardRef, c map[string]any) { c["expectedRevision"] = r.ContentRevision },
	"restore_item": func(r whiteboardRef, c map[string]any) { c["expectedRevision"] = r.ContentRevision },
	"update_edge":  func(r whiteboardRef, c map[string]any) { c["expectedRevision"] = r.Revision },
	"delete_edge":  func(r whiteboardRef, c map[string]any) { c["expectedRevision"] = r.Revision },
	"restore_edge": func(r whiteboardRef, c map[string]any) { c["expectedRevision"] = r.Revision },
	"text": func(r whiteboardRef, c map[string]any) {
		c["expectedRevision"], c["expectedTextRevision"] = r.LifecycleRevision, r.TextRevision
	},
}

// The Board's operation fields, mirrored from its contract (see
// TestWhiteboardEditMirrorsTheOperationsContract). The Board refuses a batch
// naming any other with a bare 400, so the tool names the field instead.
var (
	whiteboardCommandFields = []string{
		"type", "draftId", "id", "itemType", "expectedRevision", "expectedGeometryRevision",
		"expectedTextRevision", "geometry", "properties", "text", "points", "parentRef", "sourceRef",
		"targetRef", "label", "relation", "line", "startMarker", "endMarker", "route", "anchorY",
		"sourceLabel", "targetLabel",
	}
	whiteboardGeometryFields = []string{
		"x", "y", "width", "height", "rotation", "stackOrder", "parentFrameId", "readingOrder",
	}
	whiteboardPropertyFields = []string{
		"title", "color", "fill", "strokeWidth", "shape", "brush", "locked", "role", "caption", "alt",
		"attachmentId", "sections", "dividers", "head",
	}
)

// whiteboardChanges are the fields of the commands that change an item's
// geometry or properties; the Board ignores any other they carry, such as a
// connector's label, so the tool refuses it.
var whiteboardChanges = map[string][]string{
	"geometry": {"type", "id", "expectedRevision", "geometry", "parentRef"},
	"content":  {"type", "id", "expectedRevision", "properties"},
}

// whiteboardFold checks a command's fields and moves the geometry or
// property fields a geometry or content command names at its top level
// into the object the Board reads them from.
func whiteboardFold(index int, command map[string]any) error {
	kind, _ := command["type"].(string)
	nested := map[string][]string{"geometry": whiteboardGeometryFields, "properties": whiteboardPropertyFields}
	for field := range nested {
		if value, ok := command[field]; ok {
			object, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("commands[%d] (%s): %s must be an object", index, kind, field)
			}
			command[field] = maps.Clone(object)
		}
	}
	into := map[string]string{"geometry": "geometry", "content": "properties"}[kind]
	for key, value := range command {
		known := whiteboardCommandFields
		if own, ok := whiteboardChanges[kind]; ok {
			known = own
		}
		if slices.Contains(known, key) {
			continue
		}
		if into == "" || !slices.Contains(nested[into], key) {
			return whiteboardNoField(index, kind, into, key)
		}
		object, _ := command[into].(map[string]any)
		if object == nil {
			object = map[string]any{}
			command[into] = object
		}
		object[key] = value
		delete(command, key)
	}
	for field, known := range nested {
		object, _ := command[field].(map[string]any)
		for key := range object {
			if !slices.Contains(known, key) {
				return whiteboardNoField(index, kind, field, key)
			}
		}
	}
	return nil
}

func whiteboardNoField(index int, kind, within, key string) error {
	known := map[string][]string{
		"geometry": whiteboardGeometryFields, "properties": whiteboardPropertyFields,
	}[within]
	if known == nil {
		known = whiteboardCommandFields
	} else {
		within += " "
	}
	return fmt.Errorf(
		"commands[%d] (%s): there is no %sfield %q; the fields are %s",
		index, kind, within, key, strings.Join(known, ", "),
	)
}

// whiteboardKeep lays a geometry or content change over the object as this
// Run read it, since the Board replaces both whole: a move keeps the frame
// and the stacking, a restyle keeps the shape. A null removes a field. The
// revision filled beside it refuses the change if the object moved on.
func whiteboardKeep(seen whiteboardRef, command map[string]any) {
	field, read := "properties", seen.Properties
	if command["type"] == "geometry" {
		field, read = "geometry", seen.Geometry
	}
	kept := map[string]any{}
	if err := json.Unmarshal(read, &kept); err != nil {
		return
	}
	changes, _ := command[field].(map[string]any)
	for key, value := range changes {
		if value == nil {
			delete(kept, key)
		} else {
			kept[key] = value
		}
	}
	command[field] = kept
}

// expand turns the model's refs into IDs and fills each overwrite's
// revisions from what this Run read. Referring to an object it has not read
// (a connector end, a parent frame) is resolved on the Board; changing one
// it has not read is refused, so a turn never overwrites blind.
func (s *DefaultWhiteboardToolService) expand(
	ctx context.Context,
	board string,
	commands []map[string]any,
) ([]map[string]any, error) {
	expanded := make([]map[string]any, len(commands))
	drafts := map[string]bool{}
	for i, command := range commands {
		expanded[i] = maps.Clone(command)
		if err := whiteboardFold(i, expanded[i]); err != nil {
			return nil, err
		}
		if draft, ok := command["draftId"].(string); ok {
			drafts[draft] = true
		}
	}
	type pointer struct {
		set   func(string)
		value string
	}
	var unknown []pointer
	refer := func(value any, set func(string)) {
		ref, ok := value.(string)
		if !ok || ref == "" || drafts[ref] {
			return
		}
		if seen, ok := s.lookup(board, ref); ok {
			set(seen.ID)
			return
		}
		if _, err := uuid.Parse(ref); err != nil && whiteboardHexRef.MatchString(ref) {
			unknown = append(unknown, pointer{set: set, value: ref})
		}
	}
	var unread []string
	for _, command := range expanded {
		kind, _ := command["type"].(string)
		if fill, overwrite := whiteboardOverwrites[kind]; overwrite {
			ref, _ := command["id"].(string)
			seen, ok := s.lookup(board, ref)
			_, revised := command["expectedRevision"]
			switch {
			case ok:
				command["id"] = seen.ID
				if !revised {
					fill(seen, command)
					if kind == "geometry" || kind == "content" {
						whiteboardKeep(seen, command)
					}
				}
			case !revised:
				unread = append(unread, ref)
			}
		}
		for _, field := range []string{"sourceRef", "targetRef", "parentRef"} {
			refer(command[field], func(id string) { command[field] = id })
		}
		if geometry, ok := command["geometry"].(map[string]any); ok {
			refer(geometry["parentFrameId"], func(id string) { geometry["parentFrameId"] = id })
		}
	}
	if len(unread) > 0 {
		return nil, fmt.Errorf(
			"read %s before changing them (artifact_inspect itemRefs=[%s]); this turn has not seen them, "+
				"so a collaborator's newer change could be overwritten",
			strings.Join(unread, ", "), strings.Join(unread, ", "),
		)
	}
	if len(unknown) > 0 {
		refs := make([]string, 0, len(unknown))
		for _, item := range unknown {
			if !slices.Contains(refs, item.value) {
				refs = append(refs, item.value)
			}
		}
		if _, err := s.view(ctx, board, "", refs, false); err != nil {
			return nil, err
		}
		for _, item := range unknown {
			seen, ok := s.lookup(board, item.value)
			if !ok {
				return nil, fmt.Errorf("no object on this Board has the ref %s", item.value)
			}
			item.set(seen.ID)
		}
	}
	return expanded, nil
}

// Edit is artifact_edit on a Whiteboard: ops add content, commands change
// objects, and a requestKey alone finishes the call it left unfinished.
func (s *DefaultWhiteboardToolService) Edit(ctx context.Context, in ArtifactEditInput) (WhiteboardReading, error) {
	if in.ArtifactID == "" {
		return WhiteboardReading{}, errors.New("artifact_edit requires an artifactId")
	}
	if len(in.Ops) > 0 && len(in.Commands) > 0 {
		return WhiteboardReading{}, errors.New("artifact_edit takes ops or commands, not both")
	}
	ops := len(in.Ops) > 0
	if !ops && len(in.Commands) == 0 && in.RequestKey != "" {
		if parsed, err := uuid.Parse(in.RequestKey); err == nil {
			s.mu.Lock()
			_, ops = s.drawing[parsed.String()]
			s.mu.Unlock()
		}
	}
	if ops {
		return s.draw(ctx, in)
	}
	text, err := s.change(ctx, in)
	return WhiteboardReading{Text: text}, err
}

func (s *DefaultWhiteboardToolService) change(ctx context.Context, in ArtifactEditInput) (string, error) {
	if in.ArtifactID == "" || len(in.Commands) > 100 {
		return "", errors.New("artifact_edit takes at most 100 commands")
	}
	var batch whiteboardBatch
	if in.RequestKey != "" {
		parsed, err := uuid.Parse(in.RequestKey)
		if err != nil {
			return "", errors.New("artifact_edit requestKey must be a UUID")
		}
		s.mu.Lock()
		cached, ok := s.pending[parsed.String()]
		s.mu.Unlock()
		if ok {
			batch = cached
		} else {
			batch.RequestKey = parsed.String()
		}
	}
	if batch.Commands == nil {
		if len(in.Commands) == 0 {
			return "", errors.New("artifact_edit takes 1 to 100 commands")
		}
		commands, err := s.expand(ctx, in.ArtifactID, in.Commands)
		if err != nil {
			return "", err
		}
		batch.Commands = commands
		// Every call is a new change; only a retry reuses a key.
		if batch.RequestKey == "" {
			batch.RequestKey = uuid.NewString()
		}
	}
	var result whiteboardMutation
	err := s.api.PostRunJSON(ctx, s.path(in.ArtifactID)+"/operations", batch.RequestKey, batch, &result)
	var verdict *apiclient.APIError
	if err != nil && (!errors.As(err, &verdict) || verdict.StatusCode >= http.StatusBadGateway) {
		// Without the server's verdict the batch may already be saved.
		s.mu.Lock()
		s.pending[batch.RequestKey] = batch
		s.mu.Unlock()
		return "", fmt.Errorf(
			"artifact_edit outcome is unknown (%w): the change may already be saved; "+
				"call artifact_edit again with requestKey=%s and no commands to finish it without duplicating it",
			err,
			batch.RequestKey,
		)
	}
	s.mu.Lock()
	delete(s.pending, batch.RequestKey)
	s.mu.Unlock()
	if err != nil {
		if verdict.Code == "whiteboard_conflict" {
			return "", fmt.Errorf(
				"%w. A collaborator changed one of these objects after you read it: read them again "+
					"(artifact_inspect itemRefs) and decide again; never resend the same change blindly",
				err,
			)
		}
		return "", err
	}
	return s.mutationSummary(in.ArtifactID, result), nil
}

// resolve turns refs into IDs: a full ID stays, a ref this Run read maps
// through its read set, and any other ref is looked up on the Board.
// Only references go through here; changing an object needs a read.
func (s *DefaultWhiteboardToolService) resolve(
	ctx context.Context,
	board string,
	refs []string,
) (map[string]string, error) {
	ids := map[string]string{}
	var unknown []string
	for _, ref := range refs {
		if _, err := uuid.Parse(ref); err == nil {
			ids[ref] = ref
		} else if seen, ok := s.lookup(board, ref); ok {
			ids[ref] = seen.ID
		} else if !slices.Contains(unknown, ref) {
			unknown = append(unknown, ref)
		}
	}
	if len(unknown) == 0 {
		return ids, nil
	}
	if _, err := s.view(ctx, board, "", unknown, false); err != nil {
		return nil, err
	}
	for _, ref := range unknown {
		seen, ok := s.lookup(board, ref)
		if !ok {
			return nil, fmt.Errorf("no object on this Board has the ref %s", ref)
		}
		ids[ref] = seen.ID
	}
	return ids, nil
}

// composeRequest builds what ops send: refs resolved to IDs,
// and each arranged object's geometry revision from this Run's reads.
func (s *DefaultWhiteboardToolService) composeRequest(
	ctx context.Context,
	in ArtifactEditInput,
) (whiteboardCompose, error) {
	request := whiteboardCompose{Preview: in.Preview, Ops: make([]WhiteboardDrawOp, len(in.Ops))}
	keys := map[string]bool{}
	for _, op := range in.Ops {
		keys[op.Key] = op.Key != ""
		for _, item := range op.Items {
			keys[item.Key] = item.Key != ""
		}
		for _, node := range op.Nodes {
			keys[node.Key] = node.Key != ""
		}
	}
	var refs []string
	add := func(ref string) {
		if ref != "" && !keys[ref] {
			refs = append(refs, ref)
		}
	}
	add(in.Near)
	add(in.Into)
	for _, op := range in.Ops {
		if op.Type == "connect" {
			add(op.From)
			add(op.To)
		}
		for _, edge := range op.Edges {
			add(edge.From)
			add(edge.To)
		}
	}
	ids, err := s.resolve(ctx, in.ArtifactID, refs)
	if err != nil {
		return request, err
	}
	id := func(ref string) string {
		if resolved, ok := ids[ref]; ok {
			return resolved
		}
		return ref
	}
	if in.Near != "" {
		near := id(in.Near)
		request.Near = &near
	}
	if in.Into != "" {
		into := id(in.Into)
		request.Into = &into
	}
	var unread []string
	for i, op := range in.Ops {
		op.From, op.To = id(op.From), id(op.To)
		op.Edges = slices.Clone(op.Edges)
		for j := range op.Edges {
			op.Edges[j].From, op.Edges[j].To = id(op.Edges[j].From), id(op.Edges[j].To)
		}
		if op.Type == "arrange" {
			arranged := make([]string, 0, len(op.Refs))
			for _, ref := range op.Refs {
				seen, ok := s.lookup(in.ArtifactID, ref)
				if !ok {
					if !slices.Contains(unread, ref) {
						unread = append(unread, ref)
					}
					continue
				}
				arranged = append(arranged, seen.ID)
				// Each object's revision goes once, however many ops move it.
				if !slices.ContainsFunc(
					request.Revisions,
					func(r whiteboardRevision) bool { return r.ID == seen.ID },
				) {
					request.Revisions = append(
						request.Revisions,
						whiteboardRevision{ID: seen.ID, Revision: seen.GeometryRevision},
					)
				}
			}
			op.Refs = arranged
		}
		request.Ops[i] = op
	}
	if len(unread) > 0 {
		return request, fmt.Errorf(
			"read %s before arranging them (artifact_inspect itemRefs=[%s])",
			strings.Join(unread, ", "), strings.Join(unread, ", "),
		)
	}
	return request, nil
}

func (s *DefaultWhiteboardToolService) draw(ctx context.Context, in ArtifactEditInput) (WhiteboardReading, error) {
	var reading WhiteboardReading
	if in.ArtifactID == "" || len(in.Ops) > 20 {
		return reading, errors.New("artifact_edit takes at most 20 ops")
	}
	var request whiteboardCompose
	if in.RequestKey != "" {
		parsed, err := uuid.Parse(in.RequestKey)
		if err != nil {
			return reading, errors.New("artifact_edit requestKey must be a UUID")
		}
		s.mu.Lock()
		cached, ok := s.drawing[parsed.String()]
		s.mu.Unlock()
		if ok {
			request = cached
		} else {
			request.RequestKey = parsed.String()
		}
	}
	if request.Ops == nil {
		if len(in.Ops) == 0 {
			return reading, errors.New("artifact_edit takes 1 to 20 ops")
		}
		key := request.RequestKey
		var err error
		if request, err = s.composeRequest(ctx, in); err != nil {
			return reading, err
		}
		request.RequestKey = key
		if request.RequestKey == "" {
			request.RequestKey = uuid.NewString()
		}
	}
	var result struct {
		Mutation  whiteboardMutation `json:"mutation"`
		Bounds    *whiteboardRect    `json:"bounds"`
		Placement string             `json:"placement"`
		Preview   *whiteboardRegion  `json:"preview"`
	}
	err := s.api.PostRunJSON(ctx, s.path(in.ArtifactID)+"/compose", request.RequestKey, request, &result)
	var verdict *apiclient.APIError
	if err != nil && (!errors.As(err, &verdict) || verdict.StatusCode >= http.StatusBadGateway) {
		s.mu.Lock()
		s.drawing[request.RequestKey] = request
		s.mu.Unlock()
		return reading, fmt.Errorf(
			"artifact_edit outcome is unknown (%w): the content may already be on the Board; "+
				"call artifact_edit again with requestKey=%s and no ops to finish it without duplicating it",
			err,
			request.RequestKey,
		)
	}
	s.mu.Lock()
	delete(s.drawing, request.RequestKey)
	s.mu.Unlock()
	if err != nil {
		if verdict.Code == "whiteboard_conflict" {
			return reading, fmt.Errorf(
				"%w. A collaborator moved an object you arranged after you read it: read it again and reconsider",
				err,
			)
		}
		return reading, err
	}
	var text strings.Builder
	if b := result.Bounds; b != nil {
		where := whiteboardRectText(*b)
		switch result.Placement {
		case "view":
			fmt.Fprintf(&text, "Placed at %s, where the person you work for is looking.\n", where)
		case "partly":
			fmt.Fprintf(
				&text,
				"Placed at %s, starting where the person you work for is looking; it is larger "+
					"than the free part of their view, so some of it is past its edge.\n",
				where,
			)
		case "nearby":
			fmt.Fprintf(&text, "The area the person you work for is looking at had no room, so it went "+
				"right beside it, at %s; tell them where it is.\n", where)
		case "frame_full":
			fmt.Fprintf(&text, "The frame had no room, so the content went beside it, at %s.\n", where)
		case "unwatched":
			fmt.Fprintf(&text, "Nobody is looking at the Board now, so it went beside its content, at %s; "+
				"tell the person where it is.\n", where)
		default:
			fmt.Fprintf(&text, "Placed at %s.\n", where)
		}
	}
	text.WriteString(s.mutationSummary(in.ArtifactID, result.Mutation))
	reading.Text = text.String()
	if result.Preview != nil {
		reading.Images = append(reading.Images, WhiteboardImage{
			Label:    "The result, " + whiteboardRectText(result.Preview.Bounds) + ":",
			MIMEType: result.Preview.MIMEType,
			Data:     result.Preview.Data,
		})
	}
	return reading, nil
}

// mutationSummary records what a change committed in the read set and
// tells the model, by ref, what it created (by the draft or key it gave),
// changed and removed. A drawing's strokes are listed together.
func (s *DefaultWhiteboardToolService) mutationSummary(board string, result whiteboardMutation) string {
	draftOf := make(map[string]string, len(result.IDs))
	for draft, id := range result.IDs {
		draftOf[id] = draft
	}
	var refs []whiteboardRef
	var created, changed, strokes, connectors []string
	described := map[string]bool{}
	describe := func(id, text string) {
		described[id] = true
		if draft, ok := draftOf[id]; ok {
			created = append(created, draft+"="+text)
		} else {
			changed = append(changed, text)
		}
	}
	for _, item := range result.Items {
		ref := item.whiteboardRef
		ref.Ref = whiteboardShortRef(ref.ID)
		ref.Geometry, _ = json.Marshal(item.whiteboardGeometry)
		var properties struct {
			Title string `json:"title"`
			Shape string `json:"shape"`
		}
		_ = json.Unmarshal(ref.Properties, &properties)
		refs = append(refs, ref)
		if _, ok := draftOf[ref.ID]; ok && ref.Type == "stroke" {
			described[ref.ID] = true
			strokes = append(strokes, ref.Ref)
			continue
		}
		label := item.Text
		if label == "" {
			label = properties.Title
		}
		kind := ref.Type
		if properties.Shape != "" {
			kind += ":" + properties.Shape
		}
		describe(ref.ID, strings.TrimSpace(ref.Ref+" "+kind+" "+whiteboardQuote(label)))
	}
	for _, edge := range result.Edges {
		ref := whiteboardRef{
			Ref: whiteboardShortRef(edge.ID), ID: edge.ID, Type: "edge", Revision: edge.Revision,
		}
		refs = append(refs, ref)
		// A diagram's or sequence's connectors read as one line of refs.
		if draft, ok := draftOf[edge.ID]; ok && whiteboardComposedConnector.MatchString(draft) {
			described[edge.ID] = true
			connectors = append(connectors, ref.Ref)
			continue
		}
		describe(edge.ID, fmt.Sprintf(
			"%s edge %s->%s", ref.Ref, whiteboardShortRef(edge.SourceID), whiteboardShortRef(edge.TargetID),
		))
	}
	// A replay leaves out objects collaborators changed since; their refs
	// still name them.
	for _, draft := range slices.Sorted(maps.Keys(result.IDs)) {
		if id := result.IDs[draft]; !described[id] {
			created = append(created, draft+"="+whiteboardShortRef(id))
		}
	}
	s.remember(board, refs)
	var text strings.Builder
	if result.Replayed {
		fmt.Fprintf(&text, "Already saved earlier, sequence %d.\n", result.Sequence)
	} else {
		fmt.Fprintf(&text, "Saved, sequence %d.\n", result.Sequence)
	}
	if len(strokes) > 0 {
		created = append(created, fmt.Sprintf("%d strokes: %s", len(strokes), strings.Join(strokes, " ")))
	}
	if len(connectors) > 0 {
		created = append(
			created,
			fmt.Sprintf("%d connectors: %s", len(connectors), strings.Join(connectors, " ")),
		)
	}
	if len(created) > 0 {
		fmt.Fprintf(&text, "Created: %s\n", strings.Join(created, "; "))
	}
	if len(changed) > 0 {
		fmt.Fprintf(&text, "Changed: %s\n", strings.Join(changed, "; "))
	}
	if len(result.Removed) > 0 {
		removed := make([]string, 0, len(result.Removed))
		for _, id := range result.Removed {
			removed = append(removed, whiteboardShortRef(id))
		}
		fmt.Fprintf(&text, "Removed: %s\n", strings.Join(removed, ", "))
	}
	return text.String()
}

// whiteboardComposedConnector matches the drafts ops give a
// diagram's links and a sequence's messages.
var whiteboardComposedConnector = regexp.MustCompile(`^o\d+\.[em]\d+$`)

// whiteboardShortRef is the ref the Board's readings give an object unless
// another one shares its first eight hex digits.
func whiteboardShortRef(id string) string {
	hex := strings.ReplaceAll(id, "-", "")
	if len(hex) < 8 {
		return hex
	}
	return hex[:8]
}

func whiteboardQuote(value string) string {
	if value == "" {
		return ""
	}
	if utf8.RuneCountInString(value) > 60 {
		value = string([]rune(value)[:60]) + "…"
	}
	return strconv.Quote(value)
}

func whiteboardRectText(r whiteboardRect) string {
	return fmt.Sprintf("%.0f,%.0f %.0fx%.0f", r.X, r.Y, r.Width, r.Height)
}
