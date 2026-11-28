package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

const (
	conversationHistoryKey      = "assistant.history"
	historyProbeInterval        = 30 * time.Second
	historyOfflineProbeInterval = 10 * time.Minute
)

var errHistoryGenerationUnavailable = errors.New("runtime did not provide a history generation")

type conversationHistoryStore interface {
	ConversationHistory(context.Context, string, string) (*db.ConversationHistory, error)
	PutConversationPage(context.Context, db.ConversationPage) error
	ConversationItems(context.Context, string, string, string, int, int, int, bool) ([]conversationlog.Item, error)
	ConversationItem(context.Context, string, string, string, string) (*conversationlog.Item, error)
	PutConversationSummary(context.Context, string, string, string, int64, string) error
	ConversationHistoryStats(context.Context) (db.ConversationHistoryStats, error)
}

type conversationHistoryBook struct {
	mu               sync.Mutex
	now              func() time.Time
	wake             chan struct{}
	changes, checked map[searchKey]uint64
	probed           map[searchKey]time.Time
	retry            map[searchKey]historyRetry
}

func (s *Service) historyNow() time.Time {
	s.history.mu.Lock()
	nowFn := s.history.now
	s.history.mu.Unlock()
	if nowFn != nil {
		return nowFn()
	}
	return time.Now()
}

type historyRetry struct {
	until    time.Time
	revision int64
	change   uint64
}

// historySourceKey prevents a synchronization source read from reading itself from the database.
type historySourceKey struct{}

// liveHistoryReadKey asks historyCall to bypass the database cache and read the live source directly.
// Unlike historySourceKey, logicalCall still resolves a logical conversation to its active segment, while
// logicalHistoryPage clears this flag for retired frozen segments so their durable history remains cached.
type liveHistoryReadKey struct{}

func withLiveHistoryRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, liveHistoryReadKey{}, true)
}

func withoutLiveHistoryRead(ctx context.Context) context.Context {
	return context.WithValue(ctx, liveHistoryReadKey{}, false)
}

func liveHistoryRead(ctx context.Context) bool {
	v, _ := ctx.Value(liveHistoryReadKey{}).(bool)
	return v
}

func (s *Service) historyDB(ctx context.Context) conversationHistoryStore {
	st := s.store()
	if st == nil {
		return nil
	}
	if ctx.Value(logicalSourceKey{}) != nil {
		durable, _ := st.(conversationHistoryStore)
		return durable
	}
	var setting struct {
		Enabled *bool `json:"enabled"`
	}
	_, err := st.Setting(ctx, conversationHistoryKey, &setting)
	if err != nil || (setting.Enabled != nil && !*setting.Enabled) {
		return nil
	}
	durable, _ := st.(conversationHistoryStore)
	return durable
}

func (s *Service) historySource(ctx context.Context, machine, method string, params, out any) error {
	return s.call(context.WithValue(ctx, historySourceKey{}, true), machine, method, params, out)
}

func (s *Service) historySession(ctx context.Context, machine, sid string) (proto.Session, bool) {
	if s.devs != nil {
		if c, ok := s.physicalDevices().Session(machine, sid); ok {
			if m, exists := s.fleetMachine(machine); exists {
				c.Assistant = m.isAssistant(c)
			}
			return c, true
		}
	}
	return s.session(ctx, machine, sid)
}

// historyEligible never enrolls a coding Agent merely because its launcher also runs assistants.
func (s *Service) historyEligible(ctx context.Context, machine, sid string) bool {
	if sid == "" || s.conversationAvailable(ctx, machine, sid) != nil {
		return false
	}
	if c, ok := s.historySession(ctx, machine, sid); ok {
		return c.Assistant && c.PaneID == ""
	}
	cfg := s.settings(ctx)
	return sid == cfg.Main && machine == cfg.mainRouteMachine()
}

