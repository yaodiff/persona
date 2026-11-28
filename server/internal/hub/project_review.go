package hub

import (
	"context"
	"errors"
	"net/http"
	"path"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

type projectReviewOwnerStore interface {
	SetProjectReviewAppInputs(context.Context, string, bool) error
	RecordProjectReviewOwnerInput(context.Context, db.ProjectReviewOwnerRelay) error
	SettleProjectReviewOwner(context.Context, db.ProjectReviewOwnerRelay) error
	PendingProjectReviewOwnersOf(context.Context, []string, int) ([]db.ProjectReviewOwnerRelay, error)
	AckProjectReviewOwner(context.Context, db.ProjectReviewOwnerRelay) error
}

// viaMarked: the app's "via" says it sent the message through Siri, Shortcuts, an App Intent or the share sheet. Such
// a message is never the owner's own words (assistant/start.go messageParams); a mark of any kind but "" counts.
func viaMarked(via any) bool {
	text, isText := via.(string)
	return via != nil && !(isText && text == "")
}

// Only the authenticated device message route knows who supplied the input.
// Do not infer this from transcript roles, body metadata or forward text.
// started is the session a start just returned, for when the machine has not listed it yet.
// indirect: the request carried a "via" mark (viaMarked); it is delivered as usual but never counts as owner input.
func (h *Hub) projectReviewAppSource(r *http.Request, machine, session, id string, started *proto.Session, indirect bool) (db.ProjectReviewOwnerRelay, bool) {
	if _, trusted := r.Context().Value(trustedSubmissionKey{}).(devicewire.Submission); !trusted || indirect || session == "" || id == "" {
		return db.ProjectReviewOwnerRelay{}, false
	}
	unscoped := h.isMainOn(machine, session)
	if m := h.machineByID(machine); m != nil {
		row, ok := m.session(session)
		if !ok && started != nil && started.SessionID == session {
			row, ok = *started, true
		}
		if ok {
			if row.Space != "" || row.Origin == "meeting" || projectReviewExcludedCwd(row.Cwd) || m.isGuestCwd(row.Cwd) {
				return db.ProjectReviewOwnerRelay{}, false
			}
			unscoped = unscoped || row.Assistant || m.isAssistant(row)
		}
	}
	return db.ProjectReviewOwnerRelay{MessageID: id, Manager: h.sessionOwner(machine, session), Machine: machine, Session: session,
		Unscoped: unscoped}, true
}

// projectReviewExcludedCwd: a conversation working in cwd is never project review input, whatever its row says: the
// health space (or a folder in it) and a meeting room's. A start is classified by where it runs before its computer
// lists it with the metadata that would say so.
func projectReviewExcludedCwd(cwd string) bool {
	return identity.InHealthSpace(cwd) || isMeetingRoomCwd(cwd)
}

// isGuestCwd: cwd is where m's guest conversations run (<state dir>/guest, assistant/guest.go guestDir): other
// people's words, never the owner's project input.
func (m *Machine) isGuestCwd(cwd string) bool {
	clean := path.Clean(cwd)
	if cwd == "" || path.Base(clean) != "guest" {
		return false
	}
	if compat.IsStateDir(path.Base(path.Dir(clean))) {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info.DataDir != "" && clean == path.Join(m.info.DataDir, "guest")
}

func (h *Hub) settleProjectReviewOwner(ctx context.Context, r *http.Request, machine, session, id, outcome string, indirect bool) (bool, error) {
	if outcome != sendDelivered && outcome != sendLater {
		return false, nil
	}
	p, eligible := h.projectReviewAppSource(r, machine, session, id, nil, indirect)
	store, ok := h.store.(projectReviewOwnerStore)
	if !ok || !eligible {
		return false, nil
	}
	p.SendOutcome = outcome
	if err := store.SettleProjectReviewOwner(ctx, p); err != nil {
		return true, err
	}
	h.pokeProjectReviewOwners()
	return true, nil
}

// projectReviewRelay wakes the relay loop when there may be something for it to deliver.
type projectReviewRelay struct {
	once sync.Once
	wake chan struct{}
}

func (r *projectReviewRelay) wakeup() chan struct{} {
	r.once.Do(func() { r.wake = make(chan struct{}, 1) })
	return r.wake
}

// poke has the relay read its table again: a row was queued, or a manager that takes them said hello.
func (h *Hub) pokeProjectReviewOwners() {
	select {
	case h.projectReview.wakeup() <- struct{}{}:
	default: // a read is already due
	}
}

const (
	// projectReviewRelayBatch bounds one read of the relay; a full batch is followed by the next at once.
	projectReviewRelayBatch = 200
	// projectReviewRelayRetry is how soon a manager whose call failed gets its rows again.
	projectReviewRelayRetry = 3 * time.Second
)

// The loop only retries persisted transport. All sources are native ingress or lifecycle observations. It never scans
// conversations for owner input. It reads the relay when there may be something to deliver (at start for what a
// restart left, when a row is queued, when a manager that takes them says hello) and polls only while a call to a
// connected manager is failing: with project review off, as it is by default, it costs no database work. Rows of a
// manager that is away stay queued, unread, until it is back (its hello pokes the loop).
func (h *Hub) projectReviewOwnerLoop(ctx context.Context) {
	timer := time.NewTimer(0) // what a restart left
	defer timer.Stop()
	wake := h.projectReview.wakeup()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-wake:
		}
		timer.Stop()
		retry, err := h.relayProjectReviewOwners(ctx)
		if err != nil && ctx.Err() == nil {
			h.logf("project review: owner input relay: %v", err)
			retry = projectReviewRelayRetry
		}
		if retry > 0 {
			timer.Reset(retry)
		}
	}
}

