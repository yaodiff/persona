package node

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Antigravity CLI (agy) buffers follow-up prompts typed while busy in TUI memory and draws them above the prompt as
// `<preview>... (alt+m to send immediately)` without writing USER_INPUT to transcript.jsonl until consumed (#14871).
// agyQueues tracks both full prompts sent through Persona while busy (until consumed in transcript.jsonl or idle) and
// previews read off busy agy panes so Session.Queued stays populated and deduplicated.

// agyQueuePollEvery is how often a busy agy pane's screen is read in the background for queued lines. Tests shorten it.
var agyQueuePollEvery = 2 * time.Second

// agyQueuedLineRE matches an agy TUI queued follow-up line: `<preview> (<keys> to send immediately)`.
var agyQueuedLineRE = lazyre.New(`(?i)^(.*?)\s+\(([^)\n]+?)\s+to\s+send\s+immediately\)\s*$`)

// agyAttachedImagesBlockRE strips Persona's attached_images block when comparing a TUI preview to the full prompt text.
var agyAttachedImagesBlockRE = lazyre.New(`(?s)<attached_images>.*?</attached_images>`)

type agyQueuedItem struct {
	mu    sync.Mutex // guards watch (and serializes watch.took() / watch.lost outside agyQueues.mu)
	text  string
	watch *agySendWatch
	at    time.Time
}

type agyPaneQueue struct {
	session      string
	tracked      []*agyQueuedItem
	screenQueued []string
	screenAt     time.Time
	reading      bool
}

type agyQueues struct {
	mu     sync.Mutex
	byPane map[string]*agyPaneQueue
}

// parseAgyQueuedLines extracts queued message previews from an agy terminal screen in top-to-bottom order.
func parseAgyQueuedLines(screen string) []string {
	var out []string
	for line := range strings.SplitSeq(screen, "\n") {
		m := agyQueuedLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if !allStrokes(m[2]) {
			continue
		}
		preview := cleanAgyQueuedPreview(m[1])
		if preview != "" {
			out = append(out, preview)
		}
	}
	return out
}

func cleanAgyQueuedPreview(s string) string {
	s = strings.TrimSpace(s)
	for {
		trimmed := s
		for _, prefix := range []string{"❯", "›", ">", "•", "▸", "▹", "▶", "‣", "⎿", "│"} {
			if rest, ok := strings.CutPrefix(trimmed, prefix); ok {
				trimmed = strings.TrimSpace(rest)
			}
		}
		if trimmed == s {
			return s
		}
		s = trimmed
	}
}

func normalizeAgyQueueText(s string) string {
	s = agyAttachedImagesBlockRE.ReplaceAllString(s, " ")
	s = agyUserText(s)
	return strings.Join(strings.Fields(s), " ")
}

// agyPreviewMatchesText reports whether a TUI queue preview (possibly truncated with "..." or "…") represents fullText.
func agyPreviewMatchesText(preview, fullText string) bool {
	p := strings.TrimSpace(preview)
	p = strings.TrimSuffix(p, "...")
	p = strings.TrimSuffix(p, "…")
	p = strings.Join(strings.Fields(p), " ")
	if p == "" {
		return false
	}
	full := normalizeAgyQueueText(fullText)
	if full == "" {
		return false
	}
	lp, lf := strings.ToLower(p), strings.ToLower(full)
	return strings.HasPrefix(lf, lp) || strings.Contains(lf, lp)
}

func agyScreenShowsQueued(lines []string, text string, beforeCount int) bool {
	if len(lines) == 0 {
		return false
	}
	for _, line := range lines {
		if agyPreviewMatchesText(line, text) {
			return true
		}
	}
	return beforeCount >= 0 && len(lines) > beforeCount
}

// recordAgyQueued records a prompt accepted into agy's TUI queue while busy.
func (n *Node) recordAgyQueued(id, pane, text string, watch *agySendWatch, screenLines []string) {
	if pane == "" {
		return
	}
	q := &n.agyQueues
	q.mu.Lock()
	if q.byPane == nil {
		q.byPane = map[string]*agyPaneQueue{}
	}
	p := q.byPane[pane]
	if p == nil {
		p = &agyPaneQueue{session: id}
		q.byPane[pane] = p
	}
	p.session = id
	p.tracked = append(p.tracked, &agyQueuedItem{
		text:  text,
		watch: watch,
		at:    time.Now(),
	})
	if screenLines != nil {
		p.screenQueued = slices.Clone(screenLines)
		p.screenAt = time.Now()
	}
	q.mu.Unlock()
}

