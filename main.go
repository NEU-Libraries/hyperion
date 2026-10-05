// Command hyperion inspects Digital Repository Service objects in Atlas and
// bulk-exports the MODS of a Collection or Set. With no arguments it runs an
// interactive TUI; `hyperion get` prints any object's raw Atlas response,
// `hyperion search` a page of catalog search hits, and
// `hyperion export` (or the bare --type/--pid flags) exports headless. Auth is
// a personal-access token in the environment (ATLAS_URL + ATLAS_JWT),
// atlas_rb's BYO-JWT contract in Go.
//
// User-facing text calls the identifiers "PIDs" (the DRS/Cerberus convention);
// internally they are the resource NOIDs.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"hyperion/internal/atlas"
	"hyperion/internal/export"
)

// versionFile is .version at the repo root, the one place the version lives.
//
//go:embed .version
var versionFile string

var version = strings.TrimSpace(versionFile)

// Exit statuses. notFound lets a script ask whether a PID exists.
const (
	exitOK       = 0
	exitFailed   = 1
	exitUsage    = 2
	exitNotFound = 3
)

func main() {
	c := cli{stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv, stdoutIsTerminal: isTerminal(os.Stdout)}
	os.Exit(c.run(os.Args[1:]))
}

// cli is one invocation: where output goes and where settings come from, so
// the commands can be driven from tests.
type cli struct {
	stdout, stderr   io.Writer
	getenv           func(string) string
	stdoutIsTerminal bool
}

func (c cli) run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "get":
			return c.get(args[1:])
		case "search":
			return c.search(args[1:])
		case "export":
			return c.export(args[1:], true)
		}
	}
	// No subcommand: the TUI, or — given --type and --pid — the headless
	// export, as before subcommands existed.
	return c.export(args, false)
}

// export runs the headless export. Without a subcommand (explicit false) it
// is the default entry point, and falls through to the TUI unless both
// --type and --pid are given.
func (c cli) export(args []string, explicit bool) int {
	var (
		kindStr, pid, outDir, urlFlag string
		concurrency                   int
		showVersion                   bool
	)
	fs := flag.NewFlagSet("hyperion", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	fs.Usage = func() { c.usage() }
	strVar(fs, &kindStr, "collection or set", "type", "t")
	strVar(fs, &pid, "container PID", "pid", "p")
	strVar(fs, &outDir, "output directory (default ./<pid>-mods)", "out", "o")
	strVar(fs, &urlFlag, "Atlas base URL (default $ATLAS_URL)", "url")
	fs.IntVar(&concurrency, "concurrency", 4, "parallel downloads")
	fs.IntVar(&concurrency, "c", 4, "parallel downloads")
	fs.BoolVar(&showVersion, "version", false, "print version")
	fs.BoolVar(&showVersion, "v", false, "print version")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if showVersion {
		fmt.Fprintln(c.stdout, "hyperion "+version)
		return exitOK
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(c.stderr, "hyperion: unexpected %q — see hyperion --help\n", fs.Arg(0))
		return exitUsage
	}

	url := firstNonEmpty(urlFlag, c.getenv("ATLAS_URL"))
	token := c.getenv("ATLAS_JWT")
	if !explicit && (kindStr == "" || pid == "") {
		return runTUI(url, token, concurrency)
	}
	if kindStr == "" || pid == "" {
		fmt.Fprintln(c.stderr, "hyperion export: --type and --pid are both required")
		return exitUsage
	}

	// Headless: credentials must already be present — a script can't be
	// prompted.
	client, code := c.client(url, token)
	if client == nil {
		return code
	}
	kind, err := parseKind(kindStr)
	if err != nil {
		fmt.Fprintln(c.stderr, "hyperion:", err)
		return exitUsage
	}
	return c.headlessExport(client, kind, pid, outDir, concurrency)
}

// client builds the Atlas client for a headless command, or reports which
// setting is missing.
func (c cli) client(url, token string) (*atlas.Client, int) {
	if url == "" {
		fmt.Fprintln(c.stderr, "hyperion: no Atlas URL — set ATLAS_URL or pass --url")
		return nil, exitUsage
	}
	if token == "" {
		fmt.Fprintln(c.stderr, `hyperion: no access token — set ATLAS_JWT (from DRS → My DRS → "Programmatic access")`)
		return nil, exitUsage
	}
	return atlas.New(url, token), exitOK
}

func (c cli) headlessExport(client *atlas.Client, kind atlas.Kind, pid, outDir string, concurrency int) int {
	ctx := context.Background()
	if _, err := client.Whoami(ctx); err != nil {
		fmt.Fprintln(c.stderr, "hyperion:", explain("Couldn't sign in to Atlas.", err).text())
		return exitFailed
	}
	container, err := resolveForExport(ctx, client, kind, pid)
	if err != nil {
		fmt.Fprintln(c.stderr, "hyperion:", resolveProblem(err, kind, pid).text())
		if errors.Is(err, atlas.ErrNotFound) {
			return exitNotFound
		}
		return exitFailed
	}
	if len(container.Members) == 0 {
		fmt.Fprintln(c.stderr, "hyperion: no readable members to export.")
		return exitFailed
	}
	if outDir == "" {
		outDir = "./" + pid + "-mods"
	}
	fmt.Fprintf(c.stderr, "Exporting %s from %q to %s\n", plural(len(container.Members), "MODS file"), container.Title, outDir)
	if n := export.Existing(outDir, container.Members); n > 0 {
		fmt.Fprintf(c.stderr, "Replacing %s already in %s.\n", plural(n, "file"), outDir)
	}
	res, _ := export.Download(ctx, client, container.Members, outDir, concurrency, nil)
	fmt.Fprintf(c.stderr, "Saved %d; failed %d.\n", len(res.Saved), len(res.Failed))
	for noid, e := range res.Failed {
		fmt.Fprintf(c.stderr, "  %s: %s\n", noid, briefError(e))
	}
	if len(res.Failed) > 0 {
		return exitFailed
	}
	return exitOK
}

// resolveForExport resolves a Collection or Set to export. When no container
// of that kind has the PID, it checks what the PID is, so a Set entered as a
// Collection (or a Work entered as either) says so instead of "not found".
func resolveForExport(ctx context.Context, client *atlas.Client, kind atlas.Kind, pid string) (atlas.Container, error) {
	c, err := resolve(ctx, client, kind, pid)
	if !errors.Is(err, atlas.ErrNotFound) {
		return c, err
	}
	obj, ierr := client.Inspect(ctx, pid)
	if ierr != nil {
		return c, err
	}
	want := map[atlas.Kind]atlas.Type{atlas.Collection: atlas.TypeCollection, atlas.Set: atlas.TypeSet}[kind]
	switch obj.Type {
	case want:
		return c, err
	case atlas.TypeCollection, atlas.TypeSet:
		return c, userErr(fmt.Sprintf("%s is a %s, not a %s. Go back and choose %s.", pid, obj.Type, want, obj.Type))
	}
	return c, userErr(fmt.Sprintf("%s is a %s. Only a Collection or a Set can be exported.", pid, obj.Type))
}

func resolve(ctx context.Context, client *atlas.Client, kind atlas.Kind, pid string) (atlas.Container, error) {
	switch kind {
	case atlas.Collection:
		return client.ResolveCollection(ctx, pid)
	case atlas.Set:
		return client.ResolveSet(ctx, pid)
	default:
		return atlas.Container{}, fmt.Errorf("unknown type %q", kind)
	}
}

func parseKind(s string) (atlas.Kind, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "collection":
		return atlas.Collection, nil
	case "set":
		return atlas.Set, nil
	default:
		return "", fmt.Errorf("--type must be 'collection' or 'set' (got %q)", s)
	}
}

