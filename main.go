// Command hyperion bulk-exports the MODS of every member of a DRS Collection or
// Set — one <pid>.xml per member. With no --type/--pid it runs an interactive
// TUI; given both it runs headless. Auth is a personal-access token in the
// environment (ATLAS_URL + ATLAS_JWT), atlas_rb's BYO-JWT contract in Go.
//
// User-facing text calls the identifiers "PIDs" (the DRS/Cerberus convention);
// internally they are the resource NOIDs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"hyperion/internal/atlas"
	"hyperion/internal/export"
)

const version = "2.0.0"

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	var (
		kindStr, pid, outDir, urlFlag string
		concurrency                   int
		showVersion                   bool
	)
	fs := flag.NewFlagSet("hyperion", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { usage() }
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
			return 0
		}
		return 2
	}
	if showVersion {
		fmt.Println("hyperion " + version)
		return 0
	}

	url := firstNonEmpty(urlFlag, os.Getenv("ATLAS_URL"))
	token := os.Getenv("ATLAS_JWT")

	// Headless (both --type and --pid): credentials must already be present —
	// a script can't be prompted. Interactive: the TUI prompts for whatever's
	// missing, so `./hyperion` works with nothing set up.
	if kindStr != "" && pid != "" {
		if url == "" {
			fmt.Fprintln(os.Stderr, "hyperion: no Atlas URL — set ATLAS_URL or pass --url")
			return 1
		}
		if token == "" {
			fmt.Fprintln(os.Stderr, `hyperion: no access token — set ATLAS_JWT (from DRS → My DRS → "Programmatic access")`)
			return 1
		}
		kind, err := parseKind(kindStr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "hyperion:", err)
			return 1
		}
		return headless(atlas.New(url, token), kind, pid, outDir, concurrency)
	}
	return runTUI(url, token, concurrency)
}

func headless(client *atlas.Client, kind atlas.Kind, pid, outDir string, concurrency int) int {
	ctx := context.Background()
	if _, err := client.Whoami(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "hyperion:", authMessage(err))
		return 1
	}
	container, err := resolve(ctx, client, kind, pid)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hyperion:", resolveMessage(err, kind, pid))
		return 1
	}
	if len(container.Members) == 0 {
		fmt.Fprintln(os.Stderr, "hyperion: no readable members to export.")
		return 1
	}
	if outDir == "" {
		outDir = "./" + pid + "-mods"
	}
	fmt.Fprintf(os.Stderr, "Exporting %d MODS record(s) from %q → %s\n", len(container.Members), container.Title, outDir)
	res, _ := export.Download(ctx, client, container.Members, outDir, concurrency, nil)
	fmt.Fprintf(os.Stderr, "Saved %d; failed %d.\n", len(res.Saved), len(res.Failed))
	for noid, e := range res.Failed {
		fmt.Fprintf(os.Stderr, "  %s: %v\n", noid, e)
	}
	if len(res.Failed) > 0 {
		return 1
	}
	return 0
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

// authMessage / resolveMessage turn the client's sentinel errors into the same
// user-facing wording the TUI shows.
func authMessage(err error) string {
	if errors.Is(err, atlas.ErrUnauthorized) {
		return `access token rejected (expired or revoked?). Generate a fresh one in DRS → My DRS.`
	}
	return "could not reach Atlas: " + err.Error()
}

func resolveMessage(err error, kind atlas.Kind, pid string) string {
	if errors.Is(err, atlas.ErrNotFound) {
		return fmt.Sprintf("no %s with PID %s — unknown, or not readable with this token.", kind, pid)
	}
	return err.Error()
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

func usage() {
	fmt.Fprintf(os.Stderr, `hyperion %s — export a DRS Collection or Set's MODS

Usage:
  hyperion                                  interactive
  hyperion --type collection --pid <PID>    headless

Auth comes from the environment:
  ATLAS_URL   Atlas base URL
  ATLAS_JWT   personal access token (DRS → My DRS → "Programmatic access")

Flags:
  -t, --type TYPE       collection or set
  -p, --pid PID         container PID
  -o, --out DIR         output directory (default ./<pid>-mods)
  -c, --concurrency N   parallel downloads (default 4)
      --url URL         Atlas base URL (default $ATLAS_URL)
  -v, --version         print version
  -h, --help            show this help
`, version)
}
