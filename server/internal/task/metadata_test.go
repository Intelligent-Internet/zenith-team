package task

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestTaskMetadataNormalizesLabelsAndValidatesCalendarDates(t *testing.T) {
	input := validCreateInput()
	input.Labels = []string{" Design ", "design", "Beta"}
	start, due := "2026-09-11", "2026-09-30"
	input.StartDate, input.DueDate = &start, &due
	got, err := normalizeCreateInput(input, callerID)
	if err != nil || !reflect.DeepEqual(got.Labels, []string{"Design", "Beta"}) {
		t.Fatalf("normalized metadata = %+v, %v", got, err)
	}
	for _, date := range []string{"", "2026-02-29", "2026-9-11", "2026-09-11T00:00:00Z", "0000-01-01"} {
		input.StartDate = &date
		if _, err := normalizeCreateInput(input, callerID); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("date %q error = %v", date, err)
		}
	}
	input.StartDate, input.DueDate = &due, &start
	if _, err := normalizeCreateInput(input, callerID); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("reversed dates error = %v", err)
	}
	input.StartDate, input.DueDate = nil, nil
	for _, labels := range [][]string{{" "}, {strings.Repeat("a", 49)}, {"line\nbreak"}, make([]string, 21)} {
		input.Labels = labels
		if _, err := normalizeCreateInput(input, callerID); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("labels %v error = %v", labels, err)
		}
	}
}

func TestTaskMetadataPatchDistinguishesOmissionAndClearing(t *testing.T) {
	start, due := "2026-09-11", "2026-09-30"
	current := validResponse()
	current.StartDate, current.DueDate, current.Labels = &start, &due, []string{"Design"}
	input := UpdateInput{
		ExpectedRevision: current.Revision,
		Labels:           []string{},
		StartDate:        DateChange{Present: true},
	}
	normalized, err := normalizeUpdateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	target := applyTaskUpdateTarget(current, normalized)
	if target.StartDate != nil || target.DueDate == nil || *target.DueDate != due || len(target.Labels) != 0 {
		t.Fatalf("target = %+v", target)
	}
	if fields := changedTaskFields(
		current,
		target,
		normalized,
	); !reflect.DeepEqual(
		fields,
		[]Field{FieldLabels, FieldStartDate},
	) {
		t.Fatalf("changed fields = %v", fields)
	}
}
