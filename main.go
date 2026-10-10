package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

type ImageData struct {
	Path               string
	RelativePath       string
	AverageIlluminance float64
	MedianIlluminance  float64
	Thumbnail          []byte // JPEG
	Width              int
	Height             int
	ModTime            int64 // Unix nanoseconds of the file when it was analyzed
	Size               int64
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

type config struct {
	dir       string
	all       bool
	bookPages int
	bookMode  string
	replace   bool
	cleanup   bool
	dbPath    string
	jobs      int
}

var errNoMode = errors.New("no directory or mode given")

const usageText = `Usage: illuminasort [flags] <directory>
       illuminasort --all
       illuminasort --book <pages> [--book-mode landscape|portrait|smart]
       illuminasort --cleanup

Flags must come before the directory.

`

func parseConfig(args []string, stderr io.Writer) (config, error) {
	var cfg config
	flags := flag.NewFlagSet("illuminasort", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprint(stderr, usageText)
		flags.PrintDefaults()
	}

	defaultDB := ""
	if home, err := os.UserHomeDir(); err == nil {
		defaultDB = filepath.Join(home, ".illuminasort.db")
	}

	flags.BoolVar(&cfg.all, "all", false, "Generate report from all images in database")
	flags.IntVar(&cfg.bookPages, "book", 0, "Select N images for a book from DB (rounded up to multiple of 4), lightest first, darkest last")
	flags.StringVar(&cfg.bookMode, "book-mode", "", "Book selection mode: landscape, portrait, or smart (requires --book)")
	flags.BoolVar(&cfg.replace, "replace", false, "Recalculate and replace existing cached values in database (directory scans only)")
	flags.BoolVar(&cfg.cleanup, "cleanup", false, "Remove database entries for images that no longer exist on disk")
	flags.StringVar(&cfg.dbPath, "db", defaultDB, "Path to the SQLite database")
	flags.IntVar(&cfg.jobs, "jobs", runtime.NumCPU(), "Number of images to analyze in parallel")

	if err := flags.Parse(args); err != nil {
		return cfg, err
	}

	switch flags.NArg() {
	case 0:
	case 1:
		cfg.dir = flags.Arg(0)
	default:
		return cfg, fmt.Errorf("expected one directory, got %d arguments (flags must come before the directory)", flags.NArg())
	}

	modes := 0
	for _, set := range []bool{cfg.dir != "", cfg.all, cfg.bookPages != 0, cfg.cleanup} {
		if set {
			modes++
		}
	}
	switch {
	case modes == 0:
		return cfg, errNoMode
	case modes > 1:
		return cfg, errors.New("<directory>, --all, --book and --cleanup cannot be combined")
	case cfg.bookPages < 0:
		return cfg, errors.New("--book must be a positive number of pages")
	case cfg.bookMode != "" && cfg.bookPages == 0:
		return cfg, errors.New("--book-mode requires --book")
	case cfg.replace && cfg.dir == "":
		return cfg, errors.New("--replace only applies to directory scans")
	case cfg.jobs < 1:
		return cfg, errors.New("--jobs must be at least 1")
	case cfg.dbPath == "":
		return cfg, errors.New("could not determine home directory; pass --db")
	}
	switch cfg.bookMode {
	case "", "landscape", "portrait", "smart":
	default:
		return cfg, fmt.Errorf("unknown --book-mode %q (want landscape, portrait or smart)", cfg.bookMode)
	}
	return cfg, nil
}

func main() {
	cfg, err := parseConfig(os.Args[1:], os.Stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		os.Exit(0)
	case errors.Is(err, errNoMode):
		fmt.Fprint(os.Stderr, usageText)
		os.Exit(2)
	case err != nil:
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Warning: "+format+"\n", args...)
}

func run(cfg config) error {
	db, err := openDB(cfg.dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	switch {
	case cfg.cleanup:
		removed, err := cleanupDB(db, fileExists)
		if err != nil {
			return fmt.Errorf("cleanup: %w", err)
		}
		for _, path := range removed {
			fmt.Printf("Removed: %s\n", path)
		}
		fmt.Printf("Cleanup complete: removed %d missing image(s) from database\n", len(removed))
		return nil

	case cfg.all:
		images, err := getAllImages(db)
		if err != nil {
			return fmt.Errorf("reading database: %w", err)
		}
		if len(images) == 0 {
			fmt.Println("No images found in database")
			return nil
		}
		sortByLuminance(images)
		outputPath := "illuminance_report_all.html"
		if err := writeReport(outputPath, allImagesReport(images)); err != nil {
			return fmt.Errorf("generating HTML: %w", err)
		}
		fmt.Printf("\nReport generated with %d images: %s\n", len(images), outputPath)
		return nil

	case cfg.bookPages > 0:
		images, err := getAllImages(db)
		if err != nil {
			return fmt.Errorf("reading database: %w", err)
		}
		if len(images) == 0 {
			fmt.Println("No images found in database")
			return nil
		}
		return runBook(images, cfg.bookPages, cfg.bookMode)

	default:
		images, err := scanDirectory(db, cfg.dir, cfg.replace, cfg.jobs)
		if err != nil {
			return fmt.Errorf("scanning %s: %w", cfg.dir, err)
		}
		if len(images) == 0 {
			fmt.Println("No images found in the directory")
			return nil
		}
		outputPath := filepath.Join(cfg.dir, "illuminance_report.html")
		if err := writeReport(outputPath, scanReport(images)); err != nil {
			return fmt.Errorf("generating HTML: %w", err)
		}
		fmt.Printf("\nReport generated: %s\n", outputPath)
		return nil
	}
}

func runBook(images []ImageData, requestedPages int, mode string) error {
	if mode != "" {
		if _, _, unknown := splitByOrientation(images); unknown > 0 {
			warnf("%d image(s) have unknown dimensions and were skipped; rescan their directories to include them", unknown)
		}
	}

	outputPath := "illuminance_book.html"
	target := roundUpToMultipleOf4(requestedPages)
	var r report
	var filled int

	switch mode {
	case "smart":
		book, err := selectSmartBookImages(images, requestedPages)
		if err != nil {
			return err
		}
		r = smartBookReport(book, requestedPages)
		filled = book.FilledPages()

	case "landscape", "portrait", "":
		var selected []ImageData
		var err error
		title := "Book Image Selection"
		switch mode {
		case "landscape":
			selected, err = selectLandscapeBookImages(images, requestedPages)
			title += " (Landscape)"
		case "portrait":
			selected, err = selectPortraitBookImages(images, requestedPages)
			title += " (Portrait)"
		default:
			selected = selectBookImages(images, requestedPages)
		}
		if err != nil {
			return err
		}
		r = bookReport(title, selected, requestedPages)
		filled = len(selected)
	}

	if err := writeReport(outputPath, r); err != nil {
		return fmt.Errorf("generating HTML: %w", err)
	}
	if filled < target {
		warnf("not enough images: only %d of %d pages could be filled", filled, target)
	}
	label := ""
	if mode != "" {
		label = " (" + mode + ")"
	}
	fmt.Printf("Book report%s generated with %d pages (requested %d): %s\n", label, target, requestedPages, outputPath)
	return nil
}

// fileExists reports false only when the file is definitely gone, so that
// other errors such as permission problems never delete entries.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}
