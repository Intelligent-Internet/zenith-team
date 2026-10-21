package canvas

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func TestBoardExportTilesKeepTextLayoutAndPDFScale(t *testing.T) {
	t.Parallel()
	manifest := boardScene{Items: []boardSceneItem{{BoardItem: BoardItem{
		Type: "sticky", BoardGeometry: BoardGeometry{X: 90, Y: 20, Width: 240, Height: 190},
		Text: "Tài liệu nguồn\nTiếng Việt đầy đủ", Properties: BoardProperties{Fill: "#fff2a8"},
	}}}}
	whole, err := boardRenderRegion(
		t.Context(),
		fullRenderWork(),
		manifest,
		nil,
		boardBounds{x: 0, y: 0, w: 430, h: 240},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, region := range []boardBounds{{x: 0, y: 0, w: 160, h: 240}, {x: 160, y: 0, w: 270, h: 240}} {
		tile, err := boardRenderRegion(t.Context(), fullRenderWork(), manifest, nil, region, 1)
		if err != nil {
			t.Fatal(err)
		}
		for y := 1; y < tile.Bounds().Dy()-1; y++ {
			for x := 1; x < tile.Bounds().Dx()-1; x++ {
				if whole.RGBAAt(x+int(region.x), y) != tile.RGBAAt(x, y) {
					t.Fatalf("tile x=%v reflowed scene content at (%d,%d)", region.x, x, y)
				}
			}
		}
	}
}

func TestBoardExportCentersConnectorLabelBetweenVisibleHandles(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	manifest := boardScene{
		Items: []boardSceneItem{
			{
				BoardItem: BoardItem{
					ID:            a,
					Type:          "shape",
					BoardGeometry: BoardGeometry{Width: 100, Height: 80},
					Properties:    BoardProperties{Fill: "#ffffff"},
				},
			},
			{
				BoardItem: BoardItem{
					ID:            b,
					Type:          "shape",
					BoardGeometry: BoardGeometry{X: 240, Width: 200, Height: 80},
					Properties:    BoardProperties{Fill: "#ffffff"},
				},
			},
		},
		Edges: []BoardEdge{{SourceID: a, TargetID: b, Label: "Kết nối"}},
	}
	img, err := boardRenderRegion(
		t.Context(),
		fullRenderWork(),
		manifest,
		nil,
		boardBounds{x: 0, y: 0, w: 460, h: 100},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	left, right := img.Bounds().Max.X, 0
	// Inspect the label above the arrowhead's upper edge.
	for y := 10; y < 34; y++ {
		for x := 110; x < 239; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < 50000 && g < 50000 && b < 50000 {
				left = min(left, x)
				right = max(right, x)
			}
		}
	}
	if right == 0 || (left+right)/2 < 166 || (left+right)/2 > 174 {
		t.Fatalf("visible label bounds=(%d,%d), want centered around the 170px handle gap", left, right)
	}
}

func TestBoardExportConnectorLabelMasksDiagonalLine(t *testing.T) {
	t.Parallel()
	a, b := uuid.New(), uuid.New()
	manifest := boardScene{Items: []boardSceneItem{
		{
			BoardItem: BoardItem{
				ID:            a,
				Type:          "shape",
				BoardGeometry: BoardGeometry{Y: 100, Width: 100, Height: 80},
			},
		},
		{
			BoardItem: BoardItem{
				ID:            b,
				Type:          "shape",
				BoardGeometry: BoardGeometry{X: 240, Width: 200, Height: 80},
			},
		},
	}, Edges: []BoardEdge{{SourceID: a, TargetID: b, Label: "A          A"}}}
	img, err := boardRenderRegion(t.Context(), fullRenderWork(), manifest, nil, boardBounds{w: 460, h: 200}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if pixel := img.RGBAAt(176, 86); pixel != (color.RGBA{255, 255, 255, 255}) {
		t.Fatalf("line remains visible through label whitespace: %v", pixel)
	}
}

func TestBoardExportRendersVietnameseImagesAndInk(t *testing.T) {
	t.Parallel()
	id := uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001")
	fileID := uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002")
	source := image.NewRGBA(image.Rect(0, 0, 40, 40))
	for y := range 40 {
		for x := range 40 {
			source.Set(x, y, color.RGBA{220, 40, 30, 255})
		}
	}
	scene := boardScene{
		Items: []boardSceneItem{
			{
				BoardItem: BoardItem{
					ID:            id,
					Type:          "sticky",
					BoardGeometry: BoardGeometry{X: -200, Y: -100, Width: 320, Height: 200},
					Text:          "Tiếng Việt: Nguyễn Trần\nĐổi mới — cộng tác\nDòng thứ ba",
					Properties:    BoardProperties{Fill: "#fff2a8"},
				},
			},
			{
				BoardItem: BoardItem{
					ID:            fileID,
					Type:          "image",
					BoardGeometry: BoardGeometry{X: 500, Y: 200, Width: 200, Height: 200},
					Properties:    BoardProperties{AttachmentID: &fileID},
				},
			},
			{
				BoardItem: BoardItem{
					Type:          "stroke",
					BoardGeometry: BoardGeometry{X: 0, Y: 0, Width: 300, Height: 300},
					Properties:    BoardProperties{Color: "#287bde", StrokeWidth: 8},
				},
				Points: []BoardPoint{{X: 0, Y: 0}, {X: 100, Y: 200}, {X: 300, Y: 250}},
			},
		},
	}
	img, err := boardRenderRegion(
		t.Context(),
		fullRenderWork(),
		scene,
		map[uuid.UUID]image.Image{fileID: source},
		boardBounds{x: -232, y: -132, w: 964, h: 564},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := img.RGBAAt(600+232, 300+132); got.R < 180 || got.G > 90 {
		t.Fatalf("image pixel = %v, want the placed file", got)
	}
	var ink, glyphs int
	for y := range img.Bounds().Dy() {
		for x := range img.Bounds().Dx() {
			pixel := img.RGBAAt(x, y)
			if pixel.B > 150 && pixel.R < 120 {
				ink++
			}
			// Dark glyph pixels inside the sticky.
			if x < 352 && y < 232 && pixel.R < 100 && pixel.G < 100 && pixel.B < 100 {
				glyphs++
			}
		}
	}
	if ink < 100 || glyphs < 50 {
		t.Fatalf("ink=%d glyph=%d pixels, want stroke and Vietnamese text", ink, glyphs)
	}
}

func TestBoardExportSinglePointStroke(t *testing.T) {
	t.Parallel()
	manifest := boardScene{
		Items: []boardSceneItem{
			{
				BoardItem: BoardItem{
					Type:          "stroke",
					BoardGeometry: BoardGeometry{Width: 12, Height: 12},
					Properties:    BoardProperties{Color: "#ee3333", StrokeWidth: 6},
				},
				Points: []BoardPoint{{X: 6, Y: 6}},
			},
		},
	}
	img, err := boardRenderRegion(
		t.Context(),
		fullRenderWork(),
		manifest,
		nil,
		boardBounds{x: -32, y: -32, w: 76, h: 76},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	var painted int
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, _, _ := img.At(x, y).RGBA()
			if r > 40000 && g < 40000 {
				painted++
			}
		}
	}
	if painted < 8 {
		t.Fatalf("painted stroke pixels = %d, want visible click stroke", painted)
	}
}

func TestBoardExportKeepsMultilineTextInsideNote(t *testing.T) {
	t.Parallel()
	manifest := boardScene{
		Items: []boardSceneItem{
			{
				BoardItem: BoardItem{
					Type:          "sticky",
					BoardGeometry: BoardGeometry{Width: 240, Height: 190},
					Text:          "Kế hoạch ra mắt\nÝ tưởng từ Electron — thành viên B\nTiếng Việt đã hoàn tấtB ghi thêm trong lúc A dùng IME.\nDòng dán thứ nhất: tiếng Việt\nDòng thứ hai: thử nghiệm cộng tác\nGõ",
					Properties:    BoardProperties{Fill: "#fff2a8"},
				},
			},
		},
	}
	img, err := boardRenderRegion(
		t.Context(),
		fullRenderWork(),
		manifest,
		nil,
		boardBounds{x: -32, y: -32, w: 304, h: 300},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	// The item sits at the region's 32px margin; no glyph may escape below it.
	for y := 224; y < img.Bounds().Max.Y; y++ {
		for x := 32; x < 272; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r < 65535 || g < 65535 || b < 65535 {
				t.Fatalf("text escaped the note at %d,%d", x, y)
			}
		}
	}
}

func TestBoardExportTextBackgroundMatchesEditor(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name, fill string
		want       color.RGBA
	}{
		{name: "paper", fill: "#dff0fc", want: color.RGBA{223, 240, 252, 255}},
		{name: "none", fill: "#00000000", want: color.RGBA{255, 255, 255, 255}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scene := boardScene{Items: []boardSceneItem{{BoardItem: BoardItem{
				Type:          "text",
				BoardGeometry: BoardGeometry{Width: 200, Height: 100},
				Properties:    BoardProperties{Fill: tt.fill},
			}}}}
			img, err := boardRenderRegion(
				t.Context(),
				fullRenderWork(),
				scene,
				nil,
				boardBounds{w: 200, h: 100},
				1,
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := img.RGBAAt(150, 80); got != tt.want {
				t.Fatalf("background = %v, want %v", got, tt.want)
			}
		})
	}
}

// A file card shows what the browser shows: the file's name and kind.
func TestBoardExportLabelsFileCards(t *testing.T) {
	t.Parallel()
	card := boardSceneItem{BoardItem: BoardItem{
		Type: "file",
		File: &BoardFile{Filename: "Q3-report.pdf", ContentType: "application/pdf"},
	}}
	if got := boardItemLabel(card); got != "Q3-report.pdf\nPDF file" {
		t.Fatalf("file card label = %q", got)
	}
	note := boardSceneItem{
		BoardItem: BoardItem{Type: "sticky", Text: "Ý tưởng", Properties: BoardProperties{Title: "x"}},
	}
	if got := boardItemLabel(note); got != "Ý tưởng" {
		t.Fatalf("note label = %q", got)
	}
}

// A shape rasterizes inside its own bounds, not across the whole tile.
func TestBoardExportShapesRasterizeOnlyTheirBounds(t *testing.T) {
	tile := image.NewRGBA(image.Rect(0, 0, 1280, 1280))
	fill, stroke := color.RGBA{255, 242, 168, 255}, color.RGBA{37, 37, 45, 255}
	result := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for b.Loop() {
			boardShape(tile, fullRenderWork(), 600, 600, 100, 100, "rectangle", fill, stroke, 2)
		}
	})
	if bytes := result.AllocedBytesPerOp(); bytes > 1<<20 {
		t.Fatalf("one 100×100 shape allocates %d bytes; want its bounds only", bytes)
	}
	if tile.RGBAAt(650, 650) != fill || tile.RGBAAt(100, 100) != (color.RGBA{}) {
		t.Fatalf("shape painted %v inside and %v outside", tile.RGBAAt(650, 650), tile.RGBAAt(100, 100))
	}
}

