package assistant

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
)

// threadBindingIndex owns a channel's binding generations and admission order. The bindings themselves stay in
// the assistant's existing channel preferences. The channels book lock protects both.
type threadBindingIndex struct {
	generations map[string]uint64
	pending     map[threadBindingQueueKey]chan struct{}
}

// threadBindingRef captures the routing keys and Labs mode at admission. Keys stay inside the index so receipt
// ordering, commands and delivery all refer to the same thread when settings change while a message waits.
type threadBindingRef struct {
	channel    string
	generation uint64
	v2         bool
	primary    string
	alias      string
}

// Legacy receipts reserve the key supplied by the plugin before asking the hub for its view. Delivery still
// requires the threads capability in that mode.
func (r threadBindingRef) isThread(v channel.View) bool {
	return r.primary != "" && v.ID != "" && (r.v2 || v.Capabilities.Threads)
}

func (i *threadBindingIndex) generation(id string) uint64 { return i.generations[id] }

func (i *threadBindingIndex) isCurrent(r threadBindingRef) bool {
	return i.generation(r.channel) == r.generation
}

func (i *threadBindingIndex) invalidate(id string) {
	if i.generations == nil {
		i.generations = make(map[string]uint64)
	}
	i.generations[id]++
}

func (s *Service) threadBindingCurrent(r threadBindingRef) bool {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	return s.chans.bindings.isCurrent(r)
}

// channelRoutingMode is shared by inbound routing and the settings projection. An explicit choice stays visible
// in settings with Labs off, when routing uses the legacy key.
func channelRoutingMode(v channel.View, configured channel.ThreadMode, v2 bool) channel.ThreadMode {
	return configured.Effective(v2 && v.Capabilities.Threads)
}

func (s *Service) resolveThreadBindingLocked(ctx context.Context, id string, m channel.Message, v channel.View, v2 bool) (threadBindingRef, error) {
	c := &s.chans
	var mode channel.ThreadMode
	if v2 {
		s.loadChannelsLocked(ctx)
		if !c.loaded {
			return threadBindingRef{}, errChannelsUnread
		}
		mode = channelRoutingMode(v, s.channelPrefLocked(id).ThreadMode, v2)
	}
	return c.bindings.resolve(id, m, mode, v2), nil
}

func (i *threadBindingIndex) resolve(id string, m channel.Message, mode channel.ThreadMode, v2 bool) threadBindingRef {
	legacy := strings.TrimSpace(m.Thread)
	chatID, topicID := strings.TrimSpace(m.ChatID), strings.TrimSpace(m.TopicID)
	kind := m.ChatKind.Known()
	var topic string
	if kind != channel.ChatKindDirect && chatID != "" && topicID != "" {
		topic = channelTopicRouteKey(chatID, topicID)
	}
	// Legacy producers and incomplete structured input can identify a thread using only the old key.
	r := threadBindingRef{channel: id, generation: i.generation(id), v2: v2, primary: legacy}
	if v2 {
		switch {
		case (mode != channel.ThreadModePerGroupAndTopic && mode != channel.ThreadModePerTopic) || kind == channel.ChatKindDirect:
			r.primary = ""
		case topic != "":
			r.primary = topic
		case kind == channel.ChatKindGroup && topicID == "":
			r.primary = ""
			if mode == channel.ThreadModePerGroupAndTopic && chatID != "" {
				r.primary = channelGroupRouteKey(chatID)
			}
		}
	}
	if legacy != "" && topic != "" && legacy != topic {
		switch r.primary {
		case legacy:
			r.alias = topic
		case topic:
			r.alias = legacy
		}
	}
	return r
}

// The length prefix keeps chat and topic identifiers unambiguous even when they contain colons.
func channelTopicRouteKey(chatID, topicID string) string {
	return "v2:topic:" + strconv.Itoa(len(chatID)) + ":" + chatID + ":" + topicID
}

func channelGroupRouteKey(chatID string) string { return "v2:group:" + chatID }

// channelThread is the existing stored binding and the time of its last delivery.
type channelThread struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
}

func (t *channelThread) isSameConversation(other *channelThread) bool {
	return t != nil && other != nil && t.SessionID != "" &&
		t.MachineID == other.MachineID && t.SessionID == other.SessionID
}

const (
	channelThreadsKeep    = 500
	channelThreadsKeepFor = 30 * 24 * time.Hour
)

func channelThreadIdleTTL(v2 bool, p *channelPrefs) time.Duration {
	if v2 && p != nil && p.ThreadIdleHours > 0 {
		return time.Duration(min(p.ThreadIdleHours, channel.MaxThreadIdleHours)) * time.Hour
	}
	return channelThreadsKeepFor
}

