package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the turns a task's completion was handled for (#5718) ----
//
// Completion handling (the receipt, the judgment, the supervision message: tasks_follow.go, done.go,
// supervise_events.go) used to start only where a session list said working and a later one done or idle. A node
// whose inventory never says working for a runtime (idle, done or unknown through the whole turn), a manager that was
// down or disconnected while the turn ended, and a list that arrives out of order all left a finished turn with
// nothing, or handled it twice.
//
// Here completion is reconciled by session and turn instead. The runtime closes a turn in its transcript (turnEndOf),
// and that end names the turn. The ledger below, kept in the database, says for each open task which turn of which
// session was taken up, the judgment written for it and whether its supervision signal was told:
//
//   - the receipt of (task, machine, session, turn) has one feed id (receiptID): written once, whoever writes it;
//   - a turn taken up again (the same end told twice, a stale working row before it, a manager started again) gets
//     no second judgment: the one on the ledger is put back, or made now when there is none yet (turnAgain);
//   - its signal is told once: the ledger keeps what was told, and a manager started again tells what was not;
//   - the supervision sweep reads the transcript of a supervised task whose agents look stopped and whose rows moved
//     since it last looked, and takes up a closed turn the ledger does not have (watchTurn, recoverTurn), through the
//     end's own handler (settleTurn, tasks_follow.go): nothing of the task is changed for an end that handler refuses
//     or puts off, and a look that cannot finish is tried again ever later, or not at all where looking cannot help.
//
// A turn the runtime did not close in its transcript (an older runtime, a transcript that cannot be read) has no
// name: it is handled as before, from the working → stopped edge alone, and the sweep does not take it up. When its
// end record shows later (the node can say stopped before the runtime wrote it), the turn the ledger has is
// recognised as that one and gets its name (sameUnnamed): it is not a new turn.

// taskTurnsKey is the setting the ledger is kept in, with the stages and the supervision deduplication (no migration).
const taskTurnsKey = "assistant.task_turns"

var (
	// turnRecoverMost is how many transcripts the supervision sweep reads at one time looking for a turn that ended
	// unheard: the rest wait for the next tick.
	turnRecoverMost = 4
	// turnJudgeTries is how often the sweep has a turn judged whose judgment did not land; when the last one does not
	// land either, the turn is told unjudged.
	turnJudgeTries = 3
	// receiptRetryAfter is how long after a look that could not ask the task's agents again the turn end is looked at
	// once more (taskTurnSettled).
	receiptRetryAfter = 30 * time.Second
	// turnRetryBase and turnRetryMost space the sweep's looks at a row that stays as it is while a look at it could not
	// finish (the node did not answer the read, the judgment did not land): the next tick, then turnRetryBase doubled
	// each time, turnRetryMost at most. A row that moved is looked at on the next tick whatever came before.
	turnRetryBase = time.Minute
	turnRetryMost = 10 * time.Minute
)

// What a turn on the ledger got: a done receipt (and then a judgment), an interrupted one (nothing to judge), or
// nothing (turnBefore): it ended before the ledger began, before its agent was the task's or before the task was
// opened again, or its end was refused as the task stood (cancelled, a meeting's turn). The sweep makes nothing of
// such a turn again; an end that is told for it is handled as any first one.
const (
	turnDone        = "done"
	turnInterrupted = "interrupted"
	turnBefore      = "before"
)

// turnClose is where the runtime closed a turn in its transcript: Key names the turn (the runtime's turn id, else the
// id of the item that ends it), At is when.
type turnClose struct {
	Key string
	At  time.Time
}

// turnEndOf finds the end of the latest turn in items (a session's last turn as its node pages it); false while that
// turn is open, or when the transcript has no end to go by. A turn ends where the runtime says so: Codex's
// task_complete, Claude Code's turn_duration, and Antigravity's agy_turn_end (all turn_duration items), or the user's
// stop (the interrupted notice, Codex's turn_aborted). Anything after that end that the agent wrote or that starts a
// turn (a message, a wake, a background task's notice, an injected line) belongs to a turn still going, or to one that
// left no end: it is not the turn that ended. A notice of the transcript's own after the end (a compaction) changes nothing.
func turnEndOf(items []proto.Item) (turnClose, bool) {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		switch {
		case it.Kind == "turn_duration":
			key := it.ID
			if d := it.TurnDuration; d != nil && d.TurnID != "" {
				key = d.TurnID
			}
			return turnClose{Key: key, At: it.TS}, key != ""
		case it.Kind == proto.KindNotice && it.Text == userStoppedNotice:
			return turnClose{Key: it.ID, At: it.TS}, it.ID != ""
		case it.Kind == proto.KindText, it.Kind == proto.KindToolUse, it.Kind == proto.KindToolResult, it.Kind == proto.KindThinking,
			it.Kind == proto.KindImage, it.Role == "user", it.Wake, it.Task != nil, it.Injected != nil:
			return turnClose{}, false
		}
	}
	return turnClose{}, false
}

// turnEndForSession returns turnEndOf(items), or — for older non-Claude/non-Codex runtimes whose transcripts do not
// yet include a turn_duration item — recognizes a completed assistant text reply when the runtime explicitly reports
// StatusDone. Strict turnEndOf remains required for StatusUnknown, StatusIdle, reconnect verification, Claude Code,
// and Codex.
func turnEndForSession(row proto.Session, items []proto.Item) (turnClose, bool) {
	if end, closed := turnEndOf(items); closed {
		return end, true
	}
	if row.Status != proto.StatusDone || row.Agent == "" && row.Launcher == "" ||
		row.Agent == "claude" || row.Agent == "codex" ||
		row.Launcher == "claude" || row.Launcher == "codex" {
		return turnClose{}, false
	}
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		switch {
		case it.Kind == proto.KindText && it.Role == "assistant" && !it.Narration:
			if it.ID == "" || it.TS.IsZero() || strings.TrimSpace(it.Text) == "" {
				return turnClose{}, false
			}
			return turnClose{Key: "reply:" + it.ID, At: it.TS}, true
		case it.Kind == proto.KindNotice && it.Text != userStoppedNotice && !it.Wake && it.Task == nil && it.Injected == nil:
			continue
		default:
			return turnClose{}, false
		}
	}
	return turnClose{}, false
}

// taskTurn is the latest turn of a task that completion handling took up: which session's, the turn's end (Key; ""
// when its transcript named none), the item that opens it (Open: the prompt, when the transcript was read), the
// session's row then (Rev: the transcript's size; Seen: the row's time, which is its node's word for when the
// transcript was last written to), what it got (State), the stretch of work it closed (Work, as the judgment and the
// signals name it), the judgment written for it and the signal it raised. Receipt: a turn with no Key has its done
// receipt in the feed (a named one's receipt is found there by its id).
type taskTurn struct {
	Machine string    `json:"machine"`
	Session string    `json:"session"`
	Key     string    `json:"key,omitempty"`
	Open    string    `json:"open,omitempty"`
	Rev     int64     `json:"rev,omitempty"`
	Seen    time.Time `json:"seen,omitzero"`
	State   string    `json:"state"`
	Work    time.Time `json:"work"`
	At      time.Time `json:"at"`
	Receipt bool      `json:"receipt,omitempty"`
	// JudgeFailures counts failed judgment passes while Judged is still nil; JudgeError keeps the latest service or
	// parse failure for diagnostics. Both survive a manager restart and reset when a new stretch of work starts.
	JudgeFailures int         `json:"judge_failures,omitempty"`
	JudgeError    string      `json:"judge_error,omitempty"`
	Judged        *turnJudged `json:"judged,omitempty"`
	// Set only when loading a judgment interrupted before its supervisor signal was recorded.
	restoreUnlinked bool
	Signal          *turnSignal `json:"signal,omitempty"`
	// Silent: the turn, taken up as interrupted, had no output at all (proto.TurnErrorNoOutput, or
	// proto.TurnErrorStopped when Persona's Stop interrupted it): what the Agent's row says of it (silent_turns.go).
	Silent string `json:"silent,omitempty"`
}

