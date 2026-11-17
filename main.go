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
	"sort"
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
	case "dir":
		err = cmdDir(args)
	case "sync":
		err = cmdSync(args)
	case "ui", "tui":
		err = cmdUI(args)
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
  webeep-sync-cli ui                                                    interactive course/folder picker
  webeep-sync-cli login    [--token T|--url U|--cookie C|--username U] obtain a web service token
  webeep-sync-cli logout                                               forget the saved token
  webeep-sync-cli whoami                                               show the logged-in account
  webeep-sync-cli dir [path|--clear]                                   show or set the folder courses live in
  webeep-sync-cli courses                                              list enrolled courses
  webeep-sync-cli course enable|disable <id>                          include or skip one course
  webeep-sync-cli course path <id> "Y1S1/Biochip"                    map it to a local folder
  webeep-sync-cli course rename <id> [name]                           (same as path)
  webeep-sync-cli sync   [flags]                                       download new/changed files
  webeep-sync-cli doctor                                               check endpoints and token
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
	return askDownloadDirOnce()
}

// askDownloadDirOnce asks where course folders should live, but only if that has
// not been configured yet (and only when we can actually ask).
func askDownloadDirOnce() error {
	if !stdinInteractive() {
		return nil
	}
	conf, err := config.Load()
	if err != nil {
		return err
	}
	if conf.DownloadDir != "" {
		fmt.Printf("course folders sync under %s (`webeep-sync-cli dir <folder>` changes it)\n", conf.DownloadDir)
		return nil
	}
	dir, err := promptDownloadDir(conf)
	if err != nil {
		return err
	}
	conf.DownloadDir = dir
	return conf.Save()
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

// coursePath returns the local folder a course is mapped to in the config, or
// "" when it should keep the default (its WeBeep name).
func coursePath(prefs config.CoursePrefs) string {
	raw := ""
	switch {
	case prefs.Path != nil && strings.TrimSpace(*prefs.Path) != "":
		raw = *prefs.Path
	case prefs.Name != nil && strings.TrimSpace(*prefs.Name) != "":
		raw = *prefs.Name
	}
	return sanitizeCoursePath(raw)
}

// sanitizeCoursePath cleans user input like " Y1S1 / Biochip " into
// "Y1S1/Biochip". Mappings are always relative to the download root: absolute
// paths and anything that walks up with ".." are refused ("" is returned).
func sanitizeCoursePath(p string) string {
	p = filepath.ToSlash(strings.TrimSpace(p))
	if p == "" || strings.HasPrefix(p, "/") {
		return ""
	}
	var out []string
	for _, seg := range strings.Split(p, "/") {
		seg = fsx.Sanitize(seg)
		if seg == ".." {
			return ""
		}
		if seg == "" {
			continue
		}
		out = append(out, seg)
	}
	return strings.Join(out, "/")
}

// resolveCourses applies config preferences to the remote course list and picks
// collision-free folder names. It returns the rows plus warnings worth showing
// to the user (the UI prints them in its own status area).
func resolveCourses(courses []moodle.CourseSummary, conf *config.Config) ([]resolvedCourse, []string) {
	var warnings []string
	used := make(map[string]struct{}, len(courses))
	out := make([]resolvedCourse, 0, len(courses))

	for _, c := range courses {
		prefs := conf.CoursePrefs(c.ID)
		dir := fsx.Sanitize(moodle.DefaultCourseDir(c.FullName))
		if dir == "" {
			dir = fmt.Sprintf("course-%d", c.ID)
		}
		if mapped := coursePath(prefs); mapped != "" {
			dir = mapped
		}

		if _, taken := used[dir]; taken {
			// Two courses would share a folder: fall back to the full name.
			if alt := fsx.Sanitize(c.FullName); alt != "" && alt != dir {
				if _, taken := used[alt]; !taken {
					warnings = append(warnings, fmt.Sprintf("%q is claimed by another course: %q syncs to %q instead", dir, c.DisplayName, alt))
					dir = alt
				}
			}
		}
		if _, taken := used[dir]; taken {
			dir = fmt.Sprintf("%s (%d)", dir, c.ID)
			warnings = append(warnings, fmt.Sprintf("no free folder name left: %q syncs to %q", c.DisplayName, dir))
		}
		used[dir] = struct{}{}

		sync := true
		if prefs.Sync != nil {
			sync = *prefs.Sync
		}
		out = append(out, resolvedCourse{CourseSummary: c, Folder: dir, Sync: sync})
	}
	return out, warnings
}

func cmdCourses(args []string) error {
	fs := flag.NewFlagSet("courses", flag.ExitOnError)
	fs.Parse(args)

	conf, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	// List every course, even disabled ones, so the mapping is easy to see.
	opts := defaultSyncOptions()
	opts.All = true
	target, err := prepareSync(ctx, conf, opts)
	if err != nil {
		return err
	}
	for _, w := range target.warnings {
		fmt.Fprintln(os.Stderr, "warning: "+w)
	}

	fmt.Printf("%-10s %-5s  %-38s  %s\n", "ID", "SYNC", "LOCAL FOLDER", "WE BEEP COURSE")
	for _, c := range target.rows {
		mark := "no"
		if c.Sync {
			mark = "yes"
		}
		full := ""
		if c.Folder != c.DisplayName {
			full = c.FullName
		}
		fmt.Printf("%-10d %-5s  %-38s  %s\n", c.ID, mark, truncate(c.Folder, 38), full)
	}
	fmt.Printf("\nlocal root: %s\n", target.dir)
	fmt.Println("map a course: webeep-sync-cli course path <id> \"Y1S1/Biochip\"   (or: webeep-sync-cli ui)")
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func cmdCourse(args []string) error {
	if len(args) > 0 && args[0] == "list" {
		return cmdCourses(nil)
	}
	if len(args) < 2 {
		return fmt.Errorf("usage: webeep-sync-cli course list|enable|disable|path|rename <course-id> [folder]")
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
			prefs.Path = nil
			prefs.Name = nil
		} else {
			p := sanitizeCoursePath(name)
			if p == "" {
				return fmt.Errorf("%q is not a usable folder path (relative to the download folder, and no '..')", name)
			}
			prefs.Path = &p
		}
	case "path":
		// The folder is one argument, so quote it: course path 18804 "Y1S1/Biochip"
		if len(args) > 3 {
			return fmt.Errorf("give the folder as a single argument: webeep-sync-cli course path %s \"Y1S1/Biochip\"", idArg)
		}
		value := ""
		if len(args) == 3 {
			value = strings.TrimSpace(args[2])
		}
		if value == "" {
			prefs.Path = nil
			prefs.Name = nil
		} else {
			p := sanitizeCoursePath(value)
			if p == "" {
				return fmt.Errorf("%q is not a usable folder path (relative to the download folder, and no '..')", value)
			}
			prefs.Path = &p
		}
	default:
		return fmt.Errorf("unknown action %q (use enable, disable, path or rename)", action)
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
	if mapped := coursePath(p); mapped != "" {
		sync += fmt.Sprintf(", folder %q", mapped)
	}
	return sync
}

// --- the download root -------------------------------------------------------

func cmdDir(args []string) error {
	fs := flag.NewFlagSet("dir", flag.ExitOnError)
	clearFlag := fs.Bool("clear", false, "forget the configured folder (falls back to ~/WeBeep)")
	fs.Parse(args)

	conf, err := config.Load()
	if err != nil {
		return err
	}
	rest := fs.Args()
	switch {
	case *clearFlag:
		conf.DownloadDir = ""
		if err := conf.Save(); err != nil {
			return err
		}
		fmt.Println("download folder cleared: course folders will go under ~/WeBeep")
	case len(rest) > 1:
		return fmt.Errorf("usage: webeep-sync-cli dir [folder] | --clear")
	case len(rest) == 0:
		if conf.DownloadDir == "" {
			fmt.Println("~/WeBeep (not configured: this is the fallback)")
		} else {
			fmt.Println(conf.DownloadDir)
		}
	default:
		dir := expandTilde(strings.TrimSpace(rest[0]))
		if !filepath.IsAbs(dir) {
			return fmt.Errorf("%s is not an absolute path (try /home/you/School/PoliMi or ~/School/PoliMi)", rest[0])
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", dir, err)
		}
		conf.DownloadDir = dir
		if err := conf.Save(); err != nil {
			return err
		}
		fmt.Printf("course folders will sync under %s\n", dir)
	}
	return nil
}

// --- sync --------------------------------------------------------------------

// --- sync plumbing shared by 'sync' and the UI -------------------------------

// syncOptions are the knobs of a sync run.
type syncOptions struct {
	Dir         string   // download root for this run
	Courses     []string // --course patterns
	All         bool
	NoOverwrite bool
	Clobber     bool
	Workers     int
	ExtraMods   []string
	Quiet       bool
}

// defaultSyncOptions is a plain run: default workers, no filters.
func defaultSyncOptions() syncOptions { return syncOptions{Workers: 3} }

// syncTarget is a prepared run: the account, the download root and the courses
// the current configuration selects.
type syncTarget struct {
	conf     *config.Config
	client   *moodle.Client
	user     string
	dir      string
	remote   []moodle.CourseSummary
	rows     []resolvedCourse
	warnings []string
	chosen   []resolvedCourse
	skipped  int
}

// prepareSync checks the stored token and works out which course folders go
// into which download root.
func prepareSync(ctx context.Context, conf *config.Config, opts syncOptions) (*syncTarget, error) {
	client, _, err := tokenClient()
	if err != nil {
		return nil, err
	}
	dir := opts.Dir
	if dir == "" {
		dir = conf.DownloadDir
	}
	if dir == "" {
		dir = filepath.Join("~", "WeBeep")
	}
	dir = expandTilde(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating download folder %s: %w", dir, err)
	}

	info, err := client.GetSiteInfo(ctx)
	if err != nil {
		return nil, friendlyAPIError(err)
	}
	remote, err := client.GetUserCourses(ctx, info.UserID)
	if err != nil {
		return nil, friendlyAPIError(err)
	}
	rows, warnings := resolveCourses(remote, conf)
	chosen, skipped := selectCourses(rows, opts)
	return &syncTarget{
		conf:     conf,
		client:   client,
		user:     info.FullName,
		dir:      dir,
		remote:   remote,
		rows:     rows,
		warnings: warnings,
		chosen:   chosen,
		skipped:  skipped,
	}, nil
}

// selectCourses applies the per-course switches and the --course/--all flags.
func selectCourses(resolved []resolvedCourse, opts syncOptions) (chosen []resolvedCourse, skipped int) {
	for _, c := range resolved {
		include := c.Sync
		if opts.All {
			include = true
		}
		if len(opts.Courses) > 0 {
			include = false
			for _, pattern := range opts.Courses {
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
	return chosen, skipped
}

// plan asks WeBeep what the selected courses contain and classifies every file
// against what is already in the download folder. The returned manifest already
// has adopted and protected entries recorded.
func (t *syncTarget) plan(ctx context.Context, opts syncOptions) ([]syncpkg.Action, *syncpkg.Manifest, error) {
	exclude := append(append([]string{}, moodle.DefaultExcludeMods...), t.conf.ExcludeMods...)
	exclude = append(exclude, opts.ExtraMods...)

	// Fetch the contents of all selected courses concurrently (bounded).
	type courseFetch struct {
		course   resolvedCourse
		sections []moodle.Section
		err      error
	}
	results := make([]courseFetch, len(t.chosen))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, c := range t.chosen {
		wg.Add(1)
		go func(i int, c resolvedCourse) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			sections, err := t.client.GetCourseContents(ctx, c.ID)
			results[i] = courseFetch{course: c, sections: sections, err: err}
		}(i, c)
	}
	wg.Wait()

	var entries []moodle.FileEntry
	for _, r := range results {
		if r.err != nil {
			return nil, nil, friendlyAPIError(fmt.Errorf("fetching contents of %s: %w", r.course.FullName, r.err))
		}
		entries = append(entries, moodle.CollectFiles(r.course.ID, r.course.Folder, r.sections, exclude)...)
	}

	manifest := syncpkg.LoadManifest(t.dir)
	actions := syncpkg.Plan(entries, t.dir, manifest, syncpkg.PlanOpts{
		NoOverwrite: opts.NoOverwrite,
		Clobber:     opts.Clobber,
	})
	return actions, manifest, nil
}

// countActions tallies the plan by kind.
func countActions(actions []syncpkg.Action) map[syncpkg.ActionKind]int {
	counts := make(map[syncpkg.ActionKind]int)
	for _, a := range actions {
		counts[a.Kind]++
	}
	return counts
}

// stdinInteractive reports whether it is possible to ask the user a question.
func stdinInteractive() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// promptDownloadDir asks where course folders should live.
func promptDownloadDir(conf *config.Config) (string, error) {
	current := conf.DownloadDir
	if current == "" {
		current = filepath.Join("~", "WeBeep")
	}
	for {
		answer, err := readLine(fmt.Sprintf("which folder should your course folders live in? [%s]: ", current))
		if err != nil {
			return "", err
		}
		if answer == "" {
			answer = current
		}
		dir := expandTilde(answer)
		if !filepath.IsAbs(dir) {
			fmt.Printf("%s is not an absolute path (try /home/you/School/PoliMi or ~/School/PoliMi)\n", dir)
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("creating %s: %w", dir, err)
		}
		fmt.Printf("course folders will sync under %s\n", dir)
		return dir, nil
	}
}

// localCourseDirs lists folders that already exist under the download root, up
// to two levels deep (for example "Y1S1/Biochip"), as suggestions when mapping
// a course. Hidden folders and sync-tool folders are skipped.
func localCourseDirs(root string) []string {
	skip := map[string]bool{".stfolder": true, ".stversions": true, ".sync": true, ".snapshot": true, ".git": true, "node_modules": true}
	var out []string
	top, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, d := range top {
		if !d.IsDir() || skip[d.Name()] || strings.HasPrefix(d.Name(), ".") {
			continue
		}
		out = append(out, d.Name())
		sub, err := os.ReadDir(filepath.Join(root, d.Name()))
		if err != nil {
			continue
		}
		for _, s := range sub {
			if !s.IsDir() || skip[s.Name()] || strings.HasPrefix(s.Name(), ".") {
				continue
			}
			out = append(out, d.Name()+"/"+s.Name())
		}
	}
	sort.Strings(out)
	return out
}

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

	// First run: ask where the course folders should live, then remember it.
	if *dirFlag == "" && conf.DownloadDir == "" && !*quiet && stdinInteractive() {
		dir, err := promptDownloadDir(conf)
		if err != nil {
			return err
		}
		conf.DownloadDir = dir
		if err := conf.Save(); err != nil {
			return err
		}
	}

	opts := syncOptions{
		Dir:         expandTilde(*dirFlag),
		Courses:     courseFlags,
		All:         *all,
		NoOverwrite: *noOverwrite,
		Clobber:     *clobber,
		ExtraMods:   extraMods,
		Quiet:       *quiet,
	}
	nWorkers := *workers
	if nWorkers <= 0 {
		nWorkers = conf.Workers
	}
	if nWorkers <= 0 {
		nWorkers = 3
	}
	opts.Workers = nWorkers

	ctx, stop := ctxWithInterrupt()
	defer stop()

	target, err := prepareSync(ctx, conf, opts)
	if err != nil {
		return err
	}
	client, dir := target.client, target.dir

	if len(target.chosen) == 0 {
		fmt.Println("no courses selected for syncing (see `webeep-sync-cli courses`, `webeep-sync-cli ui`, or the --course/--all flags)")
		return nil
	}

	fmt.Printf("webeep-sync-cli %s\n", version)
	fmt.Printf("user %s | %d course(s) -> %s\n", target.user, len(target.chosen), dir)
	for _, c := range target.chosen {
		fmt.Printf("  %-42s  <-  %s\n", truncate(c.Folder, 42), c.FullName)
	}
	if target.skipped > 0 {
		fmt.Printf("  %d course(s) skipped (re-enable with `webeep-sync-cli course enable <id>`)\n", target.skipped)
	}

	actions, manifest, err := target.plan(ctx, opts)
	if err != nil {
		return err
	}

	counts := countActions(actions)
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
		// The point of a dry run is to show what is *not* touched.
		for _, a := range actions {
			switch a.Kind {
			case syncpkg.ActionAdopt:
				fmt.Printf("  adopted (yours already matches WeBeep): %s\n", a.Entry.RelPath)
			case syncpkg.ActionProtected:
				fmt.Printf("  protected (your own file, WeBeep will not touch it): %s\n", a.Entry.RelPath)
			case syncpkg.ActionProtectedChanged:
				fmt.Printf("  protected (your own file, WeBeep has a newer version): %s\n", a.Entry.RelPath)
			case syncpkg.ActionLocalIsDir:
				fmt.Printf("  skipped (you have a folder named like this file): %s\n", a.Entry.RelPath)
			}
		}
		fmt.Println("dry run: nothing was changed")
		return nil
	}

	// Files you edited yourself are never touched unless you say otherwise.
	if n := counts[syncpkg.ActionProtectedChanged]; n > 0 && !*clobber {
		if !stdinInteractive() || *quiet {
			fmt.Printf("note: %d file(s) you edited have newer versions on WeBeep; yours stay as they are (use --clobber to replace them)\n", n)
		} else {
			answer, err := readLine(fmt.Sprintf("%d file(s) you edited have a newer version on WeBeep. Replace yours? [y/N]: ", n))
			if err != nil {
				return err
			}
			if answer == "" {
				answer = "no"
			}
			if strings.HasPrefix(strings.ToLower(answer), "y") {
				clobbered := syncpkg.ClobberProtected(actions, manifest)
				counts[syncpkg.ActionUpdate] += clobbered
				counts[syncpkg.ActionProtectedChanged] -= clobbered
				fmt.Printf("replacing %d file(s) with the version on WeBeep\n", clobbered)
			} else {
				fmt.Println("keeping your edited copies")
			}
		}
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
