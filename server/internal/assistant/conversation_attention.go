package assistant

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Only the current main conversation's latest completed silent supervision
// round changes unread state. The audit transcript and full history stay intact.
const (
	conversationAttentionKey  = "assistant.conversation_attention.v2"
	conversationAttentionScan = 4
)

type conversationAttentionEntry struct {
	Rev        int64     `json:"rev"`
	Silent     bool      `json:"silent"`
	HasVisible bool      `json:"has_visible,omitempty"`
	VisibleAt  time.Time `json:"visible_at,omitzero"`
	Failed     bool      `json:"failed,omitempty"`
}

type conversationAttentionBook struct {
	mu      sync.Mutex
	loaded  bool
	entries map[string]conversationAttentionEntry

	routeOnce    sync.Once
	routeMain    string
	routeMachine string

	// scans are the classifications refreshMainConversationAttention runs off the node's read loop. They have a lock
	// of their own: the list handler takes it on that loop, and mu is held across a store write.
	scans attentionScans
}

// attentionScan is one revision of a conversation to classify.
type attentionScan struct {
	machine, session string
	rev              int64
}

// attentionScans runs at most one classification of each conversation at a time, for the newest revision asked for: a
// list that arrives while one runs only moves its target, and the running one takes it up when it is done. settled
// is the newest revision whose verdict is cached, so a list that changes nothing starts nothing.
type attentionScans struct {
	mu      sync.Mutex
	next    map[string]attentionScan // by conversation (conversationAttentionCacheKey)
	running map[string]bool
	settled map[string]int64
}

// want asks for scan; true: no classification of its conversation runs, so the caller starts one (run).
func (a *attentionScans) want(key string, scan attentionScan) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settled[key] == scan.rev {
		return false
	}
	if a.next == nil {
		a.next, a.running, a.settled = map[string]attentionScan{}, map[string]bool{}, map[string]int64{}
	}
	a.next[key] = scan
	if a.running[key] {
		return false
	}
	a.running[key] = true
	return true
}

// take is the revision to classify next; false: none, and the conversation's run ends.
func (a *attentionScans) take(key string) (attentionScan, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	scan, ok := a.next[key]
	if !ok {
		delete(a.running, key)
		return attentionScan{}, false
	}
	delete(a.next, key)
	return scan, true
}

// settle records that rev's verdict is cached.
func (a *attentionScans) settle(key string, rev int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.settled == nil {
		a.settled = map[string]int64{}
	}
	a.settled[key] = rev
}

// conversationAttentionCacheKey is safe as a Postgres JSONB object key and collision-free even if a legacy
// machine or session ID contains '|': each side is percent-escaped and joined with '|' (matching
// supervise_background.go's '|' convention). The former NUL separator caused Postgres JSONB writes to fail
// with SQLSTATE 22P05; reads and saves still accept and migrate that legacy encoding.
func conversationAttentionCacheKey(machine, session string) string {
	return url.PathEscape(machine) + "|" + url.PathEscape(session)
}

func conversationAttentionLegacyKey(machine, session string) string {
	return machine + "\x00" + session
}

func conversationAttentionHasNULKey(entries map[string]conversationAttentionEntry) bool {
	for k := range entries {
		if strings.ContainsRune(k, '\x00') {
			return true
		}
	}
	return false
}

// normalizeConversationAttentionEntries rewrites any legacy NUL-delimited keys to the JSONB-safe form so
// loaded state remains readable and subsequent PutSetting writes never emit \u0000.
func normalizeConversationAttentionEntries(raw map[string]conversationAttentionEntry) map[string]conversationAttentionEntry {
	out := make(map[string]conversationAttentionEntry, len(raw))
	for k, v := range raw {
		if !strings.ContainsRune(k, '\x00') {
			out[k] = v
		}
	}
	for k, v := range raw {
		machine, session, ok := strings.Cut(k, "\x00")
		if !ok {
			continue
		}
		migrated := conversationAttentionCacheKey(machine, session)
		if existing, exists := out[migrated]; !exists || v.Rev > existing.Rev {
			out[migrated] = v
		}
	}
	return out
}

