package assistant

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A to-do's links to an extension provider's repositories are resolved here, before the store's transaction and
// outside todos.writing: a link the to-do already has comes from storage (no call, so it is kept while the provider
// is off or gone), a new one from the provider (repo.link, and repo.describe for a missing title). Built-in links never
// reach a provider, and only a plain link on the provider's own hosts is sent to it (linkShape). Agents' reports never
// wait on a provider: the links it must be asked about are resolved off the hub's event loops (askReportedLater).
// Reads only say how the provider is now (describeProviderRow), without asking it anything.

// describeProviderRow sets an extension row's provider state and link labels for a read (never stored).
func (s *Service) describeProviderRow(row *repository.TaskRepository) {
	provider := row.SourceSnapshot.Identity.Provider
	if !repository.IsExtensionProvider(provider) {
		return
	}
	info, ok := s.repositoryProvider(provider)
	switch {
	case !ok && !s.providersListed():
		row.ProviderState = repository.ProviderOff // not running yet, right after a restart
	case !ok:
		row.ProviderState = repository.ProviderMissing
	case info.State == repository.ProviderAvailable:
		row.ProviderState = repository.ProviderAvailable
	default:
		row.ProviderState = repository.ProviderOff
	}
	if ok && len(info.LinkKinds) > 0 {
		row.LinkKinds = slices.Clone(info.LinkKinds)
	}
}

// providersListed: the extensions' manager lists its providers (it has started). Until then it lists none, and an
// address on a host no built-in owns could be any provider's.
func (s *Service) providersListed() bool {
	select {
	case <-s.repoStarted:
		return true
	default:
		return s.repoStarted == nil
	}
}

// awaitProviders waits, while ctx lasts, for the providers to be listed.
func (s *Service) awaitProviders(ctx context.Context) {
	if !s.providersListed() {
		select {
		case <-s.repoStarted:
		case <-ctx.Done():
		}
	}
}

// providersStartingCode is the code of providersStarting (repository_providers_starting, task_repositories_providers_starting).
const providersStartingCode = "providers_starting"

// providersStarting is the error for an address no one can place yet: the providers are not listed (right after a
// restart). Its text is the asker's language's, saying to try again in a moment.
var providersStarting = &repository.ProviderError{Code: providersStartingCode}

// linkSources are what a to-do's link addresses are matched against, each read once when first needed: its saved
// repositories and the saved catalog (none when they can't be read; the store's own transaction then answers).
type linkSources struct {
	rows    func() []repository.TaskRepository
	catalog func() []repository.CatalogEntry
}

func (s *Service) linkSources(ctx context.Context, id string) linkSources {
	return linkSources{
		rows: sync.OnceValue(func() []repository.TaskRepository {
			if st, ok := s.store().(todoRepositoryStore); ok {
				if set, err := st.TodoRepositories(ctx, id); err == nil {
					return set.Repositories
				}
			}
			return nil
		}),
		catalog: sync.OnceValue(func() []repository.CatalogEntry {
			if doc, err := s.repositoryDocument(ctx); err == nil {
				return doc.taskCatalog().Repositories
			}
			return nil
		}),
	}
}

// hasLinks: some input replaces a repository's links with at least one.
func hasLinks(inputs []repository.TaskInput) bool {
	return slices.ContainsFunc(inputs, func(in repository.TaskInput) bool { return len(in.Links) > 0 })
}

// resolveSetInput fills the links of a set request (PUT /todos/{id}/repositories, todo_repositories_set). When the
// revisions it names are no longer current the store refuses it anyway, so nothing is asked.
func (s *Service) resolveSetInput(ctx context.Context, id string, in repository.SetInput) error {
	if !hasLinks(in.Repositories) || !repository.ValidRevision(in.ExpectedRevision) || !repository.ValidRevision(in.ExpectedCatalogRevision) ||
		len(in.Repositories) > repository.MaxTaskRepositories {
		return nil
	}
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return nil
	}
	cur, err := st.TodoRepositories(ctx, id)
	if err != nil || cur.Revision != *in.ExpectedRevision {
		return nil
	}
	catalog := func() *repository.Catalog {
		doc, err := s.repositoryDocument(ctx)
		if err != nil || doc.Revision != *in.ExpectedCatalogRevision {
			return nil
		}
		c := doc.taskCatalog()
		return &c
	}
	return s.resolveTaskInputs(ctx, cur.Repositories, catalog, in.Repositories)
}

