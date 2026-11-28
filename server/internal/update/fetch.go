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
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

// Published is what the publisher put up, as a hub sees it. Nothing is signed (the user's call, docs/operations/hub-update.md
// "Security"): the sha256s guard against a broken download over HTTPS, not against the site itself being replaced.
type Published struct {
	Manifest *Manifest
	// Development pins a Git candidate; Source is its locally compiled feed.
	Development *DevelopmentCandidate
	Source      string
}

// Check reads the full edition's latest.json from base (the publisher's dl/).
func Check(ctx context.Context, client *http.Client, base string) (*Published, error) {
	return CheckFeed(ctx, client, base, nodeproto.EditionFull)
}

// CheckFeed reads edition's own manifest from base (FeedName: latest.json, or the core's core-latest.json, which no
// full install ever asks for) and takes it only when it says that edition, whatever the source serves under the name.
func CheckFeed(ctx context.Context, client *http.Client, base string, edition nodeproto.Edition) (*Published, error) {
	edition = edition.OrFull()
	if base == "" {
		return nil, errors.New("this personad has nowhere to look for releases")
	}
	data, err := get(ctx, client, strings.TrimSuffix(base, "/")+"/"+FeedName(edition), 1<<20)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(data)
	if err != nil {
		return nil, err
	}
	if err := m.ValidateEdition(edition); err != nil {
		return nil, err
	}
	return &Published{Manifest: m}, nil
}

var errNotFound = errors.New("not found")

func get(ctx context.Context, client *http.Client, url string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", url, limit)
	}
	return data, nil
}

