package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/mnorrsken/pave/internal/inv"
	"github.com/mnorrsken/pave/internal/invfile"
)

// groupAll is the group every host is in. ansible does not list it and the
// host picker has no use for it, but group_vars/all is the file most worth
// having a way to open.
const groupAll = "all"

// invRef is what a node of the inventory browser points at: a file to open,
// or the group or host whose files are underneath it.
type invRef struct {
	file  *invfile.File
	scope invfile.Scope
	name  string
	// hosts are a group's members; groups are the ones a host is in.
	hosts  []string
	groups []string
	// heading is what a section node says on the right. A section is not a
	// file or a name, but the cursor still passes through it.
	heading string
}

// key identifies what a node points at across a rebuild, so the cursor can go
// back to it after the inventory has been read again.
func (r *invRef) key() string {
	if r == nil {
		return ""
	}
	if r.file != nil {
		return r.file.Path
	}
	if r.heading != "" {
		return "section/" + r.name
	}
	return string(r.scope) + "/" + r.name
}

// inventoryView is the page behind i: the inventory's own files, its groups
// and its hosts on the left, and what the selected one is on the right.
type inventoryView struct {
	*tview.Flex

	tree   *tview.TreeView
	detail *tview.TextView

	// onEdit is called for a file node, which is the only thing here that
	// does anything.
	onEdit func(invfile.File)
}

func newInventoryView() *inventoryView {
	v := &inventoryView{Flex: tview.NewFlex()}

	v.tree = tview.NewTreeView()
	v.tree.SetBackgroundColor(colorBackground)
	v.tree.SetBorder(true).SetBorderColor(colorBorder).SetTitleColor(colorTitle).SetTitle(" inventory ")
	v.tree.SetGraphics(false).SetTopLevel(1)
	v.tree.SetRoot(tview.NewTreeNode(""))

	v.detail = tview.NewTextView().SetDynamicColors(true).SetWrap(true)
	v.detail.SetBackgroundColor(colorBackground)
	v.detail.SetBorder(true).SetBorderColor(colorBorder).SetTitleColor(colorTitle).SetTitle(" file ")

	v.tree.SetChangedFunc(func(n *tview.TreeNode) { v.render(refOf(n)) })
	v.tree.SetSelectedFunc(func(n *tview.TreeNode) {
		// A node with children is a heading: enter opens and closes it. Only
		// a file is a thing to edit.
		if len(n.GetChildren()) > 0 {
			n.SetExpanded(!n.IsExpanded())
			return
		}
		ref := refOf(n)
		if ref != nil && ref.file != nil && v.onEdit != nil {
			v.onEdit(*ref.file)
		}
	})

	v.AddItem(v.tree, invTreeWidth, 0, true).AddItem(v.detail, 0, 1, false)
	return v
}

// invTreeWidth is wider than the playbook tree: a host name and the kind of
// its file both have to fit.
const invTreeWidth = 42

// setInventory rebuilds the whole tree. Whatever the cursor was on stays
// under it when it is still there: this is what happens after an edit, and
// coming back to the top of a list of hosts would be no use at all.
func (v *inventoryView) setInventory(project string, l *invfile.Layout, in *inv.Inventory) {
	v.tree.SetTitle(" inventory: " + project + " ")
	want := v.current().key()

	root := tview.NewTreeNode("")
	var first, restore *tview.TreeNode

	if len(l.Sources) > 0 {
		files := sectionNode("inventory files", plural(len(l.Sources), "file")+" the inventory is read from")
		root.AddChild(files)
		for _, f := range l.Sources {
			n := fileNode(f)
			files.AddChild(n)
			if first == nil {
				first = n
			}
			if refOf(n).key() == want {
				restore = n
			}
		}
	}

	allGroups := groupsOf(in)
	groups := sectionNode("groups", plural(len(allGroups), "group")+", each with the files that set its variables")
	root.AddChild(groups)
	for _, g := range allGroups {
		n := scopeNode(l, invfile.ScopeGroup, g.Name, &invRef{hosts: g.Hosts})
		groups.AddChild(n)
		if hit := findKey(n, want); hit != nil {
			restore = hit
		}
	}

	allHosts := hostsOf(in)
	hosts := sectionNode("hosts", plural(len(allHosts), "host")+" in the inventory")
	root.AddChild(hosts)
	for _, h := range allHosts {
		n := scopeNode(l, invfile.ScopeHost, h, &invRef{groups: groupsWith(in, h)})
		hosts.AddChild(n)
		if hit := findKey(n, want); hit != nil {
			restore = hit
		}
	}

	v.tree.SetRoot(root)
	if first == nil {
		first = groups
	}
	if restore != nil {
		first = restore
	}
	v.tree.SetCurrentNode(first)
	// SetCurrentNode does not fire the changed callback.
	v.render(refOf(first))
}

// findKey looks for the node the cursor was on, opening the group or host it
// is under so that it can be seen.
func findKey(n *tview.TreeNode, want string) *tview.TreeNode {
	if want == "" {
		return nil
	}
	if refOf(n).key() == want {
		return n
	}
	for _, c := range n.GetChildren() {
		if hit := findKey(c, want); hit != nil {
			n.SetExpanded(true)
			return hit
		}
	}
	return nil
}

// sectionNode is one of the three headings. Enter opens and closes it, which
// is the only way to get past a long list of hosts.
func sectionNode(name, heading string) *tview.TreeNode {
	return tview.NewTreeNode(iconProject + " " + name).
		SetReference(&invRef{name: name, heading: heading}).
		SetColor(colorTitle).
		SetSelectable(true)
}

