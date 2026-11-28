package nodeupdate

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
)

// Signing a build before it runs (Options.Signer; docs/operations/mac-node-signing.md). macOS privacy permissions
// follow a certificate's signature from build to build, never an ad-hoc one, so every build this node runs should be
// signed first. An update signs its staged copy before the swap and its transaction records that file's sha256
// (signedStage); any other path that puts a build here (the hub's ssh deploy script, tailcat, deploy.sh, install.sh, a
// copy by hand) leaves one the resident node signs before it runs it (SignInstalled). Signing never keeps a build from
// running: what fails is logged, and the build goes on ad hoc.

// signedStage is the file Commit installs and its sha256: with a Signer, a copy of the upload signed with the
// certificate; when that fails, one signed ad hoc under the configured identifier (the build is new either way, so
// that costs no permission that still applied); when that fails too, the upload as it came.
func (m *Manager) signedStage(ctx context.Context, u *upload) (string, string) {
	if preserveOfficial(ctx, u.Path) {
		m.logSign("node signing: update %s keeps its official release signature", u.Spec.Version)
		return u.Path, u.Spec.SHA256
	}
	if m.signer == nil {
		return u.Path, u.Spec.SHA256
	}
	check := func(path string) error {
		_, err := selfCheck(ctx, path, u.Spec, m.requireResidentHandover)
		return err
	}
	for _, adhoc := range []bool{false, true} {
		path, err := m.signedCopy(ctx, u.Path, adhoc, check)
		var sum string
		if err == nil {
			if sum, err = fileHash(path); err != nil {
				_ = os.Remove(path)
			}
		}
		switch {
		case err == nil && adhoc:
			m.logSign("node signing: update %s installs signed ad hoc", u.Spec.Version)
			return path, sum
		case err == nil:
			m.logSign("node signing: update %s installs signed with the certificate", u.Spec.Version)
			return path, sum
		case !adhoc:
			m.logSign("node signing: update %s could not be signed with the certificate: %v; signing it ad hoc", u.Spec.Version, err)
		default:
			m.logSign("node signing: update %s installs with the signature it came with: %v", u.Spec.Version, err)
		}
	}
	return u.Path, u.Spec.SHA256
}

// SignInstalled signs the installed program when a Signer is set up and the file does not meet it: a build another
// path put there, or one installed before the setting. A checked, signed copy replaces the file only while the file is
// still the build it was copied from (renameStill); then it says true, and the caller runs the file again before it
// serves (at start) or as its reload, so the code running is the signed one. An update awaiting confirmation is left
// alone: its commit signed what it installed, and its transaction compares the file with that sha256. A failure is
// logged and leaves the file as it is, with the signature it has: one that may still match the permissions given to it.
func (m *Manager) SignInstalled(ctx context.Context) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.signer == nil || m.exe == "" || m.closed.Load() || updatePending(m.exe) || m.signer.Meets(ctx, m.exe) {
		return false
	}
	signed, err := m.signInstalledLocked(ctx)
	if err != nil {
		m.logSign("node signing: %s keeps its signature: %v", m.exe, err)
		return false
	}
	m.logSign("node signing: %s signed with the certificate (%s)", m.exe, signed)
	return true
}

func (m *Manager) signInstalledLocked(ctx context.Context) (string, error) {
	sum, err := fileHash(m.exe)
	if err != nil {
		return "", err
	}
	want, err := runCheck(ctx, m.exe)
	if err != nil {
		return "", err
	}
	signed, err := m.signedCopy(ctx, m.exe, false, func(path string) error {
		got, err := runCheck(ctx, path)
		if err == nil && got != want {
			err = errors.New("the signed copy's node-update-check differs from the installed file's")
		}
		return err
	})
	if err != nil {
		return "", err
	}
	newSum, err := fileHash(signed)
	if err == nil {
		err = locked(m.exe, func() error {
			if updatePending(m.exe) {
				return errors.New("a node update began meanwhile")
			}
			if err := renameStill(signed, m.exe, sum); err != nil {
				return err
			}
			return syncDir(filepath.Dir(m.exe))
		})
	}
	if err != nil {
		_ = os.Remove(signed)
		return "", err
	}
	// Keep the installed identity after signing so a later Commit can detect external replacements.
	if st, err := executableInfo(m.exe); err == nil {
		m.identity = st
	}
	return "sha256 " + newSum, nil
}

// signedCopy copies src to a new staged file beside the program, signs it (ad hoc under the identifier with adhoc) and
// has check run it. A copy: what codesign rewrites is a file nothing has run yet, and the file src stays as it was;
// named like an upload, so one a crash leaves is swept with stale uploads. Removed when a step fails.
func (m *Manager) signedCopy(ctx context.Context, src string, adhoc bool, check func(string) error) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(m.exe), ".node-update-stage-*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	err = copyInto(f, src)
	if err == nil {
		err = os.Chmod(path, 0o755)
	}
	if err == nil {
		if adhoc {
			err = m.signer.AdHoc(ctx, path)
		} else {
			err = m.signer.Sign(ctx, path)
		}
	}
	if err == nil {
		err = check(path)
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

// copyInto writes src's bytes to f, then closes it.
func copyInto(f *os.File, src string) error {
	in, err := os.Open(src)
	if err == nil {
		defer in.Close()
		_, err = io.Copy(f, in)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// dropCopy removes a signed copy Commit made of the upload and did not install.
func dropCopy(stage, upload string) {
	if stage != upload {
		_ = os.Remove(stage)
	}
}

// updatePending says an update transaction owns the program (its marker is there, or cannot be told apart from one).
func updatePending(exe string) bool {
	_, err := os.Stat(markerPath(exe))
	return !os.IsNotExist(err)
}

func (m *Manager) logSign(format string, args ...any) {
	if m.logf != nil {
		m.logf(format, args...)
		return
	}
	log.Printf(format, args...)
}
