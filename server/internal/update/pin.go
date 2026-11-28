package update

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeproto"
)

// A release's files are fetched after its latest.json was read, and the source need not serve that release any more
// by then (#9263). A GitHub repository's releases/latest/download is whichever release is newest: one published
// between a check and the download serves its files there, against the manifest checked, and every download failed
// with a mismatch until the hub checked again. So a file of a release read there comes from the release's own tag
// (releaseBase), which does not move; one from any other source whose download does not match has that source read
// once more, and a release published meanwhile is said as such (MovedError) rather than as a broken download.

// githubLatestPath ends a GitHub repository's moving release base.
const githubLatestPath = "/releases/latest/download"

// githubLatestRepo is the repository ("/<owner>/<repo>") base is the releases/latest/download of, "" when base is not
// a GitHub repository's.
func githubLatestRepo(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasSuffix(u.Path, githubLatestPath) {
		return ""
	}
	repo := strings.TrimSuffix(u.Path, githubLatestPath)
	if len(strings.Split(strings.Trim(repo, "/"), "/")) != 2 {
		return ""
	}
	return repo
}

// tagVersion is a release version that can name its tag in a URL: v<version>, the tags releases are published under
// (docs/operations/release.md).
var tagVersion = lazyre.New(`^[0-9A-Za-z][0-9A-Za-z._-]*$`)

// releaseBase is where the files of the release of version are when its latest.json was read at base: for a GitHub
// repository's releases/latest/download, the release's own releases/download/v<version>, which a newer release does
// not change. "" for any other base: there they are only beside latest.json.
func releaseBase(base, version string) string {
	if !tagVersion.MatchString(version) {
		return ""
	}
	var repo string
	trimmed := strings.TrimSuffix(base, "/")
	if repo = githubLatestRepo(trimmed); repo == "" {
		u, err := url.Parse(trimmed)
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || !strings.HasSuffix(u.Path, BetaFeedPath) {
			return ""
		}
		repo = strings.TrimSuffix(u.Path, BetaFeedPath)
		if len(strings.Split(strings.Trim(repo, "/"), "/")) != 2 {
			return ""
		}
	}
	return (&url.URL{Scheme: "https", Host: "github.com", Path: repo + "/releases/download/v" + version}).String()
}

// ReleaseBase is the immutable GitHub release-tag base for a moving update source, or "" for other sources.
// Managers use it to choose the release layout before trying the legacy versioned-directory download.
func ReleaseBase(base, version string) string { return releaseBase(base, version) }

// MovedError is a download that did not match the release checked because its source serves another release now
// (Now): one published between the check and the download. It wraps the mismatch (IsMismatch).
type MovedError struct {
	Checked string     // the version checked
	Now     *Published // what the source has now
	err     error
}

func (e *MovedError) Error() string {
	return fmt.Sprintf("the update source has moved on from %s to %s, whose files it serves now", e.Checked, e.Now.Manifest.Version)
}

func (e *MovedError) Unwrap() error { return e.err }

// DownloadRelease is Download for a file f of release m, whose latest.json was read at base. From a GitHub
// repository's releases/latest/download it comes from the release's own tag; a repository without that tag (one that
// names its tags otherwise) is downloaded from base as before. When what base itself serves does not match, base's
// latest.json is read once more: another release there makes the error a *MovedError; the same release leaves the
// mismatch, a download that is broken. The file is never fetched again either way.
func DownloadRelease(ctx context.Context, client *http.Client, base string, m *Manifest, f File, dir string) (string, error) {
	if pinned := releaseBase(base, m.Version); pinned != "" {
		bin, err := Download(ctx, client, pinned, f, dir)
		if !IsNotFound(err) {
			return bin, err
		}
	}
	bin, err := Download(ctx, client, base, f, dir)
	if !IsMismatch(err) {
		return bin, err
	}
	// The release's own edition's feed: m was taken as that edition when it was read.
	edition, _ := nodeproto.ParseEdition(m.Edition)
	now, cerr := CheckFeed(ctx, client, base, edition)
	if cerr != nil || now.Manifest.Build == m.Build && now.Manifest.Version == m.Version {
		return "", err
	}
	return "", &MovedError{Checked: m.Version, Now: now, err: err}
}
