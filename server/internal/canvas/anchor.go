package canvas

import (
	"bytes"
	"encoding/json"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// MediaAnchor is immutable review geometry in a source's own coordinate space.
type MediaAnchor struct {
	Type          string        `json:"type"`
	SchemaVersion int           `json:"schemaVersion"`
	Target        AnchorTarget  `json:"target"`
	Surface       string        `json:"surface"`
	PageIndex     *int          `json:"pageIndex,omitempty"`
	TimeMs        *int64        `json:"timeMs,omitempty"`
	Shapes        []AnchorShape `json:"shapes"`
}
type AnchorTarget struct {
	AssetVersionID uuid.UUID  `json:"assetVersionId"`
	OutputItemID   *uuid.UUID `json:"outputItemId,omitempty"`
}
type AnchorShape struct {
	Type   string      `json:"type"`
	Color  string      `json:"color"`
	X      *float64    `json:"x,omitempty"`
	Y      *float64    `json:"y,omitempty"`
	Width  *float64    `json:"width,omitempty"`
	Height *float64    `json:"height,omitempty"`
	Points [][]float64 `json:"points,omitempty"`
}

func parseReviewAnchor(raw json.RawMessage) (*MediaAnchor, error) {
	if len(raw) > 16384 {
		return nil, ErrLimit
	}
	var header struct {
		Type    string     `json:"type"`
		Exact   string     `json:"exact"`
		BlockID *uuid.UUID `json:"blockId"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return nil, ErrInvalid
	}
	if header.Type != "media" {
		if header.Type == "block" && header.BlockID == nil {
			return nil, ErrInvalid
		}
		if header.Type == "textQuote" &&
			(strings.TrimSpace(header.Exact) == "" || len([]rune(header.Exact)) > 1000) {
			return nil, ErrInvalid
		}
		if header.BlockID != nil && *header.BlockID == uuid.Nil {
			return nil, ErrInvalid
		}
		if !slices.Contains([]string{"", "version", "textQuote", "point", "block"}, header.Type) {
			return nil, ErrInvalid
		}
		return nil, nil
	}
	var a MediaAnchor
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&a) != nil || a.SchemaVersion != 1 || a.Target.AssetVersionID == uuid.Nil ||
		(a.Target.OutputItemID != nil && *a.Target.OutputItemID == uuid.Nil) {
		return nil, ErrInvalid
	}
	if !slices.Contains([]string{"image", "pdf", "video"}, a.Surface) ||
		(a.Surface == "pdf") != (a.PageIndex != nil) ||
		(a.Surface == "video") != (a.TimeMs != nil) {
		return nil, ErrInvalid
	}
	if (a.PageIndex != nil && (*a.PageIndex < 0 || *a.PageIndex > 1000000)) ||
		(a.TimeMs != nil && (*a.TimeMs < 0 || *a.TimeMs > 604800000)) {
		return nil, ErrInvalid
	}
	if len(a.Shapes) > 16 || a.Shapes == nil || (a.Surface != "video" && len(a.Shapes) == 0) {
		return nil, ErrLimit
	}
	points := 0
	for _, s := range a.Shapes {
		if !slices.Contains([]string{"amber", "rose", "blue"}, s.Color) {
			return nil, ErrInvalid
		}
		switch s.Type {
		case "point", "rectangle", "ellipse":
			if !unit(s.X) || !unit(s.Y) || len(s.Points) != 0 {
				return nil, ErrInvalid
			}
			if s.Type == "point" {
				if s.Width != nil || s.Height != nil {
					return nil, ErrInvalid
				}
			} else if !unit(s.Width) || !unit(s.Height) || *s.Width <= 0 || *s.Height <= 0 || *s.X+*s.Width > 1.00001 || *s.Y+*s.Height > 1.00001 {
				return nil, ErrInvalid
			}
		case "freehand":
			if s.X != nil || s.Y != nil || s.Width != nil || s.Height != nil || len(s.Points) < 2 {
				return nil, ErrInvalid
			}
			points += len(s.Points)
			for _, p := range s.Points {
				if len(p) != 2 || !unit(&p[0]) || !unit(&p[1]) {
					return nil, ErrInvalid
				}
			}
		default:
			return nil, ErrInvalid
		}
	}
	if points > 512 {
		return nil, ErrLimit
	}
	return &a, nil
}

func unit(v *float64) bool {
	return v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) && *v >= 0 && *v <= 1
}

func anchorSurface(contentType string) string {
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	case contentType == "application/pdf":
		return "pdf"
	}
	return ""
}
