package node

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/host"
)

// A start that ended with shell_waiting or agent_start_stalled keeps its tab (keepStartTab, #2570). Whoever remedies it
// (the assistant's main conversation, through the hub) may type into that tab while it is kept and no agent runs
// there (start.keys), and start again in it (session.start's in_pane) instead of opening another.

// startKeysText and startKeysCount bound what one start.keys types: a short answer, not a command line.
const (
	startKeysText  = 32 // runes of text
	startKeysCount = 8  // named keys
)

// keptStartTab is the kept tab of a failed start on pane while it is still kept and no agent runs there.
func (n *Node) keptStartTab(pane string) (keptStart, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.keptStartTabLocked(pane)
}

// keptStartTabLocked is keptStartTab with n.mu already held. Reusing a tab needs its expiry, binding and agent checks
// plus its deletion in one critical section, so two session.start requests cannot claim the same kept tab.
func (n *Node) keptStartTabLocked(pane string) (keptStart, bool) {
	k, ok := n.keptStarts[pane]
	if !ok || k.taken || !n.keptTabFreeLocked(pane, k) {
		return keptStart{}, false
	}
	return k, true
}

// keptTabFreeLocked: the kept tab k on pane has not expired, and no session is bound to it nor any agent runs there.
// n.mu is held.
func (n *Node) keptTabFreeLocked(pane string, k keptStart) bool {
	if !time.Now().Before(k.until) {
		return false
	}
	if _, bound := n.panes[pane]; bound {
		return false
	}
	for _, a := range n.agents {
		if a.PaneID == pane {
			return false
		}
	}
	return true
}

// errStartTabGone refuses typing into, or starting in, a pane that is not a failed start's kept tab (any more): it
// expired, an agent runs there, or it never was one.
var errStartTabGone = refusal("start_tab_gone", "that tab is not a failed start's kept tab any more (it expired, or an agent runs there)", nil)

// startTypingKey carries withStartTyping's pane.
type startTypingKey struct{}

// withStartTyping says what is typed with ctx goes into the kept tab of a failed start on pane, to answer the question
// its shell stopped on (start.continue, start.answer, start.keys), the node having checked the tab is still kept and
// no agent runs there. A host that otherwise types text only to an agent it sees in front lets it through then, into
// a terminal it opened itself and that still runs the same process (Orca: typeGate, #17040).
func withStartTyping(ctx context.Context, pane string) context.Context {
	return context.WithValue(ctx, startTypingKey{}, pane)
}

// startTypingOf is the kept tab ctx types into (withStartTyping), "" for any other typing.
func startTypingOf(ctx context.Context) string {
	pane, _ := ctx.Value(startTypingKey{}).(string)
	return pane
}

// startKeys types text (as is, no enter) and then presses keys (Persona's key names: "enter", "esc", "ctrl+c", "y"…)
// into the kept tab of a failed start on pane. Only such a tab takes them, and only a short answer. The tab is marked
// typed (keptStart.typed): the Retry that follows waits out a question the keys answered as in a new tab.
func (n *Node) startKeys(ctx context.Context, pane, text string, keys []string) error {
	if pane == "" || text == "" && len(keys) == 0 {
		return errors.New("start.keys needs a pane and text or keys")
	}
	if utf8.RuneCountInString(text) > startKeysText || len(keys) > startKeysCount {
		return refusal("too_large", "start.keys types at most 32 characters and 8 keys", nil)
	}
	// start.keys must not type into a pane that another request claims as a fresh start while it waits.
	unlock := n.typing.lock(workAgentPaneLockPrefix + pane)
	defer unlock()
	if _, ok := n.keptStartTab(pane); !ok {
		return errStartTabGone
	}
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return err
	}
	ctx = withStartTyping(ctx, pane)
	n.markKeptTyped(pane) // before the keys: a start that claims the tab meanwhile waits too
	if text != "" {
		if err := h.SendText(ctx, ref, text); err != nil {
			return err
		}
	}
	if len(keys) > 0 {
		return h.SendKeys(ctx, ref, keys...)
	}
	return nil
}

// The lifecycle of a kept tab that a start is named to (session.start's in_pane, #2570, #9534). One owner per step:
//
//   - keep: startTabFailed keeps the tab (keepStartTab) with an expiry (expireKeptStart closes it then).
//   - claim: n.start, sync and async alike, holds it (claimKeptTab) before its own checks. Nothing is closed: a held
//     tab is only marked taken, so its expiry, start.keys and another start leave it alone (another start is refused).
//   - use: newTab, the one place a start gets a tab, settles the claim (keptClaim.tab). It checks the tab again first:
//     one a session was bound to or an agent came up in meanwhile is let go untouched (a new tab instead). Else it
//     starts in the tab, or, for an Orca tab whose one launch is used (orcaHost.Run), closes it and opens a new one.
//     Every check of the start ran before newTab, so a refused start never got here.
//   - roll back: a claim newTab did not settle (the start was refused or failed before a tab), or a close that
//     failed, puts the tab back as kept with its expiry (keptClaim.release); a failed close also fails the start, so no
//     new tab opens beside the stalled agent that may still run there.
//
// After use the start owns the tab it runs in, as any start does its new tab (startTabFailed keeps or closes it).

