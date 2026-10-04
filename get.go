package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"hyperion/internal/atlas"
)

// getSection is one thing `hyperion get` can print. path builds its route;
// typed sections need the object's type first, so they resolve the PID. noun
// names the section in messages ("METS"); types are the object types it
// applies to (none listed: every type).
type getSection struct {
	name  string
	about string
	typed bool
	noun  string
	types []atlas.Type
	path  func(t atlas.Type, pid string, o getOptions) string
}

type getOptions struct {
	at   string // mods: a retained version label
	page int    // contents: which page
	q    string // search-debug: the words
}

var (
	descriptive = []atlas.Type{atlas.TypeWork, atlas.TypeCollection, atlas.TypeCommunity}
	containers  = []atlas.Type{atlas.TypeCollection, atlas.TypeCommunity}
	works       = []atlas.Type{atlas.TypeWork}
	files       = []atlas.Type{atlas.TypeFile}
)

// getSections mirror the TUI's sections, one Atlas route each. record is
// handled apart: it is the resolved object itself.
var getSections = []getSection{
	{"record", "the object's record (the default)", true, "record", nil, nil},
	{"mods", "MODS XML; with --at VERSION, as of that version", false, "MODS", descriptive, func(_ atlas.Type, pid string, o getOptions) string {
		if o.at != "" {
			return atlas.PathMODSVersion(pid, o.at)
		}
		return atlas.PathMODS(pid)
	}},
	{"mods-json", "MODS as JSON", false, "MODS", descriptive,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathMODSJSON(pid) }},
	{"mods-history", "the retained MODS versions (admins and delegated admins)", false, "MODS history", descriptive,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathMODSVersions(pid) }},
	{"mets", "METS, for a Work or FileSet", true, "METS", []atlas.Type{atlas.TypeWork, atlas.TypeFileSet},
		func(t atlas.Type, pid string, _ getOptions) string { return atlas.PathMETS(t, pid) }},
	{"permissions", "who may view and edit it", false, "permissions", nil,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathPermissions(pid) }},
	{"children", "PIDs directly beneath a Collection or Community", true, "children", containers,
		func(t atlas.Type, pid string, _ getOptions) string { return atlas.PathChildren(t, pid) }},
	{"contents", "a Set's works, one page (--page N, default 1)", false, "contents", []atlas.Type{atlas.TypeSet},
		func(_ atlas.Type, pid string, o getOptions) string { return atlas.PathSetContents(pid, o.page) }},
	{"filesets", "a Work's FileSets, with their files", false, "FileSets", works,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathFileSets(pid) }},
	{"files", "a Work's downloadable files", false, "downloadable files", works,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathAssets(pid) }},
	{"linked", "the Collections a Work is linked into", false, "linked collections", works,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathLinkedMembers(pid) }},
	{"associations", "a Work's associations, both ways", false, "associations", works,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathAssociations(pid) }},
	{"ancestry", "a File's FileSet and Work", false, "ancestry", files,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathFileAncestry(pid) }},
	{"versions", "a File's retained versions", false, "versions", files,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathFileVersions(pid) }},
	{"solr", "the Solr document (admins and delegated admins)", false, "Solr document", atlas.IndexedTypes,
		func(_ atlas.Type, pid string, _ getOptions) string { return atlas.PathSolr(pid) }},
	{"search-debug", "how search scores it for --q WORDS (admins and delegated admins)", false, "search explanation",
		atlas.ExplainedTypes, func(_ atlas.Type, pid string, o getOptions) string { return atlas.PathSearchExplanation(pid, o.q) }},
}

// appliesTo reports whether the section exists for objects of type t.
func (s getSection) appliesTo(t atlas.Type) bool {
	if len(s.types) == 0 {
		return true
	}
	for _, want := range s.types {
		if want == t {
			return true
		}
	}
	return false
}

