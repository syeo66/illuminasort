package main

import (
	"errors"
	"fmt"
	"testing"
)

func testImage(name string, lum float64, w, h int) ImageData {
	return ImageData{Path: "/img/" + name, AverageIlluminance: lum, MedianIlluminance: lum, Width: w, Height: h}
}

// mixedLibrary returns n portraits, n landscapes and a few squares spread over 0–255.
func mixedLibrary(n int) []ImageData {
	var images []ImageData
	for i := range n {
		images = append(images,
			testImage(fmt.Sprintf("p%d", i), float64(i)*250/float64(n), 2, 3),
			testImage(fmt.Sprintf("l%d", i), float64(i)*250/float64(n)+1, 3, 2),
		)
	}
	images = append(images, testImage("sq-dark", 0.5, 2, 2), testImage("sq-light", 254, 2, 2))
	return images
}

func assertDistinct(t *testing.T, images []ImageData) {
	t.Helper()
	seen := map[string]bool{}
	for _, img := range images {
		if seen[img.Path] {
			t.Errorf("image %s used twice", img.Path)
		}
		seen[img.Path] = true
	}
}

func assertLightestFirst(t *testing.T, images []ImageData) {
	t.Helper()
	for i := 1; i < len(images); i++ {
		if images[i].AverageIlluminance > images[i-1].AverageIlluminance {
			t.Errorf("page %d (%.1f) is lighter than page %d (%.1f)", i+1, images[i].AverageIlluminance, i, images[i-1].AverageIlluminance)
		}
	}
}

func TestRoundUpToMultipleOf4(t *testing.T) {
	for in, want := range map[int]int{1: 4, 4: 4, 5: 8, 20: 20, 21: 24} {
		if got := roundUpToMultipleOf4(in); got != want {
			t.Errorf("roundUpToMultipleOf4(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestSelectBookImages(t *testing.T) {
	images := mixedLibrary(30)
	selected := selectBookImages(images, 10)
	if len(selected) != 12 {
		t.Fatalf("got %d images, want 12", len(selected))
	}
	assertDistinct(t, selected)
	assertLightestFirst(t, selected)
	if selected[0].Path != "/img/sq-light" || selected[11].Path != "/img/p0" {
		t.Errorf("anchors are %s and %s, want lightest and darkest", selected[0].Path, selected[11].Path)
	}
}

func TestSelectBookImagesFewerThanPages(t *testing.T) {
	images := []ImageData{testImage("a", 10, 1, 1), testImage("b", 200, 1, 1)}
	selected := selectBookImages(images, 8)
	if len(selected) != 2 || selected[0].Path != "/img/b" {
		t.Errorf("got %v, want both images lightest first", selected)
	}
}

func TestSplitByOrientationSkipsUnknownDimensions(t *testing.T) {
	images := []ImageData{testImage("legacy", 10, 0, 0), testImage("p", 10, 2, 3), testImage("sq", 10, 2, 2)}
	portraits, landscapes, unknown := splitByOrientation(images)
	if len(portraits) != 2 || len(landscapes) != 1 || unknown != 1 {
		t.Errorf("got %d portraits, %d landscapes, %d unknown; want 2, 1, 1", len(portraits), len(landscapes), unknown)
	}
}

func TestSelectLandscapeBookImages(t *testing.T) {
	selected, err := selectLandscapeBookImages(mixedLibrary(30), 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 12 {
		t.Fatalf("got %d pages, want 12", len(selected))
	}
	assertDistinct(t, selected)
	if selected[0].IsLandscape() || selected[11].IsLandscape() {
		t.Error("anchor pages must be portrait or square")
	}
	for _, img := range selected[1:11] {
		if img.IsPortrait() {
			t.Errorf("middle page %s is portrait", img.Path)
		}
	}
}

func TestSelectLandscapeBookNeedsTwoAnchors(t *testing.T) {
	images := []ImageData{testImage("p", 100, 2, 3), testImage("l1", 50, 3, 2), testImage("l2", 150, 3, 2)}
	if _, err := selectLandscapeBookImages(images, 4); !errors.Is(err, errTooFewAnchors) {
		t.Errorf("got %v, want errTooFewAnchors", err)
	}
}

func TestSelectPortraitBookImages(t *testing.T) {
	selected, err := selectPortraitBookImages(mixedLibrary(30), 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, img := range selected {
		if img.IsLandscape() {
			t.Errorf("%s is landscape", img.Path)
		}
	}
	if _, err := selectPortraitBookImages([]ImageData{testImage("l", 1, 3, 2)}, 4); err == nil {
		t.Error("expected an error without portrait images")
	}
}

func TestSelectSmartBookImages(t *testing.T) {
	book, err := selectSmartBookImages(mixedLibrary(30), 20)
	if err != nil {
		t.Fatal(err)
	}
	if book.TotalPages != 20 || book.FilledPages() != 20 {
		t.Errorf("total %d, filled %d; want 20 and 20", book.TotalPages, book.FilledPages())
	}
	if book.FirstPage.Path == book.LastPage.Path {
		t.Error("first and last page use the same image")
	}

	used := []ImageData{book.FirstPage, book.LastPage}
	for i, s := range book.Spreads {
		if want := 2 + i*2; s.StartPage != want {
			t.Errorf("spread %d starts at page %d, want %d", i, s.StartPage, want)
		}
		switch s.Type {
		case SpreadLandscape:
			used = append(used, s.Single)
		case SpreadTwoPortrait:
			used = append(used, s.Left, s.Right)
			if s.Left.AverageIlluminance < s.Right.AverageIlluminance {
				t.Errorf("spread %d: left page is darker than right", i)
			}
		}
	}
	assertDistinct(t, used)
}

func TestSelectSmartBookWithLegacyRowsDoesNotPanic(t *testing.T) {
	images := []ImageData{testImage("a", 10, 0, 0), testImage("b", 100, 0, 0)}
	if _, err := selectSmartBookImages(images, 8); err == nil {
		t.Error("expected an error when no image has known dimensions")
	}
}

func TestSelectSmartBookReportsShortfall(t *testing.T) {
	images := []ImageData{testImage("p1", 10, 2, 3), testImage("p2", 200, 2, 3), testImage("l", 100, 3, 2)}
	book, err := selectSmartBookImages(images, 12)
	if err != nil {
		t.Fatal(err)
	}
	if book.FilledPages() != 4 {
		t.Errorf("filled %d pages, want 4 (two anchors + one landscape spread)", book.FilledPages())
	}
}
