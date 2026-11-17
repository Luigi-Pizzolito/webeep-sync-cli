package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func useTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WEBEEP_SYNC_CONFIG_DIR", dir)
	return dir
}

func TestLoadMissingConfigIsEmpty(t *testing.T) {
	useTempConfigDir(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.DownloadDir != "" || len(c.Courses) != 0 || c.Workers != 0 {
		t.Fatalf("expected empty config, got %+v", c)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	useTempConfigDir(t)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	c.DownloadDir = "/tmp/webeep"
	c.Workers = 5
	c.ExcludeMods = []string{"h5p"}
	yes := true
	name := "Analisi 1"
	c.SetCoursePrefs(4242, CoursePrefs{Sync: &yes, Name: &name})
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}

	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	p := got.CoursePrefs(4242)
	if p.Sync == nil || !*p.Sync {
		t.Fatalf("sync pref lost: %+v", p)
	}
	if p.Name == nil || *p.Name != "Analisi 1" {
		t.Fatalf("name pref lost: %+v", p)
	}
	if got.Workers != 5 || got.DownloadDir != "/tmp/webeep" || len(got.ExcludeMods) != 1 {
		t.Fatalf("config lost fields: %+v", got)
	}
	// config must not create entries for unknown courses
	if q := got.CoursePrefs(9999); q.Sync != nil || q.Name != nil {
		t.Fatalf("unexpected prefs for unknown course: %+v", q)
	}
}

func TestTokenSaveLoadDelete(t *testing.T) {
	dir := useTempConfigDir(t)

	if _, err := LoadToken(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("err = %v, want ErrNotLoggedIn", err)
	}

	tok := &Token{BaseURL: "https://webeep.polimi.it", Token: "abc123", User: "Mario", ObtainedAt: time.Now().UTC()}
	if err := SaveToken(tok); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "token.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := st.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("token.json permissions = %v, must be owner-only", perm)
	}

	got, err := LoadToken()
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "abc123" || got.User != "Mario" || got.BaseURL != "https://webeep.polimi.it" {
		t.Fatalf("token round trip wrong: %+v", got)
	}

	if err := DeleteToken(); err != nil {
		t.Fatal(err)
	}
	if err := DeleteToken(); err != nil {
		t.Fatalf("deleting a missing token must be a no-op, got %v", err)
	}
	if _, err := LoadToken(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatal("token still loads after delete")
	}
}

func TestTokenDefaultBaseURL(t *testing.T) {
	useTempConfigDir(t)
	if err := SaveToken(&Token{Token: "t"}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadToken()
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseURL != DefaultBaseURL {
		t.Fatalf("BaseURL = %q, want %q", got.BaseURL, DefaultBaseURL)
	}
}
