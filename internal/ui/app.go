package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/mnorrsken/pave/internal/config"
	"github.com/mnorrsken/pave/internal/discover"
	"github.com/mnorrsken/pave/internal/inv"
	"github.com/mnorrsken/pave/internal/run"
	"github.com/mnorrsken/pave/internal/sshcert"
)

// inventoryTimeout is generous on purpose: reading an inventory decrypts every
// vault file it references, and a dynamic inventory talks to something.
const inventoryTimeout = 90 * time.Second

// treeWidth is the fixed width of the left pane. Playbook names are short;
// the output is what deserves the room.
const treeWidth = 34

// Options are the App's dependencies. Everything that reaches outside the
// process is a function here, so a test can drive the whole interface with no
// ansible, no inventory and no ssh-agent.
type Options struct {
	// Root is the directory to scan.
	Root   string
	Config *config.Config

	// Screen is the terminal to draw on. Tests pass a tcell SimulationScreen;
	// production leaves it nil so tview opens the real one itself, which is
	// the only path that reports a failure to open it.
	Screen tcell.Screen

	// Runner starts commands. Defaults to run.PTYRunner{}.
	Runner run.Runner
	// Scan defaults to discover.Scan.
	Scan func(root string, cfg *config.Config) ([]discover.Project, error)
	// Inventory defaults to inv.Source.Load.
	Inventory func(ctx context.Context, src inv.Source) (*inv.Inventory, error)
	// Cert defaults to sshcert.Read.
	Cert func(key string) (sshcert.Status, error)
	// Now defaults to time.Now.
	Now func() time.Time
}

// App is the running interface.
type App struct {
	*tview.Application

	opts Options

	pages   *tview.Pages
	header  *tview.TextView
	body    *tview.Pages
	tree    *playbookTree
	detail  *tview.TextView
	form    *runForm
	preview *tview.TextView
	output  *outputPane
	status  *statusBar
	filter  *tview.InputField

	// modals is the stack of open dialogs. While it is not empty the global
	// keys stand down so the dialog owns the keyboard.
	modals   []string
	modalSeq int

	projects []discover.Project
	// invCache holds one inventory per project directory; reading one is slow
	// enough to be worth not repeating.
	invCache map[string]*inv.Inventory
	cert     sshcert.Status

	// session is the command currently running, and running says whether the
	// keyboard belongs to it.
	session     run.Session
	running     bool
	interrupted bool
	// lastRun names what the output pane is showing, for the log file name.
	lastRun string
	// sessionRows and sessionCols are the size the running command was last
	// told about.
	sessionRows, sessionCols int

	filtering bool
	done      chan struct{}
}

// New builds the interface and does the first scan, so the tree is populated
// before the first draw.
func New(opts Options) *App {
	if opts.Config == nil {
		opts.Config = &config.Config{}
	}
	if opts.Runner == nil {
		opts.Runner = run.PTYRunner{}
	}
	if opts.Scan == nil {
		opts.Scan = discover.Scan
	}
	if opts.Inventory == nil {
		opts.Inventory = func(ctx context.Context, src inv.Source) (*inv.Inventory, error) {
			return src.Load(ctx)
		}
	}
	if opts.Cert == nil {
		opts.Cert = sshcert.Read
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	a := &App{
		Application: tview.NewApplication(),
		opts:        opts,
		invCache:    map[string]*inv.Inventory{},
		done:        make(chan struct{}),
	}

	a.header = tview.NewTextView().SetDynamicColors(true)
	a.header.SetBackgroundColor(colorBackground)

	a.tree = newPlaybookTree()
	a.tree.onSelect = a.selectionChanged

	a.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	a.detail.SetBackgroundColor(colorBackground)
	a.detail.SetBorder(true).SetBorderColor(colorBorder).SetTitleColor(colorTitle).SetTitle(" playbook ")

	a.form = newRunForm(opts.Config.Defaults, a.runSelected, a.openHostPicker, a.openCredentials)
	a.form.changed = a.renderPreview

	a.preview = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	a.preview.SetBackgroundColor(colorBackground)

	a.output = newOutputPane()
	a.status = newStatusBar()

	a.filter = tview.NewInputField().SetLabel("filter: ")
	a.filter.SetFieldBackgroundColor(colorBackground)
	a.filter.SetBackgroundColor(colorBackground)
	a.filter.SetChangedFunc(func(text string) { a.tree.setFilter(text) })
	a.filter.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEscape {
			a.filter.SetText("")
			a.tree.setFilter("")
		}
		a.closeFilter()
	})

	right := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.detail, 0, 1, false).
		AddItem(a.form, formHeight, 0, false).
		AddItem(a.preview, 3, 0, false)

	browse := tview.NewFlex().
		AddItem(a.tree, treeWidth, 0, true).
		AddItem(right, 0, 1, false)

	a.body = tview.NewPages().
		AddPage("browse", browse, true, true).
		AddPage("output", a.output, true, false)

	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.header, 1, 0, false).
		AddItem(a.body, 0, 1, true).
		AddItem(a.status, 2, 0, false)

	a.pages = tview.NewPages().AddPage("main", main, true, true)
	a.SetRoot(a.pages, true).SetInputCapture(a.globalKeys)
	a.SetAfterDrawFunc(func(tcell.Screen) { a.resizeSession() })
	a.SetFocus(a.tree)

	a.rescan()
	a.reloadCert()
	a.refreshHints()
	return a
}

