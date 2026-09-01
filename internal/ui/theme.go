// Package ui is pave's terminal interface. It reaches ansible only through
// the run, inv and sshcert packages, all of which are injectable, which is
// what lets the whole thing be driven from a test with nothing installed.
package ui

import "github.com/gdamore/tcell/v2"

// The palette leaves the background at the terminal default, so pave inherits
// whatever theme the terminal already has instead of stamping its own
// rectangle over it.
var (
	colorBackground = tcell.ColorDefault
	colorText       = tcell.ColorDefault
	colorBorder     = tcell.ColorGray
	colorTitle      = tcell.ColorTeal
	colorAccent     = tcell.ColorTeal
	colorDim        = tcell.ColorGray
	colorError      = tcell.ColorRed
	colorWarn       = tcell.ColorYellow
	colorOK         = tcell.ColorGreen
	colorSelected   = tcell.ColorBlue
)

// Markers are ASCII: this is a tool for a console over ssh as much as for a
// terminal with a font that has everything. No square brackets in any of
// them — every one of these strings is drawn through tview's tag parser,
// which would take "[x]" for a colour.
const (
	iconProject  = "#"
	iconDir      = "+"
	iconPlaybook = "*"
	markOn       = "(x)"
	markOff      = "( )"
)

// tag renders a colour as a tview colour tag. The terminal default has no
// name, and "-" is tview's spelling of "reset to default".
func tag(c tcell.Color) string {
	if c == tcell.ColorDefault {
		return "-"
	}
	return c.Name()
}
