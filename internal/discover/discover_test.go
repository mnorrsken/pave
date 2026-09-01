package discover

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/mnorrsken/pave/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func TestScanWorkspace(t *testing.T) {
	projects, err := Scan("testdata/tree", testConfig(t))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}

	var names []string
	for _, p := range projects {
		names = append(names, p.Name)
	}
	if got, want := strings.Join(names, ","), "base,cluster"; got != want {
		// inventory/ and image/ have no ansible.cfg and no plays, so they are
		// not projects.
		t.Fatalf("projects = %q, want %q", got, want)
	}

	tests := []struct {
		project string
		want    string
	}{
		// requirements.yml, the task file and the role are all skipped.
		{"base", "playbooks/patch.yml,playbooks/site/deploy.yml"},
		{"cluster", "site.yml"},
	}
	for _, tt := range tests {
		p := project(t, projects, tt.project)
		var rels []string
		for _, pb := range p.Playbooks {
			rels = append(rels, filepath.ToSlash(pb.Rel))
		}
		if got := strings.Join(rels, ","); got != tt.want {
			t.Errorf("%s playbooks = %q, want %q", tt.project, got, tt.want)
		}
		if p.ConfigFile == "" {
			t.Errorf("%s: no ansible.cfg found", tt.project)
		}
	}
}

// A directory with playbooks but no ansible.cfg anywhere is still usable: the
// root itself becomes the one project.
func TestScanWithoutAnsibleCfg(t *testing.T) {
	projects, err := Scan("testdata/flat", testConfig(t))
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("got %d projects, want 1", len(projects))
	}
	if projects[0].Name != "flat" {
		t.Errorf("name = %q, want flat", projects[0].Name)
	}
	if projects[0].ConfigFile != "" {
		t.Errorf("config file = %q, want none", projects[0].ConfigFile)
	}
	if len(projects[0].Playbooks) != 1 || projects[0].Playbooks[0].Rel != filepath.Join("playbooks", "only.yml") {
		t.Errorf("playbooks = %+v", projects[0].Playbooks)
	}
}

func TestIsPlaybook(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"testdata/tree/base/playbooks/patch.yml", true},
		{"testdata/tree/cluster/site.yml", true},
		{"testdata/tree/base/playbooks/tasks-only.yml", false},
		{"testdata/tree/base/requirements.yml", false},
		{"testdata/tree/inventory/hosts.yml", false},
		{"testdata/tree/inventory/group_vars/all/vars.yml", false},
		{"testdata/tree/base/playbooks/README.md", false},
		{"testdata/tree/nope.yml", false},
	}
	for _, tt := range tests {
		if got := IsPlaybook(tt.path); got != tt.want {
			t.Errorf("IsPlaybook(%s) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestDescribe(t *testing.T) {
	m, err := Describe("testdata/tree/base/playbooks/patch.yml")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	want := "Patch every host.\n\n  ansible-playbook playbooks/patch.yml -l kubeworkers"
	if m.Comment != want {
		t.Errorf("comment =\n%q\nwant\n%q", m.Comment, want)
	}
	if len(m.Plays) != 1 || m.Plays[0].Name != "Patch" || m.Plays[0].Hosts != "all" {
		t.Errorf("plays = %+v", m.Plays)
	}
}

// A list of hosts renders as ansible writes it, and an import_playbook entry
// has no hosts of its own.
func TestDescribeListHostsAndImport(t *testing.T) {
	m, err := Describe("testdata/tree/cluster/site.yml")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if len(m.Plays) != 2 {
		t.Fatalf("plays = %+v", m.Plays)
	}
	if m.Plays[0].Import != "k3s.yml" {
		t.Errorf("import = %q, want k3s.yml", m.Plays[0].Import)
	}
	if m.Plays[1].Hosts != "kubemasters,kubeworkers" {
		t.Errorf("hosts = %q", m.Plays[1].Hosts)
	}
}

func project(t *testing.T, projects []Project, name string) Project {
	t.Helper()
	for _, p := range projects {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no project %q", name)
	return Project{}
}
