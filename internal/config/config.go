// Package config stores webeep-sync-cli's settings and the WeBeep web
// service token outside the download folder, so syncing never touches them.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/fsx"
)

// DefaultBaseURL is the only WeBeep endpoint the tool targets by default.
const DefaultBaseURL = "https://webeep.polimi.it"

// CoursePrefs are the per-course settings kept in config.json. A nil field
// means "use the default".
type CoursePrefs struct {
	// Name is a folder-name override. Deprecated: Path does the same job and
	// also allows a semester level.
	Name *string `json:"name,omitempty"`
	// Path is the folder the course syncs into, relative to the download root,
	// and may nest: "Y1S1/Biochip".
	Path *string `json:"path,omitempty"`
	Sync *bool   `json:"sync,omitempty"` // false disables syncing this course
}

// Config is config.json.
type Config struct {
	DownloadDir string                 `json:"download_dir,omitempty"`
	Workers     int                    `json:"workers,omitempty"`
	ExcludeMods []string               `json:"exclude_mods,omitempty"` // extra module types to skip
	Courses     map[string]CoursePrefs `json:"courses,omitempty"`      // keyed by course id
}

// ErrNotLoggedIn is returned when no token file exists.
var ErrNotLoggedIn = errors.New("not logged in: run `webeep-sync-cli login` first")

// Dir returns the config directory. It honours WEBEEP_SYNC_CONFIG_DIR (used
// by tests), then XDG_CONFIG_HOME, then ~/.config.
func Dir() (string, error) {
	if d := os.Getenv("WEBEEP_SYNC_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "webeep-sync-cli"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the config directory: %w", err)
	}
	return filepath.Join(home, ".config", "webeep-sync-cli"), nil
}

func filePath(name string) (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// Load reads config.json, returning an empty config when it does not exist.
func Load() (*Config, error) {
	p, err := filePath("config.json")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	return &c, nil
}

// Save writes config.json.
func (c *Config) Save() error {
	p, err := filePath("config.json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return fsx.AtomicWrite(p, b, 0o600)
}

// CoursePrefs returns the stored preferences for one course id.
func (c *Config) CoursePrefs(id int64) CoursePrefs {
	if c.Courses == nil {
		return CoursePrefs{}
	}
	return c.Courses[fmt.Sprintf("%d", id)]
}

// SetCoursePrefs stores preferences for one course id.
func (c *Config) SetCoursePrefs(id int64, p CoursePrefs) {
	if c.Courses == nil {
		c.Courses = map[string]CoursePrefs{}
	}
	c.Courses[fmt.Sprintf("%d", id)] = p
}

// Token is token.json: the web service token plus the identity it belongs to.
type Token struct {
	BaseURL    string    `json:"base_url"`
	Token      string    `json:"token"`
	User       string    `json:"user,omitempty"`
	ObtainedAt time.Time `json:"obtained_at"`
}

// LoadToken reads token.json, returning ErrNotLoggedIn when absent.
func LoadToken() (*Token, error) {
	p, err := filePath("token.json")
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	if t.Token == "" {
		return nil, ErrNotLoggedIn
	}
	if t.BaseURL == "" {
		t.BaseURL = DefaultBaseURL
	}
	return &t, nil
}

// SaveToken writes token.json with owner-only permissions: it holds a
// credential that grants access to everything the account can see on WeBeep.
func SaveToken(t *Token) error {
	p, err := filePath("token.json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	return fsx.AtomicWrite(p, b, 0o600)
}

// DeleteToken removes token.json; a missing file is not an error.
func DeleteToken() error {
	p, err := filePath("token.json")
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