// scopeNode is one group or host, with the files that apply to it under it.
func scopeNode(l *invfile.Layout, scope invfile.Scope, name string, ref *invRef) *tview.TreeNode {
	ref.scope, ref.name = scope, name
	files := l.For(scope, name)

	n := tview.NewTreeNode(scopeLabel(name, ref, files)).SetReference(ref).SetSelectable(true)
	// A tree of every host expanded is unreadable; the files are one enter
	// away.
	n.SetExpanded(false)
	for i := range files {
		n.AddChild(fileNode(files[i]))
	}
	return n
}

// scopeLabel says what is under a group or host without opening it: how many
// hosts a group has, and whether anything is actually set for it.
func scopeLabel(name string, ref *invRef, files []invfile.File) string {
	var notes []string
	if ref.scope == invfile.ScopeGroup {
		notes = append(notes, plural(len(ref.hosts), "host"))
	}
	if n := countExisting(files); n > 0 {
		notes = append(notes, plural(n, "file"))
	}
	label := tview.Escape(name)
	if len(notes) > 0 {
		label += fmt.Sprintf(" [%s]%s[-]", tag(colorDim), strings.Join(notes, ", "))
	}
	return label
}

func fileNode(f invfile.File) *tview.TreeNode {
	label := fmt.Sprintf("%s %s [%s]%s[-]",
		iconFile, tview.Escape(filepath.Base(f.Path)), tag(fileColor(f)), fileNote(f))
	n := tview.NewTreeNode(label).SetSelectable(true)
	file := f
	n.SetReference(&invRef{file: &file})
	if !f.Exists {
		n.SetColor(colorDim)
	}
	return n
}

// fileNote is the short word after a file's name: how it opens, or that it is
// not there yet.
func fileNote(f invfile.File) string {
	if !f.Exists {
		return "create"
	}
	if f.Kind == invfile.KindPlain {
		return ""
	}
	return f.Kind.String()
}

func fileColor(f invfile.File) tcell.Color {
	if !f.Exists {
		return colorDim
	}
	if f.Kind != invfile.KindPlain {
		return colorWarn
	}
	return colorDim
}

// render fills the right pane for whatever the cursor is on.
func (v *inventoryView) render(ref *invRef) {
	if ref == nil {
		v.detail.SetText("")
		v.detail.SetTitle(" file ")
		return
	}
	switch {
	case ref.heading != "":
		v.detail.SetTitle(" " + ref.name + " ")
		v.detail.SetText(fmt.Sprintf("[%s]%s[-]", tag(colorDim), tview.Escape(ref.heading)))
	case ref.file != nil:
		v.detail.SetTitle(" " + filepath.Base(ref.file.Path) + " ")
		v.detail.SetText(fileDetail(*ref.file))
	case ref.scope == invfile.ScopeGroup:
		v.detail.SetTitle(" group " + ref.name + " ")
		v.detail.SetText(membersDetail("hosts", ref.hosts))
	default:
		v.detail.SetTitle(" host " + ref.name + " ")
		v.detail.SetText(membersDetail("in groups", ref.groups))
	}
	v.detail.ScrollToBeginning()
}

func fileDetail(f invfile.File) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]%s[-]\n\n", tag(colorText), tview.Escape(shortPath(f.Path)))

	state := "exists"
	if !f.Exists {
		state = "not there yet — enter creates it"
	}
	fmt.Fprintf(&b, "[%s]%-8s[-] %s\n", tag(colorDim), "state", state)
	fmt.Fprintf(&b, "[%s]%-8s[-] %s\n", tag(colorDim), "opens", opensWith(f))
	if f.Kind == invfile.KindSops && !f.Exists {
		fmt.Fprintf(&b, "\n[%s]sops writes a new file from the creation rules in the tree's\n.sops.yaml. Without a rule for this path it will say so rather\nthan write anything readable.[-]\n", tag(colorDim))
	}
	return b.String()
}

// opensWith is the one-line answer to "what happens when I press enter".
func opensWith(f invfile.File) string {
	switch f.Kind {
	case invfile.KindSops:
		return "sops — it decrypts, runs the editor, encrypts again"
	case invfile.KindVault:
		return "ansible-vault — it decrypts, runs the editor, encrypts again"
	default:
		return "the editor"
	}
}

func membersDetail(label string, names []string) string {
	if len(names) == 0 {
		return fmt.Sprintf("[%s]no %s[-]", tag(colorDim), label)
	}
	return fmt.Sprintf("[%s]%s[-]\n\n%s", tag(colorDim), label,
		tview.Escape(strings.Join(names, ", ")))
}

// groupsOf is every group with somewhere to put variables, starting with all,
// which applies to the whole inventory and is not one of ansible's own list.
func groupsOf(in *inv.Inventory) []inv.Group {
	all := inv.Group{Name: groupAll}
	if in != nil {
		all.Hosts = in.Hosts
	}
	out := []inv.Group{all}
	if in != nil {
		out = append(out, in.Groups...)
	}
	return out
}

func hostsOf(in *inv.Inventory) []string {
	if in == nil {
		return nil
	}
	return in.Hosts
}

// groupsWith is the groups a host belongs to, which is what its host_vars sit
// on top of.
func groupsWith(in *inv.Inventory, host string) []string {
	if in == nil {
		return nil
	}
	var out []string
	for _, g := range in.Groups {
		if contains(g.Hosts, host) {
			out = append(out, g.Name)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func countExisting(files []invfile.File) int {
	n := 0
	for _, f := range files {
		if f.Exists {
			n++
		}
	}
	return n
}

func refOf(n *tview.TreeNode) *invRef {
	if n == nil {
		return nil
	}
	ref, _ := n.GetReference().(*invRef)
	return ref
}

// current is the node the cursor is on.
func (v *inventoryView) current() *invRef { return refOf(v.tree.GetCurrentNode()) }
