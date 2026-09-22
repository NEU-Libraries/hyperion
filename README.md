# Hyperion

Bulk-export the descriptive **MODS** metadata for every member of a Digital
Repository Service **Collection** or **Set** — one `<pid>.xml` file per member.

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