type historyParams struct {
	SessionID  string `json:"session_id"`
	ItemID     string `json:"item_id"`
	Before     *int   `json:"before_seq"`
	After      *int   `json:"after_seq"`
	From       *int   `json:"from_seq"`
	Through    *int   `json:"through_seq"`
	Limit      int    `json:"limit"`
	Turns      int    `json:"turns"`
	LazyImages bool   `json:"lazy_images"`
	FoldWork   bool   `json:"fold_work"`
	Strict     bool   `json:"strict_read"`
	UserInputs bool   `json:"user_inputs"`
	MetaOnly   bool   `json:"meta_only"`
}

type historyPage struct {
	Items     []proto.Item `json:"items"`
	History   string       `json:"history"`
	FromSeq   *int         `json:"from_seq,omitempty"`
	ToSeq     *int         `json:"to_seq,omitempty"`
	HasMore   *bool        `json:"has_more,omitempty"`
	MoreAfter bool         `json:"more_after,omitempty"`
	More      bool         `json:"more,omitempty"`
	// Partial marks missing coverage, incomplete payloads or an uncertain offline end.
	Partial bool `json:"partial,omitempty"`
}

// historyCall covers both manager-internal reads and hub-proxied node calls. It
// leaves live execution, strict source exports and input-index projections on the runtime.
func (s *Service) historyCall(ctx context.Context, machine, method string, raw json.RawMessage) (result json.RawMessage, handled bool, err error) {
	if ctx.Value(historySourceKey{}) != nil || liveHistoryRead(ctx) || ctx.Value(inlineViewSourceKey{}) != nil {
		return nil, false, nil
	}
	if method != DeviceSessionItems && method != DeviceAssistantHandoff && method != "session.item" && method != "session.image" {
		return nil, false, nil
	}
	st := s.historyDB(ctx)
	if st == nil {
		return nil, false, nil
	}
	var p historyParams
	if json.Unmarshal(raw, &p) != nil || p.SessionID == "" || p.Strict || p.UserInputs {
		return nil, false, nil
	}
	defer func() {
		if handled && err == nil {
			if gone := s.conversationAvailable(ctx, machine, p.SessionID); gone != nil {
				result, err = nil, gone
			}
		}
	}()
	if err := s.conversationAvailable(ctx, machine, p.SessionID); err != nil {
		return nil, true, err
	}
	h, err := st.ConversationHistory(ctx, machine, p.SessionID)
	if err != nil {
		s.logf("assistant: read durable history: %v", err)
		return nil, false, nil
	}
	if h == nil && !s.historyEligible(ctx, machine, p.SessionID) {
		return nil, false, nil
	}
	if method == "session.item" || method == "session.image" {
		if h == nil {
			return nil, false, nil
		}
		if c, known := s.historySession(ctx, machine, p.SessionID); known && (c.Rev == 0 || c.Rev != h.Revision || s.historyDirty(searchKey{machine, p.SessionID})) {
			return nil, false, nil
		}
		row, err := st.ConversationItem(ctx, machine, p.SessionID, h.History, p.ItemID)
		if err != nil || row == nil || !row.Complete {
			return nil, false, nil
		}
		var result any = row.Item
		if method == "session.image" {
			if row.Item.Image == nil || row.Item.Image.Data == "" {
				return nil, false, nil
			}
			result = row.Item.Image
		}
		out, err := json.Marshal(result)
		return out, true, err
	}
	if method == DeviceAssistantHandoff {
		if h != nil {
			fresh := true
			if c, known := s.historySession(ctx, machine, p.SessionID); known {
				fresh = c.Rev > 0 && c.Rev == h.Revision && !s.historyDirty(searchKey{machine, p.SessionID})
			}
			if fresh {
				if out, ok := s.historyHandoff(ctx, st, h, false); ok {
					return out, true, nil
				}
			}
			var native json.RawMessage
			if sourceErr := s.historySource(ctx, machine, method, json.RawMessage(raw), &native); sourceErr == nil {
				return native, true, nil
			}
			if ctx.Err() != nil {
				return nil, true, ctx.Err()
			}
			if out, ok := s.historyHandoff(ctx, st, h, true); ok {
				if !fresh || s.historyMachineOffline(ctx, machine) {
					var handoff mainHandoff
					if json.Unmarshal(out, &handoff) == nil {
						handoff.Partial = true
						out, _ = json.Marshal(handoff)
					}
				}
				return out, true, nil
			}
		}
		return nil, false, nil
	}
	// Known current revision gives the latest end; an offline node may still have a
	// durable end, but cannot prove it has not appended since then.
	fresh := true
	if h != nil {
		offline := s.historyMachineOffline(ctx, machine)
		if c, ok := s.historySession(ctx, machine, p.SessionID); ok {
			fresh = c.Rev > 0 && h.Revision == c.Rev && !s.historyDirty(searchKey{machine, p.SessionID})
		}
		if h.TailSeq != nil {
			if out, ok := s.historyPage(ctx, st, h, p, fresh || offline, offline); ok {
				if offline {
					out = partialHistoryPage(out)
				}
				return out, true, nil
			}
		}
	}
	params := map[string]any{}
	_ = json.Unmarshal(raw, &params)
	delete(params, "fold_work") // persist source blocks, never display folds
	sourceRevision := int64(0)
	if c, known := s.historySession(ctx, machine, p.SessionID); known {
		sourceRevision = c.Rev
	}
	var source historyPage
	if err := s.historySource(ctx, machine, DeviceSessionItems, params, &source); err != nil {
		if ctx.Err() != nil {
			return nil, true, ctx.Err()
		}
		if h != nil {
			if out, ok := s.historyPage(ctx, st, h, p, true, true); ok {
				out = partialHistoryPage(out)
				return out, true, nil
			}
		}
		return nil, true, err
	}
	if err := s.historyRemember(ctx, st, machine, p.SessionID, h, p, source, sourceRevision); err != nil && !errors.Is(err, errHistoryGenerationUnavailable) {
		s.logf("assistant: persist history page: %v", err)
	}
	if p.FoldWork && p.After == nil && p.Through == nil {
		if p.Before == nil || *p.Before <= 0 {
			source.Items = transcript.FoldWorkLatest(source.Items)
		} else {
			source.Items = transcript.FoldWork(source.Items)
		}
	}
	if p.LazyImages {
		source.Items = proto.WithoutImageData(source.Items)
	}
	out, err := json.Marshal(source)
	return out, true, err
}

