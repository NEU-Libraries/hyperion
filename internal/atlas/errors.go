package atlas

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// Sentinel errors so callers (and the TUI) can message a bad token, a resource
// the token may not read, and a missing resource distinctly — Atlas keeps 403
// and 404 apart on the wire, and so does hyperion.
var (
	ErrUnauthorized = errors.New("token rejected by Atlas (expired, revoked, or malformed)")
	ErrForbidden    = errors.New("not readable with this token")
	ErrNotFound     = errors.New("not found")
	ErrGone         = errors.New("tombstoned")
	// ErrNotIndexed is a 404 from a Solr debugging route for an object that
	// exists but that Solr holds no document for: an indexing fault, which a
	// reindex fixes.
	ErrNotIndexed = errors.New("not in the search index")
	// ErrBadResponse wraps an answer hyperion could not make sense of: a body
	// that doesn't parse, or a redirect to somewhere unexpected.
	ErrBadResponse = errors.New("unexpected response from Atlas")
)

// HTTPError is any other non-success status, with the body kept so a caller
// can show what Atlas said — a Rails 500 carries the exception it raised.
type HTTPError struct {
	Path   string
	Status int
	Body   []byte
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("atlas: GET %s → HTTP %d: %s", e.Path, e.Status, snippet(e.Body))
}

// Summary is the one line of the body worth showing a person: the exception a
// Rails 500 names ("NoMethodError: undefined method …"), else the body's
// `error` field, else "" when the body says nothing useful.
func (e *HTTPError) Summary() string {
	var body struct {
		Exception string `json:"exception"`
		Error     string `json:"error"`
	}
	if json.Unmarshal(e.Body, &body) != nil {
		return ""
	}
	if ex := strings.TrimSpace(body.Exception); ex != "" {
		return strings.TrimSuffix(strings.TrimPrefix(ex, "#<"), ">")
	}
	if body.Error != http.StatusText(e.Status) {
		return body.Error
	}
	return ""
}

func statusError(path string, status int, body []byte) error {
	switch status {
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		var said struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &said) == nil && said.Error == "not_indexed" {
			return ErrNotIndexed
		}
		return ErrNotFound
	case http.StatusGone:
		return ErrGone
	default:
		return &HTTPError{Path: path, Status: status, Body: body}
	}
}

func badResponse(path string, err error) error {
	return fmt.Errorf("%w for GET %s: %w", ErrBadResponse, path, err)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
