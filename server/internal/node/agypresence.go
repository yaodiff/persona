package node

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// An agy in a terminal holds an exclusive flock on <agy dir>/presence/<conversation>.lock for as long as it has that
// conversation open; the companion's `agy acp` takes none. The file stays behind when agy exits, so the file alone does
// not prove a terminal holds it. A message the companion took for such a conversation ran a second agy on it: the
// terminal never saw it, and the companion's turn overwrote the stored steps from where it had last loaded them (#13603).
//
// Probing the lock by taking flock (even LOCK_SH|LOCK_NB) races with a starting agy CLI: agy tries LOCK_EX|LOCK_NB once
// and never retries, so a momentary shared lock leaves that agy session without its presence lock (#14561). The node
// therefore never calls flock on the presence file: on Linux it reads /proc/locks for the file's device and inode, and
// where no lock table can be read it falls back to the presence file plus recent transcript writes by another process.

// agyPresenceSource is where a herdr-core terminal's agy conversation comes from: the presence lock its agy holds open.
const agyPresenceSource = "herdr-core:agy-presence"

const (
	// agyPresenceActiveWindow is how long a recent transcript write by another process treats a presence-file
	// conversation as open when no lock table can be read (matches the Antigravity companion's cliActiveMs).
	agyPresenceActiveWindow = time.Minute
	// agyExportSettleWindow is how long after the companion finishes work on a conversation its own transcript export
	// may still update the transcript file (matches the Antigravity companion's exportSettleTimeoutMs).
	agyExportSettleWindow = 15 * time.Second
)

// agyLockState is what the node can tell about presence/<conversation>.lock without taking flock.
type agyLockState uint8

const (
	agyLockMissing agyLockState = iota // presence file does not exist
	agyLockHeld                        // lock table confirms a held FLOCK WRITE lock on the presence file
	agyLockFree                        // lock table was read and shows no held FLOCK WRITE lock on it
	agyLockUnknown                     // presence file exists, but no lock table can be read
)

// agyPresenceOf is the conversation whose presence lock the process pid holds open, else "" (none, several, or a
// system the node cannot look into). A variable so tests can stand in for /proc.
var agyPresenceOf = agyPresenceLocks

// agyPresenceLockProbe inspects presence/<conversation>.lock without taking flock. A variable so tests can cover all
// lock-table states and fallback paths deterministically.
var agyPresenceLockProbe = agyPresenceLockState

// agyPresenceNow is the clock for presence transcript-write windows; tests override it to avoid sleeping.
var agyPresenceNow = time.Now

// agyConversationID extracts the UUID from the companion's agy-<conversation> session ID.
func agyConversationID(id string) (string, bool) {
	conversation, ok := strings.CutPrefix(id, agyAgent+"-")
	if !ok || !uuidRe.MatchString(conversation) {
		return "", false
	}
	return conversation, true
}

// agyPresenceConversation picks the conversation from a process's open files: exactly one presence/<uuid>.lock.
func agyPresenceConversation(files []string) string {
	found := ""
	for _, f := range files {
		dir, name := filepath.Split(f)
		id, ok := strings.CutSuffix(name, ".lock")
		if !ok || filepath.Base(filepath.Clean(dir)) != "presence" || !uuidRe.MatchString(id) {
			continue
		}
		if found != "" && found != id {
			return ""
		}
		found = id
	}
	return found
}

// agyPresenceSession is what a herdr-core terminal running agy reports about its conversation, nil when unknown.
func agyPresenceSession(pid int) *host.AgentSession {
	if pid <= 0 {
		return nil
	}
	if id := agyPresenceOf(pid); id != "" {
		return &host.AgentSession{Source: agyPresenceSource, Agent: agyAgent, Kind: "id", Value: id}
	}
	return nil
}

// agyPresenceScanSupported reports whether this platform (Linux /proc/<pid>/fd) or a test stub can inspect foreground
// process file descriptors for agy presence locks without extra host RPCs on platforms where it always returns "".
func agyPresenceScanSupported() bool {
	return runtime.GOOS == "linux" || reflect.ValueOf(agyPresenceOf).Pointer() != reflect.ValueOf(agyPresenceLocks).Pointer()
}

