package update

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

// A versioned prerelease owns immutable files. The beta-channel Release only
// points clients at its latest.json/android.json; it is not a source tag.
const (
	ChannelStable = "release"
	ChannelBeta   = "beta"
	BetaFeedTag   = "beta-channel"
	BetaFeedPath  = "/releases/download/" + BetaFeedTag
)

// ValidateChannel keeps a feed from another channel out of this selection.
// Old stable manifests have no channel field and remain stable.
func (m *Manifest) ValidateChannel(channel string) error {
	actual := m.Channel
	if actual == "" {
		actual = ChannelStable
	}
	if actual != channel {
		return fmt.Errorf("the update manifest belongs to the %s channel, not %s", actual, channel)
	}
	if channel == ChannelBeta {
		if !strings.Contains(m.Version, "-beta.") || m.Build <= 0 {
			return errors.New("the beta update manifest has no beta version/build")
		}
	} else if channel != ChannelStable {
		return errors.New("unknown update channel")
	}
	return nil
}

// CheckChannel reads the selected feed of edition (CheckFeed) and refuses a stable/beta mix-up.
func CheckChannel(ctx context.Context, client *http.Client, base, channel string, edition nodeproto.Edition) (*Published, error) {
	r, err := CheckFeed(ctx, client, base, edition)
	if err != nil {
		return nil, err
	}
	if channel == ChannelBeta {
		if !strings.HasSuffix(strings.TrimSuffix(base, "/"), BetaFeedPath) {
			return nil, errors.New("beta updates do not use the stable release feed")
		}
	} else if channel != ChannelStable {
		return nil, errors.New("unknown update channel")
	}
	if err := r.Manifest.ValidateChannel(channel); err != nil {
		return nil, err
	}
	return r, nil
}

// BetaReleaseBase resolves an immutable prerelease tag from the beta pointer.
func BetaReleaseBase(feed string, m *Manifest) string {
	if m == nil || m.Channel != ChannelBeta {
		return ""
	}
	return releaseBase(feed, m.Version)
}

// BetaFeed converts only the official GitHub stable feed to its independent
// beta pointer. A private/custom source is never silently replaced by one.
func BetaFeed(base string) (string, error) {
	repo := githubLatestRepo(strings.TrimSuffix(base, "/"))
	if repo == "" {
		return "", errors.New("beta updates need the official GitHub release source; a custom update.url has no beta feed")
	}
	return (&url.URL{Scheme: "https", Host: "github.com", Path: repo + BetaFeedPath}).String(), nil
}
