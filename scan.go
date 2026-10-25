package main

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var imageExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".gif":  true,
	".webp": true,
}

type imageFile struct {
	AbsPath string
	RelPath string
	ModTime int64 // Unix nanoseconds
	Size    int64
}

// findImages walks root for image files. Unreadable entries are reported and
// skipped instead of aborting the whole walk.
func findImages(root string) ([]imageFile, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}

	var files []imageFile
	err = filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == absRoot {
				return err
			}
			warnf("skipping %s: %v", path, err)
			return nil
		}
		if d.IsDir() || !imageExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		// Stat rather than d.Info() so symlinked images report their target's mtime and size
		info, err := os.Stat(path)
		if err != nil {
			warnf("skipping %s: %v", path, err)
			return nil
		}
		rel, err := filepath.Rel(absRoot, path)
		if err != nil {
			rel = path
		}
		files = append(files, imageFile{
			AbsPath: path,
			RelPath: rel,
			ModTime: info.ModTime().UnixNano(),
			Size:    info.Size(),
		})
		return nil
	})
	return files, err
}

// cacheValid reports whether a database entry is still accurate for file.
func cacheValid(cached ImageData, file imageFile) bool {
	return cached.ModTime == file.ModTime && cached.Size == file.Size && cached.Width > 0
}

// scanDirectory analyzes every image under dir, reusing valid cache entries
// unless replace is set, and returns the results sorted darkest first.
func scanDirectory(db *sql.DB, dir string, replace bool, jobs int) ([]ImageData, error) {
	files, err := findImages(dir)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}

	var results []ImageData
	var pending []imageFile
	for _, f := range files {
		if !replace {
			cached, err := getImage(db, f.AbsPath)
			if err == nil && cacheValid(cached, f) {
				cached.RelativePath = f.RelPath
				results = append(results, cached)
				continue
			}
		}
		pending = append(pending, f)
	}

	fmt.Printf("Found %d images (%d cached), analyzing %d...\n", len(files), len(results), len(pending))

	type result struct {
		file imageFile
		data analysis
		err  error
	}
	work := make(chan imageFile)
	done := make(chan result)

	var wg sync.WaitGroup
	for range jobs {
		wg.Go(func() {
			for f := range work {
				a, err := analyzeImage(f.AbsPath)
				done <- result{f, a, err}
			}
		})
	}
	go func() {
		for _, f := range pending {
			work <- f
		}
		close(work)
		wg.Wait()
		close(done)
	}()

	// Results are written to the database from this goroutine only
	writer := &batchWriter{db: db}
	processed := 0
	for r := range done {
		processed++
		fmt.Printf("Processed %d/%d: %s\n", processed, len(pending), r.file.RelPath)
		if r.err != nil {
			warnf("failed to process %s: %v", r.file.AbsPath, r.err)
			continue
		}
		img := ImageData{
			Path:               r.file.AbsPath,
			RelativePath:       r.file.RelPath,
			AverageIlluminance: r.data.Average,
			MedianIlluminance:  r.data.Median,
			Thumbnail:          r.data.Thumbnail,
			Width:              r.data.Width,
			Height:             r.data.Height,
			ModTime:            r.file.ModTime,
			Size:               r.file.Size,
		}
		if err := writer.save(img); err != nil {
			warnf("failed to save %s to database: %v", r.file.AbsPath, err)
		}
		results = append(results, img)
	}
	if err := writer.flush(); err != nil {
		warnf("failed to save results to database: %v", err)
	}

	sortByLuminance(results)
	return results, nil
}

// sortByLuminance sorts images darkest first.
func sortByLuminance(images []ImageData) {
	sort.SliceStable(images, func(i, j int) bool {
		return images[i].AverageIlluminance < images[j].AverageIlluminance
	})
}
