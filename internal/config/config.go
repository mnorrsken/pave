// Package config holds pave's settings: where to look for ansible projects,
// how to run ansible-playbook, and what a signed SSH certificate should say.
//
// Two files, both optional and both hand-edited: a user-wide one (see Path)
// and a .pave.yaml in the root being browsed, which overrides it. Neither
// ever holds a secret.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the per-root override file, looked for in the root directory.
const FileName = ".pave.yaml"

// Config is the merged settings. Zero values mean "not set"; Defaults fills
// in what the user left out.
type Config struct {
	// Root is the directory to scan for ansible projects. The -root flag and
	// $PAVE_ROOT win over it.
	Root string `yaml:"root"`

	// PlaybookDirs are the subdirectories of a project to scan, on top of the
	// project directory itself. Defaults to playbooks/ and playbook/.
	PlaybookDirs []string `yaml:"playbook_dirs"`

	// Exclude are directory names never descended into while scanning.
	Exclude []string `yaml:"exclude"`

	// OnboardPlaybook is what the onboarding shortcut preselects, relative to
	// the root, e.g. base/playbooks/onboard.yml. Empty means "pick one".
	OnboardPlaybook string `yaml:"onboard_playbook"`

	// AnsiblePlaybookBin is the command a run executes. Point it at a wrapper
	// script to run playbooks inside a container.
	AnsiblePlaybookBin string `yaml:"ansible_playbook_bin"`

	// AnsibleInventoryBin is the command that lists the inventory.
	AnsibleInventoryBin string `yaml:"ansible_inventory_bin"`

	// Env is added to the environment of every run, for things like
	// SOPS_AGE_KEY_FILE that ansible needs but the shell may not carry.
	Env map[string]string `yaml:"env"`

	SSHCert  SSHCert  `yaml:"ssh_cert"`
	Defaults Defaults `yaml:"defaults"`
}

// SSHCert describes the short-lived operator certificate pave signs.
type SSHCert struct {
	// CAKey is the user CA private key that signs.
	CAKey string `yaml:"ca_key"`
	// Key is the key to sign; the certificate is written to <Key>-cert.pub.
	Key string `yaml:"key"`
	// Validity is an ssh-keygen -V interval, e.g. +12h.
	Validity string `yaml:"validity"`
	// Principals go into -n. Empty means ssh-keygen's own default.
	Principals []string `yaml:"principals"`
	// Identity is the -I key ID. Empty means <user>@<host>.
	Identity string `yaml:"identity"`
	// AddToAgent loads the key and its fresh certificate into ssh-agent.
	AddToAgent *bool `yaml:"add_to_agent"`
}

// Defaults are the run options a newly opened playbook starts with.
type Defaults struct {
	Check     bool `yaml:"check"`
	Diff      bool `yaml:"diff"`
	Verbosity int  `yaml:"verbosity"`
}

// Dir is pave's config directory: $XDG_CONFIG_HOME/pave, otherwise
// ~/.config/pave. Not os.UserConfigDir, which on macOS points at ~/Library/
// Application Support — a terminal tool's config belongs next to everything
// else in ~/.config, where a dotfiles repo can pick it up.
func Dir() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "pave")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Nothing sensible left to do but stay relative rather than fail at
		// startup; -paths makes it obvious what happened.
		return ".pave"
	}
	return filepath.Join(home, ".config", "pave")
}

// Path is the user-wide config file.
func Path() string { return filepath.Join(Dir(), "config.yaml") }

// Load reads path, then applies the defaults. A missing file is not an error:
// pave is useful with no config at all, browsing the current directory.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if err := decodeInto(path, cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	return cfg, nil
}

// LoadRootOverride overlays root/.pave.yaml onto an already loaded Config.
// Only the keys the file sets are touched, so a root file can flip one flag
// without restating everything.
func (c *Config) LoadRootOverride(root string) error {
	if err := decodeInto(filepath.Join(root, FileName), c); err != nil {
		return err
	}
	c.applyDefaults()
	return nil
}

func decodeInto(path string, cfg *Config) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, cfg); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func (c *Config) applyDefaults() {
	if len(c.PlaybookDirs) == 0 {
		c.PlaybookDirs = []string{"playbooks", "playbook"}
	}
	if len(c.Exclude) == 0 {
		c.Exclude = []string{
			".git", ".galaxy", "node_modules", "venv", ".venv",
			// Ansible's own furniture: none of it holds a playbook, and
			// roles/ and collections/ can be very large.
			"roles", "collections", "group_vars", "host_vars", "files", "templates",
		}
	}
	if c.AnsiblePlaybookBin == "" {
		c.AnsiblePlaybookBin = "ansible-playbook"
	}
	if c.AnsibleInventoryBin == "" {
		c.AnsibleInventoryBin = "ansible-inventory"
	}
	if c.SSHCert.Key == "" {
		c.SSHCert.Key = "~/.ssh/id_ed25519"
	}
	if c.SSHCert.CAKey == "" {
		c.SSHCert.CAKey = "~/ssh-ca/user_ca"
	}
	if c.SSHCert.Validity == "" {
		c.SSHCert.Validity = "+12h"
	}
	if c.SSHCert.AddToAgent == nil {
		yes := true
		c.SSHCert.AddToAgent = &yes
	}
}

// Expand resolves a leading ~ and any $VAR in a configured path. Paths in the
// config file are written the way they are in a shell.
func Expand(path string) string {
	if path == "" {
		return ""
	}
	path = os.ExpandEnv(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// RunEnv is the environment for a run: the process environment, plus the
// configured extras with their paths expanded.
func (c *Config) RunEnv() []string {
	env := os.Environ()
	for k, v := range c.Env {
		env = append(env, k+"="+Expand(v))
	}
	return env
}