// typeList names types for a message: "a Work or FileSet".
func typeList(ts []atlas.Type) string {
	names := make([]string, len(ts))
	for i, t := range ts {
		names[i] = string(t)
	}
	if len(names) == 1 {
		return "a " + names[0]
	}
	return "a " + strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// get prints Atlas's raw response for one section of one object to stdout —
// the bytes exactly as sent, for jq or xmllint — and anything else to stderr.
func (c cli) get(args []string) int {
	var (
		urlFlag string
		o       getOptions
	)
	fs := flag.NewFlagSet("hyperion get", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	fs.Usage = func() { c.getUsage() }
	fs.StringVar(&urlFlag, "url", "", "Atlas base URL (default $ATLAS_URL)")
	fs.StringVar(&o.at, "at", "", "with mods: a retained version, e.g. v3")
	fs.IntVar(&o.page, "page", 1, "with contents: which page")
	fs.StringVar(&o.q, "q", "", "with search-debug: the words to explain")

	positional, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if len(positional) == 0 || len(positional) > 2 {
		c.getUsage()
		return exitUsage
	}
	pid, name := positional[0], "record"
	if len(positional) == 2 {
		name = positional[1]
	}
	sec, ok := findGetSection(name)
	if !ok {
		fmt.Fprintf(c.stderr, "hyperion get: no section %q — one of %s\n", name, sectionNames())
		return exitUsage
	}
	if o.at != "" && sec.name != "mods" {
		fmt.Fprintln(c.stderr, "hyperion get: --at goes with the mods section")
		return exitUsage
	}
	if strings.TrimSpace(o.q) != "" && sec.name != "search-debug" {
		fmt.Fprintln(c.stderr, "hyperion get: --q goes with the search-debug section")
		return exitUsage
	}
	if sec.name == "search-debug" && strings.TrimSpace(o.q) == "" {
		fmt.Fprintln(c.stderr, "hyperion get: search-debug needs the words to explain, with --q")
		return exitUsage
	}
	if o.page < 1 {
		fmt.Fprintln(c.stderr, "hyperion get: --page counts from 1")
		return exitUsage
	}

	client, code := c.client(firstNonEmpty(urlFlag, c.getenv("ATLAS_URL")), c.getenv("ATLAS_JWT"))
	if client == nil {
		return code
	}
	ctx := context.Background()

	var (
		body []byte
		typ  atlas.Type
	)
	if sec.typed {
		obj, err := client.Inspect(ctx, pid)
		if err != nil {
			return c.getFailed(inspectProblem(pid, err), err)
		}
		if sec.path == nil { // record: the object itself
			return c.emit(obj.Raw)
		}
		typ = obj.Type
	}
	if typ != "" && !sec.appliesTo(typ) {
		fmt.Fprintf(c.stderr, "hyperion get: PID %s is a %s; %s is only for %s.\n", pid, typ, sec.name, typeList(sec.types))
		return exitUsage
	}
	path := sec.path(typ, pid, o)
	if body, err = client.Raw(ctx, path); err != nil {
		if errors.Is(err, atlas.ErrNotFound) {
			return c.sectionNotFound(ctx, client, pid, sec, typ)
		}
		if errors.Is(err, atlas.ErrForbidden) && sec.name == "mods-history" {
			fmt.Fprintln(c.stderr, "hyperion:", modsHistoryLimit)
			return exitFailed
		}
		if errors.Is(err, atlas.ErrForbidden) && (sec.name == "solr" || sec.name == "search-debug") {
			fmt.Fprintln(c.stderr, "hyperion:", indexLimit)
			return exitFailed
		}
		if errors.Is(err, atlas.ErrNotIndexed) {
			fmt.Fprintf(c.stderr, "hyperion: PID %s: Solr holds no document for it, so it can't appear in search. "+
				"Reindexing it in Atlas fixes this.\n", pid)
			return exitFailed
		}
		p := explain(fmt.Sprintf("Couldn't get %s for PID %s.", sec.name, pid), err)
		return c.getFailed(p, err)
	}
	return c.emit(body)
}

// sectionNotFound tells apart the two things a 404 on a section's route can
// mean: no object has the PID (exit 3), or the object exists and has no such
// section — a FileSet with no METS, the ancestry of a Work — which is a
// failure, not a missing PID. typ is the object's type if already resolved.
func (c cli) sectionNotFound(ctx context.Context, client *atlas.Client, pid string, sec getSection, typ atlas.Type) int {
	if typ == "" {
		obj, err := client.Inspect(ctx, pid)
		if err != nil {
			return c.getFailed(inspectProblem(pid, err), err)
		}
		typ = obj.Type
	}
	if !sec.appliesTo(typ) {
		fmt.Fprintf(c.stderr, "hyperion get: PID %s is a %s; %s is only for %s.\n", pid, typ, sec.name, typeList(sec.types))
		return exitUsage
	}
	fmt.Fprintf(c.stderr, "hyperion: PID %s: this %s has no %s.\n", pid, typ, sec.noun)
	return exitFailed
}

// emit writes a body to stdout unchanged — except that a terminal gets a
// final newline if the body lacks one, so the prompt doesn't land after it.
func (c cli) emit(body []byte) int {
	if _, err := c.stdout.Write(body); err != nil {
		fmt.Fprintln(c.stderr, "hyperion:", err)
		return exitFailed
	}
	if c.stdoutIsTerminal && !bytes.HasSuffix(body, []byte("\n")) {
		fmt.Fprintln(c.stdout)
	}
	return exitOK
}

func (c cli) getFailed(p problem, err error) int {
	fmt.Fprintln(c.stderr, "hyperion:", p.text())
	if errors.Is(err, atlas.ErrNotFound) {
		return exitNotFound
	}
	return exitFailed
}

func findGetSection(name string) (getSection, bool) {
	for _, s := range getSections {
		if s.name == name {
			return s, true
		}
	}
	return getSection{}, false
}

func sectionNames() string {
	names := make([]string, len(getSections))
	for i, s := range getSections {
		names[i] = s.name
	}
	return strings.Join(names, ", ")
}

// parseInterspersed parses flags wherever they fall among the positional
// arguments — `get w1 mods --at v3` as well as `get --at v3 w1 mods` — which
// the flag package alone stops doing at the first positional one.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return positional, nil
		}
		if args[0] == "--" {
			return append(positional, args[1:]...), nil
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
}

func (c cli) getUsage() {
	var b strings.Builder
	b.WriteString(`Usage: hyperion get <PID> [SECTION] [flags]

Prints what Atlas returns for one object, exactly as sent — JSON or XML —
to stdout. Errors go to stderr.

Sections:
`)
	for _, s := range getSections {
		fmt.Fprintf(&b, "  %-13s %s\n", s.name, s.about)
	}
	b.WriteString(`
Flags:
  --at VERSION   with mods: the MODS as of a retained version, e.g. v3
  --page N       with contents: which page (default 1)
  --q WORDS      with search-debug: the words to explain
  --url URL      Atlas base URL (default $ATLAS_URL)

Examples:
  hyperion get p5hqf0q | jq .work.title
  hyperion get p5hqf0q mods > p5hqf0q.xml
  hyperion get z612nb3 children | jq -r '.[]'
  hyperion get p5hqf0q search-debug --q "harbor maps" | jq .matched

Exit status: 0 success, 1 failure, 2 usage, 3 no such PID.
`)
	fmt.Fprint(c.stderr, b.String())
}
