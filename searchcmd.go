package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/NEU-Libraries/hyperion/internal/atlas"
)

// search prints one page of Atlas's catalog search response to stdout,
// exactly as sent, as get does. A script pages with --page, reading
// pagination.pages.
func (c cli) search(args []string) int {
	var (
		urlFlag, typeFlag string
		page              int
	)
	fs := flag.NewFlagSet("hyperion search", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	fs.Usage = func() { c.searchUsage() }
	fs.StringVar(&urlFlag, "url", "", "Atlas base URL (default $ATLAS_URL)")
	fs.StringVar(&typeFlag, "type", "", "Work, Collection, Community or Person")
	fs.IntVar(&page, "page", 1, "which page")

	words, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	q := strings.TrimSpace(strings.Join(words, " "))
	if q == "" {
		c.searchUsage()
		return exitUsage
	}
	t, ok := parseSearchType(typeFlag)
	if !ok {
		fmt.Fprintf(c.stderr, "hyperion search: --type must be one of %s (got %q)\n", searchTypeNames(), typeFlag)
		return exitUsage
	}
	if page < 1 {
		fmt.Fprintln(c.stderr, "hyperion search: --page counts from 1")
		return exitUsage
	}

	client, code := c.client(firstNonEmpty(urlFlag, c.getenv("ATLAS_URL")), c.getenv("ATLAS_JWT"))
	if client == nil {
		return code
	}
	body, err := client.Raw(context.Background(), atlas.PathSearch(q, t, page, atlas.SearchPageSize))
	if err != nil {
		fmt.Fprintln(c.stderr, "hyperion:", explain("Couldn't search the catalog.", err).text())
		return exitFailed
	}
	return c.emit(body)
}

// parseSearchType reads --type in any case; "" searches every type.
func parseSearchType(s string) (atlas.Type, bool) {
	if s == "" {
		return "", true
	}
	for _, t := range atlas.SearchTypes {
		if strings.EqualFold(s, string(t)) {
			return t, true
		}
	}
	return "", false
}

func searchTypeNames() string {
	names := make([]string, len(atlas.SearchTypes))
	for i, t := range atlas.SearchTypes {
		names[i] = string(t)
	}
	return strings.Join(names, ", ")
}

func (c cli) searchUsage() {
	fmt.Fprintf(c.stderr, `Usage: hyperion search <WORDS> [flags]

Searches the catalog's Works, Collections, Communities and People, and prints
one page of up to %d hits exactly as Atlas sends it — JSON — to stdout. Errors
go to stderr. Hits are only what your account may view.

Flags:
  --type TYPE   only one type: %s
  --page N      which page (default 1); pagination.pages says how many
  --url URL     Atlas base URL (default $ATLAS_URL)

Examples:
  hyperion search moby dick | jq -r '.results[] | "\(.noid)  \(.title)"'
  hyperion search --type Person hopper

Exit status: 0 success, 1 failure, 2 usage.
`, atlas.SearchPageSize, searchTypeNames())
}
