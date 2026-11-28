package repositorywire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Task associations describe work. They never authorize or prepare disk resources.
const CatalogKey = "assistant.repositories"
const MaxTaskRepositories = 20
const MaxRepositoryLinks = 50
const MaxRevision int64 = 9007199254740991

type Problem string

func (e Problem) Error() string { return "task_repositories_" + string(e) }

type RequestedRef struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

type Layout string

const (
	LayoutRoot      Layout = "root"
	LayoutContainer Layout = "container"
)

// ValidLayout allows only a single repository to occupy the task root.
// A container can also be an empty task folder.
func ValidLayout(layout Layout, count int) bool {
	return layout == LayoutRoot && count == 1 || layout == LayoutContainer && count >= 0
}

// TaskWorkflow declares a repository's optional default task-collaboration cadence.
// Unset ("") and TaskWorkflowIsolated preserve existing default behavior; TaskWorkflowSharedBranch
// cooperates with serial workspace queueing (#13142) and multi-repository container workspaces (#13143).
const (
	TaskWorkflowIsolated     = "isolated"
	TaskWorkflowSharedBranch = "shared_branch"
)

// NormalizeTaskWorkflow validates an optional per-repository task_workflow preference.
// Empty means unset (default); "isolated" and "shared_branch" are accepted.
func NormalizeTaskWorkflow(workflow string) (string, Problem) {
	switch strings.TrimSpace(workflow) {
	case "":
		return "", ""
	case TaskWorkflowIsolated:
		return TaskWorkflowIsolated, ""
	case TaskWorkflowSharedBranch:
		return TaskWorkflowSharedBranch, ""
	default:
		return "", Problem("invalid")
	}
}

// CatalogEntry shares the existing catalog wire shape, including its optional transport.
type CatalogEntry struct {
	Identity
	ID       string `json:"id"`
	CloneURL string `json:"clone_url,omitempty"`
	// ProviderName is an extension provider's name when the entry was saved (absent for the built-ins).
	ProviderName string `json:"provider_name,omitempty"`
	// TaskWorkflow is an optional per-repository default task-collaboration preference ("" / "isolated" / "shared_branch").
	TaskWorkflow string `json:"task_workflow,omitempty"`
}
type Catalog struct {
	Revision     int64          `json:"revision"`
	Repositories []CatalogEntry `json:"repositories"`
}
type SourceSnapshot struct {
	Identity        Identity `json:"identity"`
	CloneURL        string   `json:"clone_url"`
	TransportOrigin string   `json:"transport_origin"`
	CatalogRevision int64    `json:"catalog_revision"`
	// ProviderName keeps an extension provider's name, so the row still reads when it is gone (absent for the built-ins).
	ProviderName string `json:"provider_name,omitempty"`
}

// Link roles distinguish a task's own deliverable from work it merely references. The delivery gate verifies only
// delivery links; reference links stay visible as context.
const (
	LinkRoleDelivery  = "delivery"
	LinkRoleReference = "reference"
)

// NormalizeLinkRole returns the stored role, applying the delivery default to rows saved before roles existed.
func NormalizeLinkRole(role string) (string, Problem) {
	switch role {
	case "", LinkRoleDelivery:
		return LinkRoleDelivery, ""
	case LinkRoleReference:
		return LinkRoleReference, ""
	default:
		return "", Problem("invalid")
	}
}

// LinkProvenance records whether a saved link carries explicit delivery intent.
type LinkProvenance string

const (
	LinkProvenanceUnknown  LinkProvenance = "" // Rows saved before provenance was recorded.
	LinkProvenanceUnbound  LinkProvenance = "unbound"
	LinkProvenanceExplicit LinkProvenance = "explicit"
)

