// Package workspacenaming resolves metadata only. Callers persist a Plan with one
// preparation operation and reuse its results without consulting current settings.
package workspacenaming

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/repositorywire"
)

const Version = "workspace-naming/v1"

type Rules struct {
	BaseDirectory         string `json:"base_directory,omitempty"`
	TaskDirectoryTemplate string `json:"task_directory_template,omitempty"`
	BranchTemplate        string `json:"branch_template,omitempty"`
}

func Defaults() Rules { return Rules{"{home}/workspace", "task-{task-id}", "persona/{task-id}"} }
func (r Rules) fields() map[string]string {
	return map[string]string{"base_directory": r.BaseDirectory, "task_directory_template": r.TaskDirectoryTemplate, "branch_template": r.BranchTemplate}
}
func (r *Rules) set(k, v string) {
	switch k {
	case "base_directory":
		r.BaseDirectory = v
	case "task_directory_template":
		r.TaskDirectoryTemplate = v
	case "branch_template":
		r.BranchTemplate = v
	}
}
func Effective(assistant, task Rules) (Rules, map[string]string) {
	r := Defaults()
	sources := map[string]string{}
	for k := range r.fields() {
		sources[k] = "default"
	}
	for i, layer := range []Rules{assistant, task} {
		for k, v := range layer.fields() {
			if v != "" {
				r.set(k, v)
				sources[k] = []string{"assistant", "task"}[i]
			}
		}
	}
	return r, sources
}

type Problem struct {
	Code     string `json:"code"`
	Field    string `json:"field"`
	RepoID   string `json:"repo_id,omitempty"`
	Severity string `json:"severity"`
}

func (p Problem) Error() string        { return "workspace_naming_" + p.Code + ": " + p.Field }
func problem(code, field string) error { return Problem{Code: code, Field: field, Severity: "error"} }

// Patch distinguishes omitted, null and empty, rejects duplicate/unknown fields,
// and returns no partially applied rules on error.
func Patch(current Rules, raw []byte) (Rules, error) {
	if len(raw) == 0 {
		return current, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return Rules{}, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return current, problem("invalid", "workspace_rules")
	}
	seen := map[string]bool{}
	next := current
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return current, problem("invalid", "workspace_rules")
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return current, problem("invalid", "workspace_rules")
		}
		seen[key] = true
		if _, ok := current.fields()[key]; !ok {
			return current, problem("unknown_field", key)
		}
		var value *string
		if d.Decode(&value) != nil {
			return current, problem("invalid", key)
		}
		if value == nil {
			next.set(key, "")
		} else {
			if *value == "" {
				return current, problem("field_empty", key)
			}
			next.set(key, *value)
		}
	}
	if _, err = d.Token(); err != nil {
		return current, problem("invalid", "workspace_rules")
	}
	var tail any
	if d.Decode(&tail) != io.EOF {
		return current, problem("invalid", "workspace_rules")
	}
	if err = Validate(next); err != nil {
		return current, err
	}
	return next, nil
}

