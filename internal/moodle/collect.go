package moodle

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/fsx"
)

// DefaultExcludeMods are the module types that never contain downloadable
// material (same set the original WeBeep Sync app skips).
var DefaultExcludeMods = []string{
	"page", "forum", "url", "wooclap", "choice", "feedback", "label", "lesson",
}

// FileEntry is one downloadable file already mapped onto the desired local
// layout. RelPath is relative to the download root, slash separated, and
// includes the final file name.
type FileEntry struct {
	CourseID     int64
	CourseDir    string
	RelPath      string
	Name         string
	Size         int64
	TimeModified int64
	URL          string
}

var defaultCourseDirRe = regexp.MustCompile(`\d+ - (.+) \(.+\)`)

// DefaultCourseDir shortens "1 - Analisi I (a.y. 2024/2025)" to "Analisi I",
// matching the naming used by the WeBeep Sync app. If nothing matches the
// fullname is used as-is.
func DefaultCourseDir(fullName string) string {
	if m := defaultCourseDirRe.FindStringSubmatch(fullName); m != nil {
		return m[1]
	}
	return fullName
}

// CollectFiles walks a course content tree and produces the local layout:
//
//	<course>/<section (unless it is a "Materiali"-like group)>/<module>/<moodle filepath>
//
// Single-file "resource" modules take the module name as file name (like the
// WeBeep web UI displays them). Module types in excludeMods are skipped, and
// duplicate (path, name) pairs get a " (2)"-style suffix instead of
// overwriting each other.
func CollectFiles(courseID int64, courseDir string, sections []Section, excludeMods []string) []FileEntry {
	excluded := make(map[string]struct{}, len(excludeMods))
	for _, m := range excludeMods {
		excluded[m] = struct{}{}
	}
	used := make(map[string]struct{})
	var files []FileEntry

	for _, sec := range sections {
		for _, mod := range sec.Modules {
			if _, skip := excluded[mod.ModName]; skip {
				continue
			}
			if len(mod.Contents) == 0 {
				continue
			}
			singleResource := mod.ModName == "resource" && len(mod.Contents) == 1

			dirParts := []string{courseDir}
			// "Materiali" and similar groups become the course root itself.
			if !strings.Contains(strings.ToLower(sec.Name), "material") {
				dirParts = append(dirParts, fsx.Sanitize(sec.Name))
			}
			if !singleResource {
				dirParts = append(dirParts, fsx.Sanitize(mod.Name))
			}

			for _, f := range mod.Contents {
				if f.Type != "file" {
					continue
				}
				name := f.Filename
				if singleResource {
					name = mod.Name + path.Ext(f.Filename)
				}
				// No slashes inside a file name: they would invent folders.
				name = strings.ReplaceAll(name, "/", "_")
				name = strings.ReplaceAll(name, "\\", "_")
				name = fsx.Sanitize(name)
				if name == "" {
					name = "file"
				}

				rel := path.Join(append(dirParts, pathParts(f.Filepath)...)...)
				if rel == "." || rel == "" || hasDotDotSegment(rel) {
					continue // refuse paths that escape the course folder
				}

				candidate := name
				if _, taken := used[rel+"/"+candidate]; taken {
					stem, ext := fsx.SplitExt(name)
					for n := 2; ; n++ {
						candidate = fmt.Sprintf("%s (%d)%s", stem, n, ext)
						if _, taken := used[rel+"/"+candidate]; !taken {
							break
						}
					}
				}
				used[rel+"/"+candidate] = struct{}{}

				files = append(files, FileEntry{
					CourseID:     courseID,
					CourseDir:    courseDir,
					RelPath:      rel + "/" + candidate,
					Name:         candidate,
					Size:         f.Filesize,
					TimeModified: f.TimeModified,
					URL:          f.FileURL,
				})
			}
		}
	}
	return files
}

// pathParts splits a Moodle filepath ("/sub dir/x") into clean, sanitised
// components, dropping empty, "." and ".." parts.
func pathParts(fp string) []string {
	fp = strings.ReplaceAll(fp, "\\", "/")
	raw := strings.Split(fp, "/")
	parts := make([]string, 0, len(raw))
	for _, p := range raw {
		p = strings.TrimSpace(p)
		if p == "" || p == "." || p == ".." {
			continue
		}
		p = fsx.Sanitize(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// hasDotDotSegment reports whether any path segment is exactly "..".
func hasDotDotSegment(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
