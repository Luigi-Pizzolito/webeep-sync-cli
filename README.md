# webeep-sync-cli

Keeps the files of your WeBeep (webeep.polimi.it) courses in per-course folders
on your own disk. One static Go binary, rsync-style rules: WeBeep may add and
update its files, and nothing WeBeep does not own is rewritten or deleted.

The terminal UI lists every course you are enrolled in, lets you map each one to
a folder such as `Y1S1/Signal Recovery`, shows what a sync would download, and
downloads after you say so.

It follows the file-layout rules of
[toto04/webeep-sync](https://github.com/toto04/webeep-sync), the Electron app
this is a Go reimplementation of.

## Where the two differ

The original app compares your local mtime against WeBeep's, so a PDF you
annotated counts as stale and comes back over your notes. That is
[#104](https://github.com/toto04/webeep-sync/issues/104) in that repository,
opened 3 October 2023 and still open. This tool never reads your mtime. It stores
the last known **remote** state, size and `timemodified`, in a manifest beside
your files, and compares against that:

| Situation | What happens |
|---|---|
| New file on WeBeep | Downloaded. |
| File unchanged on WeBeep | Left alone, even if you edited it locally. |
| File changed on WeBeep, new size or `timemodified` | Downloaded again and put in its place. `--no-overwrite` keeps yours instead. |
| Your own file sitting at a path WeBeep also has | Recorded as protected and never overwritten. `--clobber` opts in. |
| Local file that WeBeep no longer has | Left where it is. Nothing is ever deleted. |
| File answers 404 | Recorded as `missing` and not requested again until its remote state changes. |

A download lands in a hidden `.webeep-part-*` file and is renamed into place.
The file's timestamp comes from the remote `timemodified`, which is what
`rsync -a` does. `esc` or Ctrl-C drops the half-written file and keeps every
finished one.

## Requirements

Go 1.24.2 or newer, the version `go.mod` asks for, because the terminal UI
dependencies need it. Linux and macOS run the test suite locally and in CI.
Windows binaries compile and pass `GOOS=windows go vet` in CI, and the commands
run under Wine, but nobody has used the UI on a Windows console, so Windows is
untested in the part that matters.

## Quickstart

```bash
git clone https://github.com/Luigi-Pizzolito/webeep-sync-cli
cd webeep-sync-cli
go build -o webeep-sync-cli .

./webeep-sync-cli doctor      # is WeBeep and its API reachable
./webeep-sync-cli login       # obtain a web service token, see Login below
./webeep-sync-cli ui          # map folders, preview, sync
```

The first run asks where your course folders should live and saves the answer to
config. Every later `sync` uses it, cron jobs included. A run with no terminal in
front of it falls back to a `WeBeep` folder in your home directory until you set
one with `dir`. A path you type may start with `~` or `~\`, and the tool expands
it.

Plain command line instead of the UI:

```bash
./webeep-sync-cli dir ~/School/PoliMi
./webeep-sync-cli courses                              # ids and folders for every course
./webeep-sync-cli course path 18578 "Y1S1/RF Circuit Design"
./webeep-sync-cli sync --dry-run                       # changes nothing
./webeep-sync-cli sync
```

<details>
<summary>Arch Linux: install it as a package</summary>

`packaging/arch/PKGBUILD` builds the package from the checkout it lives in:

```bash
git clone https://github.com/Luigi-Pizzolito/webeep-sync-cli
cd webeep-sync-cli/packaging/arch
makepkg -si --needed          # builds, runs the test suite, installs with pacman
```

It packages the HEAD commit of your clone, so nothing is fetched from GitHub and
the private repository needs no credentials. `go` and `base-devel` are needed, and
`-s` installs them when they are missing.

What lands on disk:

| Path | What it is |
|---|---|
| `/usr/bin/webeep-sync-cli` | the command, so `webeep-sync-cli ui` works from anywhere |
| `/usr/share/licenses/webeep-sync-cli/LICENSE` | the GPL-3.0 text |
| `/usr/share/doc/webeep-sync-cli/README.md` | this file |

The package version follows the newest tag in your clone. A clone of `v0.1`
produces `webeep-sync-cli-0.1-1-x86_64.pkg.tar.zst` and a binary that reports
`webeep-sync-cli v0.1`. Build without installing with `makepkg -sf --needed` and
run `sudo pacman -U ./webeep-sync-cli-0.1-1-x86_64.pkg.tar.zst` afterwards. Clear
the build directories with `makepkg -C` or `rm -rf src pkg`.

A tagged release carries a prebuilt Arch package plus binaries for Linux, macOS
and Windows, so you can download the package and `sudo pacman -U` it instead of
building.

</details>

Sample `doctor` output, with your name and dates in place of these:

```
ok    webeep page reachable (HTTP 200)
ok    REST API answers and rejects bad tokens as expected
ok    stored token valid (user: Mario Rossi, obtained 2026-11-17T09:00:00Z)
      config dir: ~/.config/webeep-sync-cli
```

## Login

WeBeep logs students in through AUA or SPID in a browser, and this tool does not
try to reproduce that. It needs one Moodle web service token. Three ways to get
it:

**A. Read the browser redirect (works whatever login method you used)**

1. Log in at https://webeep.polimi.it in your browser.
2. Open DevTools with F12, go to the Network tab, then visit
   `https://webeep.polimi.it/admin/tool/mobile/launch.php?service=moodle_mobile_app&passport=1234`
3. The answer is a 302 or 303 whose `Location` header starts with
   `moodlemobile://token=...`. Copy the value after `token=`, a long base64
   string. A browser that refuses the external protocol still shows that URL in
   the Network tab.
4. Paste it into `webeep-sync-cli login`, or pass it with `--token` or `--url`.

**B. Hand over a session cookie**

`login --cookie "MoodleSession=..."`, with the cookie value from DevTools >
Application > Cookies. The tool then performs the launch.php exchange itself.

**C. Username and password**

`login --username mrossi` takes the password from `WEBEEP_PASSWORD` or prompts
for it, then calls Moodle's `login/token.php`. This works only while WeBeep
accepts direct password logins for your account. A SPID-only account needs
method A.

The token goes to `~/.config/webeep-sync-cli/token.json` with mode 0600. It
reads like a password and does what one does: it grants read access to everything
your account can see on WeBeep. `logout` deletes it. `login` and `doctor` also
take `--base URL`, default `https://webeep.polimi.it`.

## Commands

| Command | Purpose |
|---|---|
| `ui [--dir PATH]` | Map folders, preview, sync. |
| `login` | Obtain and validate a token (`--token`, `--url`, `--cookie`, `--username`). |
| `logout` | Delete the stored token. |
| `whoami` | Show which account the token belongs to. |
| `dir [PATH \| --clear]` | Show or set the folder all course folders live under. |
| `courses` | List enrolled courses with id, folder and sync state. |
| `course enable/disable <id>` | Include or exclude one course. |
| `course path <id> "SEM/Course"` | Map a course to a folder. |
| `course path <id> ""` | Drop the mapping and use the WeBeep course name. |
| `sync` | Download new and changed files. |
| `doctor` | Check WeBeep reachability, the REST API and the stored token. |

Take the numeric id from `courses` (`18578`) or the id inside the WeBeep full
name. Quote the folder: `course path 18578 "Y1S1/RF Circuit Design"`.

## The UI

```
WeBeep Sync  map each WeBeep course to a folder, and sync without touching your own files
account LUIGI …  folder /home/luigi/School/PoliMi  syncing 9/12 courses

  ID      LOCAL FOLDER                  WE BEEP COURSE                       NEW
▸ [x] 18578    Y1S1/RF Circuit Design      RF CIRCUIT DESIGN (LEVANTINO …     16
  [x] 21019    Y1S1/Signal Recovery        SIGNAL RECOVERY (COMOTTI …)         9
  [ ] 16110    RF CIRCUIT DESIGN           RF CIRCUIT DESIGN (LEVANTINO …     44

space sync this course  m map its folder  d change root folder  p preview  s sync
c files you edited: keep them  r refresh  j/k move  q quit
```

Sample rows from a real session layout, not your course list.

- `space`: include or exclude this course. Saved immediately.
- `p`: preview the plan, and fill the `NEW` column with the number of files each
  course would download, deselected ones included. A `0` means WeBeep has nothing
  new for that course, which is how you decide whether an old instance of it is
  worth syncing. The preview also names the folders inside your course folders
  that WeBeep no longer has.
- `m`: map it to a folder. Folders already under your root are listed and `Tab`
  completes them. Type any other name, or nothing to use the WeBeep course name.
- `d`: change the root folder, as an absolute path such as `~/School/PoliMi`.
- `s`: sync the selected courses, one progress bar per file. `esc` stops the
  download: the partial file is discarded, everything finished stays on disk and
  in the manifest. Quitting mid-run does the same.
- `c`: files you edited stay yours by default. `c` flips that and lets WeBeep
  replace them, and the preview says how many files that touches.
- `r`: reload the course list from WeBeep.

`ui` takes `--dir PATH` and `--workers N`. `--dir` applies to that run only and is
never saved, so `ui --dir ~/tmp/webeep-rehearsal` runs the whole flow against an
empty scratch folder before you point it at the real one.

## Course folders and mappings

Mappings are relative to the root folder you set with `dir`, and they nest, so a
layout by semester works:

```
PoliMi/                  <- the root
├── Y1S1/
│   ├── RF Circuit Design/              <- mapped from RF CIRCUIT DESIGN [2025-26]
│   └── Signal Recovery/
└── Y1S2/
    └── Analog Circuit Design/
```

What keeps your files safe:

- Absolute paths and `..` are refused, so a mapping cannot escape the root.
- Two courses cannot end up in one folder. If you map two of them to the same
  place, the tool prints a warning and puts the second one in its full WeBeep
  name instead, because two courses writing one folder would overwrite each
  other's files.
- Inside a course folder the layout mirrors WeBeep's section and module names as
  they are today. When WeBeep renames one, the files land in the new folder and
  the old one stays where it is, so `sync` lists the folders WeBeep no longer
  has. See `--list-leftovers` and Troubleshooting.
- A file in one of those folders that WeBeep never downloaded is recorded as
  protected: never overwritten, never deleted.

Flags for `sync`, and `ui` takes `--dir` and `--workers` as well:

| Flag | Effect |
|---|---|
| `--dir PATH` | Download folder. Default is `download_dir` from config, else a `WeBeep` folder in your home directory. For `ui` it applies to that run only and is never saved. |
| `--dry-run` | Print the plan, change nothing. |
| `--course TEXT` | Only courses whose name contains TEXT. Repeatable. |
| `--all` | Sync every course, including disabled ones. |
| `--no-overwrite` | Never replace an existing local file, even when WeBeep changed its copy. |
| `--clobber` | Allow overwriting files recorded as local-only. |
| `--workers N` | Parallel downloads. Default 3. |
| `--quiet` | Print only the final summary. |
| `--list-leftovers` | List every folder inside your course folders that WeBeep does not have. Without it, `sync` names only the folders holding material WeBeep now keeps under another name, and counts the rest. |
| `--exclude-mod NAME` | Also skip a module type, for example `h5p`. Repeatable. |

## Files the tool writes

| File | Contents |
|---|---|
| `~/.config/webeep-sync-cli/config.json` | Root download folder, worker count, module exclusions, and per-course `sync` switch and folder `path`. |
| `~/.config/webeep-sync-cli/token.json` | The web service token, mode 0600. |
| `<download_dir>/.webeep-sync-cli.json` | Manifest: last known remote size and `timemodified` for every synced file, plus the `protected` and `missing` marks. |
| `<download_dir>/**/.webeep-part-*` | The hidden file a download is written into before it is renamed. It is gone once the download finishes, fails, or is stopped with `esc`. A run killed outright can leave one behind, and the next `sync` removes it and says how many it found. |

Deleting the manifest is safe. The next run adopts local files whose size and
timestamp already match the remote copy instead of re-downloading them, and
records the mismatched ones as protected.

If the download folder is a Syncthing share, add `.webeep-part-*` to its
`.stignore` so a half-written download never travels to your other machines.
Patterns in `.stignore` are relative to the share root, and the tool's download
folder is that root.

## Running it on a schedule

Cron, after `doctor` says the site and the token are fine. A token stays valid
until WeBeep revokes it:

```
0 8-20/4 * * * cd ~/WeBeep && /usr/local/bin/webeep-sync-cli sync --quiet >> ~/.webeep-sync.log 2>&1
```

A systemd timer calling `webeep-sync-cli sync --quiet` every few hours does the
same job without a cron entry.

## Tests

```bash
go test -race ./...   # unit tests and httptest servers, no real credentials
go vet ./...
gofmt -l .            # prints nothing
```

Covered: token parsing from all three login formats, the layout builder (module
exclusions, flattening a "Materiali" section, single-file resources, duplicate
names), each row of the sync rules table, atomic downloads, 404 caching, retries,
mtime handling, cleanup after an interrupted run, and the report of folders WeBeep
no longer has. `doctor` is the only command that reaches WeBeep without a credential.

## Releases and CI

```bash
git tag v0.2 && git push origin v0.2
```

No commit triggers a workflow. `ci.yml` runs when you start it:

```bash
gh workflow run ci.yml              # the default branch
gh workflow run ci.yml --ref <sha>  # a specific commit
```

That workflow runs `go vet`, `gofmt` and `go test -race` on ubuntu-latest and
macos-latest, plus a `GOOS=windows` and `GOOS=darwin` vet so the targets that
have no test coverage keep compiling. It is manual because a private repository
bills macOS minutes at 10x. `release.yml` runs on a `v*` tag and starts with the
same checks, so a tag is tested whether or not you ran `ci.yml` by hand. It then
builds the Arch package with `makepkg` inside an Arch Linux container, cross
builds static binaries for `linux/amd64`, `linux/arm64`, `darwin/amd64`,
`darwin/arm64`, `windows/amd64` and `windows/arm64`, and publishes all of them
with a `SHA256SUMS` file as a GitHub Release named after the tag. `go build`
stamps the tag name into the binary.

Tarballs for the Unix targets, zips holding `webeep-sync-cli.exe` for Windows.
Every archive carries the binary beside `LICENSE` and `README.md`. The macOS
binaries are neither signed nor notarized, so the first run needs the quarantine
attribute cleared:

```bash
xattr -d com.apple.quarantine webeep-sync-cli
```

## Troubleshooting

**`No permission to create web service token for the service moodle_mobile_app`**
WeBeep switched mobile token creation off for part of the student body starting
30 March 2026, the same outage tracked as
[#139](https://github.com/toto04/webeep-sync/issues/139) in the original
repository. A student reported on 7 April 2026 that WeBeep support had answered
that the service was disabled temporarily for non-compliant use, and two people
in that thread reported token creation working again on 16 May 2026. If your
account is affected, `doctor` shows the site reachable and the API alive while
`login` fails, which is how you can tell a blocked service from an outage. There
is no client-side fix for a disabled service.

**`the saved token was rejected`** It expired or was revoked. Run `login` again.

**A file you annotated carries `changed on WeBeep but is protected`** Your copy
was never downloaded from WeBeep, so it is yours permanently. If you want the
WeBeep version, delete your file or run `sync --clobber`.

**A folder you filled earlier looks half empty**
WeBeep renames its sections and folders, and professors rename them partway through
the year: a `Lesson Slides` folder under one section turns up as `LESSON slides`
under another. The tool writes to the names WeBeep uses now and leaves the old folder
where it is, so the course ends up in two places and the older one stops growing.
`sync` names those folders and says how many of their file names WeBeep now has
somewhere else. Sample output:

```
note: 1 folder(s) hold material WeBeep now keeps under a different name. Nothing is deleted or overwritten:
  Analisi I/Lesson Slides  11 file(s), 10 of those names are on WeBeep as Introduzione/LESSON slides
note: 2 other folder(s) in your course folders are not on WeBeep at all: yours, and left alone (use --list-leftovers to see them)
```

The copy inside the folder WeBeep currently has is the one the tool keeps up to date.
The old folder is yours to keep or delete, and a file WeBeep dropped entirely only
survives there. `sync --list-leftovers` lists every folder WeBeep does not have,
which is the list to work through when you want to tidy up.

**Old files pile up** Nothing is deleted, by design. When a professor removes a
file your copy stays, and pruning it is your job.

## License and credits

GPL-3.0, the same license as
[toto04/webeep-sync](https://github.com/toto04/webeep-sync), whose sections,
modules and folder naming this tool copies. WeBeep is a Politecnico di Milano
service: read its current terms before automating access, and use this at your
own risk.
