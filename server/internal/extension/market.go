package extension

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Extension markets (docs/extensions/extensions.md "Extension market") publish persona-extensions.json
// at the root of a repository or website path. A registered market lists its packages
// and pins each download's sha256.

// IndexFile is the canonical market index at a repository or website root.
const IndexFile = "persona-extensions.json"

// Limits of a market.
const (
	IndexMost           = 4 << 20
	IndexExtensionsMost = 500
	MarketsMost         = 32
	marketFetchFor      = 30 * time.Second
	MarketTokenMost     = 4096
	marketTagMost       = 16
	marketPackageMost   = 16
)

// MarketSpec is where a market's index is.
type MarketSpec struct {
	Kind string // https | github | git
	// URL: the index's https URL (Kind https), or the repository's (Kind git).
	URL string
	// Owner, Repo, Ref: a GitHub repository (Kind github); Ref "" is the default branch (also of Kind git).
	Owner, Repo, Ref string
}

var githubName = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ParseMarket reads github:owner/repo[@ref], or an https URL: of the index itself (ending in .json) or of the
// directory it is in (persona-extensions.json is added).
func ParseMarket(s string) (MarketSpec, error) {
	s = strings.TrimSpace(s)
	if spec, ok, err := parseGit(s); ok {
		return spec, err
	}
	if rest, ok := strings.CutPrefix(s, "github:"); ok {
		repo, ref, _ := strings.Cut(rest, "@")
		owner, name, ok := strings.Cut(repo, "/")
		if !ok || !githubName.MatchString(owner) || !githubName.MatchString(name) || strings.Contains(name, "/") {
			return MarketSpec{}, i18n.Err("extension-market_read.github_form")
		}
		if ref != "" && (strings.ContainsAny(ref, " \t\n?#") || strings.Contains(ref, "..") || len(ref) > 200) {
			return MarketSpec{}, i18n.Err("extension-market_read.bad_ref")
		}
		return MarketSpec{Kind: "github", Owner: owner, Repo: strings.TrimSuffix(name, ".git"), Ref: ref}, nil
	}
	if strings.HasPrefix(s, "https://github.com/") {
		parts := strings.Split(strings.Trim(strings.TrimPrefix(s, "https://github.com/"), "/"), "/")
		if len(parts) == 2 {
			return ParseMarket("github:" + parts[0] + "/" + parts[1])
		}
		if len(parts) >= 4 && parts[2] == "tree" {
			return ParseMarket("github:" + parts[0] + "/" + parts[1] + "@" + strings.Join(parts[3:], "/"))
		}
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return MarketSpec{}, i18n.Err("extension-market_read.address_form")
	}
	if u.Scheme != "https" {
		return MarketSpec{}, i18n.Err("extension-market_read.https_only")
	}
	if u.User != nil {
		return MarketSpec{}, i18n.Err("extension-market_read.no_credentials")
	}
	u.Fragment = ""
	if !strings.HasSuffix(u.Path, ".json") {
		u.Path = strings.TrimSuffix(u.Path, "/") + "/" + IndexFile
	}
	return MarketSpec{Kind: "https", URL: u.String()}, nil
}

// String is the spec as kept and shown.
func (s MarketSpec) String() string {
	if s.Kind == "git" {
		out := "git:" + s.URL
		if s.Ref != "" {
			out += "#" + s.Ref
		}
		return out
	}
	if s.Kind == "github" {
		out := "github:" + s.Owner + "/" + s.Repo
		if s.Ref != "" {
			out += "@" + s.Ref
		}
		return out
	}
	return s.URL
}

// MarketID is a short stable id of a spec (the market's key in the hub's API).
func MarketID(s MarketSpec) string {
	sum := sha256.Sum256([]byte(s.String()))
	return hex.EncodeToString(sum[:5])
}