// fullRenderWork is the budget one production region renders within.
func fullRenderWork() *boardRenderWork { return &boardRenderWork{left: boardRenderBudget} }

func testBoardFace(t *testing.T, size float64) font.Face {
	t.Helper()
	parsed, err := opentype.Parse(boardFont)
	if err != nil {
		t.Fatal(err)
	}
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = face.Close() })
	return face
}

// wholeLineWrap is the wrap the renderer used to do, measuring every candidate
// line from its start; incremental measurement must break text identically.
func wholeLineWrap(face font.Face, text string, width, capacity int) ([]string, bool) {
	lines := make([]string, 0, capacity)
	var line strings.Builder
	for _, r := range text {
		if r == '\n' {
			if len(lines) == capacity {
				return lines, true
			}
			lines = append(lines, line.String())
			line.Reset()
			continue
		}
		if line.Len() > 0 && !unicode.Is(unicode.Mn, r) &&
			font.MeasureString(face, line.String()+string(r)).Ceil() > width {
			if len(lines) == capacity {
				return lines, true
			}
			current, remainder := line.String(), ""
			if split := strings.LastIndexAny(current, " \t"); split > 0 {
				current, remainder = current[:split], current[split+1:]
			}
			lines = append(lines, current)
			line.Reset()
			line.WriteString(remainder)
			if unicode.IsSpace(r) && line.Len() == 0 {
				continue
			}
		}
		if len(lines) == capacity {
			return lines, true
		}
		line.WriteRune(r)
	}
	if len(lines) == capacity {
		return lines, line.Len() > 0
	}
	lines = append(lines, line.String())
	return lines, false
}

