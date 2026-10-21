//go:build !darwin

package daemon

import (
	"context"

	"github.com/Intelligent-Internet/zenith-team/server/pkg/agent"
)

type unsupportedAppPreviewBackend struct{}

func newPlatformAppPreviewBackend() appPreviewBackend { return unsupportedAppPreviewBackend{} }

func (unsupportedAppPreviewBackend) CheckSupported() error {
	return errAppPreviewPlatformUnsupported
}

func (unsupportedAppPreviewBackend) Launch(
	context.Context, appPreviewManifest, []string, *agent.Sandbox,
) ([]appPreviewTree, error) {
	return nil, errAppPreviewPlatformUnsupported
}

func (unsupportedAppPreviewBackend) Observe(
	context.Context, int, appPreviewTree,
) appPreviewEndpointObservation {
	return appPreviewEndpointObservation{Ownership: appPreviewOwnershipUnknown}
}

func (unsupportedAppPreviewBackend) Stop(context.Context, []appPreviewTree) error {
	return errAppPreviewPlatformUnsupported
}
