package cloudrun

import (
	"regexp"
	"strings"
	"time"
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