type LinkInput struct {
	URL    string `json:"url,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Number string `json:"number,omitempty"`
	Title  string `json:"title,omitempty"`
	// Role says whether the link is the task's own delivery (default) or a reference to related work.
	Role string `json:"role,omitempty"`
	// Resolved is an extension identity's link as the service resolved it before the transaction (never JSON).
	Resolved *ResolvedLink `json:"-"`
}

// ResolvedLink is a link of an extension provider's repository, taken from the stored set or asked of the provider
// (repo.link) before any transaction; Source is the repository it belongs to.
type ResolvedLink struct {
	Source SourceSnapshot
	Link   TaskLink
}
type TaskLink struct {
	Kind         string `json:"kind"`
	Number       string `json:"number"`
	CanonicalURL string `json:"canonical_url"`
	Title        string `json:"title,omitempty"`
	// Role is delivery (default, including rows saved before roles existed) or reference.
	Role string `json:"role,omitempty"`
	// Provenance is persisted by the manager's store and stays out of the wire contract.
	Provenance LinkProvenance `json:"-"`
}
type TaskInput struct {
	RepoID         string       `json:"repo_id"`
	DirectoryAlias string       `json:"directory_alias,omitempty"`
	RequestedRef   RequestedRef `json:"requested_ref"`
	// Omitted links preserve an existing repository's links. An explicit [] clears them.
	Links []LinkInput `json:"links,omitempty"`
}
type TaskRepository struct {
	RepoID         string         `json:"repo_id"`
	DirectoryAlias string         `json:"directory_alias,omitempty"`
	RequestedRef   RequestedRef   `json:"requested_ref"`
	SourceSnapshot SourceSnapshot `json:"source_snapshot"`
	Links          []TaskLink     `json:"links"`
	CatalogState   string         `json:"catalog_state,omitempty"`
	// ProviderState and LinkKinds describe an extension provider's row when answering reads (available, off or
	// missing; its own link labels). Computed like CatalogState, never stored.
	ProviderState string      `json:"provider_state,omitempty"`
	LinkKinds     []LinkLabel `json:"link_kinds,omitempty"`
}
type TaskSet struct {
	SchemaVersion int              `json:"schema_version"`
	Manager       string           `json:"manager"`
	TodoID        string           `json:"todo_id"`
	Revision      int64            `json:"revision"`
	Repositories  []TaskRepository `json:"repositories"`
}
type CreateBindings struct {
	CatalogRevision *int64      `json:"catalog_revision"`
	Repositories    []TaskInput `json:"repositories"`
}
type SetInput struct {
	ExpectedRevision        *int64      `json:"expected_revision"`
	ExpectedCatalogRevision *int64      `json:"expected_catalog_revision"`
	Repositories            []TaskInput `json:"repositories"`
}
type LinksInput struct {
	ExpectedRevision *int64      `json:"expected_revision"`
	Links            []LinkInput `json:"links"`
}

// ValidateAlias is shared with naming and preparation. Empty means no directory override.
func ValidateAlias(alias string) error {
	if alias == "" {
		return nil
	}
	if len(alias) > 64 || strings.HasSuffix(alias, ".") {
		return Problem("invalid")
	}
	for i, c := range alias {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || i > 0 && strings.ContainsRune("._-", c)) {
			return Problem("invalid")
		}
	}
	if IsWindowsReservedName(alias) {
		return Problem("invalid")
	}
	return nil
}

// IsWindowsReservedName checks one directory component, independently of the
// alias character/length rules. Templates use it before and after expansion.
// Extensions and spaces before the extension do not escape a device name;
// Windows also reserves COM/LPT with the superscript digits 1, 2 and 3.
func IsWindowsReservedName(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	if slices.Contains([]string{"CON", "PRN", "AUX", "NUL"}, base) {
		return true
	}
	if strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT") {
		number := base[3:]
		return len(number) == 1 && number[0] >= '1' && number[0] <= '9' || number == "\u00b9" || number == "\u00b2" || number == "\u00b3"
	}
	return false
}

// RepositoryPath maps only explicitly selected layouts; legacy cwd requests never use it.
func RepositoryPath(layout Layout, alias string, count int) (string, error) {
	if count == 0 || !ValidLayout(layout, count) {
		return "", Problem("invalid")
	}
	if layout == LayoutRoot {
		return ".", nil
	}
	if alias == "" {
		return "", Problem("invalid")
	}
	if err := ValidateAlias(alias); err != nil {
		return "", err
	}
	return alias, nil
}

func NormalizeRef(ref RequestedRef) (RequestedRef, error) {
	if ref.Kind == "" && ref.Value == "" {
		ref.Kind = "default"
	}
	if ref.Kind == "default" {
		if ref.Value != "" {
			return ref, Problem("invalid")
		}
		return ref, nil
	}
	if ref.Kind == "commit" {
		if len(ref.Value) != 40 && len(ref.Value) != 64 {
			return ref, Problem("invalid")
		}
		if _, err := hex.DecodeString(ref.Value); err != nil {
			return ref, Problem("invalid")
		}
		ref.Value = strings.ToLower(ref.Value)
		return ref, nil
	}
	if ref.Kind != "branch" && ref.Kind != "tag" && ref.Kind != "ref" {
		return ref, Problem("invalid")
	}
	value := ref.Value
	if value == "" || len(value) > 1024 || value == "@" || strings.HasPrefix(value, "-") || strings.ContainsAny(value, " ~^:?*[\\") || strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.HasSuffix(value, ".") || strings.ContainsFunc(value, unicode.IsControl) {
		return ref, Problem("invalid")
	}
	if ref.Kind == "ref" && !strings.HasPrefix(value, "refs/") {
		return ref, Problem("invalid")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return ref, Problem("invalid")
		}
	}
	return ref, nil
}

// CloneTransport accepts credential-free repository roots, not provider subpages. An extension provider's address was
// checked against its hosts when it was saved; here only its shape is, so it works with the provider gone.
func CloneTransport(raw string, identity Identity) (string, error) {
	if IsExtensionProvider(identity.Provider) {
		if _, ok := CloneHost(raw); !ok {
			return "", Problem("invalid")
		}
		return raw, nil
	}
	parsed, err := Parse(raw, identity.Provider)
	if err != nil || parsed != identity {
		return "", Problem("invalid")
	}
	normalized := raw
	if strings.HasPrefix(raw, "git@") && !strings.Contains(raw, "://") {
		host, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok {
			return "", Problem("invalid")
		}
		normalized = "ssh://git@" + host + "/" + path
	}
	u, err := url.Parse(normalized)
	if err != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return "", Problem("invalid")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/")
	if strings.HasSuffix(strings.ToLower(path), ".git") {
		path = path[:len(path)-4]
	}
	if strings.ToLower(path) != identity.Path {
		return "", Problem("invalid")
	}
	return raw, nil
}

func NormalizeLink(identity Identity, in LinkInput) (TaskLink, error) {
	in.Title = strings.TrimSpace(in.Title)
	if !utf8.ValidString(in.Title) || len(in.Title) > 1024 || strings.ContainsFunc(in.Title, unicode.IsControl) {
		return TaskLink{}, Problem("invalid")
	}
	role, roleErr := NormalizeLinkRole(in.Role)
	if roleErr != "" {
		return TaskLink{}, roleErr
	}
	if IsExtensionProvider(identity.Provider) {
		return extensionLink(identity, in, role)
	}
	if in.URL != "" {
		if in.Kind != "" || in.Number != "" || len(in.URL) > 2048 {
			return TaskLink{}, Problem("invalid")
		}
		u, err := url.Parse(in.URL)
		base, baseErr := url.Parse(identity.CanonicalURL)
		if err != nil || baseErr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
			return TaskLink{}, Problem("link_repository_mismatch")
		}
		// Validate the actual URL before comparing its normalized source identity.
		// Public HTTP aliases share HTTPS identity; self-hosted schemes/ports do not.
		actual, err := Parse(in.URL, identity.Provider)
		if err != nil || actual != identity {
			return TaskLink{}, Problem("link_repository_mismatch")
		}
		prefix := base.Path + "/"
		paths := map[string]string{"issues/": "issue", "pull/": "pull_request"}
		if identity.Provider == "gitlab" {
			paths = map[string]string{"-/issues/": "issue", "-/merge_requests/": "pull_request"}
		}
		path := strings.ToLower(u.Path)
		for suffix, kind := range paths {
			if strings.HasPrefix(path, prefix+suffix) {
				in.Kind = kind
				in.Number = strings.TrimPrefix(path, prefix+suffix)
				break
			}
		}
		if in.Kind == "" {
			return TaskLink{}, Problem("link_repository_mismatch")
		}
	}
	if in.Kind != "issue" && in.Kind != "pull_request" || len(in.Number) == 0 || len(in.Number) > 20 || in.Number[0] == '0' {
		return TaskLink{}, Problem("invalid")
	}
	for _, c := range in.Number {
		if c < '0' || c > '9' {
			return TaskLink{}, Problem("invalid")
		}
	}
	segment := "issues"
	if in.Kind == "pull_request" {
		segment = "pull"
	}
	if identity.Provider == "gitlab" {
		segment = "-/issues"
		if in.Kind == "pull_request" {
			segment = "-/merge_requests"
		}
	}
	// Storage keeps the delivery default as the empty value so rows saved before roles existed read identically;
	// only an explicit reference is written.
	storedRole := ""
	if role == LinkRoleReference {
		storedRole = LinkRoleReference
	}
	return TaskLink{Kind: in.Kind, Number: in.Number, CanonicalURL: identity.CanonicalURL + "/" + segment + "/" + in.Number, Title: in.Title, Role: storedRole}, nil
}

// extensionLink is an extension identity's link as the service resolved it before the transaction: a stored link or the
// provider's answer, re-checked. Nothing is synthesized from a kind and number: without an answer the link is refused.
func extensionLink(identity Identity, in LinkInput, role string) (TaskLink, error) {
	if in.URL != "" && (in.Kind != "" || in.Number != "" || len(in.URL) > 2048) || in.URL == "" && in.Kind == "" {
		return TaskLink{}, Problem("invalid")
	}
	if in.Resolved == nil || in.Resolved.Source.Identity != identity {
		return TaskLink{}, Problem("invalid")
	}
	link := in.Resolved.Link
	if link.Kind != "issue" && link.Kind != "pull_request" || !LinkNumber(link.Number) || !UnderRepository(identity, link.CanonicalURL) ||
		in.Kind != "" && (in.Kind != link.Kind || in.Number != link.Number) {
		return TaskLink{}, Problem("link_repository_mismatch")
	}
	if in.Title != "" {
		link.Title = in.Title
	} else if len(link.Title) > 1024 || !utf8.ValidString(link.Title) || strings.ContainsFunc(link.Title, unicode.IsControl) {
		link.Title = ""
	}
	link.Role = role
	link.Provenance = LinkProvenanceUnknown
	// Storage keeps the delivery default as the empty value.
	if link.Role == LinkRoleDelivery {
		link.Role = ""
	}
	return link, nil
}

// ReconcileLinkEdits preserves omitted titles and roles for retained links. A newly added delivery link or one that
// switched to delivery is an explicit binding. Saving the same link with the same role preserves its provenance.
func ReconcileLinkEdits(current, next []TaskLink, input []LinkInput) {
	for i := range next {
		link := &next[i]
		index := slices.IndexFunc(current, func(old TaskLink) bool { return old.Kind == link.Kind && old.Number == link.Number })
		if index >= 0 {
			old := current[index]
			if link.Title == "" {
				link.Title = old.Title
			}
			// Only the input distinguishes an omitted role from an explicit delivery role.
			if input[i].Role == "" {
				link.Role = old.Role
			}
			role, _ := NormalizeLinkRole(link.Role)
			oldRole, _ := NormalizeLinkRole(old.Role)
			if link.CanonicalURL == old.CanonicalURL && role == oldRole {
				link.Provenance = old.Provenance
				continue
			}
		}
		link.Provenance = LinkProvenanceUnbound
		if role, _ := NormalizeLinkRole(link.Role); role == LinkRoleDelivery {
			link.Provenance = LinkProvenanceExplicit
		}
	}
}

func NormalizeLinks(identity Identity, input []LinkInput) ([]TaskLink, error) {
	if len(input) > MaxRepositoryLinks {
		return nil, Problem("limit")
	}
	out := make([]TaskLink, 0, len(input))
	seen := map[string]bool{}
	for _, in := range input {
		link, err := NormalizeLink(identity, in)
		if err != nil {
			return nil, err
		}
		key := link.Kind + "/" + link.Number
		if seen[key] {
			return nil, Problem("duplicate_link")
		}
		seen[key] = true
		out = append(out, link)
	}
	return out, nil
}

// Bind preserves saved identity even if a catalog ID now points at a different source.
// Removing and explicitly reselecting a repository is the only way to adopt that source.
func Bind(current []TaskRepository, input []TaskInput, catalog Catalog) ([]TaskRepository, error) {
	if len(input) > MaxTaskRepositories {
		return nil, Problem("limit")
	}
	out := make([]TaskRepository, 0, len(input))
	ids := map[string]bool{}
	aliases := map[string]bool{}
	identities := map[Identity]bool{}
	for _, in := range input {
		if in.RepoID == "" || len(in.RepoID) > 80 {
			return nil, Problem("invalid")
		}
		if ids[in.RepoID] {
			return nil, Problem("duplicate_repo")
		}
		ids[in.RepoID] = true
		if err := ValidateAlias(in.DirectoryAlias); err != nil {
			return nil, err
		}
		if in.DirectoryAlias != "" {
			key := strings.ToLower(in.DirectoryAlias)
			if aliases[key] {
				return nil, Problem("duplicate_alias")
			}
			aliases[key] = true
		}
		ref, err := NormalizeRef(in.RequestedRef)
		if err != nil {
			return nil, err
		}
		row := TaskRepository{RepoID: in.RepoID, DirectoryAlias: in.DirectoryAlias, RequestedRef: ref, Links: []TaskLink{}}
		old := slices.IndexFunc(current, func(r TaskRepository) bool { return r.RepoID == in.RepoID })
		if old >= 0 {
			row.SourceSnapshot = current[old].SourceSnapshot
			row.Links = slices.Clone(current[old].Links)
		} else {
			index := slices.IndexFunc(catalog.Repositories, func(r CatalogEntry) bool { return r.ID == in.RepoID })
			if index < 0 {
				return nil, Problem("not_found")
			}
			entry := catalog.Repositories[index]
			transport := entry.CloneURL
			origin := "catalog"
			if transport == "" {
				transport = entry.CanonicalURL
				origin = "canonical_default"
			}
			if _, err := CloneTransport(transport, entry.Identity); err != nil {
				return nil, err
			}
			row.SourceSnapshot = SourceSnapshot{entry.Identity, transport, origin, catalog.Revision, entry.ProviderName}
		}
		if identities[row.SourceSnapshot.Identity] {
			return nil, Problem("duplicate_repo")
		}
		identities[row.SourceSnapshot.Identity] = true
		if in.Links != nil {
			previous := row.Links
			row.Links, err = NormalizeLinks(row.SourceSnapshot.Identity, in.Links)
			if err != nil {
				return nil, err
			}
			ReconcileLinkEdits(previous, row.Links, in.Links)
		}
		if row.Links == nil {
			row.Links = []TaskLink{}
		}
		out = append(out, row)
	}
	return out, nil
}

func CatalogState(row TaskRepository, catalog Catalog) string {
	for _, entry := range catalog.Repositories {
		if entry.ID == row.RepoID {
			if entry.Identity == row.SourceSnapshot.Identity {
				return "current"
			}
			return "identity_changed"
		}
	}
	if strings.HasPrefix(row.RepoID, "linked_") {
		return "unlisted"
	}
	return "removed"
}

type PreparationRepository struct {
	RepoID         string         `json:"repo_id"`
	DirectoryAlias string         `json:"directory_alias,omitempty"`
	RequestedRef   RequestedRef   `json:"requested_ref"`
	SourceSnapshot SourceSnapshot `json:"source_snapshot"`
	Local          *LocalCheckout `json:"local,omitempty"`
}

// LocalCheckout is a computer-resolved Git source for a single root worktree.
// It carries the selected commit even when the source has a moving HEAD.
type LocalCheckout struct {
	CommonDir      string `json:"common_dir"`
	Commit         string `json:"commit"`
	ExistingBranch bool   `json:"existing_branch,omitempty"`
}
type LocalSourceRequest struct {
	Cwd        string `json:"cwd"`
	TaskBranch string `json:"task_branch,omitempty"`
}

type LocalSource struct {
	Repository   *PreparationRepository `json:"repository,omitempty"`
	ExistingPath string                 `json:"existing_path,omitempty"`
	Error        LocalSourceError       `json:"error,omitempty"`
}

type LocalSourceError string

const (
	LocalSourceInvalidPath LocalSourceError = "invalid_path"
	LocalSourceInvalid     LocalSourceError = "source_invalid"
	LocalSourceUnavailable LocalSourceError = "source_unavailable"
	LocalSourceInvalidRef  LocalSourceError = "ref_invalid"
	LocalSourceNoCommits   LocalSourceError = "no_commits"
)

type RepositoryID string

type BranchSourceKind string

const (
	BranchRepositoryDefault BranchSourceKind = "repository_default"
	BranchExplicit          BranchSourceKind = "explicit_branch"
	BranchOtherRef          BranchSourceKind = "non_branch_ref"
)

// BranchSource describes the input available to a branch template. A default ref has no known branch yet.
type BranchSource struct {
	RepoID RepositoryID     `json:"repo_id"`
	Kind   BranchSourceKind `json:"kind"`
	Branch string           `json:"branch,omitempty"`
}

type PreparationSnapshot struct {
	SchemaVersion int                     `json:"schema_version"`
	Manager       string                  `json:"manager"`
	TodoID        string                  `json:"todo_id"`
	Revision      int64                   `json:"revision"`
	Repositories  []PreparationRepository `json:"repositories"`
	Digest        string                  `json:"digest,omitempty"`
}

// Snapshot copies the explicitly selected repositories. Consumers persist this value
// with their operation; later edits of business associations never invalidate it.
func Snapshot(manager, todo string, revision int64, rows []TaskRepository, selected []string) (PreparationSnapshot, error) {
	out := PreparationSnapshot{SchemaVersion: 1, Manager: manager, TodoID: todo, Revision: revision, Repositories: []PreparationRepository{}}
	seen := map[string]bool{}
	for _, id := range selected {
		if seen[id] {
			return out, Problem("duplicate_repo")
		}
		seen[id] = true
		index := slices.IndexFunc(rows, func(r TaskRepository) bool { return r.RepoID == id })
		if index < 0 {
			return out, Problem("not_found")
		}
		r := rows[index]
		out.Repositories = append(out.Repositories, PreparationRepository{RepoID: r.RepoID, DirectoryAlias: r.DirectoryAlias, RequestedRef: r.RequestedRef, SourceSnapshot: r.SourceSnapshot})
	}
	slices.SortFunc(out.Repositories, func(a, b PreparationRepository) int { return strings.Compare(a.RepoID, b.RepoID) })
	return digestPreparationSnapshot(out)
}

// LocalSnapshot binds a computer-resolved source without changing the to-do's
// catalog associations. Local and catalog sources are mutually exclusive.
func LocalSnapshot(manager, todo string, repo PreparationRepository) (PreparationSnapshot, error) {
	var out PreparationSnapshot
	if repo.Local == nil || repo.SourceSnapshot != (SourceSnapshot{}) || repo.Local.CommonDir == "" ||
		repo.RepoID != "local" || repo.DirectoryAlias == "" || ValidateAlias(repo.DirectoryAlias) != nil {
		return out, Problem("invalid")
	}
	ref, err := NormalizeRef(repo.RequestedRef)
	commit, commitErr := NormalizeRef(RequestedRef{Kind: "commit", Value: repo.Local.Commit})
	if err != nil || ref != repo.RequestedRef || commitErr != nil || commit.Value != repo.Local.Commit ||
		(ref.Kind != "branch" && (ref.Kind != "commit" || ref.Value != commit.Value || repo.Local.ExistingBranch)) {
		return out, Problem("invalid")
	}
	return digestPreparationSnapshot(PreparationSnapshot{SchemaVersion: 1, Manager: manager, TodoID: todo, Repositories: []PreparationRepository{repo}})
}

func digestPreparationSnapshot(out PreparationSnapshot) (PreparationSnapshot, error) {
	payload := struct {
		SchemaVersion int                     `json:"schema_version"`
		Manager       string                  `json:"manager"`
		TodoID        string                  `json:"todo_id"`
		Repositories  []PreparationRepository `json:"repositories"`
	}{out.SchemaVersion, out.Manager, out.TodoID, out.Repositories}
	raw, err := json.Marshal(payload)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(raw)
	out.Digest = "sha256:" + hex.EncodeToString(sum[:])
	return out, nil
}

func ValidRevision(revision *int64) bool {
	return revision != nil && *revision >= 0 && *revision < MaxRevision
}
func LinkKey(identity Identity, link TaskLink) string {
	return identity.Provider + "|" + identity.InstanceURL + "|" + identity.Path + "|" + link.Kind + "|" + link.Number
}

// DefaultAlias is a suggestion only; callers must report collisions rather than silently rename.
func DefaultAlias(identity Identity) string {
	parts := strings.Split(identity.Path, "/")
	alias := parts[len(parts)-1]
	if ValidateAlias(alias) != nil {
		return "repo"
	}
	return alias
}

// ParseTaskLink derives a credential-free source from an exact issue/PR URL, offline.
func ParseTaskLink(raw string) (Identity, TaskLink, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Identity{}, TaskLink{}, Problem("invalid")
	}
	provider := "github"
	if end := strings.LastIndexByte(u.Path, '/'); end >= 0 {
		route := u.Path[:end]
		switch {
		case strings.HasSuffix(route, "/-/merge_requests"), strings.HasSuffix(route, "/-/issues"):
			provider = "gitlab"
		case strings.EqualFold(u.Hostname(), "gitlab.com") && strings.HasSuffix(route, "/merge_requests"):
			// The legacy route is an alias on GitLab. A different host may use it as its actual MR address.
			if u.RawPath != "" {
				return Identity{}, TaskLink{}, Problem("invalid")
			}
			provider = "gitlab"
			u.Path = strings.TrimSuffix(route, "/merge_requests") + "/-/merge_requests" + u.Path[end:]
			raw = u.String()
		}
	}
	identity, err := Parse(raw, provider)
	if err != nil {
		return Identity{}, TaskLink{}, Problem("invalid")
	}
	link, err := NormalizeLink(identity, LinkInput{URL: raw})
	return identity, link, err
}

// MergeTaskLinks appends references without overwriting aliases, refs or source snapshots.
// A reported repository need not be preconfigured in the manager catalog.
func MergeTaskLinks(current []TaskRepository, urls []string, catalog Catalog) ([]TaskRepository, error) {
	input := make([]LinkInput, 0, len(urls))
	for _, raw := range urls {
		input = append(input, LinkInput{URL: raw})
	}
	return MergeTaskLinkDetails(current, input, catalog, LinkProvenanceUnbound)
}

// ParseTaskLinkInput accepts an exact URL and optional title, without a remote lookup. A link the service resolved
// through its extension provider keeps that provider's repository and link.
func ParseTaskLinkInput(in LinkInput) (Identity, TaskLink, error) {
	if in.Kind != "" || in.Number != "" {
		return Identity{}, TaskLink{}, Problem("invalid")
	}
	if in.Resolved != nil {
		identity := in.Resolved.Source.Identity
		if !IsExtensionProvider(identity.Provider) {
			return Identity{}, TaskLink{}, Problem("invalid")
		}
		link, err := NormalizeLink(identity, in)
		return identity, link, err
	}
	identity, link, err := ParseTaskLink(in.URL)
	if err != nil {
		return identity, link, err
	}
	link, err = NormalizeLink(identity, LinkInput{Kind: link.Kind, Number: link.Number, Title: in.Title, Role: in.Role})
	return identity, link, err
}

// MergeTaskLinkDetails adds links or refreshes existing metadata. The caller supplies the default provenance for
// imported delivery links. An explicit delivery role binds the link, and automatic imports preserve existing bindings.
func MergeTaskLinkDetails(current []TaskRepository, input []LinkInput, catalog Catalog, provenance LinkProvenance) ([]TaskRepository, error) {
	out := slices.Clone(current)
	for i := range out {
		out[i].Links = slices.Clone(out[i].Links)
	}
	for _, in := range input {
		identity, link, err := ParseTaskLinkInput(in)
		if err != nil {
			return nil, err
		}
		link.Provenance = provenance
		switch in.Role {
		case LinkRoleDelivery:
			link.Provenance = LinkProvenanceExplicit
		case LinkRoleReference:
			link.Provenance = LinkProvenanceUnbound
		}
		index := slices.IndexFunc(out, func(r TaskRepository) bool { return r.SourceSnapshot.Identity == identity })
		if index < 0 {
			if len(out) >= MaxTaskRepositories {
				return nil, Problem("limit")
			}
			digest := sha256.Sum256([]byte(identity.Provider + "|" + identity.CanonicalURL))
			id := "linked_" + hex.EncodeToString(digest[:12])
			source := SourceSnapshot{Identity: identity, CloneURL: identity.CanonicalURL, TransportOrigin: "canonical_default"}
			if in.Resolved != nil {
				// An extension provider's repository is cloned from where its provider said (repo.parse's clone_url).
				source.ProviderName = in.Resolved.Source.ProviderName
				if clone := in.Resolved.Source.CloneURL; clone != "" && clone != identity.CanonicalURL {
					if _, err := CloneTransport(clone, identity); err == nil {
						source.CloneURL, source.TransportOrigin = clone, "provider"
					}
				}
			}
			for _, entry := range catalog.Repositories {
				if entry.Identity == identity && !slices.ContainsFunc(out, func(r TaskRepository) bool { return r.RepoID == entry.ID }) {
					// The saved repository's own transport, as binding it would give (Bind).
					id = entry.ID
					source.CatalogRevision = catalog.Revision
					source.CloneURL, source.TransportOrigin = identity.CanonicalURL, "canonical_default"
					if entry.CloneURL != "" {
						source.CloneURL = entry.CloneURL
						source.TransportOrigin = "catalog"
					}
					if entry.ProviderName != "" {
						source.ProviderName = entry.ProviderName
					}
					break
				}
			}
			if _, err := CloneTransport(source.CloneURL, identity); err != nil {
				return nil, err
			}
			out = append(out, TaskRepository{RepoID: id, RequestedRef: RequestedRef{Kind: "default"}, SourceSnapshot: source, Links: []TaskLink{}})
			index = len(out) - 1
		}
		existing := slices.IndexFunc(out[index].Links, func(l TaskLink) bool { return l.Kind == link.Kind && l.Number == link.Number })
		if existing >= 0 {
			old := &out[index].Links[existing]
			if link.Title != "" {
				old.Title = link.Title
			}
			if old.Provenance == LinkProvenanceExplicit && provenance != LinkProvenanceExplicit && in.Role != LinkRoleDelivery {
				continue
			}
			if in.Role != "" {
				old.Role = link.Role
			}
			old.Provenance = link.Provenance
			if role, _ := NormalizeLinkRole(old.Role); role != LinkRoleDelivery {
				old.Provenance = LinkProvenanceUnbound
			}
			continue
		}
		if len(out[index].Links) >= MaxRepositoryLinks {
			return nil, Problem("limit")
		}
		out[index].Links = append(out[index].Links, link)
	}
	return out, nil
}
