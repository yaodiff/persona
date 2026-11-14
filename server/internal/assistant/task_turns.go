package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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
// task_complete and Claude Code's turn_duration (both a turn_duration item), or the user's stop (the interrupted
// notice, Codex's turn_aborted). Anything after that end that the agent wrote or that starts a turn (a message, a
// wake, a background task's notice, an injected line) belongs to a turn still going, or to one that left no end: it is
// not the turn that ended. A notice of the transcript's own after the end (a compaction) changes nothing.
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

// taskTurn is the latest turn of a task that completion handling took up: which session's, the turn's end (Key; ""
// when its transcript named none), the item that opens it (Open: the prompt, when the transcript was read), the
// session's row then (Rev: the transcript's size; Seen: the row's time, which is its node's word for when the
// transcript was last written to), what it got (State), the stretch of work it closed (Work, as the judgment and the
// signals name it), the judgment written for it and the signal it raised. Receipt: a turn with no Key has its done
// receipt in the feed (a named one's receipt is found there by its id).
type taskTurn struct {
	Machine string      `json:"machine"`
	Session string      `json:"session"`
	Key     string      `json:"key,omitempty"`
	Open    string      `json:"open,omitempty"`
	Rev     int64       `json:"rev,omitempty"`
	Seen    time.Time   `json:"seen,omitzero"`
	State   string      `json:"state"`
	Work    time.Time   `json:"work"`
	At      time.Time   `json:"at"`
	Receipt bool        `json:"receipt,omitempty"`
	Judged  *turnJudged `json:"judged,omitempty"`
	Signal  *turnSignal `json:"signal,omitempty"`
}

// turnJudged is the judgment written for a turn: the judge's check, the error when it could not say (the supervisor
// was told unjudged), or why the hub does not judge the task at all (Skip, autoDoneSkip).
type turnJudged struct {
	Check     taskDoneCheck `json:"check"`
	ReasonKey string        `json:"reason_key,omitempty"`
	Err       string        `json:"err,omitempty"`
	Skip      string        `json:"skip,omitempty"`
	At        time.Time     `json:"at"`
}

// turnSignal is the supervision signal a turn's outcome raised, and when a supervision message carried it (nil: not yet).
type turnSignal struct {
	Kind    string     `json:"kind"`
	Key     string     `json:"key"`
	Machine string     `json:"machine,omitempty"`
	Detail  string     `json:"detail,omitempty"`
	Told    *time.Time `json:"told,omitempty"`
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
	Since time.Time           `json:"since"`
	Turns map[string]taskTurn `json:"turns"`
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
	// looked: the row the sweep looked at last; recovering: a look is going on; tries: how often the sweep had the
	// turn judged; retry: the looks at a row that could not finish, and when the next one is due (turnRetry).
	looked     map[string]turnLook
	recovering map[string]bool
	tries      map[string]int
	retry      map[string]turnRetry
	// judging: the end of a turn of the task is being handled (taskJudging). Under jmu, not mu: it is set with the
	// receipt's reservation and read with it under the task lock (settleTurn, turnStartHeard), which must not wait
	// for the ledger. The ledger's own looks read it with mu held (settling), so what they read of the ledger and of
	// the marks is still one moment's.
	jmu     sync.Mutex
	judging map[string]int
	// closed: the sessions whose transcript showed their turn closed, at the row it was read at: an unknown status
	// there is a stopped agent (rowsProven).
	closed map[SessionRef]turnLook
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
// the ledger then, and completion goes on as it does without one.
func (s *Service) turnsLocked(ctx context.Context) bool {
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
	b.doc, b.loaded = doc, true
	if fresh && st != nil { // since when it is kept must outlive this manager
		if err := st.PutSetting(ctx, taskTurnsKey, b.doc); err != nil {
			s.logf("task turns: %v", err)
		}
	}
	return true
}

// saveTurnsLocked writes the ledger; b.mu is held.
func (s *Service) saveTurnsLocked(ctx context.Context) {
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, taskTurnsKey, s.tasks.turns.doc); err != nil {
			s.logf("task turns: %v", err)
		}
	}
}

