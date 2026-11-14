package node

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// A new Claude's first message (start in control.go) is typed into its input box once the box shows. Claude Code is
// not always done drawing then: one that updated itself on start ("Updated to latest…") and showed the auto mode
// notice took the typed message and dropped it, and the agent sat idle with an empty box for an hour and a half
// (#2423). So the first message counts as delivered only once the transcript has it. Until then it is entered (still
// in the box) or typed again (gone) once the box is ready again, a few times; when it still does not go in, or the
// screen is not one to type into, the session is listed as needing the user, with why, instead of idle.

// firstPromptWait is how long the transcript has to show the first message after it was typed; firstPromptRecover how
// long Claude then has to show its input box again before the message is typed once more. Tests shorten both.
var firstPromptWait, firstPromptRecover = 15 * time.Second, 30 * time.Second

// firstPromptTries is how many times the first message is typed at most.
const firstPromptTries = 3

// firstPrompt types the first message into the new Claude in pane (a is how waitReady left it) and, in the
// background, makes sure the session's transcript gets it: the start answers as it did (a sync start's caller has
// a budget of its own), and messages sent to the session meanwhile wait for the check (the typing lock), so a
// message typed again cannot mix with them. An error: the host refused the first message (nothing went in).
func (n *Node) firstPrompt(ctx context.Context, pane, id, prompt string, a *host.Agent) error {
	prompt = inertControls(keepImagePaths(prompt)) // the same image-path/paste form as other Claude sends
	since := time.Now()
	if a != nil && a.AgentStatus == proto.StatusBlocked {
		// A dialog of Claude's own has the keyboard: the message would answer it. Held until the dialog is answered
		// (from a screen card or at the computer), then typed (firstAfterDialog).
		n.holdFirstForDialog(pane, id, prompt, since)
		return nil
	}
	watch := n.watchPrompt(id, prompt)
	if watch == nil {
		watch = &promptWatch{want: echoWords(prompt)} // a new transcript can appear only after the first prompt
	}
	watch.after = since
	draft := n.firstDraftFor(id, prompt)
	err := n.hostPrompt(ctx, pane, prompt)
	if errors.Is(err, host.ErrAgentBlocked) {
		n.holdFirstForDialog(pane, id, prompt, since)
		return nil
	}
	if err != nil {
		return fmt.Errorf("send first message: %w", err)
	}
	n.typing.typed(pane, prompt)
	unlock := n.typing.lock(id)
	parent := n.firstPromptParent
	if parent == nil {
		parent = context.Background()
	}
	n.firstWait.Add(1)
	go func() {
		defer n.firstWait.Done()
		defer unlock()
		bctx, cancel := context.WithTimeout(parent,
			firstPromptTries*firstPromptWait+(firstPromptTries-1)*firstPromptRecover+15*time.Second)
		defer cancel()
		if why := n.confirmFirst(bctx, pane, id, prompt, since, watch, draft); why != "" {
			log.Printf("start %s: %s", id, why)
			n.lostFirst.set(id, why, since)
			n.notifySessions()
		}
	}()
	return nil
}

// firstAfterDialogWait is how long a first message held back by a dialog waits for it to be answered;
// firstAfterDialogPoll how often the pane is looked at meanwhile. Tests shorten both.
var firstAfterDialogWait, firstAfterDialogPoll = 30 * time.Minute, time.Second

// holdFirstForDialog lists the session as needing the user (lostDialogHeld) and, in the background, types its first
// message once the dialog that kept it out is gone (firstAfterDialog).
func (n *Node) holdFirstForDialog(pane, id, prompt string, since time.Time) {
	n.lostFirst.set(id, lostDialogHeld, since)
	wait, poll := firstAfterDialogWait, firstAfterDialogPoll // read here: a goroutine still waiting never races a test setting them
	n.dialogWait.Add(1)
	go func() {
		defer n.dialogWait.Done()
		n.firstAfterDialog(pane, id, prompt, since, wait, poll)
	}()
}