// turnJudged is the judgment written for a turn: the judge's check, the error when it could not say (the supervisor
// was told unjudged), or why the hub does not judge the task at all (Skip, autoDoneSkip).
type turnJudged struct {
	Check     taskDoneCheck  `json:"check"`
	ReasonKey string         `json:"reason_key,omitempty"`
	Unlinked  []unlinkedPane `json:"agy_unlinked,omitempty"`
	Err       string         `json:"err,omitempty"`
	Skip      string         `json:"skip,omitempty"`
	At        time.Time      `json:"at"`
}

// turnSignal is the supervision signal a turn's outcome raised, and when a supervision message carried it (nil: not yet).
type turnSignal struct {
	Kind      string     `json:"kind"`
	Key       string     `json:"key"`
	Machine   string     `json:"machine,omitempty"`
	Recipient string     `json:"recipient,omitempty"`
	Detail    string     `json:"detail,omitempty"`
	Handoff   bool       `json:"handoff,omitempty"`
	Told      *time.Time `json:"told,omitempty"`
	// JudgeDown: superviseSignal.JudgeDown, so a replay after a restart is batched as the first telling was.
	JudgeDown bool `json:"judge_down,omitempty"`
	Unlinked  bool `json:"agy_unlinked,omitempty"`
}

func (sig *turnSignal) matches(g superviseSignal) bool {
	if sig == nil || sig.Kind != g.Kind || sig.Recipient != g.Recipient {
		return false
	}
	if sig.Key == g.Key {
		return true
	}
	if g.Kind != sigReview || g.Recipient != "" || g.To != "" {
		return false
	}
	// Older turn and outbox JSON kept the nanoseconds that the ordinary review's SQL row lost.
	// Only equate that old key with its actual stored precision. Two distinct full event keys stay distinct.
	old, stored := sig.Key, g.Key
	if len(old) < len(stored) {
		old, stored = stored, old
	}
	raw, ok := strings.CutPrefix(old, "review@")
	if !ok {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	return err == nil && ordinaryReviewKey(at) == stored
}

// replayTurnSignal queues only the still-current, untold ledger signal. Recovery can wait on Inbox I/O while
// another turn is taken or judged: replay must never use signal's normal path to replace that newer ledger entry.
func (s *Service) replayTurnSignal(todo string, expected taskTurn) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	current, ok := b.doc.Turns[todo]
	if !b.loaded || !ok || current.Machine != expected.Machine || current.Session != expected.Session ||
		current.Key != expected.Key || current.Open != expected.Open || !current.Seen.Equal(expected.Seen) {
		return
	}
	sig := current.Signal
	if sig == nil || expected.Signal == nil || sig.Told != nil || *sig != *expected.Signal {
		return
	}
	if s.outageHoldCoversTurn(todo, sig.Kind, sig.Key) {
		return // already folded into an active outage hold; do not replay separately
	}
	// Keep the ledger lock until queued, so an obsolete replay cannot replace a newer pending signal.
	// The digest releases its mutex before consulting or marking the turn ledger.
	g := superviseSignal{Todo: todo, Kind: sig.Kind, Key: sig.Key, Machine: sig.Machine, Recipient: sig.Recipient, Detail: sig.Detail, Handoff: sig.Handoff,
		JudgeDown: sig.JudgeDown}
	if sig.Unlinked && current.Judged != nil {
		g.Unlinked = current.Judged.Unlinked
	}
	s.queueSignal(g)
}

// settled: nothing is left to do for the turn: it is judged (or has nothing to judge) and what it raised was told.
func (e taskTurn) settled() bool {
	if e.State == turnDone && e.Judged == nil {
		return false
	}
	return e.Signal == nil || e.Signal.Told != nil
}

// of: e is the turn that ended at end in session c.
func (e taskTurn) of(c proto.Session, end turnClose) bool {
	return end.Key != "" && e.Key == end.Key && e.Machine == c.MachineID && e.Session == c.SessionID
}

// turnOpen is the id of the item that opens the turn in items (a session's last turn as its node pages it, from the
// user's prompt on); "" when the page does not begin with one.
func turnOpen(items []proto.Item) string {
	if len(items) == 0 || items[0].Role != "user" {
		return ""
	}
	return items[0].ID
}

// sameUnnamed: e was taken up with no end to name it (the transcript could not be read then, or the runtime had not
// written the turn's end yet), and the turn now read in c's transcript (items, closed at end) is that turn. The turn
// found is then e's, not a new one. It is that turn when the same prompt opens it. Where no prompt was read then (the
// read failed), it is when nothing was written to the transcript since (the same size), or when it had begun by the
// time the transcript was last written to as e was taken. Both of those times are the node's (the items' and the
// row's), so the manager's clock, which may differ from the node's, decides nothing here: a turn that began after e
// was taken is never taken for e, which would lose its completion.
func (e taskTurn) sameUnnamed(c proto.Session, items []proto.Item, end turnClose) bool {
	if e.Key != "" || end.Key == "" || e.State == turnBefore || e.Machine != c.MachineID || e.Session != c.SessionID {
		return false
	}
	if open := turnOpen(items); e.Open != "" && open != "" {
		return e.Open == open
	}
	if e.Rev != 0 && e.Rev == c.Rev {
		return true
	}
	began := end.At
	if len(items) > 0 && !items[0].TS.IsZero() {
		began = items[0].TS
	}
	return !began.IsZero() && !e.Seen.IsZero() && !began.After(e.Seen)
}

// sameRow: e was taken up at this very row of c: nothing was written to its transcript since. An end that is told
// with no transcript to name it (the read failed) is e's turn again then.
func (e taskTurn) sameRow(c proto.Session) bool {
	return e.State != turnBefore && e.Rev != 0 && e.Rev == c.Rev && e.Machine == c.MachineID && e.Session == c.SessionID
}

// receiptID is the feed id of the done receipt of todo's turn e: one id for one turn of one session, so that the
// receipt is written once whichever path writes it, before or after a restart. "" for a turn with no name.
func receiptID(todo string, e taskTurn) string {
	if e.Key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(todo + "\x00" + e.Machine + "\x00" + e.Session + "\x00" + e.Key))
	return "feed_turn_" + hex.EncodeToString(sum[:10])
}

// taskTurnsDoc is the ledger as it is kept: the turns by todo, and since when it is kept (a turn that ended before
// was handled, or not, by a manager that kept none: it is not taken up again).
type taskTurnsDoc struct {
	Since         time.Time                 `json:"since"`
	Turns         map[string]taskTurn       `json:"turns"`
	InboxEvents   map[string]taskInboxEvent `json:"inbox_events,omitempty"`
	CloseDeclines map[string]time.Time      `json:"close_declines,omitempty"`
}

// turnLook is the row of a task's last agent as the sweep last looked at it: it looks again when the row moved. A row
// moved when its status changed or its transcript was written to. Rev says the latter (the transcript's size, or the
// runtime's own revision: proto.Session.Rev changes whenever the transcript does), so the row's time is left out
// where there is one: a node's lists can carry a later UpdatedAt for a transcript that did not change (its metadata
// is read up to a few seconds late, and a list that only says sizes moved leaves the time as it was). A row with no
// Rev has only its time to go by.
type turnLook struct {
	Machine, Session, Status string
	Rev                      int64
	UpdatedAt                time.Time
}

