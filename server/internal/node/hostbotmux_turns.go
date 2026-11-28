package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// botmuxCapsTTL bounds how long a probed BotMux instance capability record is reused without re-probing,
// covering an in-place upgrade behind a wrapper script whose file stamp stays unchanged.
const (
	botmuxCapsTTL               = 5 * time.Minute
	botmuxTurnsPageSize         = 100
	botmuxTurnsMaxPages         = 20
	botmuxTurnsMaxBytes         = 8 << 20
	botmuxTurnsFetchMinInterval = 2 * time.Second
	botmuxTurnsErrorBackoff     = 15 * time.Second
	botmuxTurnsUnknownBackoff   = 15 * time.Second
	botmuxTurnsBGTimeout        = 2 * time.Second
	botmuxTurnsReadTimeout      = 200 * time.Millisecond
	// The structured turns kept in memory: sessions not read for botmuxTurnsCacheIdle are dropped (a later read fetches
	// from the start again), at most botmuxTurnsCacheSessions sessions (least recently used first) and the newest
	// botmuxTurnsCacheTurns turns of each (older items keep the wrapper decoding). An invalidated entry keeps no turns
	// and is dropped after botmuxTurnsInvalidGrace, long enough for a watch to repush what it decorated.
	botmuxTurnsCacheIdle     = 30 * time.Minute
	botmuxTurnsCacheSessions = 64
	botmuxTurnsCacheTurns    = 500
	botmuxTurnsInvalidGrace  = time.Minute
)

var errBotmuxTurnsTooLarge = fmt.Errorf("botmux: turns response exceeds %d bytes", botmuxTurnsMaxBytes)

// botmuxCaps caches what a BotMux dashboard instance supports, keyed by its loopback URL, token, and CLI file stamp.
// A dashboard restart (new URL/token) or CLI upgrade (new stamp) invalidates the cache automatically.
// ownerUnionID: its /api/trigger also takes actorUnionId (never set without ownerActor).
type botmuxCaps struct {
	known        bool
	turnsAPI     bool
	ownerActor   bool
	ownerUnionID bool
	base         string
	token        string
	stamp        string
	at           time.Time
}

// botmuxCapsOf is what a 200 answer's capabilities advertise.
func botmuxCapsOf(capabilities []string) (ownerActor, ownerUnionID bool) {
	ownerActor = slices.Contains(capabilities, transcript.BotmuxCapOwnerActor)
	return ownerActor, ownerActor && slices.Contains(capabilities, transcript.BotmuxCapOwnerUnionID)
}

func (c botmuxCaps) valid(base, token, stamp string, now time.Time) bool {
	return c.matches(base, token, stamp) && now.Sub(c.at) < botmuxCapsTTL
}

func (c botmuxCaps) matches(base, token, stamp string) bool {
	return c.known && c.base == base && c.token == token && c.stamp == stamp
}

// botmuxOwnerUnechoed records that /api/trigger did not echo actor="owner" for a specific (base, token, stamp).
// It stays sticky across subsequent sessionTurns/probeCaps updates until the instance/stamp changes or the TTL expires.
type botmuxOwnerUnechoed struct {
	base  string
	token string
	stamp string
	at    time.Time
}

func (u botmuxOwnerUnechoed) active(base, token, stamp string, now time.Time) bool {
	return !u.at.IsZero() && u.base == base && u.token == token && u.stamp == stamp && now.Sub(u.at) < botmuxCapsTTL
}

type botmuxSessionTurnsCache struct {
	base      string
	token     string
	stamp     string
	cursor    string
	fetchedAt time.Time
	usedAt    time.Time // last read or written (botmuxTurnsCacheIdle, LRU)
	invalid   bool
	invalidAt time.Time
	turns     []transcript.BotmuxTurn
	byID      map[string]int
	// released: turns held before invalidation, kept only until a refresh compares against them
	// (refreshSessionTurnsAsync) or botmuxTurnsInvalidGrace passes.
	released []transcript.BotmuxTurn
}

// invalidateLocked drops the entry's turns, keeping them aside for one repush comparison.
func (c *botmuxSessionTurnsCache) invalidateLocked(now time.Time) {
	if c.invalid {
		return
	}
	c.invalid, c.invalidAt = true, now
	c.released, c.turns, c.byID = c.turns, nil, nil
}

