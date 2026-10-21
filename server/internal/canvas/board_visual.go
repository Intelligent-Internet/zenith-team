package canvas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"math"
	"runtime"
	"slices"

	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"

	"github.com/Intelligent-Internet/zenith-team/server/internal/auth"
	generated "github.com/Intelligent-Internet/zenith-team/server/pkg/db/generated"
)

const boardVisualTileSize = 1216.0

type BoardVisualBounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type BoardVisualRegion struct {
	Index       int               `json:"index"`
	Bounds      BoardVisualBounds `json:"bounds"`
	ItemIDs     []uuid.UUID       `json:"itemIds"`
	PixelWidth  int               `json:"pixelWidth"`
	PixelHeight int               `json:"pixelHeight"`
	MIMEType    string            `json:"mimeType"`
	Data        []byte            `json:"data"`
}

type BoardVisuals struct {
	Sequence     int64               `json:"sequence"`
	Regions      []BoardVisualRegion `json:"regions"`
	TotalRegions int                 `json:"totalRegions"`
	NextOffset   *int                `json:"nextOffset"`
	HasMore      bool                `json:"hasMore"`
	// Changed reports that the Board moved past the sequence an earlier page
	// was read at; this page shows the latest committed state.
	Changed bool `json:"changed"`
	// PlanHash identifies the region plan; RegionsShifted reports that it
	// differs from the plan the expected sequence produced, so pages already
	// read may not line up with this one.
	PlanHash       string `json:"planHash"`
	RegionsShifted bool   `json:"regionsShifted"`
	// Overview is the whole Board at low resolution, on the first page only.
	Overview *BoardVisualRegion `json:"overview"`
	// Unavailable explains why no images accompany otherwise complete context.
	Unavailable string `json:"unavailable,omitempty"`
}

