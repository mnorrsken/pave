// Package sshcert signs and inspects short-lived SSH user certificates. It
// shells out to ssh-keygen rather than signing in Go on purpose: the CA key
// stays where ssh-keygen expects it, an encrypted one can prompt for its
// passphrase, and the result is byte for byte what signing by hand produces.
package sshcert

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mnorrsken/pave/internal/run"
)

// timeLayout is how ssh-keygen -L prints the validity window, in local time.
const timeLayout = "2006-01-02T15:04:05"

// Status is what the certificate next to a key currently says.
type Status struct {
	// Path is the certificate file, <key>-cert.pub.
	Path string
	// Present is false when there is no certificate yet.
	Present bool
	KeyID   string
	// Principals are the usernames the certificate may log in as.
	Principals []string
	ValidFrom  time.Time
	ValidTo    time.Time
	// Forever is set for a certificate with no expiry, which is exactly what
	// a short-lived one should never be.
	Forever bool
}

// CertPath is where OpenSSH looks for a key's certificate.
func CertPath(key string) string { return key + "-cert.pub" }

// Read inspects the certificate belonging to key. A missing certificate is
// not an error: it is the normal state at the start of the day.
func Read(key string) (Status, error) {
	path := CertPath(key)
	if _, err := os.Stat(path); err != nil {
		return Status{Path: path}, nil
	}
	out, err := run.Output("ssh-keygen", "-L", "-f", path)
	if err != nil {
		return Status{Path: path}, fmt.Errorf("read certificate: %w", err)
	}
	st := ParseStatus(out)
	st.Path = path
	st.Present = true
	return st, nil
}

// ParseStatus reads `ssh-keygen -L` output. Everything is a "Field: value"
// line except the principals, which are listed one per line underneath their
// own heading.
func ParseStatus(out string) Status {
	var st Status
	inPrincipals := false

	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		name, value, isField := field(t)
		if !isField {
			if inPrincipals {
				st.Principals = append(st.Principals, t)
			}
			continue
		}

		inPrincipals = name == "Principals" && !strings.Contains(value, "(none)")
		switch name {
		case "Key ID":
			st.KeyID = strings.Trim(value, `"`)
		case "Valid":
			parseValid(&st, value)
		}
	}
	return st
}

// field splits "Key ID: \"x@y\"" into its name and value. A principal name
// has no colon in it, which is what makes this enough to tell the two apart.
func field(line string) (name, value string, ok bool) {
	i := strings.Index(line, ":")
	if i <= 0 {
		return "", "", false
	}
	name = line[:i]
	for _, r := range name {
		if !(r == ' ' || r == '-' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return "", "", false
		}
	}
	return name, strings.TrimSpace(line[i+1:]), true
}

// parseValid reads "forever" or "from <t> to <t>".
func parseValid(st *Status, s string) {
	if strings.Contains(s, "forever") {
		st.Forever = true
		return
	}
	fields := strings.Fields(s)
	for i := 0; i+1 < len(fields); i++ {
		t, err := time.ParseInLocation(timeLayout, fields[i+1], time.Local)
		if err != nil {
			continue
		}
		switch fields[i] {
		case "from":
			st.ValidFrom = t
		case "to":
			st.ValidTo = t
		}
	}
}

// Remaining is how long the certificate is still good for, zero once it has
// expired or when there is none.
func (s Status) Remaining(now time.Time) time.Duration {
	if !s.Present || s.Forever || s.ValidTo.IsZero() {
		return 0
	}
	if d := s.ValidTo.Sub(now); d > 0 {
		return d
	}
	return 0
}

// Expired reports a certificate that exists but can no longer be used.
func (s Status) Expired(now time.Time) bool {
	if !s.Present || s.Forever || s.ValidTo.IsZero() {
		return false
	}
	return !now.Before(s.ValidTo)
}

// Request describes the certificate to sign.
type Request struct {
	// CAKey is the user CA private key.
	CAKey string
	// Key is the key to certify; the result lands at <Key>-cert.pub.
	Key string
	// Validity is an ssh-keygen -V interval such as +12h.
	Validity string
	// Identity is the key ID recorded in the certificate, for the logs on the
	// far end. Empty means <user>@<host>.
	Identity string
	// Principals are the usernames the certificate is good for.
	Principals []string
	// AddToAgent loads the key and its new certificate into ssh-agent, which
	// is what makes the certificate actually get used.
	AddToAgent bool
}

// Step is one command in signing: the signing itself, then loading the result
// into the agent. Each runs on a pty so an encrypted key can ask for its
// passphrase.
type Step struct {
	Label string
	Cmd   run.Cmd
	// AllowFail marks a step whose failure does not stop the rest — dropping
	// a key that was never in the agent, for one.
	AllowFail bool
}

// Steps builds the commands for a request, after checking the inputs and
// backing up any certificate that is about to be replaced.
func (r Request) Steps(env []string) ([]Step, error) {
	if r.CAKey == "" || r.Key == "" {
		return nil, fmt.Errorf("both a CA key and a key to sign are needed")
	}
	if _, err := os.Stat(r.CAKey); err != nil {
		return nil, fmt.Errorf("CA private key not found: %s", r.CAKey)
	}
	pub := r.Key + ".pub"
	if _, err := os.Stat(pub); err != nil {
		return nil, fmt.Errorf("public key not found: %s", pub)
	}
	if err := backup(CertPath(r.Key)); err != nil {
		return nil, err
	}

	identity := r.Identity
	if identity == "" {
		identity = defaultIdentity()
	}
	validity := r.Validity
	if validity == "" {
		validity = "+12h"
	}

	args := []string{"-s", r.CAKey, "-I", identity, "-V", validity,
		// A fresh serial per signing, so two certificates issued in the same
		// second are still distinguishable in a log.
		"-z", strconv.FormatInt(time.Now().Unix(), 10)}
	if len(r.Principals) > 0 {
		args = append(args, "-n", strings.Join(r.Principals, ","))
	}
	args = append(args, pub)

	steps := []Step{{
		Label: "sign " + CertPath(r.Key),
		Cmd:   run.Cmd{Path: "ssh-keygen", Args: args, Env: env},
	}}
	if r.AddToAgent {
		steps = append(steps,
			Step{Label: "drop the old key from ssh-agent", AllowFail: true,
				Cmd: run.Cmd{Path: "ssh-add", Args: []string{"-d", r.Key}, Env: env}},
			Step{Label: "load the key and certificate into ssh-agent",
				Cmd: run.Cmd{Path: "ssh-add", Args: []string{r.Key}, Env: env}},
		)
	}
	return steps, nil
}

// backup keeps the certificate being replaced, in case a signing goes wrong
// while the old one was still usable.
func backup(cert string) error {
	b, err := os.ReadFile(cert)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", cert, err)
	}
	name := cert + ".bak." + time.Now().Format("20060102150405")
	if err := os.WriteFile(name, b, 0o644); err != nil {
		return fmt.Errorf("back up %s: %w", cert, err)
	}
	return nil
}

func defaultIdentity() string {
	user := os.Getenv("USER")
	if user == "" {
		user = "operator"
	}
	host, err := os.Hostname()
	if err != nil {
		return user
	}
	if i := strings.Index(host, "."); i > 0 {
		host = host[:i]
	}
	return user + "@" + host
}
