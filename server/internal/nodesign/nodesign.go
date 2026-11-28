// Package nodesign signs the resident node's own program on a Mac with a code signing certificate of that Mac, under a
// fixed identifier (docs/operations/mac-node-signing.md).
//
// Go's linker signs darwin builds ad hoc and names them a.out. macOS privacy permissions (TCC: Full Disk Access,
// Accessibility, Documents, ...) remember a program by its designated requirement, which for ad-hoc code is the cdhash
// of that one build: every node update was a program TCC had never seen, and the owner granted everything again. A
// certificate's signature has a designated requirement made of the identifier and the certificate, which every build
// signed the same way meets. Re-signing ad hoc under a fixed identifier changes the name alone: the requirement stays
// the cdhash.
package nodesign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/lazyre"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
)

// File is the setting in the data directory (~/.persona/node-signing.json). Without it builds run as they come.
const File = "node-signing.json"

// Config is that file.
type Config struct {
	// Identity is the certificate codesign signs with: its name as `security find-identity -v -p codesigning` prints
	// it (between the quotes), or its SHA-1. Its private key is in the login keychain.
	Identity string `json:"identity"`
	// Identifier is the code signing identifier; empty is DefaultIdentifier of the program.
	Identifier string `json:"identifier,omitempty"`
}

var (
	identifierShape = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9.-]{0,127}$`)
	sha1Shape       = lazyre.New(`^[0-9A-Fa-f]{40}$`)
)

// DefaultIdentifier is the identifier a program is signed under when the setting names none: com.nwverse.personad,
// com.nwverse.persona-node.
func DefaultIdentifier(program string) string {
	if program == "" {
		program = "personad"
	}
	return "com.nwverse." + program
}

// Path is this computer's setting file.
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.DataDir(home), File)
}

// Load reads the setting at path. No file is the zero Config: no signing.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	c.Identity, c.Identifier = strings.TrimSpace(c.Identity), strings.TrimSpace(c.Identifier)
	if err := c.check(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// check refuses what cannot go into a code requirement as it is written: a quote, a backslash or a control character
// in the certificate's name, an identifier that is not a bundle-identifier-like name, ad hoc ("-") as the certificate.
func (c Config) check() error {
	switch {
	case c.Identity == "":
		return errors.New(`"identity" is empty: the certificate's name as security find-identity -v -p codesigning prints it, or its SHA-1`)
	case c.Identity == "-":
		return errors.New(`"identity" "-" is ad hoc, whose designated requirement is the build's cdhash: name a certificate`)
	case len(c.Identity) > 256 || strings.ContainsAny(c.Identity, "\"\\") || strings.ContainsFunc(c.Identity, func(r rune) bool { return r < 0x20 || r == 0x7f }):
		return fmt.Errorf(`"identity" %q is not a certificate name codesign takes`, c.Identity)
	case c.Identifier != "" && !identifierShape.MatchString(c.Identifier):
		return fmt.Errorf(`"identifier" %q: letters, digits, dots and hyphens, like com.nwverse.personad`, c.Identifier)
	}
	return nil
}

// Signer signs files as one Config says, with /usr/bin/codesign.
type Signer struct {
	identity, identifier string
	// codesign runs codesign with args for at most timeout; a variable for tests.
	codesign func(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error)
}

const (
	// signTimeout bounds one signing: a keychain dialog nobody answers holds codesign, and then the build goes on ad
	// hoc rather than keep the node waiting.
	signTimeout = 15 * time.Second
	// verifyTimeout bounds one check of a signature (it hashes the whole file).
	verifyTimeout = 20 * time.Second
)

// SignLimit is the most one Sign or AdHoc waits on codesign: the signing and the check of its result, each bounded
// above (and each process gets a second more to exit). An update's commit counts it once per signed copy
// (nodeupdate.CommitWait).
const SignLimit = signTimeout + verifyTimeout

