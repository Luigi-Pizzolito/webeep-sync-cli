package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRunCleansPartialsFromAKilledRun: a run that was killed while writing a
// file leaves a hidden .webeep-part-* behind in your folders (and Syncthing
// happily ships it around). The next run removes it and reports it.
func TestRunCleansPartialsFromAKilledRun(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "Y1S1", "Biochip", "Slides")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	left := filepath.Join(dir, ".webeep-part-42")
	if err := os.WriteFile(left, []byte("half a pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(left, old, old); err != nil {
		t.Fatal(err)
	}

	stats, err := Run(context.Background(), nil, nil, root, LoadManifest(root), RunOpts{}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.StaleTemps != 1 {
		t.Errorf("StaleTemps = %d, want 1", stats.StaleTemps)
	}
	if _, err := os.Stat(left); !os.IsNotExist(err) {
		t.Errorf("the leftover partial is still in your folders: %v", err)
	}
}