// managersThatCan lists the connected managers that said they have capability.
func (h *Hub) managersThatCan(capability string) []string {
	h.bus.mu.Lock()
	defer h.bus.mu.Unlock()
	var ids []string
	for id, c := range h.bus.current {
		if c != nil && slices.Contains(c.presence.Capabilities, capability) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func (h *Hub) deliverProjectReviewOwners(ctx context.Context) error {
	_, err := h.relayProjectReviewOwners(ctx)
	return err
}

// relayProjectReviewOwners delivers one batch of the rows of the connected managers that take them; retry is how soon
// to read again (0: not until a poke).
func (h *Hub) relayProjectReviewOwners(ctx context.Context) (retry time.Duration, err error) {
	store, ok := h.store.(projectReviewOwnerStore)
	if !ok {
		return 0, nil
	}
	managers := h.managersThatCan(assistantwire.CapabilityProjectReview)
	if len(managers) == 0 {
		return 0, nil // nothing read: a manager's hello pokes the loop
	}
	list, err := store.PendingProjectReviewOwnersOf(ctx, managers, projectReviewRelayBatch)
	if err != nil {
		return 0, err
	}
	failed := make(map[string]bool)
	for _, row := range list {
		if failed[row.Manager] || !h.managerCan(row.Manager, assistantwire.CapabilityProjectReview) {
			continue
		}
		if h.sessionOwner(row.Machine, row.Session) == row.Manager {
			bound, cancel := context.WithTimeout(ctx, time.Second)
			p := assistantwire.ProjectReviewOwnerInput{
				SessionRef: assistantwire.SessionRef{MachineID: row.Machine, SessionID: row.Session},
				MessageID:  assistantwire.ProjectReviewMessageID(row.MessageID), At: row.At, Unscoped: row.Unscoped,
			}
			_, err := callAssistant(bound, h.asstOf(row.Manager), assistantwire.ProjectReviewOwnerInputMethod, p)
			cancel()
			if err != nil {
				failed[row.Manager] = true
				continue
			}
		}
		if err := store.AckProjectReviewOwner(ctx, row); err != nil {
			return 0, err
		}
	}
	switch {
	case len(failed) > 0:
		return projectReviewRelayRetry, nil
	case len(list) == projectReviewRelayBatch:
		h.pokeProjectReviewOwners() // the next batch, at once
	}
	return 0, nil
}

func (h *Hub) setProjectReviewAppInputs(ctx context.Context, manager string, enabled bool) error {
	store, ok := h.store.(projectReviewOwnerStore)
	if !ok {
		if enabled {
			return errors.New("project review owner relay requires the hub's Postgres store")
		}
		return nil
	}
	return store.SetProjectReviewAppInputs(ctx, manager, enabled)
}

func (h *Hub) recordAcceptedProjectReviewInput(ctx context.Context, r *http.Request, machine, session, id string, started *proto.Session, indirect bool) {
	p, eligible := h.projectReviewAppSource(r, machine, session, id, started, indirect)
	if store, ok := h.store.(projectReviewOwnerStore); ok && eligible {
		bound, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := store.RecordProjectReviewOwnerInput(bound, p); err != nil {
			h.logf("project review: record accepted app input: %v", err)
			return
		}
		h.pokeProjectReviewOwners()
	}
}
