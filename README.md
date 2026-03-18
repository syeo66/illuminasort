# IlluminaSort

A Go CLI tool that analyzes image illuminance and generates an HTML report.

## Features

- Recursively scans directories for images (JPG, JPEG, PNG, GIF)
- Calculates average and median illuminance for each image using ITU-R BT.709 coefficients
- Generates and stores thumbnails (200px max on longest side) as base64 data URLs
- Stores all scanned images in a SQLite database (`$HOME/.illuminasort.db`)
- Caches illuminance calculations and thumbnails to avoid reprocessing the same images
- Generates portable HTML reports with embedded thumbnail data URLs
- Can generate reports from all images in the database with the `--all` flag

## Installation

```bash
go build -o illuminasort
```

## Usage

### Scan a directory

```bash
./illuminasort <directory>
```

Example:
```bash
./illuminasort ~/Photos
```

This will:
1. Scan the specified directory and all subdirectories for images
2. Calculate illuminance values and generate thumbnails for each image (or use cached values from database)
3. Store absolute paths, illuminance values, and thumbnail data URLs in the database (`$HOME/.illuminasort.db`)
4. Generate `illuminance_report.html` in the specified directory with relative image paths

### Generate report from all scanned images

```bash
./illuminasort --all
```

This will:
1. Retrieve all images from the database (from all previously scanned directories)
2. Generate `illuminance_report_all.html` in the current directory
3. The report uses embedded thumbnail data URLs, making it completely portable (no external image files needed)

## Output

### Directory scan report (`illuminance_report.html`)
- Total number of images processed
- Grid of images sorted from darkest to brightest (by average illuminance)
- Images shown with relative paths
- For each image:
  - Thumbnail (max 200px on longest side)
  - Average illuminance value
  - Median illuminance value
  - Relative path to the image

### All images report (`illuminance_report_all.html`)
- Total number of all images in database
- Grid of all images sorted from darkest to brightest
- Thumbnails embedded as base64 data URLs (fully portable, no external files needed)
- For each image:
  - Embedded thumbnail (max 200px on longest side)
  - Average illuminance value
  - Median illuminance value
  - Absolute path to the original image

## Database

All scanned images are stored in `$HOME/.illuminasort.db`. The database contains:
- Image absolute path (unique identifier)
- Average illuminance value
- Median illuminance value
- Thumbnail as base64-encoded data URL (JPEG format, max 200px)
- Timestamp of last scan

Images are identified by their absolute path, so moving an image will cause it to be rescanned. If you scan the same directory again, previously scanned images will use cached values and thumbnails from the database, making subsequent scans much faster.

## Thumbnail Storage

Thumbnails are stored as base64-encoded data URLs in the database. This approach has several benefits:
- **Portable reports**: The `--all` report can be moved or shared without needing access to the original images
- **Fast loading**: Thumbnails are embedded directly in the HTML (no separate HTTP requests)
- **Database-complete**: All image information is self-contained in the database
- **Efficient**: Thumbnails are generated once and cached for future use

## Illuminance Calculation

Illuminance is calculated using the relative luminance formula from ITU-R BT.709:

```
Luminance = 0.2126 × R + 0.7152 × G + 0.0722 × B
```

Where R, G, B are the red, green, and blue color values (0-255).
