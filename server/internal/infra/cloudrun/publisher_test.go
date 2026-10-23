//go:build !zenith_minimal

package cloudrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	artifactapi "google.golang.org/api/artifactregistry/v1"
	cloudbuildapi "google.golang.org/api/cloudbuild/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	runapi "google.golang.org/api/run/v2"
	storageapi "google.golang.org/api/storage/v1"
	"google.golang.org/grpc/codes"
)

func TestOperationFailuresClassifyRPCStatus(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		code      codes.Code
		retryable bool
	}{
		{codes.Unavailable, true},
		{codes.ResourceExhausted, true},
		{codes.DeadlineExceeded, true},
		{codes.Internal, true},
		{codes.Aborted, true},
		{codes.Canceled, false},
		{codes.InvalidArgument, false},
		{codes.PermissionDenied, false},
		{codes.Unauthenticated, false},
		{codes.FailedPrecondition, false},
		{codes.NotFound, false},
		{codes.Unknown, false},
	} {
		t.Run(test.code.String(), func(t *testing.T) {
			t.Parallel()
			publisher := &PublisherService{}
			_, buildErr := publisher.waitForBuild(t.Context(), &cloudbuildapi.Operation{
				Name: "operations/build-1", Done: true,
				Error: &cloudbuildapi.Status{Code: int64(test.code)},
			})
			runErr := publisher.waitForRunOperation(t.Context(), &runapi.GoogleLongrunningOperation{
				Name: "projects/example-project/locations/us-central1/operations/deploy-1", Done: true,
				Error: &runapi.GoogleRpcStatus{Code: int64(test.code)},
			}, FailureDeployFailed)
			artifactErr := publisher.waitForArtifactOperation(t.Context(), &artifactapi.Operation{
				Name: "projects/example-project/locations/us-central1/operations/delete-1", Done: true,
				Error: &artifactapi.Status{Code: int64(test.code)},
			})
			for _, operation := range []struct {
				name string
				err  error
				code FailureCode
			}{
				{"build", buildErr, FailureBuildFailed},
				{"run", runErr, FailureDeployFailed},
				{"artifact", artifactErr, FailureProviderUnavailable},
			} {
				t.Run(operation.name, func(t *testing.T) {
					failure, ok := errors.AsType[*Failure](operation.err)
					if !ok || failure.Retryable != test.retryable ||
						failure.Code != operation.code {
						t.Fatalf("operation failure = %+v, want code %s, retryable %t",
							failure, operation.code, test.retryable)
					}
				})
			}
		})
	}
}

func TestGoogleProviderErrorClassifiesRequests(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		err       error
		retryable bool
	}{
		{"rate limit", &googleapi.Error{Code: http.StatusTooManyRequests}, true},
		{"request timeout", &googleapi.Error{Code: http.StatusRequestTimeout}, true},
		{"server error", &googleapi.Error{Code: http.StatusInternalServerError}, true},
		{"unavailable", &googleapi.Error{Code: http.StatusServiceUnavailable}, true},
		{"bad request", &googleapi.Error{Code: http.StatusBadRequest}, false},
		{"unauthorized", &googleapi.Error{Code: http.StatusUnauthorized}, false},
		{"forbidden", &googleapi.Error{Code: http.StatusForbidden}, false},
		{"not found", &googleapi.Error{Code: http.StatusNotFound}, false},
		{"truncated response", io.ErrUnexpectedEOF, true},
		{"closed response", io.EOF, true},
		{"closed connection", net.ErrClosed, true},
		{"connection reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, true},
		{"broken pipe", &net.OpError{Op: "write", Net: "tcp", Err: syscall.EPIPE}, true},
		{"deadline exceeded", context.DeadlineExceeded, true},
		{"temporary DNS failure", &net.DNSError{IsTemporary: true}, true},
		{"DNS not found", &net.DNSError{IsNotFound: true}, false},
		{"canceled", context.Canceled, false},
		{"unknown error", errors.New("invalid credentials"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			// Google HTTP clients wrap transport errors; classification must survive that chain.
			wrapped := fmt.Errorf("Google request: %w", &url.Error{
				Op: "Post", URL: "https://run.googleapis.com", Err: test.err,
			})
			for _, code := range []FailureCode{FailureDeployFailed, FailureProviderUnavailable} {
				for _, cause := range []error{test.err, wrapped} {
					err := googleProviderError(code, cause)
					wantCode := code
					if test.retryable {
						wantCode = FailureProviderUnavailable
					}
					failure, ok := errors.AsType[*Failure](err)
					if !ok || failure.Retryable != test.retryable || failure.Code != wantCode {
						t.Fatalf("googleProviderError(%s) = %+v, want code %s, retryable %t",
							code, failure, wantCode, test.retryable)
					}
					if !errors.Is(err, test.err) {
						t.Fatalf("googleProviderError() lost cause: %v", err)
					}
				}
			}
		})
	}
}

