package canvas

import (
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/google/uuid"
)

// The index answers exactly what checking every obstacle answers, whether an
// obstacle is a note, spans many cells or covers the whole window.
func TestBoardPlaceIndexMatchesCheckingEveryObstacle(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewPCG(7, 11))
	window := BoardVisualBounds{X: -3000, Y: -2000, Width: 9000, Height: 7000}
	var obstacles []BoardVisualBounds
	for range 300 {
		size := []float64{60, 300, 1500, 20000}[random.IntN(4)]
		obstacles = append(obstacles, BoardVisualBounds{
			X:      window.X - 4000 + random.Float64()*(window.Width+8000),
			Y:      window.Y - 4000 + random.Float64()*(window.Height+8000),
			Width:  size * (0.5 + random.Float64()),
			Height: size * (0.5 + random.Float64()),
		})
	}
	// Edges exactly on cell boundaries, and a rectangle touching one.
	obstacles = append(obstacles, BoardVisualBounds{X: window.X + 800, Y: window.Y + 800, Width: 400, Height: 400})
	for _, count := range []int{0, 1, 40, len(obstacles)} {
		index := newBoardPlaceIndex(window, obstacles[:count])
		for range 4000 {
			w, h := 40+random.Float64()*3000, 40+random.Float64()*3000
			r := BoardVisualBounds{
				X:      window.X + random.Float64()*(window.Width-w),
				Y:      window.Y + random.Float64()*(window.Height-h),
				Width:  w,
				Height: h,
			}
			want := true
			for _, o := range obstacles[:count] {
				if overlaps(r, o) {
					want = false
				}
			}
			if got := index.clear(r, obstacles[:count]); got != want {
				t.Fatalf("%d obstacles: clear(%+v) = %v, want %v", count, r, got, want)
			}
		}
	}
	touching := BoardVisualBounds{X: window.X + 1200, Y: window.Y + 800, Width: 400, Height: 400}
	if !newBoardPlaceIndex(window, obstacles[len(obstacles)-1:]).clear(touching, obstacles[len(obstacles)-1:]) {
		t.Fatal("a rectangle touching an obstacle's edge is clear of it")
	}
}

// One object the size of the whole Board, or a view of the whole Board, costs
// a compose call no more than an ordinary Board: the search runs under the
// Board's lock.
func TestBoardPlaceStaysCheapForGiantObjectsAndViews(t *testing.T) {
	giant := composeTestItem(-500000, -500000, 1000000, 1000000)
	giant.Type = "frame"
	allocated := func(place func() BoardVisualBounds) (BoardVisualBounds, uint64) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		got := place()
		runtime.ReadMemStats(&after)
		return got, after.TotalAlloc - before.TotalAlloc
	}
	got, bytes := allocated(func() BoardVisualBounds {
		r, _ := newBoardPlacement(
			[]BoardItem{giant},
			composeTestView,
			uuid.New(),
			uuid.Nil,
		).place(400, 300, nil, nil)
		return r
	})
	if overlaps(got, boardVisualItemBounds(giant)) {
		t.Fatalf("placed %+v over the giant frame", got)
	}
	if bytes > 32<<20 {
		t.Fatalf("placing beside a giant frame allocated %d MB", bytes>>20)
	}

	everything := BoardFocus{
		Present: true,
		View:    BoardVisualBounds{X: -1000000, Y: -1000000, Width: 2000000, Height: 2000000},
	}
	note := composeTestItem(0, 0, 240, 190)
	p := newBoardPlacement([]BoardItem{note, giant}, everything, uuid.New(), giant.ID)
	frame := boardVisualItemBounds(giant)
	got, bytes = allocated(func() BoardVisualBounds {
		r, in := p.place(600, 300, nil, &frame)
		if !in {
			t.Fatal("the giant frame has room for the work")
		}
		return r
	})
	if overlaps(got, boardVisualItemBounds(note)) {
		t.Fatalf("placed %+v over the note", got)
	}
	if bytes > 32<<20 {
		t.Fatalf("filling a giant frame allocated %d MB", bytes>>20)
	}
	if step := boardPlaceStepFor(2000000, 2000000); (2000000/step+1)*(2000000/step+1) > 40000 {
		t.Fatalf("a view of the whole Board is searched in steps of %v", step)
	}
	if step := boardPlaceStepFor(1600, 900); step != boardPlaceStep {
		t.Fatalf("an ordinary view is searched in steps of %v, want %v", step, boardPlaceStep)
	}
}
