package tools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/mcp/apiclient"
	"boardly.dev/boardly/server/internal/runchannel"
)

const (
	whiteboardNoteID  = "a3f9c2e1-0000-4000-8000-000000000002"
	whiteboardEdgeID  = "9b01c3aa-0000-4000-8000-000000000006"
	whiteboardInkID   = "c0ffee00-0000-4000-8000-000000000004"
	whiteboardNewID   = "5e6f7a8b-0000-4000-8000-000000000009"
	whiteboardFrameID = "f7c1d2e0-0000-4000-8000-000000000001"
)

// whiteboardChannelServer answers Run channel calls with route, which sees
// the API request the channel carried.
func whiteboardChannelServer(
	t *testing.T,
	route func(http.ResponseWriter, *http.Request),
) *DefaultWhiteboardToolService {
	t.Helper()
	run := testPresentRunContext()
	const capability = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != runchannel.Path || r.Method != http.MethodPost ||
			r.Header.Get("Authorization") != runchannel.AuthorizationScheme+" "+capability ||
			r.Header.Get("X-Boardly-Run-Token") != "" {
			t.Error("whiteboard tool must use only the Run channel capability")
		}
		var request runchannel.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		response := httptest.NewRecorder()
		api := httptest.NewRequest(request.Method, "/api/v1"+request.Path, bytes.NewReader(request.Body))
		api.Header.Set("Idempotency-Key", request.RequestKey)
		route(response, api)
		_ = json.NewEncoder(w).
			Encode(runchannel.Response{SchemaVersion: 1, Status: response.Code, Body: response.Body.Bytes()})
	}))
	t.Cleanup(server.Close)
	service, err := NewWhiteboardToolService(apiclient.NewRunChannel(runchannel.RunControlConfig{
		SchemaVersion: 1, Endpoint: server.URL, Capability: capability, RunID: run.RunID,
	}), run)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

// whiteboardBoard is a fake Board behind the Run channel: it answers views
// with a note and a connector, records every batch, and answers batches
// with the reply the test sets.
type whiteboardBoard struct {
	mu      sync.Mutex
	paths   []string
	views   []map[string]any
	batches []map[string]any
	reply   func(batch map[string]any) (int, string)
	draws   []map[string]any
	drawn   func(request map[string]any) (int, string)
}

func (b *whiteboardBoard) route(t *testing.T) func(http.ResponseWriter, *http.Request) {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if r.Body != nil {
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.paths = append(b.paths, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/view"):
			if r.Header.Get("Idempotency-Key") == "" {
				t.Error("a view must carry a request key")
			}
			b.views = append(b.views, body)
			refs := []map[string]any{
				{
					"ref":               "a3f9c2e1",
					"id":                whiteboardNoteID,
					"type":              "sticky",
					"geometryRevision":  2,
					"contentRevision":   3,
					"textRevision":      4,
					"lifecycleRevision": 5,
					"geometry": map[string]any{
						"x": 0, "y": 0, "width": 240, "height": 190, "rotation": 0,
						"stackOrder": 4, "parentFrameId": whiteboardFrameID, "readingOrder": 2,
					},
					"properties": map[string]any{
						"color": "blue", "fill": "#fff7cc", "locked": false,
					},
				},
				{"ref": "9b01c3aa", "id": whiteboardEdgeID, "type": "edge", "revision": 7},
			}
			if wanted, _ := body["itemRefs"].([]any); len(wanted) > 0 {
				refs = []map[string]any{
					{
						"ref":              "c0ffee00",
						"id":               whiteboardInkID,
						"type":             "stroke",
						"geometryRevision": 1,
					},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sequence": 12,
				"text":     "Board \"Plan\", you can edit, sequence 12.\na3f9c2e1 sticky 0,0 240x190 \"Hello\"\n",
				"refs":     refs,
				"area":     map[string]any{"x": 0, "y": 0, "width": 1000, "height": 600},
				"image": map[string]any{
					"bounds": map[string]any{
						"x":      0,
						"y":      0,
						"width":  1000,
						"height": 600,
					},
					"mimeType": "image/png",
					"data":     []byte("view PNG"),
				},
			})
		case strings.HasSuffix(r.URL.Path, "/compose"):
			b.draws = append(b.draws, body)
			status, reply := b.drawn(body)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(reply))
		case strings.HasSuffix(r.URL.Path, "/operations"):
			if body["requestKey"] != r.Header.Get("Idempotency-Key") {
				t.Error("retry identity differs from the batch")
			}
			b.batches = append(b.batches, body)
			status, reply := b.reply(body)
			w.WriteHeader(status)
			_, _ = w.Write([]byte(reply))
		default:
			http.NotFound(w, r)
		}
	}
}