// agyPresenceForPane inspects the foreground processes of a classic herdr or Orca pane for an open
// presence/<uuid>.lock, mirroring herdr-core's withAgyPresence (#13603). Returns nil when zero or multiple
// conversations are found.
func agyPresenceForPane(ctx context.Context, h host.Host, pane string) *host.AgentSession {
	if h == nil || pane == "" || !agyPresenceScanSupported() {
		return nil
	}
	pctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	pids, err := h.PaneProcesses(pctx, host.ParsePane(pane))
	cancel()
	if err != nil || len(pids) == 0 {
		return nil
	}
	found := ""
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		id := agyPresenceOf(pid)
		if id == "" || !uuidRe.MatchString(id) {
			continue
		}
		if found != "" && found != id {
			return nil
		}
		found = id
	}
	if found == "" {
		return nil
	}
	return &host.AgentSession{Source: agyPresenceSource, Agent: agyAgent, Kind: "id", Value: found}
}

// agyPresenceHosts are the engines whose agy panes enrichAgyPresence names from the presence lock: their
// PaneProcesses list the pane's agy. herdr-core names its own (withAgyPresence).
var agyPresenceHosts = map[string]bool{host.Herdr: true, host.Orca: true}

// enrichAgyPresence resolves classic herdr and Orca agy panes from their foreground process presence lock when neither
// herdr's antigravity_cli integration nor Persona's own agy hook has reported a conversation for the pane (e.g.
// integration missing, agent_session replaced by custom:persona, hooks opted out, or after a node restart). Without
// it an Orca pane's conversation stayed unknown, so a message to it met the lock with no pane and was refused (#16531).
func (n *Node) enrichAgyPresence(ctx context.Context, h host.Host, agents []host.Agent) {
	if h == nil || !agyPresenceHosts[h.Kind()] || !agyPresenceScanSupported() {
		return
	}
	for i := range agents {
		a := agents[i]
		if a.Agent != agyAgent || a.PaneID == "" || reportedAgyConversation(a) != "" {
			continue
		}
		n.mu.Lock()
		hasHook := n.agyReports[a.PaneID].conversation != ""
		n.mu.Unlock()
		if hasHook {
			continue
		}
		if sess := agyPresenceForPane(ctx, h, a.PaneID); sess != nil {
			agents[i].AgentSession = sess
		}
	}
}

// procLocksHasDevIno reports whether a Linux /proc/locks snapshot contains a held FLOCK WRITE lock on the inode
// identified by (major, minor, ino). Blocked waiter lines ("->") are ignored because a waiter does not hold the lock.
func procLocksHasDevIno(data []byte, major, minor uint32, ino uint64) bool {
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		fields := strings.Fields(string(raw))
		// Linux fs/locks.c prints:
		//   "<id>: FLOCK ADVISORY WRITE <pid> <major_hex>:<minor_hex>:<ino_dec> <start> <end>"
		// Waiter lines insert "->" as fields[1], so checking fields[1] == "FLOCK" skips them.
		if len(fields) < 8 {
			continue
		}
		if !strings.HasSuffix(fields[0], ":") || fields[1] != "FLOCK" || fields[3] != "WRITE" {
			continue
		}
		if _, err := strconv.ParseUint(strings.TrimSuffix(fields[0], ":"), 10, 64); err != nil {
			continue
		}
		if _, err := strconv.ParseInt(fields[4], 10, 64); err != nil {
			continue
		}
		parts := strings.Split(fields[5], ":")
		if len(parts) != 3 {
			continue
		}
		maj, err1 := strconv.ParseUint(parts[0], 16, 32)
		min, err2 := strconv.ParseUint(parts[1], 16, 32)
		inode, err3 := strconv.ParseUint(parts[2], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		if uint32(maj) == major && uint32(min) == minor && inode == ino {
			return true
		}
	}
	return false
}

// agyTranscriptWrittenAt returns the latest modification time across a conversation's exported transcripts, or zero
// when neither exists.
func agyTranscriptWrittenAt(dir, conversation string) time.Time {
	var latest time.Time
	for _, rel := range []string{agyFullTranscriptPath, agyTranscriptPath} {
		info, err := os.Stat(filepath.Join(dir, "brain", conversation, rel))
		if err != nil || info.IsDir() {
			continue
		}
		if mt := info.ModTime(); mt.After(latest) {
			latest = mt
		}
	}
	return latest
}

// agyPresenceHeld reports whether an agy in a terminal appears to hold conversation open under dir without acquiring
// flock on presence/<conversation>.lock (#14561).
func agyPresenceHeld(dir, conversation string) bool {
	return agyPresenceHeldWith(dir, conversation, nil)
}

