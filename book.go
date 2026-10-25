package main

import (
	"errors"
	"math"
	"sort"
)

type SpreadType int

const (
	SpreadLandscape SpreadType = iota
	SpreadTwoPortrait
)

type Spread struct {
	Type      SpreadType
	HasSingle bool
	Single    ImageData
	HasLeft   bool
	Left      ImageData
	HasRight  bool
	Right     ImageData
	StartPage int
}

type SmartBook struct {
	FirstPage  ImageData
	Spreads    []Spread
	LastPage   ImageData
	TotalPages int
}

// FilledPages counts the pages that actually received an image; a landscape
// spread covers two pages.
func (b SmartBook) FilledPages() int {
	pages := 2
	for _, s := range b.Spreads {
		if s.Type == SpreadLandscape {
			pages += 2
			continue
		}
		if s.HasLeft {
			pages++
		}
		if s.HasRight {
			pages++
		}
	}
	return pages
}

var errTooFewAnchors = errors.New("need at least 2 portrait or square images for the first and last page")

// roundUpToMultipleOf4 rounds a page count up to the next multiple of 4.
func roundUpToMultipleOf4(n int) int {
	return (n + 3) / 4 * 4
}

// splitByOrientation separates images into portrait and landscape pools.
// Squares are jokers and go into both. Images with unknown dimensions (legacy
// database rows) go into neither and are counted in unknown.
func splitByOrientation(images []ImageData) (portraits, landscapes []ImageData, unknown int) {
	for _, img := range images {
		if img.Width <= 0 || img.Height <= 0 {
			unknown++
			continue
		}
		if img.IsPortrait() || img.IsSquare() {
			portraits = append(portraits, img)
		}
		if img.IsLandscape() || img.IsSquare() {
			landscapes = append(landscapes, img)
		}
	}
	return portraits, landscapes, unknown
}

// withinAnchors keeps the images whose luminance lies between the darkest and
// lightest anchor, so that no middle page is lighter than page 1 or darker
// than the last page.
func withinAnchors(images []ImageData, lightest, darkest ImageData) []ImageData {
	var kept []ImageData
	for _, img := range images {
		if img.AverageIlluminance >= darkest.AverageIlluminance && img.AverageIlluminance <= lightest.AverageIlluminance {
			kept = append(kept, img)
		}
	}
	return kept
}

// selectBookImages picks a book's worth of images (rounded up to a multiple of 4).
func selectBookImages(images []ImageData, requestedPages int) []ImageData {
	return selectNImages(images, roundUpToMultipleOf4(requestedPages))
}

// selectNImages picks up to n images with evenly-spread luminance, lightest first and darkest last.
func selectNImages(images []ImageData, n int) []ImageData {
	if n <= 0 || len(images) == 0 {
		return nil
	}

	sorted := make([]ImageData, len(images))
	copy(sorted, images)
	sortByLuminance(sorted)

	if len(sorted) <= n {
		result := make([]ImageData, len(sorted))
		for i, img := range sorted {
			result[len(sorted)-1-i] = img
		}
		return result
	}
	if n == 1 {
		return []ImageData{sorted[len(sorted)-1]}
	}

	minLum := sorted[0].AverageIlluminance
	maxLum := sorted[len(sorted)-1].AverageIlluminance

	used := make([]bool, len(sorted))
	selected := make([]ImageData, n)
	selected[0] = sorted[len(sorted)-1]
	used[len(sorted)-1] = true
	selected[n-1] = sorted[0]
	used[0] = true

	for i := 1; i < n-1; i++ {
		target := maxLum - (maxLum-minLum)*float64(i)/float64(n-1)

		bestIdx := -1
		bestDist := -1.0
		for j, img := range sorted {
			if used[j] {
				continue
			}
			dist := math.Abs(img.AverageIlluminance - target)
			if bestIdx == -1 || dist < bestDist {
				bestIdx = j
				bestDist = dist
			}
		}
		used[bestIdx] = true
		selected[i] = sorted[bestIdx]
	}

	sort.SliceStable(selected, func(i, j int) bool {
		return selected[i].AverageIlluminance > selected[j].AverageIlluminance
	})

	return selected
}

// selectPortraitBookImages uses only portrait and square images.
func selectPortraitBookImages(allImages []ImageData, requestedPages int) ([]ImageData, error) {
	portraits, _, _ := splitByOrientation(allImages)
	if len(portraits) == 0 {
		return nil, errors.New("no portrait or square images found in database")
	}
	return selectBookImages(portraits, requestedPages), nil
}

// selectLandscapeBookImages anchors page 1 and last page with portraits (lightest/darkest),
// and fills the middle pages with landscape images at evenly-spread luminance.
func selectLandscapeBookImages(allImages []ImageData, requestedPages int) ([]ImageData, error) {
	pages := roundUpToMultipleOf4(requestedPages)

	portraits, landscapes, _ := splitByOrientation(allImages)
	if len(portraits) < 2 {
		return nil, errTooFewAnchors
	}
	if len(landscapes) == 0 {
		return nil, errors.New("no landscape or square images found in database")
	}

	sortByLuminance(portraits)
	firstPage := portraits[len(portraits)-1] // lightest portrait/square
	lastPage := portraits[0]                 // darkest portrait/square

	// Exclude anchor images from the landscape pool to avoid reuse
	usedPaths := map[string]bool{firstPage.Path: true, lastPage.Path: true}
	var availLandscapes []ImageData
	for _, img := range withinAnchors(landscapes, firstPage, lastPage) {
		if !usedPaths[img.Path] {
			availLandscapes = append(availLandscapes, img)
		}
	}

	middle := selectNImages(availLandscapes, pages-2)

	result := make([]ImageData, 0, len(middle)+2)
	result = append(result, firstPage)
	result = append(result, middle...)
	result = append(result, lastPage)
	return result, nil
}

