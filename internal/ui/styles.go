package ui

import (
	"charm.land/bubbles/v2/textinput"
	"charm.land/lipgloss/v2"
)

var (
	colorAccent   = lipgloss.Color("39")
	colorMuted    = lipgloss.Color("241")
	colorBorder   = lipgloss.Color("238")
	colorAdded    = lipgloss.Color("42")
	colorModified = lipgloss.Color("214")
	colorDeleted  = lipgloss.Color("196")
	colorDir      = lipgloss.Color("75")
	colorSelected = lipgloss.Color("229")
	colorCursorBg = lipgloss.Color("57")

	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Padding(0, 1)
	subtleStyle  = lipgloss.NewStyle().Foreground(colorMuted)
	helpStyle    = lipgloss.NewStyle().Foreground(colorMuted).Padding(0, 1)
	errStyle     = lipgloss.NewStyle().Foreground(colorDeleted).Padding(0, 1)
	okStyle      = lipgloss.NewStyle().Foreground(colorAdded).Padding(0, 1)
	markStyle    = lipgloss.NewStyle().Foreground(colorSelected).Bold(true)
	cursorStyle  = lipgloss.NewStyle().Foreground(colorSelected).Background(colorCursorBg)
	dialogStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorAccent).Padding(0, 1)
	addedStyle   = lipgloss.NewStyle().Foreground(colorAdded)
	modStyle     = lipgloss.NewStyle().Foreground(colorModified)
	deletedStyle = lipgloss.NewStyle().Foreground(colorDeleted).Strikethrough(true)
	dirStyle     = lipgloss.NewStyle().Foreground(colorDir).Bold(true)
)

func paneStyle(focused bool) lipgloss.Style {
	c := colorBorder
	if focused {
		c = colorAccent
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c)
}

var (
	colorLink   = lipgloss.Color("44")
	linkStyle   = lipgloss.NewStyle().Foreground(colorLink)
	brokenStyle = lipgloss.NewStyle().Foreground(colorDeleted)
	boldStyle   = lipgloss.NewStyle().Bold(true)
)

// cursorBlink makes text inputs blink their cursor; tests turn it off
// because each blink is a timer.
var cursorBlink = true

// newInput returns a text input with layve's defaults.
func newInput() textinput.Model {
	in := textinput.New()
	s := in.Styles()
	s.Cursor.Blink = cursorBlink
	in.SetStyles(s)
	return in
}
