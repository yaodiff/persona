package extension

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// The extension model (docs/extensions/extensions.md "Dependencies, exports and packs", #1854): an extension may need others (depends), open its
// own extension points to them (exports), or be nothing but a set of others installed together (extension_pack).

// Dependency is an extension this one needs installed and on.
type Dependency struct {
	ID string `json:"id"`
	// Version is a range of its versions (">=1.2.0", "<2.0.0", both); empty: any.
	Version string `json:"version,omitempty"`
}

// Exports are the extension points it opens to other extensions.
type Exports struct {
	ExtensionPoints []ExportedPoint `json:"extension_points"`
}

// ExportedPoint is one: others contribute under contributes["<its extension>.<ID>"].
type ExportedPoint struct {
	ID          string `json:"id"`
	Version     int    `json:"version"`
	Description string `json:"description,omitempty"`
	// Schema is each contribution's JSON Schema, in the configuration's subset; it has a string id.
	Schema json.RawMessage `json:"schema,omitempty"`
}

// PackMember is one extension of a pack.
type PackMember struct {
	ID string `json:"id"`
	// Market is the address of the market it comes from, as registered (github:owner/repo …); empty: the pack's own
	// market, else the first registered market that has it.
	Market  string `json:"market,omitempty"`
	Version string `json:"version,omitempty"`
}

var (
	exportedPointID = lazyre.New(`^[a-z][a-z0-9_]{0,31}$`)
	exportedItemID  = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// SplitExported says whether point is another extension's ("<extension>.<point>") and whose.
func SplitExported(point string) (extension, name string, ok bool) {
	extension, name, ok = strings.Cut(point, ".")
	return extension, name, ok && ValidID(extension) && exportedPointID.MatchString(name)
}

// ItemOf is how point's contributions say their id: the hub's points (Points), and another extension's exported ones
// (an object with a string id). False for a point it knows nothing of.
func ItemOf(point string) (func(raw json.RawMessage) (string, error), bool) {
	if p, ok := Points[point]; ok {
		return p.Item, true
	}
	if _, _, ok := SplitExported(point); ok {
		return exportedItem, true
	}
	return nil, false
}

func exportedItem(raw json.RawMessage) (string, error) {
	var it struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &it); err != nil {
		return "", errors.New("each contribution is an object with an id")
	}
	if !exportedItemID.MatchString(it.ID) {
		return it.ID, fmt.Errorf("id %q: a-z0-9_-, at most 64", it.ID)
	}
	return it.ID, nil
}

// IsPack says whether the manifest is an extension pack.
func (m *Manifest) IsPack() bool { return len(m.ExtensionPack) > 0 }

// Exported is the extension point the manifest exports by id, nil when it has none.
func (m *Manifest) Exported(id string) *ExportedPoint {
	if m == nil || m.Exports == nil {
		return nil
	}
	for i := range m.Exports.ExtensionPoints {
		if m.Exports.ExtensionPoints[i].ID == id {
			return &m.Exports.ExtensionPoints[i]
		}
	}
	return nil
}

// DependsOn says whether the manifest needs extension id.
func (m *Manifest) DependsOn(id string) bool {
	return m != nil && slices.ContainsFunc(m.Depends, func(d Dependency) bool { return d.ID == id })
}

