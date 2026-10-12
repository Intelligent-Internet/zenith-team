package handler

import (
	"encoding/json"
	"testing"
)

func TestTaskMetadataWirePresence(t *testing.T) {
	for _, raw := range []string{"null", "42", `"label"`, `[1]`, `[null]`} {
		if _, err := parseTaskLabels(json.RawMessage(raw)); err == nil {
			t.Errorf("labels %s accepted", raw)
		}
	}
	if labels, err := parseTaskLabels(nil); err != nil || labels != nil {
		t.Fatalf("omitted labels = %v, %v", labels, err)
	}
	if labels, err := parseTaskLabels(json.RawMessage("[]")); err != nil || labels == nil || len(labels) != 0 {
		t.Fatalf("clear labels = %v, %v", labels, err)
	}
	for _, raw := range []string{"42", `"2026-02-30"`, `"2026-9-11"`, `""`} {
		if _, err := parseTaskDate(json.RawMessage(raw)); err == nil {
			t.Errorf("date %s accepted", raw)
		}
	}
	if value, err := parseTaskDate(nil); err != nil || value.Present {
		t.Fatalf("omitted date = %+v %v", value, err)
	}
	if value, err := parseTaskDate(json.RawMessage("null")); err != nil || !value.Present || value.Value != nil {
		t.Fatalf("cleared date = %+v %v", value, err)
	}
}
