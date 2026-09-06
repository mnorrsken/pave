package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/mnorrsken/pave/internal/run"
)

// i shows the inventory: the file it is read from, the groups, and the hosts,
// all of them with the files that set variables for them.
func TestInventoryBrowserShowsTheFilesAndTheMembership(t *testing.T) {
	h := newHarness(t)
	h.openInventory()
	h.sync()

	screen := h.screenText()
	for _, want := range []string{"inventory files", "hosts.yml", "groups", "all", "kubemasters", "hosts", "master1"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the browser does not show %q:\n%s", want, screen)
		}
	}

	// It opens on the inventory file itself, and says what opening it would
	// do rather than leaving the pane empty.
	ref := h.currentInvRef()
	if ref == nil || ref.file == nil || filepath.Base(ref.file.Path) != "hosts.yml" {
		t.Fatalf("the cursor starts on %+v, want the inventory file", ref)
	}
	if detail := h.app.invView.detail.GetText(true); !strings.Contains(detail, "the editor") {
		t.Errorf("detail = %q", detail)
	}

	h.key(tcell.KeyEscape)
	h.waitFor("the playbooks to come back", func() bool { return !h.app.invOpen && h.app.tree.HasFocus() })
}

// A .sops.yaml holds creation rules, not variables, and must not be offered
// as something to edit.
func TestInventoryBrowserLeavesTheSopsRulesAlone(t *testing.T) {
	h := newHarness(t)
	h.openInventory()
	h.sync()
	if strings.Contains(h.screenText(), ".sops.yaml") {
		t.Errorf("the sops creation rules are listed as a file:\n%s", h.screenText())
	}
}

// Enter on a group opens it; enter on one of its files opens the editor, and
// an encrypted one goes through sops rather than showing its ciphertext.
func TestInventoryBrowserEditsAGroupsFiles(t *testing.T) {
	h := newHarness(t)
	h.openInventory()

	h.selectInvNode("all")
	h.key(tcell.KeyEnter)
	h.waitFor("the files of the group", func() bool {
		return strings.Contains(h.screenText(), "vault.sops.yml")
	})

	h.selectInvNode("vars.yml")
	h.key(tcell.KeyEnter)
	h.waitFor("the editor to be asked for", func() bool { return h.editCount() == 1 })
	cmd := h.lastEdit()
	if cmd.Path != "vi" || len(cmd.Args) != 1 {
		t.Fatalf("edit = %s %v", cmd.Path, cmd.Args)
	}
	if want := filepath.Join("inventory", "group_vars", "all", "vars.yml"); !strings.HasSuffix(cmd.Args[0], want) {
		t.Errorf("edited %q, want the group's own vars file", cmd.Args[0])
	}

	h.selectInvNode("vault.sops.yml")
	h.key(tcell.KeyEnter)
	h.waitFor("the second edit", func() bool { return h.editCount() == 2 })
	if cmd := h.lastEdit(); cmd.Path != "sops" {
		t.Errorf("an encrypted file opened with %q, want sops", cmd.Path)
	}
}

// A host with no variables yet is offered the paths its neighbours use, and
// enter makes the directory and opens the editor on the file.
func TestInventoryBrowserCreatesAMissingFile(t *testing.T) {
	root := treeCopy(t)
	h := newHarness(t, func(o *Options) { o.Root = root })
	h.openInventory()

	// master1 has a vars file; worker1 has nothing at all.
	h.selectInvNode("worker1")
	h.key(tcell.KeyEnter)
	h.waitFor("the paths it could have", func() bool {
		return strings.Contains(h.screenText(), "create")
	})

	h.selectInvNode("vars.yml")
	ref := h.currentInvRef()
	if ref == nil || ref.file == nil || ref.file.Exists {
		t.Fatalf("the cursor is on %+v, want a file that is not there yet", ref)
	}
	want := filepath.Join(root, "inventory", "host_vars", "worker1", "vars.yml")
	if ref.file.Path != want {
		t.Fatalf("path = %q, want %q", ref.file.Path, want)
	}

	// The editor writes the file; pave only has to have made the directory it
	// goes in first, which no editor does for itself. This one writes nothing,
	// so the directory should not survive either.
	var dirWasThere bool
	h.onEdit(func(c run.Cmd) error {
		info, err := os.Stat(filepath.Dir(c.Args[len(c.Args)-1]))
		dirWasThere = err == nil && info.IsDir()
		return nil
	})

	h.key(tcell.KeyEnter)
	h.waitFor("the editor to be asked for", func() bool { return h.editCount() == 1 })

	if !dirWasThere {
		t.Error("the editor was given a path whose directory does not exist")
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Error("pave should not have written the file itself")
	}
	// Backing out of a new file leaves nothing behind.
	if _, err := os.Stat(filepath.Dir(want)); !os.IsNotExist(err) {
		t.Errorf("an empty directory was left behind: %v", err)
	}
}