// Run opens the terminal and starts the loop.
func (a *App) Run() error {
	if a.opts.Screen != nil {
		a.SetScreen(a.opts.Screen)
	}
	defer close(a.done)
	return a.Application.Run()
}

// --- scanning and the header ------------------------------------------------

func (a *App) rescan() {
	projects, err := a.opts.Scan(a.opts.Root, a.opts.Config)
	if err != nil {
		a.status.errorf("%v", err)
		return
	}
	a.projects = projects
	a.tree.setProjects(projects)
	a.renderHeader()

	switch n := a.tree.playbookCount(); {
	case n == 0:
		a.status.warn("no playbooks under %s", a.opts.Root)
	default:
		a.status.info("%d playbooks in %s", n, strings.Join(a.tree.sortedProjectNames(), ", "))
	}
}

func (a *App) reloadCert() {
	key := config.Expand(a.opts.Config.SSHCert.Key)
	if key == "" {
		return
	}
	st, err := a.opts.Cert(key)
	if err != nil {
		a.status.errorf("%v", err)
		return
	}
	a.cert = st
	a.renderHeader()
}

func (a *App) renderHeader() {
	a.header.SetText(fmt.Sprintf("[%s]pave[-]  %s   %s",
		tag(colorTitle), tview.Escape(shortPath(a.opts.Root)), a.certText()))
}

// certText is the certificate state as one coloured phrase. It is the first
// thing to look at when a run fails to authenticate.
func (a *App) certText() string {
	switch {
	case !a.cert.Present:
		return fmt.Sprintf("[%s]no certificate — press c[-]", tag(colorWarn))
	case a.cert.Forever:
		return fmt.Sprintf("[%s]certificate never expires[-]", tag(colorWarn))
	case a.cert.Expired(a.opts.Now()):
		return fmt.Sprintf("[%s]certificate expired — press c[-]", tag(colorError))
	default:
		return fmt.Sprintf("[%s]cert %s · %s[-]", tag(colorOK),
			shortDuration(a.cert.Remaining(a.opts.Now())), strings.Join(a.cert.Principals, ","))
	}
}

// --- the selected playbook --------------------------------------------------

func (a *App) selectionChanged(ref *treeRef) {
	if ref == nil || ref.playbook == nil {
		a.detail.SetText("")
		a.detail.SetTitle(" playbook ")
		a.renderPreview()
		return
	}
	pb := ref.playbook
	a.detail.SetTitle(" " + filepath.ToSlash(pb.Rel) + " ")

	var b strings.Builder
	meta, err := discover.Describe(pb.Path)
	if err != nil {
		fmt.Fprintf(&b, "[%s]%s[-]\n\n", tag(colorError), tview.Escape(err.Error()))
	}
	if meta.Comment != "" {
		fmt.Fprintf(&b, "%s\n\n", tview.Escape(meta.Comment))
	}
	for _, play := range meta.Plays {
		switch {
		case play.Import != "":
			fmt.Fprintf(&b, "[%s]import[-]  %s\n", tag(colorDim), tview.Escape(play.Import))
		default:
			name := play.Name
			if name == "" {
				name = "(unnamed play)"
			}
			fmt.Fprintf(&b, "[%s]%-8s[-] %s\n", tag(colorAccent), tview.Escape(play.Hosts), tview.Escape(name))
		}
	}
	a.detail.SetText(b.String())
	a.detail.ScrollToBeginning()
	a.renderPreview()
}