func TestBoardExportWrapMeasuresLinesIncrementally(t *testing.T) {
	t.Parallel()
	texts := []string{
		"",
		"Tài liệu nguồn\nTiếng Việt đầy đủ",
		"Tie\u0302\u0301ng Vie\u0323\u0302t to\u0302\u0301 hợp dấu rời",
		strings.Repeat("word ", 80),
		strings.Repeat("x", 300),
		"tabs\tand   several   spaces\t\tend",
		"zero\u200bwidth\u200bspace" + strings.Repeat("\u0301", 40),
		"emoji 🌱 outside the font 🚀 " + strings.Repeat("ạ", 50),
		"\n\nleading and trailing newlines\n\n",
	}
	for _, size := range []float64{9, 32} {
		face := testBoardFace(t, size)
		for _, text := range texts {
			for _, width := range []int{1, 37, 120, 400} {
				for _, capacity := range []int{1, 2, 5, 32} {
					want, wantOverflow := wholeLineWrap(face, text, width, capacity)
					got, overflow := boardWrapText(face, text, width, capacity, fullRenderWork())
					if overflow != wantOverflow || !slices.Equal(got, want) {
						t.Fatalf("size %v width %d capacity %d %q:\n got %q %v\nwant %q %v",
							size, width, capacity, text, got, overflow, want, wantOverflow)
					}
				}
			}
		}
	}
}