func partialHistoryPage(raw json.RawMessage) json.RawMessage {
	var page historyPage
	if json.Unmarshal(raw, &page) != nil {
		return raw
	}
	page.Partial = true
	out, err := json.Marshal(page)
	if err != nil {
		return raw
	}
	return out
}

func (s *Service) historyMachineOffline(ctx context.Context, machine string) bool {
	for _, d := range s.devices(ctx) {
		if d.ID == machine {
			return !d.Online && !d.Reachable
		}
	}
	return false
}

func historyItem(row conversationlog.Item, h *db.ConversationHistory) proto.Item {
	it := row.Item
	if !row.Complete {
		it.Stored = &proto.StoredItem{Complete: false, SourceBytes: row.SourceBytes,
			MachineID: h.MachineID, SessionID: h.SessionID, History: h.History, ItemID: it.ID}
	}
	return it
}

func (s *Service) historyPage(ctx context.Context, st conversationHistoryStore, h *db.ConversationHistory, p historyParams, fresh, allowPartial bool) (json.RawMessage, bool) {
	unknownTail := h.TailSeq == nil
	if unknownTail {
		if !fresh || len(h.Coverage) == 0 {
			return nil, false
		}
		copy := *h
		end := h.Coverage[len(h.Coverage)-1].Through
		copy.TailSeq = &end
		h = &copy
	}
	// Any source change may be a rewind, even when the file grew. Verify the
	// generation before using old ranges; offline fallback is explicitly partial.
	if !fresh {
		return nil, false
	}
	from, through, newest := 0, *h.TailSeq, p.After == nil
	if p.After != nil {
		from = max(0, *p.After)
	}
	if p.Through != nil {
		from, through = max(0, intValue(p.From)-1), *p.Through
		newest = false
	}
	if p.Before != nil && *p.Before > 0 {
		through = min(through, *p.Before-1)
	}
	if through < from {
		return nil, false
	}
	var span *conversationlog.Range
	for _, r := range h.Coverage {
		back := newest && (r.From < through || (through == 0 && r.From == 0)) && r.Through >= through
		forward := !newest && r.From <= from && (r.Through > from || (from == *h.TailSeq && r.Through == from))
		if back || forward {
			copy := r
			span = &copy
			break
		}
	}
	if span == nil {
		return nil, false
	}
	requestedFrom, requestedThrough := from, through
	from, through = max(from, span.From), min(through, span.Through)
	limit := p.Limit
	if limit <= 0 || limit > 500 {
		limit = 80
	}
	switch {
	case p.Through != nil:
		limit = 1000
	case p.After != nil:
		limit = 500
	case p.Turns > 0:
		limit = 2000
	}
	rows, err := st.ConversationItems(ctx, h.MachineID, h.SessionID, h.History, from, through, limit, newest)
	if err != nil {
		return nil, false
	}
	items := make([]proto.Item, 0, len(rows))
	payloadIncomplete := false
	for _, row := range rows {
		payloadIncomplete = payloadIncomplete || !row.Complete
		if !p.LazyImages && row.Item.Image != nil && !row.Complete {
			return nil, false
		}
		items = append(items, historyItem(row, h))
	}
	if p.Turns > 0 && newest {
		start, seen := len(items), 0
		for start > 0 && seen < min(p.Turns, 10) {
			start--
			if transcript.IsPagePrompt(items[start]) {
				seen++
			}
		}
		for start > 0 && items[start].Seq == items[start-1].Seq {
			start--
		}
		if !allowPartial && seen < min(p.Turns, 10) && len(rows) < limit && from > requestedFrom {
			return nil, false
		}
		items = items[start:]
		if start > 0 {
			from = max(from, items[0].Seq-1)
		}
	}
	// A covered suffix or prefix does not prove a larger requested window. Fill
	// it from the source while online; retain partial data when the source fails.
	if !allowPartial && len(rows) < limit && (p.Turns <= 0 || !newest) {
		if (newest && from > requestedFrom) || (!newest && through < requestedThrough) {
			return nil, false
		}
	}
	pageFrom, pageTo := from, through
	if len(items) > 0 {
		if newest && len(rows) >= limit {
			pageFrom = max(from, items[0].Seq-1)
		}
		if !newest && len(rows) >= limit {
			pageTo = items[len(items)-1].Seq
		}
	}
	hasMore := pageFrom > 0
	answer := historyPage{Items: items, History: h.History, FromSeq: &pageFrom, ToSeq: &pageTo,
		Partial: payloadIncomplete || unknownTail || !conversationlog.Covers(h.Coverage, 0, *h.TailSeq)}
	if newest {
		answer.HasMore = &hasMore
	} else {
		answer.MoreAfter = pageTo < *h.TailSeq
	}
	if p.Through != nil {
		answer.More = pageTo < min(*p.Through, *h.TailSeq)
		answer.MoreAfter = false
	}
	if p.FoldWork && p.After == nil && p.Through == nil {
		if p.Before == nil || *p.Before <= 0 {
			answer.Items = transcript.FoldWorkLatest(answer.Items)
		} else {
			answer.Items = transcript.FoldWork(answer.Items)
		}
	}
	if p.LazyImages {
		answer.Items = proto.WithoutImageData(answer.Items)
	}
	out, err := json.Marshal(answer)
	return out, err == nil
}

func intValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// historyRemember stores only coverage the source page proves. No content-equality deduplication.
func (s *Service) historyRemember(ctx context.Context, st conversationHistoryStore, machine, sid string, previous *db.ConversationHistory, p historyParams, page historyPage, revision int64) error {
	if page.History == "" {
		return errHistoryGenerationUnavailable
	}
	if err := s.conversationAvailable(ctx, machine, sid); err != nil {
		return err
	}
	from, through := 0, 0
	if p.After != nil {
		from = max(0, *p.After)
	}
	if p.Through != nil {
		from = max(0, intValue(p.From)-1)
	}
	if page.FromSeq != nil {
		from = *page.FromSeq
	}
	if p.After == nil && page.FromSeq == nil && (page.HasMore == nil || *page.HasMore) && len(page.Items) > 0 {
		from = max(0, page.Items[0].Seq-1)
	}
	if page.ToSeq != nil {
		through = *page.ToSeq
	}
	for _, it := range page.Items {
		through = max(through, it.Seq)
	}
	if page.ToSeq != nil && through < from {
		// The source ends before the reader's cursor, which is from an earlier numbering of it: what the page
		// proves is that end. Stored at the cursor, the tail would claim lines the source does not have.
		from = through
	}
	through = max(from, through)
	expected, history := "", page.History
	if previous != nil {
		expected = previous.History
		if history == "" {
			history = expected
		}
	}
	if previous != nil && history != expected && from != 0 {
		return db.ErrConversationHistoryChanged
	}
	tail := (p.Before == nil || *p.Before <= 0) && p.Through == nil && !page.MoreAfter
	items := append([]proto.Item(nil), page.Items...)
	upgradeCtx, cancelUpgrades := context.WithTimeout(ctx, 10*time.Second)
	defer cancelUpgrades()
	upgrades := 0
	for i := range items {
		it := &items[i]
		if it.ID == "" {
			return errors.New("source item has no stable identity")
		}
		// Full source text below the storage limit is worth recovering; oversized
		// output remains a reference and cannot turn a page into an unbounded fetch.
		if it.FullSize > 0 && it.FullSize <= conversationlog.MaxItemBytes && upgrades < 16 && upgradeCtx.Err() == nil {
			upgrades++
			var full proto.Item
			if err := s.historySource(upgradeCtx, machine, "session.item", map[string]any{"session_id": sid, "item_id": it.ID}, &full); err == nil && full.ID == it.ID && full.Seq == it.Seq {
				*it = full
			}
		}
	}
	if upgrades > 0 {
		var check historyPage
		err := s.historySource(upgradeCtx, machine, DeviceSessionItems, map[string]any{"session_id": sid, "after_seq": through, "limit": 1, "lazy_images": true}, &check)
		if err != nil {
			items = page.Items
		} else if check.History != "" && check.History != page.History {
			return db.ErrConversationHistoryChanged
		}
	}
	return st.PutConversationPage(ctx, db.ConversationPage{MachineID: machine, SessionID: sid, History: history, ExpectedHistory: expected,
		Revision: revision, FromSeq: from, ThroughSeq: through, Tail: tail, Items: items})
}

