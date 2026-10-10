package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"os"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const thumbnailMaxSize = 200

// Luma is 0.2126R + 0.7152G + 0.0722B (ITU-R BT.709 weights on 8-bit sRGB values).
// The weights ×10000 share a factor of two, so wR*r + wG*g + wB*b is an exact
// integer key equal to luma*lumaDenom. That lets a histogram give an exact median.
const (
	wR         = 1063
	wG         = 3576
	wB         = 361
	lumaDenom  = 5000.0
	maxLumaKey = 255 * (wR + wG + wB)
)

type analysis struct {
	Average   float64
	Median    float64
	Width     int // display width, after applying EXIF orientation
	Height    int // display height, after applying EXIF orientation
	Thumbnail []byte
}

// analyzeImage decodes an image once and derives luma statistics, display
// dimensions and a JPEG thumbnail from it.
func analyzeImage(path string) (analysis, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return analysis{}, err
	}

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return analysis{}, err
	}
	bounds := img.Bounds()
	if bounds.Empty() {
		return analysis{}, errors.New("image has no pixels")
	}

	avg, median := lumaStats(img)

	orientation := jpegOrientation(data)
	width, height := bounds.Dx(), bounds.Dy()
	if orientation >= 5 {
		width, height = height, width
	}

	thumb, err := makeThumbnail(img, orientation)
	if err != nil {
		return analysis{}, err
	}

	return analysis{
		Average:   avg,
		Median:    median,
		Width:     width,
		Height:    height,
		Thumbnail: thumb,
	}, nil
}

// lumaStats returns the average and median luma of all pixels. Common image
// types are read directly from their concrete pixel accessors, which avoids
// the per-pixel interface allocation of img.At.
func lumaStats(img image.Image) (average, median float64) {
	hist := make([]uint32, maxLumaKey+1)
	var sum uint64
	add := func(r, g, b uint32) {
		k := wR*(r>>8) + wG*(g>>8) + wB*(b>>8)
		hist[k]++
		sum += uint64(k)
	}

	bounds := img.Bounds()
	switch m := img.(type) {
	case *image.YCbCr:
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := m.YCbCrAt(x, y).RGBA()
				add(r, g, b)
			}
		}
	case *image.NYCbCrA:
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := m.NYCbCrAAt(x, y).RGBA()
				add(r, g, b)
			}
		}
	case *image.RGBA:
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := m.RGBAAt(x, y).RGBA()
				add(r, g, b)
			}
		}
	case *image.NRGBA:
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := m.NRGBAAt(x, y).RGBA()
				add(r, g, b)
			}
		}
	case *image.Gray:
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := m.GrayAt(x, y).RGBA()
				add(r, g, b)
			}
		}
	case *image.Paletted:
		keys := make([][3]uint32, len(m.Palette))
		for i, c := range m.Palette {
			r, g, b, _ := c.RGBA()
			keys[i] = [3]uint32{r, g, b}
		}
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				k := keys[m.ColorIndexAt(x, y)]
				add(k[0], k[1], k[2])
			}
		}
	default:
		for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
			for x := bounds.Min.X; x < bounds.Max.X; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				add(r, g, b)
			}
		}
	}

	total := uint64(bounds.Dx()) * uint64(bounds.Dy())
	average = float64(sum) / float64(total) / lumaDenom
	if total%2 == 0 {
		lo := histogramRank(hist, total/2-1)
		hi := histogramRank(hist, total/2)
		median = float64(lo+hi) / 2 / lumaDenom
	} else {
		median = float64(histogramRank(hist, total/2)) / lumaDenom
	}
	return average, median
}

// histogramRank returns the value at 0-based position k in sorted order.
func histogramRank(hist []uint32, k uint64) int {
	var seen uint64
	for v, n := range hist {
		seen += uint64(n)
		if seen > k {
			return v
		}
	}
	return len(hist) - 1
}

// thumbnailSize scales width×height so the longest side is at most maxSize.
func thumbnailSize(width, height, maxSize int) (int, int) {
	if width <= maxSize && height <= maxSize {
		return width, height
	}
	if width > height {
		return maxSize, max(1, height*maxSize/width)
	}
	return max(1, width*maxSize/height), maxSize
}

// makeThumbnail returns a JPEG thumbnail, rotated upright per the EXIF orientation.
func makeThumbnail(img image.Image, orientation int) ([]byte, error) {
	bounds := img.Bounds()
	w, h := thumbnailSize(bounds.Dx(), bounds.Dy(), thumbnailMaxSize)

	thumb := image.NewRGBA(image.Rect(0, 0, w, h))
	// JPEG has no alpha channel: flatten transparency onto white rather than black
	draw.Draw(thumb, thumb.Bounds(), image.White, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(thumb, thumb.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, applyOrientation(thumb, orientation), &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// applyOrientation transforms src according to an EXIF orientation value (1–8).
func applyOrientation(src *image.RGBA, orientation int) *image.RGBA {
	if orientation < 2 || orientation > 8 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}

	// sourceOf maps a destination pixel to the source pixel it comes from.
	var sourceOf func(x, y int) (int, int)
	switch orientation {
	case 2: // mirror horizontal
		sourceOf = func(x, y int) (int, int) { return w - 1 - x, y }
	case 3: // rotate 180
		sourceOf = func(x, y int) (int, int) { return w - 1 - x, h - 1 - y }
	case 4: // mirror vertical
		sourceOf = func(x, y int) (int, int) { return x, h - 1 - y }
	case 5: // transpose
		sourceOf = func(x, y int) (int, int) { return y, x }
	case 6: // rotate 90 clockwise
		sourceOf = func(x, y int) (int, int) { return y, h - 1 - x }
	case 7: // transverse
		sourceOf = func(x, y int) (int, int) { return w - 1 - y, h - 1 - x }
	case 8: // rotate 90 counter-clockwise
		sourceOf = func(x, y int) (int, int) { return w - 1 - y, x }
	}

	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		for x := range dw {
			sx, sy := sourceOf(x, y)
			dst.SetRGBA(x, y, src.RGBAAt(sx, sy))
		}
	}
	return dst
}

// jpegOrientation returns the EXIF orientation (1–8) of JPEG data, or 1 if the
// data is not a JPEG or carries no orientation tag.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(data) {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		switch {
		case marker == 0xFF: // fill byte
			i++
			continue
		case marker == 0xDA || marker == 0xD9: // start of scan / end of image: no more metadata
			return 1
		case marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7): // standalone markers
			i += 2
			continue
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2:]))
		if segLen < 2 || i+2+segLen > len(data) {
			return 1
		}
		seg := data[i+4 : i+2+segLen]
		if marker == 0xE1 && bytes.HasPrefix(seg, []byte("Exif\x00\x00")) {
			return tiffOrientation(seg[6:])
		}
		i += 2 + segLen
	}
	return 1
}

// tiffOrientation reads the Orientation tag (0x0112) from IFD0 of a TIFF/EXIF block.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(t[2:]) != 42 {
		return 1
	}
	ifd := uint64(order.Uint32(t[4:]))
	if ifd+2 > uint64(len(t)) {
		return 1
	}
	entries := int(order.Uint16(t[ifd:]))
	for e := range entries {
		off := int(ifd) + 2 + e*12
		if off+12 > len(t) {
			return 1
		}
		const tagOrientation, typeShort = 0x0112, 3
		if order.Uint16(t[off:]) == tagOrientation && order.Uint16(t[off+2:]) == typeShort {
			if v := int(order.Uint16(t[off+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}