// pruneTurns takes the turns of todos closed or gone off the ledger (the sweep, each tick; a write only when there is
// one to take off): the ledger holds one turn for each open task that had one, no more. A closed task that is opened
// again starts from what ends after that (recoverTurn), and a turn's receipt keeps its one id in the feed.
func (s *Service) pruneTurns(ctx context.Context) {
	b := &s.tasks.turns
	b.mu.Lock()
	if !s.turnsLocked(ctx) || len(b.doc.Turns) == 0 {
		b.mu.Unlock()
		return
	}
	ids := make([]string, 0, len(b.doc.Turns))
	for id := range b.doc.Turns {
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
		delete(b.looked, id)
		delete(b.tries, id)
		delete(b.retry, id)
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
	if !named {
		end = turnClose{}
	}
	fresh := taskTurn{Machine: last.MachineID, Session: last.SessionID, Key: end.Key, Open: turnOpen(items), Rev: last.Rev,
		Seen: last.UpdatedAt.UTC(), State: state, Work: since.UTC(), At: time.Now().UTC()}
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.turnsLocked(ctx) {
		return fresh, false
	}
	// A turn the sweep kept as seen (turnBefore) is not one taken up: that only says the sweep makes nothing of it. An
	// end that is told for it is handled as any first one.
	if cur, ok := b.doc.Turns[t.ID]; ok && cur.State != turnBefore && (cur.of(last, end) || cur.sameUnnamed(last, items, end) || !named && cur.sameRow(last)) {
		changed := !cur.Work.Equal(since) || cur.Rev != last.Rev
		if named && cur.Key == "" {
			cur.Key, cur.Open, changed = end.Key, fresh.Open, true // its name, now that the transcript has it
		}
		if changed {
			cur.Work, cur.Rev, cur.Seen = since.UTC(), last.Rev, last.UpdatedAt.UTC() // the stretch it closes now, as the judgment and the signals name it
			b.doc.Turns[t.ID] = cur
			s.saveTurnsLocked(ctx)
		}
		return cur, true
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
	j := &turnJudged{Check: c, ReasonKey: c.reasonKey, Skip: skip, At: time.Now().UTC()}
	if err != nil {
		j.Err = clip(oneLine(err.Error()), 300) // only that it failed is used again; a model service's error can be a page
	}
	e.Judged = j
	b.doc.Turns[todo] = e
	s.saveTurnsLocked(ctx)
}

// turnSignalKind: a signal that says how a turn ended, kept on its turn.
func turnSignalKind(kind string) bool {
	return kind == sigReview || kind == sigIdle || kind == sigUnjudged || kind == sigInterrupted
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
	if sig := e.Signal; sig != nil && sig.Kind == g.Kind && sig.Told != nil && (sig.Key == g.Key || b.again[g.Todo]) {
		if sig.Key != g.Key {
			next := *sig // a turn handed out keeps the signal it was read with
			next.Key = g.Key
			e.Signal = &next
			b.doc.Turns[g.Todo] = e
			s.saveTurnsLocked(ctx)
		}
		return true
	}
	if sig := e.Signal; sig == nil || sig.Kind != g.Kind || sig.Key != g.Key {
		e.Signal = &turnSignal{Kind: g.Kind, Key: g.Key, Machine: g.Machine, Detail: g.Detail}
		b.doc.Turns[g.Todo] = e
		s.saveTurnsLocked(ctx)
	}
	return false
}

// turnsTold keeps on the ledger that the turn signals in list need no telling any more: a supervision message carried
// them (sent, or queued where a restart keeps it), said the same within the deduplication window, or they no longer
// hold. A manager started again does not tell them a second time; until then it tells them from the ledger.
func (s *Service) turnsTold(ctx context.Context, list []superviseSignal) {
	b := &s.tasks.turns
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.loaded {
		return
	}
	changed := false
	for _, g := range list {
		if !turnSignalKind(g.Kind) {
			continue
		}
		e, ok := b.doc.Turns[g.Todo]
		if sig := e.Signal; ok && sig != nil && sig.Kind == g.Kind && sig.Key == g.Key && sig.Told == nil {
			next, now := *sig, time.Now().UTC()
			next.Told = &now
			e.Signal = &next
			b.doc.Turns[g.Todo] = e
			changed = true
		}
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
		s.autoFinishTask(t, since, worked)
		return
	}
	if !worked || !since.After(j.At) || j.Skip != "" {
		return // no start since: the todo and the ledger are as the judgment left them
	}
	s.logf("task %s: its agent's turn %s was handled already: its judgment stands", t.ID, clip(e.Key, 40))
	s.advanceTaskReply(ctx, t, last.MachineID, last.SessionID, text) // its stage, as its reply left it
	current, ok := s.todoNow(t.ID)
	if !ok {
		return
	}
	c := j.Check
	c.reasonKey = j.ReasonKey
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

// watchTurn is the sweep's look at supervised task t for a turn that ended unheard, from its agents as listed. It
// costs no call to a node unless the task's agents all look stopped and the last one's row moved since the sweep last
// looked (once after a start of the manager); then one transcript read, at most turnRecoverMost going at a time. A
// look that could not finish (the node did not answer, the judgment did not land) is tried again on the next tick and
// then ever later while the row stays as it is (turnRetryIn). A usage limit that is waited out is not looked at until
// the wait is over (its turn goes on then), nor an agent whose computer is not connected. What a manager before this
// one left untold is told from the ledger alone.
func (s *Service) watchTurn(ctx context.Context, t Todo, agents []proto.Session) {
	agents = currentTaskAgents(agents, supersededAgents(t, agents))
	probe := make([]proto.Session, len(agents))
	var last proto.Session
	now := time.Now()
	for i, c := range agents {
		if probe[i] = c; c.Status == proto.StatusUnknown {
			probe[i].Status = proto.StatusIdle // the transcript says whether it stopped
		}
		if limitTakenUp(c, now) {
			return // its CLI or the manager goes on after the reset (usage_limit.go): no end to take up until then
		}
		if last.SessionID == "" || c.UpdatedAt.After(last.UpdatedAt) {
			last = c
		}
	}
	if !taskAgentsSettled(t, probe) {
		return // one works, waits, or is not listed: its end comes later, by itself or by this look
	}
	var conn time.Time
	if s.devs != nil {
		conn = s.devs.ConnectedAt(last.MachineID) // a node that connected again is looked at whatever could not finish before
	}
	look := lookOf(last)
	b := &s.tasks.turns
	b.mu.Lock()
	if !s.turnsLocked(ctx) {
		b.mu.Unlock()
		return
	}
	e, known := b.doc.Turns[t.ID]
	same := known && e.Machine == last.MachineID && e.Session == last.SessionID && e.Rev == last.Rev
	var tell *turnSignal
	if sig := e.Signal; same && sig != nil && sig.Told == nil {
		tell = sig
	}
	skip := b.looked[t.ID] == look || b.recovering[t.ID]
	if !skip && same && last.Rev != 0 && e.settled() {
		b.looked[t.ID], skip = look, true // nothing was written since it was handled
	}
	if r, waits := b.retry[t.ID]; waits && (r.look != look || !r.conn.Equal(conn)) {
		delete(b.retry, t.ID) // the row moved or its node connected again: looked at now, whatever could not finish before
	} else if waits && now.Before(r.at) {
		skip = true
	}
	if !skip && len(b.recovering) >= turnRecoverMost {
		skip = true
	}
	b.mu.Unlock()
	if tell != nil { // judged before a restart, never told: told now (a signal waiting or told already stays as it is)
		s.signal(superviseSignal{Todo: t.ID, Kind: tell.Kind, Key: tell.Key, Machine: tell.Machine, Detail: tell.Detail})
	}
	if skip {
		return
	}
	if s.devs != nil { // the fleet is this manager's: the rows are its copy of the node's last list, kept while it is away
		if m, ok := s.devs.Summary(last.MachineID); ok && !m.Reachable {
			return // its computer is not connected: nothing can be read. Looked at on the tick after it is back
		}
	}
	b.mu.Lock()
	if b.recovering[t.ID] || len(b.recovering) >= turnRecoverMost {
		b.mu.Unlock()
		return
	}
	b.recovering[t.ID] = true
	b.mu.Unlock()
	go func() {
		looked := s.recoverTurn(context.WithoutCancel(ctx), t, last)
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.recovering, t.ID)
		if looked {
			b.looked[t.ID] = look
			delete(b.retry, t.ID)
			return
		}
		r := b.retry[t.ID]
		if r.look != look || !r.conn.Equal(conn) {
			r = turnRetry{look: look, conn: conn}
		}
		r.n++
		r.at = time.Now().Add(turnRetryIn(r.n))
		b.retry[t.ID] = r
	}()
}

// recoverTurn reads the transcript of supervised task t's last agent and takes up a closed turn the ledger does not
// have, through settleTurn: the end's own checks first, then the start nobody heard, then the receipt, the judgment
// and the signal. A turn the ledger has but that is not judged yet is judged (resumeTurn); one it has with no name
// gets its name (sameUnnamed). looked: the row needs no second look while it stays as it is; false has the sweep try
// again, later each time (the node did not answer, the task's agents did not all stop, the judgment did not land).
func (s *Service) recoverTurn(ctx context.Context, t Todo, last proto.Session) (looked bool) {
	items, read := s.lastTurnItems(ctx, last.MachineID, last.SessionID)
	if !read {
		return false
	}
	end, closed := turnEndOf(items)
	ref := SessionRef{MachineID: last.MachineID, SessionID: last.SessionID}
	b := &s.tasks.turns
	b.mu.Lock()
	if !closed {
		delete(b.closed, ref)
		b.mu.Unlock()
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
	if !ok || current.DoneAt != nil || current.Supervision == nil {
		return true
	}
	if known && e.of(last, end) {
		if e.settled() {
			return true
		}
		return s.resumeTurn(ctx, current, e)
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
	switch s.settleTurn(ctx, last.MachineID, last.SessionID, t.ID) {
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
		return false // not taken: an agent works or waits again, the node did not answer, another look is at it
	}
	return now.State != turnDone || now.Judged != nil // a judgment that did not land is asked again (resumeTurn)
}

// resumeTurn finishes what a manager before this one, or a look that failed, left of task t's turn e: its judgment.
// (Its untold signal is told by watchTurn.) When the last of turnJudgeTries judgments did not land either it is told
// unjudged at once, so that the supervisor hears of the stop in any case. looked: nothing more to try now.
func (s *Service) resumeTurn(ctx context.Context, t Todo, e taskTurn) (looked bool) {
	if e.State != turnDone || e.Judged != nil {
		return true // judged: only the telling is left
	}
	b := &s.tasks.turns
	b.mu.Lock()
	judging := b.settling(t.ID)
	if !judging {
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
				b.doc.Turns[t.ID] = cur
				s.saveTurnsLocked(ctx)
			}
			b.mu.Unlock()
		}
		return work
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
