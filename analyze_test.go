package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/color/palette"
	"image/jpeg"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// referenceLumaStats is the straightforward per-pixel implementation that
// lumaStats must agree with.
func referenceLumaStats(img image.Image) (float64, float64) {
	b := img.Bounds()
	var values []float64
	var sum float64
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			l := 0.2126*float64(r>>8) + 0.7152*float64(g>>8) + 0.0722*float64(bl>>8)
			values = append(values, l)
			sum += l
		}
	}
	sort.Float64s(values)
	n := len(values)
	median := values[n/2]
	if n%2 == 0 {
		median = (values[n/2-1] + values[n/2]) / 2
	}
	return sum / float64(n), median
}

func randomImages(t *testing.T, w, h int) map[string]image.Image {
	t.Helper()
	rng := rand.New(rand.NewPCG(1, 2))
	rect := image.Rect(3, 5, 3+w, 5+h) // non-zero origin on purpose

	rgba := image.NewRGBA(rect)
	nrgba := image.NewNRGBA(rect)
	gray := image.NewGray(rect)
	rgba64 := image.NewRGBA64(rect)
	paletted := image.NewPaletted(rect, palette.Plan9)
	ycbcr := image.NewYCbCr(rect, image.YCbCrSubsampleRatio420)
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			c := color.NRGBA{uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256)), uint8(rng.IntN(256))}
			rgba.Set(x, y, c)
			nrgba.SetNRGBA(x, y, c)
			gray.Set(x, y, c)
			rgba64.Set(x, y, c)
			paletted.Set(x, y, c)
		}
	}
	for i := range ycbcr.Y {
		ycbcr.Y[i] = uint8(rng.IntN(256))
	}
	for i := range ycbcr.Cb {
		ycbcr.Cb[i] = uint8(rng.IntN(256))
		ycbcr.Cr[i] = uint8(rng.IntN(256))
	}
	return map[string]image.Image{
		"RGBA": rgba, "NRGBA": nrgba, "Gray": gray, "RGBA64 (generic path)": rgba64,
		"Paletted": paletted, "YCbCr": ycbcr,
	}
}

func TestLumaStatsMatchesReference(t *testing.T) {
	for _, size := range [][2]int{{17, 9}, {16, 10}, {1, 1}} { // odd, even and single pixel counts
		for name, img := range randomImages(t, size[0], size[1]) {
			wantAvg, wantMedian := referenceLumaStats(img)
			avg, median := lumaStats(img)
			if math.Abs(avg-wantAvg) > 1e-9 || math.Abs(median-wantMedian) > 1e-9 {
				t.Errorf("%s %dx%d: got avg %v median %v, want %v %v", name, size[0], size[1], avg, median, wantAvg, wantMedian)
			}
		}
	}
}

func TestThumbnailSize(t *testing.T) {
	tests := []struct{ w, h, wantW, wantH int }{
		{100, 50, 100, 50},
		{400, 200, 200, 100},
		{200, 400, 100, 200},
		{1000, 1, 200, 1},
		{1, 1000, 1, 200},
	}
	for _, tt := range tests {
		if w, h := thumbnailSize(tt.w, tt.h, 200); w != tt.wantW || h != tt.wantH {
			t.Errorf("thumbnailSize(%d, %d) = %d, %d; want %d, %d", tt.w, tt.h, w, h, tt.wantW, tt.wantH)
		}
	}
}

// exifSegment builds an APP1 segment holding only an orientation tag.
func exifSegment(order binary.ByteOrder, orientation uint16) []byte {
	tiff := make([]byte, 8+2+12+4)
	if order == binary.LittleEndian {
		copy(tiff, "II")
	} else {
		copy(tiff, "MM")
	}
	order.PutUint16(tiff[2:], 42)
	order.PutUint32(tiff[4:], 8)
	order.PutUint16(tiff[8:], 1)       // one entry
	order.PutUint16(tiff[10:], 0x0112) // Orientation
	order.PutUint16(tiff[12:], 3)      // SHORT
	order.PutUint32(tiff[14:], 1)      // count
	order.PutUint16(tiff[18:], orientation)

	payload := append([]byte("Exif\x00\x00"), tiff...)
	seg := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(seg[2:], uint16(len(payload)+2))
	return append(seg, payload...)
}

