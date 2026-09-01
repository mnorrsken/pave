package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileIsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nothing.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AnsiblePlaybookBin != "ansible-playbook" {
		t.Errorf("bin = %q", cfg.AnsiblePlaybookBin)
	}
	if cfg.SSHCert.Validity != "+12h" {
		t.Errorf("validity = %q", cfg.SSHCert.Validity)
	}
	if cfg.SSHCert.AddToAgent == nil || !*cfg.SSHCert.AddToAgent {
		t.Errorf("add_to_agent should default to true")
	}
	if len(cfg.PlaybookDirs) == 0 || cfg.PlaybookDirs[0] != "playbooks" {
		t.Errorf("playbook dirs = %v", cfg.PlaybookDirs)
	}
}

func TestLoadAndRootOverride(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "config.yaml")
	write(t, user, `
root: /somewhere
ansible_playbook_bin: /usr/bin/ansible-playbook
env:
  SOPS_AGE_KEY_FILE: ~/keys.txt
ssh_cert:
  ca_key: ~/ca/user_ca
  validity: +8h
  principals: [ansible-admin, root]
defaults:
  diff: true
`)
	cfg, err := Load(user)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SSHCert.Validity != "+8h" || len(cfg.SSHCert.Principals) != 2 {
		t.Fatalf("ssh_cert = %+v", cfg.SSHCert)
	}
	if !cfg.Defaults.Diff || cfg.Defaults.Check {
		t.Errorf("defaults = %+v", cfg.Defaults)
	}
	// The key the user file did not set keeps its default.
	if cfg.SSHCert.Key != "~/.ssh/id_ed25519" {
		t.Errorf("key = %q", cfg.SSHCert.Key)
	}

	root := t.TempDir()
	write(t, filepath.Join(root, FileName), "onboard_playbook: base/playbooks/onboard.yml\ndefaults:\n  check: true\n")
	if err := cfg.LoadRootOverride(root); err != nil {
		t.Fatalf("override: %v", err)
	}
	if cfg.OnboardPlaybook != "base/playbooks/onboard.yml" {
		t.Errorf("onboard = %q", cfg.OnboardPlaybook)
	}
	if !cfg.Defaults.Check || !cfg.Defaults.Diff {
		t.Errorf("override should add to defaults, not replace them: %+v", cfg.Defaults)
	}
	// Untouched keys survive the overlay.
	if cfg.AnsiblePlaybookBin != "/usr/bin/ansible-playbook" {
		t.Errorf("bin = %q", cfg.AnsiblePlaybookBin)
	}
}

func TestLoadBadYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, "root: [unclosed\n")
	if _, err := Load(path); err == nil {
		t.Fatal("want an error for unparseable config")
	}
}

func TestExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	t.Setenv("PAVE_TEST_DIR", "/opt/x")
	tests := []struct{ in, want string }{
		{"", ""},
		{"~/.ssh/id_ed25519", filepath.Join(home, ".ssh/id_ed25519")},
		{"~", home},
		{"/abs/path", "/abs/path"},
		{"$PAVE_TEST_DIR/keys.txt", "/opt/x/keys.txt"},
	}
	for _, tt := range tests {
		if got := Expand(tt.in); got != tt.want {
			t.Errorf("Expand(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestRunEnv(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.Env = map[string]string{"SOPS_AGE_KEY_FILE": "~/keys.txt"}
	home, _ := os.UserHomeDir()
	want := "SOPS_AGE_KEY_FILE=" + filepath.Join(home, "keys.txt")
	for _, e := range cfg.RunEnv() {
		if e == want {
			return
		}
	}
	t.Errorf("run env has no %q", want)
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The config belongs in ~/.config, not in the platform's own idea of an
// application directory.
func TestDirFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got, want := Path(), "/xdg/pave/config.yaml"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got, want := Path(), filepath.Join(home, ".config/pave/config.yaml"); got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}
