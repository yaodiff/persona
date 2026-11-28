package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	queueSettlePoll = 150 * time.Millisecond
	queueSettleWait = 2500 * time.Millisecond
)

// editQueue takes the queued input back out of claude (the TUI's "press up to edit queued messages" pulls every
// queued message into the input box), clears the input, and re-queues the ones listed in keep, in order.
// It returns the messages that were queued so the phone can put the edited one in its composer.
func (n *Node) editQueue(ctx context.Context, id string, keep []int) ([]string, error) {
	pane, err := n.paneFor(ctx, id)
	if err != nil {
		return nil, n.paneless(id, "queue_edit_unsupported", "its queue is on its own screen, in no pane Persona can type in", err)
	}
	unlock, err := n.inputTurn(ctx, id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	p := n.transcriptPath(id)
	if p == "" {
		return nil, fmt.Errorf("transcript for session %s not found", id)
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	beforeSize, beforeMod := st.Size(), st.ModTime()
	queue := n.metaNow(p, st).queue
	if len(queue) == 0 {
		return nil, fmt.Errorf("nothing is queued")
	}
	reconcile := false
	defer func() {
		if reconcile {
			n.forgetMeta(p)
			n.notifySessionsSoon()
			n.watchQueueSettle(p, beforeSize, beforeMod)
		}
	}()
	if err := n.sendKeys(ctx, pane, "up"); err != nil {
		return nil, err
	}
	reconcile = true
	time.Sleep(300 * time.Millisecond)
	// ctrl+u only clears the current line: walk back through every line of the pulled-back text.
	lines := 0
	for _, q := range queue {
		lines += strings.Count(q, "\n") + 1
	}
	for i := 0; i < lines; i++ {
		_ = n.sendKeys(ctx, pane, "ctrl+u", "backspace")
	}
	_ = n.sendKeys(ctx, pane, "ctrl+u")
	for _, k := range keep {
		if k >= 0 && k < len(queue) {
			if err := n.promptClaude(ctx, pane, queue[k]); err != nil {
				return queue, fmt.Errorf("re-queue: %w", err)
			}
			time.Sleep(150 * time.Millisecond)
		}
	}
	return queue, nil
}

// watchQueueSettle briefly follows a transcript after a queue mutation so asynchronous queue-operation
// writes (popOne, popAll, and re-enqueued items) invalidate cached meta and trigger a session push.
func (n *Node) watchQueueSettle(p string, beforeSize int64, beforeMod time.Time) {
	if n == nil || p == "" {
		return
	}
	parent := n.firstPromptParent
	if parent == nil {
		parent = context.Background()
	}
	n.firstWait.Add(1)
	go func() {
		defer n.firstWait.Done()
		ctx, cancel := context.WithTimeout(parent, queueSettleWait)
		defer cancel()
		tick := time.NewTicker(queueSettlePoll)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				st, err := os.Stat(p)
				if err != nil {
					continue
				}
				if st.Size() != beforeSize || !st.ModTime().Equal(beforeMod) {
					beforeSize, beforeMod = st.Size(), st.ModTime()
					n.forgetMeta(p)
					n.notifySessionsSoon()
				}
			}
		}
	}()
}

// paneless is a queue call's error when paneFor found no pane: a refusal with code for a session a claude outside
// Persona's panes has open in none (Claude's desktop app, a background session), whose queued messages still go in
// order at its next step (#12789); err itself for any other session, such as one no claude has open.
func (n *Node) paneless(id, code, why string, err error) error {
	if c, ok := n.outsideClaude(id); ok && outsidePane(c) == "" {
		return refusal(code, why, nil)
	}
	return err
}

// offersSendNow: the screen shows a queued message with a send-now Persona knows how to press (sendNowOffer,
// sendnow_hint.go).
func offersSendNow(screen string) bool {
	_, keys := sendNowOffer(screen)
	return keys != nil
}