func TestWhiteboardReadShowsTheCompactViewAndItsImage(t *testing.T) {
	t.Parallel()
	board := &whiteboardBoard{}
	service := whiteboardChannelServer(t, board.route(t))
	reading, err := service.Read(t.Context(), ArtifactInspectInput{ArtifactID: "board-a", Image: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(reading.Text, `Board "Plan"`) || len(reading.Images) != 1 ||
		string(
			reading.Images[0].Data,
		) != "view PNG" || !strings.Contains(reading.Images[0].Label, "0,0 1000x600") {
		t.Fatalf("reading = %+v", reading)
	}
	if view := board.views[0]; view["image"] != true || view["area"] != nil {
		t.Fatalf("view request = %+v; want the default area with an image", view)
	}
	if _, err := service.Read(
		t.Context(),
		ArtifactInspectInput{ArtifactID: "board-a", Area: "nearby"},
	); err == nil {
		t.Fatal("an unknown area was accepted")
	}
	if _, err := service.Read(
		t.Context(),
		ArtifactInspectInput{ArtifactID: "board-a", VisualOffset: 4},
	); err == nil {
		t.Fatal("an image page continued without the whole Board and its sequence")
	}
}

// The model names objects by the refs a read gave and never copies a
// revision; the tool fills the revisions this Run saw, so a collaborator's
// newer change still conflicts.
func TestWhiteboardEditNamesObjectsByRefAndFillsRevisions(t *testing.T) {
	t.Parallel()
	board := &whiteboardBoard{reply: func(map[string]any) (int, string) {
		return http.StatusOK, `{"sequence":13,"ids":{"n1":"` + whiteboardNewID + `"},"items":[` +
			`{"id":"` + whiteboardNoteID + `","type":"sticky","text":"Hello","geometryRevision":3,` +
			`"contentRevision":3,"textRevision":5,"lifecycleRevision":5,"properties":{}},` +
			`{"id":"` + whiteboardNewID + `","type":"sticky","text":"New idea","geometryRevision":1,` +
			`"contentRevision":1,"textRevision":1,"lifecycleRevision":1,"properties":{}}],"edges":[],"removed":[]}`
	}}
	service := whiteboardChannelServer(t, board.route(t))
	if _, err := service.Read(t.Context(), ArtifactInspectInput{ArtifactID: "board-a"}); err != nil {
		t.Fatal(err)
	}
	summary, err := service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a", Commands: []map[string]any{
		{
			"type":     "geometry",
			"id":       "a3f9c2e1",
			"geometry": map[string]any{"x": 10, "y": 0, "width": 240, "height": 190},
		},
		{"type": "text", "id": "A3F9C2E1", "text": "Hello again"},
		{"type": "create_item", "draftId": "n1", "itemType": "sticky", "text": "New idea"},
		{"type": "create_edge", "draftId": "e1", "sourceRef": "n1", "targetRef": "a3f9c2e1"},
		{"type": "delete_edge", "id": "9b01c3aa"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	commands, _ := board.batches[0]["commands"].([]any)
	get := func(i int) map[string]any { return commands[i].(map[string]any) }
	if get(0)["id"] != whiteboardNoteID || get(0)["expectedRevision"] != float64(2) {
		t.Fatalf("geometry = %+v; want the note's ID and the geometry revision read", get(0))
	}
	if get(1)["expectedRevision"] != float64(5) || get(1)["expectedTextRevision"] != float64(4) {
		t.Fatalf("text = %+v; want the lifecycle and text revisions read", get(1))
	}
	if get(3)["sourceRef"] != "n1" || get(3)["targetRef"] != whiteboardNoteID {
		t.Fatalf("connector = %+v; want the draft kept and the ref expanded", get(3))
	}
	if get(4)["id"] != whiteboardEdgeID || get(4)["expectedRevision"] != float64(7) {
		t.Fatalf("delete_edge = %+v; want the connector's revision", get(4))
	}
	for _, want := range []string{"Saved, sequence 13.", `n1=5e6f7a8b sticky "New idea"`, `Changed: a3f9c2e1 sticky "Hello"`} {
		if !strings.Contains(summary.Text, want) {
			t.Fatalf("summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary.Text, whiteboardNewID) {
		t.Fatalf("summary spells out full IDs: %s", summary)
	}
	// The edit's own result is what this Run saw last.
	if _, err := service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a", Commands: []map[string]any{
		{
			"type":     "geometry",
			"id":       "a3f9c2e1",
			"geometry": map[string]any{"x": 20, "y": 0, "width": 240, "height": 190},
		},
		{"type": "content", "id": "5e6f7a8b", "properties": map[string]any{"fill": "#dff0fc"}},
	}}); err != nil {
		t.Fatal(err)
	}
	commands, _ = board.batches[1]["commands"].([]any)
	if get(0)["expectedRevision"] != float64(3) || get(1)["id"] != whiteboardNewID ||
		get(1)["expectedRevision"] != float64(1) {
		t.Fatalf("second edit = %+v; want the revisions the first edit returned", commands)
	}
}

// The Board replaces geometry and properties whole, so an edit naming a few
// fields keeps the rest as read: a move stays in its frame, a new title
// keeps the colors. Fields the Board does not know are named, not sent.
func TestWhiteboardEditChangesOnlyTheFieldsItNames(t *testing.T) {
	t.Parallel()
	board := &whiteboardBoard{reply: func(map[string]any) (int, string) {
		return http.StatusOK, `{"sequence":13,"ids":{},"items":[],"edges":[],"removed":[]}`
	}}
	service := whiteboardChannelServer(t, board.route(t))
	if _, err := service.Read(t.Context(), ArtifactInspectInput{ArtifactID: "board-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a", Commands: []map[string]any{
		{"type": "content", "id": "a3f9c2e1", "title": "Orders", "properties": map[string]any{"fill": nil}},
		{"type": "geometry", "id": "a3f9c2e1", "x": 300},
	}}); err != nil {
		t.Fatal(err)
	}
	commands, _ := board.batches[0]["commands"].([]any)
	content, _ := commands[0].(map[string]any)
	if properties, _ := content["properties"].(map[string]any); properties["title"] != "Orders" ||
		properties["color"] != "blue" || properties["locked"] != false || properties["fill"] != nil ||
		len(properties) != 3 || content["title"] != nil {
		t.Fatalf("content = %+v; want the title over the read properties, without the fill", content)
	}
	move, _ := commands[1].(map[string]any)
	if geometry, _ := move["geometry"].(map[string]any); geometry["x"] != float64(300) ||
		geometry["width"] != float64(240) || geometry["parentFrameId"] != whiteboardFrameID ||
		geometry["stackOrder"] != float64(4) || move["x"] != nil {
		t.Fatalf("geometry = %+v; want x over the read geometry, still in its frame", move)
	}
	for want, command := range map[string]map[string]any{
		`there is no properties field "label"`: {"type": "content", "id": "a3f9c2e1", "label": "Orders"},
		`there is no properties field "text"`: {
			"type": "create_item", "draftId": "n1", "itemType": "sticky", "properties": map[string]any{"text": "Idea"},
		},
		`there is no field "color"`:  {"type": "text", "id": "a3f9c2e1", "color": "pink"},
		`geometry must be an object`: {"type": "geometry", "id": "a3f9c2e1", "geometry": "300,0"},
	} {
		input := ArtifactEditInput{ArtifactID: "board-a", Commands: []map[string]any{command}}
		_, err := service.Edit(t.Context(), input)
		if err == nil || !strings.Contains(err.Error(), "commands[0]") || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: error = %v; want %q", command, err, want)
		}
	}
	if len(board.batches) != 1 {
		t.Fatalf("batches = %d; a command with an unknown field reached the Board", len(board.batches))
	}
}

// A turn may connect to or nest in objects it has not read, but changing one
// it has not read is refused before anything is sent.
func TestWhiteboardEditRefusesUnreadOverwritesButResolvesReferences(t *testing.T) {
	t.Parallel()
	board := &whiteboardBoard{reply: func(map[string]any) (int, string) {
		return http.StatusOK, `{"sequence":2,"ids":{},"items":[],"edges":[],"removed":[]}`
	}}
	service := whiteboardChannelServer(t, board.route(t))
	_, err := service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a", Commands: []map[string]any{
		{"type": "delete_item", "id": "deadbeef"},
	}})
	if err == nil || !strings.Contains(err.Error(), "read deadbeef before changing") || len(board.batches) != 0 {
		t.Fatalf("unread delete = %v, batches = %d; want it refused locally", err, len(board.batches))
	}
	if _, err = service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a", Commands: []map[string]any{
		{"type": "create_edge", "draftId": "e1", "sourceRef": "c0ffee00", "targetRef": whiteboardNoteID},
	}}); err != nil {
		t.Fatal(err)
	}
	if refs, _ := board.views[0]["itemRefs"].([]any); len(refs) != 1 || refs[0] != "c0ffee00" {
		t.Fatalf("view = %+v; want the unknown ref resolved on the Board", board.views)
	}
	commands, _ := board.batches[0]["commands"].([]any)
	if edge := commands[0].(map[string]any); edge["sourceRef"] != whiteboardInkID ||
		edge["targetRef"] != whiteboardNoteID {
		t.Fatalf("connector = %+v", edge)
	}
}

