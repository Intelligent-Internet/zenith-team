package conversation

import (
	"testing"

	"github.com/google/uuid"
)

func TestMessageConnectorReferenceRanges(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, text string
		start, end int
		want       bool
	}{
		{"ordinary", "Use $Gmail", 4, 10, true},
		{"unicode", "😀 $Gmail", 3, 9, true},
		{"code", "`$Gmail`", 1, 7, false},
		{"link", "[$Gmail](https://example.com)", 1, 7, false},
		{"escaped", `\$Gmail`, 1, 7, false},
		{"wrong trigger", "@Gmail", 0, 6, false},
		{"out of bounds", "$Gmail", 0, 9, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateMessageConnectorReferences(
				test.text,
				[]ConnectorReference{{ConnectorID: uuid.New(), Start: test.start, End: test.end}},
				nil,
			)
			if (err == nil) != test.want {
				t.Fatalf("valid = %v, want %v: %v", err == nil, test.want, err)
			}
		})
	}
	if err := validateMessageConnectorReferences(
		"$Gmail",
		[]ConnectorReference{{ConnectorID: uuid.New(), End: 6}},
		[]SkillReference{{SkillID: uuid.New(), VersionID: uuid.New(), End: 6}},
	); err == nil {
		t.Fatal("overlapping skill and connector accepted")
	}
	if err := validateMessageConnectorReferences("$Gmail", []ConnectorReference{{End: 6}}, nil); err == nil {
		t.Fatal("nil connector accepted")
	}
}
