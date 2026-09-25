package run

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestArgs(t *testing.T) {
	base := Spec{Bin: "ansible-playbook", Playbook: "playbooks/patch.yml"}

	tests := []struct {
		name string
		spec func(s Spec) Spec
		want []string
	}{
		{
			name: "bare",
			spec: func(s Spec) Spec { return s },
			want: []string{"playbooks/patch.yml"},
		},
		{
			name: "check and diff",
			spec: func(s Spec) Spec { s.Check, s.Diff = true, true; return s },
			want: []string{"playbooks/patch.yml", "--check", "--diff"},
		},
		{
			name: "verbosity is capped",
			spec: func(s Spec) Spec { s.Verbosity = 9; return s },
			want: []string{"playbooks/patch.yml", "-vvvv"},
		},
		{
			name: "limit",
			spec: func(s Spec) Spec { s.Limit = "kubeworkers:pve1"; return s },
			want: []string{"playbooks/patch.yml", "--limit", "kubeworkers:pve1"},
		},
		{
			name: "ad-hoc host is both inventory and limit",
			spec: func(s Spec) Spec { s.AdhocHost = "10.0.0.9"; return s },
			want: []string{"playbooks/patch.yml", "--inventory", "10.0.0.9,", "--limit", "10.0.0.9"},
		},
		{
			name: "an explicit limit wins over the ad-hoc one",
			spec: func(s Spec) Spec { s.AdhocHost, s.Limit = "10.0.0.9", "newhost"; return s },
			want: []string{"playbooks/patch.yml", "--inventory", "10.0.0.9,", "--limit", "newhost"},
		},
		{
			name: "password files",
			spec: func(s Spec) Spec {
				s.User, s.ConnPassFile, s.BecomePassFile = "pi", "/tmp/a", "/tmp/b"
				return s
			},
			want: []string{"playbooks/patch.yml", "--user", "pi",
				"--connection-password-file", "/tmp/a", "--become-password-file", "/tmp/b"},
		},
		{
			name: "prompting instead",
			spec: func(s Spec) Spec { s.AskPass, s.AskBecomePass = true, true; return s },
			want: []string{"playbooks/patch.yml", "--ask-pass", "--ask-become-pass"},
		},
		{
			name: "tags, vars and the escape hatch",
			spec: func(s Spec) Spec {
				s.Tags, s.SkipTags, s.ExtraVars = "reboot", "slow", "a=1 b=2"
				s.Forks = 5
				s.ExtraArgs = `--start-at-task "Install packages"`
				return s
			},
			want: []string{"playbooks/patch.yml", "--tags", "reboot", "--skip-tags", "slow",
				"--extra-vars", "a=1 b=2", "--forks", "5",
				"--start-at-task", "Install packages"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.spec(base).Args(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Args() = %q\nwant      %q", got, tt.want)
			}
		})
	}
}

func TestPreviewQuotes(t *testing.T) {
	s := Spec{Bin: "ansible-playbook", Playbook: "playbooks/patch.yml", ExtraVars: "msg=hello world"}
	want := `ansible-playbook playbooks/patch.yml --extra-vars 'hello world'`
	if got := s.Preview(); !strings.Contains(got, "'msg=hello world'") {
		t.Errorf("Preview() = %s, want the extra-vars quoted like %s", got, want)
	}
}

func TestCmdEnv(t *testing.T) {
	s := Spec{Bin: "ansible-playbook", Dir: "/w/base", ConfigFile: "/w/base/ansible.cfg", Playbook: "site.yml"}
	c := s.Cmd([]string{"PATH=/bin"})
	if c.Dir != "/w/base" {
		t.Errorf("dir = %q", c.Dir)
	}
	if !has(c.Env, "ANSIBLE_CONFIG=/w/base/ansible.cfg") || !has(c.Env, "ANSIBLE_FORCE_COLOR=1") {
		t.Errorf("env = %q", c.Env)
	}
	if !has(c.Env, "PATH=/bin") {
		t.Errorf("env dropped the base environment: %q", c.Env)
	}
}

// A project with no ansible.cfg must not export an empty ANSIBLE_CONFIG:
// ansible would take it as "no config at all" rather than "look as usual".
func TestCmdWithoutConfigFile(t *testing.T) {
	c := Spec{Bin: "ansible-playbook", Playbook: "site.yml"}.Cmd(nil)
	for _, e := range c.Env {
		if strings.HasPrefix(e, "ANSIBLE_CONFIG=") {
			t.Errorf("env should not set ANSIBLE_CONFIG: %q", e)
		}
	}
}

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"  ", nil},
		{"-e foo=bar", []string{"-e", "foo=bar"}},
		{`--start-at-task "Install packages"`, []string{"--start-at-task", "Install packages"}},
		{`-e 'json={"a": 1}'`, []string{"-e", `json={"a": 1}`}},
		{"--flush-cache   --step", []string{"--flush-cache", "--step"}},
	}
	for _, tt := range tests {
		if got := SplitArgs(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("SplitArgs(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPasswordFile(t *testing.T) {
	path, err := PasswordFile("hunter2")
	if err != nil {
		t.Fatalf("password file: %v", err)
	}
	defer os.Remove(path)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %v, want 0600", perm)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hunter2\n" {
		t.Errorf("contents = %q", b)
	}
}

// ansible decides whether to colour its output by asking whether stdout is a
// terminal, so Tee has to give it one, and the log has to get the same
// output the screen does.
func TestTeeGivesATerminalAndCopiesTheOutput(t *testing.T) {
	var screen, log bytes.Buffer
	err := Tee(Cmd{
		Path: "sh",
		Args: []string{"-c", "printf '\\033[0;32mok: [web]\\033[0m\\n'; if [ -t 1 ]; then echo tty; else echo pipe; fi; exit 3"},
		Env:  os.Environ(),
	}, &screen, &log)
	if code := ExitCode(err); code != 3 {
		t.Errorf("exit code = %d (%v), want 3", code, err)
	}
	if want := "\x1b[0;32mok: [web]\x1b[0m\r\ntty\r\n"; screen.String() != want {
		t.Errorf("screen = %q, want %q", screen.String(), want)
	}
	if log.String() != screen.String() {
		t.Errorf("log = %q, want what the screen got", log.String())
	}
}

func TestTeeReportsACommandThatWillNotStart(t *testing.T) {
	err := Tee(Cmd{Path: "/nonexistent/ansible-playbook"}, io.Discard, nil)
	if err == nil || ExitCode(err) != -1 {
		t.Errorf("err = %v, want a start failure", err)
	}
}

func has(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
