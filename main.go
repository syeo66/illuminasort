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
}

var allFlag bool
var bookPages int
var replaceFlag bool

func main() {
	flag.BoolVar(&allFlag, "all", false, "Generate report from all images in database")
	flag.IntVar(&bookPages, "book", 0, "Select N images for a book from DB (rounded up to multiple of 4), lightest first, darkest last")
	flag.BoolVar(&replaceFlag, "replace", false, "Recalculate and replace existing cached values in database")
	flag.Parse()

	// Initialize database
	db, err := initDB()
	if err != nil {
		fmt.Printf("Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if allFlag {
		// Generate report from all images in database
		imageData, err := getAllImagesFromDB(db)
		if err != nil {
			fmt.Printf("Error retrieving images from database: %v\n", err)
			os.Exit(1)
		}

		if len(imageData) == 0 {
			fmt.Println("No images found in database")
			os.Exit(0)
		}

		// Sort by average illuminance
		sort.Slice(imageData, func(i, j int) bool {
			return imageData[i].AverageIlluminance < imageData[j].AverageIlluminance
		})

		// Generate HTML report in current directory
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

		selected := selectBookImages(imageData, bookPages)
		actualPages := len(selected)
		outputPath := "illuminance_book.html"
		err = generateBookHTML(selected, bookPages, outputPath)
		if err != nil {
			fmt.Printf("Error generating HTML: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Book report generated with %d pages (requested %d): %s\n", actualPages, bookPages, outputPath)
		return
	}

	if flag.NArg() < 1 {
		fmt.Println("Usage: illuminasort <directory>")
		fmt.Println("       illuminasort --all")
		fmt.Println("       illuminasort --book <pages>")
		fmt.Println("       illuminasort --replace <directory>  (recalculate existing cached values)")
		os.Exit(1)
	}

	dir := flag.Arg(0)

	// Find all images
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

	// Analyze each image
	var imageData []ImageData
	for i, imgPath := range images {
		fmt.Printf("Processing %d/%d: %s\n", i+1, len(images), imgPath)

		// Get absolute path for database lookup
		absPath, err := filepath.Abs(imgPath)
		if err != nil {
			absPath = imgPath
		}

		// Check if image already exists in database
		if !replaceFlag {
			existing, err2 := getImageFromDB(db, absPath)
			if err2 == nil {
				// Image exists in database, use cached values
				fmt.Printf("  (using cached values)\n")
				relPath, _ := filepath.Rel(dir, imgPath)
				imageData = append(imageData, ImageData{
					Path:               imgPath,
					RelativePath:       relPath,
					AverageIlluminance: existing.AverageIlluminance,
					MedianIlluminance:  existing.MedianIlluminance,
				})
				continue
			}
		}

		// Calculate illuminance
		avg, median, err := calculateIlluminance(imgPath)
		if err != nil {
			fmt.Printf("Warning: failed to process %s: %v\n", imgPath, err)
			continue
		}

		// Generate thumbnail
		thumbnailDataURL, err := generateThumbnail(imgPath)
		if err != nil {
			fmt.Printf("Warning: failed to generate thumbnail for %s: %v\n", imgPath, err)
			continue
		}

		// Store in database (absPath already computed above)
		err = saveImageToDB(db, absPath, avg, median, thumbnailDataURL)
		if err != nil {
			fmt.Printf("Warning: failed to save to database: %v\n", err)
		}

		relPath, _ := filepath.Rel(dir, imgPath)
		imageData = append(imageData, ImageData{
			Path:               imgPath,
			RelativePath:       relPath,
			AverageIlluminance: avg,
			MedianIlluminance:  median,
		})
	}

	// Sort by average illuminance
	sort.Slice(imageData, func(i, j int) bool {
		return imageData[i].AverageIlluminance < imageData[j].AverageIlluminance
	})

	// Generate HTML report
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

func calculateIlluminance(imagePath string) (average float64, median float64, err error) {
	file, err := os.Open(imagePath)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return 0, 0, err
	}

	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	var luminanceValues []float64
	var sum float64

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			// Convert to 8-bit values
			r8 := float64(r >> 8)
			g8 := float64(g >> 8)
			b8 := float64(b >> 8)

			// Calculate relative luminance using ITU-R BT.709 coefficients
			luminance := 0.2126*r8 + 0.7152*g8 + 0.0722*b8
			luminanceValues = append(luminanceValues, luminance)
			sum += luminance
		}
	}

	totalPixels := width * height
	average = sum / float64(totalPixels)

	// Calculate median
	sort.Float64s(luminanceValues)
	if totalPixels%2 == 0 {
		median = (luminanceValues[totalPixels/2-1] + luminanceValues[totalPixels/2]) / 2
	} else {
		median = luminanceValues[totalPixels/2]
	}

	return average, median, nil
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

	// Calculate new dimensions (max 200px on longest side)
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

	// Create thumbnail
	thumbnail := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
	draw.CatmullRom.Scale(thumbnail, thumbnail.Bounds(), img, bounds, draw.Over, nil)

	// Encode to JPEG in memory
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, thumbnail, &jpeg.Options{Quality: 85})
	if err != nil {
		return "", err
	}

	// Convert to base64 data URL
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
		body {
			font-family: Arial, sans-serif;
			max-width: 1200px;
			margin: 0 auto;
			padding: 20px;
			background-color: #f5f5f5;
		}
		h1 {
			color: #333;
			text-align: center;
		}
		.image-grid {
			display: grid;
			grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
			gap: 20px;
			margin-top: 30px;
		}
		.image-card {
			background: white;
			border-radius: 8px;
			padding: 15px;
			box-shadow: 0 2px 4px rgba(0,0,0,0.1);
			text-align: center;
		}
		.image-card img {
			max-width: 200px;
			max-height: 200px;
			width: auto;
			height: auto;
			object-fit: contain;
			border-radius: 4px;
		}
		.image-info {
			margin-top: 10px;
			font-size: 14px;
			color: #666;
		}
		.image-path {
			font-size: 12px;
			color: #999;
			margin-top: 5px;
			word-break: break-all;
		}
		.value {
			font-weight: bold;
			color: #333;
		}
		.stats {
			background: white;
			border-radius: 8px;
			padding: 15px;
			margin-bottom: 20px;
			box-shadow: 0 2px 4px rgba(0,0,0,0.1);
		}
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
	}{
		Count:  len(data),
		Images: data,
	})
}

