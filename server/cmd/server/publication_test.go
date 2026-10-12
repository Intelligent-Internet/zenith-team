package main

import (
	"context"
	"io"
	"testing"

	"boardly.dev/boardly/server/internal/execution"
)

func TestPublicationProviderCatalogSelectsDefaultAndResolvesExactBindings(t *testing.T) {
	t.Parallel()

	cloudRun := stubPublicationProvider{key: execution.PublicationProviderKey{
		Type:      execution.PublicationProviderTypeCloudRun,
		BindingID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	koyeb := stubPublicationProvider{key: execution.PublicationProviderKey{
		Type:      execution.PublicationProviderType("koyeb"),
		BindingID: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}
	catalog, err := newPublicationProviderCatalog(cloudRun, koyeb)
	if err != nil {
		t.Fatalf("newPublicationProviderCatalog() error = %v", err)
	}
	if provider, ok := catalog.Default(); !ok || provider.Key() != cloudRun.key {
		t.Fatalf("Default() = %+v, %t", provider, ok)
	}
	if provider, ok := catalog.Resolve(koyeb.key); !ok || provider.Key() != koyeb.key {
		t.Fatalf("Resolve(Koyeb) = %+v, %t", provider, ok)
	}
	if _, err := newPublicationProviderCatalog(cloudRun, cloudRun); err == nil {
		t.Fatal("newPublicationProviderCatalog(duplicate) error = nil")
	}
}

type stubPublicationProvider struct {
	key execution.PublicationProviderKey
}

func (p stubPublicationProvider) Key() execution.PublicationProviderKey {
	return p.key
}

func (stubPublicationProvider) Publish(
	context.Context,
	execution.PublicationProviderRequest,
	io.Reader,
	execution.PublicationProviderObserver,
) (execution.PublicationProviderResult, error) {
	return execution.PublicationProviderResult{}, nil
}

func (stubPublicationProvider) Delete(
	context.Context,
	execution.PublicationProviderDeleteRequest,
) error {
	return nil
}

var _ execution.PublicationProvider = stubPublicationProvider{}
