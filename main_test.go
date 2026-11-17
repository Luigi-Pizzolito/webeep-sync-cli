package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/config"
	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
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
