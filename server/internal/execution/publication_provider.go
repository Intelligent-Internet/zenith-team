package execution

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
)

type PublicationProviderType string

const PublicationProviderTypeCloudRun PublicationProviderType = "cloud_run"

type PublicationProviderKey struct {
	Type      PublicationProviderType
	BindingID string
}

func (k PublicationProviderKey) IsValid() bool {
	return publicationProviderTypePattern.MatchString(string(k.Type)) &&
		publicationBindingIDPattern.MatchString(k.BindingID)
}

type PublicationProviderStage string

const (
	PublicationProviderStageBuilding  PublicationProviderStage = "building"
	PublicationProviderStageDeploying PublicationProviderStage = "deploying"
)

type PublicationProviderRequest struct {
	PublicationID string
	ServiceName   string
	Attempt       int
	SourceSize    int64
	SourceSHA256  []byte
}

type PublicationProviderResources struct {
	SourceRef      string
	SourceTemplate string
	BuildRef       string
	ArtifactRef    string
	ArtifactDigest string
	ServiceRef     string
}

type PublicationProviderProgress struct {
	Stage PublicationProviderStage
	PublicationProviderResources
}

type PublicationProviderResult struct {
	PublicationProviderResources
	ServiceURI string
}

type PublicationProviderDeleteRequest struct {
	PublicationID string
	ServiceName   string
	Resources     PublicationProviderResources
}

type PublicationProviderObserver func(context.Context, PublicationProviderProgress) error

type PublicationProvider interface {
	Key() PublicationProviderKey
	Publish(
		context.Context,
		PublicationProviderRequest,
		io.Reader,
		PublicationProviderObserver,
	) (PublicationProviderResult, error)
	Delete(context.Context, PublicationProviderDeleteRequest) error
}

type PublicationProviderCatalog interface {
	Default() (PublicationProvider, bool)
	Resolve(PublicationProviderKey) (PublicationProvider, bool)
}

type PublicationProviderFailure struct {
	Code      PublicationFailureCode
	Retryable bool
	err       error
}

func NewPublicationProviderFailure(
	code PublicationFailureCode,
	retryable bool,
	err error,
) *PublicationProviderFailure {
	if publicationFailureDetail(code) == "" {
		code = PublicationFailureProviderUnavailable
	}
	if err == nil {
		err = errors.New("publication provider failed")
	}
	return &PublicationProviderFailure{Code: code, Retryable: retryable, err: err}
}

func (e *PublicationProviderFailure) Error() string {
	if e == nil {
		return "publication provider failed"
	}
	return fmt.Sprintf("publication provider %s: %v", e.Code, e.err)
}

func (e *PublicationProviderFailure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

var (
	publicationProviderTypePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	publicationBindingIDPattern    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func validPublicationProviderKey(key PublicationProviderKey) bool {
	return key.IsValid()
}

func resolvePublicationProvider(
	catalog PublicationProviderCatalog,
	key PublicationProviderKey,
) (PublicationProvider, bool) {
	if catalog == nil || !validPublicationProviderKey(key) {
		return nil, false
	}
	provider, ok := catalog.Resolve(key)
	return provider, ok && provider != nil && provider.Key() == key
}

func defaultPublicationProvider(
	catalog PublicationProviderCatalog,
) (PublicationProviderKey, bool) {
	if catalog == nil {
		return PublicationProviderKey{}, false
	}
	provider, ok := catalog.Default()
	if !ok || provider == nil {
		return PublicationProviderKey{}, false
	}
	key := provider.Key()
	_, resolvedOK := resolvePublicationProvider(catalog, key)
	return key, resolvedOK
}