func selectSmartBookImages(images []ImageData, requestedPages int) (SmartBook, error) {
	pages := roundUpToMultipleOf4(requestedPages)

	portraits, landscapes, _ := splitByOrientation(images)

	// Fall back if not enough portrait-like images for anchor pages
	if len(portraits) < 2 {
		for _, img := range landscapes {
			if img.IsLandscape() {
				portraits = append(portraits, img)
			}
		}
		landscapes = nil
	}
	if len(portraits) < 2 {
		return SmartBook{}, errors.New("need at least 2 images with known dimensions for the first and last page")
	}

	// Sort ascending by luminance (index 0 = darkest, last = lightest)
	sortByLuminance(portraits)
	sortByLuminance(landscapes)

	firstPage := portraits[len(portraits)-1] // lightest
	lastPage := portraits[0]                 // darkest
	landscapes = withinAnchors(landscapes, firstPage, lastPage)

	maxLum := firstPage.AverageIlluminance
	minLum := lastPage.AverageIlluminance

	// Track used images by path (shared across pools so squares aren't reused)
	usedPaths := map[string]bool{
		firstPage.Path: true,
		lastPage.Path:  true,
	}

	numSpreads := (pages - 2) / 2
	spreads := make([]Spread, 0, numSpreads)

	numLandscapes := len(landscapes)
	numPortraits := len(portraits)

	type candidate struct {
		img  ImageData
		dist float64
	}

	for i := range numSpreads {
		// Select which image from each pool using index-based targeting so that
		// images are drawn from evenly-spaced positions in the sorted pool. This
		// prevents ordering inversions when the dark end of the pool is exhausted.
		// Pools are sorted ascending (index 0 = darkest, N-1 = lightest), so
		// spread 0 (brightest) targets the high end and the last spread targets index 0.
		landTargetIdx := float64(numSpreads-i) / float64(numSpreads+1) * float64(numLandscapes-1)
		portTargetIdx := float64(numSpreads-i) / float64(numSpreads+1) * float64(numPortraits-1)

		var landCandidates []candidate
		for j, l := range landscapes {
			if usedPaths[l.Path] {
				continue
			}
			landCandidates = append(landCandidates, candidate{l, math.Abs(float64(j) - landTargetIdx)})
		}
		sort.SliceStable(landCandidates, func(a, b int) bool {
			return landCandidates[a].dist < landCandidates[b].dist
		})

		var portCandidates []candidate
		for j, p := range portraits {
			if usedPaths[p.Path] {
				continue
			}
			portCandidates = append(portCandidates, candidate{p, math.Abs(float64(j) - portTargetIdx)})
		}
		sort.SliceStable(portCandidates, func(a, b int) bool {
			return portCandidates[a].dist < portCandidates[b].dist
		})

		startPage := 2 + i*2

		// Decide landscape vs portrait using luminance distance so the comparison
		// is in consistent units regardless of the two pools having different sizes.
		target := maxLum - (maxLum-minLum)*float64(i+1)/float64(numSpreads+1)
		useLandscape := len(landCandidates) > 0 && (len(portCandidates) < 2 ||
			math.Abs(landCandidates[0].img.AverageIlluminance-target) <
				(math.Abs(portCandidates[0].img.AverageIlluminance-target)+math.Abs(portCandidates[1].img.AverageIlluminance-target))/2)

		if useLandscape {
			chosen := landCandidates[0].img
			usedPaths[chosen.Path] = true
			spreads = append(spreads, Spread{
				Type:      SpreadLandscape,
				HasSingle: true,
				Single:    chosen,
				StartPage: startPage,
			})
		} else if len(portCandidates) >= 2 {
			left := portCandidates[0].img
			right := portCandidates[1].img
			usedPaths[left.Path] = true
			usedPaths[right.Path] = true
			// Lighter on left (even page), darker on right (odd page)
			if left.AverageIlluminance < right.AverageIlluminance {
				left, right = right, left
			}
			spreads = append(spreads, Spread{
				Type:      SpreadTwoPortrait,
				HasLeft:   true,
				Left:      left,
				HasRight:  true,
				Right:     right,
				StartPage: startPage,
			})
		} else if len(portCandidates) == 1 {
			usedPaths[portCandidates[0].img.Path] = true
			spreads = append(spreads, Spread{
				Type:      SpreadTwoPortrait,
				HasLeft:   true,
				Left:      portCandidates[0].img,
				StartPage: startPage,
			})
		}
	}

	return SmartBook{
		FirstPage:  firstPage,
		Spreads:    spreads,
		LastPage:   lastPage,
		TotalPages: pages,
	}, nil
}