// Visuals reads ephemeral images of the acknowledged scene. It never creates a
// file, attachment, operation receipt or other persisted product state.
func (s *DefaultBoardMediaService) Visuals(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	input BoardContextInput,
) (BoardVisuals, error) {
	result := BoardVisuals{Regions: []BoardVisualRegion{}}
	if input.VisualOffset < 0 || len(input.ItemIDs) > boardMaxContextItems ||
		(input.ExpectedSequence != nil && *input.ExpectedSequence < 0) {
		return result, ErrBoardInvalid
	}
	var regions []BoardVisualBounds
	var scene, overview boardScene
	var overviewBounds *BoardVisualBounds
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		board, _, err := boardScopeLock(ctx, q, p, scope, false)
		if err != nil {
			return err
		}
		// Collaborators keep editing between an Agent's tool calls, so later
		// pages render the latest state rather than refusing the read.
		result.Changed = input.ExpectedSequence != nil && board.HeadSeq != *input.ExpectedSequence
		result.Sequence = board.HeadSeq
		snapshot, err := boardSnapshotInTx(ctx, q, p, board, 0)
		if err != nil {
			return err
		}
		everyItem := make(map[uuid.UUID]BoardItem, len(snapshot.Items))
		for _, item := range snapshot.Items {
			everyItem[item.ID] = item
		}
		if len(input.ItemIDs) > 0 {
			wanted := make(map[uuid.UUID]bool, len(input.ItemIDs))
			for _, id := range input.ItemIDs {
				wanted[id] = true
			}
			snapshot.Items = slices.DeleteFunc(
				snapshot.Items,
				func(item BoardItem) bool { return !wanted[item.ID] },
			)
		}
		regions, err = boardVisualRegions(snapshot.Items)
		if err != nil {
			return err
		}
		result.TotalRegions = len(regions)
		result.PlanHash = boardVisualPlanHash(regions)
		result.RegionsShifted = input.ExpectedPlan != "" && input.ExpectedPlan != result.PlanHash
		// The first page of a Board with several regions starts with all of it.
		if input.VisualOffset == 0 && len(regions) > 1 {
			whole := regions[0]
			for _, region := range regions[1:] {
				whole = boardVisualUnion(whole, region)
			}
			overviewBounds = &whole
			overview = boardScene{Edges: snapshot.Edges}
			for _, item := range snapshot.Items {
				overview.Items = append(overview.Items, boardSceneItem{BoardItem: item})
			}
		}
		if input.VisualOffset > len(regions) {
			if !result.Changed {
				return ErrBoardInvalid
			}
			// Content shrank since the earlier page: this read is complete.
			input.VisualOffset = len(regions)
		}
		regions = regions[input.VisualOffset:min(len(regions), input.VisualOffset+4)]
		if len(regions) == 0 {
			return nil
		}
		scene = boardScene{Edges: snapshot.Edges}
		var ids []uuid.UUID
		for _, item := range snapshot.Items {
			for _, bounds := range regions {
				if boardVisualIntersects(boardVisualItemBounds(item), bounds) {
					scene.Items = append(scene.Items, boardSceneItem{BoardItem: item})
					ids = append(ids, item.ID)
					break
				}
			}
		}
		scene.Items = append(scene.Items, boardVisualConnectorEnds(scene, everyItem, regions)...)
		// Hydrate only this page's content, with a byte bound before transferring
		// points. This avoids loading every stroke on a large scattered board.
		// The overview adds the rest of the Board's text and ink when that fits.
		hydrate := func(ids []uuid.UUID) (bool, error) {
			size, err := q.BoardVisualHydrationBytes(
				ctx,
				generated.BoardVisualHydrationBytesParams{
					CompanyID: p.CompanyID,
					BoardID:   board.ID,
					ItemIds:   ids,
				},
			)
			return size <= 16<<20, err
		}
		if overviewBounds != nil {
			every := make([]uuid.UUID, 0, len(overview.Items))
			for _, item := range overview.Items {
				every = append(every, item.ID)
			}
			fits, err := hydrate(every)
			if err != nil {
				return err
			}
			if fits {
				ids = every
			}
		}
		fits, err := hydrate(ids)
		if err != nil {
			return err
		}
		if !fits {
			return ErrBoardLimit
		}
		return boardSceneDetailsInTx(ctx, q, p, board.ID, ids, scene.Items, overview.Items)
	})
	if err != nil {
		return result, boardError(err)
	}
	// Object downloads and rasterization happen after releasing the Board lock.
	media, err := s.visualImages(ctx, p, scope, scene)
	if err != nil {
		return result, err
	}
	bytesUsed, consumed := 0, 0
	var skipped []int
	for i, bounds := range regions {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		// A region that cannot be rendered, or alone exceeds the page budget,
		// is skipped and named, so the pages after it stay reachable.
		region, err := renderBoardVisual(ctx, scene, media, bounds)
		if err != nil || len(region.Data) > 6<<20 {
			skipped = append(skipped, input.VisualOffset+i)
			consumed++
			continue
		}
		if bytesUsed+len(region.Data) > 6<<20 {
			break
		}
		region.Index = input.VisualOffset + i
		result.Regions = append(result.Regions, region)
		bytesUsed += len(region.Data)
		consumed++
	}
	if len(skipped) > 0 {
		result.Unavailable = fmt.Sprintf(
			"Board image regions %v could not be rendered; the text context still covers them.",
			skipped,
		)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if overviewBounds != nil {
		if region, err := renderBoardVisual(ctx, overview, media, *overviewBounds); err == nil &&
			bytesUsed+len(region.Data) <= 6<<20 {
			region.Index, region.ItemIDs = -1, []uuid.UUID{}
			result.Overview = &region
		}
	}
	next := input.VisualOffset + consumed
	result.HasMore = next < result.TotalRegions
	if result.HasMore {
		result.NextOffset = &next
	}
	return result, nil
}

