# pave

A terminal front end for running ansible playbooks: pick one, tick the
options, watch it run. Written in Go on tview/tcell, one static binary, no
runtime of its own.

```
pave  ~/dev/ansible                        cert 11h32m · ansible-admin,pi,root
┌ playbooks ──────────────┬ playbooks/onboard.yml ─────────────────────────────┐
│ [p] base  playbooks/    │ Onboard a host onto the SSH user CA.               │
│  *  onboard             │                                                    │
│  *  patch               │   1. connect with a password  (you supply it)      │
│ [p] cluster  playbooks/ │   2. install CA trust + principal mapping          │
│  *  k3s-pve-reboot      │   3. PROVE certificate login works                 │
│  *  pve-upgrade         │   4. only then disable password authentication     │
│                         ├ options ───────────────────────────────────────────┤
│                         │ check mode  [ ]    diff  [x]    verbosity  none    │
│                         │ limit       kubeworkers                            │
│                         │ tags                                               │
│                         │  < run >  < hosts… >  < credentials… >             │
│                         └────────────────────────────────────────────────────┤
│                         $ ansible-playbook playbooks/onboard.yml --diff ...   │
└─────────────────────────┴────────────────────────────────────────────────────┘
```

Nothing about a particular repository layout is built in. pave scans a root
directory, treats every directory with an `ansible.cfg` as a project, and
treats every YAML file that parses as a list of plays as a playbook. It works
against a workspace of side-by-side checkouts, a single ansible repo, or a
directory of loose playbooks.

## Install

```sh
go install github.com/mnorrsken/pave/cmd/pave@latest
```

or `make build`, which puts it in `bin/`.

## Use

```sh
pave                          # scan the current directory
pave -root ~/dev/ansible      # or somewhere else
pave -paths                   # what it would use, and where the config is
```

Press `?` inside for the keys. The short version:

| key | |
|---|---|
| `enter` | on a playbook: move to the options form |
| `F5` | run it |
| `F2` / `L` | pick the limit out of the inventory |
| `F3` | credentials for a host with no certificate yet |
| `c` | sign a short lived certificate and load it into ssh-agent |
| `o` | onboard: the configured playbook with the credentials dialog open |
| `/` `r` `i` | filter the tree, rescan it, reload the inventory |
| `^C` | interrupt the run; again to kill it |
| `esc` | leave the output (the run keeps going); `tab` comes back |
| `s` | save the output of a finished run |

While a run is going, what you type goes to ansible: its vault, become and
host key prompts work exactly as they do in a terminal. The page and arrow
keys stay with pave for scrolling.

## Configuration

`~/.config/pave/config.yaml` (or `$XDG_CONFIG_HOME/pave/config.yaml`), with a
`.pave.yaml` in the root overriding it. Everything is optional; see
[docs/config.example.yaml](docs/config.example.yaml).

```yaml
root: ~/dev/projects/ansible-restructure/workspace
onboard_playbook: base/playbooks/onboard.yml
env:
  SOPS_AGE_KEY_FILE: ~/.config/sops/age/keys.txt
ssh_cert:
  ca_key: ~/ssh-ca/user_ca
  key: ~/.ssh/id_ed25519
  validity: +12h
  principals: [ansible-admin, pi, root]
defaults:
  diff: true
```

`ansible_playbook_bin` is the command a run executes. Point it at a wrapper
script and the runs go wherever that script sends them — a container, another
host — without pave having to know.

## How it runs things

Each run happens with the working directory set to the project and
`ANSIBLE_CONFIG` set to that project's `ansible.cfg`, which is what a run
started by hand from that directory would do: `roles_path`, the inventory and
the vars plugins all resolve the same way. The inventory in the host picker is
whatever `ansible-inventory --list` reports for that project, so encrypted
group vars are decrypted by ansible itself and dynamic inventories work.

The command runs on a pty. That is what makes ansible colour its output and
lets it prompt, and it is why there is no Windows build.

## Passwords

A host that has not been onboarded yet has no certificate, so it needs a
password. Type it into the credentials dialog and pave writes it to a 0600
temporary file passed as `--connection-password-file` /
`--become-password-file`, removed as soon as the run ends. It never goes on
the command line, where every process listing on the machine would see it.
If you would rather pave never held it at all, tick "let ansible prompt
instead" and answer in the output pane.

A killed process (`kill -9`, a crash) can leave the temporary file behind;
nothing else does.

## Development

```sh
make build test race vet fmt
make it        # the integration tests; needs a real ansible on PATH
make dist      # cross-compile into dist/
```

The tests drive the whole interface on a tcell simulation screen with the
runner, the inventory and the certificate reader replaced, so `make test`
needs neither ansible nor a terminal.
