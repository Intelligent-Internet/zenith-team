package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"boardly.dev/boardly/server/internal/mcp/apiclient"
	"boardly.dev/boardly/server/pkg/studiocanvas"
)

const studioCanvasFilename = "zenith-studio.canvas.json"

type (
	StudioCanvas         = studiocanvas.Canvas
	StudioCanvasNode     = studiocanvas.Node
	StudioCanvasPosition = studiocanvas.Position
	StudioCanvasEdge     = studiocanvas.Edge
)

func validateStudioCanvas(canvas *StudioCanvas, itemCount int) error {
	if itemCount >= maxPresentedOutputItems {
		return errors.New("Studio canvas needs one supporting item slot")
	}
	return studiocanvas.Validate(canvas, itemCount)
}

func (s *DefaultPresentOutputService) uploadStudioCanvas(
	ctx context.Context,
	canvas *StudioCanvas,
) (presentOutputRequestItem, error) {
	snapshot := *canvas
	if snapshot.Edges == nil {
		snapshot.Edges = []StudioCanvasEdge{}
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return presentOutputRequestItem{}, fmt.Errorf("encode studio canvas: %w", err)
	}
	digest := sha256.Sum256(encoded)
	attachmentID, err := s.api.UploadAttachment(ctx, apiclient.AttachmentUpload{
		RequestKey: deterministicAttachmentRequestKey(
			s.runContext.RunID,
			studioCanvasFilename,
			digest[:],
		).String(),
		CompanyID:   s.runContext.CompanyID,
		Filename:    studioCanvasFilename,
		ContentType: "application/vnd.zenith-studio.canvas+json",
		SizeBytes:   int64(len(encoded)),
	}, bytes.NewReader(encoded))
	if err != nil {
		return presentOutputRequestItem{}, fmt.Errorf("upload studio canvas: %w", err)
	}
	return presentOutputRequestItem{Role: "supporting", Label: "Studio canvas", AttachmentID: attachmentID}, nil
}

func isStudioCanvasPath(path string) bool {
	return path == studioCanvasFilename || strings.HasSuffix(path, "/"+studioCanvasFilename)
}

func (s *DefaultPresentOutputService) readStudioCanvasFile(path string) (*StudioCanvas, error) {
	file, upload, err := openPresentedWorkspaceFile(
		s.workspaceRoot,
		s.guard,
		s.runContext.RunID,
		s.runContext.CompanyID,
		path,
	)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if upload.SizeBytes > 1024*1024 {
		return nil, errors.New("Studio canvas file exceeds 1 MiB")
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var canvas StudioCanvas
	if err := decoder.Decode(&canvas); err != nil {
		return nil, fmt.Errorf("read Studio canvas JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("Studio canvas must contain one JSON document")
	}
	return &canvas, nil
}
