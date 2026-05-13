// Package motion implements lightweight camera-based motion detection
// via frame differencing. Decodes two JPEGs, downsamples each to a small
// grid, and computes the mean absolute pixel difference over the inner
// region (with a configurable edge crop to ignore wind, branches, and
// sun glare on the frame margins).
//
// No CGO, no OpenCV — just the Go stdlib `image/jpeg` plus arithmetic.
// The job is intentionally crude: "did the scene change?" The classifier
// downstream (Gemma) decides whether the change is worth a notification.
package motion

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
)

// Detector compares two JPEG frames and reports how different they are.
type Detector struct {
	// DownsampleWidth is the target width for the working grid (e.g. 80).
	// Height is derived from the source aspect ratio. Smaller = faster,
	// noisier; larger = slower, more sensitive to small movement.
	DownsampleWidth int

	// EdgeCropPct ignores the outer N% of pixels on each side, dropping
	// noise from wind-blown branches, sun glare on lens edges, and
	// camera-housing reflections. 0.0 disables; 0.05 is a reasonable
	// default (drops the outer 5%). Must be in [0, 0.49].
	EdgeCropPct float64
}

// New returns a Detector with sensible defaults: 80px working width,
// 5% edge crop.
func New() *Detector {
	return &Detector{DownsampleWidth: 80, EdgeCropPct: 0.05}
}

// Diff returns the mean absolute pixel difference between two JPEG frames,
// in the range [0, 255]. 0 means identical; 255 means maximally different
// (every pixel flipped from black to white across every channel).
//
// Both inputs must be decodable JPEG. Source dimensions don't have to match
// — both get downsampled to the same working grid before comparison.
func (d *Detector) Diff(prev, current []byte) (float64, error) {
	if len(prev) == 0 || len(current) == 0 {
		return 0, fmt.Errorf("motion.Diff: empty input")
	}

	prevImg, err := jpeg.Decode(bytes.NewReader(prev))
	if err != nil {
		return 0, fmt.Errorf("motion.Diff: decode prev: %w", err)
	}
	curImg, err := jpeg.Decode(bytes.NewReader(current))
	if err != nil {
		return 0, fmt.Errorf("motion.Diff: decode current: %w", err)
	}

	w := d.DownsampleWidth
	if w <= 0 {
		w = 80
	}

	// Derive height from the current frame's aspect ratio.
	bounds := curImg.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	if srcW == 0 || srcH == 0 {
		return 0, fmt.Errorf("motion.Diff: zero-sized current frame")
	}
	h := w * srcH / srcW
	if h < 1 {
		h = 1
	}

	prevGrid := downsample(prevImg, w, h)
	curGrid := downsample(curImg, w, h)

	crop := d.EdgeCropPct
	if crop < 0 {
		crop = 0
	}
	if crop > 0.49 {
		crop = 0.49
	}
	x0 := int(float64(w) * crop)
	y0 := int(float64(h) * crop)
	x1 := w - x0
	y1 := h - y0
	if x1 <= x0 || y1 <= y0 {
		return 0, fmt.Errorf("motion.Diff: edge crop ate the entire frame")
	}

	var sum int64
	var count int64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := y*w + x
			p := prevGrid[i]
			c := curGrid[i]
			sum += abs(int64(p.r) - int64(c.r))
			sum += abs(int64(p.g) - int64(c.g))
			sum += abs(int64(p.b) - int64(c.b))
			count += 3
		}
	}
	if count == 0 {
		return 0, nil
	}
	return float64(sum) / float64(count), nil
}

// pixel holds 8-bit RGB values.
type pixel struct{ r, g, b uint8 }

// downsample returns a flat row-major grid of w*h pixels sampled via
// nearest-neighbour from src. Fast and good enough for diff thresholds —
// we don't need filtered resampling for "did the scene change?"
func downsample(src image.Image, w, h int) []pixel {
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	out := make([]pixel, w*h)
	for y := 0; y < h; y++ {
		sy := bounds.Min.Y + y*srcH/h
		for x := 0; x < w; x++ {
			sx := bounds.Min.X + x*srcW/w
			r, g, b, _ := src.At(sx, sy).RGBA()
			// RGBA() returns 16-bit values pre-multiplied; shift to 8-bit.
			out[y*w+x] = pixel{
				r: uint8(r >> 8),
				g: uint8(g >> 8),
				b: uint8(b >> 8),
			}
		}
	}
	return out
}

func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