type turnRecoveryKey struct {
	Todo, Machine, Session string
}

func lookOf(c proto.Session) turnLook {
	look := turnLook{Machine: c.MachineID, Session: c.SessionID, Status: c.Status, Rev: c.Rev}
	if c.Rev == 0 {
		look.UpdatedAt = c.UpdatedAt
	}
	return look
}

// turnsState is the ledger and what this manager remembers about it; the maps are by todo unless said otherwise.
//
// Lock order: mu (the ledger, held across its database reads and writes) and the task lock s.tasks.mu are never held
// at the same time, in either order; jmu (the judging marks) is a leaf, taken under either of them or alone, and
// nothing is locked and no I/O is done while it is held.
//
// So a ledger write that does not come back keeps the ledger's own callers waiting, and nobody who holds the task
// lock: that one is taken all over the package (starting tasks, routing messages, cards, stages).
type turnsState struct {
	mu     sync.Mutex
	loaded bool
	doc    taskTurnsDoc
	// again: the judgment on the ledger is being put back (turnAgain): the signal it raises is the one told already.
	again map[string]bool
	// looked and retry are per task/recipient: one unreadable transcript must not hold up a peer.
	// recovering serializes a task's reads; tries counts its unfinished judgments.
	looked     map[turnRecoveryKey]turnLook
	recovering map[string]bool
	tries      map[string]int
	retry      map[turnRecoveryKey]turnRetry
	// judging: the end of a turn of the task is being handled (taskJudging). Under jmu, not mu: it is set with the
	// receipt's reservation and read with it under the task lock (settleTurn, turnStartHeard), which must not wait
	// for the ledger. The ledger's own looks read it with mu held (settling), so what they read of the ledger and of
	// the marks is still one moment's.
	jmu     sync.Mutex
	judging map[string]int
	// out: a done judgment of the task is being made (autoFinishTask), under jmu. A look that takes the same turn up
	// again does not ask the model alongside it: it leaves a follow-up that judges the turn when that judgment ends
	// without landing (turnAgain, #13711). The mark is gone once the judgment lands, fails or times out, so a turn is
	// never left marked as being judged, and no look waits on it.
	out map[string]*judgmentOut
	// closed: the sessions whose transcript showed their turn closed, at the row it was read at: an unknown status
	// there is a stopped agent (rowsProven).
	closed map[SessionRef]turnLook
}

// judgmentOut counts the done judgments of a task being made, and what runs once the last one has ended.
type judgmentOut struct {
	n     int
	after []func()
}

// judgingTurn marks a done judgment of todo as being made and answers what ends the mark (autoFinishTask). The
// follow-ups left meanwhile (afterJudgment) run once the last judgment of todo being made has ended, in a goroutine of
// their own.
func (s *Service) judgingTurn(todo string) (end func()) {
	b := &s.tasks.turns
	b.jmu.Lock()
	defer b.jmu.Unlock()
	if b.out == nil {
		b.out = map[string]*judgmentOut{}
	}
	o := b.out[todo]
	if o == nil {
		o = &judgmentOut{}
		b.out[todo] = o
	}
	o.n++
	return func() {
		b.jmu.Lock()
		o.n--
		var after []func()
		if o.n == 0 {
			after, o.after = o.after, nil
			if b.out[todo] == o {
				delete(b.out, todo)
			}
		}
		b.jmu.Unlock()
		if len(after) > 0 {
			go func() {
				for _, f := range after {
					f()
				}
			}()
		}
	}
}

// afterJudgment leaves f to run once the done judgments of todo being made now have ended, and says whether one was;
// false: none is, f is not kept.
func (s *Service) afterJudgment(todo string, f func()) bool {
	b := &s.tasks.turns
	b.jmu.Lock()
	defer b.jmu.Unlock()
	o := b.out[todo]
	if o == nil {
		return false
	}
	o.after = append(o.after, f)
	return true
}

// startRow is the row of a session that showed its turn starting: its transcript's size and its time (Rev and
// UpdatedAt of proto.Session). Zero: not known (the hub's own turn start).
type startRow struct {
	Rev int64
	At  time.Time
}

// startBeforeTakenTurn: the start row of session machine/session is older than the row the ledger took todo's turn up
// at, both by its transcript's size and by its time. The list told that start before that turn ended, and the handling
// of the list came after the end was taken up (a sweep can read a session before its queued list events are handled):
// it is the start of the turn taken up, not a new one (#13711). Rev is the transcript's size, which only a rewrite of
// the transcript makes smaller; a newer time then still shows a real new start. Anything not known answers false.
func (s *Service) startBeforeTakenTurn(todo, machine, session string, row startRow) bool {
	if row.Rev == 0 || row.At.IsZero() {
		return false
	}
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	e, ok := b.doc.Turns[todo]
	return ok && e.State != turnBefore && e.Machine == machine && e.Session == session && e.Rev != 0 && !e.Seen.IsZero() &&
		row.Rev < e.Rev && !row.At.After(e.Seen)
}

// settling: the end of a turn of todo is being handled right now (taskJudging). Callable with the task lock or the
// ledger's mutex held, or neither (jmu is a leaf).
func (b *turnsState) settling(todo string) bool {
	b.jmu.Lock()
	defer b.jmu.Unlock()
	return b.judging[todo] > 0
}

// turnRetry: n looks at row look, over the node's connection that began at conn, could not finish; the next is due
// at at. A row that moved or a node that connected again is looked at on the next tick.
type turnRetry struct {
	look turnLook
	conn time.Time
	n    int
	at   time.Time
}

// turnRetryIn is how long after its n-th look that could not finish a row is looked at again: the next tick, then
// turnRetryBase doubled each time, turnRetryMost at most.
func turnRetryIn(n int) time.Duration {
	if n <= 1 {
		return 0
	}
	d := turnRetryBase
	for i := 2; i < n && d < turnRetryMost; i++ {
		d *= 2
	}
	return min(d, turnRetryMost)
}

// turnsLocked reads the ledger once; b.mu is held. False when the database could not be read: nothing is taken from
// the ledger then, and completion goes on as it does without one. A fresh ledger records its starting time immediately.
func (s *Service) turnsLocked(ctx context.Context) bool {
	return s.turnsLoadLocked(ctx, true)
}

// turnsReadLocked loads the ledger for a read-only decision without writing an empty fresh ledger. A later matching
// state change still saves through saveTurnsLocked; no match means no unnecessary write.
func (s *Service) turnsReadLocked(ctx context.Context) bool {
	return s.turnsLoadLocked(ctx, false)
}

func (s *Service) turnsLoadLocked(ctx context.Context, persistFresh bool) bool {
	b := &s.tasks.turns
	if b.loaded {
		return true
	}
	doc, fresh := taskTurnsDoc{}, true
	st := s.store()
	if st != nil {
		found, err := st.Setting(ctx, taskTurnsKey, &doc)
		if err != nil {
			s.logf("task turns: %v", err)
			return false
		}
		fresh = !found
	}
	if doc.Turns == nil {
		doc.Turns = map[string]taskTurn{}
	}
	if doc.Since.IsZero() {
		doc.Since = time.Now().UTC()
	}
	for id, e := range doc.Turns {
		if e.State == turnDone && e.Signal == nil && e.Judged != nil && len(e.Judged.Unlinked) > 0 && e.Judged.Err == "" && e.Judged.Skip == "" {
			e.restoreUnlinked = true
			doc.Turns[id] = e
		}
	}
	b.doc, b.loaded = doc, true
	s.restoreQuietTurns(doc.Turns)
	if fresh && persistFresh && st != nil { // since when it is kept must outlive this manager
		if err := st.PutSetting(ctx, taskTurnsKey, b.doc); err != nil {
			s.logf("task turns: %v", err)
		}
	}
	return true
}