// firstAfterDialog waits for the dialog that kept a new session's first message out to be answered, looking every
// poll, then types the message and checks it went in as firstPrompt does. Before, the message was dropped and the user
// had to send the task again. It gives up when anything else reaches the session first (a message from the phone or
// the computer clears or replaces the lost mark: nothing is typed twice), when claude leaves the pane (the dialog's
// "exit"), or after wait, leaving the session listed as needing the user as before.
func (n *Node) firstAfterDialog(pane, id, prompt string, since time.Time, wait, poll time.Duration) {
	ours := func() bool {
		p, ok := n.lostFirst.get(id)
		return ok && p.at.Equal(since) && !n.promptedSince(id, since)
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		time.Sleep(poll)
		if !ours() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		ready, gone := n.boxFreeAfterDialog(ctx, pane)
		cancel()
		if gone {
			log.Printf("start %s: claude left the pane while its first message waited for a dialog", id)
			return
		}
		if !ready {
			continue
		}
		unlock := n.typing.lock(id) // messages sent meanwhile wait, as for a first message being checked
		if !ours() {
			unlock()
			return
		}
		// Look again under the lock: a send that held it may have left its text in the box (its delivery not
		// confirmed, so ours still holds), and the held message must not be pasted onto that draft and sent with it.
		ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
		ready, gone = n.boxFreeAfterDialog(ctx, pane)
		cancel()
		if gone {
			unlock()
			log.Printf("start %s: claude left the pane while its first message waited for a dialog", id)
			return
		}
		if !ready {
			unlock()
			continue
		}
		why := n.typeFirstAfterDialog(pane, id, prompt)
		unlock()
		if why == errDialogBack {
			continue // another dialog came up first: wait for it too
		}
		if why != "" {
			log.Printf("start %s: %s", id, why)
			n.lostFirst.set(id, why, since)
		} else {
			log.Printf("start %s: first message delivered after its dialog was answered", id)
			n.lostFirst.clear(id)
		}
		n.notifySessions()
		return
	}
	log.Printf("start %s: the dialog holding the first message was not answered within %s", id, wait)
}

// errDialogBack: typeFirstAfterDialog found a dialog on screen again; nothing was typed.
const errDialogBack = "dialog"

// typeFirstAfterDialog types a held first message and waits for the transcript to have it: "" once it does, else why
// not (errDialogBack when a dialog has the keyboard again). The caller holds the session's typing lock.
func (n *Node) typeFirstAfterDialog(pane, id, prompt string) string {
	ctx, cancel := context.WithTimeout(context.Background(),
		firstPromptTries*firstPromptWait+(firstPromptTries-1)*firstPromptRecover+15*time.Second)
	defer cancel()
	typedAt := time.Now()
	watch := n.watchPrompt(id, prompt) // as firstPrompt: the transcript may only appear with this message
	if watch == nil {
		watch = &promptWatch{want: echoWords(prompt)}
	}
	watch.after = typedAt
	draft := n.firstDraftFor(id, prompt)
	err := n.hostPrompt(ctx, pane, prompt)
	if errors.Is(err, host.ErrAgentBlocked) {
		return errDialogBack
	}
	if err != nil {
		return fmt.Sprintf("首条消息没有送进去（%v），Agent 还没开始：在这里把任务重发一遍。", err)
	}
	n.typing.typed(pane, prompt)
	return n.confirmFirst(ctx, pane, id, prompt, typedAt, watch, draft)
}

// boxFreeAfterDialog: Claude in pane is past its dialog, its input box on screen and empty (ready); gone when claude
// is no longer in the pane.
func (n *Node) boxFreeAfterDialog(ctx context.Context, pane string) (ready, gone bool) {
	a, err := n.paneAgent(ctx, pane)
	if errors.Is(err, host.ErrNoAgent) || err == nil && a.Agent != "" && a.Agent != "claude" {
		return false, n.shellInFront(ctx, pane)
	}
	if err != nil || a.AgentStatus == proto.StatusBlocked || a.AgentStatus == proto.StatusWorking {
		return false, false
	}
	s, err := n.readPaneANSI(ctx, pane, "visible", 60)
	if err != nil {
		return false, false
	}
	code, in := screen.CheckClaude(s)
	return code == "" && in.Box, false
}

