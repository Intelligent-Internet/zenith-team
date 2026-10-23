//go:build zenith_minimal

package cloudrun

import (
	"context"
	"testing"
)

func TestMinimalPublisherRefusesCloudAuthority(t *testing.T) {
	publisher, err := NewPublisher(context.Background(), Config{})
	if err == nil || publisher != nil {
		t.Fatal("minimal builds must refuse cloud publishing even if configured")
	}
	var disabled PublisherService
	if disabled.BindingID() != "" {
		t.Fatal("disabled publisher must not advertise authority")
	}
	if _, err := disabled.Publish(context.Background(), Request{}, nil, nil); err == nil {
		t.Fatal("disabled publisher accepted publication")
	}
	if err := disabled.Delete(context.Background(), DeleteRequest{}); err == nil {
		t.Fatal("disabled publisher accepted deletion")
	}
}