// resolveCreateBindings fills the links of a new to-do's repositories (POST /todos, todo_create).
func (s *Service) resolveCreateBindings(ctx context.Context, b *repository.CreateBindings) error {
	if b == nil || !hasLinks(b.Repositories) || len(b.Repositories) > repository.MaxTaskRepositories {
		return nil
	}
	catalog := func() *repository.Catalog {
		doc, err := s.repositoryDocument(ctx)
		if err != nil || doc.Revision != *b.CatalogRevision {
			return nil
		}
		c := doc.taskCatalog()
		return &c
	}
	return s.resolveTaskInputs(ctx, nil, catalog, b.Repositories)
}

// resolveLinksInput fills a links request (PUT …/repositories/{repo_id}/links, todo_repository_links_set).
func (s *Service) resolveLinksInput(ctx context.Context, id, repoID string, in repository.LinksInput) error {
	if len(in.Links) == 0 || !repository.ValidRevision(in.ExpectedRevision) {
		return nil
	}
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return nil
	}
	cur, err := st.TodoRepositories(ctx, id)
	if err != nil || cur.Revision != *in.ExpectedRevision {
		return nil
	}
	return s.resolveTaskInputs(ctx, cur.Repositories, nil, []repository.TaskInput{{RepoID: repoID, Links: in.Links}})
}