func expand(template, field string, values map[string]string, syntaxOnly bool) (string, error) {
	if len(template) > 2048 || strings.TrimSpace(template) != template || strings.ContainsAny(template, "$`\\") || strings.ContainsFunc(template, unicode.IsControl) {
		return "", problem("template_invalid", field)
	}
	var out strings.Builder
	for i := 0; i < len(template); {
		if template[i] == '}' {
			return "", problem("template_invalid", field)
		}
		if template[i] != '{' {
			out.WriteByte(template[i])
			i++
			continue
		}
		end := strings.IndexByte(template[i:], '}')
		if end < 0 {
			return "", problem("template_invalid", field)
		}
		key := template[i+1 : i+end]
		value, ok := values[key]
		if !ok {
			code := "unknown_field"
			if slices.Contains([]string{"home", "persona-home", "task-id", "task-slug", "yyyy", "mmdd", "repo", "branch", "base-branch"}, key) {
				code = "field_not_allowed"
			}
			return "", problem(code, field)
		}
		if value == "" && !syntaxOnly {
			return "", problem("field_unavailable", field)
		}
		out.WriteString(value)
		i += end + 1
	}
	return out.String(), nil
}
func fieldsFor(field string) map[string]string {
	if field == "base_directory" {
		return map[string]string{"home": "/home/example"}
	}
	v := map[string]string{"task-id": "todo_example", "task-slug": "example", "yyyy": "2026", "mmdd": "1114"}
	if field == "branch_template" {
		v["repo"] = "repo"
		v["branch"] = "main"
		v["base-branch"] = "main"
	}
	return v
}
func Validate(r Rules) error {
	for _, field := range []string{"base_directory", "task_directory_template", "branch_template"} {
		value := r.fields()[field]
		if value == "" {
			continue
		}
		expanded, err := expand(value, field, fieldsFor(field), true)
		if err != nil {
			return err
		}
		switch field {
		case "task_directory_template":
			if !safeRelative(expanded) {
				return problem("path_invalid", field)
			}
		case "branch_template":
			if _, err := repositorywire.NormalizeRef(repositorywire.RequestedRef{Kind: "branch", Value: expanded}); err != nil {
				return problem("branch_invalid", field)
			}
		case "base_directory":
			windowsAbsolute := len(expanded) > 2 && ((expanded[0] >= 'A' && expanded[0] <= 'Z') || (expanded[0] >= 'a' && expanded[0] <= 'z')) && expanded[1:3] == ":/"
			if !strings.HasPrefix(value, "{home}/") && value != "{home}" && !strings.HasPrefix(value, "~/") && !strings.HasPrefix(expanded, "/") && !windowsAbsolute {
				return problem("path_invalid", field)
			}
			if !safeBase(expanded) {
				return problem("path_invalid", field)
			}
		}
	}
	return nil
}
func safeRelative(path string) bool {
	if path == "" || strings.ContainsAny(path, "\\:<>\"|?*\x00") || strings.ContainsFunc(path, unicode.IsControl) || strings.HasPrefix(path, "/") {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." || strings.EqualFold(part, ".persona") || strings.EqualFold(part, ".git") || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || repositorywire.IsWindowsReservedName(part) {
			return false
		}
	}
	return true
}
func safeBase(path string) bool {
	if windowsNamespace(path) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." || part == "." {
			return false
		}
	}
	return !strings.ContainsFunc(path, unicode.IsControl)
}

