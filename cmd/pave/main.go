// Command pave is a terminal front end for running ansible playbooks.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mnorrsken/pave/internal/config"
	"github.com/mnorrsken/pave/internal/ui"
	"github.com/mnorrsken/pave/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "pave:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		root        = flag.String("root", "", "directory to scan for ansible projects")
		configPath  = flag.String("config", "", "override the config file")
		showVersion = flag.Bool("version", false, "print the version and exit")
		showPaths   = flag.Bool("paths", false, "print what pave would use and exit")
	)
	flag.Usage = usage
	flag.Parse()

	if *showVersion {
		fmt.Println("pave", version.String())
		return nil
	}

	path := config.Path()
	if *configPath != "" {
		path = *configPath
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}

	dir, err := rootDir(*root, cfg)
	if err != nil {
		return err
	}
	// A .pave.yaml in the root wins over the user-wide file: it belongs to
	// the tree being browsed.
	if err := cfg.LoadRootOverride(dir); err != nil {
		return err
	}

	if *showPaths {
		fmt.Println("config: ", path)
		fmt.Println("root:   ", dir)
		fmt.Println("override:", filepath.Join(dir, config.FileName))
		fmt.Println("playbook:", cfg.AnsiblePlaybookBin)
		fmt.Println("key:    ", config.Expand(cfg.SSHCert.Key))
		fmt.Println("CA key: ", config.Expand(cfg.SSHCert.CAKey))
		return nil
	}

	// The terminal is left to tview to open: it is the only path that reports
	// a failure to do so.
	app := ui.New(ui.Options{Root: dir, Config: cfg})
	if err := app.Run(); err != nil {
		return fmt.Errorf("open terminal: %w", err)
	}
	return nil
}

// rootDir is the directory to scan: the flag, then the environment, then the
// config file, then wherever pave was started.
func rootDir(flagValue string, cfg *config.Config) (string, error) {
	for _, candidate := range []string{flagValue, os.Getenv("PAVE_ROOT"), cfg.Root} {
		if candidate == "" {
			continue
		}
		return filepath.Abs(config.Expand(candidate))
	}
	return os.Getwd()
}

func usage() {
	fmt.Fprintf(flag.CommandLine.Output(), `pave %s — run ansible playbooks from a terminal interface

Usage:
  pave [flags]

Flags:
`, version.String())
	flag.PrintDefaults()
	fmt.Fprintf(flag.CommandLine.Output(), `
pave scans the root for directories with an ansible.cfg, lists the playbooks
it finds in each, and runs the one you pick with the options you tick. Press ?
inside for the keys.

Settings live in %s, and a .pave.yaml in the root overrides them.
`, config.Path())
}
