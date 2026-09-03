package ui

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"

	"github.com/mnorrsken/pave/internal/discover"
	"github.com/mnorrsken/pave/internal/inv"
)

// maxNamedHosts is how many host names the pane spells out before it stops
// counting them off. A play against the whole fleet is a number, not a list.
const maxNamedHosts = 12

// labelWidth is the column the values start in, two spaces of indent plus the
// label. A value that wraps is laid out under it rather than being left to
// tview, which would put the rest of a host list against the left edge.
const labelWidth = 11

// fallbackWidth is what the pane is assumed to be before it has been drawn
// once and can say how wide it really is.
const fallbackWidth = 46

// invState is what is known about the selected project's inventory. Reading
// one is slow, so the pane draws itself with whatever it has: nothing yet, a
// read in flight, the inventory, or the reason there is none.
type invState struct {
	in      *inv.Inventory
	err     error
	loading bool
}

// playbookDetail is the right pane: what the playbook says about itself,
// followed by what each of its plays targets and does. metaErr is a parse
// failure, which is worth showing above whatever was still readable.
func playbookDetail(meta discover.Meta, metaErr error, st invState, width int) string {
	if width < labelWidth+12 {
		width = fallbackWidth
	}
	var b strings.Builder

	if metaErr != nil {
		fmt.Fprintf(&b, "[%s]%s[-]\n\n", tag(colorError), tview.Escape(metaErr.Error()))
	}
	if meta.Comment != "" {
		fmt.Fprintf(&b, "%s\n\n", tview.Escape(meta.Comment))
	}
	fmt.Fprintf(&b, "[%s]%s[-]\n\n", tag(colorDim), tview.Escape(playbookSummary(meta, st)))

	for _, play := range meta.Plays {
		if play.Import != "" {
			fmt.Fprintf(&b, "[%s]import[-] %s\n\n", tag(colorDim), tview.Escape(play.Import))
			continue
		}
		name := play.Name
		if name == "" {
			name = "(unnamed play)"
		}
		fmt.Fprintf(&b, "[%s]%s[-]\n", tag(colorAccent), tview.Escape(name))
		detailLine(&b, "hosts", play.Hosts, width)
		affects(&b, play.Hosts, st, width)
		detailLine(&b, "roles", strings.Join(play.Roles, ", "), width)
		detailLine(&b, "tasks", taskLine(play), width)
		detailLine(&b, "tags", strings.Join(play.Tags, ", "), width)
		detailLine(&b, "runs", runsLine(play), width)
		b.WriteString("\n")
	}
	return b.String()
}

// playbookSummary is the one line above the plays: how much of the fleet this
// file touches in total.
func playbookSummary(meta discover.Meta, st invState) string {
	plays := 0
	imports := 0
	hosts := map[string]bool{}
	for _, p := range meta.Plays {
		if p.Import != "" {
			imports++
			continue
		}
		plays++
		for _, h := range st.in.Resolve(p.Hosts) {
			hosts[h] = true
		}
	}

	parts := []string{plural(plays, "play")}
	if imports > 0 {
		parts = append(parts, fmt.Sprintf("%d imported", imports))
	}
	if st.in != nil {
		parts = append(parts, plural(len(hosts), "host")+" in total")
	}
	return strings.Join(parts, " · ")
}

// affects is the line that answers the question the tool exists for: which
// machines does pressing run actually reach.
func affects(b *strings.Builder, pattern string, st invState, width int) {
	switch {
	case strings.TrimSpace(pattern) == "":
		return
	case strings.Contains(pattern, "{{"):
		warnLine(b, "affects", "a variable — decided at run time")
	case st.loading:
		dimLine(b, "affects", "reading the inventory…")
	case st.err != nil:
		warnLine(b, "affects", "no inventory: "+st.err.Error())
	case st.in == nil:
		dimLine(b, "affects", "press i to read the inventory")
	default:
		hosts := st.in.Resolve(pattern)
		if len(hosts) == 0 {
			warnLine(b, "affects", "nothing in this inventory")
			return
		}
		detailLine(b, "affects", plural(len(hosts), "host")+": "+hostList(hosts), width)
	}
}

// hostList names the hosts, up to the point where a count says more than the
// names would.
func hostList(hosts []string) string {
	if len(hosts) <= maxNamedHosts {
		return strings.Join(hosts, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(hosts[:maxNamedHosts], ", "), len(hosts)-maxNamedHosts)
}

// taskLine is what the play's own tasks do. The roles are on their own line:
// what a role does is in a different file and pave does not go looking.
func taskLine(p discover.Play) string {
	if p.Tasks == 0 {
		return ""
	}
	line := plural(p.Tasks, "task")
	if len(p.Modules) > 0 {
		line += ": " + strings.Join(p.Modules, ", ")
	}
	return line
}

// runsLine is how the play runs rather than what it does.
func runsLine(p discover.Play) string {
	var parts []string
	if p.Serial != "" {
		parts = append(parts, "serial "+p.Serial)
	}
	if p.Become {
		parts = append(parts, "become")
	}
	return strings.Join(parts, " · ")
}

func detailLine(b *strings.Builder, label, value string, width int) {
	if value == "" {
		return
	}
	for i, line := range wrap(value, width-labelWidth) {
		if i == 0 {
			fmt.Fprintf(b, "  [%s]%-8s[-] %s\n", tag(colorDim), label, tview.Escape(line))
			continue
		}
		fmt.Fprintf(b, "%s%s\n", strings.Repeat(" ", labelWidth), tview.Escape(line))
	}
}

// wrap breaks a value at spaces so it can be laid out under its label. A
// single word longer than the room left is not broken: tview wraps that one,
// which is better than cutting a host name in half.
func wrap(value string, width int) []string {
	if width < 1 {
		return []string{value}
	}
	var lines []string
	line := ""
	for _, word := range strings.Fields(value) {
		switch {
		case line == "":
			line = word
		case len(line)+1+len(word) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
		}
	}
	return append(lines, line)
}

func dimLine(b *strings.Builder, label, value string) {
	fmt.Fprintf(b, "  [%s]%-8s %s[-]\n", tag(colorDim), label, tview.Escape(value))
}

func warnLine(b *strings.Builder, label, value string) {
	fmt.Fprintf(b, "  [%s]%-8s[-] [%s]%s[-]\n", tag(colorDim), label, tag(colorWarn), tview.Escape(value))
}