func (s *Service) historyHandoff(ctx context.Context, st conversationHistoryStore, h *db.ConversationHistory, allowPartial bool) (json.RawMessage, bool) {
	if h.TailSeq == nil {
		return nil, false
	}
	partial := !h.SummaryKnown || !conversationlog.Covers(h.Coverage, 0, *h.TailSeq)
	if partial && !allowPartial {
		return nil, false
	}
	rows, err := st.ConversationItems(ctx, h.MachineID, h.SessionID, h.History, 0, *h.TailSeq, 500, true)
	if err != nil {
		return nil, false
	}
	result := mainHandoff{Summary: h.Summary, Partial: partial}
	afterCompact := 0
	for _, row := range rows {
		if row.Item.Compact != nil {
			afterCompact = max(afterCompact, row.Item.Seq)
		}
	}
	for _, row := range rows {
		it := row.Item
		if !row.Complete {
			result.Partial = true
			if !allowPartial {
				return nil, false
			}
		}
		if !row.Complete || it.Seq <= afterCompact || it.Kind != proto.KindText || (it.Role != "user" && it.Role != "assistant") {
			continue
		}
		text := strings.TrimSpace(transcript.StripInjected(it.Text))
		if text == "" {
			continue
		}
		result.Recent = append(result.Recent, struct {
			Role string `json:"role"`
			Text string `json:"text"`
		}{it.Role, clip(text, 800)})
	}
	if len(result.Recent) > 12 {
		result.Recent = result.Recent[len(result.Recent)-12:]
	}
	total := 0
	for i := len(result.Recent) - 1; i >= 0; i-- {
		total += len([]rune(result.Recent[i].Text))
		if total > 6000 {
			result.Recent = result.Recent[i+1:]
			break
		}
	}
	if len(result.Recent) == 0 && result.Summary == "" {
		return nil, false
	}
	out, err := json.Marshal(result)
	return out, err == nil
}

