package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/Intelligent-Internet/zenith-team/server/internal/execution"
	cloudrunadapter "github.com/Intelligent-Internet/zenith-team/server/internal/infra/cloudrun"
)

type publicationProviderCatalog struct {
	defaultKey execution.PublicationProviderKey
	providers  map[execution.PublicationProviderKey]execution.PublicationProvider
}

func newPublicationProviderCatalog(
	defaultProvider execution.PublicationProvider,
	providers ...execution.PublicationProvider,
) (*publicationProviderCatalog, error) {
	if defaultProvider == nil {
		return nil, errors.New("default publication provider is required")
	}
	all := append([]execution.PublicationProvider{defaultProvider}, providers...)
	byKey := make(map[execution.PublicationProviderKey]execution.PublicationProvider, len(all))
	for _, provider := range all {
		if provider == nil {
			return nil, errors.New("publication provider is required")
		}
		key := provider.Key()
		if !key.IsValid() {
			return nil, fmt.Errorf("invalid publication provider %q", key.Type)
		}
		if _, exists := byKey[key]; exists {
			return nil, fmt.Errorf("duplicate publication provider %q", key.Type)
		}
		byKey[key] = provider
	}
	return &publicationProviderCatalog{
		defaultKey: defaultProvider.Key(),
		providers:  byKey,
	}, nil
}

func (c *publicationProviderCatalog) Default() (execution.PublicationProvider, bool) {
	if c == nil {
		return nil, false
	}
	provider, ok := c.providers[c.defaultKey]
	return provider, ok
}

func (c *publicationProviderCatalog) Resolve(
	key execution.PublicationProviderKey,
) (execution.PublicationProvider, bool) {
	if c == nil {
		return nil, false
	}
	provider, ok := c.providers[key]
	return provider, ok
}

type cloudRunPublicationProvider struct {
	publisher *cloudrunadapter.PublisherService
	key       execution.PublicationProviderKey
}

func newCloudRunPublicationProvider(
	publisher *cloudrunadapter.PublisherService,
) (*cloudRunPublicationProvider, error) {
	if publisher == nil || publisher.BindingID() == "" {
		return nil, errors.New("Cloud Run publisher is required")
	}
	return &cloudRunPublicationProvider{
		publisher: publisher,
		key: execution.PublicationProviderKey{
			Type:      execution.PublicationProviderTypeCloudRun,
			BindingID: publisher.BindingID(),
		},
	}, nil
}

func (p *cloudRunPublicationProvider) Key() execution.PublicationProviderKey {
	if p == nil {
		return execution.PublicationProviderKey{}
	}
	return p.key
}

func (p *cloudRunPublicationProvider) Publish(
	ctx context.Context,
	request execution.PublicationProviderRequest,
	source io.Reader,
	observe execution.PublicationProviderObserver,
) (execution.PublicationProviderResult, error) {
	result, err := p.publisher.Publish(ctx, cloudrunadapter.Request{
		PublicationID: request.PublicationID,
		ServiceName:   request.ServiceName,
		Attempt:       request.Attempt,
		SourceSize:    request.SourceSize,
		SourceSHA256:  request.SourceSHA256,
	}, source, func(progressCtx context.Context, progress cloudrunadapter.Progress) error {
		if observe == nil {
			return nil
		}
		return observe(progressCtx, publicationProgressFromCloudRun(progress))
	})
	if err != nil {
		return execution.PublicationProviderResult{}, publicationErrorFromCloudRun(err)
	}
	return execution.PublicationProviderResult{
		PublicationProviderResources: execution.PublicationProviderResources{
			SourceRef: cloudRunSourceRef(
				result.SourceBucket,
				result.SourceObject,
				result.SourceGeneration,
			),
			SourceTemplate: string(result.Template),
			BuildRef:       result.BuildID,
			ArtifactRef:    result.ImageURI,
			ArtifactDigest: result.ImageDigest,
			ServiceRef:     result.ServiceRef,
		},
		ServiceURI: result.ServiceURI,
	}, nil
}

func (p *cloudRunPublicationProvider) Delete(
	ctx context.Context,
	request execution.PublicationProviderDeleteRequest,
) error {
	err := p.publisher.Delete(ctx, cloudrunadapter.DeleteRequest{
		PublicationID: request.PublicationID,
		ServiceName:   request.ServiceName,
	})
	return publicationErrorFromCloudRun(err)
}

func publicationProgressFromCloudRun(
	progress cloudrunadapter.Progress,
) execution.PublicationProviderProgress {
	stage := execution.PublicationProviderStage(progress.Stage)
	return execution.PublicationProviderProgress{
		Stage: stage,
		PublicationProviderResources: execution.PublicationProviderResources{
			SourceRef: cloudRunSourceRef(
				progress.SourceBucket,
				progress.SourceObject,
				progress.SourceGeneration,
			),
			SourceTemplate: string(progress.Template),
			BuildRef:       progress.BuildID,
			ArtifactRef:    progress.ImageURI,
			ArtifactDigest: progress.ImageDigest,
		},
	}
}

func cloudRunSourceRef(bucket, object string, generation int64) string {
	if bucket == "" || object == "" || generation < 1 {
		return ""
	}
	return "gs://" + bucket + "/" + object + "#" + strconv.FormatInt(generation, 10)
}

func publicationErrorFromCloudRun(err error) error {
	if err == nil {
		return nil
	}
	var failure *cloudrunadapter.Failure
	if !errors.As(err, &failure) {
		return err
	}
	code := execution.PublicationFailureProviderUnavailable
	switch failure.Code {
	case cloudrunadapter.FailureSourceInvalid:
		code = execution.PublicationFailureSourceInvalid
	case cloudrunadapter.FailureBuildFailed:
		code = execution.PublicationFailureBuildFailed
	case cloudrunadapter.FailureDeployFailed:
		code = execution.PublicationFailureDeployFailed
	case cloudrunadapter.FailureProviderUnavailable:
	}
	return execution.NewPublicationProviderFailure(code, failure.Retryable, err)
}

var (
	_ execution.PublicationProvider        = (*cloudRunPublicationProvider)(nil)
	_ execution.PublicationProviderCatalog = (*publicationProviderCatalog)(nil)
)