// agyQueuedFor returns the currently queued messages for a busy agy pane, pruning any tracked prompts already consumed
// in transcript.jsonl and polling the pane screen in the background without blocking sessionListIn.
func (n *Node) agyQueuedFor(id, pane string) []string {
	if pane == "" {
		return nil
	}
	q := &n.agyQueues
	q.mu.Lock()
	if q.byPane == nil {
		q.byPane = map[string]*agyPaneQueue{}
	}
	p := q.byPane[pane]
	if p == nil {
		p = &agyPaneQueue{session: id}
		q.byPane[pane] = p
	}
	p.session = id
	trackedSnap := slices.Clone(p.tracked)
	q.mu.Unlock()

	// Check transcript consumption outside q.mu because watch.took reads the transcript file.
	var consumed []*agyQueuedItem
	for _, item := range trackedSnap {
		item.mu.Lock()
		if item.watch == nil {
			if w := n.watchAgySend(id, pane, item.text, item.at); w != nil {
				w.off, w.info, w.rest = 0, nil, nil
				w.requireTime = true
				item.watch = w
			}
		}
		done := item.watch != nil && (item.watch.lost || item.watch.took())
		item.mu.Unlock()
		if done {
			consumed = append(consumed, item)
		}
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	p = q.byPane[pane]
	if p == nil {
		return nil
	}
	needPollNow := false
	if len(consumed) > 0 {
		consumedSet := make(map[*agyQueuedItem]bool, len(consumed))
		for _, item := range consumed {
			consumedSet[item] = true
		}
		kept := p.tracked[:0]
		var removed []*agyQueuedItem
		for _, item := range p.tracked {
			if consumedSet[item] {
				removed = append(removed, item)
			} else {
				kept = append(kept, item)
			}
		}
		clear(p.tracked[len(kept):])
		p.tracked = kept
		if len(removed) > 0 {
			// Drop matching stale screen previews immediately so a consumed prompt does not flash back before the next poll.
			if len(p.screenQueued) > 0 {
				for _, item := range removed {
					for i, preview := range p.screenQueued {
						if agyPreviewMatchesText(preview, item.text) {
							p.screenQueued = slices.Delete(p.screenQueued, i, i+1)
							break
						}
					}
				}
			}
			needPollNow = true
		}
	}

	if !p.reading && (needPollNow || p.screenAt.IsZero() || time.Since(p.screenAt) >= agyQueuePollEvery) {
		p.reading = true
		p.screenAt = time.Now()
		go n.pollAgyPaneQueue(pane, p)
	}
	return combineAgyQueued(p.tracked, p.screenQueued)
}

func combineAgyQueued(tracked []*agyQueuedItem, screenQueued []string) []string {
	if len(tracked) == 0 && len(screenQueued) == 0 {
		return nil
	}
	out := make([]string, 0, len(tracked)+len(screenQueued))
	usedScreen := make([]bool, len(screenQueued))
	for _, item := range tracked {
		out = append(out, item.text)
		for i, preview := range screenQueued {
			if !usedScreen[i] && agyPreviewMatchesText(preview, item.text) {
				usedScreen[i] = true
				break
			}
		}
	}
	for i, preview := range screenQueued {
		if !usedScreen[i] {
			out = append(out, preview)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (n *Node) pollAgyPaneQueue(pane string, p *agyPaneQueue) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	screen, err := n.readPane(ctx, pane, "visible", 80)
	cancel()

	var lines []string
	if err == nil {
		lines = parseAgyQueuedLines(screen)
	}

	q := &n.agyQueues
	q.mu.Lock()
	var changed bool
	if q.byPane[pane] == p {
		if err == nil {
			before := combineAgyQueued(p.tracked, p.screenQueued)
			p.screenQueued = lines
			after := combineAgyQueued(p.tracked, p.screenQueued)
			changed = !slices.Equal(before, after)
		}
		p.reading = false
	}
	q.mu.Unlock()

	if changed {
		n.notifySessionsSoon()
	}
}

func (n *Node) hasAgyQueued(id, pane string) bool {
	q := &n.agyQueues
	q.mu.Lock()
	defer q.mu.Unlock()
	if p := q.byPane[pane]; p != nil && (len(p.tracked) > 0 || len(p.screenQueued) > 0) {
		return true
	}
	for _, p := range q.byPane {
		if p.session == id && (len(p.tracked) > 0 || len(p.screenQueued) > 0) {
			return true
		}
	}
	return false
}

func (n *Node) clearAgyQueue(id, pane string) {
	q := &n.agyQueues
	q.mu.Lock()
	defer q.mu.Unlock()
	if pane != "" {
		delete(q.byPane, pane)
	}
	for k, p := range q.byPane {
		if p.session == id {
			delete(q.byPane, k)
		}
	}
}

func (n *Node) forgetAgyQueues(live map[string]bool) {
	q := &n.agyQueues
	q.mu.Lock()
	defer q.mu.Unlock()
	for pane, p := range q.byPane {
		if !live[pane] && !p.reading {
			delete(q.byPane, pane)
		}
	}
}
