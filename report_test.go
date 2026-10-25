package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func renderReport(t *testing.T, r report) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.html")
	if err := writeReport(path, r); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestReportEscapesPaths(t *testing.T) {
	img := ImageData{Path: `/p/<script>alert(1)</script>&"x".jpg`, RelativePath: `sub dir/a#b?.jpg`, Thumbnail: []byte{0xFF, 0xD8}}

	out := renderReport(t, allImagesReport([]ImageData{img}))
	if strings.Contains(out, "<script>alert") {
		t.Error("path was not HTML-escaped")
	}
	if !strings.Contains(out, `src="data:image/jpeg;base64,/9g="`) {
		t.Error("thumbnail data URL missing or sanitized")
	}

	out = renderReport(t, scanReport([]ImageData{img}))
	if !strings.Contains(out, `src="./sub%20dir/a%23b%3f.jpg"`) && !strings.Contains(out, `src="./sub%20dir/a%23b%3F.jpg"`) {
		t.Errorf("relative path not URL-encoded:\n%s", out)
	}
}

func TestBookReportWarnsWhenShort(t *testing.T) {
	out := renderReport(t, bookReport("Book", []ImageData{{Path: "/a.jpg"}}, 5))
	if !strings.Contains(out, "only 1 of 8 pages") {
		t.Errorf("missing shortfall warning:\n%s", out)
	}
}

func TestSmartBookReportLabelsActualOrientation(t *testing.T) {
	// Only one portrait, so smart mode falls back to landscapes for the anchors
	images := []ImageData{testImage("p", 100, 2, 3), testImage("l-dark", 10, 3, 2), testImage("l-light", 200, 3, 2)}
	book, err := selectSmartBookImages(images, 4)
	if err != nil {
		t.Fatal(err)
	}
	r := smartBookReport(book, 4)
	if first := r.Cards[0].Label; first != "landscape (lightest)" {
		t.Errorf("first page label = %q, want %q", first, "landscape (lightest)")
	}
	if last := r.Cards[len(r.Cards)-1].Label; last != "landscape (darkest)" {
		t.Errorf("last page label = %q, want %q", last, "landscape (darkest)")
	}
}