// pruneTurnsCacheLocked enforces the cache bounds: idle and long-invalid entries go, then the least recently used
// beyond botmuxTurnsCacheSessions. keep (the session being read or written) is never evicted for room.
func (h *botmuxHost) pruneTurnsCacheLocked(now time.Time, keep string) {
	for id, c := range h.turnsCache {
		if id == keep || h.turnsRefreshing[id] {
			continue
		}
		if now.Sub(c.usedAt) >= botmuxTurnsCacheIdle || c.invalid && now.Sub(c.invalidAt) >= botmuxTurnsInvalidGrace {
			delete(h.turnsCache, id)
		}
	}
	for len(h.turnsCache) > botmuxTurnsCacheSessions {
		oldest := ""
		for id, c := range h.turnsCache {
			if id != keep && (oldest == "" || c.usedAt.Before(h.turnsCache[oldest].usedAt)) {
				oldest = id
			}
		}
		if oldest == "" {
			return
		}
		delete(h.turnsCache, oldest)
	}
}

// trimTurnsLocked keeps the newest botmuxTurnsCacheTurns turns (they arrive in seq order); older transcript items fall
// back to the wrapper decoding.
func (c *botmuxSessionTurnsCache) trimTurnsLocked() {
	drop := len(c.turns) - botmuxTurnsCacheTurns
	if drop <= 0 {
		return
	}
	c.turns = slices.Clone(c.turns[drop:])
	clear(c.byID)
	for i, turn := range c.turns {
		c.byID[turn.TurnID] = i
	}
}

// externalTriggerNotice is why this instance's sends go as the plain external-event trigger, "" while they do not or
// it was not probed: botmux needs an upgrade, or it takes actor "owner" but no BotMux Owner Union ID is set.
func (h *botmuxHost) externalTriggerNotice() proto.BotmuxExternalTriggerReason {
	stamp := botmuxStamp(h.bin)
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.caps.matches(h.base, h.token, stamp) {
		return ""
	}
	return h.caps.notice(h.owner)
}

// notice is the external-trigger reason these capabilities give with owner as the BotMux Owner Union ID.
func (c botmuxCaps) notice(owner proto.BotmuxOwnerUnionID) proto.BotmuxExternalTriggerReason {
	switch {
	case !c.known:
		return ""
	case !c.ownerActor:
		return proto.BotmuxTriggerUpgrade
	case owner == "":
		return proto.BotmuxTriggerOwnerUnset
	}
	return ""
}

// setOwnerUnionID is the BotMux Owner Union ID the hub handed (node.configure botmux_owner_union_id), "" for none.
func (h *botmuxHost) setOwnerUnionID(id proto.BotmuxOwnerUnionID) {
	h.mu.Lock()
	h.owner = id
	h.mu.Unlock()
}

func (h *botmuxHost) ownerUnionID() proto.BotmuxOwnerUnionID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.owner
}

func (h *botmuxHost) refreshCapsAsync(sessionID string) {
	stamp := botmuxStamp(h.bin)
	now := time.Now()
	h.mu.Lock()
	if h.capsProbing || h.caps.valid(h.base, h.token, stamp, now) ||
		now.Before(h.capsProbeAfter[sessionID]) || h.turnsRefreshing[sessionID] {
		h.mu.Unlock()
		return
	}
	h.capsProbing = true
	h.mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), botmuxTurnsBGTimeout)
		defer cancel()
		caps := h.probeCaps(ctx, sessionID)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.capsProbing = false
		if caps.valid(h.base, h.token, stamp, time.Now()) {
			clear(h.capsProbeAfter)
			return
		}
		if h.capsProbeAfter == nil {
			h.capsProbeAfter = map[string]time.Time{}
		}
		for id, until := range h.capsProbeAfter {
			if !now.Before(until) {
				delete(h.capsProbeAfter, id)
			}
		}
		h.capsProbeAfter[sessionID] = time.Now().Add(botmuxTurnsUnknownBackoff)
	}()
}

