package canvas

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"path"
	"strings"

	// Board images decode the first frame of animated GIFs.
	_ "image/gif"

	_ "golang.org/x/image/webp" // Register only the supported, dimension-bounded raster decoders.

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/sync/semaphore"
	"golang.org/x/sync/singleflight"

	"boardly.dev/boardly/server/internal/attachment"
	"boardly.dev/boardly/server/internal/auth"
	"boardly.dev/boardly/server/internal/db"
	generated "boardly.dev/boardly/server/pkg/db/generated"
)

// BoardFile is an ordinary Company attachment placed on a Board. Images render
// inline from a bounded preview; every other file renders as a file card.
type BoardFile struct {
	AttachmentID uuid.UUID `json:"attachmentId"`
	Filename     string    `json:"filename"`
	ContentType  string    `json:"contentType"`
	SizeBytes    int64     `json:"sizeBytes"`
	Width        *int32    `json:"width"`
	Height       *int32    `json:"height"`
	HasPreview   bool      `json:"hasPreview"`
}

type BoardFileInput struct {
	AttachmentID uuid.UUID `json:"attachmentId" binding:"required"`
}

// BoardMediaService owns the files a Board references and the images rendered
// from a Board for Agents. Files keep the attachment lifecycle: a Board only
// adds a reference, and ordinary attachment cleanup reclaims unreferenced bytes.
type BoardMediaService interface {
	AttachFile(context.Context, auth.UserPrincipal, uuid.UUID, BoardFileInput) (BoardFile, error)
	// FileDownload opens a placed file, or its preview. known names the file a
	// client already holds; when it is still the file this placement serves,
	// the result is NotModified and no bytes are opened.
	FileDownload(
		ctx context.Context,
		p auth.UserPrincipal,
		scope BoardScope,
		attachmentID uuid.UUID,
		preview bool,
		known uuid.UUID,
	) (attachment.DownloadResult, error)
	PublicFileDownload(context.Context, uuid.UUID, uuid.UUID, bool) (attachment.DownloadResult, error)
	Visuals(context.Context, auth.UserPrincipal, BoardScope, BoardContextInput) (BoardVisuals, error)
	// RectVisual renders one Board rectangle, at most maxPixels on its longer
	// side: what a person looks at, or what an Agent just placed.
	RectVisual(
		ctx context.Context,
		p auth.UserPrincipal,
		scope BoardScope,
		area BoardVisualBounds,
		maxPixels int,
	) (BoardVisualRegion, error)
	// Preview draws the whole Board small for its Artifact's card, unless the
	// Board is still at the sequence known.
	Preview(ctx context.Context, p auth.UserPrincipal, scope BoardScope, known int64) (BoardPreview, error)
}

var _ BoardMediaService = (*DefaultBoardMediaService)(nil)

type DefaultBoardMediaService struct {
	store       *db.Store
	attachments attachment.AttachmentService
}

func NewBoardMediaService(store *db.Store, attachments attachment.AttachmentService) *DefaultBoardMediaService {
	return &DefaultBoardMediaService{store: store, attachments: attachments}
}

func boardFileDTO(row generated.GetBoardFileRow) BoardFile {
	file := BoardFile{
		AttachmentID: row.AttachmentID,
		Filename:     row.Filename,
		ContentType:  row.ContentType,
		SizeBytes:    row.SizeBytes,
		HasPreview:   row.PreviewAttachmentID.Valid,
	}
	if row.Width.Valid {
		file.Width, file.Height = &row.Width.Int32, &row.Height.Int32
	}
	return file
}