// New is the signer c sets up for program, nil when it sets up none or this is not a Mac.
func New(c Config, program string) *Signer {
	if c.Identity == "" || runtime.GOOS != "darwin" {
		return nil
	}
	return newSigner(c, program, runCodesign)
}

// FromFile is New with this computer's setting (Path).
func FromFile(program string) (*Signer, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	c, err := Load(Path())
	if err != nil {
		return nil, err
	}
	return New(c, program), nil
}

func newSigner(c Config, program string, run func(context.Context, time.Duration, ...string) ([]byte, error)) *Signer {
	id := c.Identifier
	if id == "" {
		id = DefaultIdentifier(program)
	}
	return &Signer{identity: c.Identity, identifier: id, codesign: run}
}

// Identifier is the identifier builds are signed under.
func (s *Signer) Identifier() string { return s.identifier }

// Requirement is what a file signed as configured meets: the identifier, and the certificate by its name or SHA-1.
// It names no anchor, so a certificate of any issuer can be configured.
func (s *Signer) Requirement() string {
	leaf := `certificate leaf[subject.CN] = "` + s.identity + `"`
	if sha1Shape.MatchString(s.identity) {
		leaf = `certificate leaf = H"` + strings.ToLower(s.identity) + `"`
	}
	return `identifier "` + s.identifier + `" and ` + leaf
}

// Meets says whether path has a valid signature that meets Requirement.
func (s *Signer) Meets(ctx context.Context, path string) bool {
	return s.satisfies(ctx, path, s.Requirement()) == nil
}

// Sign signs path with the certificate under the identifier, and checks the result meets Requirement. path must be a
// file nothing has run yet: macOS keeps what it read of a running program's signature.
func (s *Signer) Sign(ctx context.Context, path string) error {
	if out, err := s.codesign(ctx, signTimeout, "--force", "--sign", s.identity, "--identifier", s.identifier, "--timestamp=none", path); err != nil {
		return explain("codesign with the certificate", out, err)
	}
	if err := s.satisfies(ctx, path, s.Requirement()); err != nil {
		return fmt.Errorf("%w (write the certificate's whole name as security find-identity -v -p codesigning prints it, or its SHA-1)", err)
	}
	return nil
}

// AdHoc signs path ad hoc under the identifier: what an update installs when Sign fails. Only the name changes against
// the linker's signature; TCC still keys it by its cdhash.
func (s *Signer) AdHoc(ctx context.Context, path string) error {
	if out, err := s.codesign(ctx, signTimeout, "--force", "--sign", "-", "--identifier", s.identifier, path); err != nil {
		return explain("codesign ad hoc", out, err)
	}
	return s.satisfies(ctx, path, `identifier "`+s.identifier+`"`)
}

func (s *Signer) satisfies(ctx context.Context, path, requirement string) error {
	out, err := s.codesign(ctx, verifyTimeout, "--verify", "--strict", "-R", "="+requirement, path)
	if err != nil {
		return explain("codesign --verify -R '"+requirement+"'", out, err)
	}
	return nil
}

// explain is a failed codesign run, with what to do about the failures that have a known cause.
func explain(what string, out []byte, err error) error {
	text := strings.Join(strings.Fields(string(out)), " ")
	if text == "" {
		text = err.Error()
	} else {
		text += " (" + err.Error() + ")"
	}
	switch {
	case strings.Contains(text, "errSecInternalComponent") || strings.Contains(text, "User interaction is not allowed"):
		text += ": the login keychain is not open to this process; the node signs as the launchd agent of the logged-in user (gui/<uid>), with the login keychain unlocked"
	case strings.Contains(text, "no identity found") || strings.Contains(text, "ambiguous"):
		text += ": security find-identity -v -p codesigning lists the certificates this keychain can sign with"
	case errors.Is(err, context.DeadlineExceeded):
		text += ": a keychain dialog may be waiting on the screen; answer it with Always Allow"
	}
	return fmt.Errorf("%s: %s", what, text)
}

func runCodesign(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/codesign", args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("did not finish in %s: %w", timeout, context.DeadlineExceeded)
	}
	return out, err
}