func TestValidConfigRequiresDedicatedServiceAccounts(t *testing.T) {
	t.Parallel()

	valid := Config{
		ProjectID:             "example-project",
		Region:                "us-central1",
		SourceBucket:          "example-project-app-sources",
		ArtifactRegistry:      "us-central1-docker.pkg.dev/example-project/user-apps",
		BuildServiceAccount:   "builder@example-project.iam.gserviceaccount.com",
		RuntimeServiceAccount: "runtime@example-project.iam.gserviceaccount.com",
		MaxInstances:          10,
		Memory:                "512Mi",
		CPU:                   "1",
		Timeout:               5 * time.Minute,
		Concurrency:           80,
	}
	if !validConfig(valid) {
		t.Fatal("validConfig(valid) = false")
	}

	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "missing build identity",
			mutate: func(config *Config) {
				config.BuildServiceAccount = ""
			},
		},
		{
			name: "missing runtime identity",
			mutate: func(config *Config) {
				config.RuntimeServiceAccount = ""
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := valid
			test.mutate(&config)
			if validConfig(config) {
				t.Fatal("validConfig() = true")
			}
		})
	}
}

func TestBindingIDPinsResourceAuthority(t *testing.T) {
	t.Parallel()

	config := Config{
		ProjectID:        "example-project",
		Region:           "us-central1",
		SourceBucket:     "example-project-app-sources",
		ArtifactRegistry: "us-central1-docker.pkg.dev/example-project/user-apps",
	}
	publisher := &PublisherService{config: config}
	got := publisher.BindingID()
	if !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(got) {
		t.Fatalf("BindingID() = %q", got)
	}

	scalingChange := config
	scalingChange.MaxInstances = 25
	if changed := (&PublisherService{config: scalingChange}).BindingID(); changed != got {
		t.Fatalf("BindingID() changed for non-authority configuration: %q", changed)
	}

	regionChange := config
	regionChange.Region = "asia-southeast1"
	if changed := (&PublisherService{config: regionChange}).BindingID(); changed == got {
		t.Fatal("BindingID() did not change with its resource authority")
	}
}

