package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func TestS3RoundTripPreservesContentMetadata(t *testing.T) {
	t.Parallel()

	client := newMemoryS3Client()
	store := newS3Store("zenith-team-test", client)
	payload := []byte("zenith-team s3 attachment")
	key := "companies/acme/attachments/file.txt"
	if err := store.Upload(
		context.Background(), key, bytes.NewReader(payload), int64(len(payload)),
		"text/plain", "customer report.txt",
	); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if err := store.Upload(
		context.Background(), key, bytes.NewReader(payload), int64(len(payload)),
		"text/plain", "customer report.txt",
	); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("Upload() replay error = %v, want ErrAlreadyExists", err)
	}

	reader, err := store.GetReader(context.Background(), key)
	if err != nil {
		t.Fatalf("GetReader() error = %v", err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read error = %v, close error = %v", readErr, closeErr)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("GetReader() bytes = %q, want %q", got, payload)
	}
	client.mu.Lock()
	contentType := client.contentTypes[key]
	contentDisposition := client.contentDispositions[key]
	client.mu.Unlock()
	if contentType != "text/plain" || !strings.Contains(contentDisposition, "customer report.txt") {
		t.Fatalf("stored metadata = %q / %q", contentType, contentDisposition)
	}

	if err := store.DeleteObject(context.Background(), key); err != nil {
		t.Fatalf("DeleteObject() error = %v", err)
	}
	if _, err := store.GetReader(context.Background(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetReader() after delete error = %v, want ErrNotFound", err)
	}
}

func TestNewS3FailsFastOnInvalidConfiguration(t *testing.T) {
	t.Parallel()

	for name, configuration := range map[string]S3Config{
		"missing bucket": {Region: "us-east-1"},
		"missing region": {Bucket: "zenith-team-test"},
		"incomplete credentials": {
			Bucket: "zenith-team-test", Region: "us-east-1", AccessKeyID: "only-access-key",
		},
		"invalid endpoint": {
			Bucket: "zenith-team-test", Region: "us-east-1", Endpoint: "file:///private/store",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewS3(context.Background(), configuration); err == nil {
				t.Fatal("NewS3() error = nil, want configuration error")
			}
		})
	}
}

func TestS3PresignUsesBoundedTTLAndContentDisposition(t *testing.T) {
	t.Parallel()

	presigner := &presignerFake{url: "https://objects.example/signed"}
	store := &S3{bucket: "zenith-team-test", presigner: presigner}
	got, err := store.PresignGetWithContentDisposition(
		context.Background(), "companies/acme/attachments/file.txt", 15*time.Minute,
		"attachment; filename=report.txt",
	)
	if err != nil || got != presigner.url {
		t.Fatalf("PresignGetWithContentDisposition() = %q, %v", got, err)
	}
	if presigner.ttl != 15*time.Minute || presigner.disposition != "attachment; filename=report.txt" {
		t.Fatalf("presign inputs = %s / %q", presigner.ttl, presigner.disposition)
	}
	if _, err := store.PresignGet(context.Background(), "valid/key", 8*24*time.Hour); err == nil {
		t.Fatal("PresignGet() accepted a TTL beyond the S3 maximum")
	}
}

func TestS3ProviderErrorsDoNotExposeBackendDetails(t *testing.T) {
	t.Parallel()

	const detail = "https://private.invalid secret-bucket companies/private/key SECRET-CREDENTIAL"
	store := newS3Store("secret-bucket", &providerErrorS3Client{
		memoryS3Client: newMemoryS3Client(), err: errors.New(detail),
	})
	operations := []func() error{
		func() error {
			return store.Upload(
				context.Background(), "companies/private/key", bytes.NewReader(nil), 0, "", "",
			)
		},
		func() error {
			_, err := store.GetReader(context.Background(), "companies/private/key")
			return err
		},
		func() error { return store.DeleteObject(context.Background(), "companies/private/key") },
	}
	for _, operation := range operations {
		err := operation()
		if err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("operation exposed provider detail: %v", err)
		}
	}
}

