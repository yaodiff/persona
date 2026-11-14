package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Starting a session in the background (docs/protocol/protocol.md "New session"). Opening a herdr tab and waiting for claude's
// input box takes seconds (minutes behind a slow ~/.zshrc on a busy machine, or a trust dialog), so `session.start` with async
// answers right away with the session as it will be (id, directory, launcher) and starts it after. Until then the
// session is listed as idle with `starting` (not a turn: nothing is timed or pushed), its chat is empty instead of an error, and messages sent to it wait
// for claude to be ready and then go in, in order. A start that fails stays listed for a while with its error, and
// the messages waiting on it get that error.

const startFailedFor = 10 * time.Minute

type startRec struct {
	done    chan struct{}
	cancel  context.CancelFunc // nil for a record without an async worker
	session proto.Session      // what is listed while it starts
	err     error
	ended   time.Time
	tabID   string // the herdr tab, once made
	tabName string // a name that came before the tab
}

type startLog struct {
	mu      sync.Mutex
	m       map[string]*startRec
	stopped bool
	running sync.WaitGroup // the start workers and their initial session-list notifications
}

// get returns the live start or recent failure, removing a failure once its display window has elapsed.
func (l *startLog) get(id string) *startRec {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	if r != nil && !r.ended.IsZero() && time.Since(r.ended) > startFailedFor {
		delete(l.m, id)
		return nil
	}
	return r
}

// recovered forgets a failed start after an explicit resume has found or started the agent.
// Pending starts keep their channel: messages must still wait for them to finish.
func (l *startLog) recovered(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if r := l.m[id]; r != nil && !r.ended.IsZero() && r.err != nil {
		delete(l.m, id)
	}
}

func (l *startLog) add(id string, r *startRec) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return errors.New("node is shutting down")
	}
	if l.m[id] != nil {
		return fmt.Errorf("session %s is already starting", id)
	}
	if l.m == nil {
		l.m = map[string]*startRec{}
	}
	for k, old := range l.m { // failed ones are shown for a while
		if !old.ended.IsZero() && time.Since(old.ended) > startFailedFor {
			delete(l.m, k)
		}
	}
	l.m[id] = r
	if r.cancel != nil {
		l.running.Add(2)
	}
	return nil
}

// close cancels unfinished starts and joins all their work before the node's hosts and state are torn down.
// Admission and WaitGroup.Add share the lock, so a request still returning as the connection closes cannot add
// work after the wait starts. Calling close again is safe.
func (l *startLog) close() {
	l.mu.Lock()
	l.stopped = true
	var cancel []context.CancelFunc
	for _, r := range l.m {
		if r.cancel != nil {
			cancel = append(cancel, r.cancel)
		}
	}
	l.mu.Unlock()
	for _, stop := range cancel {
		stop()
	}
	l.running.Wait()
}

// finish records how the start went: a started session is listed by herdr from now on, a failed one keeps its entry.
func (l *startLog) finish(id string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	if r == nil {
		return
	}
	r.err, r.ended = err, time.Now()
	if err == nil {
		delete(l.m, id)
	} else {
		r.session.Starting = false
		r.session.Status = proto.StatusOffline
		r.session.StartError = err.Error()
		r.session.UpdatedAt = r.ended.UTC() // when it failed
		// A coded refusal keeps its code and details, which the hub words in the user's language and the apps offer
		// the remedy of (#1642); the rest is text.
		var re *rpc.Error
		if errors.As(err, &re) && re.Code != "" {
			r.session.StartErrorCode = re.Code
			r.session.StartErrorArgs = refusalArgs(re.Data)
		}
	}
	close(r.done)
}

