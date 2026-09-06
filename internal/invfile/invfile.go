// Package invfile finds the files a project's inventory is made of: the
// inventory sources themselves, and the group_vars and host_vars that go with
// them.
//
// Where the inventory lives comes from ansible, not from reading ansible.cfg
// here, for the same reason inv asks ansible-inventory rather than parsing
// hosts.yml: a project may set it in a config file, in an environment
// variable or on a wrapper's command line, and only ansible knows which won.
package invfile

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mnorrsken/pave/internal/run"
)

// Scope is which of the two directories a name's variables live in. The
// value is the directory name itself.
type Scope string

const (
	ScopeGroup Scope = "group_vars"
	ScopeHost  Scope = "host_vars"
)

// Kind is how a file has to be opened to be readable.
type Kind int

const (
	// KindPlain is an ordinary file, opened in the editor.
	KindPlain Kind = iota
	// KindSops is encrypted with sops, which decrypts it, runs the editor and
	// encrypts it again.
	KindSops
	// KindVault is encrypted with ansible-vault, which does the same.
	KindVault
)

func (k Kind) String() string {
	switch k {
	case KindSops:
		return "sops"
	case KindVault:
		return "ansible-vault"
	default:
		return "plain"
	}
}

// File is one file the browser can open.
type File struct {
	// Path is absolute.
	Path string
	// Kind is how to open it. A file that is not there yet is whatever its
	// name says it will be.
	Kind Kind
	// Exists is false for a path that could be created but is not there.
	Exists bool
}

// Layout is where a project keeps its inventory.
type Layout struct {
	// Sources are the inventory files, in ansible's own order.
	Sources []File
	// Roots are the directories group_vars/ and host_vars/ are looked for in:
	// the directory of each source, then the project itself. First wins when
	// something has to be created.
	Roots []string

	// styles is how each scope's files are already named, worked out once:
	// For is called for every group and every host in the inventory.
	styles map[Scope]scopeStyle
}

// Source is a project, as the command that reports its configuration sees it.
type Source struct {
	// Bin is the command to ask, normally ansible-config.
	Bin        string
	Dir        string
	ConfigFile string
	Env        []string
}

// Load asks ansible where the inventory is and looks at what is there.
func (s Source) Load(ctx context.Context) (*Layout, error) {
	bin := s.Bin
	if bin == "" {
		bin = "ansible-config"
	}
	cmd := exec.CommandContext(ctx, bin, "dump", "--format", "json")
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
			return nil, fmt.Errorf("%s dump: %s", bin, msg)
		}
		return nil, fmt.Errorf("%s dump: %w", bin, err)
	}
	paths, err := ParseSources(out)
	if err != nil {
		return nil, err
	}
	return Describe(s.Dir, paths), nil
}

// setting is one entry of ansible-config's JSON.
type setting struct {
	Name  string          `json:"name"`
	Value json.RawMessage `json:"value"`
}

// ParseSources pulls the inventory paths out of `ansible-config dump --format
// json`. The value is a list on every ansible that has the flag, but an old
// one wrote a single string, so both are read.
func ParseSources(b []byte) ([]string, error) {
	var settings []setting
	if err := json.Unmarshal(b, &settings); err != nil {
		return nil, fmt.Errorf("parse the ansible configuration: %w", err)
	}
	for _, s := range settings {
		if s.Name != "DEFAULT_HOST_LIST" {
			continue
		}
		var list []string
		if err := json.Unmarshal(s.Value, &list); err == nil {
			return list, nil
		}
		var one string
		if err := json.Unmarshal(s.Value, &one); err == nil && one != "" {
			return []string{one}, nil
		}
		return nil, nil
	}
	return nil, nil
}