// Painting only the glyphs that can reach the clip leaves exactly the pixels
// font.Drawer paints for the whole line, and rasterizes only that part.
func TestBoardExportDrawStringPaintsOnlyVisibleGlyphs(t *testing.T) {
	t.Parallel()
	face := testBoardFace(t, 18)
	line := strings.Repeat("Tiếng Việt ạ\u0301 ", 60)
	runes := int64(utf8.RuneCountInString(line))
	clip := image.Rect(60, 10, 240, 100)
	for _, dot := range []fixed.Point26_6{fixed.P(-700, 40), fixed.P(20, 104), fixed.P(50, 12), fixed.P(250, 40)} {
		want := image.NewRGBA(image.Rect(0, 0, 300, 120))
		got := image.NewRGBA(want.Bounds())
		reference := font.Drawer{
			Dst:  boardClippedImage{Image: want, clip: clip},
			Src:  image.Black,
			Face: face,
			Dot:  dot,
		}
		reference.DrawString(line)
		work := fullRenderWork()
		drawer := font.Drawer{
			Dst:  boardClippedImage{Image: got, clip: clip},
			Src:  image.Black,
			Face: face,
			Dot:  dot,
		}
		boardDrawString(&drawer, line, work)
		if !bytes.Equal(got.Pix, want.Pix) {
			t.Fatalf("dot %v: painted pixels differ from font.Drawer", dot)
		}
		// Every rune is at most measured; only the few dozen glyphs that reach
		// the 180 px clip are rasterized.
		if spent := boardRenderBudget - work.left; spent > runes*boardRenderRuneCost+64*boardRenderGlyphCost {
			t.Fatalf("dot %v: drawing spent %d units, as if every glyph were rasterized", dot, spent)
		}
	}
}

