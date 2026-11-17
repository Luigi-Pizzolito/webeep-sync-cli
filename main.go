// Command webeep-sync-cli keeps the files of your WeBeep (webeep.polimi.it)
// courses in a local folder, without ever deleting or rewriting local files
// that WeBeep does not own. Based on the design of toto04/webeep-sync.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/config"
	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/fsx"
	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/moodle"
	syncpkg "github.com/Luigi-Pizzolito/webeep-sync-cli/internal/sync"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "login":
		err = cmdLogin(args)
	case "logout":
		err = cmdLogout()
	case "whoami":
		err = cmdWhoami(args)
	case "courses":
		err = cmdCourses(args)
	case "course":
		err = cmdCourse(args)
	case "sync":
		err = cmdSync(args)
	case "doctor":
		err = cmdDoctor(args)
	case "version":
		fmt.Println("webeep-sync-cli", version)
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Println(`webeep-sync-cli - keep WeBeep course files synced locally, rsync-style

Usage:
  webeep-sync-cli login    [--token T|--url U|--cookie C|--username U]   obtain a web service token
  webeep-sync-cli logout                                                forget the saved token
  webeep-sync-cli whoami                                                show the logged-in account
  webeep-sync-cli courses                                               list enrolled courses
  webeep-sync-cli course enable|disable|rename <id> [name]             configure single courses
  webeep-sync-cli sync   [flags]                                        download new/changed files
  webeep-sync-cli doctor                                                check endpoints and token
  webeep-sync-cli version`)
}

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// ctxWithInterrupt cancels on SIGINT/SIGTERM so Ctrl-C stops a sync cleanly.
func ctxWithInterrupt() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func expandTilde(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}

func tokenClient() (*moodle.Client, *config.Token, error) {
	tok, err := config.LoadToken()
	if err != nil {
		return nil, nil, err
	}
	return moodle.NewClient(tok.BaseURL, tok.Token), tok, nil
}

// friendlyAPIError turns token problems into actionable messages.
func friendlyAPIError(err error) error {
	var apiErr *moodle.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Errorcode {
		case "invalidtoken":
			return fmt.Errorf("the saved token was rejected (expired or revoked): run `webeep-sync-cli login` again\n  server said: %s", apiErr)
		case "invalidparameter", "missingparamkey":
			return fmt.Errorf("API call rejected: %s", apiErr)
		}
	}
	return err
}