// Market is a registered market with what reading it needs.
type Market struct {
	Spec MarketSpec
	// HubBuild selects the compatible releases when the market is read.
	HubBuild int64
	// TrustedKey is supplied by the hub to pin the market's trust at preview time; it is not a download credential.
	TrustedKey string
	// TrustedKeys are a private market's owner-configured public keys (MarketRecord.TrustedKeys); ignored for the
	// official market, whose key is compiled in.
	TrustedKeys []string
	// Token, when set, is sent to the market's own origin only (a private repository or site).
	Token string
	// Client, GitHubAPI, GitHubRaw and GitHubWeb: tests replace them. With GitHubAPI replaced and GitHubRaw not, a
	// rate-limited read has nowhere else to go (githubFallback).
	Client    *http.Client
	GitHubAPI string
	GitHubRaw string
	GitHubWeb string
	// UpstreamProxy also configures a git market's HTTPS clone. Client carries the same proxy for HTTP market reads.
	UpstreamProxy *url.URL
	// Dir is where git markets' clones are kept (the hub's data directory).
	Dir string
	// Cache is where downloaded packages are kept by their sha256 ("": none), docs/protocol/protocol.md "Extension install, upgrade and uninstall progress".
	Cache string
	// Mirrors receive no market credentials, and always verify before fallback.
	RegistryMirror, CatalogMirror string
	// allowLocal lets tests clone a local repository.
	allowLocal bool
	// viaFallback: some file of this read came from githubFallback's hosts.
	viaFallback bool
}

func (m *Market) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return http.DefaultClient
}

func (m *Market) api() string {
	if m.GitHubAPI != "" {
		return strings.TrimSuffix(m.GitHubAPI, "/")
	}
	return "https://api.github.com"
}

// SharesTokenWith limits explicit address moves and token reuse to the original
// HTTPS origin, or repositories belonging to the same registered GitHub owner.
func (m *Market) SharesTokenWith(target MarketSpec) bool {
	if m.Spec.Kind != target.Kind {
		return false
	}
	if m.Spec.Kind == "github" {
		return strings.EqualFold(m.Spec.Owner, target.Owner)
	}
	return sameOrigin(m.Spec.URL, target.URL)
}

// origin is where the token may go.
func (m *Market) origin() string {
	if m.Spec.Kind == "github" {
		return m.api()
	}
	return m.Spec.URL
}

// MarketOrigin is the origin a plugin installed from a market records: market:<market id>/<plugin id>.
func MarketOrigin(marketID, extensionID string) string {
	return "market:" + marketID + "/" + extensionID
}

// FromMarket is the market a plugin's origin says it came from ("" when not from a market).
func FromMarket(origin string) string {
	rest, ok := strings.CutPrefix(origin, "market:")
	if !ok {
		return ""
	}
	id, _, _ := strings.Cut(rest, "/")
	return id
}

// MarketRecord is a market's row (table plugin_markets).
type MarketRecord struct {
	ID          string          `json:"id"`
	Spec        string          `json:"spec"`
	Name        string          `json:"name"`
	TokenSealed string          `json:"-"`
	Index       json.RawMessage `json:"-"`
	Warnings    []string        `json:"warnings"`
	Error       string          `json:"error,omitempty"`
	FetchedAt   *time.Time      `json:"fetched_at,omitempty"`
	AddedAt     time.Time       `json:"added_at"`
	// SigningKey is a legacy stored official pin. Unofficial pins are cleared on load;
	// official verification uses the compiled-in key and quarantines unrecognized pins.
	SigningKey string `json:"-"`
	// TrustedKeys are the public keys ("ed25519:<base64>") the owner configured for a private market: when there are
	// any, its catalog must be signed by one of them, and installs and upgrades from it are verified like the official
	// market's (docs/extensions/extensions.md "Private market keys"). Always empty for the official market.
	TrustedKeys []string `json:"-"`
	// Signature is the state of the index last read.
	Signature MarketSignature `json:"signature"`
}

// MarketStore keeps the markets (the hub's Postgres).
type MarketStore interface {
	ExtensionMarkets(ctx context.Context) ([]MarketRecord, error)
	PutExtensionMarket(ctx context.Context, r MarketRecord) error
	DeleteExtensionMarket(ctx context.Context, id string) error
}
