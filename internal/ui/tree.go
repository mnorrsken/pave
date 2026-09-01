package ui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rivo/tview"

	"github.com/mnorrsken/pave/internal/discover"
)

// treeRef is what a tree node points at. A project or directory node has no
// playbook.
type treeRef struct {
	project  *discover.Project
	playbook *discover.Playbook
}

// playbookTree is the left pane: projects, the directories under them, and
// the playbooks themselves.
type playbookTree struct {
	*tview.TreeView

	projects []discover.Project
	filter   string
	onSelect func(*treeRef)
}

func newPlaybookTree() *playbookTree {
	t := &playbookTree{TreeView: tview.NewTreeView()}
	t.SetBackgroundColor(colorBackground)
	t.SetBorder(true).SetBorderColor(colorBorder).SetTitleColor(colorTitle).SetTitle(" playbooks ")
	t.SetGraphics(false).SetTopLevel(1)
	t.SetChangedFunc(func(n *tview.TreeNode) {
		if t.onSelect == nil {
			return
		}
		ref, _ := n.GetReference().(*treeRef)
		t.onSelect(ref)
	})
	root := tview.NewTreeNode("")
	t.SetRoot(root).SetCurrentNode(root)
	return t
}

// setProjects rebuilds the tree. The current playbook stays selected across a
// rescan when it is still there.
func (t *playbookTree) setProjects(projects []discover.Project) {
	t.projects = projects
	t.rebuild()
}

func (t *playbookTree) setFilter(filter string) {
	t.filter = filter
	t.rebuild()
}

func (t *playbookTree) rebuild() {
	want := ""
	if pb := t.currentPlaybook(); pb != nil {
		want = pb.Path
	}

	root := tview.NewTreeNode("")
	var first, restore *tview.TreeNode

	for i := range t.projects {
		p := &t.projects[i]
		shown := t.matching(p)
		if len(shown) == 0 {
			continue
		}
		// One shared directory is not worth a level of its own: the whole of
		// base/ lives in playbooks/, so say so on the project instead.
		prefix := commonDir(shown)
		label := p.Name
		if prefix != "." {
			label += fmt.Sprintf(" [%s]%s/[-]", tag(colorDim), filepath.ToSlash(prefix))
		}
		pn := tview.NewTreeNode(iconProject + " " + label).
			SetReference(&treeRef{project: p}).
			SetColor(colorTitle).
			SetSelectable(true)
		root.AddChild(pn)

		dirs := map[string]*tview.TreeNode{".": pn}
		for j := range shown {
			pb := shown[j]
			rel := trimPrefixDir(pb.Rel, prefix)
			parent := ensureDir(dirs, pn, filepath.Dir(rel))
			node := tview.NewTreeNode(iconPlaybook + " " + nameOf(rel)).
				SetReference(&treeRef{project: p, playbook: pb}).
				SetSelectable(true)
			parent.AddChild(node)
			if first == nil {
				first = node
			}
			if pb.Path == want {
				restore = node
			}
		}
	}

	t.SetRoot(root)
	switch {
	case restore != nil:
		t.SetCurrentNode(restore)
	case first != nil:
		t.SetCurrentNode(first)
	default:
		t.SetCurrentNode(root)
	}
	// SetCurrentNode does not fire the changed callback, so the detail pane
	// would otherwise still be showing whatever was there before.
	if t.onSelect != nil {
		ref, _ := t.GetCurrentNode().GetReference().(*treeRef)
		t.onSelect(ref)
	}
}

// matching is the project's playbooks that survive the filter. A filter that
// matches the project name keeps all of them.
func (t *playbookTree) matching(p *discover.Project) []*discover.Playbook {
	q := strings.ToLower(strings.TrimSpace(t.filter))
	projectMatches := q == "" || strings.Contains(strings.ToLower(p.Name), q)

	var out []*discover.Playbook
	for i := range p.Playbooks {
		pb := &p.Playbooks[i]
		if projectMatches || strings.Contains(strings.ToLower(pb.Rel), q) {
			out = append(out, pb)
		}
	}
	return out
}

// ensureDir returns the node for a directory below the project, creating the
// intermediate ones.
func ensureDir(dirs map[string]*tview.TreeNode, projectNode *tview.TreeNode, dir string) *tview.TreeNode {
	if n, ok := dirs[dir]; ok {
		return n
	}
	parent := ensureDir(dirs, projectNode, filepath.Dir(dir))
	n := tview.NewTreeNode(iconDir + " " + filepath.Base(dir)).
		SetSelectable(true).
		SetColor(colorDim)
	parent.AddChild(n)
	dirs[dir] = n
	return n
}

// commonDir is the directory every playbook shares, "." when they do not.
func commonDir(pbs []*discover.Playbook) string {
	if len(pbs) == 0 {
		return "."
	}
	common := pbs[0].Dir()
	for _, pb := range pbs[1:] {
		for common != "." && !isPrefixDir(common, pb.Dir()) {
			common = filepath.Dir(common)
		}
	}
	return common
}

func isPrefixDir(prefix, dir string) bool {
	return dir == prefix || strings.HasPrefix(dir, prefix+string(filepath.Separator))
}

func trimPrefixDir(rel, prefix string) string {
	if prefix == "." {
		return rel
	}
	trimmed, err := filepath.Rel(prefix, rel)
	if err != nil {
		return rel
	}
	return trimmed
}

func nameOf(rel string) string {
	return strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
}

// current is the selected node's reference, nil on an empty tree.
func (t *playbookTree) current() *treeRef {
	n := t.GetCurrentNode()
	if n == nil {
		return nil
	}
	ref, _ := n.GetReference().(*treeRef)
	return ref
}

func (t *playbookTree) currentPlaybook() *discover.Playbook {
	if ref := t.current(); ref != nil {
		return ref.playbook
	}
	return nil
}

// selectPath moves the cursor to a playbook by its absolute path, and reports
// whether it was there.
func (t *playbookTree) selectPath(path string) bool {
	var found *tview.TreeNode
	for _, n := range t.GetRoot().GetChildren() {
		if hit := findNode(n, path); hit != nil {
			found = hit
			break
		}
	}
	if found == nil {
		return false
	}
	t.SetCurrentNode(found)
	if t.onSelect != nil {
		ref, _ := found.GetReference().(*treeRef)
		t.onSelect(ref)
	}
	return true
}

func findNode(n *tview.TreeNode, path string) *tview.TreeNode {
	if ref, ok := n.GetReference().(*treeRef); ok && ref.playbook != nil && ref.playbook.Path == path {
		return n
	}
	for _, c := range n.GetChildren() {
		if hit := findNode(c, path); hit != nil {
			return hit
		}
	}
	return nil
}

// playbookCount is how many runnable files the tree is showing.
func (t *playbookTree) playbookCount() int {
	n := 0
	for i := range t.projects {
		n += len(t.matching(&t.projects[i]))
	}
	return n
}

// sortedProjectNames is used by the tests and the status line.
func (t *playbookTree) sortedProjectNames() []string {
	names := make([]string, 0, len(t.projects))
	for _, p := range t.projects {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}