// resolveTaskInputs fills the links of every input on an extension provider's repository: rows are the saved ones,
// catalog (read only when a new repository has links) the saved catalog for the others.
func (s *Service) resolveTaskInputs(ctx context.Context, rows []repository.TaskRepository, catalog func() *repository.Catalog, inputs []repository.TaskInput) error {
	ctx = withProviderBudget(ctx)
	var saved *repository.Catalog
	read := false
	for _, in := range inputs {
		if len(in.Links) == 0 || len(in.Links) > repository.MaxRepositoryLinks {
			continue
		}
		var source repository.SourceSnapshot
		var stored []repository.TaskLink
		if i := slices.IndexFunc(rows, func(r repository.TaskRepository) bool { return r.RepoID == in.RepoID }); i >= 0 {
			source, stored = rows[i].SourceSnapshot, rows[i].Links
		} else {
			if !read && catalog != nil {
				saved, read = catalog(), true
			}
			if saved == nil {
				continue
			}
			i := slices.IndexFunc(saved.Repositories, func(e repository.CatalogEntry) bool { return e.ID == in.RepoID })
			if i < 0 {
				continue
			}
			entry := saved.Repositories[i]
			source = repository.SourceSnapshot{Identity: entry.Identity, ProviderName: entry.ProviderName}
		}
		if !repository.IsExtensionProvider(source.Identity.Provider) {
			continue
		}
		for j := range in.Links {
			if err := s.resolveRowLink(ctx, source, stored, &in.Links[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

// resolveRowLink fills one link of an extension repository: the saved link it names (by kind and number, or by its
// address), else the provider's answer. An input that is neither is left for the store to refuse.
func (s *Service) resolveRowLink(ctx context.Context, source repository.SourceSnapshot, stored []repository.TaskLink, in *repository.LinkInput) error {
	if in.URL != "" && (in.Kind != "" || in.Number != "") || in.URL == "" && (in.Kind != "issue" && in.Kind != "pull_request" || in.Number == "") {
		return nil
	}
	if i := slices.IndexFunc(stored, func(l repository.TaskLink) bool {
		return in.URL != "" && l.CanonicalURL == in.URL || in.URL == "" && l.Kind == in.Kind && l.Number == in.Number
	}); i >= 0 {
		in.Resolved = &repository.ResolvedLink{Source: source, Link: stored[i]}
		return nil
	}
	link, err := s.providerLink(ctx, source, stored, *in, "")
	if err != nil {
		return err
	}
	in.Resolved = &repository.ResolvedLink{Source: source, Link: link}
	return nil
}

// linkShape: a link input as the built-ins take one, the only kind a provider is sent: a kind and a number (1 to 20
// digits, no leading zero), or an http(s) address with nothing but a path (no credentials, query or fragment).
func linkShape(in repository.LinkInput) error {
	if in.URL == "" {
		n := in.Number
		if in.Kind != "issue" && in.Kind != "pull_request" || n == "" || len(n) > 20 || n[0] == '0' || strings.Trim(n, "0123456789") != "" {
			return repository.Problem("invalid")
		}
		return nil
	}
	u, err := url.Parse(in.URL)
	if err != nil || len(in.URL) > 2048 || u.Scheme != "http" && u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return repository.Problem("link_repository_mismatch")
	}
	return nil
}

// providerLink asks source's provider for a link (repo.link), and for its title (repo.describe) when neither the
// caller nor the answer gave one, the repository does not have the link yet and it is of the kind wanted (only; ""
// any). A failed describe only means no title. A link that is not plain, or not on the provider's hosts, is refused
// without asking.
func (s *Service) providerLink(ctx context.Context, source repository.SourceSnapshot, stored []repository.TaskLink, in repository.LinkInput, only string) (repository.TaskLink, error) {
	if err := linkShape(in); err != nil {
		return repository.TaskLink{}, err
	}
	provider := source.Identity.Provider
	call, cancel := providerCall(ctx)
	defer cancel()
	s.awaitProviders(call)
	info, ok := s.repositoryProvider(provider)
	switch {
	case !ok && !s.providersListed():
		return repository.TaskLink{}, providerProblem(providersStarting, provider, source.ProviderName)
	case !ok:
		return repository.TaskLink{}, noProvider(provider, source.ProviderName)
	case info.State != repository.ProviderAvailable:
		return repository.TaskLink{}, providerProblem(&repository.ProviderError{Code: "provider_off", Provider: info.Name}, provider, source.ProviderName)
	case in.URL != "" && repository.CheckProviderRemote(info, in.URL) != nil:
		return repository.TaskLink{}, repository.Problem("link_repository_mismatch")
	}
	link, err := s.repoExt.RepositoryLink(call, source.Identity, repository.LinkInput{URL: in.URL, Kind: in.Kind, Number: in.Number})
	if err != nil {
		return repository.TaskLink{}, providerProblem(err, provider, info.Name)
	}
	known := slices.ContainsFunc(stored, func(l repository.TaskLink) bool { return l.Kind == link.Kind && l.Number == link.Number })
	if strings.TrimSpace(in.Title) == "" && link.Title == "" && !known && (only == "" || link.Kind == only) {
		if d, err := s.repoExt.RepositoryDescribe(call, info.ID, link.CanonicalURL); err == nil {
			link.Title = d.Title
		}
	}
	return link, nil
}

// linkRoute is who answers for a URL-only link.
type linkRoute int

const (
	routeBuiltin  linkRoute = iota // the built-in rules, offline
	routeSaved                     // a link the to-do saved on an extension repository (in.Resolved is set)
	routeProvider                  // the provider claiming the address's host, to be asked
	routeUnknown                   // the providers are not listed yet: no one can tell
)

// routeLinkURL says who answers for a URL-only link (todo_repository_links_add, reports, stages, legacy links),
// asking no one. A saved extension link with that address comes from storage. An address the built-in rules give a
// GitHub or GitLab repository the to-do or the catalog already has stays with those rules: that choice was explicit,
// on any host. Otherwise a host an installed provider claims is its own; a host only saved extension repositories use
// (the to-do's or the catalog's) is no_provider, never a built-in guess; anything else keeps the built-in rules.
func (s *Service) routeLinkURL(src linkSources, in *repository.LinkInput) (repository.ProviderInfo, linkRoute, error) {
	var info repository.ProviderInfo
	if in.Kind != "" || in.Number != "" || in.Resolved != nil {
		return info, routeBuiltin, nil
	}
	host, ok := repository.RemoteHost(in.URL)
	if !ok || slices.Contains(repository.BuiltinHosts, host) {
		return info, routeBuiltin, nil
	}
	for _, row := range src.rows() {
		if !repository.IsExtensionProvider(row.SourceSnapshot.Identity.Provider) {
			continue
		}
		if i := slices.IndexFunc(row.Links, func(l repository.TaskLink) bool { return l.CanonicalURL == in.URL }); i >= 0 {
			in.Resolved = &repository.ResolvedLink{Source: row.SourceSnapshot, Link: row.Links[i]}
			return info, routeSaved, nil
		}
	}
	route, err := routeUnknown, error(nil)
	if s.providersListed() {
		if info, route, err = s.hostRoute(src, host); route == routeBuiltin && err == nil {
			return info, route, nil
		}
	}
	if guess, _, gerr := repository.ParseTaskLink(in.URL); gerr == nil &&
		(slices.ContainsFunc(src.rows(), func(r repository.TaskRepository) bool { return r.SourceSnapshot.Identity == guess }) ||
			slices.ContainsFunc(src.catalog(), func(e repository.CatalogEntry) bool { return e.Identity == guess })) {
		return repository.ProviderInfo{}, routeBuiltin, nil
	}
	return info, route, err
}

// hostRoute is who answers for an address on host (no built-in's): the installed provider claiming it, else
// no_provider when saved extension repositories use it, else the built-in rules.
func (s *Service) hostRoute(src linkSources, host string) (repository.ProviderInfo, linkRoute, error) {
	if s.repoExt != nil {
		if info, ok := s.repoExt.RepositoryProviderForHost(host); ok {
			return info, routeProvider, nil
		}
	}
	for _, row := range src.rows() {
		if id := row.SourceSnapshot.Identity; repository.IsExtensionProvider(id.Provider) && identityHost(id) == host {
			return repository.ProviderInfo{}, routeBuiltin, noProvider(id.Provider, row.SourceSnapshot.ProviderName)
		}
	}
	for _, entry := range src.catalog() {
		if repository.IsExtensionProvider(entry.Provider) && identityHost(entry.Identity) == host {
			return repository.ProviderInfo{}, routeBuiltin, noProvider(entry.Provider, entry.ProviderName)
		}
	}
	return repository.ProviderInfo{}, routeBuiltin, nil
}

// resolveLinkURL fills a URL-only link that belongs to an extension provider (routeLinkURL), asking it when it must
// (repo.parse, then repo.link); right after a restart it first waits, within ctx's budget, for the providers to be
// listed. handled is false for a link the built-in rules take, offline. only is the kind the caller keeps ("" any).
func (s *Service) resolveLinkURL(ctx context.Context, src linkSources, in *repository.LinkInput, only string) (handled bool, err error) {
	ctx = withProviderBudget(ctx)
	info, route, err := s.routeLinkURL(src, in)
	if route == routeUnknown {
		wait, cancel := providerCall(ctx)
		s.awaitProviders(wait)
		cancel()
		info, route, err = s.routeLinkURL(src, in)
	}
	switch {
	case err != nil:
		return true, err
	case route == routeUnknown:
		host, _ := repository.RemoteHost(in.URL)
		return true, providerProblem(providersStarting, "", host)
	case route == routeProvider:
		return true, s.resolveProviderURL(ctx, info, src, in, only)
	}
	return route == routeSaved, nil
}

// resolveProviderURL asks the provider claiming in.URL's host which repository and link it is. A repository the to-do
// or the catalog saved under an earlier id of the provider's extension keeps the identity it was saved with, so the
// store finds it (sameRepository).
func (s *Service) resolveProviderURL(ctx context.Context, info repository.ProviderInfo, src linkSources, in *repository.LinkInput, only string) error {
	if err := linkShape(*in); err != nil {
		return err
	}
	if info.State != repository.ProviderAvailable {
		return providerProblem(&repository.ProviderError{Code: "provider_off", Provider: info.Name}, info.ID, "")
	}
	ctx = withProviderBudget(ctx)
	call, cancel := providerCall(ctx)
	parsed, err := s.repositoryRegistry().Parse(call, in.URL, info.ID)
	cancel()
	if errors.Is(err, repository.ErrRemote) {
		return repository.Problem("invalid")
	}
	if err != nil {
		return providerProblem(err, info.ID, info.Name)
	}
	source := repository.SourceSnapshot{Identity: parsed.Identity, CloneURL: parsed.Identity.CanonicalURL, TransportOrigin: "canonical_default",
		ProviderName: parsed.ProviderName}
	if parsed.CloneURL != "" {
		source.CloneURL, source.TransportOrigin = parsed.CloneURL, "provider"
	}
	var stored []repository.TaskLink
	if i := slices.IndexFunc(src.rows(), func(row repository.TaskRepository) bool {
		return s.sameRepository(row.SourceSnapshot.Identity, parsed.Identity)
	}); i >= 0 {
		row := src.rows()[i]
		source, stored = row.SourceSnapshot, row.Links
	} else {
		saved := make([]repository.Identity, 0, len(src.catalog()))
		for _, entry := range src.catalog() {
			saved = append(saved, entry.Identity)
		}
		source.Identity = s.storedIdentity(parsed.Identity, saved)
	}
	link, err := s.providerLink(ctx, source, stored, *in, only)
	if err != nil {
		return err
	}
	in.Resolved = &repository.ResolvedLink{Source: source, Link: link}
	return nil
}

// sameRepository: a and b are one repository, also when a was saved under an extension's earlier id.
func (s *Service) sameRepository(a, b repository.Identity) bool {
	if a == b {
		return true
	}
	return a.InstanceURL == b.InstanceURL && a.Path == b.Path && a.CanonicalURL == b.CanonicalURL && s.sameProvider(a.Provider, b.Provider)
}

// sameProvider: a and b are one provider, also when one is an earlier id of its extension (after a market rename both
// resolve to its current id). A lookup, never a call.
func (s *Service) sameProvider(a, b string) bool {
	if a == b {
		return true
	}
	if !repository.IsExtensionProvider(a) || !repository.IsExtensionProvider(b) {
		return false
	}
	pa, ok := s.repositoryProvider(a)
	pb, ok2 := s.repositoryProvider(b)
	return ok && ok2 && pa.ID == pb.ID
}

// storedIdentity is identity as it was saved: itself when saved has it, else the first of saved that is the same
// repository under an earlier id of its extension. Stored identities are compared exactly (the store's transactions
// call nothing), so a provider's answer under its extension's current id takes the stored one first.
func (s *Service) storedIdentity(identity repository.Identity, saved []repository.Identity) repository.Identity {
	if !repository.IsExtensionProvider(identity.Provider) || slices.Contains(saved, identity) {
		return identity
	}
	for _, id := range saved {
		if s.sameRepository(id, identity) {
			return id
		}
	}
	return identity
}

// identityHost is the lowercase host of a repository's instance.
func identityHost(id repository.Identity) string {
	host, _ := repository.RemoteHost(id.InstanceURL)
	return host
}

// claimedHost: raw is on a host an installed extension provider claims, on or off. No call.
func (s *Service) claimedHost(raw string) bool {
	host, ok := repository.RemoteHost(raw)
	if !ok || s.repoExt == nil || slices.Contains(repository.BuiltinHosts, host) {
		return false
	}
	_, ok = s.repoExt.RepositoryProviderForHost(host)
	return ok
}

type acceptedReportLink struct {
	raw, canonical string
}

type reportedLinkSave func(context.Context, []repository.LinkInput) ([]acceptedReportLink, error)

// importReportedLinks records the links an agent reported (only: the one kind kept, "" for any): built-in and saved
// ones now, offline as before; the ones a provider must be asked about later, off the hub's event loops
// (askReportedLater). save applies the report's ownership check to both paths; nil is for deliberate stage updates
// and explicitly classified references. A link whose provider is off or gone is skipped and logged. Reported roles
// (delivery/reference) ride along on each link; an absent role keeps the delivery default.
func (s *Service) importReportedLinks(ctx context.Context, id string, raws []string, only string, save reportedLinkSave,
	onDeferred func(context.Context, []acceptedReportLink)) ([]acceptedReportLink, error) {
	return s.importReportedLinksWithRoles(ctx, id, raws, only, save, nil, onDeferred)
}

func (s *Service) importReportedLinksWithRoles(ctx context.Context, id string, raws []string, only string, save reportedLinkSave,
	roles map[string]string, onDeferred func(context.Context, []acceptedReportLink)) ([]acceptedReportLink, error) {
	if save == nil {
		save = func(ctx context.Context, links []repository.LinkInput) ([]acceptedReportLink, error) {
			return nil, s.importReported(ctx, id, links)
		}
	} else if only != "" {
		write := save
		save = func(ctx context.Context, links []repository.LinkInput) ([]acceptedReportLink, error) {
			kept := make([]repository.LinkInput, 0, len(links))
			for _, in := range links {
				_, link, err := repository.ParseTaskLinkInput(in)
				if err != nil {
					return nil, err
				}
				if link.Kind == only {
					kept = append(kept, in)
				}
			}
			return write(ctx, kept)
		}
	}
	src := s.linkSources(ctx, id)
	var links []repository.LinkInput
	var later []string
	for _, raw := range raws {
		in, ask, err := s.reportedLink(src, raw, only)
		switch {
		case err != nil:
			s.logf("task %s: repository link skipped: %v", id, err)
		case ask:
			b := &s.reportedLinks
			b.mu.Lock()
			cached, found := b.resolved[id+" "+raw]
			until := b.until[id+" "+raw]
			b.mu.Unlock()
			if found && time.Now().Before(until) && s.providersListed() {
				if info, ok := s.repositoryProvider(cached.Resolved.Source.Identity.Provider); ok && info.State == repository.ProviderAvailable {
					if role := roles[raw]; role != "" {
						cached.Role = role
					}
					links = append(links, cached)
					continue
				}
			}
			later = append(later, raw)
		case in != nil:
			if role := roles[raw]; role != "" {
				in.Role = role
			}
			links = append(links, *in)
		}
	}
	saveLater := save
	if onDeferred != nil {
		saveLater = func(ctx context.Context, links []repository.LinkInput) ([]acceptedReportLink, error) {
			accepted, err := save(ctx, links)
			if err == nil && len(accepted) != 0 {
				onDeferred(ctx, accepted)
			}
			return accepted, err
		}
	}
	s.askReportedLater(ctx, id, later, only, saveLater, roles)
	return save(ctx, links)
}

// reportedLink is a reported address as a link to record now (nil: none), or ask when its provider must be asked
// first, or the providers are not listed yet. It asks no one.
func (s *Service) reportedLink(src linkSources, raw, only string) (*repository.LinkInput, bool, error) {
	_, link, err := repository.ParseTaskLink(raw)
	if err != nil && s.providersListed() && !s.claimedHost(raw) {
		return nil, false, nil // not a link of any provider's
	}
	in := repository.LinkInput{URL: raw}
	info, route, rerr := s.routeLinkURL(src, &in)
	switch {
	case rerr != nil:
		return nil, false, rerr
	case route == routeUnknown:
		return nil, true, nil
	case route == routeProvider && info.State != repository.ProviderAvailable:
		return nil, false, providerProblem(&repository.ProviderError{Code: "provider_off", Provider: info.Name}, info.ID, "")
	case route == routeProvider:
		return nil, linkShape(in) == nil, nil // an address with a query or a fragment is no link
	case route == routeSaved:
		return keptLink(in, only), false, nil
	}
	if err != nil || only != "" && link.Kind != only {
		return nil, false, nil
	}
	return &repository.LinkInput{URL: link.CanonicalURL}, false, nil
}

// keptLink preserves both the reported address and the provider's canonical link.
func keptLink(in repository.LinkInput, only string) *repository.LinkInput {
	if only != "" && in.Resolved.Link.Kind != only {
		return nil
	}
	return &in
}

// reportedLinkBook keeps the reported addresses a provider is asked about off the hub's event loops. An address of a
// to-do is asked about by one resolution at a time, and once its provider answered (it is saved, or the provider has
// no link of the kind kept, needs a sign-in or refused), not again for reportedLinkRetry: only a reply that names it
// after that asks again. A call that got no answer leaves it to the next reply that names it (providerAnswered).
type reportedLinkBook struct {
	mu     sync.Mutex
	until  map[string]time.Time // to-do id and address → when it may be asked about again
	asking map[string]bool      // to-do id and address → a resolution is asking about it now
	// Only provider metadata is cached; every report must still establish its own ownership.
	resolved map[string]repository.LinkInput
	pending  map[string]reportedLinkSave // the latest report awaiting an in-flight resolution
	runs     sync.WaitGroup              // resolutions running (tests wait for them)
}

// reportedLinkRetry is how long a reported address its provider answered is not asked about again.
const reportedLinkRetry = 5 * time.Minute

// reportedLinkStart is how long a resolution waits for the providers to be listed after a restart (tests shorten it).
var reportedLinkStart = time.Minute

// askReportedLater resolves reported addresses through their provider in the background, so the hub's events (and a
// restart's replay of every task session's last reply) never wait on an extension, and one reply asks about an
// address once, its PR included. Each address has a provider budget of its own, so a slow answer about one never
// leaves the next without time.
func (s *Service) askReportedLater(ctx context.Context, id string, raws []string, only string, save reportedLinkSave, roles map[string]string) {
	if len(raws) == 0 {
		return
	}
	b := &s.reportedLinks
	now := time.Now()
	b.mu.Lock()
	maps.DeleteFunc(b.until, func(_ string, until time.Time) bool { return now.After(until) })
	maps.DeleteFunc(b.resolved, func(key string, _ repository.LinkInput) bool { return b.until[key].IsZero() })
	if b.until == nil {
		b.until = map[string]time.Time{}
	}
	if b.asking == nil {
		b.asking = map[string]bool{}
	}
	ask := []string{}
	for _, raw := range raws {
		key := id + " " + raw
		if b.asking[key] {
			if b.pending == nil {
				b.pending = map[string]reportedLinkSave{}
			}
			b.pending[key] = save
		} else if b.until[key].IsZero() {
			b.asking[key] = true
			ask = append(ask, raw)
		}
	}
	b.mu.Unlock()
	if len(ask) == 0 {
		return
	}
	b.runs.Add(1)
	go func() {
		defer b.runs.Done()
		ctx := context.WithoutCancel(ctx)
		wait, cancel := context.WithTimeout(ctx, reportedLinkStart)
		s.awaitProviders(wait)
		cancel()
		src := s.linkSources(ctx, id)
		var links []repository.LinkInput
		answered := []string{}
		resolved := map[string]repository.LinkInput{}
		for _, raw := range ask {
			in := repository.LinkInput{URL: raw}
			budget := context.WithValue(ctx, providerBudgetKey{}, time.Now().Add(providerBudget))
			handled, err := s.resolveLinkURL(budget, src, &in, only)
			if providerAnswered(err) {
				answered = append(answered, raw)
			}
			switch {
			case err != nil:
				s.logf("task %s: repository link skipped: %v", id, err)
			case handled:
				if kept := keptLink(in, only); kept != nil {
					if role := roles[raw]; role != "" {
						kept.Role = role
					}
					links = append(links, *kept)
					resolved[raw] = *kept
				}
			default:
				if _, link, err := repository.ParseTaskLink(raw); err == nil && (only == "" || link.Kind == only) {
					in := repository.LinkInput{URL: link.CanonicalURL, Role: roles[raw]}
					links = append(links, in)
					resolved[raw] = in
				}
			}
		}
		_, saveErr := save(ctx, links)
		if saveErr != nil {
			s.logf("task %s: repository links not saved: %v", id, saveErr)
			answered = nil // not saved: the next reply that names them tries again
		}
		b.mu.Lock()
		pending := map[string]reportedLinkSave{}
		for _, raw := range ask {
			delete(b.asking, id+" "+raw)
			if next := b.pending[id+" "+raw]; next != nil {
				pending[raw] = next
				delete(b.pending, id+" "+raw)
			}
		}
		for _, raw := range answered {
			b.until[id+" "+raw] = time.Now().Add(reportedLinkRetry)
		}
		if saveErr == nil {
			if len(resolved) > 0 && b.resolved == nil {
				b.resolved = map[string]repository.LinkInput{}
			}
			for raw, link := range resolved {
				if link.Resolved != nil { // built-in links are handled directly once providers are listed
					b.resolved[id+" "+raw] = link
				}
			}
		}
		b.mu.Unlock()
		for raw, next := range pending {
			if link, ok := resolved[raw]; ok {
				if _, err := next(ctx, []repository.LinkInput{link}); err != nil {
					s.logf("task %s: repository links not saved: %v", id, err)
				}
			} else {
				s.askReportedLater(ctx, id, []string{raw}, only, next, roles)
			}
		}
	}()
}

// providerAnswered: err (nil included) is an answer about a reported address: it was placed, or its provider said it
// has no such link, needs a sign-in or refused. A call that got no answer (out of time, canceled, or failed, which is
// also how a call that timed out reads), a provider that is off or missing, and the providers still starting are not.
func providerAnswered(err error) bool {
	var pe *repository.ProviderError
	if !errors.As(err, &pe) {
		return true
	}
	return pe.Code == "not_found" || pe.Code == "not_signed_in" || pe.Code == "refused"
}

// importReported records reported links: by address alone when no provider resolved any and no link carries a role,
// as before. A role needs the details path so it survives into the stored TaskLink.
func (s *Service) importReported(ctx context.Context, id string, links []repository.LinkInput) error {
	hasDetails := slices.ContainsFunc(links, func(in repository.LinkInput) bool { return in.Resolved != nil || in.Role != "" })
	if !hasDetails {
		urls := make([]string, 0, len(links))
		for _, in := range links {
			urls = append(urls, in.URL)
		}
		if len(urls) == 0 {
			return nil
		}
		return s.importTodoRepositoryLinks(ctx, id, urls, "report")
	}
	return s.importTodoRepositoryLinkDetails(ctx, id, links, "report")
}

// importTodoRepositoryLinkDetails is importTodoRepositoryLinks for links with titles or resolved by their provider.
func (s *Service) importTodoRepositoryLinkDetails(ctx context.Context, id string, links []repository.LinkInput, source string) error {
	st, ok := s.store().(todoRepositoryLinkDetailsStore)
	if !ok {
		return repository.Problem("store_unavailable")
	}
	_, changed, err := st.ImportTodoRepositoryLinkDetails(ctx, id, links, source)
	if err == nil && changed {
		s.todosChanged(ctx)
	}
	return err
}

// taskRepositoryToolError is a provider's refusal as the to-do tools say it (task_repositories_<code>: its text);
// any other error is returned as it is.
func (s *Service) taskRepositoryToolError(ctx context.Context, err error) error {
	var pe *repository.ProviderError
	if !errors.As(err, &pe) {
		return err
	}
	code := providerCode(pe)
	return fmt.Errorf("task_repositories_%s: %s", code, i18n.T(s.lang(ctx), "task-repositories."+code, providerArgs(pe)))
}