// saveTurnsLocked writes the ledger; b.mu is held.
func (s *Service) saveTurnsLocked(ctx context.Context) error {
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, taskTurnsKey, s.tasks.turns.doc); err != nil {
			s.logf("task turns: %v", err)
			return err
		}
	}
	return nil
}

// pruneTurns takes the turns of todos closed or gone off the ledger (the sweep, each tick; a write only when there is
// one to take off): the ledger holds one turn for each open task that had one, no more. A closed task that is opened
// again starts from what ends after that (recoverTurn), and a turn's receipt keeps its one id in the feed.
func (s *Service) pruneTurns(ctx context.Context) {
	// The verdicts and Stops kept past their time (silent_turns.go); the apps are told the rows that lose one.
	for m := range s.pruneQuiet(time.Now(), "", nil) {
		go s.publishSessions(m)
	}
	b := &s.tasks.turns
	b.mu.Lock()
	if !s.turnsLocked(ctx) || len(b.doc.Turns)+len(b.doc.InboxEvents)+len(b.doc.CloseDeclines) == 0 {
		b.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(b.doc.Turns))
	for id := range b.doc.Turns {
		ids = append(ids, id)
	}
	for _, e := range b.doc.InboxEvents {
		ids = append(ids, e.Todo)
	}
	for id := range b.doc.CloseDeclines {
		ids = append(ids, id)
	}
	b.mu.Unlock()
	var gone []string
	s.todos.mu.RLock()
	if s.todos.loaded {
		for _, id := range ids {
			if t := s.todos.todos[id]; t == nil || t.DoneAt != nil {
				gone = append(gone, id)
			}
		}
	}
	s.todos.mu.RUnlock()
	if len(gone) == 0 {
		return
	}
	b.mu.Lock()
	for _, id := range gone {
		if e, ok := b.doc.Turns[id]; ok {
			delete(b.closed, SessionRef{MachineID: e.Machine, SessionID: e.Session})
		}
		delete(b.doc.Turns, id)
		delete(b.doc.CloseDeclines, id)
		delete(b.tries, id)
		for key := range b.looked {
			if key.Todo == id {
				delete(b.closed, SessionRef{MachineID: key.Machine, SessionID: key.Session})
				delete(b.looked, key)
			}
		}
		for key := range b.retry {
			if key.Todo == id {
				delete(b.closed, SessionRef{MachineID: key.Machine, SessionID: key.Session})
				delete(b.retry, key)
			}
		}
		for key, e := range b.doc.InboxEvents {
			if e.Todo == id {
				delete(b.doc.InboxEvents, key)
			}
		}
	}
	s.saveTurnsLocked(ctx)
	b.mu.Unlock()
}

// turnOf is the turn the ledger has for todo.
func (s *Service) turnOf(ctx context.Context, todo string) (taskTurn, bool) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) {
		return taskTurn{}, false
	}
	e, ok := b.doc.Turns[todo]
	return e, ok
}

// takeTurn puts on the ledger that task t's stretch of work since since ended with the turn that closed at end in
// its agent last (items: that turn as read, none when the read failed; named: it had an end), and got state. again:
// the ledger has that very turn already, so it was taken up before, by this manager or one before it; the turn as
// the ledger has it is answered then. The ledger's turn is this one when it has the same end; when it was taken with
// no end yet and this is that turn (sameUnnamed: it gets its name now); or when no end is known now either and
// nothing was written to the transcript since (sameRow).
func (s *Service) takeTurn(ctx context.Context, t Todo, last proto.Session, items []proto.Item, end turnClose, named bool, since time.Time, state string) (e taskTurn, again bool) {
	return s.takeTurnAs(ctx, t, last, items, end, named, since, state, "")
}

// takeTurnAs is takeTurn for an interrupted turn with no output (silent: its kind, silent_turns.go), kept on the ledger
// in the same write that takes the turn up: a manager that stops between the two never has the turn without it. A
// turn taken before without it (an older manager) gets it now.
func (s *Service) takeTurnAs(ctx context.Context, t Todo, last proto.Session, items []proto.Item, end turnClose, named bool, since time.Time, state, silent string) (e taskTurn, again bool) {
	if !named {
		end = turnClose{}
	}
	fresh := taskTurn{Machine: last.MachineID, Session: last.SessionID, Key: end.Key, Open: turnOpen(items), Rev: last.Rev,
		Seen: last.UpdatedAt.UTC(), State: state, Work: since.UTC(), At: time.Now().UTC(), Silent: silent}
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) {
		return fresh, false
	}
	// A turn the sweep kept as seen (turnBefore) is not one taken up: that only says the sweep makes nothing of it. An
	// end that is told for it is handled as any first one.
	if cur, ok := b.doc.Turns[t.ID]; ok && cur.State != turnBefore && (cur.of(last, end) || cur.sameUnnamed(last, items, end) || !named && cur.sameRow(last)) {
		workChanged := !cur.Work.Equal(since)
		changed := workChanged || cur.Rev != last.Rev
		if named && cur.Key == "" {
			cur.Key, cur.Open, changed = end.Key, fresh.Open, true // its name, now that the transcript has it
		}
		if silent != "" && cur.State == turnInterrupted && cur.Silent == "" {
			cur.Silent, changed = silent, true
		}
		if changed {
			if workChanged && cur.Judged == nil {
				cur.JudgeFailures, cur.JudgeError = 0, "" // judge the latest stretch with a fresh retry budget
			}
			cur.Work, cur.Rev, cur.Seen = since.UTC(), last.Rev, last.UpdatedAt.UTC() // the stretch it closes now, as the judgment and the signals name it
			b.doc.Turns[t.ID] = cur
			s.saveTurnsLocked(ctx)
		}
		if named {
			s.projectReviewTurn(ctx, t, last, end, cur.State)
		}
		return cur, true
	}
	if named {
		s.projectReviewTurn(ctx, t, last, end, state)
	}
	b.doc.Turns[t.ID] = fresh
	delete(b.tries, t.ID)
	s.saveTurnsLocked(ctx)
	return fresh, false
}

// turnReceipted keeps on the ledger that todo's turn, taken up with no end to name it, has its done receipt in the
// feed: found under its name later (sameUnnamed), it gets no second one.
func (s *Service) turnReceipted(ctx context.Context, todo string) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return
	}
	if e, ok := b.doc.Turns[todo]; ok && e.State == turnDone && !e.Receipt {
		e.Receipt = true
		b.doc.Turns[todo] = e
		s.saveTurnsLocked(ctx)
	}
}

// turnSeen keeps on the ledger that the turn closed at end in task todo's agent last is one the sweep makes nothing
// of (turnBefore).
func (s *Service) turnSeen(ctx context.Context, todo string, last proto.Session, end turnClose) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return
	}
	if cur, ok := b.doc.Turns[todo]; ok && !cur.settled() {
		return // the turn the ledger has is still owed its judgment or its telling: it stays (the row is not read again as it is)
	}
	b.doc.Turns[todo] = taskTurn{Machine: last.MachineID, Session: last.SessionID, Key: end.Key, Rev: last.Rev, State: turnBefore, At: time.Now().UTC()}
	s.saveTurnsLocked(ctx)
}

