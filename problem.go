package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"hyperion/internal/atlas"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// problem is a failure worded for a person: what hyperion was doing, why it
// failed in plain words, and — for a bug report — the technical detail and
// the full response Atlas sent, when there was one.
type problem struct {
	headline string   // "Couldn't open PID w0vt6v0."
	reason   string   // "Atlas hit an internal error while answering. …"
	details  []string // dimmed: the Rails exception, the request
	raw      []byte   // Atlas's full response body, shown on request
}

// explain words err under headline. A userErr is already worded by the caller
// for its own context; everything else gets the generic wording for its kind.
func explain(headline string, err error) problem {
	p := problem{headline: headline}
	var (
		ue   userErr
		he   *atlas.HTTPError
		nerr net.Error
	)
	switch {
	case errors.As(err, &ue):
		p.reason = string(ue)
	case errors.Is(err, atlas.ErrUnauthorized):
		p.reason = "Atlas rejected the access token: it may have expired or been revoked. " +
			`Generate a new one in DRS → My DRS → "Programmatic access".`
	case errors.Is(err, atlas.ErrForbidden):
		p.reason = "This token may not read it."
	case errors.Is(err, atlas.ErrNotFound):
		p.reason = "Atlas has no record of it."
	case errors.Is(err, atlas.ErrGone):
		p.reason = "It is tombstoned, so Atlas no longer serves this."
	case errors.As(err, &he):
		p.reason = statusReason(he.Status)
		if s := he.Summary(); s != "" {
			p.details = append(p.details, s)
		}
		p.details = append(p.details, fmt.Sprintf("GET %s → HTTP %d %s", he.Path, he.Status, http.StatusText(he.Status)))
		if len(he.Body) > 0 {
			p.raw = he.Body
		}
	case errors.Is(err, atlas.ErrBadResponse):
		p.reason = "Atlas sent an answer Hyperion couldn't read. This is likely a mismatch between Hyperion and this version of Atlas."
		p.details = append(p.details, strings.TrimPrefix(err.Error(), atlas.ErrBadResponse.Error()+" "))
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &nerr) && nerr.Timeout():
		p.reason = "Atlas didn't answer in time. It may be busy or down; try again in a few minutes."
		p.details = append(p.details, err.Error())
	case errors.As(err, &nerr):
		p.reason = "Hyperion couldn't reach Atlas. Check the Atlas URL and your network connection."
		p.details = append(p.details, err.Error())
	default:
		p.reason = "Something went wrong."
		p.details = append(p.details, err.Error())
	}
	return p
}

// statusReason says what an unexpected HTTP status means for the user.
func statusReason(status int) string {
	switch {
	case status == http.StatusBadGateway, status == http.StatusServiceUnavailable, status == http.StatusGatewayTimeout:
		return fmt.Sprintf("Atlas is unavailable right now (HTTP %d). Try again in a few minutes.", status)
	case status >= 500:
		return fmt.Sprintf("Atlas hit an internal error (HTTP %d) while answering. "+
			"This is a fault in Atlas, not in your token or the PID. "+
			"The detail below will help whoever looks after Atlas.", status)
	default:
		return fmt.Sprintf("Atlas refused the request (HTTP %d).", status)
	}
}

// backKeys is the usual way off an error screen.
const backKeys = "enter or esc to go back"

// view renders the problem wrapped to width (0 = unwrapped), ending with the
// screen's way out (keys, e.g. backKeys) and, when Atlas sent a response, d.
func (p problem) view(width int, keys string) string {
	wrap := func(s string, indent int) string {
		if width <= indent+10 {
			return strings.Repeat(" ", indent) + s
		}
		return lipgloss.NewStyle().Width(width - indent).PaddingLeft(indent).Render(s)
	}
	var b strings.Builder
	b.WriteString(errStyle.Render("✗ ") + p.headline + "\n")
	b.WriteString(wrap(p.reason, 2) + "\n")
	if len(p.details) > 0 {
		b.WriteString("\n")
		for _, d := range p.details {
			b.WriteString(dimStyle.Render(wrap(d, 2)) + "\n")
		}
	}
	if p.raw != nil {
		keys = "d to see Atlas's full response · " + keys
	}
	b.WriteString("\n" + dimStyle.Render(keys))
	return b.String()
}

// problemKey handles a key press on a screen showing p: d opens Atlas's full
// response when there is one, and enter, esc, or q leaves via leave. Other
// keys do nothing, so typing that runs on past an error can't skip screens.
func problemKey(sess *session, p problem, msg tea.KeyMsg, leave tea.Cmd) tea.Cmd {
	if isBackKey(msg) {
		return leave
	}
	if msg.String() == "d" && p.raw != nil {
		pg := newPagerScreen(sess, "Atlas's response", p.headline, func(context.Context, *atlas.Client) (doc, error) {
			return doc{render: responseRender(p.raw), raw: p.raw}, nil
		})
		pg.rawName = "atlas-error"
		return push(pg)
	}
	return nil
}

// isBackKey reports the keys that leave a screen showing a result: enter,
// esc, and q.
func isBackKey(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "enter", "esc", "q":
		return true
	}
	return false
}

// briefError is the few words a list row or summary line has room for.
func briefError(err error) string {
	var he *atlas.HTTPError
	switch {
	case errors.Is(err, atlas.ErrForbidden):
		return "not readable"
	case errors.Is(err, atlas.ErrNotFound):
		return "not found"
	case errors.Is(err, atlas.ErrGone):
		return "tombstoned"
	case errors.Is(err, atlas.ErrUnauthorized):
		return "token rejected"
	case errors.As(err, &he):
		return fmt.Sprintf("Atlas error (HTTP %d)", he.Status)
	}
	return "failed"
}

// responseRender shows an error body highlighted as whatever it is: JSON from
// the API, XML, or (say, a proxy's HTML page) plain text.
func responseRender(body []byte) renderFunc {
	trimmed := strings.TrimSpace(string(body))
	switch {
	case json.Valid(body):
		return jsonRender(indentJSON(body))
	case strings.HasPrefix(trimmed, "<?xml"):
		return xmlRender(trimmed)
	}
	return textRender(trimmed)
}
