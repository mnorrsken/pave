// Package inv reads a project's inventory. It asks ansible rather than
// parsing hosts.yml, so a static yaml file, an ini file, a plugin and a
// dynamic script all look the same here.
package inv

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
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