// turnStartHeard: a start of task id's work was heard that no receipt closed yet (heard), and whether a look is
// handling an end of it right now (settling). Both are read under the task lock, under which settleTurn sets the
// reservation and the mark together: there is no moment at which this sees the one without the other. The ledger
// is not asked (turnsState's lock order).
func (s *Service) turnStartHeard(id string) (heard, settling bool) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	work, started := s.tasks.working[id]
	receipt, receipted := s.tasks.receipted[id]
	return started && !(receipted && !receipt.Before(work)), s.tasks.turns.settling(id)
}

// turnJudged keeps on the ledger the judgment written for todo's turn: c, the error when the judge could not say,
// or skip when the hub does not judge the task. A judgment being put back (turnAgain) is on it already.
func (s *Service) turnJudged(ctx context.Context, todo string, c taskDoneCheck, err error, skip string) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) || b.again[todo] {
		return
	}
	e, ok := b.doc.Turns[todo]
	if !ok || e.State != turnDone {
		return
	}
	j := &turnJudged{Check: c, ReasonKey: c.reasonKey, Unlinked: c.unlinked, Skip: skip, At: time.Now().UTC()}
	if err != nil {
		j.Err = clip(oneLine(err.Error()), 300) // only that it failed is used again; a model service's error can be a page
	}
	e.Judged, e.restoreUnlinked = j, false
	b.doc.Turns[todo] = e
	s.saveTurnsLocked(ctx)
}

// An activity hold is not a settled judgment or a failed attempt. This also
// invalidates a saved judgment replayed after a later start, so the sweep retries it.
func (s *Service) deferTurnJudgment(ctx context.Context, todo string) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) {
		return
	}
	if e, ok := b.doc.Turns[todo]; ok && e.State == turnDone {
		e.Judged = nil
		if e.Signal != nil && e.Signal.Kind == sigReview {
			e.Signal = nil
		}
		b.doc.Turns[todo] = e
		key := turnRecoveryKey{Todo: todo, Machine: e.Machine, Session: e.Session}
		delete(b.looked, key)
		delete(b.retry, key)
		delete(b.tries, todo)
		s.saveTurnsLocked(ctx)
	}
}

// turnSignalKind: a signal that says how a turn ended, kept on its turn.
func turnSignalKind(g superviseSignal) bool {
	return !g.externalPRReview() && (g.Kind == sigReview || g.Kind == sigIdle || g.Kind == sigUnjudged || g.Kind == sigInterrupted)
}

// turnSignalled keeps on the ledger that todo's turn raised g, before it is told. told: a supervision message
// carried it already (this manager's, or one before a restart), so it is not told again; while the turn's judgment
// is put back, the signal that judgment raised before is this one under a new key.
func (s *Service) turnSignalled(g superviseSignal) (told bool) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return false
	}
	e, ok := b.doc.Turns[g.Todo]
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sig := e.Signal
	match := sig.matches(g)
	sameHandover := sig != nil && sig.Kind == g.Kind && g.Recipient == "" && sig.Recipient != "" && sig.Key == g.Key
	if sig != nil && sig.Kind == g.Kind && sig.Told != nil && (match || sameHandover || b.again[g.Todo] && g.Recipient == "") {
		if !match && !sameHandover {
			next := *sig // a turn handed out keeps the signal it was read with
			next.Key = g.Key
			e.Signal = &next
			b.doc.Turns[g.Todo] = e
			s.saveTurnsLocked(ctx)
		}
		return true
	}
	if !sameHandover && (!match || sig.Handoff != g.Handoff || sig.Unlinked != (len(g.Unlinked) > 0)) {
		e.Signal = &turnSignal{Kind: g.Kind, Key: g.Key, Machine: g.Machine, Recipient: g.Recipient, Detail: g.Detail, Handoff: g.Handoff, JudgeDown: g.JudgeDown, Unlinked: len(g.Unlinked) > 0}
		b.doc.Turns[g.Todo] = e
		s.saveTurnsLocked(ctx)
	}
	return false
}

// retargetTurnSignalRecipient updates an in-flight handed-over sigReview receipt in the turn ledger when an explicit
// transfer moves the task to a new supervisor before the earlier handover has been delivered.
func (s *Service) retargetTurnSignalRecipient(ctx context.Context, todoID, recipient string) {
	if todoID == "" || recipient == "" {
		return
	}
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return
	}
	e, ok := b.doc.Turns[todoID]
	if !ok || e.Signal == nil || e.Signal.Kind != sigReview || e.Signal.Told != nil || e.Signal.Recipient == "" || e.Signal.Recipient == recipient {
		return
	}
	next := *e.Signal
	next.Recipient = recipient
	e.Signal = &next
	b.doc.Turns[todoID] = e
	s.saveTurnsLocked(ctx)
}

// turnsTold keeps on the ledger that the turn signals in list need no telling any more: a supervision message carried
// them (sent, or queued where a restart keeps it), said the same within the deduplication window, or they no longer
// hold. A manager started again does not tell them a second time; until then it tells them from the ledger. An ordinary
// receipt does not load an untouched ledger; recovery loads it through its ordinary read path.
func (s *Service) turnsTold(ctx context.Context, list []superviseSignal) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.loaded {
		s.turnsToldLocked(ctx, list)
	}
}

// turnsToldDurably is the strict form used before a confirmed start or a resumed stretch discards its durable signals.
// False leaves every form available for retry: neither an unreadable ledger nor a failed told-mark write may make the
// only remaining copy disappear.
func (s *Service) turnsToldDurably(ctx context.Context, list []superviseSignal) bool {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsReadLocked(ctx) {
		return false
	}
	return s.turnsToldLocked(ctx, list)
}

// turnsToldLocked writes matching receipts and rolls their in-memory marks back when persistence fails; b.mu is held.
func (s *Service) turnsToldLocked(ctx context.Context, list []superviseSignal) bool {
	b := &s.tasks.turns
	changed := false
	previous := map[string]*turnSignal{}
	mark := func(g superviseSignal) {
		e, ok := b.doc.Turns[g.Todo]
		if sig := e.Signal; ok && sig.matches(g) && sig.Told == nil {
			if _, saved := previous[g.Todo]; !saved {
				old := *sig
				previous[g.Todo] = &old
			}
			next, now := *sig, time.Now().UTC()
			next.Told = &now
			e.Signal = &next
			b.doc.Turns[g.Todo] = e
			changed = true
		}
	}
	for _, g := range list {
		if g.Kind == sigOutageRecovery && g.Outage != nil {
			for _, tk := range g.Outage.TurnKeys {
				mark(superviseSignal{Todo: g.Todo, Kind: tk.Kind, Key: tk.Key})
			}
			continue
		}
		if !turnSignalKind(g) {
			continue
		}
		mark(g)
	}
	if !changed {
		return true
	}
	if s.saveTurnsLocked(ctx) == nil {
		return true
	}
	for id, sig := range previous {
		e := b.doc.Turns[id]
		e.Signal = sig // the write did not land: a later confirmation or watch must retry the told-mark
		b.doc.Turns[id] = e
	}
	return false
}

// turnsUntold releases the ledger reservation for a signal whose durable queued copy was removed before delivery.
// Matching kind and key prevents an old queued copy from clearing a newer turn's receipt.
func (s *Service) turnsUntold(ctx context.Context, list []superviseSignal) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return
	}
	changed := false
	unmark := func(g superviseSignal) {
		e, ok := b.doc.Turns[g.Todo]
		if sig := e.Signal; ok && sig.matches(g) && sig.Told != nil {
			next := *sig
			next.Told = nil
			e.Signal = &next
			b.doc.Turns[g.Todo] = e
			changed = true
		}
	}
	for _, g := range list {
		if g.Kind == sigOutageRecovery && g.Outage != nil {
			for _, tk := range g.Outage.TurnKeys {
				unmark(superviseSignal{Todo: g.Todo, Kind: tk.Kind, Key: tk.Key})
			}
			continue
		}
		if !turnSignalKind(g) {
			continue
		}
		unmark(g)
	}
	if changed {
		s.saveTurnsLocked(ctx)
	}
}

