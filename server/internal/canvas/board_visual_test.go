package canvas

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBoardVisualRegionsGroupNearbyContentWithoutLosingDetail(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		items []BoardItem
		want  int
	}{
		{"empty", nil, 0},
		{"nearby handwriting", []BoardItem{visualTestItem(0, 0, 70, 40), visualTestItem(90, 0, 70, 40)}, 1},
		{"distant groups", []BoardItem{visualTestItem(-10000, -2000, 70, 40), visualTestItem(10000, 8000, 70, 40)}, 2},
		{"wide handwriting stays together", []BoardItem{visualTestItem(0, 0, 950, 80)}, 1},
		{"large drawing", []BoardItem{visualTestItem(0, 0, 2200, 80)}, 2},
		{"frame does not join distant drawings", []BoardItem{
			{ID: uuid.New(), Type: "frame", BoardGeometry: BoardGeometry{X: -100, Y: -100, Width: 20000, Height: 20000}},
			visualTestItem(0, 0, 70, 40), visualTestItem(9000, 9000, 70, 40),
		}, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			regions, err := boardVisualRegions(tt.items)
			if err != nil || len(regions) != tt.want {
				t.Fatalf("regions = %d, %v; want %d", len(regions), err, tt.want)
			}
			for _, region := range regions {
				if region.Width > 1280 || region.Height > 1280 {
					t.Fatalf("oversized region: %+v", region)
				}
			}
			for _, item := range tt.items {
				if item.Type == "frame" {
					continue
				}
				// Sample the whole object, including edges and tile seams.
				for x := item.X; x <= item.X+item.Width; x += item.Width / 20 {
					covered := false
					for _, r := range regions {
						covered = covered ||
							x >= r.X && x <= r.X+r.Width && item.Y >= r.Y &&
								item.Y <= r.Y+r.Height
					}
					if !covered {
						t.Fatalf("object %s missing at x=%v", item.ID, x)
					}
				}
			}
			reversed := slices.Clone(tt.items)
			slices.Reverse(reversed)
			again, err := boardVisualRegions(reversed)
			if err != nil || !slices.Equal(regions, again) {
				t.Fatalf("region order depends on input order: %v", err)
			}
		})
	}
}

func visualTestItem(x, y, w, h float64) BoardItem {
	return BoardItem{ID: uuid.New(), Type: "stroke", BoardGeometry: BoardGeometry{X: x, Y: y, Width: w, Height: h}}
}

func TestBoardVisualImageContainsInkAndCoordinateMapping(t *testing.T) {
	t.Parallel()
	item := visualTestItem(-100, 300, 80, 40)
	item.Properties.Color = "#000000"
	manifest := boardScene{
		Items: []boardSceneItem{{BoardItem: item, Points: []BoardPoint{{X: 6, Y: 6}, {X: 74, Y: 34}}}},
	}
	regions, err := boardVisualRegions([]BoardItem{item})
	if err != nil {
		t.Fatal(err)
	}
	got, err := renderBoardVisual(t.Context(), manifest, nil, regions[0])
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(got.Data))
	if err != nil {
		t.Fatal(err)
	}
	if got.MIMEType != "image/png" || got.PixelWidth != img.Bounds().Dx() || got.PixelHeight != img.Bounds().Dy() ||
		got.PixelWidth > 1280 ||
		got.PixelHeight > 1280 {
		t.Fatalf("invalid image metadata: %+v", got)
	}
	ink := 0
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < 30000 && g < 30000 && b < 30000 {
				ink++
			}
		}
	}
	if ink < 30 || !slices.Contains(got.ItemIDs, item.ID) {
		t.Fatalf("ink = %d, item IDs = %v", ink, got.ItemIDs)
	}
}

func TestBoardVisualsDrawConnectorsAcrossPages(t *testing.T) {
	t.Parallel()
	left, right := uuid.New(), uuid.New()
	items := map[uuid.UUID]BoardItem{
		left:  {ID: left, Type: "shape", BoardGeometry: BoardGeometry{X: 0, Y: 0, Width: 100, Height: 80}},
		right: {ID: right, Type: "shape", BoardGeometry: BoardGeometry{X: 5000, Y: 0, Width: 100, Height: 80}},
	}
	edge := BoardEdge{SourceID: left, TargetID: right}
	page := []BoardVisualBounds{{X: -32, Y: -32, Width: 1280, Height: 1280}}
	scene := boardScene{Items: []boardSceneItem{{BoardItem: items[left]}}, Edges: []BoardEdge{edge}}
	ends := boardVisualConnectorEnds(scene, items, page)
	if len(ends) != 1 || ends[0].ID != right {
		t.Fatalf("ends = %+v, want the off-page target", ends)
	}
	scene.Items = append(scene.Items, ends...)
	img, err := boardRenderRegion(
		t.Context(),
		fullRenderWork(),
		scene,
		nil,
		boardBounds{x: -32, y: -32, w: 1280, h: 1280},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	// The connector leaves the page at its right edge, at the handles' height.
	if pixel := img.RGBAAt(1270, 72); pixel.R > 150 {
		t.Fatalf("connector missing at the page edge: %v", pixel)
	}
	// A page the connector never crosses needs no endpoints.
	far := []BoardVisualBounds{{X: 0, Y: 4000, Width: 500, Height: 500}}
	if ends := boardVisualConnectorEnds(scene, items, far); len(ends) != 0 {
		t.Fatalf("unrelated page ends = %+v", ends)
	}
}

// Rasterizing takes one of a bounded number of slots, so concurrent reads
// queue, and give up with their request, instead of taking every core.
func TestBoardVisualRenderWaitsForASlot(t *testing.T) {
	if err := boardRenders.Acquire(t.Context(), boardRenderSlots); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := renderBoardVisual(ctx, boardScene{}, nil, BoardVisualBounds{Width: 100, Height: 100})
	boardRenders.Release(boardRenderSlots)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the request's deadline while every slot is taken", err)
	}
	if _, err := renderBoardVisual(
		t.Context(),
		boardScene{},
		nil,
		BoardVisualBounds{Width: 100, Height: 100},
	); err != nil {
		t.Fatalf("a free slot still refused the render: %v", err)
	}
}