func (s *Service) historyKnown(ctx context.Context, machine, sid string) bool {
	st := s.historyDB(ctx)
	if st == nil {
		return false
	}
	h, err := st.ConversationHistory(ctx, machine, sid)
	return err == nil && h != nil
}

func (s *Service) historyWake() {
	s.history.mu.Lock()
	if s.history.wake == nil {
		s.history.wake = make(chan struct{}, 1)
	}
	wake := s.history.wake
	s.history.mu.Unlock()
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (s *Service) historyDirty(k searchKey) bool {
	s.history.mu.Lock()
	defer s.history.mu.Unlock()
	return s.history.changes[k] != s.history.checked[k]
}

// Notifications are hints, never source records: an old event may cause a pull
// but cannot put an old generation back into the database.
func (s *Service) historySignal(machine, event string, raw json.RawMessage) {
	keys := []searchKey{}
	switch event {
	case "session.items":
		var p struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal(raw, &p) == nil && p.SessionID != "" {
			keys = append(keys, searchKey{machine, p.SessionID})
		}
	case "sessions.revs":
		var p struct {
			Revs map[string]int64 `json:"revs"`
		}
		if json.Unmarshal(raw, &p) == nil {
			for sid := range p.Revs {
				keys = append(keys, searchKey{machine, sid})
			}
		}
	default:
		return
	}
	expanded := make([]searchKey, 0, len(keys))
	for _, k := range keys {
		for _, alias := range s.physicalAliases(k.machine) {
			expanded = append(expanded, searchKey{alias, k.session})
		}
	}
	keys = expanded

	s.history.mu.Lock()
	if s.history.changes == nil {
		s.history.changes = map[searchKey]uint64{}
	}
	for _, k := range keys {
		s.history.changes[k]++
	}
	s.history.mu.Unlock()
	s.historyWake()
}

// historyLoop does bounded revision-driven pulls, without a session subscription
// or a connected UI. It also catches up after manager/node reconnects and rollback.
func (s *Service) historyLoop(ctx context.Context) {
	if _, ok := s.store().(conversationHistoryStore); !ok {
		return
	}
	s.historyWake()
	s.history.mu.Lock()
	wake := s.history.wake
	s.history.mu.Unlock()
	for {
		s.logicalSweep(ctx)
		s.wakeConversationSwitches()
		s.machineFallbackSweep(ctx, time.Now())
		if st := s.historyDB(ctx); st != nil {
			s.historySweep(ctx, st)
		} else if st, ok := s.store().(conversationHistoryStore); ok {
			rows, _, err := s.logicalIndex()
			if err == nil && len(rows) > 0 {
				syncCtx := context.WithValue(context.WithValue(ctx, logicalSourceKey{}, true), logicalHistoryOnlyKey{}, true)
				s.historySweep(syncCtx, st)
			}
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
		// Multiple revisions in one burst share a pull, and source reads stay bounded.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (s *Service) historySweep(ctx context.Context, st conversationHistoryStore) {
	for _, machine := range s.devices(ctx) {
		if !machine.Online && !machine.Reachable {
			continue
		}
		list, err := s.sessions(ctx, machine.ID)
		if physical := s.physicalDevices(); physical != nil {
			var known bool
			list, known = physical.Sessions(machine.ID)
			if !known {
				continue
			}
			if m, ok := s.fleetMachine(machine.ID); ok {
				for i := range list {
					list[i].Assistant = m.isAssistant(list[i])
				}
			}
			err = nil
		}
		if err != nil {
			continue
		}
		for _, c := range list {
			if ctx.Err() != nil {
				return
			}
			if !c.Assistant || c.PaneID != "" || c.IsDeleted() {
				continue
			}
			source := machine.ID
			root, lookupErr := func() (*db.LogicalConversation, error) {
				if logical, ok := s.store().(logicalConversationStore); ok {
					return s.physicalLogicalConversation(ctx, logical, source, c.SessionID)
				}
				return nil, nil
			}()
			if lookupErr != nil {
				continue
			}
			if ctx.Value(logicalHistoryOnlyKey{}) != nil && root == nil {
				continue
			}
			if root != nil {
				// Retired sources remain readable for frozen history, but their tails do
				// not revise the logical generation. Keep their original durable keys.
				matches := 0
				for _, seg := range root.Segments {
					if seg.SessionID == c.SessionID && s.physicalSameMachine(seg.MachineID, source) {
						source = seg.MachineID
						matches++
					}
				}
				if matches != 1 {
					continue
				}
			}
			if err := s.historySync(ctx, st, source, c); err != nil && !errors.Is(err, db.ErrConversationHistoryChanged) && ctx.Err() == nil {
				k := searchKey{source, c.SessionID}
				now := s.historyNow()
				s.history.mu.Lock()
				if s.history.retry == nil {
					s.history.retry = map[searchKey]historyRetry{}
				}
				s.history.retry[k] = historyRetry{now.Add(time.Minute), c.Rev, s.history.changes[k]}
				s.history.mu.Unlock()
				if !errors.Is(err, errHistoryGenerationUnavailable) {
					s.logf("assistant: synchronize durable conversation: %v", err)
				}
			}
		}
	}
}

func (s *Service) historySync(ctx context.Context, st conversationHistoryStore, machine string, c proto.Session) error {
	k := searchKey{machine, c.SessionID}
	now := s.historyNow()
	s.history.mu.Lock()
	change := s.history.changes[k]
	dirty := change != s.history.checked[k]
	probedAt := s.history.probed[k]
	retry := s.history.retry[k]
	s.history.mu.Unlock()
	if retry.revision == c.Rev && retry.change == change && now.Before(retry.until) {
		return nil
	}
	probeInterval := historyProbeInterval
	if c.Status == proto.StatusOffline {
		// Idle offline conversations whose durable history already covers the full
		// range at the current revision do not need a forced node probe every 30s;
		// still re-probe periodically so a same-revision rewrite is detected.
		probeInterval = historyOfflineProbeInterval
	}
	force := dirty || probedAt.IsZero() || now.Sub(probedAt) >= probeInterval
	for range 8 {
		if s.historyDB(ctx) == nil {
			return nil
		}
		if err := s.conversationAvailable(ctx, machine, c.SessionID); err != nil {
			return err
		}
		h, err := st.ConversationHistory(ctx, machine, c.SessionID)
		if err != nil {
			return err
		}
		revision := c.Rev
		if !force && h != nil && revision > 0 && h.Revision == revision && h.TailSeq != nil && conversationlog.Covers(h.Coverage, 0, *h.TailSeq) {
			return nil
		}
		after := 0
		if h != nil {
			for _, r := range h.Coverage {
				if r.From == 0 {
					after = r.Through
					break
				}
			}
		}
		var page historyPage
		p := historyParams{SessionID: c.SessionID, After: &after, LazyImages: true}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = s.historySource(cctx, machine, DeviceSessionItems, map[string]any{"session_id": c.SessionID, "after_seq": after, "limit": 500, "lazy_images": true}, &page)
		cancel()
		if err != nil {
			return err
		}
		if h != nil && page.History != "" && page.History != h.History && after != 0 {
			after = 0
			p.After = &after
			cctx, cancel = context.WithTimeout(ctx, 15*time.Second)
			err = s.historySource(cctx, machine, DeviceSessionItems, map[string]any{"session_id": c.SessionID, "after_seq": 0, "limit": 500, "lazy_images": true}, &page)
			cancel()
			if err != nil {
				return err
			}
		}
		if err := s.historyRemember(ctx, st, machine, c.SessionID, h, p, page, revision); err != nil {
			return err
		}
		if logical, ok := s.store().(logicalConversationStore); ok {
			if err := logical.ObserveConversationSource(ctx, machine, c.SessionID, page.History); err != nil {
				return err
			}
			if err := logical.ObserveConversationRevision(ctx, machine, c.SessionID, c.Rev); err != nil {
				return err
			}
			s.logicalChanged()
		}
		advanced := intValue(page.ToSeq)
		for _, it := range page.Items {
			advanced = max(advanced, it.Seq)
		}
		if page.MoreAfter && advanced <= after {
			return errors.New("runtime history cursor did not advance")
		}
		if !page.MoreAfter {
			fresh, err := st.ConversationHistory(ctx, machine, c.SessionID)
			if err != nil || fresh == nil {
				return err
			}
			needSummary := !fresh.SummaryKnown
			for _, it := range page.Items {
				needSummary = needSummary || it.Compact != nil
			}
			if needSummary {
				var handoff mainHandoff
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := s.historySource(cctx, machine, DeviceAssistantHandoff, map[string]any{"session_id": c.SessionID}, &handoff)
				cancel()
				if err == nil {
					// The summary is read through a separate runtime method. Fence a
					// rewrite between the page and summary reads before checkpointing it.
					var verified historyPage
					cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
					verifyErr := s.historySource(cctx, machine, DeviceSessionItems, map[string]any{"session_id": c.SessionID, "after_seq": intValue(fresh.TailSeq), "limit": 1, "lazy_images": true}, &verified)
					cancel()
					if verifyErr != nil {
						return verifyErr
					}
					if verified.History != fresh.History {
						return db.ErrConversationHistoryChanged
					}
					if err := st.PutConversationSummary(ctx, machine, c.SessionID, fresh.History, fresh.Revision, clip(handoff.Summary, 12000)); err != nil {
						return err
					}
				}
			}
			probedAt := s.historyNow()
			s.history.mu.Lock()
			if s.history.checked == nil {
				s.history.checked = map[searchKey]uint64{}
			}
			if s.history.probed == nil {
				s.history.probed = map[searchKey]time.Time{}
			}
			s.history.checked[k], s.history.probed[k] = change, probedAt
			delete(s.history.retry, k)
			s.history.mu.Unlock()
			return nil
		}
	}
	s.historyWake() // more legacy pages remain; continue in the next bounded sweep
	return nil
}

func (s *Service) historyRoutes() {
	s.handle("GET /api/v1/assistant/history", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		st, ok := s.store().(conversationHistoryStore)
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		stats, err := st.ConversationHistoryStats(r.Context())
		if err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"enabled": s.historyDB(r.Context()) != nil, "stats": stats})
	})
	s.handle("PUT /api/v1/assistant/history", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		var p struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&p); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if p.Enabled == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if s.store() == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if err := s.store().PutSetting(r.Context(), conversationHistoryKey, p); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		if *p.Enabled {
			s.history.mu.Lock()
			s.history.retry = nil
			s.history.probed = nil
			s.history.mu.Unlock()
		}
		s.historyWake()
		apierr.WriteJSON(w, p)
	})
}
