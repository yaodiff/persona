package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// What an extension may read of the assistant's sensitive information (#13246, scene 6 of docs/assistant/health.md
// "Using information by scene"): the user's own extension (trust owner, such as their memory extension) reads
// everything; a third party's (every extension the user did not mark) reads sensitive information only of the
// categories the user granted it, each one. Only the user changes either, in the app.

// extensionTrustStore keeps the trusts and grants (assistant_extension_trust, assistant_extension_grants).
type extensionTrustStore interface {
	ExtensionTrustOf(ctx context.Context, extension string) (string, error)
	ExtensionTrusts(ctx context.Context) (map[string]db.ExtensionTrust, error)
	SetExtensionTrust(ctx context.Context, extension, trust string, cats []string, by string) error
	ForgetExtensionTrust(ctx context.Context, extension string) error
}

var _ extensionTrustStore = (*db.DB)(nil)

// extensionIsOwner checks the current trust row for a token authenticated by this Assistant's extension manager.
func (s *Service) extensionIsOwner(ctx context.Context, extension string) bool {
	st, ok := s.store().(extensionTrustStore)
	if !ok {
		return false
	}
	trust, err := st.ExtensionTrustOf(ctx, extension)
	if err != nil {
		s.logf("extension trust: read: %v", err)
		return false
	}
	return trust == assistantwire.ExtensionTrustOwner
}

// extensionReaderKey carries the grant of the extension that reads, in a request's or an RPC's context
// (withExtensionReader), read once for the whole request.
type extensionReaderKey struct{}

// withExtensionReader is ctx of a read by extension ext: what it is shown is decided by ext's grant
// (extensionHides), read here once. ext "" is an extension whose id did not come: a third party's with no grant.
func (s *Service) withExtensionReader(ctx context.Context, ext string) context.Context {
	g := extensionGrant{}
	if ext != "" {
		g = s.extensionGrantOf(ctx, ext)
	}
	return context.WithValue(ctx, extensionReaderKey{}, g)
}

// readerGrant is what the extension reading in ctx may read (withExtensionReader): none outside such a read, and
// none for an extension the store cannot tell, as for a third party's with no grant.
func readerGrant(ctx context.Context) extensionGrant {
	g, _ := ctx.Value(extensionReaderKey{}).(extensionGrant)
	return g
}

// extensionGrantOf is extension ext's grant.
func (s *Service) extensionGrantOf(ctx context.Context, ext string) extensionGrant {
	st, ok := s.store().(extensionTrustStore)
	if !ok {
		return extensionGrant{}
	}
	all, err := st.ExtensionTrusts(ctx)
	if err != nil {
		s.logf("extension trust: read: %v", err)
		return extensionGrant{}
	}
	t := all[ext]
	cats, _ := normalCategories(t.Categories)
	return extensionGrant{Owner: t.Trust == assistantwire.ExtensionTrustOwner, Categories: cats}
}

// extensionOwnerCache keeps extensionOwners between writes: the profile is built on every publish, and the owners
// change only when the user sets trust or an extension is forgotten (extensionOwnersChanged).
type extensionOwnerCache struct {
	mu     sync.Mutex
	loaded bool
	gen    uint64 // bumped by each change, so a load that raced a write is not kept
	ids    []string
}

