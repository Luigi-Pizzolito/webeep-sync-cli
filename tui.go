package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/config"
	"github.com/Luigi-Pizzolito/webeep-sync-cli/internal/fsx"
	syncpkg "github.com/Luigi-Pizzolito/webeep-sync-cli/internal/sync"
)

// cmdUI runs the interactive picker: map every WeBeep course to a folder on
// your disk, preview what a sync would do, and only then download.
func cmdUI(args []string) error {
	if !stdinInteractive() {
		return fmt.Errorf("the UI needs a terminal: use `webeep-sync-cli sync --dry-run` instead")
	}
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	dirFlag := fs.String("dir", "", "root folder for this run only, instead of the configured one (handy for testing)")
	workers := fs.Int("workers", 0, "parallel downloads")
	fs.Parse(args)

	conf, err := config.Load()
	if err != nil {
		return err
	}
	if _, err := config.LoadToken(); err != nil {
		return err
	}
	opts := defaultSyncOptions()
	opts.Dir = expandTilde(strings.TrimSpace(*dirFlag))
	if *workers > 0 {
		opts.Workers = *workers
	}

	p := tea.NewProgram(newUIModel(conf, opts), tea.WithAltScreen())
	final, err := p.Run()
	if err != nil {
		return err
	}
	m, ok := final.(*uiModel)
	if !ok {
		return nil
	}
	// Say something useful once the alternate screen is gone.
	if m.status != "" {
		fmt.Println(m.status)
	}
	if m.errMsg != "" {
		return errors.New(m.errMsg)
	}
	return nil
}

type uiMode int

const (
	modeLoading uiMode = iota
	modeCourses
	modeMapping
	modeDir
	modePreview
	modeSync
)

type loadedMsg struct {
	target *syncTarget
	err    error
}

type planMsg struct {
	actions  []syncpkg.Action
	manifest *syncpkg.Manifest
	key      string
	thenRun  bool
	err      error
}

type progressMsg struct {
	done, total int
	current     string
	inflight    int64
	failed      bool
}

type runDoneMsg struct {
	stats syncpkg.Stats
	err   error
}

type tickMsg time.Time

type uiModel struct {
	conf   *config.Config
	opts   syncOptions // --dir and --workers for this run
	target *syncTarget
	rows   []resolvedCourse
	warn   []string

	mode    uiMode
	cursor  int
	scroll  int
	workers int
	clobber bool // also replace files I edited myself

	busy    bool
	status  string
	errMsg  string
	width   int
	height  int
	spinner spinner.Model
	input   textinput.Model
	suggest []string
	sugIdx  int
	mapFor  int64
	mapName string
	mapWas  string

	planCache     []syncpkg.Action
	manifestCache *syncpkg.Manifest
	cacheKey      string

	events chan any
	cancel context.CancelFunc
	done   int
	total  int
	notes  []string
}

func newUIModel(conf *config.Config, opts syncOptions) *uiModel {
	in := textinput.New()
	in.Prompt = "> "
	in.Width = 46
	workers := opts.Workers
	if workers <= 0 {
		workers = conf.Workers
	}
	if workers <= 0 {
		workers = 3
	}
	opts.Workers = workers
	return &uiModel{
		conf:    conf,
		opts:    opts,
		workers: workers,
		mode:    modeLoading,
		spinner: spinner.New(spinner.WithSpinner(spinner.Line)),
		input:   in,
	}
}

func (m *uiModel) Init() tea.Cmd {
	return m.loadCmd()
}

// loadCmd asks WeBeep who we are and which courses we are enrolled in.
func (m *uiModel) loadCmd() tea.Cmd {
	conf, opts := m.conf, m.opts
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		target, err := prepareSync(ctx, conf, opts)
		return loadedMsg{target: target, err: err}
	}
}

func (m *uiModel) dir() string {
	if m.target != nil {
		return m.target.dir
	}
	if m.opts.Dir != "" {
		return m.opts.Dir
	}
	if m.conf.DownloadDir != "" {
		return expandTilde(m.conf.DownloadDir)
	}
	return defaultDownloadDir()
}

