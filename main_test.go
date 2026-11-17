package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbletea"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/config"
	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
	syncpkg "github.com/Luigi-Pizzolito/webeep-sync-cli/internal/sync"
)

func strptr(s string) *string { return &s }

func TestSanitizeCoursePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Y1S1/Biochip", "Y1S1/Biochip"},
		{" Y1S1 / Biochip ", "Y1S1/Biochip"},
		{"Biochip", "Biochip"},
		{`Y1S1/RF: Circuit`, "Y1S1/RF_ Circuit"}, // ':' is illegal on Windows
		{"Y1S1//Biochip", "Y1S1/Biochip"},
		{"Y1S1/Biochip/", "Y1S1/Biochip"},
		{"", ""},
		{"/home/luigi/School", ""}, // absolute paths are refused
		{"../Outside", "Outside"},  // ".." collapses instead of escaping
		{"Y1S1/../Outside", "Y1S1/Outside"},
	}
	for _, c := range cases {
		if got := sanitizeCoursePath(c.in); got != c.want {
			t.Errorf("sanitizeCoursePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeCoursePathNeverEscapes is the guarantee the mapping feature rests
// on: whatever the user (or a config file) says, a course folder stays inside
// the download root.
func TestSanitizeCoursePathNeverEscapes(t *testing.T) {
	hostile := []string{
		"/etc/passwd", "//etc", "..", "../..", "Y1S1/../../Outside", "../",
		`..\evil`, `..\..\evil`, "./x", "Y1S1/..", "\\", " ", "",
	}
	for _, in := range hostile {
		got := sanitizeCoursePath(in)
		if strings.HasPrefix(got, "/") {
			t.Errorf("sanitizeCoursePath(%q) = %q: absolute", in, got)
		}
		for _, seg := range strings.Split(got, "/") {
			if seg == ".." {
				t.Errorf("sanitizeCoursePath(%q) = %q: keeps a '..' segment", in, got)
			}
		}
	}
}

func TestCoursePathPrefersPathOverDeprecatedName(t *testing.T) {
	if got := coursePath(config.CoursePrefs{Name: strptr("Old"), Path: strptr("Y1S1/New")}); got != "Y1S1/New" {
		t.Errorf("Path should win, got %q", got)
	}
	if got := coursePath(config.CoursePrefs{Name: strptr("Old")}); got != "Old" {
		t.Errorf("Name should still work, got %q", got)
	}
	if got := coursePath(config.CoursePrefs{Path: strptr("  ")}); got != "" {
		t.Errorf("blank mapping should mean 'no mapping', got %q", got)
	}
}

func TestResolveCoursesAppliesMappings(t *testing.T) {
	conf := &config.Config{}
	conf.SetCoursePrefs(1, config.CoursePrefs{Path: strptr("Y1S1/Biochip")})
	conf.SetCoursePrefs(2, config.CoursePrefs{Sync: func() *bool { f := false; return &f }()})

	courses := []moodle.CourseSummary{
		{ID: 1, FullName: "18804 - BIOCHIP [2025-26] (a.y. 2025/2026)", DisplayName: "BIOCHIP [2025-26]"},
		{ID: 2, FullName: "22190 - SOUND ANALYSIS [2025-26] (a.y. 2025/2026)", DisplayName: "SOUND ANALYSIS [2025-26]"},
		{ID: 3, FullName: "24737 - ANALOG CIRCUIT DESIGN [2026-27] (a.y. 2026/2027)", DisplayName: "ANALOG CIRCUIT DESIGN [2026-27]"},
	}
	rows, warnings := resolveCourses(courses, conf)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	byID := map[int64]resolvedCourse{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if byID[1].Folder != "Y1S1/Biochip" {
		t.Errorf("mapped folder = %q, want Y1S1/Biochip", byID[1].Folder)
	}
	if byID[2].Folder != "SOUND ANALYSIS [2025-26]" {
		t.Errorf("unmapped folder = %q, want the shortened WeBeep name", byID[2].Folder)
	}
	if !byID[1].Sync || byID[2].Sync {
		t.Errorf("sync flags = %v/%v, want true/false", byID[1].Sync, byID[2].Sync)
	}
}

func TestResolveCoursesRefusesSharedFolders(t *testing.T) {
	conf := &config.Config{}
	conf.SetCoursePrefs(1, config.CoursePrefs{Path: strptr("Y1S1/RF Circuit Design")})
	conf.SetCoursePrefs(2, config.CoursePrefs{Path: strptr("Y1S1/RF Circuit Design")})

	courses := []moodle.CourseSummary{
		{ID: 1, FullName: "RF CIRCUIT DESIGN [2024-25]", DisplayName: "RF CIRCUIT DESIGN [2024-25]"},
		{ID: 2, FullName: "RF CIRCUIT DESIGN [2025-26]", DisplayName: "RF CIRCUIT DESIGN [2025-26]"},
	}
	rows, warnings := resolveCourses(courses, conf)
	if len(warnings) == 0 {
		t.Fatal("sharing a folder must warn: two courses would overwrite each other")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.Folder] {
			t.Fatalf("folder %q used twice: %v", r.Folder, rows)
		}
		seen[r.Folder] = true
	}
}

func TestSelectCoursesHonoursFlagsAndSwitches(t *testing.T) {
	rows := []resolvedCourse{
		{CourseSummary: moodle.CourseSummary{ID: 1, FullName: "BIOCHIP", DisplayName: "BIOCHIP"}, Folder: "Y1S1/Biochip", Sync: true},
		{CourseSummary: moodle.CourseSummary{ID: 2, FullName: "SOUND ANALYSIS", DisplayName: "SOUND ANALYSIS"}, Folder: "SOUND ANALYSIS", Sync: false},
	}
	chosen, skipped := selectCourses(rows, defaultSyncOptions())
	if len(chosen) != 1 || chosen[0].ID != 1 || skipped != 1 {
		t.Fatalf("default run = %v (skipped %d), want only BIOCHIP", chosen, skipped)
	}

	chosen, _ = selectCourses(rows, syncOptions{All: true})
	if len(chosen) != 2 {
		t.Fatalf("--all = %d courses, want 2", len(chosen))
	}

	// A --course pattern forces inclusion even for a disabled course, and
	// matches the mapped folder too.
	chosen, _ = selectCourses(rows, syncOptions{Courses: []string{"y1s1/bio"}})
	if len(chosen) != 1 || chosen[0].ID != 1 {
		t.Fatalf("--course filter = %v, want only BIOCHIP", chosen)
	}
	chosen, _ = selectCourses(rows, syncOptions{Courses: []string{"sound"}})
	if len(chosen) != 1 || chosen[0].ID != 2 {
		t.Fatalf("a --course pattern should override a disabled course, got %v", chosen)
	}
}

func TestLocalCourseDirsSuggestsSemesterFolders(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{
		"Y1S1/Biochip", "Y1S1/RF Circuit Design", "Y1S2/Analog Circuit Design",
		".stfolder", ".stversions/old", "Y1S1/Biochip/notes",
	} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "reading.pdf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := localCourseDirs(root)
	want := []string{"Y1S1", "Y1S1/Biochip", "Y1S1/RF Circuit Design", "Y1S2", "Y1S2/Analog Circuit Design"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("suggestions = %v, want %v", got, want)
	}
}

// TestPlanKeyIgnoresSelection is what makes the UI usable: after pressing p you
// can switch courses on and off while their file counts stay on screen, but a
// changed mapping must drop the cached plan.
func TestPlanKeyIgnoresSelectionButNotMappings(t *testing.T) {
	m := &uiModel{
		conf: &config.Config{DownloadDir: "/root"},
		rows: []resolvedCourse{
			{CourseSummary: moodle.CourseSummary{ID: 1}, Folder: "Y1S1/Biochip", Sync: true},
			{CourseSummary: moodle.CourseSummary{ID: 2}, Folder: "RF CIRCUIT DESIGN", Sync: true},
		},
	}
	before := m.planKey()

	m.rows[0].Sync = false
	if m.planKey() != before {
		t.Error("selecting courses in and out must not invalidate the cached plan")
	}

	m.rows[0].Folder = "Y1S1/Biochip 2026"
	if m.planKey() == before {
		t.Error("a changed folder mapping must invalidate the cached plan")
	}

	m.conf.DownloadDir = "/other/root"
	m.rows[0].Folder = "Y1S1/Biochip"
	if got := m.planKey(); !strings.HasPrefix(got, "/other/root|") {
		t.Errorf("planKey = %q, want it to follow the new root folder", got)
	}
}

func TestSelectedPlanOnlyRunsWhatIsSelected(t *testing.T) {
	m := &uiModel{
		rows: []resolvedCourse{
			{CourseSummary: moodle.CourseSummary{ID: 1}, Sync: true},
			{CourseSummary: moodle.CourseSummary{ID: 2}, Sync: false},
		},
		planCache: []syncpkg.Action{
			{Kind: syncpkg.ActionNew, Entry: moodle.FileEntry{CourseID: 1, RelPath: "Y1S1/Biochip/a.pdf"}},
			{Kind: syncpkg.ActionNew, Entry: moodle.FileEntry{CourseID: 2, RelPath: "RF CIRCUIT DESIGN/b.pdf"}},
			{Kind: syncpkg.ActionUnchanged, Entry: moodle.FileEntry{CourseID: 1, RelPath: "Y1S1/Biochip/c.pdf"}},
		},
	}
	got := m.selectedPlan()
	if len(got) != 2 {
		t.Fatalf("selected %d actions, want 2 (course 2 is deselected)", len(got))
	}
	for _, a := range got {
		if a.Entry.CourseID != 1 {
			t.Fatalf("deselected course leaked into the run: %s", a.Entry.RelPath)
		}
	}
	if counts := m.newFileCounts(); counts[2] != 1 {
		t.Errorf("file counts must cover deselected courses too (you need them to decide): %v", counts)
	}
}

func TestShortCourseName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"095274 - RF CIRCUIT DESIGN (LEVANTINO SALVATORE) [2025-26]", "RF CIRCUIT DESIGN (LEVANTINO SALVATORE) [2025-26]"},
		{"Ingegneria Elettronica", "Ingegneria Elettronica"},
		{"12 - A - B", "A - B"},
		{"", ""},
	}
	for _, c := range cases {
		if got := shortCourseName(c.in); got != c.want {
			t.Errorf("shortCourseName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSyncScreenRepeatsNothing: the file being downloaded and the esc hint each
// appear once, not once per panel.
func TestSyncScreenRepeatsNothing(t *testing.T) {
	m := newUIModel(&config.Config{DownloadDir: "/root"}, defaultSyncOptions())
	m.width, m.height = 100, 30
	m.target = &syncTarget{user: "Test User", dir: "/root"}
	m.rows = []resolvedCourse{{CourseSummary: moodle.CourseSummary{ID: 1}, Folder: "Y1S1/Biochip", Sync: true}}
	m.mode = modeSync
	m.total, m.done = 458, 20
	m.cancel = func() {}
	m.status = "Y1S1/Biochip/Exams/Past Exams.zip (20.0MB so far)"

	view := m.View()
	if n := strings.Count(view, m.status); n != 1 {
		t.Errorf("current file appears %d times, want once\n%s", n, view)
	}
	if n := strings.Count(view, "esc "); n != 1 {
		t.Errorf("the esc hint appears %d times, want once\n%s", n, view)
	}
}

// TestEscDuringSyncStopsTheDownload: esc is the key the footer promises will
// stop the download. It must really cancel the run and stay on the sync screen
// so the summary is shown.
func TestEscDuringSyncStopsTheDownload(t *testing.T) {
	m := newUIModel(&config.Config{DownloadDir: "/root"}, defaultSyncOptions())
	stopped := false
	m.cancel = func() { stopped = true }
	m.mode = modeSync
	m.total, m.done = 505, 12
	m.events = make(chan any)
	close(m.events) // pretend the workers have already stopped

	m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})

	if !stopped {
		t.Error("esc did not stop the download")
	}
	if m.mode != modeSync {
		t.Errorf("mode = %v, want the sync screen so the summary appears", m.mode)
	}
}

// TestQuitDuringSyncReportsWhatSurvived: quitting mid-run must say what is
// already on disk, not the name of the file that was half downloaded.
func TestQuitDuringSyncReportsWhatSurvived(t *testing.T) {
	m := newUIModel(&config.Config{DownloadDir: "/root"}, defaultSyncOptions())
	m.cancel = func() {}
	m.mode = modeSync
	m.total, m.done = 505, 12
	m.status = "Y1S1/Biochip/Slides/L13 - Optical Detection.pdf (2.4MB so far)"
	m.events = make(chan any)
	close(m.events)

	m.quit()

	if !strings.Contains(m.status, "12/505") {
		t.Errorf("status = %q, want it to report 12 of 505 files", m.status)
	}
}

// TestExpandTildeHandlesBothSeparators: ~ means nothing to Go, so the tool
// expands it itself. A path stored in config on Windows reads as ~\folder and
// has to land in the same place ~/folder would.
func TestExpandTildeHandlesBothSeparators(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "student")
	t.Setenv("HOME", home)

	cases := map[string]string{
		"~":               home,
		"~/School":        filepath.Join(home, "School"),
		`~\School`:        filepath.Join(home, "School"),
		"~/School/PoliMi": filepath.Join(home, "School", "PoliMi"),
		"/already/abs":    "/already/abs",
		"relative/path":   "relative/path",
		"weird~name":      "weird~name",
	}
	for in, want := range cases {
		if got := expandTilde(in); got != want {
			t.Errorf("expandTilde(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDefaultDownloadDirIsAbsolute: the fallback folder is printed as the
// default answer, and answers are refused unless absolute. A "~..." string is
// not absolute anywhere, so the default has to be a resolved path.
func TestDefaultDownloadDirIsAbsolute(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "student")
	t.Setenv("HOME", home)

	got := defaultDownloadDir()
	if !filepath.IsAbs(got) {
		t.Errorf("defaultDownloadDir() = %q, want an absolute path", got)
	}
	if want := filepath.Join(home, "WeBeep"); got != want {
		t.Errorf("defaultDownloadDir() = %q, want %q", got, want)
	}
}

// TestPromptAcceptsItsOwnDefault: pressing Enter at the folder question must be
// usable. On Windows the old default was ~\WeBeep, which failed the absolute
// path check the prompt applies to its own suggestion.
func TestPromptAcceptsItsOwnDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	in, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = in
	t.Cleanup(func() {
		in.Close()
		os.Stdin = orig
	})

	got, err := promptDownloadDir(&config.Config{})
	if err != nil {
		t.Fatalf("accepting the default failed: %v", err)
	}
	if want := filepath.Join(home, "WeBeep"); got != want {
		t.Errorf("promptDownloadDir() = %q, want the default %q", got, want)
	}
}