// currentSpec is the run the form and the tree currently describe. ok is
// false when nothing runnable is selected.
func (a *App) currentSpec() (run.Spec, bool) {
	ref := a.tree.current()
	if ref == nil || ref.playbook == nil {
		return run.Spec{}, false
	}
	spec := run.Spec{
		Bin:        a.opts.Config.AnsiblePlaybookBin,
		Dir:        ref.project.Dir,
		ConfigFile: ref.project.ConfigFile,
		Playbook:   filepath.ToSlash(ref.playbook.Rel),
	}
	a.form.apply(&spec)
	return spec, true
}

func (a *App) renderPreview() {
	spec, ok := a.currentSpec()
	if !ok {
		a.preview.SetText("")
		return
	}
	// The password files do not exist yet, but the line should say that they
	// will be there.
	if a.form.creds.Password != "" && !a.form.creds.Ask {
		spec.ConnPassFile = "<ssh password>"
	}
	if a.form.creds.Become != "" && !a.form.creds.Ask {
		spec.BecomePassFile = "<become password>"
	}

	text := fmt.Sprintf("[%s]$[-] %s", tag(colorDim), tview.Escape(spec.Preview()))
	if s := a.form.creds.summary(); s != "" {
		text += fmt.Sprintf("\n[%s]%s[-]", tag(colorWarn), tview.Escape(s))
	}
	a.preview.SetText(text)
}

// --- running ----------------------------------------------------------------

// step is one command in a sequence shown in the output pane.
type step struct {
	label     string
	cmd       run.Cmd
	allowFail bool
}

func (a *App) runSelected() {
	if a.running {
		a.status.warn("a run is already going")
		return
	}
	spec, ok := a.currentSpec()
	if !ok {
		a.status.warn("select a playbook first")
		return
	}

	var cleanup []func()
	creds := a.form.creds
	if !creds.Ask {
		if creds.Password != "" {
			path, err := run.PasswordFile(creds.Password)
			if err != nil {
				a.status.errorf("%v", err)
				return
			}
			spec.ConnPassFile = path
			cleanup = append(cleanup, func() { os.Remove(path) })
		}
		if creds.Become != "" {
			path, err := run.PasswordFile(creds.Become)
			if err != nil {
				a.cleanupAll(cleanup)
				a.status.errorf("%v", err)
				return
			}
			spec.BecomePassFile = path
			cleanup = append(cleanup, func() { os.Remove(path) })
		}
	}

	a.lastRun = spec.Playbook
	a.start(spec.Playbook, []step{{
		label: spec.Preview(),
		cmd:   spec.Cmd(a.opts.Config.RunEnv()),
	}}, cleanup)
}

// start shows the output pane and runs the steps one after another.
func (a *App) start(title string, steps []step, cleanup []func()) {
	a.output.reset(title)
	a.body.SwitchToPage("output")
	a.SetFocus(a.output)
	a.running = true
	a.interrupted = false
	a.refreshHints()
	a.status.info("running")
	// The pane's size is read here, on the main loop: its rectangle belongs to
	// the drawing goroutine and must not be touched from the run's.
	rows, cols := a.outputSize()
	go a.runSteps(steps, cleanup, rows, cols)
}

func (a *App) runSteps(steps []step, cleanup []func(), rows, cols int) {
	defer a.cleanupAll(cleanup)

	var failure string

	for _, st := range steps {
		st := st
		a.queue(func() { a.output.banner(colorAccent, "%s", st.label) })

		st.cmd.Rows, st.cmd.Cols = rows, cols
		sess, err := a.opts.Runner.Start(st.cmd)
		if err != nil {
			failure = err.Error()
			a.queue(func() { a.output.banner(colorError, "%s", err.Error()) })
			break
		}
		a.setSession(sess)
		err = a.stream(sess)
		a.setSession(nil)

		code := run.ExitCode(err)
		switch {
		case err == nil:
			a.queue(func() { a.output.banner(colorOK, "ok") })
		case st.allowFail:
			a.queue(func() { a.output.banner(colorDim, "exit %d, carrying on", code) })
		default:
			failure = fmt.Sprintf("%s exited %d", filepath.Base(st.cmd.Path), code)
			msg := failure
			a.queue(func() { a.output.banner(colorError, "%s", msg) })
		}
		if err != nil && !st.allowFail {
			break
		}
	}

	a.queue(func() {
		a.running = false
		a.refreshHints()
		switch {
		case a.interrupted:
			a.status.warn("interrupted")
		case failure != "":
			a.status.errorf("%s", failure)
		default:
			a.status.ok("done")
		}
		a.reloadCert()
	})
}