// The tree is read again after an edit, so a file that has just been written
// stops being offered as one to create.
func TestInventoryBrowserRereadsAfterAnEdit(t *testing.T) {
	root := treeCopy(t)
	created := filepath.Join(root, "inventory", "host_vars", "worker1", "vars.yml")

	h := newHarness(t, func(o *Options) { o.Root = root })
	// A real editor is what writes the file, so the fake one writes it too.
	h.onEdit(func(c run.Cmd) error {
		return os.WriteFile(c.Args[len(c.Args)-1], []byte("a: 1\n"), 0o644)
	})

	h.openInventory()
	h.selectInvNode("worker1")
	h.key(tcell.KeyEnter)
	h.waitFor("the paths it could have", func() bool { return strings.Contains(h.screenText(), "create") })
	h.selectInvNode("vars.yml")
	h.key(tcell.KeyEnter)
	h.waitFor("the edit", func() bool { return h.editCount() == 1 })

	if _, err := os.Stat(created); err != nil {
		t.Fatalf("the fake editor did not write the file: %v", err)
	}
	h.selectInvNode("worker1")
	h.key(tcell.KeyEnter)
	// The condition runs on the event loop already, so it moves the cursor
	// itself rather than going through the harness, which would queue onto
	// the loop from the loop and never come back.
	h.waitFor("the file to be there now", func() bool {
		if !selectByLabel(h.app.invView, "vars.yml") {
			return false
		}
		ref := h.app.invView.current()
		return ref != nil && ref.file != nil && ref.file.Exists
	})
}

// An editor that fails says so.
func TestInventoryBrowserReportsAnEditorThatFails(t *testing.T) {
	h := newHarness(t)
	h.onEdit(func(run.Cmd) error { return errors.New("editor exploded") })

	h.openInventory()
	h.selectInvNode("all")
	h.key(tcell.KeyEnter)
	h.waitFor("the files of the group", func() bool { return strings.Contains(h.screenText(), "vars.yml") })
	h.selectInvNode("vars.yml")
	h.key(tcell.KeyEnter)

	h.waitFor("the error", func() bool { return strings.Contains(h.screenText(), "editor exploded") })
}

// sops exits 200 to say the file was left as it was. That is an answer, not
// a failure: nothing to report as an error, and nothing to reread.
func TestInventoryBrowserAcceptsAnUnchangedSopsFile(t *testing.T) {
	h := newHarness(t)
	h.onEdit(func(run.Cmd) error {
		return fmt.Errorf("sops: %w", sopsExit(t, 200))
	})

	h.openInventory()
	h.selectInvNode("all")
	h.key(tcell.KeyEnter)
	h.waitFor("the files of the group", func() bool { return strings.Contains(h.screenText(), "vault.sops.yml") })
	h.selectInvNode("vault.sops.yml")
	h.key(tcell.KeyEnter)

	h.waitFor("the note that nothing changed", func() bool {
		return strings.Contains(h.app.status.text(), "unchanged:")
	})
	if h.app.modalOpen() {
		t.Errorf("an unchanged file opened an error dialog:\n%s", h.screenText())
	}
}

