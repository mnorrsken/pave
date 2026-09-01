// Package run turns a set of chosen options into an ansible-playbook command
// and runs it on a pty, which is the only way ansible colours its output and
// can still prompt for a password.
package run

import (
	"strconv"
	"strings"
	"unicode"
)

// Spec is one playbook run as the user has configured it in the form.
type Spec struct {
	// Bin is the command to execute, normally ansible-playbook. It may be a
	// wrapper script, which is how a container run is arranged.
	Bin string
	// Dir is the project directory the run happens in.
	Dir string
	// ConfigFile is the project's ansible.cfg, exported as ANSIBLE_CONFIG so
	// roles_path, the inventory and the vars plugins resolve the way they do
	// for a run started by hand from that directory.
	ConfigFile string
	// Playbook is the path passed to ansible-playbook, relative to Dir.
	Playbook string

	Check     bool
	Diff      bool
	Verbosity int

	Limit     string
	Tags      string
	SkipTags  string
	User      string
	ExtraVars string
	Forks     int

	// AdhocHost is an address that is not in the inventory. It becomes a
	// one-host inventory of its own, which is what makes it possible to reach
	// a machine before it has been added anywhere.
	AdhocHost string

	// ConnPassFile and BecomePassFile hold the SSH and become passwords for a
	// host that has no certificate yet. See PasswordFile.
	ConnPassFile   string
	BecomePassFile string
	// AskPass and AskBecomePass let ansible prompt instead, on the pty.
	AskPass       bool
	AskBecomePass bool

	// ExtraArgs is appended verbatim, split the way a shell would. It is the
	// escape hatch for anything the form does not model.
	ExtraArgs string
}

// Args is everything after argv[0]. Long flags throughout: the preview line is
// meant to be read, and copied into a terminal when something needs doing by
// hand.
func (s Spec) Args() []string {
	args := []string{s.Playbook}

	if s.AdhocHost != "" {
		// The trailing comma is ansible's spelling of "this list is the whole
		// inventory".
		args = append(args, "--inventory", s.AdhocHost+",")
	}
	if s.Check {
		args = append(args, "--check")
	}
	if s.Diff {
		args = append(args, "--diff")
	}
	if v := s.Verbosity; v > 0 {
		if v > 4 {
			v = 4
		}
		args = append(args, "-"+strings.Repeat("v", v))
	}

	limit := s.Limit
	// A playbook may well guard itself with `assert: ansible_limit is
	// defined`, so an ad-hoc target is passed as a limit as well as an
	// inventory.
	if limit == "" && s.AdhocHost != "" {
		limit = s.AdhocHost
	}
	if limit != "" {
		args = append(args, "--limit", limit)
	}
	if s.Tags != "" {
		args = append(args, "--tags", s.Tags)
	}
	if s.SkipTags != "" {
		args = append(args, "--skip-tags", s.SkipTags)
	}
	if s.User != "" {
		args = append(args, "--user", s.User)
	}
	if s.ExtraVars != "" {
		args = append(args, "--extra-vars", s.ExtraVars)
	}
	if s.Forks > 0 {
		args = append(args, "--forks", strconv.Itoa(s.Forks))
	}
	if s.ConnPassFile != "" {
		args = append(args, "--connection-password-file", s.ConnPassFile)
	}
	if s.BecomePassFile != "" {
		args = append(args, "--become-password-file", s.BecomePassFile)
	}
	if s.AskPass {
		args = append(args, "--ask-pass")
	}
	if s.AskBecomePass {
		args = append(args, "--ask-become-pass")
	}

	return append(args, SplitArgs(s.ExtraArgs)...)
}

// Cmd is the command Spec describes, ready to hand to a Runner. env is the
// base environment; ANSIBLE_CONFIG and the colour forcing are added here.
func (s Spec) Cmd(env []string) Cmd {
	e := append([]string(nil), env...)
	if s.ConfigFile != "" {
		e = append(e, "ANSIBLE_CONFIG="+s.ConfigFile)
	}
	// The pty already makes ansible colour its output; this covers the case of
	// a wrapper that pipes somewhere in between.
	e = append(e, "ANSIBLE_FORCE_COLOR=1")
	return Cmd{Path: s.Bin, Args: s.Args(), Dir: s.Dir, Env: e}
}

// Preview is the command as a line you could paste into a shell.
func (s Spec) Preview() string {
	parts := append([]string{s.Bin}, s.Args()...)
	for i, p := range parts {
		parts[i] = quote(p)
	}
	return strings.Join(parts, " ")
}

// SplitArgs splits a string into arguments the way a shell would, honouring
// single and double quotes. Anything more exotic belongs in a wrapper script.
func SplitArgs(s string) []string {
	var (
		args    []string
		cur     strings.Builder
		inArg   bool
		quoteCh rune
	)
	flush := func() {
		if inArg {
			args = append(args, cur.String())
			cur.Reset()
			inArg = false
		}
	}
	for _, r := range s {
		switch {
		case quoteCh != 0:
			if r == quoteCh {
				quoteCh = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quoteCh = r
			inArg = true
		case unicode.IsSpace(r):
			flush()
		default:
			cur.WriteRune(r)
			inArg = true
		}
	}
	flush()
	return args
}

func quote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`'"\$&|;<>()*?[]{}~#!`, r)
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
