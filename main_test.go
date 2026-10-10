package main

import (
	"errors"
	"io"
	"testing"
)

func TestParseConfig(t *testing.T) {
	valid := [][]string{
		{"photos"},
		{"--replace", "photos"},
		{"--all"},
		{"--cleanup"},
		{"--book", "20"},
		{"--book", "20", "--book-mode", "smart"},
		{"--db", "/tmp/x.db", "--jobs", "2", "photos"},
	}
	for _, args := range valid {
		if _, err := parseConfig(args, io.Discard); err != nil {
			t.Errorf("%v: unexpected error %v", args, err)
		}
	}

	invalid := [][]string{
		{"--all", "--cleanup"},
		{"--all", "photos"},
		{"--book", "4", "--cleanup"},
		{"--book", "-4"},
		{"--book", "4", "--book-mode", "smrt"},
		{"--book-mode", "smart"},
		{"--replace", "--all"},
		{"--jobs", "0", "photos"},
		{"photos", "--replace"}, // flag after directory
		{"--nope"},
	}
	for _, args := range invalid {
		if _, err := parseConfig(args, io.Discard); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}

	if _, err := parseConfig(nil, io.Discard); !errors.Is(err, errNoMode) {
		t.Errorf("no args: got %v, want errNoMode", err)
	}
}
