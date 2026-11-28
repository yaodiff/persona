package node

import (
	"bytes"
	"os"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/modelretry"
	"github.com/nwverse/persona/server/internal/node/transcripttail"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// quickMeta is a cheap summary of a transcript read from its head and tail only.
type quickMeta struct {
	size         int64
	mtime        time.Time
	title        string
	titleManual  bool
	titleSource  string
	outputTokens int
	codex        bool
	first        string
	activity     time.Time // last real exchange, the tail's or the last before it; zero when unknown, never the first prompt's (#5896)
	queue        []string
	loop         *proto.LoopInfo
	toolLoop     toolLoopScan
	goal         *proto.GoalInfo        // from backgroundTasks too: a goal is often set at the start of a long transcript
	plan         *proto.PlanInfo        // from the same scan (bgPlan): a TodoWrite is often far back in a long turn
	bg           []proto.BackgroundTask // from backgroundTasks: the tail alone misses tasks started earlier in a long turn
	bgEnded      []proto.BackgroundTask // recent shell / Monitor outcomes from the same scan
	bgWait       bool                   // bg and loop are not known yet (a big first read is running)
	wf           []proto.Workflow       // workflow runs, as the transcript tells them (bgWorkflows)
	wfScript     map[string]string      // their script files by run id
	// cwd: where the session runs. An agent's latest directory (Claude Code writes the shell's directory after each
	// Bash `cd` into every line), but where it started when that latest one is an assistant or health directory; for
	// a session started in an assistant directory or inside a health space, the starting directory (#2368).
	cwd string
	// start: the first directory the transcript's head names, where the session started ("" when the head has none:
	// a first prompt bigger than the head read).
	start         string
	model         string
	serviceTier   string
	serviceTierAt time.Time
	surface       string // the client it runs in (transcript.Meta.Surface)
	lastText      string
	// answer is the latest turn's answer from its first words on (transcript.Meta.TurnAnswer); "" when the tail read
	// held none of its words.
	answer string
	// asks is what lastText asks the user (closeout.AskOf), when no message came in after it (#5096).
	asks string
	// lastTextAt is when the agent last wrote text (botmux's sentinel included: it said something, it is no probe).
	lastTextAt time.Time
	// lastPromptAt is when the latest message to the agent came in: the tail's, else the latest known before it (the
	// session's first when no nearer one is).
	lastPromptAt time.Time
	// doing / doingAt: what the agent's latest tool call does, when no text came after it (transcript.Activity).
	doing       string
	doingAt     time.Time
	tool        string // the name of that call (transcript.Meta.LastTool)
	wake        bool   // the latest turn was a background task waking the agent
	readAt      time.Time
	mode        string
	modeAt      time.Time
	effort      string
	effortAt    time.Time
	modelAt     time.Time
	version     string
	versionAt   time.Time // when the line that said version was written: a resumed process has written none yet
	ultracode   *bool
	ultracodeAt time.Time
	speed       transcript.SpeedObservation
	sandbox     string // Codex: the turn's sandbox, which with mode (its approval policy) gives the app's mode back
	// contextTokens / contextWindow: the latest reply's input tokens and, for Codex, the window its rollout records
	// (transcript.Meta; Session.ContextTokens / ContextWindow, #2430). Carried over from the last read when the tail
	// holds no reply (one huge line can fill it).
	contextTokens, contextWindow int
	contextStats                 transcript.ContextStats
	contextFullWindow            bool
	contextRestartRecommended    bool
	// turnClosed: the tail ends with a turn finished (transcript.Meta.TurnClosed): a Codex rollout's, a Claude
	// transcript's (turn_duration, an interrupt)
	turnClosed        bool
	codexClosedTurnID string
	// claudeTurn: a Claude transcript with a turn started and not finished (transcript.Meta.TurnOpen). A tail that
	// starts and ends no turn leaves both as they stood before it (readQuickMetaSince): a long turn's prompt is soon
	// out of the tail, and a recap written after a finished turn starts none.
	claudeTurn bool
	// contentAt: the newest line in the tail that has a time, when the transcript last said something. Not the file's
	// mtime: Claude Code rewrites its trailing metadata (last-prompt, ai-title, mode…, no timestamps) in place while
	// idle, and that is no sign of work (#5967).
	contentAt time.Time
	// turnOpen: a Codex rollout whose tail is inside a turn: the last turn it starts has not finished, or one turn
	// fills the whole tail (a finished turn's task_complete is within a few KB of the end; real turns write MBs)
	turnOpen bool
	// turnActivityAt: newest timestamped line of the current open Codex turn in the tail (never carried from before/head).
	turnActivityAt time.Time
	// turnErr: the latest turn failed on the model service, as the agent's record says it (transcript.Meta.TurnError…:
	// a Codex rollout's task_complete, #2185; Claude Code's own reply for a failed request, #1710).
	turnErr turnFailure
	// modelFallback comes from the incremental scan as well: the fallback may outlast the quick tail.
	modelFallback *proto.ModelRefusal
	// rewound: the lines session.rewind, or a rewind seen in the terminal, went back over (File.RewoundLines), which
	// Claude writes nothing for; kept from read to read, the tail leaves them out (applyRewound).
	rewound []string
}

// turnFailure is how the latest turn failed on the model service (Session.TurnError…): its text, its kind, a usage
// limit's reset (zero: not known), whether the CLI goes on by itself after it and whether the turn did work first.
type turnFailure struct {
	text, kind, code   string
	line               string
	streak             int
	resetsAt           time.Time
	resumes, afterWork bool
}

// set puts the failure into a session; none clears what the session says of one.
func (f turnFailure) set(s *proto.Session) {
	s.TurnError, s.TurnErrorKind, s.TurnErrorResumes, s.TurnErrorAfterWork = f.text, f.kind, f.resumes, f.afterWork
	s.TurnErrorCode = f.code
	s.TurnErrorResetsAt = nil
	if !f.resetsAt.IsZero() {
		t := f.resetsAt.UTC()
		s.TurnErrorResetsAt = &t
	}
}

// reported is the failure an app-server said of a turn (text, kind) before the rollout read may have it: with what the
// rollout adds (a usage limit's reset, the work) when its read already has the same failure. A read the file changed
// since has none (Node.meta), so what it has is the latest turn's.
func reported(text, kind string, rollout turnFailure) turnFailure {
	f := turnFailure{text: text, kind: kind}
	if rollout.text != "" && rollout.kind == kind {
		f.resetsAt, f.afterWork = rollout.resetsAt, rollout.afterWork
		if rollout.text == text {
			f.code = rollout.code
		}
	}
	if f.code == "" && modelretry.AccountExhausted(text) {
		f.code = proto.TurnErrorUpstreamUnavailable
	}
	return f
}

// quickTail: how much of a transcript's end readQuickMeta reads for its title and last words. One line can fill it:
// Claude Code writes its api-request-blob lines whole (300–500 KB each, 2026-11).
const quickTail = transcripttail.Window

// metaMinInterval: how stale a growing transcript's title / last text / background tasks may get in the list.
const metaMinInterval = 3 * time.Second

// metaNow is meta with what was written since the last read, however recent that read: for a decision a list's read
// of a few seconds ago must not make (metaMinInterval).
func (n *Node) metaNow(path string, st os.FileInfo) quickMeta {
	n.mu.Lock()
	if prev, ok := n.metas[path]; ok {
		prev.readAt = time.Time{}
		n.metas[path] = prev
	}
	n.mu.Unlock()
	return n.meta(path, st)
}

func (n *Node) meta(path string, st os.FileInfo) quickMeta {
	n.mu.Lock()
	m, ok := n.metas[path]
	n.mu.Unlock()
	// A working agent's transcript grows all the time and every list rebuild (each herdr event) would re-read its
	// 384 KB tail and scan the new bytes: a dozen busy agents with large transcripts kept a node reading ~20 MB/s.
	// A growing file's meta is re-read at most every metaMinInterval; the list's rev comes from stat, so it stays exact.
	if ok && (m.size == st.Size() && m.mtime.Equal(st.ModTime()) || time.Since(m.readAt) < metaMinInterval) {
		// Retention advances even when the transcript does not. The scan itself stays cached.
		m.bgEnded = recentBackgroundEnded(m.bgEnded)
		if m.bgWait { // the background read may be done now
			m.bg, m.loop, m.goal, m.bgWait = n.backgroundTasks(path)
			m.wf, m.wfScript = n.bgWorkflows(path)
			m.bgEnded = n.bgEnded(path)
			m.ultracode, m.ultracodeAt = n.bgUltracode(path)
			m.plan = n.bgPlan(path)
			n.mergeModelFallback(path, &m)
			n.mergeNamingMetadata(path, &m)
			n.mu.Lock()
			current := n.metas[path]
			m.toolLoop = current.toolLoop
			n.metas[path] = m
			n.mu.Unlock()
		}
		if m.size != st.Size() || !m.mtime.Equal(st.ModTime()) {
			// Written since that read: a turn error it saw may be over (a "Continue" ran and ended well within the interval),
			// and a stale one would have it continued again. None for a few seconds is the safe side (#2185). The CLI's word
			// that it resumes by itself stays: it keeps anything from typing into the session, the safe side too, and the
			// writes that make the read stale are its own continuation at the reset or the wakes of the wait (#1710).
			m.turnErr = turnFailure{resumes: m.turnErr.resumes, line: m.turnErr.line, streak: m.turnErr.streak}
		}
		return m
	}
	m = readQuickMetaSince(path, st, m) // m: the last read's, zero when there is none
	if m.start == "" {
		// A first prompt bigger than the head read (an inline image) hides where the session started: the node's own
		// assistant or health project says it (#2368).
		if dir := n.assistantDirOfProject(filepath.Base(filepath.Dir(path))); dir != "" {
			m.cwd = dir
		}
	}
	m.readAt = time.Now()
	m.bg, m.loop, m.goal, m.bgWait = n.backgroundTasks(path)
	m.wf, m.wfScript = n.bgWorkflows(path)
	m.bgEnded = n.bgEnded(path)
	m.ultracode, m.ultracodeAt = n.bgUltracode(path)
	m.plan = n.bgPlan(path)
	n.mergeModelFallback(path, &m)
	n.mergeNamingMetadata(path, &m)
	n.mu.Lock()
	current := n.metas[path]
	m.toolLoop = current.toolLoop
	n.metas[path] = m
	n.mu.Unlock()
	return m
}

func readQuickMeta(path string, st os.FileInfo) quickMeta {
	return readQuickMetaSince(path, st, quickMeta{})
}

// readQuickMetaSince reads a transcript's quick meta; prev is its meta from the last read (zero when there is none).
//
// The head is read for what belongs to the session's start (its first prompt, where it started, what a rollout says
// once at its start) and the tail for where the session stands, each into a Meta of its own: read into one, the first
// prompt's time, its open turn and its mode were the session's latest whenever the tail held no exchange, and one line
// can fill a tail (#5896). What the tail's lines do not say is as it stood before them: the last read's when nothing
// written since is out of the tail, else what the lines further back say (lookBack).
func readQuickMetaSince(path string, st os.FileInfo, prev quickMeta) quickMeta {
	m := quickMeta{size: st.Size(), mtime: st.ModTime()}
	f, err := os.Open(path)
	if err != nil {
		return m
	}
	defer f.Close()
	head, start := transcriptHead(f)
	// What the tail may not repeat: a rollout names its directory, version and client on its first line only, its model
	// and effort once a turn. Nothing of the head's exchange, turn or mode.
	tm := transcript.Meta{Codex: head.Codex, Cwd: head.Cwd, Version: head.Version, VersionAt: head.VersionAt, Surface: head.Surface,
		Model: head.Model, ModelAt: head.ModelAt, ServiceTier: head.ServiceTier, ServiceTierAt: head.ServiceTierAt, Effort: head.Effort, EffortAt: head.EffortAt}
	if prev.titleManual && prev.size <= st.Size() {
		tm.Title, tm.TitleManual, tm.TitleSource = prev.title, true, "manual"
	}
	off := max(st.Size()-quickTail, 0)
	// covered: nothing written since the last read is out of the tail, so what that read knew stands where the tail's
	// lines say nothing.
	covered := off > 0 && prev.size >= off && prev.size <= st.Size()
	if covered {
		// All newly appended rows are in the tail, so it can invalidate the previous fallback itself.
		tm.ModelFallback = prev.modelFallback
		tm.Speed = prev.speed
		if tm.Codex && prev.modelAt.After(tm.ModelAt) {
			tm.Model, tm.ModelAt = prev.model, prev.modelAt
		}
		if tm.Codex && prev.serviceTierAt.After(tm.ServiceTierAt) {
			tm.ServiceTier, tm.ServiceTierAt = prev.serviceTier, prev.serviceTierAt
		}
	}
	// The tail's lines in the transcript's tree: a turn the user went back over (a rewind in the terminal, then a recap
	// beside it) is not where the agent's last words come from (transcript/branch.go).
	var tail transcript.Tail
	tail.PriorModelFallback(tm.ModelFallback)
	tail.Rewound(prev.rewound)
	m.rewound = prev.rewound
	whole := off // where the tail's whole lines begin: after the line off cuts
	buf := make([]byte, st.Size()-off)
	_, _ = f.ReadAt(buf, off)
	lines := bytes.Split(buf, []byte("\n"))
	if off > 0 && len(lines) > 0 {
		whole += int64(len(lines[0]))
		lines = lines[1:] // first line is partial
	}
	readTail := func() {
		var codexTurnAt time.Time
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			ts := lineTime(line)
			if ts.After(m.contentAt) {
				m.contentAt = ts
			}
			prevOpen, prevClosed := tm.TurnOpen, tm.TurnClosed
			quickLine(line, &tm, &tail)
			tm.ObserveSpeed(line)
			if tm.Codex {
				switch {
				case tm.TurnClosed && !prevClosed:
					codexTurnAt = time.Time{}
				case tm.TurnOpen && !prevOpen:
					codexTurnAt = ts
				case tm.TurnOpen || (off > 0 && !tm.TurnClosed):
					if ts.After(codexTurnAt) {
						codexTurnAt = ts
					}
				}
			}
		}
		if tm.Codex && !tm.TurnClosed && (tm.TurnOpen || off > 0) {
			m.turnActivityAt = codexTurnAt
		}
	}
	base := tm
	readTail()
	if rest, ok := tail.Revoked(); ok { // Claude went on from a turn the node took for rewound (transcript.File.RewoundAt)
		m.rewound = rest
	}
	rewound := tail.Settle(&tm)
	// before: the transcript as it stood before the tail's lines, for what they do not say. Only its last words and
	// their time, last exchange, last prompt, Claude turn and mode are read. The last read's stand when nothing written
	// since is out of the tail and no turn in it was gone back over: after a rewind, what that read knew may all be the
	// turn gone back over (its words, its turn left working, its prompt and mode), so it is read again from the lines
	// before the tail, which the rewound turn's lines in the tail are not part of.
	var before quickMeta
	if covered && !rewound {
		before = prev
	} else if off > 0 {
		// words: the tail has no words of the agent's (none, or only the rewound turn's).
		words := tm.LastText == ""
		turn := !tm.TurnOpen && !tm.TurnClosed
		// An unscoped Codex command may turn out to belong to a closed turn.
		exchange := tm.LastActivity.IsZero() || tm.Codex && turn
		// Claude Code names the mode on every prompt, so it is at most a turn back. A rollout names it once a turn and
		// its turns outgrow the reach: its mode is taken when the lines read on the way say it, not looked for.
		mode := !tm.Codex && tm.PermissionMode == ""
		if words || exchange || turn || mode {
			before = lookBack(f, whole, tm.Codex, words, exchange, turn, mode)
		}
	}
	if tm.Codex && !tm.TurnOpen && !tm.TurnClosed && before.turnClosed {
		// Only a command completion or metadata may be left in this window.
		// Decode from the known closed turn so cold and warm reads both keep
		// that turn's activity time rather than the later command's time.
		tm, tail = base, transcript.Tail{}
		tm.TurnClosed, tm.CodexClosedTurnID = true, before.codexClosedTurnID
		m.turnActivityAt = time.Time{}
		readTail()
	}
	words := off > 0 && tm.LastText == "" && (!covered || rewound) // the words that stand come from before the tail
	m.title, m.cwd, m.model, m.lastText, m.first, m.activity, m.queue = tm.Title, tm.Cwd, tm.Model, tm.LastText, head.FirstUser, tm.LastActivity, tm.Queue
	if m.activity.IsZero() {
		// No exchange in the tail (a tool result or a request bigger than it, then only a recap, a title, a hook's
		// summary): the last one before it. Never the head's, the session's first; none known leaves the file's time.
		m.activity = before.activity
	}
	if tm.LastText != "" {
		m.answer = tm.TurnAnswer()
	}
	m.codex = tm.Codex
	m.serviceTier, m.serviceTierAt = tm.ServiceTier, tm.ServiceTierAt
	m.titleManual, m.titleSource = tm.TitleManual, tm.TitleSource
	if off == 0 {
		m.outputTokens = tm.OutputTokens // a whole-file tail; the incremental scan supplies larger files' totals
		if !tm.Codex && m.titleSource == "" {
			m.titleSource = "prompt"
		}
	} else if !m.titleManual {
		m.titleSource = "" // a historical custom-title may be outside this bounded read
	}
	m.start = start
	if identity.IsAssistantDir(start) || identity.InSpace(start) || start != "" && identity.IsAssistantDir(m.cwd) {
		// An assistant or space conversation is where it started: its space, CLAUDE.md, memory and resume project.
		// A `cd` into a skill's folder had it listed as an agent there (#2368). The other way round, an agent whose
		// `cd` landed in an assistant or space directory (one started in the home directory) is not one of their
		// conversations: listed there at rest, the hub took it for one. An exact assistant or space directory is
		// only ever where a session started. Space subdirectories also retain their starting directory for privacy.
		m.cwd = start
	}
	m.surface = tm.Surface
	if m.surface == "" {
		m.surface = prev.surface // Claude writes it on every line, a rollout on its first: the head read above has it
	}
	m.codexClosedTurnID = tm.CodexClosedTurnID
	m.wake, m.turnClosed, m.lastTextAt, m.lastPromptAt = tm.LastTurnWake, tm.TurnClosed, tm.LastTextAt, tm.LastPromptAt
	if m.lastText == "" {
		// An agent that said a line and then ran tools for a while: their results and hook lines push its words out of
		// the tail (a 1.5 MB transcript whose last text was 900 KB back, 2026-11-04) and its task card said "No activity yet".
		m.lastText, m.answer = before.lastText, before.answer
		if m.lastTextAt.IsZero() {
			m.lastTextAt = before.lastTextAt // when the words that stand were written
		}
	}
	if m.lastPromptAt.IsZero() {
		// No message in the tail: the latest before it, the session's first when none nearer is known (one did come in).
		if m.lastPromptAt = before.lastPromptAt; m.lastPromptAt.IsZero() {
			m.lastPromptAt = head.LastPromptAt
		}
	}
	// succeeded: the newest closed turn succeeded (transcript.Meta.TurnSucceeded, #6221). The tail can hold the closing
	// record but not the reply that proves it, so for an error-looking preview both are read together, from the end
	// (from the tail's start when a turn in it was gone back over: the hidden turn's words and outcome are no evidence).
	// Words read from before the tail are checked whatever error the tail holds; the tail's own words or the last read's
	// only when the tail holds none and no turn in it was gone back over.
	succeeded := tm.TurnSucceeded()
	if !succeeded && off > 0 && assistantwire.CutOff(m.lastText) && (words || !rewound && tm.TurnError == "") {
		end := st.Size()
		if rewound {
			end = off
		}
		_, succeeded = transcripttail.LastWordsBefore(f, end)
	}
	if succeeded && assistantwire.CutOff(m.lastText) {
		// The newest closed turn succeeded, but its visible reply was only botmux's sentinel (or tools and thinking):
		// LastText still names an older failure. Do not let the manager's legacy preview fallback revive that failure.
		// The old reply and its error remain untouched in the transcript.
		m.lastText, m.answer = "", ""
	}
	if !m.lastPromptAt.After(m.lastTextAt) { // a message after the question answered it (#5096)
		m.asks = closeout.AskOf(m.lastText)
	}
	if t := tm.LastTool; t != nil {
		m.doing, m.doingAt, m.tool = transcript.Activity(t.Name, t.Input), t.At, t.Name
	}
	m.turnOpen = tm.Codex && (tm.TurnOpen || off > 0 && !tm.TurnClosed)
	m.claudeTurn = !tm.Codex && tm.TurnOpen
	if !tm.Codex && !tm.TurnOpen && !tm.TurnClosed {
		// The tail starts and ends no turn: the turn stands as it stood before it. A turn seen starting stays open when
		// its prompt leaves the tail; one that ended does not open again for a recap written after it.
		m.claudeTurn, m.turnClosed = before.claudeTurn, before.turnClosed
	}
	m.modelFallback = tm.ModelFallback
	streak := tm.UpstreamErrorStreak
	if prev.size > 0 && prev.size <= st.Size() && !rewound {
		streak = tm.ResolveUpstreamStreak(off, prev.turnErr.streak, prev.turnErr.line)
	}
	upstreamLine := ""
	if streak > 0 {
		upstreamLine = tm.LastUpstreamLine()
	}
	errCode := tm.TurnErrorCode
	if errCode == "" && tm.TurnError != "" && (streak >= 2 || modelretry.AccountExhausted(tm.TurnError)) {
		errCode = proto.TurnErrorUpstreamUnavailable
	}
	m.turnErr = turnFailure{text: tm.TurnError, kind: tm.TurnErrorKind, code: errCode, line: upstreamLine,
		streak: streak, resetsAt: tm.TurnErrorResetsAt, resumes: tm.TurnErrorResumes, afterWork: tm.TurnErrorAfterWork}
	if tm.TurnErrorKind == proto.TurnErrorUsageLimit && !tm.TurnErrorResumesSaid() && prev.size > 0 && prev.size <= st.Size() {
		// Claude Code's notice that it goes on by itself is written once, at the first refused turn; the wait's wakes
		// write request lines of hundreds of KB each, and soon the tail starts after it. Nothing in the tail took it
		// back (a message typed, a reply, its cancel notice), so what the last read found stands (#1710).
		m.turnErr.resumes = prev.turnErr.resumes
	}
	m.mode, m.modeAt, m.effort, m.effortAt, m.modelAt, m.sandbox = tm.PermissionMode, tm.ModeAt, tm.Effort, tm.EffortAt, tm.ModelAt, tm.CodexSandbox
	if m.mode == "" && m.sandbox == "" {
		// The tail names no mode: the latest one before it, the session's first when none nearer is known.
		if m.mode, m.modeAt, m.sandbox = before.mode, before.modeAt, before.sandbox; m.mode == "" && m.sandbox == "" {
			m.mode, m.modeAt, m.sandbox = head.PermissionMode, head.ModeAt, head.CodexSandbox
		}
	}
	m.speed = tm.Speed
	m.version, m.versionAt, m.ultracode, m.ultracodeAt = tm.Version, tm.VersionAt, tm.Ultracode, tm.UltracodeAt
	m.contextTokens, m.contextWindow = tm.ContextTokens, tm.ContextWindow
	m.contextStats = tm.ContextStats
	m.contextFullWindow = off == 0 || tm.ContextStats.CompactedInWindow
	if m.contextTokens == 0 && !tm.ContextCompacted && prev.size > 0 && prev.size <= st.Size() {
		// No reply in the tail: the last read's stands, unless the tail compacted it away (unknown until a reply, #9207).
		m.contextTokens, m.contextWindow = prev.contextTokens, prev.contextWindow
	}
	if m.contextTokens > 0 && m.contextStats.Empty() && !tm.ContextCompacted && prev.size > 0 && prev.size <= st.Size() {
		m.contextStats, m.contextFullWindow = prev.contextStats, prev.contextFullWindow
	}
	return m
}