func generateHTMLAbsolute(data []ImageData, outputPath string) error {
	tmpl := `<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<title>Image Illuminance Report (All Images)</title>
	<style>
		body {
			font-family: Arial, sans-serif;
			max-width: 1200px;
			margin: 0 auto;
			padding: 20px;
			background-color: #f5f5f5;
		}
		h1 {
			color: #333;
			text-align: center;
		}
		.image-grid {
			display: grid;
			grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
			gap: 20px;
			margin-top: 30px;
		}
		.image-card {
			background: white;
			border-radius: 8px;
			padding: 15px;
			box-shadow: 0 2px 4px rgba(0,0,0,0.1);
			text-align: center;
		}
		.image-card img {
			max-width: 200px;
			max-height: 200px;
			width: auto;
			height: auto;
			object-fit: contain;
			border-radius: 4px;
		}
		.image-info {
			margin-top: 10px;
			font-size: 14px;
			color: #666;
		}
		.image-path {
			font-size: 12px;
			color: #999;
			margin-top: 5px;
			word-break: break-all;
		}
		.value {
			font-weight: bold;
			color: #333;
		}
		.stats {
			background: white;
			border-radius: 8px;
			padding: 15px;
			margin-bottom: 20px;
			box-shadow: 0 2px 4px rgba(0,0,0,0.1);
		}
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
	}{
		Count:  len(data),
		Images: data,
	})
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

	// Create table if it doesn't exist
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

	return db, nil
}

func saveImageToDB(db *sql.DB, path string, avg, median float64, thumbnailDataURL string) error {
	_, err := db.Exec(`
		INSERT OR REPLACE INTO images (path, average_illuminance, median_illuminance, thumbnail_data_url, scanned_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, path, avg, median, thumbnailDataURL)
	return err
}