// Closing a dialog opened from the browser gives the keyboard back to the
// browser. It used to go to the playbook tree, which is not on the screen, so
// the arrow keys did nothing until tab was pressed.
func TestInventoryBrowserKeepsTheKeyboardAfterADialog(t *testing.T) {
	h := newHarness(t)
	h.onEdit(func(run.Cmd) error { return errors.New("editor exploded") })

	h.openInventory()
	before := h.currentInvRef().key()
	h.selectInvNode("all")
	h.key(tcell.KeyEnter)
	h.waitFor("the files of the group", func() bool { return strings.Contains(h.screenText(), "vars.yml") })
	h.selectInvNode("vars.yml")
	h.key(tcell.KeyEnter)
	h.waitFor("the error", func() bool { return h.app.modalOpen() })

	h.key(tcell.KeyEnter) // the ok button
	h.waitFor("the dialog to close", func() bool { return !h.app.modalOpen() })
	if !h.app.invView.tree.HasFocus() {
		t.Fatal("the browser did not get the keyboard back")
	}

	// And the arrow keys move in it, with no tab needed first.
	after := h.currentInvRef().key()
	h.key(tcell.KeyUp)
	h.waitFor("the cursor to move", func() bool { return h.app.invView.current().key() != after })
	_ = before
}

// The browser's letters are its own: r rereads it rather than rescanning the
// playbook tree, and the tree's own keys stay out of the way.
func TestInventoryBrowserOwnsItsKeys(t *testing.T) {
	h := newHarness(t)
	h.openInventory()

	h.press('r')
	// The condition already runs on the event loop, so it reads the status
	// line directly: h.status would queue onto the loop from the loop.
	h.waitFor("the reread", func() bool {
		return strings.Contains(h.app.status.text(), "3 hosts in 2 groups")
	})
	if !h.app.invOpen {
		t.Error("r should not have left the browser")
	}

	// / opens the playbook filter from the tree; here it is nothing.
	h.press('/')
	h.sync()
	if h.app.filtering {
		t.Error("the tree's filter opened from inside the browser")
	}
}

// sopsExit is a real exit status, which is the only kind Unchanged looks at.
func sopsExit(t *testing.T, code int) error {
	t.Helper()
	err := exec.Command("sh", "-c", fmt.Sprintf("exit %d", code)).Run()
	if err == nil {
		t.Fatalf("exit %d did not fail", code)
	}
	return err
}

// currentInvRef is what the browser's cursor is on.
func (h *harness) currentInvRef() *invRef {
	h.t.Helper()
	var ref *invRef
	h.inspect(func() { ref = h.app.invView.current() })
	return ref
}

// selectInvNode moves the cursor onto the named node, the way the arrow keys
// would.
func (h *harness) selectInvNode(name string) {
	h.t.Helper()
	if !h.selectInvNodeIfPresent(name) {
		h.t.Fatalf("no node named %q:\n%s", name, h.screenText())
	}
}

func (h *harness) selectInvNodeIfPresent(name string) bool {
	h.t.Helper()
	var found bool
	h.inspect(func() { found = selectByLabel(h.app.invView, name) })
	return found
}

func selectByLabel(v *inventoryView, name string) bool {
	var hit *tview.TreeNode
	for _, n := range v.tree.GetRoot().GetChildren() {
		if hit = findByLabel(n, name); hit != nil {
			break
		}
	}
	if hit == nil {
		return false
	}
	v.tree.SetCurrentNode(hit)
	v.render(refOf(hit))
	return true
}

// findByLabel looks only where the cursor could go: a collapsed node's
// children are not on the screen.
func findByLabel(n *tview.TreeNode, name string) *tview.TreeNode {
	if labelName(n.GetText()) == name {
		return n
	}
	if !n.IsExpanded() {
		return nil
	}
	for _, c := range n.GetChildren() {
		if hit := findByLabel(c, name); hit != nil {
			return hit
		}
	}
	return nil
}

// labelName is a node's name without its marker or the dim note after it.
func labelName(label string) string {
	for _, icon := range []string{iconFile + " ", iconProject + " "} {
		label = strings.TrimPrefix(label, icon)
	}
	if i := strings.Index(label, " ["); i >= 0 {
		label = label[:i]
	}
	return strings.TrimSpace(label)
}
