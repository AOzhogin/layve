package ui

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/AOzhogin/layve/internal/domain/image"
)

const tabWidth = 4

// linkSuffix returns how a link is shown after its name in the tree.
func linkSuffix(n *changeTree) (arrow, target string, style lipgloss.Style) {
	if n.Value == nil {
		return "", "", lipgloss.Style{}
	}
	l := n.Value.Link()
	switch {
	case l.IsZero():
		return "", "", lipgloss.Style{}
	case l.Hard():
		return " ⇒ ", "/" + l.Target(), linkStyle
	case l.Broken():
		return " ↛ ", l.Target(), brokenStyle
	default:
		return " → ", l.Target(), linkStyle
	}
}

// infoHeader describes the node under the cursor.
func (m *LayersModel) infoHeader(width int) []string {
	n := m.currentNode()
	if n == nil {
		return nil
	}
	lines := []string{boldStyle.Render(ansi.Truncate("/"+n.Path, width, "…"))}
	if n.Value == nil {
		return append(lines, subtleStyle.Render("unchanged directory with changes inside"))
	}
	c := n.Value
	about := " · " + fileType(*c)
	if !c.Dir() && c.Link().IsZero() && c.Kind() != image.ChangeDeleted {
		about += " · " + c.Size().String()
	}
	if n.Dir() && len(n.Children) > 0 {
		about += fmt.Sprintf(" · %d entries", len(n.Children))
	}
	lines = append(lines, kindStyle(c.Kind()).Strikethrough(false).Render(c.Kind().String())+subtleStyle.Render(about))
	if c.Kind() == image.ChangeDeleted {
		return append(lines, subtleStyle.Render("removed by this layer"))
	}

	l := c.Link()
	switch {
	case l.IsZero():
	case l.Hard():
		lines = append(lines, "hard link to "+linkStyle.Render("/"+l.Target()))
	default:
		lines = append(lines, "symlink → "+linkStyle.Render(ansi.Truncate(l.Target(), width-10, "…")))
		if l.Broken() {
			lines = append(lines, brokenStyle.Render(ansi.Truncate("broken: /"+l.Resolved()+" does not exist", width, "…")))
		} else {
			lines = append(lines, "resolves to "+linkStyle.Render(ansi.Truncate("/"+l.Resolved(), width-12, "…")))
		}
	}
	return lines
}

func fileType(c image.FileChange) string {
	switch {
	case c.Dir():
		return "directory"
	case !c.Link().IsZero() && c.Link().Hard():
		return "hard link"
	case !c.Link().IsZero():
		return "symlink"
	case c.Mode()&0o111 != 0:
		return "executable"
	default:
		return "file"
	}
}

// previewLines returns the content to show under the header, or a reason.
func (m *LayersModel) previewLines() (lines []string, text bool) {
	n := m.currentNode()
	if n == nil || n.Value == nil || n.Dir() || n.Value.Kind() == image.ChangeDeleted || !n.Value.Link().IsZero() {
		return nil, false
	}
	p := n.Value.Preview()
	switch p.State() {
	case image.PreviewText:
		return strings.Split(strings.TrimSuffix(sanitize(p.Text()), "\n"), "\n"), true
	case image.PreviewBinary:
		return []string{"binary file"}, false
	case image.PreviewTooLarge:
		return []string{"too large to preview (over 64kB)"}, false
	case image.PreviewSkipped:
		return []string{"not kept: preview memory limit for this image reached"}, false
	default:
		return []string{"empty file"}, false
	}
}

// sanitize expands tabs and drops control characters so file content can
// never inject terminal escape sequences.
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\t", strings.Repeat(" ", tabWidth))
	return strings.Map(func(r rune) rune {
		if r == '\n' {
			return r
		}
		if unicode.IsControl(r) {
			return '·'
		}
		return r
	}, s)
}

func (m *LayersModel) infoView(width, height int) string {
	if m.dialog.kind != dialogNone {
		return m.dialogView(width)
	}
	if m.details == nil || m.currentNode() == nil {
		return helpStyle.Render("Select a file to see details.")
	}
	header := m.infoHeader(width)
	body, text := m.previewLines()
	if body == nil {
		return strings.Join(header, "\n")
	}
	header = append(header, subtleStyle.Render(strings.Repeat("─", width)))
	area := max(height-len(header), 1)
	if !text {
		return strings.Join(append(header, helpStyle.Render(body[0])), "\n")
	}

	m.infoOffset = max(0, min(m.infoOffset, len(body)-area))
	gutter := len(strconv.Itoa(len(body)))
	out := header
	for i := m.infoOffset; i < min(m.infoOffset+area, len(body)); i++ {
		num := subtleStyle.Render(fmt.Sprintf("%*d ", gutter, i+1))
		out = append(out, num+ansi.Truncate(body[i], width-gutter-1, "…"))
	}
	return strings.Join(out, "\n")
}

func (m *LayersModel) scrollInfo(k string) {
	page := max(m.infoHeight()-4, 1)
	switch k {
	case "up", "k":
		m.infoOffset--
	case "down", "j":
		m.infoOffset++
	case "pgup", "ctrl+u":
		m.infoOffset -= page
	case "pgdown", "ctrl+d", "space":
		m.infoOffset += page
	case "home", "g":
		m.infoOffset = 0
	case "end", "G":
		m.infoOffset = 1 << 30 // clamped when rendering
	}
	m.infoOffset = max(m.infoOffset, 0)
}
