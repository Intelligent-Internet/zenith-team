// Package cloudrun deploys provider-neutral source streams through Google
// Cloud Storage, Cloud Build, Artifact Registry, and Cloud Run. It is isolated
// beneath infra because Google API types and credentials must not enter a
// Zenith Team business domain.
package cloudrun

import (
	"context"
	"fmt"
	"io"
)

type Stage string

const (
	StageBuilding  Stage = "building"
	StageDeploying Stage = "deploying"
)

type Template string

const (
	TemplateDockerfile Template = "dockerfile"
	TemplateNextJS     Template = "nextjs"
	TemplateVite       Template = "vite"
	TemplateCRA        Template = "create_react_app"
	TemplateStatic     Template = "static_html"
	TemplateBuildpack  Template = "buildpack"
)

type BuildType string

const (
	BuildDocker BuildType = "docker"
	BuildPack   BuildType = "buildpack"
)

type FailureCode string

const (
	FailureSourceInvalid       FailureCode = "source_invalid"
	FailureBuildFailed         FailureCode = "build_failed"
	FailureDeployFailed        FailureCode = "deploy_failed"
	FailureProviderUnavailable FailureCode = "provider_unavailable"
)

type Failure struct {
	Code      FailureCode
	Retryable bool
	err       error
}

func (e *Failure) Error() string {
	if e == nil {
		return "Cloud Run publication failed"
	}
	return fmt.Sprintf("Cloud Run publication %s: %v", e.Code, e.err)
}

func (e *Failure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

type Target struct {
	ProjectID             string
	Region                string
	SourceBucket          string
	ArtifactRegistry      string
	BuildServiceAccount   string
	RuntimeServiceAccount string
}

func (t Target) IsValid() bool {
	return validTarget(t)
}

type Request struct {
	PublicationID string
	ServiceName   string
	Attempt       int
	SourceSize    int64
	SourceSHA256  []byte
}

type DeleteRequest struct {
	PublicationID string
	ServiceName   string
}

type Progress struct {
	Stage            Stage
	SourceBucket     string
	SourceObject     string
	SourceGeneration int64
	Template         Template
	BuildID          string
	ImageURI         string
	ImageDigest      string
}

type Result struct {
	SourceBucket     string
	SourceObject     string
	SourceGeneration int64
	Template         Template
	BuildID          string
	ImageURI         string
	ImageDigest      string
	ServiceRef       string
	ServiceURI       string
}

type Observer func(context.Context, Progress) error

type Publisher interface {
	Publish(context.Context, Request, io.Reader, Observer) (Result, error)
	Delete(context.Context, DeleteRequest) error
}

func sourceFailure(err error) error {
	return &Failure{Code: FailureSourceInvalid, err: err}
}

func providerFailure(code FailureCode, retryable bool, err error) error {
	return &Failure{Code: code, Retryable: retryable, err: err}
}
