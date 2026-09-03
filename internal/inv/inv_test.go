package inv

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	b, err := os.ReadFile("testdata/list.json")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := Parse(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// "all", "ungrouped" and a group with no hosts are nothing to pick from.
	var names []string
	for _, g := range inv.Groups {
		names = append(names, g.Name)
	}
	if got, want := strings.Join(names, ","), "kubemasters,kubenodes,kubeworkers,proxmox_hosts"; got != want {
		t.Errorf("groups = %q, want %q", got, want)
	}

	// A parent group carries its children's hosts.
	if got, want := strings.Join(findGroup(t, inv, "kubenodes").Hosts, ","), "master1,master2,worker1,worker2"; got != want {
		t.Errorf("kubenodes = %q, want %q", got, want)
	}

	if got, want := strings.Join(inv.Hosts, ","), "master1,master2,pve1,store1,worker1,worker2"; got != want {
		t.Errorf("hosts = %q, want %q", got, want)
	}
}

func TestParseGarbage(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("want an error")
	}
}

// A cyclic inventory is invalid, but pave must not hang on one.
func TestParseCycle(t *testing.T) {
	inv, err := Parse([]byte(`{"a": {"children": ["b"], "hosts": ["h1"]}, "b": {"children": ["a"], "hosts": ["h2"]}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got, want := strings.Join(inv.Hosts, ","), "h1,h2"; got != want {
		t.Errorf("hosts = %q, want %q", got, want)
	}
}

func TestLoadFailureReportsAnsiblesMessage(t *testing.T) {
	_, err := Source{Bin: "sh", Dir: t.TempDir(), Env: os.Environ()}.Load(context.Background())
	if err == nil {
		t.Fatal("want an error")
	}
}

// Integration: the real ansible-inventory against a small static inventory.
func TestLoadWithRealAnsible(t *testing.T) {
	if os.Getenv("PAVE_IT") != "1" {
		t.Skip("set PAVE_IT=1 to run against a real ansible")
	}
	if _, err := exec.LookPath("ansible-inventory"); err != nil {
		t.Skip("no ansible-inventory on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	inv, err := Source{Dir: "testdata/project", ConfigFile: "ansible.cfg", Env: os.Environ()}.Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := strings.Join(inv.Hosts, ","), "master1,master2,pve1,worker1"; got != want {
		t.Errorf("hosts = %q, want %q", got, want)
	}
	if g := findGroup(t, inv, "kubenodes"); len(g.Hosts) != 3 {
		t.Errorf("kubenodes = %+v", g)
	}
}

func findGroup(t *testing.T, in *Inventory, name string) Group {
	t.Helper()
	for _, g := range in.Groups {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("no group %q", name)
	return Group{}
}

func TestResolve(t *testing.T) {
	in := &Inventory{
		Groups: []Group{
			{Name: "kubemasters", Hosts: []string{"master1", "master2"}},
			{Name: "kubenodes", Hosts: []string{"master1", "master2", "worker1"}},
			{Name: "kubeworkers", Hosts: []string{"worker1"}},
			{Name: "proxmox_hosts", Hosts: []string{"pve1", "store1"}},
		},
		Hosts: []string{"master1", "master2", "pve1", "store1", "worker1"},
	}

	tests := []struct {
		pattern string
		want    string
	}{
		{"all", "master1,master2,pve1,store1,worker1"},
		{"*", "master1,master2,pve1,store1,worker1"},
		{"kubenodes", "master1,master2,worker1"},
		{"worker1", "worker1"},
		// A list in the playbook arrives here comma separated.
		{"kubeworkers,proxmox_hosts", "pve1,store1,worker1"},
		{"kubenodes:!kubeworkers", "master1,master2"},
		{"all:&kubemasters", "master1,master2"},
		{"kube*", "master1,master2,worker1"},
		{"store?", "store1"},
		// An implicit localhost is not in any inventory but is still where
		// a guard play runs.
		{"localhost", "localhost"},
		{"nothing_by_that_name", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := strings.Join(in.Resolve(tt.pattern), ","); got != tt.want {
			t.Errorf("Resolve(%q) = %q, want %q", tt.pattern, got, tt.want)
		}
	}

	if got := (*Inventory)(nil).Resolve("all"); got != nil {
		t.Errorf("Resolve on no inventory = %v", got)
	}
}