func TestS3UploadClassifiesConditionalConflicts(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		uploadErr error
		want      error
	}{
		"provider conflict": {
			uploadErr: &smithy.GenericAPIError{
				Code: "ConditionalRequestConflict", Message: "retry conditional request",
			},
			want: ErrRetryableConflict,
		},
		"HTTP conflict": {
			uploadErr: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{
					Response: &http.Response{StatusCode: http.StatusConflict},
				},
				Err: errors.New("retry conditional request"),
			},
			want: ErrRetryableConflict,
		},
		"provider precondition": {
			uploadErr: &smithy.GenericAPIError{
				Code: "PreconditionFailed", Message: "object exists",
			},
			want: ErrAlreadyExists,
		},
		"HTTP precondition": {
			uploadErr: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{
					Response: &http.Response{StatusCode: http.StatusPreconditionFailed},
				},
				Err: errors.New("object exists"),
			},
			want: ErrAlreadyExists,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store := newUploadErrorStore(newMemoryS3Client(), test.uploadErr)
			err := store.Upload(
				context.Background(), "conditional/object", bytes.NewReader(nil), 0, "", "",
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("Upload() error = %v, want %v", err, test.want)
			}
			if errors.Is(test.want, ErrRetryableConflict) && errors.Is(err, ErrAlreadyExists) {
				t.Fatalf("Upload() conflict = %v, must remain retryable", err)
			}
		})
	}
}

func TestS3MultipartCleanupUsesFreshBoundedContext(t *testing.T) {
	t.Parallel()

	const cleanupProviderDetail = "provider cleanup credential detail"
	abortCalled := false
	client := &abortS3Client{
		memoryS3Client: newMemoryS3Client(),
		abort: func(ctx context.Context, input *awss3.AbortMultipartUploadInput) error {
			abortCalled = true
			if err := ctx.Err(); err != nil {
				t.Fatalf("abort context is already canceled: %v", err)
			}
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("abort context has no deadline")
			}
			remaining := time.Until(deadline)
			if remaining <= 0 || remaining > 10*time.Second {
				t.Fatalf("abort context remaining = %s", remaining)
			}
			if aws.ToString(input.Bucket) != "zenith-team-test" ||
				aws.ToString(input.Key) != "multipart/canceled" ||
				aws.ToString(input.UploadId) != "upload-123" {
				t.Fatalf("AbortMultipartUpload input = %#v", input)
			}
			return errors.New(cleanupProviderDetail)
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &S3{
		bucket: "zenith-team-test",
		client: client,
		uploader: uploadFunc(func(
			context.Context,
			*awss3.PutObjectInput,
			...func(*manager.Uploader),
		) (*manager.UploadOutput, error) {
			cancel()
			return nil, &multiUploadFailure{uploadID: "upload-123", cause: context.Canceled}
		}),
	}

	err := store.Upload(ctx, "multipart/canceled", bytes.NewReader(nil), 0, "", "")
	if !abortCalled {
		t.Fatal("AbortMultipartUpload() was not called")
	}
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrMultipartCleanupFailed) {
		t.Fatalf("Upload() error = %v, want cancellation and cleanup failure", err)
	}
	if strings.Contains(err.Error(), cleanupProviderDetail) {
		t.Fatalf("Upload() leaked cleanup provider detail: %v", err)
	}
}

func TestS3CancellationAfterProviderCommitCannotReturnSuccess(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	store := &S3{
		bucket: "zenith-team-test",
		client: newMemoryS3Client(),
		uploader: uploadFunc(func(
			context.Context,
			*awss3.PutObjectInput,
			...func(*manager.Uploader),
		) (*manager.UploadOutput, error) {
			cancel()
			return &manager.UploadOutput{ETag: aws.String("committed")}, nil
		}),
	}
	if err := store.Upload(
		ctx, "companies/acme/attachments/canceled-after-commit",
		bytes.NewReader(nil), 0, "", "",
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Upload() error = %v, want context cancellation", err)
	}
}

func TestS3GetReaderClosesProviderBodyWhenCancellationRacesResponse(t *testing.T) {
	t.Parallel()

	body := &trackedS3Body{Reader: bytes.NewReader([]byte("private response"))}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &cancelingGetS3Client{
		memoryS3Client: newMemoryS3Client(), cancel: cancel, body: body,
	}
	store := newS3Store("zenith-team-test", client)
	reader, err := store.GetReader(ctx, "companies/acme/attachments/canceled-response")
	if reader != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("GetReader() = %#v, %v; want context cancellation", reader, err)
	}
	if !body.closed {
		t.Fatal("provider response body was not closed")
	}
}

type presignerFake struct {
	url         string
	ttl         time.Duration
	disposition string
}

func (p *presignerFake) PresignGetObject(
	_ context.Context,
	input *awss3.GetObjectInput,
	options ...func(*awss3.PresignOptions),
) (*awsv4.PresignedHTTPRequest, error) {
	settings := awss3.PresignOptions{}
	for _, option := range options {
		option(&settings)
	}
	p.ttl = settings.Expires
	p.disposition = aws.ToString(input.ResponseContentDisposition)
	return &awsv4.PresignedHTTPRequest{URL: p.url}, nil
}

type memoryS3Client struct {
	mu                  sync.Mutex
	objects             map[string][]byte
	contentTypes        map[string]string
	contentDispositions map[string]string
}

