# Hyperion

Inspect any Digital Repository Service object by **PID** — its type, title,
ancestors, and every field of its Atlas record — without cURL or a hand-built
bearer header. And bulk-export the descriptive **MODS** metadata for every
member of a **Collection** or **Set** — one `<pid>.xml` file per member.

Hyperion only ever reads: its Atlas client has no verb but `GET`.

A single self-contained binary (no runtime, no `bundle install`, no `pip`) with
an interactive terminal UI and a headless mode. It talks to the **Atlas API**
directly over HTTP, authenticated with a **personal access token**, and fetches
each member's MODS through Atlas's polymorphic `/resources/:id/mods` route.

> This is the v2 of a bash script that scraped v1's `…/pids` and fanned `wget`s
> at `/api/v1/files/neu:<pid>/mods`. (There was also a Ruby iteration used to
> shake down the `atlas_rb` client; this Go version is the real tool.)

## Install / build

Grab a release binary, or build from source (Go 1.21+; the toolchain
auto-fetches the pinned version):

```sh
go build -o hyperion .          # this platform
GOOS=darwin GOARCH=arm64 go build -o hyperion .   # Apple Silicon
GOOS=darwin GOARCH=amd64 go build -o hyperion .   # Intel Mac
GOOS=linux  GOARCH=amd64 go build -o hyperion .   # Linux
```

The result is a static binary — copy it anywhere and run it; nothing to install.

## Authentication

Hyperion authenticates as **you**, using a personal access token from the DRS
web app:

> **My DRS → Programmatic access → Generate token**

Export it along with the Atlas base URL:

```sh
export ATLAS_URL="https://atlas.library.northeastern.edu"
export ATLAS_JWT="eyJhbGciOi…"   # the token you generated
```

Tokens last one week; regenerate or revoke them from the same panel. Treat the
token like a password — it acts as you.

Setting these is optional for **interactive** use: run `./hyperion` with nothing
set up and it prompts for whatever's missing — the URL prefilled to the default,
the token entered masked. **Headless** mode requires them in the environment (a
script can't be prompted).

## Usage

### Interactive

Run with no arguments. If `ATLAS_URL` / `ATLAS_JWT` aren't set, Hyperion prompts
for them first. It then confirms who the token authenticates as and opens the
main menu. **Export MODS** asks whether you want a Collection or a Set, takes the
**PID**, shows the title and member count, and downloads with a live progress
bar. `esc` steps back a screen; `ctrl+c` quits from anywhere.

Hyperion takes over the whole terminal while it runs, and a trail under your name
shows where you are (`Inspect › Work w1 › FileSets › FileSet fs1`). When
something fails, the screen says what it was doing and why in plain words, with
a line of technical detail for a bug report; if Atlas sent an error response,
`d` shows it in full. An export's summary stays on the terminal after you quit.

```sh
./hyperion
```

### Headless (scripts / cron)

Pass `--type` and `--pid` to skip the prompts (auth still comes from
`ATLAS_URL` / `ATLAS_JWT`):

```sh
./hyperion --type collection --pid 3xsj47p --out ./coastal-mods
./hyperion -t set -p 9k41zc7 -c 8
```

| Flag | Description |
| --- | --- |
| `-t, --type` | `collection` or `set` |
| `-p, --pid` | container PID |
| `-o, --out` | output directory (default `./<pid>-mods`) |
| `-c, --concurrency` | parallel downloads (default 4) |
| `--url` | Atlas base URL (default `$ATLAS_URL`) |
| `-v, --version` / `-h, --help` | |

Exit status is `0` when every member exported, `1` if any failed (failures are
listed) — so it composes in a pipeline.

## What it does

### Inspect

1. Asks `GET /resources/:id` where the PID lives. Atlas answers with a redirect
   to the typed route (`/works/:id`, `/files/:id`, …); Hyperion reads the type
   from it and fetches that route from your configured Atlas URL.
2. A Set isn't served by `/resources/:id`, so a 404 there falls back to
   `/compilations/:id`.
3. Shows the type, title, and ancestor chain, a menu of the sections that apply
   to that type, and as much of the record as fits. Object ids are labelled
   **PID** and person ids **NUID** (depositor, proxy uploader, …).

A PID that doesn't exist and one your token may not read get different
messages, as Atlas keeps them apart (404 vs 403). A tombstoned object still
inspects — Atlas serves its record with a 410.

| Section | Types | Atlas route |
| --- | --- | --- |
| Record | all | the typed record, every field |
| MODS, MODS history | Work, Collection, Community | `/resources/:id/mods`, `…/mods/versions` (admin only) |
| METS | Work, FileSet | `/works/:id/mets`, `/file_sets/:id/mets` |
| FileSets, Files | Work | `/works/:id/file_sets`, `/works/:id/assets` |
| Linked collections, Associations | Work | `/works/:id/linked_members`, `/works/:id/associations` |
| Children | Collection, Community | `…/children` |
| Contents | Set | `/compilations/:id/contents` |
| FileSet and Work, Versions | File | `/files/:id/ancestry`, `/files/:id/versions` |
| Ancestors, Referenced PIDs | when present | from the record |
| Permissions | all but Set | `/resources/:id/permissions` |

Enter on any object in a section opens that object; `esc` comes back. Lists
that Atlas returns as bare PIDs (children, linked collections, associations)
look up each title as its row scrolls into view. Atlas has no route that lists
a FileSet's files, so a FileSet opened from its Work's FileSets carries them in
a Files section; opened by PID, it has none.

### Export

1. Resolves the container (`GET /collections/:id` or `/compilations/:id`) to
   confirm it exists and is readable with your token.
2. Enumerates members — a Collection's `…/children` (PIDs), or a Set's paged
   `…/contents`.
3. Fetches each member's MODS from the type-agnostic `GET /resources/:id/mods.xml`
   in a bounded worker pool, writing `<out>/<pid>.xml`. A member that fails is
   reported and skipped, never fatal.

## Mapping from the v1 script

| v1 (bash) | v2 (Hyperion) |
| --- | --- |
| `-t collection\|set`, `-p <pid>` | `--type`, `--pid` (or the TUI) |
| `-a <token>` query param | `ATLAS_JWT` bearer token |
| scrape `/…/pids`, `cut` | `…/children` / `…/contents` |
| per-object v1 `/mods` URL | `GET /resources/:id/mods.xml` (type-agnostic) |
| `neu_<pid>.xml` | `<pid>.xml` |
| `xargs -P4 wget` + `pv` | Go worker pool (`--concurrency`) + Bubble Tea bar |

("PID" is the DRS-facing term for what Atlas stores internally as a resource
NOID; Hyperion uses "PID" everywhere you see it, and the NOID value verbatim as
the filename.)