func (h *botmuxHost) setCapsLocked(next botmuxCaps) (noticeChanged bool) {
	if !h.ownerUnechoed.at.IsZero() {
		if h.ownerUnechoed.active(next.base, next.token, next.stamp, next.at) {
			next.ownerActor, next.ownerUnionID = false, false
		} else {
			h.ownerUnechoed = botmuxOwnerUnechoed{}
		}
	}
	prevNotice := h.caps.notice(h.owner)
	h.caps = next
	if !next.turnsAPI {
		for _, cached := range h.turnsCache {
			if next.matches(cached.base, cached.token, cached.stamp) {
				cached.invalidateLocked(next.at)
			}
		}
	}
	return prevNotice != h.caps.notice(h.owner)
}

func (h *botmuxHost) noteOwnerActorUnechoed(base, token, stamp string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if base != h.base || token != h.token {
		return // the dashboard moved since the send: what that instance did says nothing about the current one
	}
	now := time.Now()
	h.ownerUnechoed = botmuxOwnerUnechoed{
		base:  base,
		token: token,
		stamp: stamp,
		at:    now,
	}
	h.setCapsLocked(botmuxCaps{
		known:    true,
		turnsAPI: h.caps.turnsAPI,
		base:     base,
		token:    token,
		stamp:    stamp,
		at:       now,
	})
}

// probeCaps checks what this BotMux instance supports using only the read-only GET /api/sessions/:id/turns endpoint.
// Never sends a real message and never retries a write.
func (h *botmuxHost) probeCaps(ctx context.Context, sessionID string) botmuxCaps {
	if sessionID == "" {
		return botmuxCaps{}
	}
	stamp := botmuxStamp(h.bin)
	now := time.Now()
	base, token, err := h.dashboard(ctx, false)
	if err != nil {
		return botmuxCaps{}
	}
	lastKnown := func() botmuxCaps {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.caps.matches(h.base, h.token, stamp) {
			return h.caps
		}
		return botmuxCaps{}
	}
	h.mu.Lock()
	if h.caps.valid(base, token, stamp, now) {
		c := h.caps
		h.mu.Unlock()
		return c
	}
	h.mu.Unlock()

	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			base, token, err = h.dashboard(ctx, true)
			if err != nil {
				return lastKnown()
			}
			h.mu.Lock()
			if h.caps.valid(base, token, stamp, time.Now()) {
				c := h.caps
				h.mu.Unlock()
				return c
			}
			h.mu.Unlock()
		}
		status, res, errCode, err := h.fetchTurnsPage(ctx, base, token, sessionID, "0", 1)
		if errors.Is(err, errBotmuxTurnsTooLarge) {
			return lastKnown()
		}
		stale := err != nil || status == http.StatusUnauthorized || status == http.StatusForbidden ||
			(status >= 300 && status < 400)
		if stale && attempt == 0 {
			continue
		}
		if err != nil {
			return lastKnown()
		}
		switch status {
		case http.StatusNotFound:
			// New BotMux returns 404 {"ok":false,"error":"unknown_session"} when the route exists
			// but the session is not currently owned by an online daemon; do not cache that as an old BotMux.
			if errCode == "unknown_session" {
				return lastKnown()
			}
			c := botmuxCaps{known: true, turnsAPI: false, ownerActor: false, base: base, token: token, stamp: stamp, at: time.Now()}
			h.mu.Lock()
			h.setCapsLocked(c)
			h.mu.Unlock()
			return c
		case http.StatusOK:
			supported := res.OK && res.Schema == transcript.BotmuxTurnsSchema
			c := botmuxCaps{known: true, turnsAPI: supported, base: base, token: token, stamp: stamp, at: time.Now()}
			if supported {
				c.ownerActor, c.ownerUnionID = botmuxCapsOf(res.Capabilities)
			}
			h.mu.Lock()
			h.setCapsLocked(c)
			c = h.caps
			h.mu.Unlock()
			return c
		default:
			if status == http.StatusUnauthorized || status == http.StatusForbidden || (status >= 300 && status < 400) {
				return botmuxCaps{}
			}
			return lastKnown()
		}
	}
	return botmuxCaps{}
}

