// Package inv reads a project's inventory. It asks ansible rather than
// parsing hosts.yml, so a static yaml file, an ini file, a plugin and a
// dynamic script all look the same here.
package inv

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"
)

// Inventory is the flattened membership of one project's inventory.
type Inventory struct {
	// Groups are sorted by name, without "all"; a group's Hosts include those
	// of its child groups.
	Groups []Group
	// Hosts is every host, sorted.
	Hosts []string
}

// Group is one inventory group.
type Group struct {
	Name  string
	Hosts []string
}

// Source is where an inventory comes from: the ansible-inventory command, run
// in a project directory with that project's config.
type Source struct {
	Bin        string
	Dir        string
	ConfigFile string
	Env        []string
}

// Load runs ansible-inventory --list and parses what it prints. Decrypting
// vault files happens as a side effect of this, so a missing age key shows up
// here rather than in the middle of a run.
func (s Source) Load(ctx context.Context) (*Inventory, error) {
	bin := s.Bin
	if bin == "" {
		bin = "ansible-inventory"
	}
	cmd := exec.CommandContext(ctx, bin, "--list")
	cmd.Dir = s.Dir
	cmd.Env = s.Env
	if s.ConfigFile != "" {
		cmd.Env = append(cmd.Env, "ANSIBLE_CONFIG="+s.ConfigFile)
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := lastLine(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%s --list: %s", bin, msg)
		}
		return nil, fmt.Errorf("%s --list: %w", bin, err)
	}
	return Parse(out)
}

// group is one entry of ansible-inventory's JSON.
type group struct {
	Hosts    []string `json:"hosts"`
	Children []string `json:"children"`
}

// Parse turns `ansible-inventory --list` output into an Inventory.
func Parse(b []byte) (*Inventory, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse inventory: %w", err)
	}

	groups := map[string]group{}
	for name, msg := range raw {
		if name == "_meta" {
			continue
		}
		var g group
		if err := json.Unmarshal(msg, &g); err != nil {
			continue
		}
		groups[name] = g
	}

	inv := &Inventory{}
	all := map[string]bool{}
	for name := range groups {
		hosts := flatten(name, groups, map[string]bool{})
		for _, h := range hosts {
			all[h] = true
		}
		// "all" is what a run does without a limit, and an empty group is
		// nothing to pick.
		if name == "all" || len(hosts) == 0 {
			continue
		}
		inv.Groups = append(inv.Groups, Group{Name: name, Hosts: hosts})
	}

	sort.Slice(inv.Groups, func(i, j int) bool { return inv.Groups[i].Name < inv.Groups[j].Name })
	for h := range all {
		inv.Hosts = append(inv.Hosts, h)
	}
	sort.Strings(inv.Hosts)
	return inv, nil
}

// flatten returns a group's hosts including those of its children. seen stops
// a self-referential inventory from looping.
func flatten(name string, groups map[string]group, seen map[string]bool) []string {
	if seen[name] {
		return nil
	}
	seen[name] = true

	g, ok := groups[name]
	if !ok {
		return nil
	}
	set := map[string]bool{}
	for _, h := range g.Hosts {
		set[h] = true
	}
	for _, child := range g.Children {
		for _, h := range flatten(child, groups, seen) {
			set[h] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// lastLine is the most useful part of an ansible error: the summary it prints
// last, rather than the deprecation warnings above it.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

// Resolve is which hosts a play's `hosts:` pattern selects, read the way
// ansible reads it: terms separated by a colon or a comma, "!" excluding,
// "&" intersecting, "*" and "?" globbing over both group and host names.
// A term that names nothing in the inventory contributes nothing, so a
// pattern that resolves to an empty list is a play that would touch nothing.
func (in *Inventory) Resolve(pattern string) []string {
	if in == nil {
		return nil
	}
	set := map[string]bool{}
	var intersects [][]string

	for _, term := range splitPattern(pattern) {
		switch {
		case strings.HasPrefix(term, "!"):
			for _, h := range in.match(term[1:]) {
				delete(set, h)
			}
		case strings.HasPrefix(term, "&"):
			intersects = append(intersects, in.match(term[1:]))
		default:
			for _, h := range in.match(term) {
				set[h] = true
			}
		}
	}

	for _, keep := range intersects {
		only := map[string]bool{}
		for _, h := range keep {
			if set[h] {
				only[h] = true
			}
		}
		set = only
	}

	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// match is one term of a pattern: a group, a host, or a glob over both.
func (in *Inventory) match(term string) []string {
	term = strings.TrimSpace(term)
	switch term {
	case "":
		return nil
	case "all", "*":
		return in.Hosts
	case "localhost", "127.0.0.1", "::1":
		// ansible has an implicit localhost that no inventory has to mention,
		// which is what the guard play of a playbook usually runs on.
		if !contains(in.Hosts, term) {
			return []string{term}
		}
	}

	for _, g := range in.Groups {
		if g.Name == term {
			return g.Hosts
		}
	}
	if contains(in.Hosts, term) {
		return []string{term}
	}
	if !strings.ContainsAny(term, "*?") {
		return nil
	}

	var out []string
	for _, g := range in.Groups {
		if globMatch(term, g.Name) {
			out = append(out, g.Hosts...)
		}
	}
	for _, h := range in.Hosts {
		if globMatch(term, h) {
			out = append(out, h)
		}
	}
	return out
}

// splitPattern splits on both separators ansible accepts.
func splitPattern(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ':' || r == ',' || r == '\n' })
}

// globMatch is ansible's fnmatch: "*" and "?" only, and no path separator to
// worry about, so filepath.Match's semantics are the same here.
func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}
