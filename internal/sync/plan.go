// Package sync decides which WeBeep files need downloading and downloads them
// without ever deleting or rewriting local files that WeBeep does not own.
//
// The rules (rsync-like, but remote-aware):
//
//   - a file missing locally is downloaded;
//   - a file tracked by the manifest is re-downloaded only when the copy on
//     WeBeep changed (newer timemodified or size), no matter what the local
//     copy looks like;
//   - a local file that was never downloaded from WeBeep is protected: it is
//     never overwritten (only --clobber overrides that);
//   - local files that do not exist on WeBeep at all are never deleted.
package sync

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/fsx"
	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
)

// ManifestName is the state file written into the download root. It records
// the last known state of every file WeBeep owns, which is what makes local
// edits safe: comparisons are made against the manifest, not against local
// mtimes.
const ManifestName = ".webeep-sync-cli.json"

// Entry is the recorded remote state of one file. Protected marks a local
// file WeBeep does not own; it is skipped on every plan unless --clobber.
// Missing marks a file the server answered 404 for: it is not requested
// again until its remote state (timemodified/size) changes.
type Entry struct {
	Size         int64 `json:"size"`
	TimeModified int64 `json:"timemodified"`
	Protected    bool  `json:"protected,omitempty"`
	Missing      bool  `json:"missing,omitempty"`
}

// Manifest is the download root's state file.
type Manifest struct {
	Version int              `json:"version"`
	Files   map[string]Entry `json:"files"`
}

func newManifest() *Manifest {
	return &Manifest{Version: 1, Files: map[string]Entry{}}
}

// LoadManifest reads the manifest from root. A missing or corrupt manifest is
// treated as empty: existing local files are then adopted or protected, never
// blindly re-downloaded.
func LoadManifest(root string) *Manifest {
	b, err := os.ReadFile(filepath.Join(root, ManifestName))
	if err != nil {
		return newManifest()
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return newManifest()
	}
	if m.Files == nil {
		m.Files = map[string]Entry{}
	}
	m.Version = 1
	return &m
}

func (m *Manifest) get(key string) (Entry, bool) {
	e, ok := m.Files[key]
	return e, ok
}

func (m *Manifest) set(key string, e Entry) {
	m.Files[key] = e
}

// Save writes the manifest atomically.
func (m *Manifest) Save(root string) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fsx.AtomicWrite(filepath.Join(root, ManifestName), b, 0o644)
}

// ActionKind describes what the sync will do with one remote file.
type ActionKind int

const (
	ActionNew              ActionKind = iota // missing locally: download
	ActionUpdate                             // tracked file whose remote copy changed: re-download
	ActionAdopt                              // local file matching the remote copy: record, don't touch
	ActionUnchanged                          // tracked and matching: touch nothing
	ActionProtected                          // local-only file WeBeep does not own: don't touch
	ActionProtectedChanged                   // protected file, but the remote copy changed: still don't touch
	ActionNoOverwrite                        // remote copy changed but --no-overwrite was given
	ActionLocalIsDir                         // a local directory blocks the file's path
	ActionSkipMissing                        // the server 404s this file and its remote state has not changed
)

func (k ActionKind) Downloads() bool { return k == ActionNew || k == ActionUpdate }

func (k ActionKind) Label() string {
	switch k {
	case ActionNew:
		return "new"
	case ActionUpdate:
		return "update"
	case ActionAdopt:
		return "adopt"
	case ActionUnchanged:
		return "unchanged"
	case ActionProtected:
		return "protected"
	case ActionProtectedChanged:
		return "protected (remote changed)"
	case ActionNoOverwrite:
		return "kept (remote changed, --no-overwrite)"
	case ActionLocalIsDir:
		return "skipped (local directory in the way)"
	case ActionSkipMissing:
		return "skipped (404 on server, retried when it changes)"
	}
	return "unknown"
}

// Action is the decision for one remote file.
type Action struct {
	Kind      ActionKind
	Entry     moodle.FileEntry
	LocalPath string
}

// PlanOpts tunes the policy. Clobber makes even protected local files eligible
// for download; NoOverwrite keeps every existing local file even when the
// remote copy changed.
type PlanOpts struct {
	NoOverwrite bool
	Clobber     bool
	// AdoptTolerance is how far a local file's mtime may deviate from the
	// remote timemodified for it to count as an already-synced copy.
	AdoptTolerance time.Duration
}

