package attachment

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"boardly.dev/boardly/server/internal/db"
)

// unusedObjects is object storage a test proves is never reached.
type unusedObjects struct{}

func (unusedObjects) Upload(context.Context, string, io.Reader, int64, string, string) error {
	return errors.New("unexpected upload")
}
func (unusedObjects) DeleteObject(context.Context, string) error {
	return errors.New("unexpected delete")
}
func (unusedObjects) DeleteKeys(context.Context, []string) error {
	return errors.New("unexpected delete")
}
func (unusedObjects) GetReader(context.Context, string) (io.ReadCloser, error) {
	return nil, errors.New("unexpected read")
}
func (unusedObjects) PresignGet(context.Context, string, time.Duration) (string, error) {
	return "", errors.New("unexpected presign")
}

// A Run download names the Run, its file and the person who authorized it. A
// Run with no authorizer, or a request missing any id, downloads nothing and
// never reaches the database: the zero store here has no pool, so a query
// would panic (ADR 0017).
func TestDownloadForRunRefusesWithoutRunOrAuthorizer(t *testing.T) {
	t.Parallel()

	service := &DefaultAttachmentService{store: &db.Store{}, objects: unusedObjects{}}
	complete := RunDownloadInput{
		ID: uuid.New(), CompanyID: uuid.New(), RunID: uuid.New(), AuthorizedByUserID: uuid.New(),
		FrozenProjectDocument: true,
	}
	for name, change := range map[string]func(*RunDownloadInput){
		"no authorizer": func(input *RunDownloadInput) { input.AuthorizedByUserID = uuid.Nil },
		"no Run":        func(input *RunDownloadInput) { input.RunID = uuid.Nil },
		"no file":       func(input *RunDownloadInput) { input.ID = uuid.Nil },
		"no Company":    func(input *RunDownloadInput) { input.CompanyID = uuid.Nil },
	} {
		input := complete
		change(&input)
		if _, err := service.DownloadForRun(context.Background(), input); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: DownloadForRun = %v, want ErrNotFound", name, err)
		}
	}
}
