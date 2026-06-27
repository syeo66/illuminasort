package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"golang.org/x/image/draw"
	_ "modernc.org/sqlite"
)

type ImageData struct {
	Path               string
	RelativePath       string
	AverageIlluminance float64
	MedianIlluminance  float64
	ThumbnailDataURL   string
	Width              int
	Height             int
}

func (img ImageData) IsLandscape() bool {
	return img.Width > 0 && img.Width > img.Height
}

func (img ImageData) IsPortrait() bool {
	return img.Width > 0 && img.Height > img.Width
}

func (img ImageData) IsSquare() bool {
	return img.Width > 0 && img.Width == img.Height
}

type SpreadType int

const (
	SpreadLandscape   SpreadType = iota
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

var allFlag bool
var bookPages int
var bookMode string
var replaceFlag bool
var cleanupFlag bool

func main() {
	flag.BoolVar(&allFlag, "all", false, "Generate report from all images in database")
	flag.IntVar(&bookPages, "book", 0, "Select N images for a book from DB (rounded up to multiple of 4), lightest first, darkest last")
	flag.StringVar(&bookMode, "book-mode", "", "Book selection mode: landscape, portrait, or smart")
	flag.BoolVar(&replaceFlag, "replace", false, "Recalculate and replace existing cached values in database")
	flag.BoolVar(&cleanupFlag, "cleanup", false, "Remove database entries for images that no longer exist on disk")
	flag.Parse()

	db, err := initDB()
	if err != nil {
		fmt.Printf("Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if allFlag {
		imageData, err := getAllImagesFromDB(db)
		if err != nil {
			fmt.Printf("Error retrieving images from database: %v\n", err)
			os.Exit(1)
		}
		if len(imageData) == 0 {
			fmt.Println("No images found in database")
			os.Exit(0)
		}
		sort.Slice(imageData, func(i, j int) bool {
			return imageData[i].AverageIlluminance < imageData[j].AverageIlluminance
		})
		outputPath := "illuminance_report_all.html"
		err = generateHTMLAbsolute(imageData, outputPath)
		if err != nil {
			fmt.Printf("Error generating HTML: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nReport generated with %d images: %s\n", len(imageData), outputPath)
		return
	}

	if bookPages > 0 {
		imageData, err := getAllImagesFromDB(db)
		if err != nil {
			fmt.Printf("Error retrieving images from database: %v\n", err)
			os.Exit(1)
		}
		if len(imageData) == 0 {
			fmt.Println("No images found in database")
			os.Exit(0)
		}

		outputPath := "illuminance_book.html"

		switch bookMode {
		case "landscape":
			selected, lerr := selectLandscapeBookImages(imageData, bookPages)
			if lerr != nil {
				fmt.Printf("Error: %v\n", lerr)
				os.Exit(1)
			}
			err = generateBookHTML(selected, bookPages, outputPath)
			if err != nil {
				fmt.Printf("Error generating HTML: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Book report (landscape) generated with %d pages: %s\n", len(selected), outputPath)

		case "portrait":
			var filtered []ImageData
			for _, img := range imageData {
				if img.IsPortrait() || img.IsSquare() {
					filtered = append(filtered, img)
				}
			}
			if len(filtered) == 0 {
				fmt.Println("No portrait or square images found in database")
				os.Exit(1)
			}
			selected := selectBookImages(filtered, bookPages)
			err = generateBookHTML(selected, bookPages, outputPath)
			if err != nil {
				fmt.Printf("Error generating HTML: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Book report (portrait) generated with %d pages: %s\n", len(selected), outputPath)

		case "smart":
			smartBook := selectSmartBookImages(imageData, bookPages)
			err = generateSmartBookHTML(smartBook, bookPages, outputPath)
			if err != nil {
				fmt.Printf("Error generating HTML: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Smart book report generated with %d pages: %s\n", smartBook.TotalPages, outputPath)

		default:
			selected := selectBookImages(imageData, bookPages)
			actualPages := len(selected)
			err = generateBookHTML(selected, bookPages, outputPath)
			if err != nil {
				fmt.Printf("Error generating HTML: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("Book report generated with %d pages (requested %d): %s\n", actualPages, bookPages, outputPath)
		}
		return
	}

	if cleanupFlag {
		removed, err := cleanupDB(db)
		if err != nil {
			fmt.Printf("Error during cleanup: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Cleanup complete: removed %d missing image(s) from database\n", removed)
		return
	}

	if flag.NArg() < 1 {
		fmt.Println("Usage: illuminasort <directory>")
		fmt.Println("       illuminasort --all")
		fmt.Println("       illuminasort --book <pages> [--book-mode landscape|portrait|smart]")
		fmt.Println("       illuminasort --replace <directory>  (recalculate existing cached values)")
		fmt.Println("       illuminasort --cleanup              (remove database entries for missing files)")
		os.Exit(1)
	}

	dir := flag.Arg(0)

	images, err := findImages(dir)
	if err != nil {
		fmt.Printf("Error finding images: %v\n", err)
		os.Exit(1)
	}

	if len(images) == 0 {
		fmt.Println("No images found in the directory")
		os.Exit(0)
	}

	fmt.Printf("Found %d images, analyzing...\n", len(images))

	var imageData []ImageData
	for i, imgPath := range images {
		fmt.Printf("Processing %d/%d: %s\n", i+1, len(images), imgPath)

		absPath, err := filepath.Abs(imgPath)
		if err != nil {
			absPath = imgPath
		}

		if !replaceFlag {
			existing, err2 := getImageFromDB(db, absPath)
			if err2 == nil {
				fmt.Printf("  (using cached values)\n")
				relPath, _ := filepath.Rel(dir, imgPath)
				existing.RelativePath = relPath
				imageData = append(imageData, *existing)
				continue
			}
		}

		avg, median, width, height, err := calculateIlluminance(imgPath)
		if err != nil {
			fmt.Printf("Warning: failed to process %s: %v\n", imgPath, err)
			continue
		}

		thumbnailDataURL, err := generateThumbnail(imgPath)
		if err != nil {
			fmt.Printf("Warning: failed to generate thumbnail for %s: %v\n", imgPath, err)
			continue
		}

		err = saveImageToDB(db, absPath, avg, median, thumbnailDataURL, width, height)
		if err != nil {
			fmt.Printf("Warning: failed to save to database: %v\n", err)
		}

		relPath, _ := filepath.Rel(dir, imgPath)
		imageData = append(imageData, ImageData{
			Path:               imgPath,
			RelativePath:       relPath,
			AverageIlluminance: avg,
			MedianIlluminance:  median,
			Width:              width,
			Height:             height,
		})
	}

	sort.Slice(imageData, func(i, j int) bool {
		return imageData[i].AverageIlluminance < imageData[j].AverageIlluminance
	})

	outputPath := filepath.Join(dir, "illuminance_report.html")
	err = generateHTML(imageData, outputPath)
	if err != nil {
		fmt.Printf("Error generating HTML: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\nReport generated: %s\n", outputPath)
}

func findImages(root string) ([]string, error) {
	var images []string
	validExts := map[string]bool{
		".jpg":  true,
		".jpeg": true,
		".png":  true,
		".gif":  true,
	}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(path))
			if validExts[ext] {
				images = append(images, path)
			}
		}
		return nil
	})

	return images, err
}

func calculateIlluminance(imagePath string) (average float64, median float64, width int, height int, err error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return 0, 0, 0, 0, err
	}

	bounds := img.Bounds()
	width = bounds.Dx()
	height = bounds.Dy()

	var luminanceValues []float64
	var sum float64

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			r8 := float64(r >> 8)
			g8 := float64(g >> 8)
			b8 := float64(b >> 8)
			luminance := 0.2126*r8 + 0.7152*g8 + 0.0722*b8
			luminanceValues = append(luminanceValues, luminance)
			sum += luminance
		}
	}

	totalPixels := width * height
	average = sum / float64(totalPixels)

	sort.Float64s(luminanceValues)
	if totalPixels%2 == 0 {
		median = (luminanceValues[totalPixels/2-1] + luminanceValues[totalPixels/2]) / 2
	} else {
		median = luminanceValues[totalPixels/2]
	}

	return average, median, width, height, nil
}

func generateThumbnail(imagePath string) (string, error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	img, format, err := image.Decode(file)
	if err != nil {
		return "", err
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	maxSize := 200
	var newWidth, newHeight int
	if width > height {
		if width > maxSize {
			newWidth = maxSize
			newHeight = height * maxSize / width
		} else {
			newWidth = width
			newHeight = height
		}
	} else {
		if height > maxSize {
			newHeight = maxSize
			newWidth = width * maxSize / height
		} else {
			newWidth = width
			newHeight = height
		}
	}

	thumbnail := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.CatmullRom.Scale(thumbnail, thumbnail.Bounds(), img, bounds, draw.Over, nil)

	var buf bytes.Buffer
	err = jpeg.Encode(&buf, thumbnail, &jpeg.Options{Quality: 85})
	if err != nil {
		return "", err
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	mimeType := "image/jpeg"
	if format == "png" {
		mimeType = "image/png"
	} else if format == "gif" {
		mimeType = "image/gif"
	}

	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded), nil
}

func generateHTML(data []ImageData, outputPath string) error {
	tmpl := `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Image Illuminance Report</title>
	<style>
		body { font-family: Arial, sans-serif; max-width: 1200px; margin: 0 auto; padding: 20px; background-color: #f5f5f5; }
		h1 { color: #333; text-align: center; }
		.image-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 20px; margin-top: 30px; }
		.image-card { background: white; border-radius: 8px; padding: 15px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); text-align: center; }
		.image-card img { max-width: 200px; max-height: 200px; width: auto; height: auto; object-fit: contain; border-radius: 4px; }
		.image-info { margin-top: 10px; font-size: 14px; color: #666; }
		.image-path { font-size: 12px; color: #999; margin-top: 5px; word-break: break-all; }
		.value { font-weight: bold; color: #333; }
		.stats { background: white; border-radius: 8px; padding: 15px; margin-bottom: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
	</style>
</head>
<body>
	<h1>Image Illuminance Report</h1>
	<div class="stats">
		<p><strong>Total Images:</strong> {{.Count}}</p>
		<p><em>Images sorted by average illuminance (darkest to brightest)</em></p>
	</div>
	<div class="image-grid">
		{{range .Images}}
		<div class="image-card">
			<img src="{{.RelativePath}}" alt="{{.RelativePath}}" loading="lazy">
			<div class="image-info">
				<div>Avg: <span class="value">{{printf "%.2f" .AverageIlluminance}}</span></div>
				<div>Median: <span class="value">{{printf "%.2f" .MedianIlluminance}}</span></div>
			</div>
			<div class="image-path">{{.RelativePath}}</div>
		</div>
		{{end}}
	</div>
</body>
</html>`

	t, err := template.New("report").Parse(tmpl)
	if err != nil {
		return err
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	return t.Execute(file, struct {
		Count  int
		Images []ImageData
	}{Count: len(data), Images: data})
}

func generateHTMLAbsolute(data []ImageData, outputPath string) error {
	tmpl := `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Image Illuminance Report (All Images)</title>
	<style>
		body { font-family: Arial, sans-serif; max-width: 1200px; margin: 0 auto; padding: 20px; background-color: #f5f5f5; }
		h1 { color: #333; text-align: center; }
		.image-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 20px; margin-top: 30px; }
		.image-card { background: white; border-radius: 8px; padding: 15px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); text-align: center; }
		.image-card img { max-width: 200px; max-height: 200px; width: auto; height: auto; object-fit: contain; border-radius: 4px; }
		.image-info { margin-top: 10px; font-size: 14px; color: #666; }
		.image-path { font-size: 12px; color: #999; margin-top: 5px; word-break: break-all; }
		.value { font-weight: bold; color: #333; }
		.stats { background: white; border-radius: 8px; padding: 15px; margin-bottom: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
	</style>
</head>
<body>
	<h1>Image Illuminance Report (All Images)</h1>
	<div class="stats">
		<p><strong>Total Images:</strong> {{.Count}}</p>
		<p><em>Images sorted by average illuminance (darkest to brightest)</em></p>
	</div>
	<div class="image-grid">
		{{range .Images}}
		<div class="image-card">
			<img src="{{.ThumbnailDataURL}}" alt="{{.Path}}" loading="lazy">
			<div class="image-info">
				<div>Avg: <span class="value">{{printf "%.2f" .AverageIlluminance}}</span></div>
				<div>Median: <span class="value">{{printf "%.2f" .MedianIlluminance}}</span></div>
			</div>
			<div class="image-path">{{.Path}}</div>
		</div>
		{{end}}
	</div>
</body>
</html>`

	t, err := template.New("report").Parse(tmpl)
	if err != nil {
		return err
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	return t.Execute(file, struct {
		Count  int
		Images []ImageData
	}{Count: len(data), Images: data})
}

func initDB() (*sql.DB, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dbPath := filepath.Join(homeDir, ".illuminasort.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS images (
			path TEXT PRIMARY KEY,
			average_illuminance REAL NOT NULL,
			median_illuminance REAL NOT NULL,
			thumbnail_data_url TEXT NOT NULL,
			scanned_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	// Migration: add width/height for existing databases (errors ignored if columns exist)
	db.Exec(`ALTER TABLE images ADD COLUMN width INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE images ADD COLUMN height INTEGER NOT NULL DEFAULT 0`)

	return db, nil
}

func saveImageToDB(db *sql.DB, path string, avg, median float64, thumbnailDataURL string, width, height int) error {
	_, err := db.Exec(`
		INSERT OR REPLACE INTO images (path, average_illuminance, median_illuminance, thumbnail_data_url, width, height, scanned_at)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, path, avg, median, thumbnailDataURL, width, height)
	return err
}

func getImageFromDB(db *sql.DB, path string) (*ImageData, error) {
	var img ImageData
	err := db.QueryRow(`
		SELECT path, average_illuminance, median_illuminance, thumbnail_data_url, width, height
		FROM images
		WHERE path = ?
	`, path).Scan(&img.Path, &img.AverageIlluminance, &img.MedianIlluminance, &img.ThumbnailDataURL, &img.Width, &img.Height)
	if err != nil {
		return nil, err
	}
	return &img, nil
}

func generateBookHTML(data []ImageData, requestedPages int, outputPath string) error {
	type PageData struct {
		ImageData
		PageNumber int
	}
	var pages []PageData
	for i, img := range data {
		pages = append(pages, PageData{ImageData: img, PageNumber: i + 1})
	}

	tmpl := `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Book Image Selection</title>
	<style>
		body { font-family: Arial, sans-serif; max-width: 1200px; margin: 0 auto; padding: 20px; background-color: #f5f5f5; }
		h1 { color: #333; text-align: center; }
		.image-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 20px; margin-top: 30px; }
		.image-card { background: white; border-radius: 8px; padding: 15px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); text-align: center; }
		.image-card img { max-width: 200px; max-height: 200px; width: auto; height: auto; object-fit: contain; border-radius: 4px; }
		.page-number { font-size: 18px; font-weight: bold; color: #333; margin-bottom: 8px; }
		.image-info { margin-top: 10px; font-size: 14px; color: #666; }
		.image-path { font-size: 12px; color: #999; margin-top: 5px; word-break: break-all; }
		.value { font-weight: bold; color: #333; }
		.stats { background: white; border-radius: 8px; padding: 15px; margin-bottom: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
	</style>
</head>
<body>
	<h1>Book Image Selection</h1>
	<div class="stats">
		<p><strong>Requested pages:</strong> {{.RequestedPages}} &rarr; <strong>Actual pages:</strong> {{.ActualPages}} (rounded up to multiple of 4)</p>
		<p><em>Page 1 is the lightest image, last page is the darkest, luminance spread evenly in between.</em></p>
	</div>
	<div class="image-grid">
		{{range .Pages}}
		<div class="image-card">
			<div class="page-number">Page {{.PageNumber}}</div>
			<img src="{{.ThumbnailDataURL}}" alt="{{.Path}}" loading="lazy">
			<div class="image-info">
				<div>Avg: <span class="value">{{printf "%.2f" .AverageIlluminance}}</span></div>
				<div>Median: <span class="value">{{printf "%.2f" .MedianIlluminance}}</span></div>
			</div>
			<div class="image-path">{{.Path}}</div>
		</div>
		{{end}}
	</div>
</body>
</html>`

	t, err := template.New("book").Parse(tmpl)
	if err != nil {
		return err
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	return t.Execute(file, struct {
		RequestedPages int
		ActualPages    int
		Pages          []PageData
	}{
		RequestedPages: requestedPages,
		ActualPages:    len(data),
		Pages:          pages,
	})
}

func generateSmartBookHTML(book SmartBook, requestedPages int, outputPath string) error {
	type PageData struct {
		ImageData
		PageNumber int
		Label      string
	}

	pages := []PageData{{ImageData: book.FirstPage, PageNumber: 1, Label: "portrait (lightest)"}}
	for _, s := range book.Spreads {
		if s.Type == SpreadLandscape {
			pages = append(pages, PageData{
				ImageData:  s.Single,
				PageNumber: s.StartPage,
				Label:      fmt.Sprintf("landscape (pages %d–%d)", s.StartPage, s.StartPage+1),
			})
		} else {
			if s.HasLeft {
				pages = append(pages, PageData{ImageData: s.Left, PageNumber: s.StartPage, Label: "portrait (left)"})
			}
			if s.HasRight {
				pages = append(pages, PageData{ImageData: s.Right, PageNumber: s.StartPage + 1, Label: "portrait (right)"})
			}
		}
	}
	pages = append(pages, PageData{ImageData: book.LastPage, PageNumber: book.TotalPages, Label: "portrait (darkest)"})

	tmpl := `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Book Image Selection (Smart)</title>
	<style>
		body { font-family: Arial, sans-serif; max-width: 1200px; margin: 0 auto; padding: 20px; background-color: #f5f5f5; }
		h1 { color: #333; text-align: center; }
		.image-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 20px; margin-top: 30px; }
		.image-card { background: white; border-radius: 8px; padding: 15px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); text-align: center; }
		.image-card img { max-width: 200px; max-height: 200px; width: auto; height: auto; object-fit: contain; border-radius: 4px; }
		.page-number { font-size: 18px; font-weight: bold; color: #333; margin-bottom: 8px; }
		.page-label { font-size: 11px; color: #888; margin-bottom: 6px; }
		.image-info { margin-top: 10px; font-size: 14px; color: #666; }
		.image-path { font-size: 12px; color: #999; margin-top: 5px; word-break: break-all; }
		.value { font-weight: bold; color: #333; }
		.stats { background: white; border-radius: 8px; padding: 15px; margin-bottom: 20px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
	</style>
</head>
<body>
	<h1>Book Image Selection (Smart)</h1>
	<div class="stats">
		<p><strong>Requested pages:</strong> {{.RequestedPages}} &rarr; <strong>Actual pages:</strong> {{.TotalPages}} (rounded up to multiple of 4)</p>
		<p><em>Page 1 (lightest portrait) &bull; Middle spreads: landscape or portrait pairs &bull; Last page (darkest portrait)</em></p>
	</div>
	<div class="image-grid">
		{{range .Pages}}
		<div class="image-card">
			<div class="page-number">Page {{.PageNumber}}</div>
			<div class="page-label">{{.Label}}</div>
			<img src="{{.ThumbnailDataURL}}" alt="{{.Path}}" loading="lazy">
			<div class="image-info">
				<div>Avg: <span class="value">{{printf "%.2f" .AverageIlluminance}}</span></div>
				<div>Median: <span class="value">{{printf "%.2f" .MedianIlluminance}}</span></div>
			</div>
			<div class="image-path">{{.Path}}</div>
		</div>
		{{end}}
	</div>
</body>
</html>`

	t, err := template.New("smart").Parse(tmpl)
	if err != nil {
		return err
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	return t.Execute(file, struct {
		RequestedPages int
		TotalPages     int
		Pages          []PageData
	}{
		RequestedPages: requestedPages,
		TotalPages:     book.TotalPages,
		Pages:          pages,
	})
}

func selectBookImages(images []ImageData, requestedPages int) []ImageData {
	pages := requestedPages
	if pages%4 != 0 {
		pages = pages + (4 - pages%4)
	}
	return selectNImages(images, pages)
}

// selectNImages picks exactly n images with evenly-spread luminance, lightest first and darkest last.
func selectNImages(images []ImageData, n int) []ImageData {
	if n <= 0 || len(images) == 0 {
		return nil
	}

	sorted := make([]ImageData, len(images))
	copy(sorted, images)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].AverageIlluminance < sorted[j].AverageIlluminance
	})

	if len(sorted) <= n {
		result := make([]ImageData, len(sorted))
		for i, img := range sorted {
			result[len(sorted)-1-i] = img
		}
		return result
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

	sort.Slice(selected, func(i, j int) bool {
		return selected[i].AverageIlluminance > selected[j].AverageIlluminance
	})

	return selected
}

// selectLandscapeBookImages anchors page 1 and last page with portraits (lightest/darkest),
// and fills the middle pages with landscape images at evenly-spread luminance.
func selectLandscapeBookImages(allImages []ImageData, requestedPages int) ([]ImageData, error) {
	pages := requestedPages
	if pages%4 != 0 {
		pages += 4 - pages%4
	}

	var portraits, landscapes []ImageData
	for _, img := range allImages {
		// Squares are jokers: eligible for both pools
		if img.IsPortrait() || img.IsSquare() {
			portraits = append(portraits, img)
		}
		if img.IsLandscape() || img.IsSquare() {
			landscapes = append(landscapes, img)
		}
	}

	if len(portraits) == 0 {
		return nil, fmt.Errorf("no portrait or square images found in database for anchor pages")
	}
	if len(landscapes) == 0 {
		return nil, fmt.Errorf("no landscape or square images found in database")
	}

	sort.Slice(portraits, func(i, j int) bool {
		return portraits[i].AverageIlluminance < portraits[j].AverageIlluminance
	})

	firstPage := portraits[len(portraits)-1] // lightest portrait/square
	lastPage := portraits[0]                  // darkest portrait/square

	// Exclude anchor images from the landscape pool to avoid reuse
	usedPaths := map[string]bool{firstPage.Path: true, lastPage.Path: true}
	var availLandscapes []ImageData
	for _, img := range landscapes {
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

func selectSmartBookImages(images []ImageData, requestedPages int) SmartBook {
	pages := requestedPages
	if pages%4 != 0 {
		pages += 4 - pages%4
	}

	// Squares are jokers: added to both pools
	var portraits, landscapes []ImageData
	for _, img := range images {
		if img.IsPortrait() || img.IsSquare() {
			portraits = append(portraits, img)
		}
		if img.IsLandscape() || img.IsSquare() {
			landscapes = append(landscapes, img)
		}
	}

	// Fall back if not enough portrait-like images for anchor pages
	if len(portraits) < 2 {
		for _, img := range images {
			if img.IsLandscape() {
				portraits = append(portraits, img)
			}
		}
		landscapes = nil
	}

	// Sort ascending by luminance (index 0 = darkest, last = lightest)
	sort.Slice(portraits, func(i, j int) bool {
		return portraits[i].AverageIlluminance < portraits[j].AverageIlluminance
	})
	sort.Slice(landscapes, func(i, j int) bool {
		return landscapes[i].AverageIlluminance < landscapes[j].AverageIlluminance
	})

	firstPage := portraits[len(portraits)-1] // lightest
	lastPage := portraits[0]                  // darkest

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

	for i := 0; i < numSpreads; i++ {
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
		sort.Slice(landCandidates, func(a, b int) bool {
			return landCandidates[a].dist < landCandidates[b].dist
		})

		var portCandidates []candidate
		for j, p := range portraits {
			if usedPaths[p.Path] {
				continue
			}
			portCandidates = append(portCandidates, candidate{p, math.Abs(float64(j) - portTargetIdx)})
		}
		sort.Slice(portCandidates, func(a, b int) bool {
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
	}
}

func cleanupDB(db *sql.DB) (int, error) {
	rows, err := db.Query(`SELECT path FROM images`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var missing []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return 0, err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, path)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, path := range missing {
		_, err := db.Exec(`DELETE FROM images WHERE path = ?`, path)
		if err != nil {
			return 0, err
		}
		fmt.Printf("Removed: %s\n", path)
	}

	return len(missing), nil
}

func getAllImagesFromDB(db *sql.DB) ([]ImageData, error) {
	rows, err := db.Query(`
		SELECT path, average_illuminance, median_illuminance, thumbnail_data_url, width, height
		FROM images
		ORDER BY average_illuminance
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var images []ImageData
	for rows.Next() {
		var img ImageData
		err := rows.Scan(&img.Path, &img.AverageIlluminance, &img.MedianIlluminance, &img.ThumbnailDataURL, &img.Width, &img.Height)
		if err != nil {
			return nil, err
		}
		img.RelativePath = img.Path
		images = append(images, img)
	}

	return images, rows.Err()
}