// An edit whose outcome is unknown is finished by its key alone, with the
// same bytes; a server verdict is answered as it is.
func TestWhiteboardEditFinishesUnknownOutcomesByKey(t *testing.T) {
	t.Parallel()
	calls := 0
	board := &whiteboardBoard{reply: func(map[string]any) (int, string) {
		calls++
		switch calls {
		case 1:
			return http.StatusBadGateway, `{"code":"server_unreachable","detail":"timed out"}`
		case 2:
			return http.StatusOK, `{"sequence":4,"ids":{"n1":"` + whiteboardNewID + `"},"items":[],"edges":[],"removed":[],"replayed":true}`
		default:
			return http.StatusConflict, `{"code":"whiteboard_conflict","detail":"changed"}`
		}
	}}
	service := whiteboardChannelServer(t, board.route(t))
	edit := ArtifactEditInput{ArtifactID: "board-a", Commands: []map[string]any{
		{"type": "create_item", "draftId": "n1", "itemType": "sticky"},
	}}
	_, err := service.Edit(t.Context(), edit)
	key, _ := board.batches[0]["requestKey"].(string)
	if err == nil || !strings.Contains(err.Error(), "requestKey="+key) {
		t.Fatalf("unknown outcome = %v; want the key to finish it with", err)
	}
	summary, err := service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a", RequestKey: key})
	if err != nil || !strings.Contains(summary.Text, "Already saved earlier") {
		t.Fatalf("finishing by key = %q, %v", summary, err)
	}
	first, _ := json.Marshal(board.batches[0])
	second, _ := json.Marshal(board.batches[1])
	if !bytes.Equal(first, second) {
		t.Fatalf("finishing sent %s, want the same batch %s", second, first)
	}
	if _, err := service.Edit(t.Context(), edit); err == nil || !strings.Contains(err.Error(), "read them again") ||
		strings.Contains(err.Error(), "requestKey=") {
		t.Fatalf("a conflict = %v; want guidance to read again, not to retry", err)
	}
	if _, err := service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a"}); err == nil {
		t.Fatal("an edit without commands or a pending key was sent")
	}
}