// turnAgain: task t's stretch of work since since ended with turn e, which was taken up before. It gets no second
// receipt, notice or report. A judgment it never got (the manager stopped between the receipt and the judgment) is
// made now. One it has is put back, without asking the model again, when a start was heard since (a stale working row
// that came after the turn's end): that start took the check off the todo and the review wait off the ledger, and no
// turn followed it. What the supervisor decided since the judgment stands.
func (s *Service) turnAgain(ctx context.Context, t Todo, e taskTurn, last proto.Session, text string, since time.Time, worked bool) {
	if e.State != turnDone {
		return
	}
	j := e.Judged
	if j == nil {
		judge := func(failures int) {
			if failures > 0 {
				s.autoFinishTaskRetry(t, since, worked)
			} else {
				s.autoFinishTask(t, since, worked)
			}
		}
		// A judgment of this task is being made already: not a second one alongside it, and no wait on it here. It may
		// land for this turn; or not (its stretch moved on, the judge failed or ran out of time), and then the turn is
		// judged once it has ended, so it is never left unjudged (#13711).
		if s.afterJudgment(t.ID, func() {
			cur, ok := s.turnOf(context.Background(), t.ID)
			if ok && cur.Machine == e.Machine && cur.Session == e.Session && cur.Key == e.Key && cur.Judged != nil {
				return // it landed: the turn has its one judgment
			}
			if ok && (cur.Machine != e.Machine || cur.Session != e.Session || cur.Key != e.Key) {
				return // another turn since: its own end judges it
			}
			s.logf("task %s: the judgment being made did not land for turn %s: judged now", t.ID, clip(e.Key, 40))
			judge(cur.JudgeFailures)
		}) {
			s.logf("task %s: its agent's turn %s is being judged: judged after that if it does not land", t.ID, clip(e.Key, 40))
			return
		}
		judge(e.JudgeFailures)
		return
	}
	if !worked || !since.After(j.At) || j.Skip != "" {
		return // no start since: the todo and the ledger are as the judgment left them
	}
	s.logf("task %s: its agent's turn %s was handled already: its judgment stands", t.ID, clip(e.Key, 40))
	s.advanceTaskReplyAt(ctx, t, last.MachineID, last.SessionID, text, last.UpdatedAt) // its stage, as its reply left it
	current, ok := s.todoNow(t.ID)
	if !ok {
		return
	}
	c := j.Check
	c.reasonKey = j.ReasonKey
	c.unlinked = j.Unlinked
	var err error
	if j.Err != "" {
		err = errors.New(j.Err)
	}
	if v := current.Supervision; v != nil {
		for _, r := range v.Reviews {
			if r.At.After(j.At) { // reviewed since: only the check goes back, the ledger is the supervisor's
				if err == nil {
					s.keepTaskCheck(ctx, current, since, c)
				}
				return
			}
		}
	}
	b := &s.tasks.turns
	b.mu.Lock()
	b.again[t.ID] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.again, t.ID)
		b.mu.Unlock()
	}()
	s.applyTaskDone(ctx, current, since, worked, c, err)
}

// rowsProven is agents with an unknown status read as idle where the session's transcript showed its turn closed at
// that very row (recoverTurn): a node that cannot tell a runtime's status says unknown from the start of a turn to
// long after its end, and the transcript is what says it stopped. Any other unknown row stays unknown: not stopped.
func (s *Service) rowsProven(agents []proto.Session) []proto.Session {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.closed) == 0 {
		return agents
	}
	var out []proto.Session
	for i, c := range agents {
		if c.Status != proto.StatusUnknown || b.closed[SessionRef{MachineID: c.MachineID, SessionID: c.SessionID}] != lookOf(c) {
			continue
		}
		if out == nil {
			out = append([]proto.Session(nil), agents...)
		}
		out[i].Status = proto.StatusIdle
	}
	if out == nil {
		return agents
	}
	return out
}

// taskJudging marks the end of a turn of todo as being handled, from its receipt's reservation to its judgment
// (taskTurnSettled, autoFinishTask; n: +1 when one starts, -1 when it ends): the sweep leaves the task alone meanwhile.
// It is in memory only and never waits for the ledger: settleTurn calls it with the task lock held.
func (s *Service) taskJudging(todo string, n int) {
	b := &s.tasks.turns
	b.jmu.Lock()
	defer b.jmu.Unlock()
	if b.judging[todo] += n; b.judging[todo] <= 0 {
		delete(b.judging, todo)
	}
}