// jpegWithOrientation encodes img as JPEG and inserts an EXIF orientation after SOI.
func jpegWithOrientation(t *testing.T, img image.Image, order binary.ByteOrder, orientation uint16) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	out := append([]byte{}, data[:2]...)
	out = append(out, exifSegment(order, orientation)...)
	return append(out, data[2:]...)
}

func TestJPEGOrientation(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for o := uint16(1); o <= 8; o++ {
			data := jpegWithOrientation(t, img, order, o)
			if got := jpegOrientation(data); got != int(o) {
				t.Errorf("%v orientation %d: got %d", order, o, got)
			}
		}
	}

	var plain bytes.Buffer
	jpeg.Encode(&plain, img, nil)
	if got := jpegOrientation(plain.Bytes()); got != 1 {
		t.Errorf("JPEG without EXIF: got %d, want 1", got)
	}
	if got := jpegOrientation([]byte("\x89PNG\r\n\x1a\n")); got != 1 {
		t.Errorf("PNG: got %d, want 1", got)
	}
	full := jpegWithOrientation(t, img, binary.BigEndian, 6)
	for n := range 40 {
		jpegOrientation(full[:n]) // must not panic on truncated input
	}
	if got := jpegOrientation(jpegWithOrientation(t, img, binary.BigEndian, 42)); got != 1 {
		t.Errorf("invalid orientation value: got %d, want 1", got)
	}
}

func TestApplyOrientation(t *testing.T) {
	// 3×2 source; the marked pixel is the top-left corner.
	src := image.NewRGBA(image.Rect(0, 0, 3, 2))
	mark := color.RGBA{255, 0, 0, 255}
	src.SetRGBA(0, 0, mark)

	// Where the source's top-left pixel ends up for each orientation
	want := map[int]image.Point{
		1: {0, 0}, 2: {2, 0}, 3: {2, 1}, 4: {0, 1},
		5: {0, 0}, 6: {1, 0}, 7: {1, 2}, 8: {0, 2},
	}
	for o, p := range want {
		dst := applyOrientation(src, o)
		wantW, wantH := 3, 2
		if o >= 5 {
			wantW, wantH = 2, 3
		}
		if dst.Bounds().Dx() != wantW || dst.Bounds().Dy() != wantH {
			t.Errorf("orientation %d: size %v, want %dx%d", o, dst.Bounds().Size(), wantW, wantH)
			continue
		}
		if dst.RGBAAt(p.X, p.Y) != mark {
			t.Errorf("orientation %d: marked pixel not at %v", o, p)
		}
	}
}

func TestAnalyzeImageAppliesEXIFOrientation(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 40, 20))
	path := filepath.Join(t.TempDir(), "rotated.jpg")
	if err := os.WriteFile(path, jpegWithOrientation(t, img, binary.BigEndian, 6), 0o644); err != nil {
		t.Fatal(err)
	}

	a, err := analyzeImage(path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Width != 20 || a.Height != 40 {
		t.Errorf("dimensions %dx%d, want 20x40", a.Width, a.Height)
	}
	thumb, err := jpeg.Decode(bytes.NewReader(a.Thumbnail))
	if err != nil {
		t.Fatal(err)
	}
	if s := thumb.Bounds().Size(); s.X != 20 || s.Y != 40 {
		t.Errorf("thumbnail %v, want 20x40", s)
	}
}

func TestThumbnailFlattensTransparencyToWhite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clear.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, image.NewNRGBA(image.Rect(0, 0, 10, 10))) // fully transparent
	f.Close()

	a, err := analyzeImage(path)
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := jpeg.Decode(bytes.NewReader(a.Thumbnail))
	if err != nil {
		t.Fatalf("thumbnail is not a JPEG: %v", err)
	}
	if r, g, b, _ := thumb.At(5, 5).RGBA(); r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Errorf("transparent pixel became %d,%d,%d; want white", r>>8, g>>8, b>>8)
	}
}