// cachedSessionTurns returns already-cached structured turns for sessionID with no network or CLI calls.
func (h *botmuxHost) cachedSessionTurns(sessionID string) []transcript.BotmuxTurn {
	stamp := botmuxStamp(h.bin)
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cachedSessionTurnsLocked(sessionID, stamp)
}

func (h *botmuxHost) cachedSessionTurnsLocked(sessionID, stamp string) []transcript.BotmuxTurn {
	now := time.Now()
	h.pruneTurnsCacheLocked(now, sessionID)
	sc := h.turnsCache[sessionID]
	if sc != nil && now.Sub(sc.usedAt) >= botmuxTurnsCacheIdle {
		delete(h.turnsCache, sessionID) // idle too long: a later read starts from the first turn again
		return nil
	}
	if !h.usableTurnsLocked(sc, stamp) || len(sc.turns) == 0 {
		return nil
	}
	sc.usedAt = now
	return slices.Clone(sc.turns)
}

func (h *botmuxHost) usableTurnsLocked(cached *botmuxSessionTurnsCache, stamp string) bool {
	return cached != nil && !cached.invalid &&
		h.caps.matches(h.base, h.token, stamp) && h.caps.turnsAPI &&
		cached.base == h.base && cached.token == h.token && cached.stamp == stamp
}

// refreshSessionTurnsAsync triggers a deduplicated, short-timeout background fetch for sessionID
// when not within recency or error backoff windows. Never blocks the caller.
func (h *botmuxHost) refreshSessionTurnsAsync(parent context.Context, sessionID string, onChange func([]transcript.BotmuxTurn)) {
	if sessionID == "" {
		return
	}
	stamp := botmuxStamp(h.bin)
	now := time.Now()
	h.mu.Lock()
	if h.turnsRefreshing[sessionID] {
		h.mu.Unlock()
		return
	}
	cached := h.turnsCache[sessionID]
	stale := cached != nil && !h.usableTurnsLocked(cached, stamp)
	if !stale && (now.Before(h.turnsBackoffUntil) || now.Before(h.unknownBackoff[sessionID]) ||
		h.caps.valid(h.base, h.token, stamp, now) && !h.caps.turnsAPI ||
		cached != nil && now.Sub(cached.fetchedAt) < botmuxTurnsFetchMinInterval) {
		h.mu.Unlock()
		return
	}
	if h.turnsRefreshing == nil {
		h.turnsRefreshing = map[string]bool{}
	}
	h.turnsRefreshing[sessionID] = true
	var previous []transcript.BotmuxTurn
	if cached != nil {
		if len(cached.turns) > 0 {
			previous = slices.Clone(cached.turns)
		} else if len(cached.released) > 0 {
			previous = cached.released
		}
		if stale {
			delete(h.turnsCache, sessionID)
		}
	}
	h.mu.Unlock()

	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.turnsRefreshing, sessionID)
			h.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(parent, botmuxTurnsBGTimeout)
		defer cancel()
		h.sessionTurns(ctx, sessionID)
		if current := h.cachedSessionTurns(sessionID); !reflect.DeepEqual(previous, current) {
			onChange(previous)
		}
	}()
}

func (h *botmuxHost) backOffTurnsOnError(ctx context.Context, err error) {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return
	}
	h.mu.Lock()
	h.turnsBackoffUntil = time.Now().Add(botmuxTurnsErrorBackoff)
	h.mu.Unlock()
}