func agyPresenceHeldWith(dir, conversation string, companionOwns func(writtenAt, now time.Time) bool) bool {
	if dir == "" || conversation == "" {
		return false
	}
	switch agyPresenceLockProbe(filepath.Join(dir, "presence", conversation+".lock")) {
	case agyLockMissing, agyLockFree:
		return false
	case agyLockHeld:
		return true
	case agyLockUnknown:
		writtenAt := agyTranscriptWrittenAt(dir, conversation)
		if writtenAt.IsZero() {
			return false
		}
		now := agyPresenceNow()
		if now.Sub(writtenAt) >= agyPresenceActiveWindow {
			return false
		}
		if companionOwns != nil && companionOwns(writtenAt, now) {
			return false
		}
		return true
	default:
		return false
	}
}

// agyCompanionTurn tracks the node's own driving work through the Antigravity companion for one conversation so that,
// where no kernel lock table can be read, the companion's own turns and settling transcript exports are not mistaken
// for an external terminal agy writing the transcript.
type agyCompanionTurn struct {
	inFlight    int
	active      bool
	sawWorking  bool
	ownerSerial int
	seq         uint64
	endedAt     time.Time
}

func agyMethodWritesTranscript(method string) bool {
	switch method {
	case "session.send", proto.MethodSessionSendUnseen, "session.queue.send_now",
		"session.interrupt", "session.fork", "session.rewind":
		return true
	}
	return false
}

// runtimeSessionWorkingLocked reports whether h currently lists id as working or blocked. Callers hold b.mu (never h.mu).
func runtimeSessionWorkingLocked(h *runtimeConn, id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, s := range h.sessions {
		if s.SessionID == id {
			return s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked
		}
	}
	return false
}

// agyWorkBegin records an in-flight driving call to the Antigravity companion for session id; the returned finish func
// must be called with the call's error (and for session.interrupt, whether the companion reported settled: true).
func (b *runtimeRegistry) agyWorkBegin(h *runtimeConn, method, id string) func(err error, settled bool) {
	if b == nil || h == nil || (!agyMethodWritesTranscript(method) && method != "session.close") {
		return func(error, bool) {}
	}
	if h.agent() != agyAgent {
		return func(error, bool) {}
	}
	conversation, ok := agyConversationID(id)
	if !ok {
		return func(error, bool) {}
	}
	b.mu.Lock()
	if b.agyWork == nil {
		b.agyWork = map[string]*agyCompanionTurn{}
	}
	e := b.agyWork[conversation]
	if e == nil {
		e = &agyCompanionTurn{}
		b.agyWork[conversation] = e
	}
	e.seq++
	callSeq := e.seq
	if agyMethodWritesTranscript(method) {
		e.inFlight++
	}
	b.mu.Unlock()

	return func(err error, settled bool) {
		now := agyPresenceNow()
		b.mu.Lock()
		defer b.mu.Unlock()
		e := b.agyWork[conversation]
		if e == nil {
			return
		}
		if agyMethodWritesTranscript(method) && e.inFlight > 0 {
			e.inFlight--
		}
		if err == nil {
			switch method {
			case "session.send", proto.MethodSessionSendUnseen, "session.queue.send_now":
				// Preserve sawWorking if the turn was already confirmed working on this connection, or if the
				// companion already pushed StatusWorking/StatusBlocked while session.send was in flight.
				wasWorking := e.active && e.sawWorking && e.ownerSerial == h.serial
				e.active = true
				e.ownerSerial = h.serial
				e.sawWorking = wasWorking || runtimeSessionWorkingLocked(h, id)
				e.endedAt = now
			case "session.interrupt":
				e.endedAt = now
				// Only clear active when the companion confirmed the turn settled and no newer call superseded it.
				if settled && callSeq == e.seq {
					e.active = false
					e.sawWorking = false
				}
			case "session.close":
				if callSeq == e.seq {
					e.active = false
					e.sawWorking = false
				}
			default: // session.fork, session.rewind
				e.endedAt = now
			}
		}
		b.pruneAgyWorkLocked(now)
	}
}

// agyWorkDelta records live streaming output from the Antigravity companion's own turn.
func (b *runtimeRegistry) agyWorkDelta(h *runtimeConn, id string) {
	if b == nil || h == nil || h.agent() != agyAgent {
		return
	}
	conversation, ok := agyConversationID(id)
	if !ok {
		return
	}
	now := agyPresenceNow()
	b.mu.Lock()
	if b.agyWork == nil {
		b.agyWork = map[string]*agyCompanionTurn{}
	}
	e := b.agyWork[conversation]
	if e == nil {
		e = &agyCompanionTurn{}
		b.agyWork[conversation] = e
	}
	e.endedAt = now
	b.pruneAgyWorkLocked(now)
	b.mu.Unlock()
}