// Describe turns inventory paths into a Layout. dir is the project, which is
// the last place ansible looks for group_vars and the fallback when the
// inventory says nothing.
func Describe(dir string, sources []string) *Layout {
	// Everything here is compared and deduplicated by path, so it all has to
	// be spelt the same way: the same file reached through a relative root and
	// an absolute one is one file.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	l := &Layout{}
	seen := map[string]bool{}
	addRoot := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		l.Roots = append(l.Roots, p)
	}

	for _, src := range sources {
		if !filepath.IsAbs(src) {
			src = filepath.Join(dir, src)
		}
		src = filepath.Clean(src)
		info, err := os.Stat(src)
		switch {
		case err != nil:
			// A source that is not a file at all — a plugin's name, or an
			// inventory that has been moved — is not something to edit, but
			// its directory may still hold the vars.
			addRoot(filepath.Dir(src))
		case info.IsDir():
			// A directory of inventory files is its own vars root, and every
			// file in it is a source.
			addRoot(src)
			for _, f := range filesIn(src) {
				l.Sources = append(l.Sources, describeFile(f))
			}
		default:
			addRoot(filepath.Dir(src))
			l.Sources = append(l.Sources, describeFile(src))
		}
	}
	addRoot(dir)

	l.styles = map[Scope]scopeStyle{}
	group, host := l.readStyle(ScopeGroup), l.readStyle(ScopeHost)
	// Encryption is a property of the tree, not of one of its two directories:
	// a tree that keeps group secrets in sops files keeps host secrets in them
	// too, even before the first one exists.
	group.shareSecrets(&host)
	l.styles[ScopeGroup], l.styles[ScopeHost] = group, host
	return l
}

// For is every file that supplies variables for one group or host: the ones
// that are there, then the ones that could be created next to them.
func (l *Layout) For(scope Scope, name string) []File {
	var files []File
	seen := map[string]bool{}
	add := func(f File) {
		if seen[f.Path] {
			return
		}
		seen[f.Path] = true
		files = append(files, f)
	}

	for _, root := range l.Roots {
		base := filepath.Join(root, string(scope), name)
		if info, err := os.Stat(base); err == nil && info.IsDir() {
			for _, f := range filesIn(base) {
				add(describeFile(f))
			}
			continue
		}
		for _, ext := range []string{".yml", ".yaml", ".json", ""} {
			if p := base + ext; exists(p) {
				add(describeFile(p))
			}
		}
	}

	for _, p := range l.candidates(scope, name) {
		if !seen[p] {
			add(File{Path: p, Kind: kindFromName(p)})
		}
	}
	return files
}

// candidates are the paths a group or host could keep its variables in, spelt
// the way the tree already spells the ones it has. A tree with no vars at all
// gets group_vars/<name>.yml, which is ansible's own documented shape.
func (l *Layout) candidates(scope Scope, name string) []string {
	st := l.styles[scope]
	dir := filepath.Join(st.root, string(scope))
	if !st.dirs {
		out := []string{filepath.Join(dir, name+st.ext)}
		if st.sops {
			out = append(out, filepath.Join(dir, name+".sops"+st.ext))
		}
		return out
	}
	out := []string{filepath.Join(dir, name, st.plain)}
	if st.sops {
		out = append(out, filepath.Join(dir, name, st.sopsName))
	}
	return out
}

// scopeStyle is how a scope's existing files are named, and the root a new
// one is added to.
type scopeStyle struct {
	// root is the vars root that already has files, the first one otherwise.
	root string
	// dirs is <name>/vars.yml rather than <name>.yml.
	dirs bool
	// ext is the extension the tree uses, .yml or .yaml.
	ext string
	// plain is the basename of the plain file inside a directory.
	plain string
	// sops says the tree keeps secrets in sops files, and sopsName is what it
	// calls them inside a directory.
	sops     bool
	sopsName string
}

// shareSecrets carries the fact that a tree uses sops, and what it names those
// files, from whichever of the two scopes has one to the other.
func (s *scopeStyle) shareSecrets(other *scopeStyle) {
	if s.sops == other.sops {
		return
	}
	from, to := s, other
	if other.sops {
		from, to = other, s
	}
	to.sops = true
	to.sopsName = from.sopsName
}

