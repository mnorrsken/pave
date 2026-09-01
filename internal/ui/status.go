package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// statusBar is the two bottom rows: one line of state or the last error, and
// one line of the key bindings that are live for whatever has focus.
type statusBar struct {
	*tview.Flex

	line *tview.TextView
	keys *tview.TextView

	message string
	color   tcell.Color
}

func newStatusBar() *statusBar {
	s := &statusBar{
		Flex:  tview.NewFlex().SetDirection(tview.FlexRow),
		line:  tview.NewTextView().SetDynamicColors(true),
		keys:  tview.NewTextView().SetDynamicColors(true),
		color: colorText,
	}
	s.line.SetBackgroundColor(colorBackground)
	s.keys.SetBackgroundColor(colorBackground)
	s.Flex.SetBackgroundColor(colorBackground)
	s.AddItem(s.line, 1, 0, false).AddItem(s.keys, 1, 0, false)
	return s
}

func (s *statusBar) setKeys(hints string) { s.keys.SetText(hints) }

func (s *statusBar) info(format string, args ...any)   { s.set(colorText, format, args...) }
func (s *statusBar) ok(format string, args ...any)     { s.set(colorOK, format, args...) }
func (s *statusBar) warn(format string, args ...any)   { s.set(colorWarn, format, args...) }
func (s *statusBar) errorf(format string, args ...any) { s.set(colorError, format, args...) }

func (s *statusBar) set(color tcell.Color, format string, args ...any) {
	s.message = fmt.Sprintf(format, args...)
	s.color = color
	s.line.SetText(fmt.Sprintf("[%s]%s[-]", tag(s.color), tview.Escape(s.message)))
}

// text is what the status line currently says, for tests.
func (s *statusBar) text() string { return s.message }
