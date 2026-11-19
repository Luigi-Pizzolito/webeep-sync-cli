package sync

import (
	"io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Leftover is a folder inside a course folder that WeBeep's current layout does
// not have. It is either yours, or the copies that an older WeBeep layout (or
// another program) put there before this tool did. Nothing in it is ever
// touched, which is worth saying out loud: when WeBeep renames a section or a
// folder the same material ends up on disk twice, and the older copy looks like
// a sync that stopped halfway through.
type Leftover struct {
	RelPath  string // the folder, relative to the download root
	Files    int    // files inside it, nested folders included
	Matches  int    // how many of its file names WeBeep now has under LikelyIn
	LikelyIn string // where WeBeep keeps that material now, relative to the course folder
}

// LeftoverFolders walks the course folders the plan covers and reports the
// directories WeBeep does not have. It only reads.
func LeftoverFolders(actions []Action, root string) []Leftover {
	remoteDirs := map[string]bool{}
	remoteNames := map[string]map[string]struct{}{}
	courses := map[string]bool{}

	for _, a := range actions {
		if cd := filepath.ToSlash(a.Entry.CourseDir); cd != "" {
			courses[cd] = true
		}
		rel := filepath.ToSlash(a.Entry.RelPath)
		dir, base := path.Split(rel)
		dir = strings.Trim(dir, "/")
		if dir == "" || base == "" {
			continue
		}
		for p := dir; ; {
			remoteDirs[p] = true
			i := strings.LastIndex(p, "/")
			if i < 0 {
				break
			}
			p = p[:i]
		}
		if remoteNames[dir] == nil {
			remoteNames[dir] = map[string]struct{}{}
		}
		remoteNames[dir][strings.ToLower(base)] = struct{}{}
	}

	var out []Leftover
	for course := range courses {
		out = append(out, leftoversInCourse(course, root, remoteDirs, remoteNames)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out
}

// skippedDirs are sync and version-control folders that are nobody's course
// material.
var skippedDirs = map[string]bool{
	".stfolder": true, ".stversions": true, ".sync": true, ".snapshot": true,
	".git": true, "node_modules": true,
}

func leftoversInCourse(course, root string, remoteDirs map[string]bool, remoteNames map[string]map[string]struct{}) []Leftover {
	var found []Leftover

	filepath.WalkDir(filepath.Join(root, filepath.FromSlash(course)), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable folder: say nothing about it
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || skippedDirs[name] {
			return fs.SkipDir
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == filepath.ToSlash(course) {
			return nil // the course folder itself is WeBeep's
		}
		if remoteDirs[rel] || !strings.HasPrefix(rel, course+"/") {
			return nil
		}

		n, names := countFiles(p)
		if n > 0 {
			l := Leftover{RelPath: rel, Files: n}
			l.LikelyIn, l.Matches = bestMatch(names, remoteNames, course)
			found = append(found, l)
		}
		return fs.SkipDir // its subfolders belong to the same folder
	})
	return found
}

// countFiles returns how many files a folder holds and their lowercased names.
// Dotfiles are left out: that covers the manifest, half-written downloads, and
// whatever an editor leaves behind.
func countFiles(dir string) (int, map[string]struct{}) {
	names := map[string]struct{}{}
	count := 0
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != dir && (strings.HasPrefix(name, ".") || skippedDirs[name]) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		count++
		names[strings.ToLower(name)] = struct{}{}
		return nil
	})
	return count, names
}

// bestMatch finds the WeBeep folder that holds the same file names. It only
// claims a match on two names or more, and only when at least half of the
// folder's names appear there, so an unrelated folder of your own is reported
// without a guess attached.
func bestMatch(names map[string]struct{}, remoteNames map[string]map[string]struct{}, course string) (string, int) {
	bestDir, best := "", 0
	for dir, rn := range remoteNames {
		m := 0
		for n := range names {
			if _, ok := rn[n]; ok {
				m++
			}
		}
		if m > best || (m == best && m > 0 && dir < bestDir) {
			best, bestDir = m, dir
		}
	}
	if best < 2 || best*2 < len(names) {
		return "", 0
	}
	return strings.TrimPrefix(bestDir, course+"/"), best
}
