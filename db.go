package main

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// schemaVersion is stored in PRAGMA user_version.
//
//	0: legacy schema (thumbnail_data_url TEXT, width/height possibly missing)
//	2: thumbnail BLOB plus mod_time/size for cache invalidation
const schemaVersion = 2

const createImagesTable = `
	CREATE TABLE images (
		path TEXT PRIMARY KEY,
		average_illuminance REAL NOT NULL,
		median_illuminance REAL NOT NULL,
		thumbnail BLOB NOT NULL,
		width INTEGER NOT NULL DEFAULT 0,
		height INTEGER NOT NULL DEFAULT 0,
		mod_time INTEGER NOT NULL DEFAULT 0,
		size INTEGER NOT NULL DEFAULT 0,
		scanned_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)
`

const imageColumns = `path, average_illuminance, median_illuminance, thumbnail, width, height, mod_time, size`

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection keeps SQLite writes serialized without busy errors
	db.SetMaxOpenConns(1)

	if err := migrateDB(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating database %s: %w", path, err)
	}
	return db, nil
}

func migrateDB(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version >= schemaVersion {
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var tables int
	if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'images'`).Scan(&tables); err != nil {
		return err
	}
	if tables > 0 {
		err = migrateLegacyImages(tx)
	} else {
		_, err = tx.Exec(createImagesTable)
	}
	if err != nil {
		return err
	}

	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if tables > 0 {
		// Reclaim the space freed by storing thumbnails as bytes instead of base64
		if _, err := db.Exec(`VACUUM`); err != nil {
			return err
		}
	}
	return nil
}

// migrateLegacyImages rebuilds a version-0 images table into the current schema.
// Migrated rows get mod_time 0, so the next scan of their directory re-analyzes them.
func migrateLegacyImages(tx *sql.Tx) error {
	hasDimensions, err := hasColumn(tx, "images", "width")
	if err != nil {
		return err
	}
	dims := "0, 0"
	if hasDimensions {
		dims = "width, height"
	}

	rows, err := tx.Query(`SELECT path, average_illuminance, median_illuminance, thumbnail_data_url, ` + dims + `, scanned_at FROM images`)
	if err != nil {
		return err
	}
	type legacyRow struct {
		path          string
		avg, median   float64
		dataURL       string
		width, height int
		scannedAt     sql.NullString
	}
	var legacy []legacyRow
	for rows.Next() {
		var r legacyRow
		if err := rows.Scan(&r.path, &r.avg, &r.median, &r.dataURL, &r.width, &r.height, &r.scannedAt); err != nil {
			rows.Close()
			return err
		}
		legacy = append(legacy, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.Exec(`DROP TABLE images`); err != nil {
		return err
	}
	if _, err := tx.Exec(createImagesTable); err != nil {
		return err
	}
	for _, r := range legacy {
		thumb, ok := decodeDataURL(r.dataURL)
		if !ok {
			// Unreadable thumbnail: drop the row, the next scan recreates it
			continue
		}
		_, err := tx.Exec(`INSERT INTO images (`+imageColumns+`, scanned_at) VALUES (?, ?, ?, ?, ?, ?, 0, 0, COALESCE(?, CURRENT_TIMESTAMP))`,
			r.path, r.avg, r.median, thumb, r.width, r.height, r.scannedAt)
		if err != nil {
			return err
		}
	}
	return nil
}

func hasColumn(tx *sql.Tx, table, column string) (bool, error) {
	rows, err := tx.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func decodeDataURL(s string) ([]byte, bool) {
	_, payload, found := strings.Cut(s, ";base64,")
	if !found {
		return nil, false
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func saveImage(db execer, img ImageData) error {
	_, err := db.Exec(`
		INSERT OR REPLACE INTO images (`+imageColumns+`, scanned_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`, img.Path, img.AverageIlluminance, img.MedianIlluminance, img.Thumbnail, img.Width, img.Height, img.ModTime, img.Size)
	return err
}

// saveBatchSize is how many images are saved per transaction. An interrupted
// scan loses at most this many analyses.
const saveBatchSize = 100

// batchWriter groups saves into transactions, since committing every row on
// its own waits for a disk sync per image.
type batchWriter struct {
	db *sql.DB
	tx *sql.Tx
	n  int
}

func (w *batchWriter) save(img ImageData) error {
	if w.tx == nil {
		tx, err := w.db.Begin()
		if err != nil {
			return err
		}
		w.tx = tx
	}
	if err := saveImage(w.tx, img); err != nil {
		return err
	}
	w.n++
	if w.n >= saveBatchSize {
		return w.flush()
	}
	return nil
}

// flush commits the pending saves, if any.
func (w *batchWriter) flush() error {
	if w.tx == nil {
		return nil
	}
	err := w.tx.Commit()
	w.tx, w.n = nil, 0
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanImage(row rowScanner) (ImageData, error) {
	var img ImageData
	err := row.Scan(&img.Path, &img.AverageIlluminance, &img.MedianIlluminance, &img.Thumbnail, &img.Width, &img.Height, &img.ModTime, &img.Size)
	return img, err
}

// getImage returns the cached entry for path, or sql.ErrNoRows.
func getImage(db *sql.DB, path string) (ImageData, error) {
	return scanImage(db.QueryRow(`SELECT `+imageColumns+` FROM images WHERE path = ?`, path))
}

func getAllImages(db *sql.DB) ([]ImageData, error) {
	rows, err := db.Query(`SELECT ` + imageColumns + ` FROM images ORDER BY average_illuminance`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var images []ImageData
	for rows.Next() {
		img, err := scanImage(rows)
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, rows.Err()
}

// cleanupDB deletes entries whose files no longer exist and returns their paths.
func cleanupDB(db *sql.DB, exists func(path string) bool) ([]string, error) {
	rows, err := db.Query(`SELECT path FROM images`)
	if err != nil {
		return nil, err
	}
	var missing []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return nil, err
		}
		if !exists(path) {
			missing = append(missing, path)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for _, path := range missing {
		if _, err := tx.Exec(`DELETE FROM images WHERE path = ?`, path); err != nil {
			return nil, err
		}
	}
	return missing, tx.Commit()
}