func TestPublishUploadsBuildsAndDeploysDigestPinnedApplication(t *testing.T) {
	t.Parallel()

	var (
		buildRequestBody string
		buildRequestPath string
		runRequestBody   string
		runGetCalls      int
		buildGetCalls    int
	)
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		var body []byte
		if request.Body != nil {
			var err error
			body, err = io.ReadAll(request.Body)
			if err != nil {
				t.Errorf("read %s %s body: %v", request.Method, request.URL.Path, err)
			}
		}
		switch {
		case strings.Contains(request.URL.Path, "/upload/storage/v1/"):
			md5Match := regexp.MustCompile(`"md5Hash"\s*:\s*"([^"]+)"`).FindSubmatch(body)
			if len(md5Match) != 2 {
				t.Errorf("storage upload metadata has no MD5: %s", body)
				return jsonHTTPResponse(request, http.StatusBadRequest, `{}`)
			}
			return jsonHTTPResponse(request, http.StatusOK,
				`{"name":"source.zip","generation":"7","md5Hash":"`+string(md5Match[1])+`"}`)
		case strings.HasSuffix(request.URL.Path, "/builds"):
			buildRequestBody = string(body)
			buildRequestPath = request.URL.Path
			return jsonHTTPResponse(request, http.StatusOK, `{
                "name":"operations/build/example-project/opaque-operation",
                "metadata":{"@type":"type.googleapis.com/google.devtools.cloudbuild.v1.BuildOperationMetadata",
                    "build":{"id":"build-1","name":"projects/12345/locations/us-central1/builds/build-1","status":"QUEUED"}}
            }`)
		case request.URL.Path == "/v1/projects/example-project/locations/us-central1/builds/build-1":
			buildGetCalls++
			if buildGetCalls == 1 {
				return jsonHTTPResponse(request, http.StatusOK, `{"id":"build-1","status":"WORKING"}`)
			}
			return jsonHTTPResponse(request, http.StatusOK, `{
					"id":"build-1",
					"status":"SUCCESS",
                    "results":{"images":[{"digest":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}
            }`)
		case strings.HasSuffix(request.URL.Path, "/services/zenith-team-application") &&
			request.Method == http.MethodGet:
			runGetCalls++
			if runGetCalls == 1 {
				return jsonHTTPResponse(request, http.StatusNotFound, `{"error":{"code":404}}`)
			}
			return jsonHTTPResponse(request, http.StatusOK, `{
				"name":"projects/example-project/locations/us-central1/services/zenith-team-application",
				"uri":"https://zenith-team-application.example.run.app",
				"terminalCondition":{"state":"CONDITION_SUCCEEDED"}
			}`)
		case strings.HasSuffix(request.URL.Path, "/services") && request.Method == http.MethodPost:
			runRequestBody = string(body)
			var service map[string]json.RawMessage
			if err := json.Unmarshal(body, &service); err != nil {
				t.Fatalf("decode create service request: %v", err)
			}
			if _, present := service["name"]; present {
				t.Error("create service request must omit name")
				return jsonHTTPResponse(
					request,
					http.StatusBadRequest,
					`{"error":{"code":400,"message":"service.name must be empty on CreateServiceRequest"}}`,
				)
			}
			if got := request.URL.Query().Get("serviceId"); got != "zenith-team-application" {
				t.Errorf("create serviceId = %q, want zenith-team-application", got)
			}
			return jsonHTTPResponse(request, http.StatusOK, `{
				"name":"projects/example-project/locations/us-central1/operations/deploy-1",
				"done":true
			}`)
		default:
			t.Errorf("unexpected Google API request: %s %s", request.Method, request.URL.String())
			return jsonHTTPResponse(request, http.StatusNotFound, `{}`)
		}
	})}
	storageService, err := storageapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("storageapi.NewService() error = %v", err)
	}
	buildService, err := cloudbuildapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("cloudbuildapi.NewService() error = %v", err)
	}
	runService, err := runapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("runapi.NewService() error = %v", err)
	}
	publisher := &PublisherService{
		config: Config{
			ProjectID: "example-project", Region: "us-central1",
			SourceBucket:          "example-project-app-sources",
			ArtifactRegistry:      "us-central1-docker.pkg.dev/example-project/user-apps",
			BuildServiceAccount:   "builder@example-project.iam.gserviceaccount.com",
			RuntimeServiceAccount: "runtime@example-project.iam.gserviceaccount.com",
			MaxInstances:          10, Memory: "512Mi", CPU: "1",
			Timeout: 5 * time.Minute, Concurrency: 80,
		},
		storage: storageService, build: buildService, run: runService, pollInterval: time.Millisecond,
	}
	source := zipFixture(t, map[string]string{"public/index.html": "<main>Zenith Team</main>"})
	digest := sha256.Sum256(source)
	var progress []Progress
	result, err := publisher.Publish(t.Context(), Request{
		PublicationID: "01900000-0000-7000-8000-000000000001",
		ServiceName:   "zenith-team-application", Attempt: 1,
		SourceSize: int64(len(source)), SourceSHA256: digest[:],
	}, bytes.NewReader(source), func(_ context.Context, update Progress) error {
		progress = append(progress, update)
		return nil
	})
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if result.Template != TemplateStatic || result.SourceGeneration != 7 ||
		result.BuildID != "build-1" ||
		result.ImageDigest != "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" ||
		result.ServiceURI != "https://zenith-team-application.example.run.app" || len(progress) != 2 ||
		progress[0].Stage != StageBuilding || progress[1].Stage != StageDeploying {
		t.Fatalf("Publish() result/progress = %+v / %+v", result, progress)
	}
	for name, body := range map[string]string{"build": buildRequestBody, "run": runRequestBody} {
		if body == "" {
			t.Fatalf("%s request body is empty", name)
		}
	}
	if !strings.Contains(buildRequestBody,
		"projects/example-project/serviceAccounts/builder@example-project.iam.gserviceaccount.com") ||
		!strings.Contains(runRequestBody,
			"us-central1-docker.pkg.dev/example-project/user-apps/zenith-team-application@sha256:bbbb") ||
		!strings.Contains(runRequestBody, "runtime@example-project.iam.gserviceaccount.com") {
		t.Fatalf("build/run request bodies = %s / %s", buildRequestBody, runRequestBody)
	}
	if buildGetCalls != 2 {
		t.Fatalf("regional build polling calls = %d, want 2", buildGetCalls)
	}
	if buildRequestPath != "/v1/projects/example-project/locations/us-central1/builds" {
		t.Fatalf("Cloud Build request path = %q, want configured region", buildRequestPath)
	}
}

