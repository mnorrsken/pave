package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/mnorrsken/pave/internal/inv"
)

// limitSeparator is what ansible's --limit uses between patterns.
const limitSeparator = ":"

// hostPicker is the multi-select over an inventory. Groups come first, since
// a limit is nearly always a group.
func hostPicker(in *inv.Inventory, current string, onDone func(limit string), onCancel func()) tview.Primitive {
	type entry struct {
		name  string
		label string
	}

	var entries []entry
	for _, g := range in.Groups {
		entries = append(entries, entry{
			name:  g.Name,
			label: fmt.Sprintf("%-28s %d hosts", g.Name, len(g.Hosts)),
		})
	}
	for _, h := range in.Hosts {
		entries = append(entries, entry{name: h, label: h})
	}

	selected := map[string]bool{}
	for _, p := range parseLimit(current) {
		selected[p] = true
	}

	list := tview.NewList().ShowSecondaryText(false)
	list.SetBackgroundColor(colorBackground)
	list.SetSelectedBackgroundColor(colorSelected)
	list.SetBorder(true).SetTitle(" limit — space selects, enter accepts, esc cancels ")

	render := func() {
		for i, e := range entries {
			mark := markOff
			if selected[e.name] {
				mark = markOn
			}
			list.SetItemText(i, mark+" "+e.label, "")
		}
	}
	for range entries {
		list.AddItem("", "", 0, nil)
	}
	render()

	// Put the cursor on the first thing already in the limit, so reopening the
	// picker lands where the last choice was made.
	for i, e := range entries {
		if selected[e.name] {
			list.SetCurrentItem(i)
			break
		}
	}

	accept := func() {
		var chosen []string
		for _, e := range entries {
			if selected[e.name] {
				chosen = append(chosen, e.name)
			}
		}
		onDone(strings.Join(chosen, limitSeparator))
	}

	list.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch {
		case ev.Key() == tcell.KeyEnter:
			accept()
			return nil
		case ev.Key() == tcell.KeyEscape:
			onCancel()
			return nil
		case ev.Rune() == ' ':
			if i := list.GetCurrentItem(); i >= 0 && i < len(entries) {
				name := entries[i].name
				selected[name] = !selected[name]
				render()
			}
			return nil
		}
		return ev
	})
	return list
}

// parseLimit splits a --limit value into its patterns. Both separators
// ansible accepts are handled, and the negation and intersection markers are
// left alone so an existing hand-written limit survives a round trip.
func parseLimit(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == ',' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