// sessionTurns returns the cached + incrementally fetched structured turns for sessionID.
// Returns (nil, capsChanged) when the BotMux instance does not support the endpoint or cannot be reached.
func (h *botmuxHost) sessionTurns(ctx context.Context, sessionID string) ([]transcript.BotmuxTurn, bool) {
	if sessionID == "" {
		return nil, false
	}
	stamp := botmuxStamp(h.bin)
	now := time.Now()
	h.mu.Lock()
	if now.Before(h.turnsBackoffUntil) || now.Before(h.unknownBackoff[sessionID]) {
		cached := h.cachedSessionTurnsLocked(sessionID, stamp)
		h.mu.Unlock()
		return cached, false
	}
	h.mu.Unlock()

	base, token, err := h.dashboard(ctx, false)
	if err != nil {
		h.backOffTurnsOnError(ctx, err)
		return h.cachedSessionTurns(sessionID), false
	}
	h.mu.Lock()
	if h.caps.valid(base, token, stamp, now) && !h.caps.turnsAPI {
		h.mu.Unlock()
		return nil, false
	}
	cursor := "0"
	if c := h.turnsCache[sessionID]; c != nil && now.Sub(c.usedAt) >= botmuxTurnsCacheIdle {
		delete(h.turnsCache, sessionID) // idle too long: fetch from the first turn again, not from its old cursor
	}
	if c := h.turnsCache[sessionID]; base == h.base && token == h.token && h.usableTurnsLocked(c, stamp) {
		if !c.fetchedAt.IsZero() && now.Sub(c.fetchedAt) < botmuxTurnsFetchMinInterval {
			var cached []transcript.BotmuxTurn
			if len(c.turns) > 0 {
				cached = slices.Clone(c.turns)
			}
			c.usedAt = now
			h.mu.Unlock()
			return cached, false
		}
		if c.cursor != "" {
			cursor = c.cursor
			if seq, err := strconv.ParseInt(cursor, 10, 64); err == nil && seq > 0 {
				cursor = strconv.FormatInt(seq-1, 10)
			}
		}
	}
	h.mu.Unlock()

	refreshed := false
	noticeChanged := false
	for page := 0; page < botmuxTurnsMaxPages; page++ {
		status, res, errCode, err := h.fetchTurnsPage(ctx, base, token, sessionID, cursor, botmuxTurnsPageSize)
		// Only refresh the dashboard CLI on explicit auth/redirect responses, never on transport errors.
		authStale := err == nil && (status == http.StatusUnauthorized || status == http.StatusForbidden ||
			(status >= 300 && status < 400))
		if authStale && !refreshed {
			refreshed = true
			base, token, err = h.dashboard(ctx, true)
			if err != nil {
				h.backOffTurnsOnError(ctx, err)
				break
			}
			cursor = "0"
			page--
			continue
		}
		if err != nil {
			h.backOffTurnsOnError(ctx, err)
			break
		}
		if status == http.StatusNotFound {
			h.mu.Lock()
			if errCode == "unknown_session" {
				if h.unknownBackoff == nil {
					h.unknownBackoff = map[string]time.Time{}
				}
				h.unknownBackoff[sessionID] = time.Now().Add(botmuxTurnsUnknownBackoff)
				h.mu.Unlock()
				break
			}
			if h.setCapsLocked(botmuxCaps{known: true, turnsAPI: false, ownerActor: false, base: base, token: token, stamp: stamp, at: time.Now()}) {
				noticeChanged = true
			}
			h.mu.Unlock()
			return nil, noticeChanged
		}
		if status != http.StatusOK {
			h.mu.Lock()
			h.turnsBackoffUntil = time.Now().Add(botmuxTurnsErrorBackoff)
			h.mu.Unlock()
			break
		}
		if !res.OK || res.Schema != transcript.BotmuxTurnsSchema {
			h.mu.Lock()
			if h.setCapsLocked(botmuxCaps{known: true, turnsAPI: false, ownerActor: false, base: base, token: token, stamp: stamp, at: time.Now()}) {
				noticeChanged = true
			}
			h.mu.Unlock()
			return nil, noticeChanged
		}

		h.mu.Lock()
		if base != h.base || token != h.token {
			h.mu.Unlock()
			break
		}
		h.turnsBackoffUntil = time.Time{}
		delete(h.unknownBackoff, sessionID)
		ownerActor, ownerUnionID := botmuxCapsOf(res.Capabilities)
		if h.setCapsLocked(botmuxCaps{
			known:        true,
			turnsAPI:     true,
			ownerActor:   ownerActor,
			ownerUnionID: ownerUnionID,
			base:         base,
			token:        token,
			stamp:        stamp,
			at:           time.Now(),
		}) {
			noticeChanged = true
		}
		if h.turnsCache == nil {
			h.turnsCache = map[string]*botmuxSessionTurnsCache{}
		}
		sc := h.turnsCache[sessionID]
		if !h.usableTurnsLocked(sc, stamp) {
			sc = &botmuxSessionTurnsCache{
				base:  base,
				token: token,
				stamp: stamp,
				byID:  map[string]int{},
			}
			h.turnsCache[sessionID] = sc
		}
		for _, turn := range res.Turns {
			if idx, ok := sc.byID[turn.TurnID]; ok {
				sc.turns[idx] = turn
			} else {
				sc.byID[turn.TurnID] = len(sc.turns)
				sc.turns = append(sc.turns, turn)
			}
		}
		sc.trimTurnsLocked()
		if len(res.Turns) > 0 && res.Next != "" {
			sc.cursor = res.Next
		}
		sc.fetchedAt = time.Now()
		sc.usedAt = sc.fetchedAt
		h.pruneTurnsCacheLocked(sc.usedAt, sessionID)
		h.mu.Unlock()

		if len(res.Turns) < botmuxTurnsPageSize || res.Next == "" || res.Next == cursor {
			break
		}
		cursor = res.Next
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cachedSessionTurnsLocked(sessionID, stamp), noticeChanged
}

func (h *botmuxHost) fetchTurnsPage(
	ctx context.Context, base, token, sessionID, since string, limit int,
) (status int, res transcript.BotmuxTurnsResponse, errCode string, err error) {
	u := fmt.Sprintf("%s/api/sessions/%s/turns?since=%s&limit=%d",
		base, url.PathEscape(sessionID), url.QueryEscape(since), limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, res, "", err
	}
	req.AddCookie(&http.Cookie{Name: "botmux_dashboard_token", Value: token})
	resp, err := h.client.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return 0, res, "", fmt.Errorf("botmux: the dashboard does not answer: %w", err)
		}
		return 0, res, "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, botmuxTurnsMaxBytes+1))
	if len(b) > botmuxTurnsMaxBytes {
		if h.logf != nil {
			h.logf("%v", errBotmuxTurnsTooLarge)
		}
		return resp.StatusCode, res, "", errBotmuxTurnsTooLarge
	}
	if err != nil {
		return resp.StatusCode, res, "", err
	}
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(b, &res); err != nil {
			return resp.StatusCode, res, "", err
		}
		return resp.StatusCode, res, "", nil
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &errBody)
	return resp.StatusCode, res, errBody.Error, nil
}