// Download fetches f from base into dir and unpacks it to dir/<name without .gz>: the .gz must match the manifest's
// size and sha256, what it unpacks to its binary_sha256. A broken download is tried again (3 times in
// all); anything that does not match is deleted and nothing is left half-written.
func Download(ctx context.Context, client *http.Client, base string, f File, dir string) (string, error) {
	if f.Name == "" || filepath.Base(f.Name) != f.Name || strings.ContainsAny(f.Name, `/\`) {
		return "", errors.New("invalid artifact name")
	}
	remotes := artifactNames(f.Name)
	if f.Served != "" {
		if filepath.Base(f.Served) != f.Served || strings.ContainsAny(f.Served, `/\`) {
			return "", errors.New("invalid artifact name")
		}
		remotes = []string{f.Served} // an edition's own name: no older name stands in for it
	}
	if err := validateDownloadSize(f); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	gz := filepath.Join(dir, f.Name)
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			t := time.NewTimer(time.Duration(attempt) * 2 * time.Second)
			select {
			case <-ctx.Done():
				t.Stop()
				return "", ctx.Err()
			case <-t.C:
			}
		}
		for _, name := range remotes {
			err = fetchFile(ctx, client, strings.TrimSuffix(base, "/")+"/"+name, gz, f)
			if !errors.Is(err, errNotFound) {
				break
			}
		}
		if err == nil {
			break
		}
		if errors.Is(err, errMismatch) || errors.Is(err, errNotFound) {
			return "", err // the file served is not the one in the manifest: trying again will not help
		}
	}
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, strings.TrimSuffix(f.Name, ".gz"))
	if err := unpack(gz, bin, f.BinarySHA256); err != nil {
		_ = os.Remove(gz) // a download that cannot be installed is not kept either
		var local *fs.PathError
		if !errors.Is(err, errMismatch) && !errors.As(err, &local) {
			// An archive that matched the manifest's sha256 and does not unpack: the release's, broken.
			err = fmt.Errorf("%w: %s does not unpack: %v", errMismatch, f.Name, err)
		}
		return "", err
	}
	return bin, nil
}

// DownloadBinary takes the raw bytes from an authenticated Hub package index.
func DownloadBinary(ctx context.Context, client *http.Client, base string, f File, dir string) (string, error) {
	if canonical, legacy := canonicalPackage(f.Name); canonical == "" || legacy || f.Size <= 0 ||
		f.SHA256 == "" || f.SHA256 != f.BinarySHA256 {
		return "", errors.New("invalid published package")
	}
	if limit := unpackedBinaryLimit(); f.Size > limit {
		return "", fmt.Errorf("%w: %s size %d exceeds %d bytes", errMismatch, f.Name, f.Size, limit)
	}
	bin := filepath.Join(dir, f.Name)
	if err := fetchFile(ctx, client, strings.TrimSuffix(base, "/")+"/"+f.Name, bin, f); err != nil {
		return "", err
	}
	return bin, os.Chmod(bin, 0o755)
}

var errMismatch = errors.New("the download does not match the release's manifest")

// IsMismatch: err is Download finding that what was served is not what the manifest says (sizes or sha256s; a file
// that stayed shorter than the manifest's size over every attempt too), not a download that could not be made.
// IsNotFound: the source has no such file (HTTP 404), as opposed to a download that failed or did not check out.
func IsNotFound(err error) bool { return errors.Is(err, errNotFound) }

func IsMismatch(err error) bool {
	var short shortError
	return errors.Is(err, errMismatch) || errors.As(err, &short)
}

// shortError is a download that ended before the manifest's size without a transfer error: cut short, or a file that
// is not the manifest's.
type shortError struct{ error }

func (e shortError) Unwrap() error { return e.error }

const (
	defaultMaxDownloadSize   int64 = 512 << 20
	defaultMaxUnpackedBinary int64 = 512 << 20
)

var (
	maxDownloadSizeOverride   atomic.Int64
	maxUnpackedBinaryOverride atomic.Int64
)

func downloadSizeLimit() int64 {
	if v := maxDownloadSizeOverride.Load(); v > 0 {
		return v
	}
	return defaultMaxDownloadSize
}

func unpackedBinaryLimit() int64 {
	if v := maxUnpackedBinaryOverride.Load(); v > 0 {
		return v
	}
	return defaultMaxUnpackedBinary
}

func validateDownloadSize(f File) error {
	limit := downloadSizeLimit()
	if f.Size <= 0 || f.Size > limit {
		return fmt.Errorf("%w: %s invalid size %d (max %d)", errMismatch, f.Name, f.Size, limit)
	}
	return nil
}

func fetchFile(ctx context.Context, client *http.Client, url, path string, f File) error {
	if err := validateDownloadSize(f); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	tmp := path + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(resp.Body, f.Size+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err // cut off: the next attempt starts again
	}
	if n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		if n < f.Size {
			return shortError{fmt.Errorf("%s: got %d of %d bytes", url, n, f.Size)} // tried again: a transfer cut short
		}
		return fmt.Errorf("%w: %s", errMismatch, f.Name)
	}
	return os.Rename(tmp, path)
}

func unpack(gz, bin, want string) error {
	in, err := os.Open(gz)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer zr.Close()
	tmp := bin + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	h := sha256.New()
	limit := unpackedBinaryLimit()
	n, err := io.Copy(io.MultiWriter(out, h), io.LimitReader(zr, limit+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > limit {
		err = fmt.Errorf("%w: %s unpacked exceeds %d bytes", errMismatch, filepath.Base(gz), limit)
	}
	if cerr := zr.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = fmt.Errorf("%w: %s unpacked", errMismatch, filepath.Base(gz))
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, bin)
}

// CheckBinary runs the new binary's `version -v`: it must say the release's version, the release channel and its
// build number, or it is not the release's binary for this platform (or does not run here).
func CheckBinary(ctx context.Context, bin string, m *Manifest) error {
	got, err := versionOf(ctx, bin)
	if err != nil {
		return fmt.Errorf("the new personad does not run here: %w", err)
	}
	if want := releaseLine(m); got != want {
		return fmt.Errorf("the new personad says %q, not %q", got, want)
	}
	// And it is the release's edition (m.Edition, which Check held to this build's own).
	edition, ok := nodeproto.ParseEdition(m.Edition)
	if !ok {
		return fmt.Errorf("the release names an unknown edition %q", m.Edition)
	}
	return CheckEdition(bin, edition, "the new personad")
}

// versionOf is what bin's `version -v` says, trimmed.
func versionOf(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version", "-v").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(bytes.TrimSpace(out))), nil
}

// releaseLine is what `version -v` says for every binary of release m.
func releaseLine(m *Manifest) string {
	return fmt.Sprintf("%s\nchannel %s, build %d", m.Version, Release, m.Build)
}
