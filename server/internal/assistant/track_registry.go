package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
)

// The assistant's tracks (#13104, docs/assistant/health.md): a track is a named place the assistant's conversations
// run in, of a kind (a domain space is one kind), with a directory of its own. Its conversations name it by its slug
// in the field the wire calls space (Session.Space, StartParams.Space, ?space=). The main space ("") is the
// assistant's own directory and is no entry; the health space is the built-in entry, which the manager supplies.
// Every decision on how a track's information is used (the lock screen and channels, extensions, the knowledge base,
// handing it out, the other conversations' file tools) is usage's (usage.go), by the information's categories and the
// scene: a track takes part only through its default categories, never its name or kind.

// TrackPolicy is how a track's information may be used, one switch a use: usage's rules for the scenes the call sites
// ask about, summed up (policyOf). The switches are temporary: they give the call sites one place to read until each
// asks usage for its own scene.
type TrackPolicy struct {
	// Redact: pushes, the lock screen, Lark and the plugin channels say only that there is news.
	Redact bool
	// HideFromExtensions: an extension does not see the track's conversations, transcripts, cards, to-dos, feed or
	// memory.
	HideFromExtensions bool
	// NoKBWrite: the track's conversations do not write the shared knowledge base.
	NoKBWrite bool
	// ConfirmOutbound: what the track hands outside it is confirmed on the phone each time, and a task plan it makes
	// does not start on its own.
	ConfirmOutbound bool
	// DenyMainRead: the assistant's other conversations' Read and Edit are denied the track's directory.
	DenyMainRead bool
}

// strictness orders policies: the more switches on, the stricter.
func (p TrackPolicy) strictness() int {
	n := 0
	for _, on := range []bool{p.Redact, p.HideFromExtensions, p.NoKBWrite, p.ConfirmOutbound, p.DenyMainRead} {
		if on {
			n++
		}
	}
	return n
}

// strictPolicy turns every use rule on: sensitive information's outside its track, the health space's.
var strictPolicy = TrackPolicy{Redact: true, HideFromExtensions: true, NoKBWrite: true, ConfirmOutbound: true, DenyMainRead: true}

// The information categories (docs/assistant/health.md), checked here rather than in the database so adding one is a
// code change with its use rules: infoCategoriesVersion goes up with every change to the list.
const (
	categoryHealth        = "health"
	categoryFinance       = "finance"
	categoryRelationships = "relationships"
	categoryEmotion       = "emotion"
	categoryLegalID       = "legal_id"
	infoCategoriesVersion = 1
)

var infoCategories = []string{categoryHealth, categoryFinance, categoryRelationships, categoryEmotion, categoryLegalID}

// validCategories: every one of cats is a known information category.
func validCategories(cats []string) bool {
	for _, c := range cats {
		if !slices.Contains(infoCategories, c) {
			return false
		}
	}
	return true
}

// Track kinds and templates. A kind is the track's type (its defaults); a template the instructions its conversations
// get. Both are checked here, not in the database.
const (
	trackKindSpace = "space"
	// trackTemplateAssistant is kept for the main track's instructions.
	trackTemplateAssistant = "assistant"
	trackTemplateSpace     = "space"
	trackTemplateHealth    = "health"
)

var trackTemplates = []string{trackTemplateAssistant, trackTemplateSpace, trackTemplateHealth}

// trackEntry is one registered track.
type trackEntry struct {
	Slug        string
	Kind        string
	Title       string
	Icon        string
	Description string
	// DefaultCategories are the information categories everything produced in the track carries at least (the
	// tagging default); the health space's is health.
	DefaultCategories []string
	Template          string
	// DirName is the track's directory, relative to the state directory: data, never derived from the kind. The
	// health space's is the manager's (identity.HealthDirName); a new track's defaults to tracks/<slug>.
	DirName  string
	Builtin  bool
	Archived bool
	// ArchivedAt is when it was archived (nil: it is not).
	ArchivedAt *time.Time
	Revision   int64
	// Config is the track's effective config: its kind's, with the track's template, default categories and override
	// (track_kinds.go). Every decision a kind makes reads it, never the kind's name.
	Config assistantwire.TrackKindConfig
	// Override is what the track sets apart from its kind (config_override), as stored.
	Override []byte
}