// artifact_edit ops send intent, not coordinates: keys stay keys, refs become
// IDs, arranged objects carry the revisions this Run read, and the answer
// says where the work landed and names every new object by ref.
func TestWhiteboardDrawSendsIntentAndNamesWhatItPlaced(t *testing.T) {
	t.Parallel()
	calls := 0
	board := &whiteboardBoard{drawn: func(map[string]any) (int, string) {
		calls++
		if calls == 1 {
			return http.StatusBadGateway, `{"code":"server_unreachable","detail":"timed out"}`
		}
		return http.StatusOK, `{"mutation":{"sequence":20,"ids":{"n1":"` + whiteboardNewID + `"},"items":[` +
			`{"id":"` + whiteboardNewID + `","type":"sticky","text":"Idea","geometryRevision":1}],` +
			`"edges":[],"removed":[]},"bounds":{"x":100,"y":200,"width":240,"height":190},"placement":"view",` +
			`"preview":{"bounds":{"x":60,"y":160,"width":320,"height":270},` +
			`"mimeType":"image/png","data":"UE5H"}}`
	}}
	service := whiteboardChannelServer(t, board.route(t))
	if _, err := service.Read(t.Context(), ArtifactInspectInput{ArtifactID: "board-a"}); err != nil {
		t.Fatal(err)
	}
	draw := ArtifactEditInput{
		ArtifactID: "board-a", Near: "c0ffee00", Preview: true,
		Ops: []WhiteboardDrawOp{
			{Type: "notes", Items: []WhiteboardDrawNote{{Key: "n1", Text: "Idea"}}},
			{Type: "connect", From: "n1", To: "a3f9c2e1"},
			{Type: "arrange", Refs: []string{"a3f9c2e1", "c0ffee00"}, Align: "top"},
		},
	}
	_, err := service.Edit(t.Context(), draw)
	key, _ := board.draws[0]["requestKey"].(string)
	if err == nil || !strings.Contains(err.Error(), "requestKey="+key) {
		t.Fatalf("unknown outcome = %v; want the key to finish it with", err)
	}
	sent := board.draws[0]
	ops, _ := sent["ops"].([]any)
	connect := ops[1].(map[string]any)
	arrange := ops[2].(map[string]any)
	if sent["near"] != whiteboardInkID || connect["from"] != "n1" || connect["to"] != whiteboardNoteID {
		t.Fatalf("draw request = %+v; want keys kept and refs expanded", sent)
	}
	if refs, _ := arrange["refs"].([]any); len(refs) != 2 || refs[0] != whiteboardNoteID {
		t.Fatalf("arrange = %+v", arrange)
	}
	revisions, _ := sent["revisions"].([]any)
	if len(revisions) != 2 || revisions[0].(map[string]any)["revision"] != float64(2) ||
		revisions[1].(map[string]any)["revision"] != float64(1) {
		t.Fatalf("revisions = %+v; want the geometry revisions read", revisions)
	}
	reading, err := service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a", RequestKey: key})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(board.draws[0])
	second, _ := json.Marshal(board.draws[1])
	if !bytes.Equal(first, second) {
		t.Fatalf("finishing sent %s, want the same request %s", second, first)
	}
	for _, want := range []string{"Placed at 100,200 240x190, where the person you work for is looking.", `n1=5e6f7a8b sticky "Idea"`} {
		if !strings.Contains(reading.Text, want) {
			t.Fatalf("reading lacks %q:\n%s", want, reading.Text)
		}
	}
	if len(reading.Images) != 1 || string(reading.Images[0].Data) != "PNG" {
		t.Fatalf("images = %+v; want the preview", reading.Images)
	}
	if _, err := service.Edit(t.Context(), ArtifactEditInput{ArtifactID: "board-a", Ops: []WhiteboardDrawOp{
		{Type: "arrange", Refs: []string{"77de0a12", "a3f9c2e1"}, Align: "left"},
	}}); err == nil || !strings.Contains(err.Error(), "read 77de0a12 before arranging") || len(board.draws) != 2 {
		t.Fatalf("arranging an unread object = %v; want it refused before sending", err)
	}
}

