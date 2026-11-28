package assistant

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// todosV2QuietWindow is a parsed 24-hour local-time quiet range [startMin, endMin) in minutes since midnight (#16433).
// When startMin > endMin the range spans midnight (e.g. "22:00-08:00").
type todosV2QuietWindow struct {
	startMin int
	endMin   int
}

// parseTodosV2QuietHours parses a strict "HH:MM-HH:MM" 24-hour range with distinct start and end times.
func parseTodosV2QuietHours(raw string) (todosV2QuietWindow, bool) {
	raw = strings.TrimSpace(raw)
	if len(raw) != 11 || raw[2] != ':' || raw[5] != '-' || raw[8] != ':' {
		return todosV2QuietWindow{}, false
	}
	sh, ok1 := parseTwoDigits(raw[0], raw[1])
	sm, ok2 := parseTwoDigits(raw[3], raw[4])
	eh, ok3 := parseTwoDigits(raw[6], raw[7])
	em, ok4 := parseTwoDigits(raw[9], raw[10])
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return todosV2QuietWindow{}, false
	}
	if sh > 23 || sm > 59 || eh > 23 || em > 59 {
		return todosV2QuietWindow{}, false
	}
	startMin := sh*60 + sm
	endMin := eh*60 + em
	if startMin == endMin {
		return todosV2QuietWindow{}, false
	}
	return todosV2QuietWindow{startMin: startMin, endMin: endMin}, true
}

func parseTwoDigits(a, b byte) (int, bool) {
	if a < '0' || a > '9' || b < '0' || b > '9' {
		return 0, false
	}
	return int(a-'0')*10 + int(b-'0'), true
}

func (w todosV2QuietWindow) contains(t time.Time) bool {
	cur := t.Hour()*60 + t.Minute()
	if w.startMin < w.endMin {
		return cur >= w.startMin && cur < w.endMin
	}
	return cur >= w.startMin || cur < w.endMin
}

type todosV2AttentionState struct {
	mu         sync.Mutex
	lastPushBy map[string]time.Time
	nowFn      func() time.Time
}

// todosV2AttentionRegistry isolates per-Service digest-window state without widening Service fields,
// and cleans up entries automatically when a Service is garbage-collected.
var todosV2AttentionRegistry = struct {
	mu    sync.Mutex
	bySvc map[weak.Pointer[Service]]*todosV2AttentionState
}{
	bySvc: make(map[weak.Pointer[Service]]*todosV2AttentionState),
}

func (s *Service) todosV2AttentionState(create bool) *todosV2AttentionState {
	if s == nil {
		return nil
	}
	wp := weak.Make(s)
	todosV2AttentionRegistry.mu.Lock()
	defer todosV2AttentionRegistry.mu.Unlock()
	if st, ok := todosV2AttentionRegistry.bySvc[wp]; ok {
		return st
	}
	if !create {
		return nil
	}
	st := &todosV2AttentionState{
		lastPushBy: make(map[string]time.Time),
	}
	todosV2AttentionRegistry.bySvc[wp] = st
	runtime.AddCleanup(s, func(key weak.Pointer[Service]) {
		todosV2AttentionRegistry.mu.Lock()
		delete(todosV2AttentionRegistry.bySvc, key)
		todosV2AttentionRegistry.mu.Unlock()
	}, wp)
	return st
}

func (s *Service) setTodosV2AttentionClock(fn func() time.Time) {
	if st := s.todosV2AttentionState(true); st != nil {
		st.mu.Lock()
		st.nowFn = fn
		st.mu.Unlock()
	}
}

func (s *Service) todosV2AttentionNow() time.Time {
	if st := s.todosV2AttentionState(false); st != nil {
		st.mu.Lock()
		fn := st.nowFn
		st.mu.Unlock()
		if fn != nil {
			return fn()
		}
	}
	return time.Now()
}

func (s *Service) todosV2AttentionBucket(n Notification) string {
	if sid := strings.TrimSpace(n.SessionID); sid != "" {
		return "session:" + sid
	}
	if did := strings.TrimSpace(n.DeviceID); did != "" {
		return "device:" + did
	}
	if sub := strings.TrimSpace(n.Subtitle); sub != "" {
		return "subtitle:" + sub
	}
	return "global"
}

// applyTodosV2PushAttention applies user-configured todos_v2 quiet-hours and digest-window downgrades (#16433).
// Cards, approvals, explicit notify_user pushes (Asked), and LevelP0 pushes are strictly immune and never downgraded.
func (s *Service) applyTodosV2PushAttention(ctx context.Context, n *Notification) {
	if s == nil || n == nil {
		return
	}
	cfg := s.settings(ctx)
	if !cfg.todosV2On() {
		return
	}
	if cardPush(*n) || n.CardID != "" || n.Asked || n.Level == assistantwire.LevelP0 {
		return
	}
	now := s.todosV2AttentionNow()
	if qh := cfg.todosV2QuietHours(); qh != "" {
		if win, ok := parseTodosV2QuietHours(qh); ok {
			if win.contains(now.In(zoneOf(cfg.Timezone))) {
				n.Level = cappedLevel(n.Kind, n.Level, assistantwire.LevelP2)
				return
			}
		}
	}
	if winMin := cfg.todosV2DigestWindowMinutes(); winMin > 0 {
		st := s.todosV2AttentionState(true)
		bucket := s.todosV2AttentionBucket(*n)
		window := time.Duration(winMin) * time.Minute
		st.mu.Lock()
		last, seen := st.lastPushBy[bucket]
		if seen && !now.Before(last) && now.Sub(last) < window {
			st.mu.Unlock()
			n.Level = cappedLevel(n.Kind, n.Level, assistantwire.LevelP2)
			return
		}
		st.lastPushBy[bucket] = now
		if len(st.lastPushBy) > 512 {
			for k, ts := range st.lastPushBy {
				if now.Sub(ts) >= 24*time.Hour {
					delete(st.lastPushBy, k)
				}
			}
		}
		st.mu.Unlock()
	}
}
