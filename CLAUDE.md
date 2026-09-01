# pave

Terminal front end for running ansible playbooks, in Go on tview/tcell:
project/playbook tree on the left, playbook detail and the run options on the
right, the run itself streaming into an output pane. One static binary for
linux and darwin.

## Commands

- `make build` (to `bin/`), `make run`, `make test`, `make race`, `make vet`,
  `make fmt`, `make lint`
- `make it`: the integration tests (`PAVE_IT=1 go test ./...`). They need a
  real `ansible-inventory` and `ansible-playbook` on PATH.
- `make dist`: the same cross-compile the release does. No windows target: the
  runner needs a pty.

## Verify before done

`make test` green, and `make race` for anything touching the run goroutine.
`make it` when the change touches inventory reading or the command line pave
builds. The UI tests run on a simulation screen and catch a surprising amount,
but colours and focus do not show up in them — run it against a real tree once
for any UI change.

## Layout and conventions

- `internal/discover`: what is a project, what is a playbook. Never a list of
  known names — a project is a directory with an `ansible.cfg`, a playbook is
  a YAML file that parses as a list of plays. Keep it that way; the point of
  the tool is that it works on trees it has never seen.
- `internal/run`: the argv `Spec` builds, and the pty runner. `Spec.Args` is a
  pure function with a table test; anything new goes there, not into the UI.
- `internal/inv`: inventories come from `ansible-inventory --list`, never from
  parsing hosts.yml.
- `internal/sshcert`: shells out to ssh-keygen, so an encrypted CA key can
  prompt and the result is what signing by hand produces.
- `internal/ui`: everything that reaches outside the process is a function on
  `ui.Options`, which is what lets the tests drive the whole app against
  fakes.

## Things that will bite

- **Only the main loop may touch tview state.** Output from a run is parked in
  `outputPane.pending` on the run's goroutine and written into the TextView by
  `flush` on the main loop. `GetText` and the box rectangles are not safe to
  read from anywhere else; both have already caused a race.
- **Escape the brackets.** ansible prints `PLAY [all]`, `TASK [role : task]`,
  `ok: [host]`, and a TextView with dynamic colours eats every one of them as
  a colour tag. `outputPane.w` escapes first, then translates the ANSI codes;
  do not swap the order.
- **Passwords go in files, never in argv.** `--connection-password-file` and
  `--become-password-file`, 0600, removed when the run ends.
- The whole run form counts as a typing context: single letter keys must not
  fire while it has focus.

## Release notes (what differs from the wiki "GitHub Release Process" skill)

- Changelog heading: `## [X.Y.Z] - YYYY-MM-DD`, no `v`. `release.yml` extracts
  that exact section for the release notes and fails without it.
- Pre-1.0: breaking changes are a minor bump.
- Release commit: `Add <feature> (vX.Y.Z)`. Tag `vX.Y.Z`, push main and the tag.
