package assistant

import (
	"context"
	"sync"

	"github.com/nwverse/persona/server/internal/identity"
)

// Machine trust comes from the manager's device configuration, never node.info or an MCP hint.
func (s *Service) isWorkMachine(machine string) bool {
	if s.devs != nil {
		machine = s.physicalDevices().Canonical(machine)
		for _, d := range s.devs.List() {
			if d.ID == machine && d.Trust == "work" {
				return true
			}
		}
	}
	for _, id := range s.hello(context.Background()).Assistant.WorkMachines {
		if s.sameMachine(machine, id) {
			return true
		}
	}
	return false
}

func (s *Service) sameMachine(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if s.devs != nil {
		return s.physicalSameMachine(a, b)
	}
	return a == b
}

// A new runtime can request tools or context before it has a session ID or appears in the node's list.
// Only manager-initiated starts grant that bootstrap on an explicitly selected personal machine, and
// each in-flight start also records its target space so an older node's first assistant.context call
// never leaks the main chat's snapshot into a space conversation (#17557).
type assistantStarts struct {
	mu        sync.Mutex
	machines  map[string]int
	spaces    map[string]map[string]int
	bySession map[[2]string]map[string]int
}

func (s *Service) beginAssistantStart(machine string) func() {
	return s.beginAssistantStartSpace(machine, "", "")
}

func (s *Service) beginAssistantStartSpace(machine, space, sessionID string) func() {
	if s.devs != nil {
		machine = s.devs.Canonical(machine)
	}
	b := &s.startingAssistants
	b.mu.Lock()
	if b.machines == nil {
		b.machines = map[string]int{}
	}
	b.machines[machine]++
	if b.spaces == nil {
		b.spaces = map[string]map[string]int{}
	}
	if b.spaces[machine] == nil {
		b.spaces[machine] = map[string]int{}
	}
	b.spaces[machine][space]++
	var sk [2]string
	if sessionID != "" {
		sk = [2]string{machine, sessionID}
		if b.bySession == nil {
			b.bySession = map[[2]string]map[string]int{}
		}
		if b.bySession[sk] == nil {
			b.bySession[sk] = map[string]int{}
		}
		b.bySession[sk][space]++
	}
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.machines[machine]--
			if b.machines[machine] == 0 {
				delete(b.machines, machine)
			}
			if m := b.spaces[machine]; m != nil {
				m[space]--
				if m[space] == 0 {
					delete(m, space)
				}
				if len(m) == 0 {
					delete(b.spaces, machine)
				}
			}
			if sessionID != "" {
				if m := b.bySession[sk]; m != nil {
					m[space]--
					if m[space] == 0 {
						delete(m, space)
					}
					if len(m) == 0 {
						delete(b.bySession, sk)
					}
				}
			}
			b.mu.Unlock()
		})
	}
}

// startingSpace reports the target space of an in-flight start on machine (and sessionID when pre-assigned).
// known is true when all matching in-flight starts agree on one space ("" for main space); ambiguous is true when
// concurrent starts on machine span different spaces so an older node without cwd must not receive the main snapshot.
func (s *Service) startingSpace(machine, sessionID string) (space string, known, ambiguous bool) {
	if machine == "" {
		return "", false, false
	}
	if s.devs != nil {
		machine = s.devs.Canonical(machine)
	}
	b := &s.startingAssistants
	b.mu.Lock()
	defer b.mu.Unlock()
	if sessionID != "" {
		if m := b.bySession[[2]string{machine, sessionID}]; len(m) == 1 {
			for sp := range m {
				return sp, true, false
			}
		} else if len(m) > 1 {
			return "", false, true
		}
	}
	m := b.spaces[machine]
	if len(m) == 0 {
		return "", false, false
	}
	if len(m) == 1 {
		for sp := range m {
			return sp, true, false
		}
	}
	return "", false, true
}

// assistantCaller checks the connection's machine before considering the session. The node's
// assistant flag selects the tool set; it does not grant access to it. Unknown sessions on the
// configured machines are permitted during startup (including Codex's temporary proc-* IDs).
func (s *Service) assistantCaller(ctx context.Context, machine, session string) bool {
	if machine == "" || s.isWorkMachine(machine) {
		return false
	}
	cfg := s.settings(ctx)
	allowed := s.sameMachine(machine, cfg.MachineID) ||
		s.sameMachine(machine, s.hello(ctx).Models.InboxAgentMachine)
	b := &s.startingAssistants
	id := machine
	if s.devs != nil {
		id = s.devs.Canonical(id)
	}
	b.mu.Lock()
	allowed = allowed || b.machines[id] > 0
	b.mu.Unlock()
	startedHere := false
	if session != "" {
		s.origins.mu.Lock()
		if s.originsLocked(ctx) {
			if o := s.origins.m[session]; o != nil && o.By == originByStart && s.sameMachine(machine, o.MachineID) {
				allowed = true
				startedHere = true
			}
		}
		s.origins.mu.Unlock()
		if !allowed && s.isBackgroundCallerOn(ctx, machine, session) {
			allowed = true
		}
	}
	if !allowed {
		return false
	}
	if _, guest, err := s.guestOf(ctx, machine, session); err != nil {
		return false
	} else if guest {
		return true
	} // its tool whitelist and no-context rule are enforced separately
	if sess, known := s.nodeSession(machine, session); known {
		if sess.PaneID != "" {
			return false
		}
		if sess.Cwd != "" {
			m, ok := s.fleetMachine(machine)
			if ok && m.isAssistant(sess) {
				return true
			}
			// A background supervisor or its forked round branch (no host pane) keeps its assistant tool
			// authorization even if its listed cwd drifted (e.g. after a Bash cd or forked transcript).
			if s.isBackgroundCallerOn(ctx, machine, session) {
				return true
			}
			// A headless conversation started by this manager whose listed cwd is still its assistant dir
			// stays authorized even before the node's home directory is populated on fleetMachine.
			return startedHere && identity.IsAssistantDirOf(sess.Cwd, s.id)
		}
	}
	return true
}

// Runtime tool discovery has no session ID, even when reconnecting an existing assistant.
// A machine the manager has started an assistant on can rediscover the schema; calls still
// require assistantCaller and the recorded session. Backfilled node claims grant neither.
func (s *Service) assistantDiscovery(ctx context.Context, machine, session string) bool {
	if s.assistantCaller(ctx, machine, session) {
		return true
	}
	if session != "" || machine == "" || s.isWorkMachine(machine) {
		return false
	}
	s.origins.mu.Lock()
	defer s.origins.mu.Unlock()
	if !s.originsLocked(ctx) {
		return false
	}
	for _, o := range s.origins.m {
		if o.By == originByStart && s.sameMachine(machine, o.MachineID) {
			return true
		}
	}
	return false
}