func (m *Manifest) validateModel() []error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	seen := map[string]bool{}
	for _, d := range m.Depends {
		switch {
		case !ValidID(d.ID):
			bad("depends: %q is not an extension id", d.ID)
		case d.ID == m.ID:
			bad("depends: on itself")
		case seen[d.ID]:
			bad("depends: %q twice", d.ID)
		}
		seen[d.ID] = true
		if d.Version != "" {
			if _, err := ParseVersionRange(d.Version); err != nil {
				bad("depends: %s %q: %v", d.ID, d.Version, err)
			}
		}
	}
	if m.Exports != nil {
		if len(m.Exports.ExtensionPoints) == 0 {
			bad("exports.extension_points: none")
		}
		ids := map[string]bool{}
		for _, p := range m.Exports.ExtensionPoints {
			if !exportedPointID.MatchString(p.ID) {
				bad("exports: %q: a-z0-9_, a letter first, at most 32", p.ID)
			}
			if ids[p.ID] {
				bad("exports: %q twice", p.ID)
			}
			ids[p.ID] = true
			if p.Version < 1 {
				bad("exports: %s: version 1 or more", p.ID)
			}
			if len(p.Schema) > 0 {
				s, err := ParseSchema(p.Schema)
				if err != nil {
					bad("exports: %s: schema: %v", p.ID, err)
				} else if s == nil || !slices.ContainsFunc(s.Fields, func(f Field) bool { return f.Name == "id" && f.Type == "string" }) {
					bad("exports: %s: schema: each contribution has a string id: declare it", p.ID)
				}
			}
		}
	}
	for _, name := range sortedKeys(m.Contributes) {
		owner, _, ok := SplitExported(name)
		if !ok {
			if strings.Contains(name, ".") {
				bad("contributes.%s: another extension's point is <extension id>.<point id>", name)
			}
			continue
		}
		if owner == m.ID {
			bad("contributes.%s: its own exported point", name)
		}
		if !m.DependsOn(owner) {
			bad("contributes.%s: contributes to %s, which is not in depends", name, owner)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(m.Contributes[name], &items); err != nil || len(items) == 0 {
			bad("contributes.%s: a list, not empty", name)
			continue
		}
		ids := map[string]bool{}
		for _, raw := range items {
			id, err := exportedItem(raw)
			if err != nil {
				bad("contributes.%s: %v", name, err)
				continue
			}
			if ids[id] {
				bad("contributes.%s: %q twice", name, id)
			}
			ids[id] = true
		}
	}
	if m.IsPack() {
		if m.Runtime != nil || m.Node != nil || len(m.Contributes) > 0 || len(m.ActivationEvents) > 0 || len(m.Configuration) > 0 ||
			len(m.Secrets) > 0 || len(m.Depends) > 0 || m.Exports != nil ||
			len(m.Permissions.Network) > 0 || m.Permissions.HubAPI || len(m.Permissions.Hub) > 0 || m.Permissions.LauncherKeys || m.Permissions.GhLogin ||
			m.Permissions.UsageGateways || m.Permissions.AgentRuntimes || m.Permissions.ErrorReports || m.Permissions.RoutineFacts || m.Permissions.AuthRelay {
			bad("extension_pack: a pack has only its members (no runtime, contributes, settings, secrets, permissions, depends or exports)")
		}
		members := map[string]bool{}
		for _, p := range m.ExtensionPack {
			switch {
			case !ValidID(p.ID):
				bad("extension_pack: %q is not an extension id", p.ID)
			case p.ID == m.ID:
				bad("extension_pack: itself")
			case members[p.ID]:
				bad("extension_pack: %q twice", p.ID)
			}
			members[p.ID] = true
			if p.Version != "" {
				if _, err := ParseVersionRange(p.Version); err != nil {
					bad("extension_pack: %s %q: %v", p.ID, p.Version, err)
				}
			}
		}
		if len(m.ExtensionPack) > 64 {
			bad("extension_pack: more than 64")
		}
	}
	return errs
}

// VersionRange is a range of versions: ">=1.2.0", "<2.0.0", or both.
type VersionRange struct {
	bounds []versionBound
}

type versionBound struct {
	op string // ">=" or "<"
	v  Version
}

// ParseVersionRange reads ">=X.Y.Z", "<X.Y.Z" or both, separated by spaces.
func ParseVersionRange(s string) (VersionRange, error) {
	var r VersionRange
	for _, f := range strings.Fields(s) {
		var b versionBound
		switch {
		case strings.HasPrefix(f, ">="):
			b.op = ">="
		case strings.HasPrefix(f, "<") && !strings.HasPrefix(f, "<="):
			b.op = "<"
		default:
			return VersionRange{}, fmt.Errorf("%q: only >=X.Y.Z and <X.Y.Z", f)
		}
		v, err := ParseVersion(strings.TrimPrefix(f, b.op))
		if err != nil {
			return VersionRange{}, fmt.Errorf("%q: %v", f, err)
		}
		b.v = v
		r.bounds = append(r.bounds, b)
	}
	if len(r.bounds) == 0 {
		return VersionRange{}, errors.New("empty")
	}
	return r, nil
}

// Allows says whether version is in the range (a version that does not parse is not).
func (r VersionRange) Allows(version string) bool {
	v, err := ParseVersion(version)
	if err != nil {
		return false
	}
	for _, b := range r.bounds {
		c := v.Compare(b.v)
		if b.op == ">=" && c < 0 || b.op == "<" && c >= 0 {
			return false
		}
	}
	return true
}