// stream parks a session's output for the main loop to draw and returns what
// the command exited with.
func (a *App) stream(sess run.Session) error {
	// A ticker rather than a flush per read: ansible writes line by line, and
	// one redraw per line is wasted work on a play with a thousand tasks.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		t := time.NewTicker(flushEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if !a.output.empty() {
					a.queue(a.output.flush)
				}
			}
		}
	}()

	buf := make([]byte, 16<<10)
	for {
		n, rerr := sess.Read(buf)
		if n > 0 && a.output.push(buf[:n]) {
			a.queue(a.output.flush)
		}
		if rerr != nil {
			break
		}
	}
	err := sess.Wait()
	sess.Close()
	a.queue(a.output.flush)
	return err
}

// resizeSession keeps the running command's idea of the terminal the same
// size as the pane it is being drawn into, so ansible wraps its lines where
// the user can see the wrap.
func (a *App) resizeSession() {
	if !a.running || a.session == nil {
		return
	}
	rows, cols := a.outputSize()
	if rows == a.sessionRows && cols == a.sessionCols {
		return
	}
	a.sessionRows, a.sessionCols = rows, cols
	a.session.Resize(rows, cols)
}

func (a *App) outputSize() (rows, cols int) {
	_, _, w, h := a.output.GetInnerRect()
	if w <= 0 || h <= 0 {
		return 24, 100
	}
	return h, w
}

func (a *App) setSession(s run.Session) {
	a.queue(func() { a.session = s })
}

func (a *App) cleanupAll(cleanup []func()) {
	for _, f := range cleanup {
		f()
	}
}

// queue runs f on the main loop. It is a no-op once the loop has stopped, so
// a run that outlives the interface cannot block forever on a redraw.
func (a *App) queue(f func()) {
	select {
	case <-a.done:
		return
	default:
	}
	a.Application.QueueUpdateDraw(f)
}

// interrupt is ^C during a run: the first one asks ansible to stop, a second
// one does not ask.
func (a *App) interrupt() {
	if a.session == nil {
		return
	}
	if a.interrupted {
		a.output.banner(colorError, "killing it")
		a.session.Kill()
		return
	}
	a.interrupted = true
	a.output.banner(colorWarn, "interrupt sent, ^C again to kill")
	a.session.Interrupt()
}

// --- dialogs ----------------------------------------------------------------

func (a *App) openModal(p tview.Primitive) string {
	a.modalSeq++
	name := fmt.Sprintf("modal-%d", a.modalSeq)
	a.modals = append(a.modals, name)
	a.pages.AddPage(name, p, true, true)
	a.SetFocus(p)
	a.refreshHints()
	return name
}

func (a *App) closeModal(name string) {
	a.pages.RemovePage(name)
	for i, n := range a.modals {
		if n == name {
			a.modals = append(a.modals[:i], a.modals[i+1:]...)
			break
		}
	}
	switch {
	case len(a.modals) > 0:
		// Back to the dialog underneath this one.
		if _, p := a.pages.GetFrontPage(); p != nil {
			a.SetFocus(p)
		}
	case a.running:
		a.SetFocus(a.output)
	default:
		a.SetFocus(a.tree)
	}
	a.refreshHints()
}

func (a *App) modalOpen() bool { return len(a.modals) > 0 }

func (a *App) showError(title string, err error) {
	var name string
	name = a.openModal(center(messageBox(title, err.Error(), colorError, func() { a.closeModal(name) }), 70, 9))
	a.status.errorf("%v", err)
}

func (a *App) openHelp() {
	var name string
	name = a.openModal(centerLarge(helpView(func() { a.closeModal(name) }), 80))
}

