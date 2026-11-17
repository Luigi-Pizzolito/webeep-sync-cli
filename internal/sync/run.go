package sync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
)

// RunOpts tunes the download run.
type RunOpts struct {
	Workers int
}

// Stats summarises a run.
type Stats struct {
	New      int
	Updated  int
	NotFound int
	Failed   int
	Bytes    int64
	// StaleTemps counts partial downloads this tool left behind in an earlier
	// run that was killed mid-file. They are removed before this run starts.
	StaleTemps int
}

// Callbacks report progress while running; OnDone and OnFail are called under
// a lock, so they need no extra synchronisation of their own. OnProgress is
// called from the downloading worker with the bytes streamed so far for the
// current file.
type Callbacks struct {
	OnDone     func(a Action, written int64, completed, total int)
	OnFail     func(a Action, err error)
	OnProgress func(a Action, downloaded int64)
}

// Run downloads all pending actions with a bounded worker pool. After each
// file the manifest is updated, so an interrupted run keeps what it already
// finished. Downloads that the server no longer has (404) are skipped, not
// treated as failures.
func Run(ctx context.Context, c *moodle.Client, actions []Action, root string, m *Manifest, opts RunOpts, cb Callbacks) (Stats, error) {
	var pending []Action
	for _, a := range actions {
		if a.Kind.Downloads() {
			pending = append(pending, a)
		}
	}
	var stats Stats
	// A run that was killed outright can leave a partial download behind in
	// your folders (and into Syncthing). Pick those up before downloading.
	stats.StaleTemps, _ = moodle.CleanStaleTemps(root)
	if len(pending) == 0 {
		return stats, nil
	}

	workers := opts.Workers
	if workers < 1 {
		workers = 3
	}
	if workers > len(pending) {
		workers = len(pending)
	}

	var (
		mu        sync.Mutex
		completed int
		firstErr  error
	)

	jobs := make(chan Action)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				if ctx.Err() != nil {
					continue // interrupted: drop the rest of the queue
				}
				var onProgress func(int64)
				if cb.OnProgress != nil {
					onProgress = func(n int64) { cb.OnProgress(a, n) }
				}
				written, err := c.DownloadFile(ctx, a.Entry.URL, a.LocalPath, onProgress)
				mu.Lock()
				switch {
				case err == nil:
					if a.Entry.TimeModified > 0 {
						// rsync -a style: the local copy carries the remote timestamp.
						ts := time.Unix(a.Entry.TimeModified, 0)
						_ = os.Chtimes(a.LocalPath, ts, ts) // best effort; the manifest is authoritative
					}
					if a.Kind == ActionUpdate {
						stats.Updated++
					} else {
						stats.New++
					}
					stats.Bytes += written
					m.set(a.Entry.RelPath, Entry{
						Size:         written,
						TimeModified: a.Entry.TimeModified,
						Protected:    false,
					})
					if saveErr := m.Save(root); saveErr != nil {
						if firstErr == nil {
							firstErr = fmt.Errorf("saving manifest: %w", saveErr)
						}
					}
					completed++
					if cb.OnDone != nil {
						cb.OnDone(a, written, completed, len(pending))
					}
				case errors.Is(err, moodle.ErrNotFound):
					stats.NotFound++
					// Remember the 404 so a permanently removed file (e.g. a
					// broken "index.html") is not re-requested on every sync.
					m.set(a.Entry.RelPath, Entry{
						Size:         a.Entry.Size,
						TimeModified: a.Entry.TimeModified,
						Missing:      true,
					})
					if saveErr := m.Save(root); saveErr != nil && firstErr == nil {
						firstErr = fmt.Errorf("saving manifest: %w", saveErr)
					}
					if cb.OnFail != nil {
						cb.OnFail(a, err)
					}
				case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
					// interrupted on purpose: not a failure
				default:
					stats.Failed++
					if firstErr == nil {
						firstErr = err
					}
					if cb.OnFail != nil {
						cb.OnFail(a, err)
					}
				}
				mu.Unlock()
			}
		}()
	}

feed:
	for _, a := range pending {
		select {
		case jobs <- a:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return stats, ctxErr
	}
	return stats, firstErr
}