// rebuild re-resolves folders after a mapping or switch changed. No network
// needed: the course list is already in m.target. The cached plan stays valid,
// because file counts per course do not depend on which ones you select.
func (m *uiModel) rebuild() {
	rows, warnings := resolveCourses(m.target.remote, m.conf)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Folder < rows[j].Folder })
	m.rows, m.warn = rows, warnings
	m.target.rows = rows
	m.target.warnings = warnings
	m.target.chosen, m.target.skipped = selectCourses(rows, defaultSyncOptions())
}

func (m *uiModel) invalidatePlan() {
	m.planCache, m.manifestCache, m.cacheKey = nil, nil, ""
}

// planKey ties a cached plan to the folder layout it was built for. Selecting
// courses in and out does not change it, so counts survive toggling.
func (m *uiModel) planKey() string {
	parts := make([]string, 0, len(m.rows))
	for _, r := range m.rows {
		parts = append(parts, fmt.Sprintf("%d=%s", r.ID, r.Folder))
	}
	return m.dir() + "|" + strings.Join(parts, ",")
}

// selectedPlan returns the cached actions for the courses you have selected.
func (m *uiModel) selectedPlan() []syncpkg.Action {
	enabled := make(map[int64]bool, len(m.rows))
	for _, r := range m.rows {
		enabled[r.ID] = r.Sync
	}
	out := make([]syncpkg.Action, 0, len(m.planCache))
	for _, a := range m.planCache {
		if enabled[a.Entry.CourseID] {
			out = append(out, a)
		}
	}
	return out
}

// fetchPlan lists what WeBeep has for every course, selected or not: seeing how
// many files an old instance holds is exactly how you decide whether to sync it.
func (m *uiModel) fetchPlan(thenRun bool) tea.Cmd {
	if m.cacheKey != "" && m.cacheKey == m.planKey() {
		if thenRun {
			return m.startRun()
		}
		m.mode = modePreview
		return nil
	}
	target := m.target
	rows := append([]resolvedCourse(nil), m.rows...)
	target.chosen, target.skipped = selectCourses(rows, syncOptions{All: true})
	key := m.planKey()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		actions, manifest, err := target.plan(ctx, syncOptions{Dir: target.dir, Workers: m.workers})
		return planMsg{actions: actions, manifest: manifest, key: key, thenRun: thenRun, err: err}
	}
}