func (s *Service) loadConversationAttentionLocked(ctx context.Context) bool {
	b := &s.convAttention
	if b.loaded {
		return true
	}
	var loaded map[string]conversationAttentionEntry
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, conversationAttentionKey, &loaded); err != nil {
			s.logf("load conversation attention: %v", err)
			return false
		}
	}
	b.entries = normalizeConversationAttentionEntries(loaded)
	b.loaded = true
	return true
}

// mainConversationRoute reads the settings only before the first cached profile.
// Once a profile exists, the caller uses that fresher snapshot instead.
func (s *Service) mainConversationRoute() (string, string) {
	b := &s.convAttention
	b.routeOnce.Do(func() {
		cfg := s.settings(context.Background())
		b.routeMain, b.routeMachine = cfg.Main, cfg.mainRouteMachine()
	})
	return b.routeMain, b.routeMachine
}

func (s *Service) conversationAttentionCached(machine, session string, rev int64) (conversationAttentionEntry, bool) {
	if rev == 0 {
		return conversationAttentionEntry{}, false
	}
	b := &s.convAttention
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadConversationAttentionLocked(context.Background()) {
		return conversationAttentionEntry{}, false
	}
	e, ok := b.entries[conversationAttentionCacheKey(machine, session)]
	if !ok || e.Rev != rev {
		if legacy, legacyOK := b.entries[conversationAttentionLegacyKey(machine, session)]; legacyOK && legacy.Rev == rev {
			return legacy, true
		}
	}
	return e, ok && e.Rev == rev
}

func (s *Service) putConversationAttention(ctx context.Context, machine, session string, entry conversationAttentionEntry) {
	if entry.Rev == 0 {
		return
	}
	b := &s.convAttention
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadConversationAttentionLocked(ctx) {
		return
	}
	if b.entries == nil || conversationAttentionHasNULKey(b.entries) {
		b.entries = normalizeConversationAttentionEntries(b.entries)
	}
	b.entries[conversationAttentionCacheKey(machine, session)] = entry
	if st := s.store(); st != nil {
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := st.PutSetting(writeCtx, conversationAttentionKey, b.entries); err != nil {
			s.logf("save conversation attention: %v", err)
		}
	}
}

// failConversationAttention negatively caches one failed revision. The result
// stays unread, but a list refresh does not repeat the same failing RPC.
func (s *Service) failConversationAttention(machine, session string, rev int64) (conversationAttentionEntry, bool) {
	if rev != 0 {
		s.putConversationAttention(context.Background(), machine, session, conversationAttentionEntry{Rev: rev, Failed: true})
	}
	return conversationAttentionEntry{}, false
}

// finishConversationAttention caches only a revision the node still lists as
// the same completed turn. Input or a promotion observed during a paged read
// invalidates that read instead of letting its silent verdict hide new work.
func (s *Service) finishConversationAttention(machine, session string, rev int64, entry conversationAttentionEntry) (conversationAttentionEntry, bool) {
	if s.devs != nil {
		current, ok := s.nodeSession(machine, session)
		if !ok || current.Rev != rev || current.Starting || (current.Status != proto.StatusDone && current.Status != proto.StatusIdle) {
			return s.failConversationAttention(machine, session, rev)
		}
	}
	s.putConversationAttention(context.Background(), machine, session, entry)
	return entry, true
}