func boardImageType(contentType string) bool {
	switch contentType {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

// boardFileAllowed requires a file registered on this Board; image placements
// additionally require an image file.
func boardFileAllowed(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	board uuid.UUID,
	itemType string,
	attachmentID *uuid.UUID,
) error {
	placesFile := itemType == "image" || itemType == "file"
	if placesFile != (attachmentID != nil) {
		return ErrBoardInvalid
	}
	if !placesFile {
		return nil
	}
	file, err := q.GetBoardFile(
		ctx,
		generated.GetBoardFileParams{CompanyID: p.CompanyID, BoardID: board, AttachmentID: *attachmentID},
	)
	if err != nil {
		return boardError(err)
	}
	if itemType == "image" && !boardImageType(file.ContentType) {
		return ErrBoardInvalid
	}
	return nil
}

// boardPreparedFile is an upload validated outside the Board lock.
type boardPreparedFile struct {
	previewID     uuid.NullUUID
	width, height *int32
}

func (s *DefaultBoardMediaService) AttachFile(
	ctx context.Context,
	p auth.UserPrincipal,
	boardID uuid.UUID,
	input BoardFileInput,
) (BoardFile, error) {
	if input.AttachmentID == uuid.Nil {
		return BoardFile{}, ErrBoardInvalid
	}
	var result BoardFile
	var upload generated.Attachment
	// The first pass authorizes and handles retries and Board-to-Board copies
	// cheaply; only a fresh upload continues to image validation.
	err := s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var done bool
		var err error
		result, upload, done, err = boardRegisterKnownFile(ctx, q, p, boardID, input.AttachmentID)
		if err != nil || done {
			return err
		}
		return nil
	})
	if err != nil || result.AttachmentID != uuid.Nil {
		return result, boardError(err)
	}
	// Concurrent attaches of one upload validate it once.
	shared, err, _ := boardUploadPreparation.Do(upload.ID.String(), func() (any, error) {
		return s.prepareUpload(ctx, p, upload)
	})
	if err != nil {
		return BoardFile{}, err
	}
	prepared := shared.(boardPreparedFile)
	err = s.store.WithinTx(ctx, pgx.TxOptions{}, func(q *generated.Queries) error {
		var done bool
		var err error
		result, _, done, err = boardRegisterKnownFile(ctx, q, p, boardID, input.AttachmentID)
		if err != nil || done {
			return err
		}
		if err = q.RegisterBoardFile(
			ctx,
			generated.RegisterBoardFileParams{
				CompanyID:           p.CompanyID,
				BoardID:             boardID,
				AttachmentID:        input.AttachmentID,
				PreviewAttachmentID: prepared.previewID,
				Width:               nullInt32(prepared.width),
				Height:              nullInt32(prepared.height),
				CreatedBy:           p.UserID,
			},
		); err != nil {
			return err
		}
		row, err := q.GetBoardFile(
			ctx,
			generated.GetBoardFileParams{
				CompanyID:    p.CompanyID,
				BoardID:      boardID,
				AttachmentID: input.AttachmentID,
			},
		)
		result = boardFileDTO(row)
		return err
	})
	return result, boardError(err)
}

// boardRegisterKnownFile returns an existing registration, copies a file this
// member can already read on another Board, or locks the member's own unshared
// upload for validation. Anything else is not found: a member cannot place a
// file shared elsewhere (chat, Project) and widen its audience.
func boardRegisterKnownFile(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	boardID, attachmentID uuid.UUID,
) (BoardFile, generated.Attachment, bool, error) {
	if _, err := boardLock(ctx, q, p, boardID, true); err != nil {
		return BoardFile{}, generated.Attachment{}, false, err
	}
	params := generated.GetBoardFileParams{CompanyID: p.CompanyID, BoardID: boardID, AttachmentID: attachmentID}
	existing, err := q.GetBoardFile(ctx, params)
	if err == nil {
		return boardFileDTO(existing), generated.Attachment{}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BoardFile{}, generated.Attachment{}, false, err
	}
	source, err := q.GetReadableBoardFileSource(
		ctx,
		generated.GetReadableBoardFileSourceParams{
			CompanyID:    p.CompanyID,
			AttachmentID: attachmentID,
			UserID:       p.UserID,
		},
	)
	if err == nil {
		if err = q.RegisterBoardFile(
			ctx,
			generated.RegisterBoardFileParams{
				CompanyID:           p.CompanyID,
				BoardID:             boardID,
				AttachmentID:        attachmentID,
				PreviewAttachmentID: source.PreviewAttachmentID,
				Width:               source.Width,
				Height:              source.Height,
				CreatedBy:           p.UserID,
			},
		); err != nil {
			return BoardFile{}, generated.Attachment{}, false, err
		}
		row, err := q.GetBoardFile(ctx, params)
		return boardFileDTO(row), generated.Attachment{}, true, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return BoardFile{}, generated.Attachment{}, false, err
	}
	upload, err := q.LockCanvasUpload(
		ctx,
		generated.LockCanvasUploadParams{CompanyID: p.CompanyID, ID: attachmentID, UploadedByUserID: p.UserID},
	)
	if err != nil {
		return BoardFile{}, generated.Attachment{}, false, boardError(err)
	}
	unshared, err := q.BoardUploadIsUnshared(
		ctx,
		generated.BoardUploadIsUnsharedParams{
			CompanyID:        p.CompanyID,
			ID:               attachmentID,
			UploadedByUserID: p.UserID,
		},
	)
	if err != nil {
		return BoardFile{}, generated.Attachment{}, false, err
	}
	if !unshared {
		return BoardFile{}, generated.Attachment{}, false, ErrBoardForbidden
	}
	return BoardFile{}, upload, false, nil
}

