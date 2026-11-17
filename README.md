# webeep-sync-cli

A Go command-line tool that keeps all the files of your WeBeep
(webeep.polimi.it) courses in a local folder. It is a rework of
[toto04/webeep-sync](https://github.com/toto04/webeep-sync) (Electron app,
mostly unmaintained) as a single static binary with rsync-style behavior:
files are added and updated from WeBeep, and your own local files are never
touched.

## Why a rework

The original app compares your local file's mtime against WeBeep's, so any
file you edited locally (an annotated PDF, a filled exercise) gets silently
re-downloaded and overwritten (their issue #104, open since 2024). Files that
disappeared from the server get requested forever (issue #87). This tool
instead stores the last known **remote** state in a manifest and compares
against that:

| Situation | What happens |
|---|---|
| New file on WeBeep | Downloaded. |
| File unchanged on WeBeep | Left exactly as it is, even if you edited it locally. |
| File updated on WeBeep (new size or `timemodified`) | Re-downloaded, local copy replaced (`--no-overwrite` keeps the local copy instead). |
| Your own file, never from WeBeep, same path | Recorded as protected and never overwritten (`--clobber` opts in). |
| Local files that don't exist on WeBeep | Never deleted. |
| File gone from the server (404) | Remembered, not requested again until its remote state changes. |

Downloads write to a temporary file and are renamed into place, timestamps
are set from the remote `timemodified` (`rsync -a` style), and Ctrl-C stops
without leaving partial files. Interrupted syncs keep everything already
finished.

## Requirements

Go 1.23 or newer to build. Linux and macOS are exercised by the local test
suite; Windows builds are expected to work but are untested until end-to-end
verification is done.

## Quickstart

```bash
git clone https://github.com/Luigi-Pizzolito/webeep-sync-cli
cd webeep-sync-cli
go build -o webeep-sync-cli .

./webeep-sync-cli doctor      # checks that WeBeep and its API are reachable
./webeep-sync-cli login       # obtain a web service token (see Login below)
./webeep-sync-cli courses     # list your enrolled courses and their folders
./webeep-sync-cli sync --dry-run
./webeep-sync-cli sync        # downloads into ~/WeBeep
```

`doctor` output on a working setup:

```
ok    webeep page reachable (HTTP 200)
ok    REST API answers and rejects bad tokens as expected
ok    stored token valid (user: Mario Rossi, obtained 2026-11-17T09:00:00Z)
      config dir: ~/.config/webeep-sync-cli
```

## Login

WeBeep authenticates with AUA or SPID in the browser, so the tool does not
try to reproduce that flow. It needs one Moodle web service token, which you
can get in three ways:

**A. Browser redirect (works with any login method, recommended)**

1. Log in at https://webeep.polimi.it in your browser.
2. Open DevTools (F12) and the Network tab, then visit
   `https://webeep.polimi.it/admin/tool/mobile/launch.php?service=moodle_mobile_app&passport=1234`
3. The server answers with a 302/303 whose `Location` header starts with
   `moodlemobile://token=...`. Copy the `token=` value (a long base64 string).
   A browser that blocks the external protocol still shows that URL.
4. Paste it into `webeep-sync-cli login` (or pass it with `--token` / `--url`).

**B. Session cookie**

With `--cookie "MoodleSession=..."` (copy the cookie value from DevTools ->
Application -> Cookies) the tool performs the launch.php exchange itself.

**C. Username and password**

`login --username mrossi` reads the password from `WEBEEP_PASSWORD` or
prompts for it and calls Moodle's `login/token.php`. This only works while
WeBeep accepts direct password logins for your account; SPID-only accounts
must use method A.

The token is stored in `~/.config/webeep-sync-cli/token.json` with
owner-only permissions (0600). Treat it like a password: it grants read
access to everything your account sees on WeBeep. `logout` removes it.

## Commands

| Command | Purpose |
|---|---|
| `login` | Obtain and validate a token (`--token`, `--url`, `--cookie`, `--username`). |
| `logout` | Delete the stored token. |
| `whoami` | Show the account the token belongs to. |
| `courses` | List enrolled courses, ids, folders and sync state. |
| `course enable/disable <id>` | Include or exclude one course from syncing. |
| `course rename <id> <name>` | Choose the download folder for a course. |
| `sync` | Download new and changed files. |
| `doctor` | Check WeBeep reachability, the REST API and the stored token. |

Useful `sync` flags:

| Flag | Effect |
|---|---|
| `--dir PATH` | Download folder (default `~/WeBeep`, or `download_dir` from config). |
| `--dry-run` | Print the plan, change nothing. |
| `--course TEXT` | Only sync courses whose name contains TEXT (repeatable). |
| `--all` | Sync every course, even disabled ones. |
| `--no-overwrite` | Never replace an existing local file, even when WeBeep changed it. |
| `--clobber` | Allow overwriting even protected (local-only) files. |
| `--workers N` | Parallel downloads (default 3). |
| `--exclude-mod NAME` | Additionally skip a module type (e.g. `h5p`). |

## Files the tool writes

| File | Contents |
|---|---|
| `~/.config/webeep-sync-cli/config.json` | Download dir, worker count, module exclusions, per-course `sync` and folder `name` overrides. |
| `~/.config/webeep-sync-cli/token.json` | The web service token (0600). |
| `<download_dir>/.webeep-sync-cli.json` | Manifest: last known remote state (size, `timemodified`) of every synced file, plus `protected` and `missing` marks. |

Deleting the manifest is safe: on the next run, local files that match the
remote size and timestamp are adopted without re-downloading, and files that
don't match are protected rather than overwritten.

## Run it periodically

With cron (check `doctor` output first; the token stays valid until WeBeep
revokes it):

```
0 8-20/4 * * * cd ~/WeBeep && /usr/local/bin/webeep-sync-cli sync --quiet >> ~/.webeep-sync.log 2>&1
```

Or a systemd timer that runs `webeep-sync-cli sync --quiet` every few hours.

## Development

```bash
go test -race ./...   # unit and httptest integration tests, no real credentials
go vet ./...
gofmt -l .            # must print nothing
```

The test suite covers token parsing from all three login formats, the file
layout builder (module exclusions, "Materiali" flattening, single-file
resources, duplicate names), every sync decision from the table above,
atomic downloads, 404 caching, retries, mtime handling and interrupt
cleanup. `doctor` is the one command that talks to WeBeep without
credentials.

## Troubleshooting

**`No permission to create web service token for the service moodle_mobile_app`**
WeBeep disabled mobile-token creation for parts of the student body between
March and May 2026 (original repo issue #139; it was restored on 2026-05-16).
If it happens again, `doctor` will confirm the site is up while tokens are
blocked. Wait for the policy to be lifted or contact WeBeep support; there is
no client-side fix for a disabled service.

**`the saved token was rejected`** The token expired or was revoked. Run
`login` again.

**A file you annotated keeps a note `changed on WeBeep but is protected`**
A local file with the same name was never downloaded from WeBeep, so it is
kept forever. If you actually want the WeBeep version, delete the local file
or run `sync --clobber`.

**Old files pile up** Nothing is ever deleted by design. If a professor
removes a file, your copy stays; prune manually if you want.

## Credits and license

Design and file-layout rules are based on
[toto04/webeep-sync](https://github.com/toto04/webeep-sync), a GPLv3 project;
this is an independent reimplementation in Go, also GPLv3. WeBeep is a Politecnico
di Milano service: check its current terms before automating access, and use
this tool at your own risk.