// confirmFirst waits for the transcript to get the first message typed at since, typing it again when it was
// dropped. It returns "" once the transcript has it, else why it did not go in, for the user.
func (n *Node) confirmFirst(ctx context.Context, pane, id, prompt string, since time.Time, watch *promptWatch, draft *firstDraft) string {
	typedAt := since
	for try := 1; ; try++ {
		taken, entered := n.firstPromptTaken(ctx, pane, id, prompt, typedAt, watch, draft)
		if taken {
			return ""
		}
		if ctx.Err() != nil || !watch.usable() {
			return lostTimeout
		}
		if entered {
			// Enter may have submitted it even if the transcript is late. Never paste it again after that;
			// the owned draft was retried in firstPromptTaken, and an empty box is not proof of a dropped prompt.
			why := n.readyForFirstPrompt(ctx, pane, prompt, draft)
			if n.firstWatchTaken(id, watch) {
				return ""
			}
			if why != "" {
				return why
			}
			return lostTimeout
		}
		if try == firstPromptTries {
			return fmt.Sprintf("首条消息送了 %d 次都没进去，Agent 还没开始：看看电脑上的终端，或者在这里把任务重发一遍。", firstPromptTries)
		}
		log.Printf("start %s: the first message did not reach the transcript (try %d), typing it again", id, try)
		why := n.readyForFirstPrompt(ctx, pane, prompt, draft)
		if n.firstWatchTaken(id, watch) {
			return "" // delivery can become visible during the readiness wait
		}
		if why != "" {
			return why
		}
		if !watch.usable() {
			return lostTimeout
		}
		typedAt = time.Now()
		draft = &firstDraft{} // a dropped paste may still have consumed its chip IDs
		err := n.hostPrompt(ctx, pane, prompt)
		if errors.Is(err, host.ErrAgentBlocked) {
			return lostDialog
		}
		if err != nil {
			return fmt.Sprintf("首条消息没有送进去（%v），Agent 还没开始：在这里把任务重发一遍。", err)
		}
		n.typing.typed(pane, prompt)
	}
}

// User-facing reasons a first message did not go in.
const (
	lostDialog = "终端上开着一个对话框，首条消息没有送进去，Agent 还没开始：先在终端里处理它，再在这里把任务重发一遍。"
	// lostDialogHeld: a dialog kept the first message out and the node types it once the dialog is answered
	// (firstAfterDialog).
	lostDialogHeld = "终端上开着一个对话框，首条消息还没有送进去，Agent 还没开始：在这里或终端上回答它，回答后首条消息会自动送进去。"
	lostDraft      = "首条消息停在输入框里没有发出去，Agent 还没开始：在电脑上按回车，或者清空输入框后在这里重发。"
	lostOther      = "输入框里有别的内容，首条消息没有再送，Agent 还没开始：看看电脑上的终端，再在这里把任务重发一遍。"
	lostNoBox      = "Claude 没有回到可以输入的状态，首条消息没有送进去，Agent 还没开始：看看电脑上的终端，再在这里把任务重发一遍。"
	lostTimeout    = "等首条消息送进去超时了，Agent 还没开始：看看电脑上的终端，或者在这里把任务重发一遍。"
	// lostUltracode: the start's ultracode command may still sit in the input box (ultracode.go), or the box could
	// not be read after it.
	lostUltracode = "ultracode 的开关命令可能还停在电脑上的输入框里，首条消息没有送进去，Agent 还没开始：看看电脑上的终端，再在这里把任务重发一遍。"
)

// firstPromptTaken waits for the transcript to show a message from since on, for firstPromptWait after it was typed.
// While the owned draft remains (including image/long-paste placeholders), retry only Enter, with submitGrace
// between attempts: image decoding can swallow an early Enter. Never press keys on a dialog or another draft.
// entered records any attempted Enter, even a failed RPC: its delivery is ambiguous, so the caller must not re-paste.
// At the deadline, working also counts as taken: typing it again would run it twice.
func (n *Node) firstPromptTaken(ctx context.Context, pane, id, prompt string, typedAt time.Time, watch *promptWatch, draft *firstDraft) (taken, entered bool) {
	deadline := typedAt.Add(firstPromptWait)
	nextEnter := typedAt.Add(submitGrace)
	for {
		select {
		case <-ctx.Done():
			return false, entered
		case <-time.After(firstPromptPoll):
		}
		if n.firstWatchTaken(id, watch) {
			return true, entered
		}
		if time.Now().After(deadline) {
			break
		}
		if s, err := n.readPaneANSI(ctx, pane, "visible", 60); err == nil {
			if code, in := screen.CheckClaude(s); code == screen.InputDraft && draft.matches(in.Text, prompt) && !time.Now().Before(nextEnter) {
				// The transcript may have appeared during the screen read. Do not send another Enter then.
				if n.firstWatchTaken(id, watch) {
					return true, entered
				}
				entered = true
				n.logf("start %s: first prompt still in the input box, pressing enter", id)
				_ = n.sendKeys(ctx, pane, "enter")
				nextEnter = time.Now().Add(submitGrace)
			}
		}
	}
	a, err := n.paneAgent(ctx, pane)
	return err == nil && a.AgentStatus == proto.StatusWorking, entered
}

// firstWatchTaken attaches lazily if Claude has not created the file before typing. Unlike quick metadata's
// bounded head/tail, promptWatch reads complete image-bearing lines, and filters older matching rows on late attach.
func (n *Node) firstWatchTaken(id string, watch *promptWatch) bool {
	if watch.path == "" {
		watch.path = n.transcriptPath(id)
	}
	return watch.took()
}