// startRun downloads the cached plan for the selected courses, streaming
// progress through m.events.
func (m *uiModel) startRun() tea.Cmd {
	actions, manifest := m.selectedPlan(), m.manifestCache
	if manifest == nil || len(actions) == 0 {
		m.status = "nothing selected: press space next to a course, then s"
		return nil
	}
	if m.clobber {
		syncpkg.ClobberProtected(actions, manifest)
	}
	// Record adopted and protected findings before the first download.
	if err := manifest.Save(m.dir()); err != nil {
		m.notes = appendNotes(m.notes, "manifest not saved: "+err.Error())
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.events = make(chan any, 1024)
	m.mode = modeSync
	m.busy = true
	m.done, m.total, m.notes = 0, 0, nil
	for _, a := range actions {
		if a.Kind.Downloads() {
			m.total++
		}
	}
	m.status = "starting..."

	events := m.events
	go func() {
		defer close(events)
		stats, err := syncpkg.Run(ctx, m.target.client, actions, m.dir(), manifest,
			syncpkg.RunOpts{Workers: m.workers},
			syncpkg.Callbacks{
				OnDone: func(a syncpkg.Action, written int64, done, total int) {
					trySend(events, progressMsg{done: done, total: total, current: a.Entry.RelPath})
				},
				OnFail: func(a syncpkg.Action, err error) {
					trySend(events, progressMsg{current: a.Entry.RelPath + ": " + err.Error(), failed: true})
				},
				OnProgress: func(a syncpkg.Action, downloaded int64) {
					trySend(events, progressMsg{current: a.Entry.RelPath, inflight: downloaded})
				},
			})
		trySend(events, runDoneMsg{stats: stats, err: err})
	}()
	return waitFor(events)
}

func trySend(ch chan any, msg any) {
	select {
	case ch <- msg:
	default: // the UI stopped reading (we are quitting): drop this update
	}
}

func waitFor(ch chan any) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

// --- update ------------------------------------------------------------------

func (m *uiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.input.Width = minInt(msg.Width-8, 46)
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case loadedMsg:
		m.busy = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.mode = modeCourses
			return m, nil
		}
		m.target = msg.target
		m.rebuild()
		m.mode = modeCourses
		if m.conf.DownloadDir == "" && m.opts.Dir == "" {
			m.openDirEditor()
		}
		return m, nil

	case planMsg:
		m.busy = false
		if msg.err != nil {
			m.errMsg = friendlyAPIErrorText(msg.err)
			m.mode = modeCourses
			return m, nil
		}
		m.planCache, m.manifestCache, m.cacheKey = msg.actions, msg.manifest, msg.key
		if msg.thenRun {
			return m, m.startRun()
		}
		m.mode = modePreview
		return m, nil

	case progressMsg:
		if msg.done > 0 {
			m.done = msg.done
		}
		if msg.total > 0 {
			m.total = msg.total
		}
		if msg.failed {
			m.notes = appendNotes(m.notes, msg.current)
		} else if msg.current != "" {
			m.status = msg.current
			if msg.inflight > 0 {
				m.status = fmt.Sprintf("%s (%s so far)", msg.current, fsx.HumanBytes(msg.inflight))
			}
		}
		return m, waitFor(m.events)

	case runDoneMsg:
		m.busy = false
		m.mode = modeCourses
		m.status = summaryLine(msg.stats, msg.err)
		if msg.err != nil {
			m.errMsg = friendlyAPIErrorText(msg.err)
		}
		m.invalidatePlan()
		return m, nil

	case tickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.busy {
			return m, tea.Batch(cmd, m.tick())
		}
		return m, cmd
	}
	return m, nil
}

func summaryLine(s syncpkg.Stats, err error) string {
	line := fmt.Sprintf("synced %d new and %d updated files (%s downloaded) | %d gone from WeBeep, %d failed",
		s.New, s.Updated, fsx.HumanBytes(s.Bytes), s.NotFound, s.Failed)
	if s.StaleTemps > 0 {
		line += fmt.Sprintf(" | cleaned %d partial file(s) left by an interrupted run", s.StaleTemps)
	}
	if err != nil {
		line = "stopped early: " + line
	}
	return line
}

func friendlyAPIErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if strings.Contains(text, "login page") || strings.Contains(text, "invalid token") {
		return text + " — run `webeep-sync-cli login` again"
	}
	return text
}

func appendNotes(notes []string, line string) []string {
	notes = append(notes, line)
	if len(notes) > 5 {
		notes = notes[len(notes)-5:]
	}
	return notes
}

func (m *uiModel) tick() tea.Cmd {
	return tea.Tick(120*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *uiModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.mode == modeMapping || m.mode == modeDir {
		return m.handleEditor(msg)
	}

	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit()
		return m, tea.Quit
	case tea.KeyEsc:
		if m.mode == modeSync {
			// esc really means stop downloading. Stay on the sync screen until
			// the workers have stopped and the summary arrives.
			m.stopRun()
			return m, nil
		}
		if m.mode != modeCourses {
			m.mode = modeCourses
			return m, nil
		}
		m.quit()
		return m, tea.Quit
	case tea.KeyUp:
		m.move(-1)
		return m, nil
	case tea.KeyDown:
		m.move(1)
		return m, nil
	case tea.KeySpace:
		if m.mode == modeCourses && len(m.rows) > 0 {
			id := m.rows[m.cursor].ID
			m.toggleCourse(id)
			m.status = fmt.Sprintf("%s: sync %s", m.rows[m.cursor].Folder,
				map[bool]string{true: "on", false: "off"}[m.rows[m.cursor].Sync])
		}
		return m, nil
	case tea.KeyEnter:
		if m.mode == modePreview {
			return m, m.startRun()
		}
		return m, nil
	case tea.KeyRunes:
		return m.handleRune(string(msg.Runes))
	}
	return m, nil
}