// refusalArgs are a refusal's details that are text, but whether it was delivered (a start delivers nothing).
func refusalArgs(data json.RawMessage) map[string]string {
	var m map[string]any
	if len(data) == 0 || json.Unmarshal(data, &m) != nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range m {
		if s, ok := v.(string); ok && k != "delivered" {
			out[k] = s
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// wait blocks while the session is starting and returns the start's error, if it failed.
func (l *startLog) wait(ctx context.Context, id string) error {
	r := l.get(id)
	if r == nil {
		return nil
	}
	select {
	case <-r.done:
	case <-ctx.Done():
		return fmt.Errorf("这个 Agent 还在启动，没等到它准备好: %w", ctx.Err())
	}
	if r.err != nil {
		err := fmt.Errorf("这个 Agent 没有启动起来：%v", r.err)
		var re *rpc.Error
		if errors.As(r.err, &re) && re.Code != "" { // the start's refusal, still coded for the hub to word
			return &rpc.Error{Code: re.Code, Message: err.Error(), Data: re.Data}
		}
		return err
	}
	return nil
}

// tabCreated notes the new tab and returns a name given for it while it was being made.
func (l *startLog) tabCreated(id, tabID string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	if r == nil {
		return ""
	}
	r.tabID = tabID
	return r.tabName
}

// nameTab takes a tab name for a session that is starting: kept for the tab (true) while there is none yet, else
// the tab to rename. Neither: the session is not starting.
func (l *startLog) nameTab(id, name string) (kept bool, tabID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.m[id]
	if r == nil || !r.ended.IsZero() {
		return false, ""
	}
	if r.tabID == "" {
		r.tabName = name
		return true, ""
	}
	return false, r.tabID
}

// listed are the sessions starting (or failed to) that herdr does not show yet.
func (l *startLog) listed(seen map[string]bool) []proto.Session {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []proto.Session
	for id, r := range l.m {
		if seen[id] || (!r.ended.IsZero() && time.Since(r.ended) > startFailedFor) {
			continue
		}
		out = append(out, r.session)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionID < out[j].SessionID })
	return out
}

// startAsync checks the arguments, answers with the session it is going to be and starts it in the background.
// id is the session id to use (the hub may pick it); empty: a new one.
func (n *Node) startAsync(id, cwd, launcher, prompt, workspaceID, mode, effort, tabName string, options ...modelExecution) (*proto.Session, error) {
	return n.startAsyncIn("", id, cwd, launcher, prompt, workspaceID, mode, effort, tabName, options...)
}

// startAsyncIn is startAsync in host kind ("" : the default host, withStartHost).
func (n *Node) startAsyncIn(kind, id, cwd, launcher, prompt, workspaceID, mode, effort, tabName string, options ...modelExecution) (*proto.Session, error) {
	execution := executionOption(options)
	if err := execution.check(launcher); err != nil {
		return nil, err
	}
	if kind == "" && n.defaultKind() == "" { // no host enabled: refused before a starting row shows up on the phone
		return nil, noEngineRefusal()
	}
	if id == "" {
		id = newUUID()
	} else if !validID(id) {
		return nil, fmt.Errorf("invalid session id %q", id)
	}
	if n.starts.get(id) != nil {
		return nil, fmt.Errorf("session %s is already starting", id)
	}
	cwd, launcher, err := n.checkStart(cwd, launcher, "", mode, effort, "")
	if err != nil {
		return nil, err
	}
	if err := n.codexFunctionLauncher(context.Background(), launcher, false); err != nil {
		return nil, err
	}
	if _, ok := n.codexLauncher(context.Background(), launcher); ok {
		// Codex picks its thread's id when it gets the first message: there is no id to answer with yet. The app
		// starts a Codex with its first message and waits (codex_start.go).
		return nil, refusal("codex_needs_prompt", "新建 Codex 会话要连第一条消息一起发（更新 App 后再试）。", nil)
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(withStartHost(context.Background(), kind), n.startWait())
	r := &startRec{done: make(chan struct{}), cancel: cancel, session: proto.Session{
		SessionID: id, Cwd: cwd, Launcher: launcher, Agent: "claude", Status: proto.StatusIdle, Starting: true,
		StartedByPersona: true, PermissionMode: mode, Effort: effort, UpdatedAt: now, WorkspaceID: workspaceID,
	}}
	if err := n.starts.add(id, r); err != nil {
		cancel()
		return nil, err
	}
	// Known from now on: its chat loads empty (startingSession) and its watch waits for the transcript.
	n.state.put(id, sessionState{Launcher: launcher, Cwd: cwd, StartedAt: now})
	n.noteModes(id, mode, effort)
	// The answer is the session as it starts, copied before the start runs: a start that fails at once has finish
	// rewrite r.session (under the log's lock) before this goroutine would get to it.
	s := r.session
	go func() {
		defer n.starts.running.Done()
		defer cancel()
		_, err := n.start(ctx, cwd, launcher, prompt, "", workspaceID, mode, effort, tabName, id, "", execution)
		if err != nil && !errors.Is(ctx.Err(), context.Canceled) {
			n.logf("start %s: %v", id, err)
		}
		n.starts.finish(id, err)
		n.notifySessions()
	}()
	n.decorate(&s)
	go func() {
		defer n.starts.running.Done()
		n.notifySessions()
	}()
	return &s, nil
}
