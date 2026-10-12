package cloudrun

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	artifactapi "google.golang.org/api/artifactregistry/v1"
	cloudbuildapi "google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/googleapi"
	runapi "google.golang.org/api/run/v2"
	storageapi "google.golang.org/api/storage/v1"
	"google.golang.org/grpc/codes"
)

const (
	defaultPollInterval     = 2 * time.Second
	maximumPublishDuration  = 25 * time.Minute
	maximumDeleteDuration   = 5 * time.Minute
	cloudBuildTimeout       = 15 * time.Minute
	cloudBuildMachineType   = "E2_HIGHCPU_8"
	cloudBuildDockerBuilder = "gcr.io/cloud-builders/docker"
	cloudBuildPackBuilder   = "gcr.io/k8s-skaffold/pack"
	cloudBuildBuildpack     = "gcr.io/buildpacks/builder:google-22"
)

var (
	serviceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}[a-z0-9]$`)
	projectIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	regionPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9][0-9]$`)
	bucketPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$`)
	registryPattern    = regexp.MustCompile(
		`^[a-z][a-z0-9-]{0,30}[a-z0-9][0-9]-docker\.pkg\.dev/[a-z][a-z0-9-]{4,28}[a-z0-9]/[a-z0-9][a-z0-9._-]{1,126}$`,
	)
	serviceAccountPattern = regexp.MustCompile(
		`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$`,
	)
	publicationIDPattern = regexp.MustCompile(
		`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`,
	)
)

type Config struct {
	ProjectID             string
	Region                string
	SourceBucket          string
	ArtifactRegistry      string
	BuildServiceAccount   string
	RuntimeServiceAccount string
	MaxInstances          int64
	MinInstances          int64
	Memory                string
	CPU                   string
	Timeout               time.Duration
	Concurrency           int64
}

type PublisherService struct {
	config        Config
	storage       *storageapi.Service
	build         *cloudbuildapi.Service
	artifact      *artifactapi.Service
	run           *runapi.Service
	pollInterval  time.Duration
	deleteTimeout time.Duration
}

var _ Publisher = (*PublisherService)(nil)

func NewPublisher(ctx context.Context, config Config) (*PublisherService, error) {
	if ctx == nil || !validConfig(config) {
		return nil, errors.New("invalid Cloud Run publisher configuration")
	}
	storageService, err := storageapi.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("construct Google Cloud Storage client: %w", err)
	}
	buildService, err := cloudbuildapi.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("construct Google Cloud Build client: %w", err)
	}
	artifactService, err := artifactapi.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("construct Google Artifact Registry client: %w", err)
	}
	runService, err := runapi.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("construct Google Cloud Run client: %w", err)
	}
	return &PublisherService{
		config: config, storage: storageService, build: buildService, artifact: artifactService,
		run:          runService,
		pollInterval: defaultPollInterval,
	}, nil
}

func (p *PublisherService) BindingID() string {
	if p == nil {
		return ""
	}
	target := targetFromConfig(p.config)
	digest := sha256.Sum256([]byte(strings.Join([]string{
		"cloud_run",
		target.ProjectID,
		target.Region,
		target.SourceBucket,
		target.ArtifactRegistry,
	}, "\x00")))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func validConfig(config Config) bool {
	return validTarget(targetFromConfig(config)) && config.MaxInstances >= 1 &&
		config.MinInstances >= 0 && config.MinInstances <= config.MaxInstances &&
		config.Memory != "" && config.CPU != "" && config.Timeout > 0 &&
		config.Concurrency >= 1
}

func targetFromConfig(config Config) Target {
	return Target{
		ProjectID:             config.ProjectID,
		Region:                config.Region,
		SourceBucket:          config.SourceBucket,
		ArtifactRegistry:      config.ArtifactRegistry,
		BuildServiceAccount:   config.BuildServiceAccount,
		RuntimeServiceAccount: config.RuntimeServiceAccount,
	}
}

func validTarget(target Target) bool {
	_, registryMatchesTarget := artifactRepository(target)
	return projectIDPattern.MatchString(target.ProjectID) &&
		regionPattern.MatchString(target.Region) &&
		bucketPattern.MatchString(target.SourceBucket) &&
		!strings.Contains(target.SourceBucket, "..") &&
		!strings.HasPrefix(target.SourceBucket, "goog") &&
		!strings.Contains(target.SourceBucket, "google") &&
		registryPattern.MatchString(target.ArtifactRegistry) &&
		registryMatchesTarget &&
		serviceAccountPattern.MatchString(target.BuildServiceAccount) &&
		serviceAccountPattern.MatchString(target.RuntimeServiceAccount)
}

func artifactRepository(target Target) (string, bool) {
	prefix := target.Region + "-docker.pkg.dev/" + target.ProjectID + "/"
	repository, ok := strings.CutPrefix(target.ArtifactRegistry, prefix)
	return repository, ok && repository != "" && !strings.Contains(repository, "/")
}

func (p *PublisherService) Publish(
	ctx context.Context,
	request Request,
	source io.Reader,
	observe Observer,
) (Result, error) {
	if !validPublishCall(ctx, p, request, source) {
		return Result{}, sourceFailure(errors.New("invalid publication request"))
	}
	if observe == nil {
		observe = func(context.Context, Progress) error { return nil }
	}
	operationCtx, cancel := context.WithTimeout(ctx, maximumPublishDuration)
	defer cancel()
	target := targetFromConfig(p.config)

	prepared, err := prepareSourceArchive(
		operationCtx,
		source,
		request.SourceSize,
		request.SourceSHA256,
	)
	if err != nil {
		return Result{}, err
	}
	defer prepared.Close()

	sourceObject, sourceGeneration, err := p.uploadSource(operationCtx, target, request, prepared)
	if err != nil {
		return Result{}, err
	}
	if err := observe(operationCtx, Progress{
		Stage: StageBuilding, SourceBucket: target.SourceBucket,
		SourceObject: sourceObject, SourceGeneration: sourceGeneration,
		Template: prepared.Template,
	}); err != nil {
		return Result{}, err
	}
	buildResult, err := p.buildImage(
		operationCtx,
		target,
		request,
		prepared,
		sourceObject,
		sourceGeneration,
	)
	if err != nil {
		return Result{}, err
	}
	if err := observe(operationCtx, Progress{
		Stage: StageDeploying, BuildID: buildResult.BuildID,
		ImageURI: buildResult.ImageURI, ImageDigest: buildResult.ImageDigest,
	}); err != nil {
		return Result{}, err
	}
	serviceURI, err := p.deployService(
		operationCtx,
		target,
		request,
		prepared.Port,
		buildResult.ImageURI,
	)
	if err != nil {
		return Result{}, err
	}
	return Result{
		SourceBucket: target.SourceBucket, SourceObject: sourceObject,
		SourceGeneration: sourceGeneration, Template: prepared.Template,
		BuildID: buildResult.BuildID, ImageURI: buildResult.ImageURI,
		ImageDigest: buildResult.ImageDigest,
		ServiceRef: "projects/" + target.ProjectID + "/locations/" + target.Region +
			"/services/" + request.ServiceName,
		ServiceURI: serviceURI,
	}, nil
}

func validPublishCall(
	ctx context.Context,
	publisher *PublisherService,
	request Request,
	source io.Reader,
) bool {
	return ctx != nil && publisher != nil && publisher.storage != nil && publisher.build != nil &&
		publisher.run != nil && source != nil && publicationIDPattern.MatchString(request.PublicationID) &&
		serviceNamePattern.MatchString(request.ServiceName) && validConfig(publisher.config) &&
		request.Attempt >= 1 &&
		request.SourceSize >= 1 && request.SourceSize <= maximumSourceArchiveBytes &&
		(len(request.SourceSHA256) == 0 || len(request.SourceSHA256) == sha256.Size)
}

func (p *PublisherService) uploadSource(
	ctx context.Context,
	target Target,
	request Request,
	prepared *preparedSource,
) (string, int64, error) {
	objectName := "boardly/publications/" + request.PublicationID + "/source.zip"
	md5Hash := base64.StdEncoding.EncodeToString(prepared.MD5)
	object := &storageapi.Object{
		Name: objectName, ContentType: "application/zip", Md5Hash: md5Hash,
		Metadata: map[string]string{"managed-by": "boardly", "publication-id": request.PublicationID},
	}
	created, err := p.storage.Objects.Insert(target.SourceBucket, object).
		Name(objectName).
		IfGenerationMatch(0).
		Media(prepared, googleapi.ContentType("application/zip")).
		Context(ctx).
		Do()
	if err == nil {
		if created == nil || created.Generation < 1 || created.Md5Hash != md5Hash {
			return "", 0, providerFailure(
				FailureProviderUnavailable,
				true,
				errors.New("Google Cloud Storage returned invalid source metadata"),
			)
		}
		return objectName, created.Generation, nil
	}
	if googleAPIStatus(err) != http.StatusPreconditionFailed {
		return "", 0, googleProviderError(FailureProviderUnavailable, err)
	}
	existing, getErr := p.storage.Objects.Get(target.SourceBucket, objectName).Context(ctx).Do()
	if getErr != nil {
		return "", 0, googleProviderError(FailureProviderUnavailable, getErr)
	}
	decodedMD5, decodeErr := base64.StdEncoding.DecodeString(existing.Md5Hash)
	if decodeErr != nil || existing.Generation < 1 || existing.Size != uint64(prepared.Size) ||
		subtle.ConstantTimeCompare(decodedMD5, prepared.MD5) != 1 {
		return "", 0, sourceFailure(errors.New("immutable source object conflicts with prepared archive"))
	}
	return objectName, existing.Generation, nil
}

type imageBuildResult struct {
	BuildID     string
	ImageURI    string
	ImageDigest string
}

func (p *PublisherService) buildImage(
	ctx context.Context,
	target Target,
	request Request,
	prepared *preparedSource,
	sourceObject string,
	sourceGeneration int64,
) (imageBuildResult, error) {
	imageTag := target.ArtifactRegistry + "/" + request.ServiceName + ":attempt-" +
		strconv.Itoa(request.Attempt)
	step := &cloudbuildapi.BuildStep{Name: cloudBuildDockerBuilder}
	if prepared.Build == BuildPack {
		step.Name = cloudBuildPackBuilder
		step.Args = []string{"build", imageTag, "--builder", cloudBuildBuildpack}
	} else {
		step.Args = []string{"build", "--pull", "-f", prepared.DockerfilePath, "-t", imageTag, "."}
	}
	build := &cloudbuildapi.Build{
		Source: &cloudbuildapi.Source{StorageSource: &cloudbuildapi.StorageSource{
			Bucket: target.SourceBucket, Object: sourceObject, Generation: sourceGeneration,
		}},
		Steps: []*cloudbuildapi.BuildStep{step}, Images: []string{imageTag},
		Options: &cloudbuildapi.BuildOptions{
			Logging: "CLOUD_LOGGING_ONLY", MachineType: cloudBuildMachineType,
			SourceProvenanceHash: []string{"SHA256"},
		},
		Timeout: googleDuration(cloudBuildTimeout),
		Tags:    []string{"boardly-publication"},
	}
	build.ServiceAccount = "projects/" + target.ProjectID +
		"/serviceAccounts/" + target.BuildServiceAccount
	parent := "projects/" + target.ProjectID + "/locations/" + target.Region
	operation, err := p.build.Projects.Locations.Builds.Create(parent, build).Context(ctx).Do()
	if err != nil {
		return imageBuildResult{}, googleProviderError(FailureProviderUnavailable,
			fmt.Errorf("create Cloud Build in %s: %w", parent, err))
	}
	completed, err := p.waitForBuild(ctx, operation)
	if err != nil {
		return imageBuildResult{}, err
	}
	if completed.Status != "SUCCESS" || completed.Id == "" || completed.Results == nil ||
		len(completed.Results.Images) != 1 || completed.Results.Images[0].Digest == "" {
		failure := FailureBuildFailed
		retryable := completed.Status == "INTERNAL_ERROR"
		if retryable {
			failure = FailureProviderUnavailable
		}
		return imageBuildResult{}, providerFailure(
			failure,
			retryable,
			fmt.Errorf("Cloud Build completed with status %s", completed.Status),
		)
	}
	digest := completed.Results.Images[0].Digest
	imageName, _, _ := strings.Cut(imageTag, ":")
	return imageBuildResult{
		BuildID: completed.Id, ImageURI: imageName + "@" + digest, ImageDigest: digest,
	}, nil
}

func (p *PublisherService) waitForBuild(
	ctx context.Context,
	operation *cloudbuildapi.Operation,
) (*cloudbuildapi.Build, error) {
	if operation == nil || operation.Name == "" {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			errors.New("Cloud Build returned no operation"),
		)
	}
	if !operation.Done {
		var metadata cloudbuildapi.BuildOperationMetadata
		if json.Unmarshal(operation.Metadata, &metadata) != nil || metadata.Build == nil ||
			metadata.Build.Id == "" || strings.Contains(metadata.Build.Id, "/") {
			return nil, providerFailure(FailureProviderUnavailable, false,
				errors.New("Cloud Build returned no build identity"))
		}
		return p.waitForRegionalBuild(ctx, metadata.Build.Id)
	}
	if operation.Error != nil {
		return nil, providerFailure(
			FailureBuildFailed,
			retryableGoogleOperation(operation.Error.Code),
			fmt.Errorf("Cloud Build operation failed with code %d", operation.Error.Code),
		)
	}
	var build cloudbuildapi.Build
	if len(operation.Response) == 0 || json.Unmarshal(operation.Response, &build) != nil {
		return nil, providerFailure(
			FailureProviderUnavailable,
			true,
			errors.New("Cloud Build returned an invalid result"),
		)
	}
	return &build, nil
}

// Regional builds must be read in the region where they were created; the
// legacy operation name alone can resolve against the global build location.
func (p *PublisherService) waitForRegionalBuild(ctx context.Context, buildID string) (*cloudbuildapi.Build, error) {
	name := "projects/" + p.config.ProjectID + "/locations/" + p.config.Region + "/builds/" + buildID
	for {
		if err := waitForPoll(ctx, p.pollInterval); err != nil {
			return nil, err
		}
		build, err := p.build.Projects.Locations.Builds.Get(name).Context(ctx).Do()
		if err != nil {
			return nil, googleProviderError(
				FailureProviderUnavailable,
				fmt.Errorf("read Cloud Build %s status: %w", name, err),
			)
		}
		switch build.Status {
		case "PENDING", "QUEUED", "WORKING":
			continue
		default:
			return build, nil
		}
	}
}

func (p *PublisherService) deployService(
	ctx context.Context,
	target Target,
	request Request,
	port int64,
	imageURI string,
) (string, error) {
	parent := "projects/" + target.ProjectID + "/locations/" + target.Region
	name := parent + "/services/" + request.ServiceName
	service := &runapi.GoogleCloudRunV2Service{
		Description: "Zenith Team Output publication " + request.PublicationID,
		Labels:      map[string]string{"managed-by": "boardly"},
		Ingress:     "INGRESS_TRAFFIC_ALL", InvokerIamDisabled: true,
		Template: &runapi.GoogleCloudRunV2RevisionTemplate{
			Containers: []*runapi.GoogleCloudRunV2Container{{
				Image: imageURI,
				Ports: []*runapi.GoogleCloudRunV2ContainerPort{{ContainerPort: port}},
				Resources: &runapi.GoogleCloudRunV2ResourceRequirements{
					Limits:  map[string]string{"memory": p.config.Memory, "cpu": p.config.CPU},
					CpuIdle: true,
				},
			}},
			Scaling: &runapi.GoogleCloudRunV2RevisionScaling{
				MinInstanceCount: p.config.MinInstances, MaxInstanceCount: p.config.MaxInstances,
			},
			MaxInstanceRequestConcurrency: p.config.Concurrency,
			Timeout:                       googleDuration(p.config.Timeout),
			ServiceAccount:                target.RuntimeServiceAccount,
		},
	}
	_, getErr := p.run.Projects.Locations.Services.Get(name).Context(ctx).Do()
	var operation *runapi.GoogleLongrunningOperation
	var err error
	switch {
	case googleAPIStatus(getErr) == http.StatusNotFound:
		operation, err = p.run.Projects.Locations.Services.Create(parent, service).
			ServiceId(request.ServiceName).
			Context(ctx).
			Do()
	case getErr != nil:
		return "", googleProviderError(FailureProviderUnavailable, getErr)
	default:
		service.Name = name
		operation, err = p.run.Projects.Locations.Services.Patch(name, service).
			UpdateMask("description,labels,ingress,invoker_iam_disabled,template").
			Context(ctx).
			Do()
	}
	if err != nil {
		return "", googleProviderError(FailureDeployFailed, err)
	}
	if err := p.waitForRunOperation(ctx, operation, FailureDeployFailed); err != nil {
		return "", err
	}
	deployed, err := p.run.Projects.Locations.Services.Get(name).Context(ctx).Do()
	if err != nil {
		return "", googleProviderError(FailureDeployFailed, err)
	}
	if deployed.Uri == "" || deployed.Reconciling || deployed.TerminalCondition == nil ||
		deployed.TerminalCondition.State != "CONDITION_SUCCEEDED" {
		return "", providerFailure(
			FailureDeployFailed,
			false,
			errors.New("Cloud Run service did not become ready"),
		)
	}
	return deployed.Uri, nil
}

func (p *PublisherService) Delete(ctx context.Context, request DeleteRequest) error {
	if ctx == nil || p == nil {
		return sourceFailure(errors.New("invalid Cloud Run publication deletion"))
	}
	target := targetFromConfig(p.config)
	repository, repositoryOK := artifactRepository(target)
	if p.run == nil || p.artifact == nil || p.storage == nil ||
		!validDeleteTarget(target) || !repositoryOK ||
		!bucketPattern.MatchString(target.SourceBucket) ||
		!publicationIDPattern.MatchString(request.PublicationID) ||
		!serviceNamePattern.MatchString(request.ServiceName) {
		return sourceFailure(errors.New("invalid Cloud Run publication deletion"))
	}
	var cleanupErrors []error
	timeout := p.deleteOperationTimeout()
	if err := runDeleteAttempt(ctx, timeout, func(attemptCtx context.Context) error {
		return p.deleteService(attemptCtx, target, request.ServiceName)
	}); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := runDeleteAttempt(ctx, timeout, func(attemptCtx context.Context) error {
		return p.deleteImages(attemptCtx, target, repository, request.ServiceName)
	}); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := runDeleteAttempt(ctx, timeout, func(attemptCtx context.Context) error {
		return p.deleteSource(attemptCtx, target.SourceBucket, request.PublicationID)
	}); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := errors.Join(cleanupErrors...); err != nil {
		return providerFailure(FailureProviderUnavailable, true, err)
	}
	return nil
}

func (p *PublisherService) deleteOperationTimeout() time.Duration {
	if p.deleteTimeout > 0 {
		return p.deleteTimeout
	}
	return maximumDeleteDuration
}

func runDeleteAttempt(
	ctx context.Context,
	timeout time.Duration,
	deleteResource func(context.Context) error,
) error {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return deleteResource(attemptCtx)
}

func (p *PublisherService) deleteService(ctx context.Context, target Target, serviceName string) error {
	name := "projects/" + target.ProjectID + "/locations/" + target.Region +
		"/services/" + serviceName
	operation, err := p.run.Projects.Locations.Services.Delete(name).Context(ctx).Do()
	if googleAPIStatus(err) == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return googleProviderError(FailureProviderUnavailable, err)
	}
	return p.waitForRunOperation(ctx, operation, FailureProviderUnavailable)
}

func (p *PublisherService) deleteImages(
	ctx context.Context,
	target Target,
	repository string,
	serviceName string,
) error {
	name := "projects/" + target.ProjectID + "/locations/" + target.Region +
		"/repositories/" + repository + "/packages/" + serviceName
	operation, err := p.artifact.Projects.Locations.Repositories.Packages.Delete(name).Context(ctx).Do()
	if googleAPIStatus(err) == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return googleProviderError(FailureProviderUnavailable, err)
	}
	return p.waitForArtifactOperation(ctx, operation)
}

func (p *PublisherService) deleteSource(ctx context.Context, bucket, publicationID string) error {
	object := "boardly/publications/" + publicationID + "/source.zip"
	err := p.storage.Objects.Delete(bucket, object).Context(ctx).Do()
	if googleAPIStatus(err) == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return googleProviderError(FailureProviderUnavailable, err)
	}
	return nil
}

func validDeleteTarget(target Target) bool {
	return projectIDPattern.MatchString(target.ProjectID) && regionPattern.MatchString(target.Region)
}

func (p *PublisherService) waitForRunOperation(
	ctx context.Context,
	operation *runapi.GoogleLongrunningOperation,
	code FailureCode,
) error {
	if operation == nil || operation.Name == "" {
		return providerFailure(code, true, errors.New("Cloud Run returned no operation"))
	}
	for !operation.Done {
		if err := waitForPoll(ctx, p.pollInterval); err != nil {
			return err
		}
		var err error
		operation, err = p.run.Projects.Locations.Operations.Get(operation.Name).Context(ctx).Do()
		if err != nil {
			return googleProviderError(code, err)
		}
	}
	if operation.Error != nil {
		return providerFailure(
			code,
			retryableGoogleOperation(operation.Error.Code),
			fmt.Errorf("Cloud Run operation failed with code %d", operation.Error.Code),
		)
	}
	return nil
}

func (p *PublisherService) waitForArtifactOperation(
	ctx context.Context,
	operation *artifactapi.Operation,
) error {
	if operation == nil || operation.Name == "" {
		return providerFailure(
			FailureProviderUnavailable,
			true,
			errors.New("Artifact Registry returned no operation"),
		)
	}
	for !operation.Done {
		if err := waitForPoll(ctx, p.pollInterval); err != nil {
			return err
		}
		var err error
		operation, err = p.artifact.Projects.Locations.Operations.Get(operation.Name).Context(ctx).Do()
		if err != nil {
			return googleProviderError(FailureProviderUnavailable, err)
		}
	}
	if operation.Error != nil {
		return providerFailure(
			FailureProviderUnavailable,
			retryableGoogleOperation(operation.Error.Code),
			fmt.Errorf("Artifact Registry operation failed with code %d", operation.Error.Code),
		)
	}
	return nil
}

func waitForPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func googleProviderError(code FailureCode, err error) error {
	retryable := retryableGoogleRequest(err)
	if code != FailureDeployFailed || retryable {
		code = FailureProviderUnavailable
	}
	return providerFailure(code, retryable, err)
}

// Long-running operation errors carry google.rpc.Code, even over the REST API.
func retryableGoogleOperation(code int64) bool {
	switch code {
	case int64(codes.DeadlineExceeded), int64(codes.ResourceExhausted), int64(codes.Aborted),
		int64(codes.Internal), int64(codes.Unavailable):
		return true
	default:
		return false
	}
}

func retryableGoogleRequest(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if status := googleAPIStatus(err); status != 0 {
		return status == http.StatusTooManyRequests || status == http.StatusRequestTimeout ||
			(status >= 500 && status <= 599)
	}
	if networkError, ok := errors.AsType[net.Error](err); ok && networkError.Timeout() {
		return true
	}
	if dnsError, ok := errors.AsType[*net.DNSError](err); ok && dnsError.IsTemporary {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
}

func googleAPIStatus(err error) int {
	var apiError *googleapi.Error
	if errors.As(err, &apiError) {
		return apiError.Code
	}
	return 0
}

func googleDuration(duration time.Duration) string {
	return strconv.FormatInt(int64(duration/time.Second), 10) + "s"
}
