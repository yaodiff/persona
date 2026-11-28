package update

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

// VerifyRelease is a publisher's last check before uploading a release (docs/operations/hub-update.md "Version manifest"):
// every download m lists is in dir at its size and sha256s, every binary carries m's build number, and the ones this
// machine runs (its own platform's) say m's version, the release channel and that build. A manifest written in
// another run than its binaries, with another build number (v0.2.1, #4522), fails here rather than on every hub
// that installs it.
func VerifyRelease(ctx context.Context, dir string, m *Manifest) error {
	if m == nil || len(m.Files) == 0 {
		return errors.New("the manifest lists no personad downloads")
	}
	if _, err := ParseNumber(strconv.FormatInt(m.Build, 10)); err != nil {
		return err
	}
	edition, ok := nodeproto.ParseEdition(m.Edition)
	if !ok {
		return fmt.Errorf("the manifest names an unknown edition %q", m.Edition)
	}
	type entry struct {
		program, platform string
		file              File
	}
	var all []entry
	for plat, f := range m.Files {
		all = append(all, entry{"personad", plat, f})
	}
	for _, group := range []map[string]map[string]File{m.Programs, m.Nodes, {"persona-link": m.Links}} {
		for prog, plats := range group {
			for plat, f := range plats {
				all = append(all, entry{prog, plat, f})
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].file.Name < all[j].file.Name })
	here := runtime.GOOS + "-" + runtime.GOARCH
	tmp, err := os.MkdirTemp("", "persona-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for _, e := range all {
		run := ""
		if e.platform == here {
			run = filepath.Join(tmp, e.file.Name+".bin")
		}
		if err := verifyDownload(dir, e.file, m.Build, run); err != nil {
			return fmt.Errorf("%s for %s: %w", e.program, e.platform, err)
		}
		if run == "" {
			continue
		}
		if got, err := versionOf(ctx, run); err != nil {
			return fmt.Errorf("%s for %s does not run here: %w", e.program, e.platform, err)
		} else if want := releaseLine(m); got != want {
			return fmt.Errorf("%s says %q, not %q: the manifest was written for another build than this binary", e.file.Name, got, want)
		}
		// A release is of one edition: a full build listed in the core's manifest (or the reverse) would be installed
		// by every hub that trusts it. Read from the build info; a file without any says nothing here.
		if got, ok := EditionOf(run); ok && got != edition {
			return fmt.Errorf("%s is the %s edition, in a manifest of the %s edition", e.file.Name, got, edition)
		}
	}
	// Helpers carry their own version, not the release's build number: their bytes are checked, and the one for this
	// machine has to run and say its name.
	helpers := make([]string, 0, len(m.Helpers))
	for name := range m.Helpers {
		helpers = append(helpers, name)
	}
	sort.Strings(helpers)
	for _, name := range helpers {
		plats := make([]string, 0, len(m.Helpers[name]))
		for plat := range m.Helpers[name] {
			plats = append(plats, plat)
		}
		sort.Strings(plats)
		for _, plat := range plats {
			f := m.Helpers[name][plat]
			run := ""
			if plat == here {
				run = filepath.Join(tmp, f.Name+".bin")
			}
			if err := verifyDownload(dir, f, 0, run); err != nil {
				return fmt.Errorf("%s for %s: %w", name, plat, err)
			}
			if run == "" {
				continue
			}
			if got, err := helperVersion(ctx, run); err != nil {
				return fmt.Errorf("%s for %s does not run here: %w", name, plat, err)
			} else if !strings.HasPrefix(got, name+" ") {
				return fmt.Errorf("%s says %q, not %s's version", f.Name, got, name)
			}
		}
	}
	return nil
}