// resolveProblem words a failed Collection or Set lookup, for the export
// screen and the headless run alike.
func resolveProblem(err error, kind atlas.Kind, pid string) problem {
	switch {
	case errors.Is(err, atlas.ErrNotFound):
		err = userErr(fmt.Sprintf("No %s has this PID, or your account doesn't have permission to view it.", kind))
	case errors.Is(err, atlas.ErrForbidden):
		err = userErr(fmt.Sprintf("The %s exists, but your account doesn't have permission to view it.", kind))
	}
	return explain(fmt.Sprintf("Couldn't open the %s with PID %s.", kind, pid), err)
}

// text renders a problem as plain lines for stderr.
func (p problem) text() string {
	out := p.headline + " " + p.reason
	for _, d := range p.details {
		out += "\n  " + d
	}
	return out
}

func strVar(fs *flag.FlagSet, p *string, help string, names ...string) {
	for _, n := range names {
		fs.StringVar(p, n, "", help)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// isTerminal reports whether f is a terminal rather than a pipe or file.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (c cli) usage() {
	fmt.Fprintf(c.stderr, `hyperion %s — inspect DRS objects in Atlas, and export MODS

Usage:
  hyperion                                  interactive
  hyperion get <PID> [SECTION] [flags]      print Atlas's raw response
  hyperion search <WORDS> [flags]           search the catalog, raw
  hyperion export --type TYPE --pid <PID>   export a Collection or Set's MODS
  hyperion --type TYPE --pid <PID>          the same export, the older form

Auth comes from the environment:
  ATLAS_URL   Atlas base URL
  ATLAS_JWT   personal access token (DRS → My DRS → "Programmatic access")

Export flags:
  -t, --type TYPE       collection or set
  -p, --pid PID         container PID
  -o, --out DIR         output directory (default ./<pid>-mods)
  -c, --concurrency N   parallel downloads (default 4)
      --url URL         Atlas base URL (default $ATLAS_URL)
  -v, --version         print version
  -h, --help            show this help

Run hyperion get --help for its sections and flags, and hyperion search
--help for its flags.

Exit status: 0 success, 1 failure, 2 usage, 3 no such PID.
`, version)
}
