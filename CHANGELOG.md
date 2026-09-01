# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the versions
follow [semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