// helperVersion is what a helper's `version` prints ("persona-iphone-bridge 0.1.0 (wire 1)").
func helperVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// verifyDownload checks f in dir against the manifest: size and sha256 of the archive, sha256 of what it unpacks to,
// and build in that (a binary of another run lacks it). With run set, the unpacked binary is written there, runnable,
// for the caller to run instead.
func verifyDownload(dir string, f File, build int64, run string) error {
	if f.Name == "" || filepath.Base(f.Name) != f.Name || strings.ContainsAny(f.Name, `/\`) {
		return fmt.Errorf("invalid artifact name %q", f.Name)
	}
	in, err := os.Open(filepath.Join(dir, f.Name))
	if err != nil {
		return err
	}
	defer in.Close()
	outer := sha256.New()
	n, err := io.Copy(outer, in)
	if err != nil {
		return err
	}
	if n != f.Size || hex.EncodeToString(outer.Sum(nil)) != f.SHA256 {
		return fmt.Errorf("%s is not the file the manifest lists (size or sha256)", f.Name)
	}
	if _, err := in.Seek(0, io.SeekStart); err != nil {
		return err
	}
	zr, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	defer zr.Close()
	inner := sha256.New()
	stamp := newStampScan(build)
	sinks := []io.Writer{inner, stamp}
	var out *os.File
	if run != "" {
		if out, err = os.OpenFile(run, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755); err != nil {
			return err
		}
		defer out.Close()
		sinks = append(sinks, out)
	}
	limit := unpackedBinaryLimit()
	unpacked, err := io.Copy(io.MultiWriter(sinks...), io.LimitReader(zr, limit+1))
	if err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	if unpacked > limit {
		return fmt.Errorf("%s: unpacked binary exceeds %d bytes", f.Name, limit)
	}
	if err := zr.Close(); err != nil {
		return fmt.Errorf("%s: %w", f.Name, err)
	}
	if out != nil {
		if err := out.Close(); err != nil {
			return err
		}
	}
	if hex.EncodeToString(inner.Sum(nil)) != f.BinarySHA256 {
		return fmt.Errorf("%s does not unpack to the binary the manifest lists (binary_sha256)", f.Name)
	}
	// One that runs here says its build itself (VerifyRelease), with its version. A helper (build 0) has none.
	if build != 0 && run == "" && !stamp.done() {
		return fmt.Errorf("%s is not build %d: the manifest was written for another build than this binary", f.Name, build)
	}
	return nil
}

// stampScan looks for a build number in a binary as it streams by: its twelve digits with no digit right before or
// after. The -X main.build value a release is built with is kept in every binary as is, whatever its platform, so a
// binary of another platform is checked without running it.
type stampScan struct {
	token []byte
	carry []byte // the end of what came before: a number cut in two is found when the rest arrives
	start bool   // carry begins at the binary's first byte
	found bool
}

func newStampScan(build int64) *stampScan {
	return &stampScan{token: []byte(strconv.FormatInt(build, 10)), start: true}
}

func (s *stampScan) Write(p []byte) (int, error) {
	if s.found {
		return len(p), nil
	}
	buf := append(s.carry, p...)
	s.found = s.search(buf, false)
	// Keep the number's length and one more: a number that ends at the end of buf is looked at again, with the byte
	// after it, and the byte before it still there.
	if keep := len(s.token) + 1; len(buf) > keep {
		s.carry = append([]byte(nil), buf[len(buf)-keep:]...)
		s.start = false
	} else {
		s.carry = buf
	}
	return len(p), nil
}

// done says whether the number was found, the end of the binary counting as a byte that is no digit.
func (s *stampScan) done() bool { return s.found || s.search(s.carry, true) }

// search finds the number standing alone in buf; one that reaches buf's end counts only at the end of the binary.
func (s *stampScan) search(buf []byte, end bool) bool {
	digit := func(b byte) bool { return b >= '0' && b <= '9' }
	for from := 0; ; {
		i := bytes.Index(buf[from:], s.token)
		if i < 0 {
			return false
		}
		at, after := from+i, from+i+len(s.token)
		before := (at == 0 && s.start) || (at > 0 && !digit(buf[at-1]))
		if before && ((after == len(buf) && end) || (after < len(buf) && !digit(buf[after]))) {
			return true
		}
		from = at + 1
	}
}
