package ui

import (
	"strings"

	"github.com/rivo/tview"

	"github.com/mnorrsken/pave/internal/config"
	"github.com/mnorrsken/pave/internal/run"
)

// Form item positions. tview forms are addressed by index, so the names live
// here rather than being spelled out at every call site.
const (
	fieldDiff = iota
	fieldVerbose
	fieldLimit
	fieldTags
	fieldSkipTags
	fieldExtraVars
	fieldExtraArgs
	fieldCount
)

// formHeight is the room the run form needs. See formBoxHeight.
const formHeight = fieldCount + formExtraRows

// credentials are for a host that cannot be reached with the operator
// certificate yet — a machine being onboarded, typically.
type credentials struct {
	User     string
	Password string
	Become   string
	// Ask leaves the asking to ansible, on the pty, so no password passes
	// through pave at all.
	Ask bool
	// Target is an address that is not in the inventory.
	Target string
}

// used reports whether anything in the credentials has to reach the command
// line.
func (c credentials) used() bool {
	return c.User != "" || c.Password != "" || c.Become != "" || c.Ask || c.Target != ""
}

// summary is the one-line description shown under the form.
func (c credentials) summary() string {
	if !c.used() {
		return ""
	}
	var parts []string
	if c.Target != "" {
		parts = append(parts, "target "+c.Target)
	}
	if c.User != "" {
		parts = append(parts, "as "+c.User)
	}
	switch {
	case c.Ask:
		parts = append(parts, "ansible prompts for the passwords")
	case c.Password != "" && c.Become != "":
		parts = append(parts, "ssh and become passwords set")
	case c.Password != "":
		parts = append(parts, "ssh password set")
	case c.Become != "":
		parts = append(parts, "become password set")
	}
	return strings.Join(parts, ", ")
}

// runForm is the options pane: what to pass to ansible-playbook.
type runForm struct {
	*tview.Form

	// verbosity is what the verbose checkbox is worth when it is ticked. The
	// level is a setting, not something to pick before every run.
	verbosity int

	creds   credentials
	changed func()
}

func newRunForm(d config.Defaults, onRun, onHosts, onCreds, onCancel func()) *runForm {
	f := &runForm{Form: tview.NewForm(), verbosity: d.Verbosity}
	f.SetBackgroundColor(colorBackground)
	f.SetFieldBackgroundColor(colorBackground)
	f.SetBorder(true).SetBorderColor(colorBorder).SetTitleColor(colorTitle).SetTitle(" run options ")
	compactForm(f.Form)

	notify := func(string) {
		if f.changed != nil {
			f.changed()
		}
	}
	f.AddCheckbox("diff", d.Diff == nil || *d.Diff, func(bool) { notify("") })
	f.AddCheckbox("verbose ("+verbosityFlag(f.verbosity)+")", d.Verbose, func(bool) { notify("") })
	f.AddInputField("limit", "", 0, nil, notify)
	f.AddInputField("tags", "", 0, nil, notify)
	f.AddInputField("skip tags", "", 0, nil, notify)
	f.AddInputField("extra vars", "", 0, nil, notify)
	f.AddInputField("extra args", "", 0, nil, notify)

	for i, b := range []func(){onRun, onHosts, onCreds, onCancel} {
		f.AddButton(runFormButtons[i], b)
	}

	showCheckbox(f.checkbox(fieldDiff))
	showCheckbox(f.checkbox(fieldVerbose))
	return f
}

// runFormButtons are the form's buttons, in order. tview numbers them after
// the items, so the run button is at fieldCount.
var runFormButtons = []string{"run", "hosts…", "credentials…", "cancel"}

// buttonRun is the run button's position.
const buttonRun = fieldCount

// focusRun puts the keyboard on the run button. The options open there
// because most runs want them as they are: enter runs, tab reaches the
// fields.
func (f *runForm) focusRun() { f.SetFocus(buttonRun) }

func (f *runForm) runButton() *tview.Button { return f.GetButton(buttonRun - fieldCount) }

// verbosityFlag is the flag a verbosity level spells, for the checkbox label.
func verbosityFlag(level int) string {
	if level < 1 {
		level = 1
	}
	if level > 4 {
		level = 4
	}
	return "-" + strings.Repeat("v", level)
}

// previewRows is the box under the form in the run dialog: two border rows
// and room for a command line that wraps.
const previewRows = 5

// runOptionsHeight is how tall the whole dialog has to be.
const runOptionsHeight = formHeight + previewRows

