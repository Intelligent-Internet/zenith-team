package canvas

import (
	"context"
	_ "embed"
	"encoding/hex"
	"image"
	"image/color"
	"image/draw"
	"math"
	"path"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/f64"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

// Noto Sans is redistributed under the adjacent SIL Open Font License.
// Source: https://github.com/notofonts/noto-fonts/tree/main/hinted/ttf/NotoSans
//
//go:embed fonts/NotoSans-Regular.ttf
var boardFont []byte

// boardScene is the committed content one rendered region draws: items with
// their hydrated text and stroke points, and the explicit connectors.
type boardScene struct {
	Items []boardSceneItem
	Edges []BoardEdge
}
type boardSceneItem struct {
	BoardItem
	Points []BoardPoint
}
type boardBounds struct{ x, y, w, h float64 }

// boardRenderWork bounds what rasterizing one region may cost, in units of
// about three nanoseconds: one filled pixel or one resampling kernel tap.
// Measuring text, glyphs, ink, shapes, images and rotated layers all spend
// it, so no Board content holds a CPU for more than about two seconds per
// region. Past the budget every drawing call is skipped and the region is
// a limit, reported like any region that cannot be rendered.
type boardRenderWork struct{ left int64 }

const (
	boardRenderBudget = 1 << 29
	// Measured on the bundled font, in budget units.
	boardRenderRuneCost  = 50  // measuring one rune
	boardRenderGlyphCost = 400 // rasterizing one glyph
)

// spend reports whether units more work fit; once they do not, nothing does.
func (w *boardRenderWork) spend(units int64) bool {
	w.left -= units
	return w.left >= 0
}

func (w *boardRenderWork) exhausted() bool { return w.left < 0 }

func boardRenderRegion(
	ctx context.Context,
	work *boardRenderWork,
	scene boardScene,
	media map[uuid.UUID]image.Image,
	bounds boardBounds,
	scale float64,
) (*image.RGBA, error) {
	dst := image.NewRGBA(
		image.Rect(0, 0, max(1, int(math.Ceil(bounds.w*scale))), max(1, int(math.Ceil(bounds.h*scale)))),
	)
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	parsed, err := boardParsedFont()
	if err != nil {
		return nil, err
	}
	face, err := opentype.NewFace(
		parsed,
		&opentype.FaceOptions{Size: math.Max(1, 12*scale), DPI: 72, Hinting: font.HintingNone},
	)
	if err != nil {
		return nil, err
	}
	defer face.Close()
	point := func(x, y float64) (float64, float64) { return (x - bounds.x) * scale, (y - bounds.y) * scale }
	items := append([]boardSceneItem(nil), scene.Items...)
	// Frames and sequence fragments sit under everything else, as in the
	// editor.
	underneath := func(item boardSceneItem) bool {
		return item.Type == "frame" || item.Properties.Shape == BoardShapeTypeFragment
	}
	sort.SliceStable(items, func(i, j int) bool {
		if underneath(items[i]) != underneath(items[j]) {
			return underneath(items[i])
		}
		return items[i].StackOrder < items[j].StackOrder
	})
	byID := map[uuid.UUID]boardSceneItem{}
	for _, item := range items {
		byID[item.ID] = item
	}
	type edgeLabel struct {
		text        string
		x, y, width float64
	}
	var labels []edgeLabel
	ink := color.RGBA{75, 85, 99, 255}
	for _, edge := range scene.Edges {
		a, aok := byID[edge.SourceID]
		b, bok := byID[edge.TargetID]
		if !aok || !bok {
			continue
		}
		style := edge.notation()
		path := boardEdgePoints(
			boardItemRect(a.BoardGeometry), boardItemRect(b.BoardGeometry),
			a.Properties.Shape, b.Properties.Shape, style, a.ID == b.ID,
		)
		for i := range path {
			path[i][0], path[i][1] = point(path[i][0], path[i][1])
		}
		width := math.Max(1, 2*scale)
		dash, gap := 0.0, 0.0
		if style.Line == BoardLineTypeDashed {
			dash, gap = boardDashLength*scale, boardDashGap*scale
		}
		boardPolyline(dst, work, path, width, ink, dash, gap)
		ends := [2][2][2]float64{{path[0], path[1]}, {path[len(path)-1], path[len(path)-2]}}
		for i, marker := range []BoardMarkerType{style.Start, style.End} {
			tip, from := ends[i][0], ends[i][1]
			if length := math.Hypot(tip[0]-from[0], tip[1]-from[1]); length > 0 {
				ux, uy := (tip[0]-from[0])/length, (tip[1]-from[1])/length
				boardMarker(dst, work, marker, tip[0], tip[1], ux, uy, scale, width, ink)
			}
		}
		texts := []string{edge.Label, style.SourceLabel, style.TargetLabel}
		if !work.spend(boardRenderRuneCost * int64(len(texts[0])+len(texts[1])+len(texts[2]))) {
			break
		}
		if edge.Label != "" {
			width := math.Min(300*scale, float64(font.MeasureString(face, edge.Label).Ceil()))
			mx, my := boardPathMiddle(path)
			x, y := mx-width/2, my-6*scale
			switch {
			case a.ID == b.ID:
				x, y = path[1][0]+6*scale, (path[1][1]+path[2][1])/2+4*scale
			case style.Route == BoardRouteTypeMessage:
				y = my - 8*scale
			}
			labels = append(labels, edgeLabel{text: edge.Label, x: x, y: y, width: width})
		}
		// End labels sit just off each end, wholly beside the line, so they
		// cover neither it nor its marker.
		for i, text := range texts[1:] {
			if text == "" {
				continue
			}
			tip, from := ends[i][0], ends[i][1]
			length := math.Hypot(from[0]-tip[0], from[1]-tip[1])
			if length == 0 {
				continue
			}
			ux, uy := (from[0]-tip[0])/length, (from[1]-tip[1])/length
			nx, ny := -uy, ux
			width := math.Min(120*scale, float64(font.MeasureString(face, text).Ceil()))
			ascent, descent := float64(face.Metrics().Ascent.Ceil()), float64(face.Metrics().Descent.Ceil())
			x, y := tip[0]+ux*18*scale+nx*10*scale, tip[1]+uy*18*scale+ny*10*scale
			labels = append(labels, edgeLabel{
				text:  text,
				x:     x + 4 + boardEndLabelAlign(nx)*(width+8),
				y:     y + boardEndLabelAlign(ny)*(ascent+descent+4) + ascent + 2,
				width: width,
			})
		}
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if work.exhausted() {
			return nil, ErrBoardLimit
		}
		x, y := point(item.X, item.Y)
		w, h := item.Width*scale, item.Height*scale
		// The object's extent rotated about its center, with room for its
		// outline and the resampling kernel; an object that misses the tile
		// draws nothing here.
		a := item.Rotation * math.Pi / 180
		cos, sin := math.Cos(a), math.Sin(a)
		ex, ey := w, h
		if item.Rotation != 0 {
			pad := math.Max(1, item.Properties.StrokeWidth*scale) + 2
			ex = math.Abs(cos)*w + math.Abs(sin)*h + 2*pad
			ey = math.Abs(sin)*w + math.Abs(cos)*h + 2*pad
		}
		ox, oy := x+(w-ex)/2, y+(h-ey)/2
		if item.Type != "stroke" &&
			(ox+ex < 0 || oy+ey < 0 || ox > float64(dst.Bounds().Dx()) || oy > float64(dst.Bounds().Dy())) {
			continue
		}
		stroke := boardColor(item.Properties.Color, color.RGBA{51, 65, 85, 255})
		fill := boardColor(item.Properties.Fill, color.RGBA{241, 245, 249, 255})
		if item.Type == "sticky" && item.Properties.Fill == "" {
			fill = color.RGBA{255, 241, 168, 255}
		}
		if item.Type == "stroke" {
			strokeWidth := item.Properties.StrokeWidth
			if strokeWidth == 0 {
				strokeWidth = 3
			}
			width := math.Max(1, strokeWidth*scale)
			if item.Properties.Brush == "highlighter" {
				stroke.A = 90
			}
			strokePoint := boardStrokeTransform(item)
			if len(item.Points) == 1 {
				px, py := strokePoint(item.Points[0])
				px, py = point(px, py)
				boardShape(
					dst,
					work,
					px-width/2,
					py-width/2,
					width,
					width,
					"ellipse",
					stroke,
					stroke,
					0,
				)
			}
			for i := 1; i < len(item.Points) && !work.exhausted(); i++ {
				ax, ay := strokePoint(item.Points[i-1])
				bx, by := strokePoint(item.Points[i])
				ax, ay = point(ax, ay)
				bx, by = point(bx, by)
				boardLine(dst, work, ax, ay, bx, by, width, stroke)
			}
			continue
		}
		layer := dst
		lx, ly := x, y
		if item.Rotation != 0 {
			if w*h > 16<<20 {
				return nil, ErrBoardLimit
			}
			// The layer keeps the object's own coordinates but holds only the
			// part that rotates into the tile.
			sampled := boardRotatedSource(dst.Bounds(), x, y, w, h, cos, sin)
			if sampled.Empty() {
				continue
			}
			if !work.spend(int64(sampled.Dx()) * int64(sampled.Dy())) {
				return nil, ErrBoardLimit
			}
			layer = image.NewRGBA(sampled)
			lx, ly = 0, 0
		}
		if item.Type == "shape" && item.Properties.Shape != "" {
			if err := boardDrawShapeItem(
				layer,
				work,
				item,
				lx,
				ly,
				w,
				h,
				scale,
				fill,
				stroke,
				face,
				parsed,
			); err != nil {
				return nil, err
			}
			if item.Rotation != 0 {
				if err := boardRotateLayer(
					dst,
					work,
					layer,
					x,
					y,
					w,
					h,
					ox,
					oy,
					ex,
					ey,
					cos,
					sin,
				); err != nil {
					return nil, err
				}
			}
			continue
		}
		// Plain text draws no box; a text background renders like the editor's
		// paper, without an outline.
		paper := item.Type == "text" && item.Properties.Fill != "" && fill.A > 0
		if item.Type != "text" || paper {
			outline := stroke
			if item.Type == "frame" {
				fill.A = 25
			}
			if paper {
				outline = fill
			}
			boardShape(
				layer,
				work,
				lx,
				ly,
				w,
				h,
				string(item.Properties.Shape),
				fill,
				outline,
				math.Max(1, item.Properties.StrokeWidth*scale),
			)
		}
		if item.Type == "image" && item.Properties.AttachmentID != nil {
			if img := media[*item.Properties.AttachmentID]; img != nil {
				boardPlaceImage(
					layer,
					work,
					image.Rect(int(lx), int(ly), int(lx+w), int(ly+h)),
					img,
					dst.Bounds(),
				)
			}
		}
		text := boardItemLabel(item)
		if item.Type != "image" {
			// Layout uses the whole item. Only painting is clipped to the tile;
			// intersecting here would reflow a note differently on each PDF page.
			textBounds := image.Rect(
				int(lx+12*scale),
				int(ly+12*scale),
				int(lx+w-12*scale),
				int(ly+h-12*scale),
			)
			if err := boardDrawItemText(
				layer,
				work,
				parsed,
				text,
				textBounds,
				scale,
				stroke,
				false,
			); err != nil {
				return nil, err
			}
		}

		if item.Rotation != 0 {
			if err := boardRotateLayer(dst, work, layer, x, y, w, h, ox, oy, ex, ey, cos, sin); err != nil {
				return nil, err
			}
		}
	}
	for _, label := range labels {
		if label.text == "" {
			continue
		}
		lineHeight := max(1, int(17*scale))
		lines, _ := boardWrapText(face, label.text, max(1, int(label.width)), 32, work)
		labelBounds := image.Rect(
			int(label.x)-4, int(label.y)-face.Metrics().Ascent.Ceil()-2,
			int(math.Ceil(label.x+label.width))+4,
			int(label.y)+(len(lines)-1)*lineHeight+face.Metrics().Descent.Ceil()+2,
		).Intersect(dst.Bounds())
		if !work.spend(int64(labelBounds.Dx()) * int64(labelBounds.Dy())) {
			break
		}
		draw.Draw(dst, labelBounds, image.White, image.Point{}, draw.Src)
		drawer := font.Drawer{Dst: dst, Src: image.NewUniform(color.RGBA{30, 41, 59, 255}), Face: face}
		for index, line := range lines {
			drawer.Dot = fixed.P(int(label.x), int(label.y+float64(index*lineHeight)))
			boardDrawString(&drawer, line, work)
		}
	}
	if work.exhausted() {
		return nil, ErrBoardLimit
	}
	return dst, nil
}

// boardRotateLayer draws an object's layer onto the tile, rotated about its
// center. Rotating at the tile's scale samples sixteen layer pixels for each
// pixel it draws.
func boardRotateLayer(
	dst *image.RGBA,
	work *boardRenderWork,
	layer *image.RGBA,
	x, y, w, h, ox, oy, ex, ey, cos, sin float64,
) error {
	drawn := image.Rect(int(math.Floor(ox)), int(math.Floor(oy)), int(math.Ceil(ox+ex)), int(math.Ceil(oy+ey))).
		Intersect(dst.Bounds())
	if !work.spend(16 * int64(drawn.Dx()) * int64(drawn.Dy())) {
		return ErrBoardLimit
	}
	xdraw.CatmullRom.Transform(
		dst,
		[6]float64{cos, -sin, x + w/2 - cos*w/2 + sin*h/2, sin, cos, y + h/2 - sin*w/2 - cos*h/2},
		layer,
		layer.Bounds(),
		draw.Over,
		nil,
	)
	return nil
}

// boardDrawShapeItem draws a shape of a named kind and its text: the kind's
// outline, its label where the kind keeps it, and a class's, entity's or
// fragment's compartments.
func boardDrawShapeItem(
	layer *image.RGBA,
	work *boardRenderWork,
	item boardSceneItem,
	x, y, w, h, scale float64,
	fill, stroke color.RGBA,
	face font.Face,
	parsed *opentype.Font,
) error {
	kind, props := item.Properties.Shape, item.Properties
	width := math.Max(1, props.StrokeWidth*scale)
	tag := 0.0
	if kind == BoardShapeTypeFragment {
		tag = math.Min(w, float64(font.MeasureString(face, props.Title).Ceil())+20*scale)
	}
	outline := boardShapeOutline(kind, x, y, w, h, scale, props, tag)
	if outline.body != nil {
		body := &fill
		if outline.bodyFill != nil {
			body = outline.bodyFill
		}
		if outline.clear {
			body = nil
		}
		boardPolygon(layer, work, outline.body, body, stroke, width)
	}
	for _, part := range outline.parts {
		boardPolygon(layer, work, part, &fill, stroke, width)
	}
	for _, line := range outline.lines {
		boardPolyline(layer, work, line, width, stroke, 0, 0)
	}
	for _, line := range outline.dashed {
		boardPolyline(layer, work, line, width, stroke, boardDashLength*scale, boardDashGap*scale)
	}
	lines := func(text string, left, top, right float64) {
		drawer := font.Drawer{
			Dst: boardClippedImage{
				Image: layer,
				clip:  image.Rect(int(left), int(top), int(right), int(y+h)),
			},
			Src:  image.NewUniform(color.NRGBA(stroke)),
			Face: face,
		}
		baseline := top + float64(face.Metrics().Ascent.Ceil())
		for _, line := range strings.Split(text, "\n") {
			if baseline > y+h {
				return
			}
			drawer.Dot = fixed.P(int(left), int(baseline))
			boardDrawString(&drawer, line, work)
			baseline += boardCompartmentLine * scale
		}
	}
	switch kind {
	case BoardShapeTypeClass, BoardShapeTypeEntity:
		title := math.Min(h, boardCompartmentTitle*scale)
		bounds := image.Rect(int(x+8*scale), int(y+6*scale), int(x+w-8*scale), int(y+title))
		if err := boardDrawItemText(layer, work, parsed, props.Title, bounds, scale, stroke, true); err != nil {
			return err
		}
		at := y + title
		for _, section := range props.Sections {
			lines(section, x+8*scale, at+boardCompartmentMargin/2*scale, x+w-8*scale)
			at += boardCompartmentHeight(section) * scale
		}
		return nil
	case BoardShapeTypeFragment:
		lines(props.Title, x+8*scale, y+4*scale, x+tag)
		for i, section := range props.Sections {
			left, top := x+8*scale, y+4*scale
			if i == 0 {
				left = x + tag + 8*scale
			} else if i-1 < len(props.Dividers) {
				top = y + props.Dividers[i-1]*scale + 4*scale
			}
			lines(section, left, top, x+w-8*scale)
		}
		return nil
	}
	bounds := boardShapeTextBox(kind, props.Head, x, y, w, h, scale)
	return boardDrawItemText(layer, work, parsed, boardItemLabel(item), bounds, scale, stroke, true)
}

// Fit ordinary multiline notes at a readable size. When an unusually long note
// still cannot fit, an ellipsis explicitly marks the bounded visual excerpt;
// the immutable native manifest always retains the entire original text.
func boardDrawItemText(
	dst draw.Image,
	work *boardRenderWork,
	parsed *opentype.Font,
	text string,
	bounds image.Rectangle,
	scale float64,
	c color.RGBA,
	center bool,
) error {
	// Text that misses the tile paints nothing, so it is not laid out either.
	if bounds.Empty() || text == "" || bounds.Intersect(dst.Bounds()).Empty() {
		return nil
	}
	for size := 16; size >= 9; size-- {
		face, err := opentype.NewFace(
			parsed,
			&opentype.FaceOptions{
				Size:    math.Max(1, float64(size)*scale),
				DPI:     72,
				Hinting: font.HintingNone,
			},
		)
		if err != nil {
			return err
		}
		lineHeight := max(1, int(math.Ceil(float64(size)*1.4*scale)))
		capacity := max(1, bounds.Dy()/lineHeight)
		lines, overflow := boardWrapText(face, text, bounds.Dx(), capacity, work)
		if work.exhausted() {
			_ = face.Close()
			return ErrBoardLimit
		}
		if overflow && size > 9 {
			_ = face.Close()
			continue
		}
		if overflow {
			last := []rune(lines[len(lines)-1])
			for len(last) > 0 && font.MeasureString(face, string(last)+"…").Ceil() > bounds.Dx() {
				last = last[:len(last)-1]
			}
			lines[len(lines)-1] = string(last) + "…"
		}
		drawer := font.Drawer{
			Dst:  boardClippedImage{Image: dst, clip: bounds},
			Src:  image.NewUniform(color.NRGBA(c)),
			Face: face,
		}
		// Lines whose ink cannot reach the tile only advance the baseline.
		visible, reach := drawer.Dst.Bounds(), face.Metrics().Height.Ceil()
		baseline := bounds.Min.Y + face.Metrics().Ascent.Ceil()
		if center {
			// A shape's label sits in the middle of its box, as in the editor.
			baseline += max(0, (bounds.Dy()-len(lines)*lineHeight)/2)
		}
		for _, line := range lines {
			if baseline > bounds.Max.Y || baseline-reach > visible.Max.Y {
				break
			}
			if baseline+reach >= visible.Min.Y {
				left := bounds.Min.X
				if center {
					left += max(0, (bounds.Dx()-font.MeasureString(face, line).Ceil())/2)
				}
				drawer.Dot = fixed.P(left, baseline)
				boardDrawString(&drawer, line, work)
			}
			baseline += lineHeight
		}
		return face.Close()
	}
	return nil
}

type boardClippedImage struct {
	draw.Image
	clip image.Rectangle
}

func (b boardClippedImage) Bounds() image.Rectangle { return b.clip.Intersect(b.Image.Bounds()) }
func (b boardClippedImage) Set(x, y int, c color.Color) {
	if image.Pt(x, y).In(b.clip) {
		b.Image.Set(x, y, c)
	}
}

// boardWrapText breaks text into at most capacity lines of width pixels. Each
// line's width grows rune by rune, so a line is never measured again from its
// start; it breaks exactly where measuring the whole candidate line would.
func boardWrapText(face font.Face, text string, width, capacity int, work *boardRenderWork) ([]string, bool) {
	lines := make([]string, 0, capacity)
	var line strings.Builder
	lineWidth, last := fixed.Int26_6(0), rune(-1)
	for _, r := range text {
		if !work.spend(boardRenderRuneCost) {
			return lines, true
		}
		if r == '\n' {
			if len(lines) == capacity {
				return lines, true
			}
			lines = append(lines, line.String())
			line.Reset()
			lineWidth, last = 0, -1
			continue
		}
		advance := boardGlyphAdvance(face, last, r)
		if line.Len() > 0 && !unicode.Is(unicode.Mn, r) && (lineWidth+advance).Ceil() > width {
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
			work.spend(boardRenderRuneCost * int64(len(remainder)))
			lineWidth, last = boardMeasure(face, remainder)
			if unicode.IsSpace(r) && line.Len() == 0 {
				continue
			}
			advance = boardGlyphAdvance(face, last, r)
		}
		if len(lines) == capacity {
			return lines, true
		}
		line.WriteRune(r)
		lineWidth, last = lineWidth+advance, r
	}
	if len(lines) == capacity {
		return lines, line.Len() > 0
	}
	lines = append(lines, line.String())
	return lines, false
}

// boardGlyphAdvance is how far r moves the pen after last, as font.MeasureString
// counts it: the kerning against the previous rune, then the glyph's advance.
func boardGlyphAdvance(face font.Face, last, r rune) fixed.Int26_6 {
	advance, _ := face.GlyphAdvance(r)
	if last >= 0 {
		advance += face.Kern(last, r)
	}
	return advance
}

// boardMeasure is font.MeasureString that also names the last rune measured.
func boardMeasure(face font.Face, s string) (fixed.Int26_6, rune) {
	width, last := fixed.Int26_6(0), rune(-1)
	for _, r := range s {
		width, last = width+boardGlyphAdvance(face, last, r), r
	}
	return width, last
}

// boardDrawString draws s like font.Drawer.DrawString, but rasterizes only the
// glyphs whose ink can reach the destination's bounds; the rest only advance
// the pen, so a long line costs its visible part.
func boardDrawString(d *font.Drawer, s string, work *boardRenderWork) {
	clip := d.Dst.Bounds()
	// Ink reaches at most about a line height past the pen in either direction.
	reach := d.Face.Metrics().Height
	minX, maxX := fixed.I(clip.Min.X)-reach, fixed.I(clip.Max.X)+reach
	last := rune(-1)
	for _, r := range s {
		if !work.spend(boardRenderRuneCost) {
			return
		}
		if last >= 0 {
			d.Dot.X += d.Face.Kern(last, r)
		}
		last = r
		if d.Dot.X > maxX {
			return
		}
		if advance, _ := d.Face.GlyphAdvance(r); d.Dot.X+advance < minX {
			d.Dot.X += advance
			continue
		}
		if !work.spend(boardRenderGlyphCost) {
			return
		}
		dr, mask, maskp, advance, _ := d.Face.Glyph(d.Dot, r)
		if !dr.Empty() {
			draw.DrawMask(d.Dst, dr, d.Src, image.Point{}, mask, maskp, draw.Over)
		}
		d.Dot.X += advance
	}
}

// boardRotatedSource is the part of a rotated object's w×h layer that the tile
// samples: the tile's corners rotated back about the object's center, widened
// by the resampling kernel's reach so every sampled pixel keeps all its taps.
func boardRotatedSource(tile image.Rectangle, x, y, w, h, cos, sin float64) image.Rectangle {
	cx, cy := x+w/2, y+h/2
	minU, minV, maxU, maxV := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	for _, corner := range []image.Point{tile.Min, {tile.Max.X, tile.Min.Y}, {tile.Min.X, tile.Max.Y}, tile.Max} {
		dx, dy := float64(corner.X)-cx, float64(corner.Y)-cy
		u, v := cos*dx+sin*dy+w/2, -sin*dx+cos*dy+h/2
		minU, maxU = math.Min(minU, u), math.Max(maxU, u)
		minV, maxV = math.Min(minV, v), math.Max(maxV, v)
	}
	return image.Rect(
		int(math.Floor(minU))-3, int(math.Floor(minV))-3, int(math.Ceil(maxU))+3, int(math.Ceil(maxV))+3,
	).Intersect(image.Rect(0, 0, max(1, int(math.Ceil(w))), max(1, int(math.Ceil(h)))))
}

// boardPlaceImage draws img over target. A placement about the tile's size is
// scaled in two separable passes, which buffer the whole target width for
// every source row; a larger one resamples only the pixels the tile shows.
func boardPlaceImage(
	dst *image.RGBA,
	work *boardRenderWork,
	target image.Rectangle,
	img image.Image,
	tile image.Rectangle,
) {
	drawn := target.Intersect(dst.Bounds())
	source := img.Bounds()
	if drawn.Empty() || source.Empty() {
		return
	}
	sx, sy := float64(target.Dx())/float64(source.Dx()), float64(target.Dy())/float64(source.Dy())
	// A Catmull-Rom tap covers two source pixels on each side, more when
	// shrinking.
	tapsX, tapsY := int64(math.Ceil(4*math.Max(1, 1/sx))), int64(math.Ceil(4*math.Max(1, 1/sy)))
	if target.Dx() <= 2*tile.Dx() && target.Dy() <= 2*tile.Dy() {
		if work.spend(int64(target.Dx())*int64(source.Dy())*tapsX + int64(drawn.Dx())*int64(drawn.Dy())*tapsY) {
			xdraw.CatmullRom.Scale(dst, target, img, source, draw.Over, nil)
		}
		return
	}
	if work.spend(int64(drawn.Dx()) * int64(drawn.Dy()) * tapsX * tapsY) {
		xdraw.CatmullRom.Transform(
			dst,
			f64.Aff3{
				sx, 0, float64(target.Min.X) - float64(source.Min.X)*sx,
				0, sy, float64(target.Min.Y) - float64(source.Min.Y)*sy,
			},
			img,
			source,
			draw.Over,
			nil,
		)
	}
}

func boardColor(value string, fallback color.RGBA) color.RGBA {
	if (len(value) != 7 && len(value) != 9) || value[0] != '#' {
		return fallback
	}
	b, err := hex.DecodeString(value[1:])
	if err != nil {
		return fallback
	}
	alpha := byte(255)
	if len(b) == 4 {
		alpha = b[3]
	}
	return color.RGBA{b[0], b[1], b[2], alpha}
}

func boardLine(dst *image.RGBA, work *boardRenderWork, x1, y1, x2, y2, width float64, c color.RGBA) {
	dx, dy := x2-x1, y2-y1
	length := math.Hypot(dx, dy)
	if length == 0 {
		return
	}
	nx, ny := -dy/length*width/2, dx/length*width/2
	area := image.Rect(int(math.Floor(math.Min(x1, x2)-width)), int(math.Floor(math.Min(y1, y2)-width)), int(math.Ceil(math.Max(x1, x2)+width)), int(math.Ceil(math.Max(y1, y2)+width))).
		Intersect(dst.Bounds())
	// A segment rasterizes its whole bounding box, however thin it is.
	if area.Empty() || !work.spend(int64(area.Dx())*int64(area.Dy())) {
		return
	}
	x1, x2 = x1-float64(area.Min.X), x2-float64(area.Min.X)
	y1, y2 = y1-float64(area.Min.Y), y2-float64(area.Min.Y)
	raster := vector.NewRasterizer(area.Dx(), area.Dy())
	raster.MoveTo(float32(x1+nx), float32(y1+ny))
	raster.LineTo(float32(x2+nx), float32(y2+ny))
	raster.LineTo(float32(x2-nx), float32(y2-ny))
	raster.LineTo(float32(x1-nx), float32(y1-ny))
	raster.ClosePath()
	raster.Draw(dst, area, image.NewUniform(color.NRGBA(c)), image.Point{})
}

func boardStrokeTransform(item boardSceneItem) func(BoardPoint) (float64, float64) {
	originalWidth, originalHeight := 12.0, 12.0
	for _, sample := range item.Points {
		originalWidth = math.Max(originalWidth, sample.X+6)
		originalHeight = math.Max(originalHeight, sample.Y+6)
	}
	angle := item.Rotation * math.Pi / 180
	cos, sin := math.Cos(angle), math.Sin(angle)
	return func(point BoardPoint) (float64, float64) {
		x, y := point.X*item.Width/originalWidth-item.Width/2, point.Y*item.Height/originalHeight-item.Height/2
		return item.X + item.Width/2 + x*cos - y*sin, item.Y + item.Height/2 + x*sin + y*cos
	}
}

func boardShape(
	dst *image.RGBA,
	work *boardRenderWork,
	x, y, w, h float64,
	shape string,
	fill, stroke color.RGBA,
	width float64,
) {
	var points [][2]float64
	switch shape {
	case "ellipse":
		for i := range 64 {
			a := float64(i) * 2 * math.Pi / 64
			points = append(points, [2]float64{x + w/2 + w/2*math.Cos(a), y + h/2 + h/2*math.Sin(a)})
		}
	case "diamond":
		points = [][2]float64{{x + w/2, y}, {x + w, y + h/2}, {x + w/2, y + h}, {x, y + h/2}}
	case "triangle":
		points = [][2]float64{{x + w/2, y}, {x + w, y + h}, {x, y + h}}
	default:
		points = [][2]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}
	}
	// Fill only the shape's bounds: a rasterizer the size of the tile would
	// allocate and scan every pixel for each shape.
	area := image.Rect(int(math.Floor(x)), int(math.Floor(y)), int(math.Ceil(x+w)), int(math.Ceil(y+h))).
		Intersect(dst.Bounds())
	if !area.Empty() && work.spend(int64(area.Dx())*int64(area.Dy())) {
		raster := vector.NewRasterizer(area.Dx(), area.Dy())
		ox, oy := float64(area.Min.X), float64(area.Min.Y)
		raster.MoveTo(float32(points[0][0]-ox), float32(points[0][1]-oy))
		for _, p := range points[1:] {
			raster.LineTo(float32(p[0]-ox), float32(p[1]-oy))
		}
		raster.ClosePath()
		raster.Draw(dst, area, image.NewUniform(color.NRGBA(fill)), image.Point{})
	}
	for i, p := range points {
		q := points[(i+1)%len(points)]
		boardLine(dst, work, p[0], p[1], q[0], q[1], width, stroke)
	}
}

// boardItemLabel is the text an item shows: a note's text, a file card's name
// and kind, otherwise its title.
func boardItemLabel(item boardSceneItem) string {
	if item.Type == "file" && item.File != nil {
		kind := strings.ToUpper(strings.TrimPrefix(path.Ext(item.File.Filename), "."))
		if kind == "" {
			kind = item.File.ContentType
		}
		return item.File.Filename + "\n" + kind + " file"
	}
	if item.Text != "" {
		return item.Text
	}
	return item.Properties.Title
}

// boardEndLabelAlign is the fraction of an end label's size to shift it by
// so it lies wholly on the side of its line the normal n points to.
func boardEndLabelAlign(n float64) float64 {
	switch {
	case n > 0.3:
		return 0
	case n < -0.3:
		return -1
	}
	return -0.5
}
