# Hyperion

Search the catalog, or inspect any Digital Repository Service object by
**PID** — its type, title, ancestors, and every field of its Atlas record —
without cURL or a hand-built bearer header. And bulk-export the descriptive **MODS** metadata for every
member of a **Collection** or **Set** — one `<pid>.xml` file per member.

Hyperion only ever reads: its Atlas client has no verb but `GET`.

A single self-contained binary (no runtime, no `bundle install`, no `pip`) with
an interactive terminal UI and a headless mode. It talks to the **Atlas API**
directly over HTTP, authenticated with a **personal access token**, and fetches
each member's MODS through Atlas's polymorphic `/resources/:id/mods` route.

> Hyperion began as a successor to a bash script that exported MODS from DRS
> v1. It is now a tool of its own; [Coming from the bash export
> script](#coming-from-the-bash-export-script) maps the old flags to the new.

## Install / build

### Download a release

The [Releases page](https://github.com/NEU-Libraries/hyperion/releases) has a
binary for each Mac: `hyperion-darwin-arm64` for Apple Silicon and
`hyperion-darwin-amd64` for Intel. Run `uname -m` if you're unsure: `arm64` is
Apple Silicon, `x86_64` is Intel.

1. Download the binary for your Mac.
2. In Terminal, go to the folder it downloaded to, and rename it:

   ```sh
   mv hyperion-darwin-arm64 hyperion
   ```

3. Make it executable. A browser saves a download without that permission.

   ```sh
   chmod +x hyperion
   ```

4. Clear macOS's quarantine flag. A binary downloaded through a browser, Mail,
   or AirDrop is quarantined, and Gatekeeper refuses to run it because it
   isn't notarized.

   ```sh
   xattr -d com.apple.quarantine hyperion
   ```

   If this answers `No such xattr`, the file wasn't quarantined (`curl`
   doesn't quarantine), and there is nothing to clear.

5. Run it:

   ```sh
   ./hyperion --version
   ```

It is a static binary: move it anywhere, such as a folder on your `PATH`.
There is nothing else to install.

### Build from source

Go 1.21 or later; the toolchain fetches the version `go.mod` pins.

```sh
go build -o hyperion .          # this platform
GOOS=darwin GOARCH=arm64 go build -o hyperion .   # Apple Silicon
GOOS=darwin GOARCH=amd64 go build -o hyperion .   # Intel Mac
GOOS=linux  GOARCH=amd64 go build -o hyperion .   # Linux
```

`make mac` builds both Mac binaries at once, as `hyperion-darwin-arm64` and
`hyperion-darwin-amd64`. A binary you build yourself is already executable
and isn't quarantined.

With Go installed, `go install` fetches, builds and installs it into
`$(go env GOPATH)/bin` in one step:

```sh
go install github.com/NEU-Libraries/hyperion@latest
```

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
for them first; if sign-in fails, `enter` tries again with what you typed kept.
It then shows who you're signed in as and opens the main menu. **Search the
catalog** takes words, and `tab` narrows the search to Works, Collections,
Communities or People; enter on a result opens it as if you'd typed its PID. It
shows the first 100 results, and says how many more matched. **Export MODS**
asks whether you want a Collection or a Set, takes the **PID** and a folder,
says how many MODS files it will save — and how many already in the folder it
would replace — and downloads with a live progress bar. `esc` steps back a
screen (going back from an export error keeps what you entered), and `h` goes
straight back to the main menu from any screen — except while an export is
running, which it would cut short. `q` quits from
any screen, after asking — `y` to quit, `n` or `esc` to stay; if an export is
running, the question says how far it got. Where you're typing (a PID, a
folder, a file name, the sign-in), `q` and `h` are just letters; `ctrl+c` quits at
once from anywhere, and the last item on every screen's key row shows which
applies.

Hyperion takes over the whole terminal while it runs, and a trail under your name
shows where you are (`Inspect › Work w1 › FileSets › FileSet fs1`). Each screen's
content sits in a titled panel, with its keys listed underneath; on an object's
overview the Sections panel and a Record preview sit side by side when the
terminal is wide enough. When
something fails, the screen says what it was doing and why in plain words, with
a line of technical detail for a bug report; if Atlas sent an error response,
`d` shows it in full. An export's summary stays on the terminal after you quit.

```sh
./hyperion
```

### Get: an object's raw Atlas response (scripts)

`hyperion get` prints what Atlas returns for one object — exactly as sent, JSON
or XML — to stdout, so it drops straight into `jq` or `xmllint`. It replaces
hand-built cURL calls with a bearer header. Auth comes from `ATLAS_URL` /
`ATLAS_JWT`.

```sh
./hyperion get w1 | jq -r .work.title        # the record (the default)
./hyperion get w1 mods > w1.xml              # current MODS
./hyperion get w1 mods --at v3               # MODS as of a retained version
./hyperion get k1 children | jq -r '.[]'     # PIDs beneath a Collection
./hyperion get w1 search-debug --q "harbor maps" | jq .matched
```

| Section | What it prints |
| --- | --- |
| `record` | the object's record (the default) |
| `mods` | MODS XML; with `--at VERSION`, as of that version |
| `mods-json` | MODS as JSON |
| `mods-history` | the retained MODS versions (admins and delegated admins) |
| `mets` | METS, for a Work or FileSet |
| `permissions` | the access-control envelope |
| `children` | PIDs directly beneath a Collection or Community; with `--all`, every child, live or tombstoned, as digests, even beneath a tombstoned container (admins and delegated admins) |
| `contents` | a Set's works, one page (`--page N`, default 1) |
| `filesets`, `files` | a Work's FileSets with their files, or its downloadable files |
| `linked`, `associations` | the Collections a Work is linked into; its associations |
| `ancestry`, `versions` | a File's FileSet and Work; its retained versions |
| `solr` | the Solr document (admins and delegated admins) |
| `search-debug` | how the catalog search scores a Work, Collection or Community for `--q WORDS` (admins and delegated admins) |

Flags may come before or after the PID. Output to a pipe or file is never
altered; on a terminal, a final newline is added if the body lacks one. Errors
go to stderr in the same words the TUI uses. Run `./hyperion get --help` for
the list above.

### Search: the catalog's raw search response (scripts)

`hyperion search` prints one page of up to 100 results from Atlas's catalog
search — exactly as sent — to stdout. The results are only what your token may
view. Page with `--page N`, reading `pagination.pages` for how many there are.

```sh
./hyperion search boston harbor | jq -r '.results[] | "\(.noid)  \(.title)"'
./hyperion search --type Person hopper       # Work, Collection, Community or Person
```

Subjects, names and dates aren't searchable yet, and Sets aren't in the
results: open a Set by its PID.

### Export (scripts / cron)

`hyperion export` exports a Collection or Set without the TUI. The older form —
the same flags with no subcommand — still works.

```sh
./hyperion export --type collection --pid 3xsj47p --out ./coastal-mods
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

### Exit status

| Status | Meaning |
| --- | --- |
| `0` | success (for export: every member exported) |
| `1` | failure (for export: any member failed; failures are listed) |
| `2` | usage: a bad flag or section, or `ATLAS_URL` / `ATLAS_JWT` unset |
| `3` | no such PID (or, for a Collection or Set, none this token can read) |

## What it does

### Inspect

1. Asks `GET /resources/:id` where the PID lives. Atlas answers with a redirect
   to the typed route (`/works/:id`, `/files/:id`, …); Hyperion reads the type
   from it and fetches that route from your configured Atlas URL.
2. A Set isn't served by `/resources/:id`, so a 404 there falls back to
   `/compilations/:id`.
3. Shows the type, title, and ancestor chain, a menu of the sections that apply
   to that type, and a preview of the record. The menu is grouped the same way
   for every type — Metadata, Content, Relationships, Search, Access — and
   scrolls on a short terminal. Object ids are labelled **PID** and person ids **NUID**
   (depositor, proxy uploader, …); timestamps show in local time.

A section that simply has nothing — no METS yet, MODS history for someone who
isn't an admin or a delegated admin — says so calmly rather than as an error.

A PID that doesn't exist and one your account may not view get different
messages, as Atlas keeps them apart (404 vs 403). A tombstoned object still
inspects — Atlas serves its record with a 410.

| Section | Types | Atlas route |
| --- | --- | --- |
| Record | all | the typed record, every field |
| MODS, MODS JSON, MODS history | Work, Collection, Community | `/resources/:id/mods.xml`, `/resources/:id/mods`, `…/mods/versions` (admins and delegated admins) |
| METS | Work, FileSet | `/works/:id/mets`, `/file_sets/:id/mets` |
| FileSets, Downloadable files | Work | `/works/:id/file_sets`, `/works/:id/assets` |
| Linked collections, Associations | Work | `/works/:id/linked_members`, `/works/:id/associations` |
| Children | Collection, Community | `…/children`; for a tombstoned one, `/resources/:id/children` (admins and delegated admins) |
| Contents | Set | `/compilations/:id/contents` |
| FileSet and Work, Versions | File | `/files/:id/ancestry`, `/files/:id/versions` |
| Ancestors; Set definition or Referenced PIDs | when present | from the record |
| Solr document | all but Set | `/resources/:id/solr` (admins and delegated admins) |
| Search debug | Work, Collection, Community | `/resources/:id/search_explanation` (admins and delegated admins) |
| Permissions | all but Set | `/resources/:id/permissions` |

Enter on any object in a section opens that object; `esc` comes back. Lists
that Atlas returns as bare PIDs (children, linked collections, associations)
look up each title as its row scrolls into view. A long list moves a screen at
a time with `pgdn`/`pgup` (or space and `b`), and `g`/`G` jump to its start and
end; the key row shows where you are ("15/250"). Atlas has no route that lists
a FileSet's files, so a FileSet opened from its Work's FileSets carries them in
a Files section; opened by PID, it has none.

Documents (MODS, METS, the record, Atlas's error responses) open in a pager.
MODS is highlighted as XML and METS as JSON. Long lines are cut at the edge and
`←`/`→` pans them; `w` wraps them instead, and the choice carries over to every
pager until you quit. `↑`/`↓` scroll, and `g`/`G` jump to the top and bottom.

`r` on an object or any of its sections shows exactly what Atlas returned for
it — the request (`GET /works/w1/file_sets`) above the response, highlighted —
the same bytes cURL would print; `r` again comes back. `s` on a raw view, MODS,
or an Atlas error response saves it, offering `./<pid>-<section>.<ext>`. The
file gets Atlas's bytes unchanged, and Hyperion never writes over an existing
file.

### Solr document and Search debug

Both are for admins and delegated admins, and Atlas also checks that you can
read the object. Anyone else sees "(admins only)" beside them in the menu.

**Solr document** shows what Atlas wrote to Solr for the object. Atlas writes
most properties several ways (`depositor_ssi`, `depositor_tesim`, …); where the
variants hold the same value they share one row, named
`depositor_{ssi,ssim,tesi,tesim,tsi,tsim}`. A long value is cut to a line and a
long list to 8 lines; `r` shows the document whole. Fields that are searched
but not stored, such as the stemmed titles and `descriptive_keywords_tesim`,
never appear in a Solr document. If Solr holds no document for the object, the
screen says so: that is an indexing fault, and reindexing the object in Atlas
fixes it.

**Search debug** answers "why does this record not come up for these words?"
and "why does it rank where it does?". Type words, and about 300 ms after you
stop, Hyperion asks Atlas how the catalog search scores this object for them,
whether it matches or not. The screen shows:

- whether the object matches, its score, and any catalog filter that hides it
  anyway (a featured Collection, a tombstone);
- for each word, the field it scored best in, or that it matched nowhere;
- every searched field (Solr's `qf`), strongest first, with its boost and
  points;
- for the selected field (`↑`/`↓`), its stored text with the matched words
  marked — as typed, or in another form, such as "Survey" for "surveys" — and
  its index and query tokens.

`enter` swaps the field detail for Solr's explanation tree and the parsed
query; `pgdn`/`pgup` page through either. The field boosts and the minimum-match
rule come from Solr's search handler, so the screen stays right when
`solrconfig.xml` changes. Hyperion reads the tree the way Cerberus's "Why this
result?" dialog does: each word scores its best field in full, plus 1% of each
other field it matched.

### Export

1. Resolves the container (`GET /collections/:id` or `/compilations/:id`) to
   confirm it exists and is readable with your token.
2. Enumerates members — a Collection's `…/children` (PIDs), or a Set's paged
   `…/contents`.
3. Fetches each member's MODS from the type-agnostic `GET /resources/:id/mods.xml`
   in a bounded worker pool, writing `<out>/<pid>.xml`. A member that fails is
   reported and skipped, never fatal.

## Coming from the bash export script

| bash script | Hyperion |
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

## Development

```sh
make tools   # once: installs the pinned golangci-lint into $(go env GOPATH)/bin
make test    # go test ./...
make lint    # the linters in .golangci.yml
make fmt     # gofmt and goimports, in place
make check   # test, then lint — what CI runs
```

`.golangci.yml` holds the limits: a function may have a cyclomatic
complexity of 20, a cognitive complexity of 30, and 100 lines. Tests are
exempt from those. CI (`.github/workflows/ci.yml`) runs the tests, the
linters and `make mac` on every push to `main` and every pull request.