// prepareUpload validates an image's bytes and stores a bounded preview as its
// own attachment. Other file types are referenced as uploaded.
func (s *DefaultBoardMediaService) prepareUpload(
	ctx context.Context,
	p auth.UserPrincipal,
	upload generated.Attachment,
) (boardPreparedFile, error) {
	if !boardImageType(upload.ContentType) {
		return boardPreparedFile{}, nil
	}
	file, err := s.downloadBoardAttachment(ctx, p, upload.ID)
	if err != nil {
		return boardPreparedFile{}, err
	}
	data, err := readBoardImage(file.Body)
	if err != nil {
		return boardPreparedFile{}, err
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return boardPreparedFile{}, ErrBoardInvalid
	}
	if int64(config.Width)*int64(config.Height) > boardMaxImagePixels {
		return boardPreparedFile{}, ErrBoardLimit
	}
	width, height := int32(config.Width), int32(config.Height)
	prepared := boardPreparedFile{width: &width, height: &height}
	release, err := acquireBoardDecode(ctx, boardDecodeWeight(config))
	if err != nil {
		return boardPreparedFile{}, err
	}
	defer release()
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return boardPreparedFile{}, ErrBoardInvalid
	}
	// Small images are their own preview.
	if max(config.Width, config.Height) <= boardPreviewSize && len(data) <= 512<<10 {
		return prepared, nil
	}
	encoded, contentType := encodeBoardPreview(img)
	name := strings.TrimSuffix(upload.Filename, path.Ext(upload.Filename)) + "-preview"
	if contentType == "image/jpeg" {
		name += ".jpg"
	} else {
		name += ".png"
	}
	initiated, err := s.attachments.Initiate(
		ctx,
		attachment.InitiateInput{
			RequestKey:       uuid.NewSHA1(upload.ID, []byte("whiteboard-preview")),
			CompanyID:        p.CompanyID,
			UploadedByUserID: p.UserID,
			Filename:         name,
			ContentType:      contentType,
			SizeBytes:        int64(len(encoded)),
		},
		bytes.NewReader(encoded),
	)
	if err != nil {
		return boardPreparedFile{}, err
	}
	if _, err = s.attachments.Complete(
		ctx,
		attachment.CompleteInput{
			ID:               initiated.Attachment.ID,
			CompanyID:        p.CompanyID,
			UploadedByUserID: p.UserID,
		},
	); err != nil {
		return boardPreparedFile{}, err
	}
	prepared.previewID = uuid.NullUUID{UUID: initiated.Attachment.ID, Valid: true}
	return prepared, nil
}

// boardPreviewSize bounds the longest preview edge; previews are what the
// editor, the public reader and Agent visuals decode.
const boardPreviewSize = 1024

// Image bounds: an uploaded image file and the pixels it may decode to.
const (
	boardMaxImageBytes  = 32 << 20
	boardMaxImagePixels = 32 << 20
)

// boardDecodeBudget is the decoder memory one process spends on Board images
// at once. A single largest image fits; more wait their turn.
const boardDecodeBudget = 512 << 20

var (
	boardDecodes           = semaphore.NewWeighted(boardDecodeBudget)
	boardUploadPreparation singleflight.Group
)

// boardDecodeWeight estimates the bytes image.Decode allocates for an image:
// 16-bit pixels take eight bytes, JPEG (progressive coefficients plus the
// planes) up to sixteen, everything else four.
func boardDecodeWeight(config image.Config) int64 {
	perPixel := int64(4)
	switch config.ColorModel {
	case color.RGBA64Model, color.NRGBA64Model, color.Gray16Model, color.Alpha16Model:
		perPixel = 8
	case color.YCbCrModel, color.CMYKModel:
		perPixel = 16
	}
	return perPixel * int64(config.Width) * int64(config.Height)
}

