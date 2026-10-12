package canvas

import (
	"errors"
	"strings"
	"testing"
)

// The Board draws the shapes of common software diagrams. Only boxes with
// compartments carry sections, and only a fragment carries the dividers
// between its operands.
func TestBoardPropertiesAcceptDiagramShapes(t *testing.T) {
	t.Parallel()
	for _, shape := range []BoardShapeType{
		BoardShapeTypeRectangle, BoardShapeTypeEllipse, BoardShapeTypeDiamond, BoardShapeTypeTriangle,
		BoardShapeTypeRounded, BoardShapeTypeTerminal, BoardShapeTypeActor, BoardShapeTypeDatabase,
		BoardShapeTypeQueue, BoardShapeTypeCloud, BoardShapeTypeComponent, BoardShapeTypeDocument,
		BoardShapeTypeNote, BoardShapeTypeLifeline, BoardShapeTypeClass, BoardShapeTypeEntity,
		BoardShapeTypeFragment,
	} {
		if err := normalizeBoardProperties(BoardProperties{Shape: shape}); err != nil {
			t.Errorf("shape %s = %v", shape, err)
		}
	}
	for name, tt := range map[string]struct {
		props BoardProperties
		want  error
	}{
		"unknown shape":          {BoardProperties{Shape: "hexagon"}, ErrBoardInvalid},
		"class":                  {BoardProperties{Shape: BoardShapeTypeClass, Sections: []string{"- id: UUID", "+ save()"}}, nil},
		"entity":                 {BoardProperties{Shape: BoardShapeTypeEntity, Sections: []string{"PK id uuid"}}, nil},
		"sections on a database": {BoardProperties{Shape: BoardShapeTypeDatabase, Sections: []string{"x"}}, ErrBoardInvalid},
		"too many sections":      {BoardProperties{Shape: BoardShapeTypeClass, Sections: []string{"", "", "", "", ""}}, ErrBoardInvalid},
		"a long section":         {BoardProperties{Shape: BoardShapeTypeClass, Sections: []string{strings.Repeat("x", 2001)}}, ErrBoardInvalid},
		// Limits count characters, as the API contract and the editor do, so
		// text in Vietnamese fits as much as text in English.
		"a full section in Vietnamese": {
			BoardProperties{Shape: BoardShapeTypeClass, Sections: []string{strings.Repeat("ệ", 2000)}}, nil,
		},
		"a full title in Vietnamese":  {BoardProperties{Title: strings.Repeat("ệ", 1000)}, nil},
		"a title past its characters": {BoardProperties{Title: strings.Repeat("e", 1001)}, ErrBoardInvalid},
		// A lifeline's head is a box, or an actor's figure over its name.
		"an actor lifeline": {BoardProperties{Shape: BoardShapeTypeLifeline, Head: BoardHeadTypeActor}, nil},
		"a head on a class": {BoardProperties{Shape: BoardShapeTypeClass, Head: BoardHeadTypeActor}, ErrBoardInvalid},
		"an unknown head":   {BoardProperties{Shape: BoardShapeTypeLifeline, Head: "robot"}, ErrBoardInvalid},
		"fragment operands": {
			BoardProperties{Shape: BoardShapeTypeFragment, Sections: []string{"[ok]", "[else]"}, Dividers: []float64{80}}, nil,
		},
		"dividers on a class": {
			BoardProperties{Shape: BoardShapeTypeClass, Sections: []string{"a", "b"}, Dividers: []float64{30}}, ErrBoardInvalid,
		},
		"a divider per operand": {
			BoardProperties{Shape: BoardShapeTypeFragment, Sections: []string{"[a]", "[b]"}, Dividers: []float64{40, 80}},
			ErrBoardInvalid,
		},
		"dividers out of order": {
			BoardProperties{Shape: BoardShapeTypeFragment, Sections: []string{"[a]", "[b]", "[c]"}, Dividers: []float64{80, 40}},
			ErrBoardInvalid,
		},
	} {
		if err := normalizeBoardProperties(tt.props); !errors.Is(err, tt.want) {
			t.Errorf("%s = %v, want %v", name, err, tt.want)
		}
	}
}
