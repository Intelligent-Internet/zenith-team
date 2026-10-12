package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsv4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

type s3Client interface {
	PutObject(context.Context, *awss3.PutObjectInput, ...func(*awss3.Options)) (*awss3.PutObjectOutput, error)
	GetObject(context.Context, *awss3.GetObjectInput, ...func(*awss3.Options)) (*awss3.GetObjectOutput, error)
	DeleteObject(
		context.Context,
		*awss3.DeleteObjectInput,
		...func(*awss3.Options),
	) (*awss3.DeleteObjectOutput, error)
	AbortMultipartUpload(
		context.Context,
		*awss3.AbortMultipartUploadInput,
		...func(*awss3.Options),
	) (*awss3.AbortMultipartUploadOutput, error)
}

type s3Uploader interface {
	Upload(
		context.Context,
		*awss3.PutObjectInput,
		...func(*manager.Uploader),
	) (*manager.UploadOutput, error)
}

type s3Presigner interface {
	PresignGetObject(
		context.Context,
		*awss3.GetObjectInput,
		...func(*awss3.PresignOptions),
	) (*awsv4.PresignedHTTPRequest, error)
}

type S3 struct {
	bucket    string
	client    s3Client
	uploader  s3Uploader
	presigner s3Presigner
}

type S3Config struct {
	Bucket          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	UsePathStyle    bool
}

func NewS3(ctx context.Context, config S3Config) (*S3, error) {
	config.Bucket = strings.TrimSpace(config.Bucket)
	config.Region = strings.TrimSpace(config.Region)
	config.Endpoint = strings.TrimSpace(config.Endpoint)
	if config.Bucket == "" {
		return nil, errors.New("S3 bucket is required")
	}
	if config.Region == "" {
		return nil, errors.New("S3 region is required")
	}
	if (config.AccessKeyID == "") != (config.SecretAccessKey == "") {
		return nil, errors.New("S3 access key ID and secret access key must be configured together")
	}
	if config.Endpoint != "" {
		parsed, err := url.Parse(config.Endpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("S3 endpoint must be an absolute HTTP or HTTPS URL")
		}
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
		parsed.RawPath = ""
		config.Endpoint = parsed.String()
	}

	loadOptions := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(config.Region),
	}
	if config.AccessKeyID != "" {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				config.AccessKeyID,
				config.SecretAccessKey,
				config.SessionToken,
			),
		))
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, s3ProviderFailure(ctx, "load S3 configuration", err)
	}
	client := awss3.NewFromConfig(awsConfig, func(options *awss3.Options) {
		options.UsePathStyle = config.UsePathStyle
		if config.Endpoint != "" {
			options.BaseEndpoint = aws.String(config.Endpoint)
		}
	})
	uploader := manager.NewUploader(
		client,
		func(options *manager.Uploader) {
			options.PartSize = 8 * 1024 * 1024
			options.Concurrency = 2
		},
	)
	return &S3{
		bucket: config.Bucket, client: client, uploader: uploader,
		presigner: awss3.NewPresignClient(client),
	}, nil
}

func newS3Store(bucket string, client s3Client) *S3 {
	return &S3{
		bucket: bucket, client: client, uploader: directS3Uploader{client: client},
	}
}

func (s *S3) Upload(
	ctx context.Context,
	key string,
	source io.Reader,
	sizeBytes int64,
	contentType string,
	filename string,
) error {
	if ctx == nil || source == nil || sizeBytes < 0 || strings.ContainsAny(contentType+filename, "\r\n") {
		return ErrInvalidUpload
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateObjectKey(key); err != nil {
		return err
	}
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: source}, N: sizeBytes + 1}
	counted := &countingReader{reader: limited}
	input := &awss3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        counted,
		IfNoneMatch: aws.String("*"),
	}
	if contentType != "" {
		input.ContentType = aws.String(contentType)
	}
	if filename != "" {
		input.ContentDisposition = aws.String(mime.FormatMediaType("attachment", map[string]string{
			"filename": filename,
		}))
	}
	output, err := s.uploader.Upload(ctx, input)
	cleanupErr := s.abortFailedMultipart(ctx, key, err)
	if contextErr := ctx.Err(); contextErr != nil {
		return joinStorageError(contextErr, cleanupErr)
	}
	if isS3AlreadyExists(err) {
		return joinStorageError(ErrAlreadyExists, cleanupErr)
	}
	if isS3RetryableConflict(err) {
		return joinStorageError(ErrRetryableConflict, cleanupErr)
	}
	if err != nil {
		return joinStorageError(s3ProviderFailure(ctx, "create S3 object", err), cleanupErr)
	}
	if output == nil {
		return s3ProviderFailure(ctx, "create S3 object", errors.New("invalid provider response"))
	}
	if counted.read != sizeBytes {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		deletionErr := s.DeleteObject(cleanupCtx, key)
		cancel()
		if deletionErr != nil {
			return errors.Join(ErrSizeMismatch, ErrUploadCleanupFailed)
		}
		return ErrSizeMismatch
	}
	return nil
}

func (s *S3) abortFailedMultipart(ctx context.Context, key string, uploadErr error) error {
	if uploadErr == nil {
		return nil
	}
	var failure manager.MultiUploadFailure
	if !errors.As(uploadErr, &failure) || failure.UploadID() == "" {
		return nil
	}
	if s.client == nil {
		return ErrMultipartCleanupFailed
	}

	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, err := s.client.AbortMultipartUpload(cleanupCtx, &awss3.AbortMultipartUploadInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(failure.UploadID()),
	})
	if err == nil || isS3NoSuchUpload(err) {
		return nil
	}
	return ErrMultipartCleanupFailed
}