func (a *App) openCredentials() {
	var name string
	name = a.openModal(center(credentialsForm(a.form.creds,
		func(c credentials) {
			a.form.setCredentials(c)
			a.closeModal(name)
			if c.used() {
				a.status.info("credentials set for this run — %s", c.summary())
			} else {
				a.status.info("credentials cleared")
			}
		},
		func() { a.closeModal(name) },
	), 74, formBoxHeight(5)))
}

// openHostPicker needs the inventory of the selected project, which is read
// on first use.
func (a *App) openHostPicker() {
	ref := a.tree.current()
	if ref == nil {
		a.status.warn("select a playbook first")
		return
	}
	in, err := a.inventory(ref.project, false)
	if err != nil {
		a.showError("inventory", err)
		return
	}
	if len(in.Groups) == 0 && len(in.Hosts) == 0 {
		a.status.warn("the inventory of %s is empty", ref.project.Name)
		return
	}

	var name string
	name = a.openModal(centerLarge(hostPicker(in, a.form.limit(),
		func(limit string) {
			a.form.setLimit(limit)
			a.closeModal(name)
			if limit == "" {
				a.status.info("limit cleared — the play runs against everything it targets")
			} else {
				a.status.info("limit %s", limit)
			}
		},
		func() { a.closeModal(name) },
	), 60))
}

// inventory reads a project's inventory, from the cache unless reload is set.
func (a *App) inventory(p *discover.Project, reload bool) (*inv.Inventory, error) {
	if p == nil {
		return nil, fmt.Errorf("no project selected")
	}
	if !reload {
		if in, ok := a.invCache[p.Dir]; ok {
			return in, nil
		}
	}
	a.status.info("reading the inventory of %s…", p.Name)
	ctx, cancel := context.WithTimeout(context.Background(), inventoryTimeout)
	defer cancel()

	in, err := a.opts.Inventory(ctx, inv.Source{
		Bin:        a.opts.Config.AnsibleInventoryBin,
		Dir:        p.Dir,
		ConfigFile: p.ConfigFile,
		Env:        a.opts.Config.RunEnv(),
	})
	if err != nil {
		return nil, err
	}
	a.invCache[p.Dir] = in
	return in, nil
}

func (a *App) reloadInventory() {
	ref := a.tree.current()
	if ref == nil {
		return
	}
	in, err := a.inventory(ref.project, true)
	if err != nil {
		a.showError("inventory", err)
		return
	}
	a.status.ok("%s: %d hosts in %d groups", ref.project.Name, len(in.Hosts), len(in.Groups))
}

// openCertForm signs a new certificate. The commands run in the output pane
// like everything else, so an encrypted CA key can ask for its passphrase.
func (a *App) openCertForm() {
	if a.running {
		a.status.warn("wait for the run to finish")
		return
	}
	c := a.opts.Config.SSHCert
	form := tview.NewForm()
	compactForm(form)
	form.AddInputField("CA key", config.Expand(c.CAKey), 0, nil, nil)
	form.AddInputField("key to sign", config.Expand(c.Key), 0, nil, nil)
	form.AddInputField("validity", c.Validity, 0, nil, nil)
	form.AddInputField("principals", strings.Join(c.Principals, ","), 0, nil, nil)
	form.AddInputField("identity", c.Identity, 0, nil, nil)
	form.AddCheckbox("load into ssh-agent", c.AddToAgent == nil || *c.AddToAgent, nil)
	showCheckbox(form.GetFormItem(5).(*tview.Checkbox))

	var name string
	text := func(i int) string {
		return strings.TrimSpace(form.GetFormItem(i).(*tview.InputField).GetText())
	}
	form.AddButton("sign", func() {
		req := sshcert.Request{
			CAKey:      text(0),
			Key:        text(1),
			Validity:   text(2),
			Principals: splitList(text(3)),
			Identity:   text(4),
			AddToAgent: form.GetFormItem(5).(*tview.Checkbox).IsChecked(),
		}
		steps, err := req.Steps(a.opts.Config.RunEnv())
		if err != nil {
			a.showError("sign", err)
			return
		}
		a.closeModal(name)

		out := make([]step, 0, len(steps))
		for _, s := range steps {
			out = append(out, step{label: s.Label, cmd: s.Cmd, allowFail: s.AllowFail})
		}
		a.start("signing a certificate", out, nil)
	})
	form.AddButton("cancel", func() { a.closeModal(name) })
	form.SetCancelFunc(func() { a.closeModal(name) })
	form.SetBorder(true).SetTitle(" sign a short lived certificate ")
	name = a.openModal(center(form, 76, formBoxHeight(6)))
}

