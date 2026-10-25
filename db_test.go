package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrateLegacyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The original schema, before width/height were added
	_, err = legacy.Exec(`
		CREATE TABLE images (
			path TEXT PRIMARY KEY,
			average_illuminance REAL NOT NULL,
			median_illuminance REAL NOT NULL,
			thumbnail_data_url TEXT NOT NULL,
			scanned_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`)
	if err != nil {
		t.Fatal(err)
	}
	thumb := []byte{0xFF, 0xD8, 0xFF, 0x01, 0x02}
	legacy.Exec(`INSERT INTO images (path, average_illuminance, median_illuminance, thumbnail_data_url) VALUES (?, 1.5, 2.5, ?)`,
		"/photos/a.png", "data:image/png;base64,"+base64.StdEncoding.EncodeToString(thumb))
	legacy.Exec(`INSERT INTO images (path, average_illuminance, median_illuminance, thumbnail_data_url) VALUES ('/photos/broken.jpg', 1, 1, 'garbage')`)
	legacy.Close()

	for range 2 { // second open must be a no-op
		db, err := openDB(path)
		if err != nil {
			t.Fatal(err)
		}
		images, err := getAllImages(db)
		if err != nil {
			t.Fatal(err)
		}
		if len(images) != 1 {
			t.Fatalf("got %d images, want 1 (broken row dropped)", len(images))
		}
		got := images[0]
		if got.Path != "/photos/a.png" || got.AverageIlluminance != 1.5 || string(got.Thumbnail) != string(thumb) || got.Width != 0 || got.ModTime != 0 {
			t.Errorf("unexpected migrated row: %+v", got)
		}
		var version int
		db.QueryRow(`PRAGMA user_version`).Scan(&version)
		if version != schemaVersion {
			t.Errorf("user_version %d, want %d", version, schemaVersion)
		}
		db.Close()
	}
}

func TestSaveGetAndCleanup(t *testing.T) {
	db := openTestDB(t)
	for _, p := range []string{"/keep.jpg", "/gone.jpg"} {
		if err := saveImage(db, ImageData{Path: p, AverageIlluminance: 3, Thumbnail: []byte{1}, Width: 4, Height: 5, ModTime: 6, Size: 7}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := getImage(db, "/keep.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 4 || got.Height != 5 || got.ModTime != 6 || got.Size != 7 {
		t.Errorf("round trip lost data: %+v", got)
	}

	removed, err := cleanupDB(db, func(p string) bool { return p == "/keep.jpg" })
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "/gone.jpg" {
		t.Errorf("removed %v, want [/gone.jpg]", removed)
	}
	if _, err := getImage(db, "/gone.jpg"); err != sql.ErrNoRows {
		t.Errorf("deleted row still present: %v", err)
	}
}

func writePNG(t *testing.T, path string, c color.Color, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func TestScanDirectoryInvalidatesChangedFiles(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	writePNG(t, path, color.Black, 4, 4)

	images, err := scanDirectory(db, dir, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].AverageIlluminance != 0 {
		t.Fatalf("first scan: %+v", images)
	}

	// Replace the file in place with different content and a new mtime
	writePNG(t, path, color.White, 4, 4)
	later := time.Now().Add(time.Hour)
	os.Chtimes(path, later, later)

	images, err = scanDirectory(db, dir, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].AverageIlluminance < 254 {
		t.Errorf("edited file served from stale cache: %+v", images)
	}
}

func TestScanDirectoryRefreshesRowsWithoutDimensions(t *testing.T) {
	db := openTestDB(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.png")
	writePNG(t, path, color.White, 6, 3)
	info, _ := os.Stat(path)
	abs, _ := filepath.Abs(path)
	// A legacy-style row with matching mtime/size but no dimensions
	saveImage(db, ImageData{Path: abs, Thumbnail: []byte{1}, ModTime: info.ModTime().UnixNano(), Size: info.Size()})

	images, err := scanDirectory(db, dir, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Width != 6 || images[0].Height != 3 {
		t.Errorf("row without dimensions was not refreshed: %+v", images)
	}
}

func TestFindImagesSkipsUnreadableDirectories(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits not enforced")
	}
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "ok.png"), color.White, 1, 1)
	locked := filepath.Join(dir, "locked")
	os.Mkdir(locked, 0o755)
	writePNG(t, filepath.Join(locked, "hidden.png"), color.White, 1, 1)
	os.Chmod(locked, 0)
	t.Cleanup(func() { os.Chmod(locked, 0o755) })

	files, err := findImages(dir)
	if err != nil {
		t.Fatalf("walk aborted: %v", err)
	}
	if len(files) != 1 || files[0].RelPath != "ok.png" {
		t.Errorf("got %+v, want only ok.png", files)
	}
}

func TestBatchWriterSavesAllImages(t *testing.T) {
	db := openTestDB(t)
	w := &batchWriter{db: db}
	const n = saveBatchSize*2 + 5 // two full batches and a partial one
	for i := range n {
		if err := w.save(ImageData{Path: fmt.Sprintf("/img/%d.jpg", i), Thumbnail: []byte{1}, Width: 1, Height: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	images, err := getAllImages(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != n {
		t.Errorf("got %d images, want %d", len(images), n)
	}
}