func (m *uiModel) handleRune(r string) (tea.Model, tea.Cmd) {
	switch r {
	case "q":
		m.quit()
		return m, tea.Quit
	case "j":
		m.move(-1)
		return m, nil
	case "k":
		m.move(1)
		return m, nil
	case "c":
		m.clobber = !m.clobber
		m.status = "files you edited: " + map[bool]string{true: "will be replaced by WeBeep", false: "stay yours"}[m.clobber]
		return m, nil
	case "m":
		if m.mode == modeCourses && len(m.rows) > 0 {
			m.openMappingEditor()
		}
		return m, nil
	case "d":
		m.openDirEditor()
		return m, nil
	case "p":
		if m.target == nil {
			return m, nil
		}
		m.busy, m.errMsg = true, ""
		return m, tea.Batch(m.fetchPlan(false), m.tick())
	case "s":
		if m.target == nil {
			return m, nil
		}
		m.busy, m.errMsg = true, ""
		return m, tea.Batch(m.fetchPlan(true), m.tick())
	case "r":
		m.errMsg, m.busy = "", true
		m.mode = modeLoading
		m.invalidatePlan()
		return m, tea.Batch(m.loadCmd(), m.tick())
	}
	return m, nil
}

func (m *uiModel) handleEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		m.quit()
		return m, tea.Quit
	case tea.KeyEsc:
		m.mode = modeCourses
		m.input.Blur()
		return m, nil
	case tea.KeyTab:
		if len(m.suggest) > 0 {
			m.sugIdx = (m.sugIdx + 1) % len(m.suggest)
			m.input.SetValue(m.suggest[m.sugIdx])
			m.input.CursorEnd()
		}
		return m, nil
	case tea.KeyEnter:
		return m.saveEditor()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *uiModel) openMappingEditor() {
	row := m.rows[m.cursor]
	m.mode = modeMapping
	m.mapFor = row.ID
	m.mapName = row.DisplayName
	m.mapWas = row.Folder
	m.suggest = localCourseDirs(m.dir())
	m.sugIdx = 0
	m.input.SetValue(row.Folder)
	m.input.Placeholder = "Y1S1/Biochip"
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *uiModel) openDirEditor() {
	m.mode = modeDir
	m.suggest = nil
	m.mapFor, m.mapName, m.mapWas = 0, "", ""
	m.input.SetValue(m.dir())
	m.input.Placeholder = "/home/you/School/PoliMi"
	m.input.CursorEnd()
	m.input.Focus()
}

func (m *uiModel) saveEditor() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	m.input.Blur()

	if m.mode == modeDir {
		dir := expandTilde(value)
		if !filepath.IsAbs(dir) {
			m.errMsg = fmt.Sprintf("%s is not an absolute path (try %s or ~/School/PoliMi)", value, pathHint())
			return m, nil
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			m.errMsg = fmt.Sprintf("creating %s: %v", dir, err)
			return m, nil
		}
		m.conf.DownloadDir = dir
		if err := m.conf.Save(); err != nil {
			m.errMsg = "saving config: " + err.Error()
			return m, nil
		}
		if m.target != nil {
			m.target.dir = dir
		}
		m.rebuild()
		m.mode = modeCourses
		m.status = "course folders sync under " + dir
		return m, nil
	}

	if value != m.mapWas {
		if value != "" && sanitizeCoursePath(value) == "" {
			m.errMsg = fmt.Sprintf("%q is not a usable folder (relative to %s, and no '..')", value, m.dir())
			return m, nil
		}
		prefs := m.conf.CoursePrefs(m.mapFor)
		if value == "" || value == sanitizeCoursePath(m.mapName) {
			prefs.Path = nil // the default: the WeBeep course name
		} else {
			p := sanitizeCoursePath(value)
			prefs.Path = &p
		}
		m.conf.SetCoursePrefs(m.mapFor, prefs)
		if err := m.conf.Save(); err != nil {
			m.errMsg = "saving config: " + err.Error()
			return m, nil
		}
		m.rebuild()
		for i, row := range m.rows {
			if row.ID == m.mapFor {
				m.cursor = i
				break
			}
		}
		if value == "" {
			m.status = m.mapName + ": using the WeBeep course name again"
		} else {
			m.status = m.mapName + " syncs to " + sanitizeCoursePath(value)
		}
	}
	m.mode = modeCourses
	return m, nil
}