// onboard is the shortcut for a host that has no certificate yet: the
// configured playbook, with the credentials dialog already open.
func (a *App) onboard() {
	if pb := config.Expand(a.opts.Config.OnboardPlaybook); pb != "" {
		path := pb
		if !filepath.IsAbs(path) {
			path = filepath.Join(a.opts.Root, pb)
		}
		if abs, err := filepath.Abs(path); err == nil {
			if !a.tree.selectPath(abs) {
				a.status.warn("onboard_playbook %s is not in the tree", pb)
			}
		}
	}
	a.openCredentials()
}

func (a *App) saveLog() {
	if a.running {
		a.status.warn("wait for the run to finish")
		return
	}
	if a.output.text() == "" {
		a.status.warn("nothing to save")
		return
	}
	def := savePath(a.lastRun, a.opts.Now())
	var name string
	name = a.openModal(center(promptBox("save the output", "file", def, "",
		func(path string) {
			a.closeModal(name)
			if path = strings.TrimSpace(path); path == "" {
				return
			}
			if err := os.WriteFile(config.Expand(path), []byte(a.output.text()), 0o644); err != nil {
				a.showError("save", err)
				return
			}
			a.status.ok("written to %s", path)
		},
		func() { a.closeModal(name) },
	), 70, 7))
}

func (a *App) openFilter() {
	a.filtering = true
	// The filter bar takes the place of the status line's first row, so the
	// layout does not shift while typing.
	a.status.Clear()
	a.status.AddItem(a.filter, 1, 0, true).AddItem(a.status.keys, 1, 0, false)
	a.SetFocus(a.filter)
	a.refreshHints()
}

func (a *App) closeFilter() {
	a.filtering = false
	a.status.Clear()
	a.status.AddItem(a.status.line, 1, 0, false).AddItem(a.status.keys, 1, 0, false)
	a.SetFocus(a.tree)
	a.refreshHints()
}

// --- keys -------------------------------------------------------------------

func (a *App) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	if a.modalOpen() || a.filtering {
		return ev
	}

	// While something is running the output pane is ansible's keyboard.
	if a.running && a.focusedOn(a.output) {
		return a.outputKeys(ev)
	}

	// ^C never quits out from under a run.
	if ev.Key() == tcell.KeyCtrlC && a.running {
		a.interrupt()
		return nil
	}

	// Inside the run form the keyboard belongs to the form: a letter is
	// something being typed, not a command.
	if a.focusedOn(a.form) || isTextInput(a.GetFocus()) {
		switch ev.Key() {
		case tcell.KeyEscape:
			a.SetFocus(a.tree)
			a.refreshHints()
			return nil
		case tcell.KeyF5:
			a.runSelected()
			return nil
		case tcell.KeyF2:
			a.openHostPicker()
			return nil
		case tcell.KeyF3:
			a.openCredentials()
			return nil
		}
		return ev
	}

	switch ev.Key() {
	case tcell.KeyTab:
		a.focusNext()
		return nil
	case tcell.KeyBacktab:
		a.focusNext()
		return nil
	case tcell.KeyEscape:
		if a.focusedOn(a.output) {
			a.body.SwitchToPage("browse")
			a.SetFocus(a.tree)
			a.refreshHints()
		}
		return nil
	case tcell.KeyEnter:
		if a.focusedOn(a.tree) {
			if a.tree.currentPlaybook() != nil {
				a.SetFocus(a.form)
				a.refreshHints()
				return nil
			}
			return ev // let the tree expand or collapse the node
		}
	case tcell.KeyF5:
		a.runSelected()
		return nil
	case tcell.KeyF2:
		a.openHostPicker()
		return nil
	case tcell.KeyF3:
		a.openCredentials()
		return nil
	}

	switch ev.Rune() {
	case 'q':
		a.quit()
		return nil
	case '?':
		a.openHelp()
		return nil
	case '/':
		if a.focusedOn(a.tree) {
			a.openFilter()
			return nil
		}
	case 'r':
		if a.focusedOn(a.tree) {
			a.rescan()
			return nil
		}
	case 'i':
		if a.focusedOn(a.tree) {
			a.reloadInventory()
			return nil
		}
	case 'L':
		a.openHostPicker()
		return nil
	case 'c':
		a.openCertForm()
		return nil
	case 'o':
		a.onboard()
		return nil
	case 's':
		if a.focusedOn(a.output) {
			a.saveLog()
			return nil
		}
	}
	return ev
}

