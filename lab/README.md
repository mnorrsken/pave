# lab

Four containers with sshd on them and a workspace of playbooks to point pave
at, so there is something real to run without touching anything that matters.
Everything here uses `ansible.builtin` only: no collections, no roles from
galaxy, nothing to install but ansible itself and docker.

```sh
make lab          # start the containers, build pave, open it on the workspace
make lab-status   # what is running
make lab-down     # remove it all
```

`make lab-up` generates `lab/.ssh/id_ed25519` on the first run, bakes the
public half into the image and starts one container per host. They listen on
the loopback address only — `127.0.0.1:2221` upwards — and password login is
off, so the key file is the only way in. `make lab-down` removes them; the key
stays, and `rm -rf lab/.ssh` gets rid of that too.

Without pave:

```sh
cd lab/workspace/base
ansible-playbook playbooks/ping.yml
ansible-playbook playbooks/site.yml --check --diff -l web
```

## What is in it

```
lab/workspace/
  inventory/hosts.yml       web1 web2 · db1 · edge1, and the ports they are on
  inventory/group_vars/     the connection settings and a few role variables
  base/                     a project: ansible.cfg, playbooks/, roles/
  edge/                     a second one, so the tree has more than one
```

| playbook | what it does |
|---|---|
| `base/playbooks/ping.yml` | ping and gather facts. Run this first. |
| `base/playbooks/facts.yml` | prints a lot, for watching the output pane |
| `base/playbooks/site.yml` | the `baseline` role: users, a motd, `/etc/lab.conf` |
| `base/playbooks/patch.yml` | `apt` upgrade. Needs the network; try `--check` |
| `edge/site.yml` | two plays, one serial, the `webroot` role, tags |

Between them they cover `ping`, `setup`, `debug`, `command`, `copy`,
`template`, `file`, `stat`, `user`, `group`, `lineinfile`, `blockinfile`,
`assert` and `apt`, with handlers, tags, loops, `serial`, `become` and
group_vars — enough for the right hand pane to have something to say about
every one of them.

## Notes

- The containers get new host keys every time they start, so the lab's
  `ansible.cfg` turns host key checking off and sends the keys to
  `/dev/null` rather than your `known_hosts`.
- `become` is passwordless sudo. Real hosts would ask, and pave's credentials
  dialog is what answers; there is nothing here to try it against.
- `DOCKER=podman make lab-up` works the same way.