func (m *uiModel) toggleCourse(id int64) {
	prefs := m.conf.CoursePrefs(id)
	on := prefs.Sync == nil || *prefs.Sync
	off := !on
	prefs.Sync = &off
	m.conf.SetCoursePrefs(id, prefs)
	m.rebuild()
	if err := m.conf.Save(); err != nil {
		m.errMsg = "saving config: " + err.Error()
	}
}

func (m *uiModel) move(delta int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	visible := m.visibleRows()
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+visible {
		m.scroll = m.cursor - visible + 1
	}
}

func (m *uiModel) quit() {
	m.stopRun()
	if m.mode == modeSync && m.total > 0 {
		m.status = fmt.Sprintf("stopped after %d/%d files: what already finished stays downloaded",
			minInt(m.done, m.total), m.total)
	}
	m.waitForWorkers()
}

// stopRun cancels a download that is running without leaving the UI: whatever
// already finished stays downloaded.
func (m *uiModel) stopRun() {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
}

// waitForWorkers lets the download goroutine finish stopping before the process
// exits, so a partial file is dropped instead of left in your folders.
func (m *uiModel) waitForWorkers() {
	if m.events == nil {
		return
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, open := <-m.events:
			if !open {
				return // the run is over, and so are its temporary files
			}
		case <-deadline:
			return
		}
	}
}

// --- view --------------------------------------------------------------------

func (m *uiModel) visibleRows() int {
	n := m.height - 14
	if n < 4 {
		n = 4
	}
	return n
}

func (m *uiModel) View() string {
	if m.width == 0 {
		return "starting..."
	}
	var b strings.Builder
	b.WriteString(uiTitle.Render("WeBeep Sync") + "  " +
		uiDim.Render("map each WeBeep course to a folder, and sync without touching your own files"))
	b.WriteString("\n")
	if m.target != nil {
		selected := 0
		for _, r := range m.rows {
			if r.Sync {
				selected++
			}
		}
		b.WriteString(uiDim.Render("account ") + m.target.user + "  " +
			uiDim.Render("folder ") + m.dir() + "  " +
			uiDim.Render("syncing ") + fmt.Sprintf("%d/%d courses", selected, len(m.rows)))
		b.WriteString("\n")
	}

	switch m.mode {
	case modeLoading:
		b.WriteString("\n" + m.spinner.View() + " asking WeBeep for your courses...")
	case modeMapping:
		b.WriteString(m.mappingView())
	case modeDir:
		b.WriteString(m.dirView())
	case modePreview:
		b.WriteString(m.planView())
	case modeSync:
		b.WriteString(m.syncView())
	default:
		b.WriteString(m.coursesView())
	}

	b.WriteString("\n\n" + m.footer())
	for _, note := range m.notes {
		b.WriteString("\n" + uiDim.Render(note))
	}
	// The sync panel already prints the file it is on, so do not repeat it.
	switch {
	case m.errMsg != "":
		b.WriteString("\n" + uiErr.Render(m.errMsg))
	case m.status != "" && m.mode != modeSync:
		b.WriteString("\n" + uiDim.Render(m.status))
	}
	return b.String()
}

