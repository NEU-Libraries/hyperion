package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Syntax colours for the pager. They go through lipgloss like every other
// style, so they drop away on a terminal without colour (or with NO_COLOR).
var (
	synPunct   = dimStyle                                              // < > / = { } [ ] , :
	synElement = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))  // element names, JSON keys
	synAttr    = lipgloss.NewStyle().Foreground(lipgloss.Color("180")) // attribute names
	synString  = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // attribute values, JSON strings
	synLiteral = lipgloss.NewStyle().Foreground(lipgloss.Color("176")) // entities, numbers, true/false/null
	synComment = lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Italic(true)
	synDecl    = lipgloss.NewStyle().Foreground(lipgloss.Color("140")) // <?xml …?>, <!DOCTYPE …>, CDATA
)

// paint colours s line by line. Style.Render would pad a multi-line token
// into a block and expand its tabs, changing the text; this only adds colour.
func paint(style lipgloss.Style, s string) string {
	style = style.TabWidth(lipgloss.NoTabConversion)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = style.Render(l)
		}
	}
	return strings.Join(lines, "\n")
}

// highlightXML colours an XML document token by token. It never changes the
// text itself — strip the colour and the input comes back — and anything it
// can't make sense of (a stray "<", an unterminated comment) passes through
// as plain text rather than failing.
func highlightXML(src string) string {
	var b strings.Builder
	for len(src) > 0 {
		lt := strings.IndexByte(src, '<')
		if lt < 0 {
			b.WriteString(highlightText(src))
			break
		}
		b.WriteString(highlightText(src[:lt]))
		src = src[lt:]

		var n int
		switch {
		case strings.HasPrefix(src, "<!--"):
			n = spanTo(src, "-->")
			b.WriteString(paint(synComment, src[:n]))
		case strings.HasPrefix(src, "<![CDATA["):
			n = spanTo(src, "]]>")
			b.WriteString(paint(synDecl, src[:n]))
		case strings.HasPrefix(src, "<?"), strings.HasPrefix(src, "<!"):
			n = spanTo(src, ">")
			b.WriteString(paint(synDecl, src[:n]))
		default:
			var tag string
			tag, n = highlightTag(src)
			b.WriteString(tag)
		}
		src = src[n:]
	}
	return b.String()
}

// spanTo is the length of src up to and including end, or all of src.
func spanTo(src, end string) int {
	if i := strings.Index(src, end); i >= 0 {
		return i + len(end)
	}
	return len(src)
}

// highlightTag colours one tag starting at src[0] == '<' and says how much of
// src it consumed. A "<" that doesn't open a tag is emitted as plain text.
func highlightTag(src string) (string, int) {
	end := tagEnd(src)
	if end < 0 || end < 2 || !isNameStart(src[1]) && src[1] != '/' {
		return "<", 1
	}
	tag := src[:end]

	var b strings.Builder
	i := 1
	b.WriteString(paint(synPunct, "<"))
	if tag[i] == '/' {
		b.WriteString(paint(synPunct, "/"))
		i++
	}
	j := scanName(tag, i)
	b.WriteString(paint(synElement, tag[i:j]))
	i = j

	for i < len(tag)-1 {
		c := tag[i]
		switch {
		case isSpace(c):
			j = i
			for j < len(tag) && isSpace(tag[j]) {
				j++
			}
			b.WriteString(tag[i:j])
		case c == '/' || c == '=':
			b.WriteString(paint(synPunct, string(c)))
			j = i + 1
		case c == '"' || c == '\'':
			j = strings.IndexByte(tag[i+1:], c)
			if j < 0 {
				j = len(tag) - 1
			} else {
				j += i + 2
			}
			b.WriteString(paint(synString, tag[i:j]))
		case isNameStart(c):
			j = scanName(tag, i)
			b.WriteString(paint(synAttr, tag[i:j]))
		default:
			j = i + 1
			b.WriteString(tag[i:j])
		}
		i = j
	}
	b.WriteString(paint(synPunct, ">"))
	return b.String(), end
}

// tagEnd finds the ">" closing the tag at src[0], skipping any inside quoted
// attribute values; -1 if there is none.
func tagEnd(src string) int {
	var quote byte
	for i := 1; i < len(src); i++ {
		c := src[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i + 1
		case c == '<':
			return -1
		}
	}
	return -1
}

// highlightText colours the entities (&amp; &#x2014; …) in character data.
func highlightText(s string) string {
	var b strings.Builder
	for {
		amp := strings.IndexByte(s, '&')
		if amp < 0 {
			b.WriteString(s)
			return b.String()
		}
		semi := strings.IndexByte(s[amp:], ';')
		if semi < 0 || semi > 12 || strings.ContainsAny(s[amp:amp+semi], " \t\n<") {
			b.WriteString(s[:amp+1])
			s = s[amp+1:]
			continue
		}
		b.WriteString(s[:amp] + paint(synLiteral, s[amp:amp+semi+1]))
		s = s[amp+semi+1:]
	}
}

func scanName(s string, i int) int {
	for i < len(s) && (isNameStart(s[i]) || s[i] >= '0' && s[i] <= '9' || s[i] == '-' || s[i] == '.') {
		i++
	}
	return i
}

func isNameStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == ':' || c >= 0x80
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// highlightJSON colours JSON text — keys, strings, numbers, literals, and
// punctuation — without re-formatting it. Like highlightXML it only adds
// colour, and passes anything unexpected through unchanged.
func highlightJSON(src string) string {
	var b strings.Builder
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(src))
			style := synString
			if isKey(src[j:]) {
				style = synElement
			}
			b.WriteString(paint(style, src[i:j]))
			i = j
		case strings.ContainsRune("{}[],:", rune(c)):
			b.WriteString(paint(synPunct, string(c)))
			i++
		case c == '-' || c >= '0' && c <= '9':
			j := i + 1
			for j < len(src) && strings.ContainsRune("0123456789.eE+-", rune(src[j])) {
				j++
			}
			b.WriteString(paint(synLiteral, src[i:j]))
			i = j
		case strings.HasPrefix(src[i:], "true"), strings.HasPrefix(src[i:], "null"):
			b.WriteString(paint(synLiteral, src[i:i+4]))
			i += 4
		case strings.HasPrefix(src[i:], "false"):
			b.WriteString(paint(synLiteral, src[i:i+5]))
			i += 5
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// isKey reports whether the string just scanned is an object key: the next
// non-space character is a colon.
func isKey(rest string) bool {
	rest = strings.TrimLeft(rest, " \t\r\n")
	return strings.HasPrefix(rest, ":")
}

// xmlRender and jsonRender show a document highlighted, trailing whitespace
// trimmed. The colouring is done once, not on every resize.
func xmlRender(text string) renderFunc {
	return textRender(highlightXML(strings.TrimRight(text, " \n\t\r")))
}

func jsonRender(text string) renderFunc {
	return textRender(highlightJSON(strings.TrimRight(text, " \n\t\r")))
}
