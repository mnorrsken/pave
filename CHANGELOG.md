# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions
follow [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.1] - 2026-09-03

### Changed

- The host names in the README screen and in the test fixtures are made up
  ones now rather than a real inventory. pave itself is unchanged from 0.2.0.

## [0.2.0] - 2026-09-03

### Added

- The pane on the right now says what a playbook would do before it is run:
  every play, the hosts its `hosts:` pattern resolves to in the project's own
  inventory, the roles it applies, the modules its own tasks call, the tags it
  can be run with, and whether it runs serially or escalates. The inventory is
  read in the background the first time a project is selected, so the tree
  stays usable while `ansible-inventory` works.

### Changed

- The run options are a dialog now rather than a pane in the layout: enter or
  F5 opens them for the selected playbook, F5 inside them starts the run, esc
  cancels. They are filled in immediately before a run, which is the only time
  they mean anything, and the right hand side belongs to the playbook instead.

## [0.1.1] - 2026-09-02

### Fixed

- The integration tests no longer time out on a cold CI runner, which is what
  stopped 0.1.0 from publishing. pave itself is unchanged from 0.1.0.

## [0.1.0] - 2026-09-02

### Added

- Browse the ansible projects under a root: any directory with an
  `ansible.cfg` is a project, and any YAML file that parses as a list of plays
  is a playbook. Subdirectories are shown as a tree.
- Run a playbook with check mode, diff, verbosity, limit, tags, skip tags,
  extra vars and a free-form argument field, with the exact command shown
  before it runs.
- Output streams into the app on a pty, colours and all. What you type goes to
  ansible, so its vault, become and host key prompts work; `^C` interrupts,
  a second one kills.
- Pick the limit out of the real inventory, read with `ansible-inventory
  --list` so static, ini, dynamic and plugin inventories all work.
- Sign a short-lived SSH user certificate and load it into ssh-agent, with the
  time left shown in the header.
- Reach a host that has no certificate yet with a username and password, or an
  address that is not in the inventory at all. Passwords go to 0600 files that
  are removed when the run ends, never onto the command line.
- Save the output of a finished run to a file.