// extensionOwners are the extensions the user marked as their own, sorted (Profile.ExtensionOwners): none when the
// store cannot tell, so the hub keeps hiding every track from every extension. A failed read is not kept: the next
// publish reads again.
func (s *Service) extensionOwners(ctx context.Context) []string {
	st, ok := s.store().(extensionTrustStore)
	if !ok {
		return nil
	}
	c := &s.extOwners
	c.mu.Lock()
	if c.loaded {
		out := slices.Clone(c.ids)
		c.mu.Unlock()
		return out
	}
	gen := c.gen
	c.mu.Unlock()
	all, err := st.ExtensionTrusts(ctx)
	if err != nil {
		s.logf("extension trust: read: %v", err)
		return nil
	}
	var out []string
	for id, t := range all {
		if t.Trust == assistantwire.ExtensionTrustOwner {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	c.mu.Lock()
	if c.gen == gen {
		c.ids, c.loaded = slices.Clone(out), true
	}
	c.mu.Unlock()
	return out
}

// extensionOwnersChanged: a trust was written or forgotten; the next profile reads the owners again.
func (s *Service) extensionOwnersChanged() {
	c := &s.extOwners
	c.mu.Lock()
	c.loaded = false
	c.gen++
	c.mu.Unlock()
}

// forgetExtensionTrust drops what the user let extension ext read: it was removed (assistant.extension_trust_forget),
// and the hub learns the owners again.
func (s *Service) forgetExtensionTrust(ctx context.Context, ext string) error {
	st, ok := s.store().(extensionTrustStore)
	if !ok || ext == "" {
		return nil
	}
	err := st.ForgetExtensionTrust(ctx, ext)
	s.extensionOwnersChanged() // a failed write may still have changed rows: read again
	if err != nil {
		return err
	}
	s.publish(context.WithoutCancel(ctx))
	return nil
}

// trackUse is what everything from track space carries at least (trackFloor), for a decision made by track.
func (s *Service) trackUse(ctx context.Context, space string) infoUse {
	floor, known := s.trackFloor(ctx, space)
	return infoUse{Categories: floor, Known: known, SourceTrack: space, Rules: s.trackRules(ctx, space)}
}

// extensionHides: the extension reading in ctx (withExtensionReader) is not shown what comes from track space (usage,
// sceneExtension, by the track's default categories). Without a grant this is the track's HideFromExtensions.
func (s *Service) extensionHides(ctx context.Context, space string) bool {
	return usage(s.trackUse(ctx, space), sceneExtension, readerGrant(ctx)) == usageDeny
}

// extensionHidesConversation: the extension reading in ctx is not shown conversation c, by its categories as a whole
// (its track's, as c says it, and what it was tagged with; not known while a turn of it waits to be tagged).
func (s *Service) extensionHidesConversation(ctx context.Context, c proto.Session) bool {
	space, known := s.conversationRowSpace(ctx, c)
	if !known {
		return true
	}
	cats, _, ok := s.categoryStateIn(ctx, c.SessionID, space, true)
	info := infoUse{Categories: cats, Known: ok && !s.infoTagWaiting.has(c.SessionID), SourceTrack: space, Rules: s.trackRules(ctx, space)}
	return usage(info, sceneExtension, readerGrant(ctx)) == usageDeny
}

// extensionHidesEntryWith decides whether an extension with grant is hidden from ledger entry e, combining its source
// track's floor and usage rules (base) with the entry's own categories (scene 6, #13548).
func extensionHidesEntryWith(grant extensionGrant, base infoUse, e proto.AssistantEntry) bool {
	cats, catsKnown := normalCategories(append(slices.Clone(base.Categories), e.Category...))
	info := infoUse{
		Categories:  cats,
		Known:       base.Known && catsKnown,
		SourceTrack: base.SourceTrack,
		Rules:       base.Rules,
	}
	return usage(info, sceneExtension, grant) == usageDeny
}

// extensionReaderRequest is r with the reading extension's grant in its context, when an extension's token sent it
// (the hub's headers: a client's own are dropped on the way).
func (s *Service) extensionReaderRequest(r *http.Request) *http.Request {
	if r.Header.Get(RoleHeader) != roleExtension {
		return r
	}
	return r.WithContext(s.withExtensionReader(r.Context(), r.Header.Get(ExtensionHeader)))
}

func (s *Service) infoAccessRoutes() {
	s.handle("GET /api/v1/assistant/extension-trust", s.extensionTrustList)
	s.handle("PUT /api/v1/assistant/extension-trust/{id}", s.extensionTrustSet)
	s.handle("GET /api/v1/assistant/conversations/{id}/categories", s.conversationCategoriesGet)
	s.handle("PUT /api/v1/assistant/conversations/{id}/categories", s.conversationCategoriesSet)
}

// userOnly answers 403 unless r is the user's own (a full-access client): an extension's token, a scoped device or
// an agent never changes who reads what (#13246).
func userOnly(w http.ResponseWriter, r *http.Request) bool {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("only the user changes this, in the app"))
		return false
	}
	return true
}

func (s *Service) extensionTrustList(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	st, ok := s.store().(extensionTrustStore)
	if !ok {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("extension trust store unavailable"))
		return
	}
	all, err := st.ExtensionTrusts(r.Context())
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	out := []assistantwire.ExtensionTrust{}
	for _, t := range all {
		trust := t.Trust
		if trust != assistantwire.ExtensionTrustOwner {
			trust = assistantwire.ExtensionTrustThirdParty
		}
		cats, _ := normalCategories(t.Categories)
		if cats == nil {
			cats = []string{}
		}
		out = append(out, assistantwire.ExtensionTrust{Extension: t.Extension, Trust: trust, Categories: cats})
	}
	slices.SortFunc(out, func(a, b assistantwire.ExtensionTrust) int { return cmp.Compare(a.Extension, b.Extension) })
	apierr.WriteJSON(w, map[string]any{"items": out})
}