// The largest note on one unwrapped line lays out in linear time. Measuring
// the whole line again for every rune took minutes for one 64 KiB note.
func TestBoardExportLaysOutTheLongestNoteOnOneLine(t *testing.T) {
	t.Parallel()
	scene := boardScene{Items: []boardSceneItem{{BoardItem: BoardItem{
		Type: "text", BoardGeometry: BoardGeometry{Width: 1000000, Height: 100},
		Text: strings.Repeat("a", boardMaxNoteText),
	}}}}
	start := time.Now()
	img, err := boardRenderRegion(
		t.Context(),
		fullRenderWork(),
		scene,
		nil,
		boardBounds{x: -32, y: -32, w: 1216, h: 1216},
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("one region took %v", elapsed)
	}
	ink := 0
	for y := 40; y < 70; y++ {
		for x := 44; x < 1216; x++ {
			if pixel := img.RGBAAt(x, y); pixel.R < 128 {
				ink++
			}
		}
	}
	if ink < 500 {
		t.Fatalf("the visible start of the line is missing: %d dark pixels", ink)
	}
}

// Content that could hold a CPU for minutes per region stops at the region's
// work budget, which the read reports as a region it could not render.
func TestBoardExportRegionStopsAtItsWorkBudget(t *testing.T) {
	t.Parallel()
	zigzag := make([]BoardPoint, boardMaxStrokePoints)
	for i := range zigzag {
		zigzag[i] = BoardPoint{X: 6, Y: 6}
		if i%2 == 1 {
			zigzag[i] = BoardPoint{X: 1194, Y: 1194}
		}
	}
	photo := uuid.New()
	media := map[uuid.UUID]image.Image{photo: image.NewRGBA(image.Rect(0, 0, 64, 64))}
	tileSized := BoardGeometry{Width: 1200, Height: 1200}
	stack := func(n int, item boardSceneItem) boardScene {
		scene := boardScene{}
		for range n {
			scene.Items = append(scene.Items, item)
		}
		return scene
	}
	for _, tt := range []struct {
		name  string
		scene boardScene
	}{
		{"ink crossing the tile", stack(1, boardSceneItem{
			BoardItem: BoardItem{Type: "stroke", BoardGeometry: tileSized}, Points: zigzag,
		})},
		{"stacked shapes", stack(5000, boardSceneItem{BoardItem: BoardItem{Type: "shape", BoardGeometry: tileSized}})},
		{"stacked long notes", stack(256, boardSceneItem{BoardItem: BoardItem{
			Type: "sticky", BoardGeometry: tileSized, Text: strings.Repeat("Tiếng Việt ", boardMaxNoteText/14),
		}})},
		{"one image placed many times", stack(5000, boardSceneItem{BoardItem: BoardItem{
			Type: "image", BoardGeometry: tileSized, Properties: BoardProperties{AttachmentID: &photo},
		}})},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			work := &boardRenderWork{left: 1 << 24}
			if _, err := boardRenderRegion(
				t.Context(),
				work,
				tt.scene,
				media,
				boardBounds{w: 1216, h: 1216},
				1,
			); !errors.Is(
				err,
				ErrBoardLimit,
			) {
				t.Fatalf("err = %v, want the region's work limit", err)
			}
		})
	}
}