// acquireBoardDecode reserves decoder memory; a decode the whole budget
// cannot hold is a limit, not a wait.
func acquireBoardDecode(ctx context.Context, weight int64) (func(), error) {
	if weight > boardDecodeBudget {
		return nil, ErrBoardLimit
	}
	if err := boardDecodes.Acquire(ctx, weight); err != nil {
		return nil, err
	}
	return func() { boardDecodes.Release(weight) }, nil
}

func encodeBoardPreview(img image.Image) ([]byte, string) {
	bounds := img.Bounds()
	scale := math.Min(1, float64(boardPreviewSize)/float64(max(bounds.Dx(), bounds.Dy())))
	w, h := max(1, int(math.Round(float64(bounds.Dx())*scale))), max(1, int(math.Round(float64(bounds.Dy())*scale)))
	preview := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(preview, preview.Bounds(), img, bounds, draw.Src, nil)
	var out bytes.Buffer
	if preview.Opaque() {
		_ = jpeg.Encode(&out, preview, &jpeg.Options{Quality: 82})
		return out.Bytes(), "image/jpeg"
	}
	_ = png.Encode(&out, preview)
	return out.Bytes(), "image/png"
}

func readBoardImage(body io.ReadCloser) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, boardMaxImageBytes+1))
	closeErr := body.Close()
	if err != nil || closeErr != nil {
		return nil, errors.Join(err, closeErr)
	}
	if len(data) > boardMaxImageBytes {
		return nil, ErrBoardLimit
	}
	return data, nil
}

// decodeBoardImage decodes one rendered image within an aggregate pixel budget.
func decodeBoardImage(body io.ReadCloser, totalPixels *int64) (image.Image, error) {
	data, err := readBoardImage(body)
	if err != nil {
		return nil, err
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, ErrBoardInvalid
	}
	pixels := int64(config.Width) * int64(config.Height)
	*totalPixels += pixels
	// Previews are at most 1024 px square; sixteen of them bound one read.
	if pixels > boardMaxImagePixels || *totalPixels > 16<<20 {
		return nil, ErrBoardLimit
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ErrBoardInvalid
	}
	return img, nil
}

func (s *DefaultBoardMediaService) FileDownload(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	attachmentID uuid.UUID,
	preview bool,
	known uuid.UUID,
) (attachment.DownloadResult, error) {
	var file uuid.UUID
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		if _, _, err := boardScopeLock(ctx, q, p, scope, false); err != nil {
			return err
		}
		row, err := q.GetBoardFile(
			ctx,
			generated.GetBoardFileParams{
				CompanyID:    p.CompanyID,
				BoardID:      scope.BoardID,
				AttachmentID: attachmentID,
			},
		)
		file = boardFileVariant(row.AttachmentID, row.PreviewAttachmentID, preview)
		return err
	})
	if err != nil {
		return attachment.DownloadResult{}, boardError(err)
	}
	// Board access was just resolved for this placement, which is the
	// attachment's Board audience; a held copy of the same file is current.
	if known == file {
		return attachment.DownloadResult{Attachment: attachment.Response{ID: file}, NotModified: true}, nil
	}
	return s.downloadBoardAttachment(ctx, p, file)
}

func boardFileVariant(original uuid.UUID, preview uuid.NullUUID, wantPreview bool) uuid.UUID {
	if wantPreview && preview.Valid {
		return preview.UUID
	}
	return original
}

func (s *DefaultBoardMediaService) downloadBoardAttachment(
	ctx context.Context,
	p auth.UserPrincipal,
	file uuid.UUID,
) (attachment.DownloadResult, error) {
	result, err := s.attachments.Download(
		ctx,
		attachment.DownloadInput{ID: file, CompanyID: p.CompanyID, RequestedByUserID: p.UserID},
	)
	if errors.Is(err, attachment.ErrNotFound) {
		return attachment.DownloadResult{}, ErrBoardNotFound
	}
	return result, err
}

func nullInt32(value *int32) (result pgtype.Int4) {
	if value != nil {
		result.Int32, result.Valid = *value, true
	}
	return result
}
