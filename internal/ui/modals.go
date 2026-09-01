package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// center puts a primitive in the middle of the screen at a fixed size.
func center(p tview.Primitive, width, height int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(p, height, 0, true).
			AddItem(nil, 0, 1, false), width, 0, true).
		AddItem(nil, 0, 1, false)
}

// centerLarge is for dialogs that should grow with the terminal: a host list,
// the help.
func centerLarge(p tview.Primitive, maxWidth int) tview.Primitive {
	return tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 2, 0, false).
			AddItem(p, 0, 1, true).
			AddItem(nil, 2, 0, false), maxWidth, 0, true).
		AddItem(nil, 0, 1, false)
}

func frame(p tview.Primitive, title string) tview.Primitive {
	if b, ok := p.(interface {
		SetBorder(bool) *tview.Box
		SetTitle(string) *tview.Box
	}); ok {
		b.SetBorder(true)
		b.SetTitle(" " + title + " ")
	}
	return p
}

// messageBox shows one message with a single way out.
func messageBox(title, message string, color tcell.Color, onClose func()) tview.Primitive {
	m := tview.NewModal().
		SetText(message).
		AddButtons([]string{"ok"}).
		SetDoneFunc(func(int, string) { onClose() })
	m.SetTextColor(color)
	m.SetTitle(" " + title + " ").SetBorder(true)
	return m
}

// confirmBox asks before something that cannot be taken back.
func confirmBox(title, message string, onYes, onNo func()) tview.Primitive {
	m := tview.NewModal().
		SetText(message).
		AddButtons([]string{"no", "yes"}).
		SetDoneFunc(func(_ int, label string) {
			if label == "yes" {
				onYes()
				return
			}
			onNo()
		})
	m.SetTitle(" " + title + " ").SetBorder(true)
	return m
}

// promptBox asks for one value. It is a bare input field rather than a form:
// with one field, enter should mean "yes, that one" and not "move to the ok
// button".
func promptBox(title, label, value, hint string, onSubmit func(string), onCancel func()) tview.Primitive {
	input := tview.NewInputField().SetLabel(label + ": ").SetText(value)
	input.SetFieldBackgroundColor(colorBackground)
	input.SetBackgroundColor(colorBackground)
	input.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEnter:
			onSubmit(input.GetText())
		case tcell.KeyEscape:
			onCancel()
		}
	})
	t := title
	if hint != "" {
		t += " — " + hint
	}
	input.SetBorder(true).SetTitle(" " + t + " — enter accepts, esc cancels ")
	return input
}

// helpView is the key list. It is the only documentation the app itself has,
// so it lists everything.
func helpView(onClose func()) tview.Primitive {
	t := tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	t.SetBackgroundColor(colorBackground)
	t.SetText(helpText)
	t.SetDoneFunc(func(tcell.Key) { onClose() })
	t.SetBorder(true).SetTitle(" keys ")
	return t
}

const helpText = `
  [::b]browsing[-::-]
    up/down        move
    enter          on a playbook: fill in the run form
    /              filter the tree
    r              rescan the root for projects and playbooks
    tab            move between the tree, the form and the output

  [::b]running[-::-]
    F5             run the playbook with the options in the form
    F2 or L        pick the limit out of the inventory
    F3             credentials for a host that has no certificate yet
    i              reload the inventory of the selected project
    ctrl-c         interrupt the run (a second one kills it)
    s              save the output of a finished run to a file
    esc            leave the output; the run keeps going, tab comes back

  While a run is going, what you type goes to ansible: its vault, become and
  host key prompts work as they do in a terminal. Page up/down and the arrow
  keys stay here for scrolling.

  [::b]certificates[-::-]
    c              sign a short lived certificate and load it into ssh-agent

  [::b]anywhere[-::-]
    ?              this help
    q              quit
`