// outputKeys is what happens while a command is running: everything that is
// not a scrolling key goes to the process, so ansible's prompts work.
func (a *App) outputKeys(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyCtrlC:
		a.interrupt()
		return nil
	case tcell.KeyEscape:
		// Escape is the way out of a run that is still going: the run keeps
		// going, tab comes back to it. Nothing ansible prompts for needs an
		// escape key.
		a.body.SwitchToPage("browse")
		a.SetFocus(a.tree)
		a.refreshHints()
		return nil
	}
	if !a.output.scrollKey(ev) {
		return ev
	}
	if a.session == nil {
		return nil
	}
	if b := keyBytes(ev); len(b) > 0 {
		a.session.Write(b)
	}
	return nil
}

// keyBytes is what a terminal would have sent for this key.
func keyBytes(ev *tcell.EventKey) []byte {
	switch ev.Key() {
	case tcell.KeyRune:
		buf := make([]byte, utf8.UTFMax)
		return buf[:utf8.EncodeRune(buf, ev.Rune())]
	case tcell.KeyEnter:
		return []byte{'\r'}
	case tcell.KeyTab:
		return []byte{'\t'}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		return []byte{0x7f}
	}
	// The control keys are their own byte, which is how ^D ends a prompt.
	if k := ev.Key(); k > 0 && k <= 0x1f {
		return []byte{byte(k)}
	}
	return nil
}

func (a *App) quit() {
	if a.running && a.session != nil {
		var name string
		name = a.openModal(center(confirmBox("quit",
			"A run is going. Quitting kills it.",
			func() {
				a.session.Kill()
				a.Stop()
			},
			func() { a.closeModal(name) },
		), 60, 9))
		return
	}
	a.Stop()
}

func (a *App) focusedOn(p tview.Primitive) bool {
	focus := a.GetFocus()
	if focus == p {
		return true
	}
	// A composite such as the form reports whichever item has the keyboard.
	if b, ok := p.(interface{ HasFocus() bool }); ok {
		return b.HasFocus()
	}
	return false
}

func (a *App) focusNext() {
	switch {
	case a.focusedOn(a.tree):
		if a.body.HasPage("output") {
			name, _ := a.body.GetFrontPage()
			if name == "output" {
				a.SetFocus(a.output)
				break
			}
		}
		a.SetFocus(a.form)
	case a.focusedOn(a.form):
		a.SetFocus(a.tree)
	default:
		a.SetFocus(a.tree)
	}
	a.refreshHints()
}

func (a *App) refreshHints() {
	switch {
	case a.modalOpen():
		a.status.setKeys("esc close · tab move · enter accept")
	case a.filtering:
		a.status.setKeys("[filter] type to narrow the tree · enter keep it · esc clear")
	case a.running && a.focusedOn(a.output):
		a.status.setKeys("[run] what you type goes to ansible · ^C interrupt · pgup/pgdn scroll · esc leave it running")
	case a.focusedOn(a.output):
		a.status.setKeys("[output] esc back · s save · pgup/pgdn scroll · ? help")
	case a.focusedOn(a.form):
		a.status.setKeys("[options] F5 run · F2 hosts · F3 credentials · tab next field · esc back to the tree")
	default:
		a.status.setKeys("[tree] enter options · F5 run · F2 hosts · / filter · r rescan · i inventory · c cert · o onboard · ? help · q quit")
	}
}

// --- small helpers ----------------------------------------------------------

// isTextInput reports whether the focused primitive is something the user
// types into, in which case the single letter bindings must not fire.
func isTextInput(p tview.Primitive) bool {
	switch p.(type) {
	case *tview.InputField, *tview.DropDown, *tview.TextArea:
		return true
	}
	return false
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func shortPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || !strings.HasPrefix(p, home) {
		return p
	}
	return "~" + strings.TrimPrefix(p, home)
}

// shortDuration is "11h32m" rather than "11h32m4.123s".
func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}
