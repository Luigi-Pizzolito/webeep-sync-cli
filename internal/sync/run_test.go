package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
)

// testServer serves a small fake WeBeep file tree.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/slides.pdf", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "the slides content")
	})
	mux.HandleFunc("/flaky.pdf", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"busy"}`)
	})
	mux.HandleFunc("/gone.pdf", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/slow.pdf", func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		for i := 0; i < 200; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
				if _, err := fmt.Fprint(w, "0123456789"); err != nil {
					return
				}
				if err := rc.Flush(); err != nil {
					return
				}
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func testRunClient(t *testing.T, srv *httptest.Server) *moodle.Client {
	c := moodle.NewClient(srv.URL, "tok0123456789tok0123456789tok012")
	c.RetryDelay = 10 * time.Millisecond
	return c
}

func runEntry(rel, urlPath string, size, mtime int64) moodle.FileEntry {
	return moodle.FileEntry{
		CourseID: 1, CourseDir: "Corso", RelPath: rel, Name: filepath.Base(rel),
		Size: size, TimeModified: mtime, URL: urlPath,
	}
}

func TestRunDownloadsAndRecordsManifest(t *testing.T) {
	srv := testServer(t)
	client := testRunClient(t, srv)
	root := t.TempDir()

	entries := []moodle.FileEntry{
		runEntry("Corso/Materiali/slides.pdf", srv.URL+"/slides.pdf?token=irrelevant", 18, 1_700_000_000),
		runEntry("Corso/Materiali/gone.pdf", srv.URL+"/gone.pdf", 10, 1_700_000_001),
	}
	m := newManifest()
	actions := Plan(entries, root, m, PlanOpts{})

	var done []string
	stats, err := Run(context.Background(), client, actions, root, m, RunOpts{Workers: 2}, Callbacks{
		OnDone: func(a Action, written int64, completed, total int) { done = append(done, a.Entry.RelPath) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.New != 1 || stats.NotFound != 1 || stats.Failed != 0 {
		t.Fatalf("stats = %+v, want 1 new 1 not-found", stats)
	}
	if stats.Bytes != 18 {
		t.Fatalf("bytes = %d, want 18", stats.Bytes)
	}

	local := filepath.Join(root, "Corso/Materiali/slides.pdf")
	b, err := os.ReadFile(local)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "the slides content" {
		t.Fatalf("content = %q", b)
	}
	st, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(time.Unix(1_700_000_000, 0)) {
		t.Fatalf("mtime = %v, want the remote timemodified (rsync -a style timestamps)", st.ModTime())
	}

	rec, ok := m.get("Corso/Materiali/slides.pdf")
	if !ok || rec.Size != 18 || rec.TimeModified != 1_700_000_000 || rec.Protected {
		t.Fatalf("manifest entry wrong: %+v ok=%v", rec, ok)
	}
	missingRec, ok := m.get("Corso/Materiali/gone.pdf")
	if !ok || !missingRec.Missing {
		t.Fatalf("the 404 file should be remembered as missing: %+v ok=%v", missingRec, ok)
	}
	if _, err := os.Stat(filepath.Join(root, "Corso/Materiali/gone.pdf")); !os.IsNotExist(err) {
		t.Fatal("the 404 file must not exist locally")
	}
	if len(done) != 1 || done[0] != "Corso/Materiali/slides.pdf" {
		t.Fatalf("progress callbacks = %v", done)
	}

	// a second, identical run must plan nothing
	saved := LoadManifest(root)
	wantKinds := map[string]ActionKind{
		"Corso/Materiali/slides.pdf": ActionUnchanged,
		"Corso/Materiali/gone.pdf":   ActionSkipMissing,
	}
	again := Plan(entries, root, saved, PlanOpts{})
	for _, a := range again {
		want, expect := wantKinds[a.Entry.RelPath]
		if !expect || want != a.Kind {
			t.Fatalf("second run planned %s as %s, want %s", a.Entry.RelPath, a.Kind.Label(), want.Label())
		}
	}
}

func TestRunUpdatesTrackedFileAndKeepsManifestConsistent(t *testing.T) {
	srv := testServer(t)
	client := testRunClient(t, srv)
	root := t.TempDir()
	local := filepath.Join(root, "Corso/Materiali/slides.pdf")

	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("outdated"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newManifest()
	m.set("Corso/Materiali/slides.pdf", Entry{Size: 8, TimeModified: 1000})

	entries := []moodle.FileEntry{
		runEntry("Corso/Materiali/slides.pdf", srv.URL+"/slides.pdf", 18, 2000),
	}
	stats, err := Run(context.Background(), client, Plan(entries, root, m, PlanOpts{}), root, m, RunOpts{Workers: 1}, Callbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Updated != 1 {
		t.Fatalf("stats = %+v, want 1 updated", stats)
	}
	b, _ := os.ReadFile(local)
	if string(b) != "the slides content" {
		t.Fatalf("content after update = %q", b)
	}
}

func TestRunCountsFailuresButSkips404(t *testing.T) {
	srv := testServer(t)
	client := testRunClient(t, srv)
	root := t.TempDir()

	entries := []moodle.FileEntry{
		runEntry("Corso/a.pdf", srv.URL+"/flaky.pdf", 1, 1),
	}
	m := newManifest()
	var failErrs []string
	stats, err := Run(context.Background(), client, Plan(entries, root, m, PlanOpts{}), root, m, RunOpts{Workers: 1}, Callbacks{
		OnFail: func(a Action, err error) { failErrs = append(failErrs, err.Error()) },
	})
	if stats.Failed != 1 {
		t.Fatalf("stats = %+v, want 1 failed", stats)
	}
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v, want the persistent 503", err)
	}
	if len(failErrs) != 1 {
		t.Fatalf("OnFail calls = %v", failErrs)
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, "Corso", ".webeep-part-*"))
	if len(leftovers) != 0 {
		t.Fatalf("leftover temp files: %v", leftovers)
	}
}

func TestRunInterruptCancelsAndCleansUp(t *testing.T) {
	srv := testServer(t)
	client := testRunClient(t, srv)
	root := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelTimer := time.AfterFunc(200*time.Millisecond, cancel)
	defer cancelTimer.Stop()

	entries := []moodle.FileEntry{
		runEntry("Corso/slow.pdf", srv.URL+"/slow.pdf", 0, 1),
	}
	m := newManifest()
	stats, err := Run(ctx, client, Plan(entries, root, m, PlanOpts{}), root, m, RunOpts{Workers: 1}, Callbacks{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if stats.Failed != 0 {
		t.Fatalf("an interrupt must not count as a download failure: %+v", stats)
	}
	if _, err := os.Stat(filepath.Join(root, "Corso/slow.pdf")); !os.IsNotExist(err) {
		t.Fatal("a cancelled download must not leave the final file")
	}
	leftovers, _ := filepath.Glob(filepath.Join(root, "Corso", ".webeep-part-*"))
	if len(leftovers) != 0 {
		t.Fatalf("leftover partial files: %v", leftovers)
	}
	if _, ok := m.get("Corso/slow.pdf"); ok {
		t.Fatal("cancelled download must not be recorded")
	}
}