// watchTurn looks for a supervised task's unheard turn, or retries an unsupervised task's pending judgment. It
// costs no call to a node unless the task's agents all look stopped and the last one's row moved since the sweep last
// looked (once after a start of the manager); then one transcript read, at most turnRecoverMost going at a time. A
// look that could not finish (the node did not answer, the judgment did not land) is tried again on the next tick and
// then ever later while the row stays as it is (turnRetryIn). A usage limit that is waited out is not looked at until
// the wait is over (its turn goes on then), nor an agent whose computer is not connected. An unchanged revision lets
// the sweep replay a previous manager's untold signal from the ledger alone. Recorded recipients are recovered
// independently of a working peer or another recipient's failed read. Reads remain serial within a task and bounded
// by turnRecoverMost across tasks.
func (s *Service) watchTurn(ctx context.Context, t Todo, agents []proto.Session) {
	agents = currentTaskAgents(agents, supersededAgents(t, agents))
	// A deferred, already-receipted turn can outlive its inventory row.
	// This is only a read address: recoverTurn must still verify the same closed turn.
	pending, missing := s.missingPendingTurnAgent(ctx, t, agents)
	if missing {
		agents = append(slices.Clone(agents), pending)
	}
	probe := make([]proto.Session, len(agents))
	var last proto.Session
	now := time.Now()
	waiting := false
	for i, c := range agents {
		if probe[i] = c; c.Status == proto.StatusUnknown {
			probe[i].Status = proto.StatusIdle // the transcript says whether it stopped
		}
		if limitTakenUp(c, now) {
			waiting = true // another Agent's wait must not hide a recorded event recipient
		}
		if last.SessionID == "" || c.UpdatedAt.After(last.UpdatedAt) {
			last = c
		}
	}
	if missing {
		last = pending
	}
	b := &s.tasks.turns
	b.mu.Lock()
	if !s.turnsLocked(ctx) {
		b.mu.Unlock()
		return
	}
	var candidates []proto.Session
	for i, c := range agents {
		ref := SessionRef{MachineID: c.MachineID, SessionID: c.SessionID}
		key := turnRecoveryKey{Todo: t.ID, Machine: c.MachineID, Session: c.SessionID}
		retry, retrying := b.retry[key]
		// A closed row can still need its settlement read retried.
		if b.closed[ref] == lookOf(c) && (!retrying || retry.look != lookOf(c)) ||
			!taskAgentSettledIn(t, probe, probe[i]) || limitTakenUp(c, now) {
			continue
		}
		for _, e := range b.doc.InboxEvents {
			if e.Todo == t.ID && e.Machine == c.MachineID && e.Session == c.SessionID {
				candidates = append(candidates, c)
				break
			}
		}
	}
	if len(candidates) == 0 {
		if waiting || !taskAgentsSettled(t, probe) {
			b.mu.Unlock()
			return // one works, waits, or is not listed: its end comes later
		}
		candidates = append(candidates, last)
	}
	e, known := b.doc.Turns[t.ID]
	var tell *taskTurn
	if known && e.Signal != nil && e.Signal.Told == nil {
		for _, row := range candidates {
			if e.Machine == row.MachineID && e.Session == row.SessionID && e.Rev == row.Rev {
				tell = &e
				break
			}
		}
	}
	confirmed := known && e.Signal != nil && e.Signal.Told != nil
	for _, c := range agents {
		confirmed = confirmed || c.Status != proto.StatusOffline && c.CreationID() == t.ID
	}
	startUnknown := known && e.Signal != nil && e.Signal.Kind == sigUnjudged && e.Signal.Key == "start-unknown@"+t.ID && confirmed
	busy := b.recovering[t.ID] || len(b.recovering) >= turnRecoverMost
	if !busy {
		b.recovering[t.ID] = true
	}
	b.mu.Unlock()
	if startUnknown {
		// Only the exact creation id or an already persisted told receipt confirms the start. Retry retraction even when
		// that receipt landed already: a crash before queue/outbox cleanup must not restore the confirmed signal.
		if !s.retractStartUnknown(ctx, t.ID) {
			if !busy {
				b.mu.Lock()
				delete(b.recovering, t.ID) // this look never started; leave the next sweep free to retry the retraction
				b.mu.Unlock()
			}
			return
		}
		tell = nil
	}
	if tell != nil {
		if t.Supervision.state(t) == reviewParked {
			// Parking supersedes the old stop. Acknowledge its exact turn signal so this and every later sweep leave it
			// untold no longer, rather than reviving the idle/review wait that the parking decision replaced (#7671).
			s.turnsTold(ctx, []superviseSignal{{Todo: t.ID, Kind: tell.Signal.Kind, Key: tell.Signal.Key, Machine: tell.Signal.Machine}})
		} else { // the ledger's untold notice does not need a transcript-read slot
			s.replayTurnSignal(t.ID, *tell)
		}
	}
	if busy {
		return
	}
	go func() {
		defer func() {
			b.mu.Lock()
			delete(b.recovering, t.ID)
			b.mu.Unlock()
		}()
		recoveryCtx := context.WithoutCancel(ctx)
		if e.restoreUnlinked && slices.ContainsFunc(agents, func(row proto.Session) bool {
			return row.MachineID == e.Machine && row.SessionID == e.Session && row.Rev == e.Rev &&
				(row.Rev != 0 || row.UpdatedAt.Equal(e.Seen))
		}) {
			s.restoreUnlinkedJudgment(recoveryCtx, t, e)
		}
		for _, row := range candidates {
			key := turnRecoveryKey{Todo: t.ID, Machine: row.MachineID, Session: row.SessionID}
			look := lookOf(row)
			var conn time.Time
			if s.devs != nil {
				conn = s.devs.ConnectedAt(row.MachineID)
			}
			b.mu.Lock()
			e, known := b.doc.Turns[t.ID]
			same := known && e.Machine == row.MachineID && e.Session == row.SessionID && e.Rev == row.Rev
			skip := b.looked[key] == look
			if !skip && same && row.Rev != 0 && e.settled() {
				b.looked[key], skip = look, true
			}
			if r, waits := b.retry[key]; waits && (r.look != look || !r.conn.Equal(conn)) {
				delete(b.retry, key) // a new row or connection retries only this recipient
			} else if waits && time.Now().Before(r.at) {
				skip = true
			}
			b.mu.Unlock()
			if skip {
				continue
			}
			if s.devs != nil {
				if m, ok := s.devs.Summary(row.MachineID); ok && !m.Reachable {
					continue
				}
			}
			looked := s.recoverTurn(recoveryCtx, t, row)
			b.mu.Lock()
			if looked {
				b.looked[key] = look
				delete(b.retry, key)
			} else {
				r := b.retry[key]
				if r.look != look || !r.conn.Equal(conn) {
					r = turnRetry{look: look, conn: conn}
				}
				r.n++
				r.at = time.Now().Add(turnRetryIn(r.n))
				b.retry[key] = r
			}
			b.mu.Unlock()
		}
	}()
}

// A placeholder has no native turn end to rediscover. If the previous manager saved the
// judgment but stopped before registering its signal, reapply only that notification.
func (s *Service) restoreUnlinkedJudgment(ctx context.Context, t Todo, e taskTurn) {
	// Check the nodes outside the completion lock, then exclude a new start while registering the signal.
	if !s.taskCompletionCurrent(ctx, t, e.Work) {
		return
	}
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	current, ok := s.todoNow(t.ID)
	if !ok || current.Supervision == nil || current.Check == nil || current.Check.Verdict != verdictUnsure ||
		!current.Check.WorkSince.Equal(e.Work) || current.Check.At.After(e.Judged.At) ||
		autoDoneSkip(current, e.Work, true) != "" || !s.taskWorkCurrent(t.ID, e.Work) {
		return
	}
	for _, review := range current.Supervision.Reviews {
		if review.At.After(e.Judged.At) {
			return
		}
	}
	b := &s.tasks.turns
	b.mu.Lock()
	entry := b.doc.Turns[t.ID]
	if !entry.restoreUnlinked || entry.Signal != nil || entry.Judged != e.Judged || !entry.Work.Equal(e.Work) {
		b.mu.Unlock()
		return
	}
	entry.restoreUnlinked = false
	b.doc.Turns[t.ID] = entry
	b.mu.Unlock()
	check := e.Judged.Check
	check.reasonKey, check.unlinked = e.Judged.ReasonKey, e.Judged.Unlinked
	s.superviseJudged(ctx, current, e.Work, check, nil)
}

func (s *Service) missingPendingTurnAgent(ctx context.Context, t Todo, agents []proto.Session) (proto.Session, bool) {
	e, ok := s.turnOf(ctx, t.ID)
	if !ok || e.State != turnDone || e.Judged != nil || e.Key == "" ||
		autoDoneSkip(t, e.Work, true) != "" {
		return proto.Session{}, false
	}
	for _, c := range agents {
		if c.MachineID == e.Machine && c.SessionID == e.Session {
			return proto.Session{}, false // a current runtime row always outranks the ledger
		}
	}
	for _, a := range t.Agents {
		if a.MachineID == e.Machine && a.SessionID == e.Session && a.ReleasedAt == nil && a.DismissedAt == nil &&
			!a.LinkedAt.After(e.At) {
			return proto.Session{MachineID: e.Machine, SessionID: e.Session, Rev: e.Rev, UpdatedAt: e.Seen,
				Status: proto.StatusDone}, true
		}
	}
	return proto.Session{}, false
}

