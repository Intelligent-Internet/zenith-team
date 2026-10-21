package attachment

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Intelligent-Internet/zenith-team/server/internal/db"
)

func TestNewAttachmentServiceRejectsInvalidDependenciesAndOptions(t *testing.T) {
	objects := newAttachmentObjectStorageFake()

	bus := attachmentTestBus(t)
	for name, options := range map[string]ServiceOptions{
		"nil store": {Bus: bus, Objects: objects},
		"nil bus": {
			Store: &db.Store{}, Objects: objects,
		},
		"nil object store": {
			Store: &db.Store{}, Bus: bus,
		},
		"short pending TTL": {
			Store: &db.Store{}, Bus: bus, Objects: objects,
			PendingTTL: minimumPendingTTL - time.Nanosecond,
		},
		"long pending TTL": {
			Store: &db.Store{}, Bus: bus, Objects: objects,
			PendingTTL: maximumPendingTTL + time.Nanosecond,
		},
	} {
		t.Run(name, func(t *testing.T) {
			service, err := NewAttachmentService(options)
			if service != nil || !errors.Is(err, ErrInvalidServiceConfiguration) {
				t.Fatalf("NewAttachmentService() = %#v, %v; want configuration error", service, err)
			}
		})
	}

	var nilStore *db.Store
	if service, err := NewAttachmentService(ServiceOptions{
		Store: nilStore, Bus: bus, Objects: objects,
	}); service != nil || !errors.Is(err, ErrInvalidServiceConfiguration) {
		t.Fatalf("NewAttachmentService(typed nil store) = %#v, %v", service, err)
	}
	var nilObjects *attachmentObjectStorageFake
	if service, err := NewAttachmentService(ServiceOptions{
		Store: &db.Store{}, Bus: bus, Objects: nilObjects,
	}); service != nil || !errors.Is(err, ErrInvalidServiceConfiguration) {
		t.Fatalf("NewAttachmentService(typed nil objects) = %#v, %v", service, err)
	}
}

func TestInitiateRejectsNilContentReader(t *testing.T) {
	t.Parallel()

	service, _ := newAttachmentUnitService(t)
	input := validAttachmentInitiateInput(nil)
	if result, err := service.Initiate(context.Background(), input, nil); result != (InitiateResult{}) ||
		!errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Initiate(nil reader) = %#v, %v", result, err)
	}
}

// newAttachmentUnitService builds DefaultAttachmentService exactly as
// production does but on a zero-value store, so calls that must fail before
// any query still can.
func newAttachmentUnitService(t *testing.T) (*DefaultAttachmentService, *attachmentObjectStorageFake) {
	t.Helper()
	objects := newAttachmentObjectStorageFake()
	service, err := NewAttachmentService(ServiceOptions{
		Store: &db.Store{}, Bus: attachmentTestBus(t), Objects: objects,
	})
	if err != nil {
		t.Fatalf("NewAttachmentService() error = %v", err)
	}
	return service, objects
}