func joinStorageError(operationErr, cleanupErr error) error {
	if cleanupErr == nil {
		return operationErr
	}
	return errors.Join(operationErr, cleanupErr)
}

func isS3NoSuchUpload(err error) bool {
	var apiError smithy.APIError
	if errors.As(err, &apiError) && apiError.ErrorCode() == "NoSuchUpload" {
		return true
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == 404
}

func isS3AlreadyExists(err error) bool {
	if err == nil {
		return false
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		if apiError.ErrorCode() == "PreconditionFailed" {
			return true
		}
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == 412
}

func isS3RetryableConflict(err error) bool {
	if err == nil {
		return false
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) && apiError.ErrorCode() == "ConditionalRequestConflict" {
		return true
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == 409
}

// directS3Uploader keeps the object client independently testable. Production
// uses manager.Uploader so callers may provide a non-seekable streaming reader.
type directS3Uploader struct {
	client s3Client
}

func (u directS3Uploader) Upload(
	ctx context.Context,
	input *awss3.PutObjectInput,
	_ ...func(*manager.Uploader),
) (*manager.UploadOutput, error) {
	output, err := u.client.PutObject(ctx, input)
	if err != nil {
		return nil, err
	}
	return &manager.UploadOutput{ETag: output.ETag, Key: input.Key}, nil
}

func (s *S3) GetReader(ctx context.Context, key string) (io.ReadCloser, error) {
	if ctx == nil {
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateObjectKey(key); err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if contextErr := ctx.Err(); contextErr != nil {
		closeS3GetObjectBody(output)
		return nil, contextErr
	}
	if isS3NotFound(err) {
		closeS3GetObjectBody(output)
		return nil, ErrNotFound
	}
	if err != nil {
		closeS3GetObjectBody(output)
		return nil, s3ProviderFailure(ctx, "get S3 object", err)
	}
	if output == nil || output.Body == nil {
		closeS3GetObjectBody(output)
		return nil, s3ProviderFailure(ctx, "get S3 object", errors.New("invalid provider response"))
	}
	return output.Body, nil
}

func closeS3GetObjectBody(output *awss3.GetObjectOutput) {
	if output != nil && output.Body != nil {
		_ = output.Body.Close()
	}
}

func (s *S3) DeleteObject(ctx context.Context, key string) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateObjectKey(key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	if err != nil && !isS3NotFound(err) {
		return s3ProviderFailure(ctx, "delete S3 object", err)
	}
	return nil
}

func (s *S3) DeleteKeys(ctx context.Context, keys []string) error {
	if ctx == nil {
		return context.Canceled
	}
	var combined error
	for _, key := range keys {
		if err := s.DeleteObject(ctx, key); err != nil {
			combined = errors.Join(combined, err)
		}
	}
	return combined
}

func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return s.presignGet(ctx, key, ttl, "")
}

func (s *S3) PresignGetWithContentDisposition(
	ctx context.Context,
	key string,
	ttl time.Duration,
	contentDisposition string,
) (string, error) {
	if contentDisposition == "" || strings.ContainsAny(contentDisposition, "\r\n") {
		return "", ErrInvalidUpload
	}
	return s.presignGet(ctx, key, ttl, contentDisposition)
}

func (s *S3) presignGet(
	ctx context.Context,
	key string,
	ttl time.Duration,
	contentDisposition string,
) (string, error) {
	if ctx == nil || ttl <= 0 || ttl > 7*24*time.Hour || s == nil || s.presigner == nil {
		return "", ErrInvalidUpload
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateObjectKey(key); err != nil {
		return "", err
	}
	input := &awss3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)}
	if contentDisposition != "" {
		input.ResponseContentDisposition = aws.String(contentDisposition)
	}
	result, err := s.presigner.PresignGetObject(ctx, input, func(options *awss3.PresignOptions) {
		options.Expires = ttl
	})
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	if err != nil {
		return "", s3ProviderFailure(ctx, "presign S3 object", err)
	}
	if result == nil || result.URL == "" {
		return "", s3ProviderFailure(ctx, "presign S3 object", errors.New("invalid provider response"))
	}
	return result.URL, nil
}

func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		return apiError.ErrorCode() == "NoSuchKey" || apiError.ErrorCode() == "NotFound"
	}
	var responseError *smithyhttp.ResponseError
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == http.StatusNotFound
}

func s3ProviderFailure(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, sentinel := range []error{
		ErrAlreadyExists,
		ErrInvalidKey,
		ErrInvalidUpload,
		ErrMultipartCleanupFailed,
		ErrNotFound,
		ErrRetryableConflict,
		ErrSizeMismatch,
		ErrUploadCleanupFailed,
	} {
		if errors.Is(err, sentinel) {
			return sentinel
		}
	}
	return fmt.Errorf("%s: %s", operation, s3ProviderErrorClass(err))
}

func s3ProviderErrorClass(err error) string {
	var responseError *smithyhttp.ResponseError
	if errors.As(err, &responseError) {
		switch status := responseError.HTTPStatusCode(); {
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			return "authentication_failed"
		case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
			return "provider_unavailable"
		default:
			return "provider_failure"
		}
	}
	var apiError smithy.APIError
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "AccessDenied", "ExpiredToken", "InvalidAccessKeyId", "InvalidToken", "SignatureDoesNotMatch":
			return "authentication_failed"
		case "InternalError",
			"RequestTimeout",
			"ServiceUnavailable",
			"SlowDown",
			"Throttling",
			"ThrottlingException":
			return "provider_unavailable"
		}
	}
	return "provider_failure"
}

type countingReader struct {
	reader io.Reader
	read   int64
}

func (r *countingReader) Read(destination []byte) (int, error) {
	read, err := r.reader.Read(destination)
	r.read += int64(read)
	return read, err
}
