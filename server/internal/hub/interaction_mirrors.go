package hub

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// featureApprovalMirrors is the client capability for receiving display copies.
// A client that only knows the authoritative model keeps its pre-mirror counts.
const featureApprovalMirrors = "approval_mirrors"

const displayMirrorAPIPrefix = "/api/v1/interactions/"

func reservedDisplayOriginal(ix proto.Interaction) bool {
	return ix.MirrorOf == "" && strings.HasPrefix(ix.ID, proto.ApprovalMirrorIDPrefix)
}

// validDisplayMirror rejects orphaned, nested, private or same-conversation
// copies. A mirror is display state only and never substitutes for its origin.
func validDisplayMirror(origin, mirror proto.Interaction) bool {
	return mirror.ID != "" && mirror.ID == proto.ApprovalMirrorID(origin.ID, origin.CreatedAt) && mirror.MirrorOf == origin.ID && mirror.ID != origin.ID &&
		!reservedDisplayOriginal(origin) && origin.MirrorOf == "" && origin.MirrorMain && !mirror.MirrorMain && origin.Kind == proto.IxPermission && !origin.Private && !mirror.Private &&
		origin.Manager == mirror.Manager &&
		(origin.MachineID != mirror.MachineID || origin.SessionID != mirror.SessionID)
}

// refreshedDisplayMirror applies an original card's new deadline, text and
// choices to an existing copy without moving that copy out of its destination.
func refreshedDisplayMirror(origin, mirror proto.Interaction) proto.Interaction {
	out := origin
	out.ID = mirror.ID
	out.MirrorOf = mirror.MirrorOf
	out.MirrorMain = false
	out.MachineID = mirror.MachineID
	out.SessionID = mirror.SessionID
	out.Manager = mirror.Manager
	out.FoldedUnder, out.FoldedAs, out.FoldedSide = mirror.FoldedUnder, mirror.FoldedAs, mirror.FoldedSide
	out.Muted = mirror.Muted
	return out
}

// withNormalizedDisplayMirror makes the authoritative target part of the
// request before an operation receipt hashes its path, not just before relay.
func (h *Hub) withNormalizedDisplayMirror(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.normalizeDisplayMirror(r)
		h.normalizeRecordedDisplayMirror(r)
		next(w, r)
	}
}

// setDisplayMirrorOrigin preserves both forms of an opaque authoritative ID:
// Path is decoded for gates and receipt identity, RawPath for the manager relay.
func setDisplayMirrorOrigin(r *http.Request, origin, suffix string) {
	r.SetPathValue("id", origin)
	r.URL.Path = displayMirrorAPIPrefix + origin + suffix
	r.URL.RawPath = displayMirrorAPIPrefix + url.PathEscape(origin) + suffix
	if r.URL.RawPath == r.URL.Path {
		r.URL.RawPath = ""
	}
}

// normalizeRecordedDisplayMirror resolves a stale copy only through the
// operation's durable receipt. The recorded path must name the same action and
// hash back to this exact display ID; an arbitrary alias cannot redirect a retry.
// For an original-ID retry, the same receipt restores its occurrence without
// changing the path, so mirror-then-original retries keep one digest.
func (h *Hub) normalizeRecordedDisplayMirror(r *http.Request) {
	id := r.PathValue("id")
	if h.store == nil {
		return
	}
	opID := strings.ToLower(r.Header.Get("X-Persona-Op-Id"))
	if opID == "" {
		return
	}
	op, err := h.store.ClientOp(r.Context(), opID)
	if err != nil || op == nil {
		return
	}
	var target struct {
		Path       string `json:"path"`
		Occurrence string `json:"occurrence"`
	}
	if json.Unmarshal(op.Target, &target) != nil {
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, displayMirrorAPIPrefix+id)
	if suffix == r.URL.Path || suffix == "" || !strings.HasPrefix(target.Path, displayMirrorAPIPrefix) || !strings.HasSuffix(target.Path, suffix) {
		return
	}
	setOccurrence := func() {
		if target.Occurrence != "" {
			r.Header.Set(assistantwire.InteractionCreatedAtHeader, target.Occurrence)
		} else {
			// A receipt written before occurrence pinning has no such target field;
			// do not add it to the retry's digest during a rolling upgrade.
			r.Header.Del(assistantwire.InteractionCreatedAtHeader)
		}
	}
	if !strings.HasPrefix(id, proto.ApprovalMirrorIDPrefix) {
		if target.Path == r.URL.Path {
			setOccurrence()
		}
		return
	}
	origin := strings.TrimSuffix(strings.TrimPrefix(target.Path, displayMirrorAPIPrefix), suffix)
	if origin == "" || !proto.ApprovalMirrorReceiptID(origin, id) || target.Path != displayMirrorAPIPrefix+origin+suffix {
		return
	}
	setDisplayMirrorOrigin(r, origin, suffix)
	setOccurrence()
}

// normalizeDisplayMirror rewrites an action on a display copy before any owner,
// scoped-token, extension, handoff or clarification gate examines its target.
// A stale or invalid copy is left untouched and therefore cannot reach the
// authoritative operation through this path.
func (h *Hub) normalizeDisplayMirror(r *http.Request) {
	id := r.PathValue("id")
	// A current client supplies the occurrence it displayed. Preserve a stale or
	// malformed value so it cannot be rebound to a same-ID replacement. Older
	// clients omit it and retain the hub-observed rolling-upgrade behavior.
	supplied := r.Header.Get(assistantwire.InteractionCreatedAtHeader)
	matches := func(ix proto.Interaction) bool {
		if supplied == "" {
			return true
		}
		at, err := time.Parse(time.RFC3339Nano, supplied)
		return err == nil && ix.CreatedAt.Equal(at)
	}
	f := &h.reported
	f.mu.Lock()
	defer f.mu.Unlock()
	mirror, ok := f.interactions[id]
	if ok && mirror.MirrorOf == "" {
		if matches(mirror) {
			r.Header.Set(assistantwire.InteractionCreatedAtHeader, mirror.CreatedAt.UTC().Format(time.RFC3339Nano))
		}
		return
	}
	if !ok {
		return
	}
	origin, ok := f.interactions[mirror.MirrorOf]
	if ok && matches(origin) && validDisplayMirror(origin, mirror) {
		suffix := strings.TrimPrefix(r.URL.Path, displayMirrorAPIPrefix+id)
		if suffix != r.URL.Path && suffix != "" {
			setDisplayMirrorOrigin(r, origin.ID, suffix)
			r.Header.Set(assistantwire.InteractionCreatedAtHeader, origin.CreatedAt.UTC().Format(time.RFC3339Nano))
		}
	}
}