func TestDeployServiceUpdatesExistingApplication(t *testing.T) {
	t.Parallel()

	name := "projects/example-project/locations/us-central1/services/zenith-team-application"
	var methods []string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		methods = append(methods, request.Method)
		if request.URL.Path != "/v2/"+name {
			t.Errorf("service request path = %q, want %q", request.URL.Path, "/v2/"+name)
		}
		switch request.Method {
		case http.MethodGet:
			return jsonHTTPResponse(request, http.StatusOK, `{
				"uri":"https://zenith-team-application.example.run.app",
				"terminalCondition":{"state":"CONDITION_SUCCEEDED"}
			}`)
		case http.MethodPatch:
			var service runapi.GoogleCloudRunV2Service
			if err := json.NewDecoder(request.Body).Decode(&service); err != nil {
				t.Fatalf("decode update service request: %v", err)
			}
			if service.Name != name {
				t.Errorf("update service name = %q, want %q", service.Name, name)
			}
			if got := request.URL.Query().
				Get("updateMask"); got != "description,labels,ingress,invoker_iam_disabled,template" {
				t.Errorf("updateMask = %q, want mutable service configuration", got)
			}
			return jsonHTTPResponse(
				request,
				http.StatusOK,
				`{"name":"projects/example-project/locations/us-central1/operations/update-1","done":true}`,
			)
		default:
			t.Errorf("unexpected service request method = %q", request.Method)
			return jsonHTTPResponse(request, http.StatusBadRequest, `{}`)
		}
	})}
	runService, err := runapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	publisher := &PublisherService{
		config: Config{MaxInstances: 10, Memory: "512Mi", CPU: "1", Timeout: 5 * time.Minute, Concurrency: 80},
		run:    runService,
	}
	got, err := publisher.deployService(t.Context(), Target{ProjectID: "example-project", Region: "us-central1"},
		Request{ServiceName: "zenith-team-application"}, 8080, "registry/app@sha256:digest")
	if err != nil || got != "https://zenith-team-application.example.run.app" {
		t.Fatalf("deployService() = %q, %v", got, err)
	}
	if want := []string{http.MethodGet, http.MethodPatch, http.MethodGet}; !slices.Equal(methods, want) {
		t.Fatalf("service request methods = %v, want %v", methods, want)
	}
}