// errStartTabTaken refuses a start named to a kept tab another start holds.
var errStartTabTaken = refusal("start_tab_taken", "another start is using that kept tab", nil)

// errKeptTabUnavailable leaves a tab that recovered or expired during its close lookup untouched by the retry.
var errKeptTabUnavailable = errors.New("the kept tab is no longer free")

// keptClaim is a start's hold on a kept tab (claimKeptTab). It is used by one start, from one goroutine.
type keptClaim struct {
	n       *Node
	pane    string
	k       keptStart
	settled bool
}

// keptClaimKey carries a start's claim from n.start to its newTab (keptClaimOf); it never leaves that start.
type keptClaimKey struct{}

// keptClaimOf is the claim of the start ctx belongs to, or nil.
func keptClaimOf(ctx context.Context) *keptClaim {
	c, _ := ctx.Value(keptClaimKey{}).(*keptClaim)
	return c
}

// claimKeptTab holds the kept tab on pane for a start. nil, nil: pane is "" or not a kept tab (any more), and the
// start opens a new tab. A tab another start holds is refused.
func (n *Node) claimKeptTab(pane string) (*keptClaim, error) {
	if pane == "" {
		return nil, nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if k, ok := n.keptStarts[pane]; ok && k.taken {
		return nil, errStartTabTaken
	}
	k, ok := n.keptStartTabLocked(pane)
	if !ok {
		return nil, nil
	}
	k.taken = true
	n.keptStarts[pane] = k
	return &keptClaim{n: n, pane: pane, k: k}, nil
}

// tab settles the claim for newTab: the kept tab to start in, or nil, nil once it is closed for a new one (an Orca
// tab whose one launch is used). A tab that did not close is released (still kept) and its error returned.
func (c *keptClaim) tab(ctx context.Context) (*host.Created, error) {
	if c == nil || c.settled {
		return nil, nil
	}
	// Checked again now, not only when claimed: the stalled agent may have come up there during the start's checks
	// (its SessionStart hook binds the pane). Such a tab is left as it is, kept until its expiry, which leaves a bound
	// tab open, and the start opens a new tab as for a pane that is not kept.
	c.n.mu.Lock()
	free := c.n.keptTabFreeLocked(c.pane, c.k)
	c.n.mu.Unlock()
	if !free {
		c.release()
		return nil, nil
	}
	if o, isOrca := c.k.launch.(*orcaHost); !isOrca || o.launchable(c.k.created.Pane) {
		c.drop()
		return c.k.created, nil
	}
	if err := c.closeStalledTab(ctx); err != nil {
		c.release()
		if errors.Is(err, errKeptTabUnavailable) {
			return nil, nil // as above: leave a recovered tab open and start in a new one
		}
		return nil, fmt.Errorf("the stalled start's tab did not close, so no new one was opened: %w", err)
	}
	c.drop()
	return nil, nil
}

// closeStalledTab closes the kept tab of a stalled Orca start, whose one launch is used, for the user's retry to open a
// new one. The local free check is repeated after each terminal lookup, just before sending the close: an agent
// that recovered during lookup is left open (docs/integrations/engines.md "Agents run in a terminal"). This is not
// atomic with recovery after the check or with runtime execution of the close.
func (c *keptClaim) closeStalledTab(ctx context.Context) error {
	o, ok := c.k.launch.(*orcaHost)
	if !ok {
		return nil
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return o.guardedClose(cctx, c.k.created.Pane, func() error {
		c.n.mu.Lock()
		free := c.n.keptTabFreeLocked(c.pane, c.k)
		c.n.mu.Unlock()
		if !free {
			return errKeptTabUnavailable
		}
		return nil
	})
}

// drop ends the claim with the tab no longer kept: the start runs in it, or closed it.
func (c *keptClaim) drop() {
	c.settled = true
	c.n.mu.Lock()
	delete(c.n.keptStarts, c.pane)
	c.n.mu.Unlock()
}

// release ends an unsettled claim with the tab kept again until its expiry (a timer for it: its own may have fired
// while it was held; expireKeptStart acts once for that expiry). Safe on nil and after tab.
func (c *keptClaim) release() {
	if c == nil || c.settled {
		return
	}
	c.settled = true
	k := c.k
	k.taken = false
	c.n.mu.Lock()
	c.n.keptStarts[c.pane] = k
	c.n.mu.Unlock()
	time.AfterFunc(time.Until(k.until), func() { c.n.expireKeptStart(c.pane, k.until) })
}