// runOptionsBox is the dialog the options are filled in before a run: the
// form, and the command it currently describes.
func runOptionsBox(f *runForm, preview *tview.TextView) tview.Primitive {
	preview.SetBorder(true).SetBorderColor(colorBorder).SetTitleColor(colorTitle).SetTitle(" command ")
	return tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(f, formHeight, 0, true).
		AddItem(preview, previewRows, 0, false)
}

// formExtraRows is what a form needs on top of one row per item: the blank
// row tview leaves before the buttons, the button row itself, and the two
// border rows. It only adds up with compactForm's padding.
const formExtraRows = 4

// formBoxHeight is how tall a box has to be to show the whole of a form.
func formBoxHeight(items int) int { return items + formExtraRows }

// compactForm turns off the padding tview would otherwise put above and
// below, so a dialog can be exactly as tall as its contents.
func compactForm(f *tview.Form) {
	f.SetBackgroundColor(colorBackground)
	f.SetFieldBackgroundColor(colorBackground)
	f.SetBorderPadding(0, 0, 1, 1)
	f.SetItemPadding(0)
}

// showCheckbox gives a checkbox something to see. tview draws an unchecked
// box as a single space, which on a terminal-default background is nothing at
// all. No square brackets: the strings go through the tag parser.
func showCheckbox(c *tview.Checkbox) {
	c.SetUncheckedString(markOff).SetCheckedString(markOn)
}

func (f *runForm) input(i int) *tview.InputField {
	return f.GetFormItem(i).(*tview.InputField)
}

func (f *runForm) checkbox(i int) *tview.Checkbox {
	return f.GetFormItem(i).(*tview.Checkbox)
}

func (f *runForm) limit() string { return f.input(fieldLimit).GetText() }

func (f *runForm) setLimit(s string) {
	f.input(fieldLimit).SetText(s)
	if f.changed != nil {
		f.changed()
	}
}

func (f *runForm) setCredentials(c credentials) {
	f.creds = c
	if f.changed != nil {
		f.changed()
	}
}

// apply copies the form's options onto a spec. Check mode is not one of them:
// it is answered in the confirmation, so it cannot be left on by mistake. The
// password files are not set here either: they only exist for the length of
// one run, and the caller writes them just before starting it.
func (f *runForm) apply(s *run.Spec) {
	s.Diff = f.checkbox(fieldDiff).IsChecked()
	if f.checkbox(fieldVerbose).IsChecked() {
		s.Verbosity = f.verbosity
	}
	s.Limit = strings.TrimSpace(f.limit())
	s.Tags = strings.TrimSpace(f.input(fieldTags).GetText())
	s.SkipTags = strings.TrimSpace(f.input(fieldSkipTags).GetText())
	s.ExtraVars = strings.TrimSpace(f.input(fieldExtraVars).GetText())
	s.ExtraArgs = strings.TrimSpace(f.input(fieldExtraArgs).GetText())

	s.User = f.creds.User
	s.AdhocHost = f.creds.Target
	s.AskPass = f.creds.Ask && f.creds.Password == ""
	s.AskBecomePass = f.creds.Ask && f.creds.Become == ""
}

// credentialsForm collects what is needed to reach a host that has no
// certificate yet.
func credentialsForm(c credentials, onSave func(credentials), onCancel func()) tview.Primitive {
	form := tview.NewForm()
	compactForm(form)
	form.AddInputField("target (not in the inventory)", c.Target, 0, nil, nil)
	form.AddInputField("user", c.User, 0, nil, nil)
	form.AddPasswordField("ssh password", c.Password, 0, '*', nil)
	form.AddPasswordField("become password", c.Become, 0, '*', nil)
	form.AddCheckbox("let ansible prompt instead", c.Ask, nil)
	showCheckbox(form.GetFormItem(4).(*tview.Checkbox))

	read := func() credentials {
		return credentials{
			Target:   strings.TrimSpace(form.GetFormItem(0).(*tview.InputField).GetText()),
			User:     strings.TrimSpace(form.GetFormItem(1).(*tview.InputField).GetText()),
			Password: form.GetFormItem(2).(*tview.InputField).GetText(),
			Become:   form.GetFormItem(3).(*tview.InputField).GetText(),
			Ask:      form.GetFormItem(4).(*tview.Checkbox).IsChecked(),
		}
	}
	form.AddButton("use", func() { onSave(read()) })
	form.AddButton("clear", func() { onSave(credentials{}) })
	form.AddButton("cancel", onCancel)
	form.SetCancelFunc(onCancel)
	form.SetBorder(true).SetTitle(" credentials for this run ")
	return form
}
