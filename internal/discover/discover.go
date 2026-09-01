// Package discover finds ansible projects and their playbooks by looking at a
// directory tree. Nothing about a particular repository layout is assumed: a
// project is a directory with an ansible.cfg, and a playbook is a YAML file
// that parses as a list of plays.
package discover

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/mnorrsken/pave/internal/config"
)

// maxProjectDepth is how far below the root a directory may sit and still be
// picked up as a project. Three levels covers a workspace of checkouts without
// walking a whole home directory.
const maxProjectDepth = 3

// maxPlaybookSize is the largest file still worth parsing as a playbook.
const maxPlaybookSize = 4 << 20

// Project is one ansible working directory: the place a run cds into and
// whose ansible.cfg it uses.
type Project struct {
	// Name is the path relative to the scan root, or the directory's own name
	// when the root itself is the project.
	Name string
	// Dir is the absolute directory.
	Dir string
	// ConfigFile is its ansible.cfg, empty when it has none.
	ConfigFile string
	// Playbooks are sorted by Rel.
	Playbooks []Playbook
}

// Playbook is one runnable file.
type Playbook struct {
	// Path is absolute.
	Path string
	// Rel is the path relative to the project directory, which is also what
	// gets passed to ansible-playbook.
	Rel string
	// Project is the owning project's directory.
	Project string
}

// Name is the file name without its extension.
func (p Playbook) Name() string {
	return strings.TrimSuffix(filepath.Base(p.Rel), filepath.Ext(p.Rel))
}

// Dir is the Rel directory, "." for a playbook in the project root.
func (p Playbook) Dir() string { return filepath.Dir(p.Rel) }

// Scan walks root and returns every project it holds. When no directory has
// an ansible.cfg the root itself is returned as a single project, so pave is
// still useful in a plain directory of playbooks.
func Scan(root string, cfg *config.Config) ([]Project, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("scan root: %w", err)
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("scan root: %s is not a directory", root)
	}

	skip := excludeSet(cfg)
	var dirs []string
	walkProjects(root, root, 0, skip, &dirs)

	if len(dirs) == 0 {
		dirs = []string{root}
	}

	projects := make([]Project, 0, len(dirs))
	for _, dir := range dirs {
		p := Project{Dir: dir, Name: projectName(root, dir)}
		if cfgPath := filepath.Join(dir, "ansible.cfg"); exists(cfgPath) {
			p.ConfigFile = cfgPath
		}
		p.Playbooks = playbooks(dir, cfg, skip)
		if len(p.Playbooks) == 0 && p.ConfigFile == "" {
			continue
		}
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func projectName(root, dir string) string {
	if dir == root {
		return filepath.Base(root)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return filepath.Base(dir)
	}
	return rel
}

// walkProjects collects directories holding an ansible.cfg. It stops
// descending once it finds one: a project's own subdirectories are its
// playbooks and roles, not more projects.
func walkProjects(root, dir string, depth int, skip map[string]bool, out *[]string) {
	if exists(filepath.Join(dir, "ansible.cfg")) {
		*out = append(*out, dir)
		return
	}
	if depth >= maxProjectDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !isDir(dir, e) || skipDir(e.Name(), skip) {
			continue
		}
		walkProjects(root, filepath.Join(dir, e.Name()), depth+1, skip, out)
	}
}

// playbooks lists the play files of one project: the project directory itself,
// not recursed, plus the configured playbook directories, recursed.
func playbooks(dir string, cfg *config.Config, skip map[string]bool) []Playbook {
	seen := map[string]bool{}
	var found []Playbook

	add := func(path string) {
		if seen[path] || !IsPlaybook(path) {
			return
		}
		seen[path] = true
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return
		}
		found = append(found, Playbook{Path: path, Rel: rel, Project: dir})
	}

	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !isDir(dir, e) && isYAML(e.Name()) {
				add(filepath.Join(dir, e.Name()))
			}
		}
	}

	for _, sub := range cfg.PlaybookDirs {
		base := filepath.Join(dir, sub)
		if !isDirPath(base) {
			continue
		}
		_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if path != base && skipDir(d.Name(), skip) {
					return fs.SkipDir
				}
				return nil
			}
			if isYAML(d.Name()) {
				add(path)
			}
			return nil
		})
	}

	sort.Slice(found, func(i, j int) bool { return found[i].Rel < found[j].Rel })
	return found
}

// IsPlaybook reports whether path parses as a list of plays. This is what
// keeps requirements.yml, vars files and task files out of the tree without
// having to keep a list of names to ignore.
func IsPlaybook(path string) bool {
	b, ok := readCapped(path)
	if !ok {
		return false
	}
	var plays []map[string]any
	if err := yaml.Unmarshal(b, &plays); err != nil {
		return false
	}
	for _, play := range plays {
		for _, key := range []string{"hosts", "import_playbook", "include_playbook", "ansible.builtin.import_playbook"} {
			if _, ok := play[key]; ok {
				return true
			}
		}
	}
	return false
}

// Play is one play in a playbook, for the detail pane.
type Play struct {
	Name  string
	Hosts string
	// Import is set instead of Hosts when the entry is an import_playbook.
	Import string
}

// Meta is what the detail pane shows about a playbook: its leading comment
// block, which is where these files explain themselves, and its plays.
type Meta struct {
	Comment string
	Plays   []Play
}

// Describe reads a playbook's header comment and plays.
func Describe(path string) (Meta, error) {
	b, ok := readCapped(path)
	if !ok {
		return Meta{}, fmt.Errorf("read %s", filepath.Base(path))
	}
	m := Meta{Comment: leadingComment(string(b))}

	var plays []map[string]any
	if err := yaml.Unmarshal(b, &plays); err != nil {
		return m, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	for _, p := range plays {
		play := Play{Name: str(p["name"]), Hosts: str(p["hosts"])}
		for _, key := range []string{"import_playbook", "include_playbook", "ansible.builtin.import_playbook"} {
			if v, ok := p[key]; ok {
				play.Import = str(v)
			}
		}
		m.Plays = append(m.Plays, play)
	}
	return m, nil
}

// leadingComment returns the comment block at the top of the file, with the
// leading "# " stripped. A document marker before it is ignored.
func leadingComment(s string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == "---" && len(out) == 0:
			continue
		case strings.HasPrefix(t, "#"):
			out = append(out, strings.TrimPrefix(strings.TrimPrefix(t, "#"), " "))
		case t == "" && len(out) == 0:
			continue
		default:
			return strings.TrimRight(strings.Join(out, "\n"), "\n")
		}
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

// str renders a scalar or a list of scalars the way ansible writes it.
func str(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, str(e))
		}
		return strings.Join(parts, ",")
	default:
		return fmt.Sprint(t)
	}
}

func readCapped(path string) ([]byte, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Size() > maxPlaybookSize {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

func excludeSet(cfg *config.Config) map[string]bool {
	skip := make(map[string]bool, len(cfg.Exclude))
	for _, name := range cfg.Exclude {
		skip[name] = true
	}
	return skip
}

// skipDir also drops every dotted directory: nothing runnable lives in one,
// and .git alone can be enormous.
func skipDir(name string, skip map[string]bool) bool {
	return skip[name] || strings.HasPrefix(name, ".")
}

func isYAML(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDirPath(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// isDir resolves symlinked directories too, which is how a workspace often
// points at a checkout that lives somewhere else.
func isDir(parent string, e fs.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&fs.ModeSymlink == 0 {
		return false
	}
	return isDirPath(filepath.Join(parent, e.Name()))
}
