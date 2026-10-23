//go:build zenith_minimal

package cloudrun

import (
	"context"
	"errors"
	"io"
)

// PublisherService preserves the adapter contract without linking the Google
// SDK. Minimal builds never acquire cloud credentials or publication authority.
type PublisherService struct{}

var _ Publisher = (*PublisherService)(nil)

var errPublicationDisabled = errors.New("Cloud Run publication is unavailable in the zenith_minimal build")

func NewPublisher(context.Context, Config) (*PublisherService, error) {
	return nil, errPublicationDisabled
}

func (*PublisherService) BindingID() string { return "" }

func (*PublisherService) Publish(context.Context, Request, io.Reader, Observer) (Result, error) {
	return Result{}, errPublicationDisabled
}

func (*PublisherService) Delete(context.Context, DeleteRequest) error {
	return errPublicationDisabled
}