func newMemoryS3Client() *memoryS3Client {
	return &memoryS3Client{
		objects: make(map[string][]byte), contentTypes: make(map[string]string),
		contentDispositions: make(map[string]string),
	}
}

func (c *memoryS3Client) PutObject(
	_ context.Context,
	input *awss3.PutObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.PutObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := aws.ToString(input.Key)
	if _, exists := c.objects[key]; exists && aws.ToString(input.IfNoneMatch) == "*" {
		return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "already exists"}
	}
	payload, err := io.ReadAll(input.Body)
	if err != nil {
		return nil, err
	}
	c.objects[key] = append([]byte(nil), payload...)
	c.contentTypes[key] = aws.ToString(input.ContentType)
	c.contentDispositions[key] = aws.ToString(input.ContentDisposition)
	return &awss3.PutObjectOutput{ETag: aws.String("test-etag")}, nil
}

func (c *memoryS3Client) GetObject(
	_ context.Context,
	input *awss3.GetObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.GetObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	payload, exists := c.objects[aws.ToString(input.Key)]
	if !exists {
		return nil, &smithy.GenericAPIError{Code: "NoSuchKey", Message: "not found"}
	}
	return &awss3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(payload))}, nil
}

func (c *memoryS3Client) DeleteObject(
	_ context.Context,
	input *awss3.DeleteObjectInput,
	_ ...func(*awss3.Options),
) (*awss3.DeleteObjectOutput, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.objects, aws.ToString(input.Key))
	return &awss3.DeleteObjectOutput{}, nil
}

func (*memoryS3Client) AbortMultipartUpload(
	context.Context,
	*awss3.AbortMultipartUploadInput,
	...func(*awss3.Options),
) (*awss3.AbortMultipartUploadOutput, error) {
	return &awss3.AbortMultipartUploadOutput{}, nil
}

type providerErrorS3Client struct {
	*memoryS3Client
	err error
}

type abortS3Client struct {
	*memoryS3Client
	abort func(context.Context, *awss3.AbortMultipartUploadInput) error
}

func (c *abortS3Client) AbortMultipartUpload(
	ctx context.Context,
	input *awss3.AbortMultipartUploadInput,
	_ ...func(*awss3.Options),
) (*awss3.AbortMultipartUploadOutput, error) {
	if err := c.abort(ctx, input); err != nil {
		return nil, err
	}
	return &awss3.AbortMultipartUploadOutput{}, nil
}

type cancelingGetS3Client struct {
	*memoryS3Client
	cancel context.CancelFunc
	body   io.ReadCloser
}

func (c *cancelingGetS3Client) GetObject(
	context.Context,
	*awss3.GetObjectInput,
	...func(*awss3.Options),
) (*awss3.GetObjectOutput, error) {
	c.cancel()
	return &awss3.GetObjectOutput{Body: c.body}, nil
}

type trackedS3Body struct {
	io.Reader
	closed bool
}

func (b *trackedS3Body) Close() error {
	b.closed = true
	return nil
}

type multiUploadFailure struct {
	uploadID string
	cause    error
}

func (f *multiUploadFailure) Error() string    { return "multipart upload failed: " + f.cause.Error() }
func (f *multiUploadFailure) Unwrap() error    { return f.cause }
func (f *multiUploadFailure) UploadID() string { return f.uploadID }

type uploadFunc func(
	context.Context,
	*awss3.PutObjectInput,
	...func(*manager.Uploader),
) (*manager.UploadOutput, error)

func (u uploadFunc) Upload(
	ctx context.Context,
	input *awss3.PutObjectInput,
	options ...func(*manager.Uploader),
) (*manager.UploadOutput, error) {
	return u(ctx, input, options...)
}

func newUploadErrorStore(client s3Client, uploadErr error) *S3 {
	return &S3{
		bucket: "zenith-team-test",
		client: client,
		uploader: uploadFunc(func(
			context.Context,
			*awss3.PutObjectInput,
			...func(*manager.Uploader),
		) (*manager.UploadOutput, error) {
			return nil, uploadErr
		}),
	}
}

func (c *providerErrorS3Client) PutObject(
	context.Context,
	*awss3.PutObjectInput,
	...func(*awss3.Options),
) (*awss3.PutObjectOutput, error) {
	return nil, c.err
}

func (c *providerErrorS3Client) GetObject(
	context.Context,
	*awss3.GetObjectInput,
	...func(*awss3.Options),
) (*awss3.GetObjectOutput, error) {
	return nil, c.err
}

func (c *providerErrorS3Client) DeleteObject(
	context.Context,
	*awss3.DeleteObjectInput,
	...func(*awss3.Options),
) (*awss3.DeleteObjectOutput, error) {
	return nil, c.err
}