// mergeNamingMetadata reuses the existing incremental scan, including when a cold scan finishes without another
// file append. Never publish a partial tail's automatic provenance before historical manual titles are checked.
func (n *Node) mergeNamingMetadata(path string, m *quickMeta) {
	n.mu.Lock()
	sc := n.bgScans[path]
	n.mu.Unlock()
	if sc == nil {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.pending || sc.off != m.size {
		if !m.titleManual {
			m.titleSource = ""
		}
		return
	}
	if sc.meta.Speed.Supersedes(m.speed) {
		m.speed = sc.meta.Speed
	}
	m.outputTokens = sc.meta.OutputTokens
	m.contextRestartRecommended = sc.meta.ContextRestartRecommended()
	if m.codex && sc.meta.ModelAt.After(m.modelAt) {
		// A long tool result can push the latest turn_context out of the quick tail.
		m.model, m.modelAt = sc.meta.Model, sc.meta.ModelAt
	}
	if m.codex && sc.meta.ServiceTierAt.After(m.serviceTierAt) {
		m.serviceTier, m.serviceTierAt = sc.meta.ServiceTier, sc.meta.ServiceTierAt
	}
	if m.codex && sc.meta.EffortAt.After(m.effortAt) {
		// A long tool result can push the latest turn_context out of the quick tail.
		m.effort, m.effortAt = sc.meta.Effort, sc.meta.EffortAt
	}
	if sc.meta.TitleManual {
		m.title, m.titleManual, m.titleSource = sc.meta.Title, true, "manual"
	} else if sc.meta.TitleSource != "" {
		m.title, m.titleManual, m.titleSource = sc.meta.Title, false, sc.meta.TitleSource
	} else if m.codex {
		// Rollouts do not distinguish a manually chosen thread name from an automatic one.
		m.titleSource = ""
	} else if m.titleSource == "" {
		m.titleSource = "prompt"
	}
}

// transcriptHead reads a transcript's head up to its first prompt, for what belongs to the session's start: the first
// prompt, and start, the first directory its lines name (head.Cwd moves with every `cd` they record).
func transcriptHead(f *os.File) (head transcript.Meta, start string) {
	// The first prompt is usually in the first 32 KB. A Codex rollout puts its instructions first (the assistant's
	// AGENTS.md and snapshot: 74 KB in), so up to 256 KB is read while it has not turned up.
	buf := make([]byte, 32*1024)
	nr, _ := f.ReadAt(buf, 0)
	for {
		for _, line := range bytes.Split(buf[:nr], []byte("\n")) {
			if bytes.Contains(line, []byte(`"cwd"`)) || bytes.Contains(line, []byte(`"type":"user"`)) ||
				bytes.Contains(line, []byte(`"type":"UserMessage"`)) { // Codex rollouts: session_meta, then the first prompt
				transcript.ParseLine(0, line, &head)
				if start == "" {
					start = head.Cwd
				}
				if head.Cwd != "" && head.FirstUser != "" {
					break
				}
			}
		}
		if head.FirstUser != "" || nr < len(buf) || len(buf) >= 256*1024 {
			break
		}
		buf = make([]byte, len(buf)*2)
		nr, _ = f.ReadAt(buf, 0)
	}
	return head, start
}

// quickLine reads one line of a transcript's end into tm, as the quick read does: tail is the tree the line takes its
// place in (nil when the lines are not followed in one: lookBack).
func quickLine(line []byte, tm *transcript.Meta, tail *transcript.Tail) {
	// Skip parsing big tool results; they still count as activity (a turn that ended on one would otherwise look
	// finished earlier, and a look at it in the app in between would count as having seen the end). Not a compaction
	// summary or a meta line: no prompt (Claude Code compacts on its own while idle, and its 17 KB summary read as one
	// left the session working for hours, #5967); the parser reads them as what they are.
	if bytes.Contains(line, []byte(`"type":"user"`)) && len(line) > 8<<10 && !notAPrompt(line) {
		tm.ObserveSkippedUserLine(line)
		ts := lineTime(line)
		if ts.After(tm.LastActivity) {
			tm.LastActivity = ts
		}
		if !bytes.Contains(line, []byte(`"tool_result"`)) {
			tm.LastTool = nil // a long prompt: a new turn
			if !tm.Codex {
				tm.TurnOpen, tm.TurnClosed = true, false
				tm.ClaudeTurnStarted(bytes.Contains(line, []byte(`<task-notification>`))) // the last turn's error is over
			}
			if !bytes.Contains(line, []byte(`"isMeta":true`)) && ts.After(tm.LastPromptAt) {
				tm.LastPromptAt = ts
			}
		}
		if !tm.Codex && tail != nil {
			tail.Skipped(line) // its place in the tree: a rewound turn with it in is still found, and hidden whole
		}
		return
	}
	items := transcript.ParseLine(0, line, tm)
	if tail != nil {
		tail.Line(tm, items)
	}
}

// lookBackReach: how far before the tail lookBack reads.
const lookBackReach = transcripttail.Reach

// lookBack reads how the transcript stood before end, where a tail read's whole lines begin: the line the tail's
// start cuts is read whole here (a tool result or a prompt bigger than the tail is the last exchange). It reads one
// stretch of lines ending at end into one Meta, a longer one each time (twice the last, re-reading the newer lines, as
// transcripttail.LastWordsBefore does) up to lookBackReach, until that has what is asked for: the agent's last words,
// the last exchange, how the turn stands, the mode. Every field is then that Meta's, where the transcript stood at
// end: one window's turn, words or prompt is not kept beside another's, and a line read on its own does not stand for
// what the lines before it say (a local command ends the turn it starts only where the state before it is known).
// Only those fields and the last prompt's time are set; what is not found stays zero.
func lookBack(f *os.File, end int64, codex, words, exchange, turn, mode bool) quickMeta {
	var b quickMeta
	for chunk := int64(quickTail); end > 0; chunk = min(chunk*2, lookBackReach) {
		start := max(end-chunk, 0)
		buf := make([]byte, end-start)
		if n, err := f.ReadAt(buf, start); err != nil && n < len(buf) {
			break
		}
		lines := bytes.Split(buf, []byte("\n"))
		if start > 0 && len(lines) > 0 {
			lines = lines[1:] // a partial line: whole in the next, longer stretch
		}
		tm := transcript.Meta{Codex: codex}
		for _, line := range lines {
			if len(line) > 0 {
				quickLine(line, &tm, nil)
			}
		}
		b = quickMeta{lastText: tm.LastText, lastTextAt: tm.LastTextAt, activity: tm.LastActivity, lastPromptAt: tm.LastPromptAt,
			claudeTurn: tm.TurnOpen, turnClosed: tm.TurnClosed, codexClosedTurnID: tm.CodexClosedTurnID,
			mode: tm.PermissionMode, modeAt: tm.ModeAt, sandbox: tm.CodexSandbox}
		if (!words || b.lastText != "") && (!exchange || !b.activity.IsZero()) && (!turn || b.claudeTurn || b.turnClosed) &&
			(!mode || b.mode != "") || start == 0 || chunk == lookBackReach {
			break
		}
	}
	return b
}

// codexStaleWorking: how long herdr may say a Codex agent works while its rollout says the turn is over. Codex writes
// task_started the moment a turn begins; herdr reads "working" off the screen and can be a moment ahead of it.
const codexStaleWorking = 30 * time.Second

// codexTurnOver reports whether a Codex session herdr calls working has in fact finished: its rollout ends with the
// turn complete and nothing has been written for a while. herdr has said so for hours-old turns (2026-10-30: a turn
// finished two days earlier showed "working for 45 hours" for eight minutes, the rollout untouched).
func (n *Node) codexTurnOver(id string, now time.Time) bool {
	return n.codexTurnOverIn(id, now, nil)
}

func (n *Node) codexTurnOverIn(id string, now time.Time, pass *codexListPass) bool {
	// Paginated history can contain only older completed turns while a continuation runs. The live owner's
	// cached observation wins even when this node joined after turn/started and has not observed a turn ID.
	if _, thread, err := n.codexOwner(id); err == nil &&
		(thread.status == proto.StatusWorking || thread.status == proto.StatusBlocked) {
		return false
	}
	p := n.transcriptPathIn(id, pass)
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	return n.meta(p, st).turnClosed && now.Sub(st.ModTime()) > codexStaleWorking
}

// lineTime reads a transcript line's own "timestamp" without decoding it (Claude Code writes it after the message, so
// the last one; quotes inside the message are escaped and do not match).
// notAPrompt: a user line that is not a message to the agent whatever its size: a compaction's summary or a meta line.
func notAPrompt(line []byte) bool {
	return bytes.Contains(line, []byte(`"isCompactSummary":true`)) || bytes.Contains(line, []byte(`"isMeta":true`))
}

func lineTime(line []byte) time.Time {
	key := []byte(`"timestamp":"`)
	i := bytes.LastIndex(line, key)
	if i < 0 {
		return time.Time{}
	}
	rest := line[i+len(key):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, string(rest[:j]))
	return t
}

// activityOf is the session's activity for the list: what its latest tool call does, and since when.
func (m quickMeta) activityOf() (string, *time.Time) {
	if m.doing == "" {
		return "", nil
	}
	if m.doingAt.IsZero() {
		return m.doing, nil
	}
	at := m.doingAt.UTC()
	return m.doing, &at
}

// lastUsed is when the session was last really used: its last exchange, or the file time for transcripts
// without any (only system lines).
func lastUsed(m quickMeta, st os.FileInfo) time.Time {
	if !m.activity.IsZero() {
		return m.activity.UTC()
	}
	return st.ModTime().UTC()
}

// untitled gives sessions without an AI title (headless ones) their first message as title (promptTitle).
func untitled(s *proto.Session, m quickMeta) {
	if s.Title != "" {
		return
	}
	if title := promptTitle(m.first); title != "" {
		s.Title = title
		if m.titleSource == "prompt" {
			s.TitleSource = proto.TitleSourcePrompt
		}
	}
}

// promptTitle is a first message as a row's title: what the user wrote, not the context blocks it starts with (the
// main conversation's, a Feishu /agent request's), or the title of the block it is made of only ("continue the old main
// chat"). Stripped first, clipped after. An agy pane's row falls back to it too (agytitle.go).
func promptTitle(first string) string {
	first = transcript.Title(first)
	if first == "" {
		return ""
	}
	r := []rune(snippet(first))
	if len(r) > 40 {
		r = append(r[:40], '…')
	}
	return string(r)
}