// boardSceneDetailsInTx loads the text and ink of the given items into every
// scene that draws them.
func boardSceneDetailsInTx(
	ctx context.Context,
	q *generated.Queries,
	p auth.UserPrincipal,
	board uuid.UUID,
	ids []uuid.UUID,
	scenes ...[]boardSceneItem,
) error {
	details, err := q.ListBoardVisualHydration(
		ctx,
		generated.ListBoardVisualHydrationParams{CompanyID: p.CompanyID, BoardID: board, ItemIds: ids},
	)
	if err != nil {
		return err
	}
	byID := make(map[uuid.UUID]generated.ListBoardVisualHydrationRow, len(details))
	for _, detail := range details {
		byID[detail.ID] = detail
	}
	for _, items := range scenes {
		for i := range items {
			item := &items[i]
			detail, ok := byID[item.ID]
			if !ok {
				continue
			}
			item.Text, item.TextTruncated = detail.PlainText, false
			if len(detail.Points) > 0 {
				if err := json.Unmarshal(detail.Points, &item.Points); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s *DefaultBoardMediaService) RectVisual(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	area BoardVisualBounds,
	maxPixels int,
) (BoardVisualRegion, error) {
	if area.Width <= 0 || area.Height <= 0 || maxPixels < 64 || maxPixels > 1280 {
		return BoardVisualRegion{}, ErrBoardInvalid
	}
	region, _, err := s.drawBoard(ctx, p, scope, &area, -1, maxPixels)
	if err != nil {
		return BoardVisualRegion{}, err
	}
	return *region, nil
}

// BoardPreview is a small image of a whole Board for its Artifact's card, at
// the Board sequence it shows. NotModified means the client already holds
// that sequence's image; Image is nil when the Board has nothing on it.
type BoardPreview struct {
	Sequence    int64
	NotModified bool
	Image       *BoardVisualRegion
}

// Preview draws all of a Board's content small, framed 16:9 like other
// Artifact thumbnails. known is the sequence of the image a client holds; an
// unchanged Board is not drawn again.
func (s *DefaultBoardMediaService) Preview(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	known int64,
) (BoardPreview, error) {
	image, sequence, err := s.drawBoard(ctx, p, scope, nil, known, boardPreviewPixels)
	if err != nil {
		return BoardPreview{}, err
	}
	return BoardPreview{Sequence: sequence, NotModified: sequence == known, Image: image}, nil
}

// boardPreviewArea frames content 16:9 around its center, at least as large
// as a screenful of Board, so a lone note is not blown up to fill a card.
func boardPreviewArea(content BoardVisualBounds) BoardVisualBounds {
	w := math.Max(content.Width+2*boardPreviewMargin, boardPreviewMinWidth)
	h := math.Max(content.Height+2*boardPreviewMargin, boardPreviewMinWidth*9/16)
	w, h = math.Max(w, h*16/9), math.Max(h, w*9/16)
	return BoardVisualBounds{
		X:      content.X + content.Width/2 - w/2,
		Y:      content.Y + content.Height/2 - h/2,
		Width:  w,
		Height: h,
	}
}

const (
	boardPreviewPixels   = 640
	boardPreviewMargin   = 24.0
	boardPreviewMinWidth = 640.0
)

// drawBoard renders area of the Board, or all of its content framed by
// boardPreviewArea when area is nil, at most maxPixels on the longer side,
// and returns the sequence it read. A Board at sequence known, or with no
// content to frame, is not drawn and has a nil image.
func (s *DefaultBoardMediaService) drawBoard(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	area *BoardVisualBounds,
	known int64,
	maxPixels int,
) (*BoardVisualRegion, int64, error) {
	var scene boardScene
	var sequence int64
	draw := false
	err := s.store.WithinTx(ctx, boardRead, func(q *generated.Queries) error {
		board, _, err := boardScopeLock(ctx, q, p, scope, false)
		if err != nil {
			return err
		}
		sequence = board.HeadSeq
		if sequence == known {
			return nil
		}
		snapshot, err := boardSnapshotInTx(ctx, q, p, board, 0)
		if err != nil {
			return err
		}
		if area == nil {
			if len(snapshot.Items) == 0 {
				return nil
			}
			content := boardVisualItemBounds(snapshot.Items[0])
			for _, item := range snapshot.Items[1:] {
				content = boardVisualUnion(content, boardVisualItemBounds(item))
			}
			framed := boardPreviewArea(content)
			area = &framed
		}
		draw = true
		everyItem := make(map[uuid.UUID]BoardItem, len(snapshot.Items))
		scene = boardScene{Edges: snapshot.Edges}
		var ids []uuid.UUID
		for _, item := range snapshot.Items {
			everyItem[item.ID] = item
			if boardVisualIntersects(boardVisualItemBounds(item), *area) {
				scene.Items = append(scene.Items, boardSceneItem{BoardItem: item})
				ids = append(ids, item.ID)
			}
		}
		scene.Items = append(
			scene.Items,
			boardVisualConnectorEnds(scene, everyItem, []BoardVisualBounds{*area})...)
		size, err := q.BoardVisualHydrationBytes(
			ctx,
			generated.BoardVisualHydrationBytesParams{
				CompanyID: p.CompanyID,
				BoardID:   board.ID,
				ItemIds:   ids,
			},
		)
		if err != nil {
			return err
		}
		if size > 16<<20 {
			return ErrBoardLimit
		}
		return boardSceneDetailsInTx(ctx, q, p, board.ID, ids, scene.Items)
	})
	if err != nil || !draw {
		return nil, sequence, boardError(err)
	}
	// Object downloads and rasterization happen after releasing the Board lock.
	media, err := s.visualImages(ctx, p, scope, scene)
	if err != nil {
		return nil, sequence, err
	}
	region, err := renderBoardVisualAt(ctx, scene, media, *area, float64(maxPixels))
	if err != nil {
		return nil, sequence, err
	}
	return &region, sequence, nil
}

// visualImages decodes each placed image once, from its bounded preview.
func (s *DefaultBoardMediaService) visualImages(
	ctx context.Context,
	p auth.UserPrincipal,
	scope BoardScope,
	scene boardScene,
) (map[uuid.UUID]image.Image, error) {
	media := map[uuid.UUID]image.Image{}
	var pixels int64
	for _, item := range scene.Items {
		if item.Type != "image" || item.Properties.AttachmentID == nil {
			continue
		}
		id := *item.Properties.AttachmentID
		if media[id] != nil {
			continue
		}
		file, err := s.FileDownload(ctx, p, scope, id, true, uuid.Nil)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			// One unreadable file leaves an empty placeholder, not a failed read.
			continue
		}
		img, err := decodeBoardImage(file.Body, &pixels)
		if err != nil {
			continue
		}
		media[id] = img
	}
	return media, nil
}

func boardVisualItemBounds(item BoardItem) BoardVisualBounds {
	a := item.Rotation * math.Pi / 180
	w := math.Abs(math.Cos(a))*item.Width + math.Abs(math.Sin(a))*item.Height
	h := math.Abs(math.Sin(a))*item.Width + math.Abs(math.Cos(a))*item.Height
	padding := math.Max(2, item.Properties.StrokeWidth/2)
	return BoardVisualBounds{
		X:      item.X + (item.Width-w)/2 - padding,
		Y:      item.Y + (item.Height-h)/2 - padding,
		Width:  w + 2*padding,
		Height: h + 2*padding,
	}
}

func boardVisualIntersects(a, b BoardVisualBounds) bool {
	return a.X <= b.X+b.Width && b.X <= a.X+a.Width && a.Y <= b.Y+b.Height && b.Y <= a.Y+a.Height
}

func boardVisualUnion(a, b BoardVisualBounds) BoardVisualBounds {
	x, y := math.Min(a.X, b.X), math.Min(a.Y, b.Y)
	return BoardVisualBounds{
		X:      x,
		Y:      y,
		Width:  math.Max(a.X+a.Width, b.X+b.Width) - x,
		Height: math.Max(a.Y+a.Height, b.Y+b.Height) - y,
	}
}

func boardVisualRegions(items []BoardItem) ([]BoardVisualBounds, error) {
	var content []BoardVisualBounds
	for _, item := range items {
		if item.Type == "frame" {
			// The frame label is content, but its enclosing outline must not
			// join distant drawings into one enormous image.
			w, h := math.Min(item.Width, 320), math.Min(item.Height, 48)
			a := item.Rotation * math.Pi / 180
			dx, dy := (w-item.Width)/2, (h-item.Height)/2
			item.X += item.Width/2 + dx*math.Cos(a) - dy*math.Sin(a) - w/2
			item.Y += item.Height/2 + dx*math.Sin(a) + dy*math.Cos(a) - h/2
			item.Width, item.Height = w, h
		}
		content = append(content, boardVisualItemBounds(item))
	}
	parents := make([]int, len(content))
	for i := range parents {
		parents[i] = i
	}
	root := func(i int) int {
		for parents[i] != i {
			parents[i] = parents[parents[i]]
			i = parents[i]
		}
		return i
	}
	for i, a := range content {
		for j := range i {
			b := content[j]
			dx := math.Max(0, math.Max(a.X-b.X-b.Width, b.X-a.X-a.Width))
			dy := math.Max(0, math.Max(a.Y-b.Y-b.Height, b.Y-a.Y-a.Height))
			if dx*dx+dy*dy <= 160*160 {
				parents[root(i)] = root(j)
			}
		}
	}
	groups := map[int][]BoardVisualBounds{}
	for i, bounds := range content {
		id := root(i)
		groups[id] = append(groups[id], bounds)
	}
	var regions []BoardVisualBounds
	for _, group := range groups {
		bounds := group[0]
		for _, other := range group[1:] {
			bounds = boardVisualUnion(bounds, other)
		}
		// Keep a native-size core and overlapping margins, so splitting a long
		// line or handwriting at a tile boundary does not remove its context.
		// Enumerate occupied cells rather than scanning the empty rectangle
		// between objects in a long or diagonal group.
		cells := map[[2]int]bool{}
		cols := int(math.Ceil(bounds.Width / boardVisualTileSize))
		rows := int(math.Ceil(bounds.Height / boardVisualTileSize))
		cellWidth, cellHeight := bounds.Width/float64(cols), bounds.Height/float64(rows)
		for _, item := range group {
			left := int((item.X - bounds.X) / cellWidth)
			top := int((item.Y - bounds.Y) / cellHeight)
			right := min(cols-1, int((item.X+item.Width-bounds.X)/cellWidth))
			bottom := min(rows-1, int((item.Y+item.Height-bounds.Y)/cellHeight))
			for row := top; row <= bottom; row++ {
				for col := left; col <= right; col++ {
					cells[[2]int{col, row}] = true
					if len(regions)+len(cells) > 8192 {
						return nil, ErrBoardLimit
					}
				}
			}
		}
		for cell := range cells {
			x := bounds.X + float64(cell[0])*cellWidth
			y := bounds.Y + float64(cell[1])*cellHeight
			regions = append(regions, BoardVisualBounds{
				X: x - 32, Y: y - 32,
				Width:  cellWidth + 64,
				Height: cellHeight + 64,
			})
		}
	}
	slices.SortFunc(regions, func(a, b BoardVisualBounds) int {
		for _, pair := range [][2]float64{{a.Y, b.Y}, {a.X, b.X}, {a.Width, b.Width}, {a.Height, b.Height}} {
			if pair[0] < pair[1] {
				return -1
			}
			if pair[0] > pair[1] {
				return 1
			}
		}
		return 0
	})
	return regions, nil
}

// boardRenders lets half the CPUs rasterize regions at once, so Agent images
// never take every core from the API; a read waits for a slot and gives up
// with its request.
var (
	boardRenderSlots = int64(max(1, runtime.GOMAXPROCS(0)/2))
	boardRenders     = semaphore.NewWeighted(boardRenderSlots)
)

func renderBoardVisual(
	ctx context.Context,
	scene boardScene,
	media map[uuid.UUID]image.Image,
	bounds BoardVisualBounds,
) (BoardVisualRegion, error) {
	return renderBoardVisualAt(ctx, scene, media, bounds, 1280)
}

func renderBoardVisualAt(
	ctx context.Context,
	scene boardScene,
	media map[uuid.UUID]image.Image,
	bounds BoardVisualBounds,
	maxPixels float64,
) (BoardVisualRegion, error) {
	result := BoardVisualRegion{Bounds: bounds, ItemIDs: []uuid.UUID{}, MIMEType: "image/png"}
	for _, item := range scene.Items {
		if boardVisualIntersects(boardVisualItemBounds(item.BoardItem), bounds) {
			result.ItemIDs = append(result.ItemIDs, item.ID)
		}
	}
	if err := boardRenders.Acquire(ctx, 1); err != nil {
		return result, err
	}
	defer boardRenders.Release(1)
	scale := math.Min(2, maxPixels/math.Max(bounds.Width, bounds.Height))
	raster, err := boardRenderRegion(
		ctx,
		&boardRenderWork{left: boardRenderBudget},
		scene,
		media,
		boardBounds{x: bounds.X, y: bounds.Y, w: bounds.Width, h: bounds.Height},
		scale,
	)
	if err != nil {
		return result, err
	}
	var data bytes.Buffer
	if err := png.Encode(&data, raster); err != nil {
		return result, err
	}
	result.Data, result.PixelWidth, result.PixelHeight = data.Bytes(), raster.Bounds().Dx(), raster.Bounds().Dy()
	return result, nil
}

// boardVisualConnectorEnds returns the endpoints a page needs to draw every
// connector that crosses its regions. An endpoint outside the regions is drawn
// off the raster, so only the connector's crossing segment is visible.
func boardVisualConnectorEnds(
	scene boardScene,
	items map[uuid.UUID]BoardItem,
	regions []BoardVisualBounds,
) []boardSceneItem {
	present := make(map[uuid.UUID]bool, len(scene.Items))
	for _, item := range scene.Items {
		present[item.ID] = true
	}
	var ends []boardSceneItem
	for _, edge := range scene.Edges {
		source, sourceOK := items[edge.SourceID]
		target, targetOK := items[edge.TargetID]
		if !sourceOK || !targetOK || present[source.ID] && present[target.ID] {
			continue
		}
		path := boardEdgePoints(
			boardItemRect(source.BoardGeometry), boardItemRect(target.BoardGeometry),
			source.Properties.Shape, target.Properties.Shape, edge.notation(), source.ID == target.ID,
		)
		crosses := false
		for i := 1; i < len(path) && !crosses; i++ {
			for _, bounds := range regions {
				if boardSegmentIntersects(path[i-1][0], path[i-1][1], path[i][0], path[i][1], bounds) {
					crosses = true
					break
				}
			}
		}
		if !crosses {
			continue
		}
		for _, end := range []BoardItem{source, target} {
			if !present[end.ID] {
				present[end.ID] = true
				ends = append(ends, boardSceneItem{BoardItem: end})
			}
		}
	}
	return ends
}

// boardSegmentIntersects clips a segment against a rectangle (Liang–Barsky).
func boardSegmentIntersects(x1, y1, x2, y2 float64, b BoardVisualBounds) bool {
	_, _, ok := boardClipSegment(x1, y1, x2, y2, b)
	return ok
}

// boardVisualPlanHash identifies a region plan by its rounded bounds.
func boardVisualPlanHash(regions []BoardVisualBounds) string {
	hash := sha256.New()
	for _, region := range regions {
		fmt.Fprintf(hash, "%.0f,%.0f,%.0f,%.0f;", region.X, region.Y, region.Width, region.Height)
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}
