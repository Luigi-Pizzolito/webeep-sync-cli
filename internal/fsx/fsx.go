// Package fsx contains filesystem helpers shared across webeep-sync-cli:
// path sanitisation (following the naming rules of the original WeBeep Sync
// app), atomic writes and small formatting utilities.
package fsx

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	whitespaceRe   = regexp.MustCompile(`[\n\t\r]+`)
	invalidRe      = regexp.MustCompile(`[:*?"<>|]`)
	controlRe      = regexp.MustCompile(`[\x00-\x1f\x7f]`)
	slashPadRe     = regexp.MustCompile(` *\.? *\/ *`)
	backslashPadRe = regexp.MustCompile(` *\.? *\\ *`)
)

// Sanitize cleans a path (or a single segment) so it can be stored safely on
// any OS: invalid Windows characters are replaced, whitespace is normalised
// and padding around path separators is stripped. This mirrors the
// sanitizePath rules used by toto04/webeep-sync, plus per-segment trimming of
// leading spaces and trailing spaces/dots (illegal on Windows).
func Sanitize(s string) string {
	s = whitespaceRe.ReplaceAllString(s, " ")
	s = invalidRe.ReplaceAllString(s, "_")
	s = slashPadRe.ReplaceAllString(s, "/")
	s = backslashPadRe.ReplaceAllString(s, "\\")
	s = controlRe.ReplaceAllString(s, "")
	segments := strings.Split(s, "/")
	for i := range segments {
		segments[i] = strings.TrimRight(strings.TrimLeft(segments[i], " "), " .")
	}
	return strings.Join(segments, "/")
}

// SplitExt splits a file name into stem and extension ("a.tar.gz" -> "a.tar", ".gz").
func SplitExt(name string) (stem, ext string) {
	ext = filepath.Ext(name)
	return name[:len(name)-len(ext)], ext
}

// HumanBytes formats a size the way the WeBeep UI does (B, kB, MB, ...).
func HumanBytes(n int64) string {
	units := []string{"B", "kB", "MB", "GB", "TB"}
	f := float64(n)
	i := 0
	for f > 1000 && i < len(units)-1 {
		f /= 1000
		i++
	}
	return fmt.Sprintf("%.1f%s", f, units[i])
}

// AtomicWrite writes data to path through a temporary file in the same
// directory plus a rename, so a partially written file is never visible under
// its final name.
func AtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".atomic-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