// botmuxSessionID identifies a BotMux transcript and reports whether its session can be refreshed.
func (n *Node) botmuxSessionID(sessionID string) (id string, live bool) {
	n.mu.Lock()
	agent, listed := n.agents[sessionID]
	pane := agent.PaneID
	if !listed {
		pane = n.lastPanes[sessionID]
	}
	n.mu.Unlock()
	if ref := host.ParsePane(pane); ref.Kind == host.Botmux {
		return ref.ID, listed && agent.AgentStatus != proto.StatusOffline
	}
	return "", false
}

// applyBotmuxTurns decorates BotMux wrapper items and overlays structured BotMux turn records for a BotMux session.
// Non-BotMux sessions return items untouched with no decoration and no network calls.
func (n *Node) applyBotmuxTurns(ctx context.Context, sessionID string, f *transcript.File, items []proto.Item, afterSeq bool) []proto.Item {
	ctx, cancel := context.WithTimeout(ctx, botmuxTurnsReadTimeout)
	defer cancel()
	return n.applyBotmux(ctx, sessionID, f, items, false, afterSeq)
}

// applyBotmuxTurnsCached is the non-blocking variant for live pushItems: it uses only cached structured turns
// and triggers a bounded asynchronous refresh without holding up the session push lock.
func (n *Node) applyBotmuxTurnsCached(sessionID string, f *transcript.File, items []proto.Item) []proto.Item {
	return n.applyBotmux(context.Background(), sessionID, f, items, true, true)
}

