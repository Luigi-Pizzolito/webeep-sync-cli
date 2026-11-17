package moodle

import (
	"strings"
	"testing"
)

func fixture() []Section {
	return []Section{
		{
			ID:   1,
			Name: "Materiali didattici",
			Modules: []ModuleRef{
				{
					ID: 10, Name: "Slides", ModName: "folder",
					Contents: []FileContent{
						{Type: "file", Filename: "slides01.pdf", Filepath: "/", Filesize: 100, FileURL: "https://x/f1", TimeModified: 111},
						{Type: "file", Filename: "hand/out.pdf", Filepath: "/pdfs", Filesize: 50, FileURL: "https://x/f2", TimeModified: 112},
					},
				},
				{ID: 11, Name: "Introduction", ModName: "label", Contents: []FileContent{
					{Type: "file", Filename: "should-not-download.pdf", Filepath: "/", Filesize: 1, FileURL: "https://x/f3", TimeModified: 1},
				}},
				{ID: 12, Name: "Forum", ModName: "forum", Contents: []FileContent{
					{Type: "file", Filename: "attach.pdf", Filepath: "/", Filesize: 1, FileURL: "https://x/f4", TimeModified: 1},
				}},
			},
		},
		{
			ID:   2,
			Name: "Esercizi",
			Modules: []ModuleRef{
				{
					ID: 20, Name: "Lecture 1", ModName: "resource",
					Contents: []FileContent{
						{Type: "file", Filename: "lec1.pdf", Filepath: "/", Filesize: 30, FileURL: "https://x/f5", TimeModified: 113},
					},
				},
				{
					ID: 21, Name: "Files", ModName: "folder",
					Contents: []FileContent{
						{Type: "file", Filename: "sol.pdf", Filepath: "/", Filesize: 20, FileURL: "https://x/f6", TimeModified: 114},
						{Type: "file", Filename: "sol.pdf", Filepath: "/", Filesize: 21, FileURL: "https://x/f7", TimeModified: 115},
						{Type: "file", Filename: "solutions v2.pdf", Filepath: "/", Filesize: 22, FileURL: "https://x/f8", TimeModified: 116},
						{Type: "link", Filename: "linky", Filepath: "/", Filesize: 0, FileURL: "https://x/f9", TimeModified: 117},
					},
				},
			},
		},
	}
}

func TestCollectFilesLayout(t *testing.T) {
	files := CollectFiles(7, "Analisi 1", fixture(), DefaultExcludeMods)

	want := []struct {
		rel   string
		size  int64
		mtime int64
	}{
		{"Analisi 1/Slides/slides01.pdf", 100, 111},
		{"Analisi 1/Slides/pdfs/hand_out.pdf", 50, 112},
		{"Analisi 1/Esercizi/Lecture 1.pdf", 30, 113},
		{"Analisi 1/Esercizi/Files/sol.pdf", 20, 114},
		{"Analisi 1/Esercizi/Files/sol (2).pdf", 21, 115},
		{"Analisi 1/Esercizi/Files/solutions v2.pdf", 22, 116},
	}
	if len(files) != len(want) {
		t.Fatalf("got %d files, want %d: %+v", len(files), len(want), files)
	}
	for i, w := range want {
		f := files[i]
		if f.RelPath != w.rel {
			t.Errorf("file %d: RelPath = %q, want %q", i, f.RelPath, w.rel)
		}
		if f.Size != w.size || f.TimeModified != w.mtime {
			t.Errorf("file %d: size/mtime = %d/%d, want %d/%d", i, f.Size, f.TimeModified, w.size, w.mtime)
		}
	}
	for _, f := range files {
		if f.CourseID != 7 {
			t.Fatalf("CourseID = %d, want 7", f.CourseID)
		}
	}
}

func TestCollectFilesCustomExclusions(t *testing.T) {
	exclude := append(append([]string{}, DefaultExcludeMods...), "folder")
	files := CollectFiles(7, "C", fixture(), exclude)
	for _, f := range files {
		if f.RelPath == "C/Slides/slides01.pdf" {
			t.Fatal("folder module was not excluded")
		}
	}
}

func TestDefaultCourseDir(t *testing.T) {
	cases := []struct{ in, want string }{
		{"123456 - Analisi 1 (a.y. 2025/2026)", "Analisi 1"},
		{"Analisi supplementari", "Analisi supplementari"},
		{"9 - Lab A (a.y. 2024/2025) extra", "Lab A"},
	}
	for _, c := range cases {
		if got := DefaultCourseDir(c.in); got != c.want {
			t.Errorf("DefaultCourseDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCollectFilesRefusesTraversal(t *testing.T) {
	sections := []Section{{
		Name: "Materiali",
		Modules: []ModuleRef{{
			Name: "M", ModName: "folder",
			Contents: []FileContent{
				{Type: "file", Filename: "x.pdf", Filepath: "/../../etc", Filesize: 1, FileURL: "u", TimeModified: 1},
			},
		}},
	}}
	files := CollectFiles(1, "C", sections, nil)
	if len(files) != 1 {
		t.Fatalf("expected the traversal to be flattened into the course folder, got %+v", files)
	}
	if files[0].RelPath != "C/M/etc/x.pdf" {
		t.Fatalf("RelPath = %q", files[0].RelPath)
	}
}

// TestCollectFilesSemesterFolder covers mapped folders that nest: a course can
// sync into "Y1S1/Biochip", and its whole tree must follow that prefix.
func TestCollectFilesSemesterFolder(t *testing.T) {
	files := CollectFiles(7, "Y1S1/Analisi 1", fixture(), DefaultExcludeMods)
	for _, f := range files {
		if !strings.HasPrefix(f.RelPath, "Y1S1/Analisi 1/") {
			t.Errorf("RelPath %q does not stay under the mapped folder", f.RelPath)
		}
	}
	if len(files) == 0 {
		t.Fatal("no files collected")
	}
	if got := files[0].RelPath; got != "Y1S1/Analisi 1/Slides/slides01.pdf" {
		t.Fatalf("RelPath = %q", got)
	}
	if got := files[len(files)-1].RelPath; got != "Y1S1/Analisi 1/Esercizi/Files/solutions v2.pdf" {
		t.Fatalf("RelPath = %q", got)
	}
}
