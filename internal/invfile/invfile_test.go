package invfile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSources(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a list", `[{"name":"DEFAULT_HOST_LIST","value":["/x/inventory/hosts.yml"]}]`, "/x/inventory/hosts.yml"},
		{"two of them", `[{"name":"DEFAULT_HOST_LIST","value":["/a","/b"]}]`, "/a,/b"},
		{"an old ansible's single string", `[{"name":"DEFAULT_HOST_LIST","value":"/x/hosts"}]`, "/x/hosts"},
		{"nothing to say", `[{"name":"DEFAULT_FORKS","value":20}]`, ""},
	}
	for _, tt := range tests {
		got, err := ParseSources([]byte(tt.in))
		if err != nil {
			t.Errorf("%s: %v", tt.name, err)
			continue
		}
		if strings.Join(got, ",") != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, got, tt.want)
		}
	}
	if _, err := ParseSources([]byte("not json")); err == nil {
		t.Error("want an error for unparseable output")
	}
}

// The layout of the tree pave was written for: an inventory directory beside
// the projects, group_vars as directories, secrets in sops files next to the
// plain ones.
func TestDescribeADirectoryStyleTree(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "inventory/hosts.yml"), "all:\n  hosts:\n    web1:\n")
	write(t, filepath.Join(root, "inventory/group_vars/all/vars.yml"), "ntp: pool.example.com\n")
	write(t, filepath.Join(root, "inventory/group_vars/all/vault.sops.yml"), "secret: ENC[x]\nsops:\n  version: 3\n")
	write(t, filepath.Join(root, "inventory/host_vars/web1/vars.yml"), "role: web\n")
	project := filepath.Join(root, "base")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	l := Describe(project, []string{"../inventory/hosts.yml"})

	if len(l.Sources) != 1 || filepath.Base(l.Sources[0].Path) != "hosts.yml" || !l.Sources[0].Exists {
		t.Fatalf("sources = %+v", l.Sources)
	}
	// The inventory's own directory comes before the project: it is where
	// ansible looks first and where a new file belongs.
	if got, want := rel(root, l.Roots), "inventory,base"; got != want {
		t.Errorf("roots = %q, want %q", got, want)
	}

	files := l.For(ScopeGroup, "all")
	if got, want := rel(root, paths(files)), "inventory/group_vars/all/vars.yml,inventory/group_vars/all/vault.sops.yml"; got != want {
		t.Fatalf("group all = %q, want %q", got, want)
	}
	if files[0].Kind != KindPlain || files[1].Kind != KindSops {
		t.Errorf("kinds = %v, %v", files[0].Kind, files[1].Kind)
	}
	for _, f := range files {
		if !f.Exists {
			t.Errorf("%s should exist", f.Path)
		}
	}

	// A group with nothing yet is offered the shape the others already use,
	// including a sops file because this tree keeps its secrets that way.
	files = l.For(ScopeGroup, "newgroup")
	if got, want := rel(root, paths(files)), "inventory/group_vars/newgroup/vars.yml,inventory/group_vars/newgroup/vault.sops.yml"; got != want {
		t.Errorf("new group = %q, want %q", got, want)
	}
	for _, f := range files {
		if f.Exists {
			t.Errorf("%s should not exist", f.Path)
		}
	}

	// An existing host directory still offers the encrypted file it is
	// missing.
	files = l.For(ScopeHost, "web1")
	if got, want := rel(root, paths(files)), "inventory/host_vars/web1/vars.yml,inventory/host_vars/web1/vault.sops.yml"; got != want {
		t.Errorf("host web1 = %q, want %q", got, want)
	}
	if files[0].Exists == files[1].Exists {
		t.Errorf("one of these is there and one is not: %+v", files)
	}
}

// The other shape ansible documents: one file per group, no directories, no
// secrets. Nothing should invent a sops file for a tree that has none.
func TestDescribeAFlatTree(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "hosts.yml"), "all:\n  hosts:\n    web1:\n")
	write(t, filepath.Join(root, "group_vars/webservers.yaml"), "port: 80\n")

	l := Describe(root, []string{"hosts.yml"})

	if got, want := rel(root, paths(l.For(ScopeGroup, "webservers"))), "group_vars/webservers.yaml"; got != want {
		t.Errorf("webservers = %q, want %q", got, want)
	}
	// The extension follows the file that is already there, and there is no
	// encrypted file to offer.
	if got, want := rel(root, paths(l.For(ScopeGroup, "dbservers"))), "group_vars/dbservers.yaml"; got != want {
		t.Errorf("dbservers = %q, want %q", got, want)
	}
	// A tree with no host_vars at all still has somewhere to put one.
	if got, want := rel(root, paths(l.For(ScopeHost, "web1"))), "host_vars/web1.yml"; got != want {
		t.Errorf("web1 = %q, want %q", got, want)
	}
}