// mainConversationAttention classifies one revision. Older pages are read only
// while the newest round is silent and an earlier visible round has not been
// found. A failed or incomplete scan fails open as unread and is negatively
// cached only until the revision changes, never hidden as a silent success.
func (s *Service) mainConversationAttention(ctx context.Context, machine, session string, rev int64) (conversationAttentionEntry, bool) {
	if rev == 0 { // without a revision, a cached silent verdict could outlive a later genuine input
		return conversationAttentionEntry{}, false
	}
	if cached, ok := s.conversationAttentionCached(machine, session, rev); ok {
		if cached.Failed {
			return conversationAttentionEntry{}, false
		}
		return cached, true
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var all []proto.Item
	earliest := 0
	for range conversationAttentionScan {
		params := map[string]any{"session_id": session, "turns": 8, "lazy_images": true, "fold_work": true}
		if earliest > 0 {
			params["before_seq"] = earliest
		}
		var page struct {
			Items      []proto.Item `json:"items"`
			MoreBefore bool         `json:"more_before"`
			More       bool         `json:"more"`
			HasMore    bool         `json:"has_more"`
		}
		if err := s.call(ctx, machine, DeviceSessionItems, params, &page); err != nil {
			return s.failConversationAttention(machine, session, rev)
		}
		if len(page.Items) == 0 && len(all) == 0 {
			return s.failConversationAttention(machine, session, rev)
		}
		if len(page.Items) > 0 && page.Items[0].Seq == earliest && len(all) > 0 {
			return s.failConversationAttention(machine, session, rev) // no paging progress
		}
		all = append(append([]proto.Item(nil), page.Items...), all...)
		rounds := transcript.SupervisionRoundVisibilities(all)
		silentLatest := false
		if len(rounds) == 0 {
			if len(page.Items) == 0 || !(page.MoreBefore || page.More || page.HasMore || earliest > 0) {
				return s.failConversationAttention(machine, session, rev)
			}
		} else {
			latest := rounds[len(rounds)-1]
			if latest.Silent && !latest.Closed {
				// Open is a state, not a failed RPC: it can become closed on
				// the next list with the same revision and must remain re-read.
				return conversationAttentionEntry{}, false
			}
			if !latest.Silent {
				entry := conversationAttentionEntry{Rev: rev, HasVisible: true, VisibleAt: latest.End}
				return s.finishConversationAttention(machine, session, rev, entry)
			}
			silentLatest = true
			for i := len(rounds) - 2; i >= 0; i-- {
				if !rounds[i].Silent {
					entry := conversationAttentionEntry{Rev: rev, Silent: true, HasVisible: true, VisibleAt: rounds[i].End}
					return s.finishConversationAttention(machine, session, rev, entry)
				}
			}
		}
		firstSeq := 0
		if len(all) > 0 {
			firstSeq = all[0].Seq
		}
		more := page.MoreBefore || page.More || page.HasMore
		if !more || firstSeq <= 1 && firstSeq != 0 {
			if !silentLatest {
				return s.failConversationAttention(machine, session, rev)
			}
			entry := conversationAttentionEntry{Rev: rev, Silent: true}
			return s.finishConversationAttention(machine, session, rev, entry)
		}
		if len(page.Items) == 0 {
			return s.failConversationAttention(machine, session, rev) // the page said more history existed but made no progress
		}
		earliest = page.Items[0].Seq
		if earliest <= 0 {
			return s.failConversationAttention(machine, session, rev)
		}
	}
	return s.failConversationAttention(machine, session, rev)
}

// refreshMainConversationAttention computes one newly seen revision once, not
// once per inventory decoration, so the inventory only reads the resulting
// classification. setSessions calls it after a list arrives, on the read loop
// of the node connection the list came in on, and the scan reads the
// conversation from that same node: only that loop reads the answer. Waited for
// there, the read ran into its 15 s deadline after every finished main-chat
// turn and held every reply and event of the node meanwhile (#16214). So the
// scan runs on a goroutine of its own, and the machine's list is published
// again once its verdict is in.
func compactionRunning(c *proto.Compaction) bool {
	return c != nil && c.State == proto.CompactionRunning
}

func (s *Service) refreshMainConversationAttention(sess proto.Session, mainSession, mainMachine string) {
	if sess.Rev == 0 || sess.Starting || compactionRunning(sess.Compaction) || (sess.Status != proto.StatusDone && sess.Status != proto.StatusIdle) {
		return
	}
	if sess.SessionID != mainSession || sess.MachineID != mainMachine {
		return
	}
	key := conversationAttentionCacheKey(sess.MachineID, sess.SessionID)
	if s.convAttention.scans.want(key, attentionScan{machine: sess.MachineID, session: sess.SessionID, rev: sess.Rev}) {
		go s.runConversationAttention(key)
	}
}

// runConversationAttention classifies the revisions refreshMainConversationAttention asks for, the newest each time,
// until none is left. A cached verdict settles its revision; an open round does not, so the next list reads it again.
// The scan bounds itself (15 s) as it did on the read loop; one still in flight when the manager's run ends can reach
// the closed store once, within those 15 s.
func (s *Service) runConversationAttention(key string) {
	for {
		scan, ok := s.convAttention.scans.take(key)
		if !ok {
			return
		}
		if _, cached := s.conversationAttentionCached(scan.machine, scan.session, scan.rev); !cached {
			if _, ok := s.mainConversationAttention(context.Background(), scan.machine, scan.session, scan.rev); ok {
				s.publishSessions(scan.machine) // the row reads the verdict (applyMainConversationAttention)
			}
		}
		if _, cached := s.conversationAttentionCached(scan.machine, scan.session, scan.rev); cached {
			s.convAttention.scans.settle(key, scan.rev)
		}
	}
}

func mainConversationAttentionCandidate(assistant bool, status, state string, starting bool, pending int) bool {
	return assistant && status == proto.StatusDone && !starting &&
		(state == proto.StateUnread || state == proto.StateIdle) && pending == 0
}

// applyMainConversationAttention reads only the classification already computed
// by setSessions. It never invokes the transcript scanner on the inventory's
// hot path; a cache miss stays unread rather than guessing.
func (s *Service) applyMainConversationAttention(row *proto.Session, mainSession, mainMachine string) {
	if row.SessionID != mainSession || row.MachineID != mainMachine {
		return
	}
	entry, ok := s.conversationAttentionCached(row.MachineID, row.SessionID, row.Rev)
	if !ok || !entry.Silent {
		return
	}
	if entry.HasVisible && (row.SeenAt.IsZero() || entry.VisibleAt.IsZero() || row.SeenAt.Before(entry.VisibleAt.Add(-2*time.Second))) {
		row.State = proto.StateUnread
		return
	}
	row.State = proto.StateIdle
}

// decorateMainConversationAttention preserves working and needs-you first. A
// silent round clears only its own unread mark; an earlier visible round remains
// unread until seen_at reaches that round's end.
func (s *Service) decorateMainConversationAttention(row *proto.Session, cfg Settings) {
	s.decorateMainConversationAttentionForRoute(row, cfg.Main, cfg.mainRouteMachine())
}

func (s *Service) decorateMainConversationAttentionForRoute(row *proto.Session, mainSession, mainMachine string) {
	if compactionRunning(row.Compaction) || !mainConversationAttentionCandidate(row.Assistant, row.Status, row.State, row.Starting, row.PendingInteractions) || row.SessionID != mainSession || row.MachineID != mainMachine {
		return
	}
	row.State = s.mainConversationAttentionState(row.MachineID, row.SessionID, row.State, row.Rev, row.SeenAt)
}

// mainConversationAttentionState takes only the fields the classification
// needs. In particular, the fleet inventory's row storage must not escape into
// the scanner's RPC calls: those calls retain their session argument.
func (s *Service) mainConversationAttentionState(machine, session, state string, rev int64, seenAt time.Time) string {
	if rev == 0 {
		// A revision-less row has no durable disposition to classify. Keep its
		// current state; in particular, never resurrect an already-seen row.
		return state
	}
	if state == proto.StateIdle {
		// Only a previously silent verdict needs reclassification while idle:
		// its revision may now carry a promotion. An already-seen ordinary row
		// (or one with no verdict yet) is not reopened by a history read.
		cached, ok := s.conversationAttentionCached(machine, session, rev)
		if !ok || !cached.Silent {
			return state
		}
	}
	entry, ok := s.mainConversationAttention(context.Background(), machine, session, rev)
	if !ok {
		return state // an unread row fails open; an already-seen row is not resurrected on a read error
	}
	// A non-silent entry is the latest visible round. For a silent entry it is
	// the nearest earlier visible round; without one, only the silent round is
	// retired and there is no unread work to preserve.
	if !entry.HasVisible {
		return proto.StateIdle
	}
	if seenAt.IsZero() || entry.VisibleAt.IsZero() || seenAt.Before(entry.VisibleAt.Add(-2*time.Second)) {
		return proto.StateUnread
	}
	return proto.StateIdle
}