// Plan classifies every remote file. As a side effect it records adopted and
// protected files in the manifest so later runs are stable.
func Plan(entries []moodle.FileEntry, root string, m *Manifest, opts PlanOpts) []Action {
	if opts.AdoptTolerance == 0 {
		opts.AdoptTolerance = 2 * time.Second
	}
	actions := make([]Action, 0, len(entries))
	for _, e := range entries {
		local := filepath.Join(root, filepath.FromSlash(e.RelPath))
		rec, known := m.get(e.RelPath)
		st, statErr := os.Lstat(local)

		switch {
		case statErr != nil && !os.IsNotExist(statErr):
			// Unreadable path (permissions, broken symlink): be conservative.
			actions = append(actions, Action{Kind: ActionProtected, Entry: e, LocalPath: local})

		case os.IsNotExist(statErr):
			if known && rec.Missing && !remoteChanged(rec, e) {
				// already known to be gone from the server: don't request it again
				actions = append(actions, Action{Kind: ActionSkipMissing, Entry: e, LocalPath: local})
			} else {
				actions = append(actions, Action{Kind: ActionNew, Entry: e, LocalPath: local})
			}

		case st.IsDir():
			actions = append(actions, Action{Kind: ActionLocalIsDir, Entry: e, LocalPath: local})

		case known && rec.Protected && !opts.Clobber:
			kind := ActionProtected
			if remoteChanged(rec, e) {
				kind = ActionProtectedChanged
			}
			actions = append(actions, Action{Kind: kind, Entry: e, LocalPath: local})

		case known && !remoteChanged(rec, e):
			actions = append(actions, Action{Kind: ActionUnchanged, Entry: e, LocalPath: local})

		case known: // tracked and the remote copy changed
			kind := ActionUpdate
			if opts.NoOverwrite {
				kind = ActionNoOverwrite
			}
			actions = append(actions, Action{Kind: kind, Entry: e, LocalPath: local})

		default: // a local file that is not tracked by the manifest
			if canAdopt(st, e, opts.AdoptTolerance) {
				m.set(e.RelPath, Entry{Size: e.Size, TimeModified: e.TimeModified})
				actions = append(actions, Action{Kind: ActionAdopt, Entry: e, LocalPath: local})
				continue
			}
			// Unknown local file with the same name: WeBeep must not touch it.
			m.set(e.RelPath, Entry{Size: e.Size, TimeModified: e.TimeModified, Protected: true})
			kind := ActionProtected
			if opts.Clobber {
				kind = ActionUpdate
			}
			actions = append(actions, Action{Kind: kind, Entry: e, LocalPath: local})
		}
	}
	return actions
}

// ClobberProtected reclassifies "protected, but WeBeep has a newer version"
// actions as downloads, which is what --clobber does, and records those files
// as WeBeep-owned in the manifest. It returns how many actions changed.
func ClobberProtected(actions []Action, m *Manifest) int {
	changed := 0
	for i := range actions {
		if actions[i].Kind != ActionProtectedChanged {
			continue
		}
		actions[i].Kind = ActionUpdate
		if rec, ok := m.get(actions[i].Entry.RelPath); ok {
			rec.Protected = false
			rec.Size = actions[i].Entry.Size
			rec.TimeModified = actions[i].Entry.TimeModified
			m.set(actions[i].Entry.RelPath, rec)
		} else {
			m.set(actions[i].Entry.RelPath, Entry{Size: actions[i].Entry.Size, TimeModified: actions[i].Entry.TimeModified})
		}
		changed++
	}
	return changed
}

func remoteChanged(rec Entry, e moodle.FileEntry) bool {
	if rec.TimeModified != e.TimeModified {
		return true
	}
	// Moodle reports size 0 for some streamed contents: rely on timemodified.
	return e.Size != 0 && rec.Size != e.Size
}

// canAdopt matches an untracked local file against the remote copy by size
// and mtime (within tolerance), so an already mirrored folder is adopted
// instead of re-downloaded on the first run.
func canAdopt(st os.FileInfo, e moodle.FileEntry, tol time.Duration) bool {
	if e.Size != 0 && st.Size() != e.Size {
		return false
	}
	diff := st.ModTime().Sub(time.Unix(e.TimeModified, 0))
	if diff < 0 {
		diff = -diff
	}
	return diff <= tol
}
