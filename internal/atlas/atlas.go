// Package atlas is a small HTTP client for the parts of the Atlas API hyperion
// needs. It authenticates in bring-your-own-JWT mode: every request carries the
// personal-access token (from DRS → My DRS → "Programmatic access") as a bearer
// header — the same contract atlas_rb uses, reimplemented in Go so the tool
// ships as a single dependency-free binary.
package atlas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Sentinel errors so callers (and the TUI) can message auth vs missing distinctly.
var (
	ErrUnauthorized = errors.New("token rejected by Atlas (expired, revoked, or malformed)")
	ErrNotFound     = errors.New("not found")
)

// Client talks to one Atlas instance as one token-bearing user.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client. baseURL may carry a trailing slash; it's trimmed.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

// User is the subset of GET /user hyperion displays.
type User struct {
	NUID   string   `json:"nuid"`
	Name   string   `json:"name"`
	Role   string   `json:"role"`
	Groups []string `json:"groups"`
}

// Member is one thing to export — a NOID and, when the source gave us one, a title.
type Member struct {
	NOID  string
	Title string
}

// Kind distinguishes the two container types (v1's -t collection|set).
type Kind string

const (
	Collection Kind = "collection"
	Set        Kind = "set"
)

// Container is a resolved collection/set: its title and the members to export.
type Container struct {
	Kind    Kind
	NOID    string
	Title   string
	Members []Member
}

// Whoami resolves the token's own user (GET /user). On the BYO-JWT path Atlas
// reads identity from the token, so this both fetches the user and proves the
// credential — a rejected token comes back 401 → ErrUnauthorized.
func (c *Client) Whoami(ctx context.Context) (User, error) {
	body, err := c.get(ctx, "/user")
	if err != nil {
		return User{}, err
	}
	var u User
	if err := json.Unmarshal(body, &u); err != nil {
		return User{}, fmt.Errorf("atlas: unexpected /user response: %w", err)
	}
	return u, nil
}

// ResolveCollection loads a Collection's title and its member NOIDs. Children
// come back as bare NOIDs (no type) — that's fine now that MODS is fetched
// through the polymorphic /resources/:id/mods route, so no per-child type
// lookup is needed.
func (c *Client) ResolveCollection(ctx context.Context, id string) (Container, error) {
	var wrap struct {
		Collection struct {
			Title string `json:"title"`
		} `json:"collection"`
	}
	if err := c.getJSON(ctx, "/collections/"+id, &wrap); err != nil {
		return Container{}, err
	}

	var noids []string
	if err := c.getJSON(ctx, "/collections/"+id+"/children", &noids); err != nil {
		return Container{}, err
	}
	members := make([]Member, len(noids))
	for i, n := range noids {
		members[i] = Member{NOID: n}
	}
	return Container{Kind: Collection, NOID: id, Title: title(wrap.Collection.Title), Members: members}, nil
}

// ResolveSet loads a Set (Compilation) title and walks every page of its
// resolved contents. Contents digests already carry noid + title.
func (c *Client) ResolveSet(ctx context.Context, id string) (Container, error) {
	var wrap struct {
		Compilation struct {
			Title string `json:"title"`
		} `json:"compilation"`
	}
	if err := c.getJSON(ctx, "/compilations/"+id, &wrap); err != nil {
		return Container{}, err
	}

	var members []Member
	for page := 1; ; page++ {
		var res struct {
			Contents []struct {
				NOID  string `json:"noid"`
				Title string `json:"title"`
			} `json:"contents"`
			Pagination struct {
				Pages int `json:"pages"`
			} `json:"pagination"`
		}
		path := fmt.Sprintf("/compilations/%s/contents?page=%d&per_page=100", id, page)
		if err := c.getJSON(ctx, path, &res); err != nil {
			return Container{}, err
		}
		for _, d := range res.Contents {
			members = append(members, Member{NOID: d.NOID, Title: d.Title})
		}
		if len(res.Contents) == 0 || page >= res.Pagination.Pages {
			break
		}
	}
	return Container{Kind: Set, NOID: id, Title: title(wrap.Compilation.Title), Members: members}, nil
}

// MODS fetches a member's descriptive MODS XML by NOID via the type-agnostic
// /resources/:id/mods route (the .xml extension selects the format).
func (c *Client) MODS(ctx context.Context, noid string) ([]byte, error) {
	return c.get(ctx, "/resources/"+noid+"/mods.xml")
}

func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	body, err := c.get(ctx, path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("atlas: unexpected response for %s: %w", path, err)
	}
	return nil
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json, application/xml")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, ErrUnauthorized
	case http.StatusNotFound:
		return nil, ErrNotFound
	default:
		return nil, fmt.Errorf("atlas: GET %s → HTTP %d: %s", path, resp.StatusCode, snippet(body))
	}
}

func title(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(untitled)"
	}
	return s
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