func readLine(prompt string) (string, error) {
	fmt.Print(prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// --- login -------------------------------------------------------------------

const loginHelp = `To get a WeBeep web service token without a password prompt:
  1. log in at https://webeep.polimi.it in your browser (AUA or SPID)
  2. open DevTools (F12) -> Network, then visit
     https://webeep.polimi.it/admin/tool/mobile/launch.php?service=moodle_mobile_app&passport=1234
  3. find the 302/303 response whose Location starts with moodlemobile:// and copy
     its token=... value (a long base64 string). A browser that blocks the redirect
     still shows that URL in the address bar or network tab.
Paste the value below. You can also use --token, --url (the whole moodlemobile://
URL), --cookie "MoodleSession=..." or --username with the WEBEEP_PASSWORD variable.`

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	token := fs.String("token", "", "web service token (base64 blob or raw token)")
	urlFlag := fs.String("url", "", "full moodlemobile://...token=... redirect URL")
	cookie := fs.String("cookie", "", "session cookies from a logged-in browser, e.g. 'MoodleSession=abc123'")
	username := fs.String("username", "", "try password login via /login/token.php (password from WEBEEP_PASSWORD)")
	base := fs.String("base", config.DefaultBaseURL, "WeBeep base URL")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	probe := moodle.NewClient(*base, "")

	var rawToken string
	var err error
	switch {
	case *token != "":
		if *token == "-" {
			*token, err = readLine("token: ")
			if err != nil {
				return err
			}
		}
		rawToken, err = moodle.ParseToken(*token)
	case *urlFlag != "":
		rawToken, err = moodle.ParseToken(*urlFlag)
	case *cookie != "":
		rawToken, err = probe.TokenViaSessionCookie(ctx, *cookie)
	case *username != "":
		password := os.Getenv("WEBEEP_PASSWORD")
		if password == "" {
			password, err = readLine("password (typed input is not hidden): ")
			if err != nil {
				return err
			}
		}
		rawToken, err = probe.TokenViaPasswordLogin(ctx, *username, password)
	default:
		fmt.Println(loginHelp)
		pasted, err := readLine("token or moodlemobile:// URL: ")
		if err != nil {
			return err
		}
		rawToken, err = moodle.ParseToken(pasted)
	}
	if err != nil {
		return err
	}

	// Validate the token before saving it.
	client := moodle.NewClient(*base, rawToken)
	info, err := client.GetSiteInfo(ctx)
	if err != nil {
		return fmt.Errorf("token obtained but rejected by the API: %w", friendlyAPIError(err))
	}
	if err := config.SaveToken(&config.Token{
		BaseURL:    *base,
		Token:      rawToken,
		User:       info.FullName,
		ObtainedAt: time.Now().UTC(),
	}); err != nil {
		return err
	}
	dir, _ := config.Dir()
	fmt.Printf("Logged in as %s (site: %s)\ntoken stored in %s/token.json (keep it private)\n",
		info.FullName, info.SiteName, dir)
	return nil
}

func cmdLogout() error {
	if err := config.DeleteToken(); err != nil {
		return err
	}
	fmt.Println("saved token removed")
	return nil
}

func cmdWhoami(args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	fs.Parse(args)

	client, tok, err := tokenClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	info, err := client.GetSiteInfo(ctx)
	if err != nil {
		return friendlyAPIError(err)
	}
	fmt.Printf("user:    %s (@%s)\nsite:    %s\nbase:    %s\ntoken:   obtained %s\n",
		info.FullName, info.Username, info.SiteName, tok.BaseURL,
		tok.ObtainedAt.Format(time.RFC3339))
	return nil
}

// --- courses -----------------------------------------------------------------

// resolvedCourse is a course with its final download folder name.
type resolvedCourse struct {
	moodle.CourseSummary
	Folder string
	Sync   bool
}

// resolveCourses applies config preferences to the remote course list and
// picks collision-free folder names.
func resolveCourses(courses []moodle.CourseSummary, conf *config.Config) []resolvedCourse {
	defaults := make(map[int64]string, len(courses))
	count := make(map[string]int, len(courses))
	for _, c := range courses {
		dir := fsx.Sanitize(moodle.DefaultCourseDir(c.FullName))
		if dir == "" {
			dir = fmt.Sprintf("course-%d", c.ID)
		}
		defaults[c.ID] = dir
		count[dir]++
	}

	used := make(map[string]struct{}, len(courses))
	out := make([]resolvedCourse, 0, len(courses))
	for _, c := range courses {
		prefs := conf.CoursePrefs(c.ID)
		dir := defaults[c.ID]
		if prefs.Name != nil && *prefs.Name != "" {
			dir = fsx.Sanitize(*prefs.Name)
		}
		if _, taken := used[dir]; taken {
			// Two courses would share a folder: fall back to the full name.
			if alt := fsx.Sanitize(c.FullName); alt != "" && alt != dir {
				if _, taken := used[alt]; !taken {
					fmt.Fprintf(os.Stderr, "warning: %q clashes with another course, using %q instead\n", dir, alt)
					dir = alt
				}
			}
		}
		if _, taken := used[dir]; taken {
			dir = fmt.Sprintf("%s (%d)", dir, c.ID)
		}
		used[dir] = struct{}{}

		sync := true
		if prefs.Sync != nil {
			sync = *prefs.Sync
		}
		out = append(out, resolvedCourse{CourseSummary: c, Folder: dir, Sync: sync})
	}
	return out
}

func cmdCourses(args []string) error {
	fs := flag.NewFlagSet("courses", flag.ExitOnError)
	fs.Parse(args)

	client, _, err := tokenClient()
	if err != nil {
		return err
	}
	conf, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	info, err := client.GetSiteInfo(ctx)
	if err != nil {
		return friendlyAPIError(err)
	}
	remote, err := client.GetUserCourses(ctx, info.UserID)
	if err != nil {
		return friendlyAPIError(err)
	}
	resolved := resolveCourses(remote, conf)

	fmt.Printf("%-10s %-5s  %-40s  %s\n", "ID", "SYNC", "DOWNLOAD FOLDER", "WE BEEP FULL NAME")
	for _, c := range resolved {
		mark := "no"
		if c.Sync {
			mark = "yes"
		}
		full := ""
		if c.FullName != c.Folder {
			full = c.FullName
		}
		fmt.Printf("%-10d %-5s  %-40s  %s\n", c.ID, mark, truncate(c.Folder, 40), full)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func cmdCourse(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: webeep-sync-cli course enable|disable|rename <course-id> [new folder name]")
	}
	action, idArg := args[0], args[1]
	id, err := strconv.ParseInt(idArg, 10, 64)
	if err != nil {
		return fmt.Errorf("course id must be a number (see `webeep-sync-cli courses`): %w", err)
	}
	conf, err := config.Load()
	if err != nil {
		return err
	}
	prefs := conf.CoursePrefs(id)

	switch action {
	case "enable":
		yes := true
		prefs.Sync = &yes
	case "disable":
		no := false
		prefs.Sync = &no
	case "rename":
		name := strings.TrimSpace(strings.Join(args[2:], " "))
		if name == "" {
			prefs.Name = nil
		} else {
			name = fsx.Sanitize(name)
			prefs.Name = &name
		}
	default:
		return fmt.Errorf("unknown action %q (use enable, disable or rename)", action)
	}
	conf.SetCoursePrefs(id, prefs)
	if err := conf.Save(); err != nil {
		return err
	}
	fmt.Printf("course %d: %s\n", id, describePrefs(prefs))
	return nil
}

func describePrefs(p config.CoursePrefs) string {
	sync := "synced by default"
	if p.Sync != nil {
		if *p.Sync {
			sync = "will sync"
		} else {
			sync = "will NOT sync"
		}
	}
	if p.Name != nil && *p.Name != "" {
		sync += fmt.Sprintf(", folder %q", *p.Name)
	}
	return sync
}

// --- sync --------------------------------------------------------------------

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	dirFlag := fs.String("dir", "", "download folder (default: \"download_dir\" from config, else ~/WeBeep)")
	workers := fs.Int("workers", 0, "parallel downloads (default 3)")
	dryRun := fs.Bool("dry-run", false, "print what would be downloaded and change nothing")
	noOverwrite := fs.Bool("no-overwrite", false, "never replace an existing local file, even if the WeBeep copy changed")
	clobber := fs.Bool("clobber", false, "allow overwriting even files recorded as local-only (protected)")
	all := fs.Bool("all", false, "sync every enrolled course, ignoring disabled ones")
	quiet := fs.Bool("quiet", false, "print only the final summary")
	var courseFlags stringList
	fs.Var(&courseFlags, "course", "sync only courses whose name contains this text (repeatable)")
	var extraMods stringList
	fs.Var(&extraMods, "exclude-mod", "additionally skip this module type (repeatable)")
	fs.Parse(args)

	conf, err := config.Load()
	if err != nil {
		return err
	}
	client, _, err := tokenClient()
	if err != nil {
		return err
	}

	dir := *dirFlag
	if dir == "" {
		dir = conf.DownloadDir
	}
	if dir == "" {
		dir = filepath.Join("~", "WeBeep")
	}
	dir = expandTilde(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating download folder %s: %w", dir, err)
	}

	nWorkers := *workers
	if nWorkers <= 0 {
		nWorkers = conf.Workers
	}
	if nWorkers <= 0 {
		nWorkers = 3
	}

	exclude := append(append([]string{}, moodle.DefaultExcludeMods...), conf.ExcludeMods...)
	exclude = append(exclude, extraMods...)

	ctx, stop := ctxWithInterrupt()
	defer stop()

	info, err := client.GetSiteInfo(ctx)
	if err != nil {
		return friendlyAPIError(err)
	}
	remote, err := client.GetUserCourses(ctx, info.UserID)
	if err != nil {
		return friendlyAPIError(err)
	}
	resolved := resolveCourses(remote, conf)

	var chosen []resolvedCourse
	skipped := 0
	for _, c := range resolved {
		include := c.Sync
		if *all {
			include = true
		}
		if len(courseFlags) > 0 {
			include = false
			for _, pattern := range courseFlags {
				if nameMatches(c, pattern) {
					include = true
					break
				}
			}
		}
		if !include {
			skipped++
			continue
		}
		chosen = append(chosen, c)
	}
	if len(chosen) == 0 {
		fmt.Println("no courses selected for syncing (see `webeep-sync-cli courses` and the --course/--all flags)")
		return nil
	}

	fmt.Printf("webeep-sync-cli %s\n", version)
	fmt.Printf("user %s | %d course(s) -> %s\n", info.FullName, len(chosen), dir)

	// Fetch contents of all selected courses concurrently (bounded).
	type courseFetch struct {
		course   resolvedCourse
		sections []moodle.Section
		err      error
	}
	results := make([]courseFetch, len(chosen))
	{
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for i, c := range chosen {
			wg.Add(1)
			go func(i int, c resolvedCourse) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				sections, err := client.GetCourseContents(ctx, c.ID)
				results[i] = courseFetch{course: c, sections: sections, err: err}
			}(i, c)
		}
		wg.Wait()
	}
	var entries []moodle.FileEntry
	for _, r := range results {
		if r.err != nil {
			return friendlyAPIError(fmt.Errorf("fetching contents of %s: %w", r.course.FullName, r.err))
		}
		entries = append(entries, moodle.CollectFiles(r.course.ID, r.course.Folder, r.sections, exclude)...)
	}

	manifest := syncpkg.LoadManifest(dir)
	actions := syncpkg.Plan(entries, dir, manifest, syncpkg.PlanOpts{
		NoOverwrite: *noOverwrite,
		Clobber:     *clobber,
	})

	counts := make(map[syncpkg.ActionKind]int)
	for _, a := range actions {
		counts[a.Kind]++
	}
	fmt.Printf("plan: %d new, %d update, %d unchanged, %d adopted, %d protected, %d kept(--no-overwrite), %d 404-cached, %d local-dir conflicts\n",
		counts[syncpkg.ActionNew], counts[syncpkg.ActionUpdate], counts[syncpkg.ActionUnchanged],
		counts[syncpkg.ActionAdopt], counts[syncpkg.ActionProtected]+counts[syncpkg.ActionProtectedChanged],
		counts[syncpkg.ActionNoOverwrite], counts[syncpkg.ActionSkipMissing], counts[syncpkg.ActionLocalIsDir])

	for _, a := range actions {
		switch a.Kind {
		case syncpkg.ActionProtectedChanged:
			fmt.Printf("note: %s changed on WeBeep but is protected as a local file; use --clobber to replace it\n", a.Entry.RelPath)
		case syncpkg.ActionNoOverwrite:
			fmt.Printf("note: %s changed on WeBeep but was kept (--no-overwrite)\n", a.Entry.RelPath)
		case syncpkg.ActionLocalIsDir:
			fmt.Printf("note: %s exists as a local directory and was skipped\n", a.Entry.RelPath)
		}
	}

	if *dryRun {
		for _, a := range actions {
			if a.Kind.Downloads() {
				fmt.Printf("  would download (%s): %s  %s\n", a.Kind.Label(), a.Entry.RelPath, fsx.HumanBytes(a.Entry.Size))
			}
		}
		fmt.Println("dry run: nothing was changed")
		return nil
	}

	if err := manifest.Save(dir); err != nil {
		return fmt.Errorf("saving manifest: %w", err)
	}

	if counts[syncpkg.ActionNew]+counts[syncpkg.ActionUpdate] == 0 {
		fmt.Println("everything is already synced")
		return nil
	}

	var failNotes []string
	var notesMu sync.Mutex
	stats, runErr := syncpkg.Run(ctx, client, actions, dir, manifest, syncpkg.RunOpts{Workers: nWorkers}, syncpkg.Callbacks{
		OnDone: func(a syncpkg.Action, written int64, done, total int) {
			if !*quiet {
				kind := "new"
				if a.Kind == syncpkg.ActionUpdate {
					kind = "upd"
				}
				fmt.Printf("  [%d/%d] %s  %s  (%s)\n", done, total, kind, a.Entry.RelPath, fsx.HumanBytes(written))
			}
		},
		OnFail: func(a syncpkg.Action, err error) {
			notesMu.Lock()
			failNotes = append(failNotes, fmt.Sprintf("%s: %v", a.Entry.RelPath, err))
			notesMu.Unlock()
			if !*quiet {
				fmt.Printf("  ! %s: %v\n", a.Entry.RelPath, err)
			}
		},
	})

	fmt.Printf("synced: %d new, %d updated (%s downloaded) | %d not on server, %d failed\n",
		stats.New, stats.Updated, fsx.HumanBytes(stats.Bytes), stats.NotFound, stats.Failed)
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			return fmt.Errorf("sync interrupted (completed work up to this point was kept)")
		}
		if errors.Is(runErr, moodle.ErrTokenRejected) {
			return fmt.Errorf("WeBeep rejected the token mid-sync: run `webeep-sync-cli login` again")
		}
		return runErr
	}
	if len(failNotes) > 0 {
		return fmt.Errorf("%d file(s) failed to download", len(failNotes))
	}
	return nil
}