// builtinTracks are the tracks every manager has whatever its store holds: the health space, of kind space, with the
// health template and the health category as its overrides of that kind's defaults, in the manager's health
// directory.
func (s *Service) builtinTracks() []trackEntry {
	e := trackEntry{Slug: SpaceHealth, Kind: trackKindHealth, DefaultCategories: []string{categoryHealth},
		Template: trackTemplateHealth, DirName: identity.HealthDirName(s.id), Builtin: true, Revision: 1}
	e.Config, _ = effectiveTrackConfig(builtinKindConfigs()[trackKindHealth], e.Template, e.DefaultCategories, nil)
	return []trackEntry{e}
}

// builtinKinds are the kinds every manager has, in builtinKindOrder.
func builtinKinds() []kindEntry {
	cfgs := builtinKindConfigs()
	out := make([]kindEntry, 0, len(builtinKindOrder))
	for _, slug := range builtinKindOrder {
		out = append(out, kindEntry{Slug: slug, Config: cfgs[slug], Builtin: true, Revision: 1})
	}
	return out
}

// defaultTrackDir is where a new track of this manager keeps its directory: tracks/<slug> (tracks-<id>/<slug>), the
// same whatever its kind.
func (s *Service) defaultTrackDir(slug string) string {
	return path.Join(identity.TracksDirName(s.id), slug)
}