// firstDraft pins the first observed paste placeholder before any recovery key. Claude increments its chip IDs
// for later pastes: the input lock serializes Persona sends, but cannot prevent a person replacing the terminal draft.
// A changed placeholder or appended text must not receive our next Enter, even when the original prompt was long.
type firstDraft struct {
	placeholder   string
	expectedPaste int // a fresh session assigns image chips first, then the text chip
}

func (n *Node) firstDraftFor(id, prompt string) *firstDraft {
	d := &firstDraft{}
	if p := n.transcriptPath(id); p != "" {
		if st, err := os.Stat(p); err == nil && (st.Size() >= 256*1024 || !n.meta(p, st).lastPromptAt.IsZero()) {
			return d // resumed sessions may already have paste IDs; pin their first observed draft instead
		}
	}
	d.expectedPaste = 1 + strings.Count(attachedNote.FindString(prompt), "\n"+imagePathLine)
	return d
}

func (d *firstDraft) matches(box, prompt string) bool {
	if !pendingPrompt(box, prompt) {
		return false
	}
	if pastePlaceholder.MatchString(box) {
		if d.expectedPaste > 0 {
			_, number, _ := strings.Cut(box, "[Pasted text #")
			expected := fmt.Sprint(d.expectedPaste)
			if !strings.HasPrefix(number, expected+" ") && !strings.HasPrefix(number, expected+"]") {
				return false // another paste arrived before our first screen read
			}
		}
		key := strings.Join(strings.Fields(box), "") // ignore wrapping, not chip IDs or added text
		if d.placeholder == "" {
			d.placeholder = key
		}
		return d.placeholder == key
	}
	return d.placeholder == ""
}

// firstPromptPoll is how often firstPromptTaken looks at the transcript.
var firstPromptPoll = 500 * time.Millisecond

// readyForFirstPrompt waits for Claude's input box to show again (accepting a trust dialog on the way) and checks the
// box is empty: "" when the message can be typed again, else why not.
func (n *Node) readyForFirstPrompt(ctx context.Context, pane, prompt string, draft *firstDraft) string {
	rctx, cancel := context.WithTimeout(ctx, firstPromptRecover)
	defer cancel()
	a, err := n.waitReady(rctx, pane, nil)
	switch {
	case ctx.Err() != nil:
		return lostTimeout
	case err != nil:
		return lostNoBox
	case a.AgentStatus == proto.StatusBlocked:
		return lostDialog
	}
	s, err := n.readPaneANSI(ctx, pane, "visible", 60)
	if err != nil {
		return lostNoBox
	}
	switch code, in := screen.CheckClaude(s); {
	case code == screen.DialogOpen:
		return lostDialog
	case code == screen.InputDraft && draft.matches(in.Text, prompt):
		return lostDraft
	case code == screen.InputDraft:
		return lostOther
	}
	return ""
}

// promptedSince: the session's transcript has a message to the agent from since on (a second earlier, for the
// transcript's own clock rounding).
func (n *Node) promptedSince(id string, since time.Time) bool {
	p := n.transcriptPath(id)
	if p == "" {
		return false
	}
	st, err := os.Stat(p)
	if err != nil {
		return false
	}
	at := n.meta(p, st).lastPromptAt
	return !at.IsZero() && !at.Before(since.Add(-time.Second))
}

// lostPrompts are the sessions whose first message did not go in, with why: listed as needing the user until a
// message reaches the agent (sent from the phone, or typed at the computer). Kept in memory: a restarted node lists
// them as what herdr says (idle).
type lostPrompts struct {
	mu sync.Mutex
	m  map[string]lostPrompt
}

type lostPrompt struct {
	why string
	at  time.Time // when the start typed it
}

func (l *lostPrompts) set(id, why string, at time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]lostPrompt{}
	}
	l.m[id] = lostPrompt{why, at}
}

func (l *lostPrompts) clear(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, id)
}

func (l *lostPrompts) get(id string) (lostPrompt, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	p, ok := l.m[id]
	return p, ok
}

// lostFirstPrompt is why the session waits for the user because its first message did not go in, "" when it does not
// (any more: it works, or its transcript got a message since).
func (n *Node) lostFirstPrompt(id, status string) string {
	p, ok := n.lostFirst.get(id)
	if !ok {
		return ""
	}
	if status == proto.StatusWorking || n.promptedSince(id, p.at) {
		n.lostFirst.clear(id)
		n.dropHandoff(id, nil) // a switch's handoff waiting for this message (engine_switch.go) missed it
		return ""
	}
	return p.why
}