// recoverTurn reads the transcript of supervised task t's selected agent and takes up a closed turn the ledger does not
// have, through settleTurn: the end's own checks first, then the start nobody heard, then the receipt, the judgment
// and the signal. A turn the ledger has but that is not judged yet is judged (resumeTurn); one it has with no name
// gets its name (sameUnnamed). looked: the row needs no second look while it stays as it is; false has the sweep try
// again, later each time (the node did not answer, the task's agents did not all stop, the judgment did not land).
func (s *Service) recoverTurn(ctx context.Context, t Todo, last proto.Session) (looked bool) {
	items, read := s.lastTurnItems(ctx, last.MachineID, last.SessionID)
	if !read {
		return false
	}
	end, closed := turnEndForSession(last, items)
	ref := SessionRef{MachineID: last.MachineID, SessionID: last.SessionID}
	b := &s.tasks.turns
	b.mu.Lock()
	if !closed {
		delete(b.closed, ref)
		e, known := b.doc.Turns[t.ID]
		b.mu.Unlock()
		if known && e.State == turnDone && e.Judged == nil && e.Key == "" &&
			(e.JudgeFailures > 0 || last.Status == proto.StatusDone || last.Status == proto.StatusIdle) &&
			e.Machine == last.MachineID && e.Session == last.SessionID && e.sameRow(last) {
			// This unnamed turn's receipt already proved that its agents stopped. Its unfinished judgment is resumed here
			// even when an older runtime never writes a native end; taskCompletionCurrent still rejects any newer work.
			return s.resumeTurn(ctx, t, e)
		}
		return true // open, or nothing to go by: looked at again when its row moves
	}
	b.closed[ref] = lookOf(last)
	e, known := b.doc.Turns[t.ID]
	if known && e.sameUnnamed(last, items, end) {
		// The end that was handled before the transcript named it (the read failed then, or the runtime wrote its end
		// record later): this is that turn, with its name now. Not a new one.
		e.Key, e.Open, e.Rev = end.Key, turnOpen(items), last.Rev
		b.doc.Turns[t.ID] = e
		s.saveTurnsLocked(ctx)
	}
	since := b.doc.Since
	settling := b.settling(t.ID) // with the ledger's mutex held: the turn on the ledger and the mark are one moment's
	b.mu.Unlock()
	current, ok := s.todoNow(t.ID)
	if !ok || current.DoneAt != nil {
		return true
	}
	if s.taskInboxRecipient(ctx, t.ID, last.MachineID, last.SessionID) {
		if current.AgentAborted(last.MachineID, last.SessionID) || s.meetingTurnNow(last.MachineID, last.SessionID) {
			return true
		}
		if s.endHeld(ctx, last.MachineID, last.SessionID) {
			return false
		}
	}
	if known && e.of(last, end) {
		s.taskInboxTurnEnded(ctx, current, last, items, silentTurn(items))
		if e.settled() {
			return true
		}
		// A bookkeeping entry can change the revision after the turn ends. Once its native end
		// identifies the same turn, replay its outstanding signal even if the revision keeps moving.
		s.replayTurnSignal(t.ID, e)
		return s.resumeTurn(ctx, current, e)
	}
	if current.Supervision == nil {
		return true // unsupervised tasks retry only a turn already pending on the ledger
	}
	if settling {
		return false // its end is being handled right now (settleTurn): looked at again
	}
	// A turn the ledger does not have. One that ended before the ledger was kept, before this agent was the task's or
	// before the task was opened again is not this task's news: a manager that kept no ledger handled it or not, and
	// a reopened task is judged by what its agent does after that (autoDoneSkip). It is kept as seen, and the sweep
	// makes nothing of it.
	for _, at := range []*time.Time{&current.CreatedAt, current.ReopenedAt} {
		if at != nil && at.After(since) {
			since = *at
		}
	}
	for _, a := range current.Agents {
		if a.MachineID == last.MachineID && a.SessionID == last.SessionID && a.LinkedAt.After(since) {
			since = a.LinkedAt
		}
	}
	if end.At.IsZero() || !end.At.After(since) {
		s.turnSeen(ctx, t.ID, last, end)
		return true
	}
	settled := s.settleTurn(ctx, last.MachineID, last.SessionID, t.ID)
	switch settled {
	case settleRefused:
		// Its end is refused as the task stands (cancelled, its agent let go, a meeting's turn), and nothing was
		// changed for it. Kept as seen: not read again, and not taken up when the task changes later.
		s.turnSeen(ctx, t.ID, last, end)
		return true
	case settleHeld:
		return true // nothing until its transcript is written to again: its row moves then
	}
	now, ok := s.turnOf(ctx, t.ID)
	if !ok || !now.of(last, end) {
		// A recipient may settle its event while a peer still works, without closing the task's stretch.
		return settled == settleTaken && s.taskInboxTurnEnded(ctx, current, last, items, silentTurn(items))
	}
	return now.State != turnDone || now.Judged != nil // a judgment that did not land is asked again (resumeTurn)
}

// resumeTurn finishes what a manager before this one, or a look that failed, left of task t's turn e: its judgment.
// The sweep replays its untold signal after matching the revision or the native end. Failed judgment calls use their
// own durable bounded retry budget. When the last of turnJudgeTries judgments whose result did not land is lost too,
// the turn is told unjudged at once, so that the supervisor hears of the stop in any case.
// looked: nothing more to try now.
func (s *Service) resumeTurn(ctx context.Context, t Todo, e taskTurn) (looked bool) {
	if e.State != turnDone || e.Judged != nil {
		return true // judged: only the telling is left
	}
	if s.taskCloseActivityError(ctx, t, time.Now()) != nil {
		return false // waiting for the owner does not spend the bounded judgment retries
	}
	b := &s.tasks.turns
	serviceRetry := e.JudgeFailures > 0
	b.mu.Lock()
	judging := b.settling(t.ID)
	if !judging && !serviceRetry {
		b.tries[t.ID]++
	}
	tries := b.tries[t.ID]
	b.mu.Unlock()
	if judging {
		return false // this manager is at it
	}
	// stretch is the stretch of work the turn closes, as the manager has it now. A start heard since the receipt, while
	// the transcript just read ends with this very turn and the agents are stopped, opened no turn (a list that arrived
	// late, a start told twice): the stretch it opened is closed by this turn too, and the judgment is made for it.
	stretch := func() time.Time {
		s.tasks.mu.Lock()
		work, started := s.tasks.working[t.ID]
		if !started {
			work = e.Work // the stretch the receipt closed, as the manager before knew it
			s.tasks.working[t.ID] = work
		}
		s.tasks.mu.Unlock()
		if work.After(e.Work) {
			b.mu.Lock()
			if cur, ok := b.doc.Turns[t.ID]; ok && cur.Key == e.Key && cur.Session == e.Session && !cur.Work.Equal(work) {
				cur.Work = work.UTC()
				cur.JudgeFailures, cur.JudgeError = 0, "" // a new stretch gets the full service-failure retry budget
				b.doc.Turns[t.ID] = cur
				s.saveTurnsLocked(ctx)
			}
			b.mu.Unlock()
		}
		return work
	}
	if serviceRetry {
		s.autoFinishTaskRetry(t, stretch(), true)
		if now, ok := s.turnOf(ctx, t.ID); ok && now.Judged != nil {
			return true
		}
		return false // a failed judgment call is retried by the sweep, later each time
	}
	if tries <= turnJudgeTries {
		s.autoFinishTask(t, stretch(), true)
		if now, ok := s.turnOf(ctx, t.ID); ok && now.Judged != nil {
			return true
		}
		if tries < turnJudgeTries {
			return false // asked again, later each time (watchTurn)
		}
	}
	s.logf("task %s: the judgment of its agent's turn did not land %d times: told unjudged", t.ID, turnJudgeTries)
	s.applyTaskDone(ctx, t, stretch(), true, taskDoneCheck{}, errors.New("the judgment did not land")) // for the stretch as it is now
	return true
}

// taskStartMissed is taskWorking for a start nobody heard, found by its turn's end (recoverTurn): the open task is
// taken to work from now. It never reopens a closed task: a turn found in a transcript is no word that someone asked
// for more. False when the task is closed or gone.
func (s *Service) taskStartMissed(ctx context.Context, id string) bool {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	t, ok := s.todoNow(id)
	if !ok || t.DoneAt != nil {
		return false
	}
	s.taskWorks(ctx, t)
	return true
}