// nameMatches reports whether a course matches a --course pattern
// (case-insensitive substring of folder, fullname or display name).
func nameMatches(c resolvedCourse, pattern string) bool {
	p := strings.ToLower(strings.TrimSpace(pattern))
	return strings.Contains(strings.ToLower(c.Folder), p) ||
		strings.Contains(strings.ToLower(c.FullName), p) ||
		strings.Contains(strings.ToLower(c.DisplayName), p)
}

// --- doctor ------------------------------------------------------------------

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	base := fs.String("base", config.DefaultBaseURL, "WeBeep base URL")
	fs.Parse(args)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	failed := false

	// 1) the site itself
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(*base, "/")+"/login/index.php", nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "webeep-sync-cli")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("FAIL  webeep.polimi.it is unreachable: %v\n", err)
		failed = true
	} else {
		resp.Body.Close()
		if resp.StatusCode < 400 {
			fmt.Printf("ok    webeep page reachable (HTTP %d)\n", resp.StatusCode)
		} else {
			fmt.Printf("FAIL  webeep page returned HTTP %d\n", resp.StatusCode)
			failed = true
		}
	}

	// 2) the REST endpoint, with a deliberately invalid probe token
	probe := moodle.NewClient(*base, "doctor-probe-token-0000000000000000000000")
	probe.RetryDelay = 200 * time.Millisecond
	if _, err := probe.GetSiteInfo(ctx); err != nil {
		var apiErr *moodle.APIError
		if errors.As(err, &apiErr) && apiErr.Errorcode == "invalidtoken" {
			fmt.Println("ok    REST API answers and rejects bad tokens as expected")
		} else if errors.As(err, &apiErr) {
			fmt.Printf("ok    REST API answers (error: %v)\n", apiErr)
		} else {
			fmt.Printf("FAIL  REST API unreachable: %v\n", err)
			failed = true
		}
	} else {
		fmt.Println("warn  REST API accepted a probe token it should have rejected")
	}

	// 3) the stored token
	if tok, err := config.LoadToken(); err == nil {
		client := moodle.NewClient(tok.BaseURL, tok.Token)
		if info, err := client.GetSiteInfo(ctx); err != nil {
			fmt.Printf("FAIL  stored token rejected: %v\n", friendlyAPIError(err))
			failed = true
		} else {
			fmt.Printf("ok    stored token valid (user: %s, obtained %s)\n",
				info.FullName, tok.ObtainedAt.Format(time.RFC3339))
		}
	} else {
		fmt.Println("note  no stored token: run `webeep-sync-cli login`")
	}

	if dir, err := config.Dir(); err == nil {
		fmt.Printf("      config dir: %s\n", dir)
	}
	if failed {
		return errors.New("doctor found problems")
	}
	return nil
}
