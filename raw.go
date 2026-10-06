package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/NEU-Libraries/hyperion/internal/atlas"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// withRaw gives a pager an r key showing path's raw response, saved as stem.
func (s pagerScreen) withRaw(path, stem string) pagerScreen {
	s.rawPath, s.rawName = path, stem
	return s
}

// withRaw gives a list an r key showing path's raw response, saved as stem.
func (s listScreen) withRaw(path, stem string) listScreen {
	s.rawPath, s.rawName = path, stem
	return s
}

// withRecord points r at the object's own record, already in hand — for the
// views built from it (the record, its ancestors, the PIDs it references).
func (s pagerScreen) withRecord(obj atlas.Object) pagerScreen {
	s = s.withRaw(obj.Path, stem(obj, "record"))
	s.rawBody, s.rawTitle = obj.Raw, "Record"
	return s
}

func (s listScreen) withRecord(obj atlas.Object) listScreen {
	s = s.withRaw(obj.Path, stem(obj, "record"))
	s.rawBody, s.rawTitle = obj.Raw, "Record"
	return s
}

// rawTitled names what r shows when it isn't what the screen shows — one
// revision's pager, whose raw response is every version.
func (s pagerScreen) rawTitled(title string) pagerScreen {
	s.rawTitle = title
	return s
}

// stem is the default file name for a section's download: "w1-mods".
func stem(obj atlas.Object, section string) string {
	return obj.NOID + "-" + strings.ReplaceAll(strings.ToLower(section), " ", "-")
}

// newRawPager shows the body Atlas sends for path — highlighted, with the
// request above it — and offers it for saving byte for byte. With body set
// it shows that instead of asking again (the record, already in hand).
func newRawPager(sess *session, title, path, stem string, body []byte) pagerScreen {
	p := newPagerScreen(sess, title+" (raw)", "GET "+path, func(ctx context.Context, c *atlas.Client) (doc, error) {
		if body == nil {
			var err error
			if body, err = c.Raw(ctx, path); err != nil {
				return doc{}, err
			}
		}
		return doc{render: responseRender(body), raw: body}, nil
	})
	p.rawName, p.isRaw = stem, true
	return p
}

// saveState is a pager's save prompt: the filename being typed, then what
// became of the save.
type saveState struct {
	active bool
	input  textinput.Model
	notice string // the outcome, shown in the footer until the next key
	failed bool
}

// start opens the prompt, offering ./<stem>.<ext> for the document's format.
func (v *saveState) start(stem string, raw []byte) tea.Cmd {
	in := textinput.New()
	in.Prompt = "Save to: "
	in.SetValue("./" + stem + extensionFor(raw))
	in.CursorEnd()
	in.Focus()
	v.active, v.input, v.notice = true, in, ""
	return textinput.Blink
}

func (v saveState) update(msg tea.KeyMsg, raw []byte) (saveState, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		v.active, v.notice = false, ""
		return v, nil
	case tea.KeyEnter:
		name := strings.TrimSpace(v.input.Value())
		if name == "" {
			return v, nil
		}
		path, err := saveNew(name, raw)
		if err != nil {
			// Keep the prompt open so the name can be fixed.
			v.notice, v.failed = saveProblem(name, err), true
			return v, nil
		}
		v.active = false
		v.notice, v.failed = fmt.Sprintf("Saved %s to %s", humanBytes(float64(len(raw))), path), false
		return v, nil
	}
	v.notice = ""
	var cmd tea.Cmd
	v.input, cmd = v.input.Update(msg)
	return v, cmd
}

func (v saveState) view() string {
	notice := ""
	if v.notice != "" {
		style := okStyle
		if v.failed {
			style = errStyle
		}
		notice = style.Render(v.notice)
	}
	if !v.active {
		return notice
	}
	// One line either way, so the footer never grows and pushes the screen up:
	// the problem with the name, while there is one, in place of the key help.
	if notice != "" {
		return v.input.View() + "  " + notice
	}
	return v.input.View() + dimStyle.Render("  enter to save · esc to cancel")
}

// saveNew writes raw to name exactly as Atlas sent it, and never over an
// existing file. A leading ~/ means the home directory.
func saveNew(name string, raw []byte) (string, error) {
	if rest, ok := strings.CutPrefix(name, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		name = filepath.Join(home, rest)
	}
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return "", err
	}
	return name, f.Close()
}

func saveProblem(name string, err error) string {
	switch {
	case errors.Is(err, fs.ErrExist):
		return name + " already exists. Choose another name."
	case errors.Is(err, fs.ErrNotExist):
		return "The folder for " + name + " doesn't exist."
	case errors.Is(err, fs.ErrPermission):
		return "You don't have permission to write " + name + "."
	}
	return "Couldn't save: " + err.Error()
}

// extensionFor names a document's format by its content.
func extensionFor(raw []byte) string {
	switch {
	case json.Valid(raw):
		return ".json"
	case strings.HasPrefix(strings.TrimSpace(string(raw)), "<"):
		return ".xml"
	}
	return ".txt"
}