// sendNowWait is how long the node looks for the send-now hint to go once the key is pressed.
var sendNowWait = 3 * time.Second

// sendNowScrollWait is how long the node waits for a scrolled-up Claude Code to be back at its end.
var sendNowScrollWait = time.Second

// sendQueuedNow gives the agent what waits in its queue now (#6088, #14871), with the CLI's own send-now keys named
// by its hint (Claude Code's ctrl+enter / ctrl+x ctrl+s, Antigravity CLI's alt+m). Pressed only while the screen offers
// it: that is the queue right now (the transcript says a message was taken about a second late, and a key pressed then
// would hit a turn that already has it), and an older CLI without it offers nothing.
func (n *Node) sendQueuedNow(ctx context.Context, id string) error {
	a, hasAgent := n.agent(id)
	if hasAgent && a.Agent != "claude" && a.Agent != agyAgent {
		return refusal("send_now_unsupported", "only Claude Code and Antigravity CLI can send queued messages now", nil)
	}
	pane, err := n.paneFor(ctx, id)
	if err != nil {
		return n.paneless(id, "send_now_unsupported", "its send-now is a key on its own screen, in no pane Persona can press it in", err)
	}
	screen, err := n.readPane(ctx, pane, "visible", 80)
	if err != nil {
		return err
	}
	// A fullscreen Claude Code scrolled up shows older lines, with the queued message and its hint off screen (#4537):
	// back to the end with its own scroll key, which sends nothing, and the hint is read there.
	if jump, ok := scrolledUp(screen); ok && n.sendKeys(ctx, pane, jump...) == nil {
		for deadline := time.Now().Add(sendNowScrollWait); time.Now().Before(deadline); {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
			if s, err := n.readPane(ctx, pane, "visible", 80); err == nil {
				screen = s
				if _, still := scrolledUp(s); !still {
					break
				}
			}
		}
	}
	p := n.transcriptPath(id)
	var beforeSize int64
	var beforeMod time.Time
	hasStat := false
	if p != "" {
		if st, err := os.Stat(p); err == nil {
			hasStat = true
			beforeSize, beforeMod = st.Size(), st.ModTime()
		}
	}
	hint, keys := sendNowOffer(screen)
	if keys == nil {
		// Something is queued but its hint is not one Persona can press: nothing pressed, and no guess at the keys.
		if hint != "" {
			return refusal("send_now_unrecognized", fmt.Sprintf("the screen offers send-now as %q, keys Persona cannot press", hint), nil)
		}
		if p != "" {
			if st, err := os.Stat(p); err == nil && len(n.metaNow(p, st).queue) > 0 {
				return refusal("send_now_unrecognized", "messages are queued but the screen shows no send-now hint Persona knows", nil)
			}
		}
		if hasAgent && a.Agent == agyAgent && n.hasAgyQueued(id, pane) {
			return refusal("send_now_unrecognized", "messages are queued but the screen shows no send-now hint Persona knows", nil)
		}
		return refusal("nothing_queued", "nothing is queued for the agent any more", nil)
	}
	if err := n.sendKeys(ctx, pane, keys...); err != nil {
		return err
	}
	for deadline := time.Now().Add(sendNowWait); time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
		if s, err := n.readPane(ctx, pane, "visible", 80); err == nil && !offersSendNow(s) {
			if hasAgent && a.Agent == agyAgent {
				n.clearAgyQueue(id, pane)
			}
			if p != "" {
				n.forgetMeta(p)
				if hasStat {
					n.watchQueueSettle(p, beforeSize, beforeMod)
				}
			}
			n.notifySessionsSoon()
			return nil
		}
	}
	// the key went in and the queue is still shown: perhaps it was taken, perhaps not (not "none", as a refusal says)
	return &rpc.Error{Code: "send_now_in_doubt", Message: "the queued messages are still shown as queued", Data: json.RawMessage(`{"delivered":"unknown"}`)}
}
