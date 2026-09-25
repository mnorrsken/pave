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

// runConfirmBox is the last thing between the options and a run. Check mode
// is a choice here rather than a checkbox in the form, so a dry run is one
// key away and can never be left ticked from the last time.
func runConfirmBox(playbook string, onRun, onCheck, onCancel func()) tview.Primitive {
	m := tview.NewModal().
		SetText("Run " + playbook + "?").
		AddButtons([]string{"run", "check mode", "cancel"}).
		SetDoneFunc(func(_ int, label string) {
			switch label {
			case "run":
				onRun()
			case "check mode":
				onCheck()
			default:
				// The cancel button, and escape, which comes through with no
				// label at all.
				onCancel()
			}
		})
	m.SetTitle(" run ").SetBorder(true)
	return m
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
    enter          on a playbook: the run options
    /              filter the tree
    r              rescan the root for projects and playbooks
    tab            move between the tree and the playbook

  The pane on the right is what the playbook would do: every play, the hosts
  its pattern resolves to in the project's inventory, the roles it applies and
  the modules its own tasks call. The inventory is read in the background the
  first time a project is selected.

  [::b]running[-::-]
    F5             the run options for the selected playbook
    F5 again       ask whether to run it, run it in check mode, or not
    F2 or L        pick the limit out of the inventory
    F3             credentials for a host that has no certificate yet
    esc            close the options without running anything

  A run has the terminal to itself, as if ansible-playbook had been typed in
  a shell: its prompts, ctrl-c and the terminal's scrollback all work as they
  do there. When it ends, esc, q or enter comes back here, and the status line
  says how it went.

  Every playbook run is also written to .pave/logs in the root, named after
  the playbook and the time it started.

  [::b]the inventory[-::-]
    i              the inventory of the selected project: the files it is
                   made of, its groups and its hosts
    enter          on a file: open it in the editor. A sops or ansible-vault
                   file is decrypted first and encrypted again afterwards.
                   A file that is not there yet is created.
    enter          on a group or a host: its files
    r              read the inventory again
    esc            back to the playbooks

  pave gives the terminal back while the editor has it, so a full screen
  editor works as it does anywhere else. The editor is $VISUAL, then $EDITOR,
  then vi, unless the config names one.

  [::b]certificates[-::-]
    c              sign a short lived certificate and load it into ssh-agent

  [::b]anywhere[-::-]
    ?              this help
    q              quit
`
