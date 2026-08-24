package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A write that fails must leave nothing behind.
//
// os.WriteFile creates the destination and writes into it, so a disk that fills
// part way through left a truncated file under the final name. The row is only
// inserted after the write succeeds, so that file was referenced by nothing and
// invisible to every cleanup path the product has. Measured on a 320 KB volume:
// one failed upload left 319 KB of it consumed, and it stayed consumed.
// guards: writeAttachmentFile
func TestFailedAttachmentWriteLeavesNothingBehind(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "capture.png")

	// A destination that cannot be renamed into: a directory of the same name.
	// The bytes are written to the temporary neighbour and the rename fails,
	// which is the shape of any late failure including a full disk.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeAttachmentFile(path, []byte("some image bytes")); err == nil {
		t.Fatal("writing over a directory should fail")
	}

	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "capture.png" {
			continue // the directory that made the write fail
		}
		t.Errorf("a failed write left %q behind", entry.Name())
	}
}

// The file appears complete or not at all: the final name never refers to a
// partly written image, because the bytes land on a neighbour first.
// guards: writeAttachmentFile
func TestAttachmentWriteIsAllOrNothing(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "capture.png")
	body := []byte(strings.Repeat("PNGDATA", 500))

	if err := writeAttachmentFile(path, body); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(body) {
		t.Errorf("stored %d bytes, wrote %d", len(stored), len(body))
	}
	// And no temporary neighbour survives a success either.
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("directory holds %v, want only the stored image", names)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode=%v want 0600 — an attachment is somebody's screenshot", mode)
	}
}
