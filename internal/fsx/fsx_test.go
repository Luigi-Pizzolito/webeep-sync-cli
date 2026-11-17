package fsx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Lezioni: 01 * intro?", "Lezioni_ 01 _ intro_"},
		{"Mat ./ iali", "Mat/iali"},
		{"a\\b", `a\b`},
		{"file\nname", "file name"},
		{"a\tb\r\nc", "a b c"},
		{"<>con|trived", "__con_trived"},
		{"dir / sub ", "dir/sub"},
		{"normal name.pdf", "normal name.pdf"},
		{"note..pdf", "note..pdf"}, // inner dots must survive
		{"a\x00b\x1fc", "abc"},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSplitExt(t *testing.T) {
	cases := []struct {
		name, stem, ext string
	}{
		{"a.pdf", "a", ".pdf"},
		{"a.tar.gz", "a.tar", ".gz"},
		{"noext", "noext", ""},
		{".hidden", "", ".hidden"}, // Go's Ext treats dotfiles as all-extension
		{"a b .pdf", "a b ", ".pdf"},
	}
	for _, c := range cases {
		stem, ext := SplitExt(c.name)
		if stem != c.stem || ext != c.ext {
			t.Errorf("SplitExt(%q) = %q,%q want %q,%q", c.name, stem, ext, c.stem, c.ext)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{999, "999.0B"},
		{1500, "1.5kB"},
		{2_500_000, "2.5MB"},
		{0, "0.0B"},
	}
	for _, c := range cases {
		if got := HumanBytes(c.n); got != c.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "state.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"v":1}` {
		t.Fatalf("content = %s", b)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	// overwrite must also work and leave no temporary files behind
	if err := AtomicWrite(path, []byte(`{"v":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".atomic-*"))
	if len(leftovers) != 0 {
		t.Fatalf("leftover temp files: %v", leftovers)
	}
}
