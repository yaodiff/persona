package extension

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// get fetches u into w (at most limit bytes), with the token when u is the market's own origin.
func (m *Market) get(ctx context.Context, u, accept string, w io.Writer, limit int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if m.Token != "" && sameOrigin(u, m.origin()) {
		req.Header.Set("Authorization", "Bearer "+m.Token)
	}
	if m.Spec.Kind == "github" {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	sent := req.Header.Get("Authorization") != "" // the token goes to the market's own origin only
	client := *m.client()
	previous := client.CheckRedirect
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 || next.URL.User != nil || next.URL.Scheme != "https" && !sameOrigin(next.URL.String(), u) {
			return i18n.Err("extension-market_security.redirect")
		}
		if !sameOrigin(next.URL.String(), m.origin()) || via[len(via)-1].Header.Get("Authorization") == "" {
			next.Header.Del("Authorization")
		}
		if previous != nil {
			return previous(next, via)
		}
		return nil
	}
	res, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return i18n.Err("extension-market_security.request")
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound && m.Token == "":
		return notFound{i18n.Err("extension-market_read.not_found")}
	case res.StatusCode == http.StatusNotFound:
		return notFound{i18n.Err("extension-market_read.not_found_token")}
	case rateLimited(res):
		key := "extension-market_read.rate_limited"
		switch {
		case m.Spec.Kind == "github" && !sent:
			key = "extension-market_read.rate_limited_github_add_token" // a token raises the limit, public repositories too
		case m.Spec.Kind == "github":
			key = "extension-market_read.rate_limited_github"
		}
		return messageOfKind(ErrRateLimited, key, i18n.Args{"status": res.StatusCode, "retry": retryIn(res.Header, time.Now())})
	case (res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden) && !sent:
		return i18n.Err("extension-market_read.sign_in", i18n.Args{"status": res.StatusCode})
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return i18n.Err("extension-market_read.no_permission", i18n.Args{"status": res.StatusCode})
	case res.StatusCode != http.StatusOK:
		return i18n.Err("extension-market_read.http_status", i18n.Args{"status": res.StatusCode})
	}
	n, err := io.Copy(w, io.LimitReader(res.Body, limit+1))
	if err == nil && n > limit {
		err = i18n.Err("extension-market_read.too_large", i18n.Args{"bytes": limit})
	}
	return err
}

// rateLimited says whether a market refused a request for its rate limit, not for rights: a 429, or GitHub's 403
// with no requests left (X-RateLimit-Remaining: 0), a Retry-After, or a body that says so (its secondary limit may
// come with neither header). It reads at most the first KB of a 403's body.
func rateLimited(res *http.Response) bool {
	switch {
	case res.StatusCode == http.StatusTooManyRequests:
		return true
	case res.StatusCode != http.StatusForbidden:
		return false
	case res.Header.Get("X-RateLimit-Remaining") == "0" || res.Header.Get("Retry-After") != "":
		return true
	}
	head, _ := io.ReadAll(io.LimitReader(res.Body, 1<<10))
	return strings.Contains(strings.ToLower(string(head)), "rate limit")
}

// retryIn says when a rate-limited request may be tried again, from Retry-After (seconds) or X-RateLimit-Reset
// (Unix seconds); "later" when neither says. It is a sentence of its own, read in the reader's language.
func retryIn(h http.Header, now time.Time) error {
	var wait time.Duration
	if s, err := strconv.ParseInt(strings.TrimSpace(h.Get("Retry-After")), 10, 64); err == nil && s >= 0 {
		wait = time.Duration(s) * time.Second
	} else if s, err := strconv.ParseInt(strings.TrimSpace(h.Get("X-RateLimit-Reset")), 10, 64); err == nil && s > 0 {
		wait = time.Unix(s, 0).Sub(now)
	} else {
		return i18n.Err("extension-market_read.retry_later")
	}
	if wait <= time.Minute {
		return i18n.Err("extension-market_read.retry_minute")
	}
	return i18n.Err("extension-market_read.retry_minutes", i18n.Args{"count": int((wait + time.Minute - 1) / time.Minute)})
}

// notFound is a catalog or package that the origin could not find.
type notFound struct{ error }

func (e notFound) Unwrap() error { return e.error }

func sameOrigin(a, b string) bool {
	ua, err1 := url.Parse(a)
	ub, err2 := url.Parse(b)
	if err1 != nil || err2 != nil || ua.Scheme == "" || ua.Hostname() == "" || !strings.EqualFold(ua.Scheme, ub.Scheme) {
		return false
	}
	return strings.EqualFold(ua.Hostname(), ub.Hostname()) && canonicalPort(ua) == canonicalPort(ub)
}

func canonicalPort(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}

// ErrRateLimited: the market refused a request for its rate limit, not for rights.
var ErrRateLimited = errors.New("rate limited")