func TestDeleteRemovesServiceSourceAndImages(t *testing.T) {
	t.Parallel()

	requestedPaths := []string{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requestedPaths = append(requestedPaths, request.URL.EscapedPath())
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(http.NoBody),
			Request:    request,
		}
	})}
	runService, err := runapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("runapi.NewService() error = %v", err)
	}
	storageService, err := storageapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("storageapi.NewService() error = %v", err)
	}
	artifactService, err := artifactapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("artifactapi.NewService() error = %v", err)
	}
	publisher := &PublisherService{
		config: Config{
			ProjectID:        "pinned-project",
			Region:           "asia-southeast1",
			SourceBucket:     "pinned-source-bucket",
			ArtifactRegistry: "asia-southeast1-docker.pkg.dev/pinned-project/user-apps",
		},
		storage:  storageService,
		artifact: artifactService,
		run:      runService,
	}
	if err := publisher.Delete(t.Context(), DeleteRequest{
		PublicationID: "01900000-0000-7000-8000-000000000001",
		ServiceName:   "zenith-team-application",
	}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	want := []string{
		"/v2/projects/pinned-project/locations/asia-southeast1/services/zenith-team-application",
		"/v1/projects/pinned-project/locations/asia-southeast1/repositories/user-apps/packages/zenith-team-application",
		"/storage/v1/b/pinned-source-bucket/o/zenith-team%2Fpublications%2F01900000-0000-7000-8000-000000000001%2Fsource.zip",
	}
	if !slices.Equal(requestedPaths, want) {
		t.Fatalf("Delete() paths = %#v, want %#v", requestedPaths, want)
	}
}

func TestDeleteContinuesCleanupAfterOneResourceFails(t *testing.T) {
	t.Parallel()

	requestCount := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) *http.Response {
		requestCount++
		status := http.StatusNotFound
		if requestCount == 1 {
			status = http.StatusForbidden
		}
		return &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
			Body:       io.NopCloser(http.NoBody),
			Request:    request,
		}
	})}
	runService, err := runapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("runapi.NewService() error = %v", err)
	}
	storageService, err := storageapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("storageapi.NewService() error = %v", err)
	}
	artifactService, err := artifactapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("artifactapi.NewService() error = %v", err)
	}
	publisher := &PublisherService{
		config: Config{
			ProjectID:        "pinned-project",
			Region:           "asia-southeast1",
			SourceBucket:     "pinned-source-bucket",
			ArtifactRegistry: "asia-southeast1-docker.pkg.dev/pinned-project/user-apps",
		},
		storage:  storageService,
		artifact: artifactService,
		run:      runService,
	}
	err = publisher.Delete(t.Context(), DeleteRequest{
		PublicationID: "01900000-0000-7000-8000-000000000001",
		ServiceName:   "zenith-team-application",
	})
	var failure *Failure
	if !errors.As(err, &failure) || requestCount != 3 {
		t.Fatalf("Delete() error = %v, requests = %d", err, requestCount)
	}
}

func TestDeleteGivesEveryCleanupAttemptALiveContext(t *testing.T) {
	t.Parallel()

	requestCount := 0
	client := &http.Client{Transport: roundTripErrorFunc(func(
		request *http.Request,
	) (*http.Response, error) {
		requestCount++
		if requestCount == 1 {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}
		if err := request.Context().Err(); err != nil {
			t.Fatalf("cleanup request %d started with canceled context: %v", requestCount, err)
		}
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(http.NoBody),
			Request:    request,
		}, nil
	})}
	runService, err := runapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("runapi.NewService() error = %v", err)
	}
	storageService, err := storageapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("storageapi.NewService() error = %v", err)
	}
	artifactService, err := artifactapi.NewService(t.Context(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatalf("artifactapi.NewService() error = %v", err)
	}
	publisher := &PublisherService{
		config: Config{
			ProjectID:        "pinned-project",
			Region:           "asia-southeast1",
			SourceBucket:     "pinned-source-bucket",
			ArtifactRegistry: "asia-southeast1-docker.pkg.dev/pinned-project/user-apps",
		},
		storage:       storageService,
		artifact:      artifactService,
		run:           runService,
		deleteTimeout: 10 * time.Millisecond,
	}
	err = publisher.Delete(t.Context(), DeleteRequest{
		PublicationID: "01900000-0000-7000-8000-000000000001",
		ServiceName:   "zenith-team-application",
	})
	var failure *Failure
	if !errors.As(err, &failure) || requestCount != 3 {
		t.Fatalf("Delete() error = %v, requests = %d, want three independent attempts", err, requestCount)
	}
}

type roundTripFunc func(*http.Request) *http.Response

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request), nil
}

var _ http.RoundTripper = roundTripFunc(nil)

type roundTripErrorFunc func(*http.Request) (*http.Response, error)

func (f roundTripErrorFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

var _ http.RoundTripper = roundTripErrorFunc(nil)

func jsonHTTPResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}