func (n *Node) applyBotmux(ctx context.Context, sessionID string, f *transcript.File, items []proto.Item, cacheOnly, replacePrefix bool) []proto.Item {
	if len(items) == 0 {
		return items
	}
	bmxSID, live := n.botmuxSessionID(sessionID)
	if bmxSID == "" {
		return items
	}
	var turns []transcript.BotmuxTurn
	if h, ok := n.hosts.Get(host.Botmux).(*botmuxHost); ok {
		if cacheOnly || !live {
			turns = h.cachedSessionTurns(bmxSID)
		} else {
			turns, _ = h.sessionTurns(ctx, bmxSID)
		}
	}
	var prefix, suffix []proto.Item
	if f != nil {
		prefix, suffix = f.TurnWindow(items[0].Seq, items[len(items)-1].Seq)
	}
	transcriptID := n.botmuxTranscriptID(sessionID)
	if cacheOnly {
		suffix = nil
	}
	if len(prefix) > 0 || len(suffix) > 0 {
		combined := make([]proto.Item, 0, len(prefix)+len(items)+len(suffix))
		combined = append(combined, prefix...)
		combined = append(combined, items...)
		combined = append(combined, suffix...)
		combined = decorateBotmuxTurns(combined, turns, transcriptID)
		if replacePrefix && len(prefix) > 0 && slices.ContainsFunc(items, func(item proto.Item) bool {
			return item.Kind == proto.KindToolResult
		}) {
			before := decorateBotmuxTurns(prefix, turns, transcriptID)
			changed := botmuxChangedItems(before, combined)
			for index, item := range changed {
				if item.Seq >= items[0].Seq {
					changed = changed[:index]
					break
				}
			}
			if cacheOnly {
				changed = f.WithImages(changed)
			}
			return append(changed, combined[len(prefix):len(prefix)+len(items)]...)
		}
		return combined[len(prefix) : len(prefix)+len(items)]
	}
	return decorateBotmuxTurns(items, turns, transcriptID)
}

func (n *Node) botmuxTranscriptID(sessionID string) string {
	if source, ok := n.codexSource(sessionID); ok {
		return source.thread
	}
	return sessionID
}

func decorateBotmuxTurns(items []proto.Item, turns []transcript.BotmuxTurn, transcriptID string) []proto.Item {
	return transcript.ApplyBotmuxTurns(transcript.DecorateBotmux(items), turns, transcriptID)
}

func (n *Node) refreshWatchedBotmux(w *watch) {
	if w == nil || w.ctx == nil || w.ctx.Err() != nil || n.peer == nil {
		return
	}
	bmxSID, live := n.botmuxSessionID(w.id)
	if !live || bmxSID == "" {
		return
	}
	if h, ok := n.hosts.Get(host.Botmux).(*botmuxHost); ok {
		h.refreshSessionTurnsAsync(w.ctx, bmxSID, func(previous []transcript.BotmuxTurn) {
			mu := n.pushLock(w.id)
			mu.Lock()
			defer mu.Unlock()
			n.mu.Lock()
			watched := n.watches[w.id] == w
			n.mu.Unlock()
			current, live := n.botmuxSessionID(w.id)
			if !watched || w.ctx.Err() != nil || !live || current != bmxSID {
				return
			}
			items, history := w.f.LoadedItems()
			transcriptID := n.botmuxTranscriptID(w.id)
			before := decorateBotmuxTurns(items, previous, transcriptID)
			after := decorateBotmuxTurns(items, h.cachedSessionTurns(bmxSID), transcriptID)
			changed := botmuxChangedItems(before, after)
			if len(changed) > 0 {
				agentUsage(w.f, changed)
				changed = n.echoed(w.id, w.f, w.f.WithImages(changed))
				_ = n.peer.Notify("session.items", map[string]any{
					"session_id": w.id, "items": changed, "history": history,
				})
			}
		})
	}
}

// When an updated reply was folded, resend the entire run so replacing the fold preserves its other items.
func botmuxChangedItems(before, after []proto.Item) []proto.Item {
	changed := make([]bool, len(after))
	hasChanges := false
	for index := range before {
		if !reflect.DeepEqual(before[index].Botmux, after[index].Botmux) {
			changed[index] = true
			hasChanges = true
		}
	}
	if !hasChanges {
		return nil
	}
	var out []proto.Item
	index := 0
	for _, item := range transcript.FoldWork(after) {
		end := index + 1
		if item.Fold != nil {
			end = index + item.Fold.Items
		}
		if slices.Contains(changed[index:end], true) {
			out = append(out, after[index:end]...)
		}
		index = end
	}
	return out
}