func (s *Service) extensionTrustSet(w http.ResponseWriter, r *http.Request) {
	if !userOnly(w, r) {
		return
	}
	id := r.PathValue("id")
	if id == "" || len(id) > 200 {
		apierr.Write(w, http.StatusBadRequest, errors.New("invalid extension id"))
		return
	}
	var in assistantwire.ExtensionTrust
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	if in.Trust != assistantwire.ExtensionTrustOwner && in.Trust != assistantwire.ExtensionTrustThirdParty {
		apierr.Write(w, http.StatusBadRequest, errors.New("trust must be owner or third_party"))
		return
	}
	cats, known := normalCategories(in.Categories)
	if !known {
		apierr.Write(w, http.StatusBadRequest, errors.New("unknown category"))
		return
	}
	st, ok := s.store().(extensionTrustStore)
	if !ok {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("extension trust store unavailable"))
		return
	}
	err := st.SetExtensionTrust(r.Context(), id, in.Trust, cats, infoTaggedByUser)
	s.extensionOwnersChanged() // a failed write may still have changed rows: read again
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	s.publish(context.WithoutCancel(r.Context())) // publish Profile.ExtensionOwners for older hubs
	if cats == nil {
		cats = []string{}
	}
	apierr.WriteJSON(w, assistantwire.ExtensionTrust{Extension: id, Trust: in.Trust, Categories: cats})
}

func (s *Service) conversationCategoriesGet(w http.ResponseWriter, r *http.Request) {
	if !spacesWriter(w, r) {
		return
	}
	id := r.PathValue("id")
	if space, _ := s.conversationSpace(r.Context(), id); space != "" && r.Header.Get(RoleHeader) == roleExtension && s.extensionHides(r.Context(), space) {
		apierr.Write(w, http.StatusNotFound, errors.New("conversation not found"))
		return
	}
	if !validSessionID(id) {
		apierr.Write(w, http.StatusBadRequest, errors.New("invalid conversation id"))
		return
	}
	out, err := s.conversationCategoryView(r.Context(), id)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	apierr.WriteJSON(w, out)
}

func (s *Service) conversationCategoriesSet(w http.ResponseWriter, r *http.Request) {
	if !spacesWriter(w, r) {
		return
	}
	id := r.PathValue("id")
	if space, _ := s.conversationSpace(r.Context(), id); space != "" && r.Header.Get(RoleHeader) == roleExtension && s.extensionHides(r.Context(), space) {
		apierr.Write(w, http.StatusNotFound, errors.New("conversation not found"))
		return
	}
	if !validSessionID(id) {
		apierr.Write(w, http.StatusBadRequest, errors.New("invalid conversation id"))
		return
	}
	var in assistantwire.ConversationCategoriesChange
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	add, okAdd := normalCategories(in.Add)
	remove, okRemove := normalCategories(in.Remove)
	if !okAdd || !okRemove {
		apierr.Write(w, http.StatusBadRequest, errors.New("unknown category"))
		return
	}
	space, _ := s.conversationSpace(r.Context(), id)
	if floor, _ := s.trackFloor(r.Context(), space); slices.ContainsFunc(remove, func(c string) bool { return slices.Contains(floor, c) }) {
		apierr.Write(w, http.StatusConflict, errors.New("a track's own categories cannot be taken away"))
		return
	}
	st, ok := s.store().(infoCategoryStore)
	if !ok {
		apierr.Write(w, http.StatusServiceUnavailable, errors.New("category store unavailable"))
		return
	}
	if err := st.AddInfoCategories(r.Context(), db.InfoCategoryConversation, id, add, infoTaggedByUser, ""); err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	if err := st.RemoveInfoCategories(r.Context(), db.InfoCategoryConversation, id, remove); err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	out, err := s.conversationCategoryView(r.Context(), id)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	apierr.WriteJSON(w, out)
}

// conversationCategoryView is conversation sid's categories as the app shows them.
func (s *Service) conversationCategoryView(ctx context.Context, sid string) (assistantwire.ConversationCategories, error) {
	out := assistantwire.ConversationCategories{Categories: []string{}, Floor: []string{}, Removed: []string{}}
	space, _ := s.conversationSpace(ctx, sid)
	floor, _ := s.trackFloor(ctx, space)
	out.Floor = append(out.Floor, floor...)
	cats := slices.Clone(floor)
	if st, ok := s.store().(infoCategoryStore); ok {
		rows, err := st.InfoCategories(ctx, db.InfoCategoryConversation, []string{sid})
		if err != nil {
			return out, err
		}
		for _, r := range rows[sid] {
			if r.RemovedByUser {
				out.Removed = append(out.Removed, r.Category)
			} else {
				cats = append(cats, r.Category)
			}
		}
	}
	cats, _ = normalCategories(cats)
	out.Categories = append(out.Categories, cats...)
	out.Removed, _ = normalCategories(out.Removed)
	if out.Removed == nil {
		out.Removed = []string{}
	}
	return out, nil
}