// Device/extended namespaces can name an ordinary directory with a different
// filepath volume. Reject them instead of comparing inequivalent spellings or
// changing their special Windows semantics by stripping the prefix.
func windowsNamespace(path string) bool {
	path = strings.ReplaceAll(path, `\`, "/")
	return strings.HasPrefix(path, "//?/") || strings.HasPrefix(path, "//./") || strings.HasPrefix(path, "/??/")
}
func fragment(s string, limit int) string {
	var out strings.Builder
	dash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			if r >= 'A' && r <= 'Z' {
				r += 32
			}
			if dash && out.Len() > 0 {
				out.WriteByte('-')
			}
			dash = false
			if limit > 0 && out.Len()+len(string(r)) > limit {
				break
			}
			out.WriteRune(r)
		} else {
			dash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

type Source struct {
	Manager      string                             `json:"manager"`
	TodoID       string                             `json:"todo_id"`
	Title        string                             `json:"title"`
	MachineID    string                             `json:"machine_id"`
	PlannedAt    time.Time                          `json:"planned_at"`
	Timezone     string                             `json:"timezone"`
	Rules        Rules                              `json:"rules"`
	RuleSources  map[string]string                  `json:"rule_sources,omitempty"`
	Layout       repositorywire.Layout              `json:"layout"`
	Repositories repositorywire.PreparationSnapshot `json:"repository_snapshot"`
	// TaskBranch is the literal branch explicitly requested for a single checkout.
	TaskBranch string `json:"task_branch,omitempty"`
}
type NodeContext struct {
	Home        string `json:"home"`
	PersonaHome string `json:"persona_home,omitempty"`
	PathFlavor  string `json:"path_flavor,omitempty"`
}
type RepositoryPlan struct {
	RepoID       string                      `json:"repo_id"`
	RequestedRef repositorywire.RequestedRef `json:"requested_ref"`
	RelativePath string                      `json:"relative_path"`
	AbsolutePath string                      `json:"absolute_path"`
	TaskBranch   string                      `json:"task_branch"`
}
type Plan struct {
	Version      string           `json:"version"`
	Source       Source           `json:"source"`
	Node         NodeContext      `json:"node"`
	TaskSlug     string           `json:"task_slug"`
	BasePath     string           `json:"base_path"`
	RootPath     string           `json:"root_path"`
	Repositories []RepositoryPlan `json:"repositories"`
	Digest       string           `json:"digest"`
}
type Preview struct {
	Status         string         `json:"status"`
	Sample         bool           `json:"sample"`
	Plan           *Plan          `json:"plan,omitempty"`
	Diagnostics    []Problem      `json:"diagnostics"`
	SystemDefaults *Rules         `json:"system_defaults,omitempty"`
	BranchSources  []BranchSource `json:"branch_sources,omitempty"`
}

type BranchSource = repositorywire.BranchSource

func branchSource(repo repositorywire.PreparationRepository) BranchSource {
	id := repositorywire.RepositoryID(repo.RepoID)
	switch repo.RequestedRef.Kind {
	case "default", "":
		return BranchSource{RepoID: id, Kind: repositorywire.BranchRepositoryDefault}
	case "branch":
		return BranchSource{RepoID: id, Kind: repositorywire.BranchExplicit, Branch: strings.TrimPrefix(repo.RequestedRef.Value, "refs/heads/")}
	default:
		return BranchSource{RepoID: id, Kind: repositorywire.BranchOtherRef}
	}
}

// Describe supplies preview metadata independently of the target Node version.
func (p *Preview) Describe(s Source) {
	p.SystemDefaults = new(Defaults())
	p.BranchSources = nil
	for _, repo := range s.Repositories.Repositories {
		p.BranchSources = append(p.BranchSources, branchSource(repo))
	}
	slices.SortFunc(p.BranchSources, func(a, b BranchSource) int { return cmp.Compare(a.RepoID, b.RepoID) })
}

func (p *Preview) add(code, field, repo, severity string) {
	p.Diagnostics = append(p.Diagnostics, Problem{code, field, repo, severity})
	if severity == "error" {
		if code == "field_unavailable" && p.Status != "invalid" {
			p.Status = "incomplete"
		} else {
			p.Status = "invalid"
		}
	}
}
func within(base, path string) bool {
	return withinOS(base, path, runtime.GOOS)
}
func withinOS(base, path, goos string) bool {
	// macOS commonly uses a case-insensitive volume. Reserve private paths
	// conservatively there, even on a case-sensitive volume; Linux stays exact.
	if goos == "darwin" {
		base, path = strings.ToLower(base), strings.ToLower(path)
	}
	rel, err := filepath.Rel(base, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// InPrivateState checks lexical containment under the target's private-state policy.
// Node callers must resolve existing symlinks before checking physical paths.
func InPrivateState(path, state string) bool { return within(state, path) }

// Resolve never reads the clock, filesystem, Git, environment or network. Node
// supplies its own path context. Paths are lexical, not ownership attestation.
func Resolve(s Source, n NodeContext) Preview {
	return ResolveWithPhysicalHome(s, n, n.Home)
}

// PhysicalHomePath substitutes only the configured home prefix. Any remaining
// symlinks and non-clean source paths must reach the node's path checks intact.
func PhysicalHomePath(path, home, physicalHome string) string {
	if home == physicalHome || !filepath.IsAbs(home) || !filepath.IsAbs(physicalHome) ||
		!filepath.IsAbs(path) || path != filepath.Clean(path) {
		return path
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return path
	}
	return filepath.Join(physicalHome, rel)
}

// ResolveWithPhysicalHome uses the node-supplied physical prefix to derive each
// workspace path before checking its length and freezing the plan. The original
// context and rules stay in the snapshot, including for previously valid plans.
func ResolveWithPhysicalHome(s Source, n NodeContext, physicalHome string) Preview {
	p := Preview{Status: "complete", Diagnostics: []Problem{}}
	p.Describe(s)
	rules := s.Rules
	if len(s.Repositories.Repositories) == 0 || s.TaskBranch != "" {
		rules.BranchTemplate = ""
	}
	if err := Validate(rules); err != nil {
		e := err.(Problem)
		p.add(e.Code, e.Field, "", "error")
		return p
	}
	if s.TodoID == "" || !safeRelative(s.TodoID) || strings.Contains(s.TodoID, "/") {
		p.add("invalid_task_id", "todo_id", "", "error")
		return p
	}
	if s.PlannedAt.IsZero() {
		p.add("field_unavailable", "planned_at", "", "error")
		return p
	}
	if s.Timezone == "" {
		s.Timezone = "UTC"
		p.add("timezone_defaulted", "timezone", "", "warning")
	}
	zone, err := time.LoadLocation(s.Timezone)
	if err != nil {
		p.add("invalid", "timezone", "", "error")
		return p
	}
	s.PlannedAt = s.PlannedAt.UTC()
	date := s.PlannedAt.In(zone)
	slug := fragment(s.Title, 64)
	if slug == "" {
		slug = "task"
	}
	values := map[string]string{"task-id": s.TodoID, "task-slug": slug, "yyyy": date.Format("2006"), "mmdd": date.Format("0102")}
	if !filepath.IsAbs(n.Home) || n.PathFlavor != "" && n.PathFlavor != runtimeFlavor() ||
		runtimeFlavor() == "windows" && (windowsNamespace(n.Home) || windowsNamespace(n.PersonaHome)) {
		p.add("context_invalid", "machine_id", "", "error")
		return p
	}
	n.PathFlavor = runtimeFlavor()
	baseTemplate := s.Rules.BaseDirectory
	if strings.HasPrefix(baseTemplate, "~/") {
		baseTemplate = "{home}/" + strings.TrimPrefix(baseTemplate, "~/")
	}
	base, err := expand(baseTemplate, "base_directory", map[string]string{"home": n.Home}, false)
	if err != nil {
		e := err.(Problem)
		p.add(e.Code, e.Field, "", "error")
		return p
	}
	base = filepath.Clean(base)
	state := n.PersonaHome
	if state == "" {
		state = filepath.Join(n.Home, ".persona")
	}
	if !filepath.IsAbs(base) || InPrivateState(base, state) {
		p.add("base_scope_denied", "base_directory", "", "error")
		return p
	}
	base = PhysicalHomePath(base, n.Home, physicalHome)
	rel, err := expand(s.Rules.TaskDirectoryTemplate, "task_directory_template", values, false)
	if err != nil {
		e := err.(Problem)
		p.add(e.Code, e.Field, "", "error")
		return p
	}
	if !safeRelative(rel) {
		p.add("path_invalid", "task_directory_template", "", "error")
		return p
	}
	root := filepath.Join(base, filepath.FromSlash(rel))
	if !within(base, root) || root == base || InPrivateState(root, state) {
		p.add("path_invalid", "task_directory_template", "", "error")
		return p
	}
	if len(root) > 4096 || slices.ContainsFunc(strings.Split(filepath.ToSlash(root), "/"), func(part string) bool { return len(part) > 255 }) {
		p.add("path_too_long", "task_directory_template", "", "error")
		return p
	}
	if !repositorywire.ValidLayout(s.Layout, len(s.Repositories.Repositories)) {
		p.add("layout_invalid", "layout", "", "error")
		return p
	}
	if s.TaskBranch != "" && len(s.Repositories.Repositories) != 1 {
		p.add("branch_invalid", "branch_template", "", "error")
		return p
	}
	plan := Plan{Version: Version, Source: s, Node: n, TaskSlug: slug, BasePath: base, RootPath: root, Repositories: []RepositoryPlan{}}
	plan.Source.Repositories.Repositories = slices.Clone(s.Repositories.Repositories)
	slices.SortFunc(plan.Source.Repositories.Repositories, func(a, b repositorywire.PreparationRepository) int { return strings.Compare(a.RepoID, b.RepoID) })
	aliases := map[string]bool{}
	ids := map[string]bool{}
	for _, repo := range plan.Source.Repositories.Repositories {
		if ids[repo.RepoID] || repo.RepoID == "" {
			p.add("repository_invalid", "repositories", repo.RepoID, "error")
			continue
		}
		ids[repo.RepoID] = true
		if _, err := repositorywire.NormalizeRef(repo.RequestedRef); err != nil {
			p.add("repository_invalid", "requested_ref", repo.RepoID, "error")
			continue
		}
		alias := repo.DirectoryAlias
		if alias == "" {
			alias = repositorywire.DefaultAlias(repo.SourceSnapshot.Identity)
		}
		path, err := repositorywire.RepositoryPath(s.Layout, alias, len(s.Repositories.Repositories))
		if err != nil {
			p.add("alias_invalid", "directory_alias", repo.RepoID, "error")
			continue
		}
		if aliases[strings.ToLower(path)] {
			p.add("alias_duplicate", "directory_alias", repo.RepoID, "error")
			continue
		}
		aliases[strings.ToLower(path)] = true
		v := map[string]string{}
		for k, x := range values {
			v[k] = x
		}
		v["repo"] = alias
		v["branch"] = ""
		v["base-branch"] = ""
		if branch := branchSource(repo).Branch; branch != "" {
			v["branch"] = fragment(branch, 0)
			v["base-branch"] = v["branch"]
			if v["branch"] != branch && (strings.Contains(s.Rules.BranchTemplate, "{branch}") || strings.Contains(s.Rules.BranchTemplate, "{base-branch}")) {
				p.add("fragment_normalized", "branch_template", repo.RepoID, "warning")
			}
		}
		branch := s.TaskBranch
		if branch == "" {
			branch, err = expand(s.Rules.BranchTemplate, "branch_template", v, false)
			if err != nil {
				e := err.(Problem)
				p.add(e.Code, e.Field, repo.RepoID, "error")
				continue
			}
		}
		if _, err = repositorywire.NormalizeRef(repositorywire.RequestedRef{Kind: "branch", Value: branch}); err != nil {
			p.add("branch_invalid", "branch_template", repo.RepoID, "error")
			continue
		}
		plan.Repositories = append(plan.Repositories, RepositoryPlan{repo.RepoID, repo.RequestedRef, path, filepath.Join(root, path), branch})
	}
	if !strings.Contains(s.Rules.TaskDirectoryTemplate, "{task-id}") {
		p.add("collision_risk", "task_directory_template", "", "warning")
	}
	if p.Status == "complete" {
		plan.Digest = Digest(plan)
		p.Plan = &plan
	}
	return p
}
func runtimeFlavor() string {
	if filepath.Separator == '\\' {
		return "windows"
	}
	return "posix"
}

// Digest is computed in one Go implementation, independent of client JSON order.
func Digest(p Plan) string {
	p.Digest = ""
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CheckPlan checks a frozen snapshot's version and integrity. It must not resolve
// paths again: a manager may run on a different OS than the target Node.
func CheckPlan(p Plan) error {
	if len(p.Repositories) != len(p.Source.Repositories.Repositories) || !repositorywire.ValidLayout(p.Source.Layout, len(p.Repositories)) {
		return problem("layout_invalid", "layout")
	}
	for _, repo := range p.Source.Repositories.Repositories {
		if repo.Local == nil {
			continue
		}
		snapshot, err := repositorywire.LocalSnapshot(p.Source.Manager, p.Source.TodoID, repo)
		if err != nil || p.Source.Layout != repositorywire.LayoutRoot || snapshot.Digest != p.Source.Repositories.Digest {
			return problem("repository_invalid", "repositories")
		}
	}
	if p.Version != Version || p.Digest == "" || p.Digest != Digest(p) {
		return problem("plan_invalid", "plan")
	}
	return nil
}
func ValidOperationID(id string) bool {
	return len(id) > 0 && len(id) <= 100 && repositorywire.ValidateAlias(id) == nil
}
