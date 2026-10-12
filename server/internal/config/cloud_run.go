package config

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultCloudRunMaxInstances = int64(10)
	defaultCloudRunMinInstances = int64(0)
	maximumCloudRunInstances    = int64(100)
	defaultCloudRunTimeout      = 300 * time.Second
	defaultCloudRunConcurrency  = int64(80)
)

var (
	gcpProjectPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	gcpRegionPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9][0-9]$`)
	gcsBucketPattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]$`)
	artifactRegistryPattern = regexp.MustCompile(
		`^[a-z][a-z0-9-]{0,30}[a-z0-9][0-9]-docker\.pkg\.dev/[a-z][a-z0-9-]{4,28}[a-z0-9]/[a-z0-9][a-z0-9._-]{1,126}$`,
	)
	cloudRunMemoryPattern = regexp.MustCompile(`^[1-9][0-9]*(Mi|Gi)$`)
	serviceAccountPattern = regexp.MustCompile(
		`^[a-z][a-z0-9-]{4,28}[a-z0-9]@[a-z][a-z0-9-]{4,28}[a-z0-9]\.iam\.gserviceaccount\.com$`,
	)
)

type CloudRunConfig struct {
	Enabled               bool
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

func loadCloudRunConfig(lookup LookupEnv) (CloudRunConfig, error) {
	projectID := strings.TrimSpace(valueOrDefault(lookup, "GCP_PROJECT_ID", ""))
	if projectID == "" {
		return CloudRunConfig{}, nil
	}
	if !gcpProjectPattern.MatchString(projectID) {
		return CloudRunConfig{}, errors.New("GCP_PROJECT_ID is invalid")
	}
	region := strings.TrimSpace(valueOrDefault(lookup, "GCP_REGION", "us-central1"))
	if !gcpRegionPattern.MatchString(region) {
		return CloudRunConfig{}, errors.New("GCP_REGION is invalid")
	}
	sourceBucket := strings.TrimSpace(valueOrDefault(
		lookup,
		"GCP_SOURCE_BUCKET",
		projectID+"-app-sources",
	))
	if !validGCSBucket(sourceBucket) {
		return CloudRunConfig{}, errors.New("GCP_SOURCE_BUCKET is invalid")
	}
	artifactRegistry := strings.TrimSuffix(strings.TrimSpace(valueOrDefault(
		lookup,
		"GCP_ARTIFACT_REGISTRY",
		region+"-docker.pkg.dev/"+projectID+"/user-apps",
	)), "/")
	if !artifactRegistryPattern.MatchString(artifactRegistry) {
		return CloudRunConfig{}, errors.New("GCP_ARTIFACT_REGISTRY is invalid")
	}
	maxInstances, err := cloudRunInteger(
		lookup,
		"CLOUD_RUN_MAX_INSTANCES",
		defaultCloudRunMaxInstances,
		1,
		maximumCloudRunInstances,
	)
	if err != nil {
		return CloudRunConfig{}, err
	}
	minInstances, err := cloudRunInteger(
		lookup,
		"CLOUD_RUN_MIN_INSTANCES",
		defaultCloudRunMinInstances,
		0,
		maxInstances,
	)
	if err != nil {
		return CloudRunConfig{}, err
	}
	memory := strings.TrimSpace(valueOrDefault(lookup, "CLOUD_RUN_MEMORY", "512Mi"))
	if !cloudRunMemoryPattern.MatchString(memory) {
		return CloudRunConfig{}, errors.New("CLOUD_RUN_MEMORY is invalid")
	}
	cpu := strings.TrimSpace(valueOrDefault(lookup, "CLOUD_RUN_CPU", "1"))
	if cpu != "1" && cpu != "2" && cpu != "4" && cpu != "8" {
		return CloudRunConfig{}, errors.New("CLOUD_RUN_CPU must be 1, 2, 4, or 8")
	}
	buildServiceAccount := strings.TrimSpace(valueOrDefault(
		lookup,
		"GCP_SERVICE_ACCOUNT_EMAIL",
		"",
	))
	if buildServiceAccount == "" {
		return CloudRunConfig{}, errors.New("GCP_SERVICE_ACCOUNT_EMAIL is required when publication is enabled")
	}
	if !serviceAccountPattern.MatchString(buildServiceAccount) {
		return CloudRunConfig{}, errors.New("GCP_SERVICE_ACCOUNT_EMAIL is invalid")
	}
	runtimeServiceAccount := strings.TrimSpace(valueOrDefault(
		lookup,
		"CLOUD_RUN_SERVICE_ACCOUNT",
		"",
	))
	if runtimeServiceAccount == "" {
		return CloudRunConfig{}, errors.New("CLOUD_RUN_SERVICE_ACCOUNT is required when publication is enabled")
	}
	if !serviceAccountPattern.MatchString(runtimeServiceAccount) {
		return CloudRunConfig{}, errors.New("CLOUD_RUN_SERVICE_ACCOUNT is invalid")
	}
	return CloudRunConfig{
		Enabled: true, ProjectID: projectID, Region: region,
		SourceBucket: sourceBucket, ArtifactRegistry: artifactRegistry,
		BuildServiceAccount: buildServiceAccount, RuntimeServiceAccount: runtimeServiceAccount,
		MaxInstances: maxInstances, MinInstances: minInstances,
		Memory: memory, CPU: cpu, Timeout: defaultCloudRunTimeout,
		Concurrency: defaultCloudRunConcurrency,
	}, nil
}

func validGCSBucket(value string) bool {
	return gcsBucketPattern.MatchString(value) && !strings.Contains(value, "..") &&
		!strings.HasPrefix(value, "goog") && !strings.Contains(value, "google")
}

func cloudRunInteger(
	lookup LookupEnv,
	key string,
	fallback, minimum, maximum int64,
) (int64, error) {
	raw := strings.TrimSpace(valueOrDefault(lookup, key, strconv.FormatInt(fallback, 10)))
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < minimum || value > maximum {
		return 0, errors.New(key + " is outside the supported range")
	}
	return value, nil
}
