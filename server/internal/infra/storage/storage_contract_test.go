package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
)

func TestS3ObjectStorageContract(t *testing.T) {
	t.Parallel()

	store := newS3Store("zenith-team-test", newMemoryS3Client())
	key := "companies/acme/attachments/contract.txt"
	payload := []byte("streamed attachment")
	if err := store.Upload(
		context.Background(), key, &chunkStream{chunks: [][]byte{payload[:8], payload[8:]}},
		int64(len(payload)), "text/plain", "contract.txt",
	); err != nil {
		t.Fatalf("Upload() error = %v", err)
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
		t.Fatalf("stored bytes = %q, want %q", got, payload)
	}

	if err := store.DeleteKeys(context.Background(), []string{key, "already/missing"}); err != nil {
		t.Fatalf("DeleteKeys() error = %v", err)
	}
	if _, err := store.GetReader(context.Background(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetReader() after delete error = %v, want ErrNotFound", err)
	}
}

func TestS3UploadRejectsDeclaredSizeMismatch(t *testing.T) {
	t.Parallel()

	store := newS3Store("zenith-team-test", newMemoryS3Client())
	for name, test := range map[string]struct {
		payload  string
		declared int64
	}{
		"short": {payload: "short", declared: 6},
		"long":  {payload: "too long", declared: 3},
	} {
		t.Run(name, func(t *testing.T) {
			key := "size/" + name
			err := store.Upload(
				context.Background(), key, bytes.NewBufferString(test.payload),
				test.declared, "text/plain", name+".txt",
			)
			if !errors.Is(err, ErrSizeMismatch) {
				t.Fatalf("Upload() error = %v, want ErrSizeMismatch", err)
			}
		})
	}
}

func TestS3ConcurrentUploadHasOneCreateOnlyWinner(t *testing.T) {
	t.Parallel()

	store := newS3Store("zenith-team-test", newMemoryS3Client())
	key := "companies/acme/attachments/concurrent.txt"
	payloads := [][]byte{[]byte("writer a"), []byte("writer b")}
	results := make([]error, len(payloads))
	start := make(chan struct{})
	var writers sync.WaitGroup
	writers.Add(len(payloads))
	for index, payload := range payloads {
		go func() {
			defer writers.Done()
			<-start
			results[index] = store.Upload(
				context.Background(), key, bytes.NewReader(payload), int64(len(payload)),
				"text/plain", "concurrent.txt",
			)
		}()
	}
	close(start)
	writers.Wait()

	winner := -1
	for index, err := range results {
		switch {
		case err == nil:
			if winner >= 0 {
				t.Fatalf("writers %d and %d both succeeded", winner, index)
			}
			winner = index
		case errors.Is(err, ErrAlreadyExists):
		default:
			t.Fatalf("writer %d error = %v", index, err)
		}
	}
	if winner < 0 {
		t.Fatalf("no writer succeeded: %v", results)
	}
	reader, err := store.GetReader(context.Background(), key)
	if err != nil {
		t.Fatalf("GetReader() error = %v", err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || !bytes.Equal(got, payloads[winner]) {
		t.Fatalf("stored bytes = %q, read=%v, close=%v", got, readErr, closeErr)
	}
}

func TestS3CanceledUploadDoesNotReadOrPublish(t *testing.T) {
	t.Parallel()

	store := newS3Store("zenith-team-test", newMemoryS3Client())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	read := false
	err := store.Upload(ctx, "companies/acme/attachments/canceled.txt", readerFunc(func([]byte) (int, error) {
		read = true
		return 0, io.EOF
	}), 0, "text/plain", "canceled.txt")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Upload() error = %v, want context cancellation", err)
	}
	if read {
		t.Fatal("Upload() read its source after cancellation")
	}
	if _, err := store.GetReader(
		context.Background(), "companies/acme/attachments/canceled.txt",
	); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetReader() error = %v, want ErrNotFound", err)
	}
}

type chunkStream struct {
	chunks [][]byte
	index  int
}

type readerFunc func([]byte) (int, error)

func (r readerFunc) Read(destination []byte) (int, error) { return r(destination) }

func (r *chunkStream) Read(destination []byte) (int, error) {
	if r.index >= len(r.chunks) {
		return 0, io.EOF
	}
	read := copy(destination, r.chunks[r.index])
	if read == len(r.chunks[r.index]) {
		r.index++
	} else {
		r.chunks[r.index] = r.chunks[r.index][read:]
	}
	return read, nil
}
