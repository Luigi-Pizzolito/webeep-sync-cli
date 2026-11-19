package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
)

// leftoverEntry makes an entry whose CourseDir is the course folder, which is
// what moodle.CollectFiles produces and what LeftoverFolders walks.
func leftoverEntry(course, rel string) moodle.FileEntry {
	return moodle.FileEntry{
		CourseID: 1, CourseDir: course, RelPath: rel,
		Name: filepath.Base(rel), Size: 10, TimeModified: 1000,
		URL: "https://webeep.invalid/" + rel,
	}
}

func writeLocal(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remoteActions(course string, rels ...string) []Action {
	var out []Action
	for _, rel := range rels {
		out = append(out, Action{Kind: ActionUnchanged, Entry: leftoverEntry(course, rel)})
	}
	return out
}

// WeBeep renamed the folder, so the material sits in two places and the older
// copy is the one people read as "the sync stopped halfway".
func TestLeftoverFoldersNamesARenamedWeBeepFolder(t *testing.T) {
	root := t.TempDir()
	actions := remoteActions("Corso",
		"Corso/Introduzione/LESSON slides/a.pdf",
		"Corso/Introduzione/LESSON slides/b.pdf",
	)
	for _, rel := range []string{
		"Corso/Introduzione/LESSON slides/a.pdf",
		"Corso/Introduzione/LESSON slides/b.pdf",
		"Corso/Lesson Slides/a.pdf",
		"Corso/Lesson Slides/b.pdf",
		"Corso/Lesson Slides/es 10- fda.pdf",
	} {
		writeLocal(t, root, rel)
	}

	got := LeftoverFolders(actions, root)
	if len(got) != 1 {
		t.Fatalf("leftovers = %+v, want one folder", got)
	}
	l := got[0]
	if l.RelPath != "Corso/Lesson Slides" {
		t.Fatalf("RelPath = %q", l.RelPath)
	}
	if l.Files != 3 {
		t.Fatalf("Files = %d, want 3", l.Files)
	}
	if l.LikelyIn != "Introduzione/LESSON slides" || l.Matches != 2 {
		t.Fatalf("LikelyIn = %q with %d matches, want the renamed folder with 2", l.LikelyIn, l.Matches)
	}
}

func TestLeftoverFoldersReportsOneEntryForANestedOldLayout(t *testing.T) {
	root := t.TempDir()
	actions := remoteActions("Corso", "Corso/Now/one.pdf")
	for _, rel := range []string{
		"Corso/Then/Old One/one.pdf",
		"Corso/Then/Old Two/two.pdf",
		"Corso/Then/three.pdf",
	} {
		writeLocal(t, root, rel)
	}

	got := LeftoverFolders(actions, root)
	if len(got) != 1 {
		t.Fatalf("leftovers = %+v, want only the top folder once", got)
	}
	if got[0].RelPath != "Corso/Then" || got[0].Files != 3 {
		t.Fatalf("got %+v, want Corso/Then with 3 files", got[0])
	}
}

// Your own files inside a folder WeBeep uses are not a leftover: only whole
// folders that WeBeep does not have are worth mentioning.
func TestLeftoverFoldersIgnoresExtraFilesInWeBeepFolders(t *testing.T) {
	root := t.TempDir()
	actions := remoteActions("Corso", "Corso/Sect/remote.pdf")
	writeLocal(t, root, "Corso/Sect/remote.pdf")
	writeLocal(t, root, "Corso/Sect/my own summary.md")

	if got := LeftoverFolders(actions, root); len(got) != 0 {
		t.Fatalf("leftovers = %+v, want none", got)
	}
}

func TestLeftoverFoldersAddsNoGuessForYourOwnFolder(t *testing.T) {
	root := t.TempDir()
	actions := remoteActions("Corso", "Corso/Sect/remote.pdf")
	writeLocal(t, root, "Corso/Sect/remote.pdf")
	writeLocal(t, root, "Corso/Sect/My Summaries/summary.pdf")

	got := LeftoverFolders(actions, root)
	if len(got) != 1 || got[0].RelPath != "Corso/Sect/My Summaries" {
		t.Fatalf("leftovers = %+v", got)
	}
	if got[0].LikelyIn != "" || got[0].Matches != 0 {
		t.Fatalf("got %+v, want no guess attached", got[0])
	}
}

// Half the names matching is the line: below it the tool reports the folder
// without claiming it is WeBeep's material.
func TestLeftoverFoldersOnlyGuessesWhenMostNamesMatch(t *testing.T) {
	root := t.TempDir()
	actions := remoteActions("Corso", "Corso/Now/one.pdf")
	for _, rel := range []string{"Corso/Then/one.pdf", "Corso/Then/two.pdf", "Corso/Then/three.pdf", "Corso/Then/four.pdf"} {
		writeLocal(t, root, rel)
	}

	got := LeftoverFolders(actions, root)
	if len(got) != 1 {
		t.Fatalf("leftovers = %+v", got)
	}
	if got[0].Files != 4 || got[0].LikelyIn != "" {
		t.Fatalf("got %+v, want 4 files and no guess", got[0])
	}
}

func TestLeftoverFoldersSkipsToolAndHiddenFolders(t *testing.T) {
	root := t.TempDir()
	actions := remoteActions("Corso", "Corso/Now/one.pdf")
	writeLocal(t, root, "Corso/Now/one.pdf")
	writeLocal(t, root, filepath.Join("Corso", ".stfolder", "marker"))
	writeLocal(t, root, "Corso/.webeep-part-stray")
	writeLocal(t, root, "Corso/Old/.webeep-part-halfwritten")
	writeLocal(t, root, "Corso/.git/config")
	if err := os.WriteFile(filepath.Join(root, "Corso", ManifestName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := LeftoverFolders(actions, root); len(got) != 0 {
		t.Fatalf("leftovers = %+v, want none", got)
	}
}

func TestLeftoverFoldersWithNoCoursesOrMissingFolders(t *testing.T) {
	root := t.TempDir()
	if got := LeftoverFolders(nil, root); len(got) != 0 {
		t.Fatalf("leftovers = %+v, want none", got)
	}
	// A course folder that does not exist locally has nothing to report.
	actions := remoteActions("Corso", "Corso/Now/one.pdf")
	if got := LeftoverFolders(actions, root); len(got) != 0 {
		t.Fatalf("leftovers = %+v, want none", got)
	}
}
