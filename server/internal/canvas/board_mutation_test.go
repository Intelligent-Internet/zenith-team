package canvas

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/events"
	"boardly.dev/boardly/server/pkg/protocol"
)

func TestBoardBatchValidatesItemTypesAndFilePlacements(t *testing.T) {
	t.Parallel()
	file := uuid.New()
	for _, tt := range []struct {
		name, itemType string
		attachment     *uuid.UUID
		want           error
	}{
		{name: "removed link", itemType: "link", want: ErrBoardInvalid},
		{name: "file without attachment", itemType: "file", want: ErrBoardInvalid},
		{name: "image without attachment", itemType: "image", want: ErrBoardInvalid},
		{name: "sticky with attachment", itemType: "sticky", attachment: &file, want: ErrBoardInvalid},
		{name: "file", itemType: "file", attachment: &file},
		{name: "image", itemType: "image", attachment: &file},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := normalizeBoardBatch(BoardBatch{
				RequestKey: uuid.New(),
				Commands: []BoardCommand{{
					Type: "create_item", DraftID: "placed", ItemType: tt.itemType,
					Geometry:   &BoardGeometry{Width: 240, Height: 190},
					Properties: &BoardProperties{AttachmentID: tt.attachment},
				}},
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("create %s = %v, want %v", tt.itemType, err, tt.want)
			}
		})
	}
}

func TestBoardBatchAllowsLargeSelections(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		commands int
		want     error
	}{
		{commands: 1000},
		{commands: 1001, want: ErrBoardInvalid},
	} {
		commands := make([]BoardCommand, tt.commands)
		for i := range commands {
			id := uuid.New()
			commands[i] = BoardCommand{
				Type: "geometry", ID: &id, ExpectedRevision: 1,
				Geometry: &BoardGeometry{X: float64(i), Width: 240, Height: 190},
			}
		}
		if err := normalizeBoardBatch(BoardBatch{RequestKey: uuid.New(), Commands: commands}); !errors.Is(
			err,
			tt.want,
		) {
			t.Fatalf("%d commands = %v, want %v", tt.commands, err, tt.want)
		}
	}
}

// An Agent reads swatches by name and writes them back so; a hex value
// stays as sent and anything else is still refused.
func TestBoardBatchStoresSwatchNamesAsTheirValues(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	named := &BoardProperties{Color: "Blue", Fill: "pink"}
	sent := &BoardProperties{Color: "#4C6FFF", Fill: "transparent"}
	unknown := &BoardProperties{Fill: "mauve"}
	input := BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{
		{Type: "content", ID: &id, ExpectedRevision: 1, Properties: named},
		{Type: "content", ID: &id, ExpectedRevision: 1, Properties: sent},
	}}
	boardSwatchHexes(input)
	if named.Color != "#4c6fff" || named.Fill != "#fce1eb" || sent.Color != "#4C6FFF" || sent.Fill != "#00000000" {
		t.Fatalf("swatches = %+v, %+v; want the ink and fill values", *named, *sent)
	}
	if err := normalizeBoardBatch(input); err != nil {
		t.Fatal(err)
	}
	input.Commands = []BoardCommand{{Type: "content", ID: &id, ExpectedRevision: 1, Properties: unknown}}
	boardSwatchHexes(input)
	if err := normalizeBoardBatch(input); !errors.Is(err, ErrBoardInvalid) {
		t.Fatalf("fill mauve = %v, want refused", err)
	}
}

// A refused batch names the command and why, so an Agent can correct it.
func TestBoardBatchRefusalsNameTheCommandAndWhy(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	valid := BoardCommand{Type: "content", ID: &id, ExpectedRevision: 1, Properties: &BoardProperties{}}
	for want, command := range map[string]BoardCommand{
		"commands[1] (paint): type is one of create_item": {Type: "paint"},
		"commands[1] (content): properties are required":  {Type: "content", ID: &id, ExpectedRevision: 1},
		"commands[1] (content): fill is #rrggbb, #rrggbbaa or a swatch: blue, green, none, pink, yellow": {
			Type: "content", ID: &id, ExpectedRevision: 1, Properties: &BoardProperties{Fill: "mauve"},
		},
		"commands[1] (geometry): geometry lies within": {
			Type: "geometry", ID: &id, ExpectedRevision: 1, Geometry: &BoardGeometry{},
		},
		"commands[1] (create_item): only a sticky or text item has text": {
			Type: "create_item", DraftID: "box", ItemType: "shape", Text: "Orders",
			Geometry: &BoardGeometry{Width: 200, Height: 120},
		},
		"commands[1] (update_edge): route is flow, straight, elbow, message": {
			Type: "update_edge", ID: &id, ExpectedRevision: 1, Route: "curved",
		},
	} {
		err := normalizeBoardBatch(BoardBatch{RequestKey: uuid.New(), Commands: []BoardCommand{valid, command}})
		var invalid BoardInvalidError
		if !errors.As(err, &invalid) || !strings.HasPrefix(invalid.Reason, want) {
			t.Errorf("%s = %v; want the reason %q", command.Type, err, want)
		}
	}
}

func TestBoardEventActorCarriesRunAndAuthorizer(t *testing.T) {
	t.Parallel()
	member, agent, run := uuid.New(), uuid.New(), uuid.New()
	human := boardEventActor(auth.UserPrincipal{UserID: member}, BoardViewer{
		Actor: protocol.ActorRef{ID: member, Kind: protocol.ActorKindUser},
	})
	if human != events.UserActor(member.String()) {
		t.Fatalf("member actor = %+v", human)
	}
	got := boardEventActor(auth.UserPrincipal{UserID: member}, BoardViewer{
		Actor: protocol.ActorRef{ID: agent, Kind: protocol.ActorKindAgent},
		RunID: &run,
	})
	if got != events.RunActor(agent.String(), run.String(), member.String()) || !got.Valid() {
		t.Fatalf("agent actor = %+v, want Run actor for the authorizer", got)
	}
}

func TestBoardStrokePointsAreQuantized(t *testing.T) {
	t.Parallel()
	got := boardStrokePoints([]BoardPoint{
		{X: 12.345678, Y: -0.04999, Pressure: 0.123456},
		{X: 1e6, Y: 7.25},
	})
	want := []BoardPoint{{X: 12.3, Y: 0, Pressure: 0.12}, {X: 1e6, Y: 7.3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("points = %+v, want %+v", got, want)
	}
	raw, err := json.Marshal(got)
	if err != nil || string(raw) != `[{"x":12.3,"y":0,"pressure":0.12},{"x":1000000,"y":7.3}]` {
		t.Fatalf("encoded = %s, %v", raw, err)
	}
}