// An inventory that is a directory is read whole, and is its own vars root.
func TestDescribeAnInventoryDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "inventory/10-hosts.yml"), "all:\n")
	write(t, filepath.Join(root, "inventory/20-cloud.yml"), "all:\n")
	write(t, filepath.Join(root, "inventory/.sops.yaml"), "creation_rules: []\n")

	l := Describe(root, []string{filepath.Join(root, "inventory")})

	if got, want := rel(root, paths(l.Sources)), "inventory/10-hosts.yml,inventory/20-cloud.yml"; got != want {
		t.Errorf("sources = %q, want %q", got, want)
	}
}

func TestKinds(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "secrets.yml")
	write(t, vault, "$ANSIBLE_VAULT;1.1;AES256\n61626364\n")
	sopsNoName := filepath.Join(dir, "encrypted.yml")
	write(t, sopsNoName, "key: ENC[x]\nsops:\n    version: 3.13.3\n")
	plain := filepath.Join(dir, "vars.yml")
	write(t, plain, "a: 1\n")

	tests := []struct {
		path string
		want Kind
	}{
		{vault, KindVault},
		{sopsNoName, KindSops},
		{plain, KindPlain},
		{filepath.Join(dir, "vault.sops.yml"), KindSops},
	}
	for _, tt := range tests {
		if got := describeFile(tt.path).Kind; got != tt.want {
			t.Errorf("%s = %v, want %v", filepath.Base(tt.path), got, tt.want)
		}
	}
}

func TestEditCmd(t *testing.T) {
	env := []string{"PATH=/usr/bin", "EDITOR=nano"}
	tests := []struct {
		name string
		file File
		want string
	}{
		{"plain", File{Path: "/x/vars.yml", Exists: true}, "vi /x/vars.yml"},
		{"sops", File{Path: "/x/vault.sops.yml", Kind: KindSops, Exists: true}, "sops /x/vault.sops.yml"},
		{"a sops file to be created", File{Path: "/x/vault.sops.yml", Kind: KindSops}, "sops /x/vault.sops.yml"},
		{"vault", File{Path: "/x/secrets.yml", Kind: KindVault, Exists: true}, "ansible-vault edit /x/secrets.yml"},
		{"a vault file to be created", File{Path: "/x/secrets.yml", Kind: KindVault}, "ansible-vault create /x/secrets.yml"},
	}
	for _, tt := range tests {
		c := EditCmd(tt.file, "vi", env)
		if got := c.Path + " " + strings.Join(c.Args, " "); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, got, tt.want)
		}
		if c.Dir != "/x" {
			t.Errorf("%s: dir = %q", tt.name, c.Dir)
		}
	}

	// An editor with arguments of its own is a command line, not a name.
	c := EditCmd(File{Path: "/x/vars.yml", Exists: true}, "code -w", env)
	if got := c.Path + " " + strings.Join(c.Args, " "); got != "code -w /x/vars.yml" {
		t.Errorf("editor with flags = %q", got)
	}

	// sops and ansible-vault run the editor themselves, so the configured one
	// has to reach them the only way they read it.
	c = EditCmd(File{Path: "/x/vault.sops.yml", Kind: KindSops}, "code -w", env)
	if n := count(c.Env, "EDITOR="); n != 1 {
		t.Fatalf("%d EDITOR entries in %v", n, c.Env)
	}
	if !has(c.Env, "EDITOR=code -w") {
		t.Errorf("env = %v, want the configured editor", c.Env)
	}
}

func TestPrepareMakesTheDirectory(t *testing.T) {
	dir := t.TempDir()
	f := File{Path: filepath.Join(dir, "group_vars", "new", "vars.yml")}
	if err := Prepare(f); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if info, err := os.Stat(filepath.Dir(f.Path)); err != nil || !info.IsDir() {
		t.Errorf("the directory was not made: %v", err)
	}
	// The file itself is the editor's to write.
	if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
		t.Errorf("prepare should not create the file")
	}
}

func paths(files []File) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

// rel makes the paths readable in a failure message, and independent of the
// temporary directory they are under.
func rel(root string, paths []string) string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		r, err := filepath.Rel(root, p)
		if err != nil {
			r = p
		}
		out = append(out, filepath.ToSlash(r))
	}
	return strings.Join(out, ",")
}

func has(env []string, want string) bool { return count(env, want) > 0 }

func count(env []string, prefix string) int {
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Integration: the real ansible-config, whose JSON is the one contract in
// this package that is not pave's to define.
func TestLoadWithRealAnsible(t *testing.T) {
	if os.Getenv("PAVE_IT") != "1" {
		t.Skip("set PAVE_IT=1 to run against a real ansible")
	}
	if _, err := exec.LookPath("ansible-config"); err != nil {
		t.Skip("no ansible-config on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	l, err := Source{Dir: "testdata/project", ConfigFile: "ansible.cfg", Env: os.Environ()}.Load(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(l.Sources) != 1 || filepath.Base(l.Sources[0].Path) != "hosts.yml" {
		t.Fatalf("sources = %+v", l.Sources)
	}
	// The project directory is where its group_vars are, so it has to be a
	// root even when the inventory is a file inside it.
	if got := l.For(ScopeGroup, "kubenodes"); len(got) != 1 || filepath.Base(got[0].Path) != "kubenodes.yml" || !got[0].Exists {
		t.Errorf("kubenodes = %+v", got)
	}
}
