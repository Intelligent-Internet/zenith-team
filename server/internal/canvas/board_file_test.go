package canvas

import (
	"context"
	"errors"
	"image"
	"image/color"
	"testing"
)

// Decoding weighs what the decoder allocates, not the file size: 16-bit pixels
// take twice the memory and progressive JPEG coefficients more still. The
// process never decodes more than its budget at once.
func TestBoardImageDecodesStayWithinTheMemoryBudget(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		model color.Model
		want  int64
	}{
		{color.RGBAModel, 4 * 100 * 100},
		{color.NRGBA64Model, 8 * 100 * 100},
		{color.YCbCrModel, 16 * 100 * 100},
	} {
		if got := boardDecodeWeight(
			image.Config{ColorModel: tt.model, Width: 100, Height: 100},
		); got != tt.want {
			t.Errorf("weight of %T = %d, want %d", tt.model, got, tt.want)
		}
	}
	if _, err := acquireBoardDecode(t.Context(), boardDecodeBudget+1); !errors.Is(err, ErrBoardLimit) {
		t.Fatalf("decode larger than the budget = %v, want ErrBoardLimit", err)
	}
	release, err := acquireBoardDecode(t.Context(), boardDecodeBudget)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := acquireBoardDecode(waiting, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("decode while the budget is taken = %v, want the caller's cancellation", err)
	}
	release()
}