// Aligning and then spacing the same objects sends each object's revision
// once: the Board takes at most 100.
func TestWhiteboardDrawSendsEachArrangedRevisionOnce(t *testing.T) {
	t.Parallel()
	board := &whiteboardBoard{drawn: func(map[string]any) (int, string) {
		return http.StatusOK, `{"mutation":{"sequence":21,"ids":{},"items":[],"edges":[],"removed":[]},"bounds":null}`
	}}
	service := whiteboardChannelServer(t, board.route(t))
	if _, err := service.Read(t.Context(), ArtifactInspectInput{ArtifactID: "board-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Edit(
		t.Context(),
		ArtifactEditInput{ArtifactID: "board-a", Near: "c0ffee00", Ops: []WhiteboardDrawOp{
			{Type: "arrange", Refs: []string{"a3f9c2e1", "c0ffee00"}, Align: "left"},
			{Type: "arrange", Refs: []string{"c0ffee00", "a3f9c2e1"}, Distribute: "vertical"},
		}},
	); err != nil {
		t.Fatal(err)
	}
	if revisions, _ := board.draws[0]["revisions"].([]any); len(revisions) != 2 {
		t.Fatalf("revisions = %+v; want one per object", revisions)
	}
}

// A flowchart's answer stays small: every new object by key and ref, no IDs.
func TestWhiteboardDrawAnswerStaysWithinItsBudget(t *testing.T) {
	t.Parallel()
	service := whiteboardChannelServer(t, http.NotFound)
	var result whiteboardMutation
	result.Sequence, result.IDs = 9, map[string]string{}
	for i := range 8 {
		id := uuid.NewString()
		result.IDs[fmt.Sprintf("step%d", i)] = id
		properties := fmt.Sprintf(`{"title":"Onboarding step %d","shape":"class"}`, i+1)
		result.Items = append(result.Items, whiteboardMutationItem{
			whiteboardRef: whiteboardRef{ID: id, Type: "shape", Properties: json.RawMessage(properties)},
		})
	}
	connect := func(draft, from, to string) {
		id := uuid.NewString()
		result.IDs[draft] = id
		result.Edges = append(
			result.Edges,
			whiteboardMutationEdge{ID: id, SourceID: from, TargetID: to, Revision: 1},
		)
	}
	for i := range 7 {
		connect(
			fmt.Sprintf("o1.e%d", i+1),
			result.IDs[fmt.Sprintf("step%d", i)],
			result.IDs[fmt.Sprintf("step%d", i+1)],
		)
	}
	// A long sequence's messages read as one line of refs.
	for i := range 40 {
		connect(fmt.Sprintf("o2.m%d", i+1), result.IDs["step0"], result.IDs["step1"])
	}
	summary := service.mutationSummary("board-a", result)
	if len(summary) > 1536 || regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-`).MatchString(summary) {
		t.Fatalf("summary is %d bytes:\n%s", len(summary), summary)
	}
	if !strings.Contains(summary, `shape:class "Onboarding step 1"`) ||
		!strings.Contains(summary, "47 connectors:") {
		t.Fatalf("summary does not name kinds or group connectors:\n%s", summary)
	}
}

// The draw tool's inputs mirror the Board's compose contract field for field,
// so nothing an Agent writes is silently dropped (this package cannot import
// the canvas package).
func TestWhiteboardDrawMirrorsTheComposeContract(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../../docs/api/spec/whiteboards.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Schemas map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for schema, value := range map[string]any{
		"BoardComposeOp": WhiteboardDrawOp{}, "BoardComposeNode": WhiteboardDrawNode{},
		"BoardComposeEdge": WhiteboardDrawEdge{}, "BoardComposeFragment": WhiteboardDrawFragment{},
		"BoardComposeBranch": WhiteboardDrawBranch{}, "BoardComposeNote": WhiteboardDrawNote{},
		"BoardComposePath": WhiteboardDrawPath{},
	} {
		want := spec.Schemas[schema].Properties
		if len(want) == 0 {
			t.Fatalf("schema %s has no properties", schema)
		}
		got := map[string]bool{}
		kind := reflect.TypeOf(value)
		for i := range kind.NumField() {
			name := strings.Split(kind.Field(i).Tag.Get("json"), ",")[0]
			got[name] = true
		}
		for name := range want {
			if !got[name] {
				t.Errorf("%s.%s is not in the draw tool", schema, name)
			}
		}
		for name := range got {
			if _, ok := want[name]; !ok {
				t.Errorf("the draw tool's %s is not %s.%s", kind.Name(), schema, name)
			}
		}
	}
}

// The edit tool names the fields the Board's operations take, so it can
// name an unknown one rather than pass on a bare 400.
func TestWhiteboardEditMirrorsTheOperationsContract(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../../../docs/api/spec/whiteboards.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Schemas map[string]struct {
			Properties map[string]json.RawMessage `json:"properties"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	geometry := reflect.TypeOf(whiteboardGeometry{})
	tagged := make([]string, geometry.NumField())
	for i := range tagged {
		tagged[i] = geometry.Field(i).Tag.Get("json")
	}
	for schema, fields := range map[string][]string{
		"BoardCommand": whiteboardCommandFields, "BoardGeometry": whiteboardGeometryFields,
		"BoardProperties": whiteboardPropertyFields,
	} {
		want := slices.Sorted(maps.Keys(spec.Schemas[schema].Properties))
		if got := slices.Sorted(slices.Values(fields)); !slices.Equal(got, want) {
			t.Errorf("%s fields = %v; the contract has %v", schema, got, want)
		}
		if schema == "BoardGeometry" && !slices.Equal(slices.Sorted(slices.Values(tagged)), want) {
			t.Errorf("whiteboardGeometry fields = %v; the contract has %v", tagged, want)
		}
	}
}
