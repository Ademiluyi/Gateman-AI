package motion

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"
)

// solidJPEG returns a JPEG-encoded WxH image filled with one colour.
func solidJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

// markedJPEG returns a JPEG that is `bg` everywhere except a filled rect
// of `fg` between (x0,y0)-(x1,y1).
func markedJPEG(t *testing.T, w, h int, bg, fg color.RGBA, x0, y0, x1, y1 int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x >= x0 && x < x1 && y >= y0 && y < y1 {
				img.Set(x, y, fg)
			} else {
				img.Set(x, y, bg)
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func TestIdenticalFramesScoreLow(t *testing.T) {
	d := New()
	gray := color.RGBA{128, 128, 128, 255}
	frame := solidJPEG(t, 640, 480, gray)
	got, err := d.Diff(frame, frame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// JPEG compression artefacts mean it won't be exactly 0, but tiny.
	if got > 2.0 {
		t.Fatalf("identical frames scored %.2f, expected < 2.0", got)
	}
}

func TestBlackVsWhiteScoresHigh(t *testing.T) {
	d := New()
	black := solidJPEG(t, 640, 480, color.RGBA{0, 0, 0, 255})
	white := solidJPEG(t, 640, 480, color.RGBA{255, 255, 255, 255})
	got, err := d.Diff(black, white)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 200 {
		t.Fatalf("black-vs-white scored %.2f, expected near 255", got)
	}
}

func TestCentralChangeScoresHigh(t *testing.T) {
	d := New()
	gray := color.RGBA{128, 128, 128, 255}
	black := color.RGBA{0, 0, 0, 255}
	prev := solidJPEG(t, 640, 480, gray)
	// Big central black rectangle — well inside any reasonable edge crop.
	current := markedJPEG(t, 640, 480, gray, black, 200, 150, 440, 330)
	got, err := d.Diff(prev, current)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < 15 {
		t.Fatalf("central change scored %.2f, expected > 15", got)
	}
}

// TestEdgeCropMutesCornerChange proves the edge crop actually drops
// pixels: a change confined to the outer band must score lower than the
// same change with crop=0.
func TestEdgeCropMutesCornerChange(t *testing.T) {
	gray := color.RGBA{128, 128, 128, 255}
	white := color.RGBA{255, 255, 255, 255}
	prev := solidJPEG(t, 640, 480, gray)
	// Change confined to a corner strip ~3% of the frame width — inside the
	// 5% default crop. The inner 90% is unchanged.
	current := markedJPEG(t, 640, 480, gray, white, 0, 0, 18, 480)

	cropped := &Detector{DownsampleWidth: 80, EdgeCropPct: 0.05}
	uncropped := &Detector{DownsampleWidth: 80, EdgeCropPct: 0}

	gotCropped, err := cropped.Diff(prev, current)
	if err != nil {
		t.Fatalf("cropped: %v", err)
	}
	gotUncropped, err := uncropped.Diff(prev, current)
	if err != nil {
		t.Fatalf("uncropped: %v", err)
	}
	if gotCropped >= gotUncropped {
		t.Fatalf("expected cropped (%.2f) < uncropped (%.2f) when change is in the outer band",
			gotCropped, gotUncropped)
	}
}

func TestDiffRejectsEmptyInput(t *testing.T) {
	d := New()
	frame := solidJPEG(t, 320, 240, color.RGBA{100, 100, 100, 255})
	if _, err := d.Diff(nil, frame); err == nil {
		t.Fatal("expected error on empty prev")
	}
	if _, err := d.Diff(frame, nil); err == nil {
		t.Fatal("expected error on empty current")
	}
}

func TestDiffRejectsInvalidJPEG(t *testing.T) {
	d := New()
	frame := solidJPEG(t, 320, 240, color.RGBA{100, 100, 100, 255})
	if _, err := d.Diff([]byte("not a jpeg"), frame); err == nil {
		t.Fatal("expected error on invalid prev")
	}
}
