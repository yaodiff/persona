package hub

import (
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// What an extension's token never sees of the conversations, whatever scopes it was granted (docs/extensions/extensions.md
// "Never shown to extensions"): a space's conversations (the health space's and any other) are not listed (the legacy list, the paged
// list, its totals and counts) and their items, images, info and screen are not read. A conversation the hub does not
// know is not read either: its space cannot be told. The extensions' agent.needs_you and agent.finished are not sent
// of a card or a session they are not shown (notifyws.go).

// spaceSession: s runs in a space, by its own Space or its directory (identity.InSpace counts a folder inside one,
// which only hides more). The hub cannot see the manager's registry of spaces, so any space hides.
func spaceSession(s *proto.Session) bool {
	return s.Space != "" || identity.InSpace(s.Cwd)
}

// extensionToken: r carries an extension's token.
func extensionToken(r *http.Request) bool { return principalOf(r.Context()).Role == roleExtension }

func restrictedAgentData(r *http.Request) bool {
	role := principalOf(r.Context()).Role
	return role == roleExtension || role == roleScoped || role == roleReadOnly
}

// ownerExtension checks the owner's trust verdict from the connection that authenticated the extension and its route.
// A legacy Assistant can confirm owner trust through a snapshot it sent on that same connection.
func (h *Hub) ownerExtension(r *http.Request) bool {
	p := principalOf(r.Context())
	c := p.extAuthConn
	if p.Role != roleExtension || c == nil {
		return false
	}
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	if h.bus.current[p.Manager] != c {
		return false
	}
	select {
	case <-c.peer.Done():
		return false
	default:
	}
	if p.extOwner != extwire.OwnerUnreported {
		return p.extOwner == extwire.OwnerConfirmed
	}
	if slices.Contains(c.presence.Capabilities, assistantwire.CapabilityExtensionInfoAccessScoped) {
		return false
	}
	profile := c.extensionOwnerProfile.Load()
	return profile != nil && slices.Contains(profile.ExtensionOwners, p.Extension)
}

// spacesHidden: r's token is shown none of the tracks' conversations on the read routes: an extension's other than
// the user's own (ownerExtension), or a scoped device's. Controls stay closed to every extension (extensionControllable).
func (h *Hub) spacesHidden(r *http.Request) bool {
	return restrictedAgentData(r) && !h.ownerExtension(r)
}

// withoutSpaceSessions drops the spaces' conversations from list for an extension's token (list itself
// otherwise, and when none is dropped). list is never changed.
func (h *Hub) withoutSpaceSessions(r *http.Request, list []proto.Session) []proto.Session {
	if !h.spacesHidden(r) {
		return list
	}
	for i := range list {
		if !spaceSession(&list[i]) {
			continue
		}
		out := make([]proto.Session, i, len(list)-1)
		copy(out, list[:i])
		for j := i + 1; j < len(list); j++ {
			if !spaceSession(&list[j]) {
				out = append(out, list[j])
			}
		}
		return out
	}
	return list
}

// hiddenFromExtensions: an extension's token is not shown card ix: a private one (a Just me turn's), one of a space
// (its directory or its conversation's), or one whose space cannot be told (no directory, a conversation the hub
// does not list).
func (h *Hub) hiddenFromExtensions(ix proto.Interaction) bool {
	if ix.Private || identity.InSpace(ix.Cwd) {
		return true
	}
	if m := h.localMachine(ix.MachineID); m != nil {
		if s, ok := m.session(ix.SessionID); ok {
			return spaceSession(&s)
		}
	}
	return ix.Cwd == ""
}

// pendingCardHiddenFromExtensions says whether pending card id is one an extension's token may not act on (resolve,
// choose): a private card or one of a space, by the card's own directory or the session it belongs to. False
// too when the id is not pending (answered, withdrawn or unknown), which the action routes handle with their own rules.
// Unlike hiddenFromExtensions it does not fail closed on an unlisted session with no directory: that rule protects
// reads and notifications, where hiding is the safe answer, but actions on such a card keep going to its manager,
// which enforces its own scoping. The action routes answer a hidden card 404 as if it did not exist, the same answer
// the read routes give (#12216).
func (h *Hub) pendingCardHiddenFromExtensions(id string) (proto.Interaction, bool) {
	ix, pending := h.pendingInteraction(id)
	if !pending {
		return proto.Interaction{}, false
	}
	return ix, h.actionCardHiddenFromExtensions(ix)
}

// actionCardHiddenFromExtensions is the definitive "an extension must not act on this card" rule, with no
// cannot-tell branch: a private card, a card whose directory is in a space, or one whose listed session is a space's
// conversation.
func (h *Hub) actionCardHiddenFromExtensions(ix proto.Interaction) bool {
	if ix.Private || identity.InSpace(ix.Cwd) {
		return true
	}
	if m := h.localMachine(ix.MachineID); m != nil {
		if s, ok := m.session(ix.SessionID); ok {
			return spaceSession(&s)
		}
	}
	return false
}

// sessionHiddenFromExtensions: the extensions are not told of session sid of computer machine (agent.finished): one of
// a space, or one the hub does not list, whose space cannot be told. A push about no session is the manager's
// own.
func (h *Hub) sessionHiddenFromExtensions(machine, sid string) bool {
	if sid == "" {
		return false
	}
	if m := h.localMachine(machine); m != nil {
		if s, ok := m.session(sid); ok {
			return spaceSession(&s)
		}
	}
	return true
}

// extensionResume answers whether an extension's token may resume conversation id on computer m (POST
// /api/v1/sessions with resume_session_id), and whether only the computer's own check lets it (checked: the start
// carries resume_outside_health); not ok has written the answer. A conversation the computer lists goes on unless an
// extension may not control it (publicSessionForControl: a space's, one in a folder set to Hide, a deleted
// one): 404, as for one that does not exist. One it does not list (quiet for a week, past its 80 newest, a short
// transcript: a stopped Agent takes no message, so a resume is the only way back to one the extension started) goes,
// unless the owner deleted it, to a node that announces nodeproto.ResumeOutsideHealth, which resumes it only when it
// has it outside the space; an older node would start a new conversation under an id it has nothing of, which the
// manager would take for the one the id names, a health chat's included (#10941, docs/extensions/extensions.md
// "Resuming a chat"): 404. While the hub cannot tell which it is (no list of that computer's sessions yet, which may
// have it, or no word of its node since the hub or the devices manager started) the answer is 503, retryable. The
// caller asks it after every check that holds for any id, so that only this answer depends on the id.
func (h *Hub) extensionResume(w http.ResponseWriter, m *Machine, id string) (checked, ok bool) {
	if _, listed := m.session(id); listed {
		if !h.publicSessionForControl(m, id) {
			writeErr(w, http.StatusNotFound, errDeleteUnknown)
			return false, false
		}
		return false, true
	}
	if _, deleted := h.deletedAt(m.cfg.ID)[id]; deleted {
		writeErr(w, http.StatusNotFound, errDeleteUnknown)
		return false, false
	}
	if m.announces(nodeproto.ResumeOutsideHealth) {
		return true, true
	}
	if !m.hadList() || !m.nodeReported() {
		body := apierr.CodeBody(w, http.StatusServiceUnavailable, codeSessionsNotListed, i18n.Err("hub-http.sessions_not_listed"))
		body["retryable"], body["delivered"] = true, "none"
		w.Header().Set("Retry-After", "5")
		writeErrorBody(w, http.StatusServiceUnavailable, body)
		return false, false
	}
	writeErr(w, http.StatusNotFound, errDeleteUnknown)
	return false, false
}

// codeSessionsNotListed: the hub has had no list of the computer's sessions yet, or no word of its node
// (extensionResume); 503, retryable.
const codeSessionsNotListed = "sessions_not_listed"

// extensionReadable wraps a route of one conversation ({mid}/{sid}): an extension's token is answered 404, as for a
// conversation there is not, when it is a space's conversation or one the hub does not list (unless it was given
// it as a history row lately, sessions_history.go). Anyone else, and an
// unknown machine (the route's own 404), go on to next.
func (h *Hub) extensionReadable(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.spacesHidden(r) {
			if m := h.localMachine(r.PathValue("mid")); m != nil {
				s, ok := m.session(r.PathValue("sid"))
				if !ok && h.historyReads.readable(m.cfg.ID, r.PathValue("sid"), time.Now()) {
					next(w, r) // a history row it was given (sessions_history.go), never a space's one
					return
				}
				if !ok || spaceSession(&s) {
					writeErr(w, http.StatusNotFound, errDeleteUnknown) // "No such session"
					return
				}
			}
		}
		next(w, r)
	}
}

// extensionControllable protects controls before they record receipts or forward
// a request. A history read grant does not authorize changes to that session.
func (h *Hub) extensionControllable(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if restrictedAgentData(r) {
			if m := h.localMachine(r.PathValue("mid")); m != nil && !h.publicSessionForControl(m, r.PathValue("sid")) {
				writeErr(w, http.StatusNotFound, errDeleteUnknown)
				return
			}
		}
		next(w, r)
	}
}

func (h *Hub) publicSessionForControl(m *Machine, id string) bool {
	s, ok := m.session(id)
	return ok && !spaceSession(&s) && !s.HiddenByDirectory() && !s.IsDeleted() &&
		!sessionHiddenByDeletion(s, h.deletedAt(m.cfg.ID))
}
