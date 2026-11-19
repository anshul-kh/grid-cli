# grid

`grid` is the companion CLI for [Grid](https://github.com/anshul-kh) lessons. It scaffolds
lesson workspaces, measures your checks locally, and submits evidence to the Grid server for a
verdict.

**`grid` measures. The server judges.** `grid check` can only ever report what it measured —
`ok`, `error`, or `skipped` — never pass or fail. Only `grid submit`'s server-returned verdict
(`PASSED` / `FAILED` / `INVALID`) is ever rendered as a judgement.

## Install

### Prebuilt binary (recommended)

macOS or Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/anshul-kh/grid-cli/main/install.sh | sh
```

Fedora / RHEL / Rocky / Alma:

```bash
# system-wide package (also installs shell completions)
curl -fsSL https://raw.githubusercontent.com/anshul-kh/grid-cli/main/install.sh \
  | sudo GRID_INSTALL_METHOD=rpm sh
# or download the RPM from the release page and let dnf handle it
sudo dnf install ./grid-cli_<version>_linux_x86_64.rpm
```

Windows (PowerShell):

```powershell
irm https://raw.githubusercontent.com/anshul-kh/grid-cli/main/install.ps1 | iex
```

Both scripts download the release archive for your OS/arch from
[GitHub Releases](https://github.com/anshul-kh/grid-cli/releases) (built by
`.goreleaser.yaml`: `linux`/`darwin`/`windows` × `amd64`/`arm64`, plus a
Fedora/RHEL `.rpm`), **verify its checksum against the release's
`checksums.txt` before installing anything**, and never prompt for or invoke
root/sudo (or a Windows UAC elevation) — if neither a writable system directory
nor a per-user one is available, they fail with instructions instead of
escalating silently. `install.sh` prefers `/usr/local/bin` if it's writable,
falling back to `~/.local/bin` (printing a `PATH` hint if needed); `install.ps1`
prefers `%ProgramFiles%\grid-cli`, falling back to `%LOCALAPPDATA%\grid-cli`
(added to your user `PATH` automatically). Pin a version with
`GRID_INSTALL_VERSION=v1.2.3` / `$env:GRID_INSTALL_VERSION = "v1.2.3"`.

If a release is missing the archive for your platform, `auto` (the default)
falls back to cloning that tag and building with `go build` — install `git` and
Go 1.27+ first (`sudo dnf install -y golang git` on Fedora,
`sudo apt-get install -y golang-go git` on Debian/Ubuntu). Force a path with
`GRID_INSTALL_METHOD=archive|source|rpm` (`$env:GRID_INSTALL_METHOD` on
Windows, where `auto|archive|source` apply).

### Go toolchain

```bash
go install github.com/anshul-kh/grid-cli@latest
```

`go install` names the binary after the module path's last element, so this places
**`grid-cli`** (`grid-cli.exe` on Windows, not `grid`) in `$(go env GOPATH)/bin` — make sure
that directory is on your `PATH`, and rename/symlink it to `grid` if you want the short name
the rest of this README uses.

### From a local clone (recommended for source installs)

```bash
git clone https://github.com/anshul-kh/grid-cli
cd grid-cli
make install
```

`make install` builds `grid`, installs it, and puts its directory on your `PATH` — then tells
you to `source` your shell config (or open a new terminal) so the change takes effect in the
session you're already in.

Unlike `go install`, it names the binary **`grid`**, and it stamps the build with the same
version/commit/date metadata a released binary carries, so `grid --version` reports real
provenance instead of `dev`.

Where it installs, in order of precedence — matching `install.sh`, so the two never disagree:

| | |
|---|---|
| `GRID_INSTALL_DIR` | if you set it |
| `/usr/local/bin` | if it's writable by you |
| `~/.local/bin` | otherwise |

**It never runs `sudo`.** If you want `/usr/local/bin` and it's root-owned, either take
ownership yourself or pick a different directory:

```bash
make install GRID_INSTALL_DIR=$HOME/bin
```

The `PATH` edit is a single marked block, added at most once no matter how many times you
run it. `make uninstall` removes both the binary and that block, leaving the rest of your
shell config untouched. `make doctor` shows where `grid` resolves from if something looks off.

Run `make` on its own to see every target (`build`, `test`, `test-race`, `lint`, `fmt`,
`tidy`, `clean`, `snapshot`).

Just want the binary without touching your `PATH`:

```bash
make build              # produces ./grid in the repo root
```

### Verify the install

```bash
grid --version
```

## Quickstart

```bash
grid login                                  # device-code pairing with the Grid server
grid run <lessonId>                         # scaffold the lesson workspace
cd <the directory grid run just printed>
grid check                                  # measure locally — no submission, no verdict
grid submit                                 # sign your evidence and submit it for a verdict
```

`grid run` prints the lesson's task list and the exact next commands to type, including the
`cd` into the scaffolded workspace (when the lesson scaffolds one at all — see "Workspace modes"
below). `grid check` is safe to run as many times as you like while you work — it never talks to
the server. `grid submit` is what actually earns XP and renders a verdict.

## Workspace modes

`grid run` creates exactly what the lesson declared — and nothing more. The lesson's
`workspace.mode`, not a flag, decides which:

| Mode | What `grid run` creates | Typical lesson |
|---|---|---|
| `none` | Nothing in the current directory at all — no folder, no git. The attempt lives in your CLI config directory instead, keyed by lesson id, so `grid check`/`grid status`/`grid submit`/`grid open` still find it from anywhere. | "Is Docker installed?" |
| `folder` | A plain directory (today's default when a lesson doesn't declare a mode). | Most lessons. |
| `repo` | A directory, plus `git init` inside it. | Anything with a `git.*` check. |
| `bundle` | A directory, git, and (soon) a starter archive unpacked into it. Until that download lands, `grid run` falls back to the same directory-plus-git treatment as `repo`, and says so. | Scaffolded project lessons. |

If `git` isn't installed, `repo`/`bundle` modes warn on stderr and continue without it — the
scaffold still completes, and any `git.*` checks will then honestly report that they couldn't
measure anything, rather than `grid run` failing the whole lesson over an optional tool.

For a `none`-mode lesson, `cd` into anything printed by `grid run` is unnecessary — `grid check`
and `grid submit` work from wherever you already are.

## Command reference

| Command | What it does |
|---|---|
| `grid login` | Device-code pairing: prints a pairing code and URL, opens your browser if it can, and polls until you approve it. Stores credentials in your config directory (`0600` on Unix). |
| `grid logout` | Revokes the session on the server and deletes your local credentials. Succeeds locally even if the server can't be reached. |
| `grid whoami` | Prints the logged-in account: username, display name, rank, XP, streak. |
| `grid run <lessonId>` | Fetches the lesson's attempt and check-spec, and scaffolds its workspace per the lesson's declared mode (see "Workspace modes" above), then writes `.grid/attempt.json` (or, for `mode: none`, the equivalent record in your CLI config directory). Refuses to overwrite a non-empty directory unless you pass `--force`. `--here` scaffolds into the current directory instead of creating a new one (ignored for `mode: none`, which never scaffolds a directory). |
| `grid check` | Runs the lesson's checks locally and prints a measurement table (`ok` / `error` / `skipped`) with durations. **Never submits anything.** Exits non-zero only if a measurement itself could not be taken. |
| `grid submit` | Runs the checks, signs the evidence, and submits it. Renders the server's verdict, per-check results with messages/hints for anything that didn't pass, your XP delta, rank, streak, and any new badges. |
| `grid status` | Shows the current workspace's local state — the attempt's expiry countdown, the last `grid check` results, the last `grid submit` verdict. Never touches the network. `--refresh` just re-reads that same local state against the current clock (there is no server endpoint to refresh an attempt against). |
| `grid open` | Opens the current lesson's URL in your browser. `--print` just prints the URL instead. Never fails when a browser can't be launched — it always prints the URL as a fallback. |
| `grid courses` | Lists your enrolled courses with a progress bar. |
| `grid upgrade` | Checks GitHub releases for a newer version, verifies its checksum, and atomically replaces the running binary. Detects a package-manager install (Homebrew, a root-owned `/usr/local/bin`) and tells you to upgrade that way instead. `--check` only reports what's available, without downloading or replacing anything. |
| `grid version` | Prints the version, commit, build date, OS/arch, and Go version. |

Every command accepts `--help` for its full flag list, and `--json` for a stable,
machine-readable form of its output on stdout (see below).

### Global flags

| Flag | Effect |
|---|---|
| `--json` | Emit machine-readable JSON on stdout instead of the human-formatted output. Diagnostics and progress always go to stderr, so `grid ... --json \| jq` is always safe. |
| `--no-color` | Disable ANSI colour output. |
| `--verbose` | Print extra diagnostics to stderr (e.g. a `shell: true` check warning). |
| `--dir <path>` | Use `<path>` as the working directory instead of the current directory. |
| `--api <url>` | Override the Grid API base URL for this invocation. |
| `--timeout <duration>` | Override the per-request API timeout (e.g. `45s`, `2m`). Default `30s`. |

Colour is also disabled automatically when `NO_COLOR`/`GRID_NO_COLOR` is set, when `TERM=dumb`,
or whenever stdout isn't a terminal (so piping `grid courses | less` never shows escape codes).

## Configuration

Precedence, highest wins: **command-line flag → environment variable → built-in default.**
There is no config file yet.

| Environment variable | Equivalent flag | Default |
|---|---|---|
| `GRID_API_URL` | `--api` | `https://3-111-242-107.nip.io/api/v1` |
| `GRID_WEB_URL` | — | `https://grid-ghost-ebon.vercel.app` (fallback for messages with no server-provided URL) |
| `GRID_NO_COLOR` (or the standard `NO_COLOR`) | `--no-color` | colour enabled (when stdout is a terminal) |
| `GRID_DIR` | `--dir` | the current directory |
| `GRID_TIMEOUT` | `--timeout` | `30s` |

Credentials are stored outside the repo, with mode `0600` on Unix:

| OS | Location |
|---|---|
| macOS | `~/Library/Application Support/grid/credentials.json` |
| Linux | `$XDG_CONFIG_HOME/grid/credentials.json`, or `~/.config/grid/credentials.json` |
| Windows | `%AppData%\grid\credentials.json` |

A `mode: none` lesson's attempt record lives alongside credentials.json, under an `attempts/`
subdirectory of the same config directory, keyed by lesson id — that's what makes it findable
by `grid check`/`grid status`/`grid submit`/`grid open` from any directory.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success (including a `PASSED` verdict from `grid submit`). |
| `1` | Failure — a measurement error (`grid check`), a `FAILED` verdict (`grid submit`), **not enrolled in the lesson's course** (`grid run`/`grid submit` — see below), or any other runtime failure. |
| `2` | Invalid submission — `grid submit` got `INVALID` back from the server (typically a setup/services problem that never reached the point of judging a check, not a judged check failure). |
| `3` | Auth required — you're not logged in, or your session has expired. Run `grid login`. |
| `4` | Network or server error — the Grid server couldn't be reached or returned an unexpected error, the lesson's check-spec failed to parse/validate, or (for `grid run`) the server sent bad workspace data. |
| `5` | Usage error — a bad flag, an unknown command, a non-UUID lesson id (`grid run`), or (for `grid check`/`grid submit`/`grid status`/`grid open`) no open attempt anywhere `grid run` could have left one. |
| `130` | Interrupted — you pressed Ctrl-C while `grid check`/`grid submit`/`grid upgrade` was running; any started services are stopped before exiting. |

Errors are printed to stderr exactly once, in the shape `Error: <what happened>`, and are always
actionable — they say what to run next, not just what went wrong.

## Troubleshooting

**"not logged in — run `grid login` first" (exit 3)**
Every command except `grid check`/`grid status`/`grid open`/`grid version` needs a session. Run
`grid login` and approve the pairing code at the printed URL.

**"session missing or expired — run `grid login` to log in again" (exit 3)**
Your stored session is no longer valid on the server. Log in again; nothing else about your
local workspace is affected.

**"This attempt expired. Run `grid run <id>` again." (exit 4, on `grid submit`)**
Lesson attempts have a time limit. Re-run `grid run <lessonId>` to fetch a fresh attempt, then
`grid check`/`grid submit` again — your workspace files are untouched.

**"You already submitted this attempt." / a `nonce already used` error (exit 1)**
This is Grid's replay protection: each submission consumes a one-time nonce, and the server
rotates a new one into `.grid/attempt.json` after every submit (even a failing one) so the very
next `grid submit` just works. If you see this anyway, your `.grid/attempt.json` is stale —
usually because it was copied from elsewhere, or a prior `grid submit` never got to write the
rotated nonce back (e.g. the process was killed mid-run). Run `grid run <lessonId>` again to get
a fresh attempt.

**A submission fails after a long pause with no explanation**
`grid submit` automatically handles HTTP 413 (evidence over the server's size cap): it
re-truncates the largest evidence fields at half the usual limit and retries once, silently. If
you see a submission error instead, the evidence was still too large after that retry — check
for a check that's capturing unexpectedly large output (e.g. a command that doesn't stop
producing stdout) and tighten it, since capture is capped but not entirely free.

**"no open attempt — enroll at .../courses, then run `grid run <lessonId>`" (exit 5)**
`grid check`, `grid submit`, `grid status`, and `grid open` look for `.grid/attempt.json` in the
current directory (or `--dir`/`GRID_DIR`) first, then fall back to the `mode: none` attempt
record in your CLI config directory (see "Workspace modes"). If neither has anything, `grid run`
simply hasn't been run for any lesson yet from this machine. `cd` into a previously scaffolded
workspace, or run `grid run <lessonId>` to start one.

**"Enroll in this course to open its lessons." / exit 1 with a course URL (`grid run`/`grid submit`)**
Grid never auto-enrolls you. The printed URL takes one click to enroll — do that, then re-run
`grid run <lessonId>`. `grid submit` re-checks enrollment on every submission (not just once at
`grid run` time), so unenrolling partway through a lesson surfaces the same error there too.

**`grid run` refuses with "directory is not empty" (exit 1)**
`grid run` never overwrites files in a non-empty directory unless you pass `--force`. Use
`--here` to scaffold into the current directory instead of creating a new one, or `--force` if
you're sure you want to overwrite what's there.

**`grid upgrade` says to use your package manager instead**
If `grid` was installed via Homebrew, or lives in a root-owned `/usr/local/bin`, `grid upgrade`
deliberately refuses to self-replace the binary — use `brew upgrade grid` (or your package
manager's equivalent) instead.

**Checking what a command would output without side effects**
Every command supports `--json`. For anything you're about to script against, pipe it through
`jq` to inspect the shape: `grid status --json | jq`.
