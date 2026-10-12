package canvas

import (
	"math"
	"slices"
	"unicode/utf8"
)

// BoardShapeType is a shape item's kind: basic shapes, and the notation of
// common software diagrams (architecture, use case, sequence, class and
// entity relationship diagrams).
type BoardShapeType string

const (
	BoardShapeTypeRectangle BoardShapeType = "rectangle"
	BoardShapeTypeEllipse   BoardShapeType = "ellipse"
	BoardShapeTypeDiamond   BoardShapeType = "diamond"
	BoardShapeTypeTriangle  BoardShapeType = "triangle"
	// BoardShapeTypeRounded is a rectangle with rounded corners: a process or
	// a service.
	BoardShapeTypeRounded BoardShapeType = "rounded"
	// BoardShapeTypeTerminal is a pill: a flow's start or end.
	BoardShapeTypeTerminal BoardShapeType = "terminal"
	// BoardShapeTypeActor is a stick figure with its name below: a person or
	// an outside system.
	BoardShapeTypeActor BoardShapeType = "actor"
	// BoardShapeTypeDatabase is an upright cylinder.
	BoardShapeTypeDatabase BoardShapeType = "database"
	// BoardShapeTypeQueue is a cylinder on its side: a queue or a topic.
	BoardShapeTypeQueue BoardShapeType = "queue"
	BoardShapeTypeCloud BoardShapeType = "cloud"
	// BoardShapeTypeComponent is a box with two tabs on its left side.
	BoardShapeTypeComponent BoardShapeType = "component"
	// BoardShapeTypeDocument is a box with a wavy bottom.
	BoardShapeTypeDocument BoardShapeType = "document"
	// BoardShapeTypeNote is a box with a folded corner.
	BoardShapeTypeNote BoardShapeType = "note"
	// BoardShapeTypeLifeline is a sequence diagram participant: a header box
	// over a dashed line down the middle.
	BoardShapeTypeLifeline BoardShapeType = "lifeline"
	// BoardShapeTypeClass is a class box: the name over attribute and method
	// compartments.
	BoardShapeTypeClass BoardShapeType = "class"
	// BoardShapeTypeEntity is a database table: the name over its columns.
	BoardShapeTypeEntity BoardShapeType = "entity"
	// BoardShapeTypeFragment is a sequence diagram's combined fragment (alt,
	// loop, opt…): a box with its operator in a corner tag and a guard for
	// each operand, operands split by dashed dividers.
	BoardShapeTypeFragment BoardShapeType = "fragment"
)

// BoardHeadType is how a lifeline's head is drawn. Empty is a box.
type BoardHeadType string

// BoardHeadTypeActor is an actor's figure over its name: a person or an
// outside system taking part in a sequence.
const BoardHeadTypeActor BoardHeadType = "actor"

var boardShapeTypes = []BoardShapeType{
	BoardShapeTypeRectangle, BoardShapeTypeEllipse, BoardShapeTypeDiamond, BoardShapeTypeTriangle,
	BoardShapeTypeRounded, BoardShapeTypeTerminal, BoardShapeTypeActor, BoardShapeTypeDatabase,
	BoardShapeTypeQueue, BoardShapeTypeCloud, BoardShapeTypeComponent, BoardShapeTypeDocument,
	BoardShapeTypeNote, BoardShapeTypeLifeline, BoardShapeTypeClass, BoardShapeTypeEntity,
	BoardShapeTypeFragment,
}

// boardShapeMaxSections bounds a class's compartments and a fragment's
// operands.
const boardShapeMaxSections = 4

// compartmented reports a kind whose text lies in sections below its title.
func (s BoardShapeType) compartmented() bool {
	return s == BoardShapeTypeClass || s == BoardShapeTypeEntity || s == BoardShapeTypeFragment
}

// normalizeBoardShape checks a shape's kind, and that only compartmented
// kinds carry sections and only a fragment the dividers between its
// operands: one fewer than its sections, increasing, below its top.
func normalizeBoardShape(p BoardProperties) error {
	if p.Shape != "" && !slices.Contains(boardShapeTypes, p.Shape) {
		return boardInvalid("shape %q is not a Board shape", p.Shape)
	}
	if len(p.Sections) > 0 && !p.Shape.compartmented() {
		return boardInvalid("sections belong to class, entity and fragment shapes")
	}
	if len(p.Sections) > boardShapeMaxSections {
		return boardInvalid("a shape has at most %d sections", boardShapeMaxSections)
	}
	for _, section := range p.Sections {
		// Characters, as the API contract and the editor count them.
		if utf8.RuneCountInString(section) > 2000 {
			return boardInvalid("a section is at most 2000 characters")
		}
	}
	switch {
	case p.Head != "" && p.Head != BoardHeadTypeActor:
		return boardInvalid("head %q is not actor", p.Head)
	case p.Head != "" && p.Shape != BoardShapeTypeLifeline:
		return boardInvalid("head belongs to a lifeline")
	}
	if len(p.Dividers) == 0 {
		return nil
	}
	if p.Shape != BoardShapeTypeFragment || len(p.Dividers) != len(p.Sections)-1 {
		return boardInvalid("dividers belong to a fragment, one fewer than its sections")
	}
	previous := 0.0
	for _, divider := range p.Dividers {
		if math.IsNaN(divider) || divider <= previous || divider > boardEdgeMaxAnchor {
			return boardInvalid("a fragment's dividers lie below its top, each below the one before")
		}
		previous = divider
	}
	return nil
}