// A rotated object outside the tile is skipped like any other object instead
// of allocating and resampling a layer of its own size.
func TestBoardExportSkipsRotatedObjectsOutsideTheTile(t *testing.T) {
	t.Parallel()
	scene := boardScene{}
	for i := range 50 {
		scene.Items = append(scene.Items, boardSceneItem{BoardItem: BoardItem{
			Type: "shape",
			Text: "far away",
			BoardGeometry: BoardGeometry{
				X:        20000 + float64(i),
				Y:        20000,
				Width:    3300,
				Height:   3300,
				Rotation: 30,
			},
		}})
	}
	work := fullRenderWork()
	if _, err := boardRenderRegion(t.Context(), work, scene, nil, boardBounds{w: 1216, h: 1216}, 1); err != nil {
		t.Fatal(err)
	}
	if work.left != boardRenderBudget {
		t.Fatalf("rotated objects outside the tile spent %d units", boardRenderBudget-work.left)
	}
}

// A rotated object draws only the part of its layer that rotates into the
// tile, and every tile still shows exactly the pixels the whole region shows.
func TestBoardExportTilesKeepRotatedObjects(t *testing.T) {
	t.Parallel()
	scene := boardScene{Items: []boardSceneItem{
		{BoardItem: BoardItem{
			Type: "shape", Text: "Xoay 17 độ với chữ",
			BoardGeometry: BoardGeometry{X: 150, Y: 30, Width: 220, Height: 140, Rotation: 17},
			Properties:    BoardProperties{Shape: "diamond", Fill: "#ffd6e7", StrokeWidth: 3},
		}},
		{BoardItem: BoardItem{
			Type: "sticky", Text: strings.Repeat("Ghi chú xoay nhiều chữ ", 12),
			BoardGeometry: BoardGeometry{X: 40, Y: 150, Width: 300, Height: 200, Rotation: -30},
		}},
		// Text under every tile corner, where a layer cut too tight would lose
		// the kernel's outer taps.
		{BoardItem: BoardItem{
			Type: "text", Text: strings.Repeat("Chữ dày phủ kín các góc của tile ", 400),
			BoardGeometry: BoardGeometry{X: -300, Y: -300, Width: 1100, Height: 1000, Rotation: 25},
		}},
	}}
	whole, err := boardRenderRegion(t.Context(), fullRenderWork(), scene, nil, boardBounds{w: 480, h: 420}, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, region := range []boardBounds{{w: 240, h: 420}, {x: 240, w: 240, h: 420}} {
		tile, err := boardRenderRegion(t.Context(), fullRenderWork(), scene, nil, region, 1)
		if err != nil {
			t.Fatal(err)
		}
		for y := range tile.Bounds().Dy() {
			for x := range tile.Bounds().Dx() {
				if whole.RGBAAt(x+int(region.x), y) != tile.RGBAAt(x, y) {
					t.Fatalf("tile x=%v differs at (%d,%d)", region.x, x, y)
				}
			}
		}
	}
	// A rotated object far larger than the tile samples about the tile's area.
	sampled := boardRotatedSource(
		image.Rect(0, 0, 1216, 1216),
		-2000,
		-2000,
		5000,
		5000,
		math.Cos(0.5),
		math.Sin(0.5),
	)
	if area := sampled.Dx() * sampled.Dy(); area > 3*1216*1216 {
		t.Fatalf("layer source %v covers %d pixels for a 1216 px tile", sampled, area)
	}
}

func TestBoardExportRegionStopsWhenTheReadIsCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	scene := boardScene{Items: []boardSceneItem{{BoardItem: BoardItem{
		Type: "sticky", BoardGeometry: BoardGeometry{Width: 100, Height: 100}, Text: "note",
	}}}}
	if _, err := boardRenderRegion(ctx, fullRenderWork(), scene, nil, boardBounds{w: 200, h: 200}, 1); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("err = %v, want the cancellation", err)
	}
}