func (m *uiModel) coursesView() string {
	if len(m.rows) == 0 {
		return "\nno courses to show (press r to refresh, or log in with `webeep-sync-cli login`)"
	}
	visible := m.visibleRows()
	if m.cursor >= m.scroll+visible {
		m.scroll = m.cursor - visible + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
	dirWidth := 28
	for _, r := range m.rows {
		if n := len(r.Folder); n > dirWidth {
			dirWidth = n
		}
	}
	dirWidth = minInt(dirWidth, maxInt(m.width-56, 12))
	counts := m.newFileCounts()

	var b strings.Builder
	header := fmt.Sprintf("%-7s  %-*s  %s", "ID", dirWidth, "LOCAL FOLDER", "WE BEEP COURSE")
	if counts != nil {
		header += "  NEW"
	}
	b.WriteString("\n  " + uiDim.Render(header))
	b.WriteString("\n")
	for i := m.scroll; i < len(m.rows) && i < m.scroll+visible; i++ {
		r := m.rows[i]
		cursor := "  "
		if i == m.cursor {
			cursor = "▸ "
		}
		box := "[ ]"
		if r.Sync {
			box = uiChecked.Render("[x]")
		}
		folder := truncate(r.Folder, dirWidth)
		if i == m.cursor {
			folder = uiCursor.Render(folder)
		}
		name := shortCourseName(r.DisplayName)
		if name == "" || name == r.Folder {
			name = "-"
		}
		tail := ""
		if counts != nil {
			if n := counts[r.ID]; n > 0 {
				tail = fmt.Sprintf("  %s", uiChecked.Render(fmt.Sprintf("%d", n)))
			} else {
				tail = uiDim.Render("  0")
			}
		}
		b.WriteString(fmt.Sprintf("%s%s %-7s  %-*s  %s%s\n", cursor, box, strconv.FormatInt(r.ID, 10),
			dirWidth, folder, uiDim.Render(truncate(name, maxInt(m.width-dirWidth-32, 8))), tail))
	}
	for _, w := range m.warn {
		b.WriteString(uiWarn.Render("note: "+w) + "\n")
	}
	if counts == nil {
		b.WriteString(uiDim.Render("press p to see how many files each course would download"))
	}
	return b.String()
}

// newFileCounts counts, per course, how many files the cached plan would
// download. It is what makes "is this old instance worth syncing?" answerable.
func (m *uiModel) newFileCounts() map[int64]int {
	if len(m.planCache) == 0 {
		return nil
	}
	counts := make(map[int64]int)
	for _, a := range m.planCache {
		if a.Kind.Downloads() {
			counts[a.Entry.CourseID]++
		}
	}
	return counts
}

// shortCourseName drops the "095274 - " id prefix for display only.
func shortCourseName(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, " - "); i > 0 && isAllDigits(s[:i]) {
		s = strings.TrimSpace(s[i+3:])
	}
	return s
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (m *uiModel) mappingView() string {
	var b strings.Builder
	b.WriteString("\nMap " + uiCursor.Render(m.mapName) + " to a folder under " + m.dir() + "\n")
	b.WriteString(uiDim.Render("Relative to that root, so a semester folder works: Y1S1/Biochip. Empty means the WeBeep course name.") + "\n\n")
	b.WriteString(m.input.View() + "\n")
	if len(m.suggest) > 0 {
		b.WriteString(uiDim.Render("folders that already exist (Tab fills the next one):") + "\n")
		limit := minInt(len(m.suggest), maxInt(m.height-18, 3))
		for _, s := range m.suggest[:limit] {
			b.WriteString("  " + s + "\n")
		}
		if left := len(m.suggest) - limit; left > 0 {
			b.WriteString(uiDim.Render(fmt.Sprintf("  ... %d more", left)) + "\n")
		}
	}
	return b.String()
}

func (m *uiModel) dirView() string {
	return "\nWhere should your course folders live?\n" +
		uiDim.Render("An absolute path, for example /home/you/School/PoliMi. Your semester and course folders go inside it.") +
		"\n\n" + m.input.View() + "\n"
}

func (m *uiModel) planView() string {
	selected := m.selectedPlan()
	counts := countActions(selected)
	unselected := len(m.planCache) - len(selected)
	toDownload := counts[syncpkg.ActionNew] + counts[syncpkg.ActionUpdate]
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\nplan for %s\n", m.dir()))
	b.WriteString(uiDim.Render(fmt.Sprintf("%d to download, %d already up to date, %d adopted, %d protected, %d no longer on WeBeep\n",
		toDownload, counts[syncpkg.ActionUnchanged], counts[syncpkg.ActionAdopt],
		counts[syncpkg.ActionProtected]+counts[syncpkg.ActionProtectedChanged], counts[syncpkg.ActionSkipMissing])))
	if unselected > 0 {
		b.WriteString(uiDim.Render(fmt.Sprintf("%d file(s) belong to courses you deselected: they stay where they are\n", unselected)))
	}

	shown, maxShown := 0, maxInt(m.height-24, 3)
	for _, a := range selected {
		if !a.Kind.Downloads() {
			continue
		}
		if shown >= maxShown {
			b.WriteString(uiDim.Render(fmt.Sprintf("  ... %d more to download\n", toDownload-shown)))
			break
		}
		b.WriteString(fmt.Sprintf("  %s  %s  %s\n", uiDim.Render(a.Kind.Label()), a.Entry.RelPath, uiDim.Render(fsx.HumanBytes(a.Entry.Size))))
		shown++
	}
	if n := counts[syncpkg.ActionProtectedChanged]; n > 0 {
		b.WriteString(uiWarn.Render(fmt.Sprintf("%d file(s) you edited have newer versions on WeBeep: yours stay unless you press c", n)) + "\n")
	}
	return b.String()
}

func (m *uiModel) syncView() string {
	width := maxInt(m.width-22, 10)
	pct := 0.0
	if m.total > 0 {
		pct = float64(minInt(m.done, m.total)) / float64(m.total)
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("\nsyncing  %d/%d files  %3.0f%%\n", minInt(m.done, m.total), m.total, pct*100))
	b.WriteString("  " + progressBar(m.done, m.total, width) + "\n")
	b.WriteString("  " + uiDim.Render(truncate(m.status, m.width-4)))
	return b.String()
}

func progressBar(done, total, width int) string {
	if total <= 0 {
		total = 1
	}
	done = minInt(maxInt(done, 0), total)
	filled := width * done / total
	filled = minInt(maxInt(filled, 0), width)
	bar := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render(strings.Repeat("█", filled))
	return bar + uiDim.Render(strings.Repeat("░", width-filled))
}

func (m *uiModel) footer() string {
	clobberHelp := keyHelp("c", "files you edited: "+map[bool]string{true: "replace them", false: "keep them"}[m.clobber])
	switch m.mode {
	case modeMapping:
		return keyHelp("enter", "save") + "  " + keyHelp("tab", "take a suggestion") + "  " + keyHelp("esc", "cancel")
	case modeDir:
		return keyHelp("enter", "save") + "  " + keyHelp("esc", "cancel")
	case modePreview:
		return keyHelp("s", "download now") + "  " + clobberHelp + "  " + keyHelp("esc", "back")
	case modeSync:
		return keyHelp("esc", "stop downloading (what finished stays downloaded)")
	default:
		return keyHelp("space", "sync this course") + "  " + keyHelp("m", "map its folder") + "  " +
			keyHelp("d", "change root folder") + "  " + keyHelp("p", "preview") + "  " +
			keyHelp("s", "sync") + "  " + clobberHelp + "  " + keyHelp("r", "refresh") + "  " +
			keyHelp("j/k", "move") + "  " + keyHelp("q", "quit")
	}
}

func keyHelp(key, what string) string {
	return uiKey.Render(key) + uiDim.Render(" "+what)
}

var (
	uiTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	uiDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	uiCursor  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	uiChecked = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	uiWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	uiErr     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
	uiKey     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("255"))
)

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