func getImageFromDB(db *sql.DB, path string) (*ImageData, error) {
	var img ImageData
	err := db.QueryRow(`
		SELECT path, average_illuminance, median_illuminance, thumbnail_data_url
		FROM images
		WHERE path = ?
	`, path).Scan(&img.Path, &img.AverageIlluminance, &img.MedianIlluminance, &img.ThumbnailDataURL)

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
		body {
			font-family: Arial, sans-serif;
			max-width: 1200px;
			margin: 0 auto;
			padding: 20px;
			background-color: #f5f5f5;
		}
		h1 {
			color: #333;
			text-align: center;
		}
		.image-grid {
			display: grid;
			grid-template-columns: repeat(auto-fill, minmax(250px, 1fr));
			gap: 20px;
			margin-top: 30px;
		}
		.image-card {
			background: white;
			border-radius: 8px;
			padding: 15px;
			box-shadow: 0 2px 4px rgba(0,0,0,0.1);
			text-align: center;
		}
		.image-card img {
			max-width: 200px;
			max-height: 200px;
			width: auto;
			height: auto;
			object-fit: contain;
			border-radius: 4px;
		}
		.page-number {
			font-size: 18px;
			font-weight: bold;
			color: #333;
			margin-bottom: 8px;
		}
		.image-info {
			margin-top: 10px;
			font-size: 14px;
			color: #666;
		}
		.image-path {
			font-size: 12px;
			color: #999;
			margin-top: 5px;
			word-break: break-all;
		}
		.value {
			font-weight: bold;
			color: #333;
		}
		.stats {
			background: white;
			border-radius: 8px;
			padding: 15px;
			margin-bottom: 20px;
			box-shadow: 0 2px 4px rgba(0,0,0,0.1);
		}
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

func selectBookImages(images []ImageData, requestedPages int) []ImageData {
	// Round up to next multiple of 4
	pages := requestedPages
	if pages%4 != 0 {
		pages = pages + (4 - pages%4)
	}

	// Sort by average illuminance ascending (darkest first)
	sorted := make([]ImageData, len(images))
	copy(sorted, images)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].AverageIlluminance < sorted[j].AverageIlluminance
	})

	if len(sorted) <= pages {
		// Not enough images: return all, lightest first
		result := make([]ImageData, len(sorted))
		for i, img := range sorted {
			result[len(sorted)-1-i] = img
		}
		return result
	}

	minLum := sorted[0].AverageIlluminance
	maxLum := sorted[len(sorted)-1].AverageIlluminance

	// Pin first (lightest) and last (darkest) images explicitly
	used := make([]bool, len(sorted))
	selected := make([]ImageData, pages)
	selected[0] = sorted[len(sorted)-1]
	used[len(sorted)-1] = true
	selected[pages-1] = sorted[0]
	used[0] = true

	// Fill intermediate pages with evenly-spaced luminance targets
	for i := 1; i < pages-1; i++ {
		target := maxLum - (maxLum-minLum)*float64(i)/float64(pages-1)

		bestIdx := -1
		bestDist := -1.0
		for j, img := range sorted {
			if used[j] {
				continue
			}
			dist := img.AverageIlluminance - target
			if dist < 0 {
				dist = -dist
			}
			if bestIdx == -1 || dist < bestDist {
				bestIdx = j
				bestDist = dist
			}
		}
		used[bestIdx] = true
		selected[i] = sorted[bestIdx]
	}

	// Sort selected from lightest (page 1) to darkest (last page)
	sort.Slice(selected, func(i, j int) bool {
		return selected[i].AverageIlluminance > selected[j].AverageIlluminance
	})

	return selected
}

func getAllImagesFromDB(db *sql.DB) ([]ImageData, error) {
	rows, err := db.Query(`
		SELECT path, average_illuminance, median_illuminance, thumbnail_data_url
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
		err := rows.Scan(&img.Path, &img.AverageIlluminance, &img.MedianIlluminance, &img.ThumbnailDataURL)
		if err != nil {
			return nil, err
		}
		img.RelativePath = img.Path
		images = append(images, img)
	}

	return images, rows.Err()
}
