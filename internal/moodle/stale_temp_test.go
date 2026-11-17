package moodle

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanStaleTempsOnlyTouchesOurOldPartials: the sweep may never delete a
// real file, and it may never delete a partial download another process is
// still writing.
func TestCleanStaleTempsOnlyTouchesOurOldPartials(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Y1S1", "Biochip", "Slides")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".webeep-part-111")
	fresh := filepath.Join(dir, ".webeep-part-222")
	mine := filepath.Join(dir, "My own notes.pdf")
	for _, p := range []string{stale, fresh, mine} {
		if err := os.WriteFile(p, []byte("half a file"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	longAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, longAgo, longAgo); err != nil {
		t.Fatal(err)
	}

	removed, err := CleanStaleTemps(root)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed %d file(s), want 1", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("the abandoned partial is still there: %v", err)
	}
	for _, p := range []string{fresh, mine} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s should have survived: %v", filepath.Base(p), err)
		}
	}
}

// TestCleanStaleTempsMissingRoot: a root that does not exist yet is not an error.
func TestCleanStaleTempsMissingRoot(t *testing.T) {
	removed, err := CleanStaleTemps(filepath.Join(t.TempDir(), "not-created"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if removed != 0 {
		t.Errorf("removed %d, want 0", removed)
	}
}