// githubFallback: when the API rate-limits a GitHub market read without a token (60 requests an hour for each
// address, which every device behind one router shares), the same file is read where the API's limit does not
// apply: raw.githubusercontent.com for files of the repository, github.com/…/releases/download for release assets.
// Only public repositories are there; a private one says 404 and the API's answer stands. A market with a token
// always uses the API (5000 an hour, and the token goes nowhere else). The index stays signed and every package
// pinned by its sha256, so where they were read from changes nothing about what is trusted.
func (m *Market) githubFallback() (raw, web string, ok bool) {
	if m.Spec.Kind != "github" || m.Token != "" {
		return "", "", false
	}
	raw, web = m.GitHubRaw, m.GitHubWeb
	if m.GitHubAPI != "" && raw == "" { // a test's API: never the real raw host
		return "", "", false
	}
	if raw == "" {
		raw = "https://raw.githubusercontent.com"
	}
	if web == "" {
		web = "https://github.com"
	}
	return strings.TrimSuffix(raw, "/"), strings.TrimSuffix(web, "/"), true
}

// getRepoFile reads a file of a GitHub market's repository through the API, and from raw.githubusercontent.com when
// the API rate-limits a market without a token (githubFallback).
func (m *Market) getRepoFile(ctx context.Context, p string, w io.Writer, limit int64) error {
	err := m.get(ctx, m.contentsURL(p), "application/vnd.github.raw", w, limit)
	raw, _, ok := m.githubFallback()
	if !ok || !errors.Is(err, ErrRateLimited) {
		return err
	}
	ref := m.Spec.Ref
	if ref == "" {
		ref = "HEAD" // the default branch
	}
	u := fmt.Sprintf("%s/%s/%s/%s/%s", raw, url.PathEscape(m.Spec.Owner), url.PathEscape(m.Spec.Repo), escapePath(ref), escapePath(p))
	ferr := m.get(ctx, u, "", w, limit)
	switch {
	case ferr == nil:
		m.viaFallback = true
		return nil
	case errors.As(ferr, new(notFound)):
		// Not there: the canonical index or its signature is missing, or the repository is private.
		// A market without a token cannot read either way; return that failure unchanged.
		return ferr
	}
	return err
}

// contentsURL is a file of a GitHub market's repository, through the API (works for private ones with a token).
func (m *Market) contentsURL(p string) string {
	u := fmt.Sprintf("%s/repos/%s/%s/contents/%s", m.api(), url.PathEscape(m.Spec.Owner), url.PathEscape(m.Spec.Repo), escapePath(p))
	if m.Spec.Ref != "" {
		u += "?ref=" + url.QueryEscape(m.Spec.Ref)
	}
	return u
}

func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// Fetched keeps the full catalog envelope for restart-time verification.
type Fetched struct {
	Index        *MarketIndex
	Warnings     []string
	Raw, Payload []byte
	// SignedBy is the trusted key ("ed25519:…") whose signature verified the catalog; "" when it was not verified.
	SignedBy string
}

func (m *Market) Fetch(ctx context.Context) (*MarketIndex, []string, error) {
	f, err := m.FetchSigned(ctx)
	if err != nil {
		return nil, nil, err
	}
	return f.Index, f.Warnings, nil
}

// FetchSigned reads one document, and enforces the compiled-in pin for all official aliases. A failure is
// ErrCatalogUnavailable (nothing read) or ErrCatalogUnverified (read, and it does not verify). A catalog mirror
// that cannot be read or does not verify is skipped for the origin, whose answer alone decides: a mirror can
// neither stand in an unverified catalog nor, when the origin is unreachable, quarantine the market.
func (m *Market) FetchSigned(ctx context.Context) (*Fetched, error) {
	limit := marketFetchFor
	if m.Spec.Kind == "git" {
		limit = gitFor
	}
	key := ""
	if IsOfficialMarket(m.Spec.String()) {
		key = Official.SigningKey
		if _, err := ParseSigningKey(key); err != nil {
			return nil, catalogFailure(ErrCatalogUnverified, i18n.Err("extension-market_security.signature"))
		}
	}
	if key != "" && m.CatalogMirror != "" {
		mirrorCtx, stop := context.WithTimeout(ctx, mirrorFetchFor)
		var b strings.Builder
		anonymous := *m
		anonymous.Token = ""
		err := anonymous.get(mirrorCtx, m.CatalogMirror, "application/json", &b, IndexMost)
		stop()
		if err == nil {
			if f, err := ParseCatalogKeysForBuild([]byte(b.String()), []string{key}, m.HubBuild); err == nil {
				return f, nil
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	raw, err := m.fetchRaw(ctx)
	if err != nil {
		return nil, catalogFailure(ErrCatalogUnavailable, err)
	}
	return ParseCatalogKeysForBuild(raw, m.VerifyKeys(), m.HubBuild)
}

// VerifyKeys are the keys a catalog of m must be signed by (any one of them): the compiled-in key for the official
// market, the owner's keys for a private market that has them, none for an unsigned market.
func (m *Market) VerifyKeys() []string {
	if IsOfficialMarket(m.Spec.String()) {
		return []string{Official.SigningKey}
	}
	return slices.Clone(m.TrustedKeys)
}

// fetchRaw never reads an adjacent signature or another catalog filename.
func (m *Market) fetchRaw(ctx context.Context) ([]byte, error) {
	var b strings.Builder
	var err error
	switch m.Spec.Kind {
	case "git":
		if err = m.syncGit(ctx); err == nil {
			err = m.gitFile(IndexFile, &b, IndexMost)
		}
	case "github":
		err = m.getRepoFile(ctx, IndexFile, &b, IndexMost)
	default:
		err = m.get(ctx, m.Spec.URL, "application/json", &b, IndexMost)
	}
	return []byte(b.String()), err
}