// validTrackDir: dir names a directory below the state directory (relative, clean, no "..").
func validTrackDir(dir string) bool {
	return dir != "" && !path.IsAbs(dir) && path.Clean(dir) == dir && dir != "." && !strings.HasPrefix(dir, "../") &&
		!strings.Contains(dir, "/../") && !strings.Contains(dir, `\`)
}

// trackStore reads the registry (assistant_tracks).
type trackStore interface {
	Tracks(ctx context.Context) ([]db.Track, error)
}

// trackKindStore reads the user's kinds (assistant_track_kinds).
type trackKindStore interface {
	TrackKinds(ctx context.Context) ([]db.TrackKind, error)
}

var (
	_ trackStore     = (*db.DB)(nil)
	_ trackKindStore = (*db.DB)(nil)
)

// trackRegistryTTL bounds how long the manager keeps what it read of the registry.
const trackRegistryTTL = 30 * time.Second

// trackRegistry caches the registry: read at most once a TTL, and dropped when the registry changes.
type trackRegistry struct {
	mu    sync.Mutex
	at    time.Time
	list  []trackEntry
	kinds []kindEntry
}

// tracks are the registered tracks, archived ones included: the built-in ones as the manager has them, then those its
// store registered. A row that is not valid here (its slug, template, categories, directory, kind or override) is left
// out and logged: its conversations get the policy of an unknown track, the strictest. So is a row whose kind is not
// enabled (unknown, or newer than this manager). A store that cannot be read leaves the built-in ones (and what it read
// last).
func (s *Service) tracks(ctx context.Context) []trackEntry {
	list, _ := s.registry(ctx)
	return list
}

// trackKinds are the kinds: the built-in ones, then the user's (disabled ones included, saying why).
func (s *Service) trackKinds(ctx context.Context) []kindEntry {
	_, kinds := s.registry(ctx)
	return kinds
}

// registry reads (or gives what it read within the TTL of) the tracks and the kinds.
func (s *Service) registry(ctx context.Context) ([]trackEntry, []kindEntry) {
	r := &s.trackReg
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.list != nil && time.Since(r.at) < trackRegistryTTL {
		return r.list, r.kinds
	}
	list, kinds := s.builtinTracks(), builtinKinds()
	if s.trackKindsExt != nil {
		raw, err := s.trackKindsExt.DomainTrackKindsJSON(ctx)
		if err != nil {
			s.logf("track kinds: extensions: %v", err)
		} else {
			var items []struct {
				ID     string                        `json:"id"`
				Title  string                        `json:"title"`
				Config assistantwire.TrackKindConfig `json:"config"`
				// Extension is the extension contributing the kind, as the hub knows it (extmgr/track_kinds.go).
				Extension string `json:"extension"`
			}
			if err := json.Unmarshal(raw, &items); err != nil {
				s.logf("track kinds: extensions: %v", err)
			}
			for _, item := range items {
				if !identity.ValidTrackSlug(item.ID) || slices.Contains(builtinKindOrder, item.ID) {
					s.logf("track kinds: extension kind %q is reserved or invalid", item.ID)
					continue
				}
				cfg, err := normalKindConfig(item.Config)
				k := kindEntry{Slug: item.ID, Title: item.Title, Config: cfg, Extension: true, Owner: item.Extension, Revision: 1}
				if err != nil {
					k.Disabled = kindDisabledInvalid
					s.logf("track kinds: extension kind %q: %v", item.ID, err)
				}
				kinds = append(kinds, k)
			}
		}
	}
	st, ok := s.store().(trackStore)
	if !ok {
		r.list, r.kinds, r.at = list, kinds, time.Now()
		return list, kinds
	}
	if ks, ok := s.store().(trackKindStore); ok {
		rows, err := ks.TrackKinds(ctx)
		if err != nil {
			s.logf("track kinds: read: %v", err)
			if r.list != nil {
				return r.list, r.kinds
			}
			return list, kinds
		}
		for _, row := range rows {
			if row.Builtin || slices.Contains(builtinKindOrder, row.Slug) {
				continue // the built-in ones are the manager's
			}
			k := kindEntry{Slug: row.Slug, Title: row.Title, Revision: row.Revision}
			cfg, err := parseKindConfig(row.Config)
			switch {
			case errors.Is(err, errKindSchema):
				k.Disabled = kindDisabledSchema
			case err != nil:
				s.logf("track kinds: %q is not valid here: %v", row.Slug, err)
				k.Disabled = kindDisabledInvalid
			}
			k.Config = cfg
			kinds = append(kinds, k)
		}
	}
	rows, err := st.Tracks(ctx)
	if err != nil {
		s.logf("tracks: read: %v", err)
		if r.list != nil {
			return r.list, r.kinds
		}
		return list, kinds
	}
	for _, row := range rows {
		if row.Builtin || row.Slug == SpaceHealth {
			continue // the built-in ones are the manager's
		}
		e := trackEntry{Slug: row.Slug, Kind: row.Kind, Title: row.Title, Icon: row.Icon, Description: row.Description,
			DefaultCategories: row.DefaultCategories, Template: row.Template, DirName: row.DirName,
			Archived: row.ArchivedAt != nil, ArchivedAt: row.ArchivedAt, Revision: row.Revision, Override: row.ConfigOverride}
		if e.Kind == "" {
			e.Kind = trackKindSpace // a row from before kinds were told
		}
		if e.DirName == "" {
			e.DirName = s.defaultTrackDir(e.Slug)
		}
		if !identity.ValidTrackSlug(e.Slug) || !slices.Contains(trackTemplates, e.Template) || !validCategories(e.DefaultCategories) ||
			!validTrackDir(e.DirName) {
			s.logf("tracks: %q is not valid here (categories v%d): left out", e.Slug, infoCategoriesVersion)
			continue
		}
		k, ok := kindOf(kinds, e.Kind)
		if !ok || k.Disabled != "" {
			s.logf("tracks: %q is of kind %q, not enabled here: left out", e.Slug, e.Kind)
			continue
		}
		if e.Config, err = effectiveTrackConfig(k.Config, e.Template, e.DefaultCategories, e.Override); err != nil {
			s.logf("tracks: %q: %v: left out", e.Slug, err)
			continue
		}
		list = append(list, e)
	}
	r.list, r.kinds, r.at = list, kinds, time.Now()
	return list, kinds
}

// kindOf is kind slug among kinds.
func kindOf(kinds []kindEntry, slug string) (kindEntry, bool) {
	for _, k := range kinds {
		if k.Slug == slug {
			return k, true
		}
	}
	return kindEntry{}, false
}

// tracksChanged drops what the manager read of the registry, so the next lookup reads it again.
func (s *Service) tracksChanged() {
	r := &s.trackReg
	r.mu.Lock()
	r.list = nil
	r.mu.Unlock()
}

// trackEntryOf is the registered track slug, archived or not.
func (s *Service) trackEntryOf(ctx context.Context, slug string) (trackEntry, bool) {
	if slug == "" {
		return trackEntry{}, false
	}
	for _, e := range s.tracks(ctx) {
		if e.Slug == slug {
			return e, true
		}
	}
	return trackEntry{}, false
}

// trackPolicy is the policy of track space, from what its information carries at least: the main space's (none) has
// every switch off, a registered track's (archived or not) follows its default categories (the health space's, health,
// turns every switch on), and a slug the registry does not know is taken for sensitive (strictPolicy).
func (s *Service) trackPolicy(ctx context.Context, space string) TrackPolicy {
	if space == "" {
		return policyOf(infoUse{Known: true})
	}
	e, ok := s.trackEntryOf(ctx, space)
	if !ok {
		return policyOf(infoUse{SourceTrack: space})
	}
	return policyOf(infoUse{Categories: e.DefaultCategories, Known: true, SourceTrack: space, Rules: e.Config.Usage})
}

// trackKindOf is the kind of track space: the main track's (orchestration) for "", a registered track's own, "" for a
// track the registry does not know.
func (s *Service) trackKindOf(ctx context.Context, space string) string {
	if space == "" {
		return trackKindOrchestration
	}
	if e, ok := s.trackEntryOf(ctx, space); ok {
		return e.Kind
	}
	return ""
}

// trackRules are the usage rules of track space (its effective config's, #13472): none for the main space or a track
// the registry does not know (its information is taken for sensitive anyway).
func (s *Service) trackRules(ctx context.Context, space string) []assistantwire.TrackUsageRule {
	if e, ok := s.trackEntryOf(ctx, space); ok {
		return e.Config.Usage
	}
	return nil
}

// trackKnown: slug is the main space ("") or a registered track that is not archived: what a conversation, a task or
// a list may name.
func (s *Service) trackKnown(ctx context.Context, slug string) bool {
	if slug == "" {
		return true
	}
	e, ok := s.trackEntryOf(ctx, slug)
	return ok && !e.Archived
}

// denyDir is one track directory a node denies the assistant's other conversations (assistant.prepare deny_dirs):
// relative to the machine's home, as assistant.prepare's dir, with its track's slug.
type denyDir struct {
	Dir   string `json:"dir"`
	Track string `json:"track"`
}

// trackDenyDirs are the directories of the registered tracks the assistant's other conversations' Read and Edit are
// denied (DenyMainRead; archived ones too: their files stay), for the node to add to the health spaces it finds itself
// (nodeproto.DenyDirs). The health space is not listed: every node finds every manager's. Never nil: an empty list
// tells the node none is.
func (s *Service) trackDenyDirs(ctx context.Context) []denyDir {
	out := []denyDir{}
	for _, e := range s.tracks(ctx) {
		if e.Builtin || !s.trackPolicy(ctx, e.Slug).DenyMainRead {
			continue
		}
		out = append(out, denyDir{Dir: path.Join(compat.StateDir, e.DirName), Track: e.Slug})
	}
	return out
}