// lookupThreadBinding tries the alias only when there is no live binding under the primary key.
func (s *Service) lookupThreadBinding(ctx context.Context, r threadBindingRef) *channelThread {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.bindings.isCurrent(r) {
		return nil
	}
	s.loadChannelsLocked(ctx)
	p := s.channelPrefLocked(r.channel)
	now, ttl := c.clock(), channelThreadIdleTTL(r.v2, p)
	for _, key := range [...]string{r.primary, r.alias} {
		if t := p.Threads[key]; key != "" && t != nil && now.Sub(t.At) <= ttl {
			cp := *t
			return &cp
		}
	}
	return nil
}

// refreshThreadBinding records a delivery while its binding generation is current. A settings write failure is
// logged and rolled back by saveChannelsLocked. The caller still follows the delivered message.
func (s *Service) refreshThreadBinding(r threadBindingRef, machine, session string) bool {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.bindings.isCurrent(r) {
		return false
	}
	s.loadChannelsLocked(context.Background())
	p := c.prefsForUpdate(r.channel)
	p.Threads = maps.Clone(p.Threads)
	if p.Threads == nil {
		p.Threads = make(map[string]*channelThread)
	}
	now := c.clock().UTC()
	binding := &channelThread{MachineID: machine, SessionID: session, At: now}
	p.Threads[r.primary] = binding
	// Once both keys have been written, they share the conversation and its idle clock.
	if r.alias != "" && p.Threads[r.alias] != nil {
		p.Threads[r.alias] = binding
	}
	// Storage keeps both modes' idle windows so either mode can resume its active conversations after a switch.
	keepFor := max(channelThreadsKeepFor, channelThreadIdleTTL(true, &p))
	pruneThreadBindings(p.Threads, now, keepFor)
	_ = s.saveChannelsLocked(r.channel, p)
	return true
}

// resetThreadBinding forgets the topic's bindings so /new starts fresh in either mode.
func (s *Service) resetThreadBinding(r threadBindingRef) error {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.bindings.isCurrent(r) {
		return nil
	}
	s.loadChannelsLocked(context.Background())
	if !c.loaded {
		return errChannelsUnread
	}
	p := c.prefsForUpdate(r.channel)
	primary, alias := p.Threads[r.primary], p.Threads[r.alias]
	if primary == nil && alias == nil {
		return nil
	}
	p.Threads = maps.Clone(p.Threads)
	// A command carrying a single key also clears the other bindings for its conversation.
	maps.DeleteFunc(p.Threads, func(key string, binding *channelThread) bool {
		return key == r.primary || (r.alias != "" && key == r.alias) ||
			binding.isSameConversation(primary) || binding.isSameConversation(alias)
	})
	return s.saveChannelsLocked(r.channel, p)
}

func pruneThreadBindings(threads map[string]*channelThread, now time.Time, keepFor time.Duration) {
	for key, t := range threads {
		if t == nil || now.Sub(t.At) > keepFor {
			delete(threads, key)
		}
	}
	if len(threads) <= channelThreadsKeep {
		return
	}
	keys := slices.Collect(maps.Keys(threads))
	slices.SortFunc(keys, func(a, b string) int { return threads[a].At.Compare(threads[b].At) })
	for _, key := range keys[:len(keys)-channelThreadsKeep] {
		delete(threads, key)
	}
}

type threadBindingQueueKey struct {
	channel, thread string
	generation      uint64
}

type threadBindingSlot struct {
	key  threadBindingQueueKey
	prev chan struct{}
}

type threadBindingReservation struct {
	slots [2]threadBindingSlot
	count int
	done  chan struct{}
}

// reserve orders a dual-format topic behind either predecessor. The empty primary key is the ordinary route.
func (i *threadBindingIndex) reserve(r threadBindingRef) threadBindingReservation {
	if i.pending == nil {
		i.pending = make(map[threadBindingQueueKey]chan struct{})
	}
	q := threadBindingReservation{done: make(chan struct{})}
	for slot, thread := range [...]string{r.primary, r.alias} {
		if slot == 1 && thread == "" {
			break
		}
		key := threadBindingQueueKey{channel: r.channel, thread: thread, generation: r.generation}
		q.slots[slot] = threadBindingSlot{key: key, prev: i.pending[key]}
		q.count++
		i.pending[key] = q.done
	}
	return q
}

// rollback is called before the book lock is released, so no later reservation can have joined this one.
func (i *threadBindingIndex) rollback(q threadBindingReservation) {
	for _, slot := range q.slots[:q.count] {
		if slot.prev == nil {
			delete(i.pending, slot.key)
		} else {
			i.pending[slot.key] = slot.prev
		}
	}
}

func (q threadBindingReservation) wait() {
	for _, slot := range q.slots[:q.count] {
		if slot.prev != nil {
			<-slot.prev
		}
	}
}

func (i *threadBindingIndex) release(q threadBindingReservation) {
	for _, slot := range q.slots[:q.count] {
		if i.pending[slot.key] == q.done {
			delete(i.pending, slot.key)
		}
	}
	close(q.done)
}