// agyWorkObserveSessionsLocked updates active companion turns from a runtime.sessions list. Callers hold b.mu.
func (b *runtimeRegistry) agyWorkObserveSessionsLocked(h *runtimeConn, agent string, list []proto.Session, now time.Time) {
	if agent != agyAgent || len(b.agyWork) == 0 {
		return
	}
	statusByConv := make(map[string]string, len(list))
	for _, s := range list {
		if conv, ok := agyConversationID(s.SessionID); ok {
			statusByConv[conv] = s.Status
		}
	}
	for conv, e := range b.agyWork {
		if !e.active || e.ownerSerial != h.serial {
			continue
		}
		// Expire an unconfirmed fast turn before inspecting StatusWorking so it can never later latch onto an external
		// terminal agy's working status.
		if !e.sawWorking && e.inFlight == 0 && now.Sub(e.endedAt) > agyExportSettleWindow {
			e.active = false
			continue
		}
		status, listed := statusByConv[conv]
		if listed && (status == proto.StatusWorking || status == proto.StatusBlocked) {
			e.sawWorking = true
			e.endedAt = now
			continue
		}
		if e.sawWorking || !listed {
			e.active = false
			e.sawWorking = false
			e.endedAt = now
		}
	}
	b.pruneAgyWorkLocked(now)
}

// agyWorkRemoveConnLocked clears active turns owned by a disconnecting companion connection. Callers hold b.mu.
func (b *runtimeRegistry) agyWorkRemoveConnLocked(h *runtimeConn, now time.Time) {
	for _, e := range b.agyWork {
		if e.active && e.ownerSerial == h.serial {
			e.active = false
			e.sawWorking = false
			e.endedAt = now
		}
	}
}

func (b *runtimeRegistry) pruneAgyWorkLocked(now time.Time) {
	if len(b.agyWork) <= maxStarted {
		return
	}
	cutoff := agyPresenceActiveWindow + agyExportSettleWindow
	for conv, e := range b.agyWork {
		if e.active && !e.sawWorking && e.inFlight == 0 && now.Sub(e.endedAt) > agyExportSettleWindow {
			e.active = false
		}
		if e.inFlight == 0 && !e.active && (e.endedAt.IsZero() || now.Sub(e.endedAt) > cutoff) {
			delete(b.agyWork, conv)
		}
	}
}

// agyCompanionOwnsWrite reports whether a transcript write at writtenAt is attributable to the companion's own active
// turn or its settling export window.
func (b *runtimeRegistry) agyCompanionOwnsWrite(conversation string, writtenAt, now time.Time) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.agyWork[conversation]
	if e == nil {
		return false
	}
	if e.inFlight > 0 {
		return true
	}
	if e.active {
		if e.sawWorking || now.Sub(e.endedAt) <= agyExportSettleWindow {
			return true
		}
		e.active = false
	}
	return !e.endedAt.IsZero() && !writtenAt.After(e.endedAt.Add(agyExportSettleWindow))
}

// agyDrives: the calls that make the companion run agy on a conversation (or change it), as opposed to reading it.
func agyDrives(method string) bool {
	return !awayReads[method] && method != "session.unwatch"
}

// codeAgyInTerminal refuses a call that would drive a conversation an agy in a terminal has open.
const codeAgyInTerminal = "agy_in_terminal"

// agyOpenCall is agyOpenIn for a call that drives the session; reads stay the companion's.
func (n *Node) agyOpenCall(method, id string) (pane string, a host.Agent, held bool) {
	if !agyDrives(method) {
		return "", host.Agent{}, false
	}
	return n.agyOpenIn(id)
}

// agyOpenIn is where the companion's session agy-<conversation> is open, for a call that would drive it: the live pane
// running it (its session id and agent), else held when an agy outside any pane the node knows holds its presence lock.
func (n *Node) agyOpenIn(id string) (pane string, a host.Agent, held bool) {
	conversation, ok := agyConversationID(id)
	if !ok {
		return "", host.Agent{}, false
	}
	live := map[string]host.Agent{}
	n.mu.Lock()
	for candidate, ag := range n.agents {
		if ag.Agent == agyAgent && ag.AgentStatus != proto.StatusOffline && ag.AgentStatus != proto.StatusInterrupted {
			live[candidate] = ag
		}
	}
	n.mu.Unlock()
	for candidate, ag := range live { // agyConversation takes n.mu itself and may read the transcript
		if n.agyConversation(candidate, ag.PaneID) == conversation {
			pane, a = candidate, ag
			break
		}
	}
	if pane != "" {
		return pane, a, true
	}
	return "", host.Agent{}, agyPresenceHeldWith(n.agyRootFor(conversation), conversation, func(writtenAt, now time.Time) bool {
		return n.runtimes.agyCompanionOwnsWrite(conversation, writtenAt, now)
	})
}
