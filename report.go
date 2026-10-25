package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"net/url"
	"os"
	"path/filepath"
)

//go:embed templates/report.html
var reportTemplateSource string

var reportTemplate = template.Must(template.New("report").Parse(reportTemplateSource))

type report struct {
	Title    string
	Stats    []string
	Warnings []string
	Note     string
	Cards    []card
}

type card struct {
	Heading string
	Label   string
	Src     template.URL
	Average float64
	Median  float64
	Caption string
}

func writeReport(path string, r report) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := reportTemplate.Execute(file, r); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

// thumbnailURL embeds a thumbnail as a data URL. Thumbnails are always JPEG.
func thumbnailURL(thumb []byte) template.URL {
	return template.URL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(thumb))
}

// relativeURL turns a relative file path into a percent-encoded URL. The "./"
// prefix stops a name like "a:b.jpg" from being read as a URL scheme.
func relativeURL(rel string) template.URL {
	u := url.URL{Path: "./" + filepath.ToSlash(rel)}
	return template.URL(u.String())
}

func imageCard(img ImageData, src template.URL, caption string) card {
	return card{
		Src:     src,
		Average: img.AverageIlluminance,
		Median:  img.MedianIlluminance,
		Caption: caption,
	}
}

const sortedNote = "Images sorted by average illuminance (darkest to brightest)"

// scanReport links to the original files relative to the scanned directory.
func scanReport(images []ImageData) report {
	r := report{
		Title: "Image Illuminance Report",
		Stats: []string{fmt.Sprintf("Total Images: %d", len(images))},
		Note:  sortedNote,
	}
	for _, img := range images {
		r.Cards = append(r.Cards, imageCard(img, relativeURL(img.RelativePath), img.RelativePath))
	}
	return r
}

// allImagesReport embeds thumbnails so the report is portable.
func allImagesReport(images []ImageData) report {
	r := report{
		Title: "Image Illuminance Report (All Images)",
		Stats: []string{fmt.Sprintf("Total Images: %d", len(images))},
		Note:  sortedNote,
	}
	for _, img := range images {
		r.Cards = append(r.Cards, imageCard(img, thumbnailURL(img.Thumbnail), img.Path))
	}
	return r
}

func bookStats(requestedPages, filledPages int) (stats, warnings []string) {
	target := roundUpToMultipleOf4(requestedPages)
	stats = []string{fmt.Sprintf("Requested pages: %d → book pages: %d (rounded up to a multiple of 4)", requestedPages, target)}
	if filledPages < target {
		warnings = []string{fmt.Sprintf("Not enough images: only %d of %d pages could be filled.", filledPages, target)}
	}
	return stats, warnings
}

func bookReport(title string, selected []ImageData, requestedPages int) report {
	stats, warnings := bookStats(requestedPages, len(selected))
	r := report{
		Title:    title,
		Stats:    stats,
		Warnings: warnings,
		Note:     "Page 1 is the lightest image, last page is the darkest, luminance spread evenly in between.",
	}
	for i, img := range selected {
		c := imageCard(img, thumbnailURL(img.Thumbnail), img.Path)
		c.Heading = fmt.Sprintf("Page %d", i+1)
		r.Cards = append(r.Cards, c)
	}
	return r
}

func smartBookReport(book SmartBook, requestedPages int) report {
	stats, warnings := bookStats(requestedPages, book.FilledPages())
	r := report{
		Title:    "Book Image Selection (Smart)",
		Stats:    stats,
		Warnings: warnings,
		Note:     "Page 1 (lightest) • Middle spreads: one landscape or two single pages • Last page (darkest)",
	}
	// Labels come from each image's own orientation: when there are too few
	// portraits, landscapes fill the anchor and single-page slots.
	page := func(img ImageData, number int, position string) {
		c := imageCard(img, thumbnailURL(img.Thumbnail), img.Path)
		c.Heading = fmt.Sprintf("Page %d", number)
		c.Label = fmt.Sprintf("%s (%s)", orientationName(img), position)
		r.Cards = append(r.Cards, c)
	}

	page(book.FirstPage, 1, "lightest")
	for _, s := range book.Spreads {
		if s.Type == SpreadLandscape {
			page(s.Single, s.StartPage, fmt.Sprintf("pages %d–%d", s.StartPage, s.StartPage+1))
			continue
		}
		if s.HasLeft {
			page(s.Left, s.StartPage, "left")
		}
		if s.HasRight {
			page(s.Right, s.StartPage+1, "right")
		}
	}
	page(book.LastPage, book.TotalPages, "darkest")
	return r
}

func orientationName(img ImageData) string {
	switch {
	case img.IsLandscape():
		return "landscape"
	case img.IsSquare():
		return "square"
	default:
		return "portrait"
	}
}
