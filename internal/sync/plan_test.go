package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
)

func writeFileWithMtime(t *testing.T, path string, content string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func entry(rel string, size, mtime int64) moodle.FileEntry {
	return moodle.FileEntry{
		CourseID: 1, CourseDir: filepath.Base(filepath.Dir(rel)),
		RelPath: rel, Name: filepath.Base(rel),
		Size: size, TimeModified: mtime,
		URL: "https://webeep.invalid/" + rel,
	}
}

func planOne(t *testing.T, root string, m *Manifest, e moodle.FileEntry, opts PlanOpts) Action {
	t.Helper()
	actions := Plan([]moodle.FileEntry{e}, root, m, opts)
	if len(actions) != 1 {
		t.Fatalf("expected exactly 1 action, got %d: %+v", len(actions), actions)
	}
	return actions[0]
}

const remotePath = "Corso/Materiali/lezione.pdf"

func TestPlanNewFile(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	a := planOne(t, root, m, entry(remotePath, 10, 1000), PlanOpts{})
	if a.Kind != ActionNew {
		t.Fatalf("kind = %s, want new", a.Kind.Label())
	}
	if !a.Kind.Downloads() {
		t.Fatal("new files must download")
	}
}

func TestPlanUnchangedKeepsLocal(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	m.set(remotePath, Entry{Size: 10, TimeModified: 1000})
	writeFileWithMtime(t, filepath.Join(root, remotePath), "0123456789", time.Unix(1000, 0))

	a := planOne(t, root, m, entry(remotePath, 10, 1000), PlanOpts{})
	if a.Kind != ActionUnchanged {
		t.Fatalf("kind = %s, want unchanged", a.Kind.Label())
	}
}

// The annotated-PDF guarantee: a tracked file edited locally is *not*
// re-downloaded, because only the manifest (remote state) is compared.
func TestPlanLocalEditIsNeverReDownloaded(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	m.set(remotePath, Entry{Size: 10, TimeModified: 1000})
	local := filepath.Join(root, remotePath)
	edited := "annotated copy, longer than the remote file"
	writeFileWithMtime(t, local, edited, time.Now())

	a := planOne(t, root, m, entry(remotePath, 10, 1000), PlanOpts{})
	if a.Kind != ActionUnchanged {
		t.Fatalf("kind = %s, want unchanged (local edits must be safe)", a.Kind.Label())
	}
	b, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != edited {
		t.Fatalf("local file was modified: %q", b)
	}
}

func TestPlanRemoteChangeUpdatesTrackedFile(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	m.set(remotePath, Entry{Size: 10, TimeModified: 1000})
	writeFileWithMtime(t, filepath.Join(root, remotePath), "0123456789", time.Unix(1000, 0))

	a := planOne(t, root, m, entry(remotePath, 11, 2000), PlanOpts{})
	if a.Kind != ActionUpdate {
		t.Fatalf("kind = %s, want update", a.Kind.Label())
	}

	// --no-overwrite must keep the local copy instead
	a = planOne(t, root, m, entry(remotePath, 11, 2000), PlanOpts{NoOverwrite: true})
	if a.Kind != ActionNoOverwrite {
		t.Fatalf("kind = %s, want kept (--no-overwrite)", a.Kind.Label())
	}
}

func TestPlanAdoptsMatchingUntrackedFile(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	writeFileWithMtime(t, filepath.Join(root, remotePath), "0123456789", time.Unix(1000, 0))

	a := planOne(t, root, m, entry(remotePath, 10, 1000), PlanOpts{})
	if a.Kind != ActionAdopt {
		t.Fatalf("kind = %s, want adopt", a.Kind.Label())
	}
	rec, ok := m.get(remotePath)
	if !ok || rec.Protected || rec.Size != 10 || rec.TimeModified != 1000 {
		t.Fatalf("manifest not updated correctly: %+v ok=%v", rec, ok)
	}
}

func TestPlanProtectsForeignLocalFile(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	local := filepath.Join(root, remotePath)
	writeFileWithMtime(t, local, "my own coursework.pdf contents", time.Now())

	a := planOne(t, root, m, entry(remotePath, 10, 1000), PlanOpts{})
	if a.Kind != ActionProtected {
		t.Fatalf("kind = %s, want protected", a.Kind.Label())
	}
	rec, ok := m.get(remotePath)
	if !ok || !rec.Protected {
		t.Fatalf("file not recorded as protected: %+v ok=%v", rec, ok)
	}

	// later, the remote copy changes: the local file is still not touched
	a = planOne(t, root, m, entry(remotePath, 10, 2000), PlanOpts{})
	if a.Kind != ActionProtectedChanged {
		t.Fatalf("kind = %s, want protected (remote changed)", a.Kind.Label())
	}
	if a.Kind.Downloads() {
		t.Fatal("protected files must not download")
	}

	// --clobber allows touching protected files, but there is nothing to do
	// while the remote copy has not changed
	a = planOne(t, root, m, entry(remotePath, 10, 1000), PlanOpts{Clobber: true})
	if a.Kind != ActionUnchanged {
		t.Fatalf("kind = %s, want unchanged", a.Kind.Label())
	}
}

func TestPlanReDownloadsRemovedProtectedFile(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	m.set(remotePath, Entry{Size: 10, TimeModified: 1000, Protected: true})
	// the user deleted the local file: fetching it from WeBeep is fine now
	a := planOne(t, root, m, entry(remotePath, 10, 1000), PlanOpts{})
	if a.Kind != ActionNew {
		t.Fatalf("kind = %s, want new", a.Kind.Label())
	}
}

func TestPlanLocalDirectoryIsSkipped(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	if err := os.MkdirAll(filepath.Join(root, remotePath), 0o755); err != nil {
		t.Fatal(err)
	}
	a := planOne(t, root, m, entry(remotePath, 10, 1000), PlanOpts{})
	if a.Kind != ActionLocalIsDir {
		t.Fatalf("kind = %s, want skipped directory", a.Kind.Label())
	}
	if a.Kind.Downloads() {
		t.Fatal("a blocking directory must not be overwritten")
	}
}

func TestManifestRoundTrip(t *testing.T) {
	root := t.TempDir()
	m := newManifest()
	m.set(remotePath, Entry{Size: 12, TimeModified: 99, Protected: true})
	if err := m.Save(root); err != nil {
		t.Fatal(err)
	}
	got := LoadManifest(root)
	rec, ok := got.get(remotePath)
	if !ok || rec.Size != 12 || rec.TimeModified != 99 || !rec.Protected {
		t.Fatalf("round trip lost data: %+v ok=%v", rec, ok)
	}
}

func TestLoadManifestCorruptIsTreatedAsEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ManifestName), []byte("not json {{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := LoadManifest(root)
	if len(m.Files) != 0 {
		t.Fatalf("corrupt manifest should load as empty, got %+v", m.Files)
	}
}