func (l *Layout) readStyle(scope Scope) scopeStyle {
	st := scopeStyle{ext: ".yml", plain: "vars.yml", sopsName: "vault.sops.yml"}
	for _, r := range l.Roots {
		dir := filepath.Join(r, string(scope))
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) == 0 {
			continue
		}
		if st.root == "" {
			st.root = r
		}
		for _, e := range entries {
			if e.IsDir() {
				st.dirs = true
				for _, f := range filesIn(filepath.Join(dir, e.Name())) {
					st.note(filepath.Base(f))
				}
				continue
			}
			st.note(e.Name())
		}
	}
	if st.root == "" && len(l.Roots) > 0 {
		st.root = l.Roots[0]
	}
	return st
}

// note takes the naming of one existing file: its extension, and what an
// encrypted one next to it is called.
func (s *scopeStyle) note(name string) {
	if ext := filepath.Ext(name); ext == ".yaml" || ext == ".yml" {
		s.ext = ext
	}
	if isSopsName(name) {
		s.sops = true
		if s.dirs {
			s.sopsName = name
		}
		return
	}
	if s.dirs && !strings.HasPrefix(name, ".") {
		s.plain = name
	}
}

// describeFile reads enough of a file to say how it has to be opened.
func describeFile(path string) File {
	f := File{Path: path, Exists: true, Kind: kindFromName(path)}
	if f.Kind != KindPlain {
		return f
	}
	f.Kind = kindFromContent(path)
	return f
}

// kindFromName is what a file's name alone says. It is all there is to go on
// for a file that does not exist yet.
func kindFromName(path string) Kind {
	if isSopsName(filepath.Base(path)) {
		return KindSops
	}
	return KindPlain
}

func isSopsName(name string) bool {
	for _, suffix := range []string{".sops.yml", ".sops.yaml", ".sops.json"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// kindFromContent recognises a file whose name says nothing. ansible-vault
// writes its header on the first line; sops leaves a top-level sops key,
// which in yaml is at the start of a line and in json is quoted.
func kindFromContent(path string) Kind {
	b, err := os.ReadFile(path)
	if err != nil {
		return KindPlain
	}
	if strings.HasPrefix(string(b), "$ANSIBLE_VAULT") {
		return KindVault
	}
	s := string(b)
	if strings.HasPrefix(s, "sops:") || strings.Contains(s, "\nsops:") || strings.Contains(s, `"sops":`) {
		return KindSops
	}
	return KindPlain
}

// EditCmd is the command that opens a file so it can be edited in place.
// sops and ansible-vault both run the editor themselves, on the decrypted
// text, and put back what comes out.
func EditCmd(f File, editor string, env []string) run.Cmd {
	if editor == "" {
		editor = "vi"
	}
	dir := filepath.Dir(f.Path)
	switch f.Kind {
	case KindSops:
		// sops creates a file that is not there yet, from the creation rules
		// in the tree's .sops.yaml. Without a rule it says so, which is the
		// right answer: pave has no business choosing who can decrypt.
		return run.Cmd{Path: "sops", Args: []string{f.Path}, Dir: dir, Env: withEditor(env, editor)}
	case KindVault:
		verb := "edit"
		if !f.Exists {
			verb = "create"
		}
		return run.Cmd{Path: "ansible-vault", Args: []string{verb, f.Path}, Dir: dir, Env: withEditor(env, editor)}
	default:
		args := run.SplitArgs(editor)
		return run.Cmd{Path: args[0], Args: append(args[1:], f.Path), Dir: dir, Env: env}
	}
}

// withEditor tells sops and ansible-vault which editor to run. They read
// $EDITOR, so a configured one has to reach them that way.
func withEditor(env []string, editor string) []string {
	out := make([]string, 0, len(env)+1)
	for _, e := range env {
		if strings.HasPrefix(e, "EDITOR=") {
			continue
		}
		out = append(out, e)
	}
	return append(out, "EDITOR="+editor)
}

// Prepare makes the directory a file is about to be created in. sops and the
// editor both write the file itself; neither creates its parent.
func Prepare(f File) error {
	if f.Exists {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return fmt.Errorf("create the directory for %s: %w", filepath.Base(f.Path), err)
	}
	return nil
}

// filesIn is the regular files of a directory, sorted, without the hidden
// ones: a .sops.yaml holds creation rules, not variables.
func filesIn(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

func exists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}