// Allows says whether version meets the dependency's range (any, without one).
func (d Dependency) Allows(version string) bool {
	if d.Version == "" {
		return true
	}
	r, err := ParseVersionRange(d.Version)
	return err == nil && r.Allows(version)
}

// Dependency states (DependencyState.State).
const (
	DepOK       = "ok"
	DepMissing  = "missing"
	DepDisabled = "disabled"
	DepOld      = "old"       // installed, its version out of the range
	DepNotReady = "not_ready" // installed, on and compatible, but settings or transitive dependencies are not ready
)

// DependencyState is one dependency of an extension on this hub.
type DependencyState struct {
	ID        string `json:"id"`
	Version   string `json:"version,omitempty"` // the range asked for
	State     string `json:"state"`
	Installed string `json:"installed,omitempty"` // the version installed
	Name      string `json:"name,omitempty"`
}

// Dependencies are the states of m's dependencies on this hub.
func (r *Registry) Dependencies(m *Manifest) []DependencyState {
	return r.DependenciesContext(context.Background(), m)
}

func (r *Registry) DependenciesContext(ctx context.Context, m *Manifest) []DependencyState {
	var out []DependencyState
	if m == nil {
		return out
	}
	g := r.dependencyGraph(ctx)
	g.withManifest(m)
	for _, d := range m.Depends {
		out = append(out, g.state(d))
	}
	return out
}

// checkExported holds contributions to an exported point to its schema.
func checkExported(ep *ExportedPoint, raw json.RawMessage) error {
	if len(ep.Schema) == 0 {
		return nil
	}
	s, err := ParseSchema(ep.Schema)
	if err != nil || s == nil {
		return nil // its own manifest was checked at install: nothing to hold to
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	for _, it := range items {
		if err := s.Check(it); err != nil {
			return err
		}
	}
	return nil
}

// Dependents are the extensions that depend on id, by id.
func (r *Registry) Dependents(id string) []*Extension {
	var out []*Extension
	for _, p := range r.List() {
		if p.Full.DependsOn(id) {
			out = append(out, p)
		}
	}
	return out
}

// ExportedContribution is one contribution to an extension's exported point, as its exporter gets it.
type ExportedContribution struct {
	Extension string          `json:"extension"`
	Item      json.RawMessage `json:"item"`
}

// ContributionsTo are the contributions to exporter's points from the other extensions that are on, by point id: what
// initialize's contributions_to_me says (and extension.contributions_changed).
func (r *Registry) ContributionsTo(exporter string) map[string][]ExportedContribution {
	return r.ContributionsToContext(context.Background(), exporter)
}

func (r *Registry) ContributionsToContext(ctx context.Context, exporter string) map[string][]ExportedContribution {
	out := map[string][]ExportedContribution{}
	g := r.dependencyGraph(ctx)
	ex := g.nodes[exporter]
	if ex == nil || ex.Full.Exports == nil || !g.ready(exporter) {
		return out
	}
	for _, id := range sortedKeys(g.nodes) {
		p := g.nodes[id]
		if id == exporter || !p.Full.DependsOn(exporter) || !g.ready(id) {
			continue
		}
		for _, point := range sortedKeys(p.Manifest.Contributes) {
			owner, name, ok := SplitExported(point)
			if !ok || owner != exporter || ex.Full.Exported(name) == nil {
				continue
			}
			var items []json.RawMessage
			if json.Unmarshal(p.Manifest.Contributes[point], &items) != nil {
				continue
			}
			for _, it := range items {
				out[name] = append(out[name], ExportedContribution{Extension: id, Item: it})
			}
		}
	}
	return out
}

// PreviewOf is a waiting preview (left in place), nil when it expired or was used.
func (r *Registry) PreviewOf(id string) *Preview { return r.peek(id) }

// DropExpiredPreviews removes the previews past their time with their staged packages. Previews otherwise expire
// only when another one is made or used, so an abandoned one would keep its package on disk on an idle hub.
func (r *Registry) DropExpiredPreviews() { r.expire() }

// ContributesTo are the extensions m contributes to (the owners of its exported points).
func (m *Manifest) ContributesTo() []string {
	var out []string
	for point := range m.Contributes {
		if owner, _, ok := SplitExported(point); ok && !slices.Contains(out, owner) {
			out = append(out, owner)
		}
	}
	slices.Sort(out)
	return out
}
