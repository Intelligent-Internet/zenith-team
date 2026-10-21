package task

import (
	"reflect"
	"testing"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
	"github.com/Intelligent-Internet/zenith-team/server/internal/events"
)

// TestChangedTaskFieldsReportsStatusOnly pins that a status change reports
// exactly one field. Completion is not a second fact about the row: a task in
// 'done' finished when it was last written, and who finished it is the
// activity stream's to say.
func TestChangedTaskFieldsReportsStatusOnly(t *testing.T) {
	t.Parallel()

	before := validResponse()
	after := before
	after.Status = StatusDone
	status := StatusDone
	got := changedTaskFields(before, after, UpdateInput{Status: &status})
	want := []Field{FieldStatus}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changedTaskFields() = %v, want %v", got, want)
	}
}

func TestServiceRequiresStoreAndBus(t *testing.T) {
	t.Parallel()

	if _, err := NewTaskService(nil, newTestBus(t), nil); err == nil {
		t.Fatal("NewTaskService(nil) error = nil")
	}
	if _, err := NewTaskService(&db.Store{}, nil, nil); err == nil {
		t.Fatal("NewTaskService(nil bus) error = nil")
	}
	if _, err := NewTaskService(&db.Store{}, newTestBus(t), nil); err != nil {
		t.Fatalf("NewTaskService() error = %v", err)
	}
}

func newTestBus(t *testing.T) *events.Bus {
	t.Helper()
	bus, err := events.NewBus(events.BusOptions{})
	if err != nil {
		t.Fatalf("events.NewBus() error = %v", err)
	}
	return bus
}
