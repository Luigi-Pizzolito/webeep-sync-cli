package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
)

// TestClobberProtected is the user-facing guarantee: files you edited yourself
// stay untouched, and only an explicit "yes, replace them" makes WeBeep win.
func TestClobberProtectedTurnsProtectedIntoUpdates(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Corso"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Corso", "notes.pdf"), []byte("my own notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := LoadManifest(root)
	remote := moodle.FileEntry{RelPath: "Corso/notes.pdf", URL: "https://x/1", Size: 10, TimeModified: 100}

	actions := Plan([]moodle.FileEntry{remote}, root, m, PlanOpts{})
	if got := actions[0].Kind; got != ActionProtected {
		t.Fatalf("an untracked local file should be protected, got %s", got.Label())
	}

	// WeBeep now has a newer version: still protected, but worth showing.
	remote.Size, remote.TimeModified = 11, 200
	actions = Plan([]moodle.FileEntry{remote}, root, m, PlanOpts{})
	if got := actions[0].Kind; got != ActionProtectedChanged {
		t.Fatalf("kind = %s, want %s", got.Label(), ActionProtectedChanged.Label())
	}

	if n := ClobberProtected(actions, m); n != 1 {
		t.Fatalf("ClobberProtected changed %d actions, want 1", n)
	}
	if actions[0].Kind != ActionUpdate {
		t.Fatalf("kind after clobber = %s, want %s", actions[0].Kind.Label(), ActionUpdate.Label())
	}
	if rec, ok := m.get("Corso/notes.pdf"); !ok || rec.Protected {
		t.Fatalf("manifest should record the file as WeBeep-owned now: %+v (ok=%v)", rec, ok)
	}
}

func TestClobberProtectedLeavesOtherActionsAlone(t *testing.T) {
	root := t.TempDir()
	m := LoadManifest(root)
	m.set("Corso/own.txt", Entry{Size: 1, TimeModified: 5, Protected: true})

	actions := Plan([]moodle.FileEntry{
		{RelPath: "Corso/new.pdf", URL: "https://x/1", Size: 3, TimeModified: 9},
		{RelPath: "Corso/own.txt", URL: "https://x/2", Size: 1, TimeModified: 5},
	}, root, m, PlanOpts{})
	if n := ClobberProtected(actions, m); n != 0 {
		t.Fatalf("ClobberProtected changed %d actions, want 0 (nothing changed remotely)", n)
	}
	if rec, _ := m.get("Corso/own.txt"); !rec.Protected {
		t.Fatal("an unchanged protected file must stay protected")
	}
}
