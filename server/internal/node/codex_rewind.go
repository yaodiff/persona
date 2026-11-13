package node

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Native revert changes the durable paginated history and starts another rollout segment. Use that history for
// both validation and rendering, including the retained prefix and an empty conversation after the first turn.
func (n *Node) canRewindCodex(id string) bool {
	if _, _, ok := n.codexEffortThread(id); !ok || n.codexAppHistoryDir() == "" {
		return false
	}
	src, ok := n.codexSource(id)
	if !ok {
		return false
	}
	_, ok = codexPaginatedThreadOf(src)
	return ok
}

func (n *Node) codexRewindRow(id string, s *proto.Session) {
	if s.Capabilities == nil {
		// Native panes previously allowed these operations through the clients' legacy capability fallback.
		s.Capabilities = &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true, Approve: true, Terminal: true}
	}
	s.Capabilities.Rewind = proto.CanModes(!s.IsSubagent && n.canRewindCodex(id))
	s.Capabilities.RewindCode = proto.CanModes(false)
}

type codexRewindTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Items  []struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"items"`
}

func codexMethodMissing(err error) bool {
	var native *codexError
	return errors.As(err, &native) && native.Code == -32601
}

// Read oldest first. A target's occurrence and prompts_after count user inputs, while legacy rollback counts turns.
func readCodexRewindTurns(ctx context.Context, c *codexConn, id string) ([]codexRewindTurn, error) {
	var turns []codexRewindTurn
	cursor := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Data       []codexRewindTurn `json:"data"`
			NextCursor string            `json:"nextCursor"`
		}
		params := map[string]any{"threadId": id, "sortDirection": "asc", "itemsView": "full", "limit": 100}
		if cursor != "" {
			params["cursor"] = cursor
		}
		err := c.call(ctx, "thread/turns/list", params, &page)
		if codexMethodMissing(err) && cursor == "" {
			var old struct {
				Thread struct {
					Turns []codexRewindTurn `json:"turns"`
				} `json:"thread"`
			}
			err = c.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true}, &old)
			return old.Thread.Turns, err
		}
		if err != nil {
			return nil, err
		}
		turns = append(turns, page.Data...)
		if page.NextCursor == "" {
			return turns, nil
		}
		if seen[page.NextCursor] {
			return nil, rewindError(rewindFailed, "Codex repeated a history page. Reload the conversation before rewinding.")
		}
		seen[page.NextCursor], cursor = true, page.NextCursor
	}
}

func (n *Node) rewindCodex(ctx context.Context, id string, o rewindOpts) (map[string]any, error) {
	if o.Restore != RestoreConversation || !n.canRewindCodex(id) {
		return nil, rewindError(rewindUnsupported, "This Codex session needs a shared app-server and persistent paginated history to rewind.")
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, rewindError(rewindUnsupported, "The node needs sqlite3 to synchronize Codex history after rewinding.")
	}
	ctx, cancel := context.WithTimeout(ctx, rewindTotalLimit)
	defer cancel()
	c, t, err := n.codexOwner(id)
	if err != nil {
		return nil, err
	}
	if t.turn != "" || t.status == proto.StatusWorking || t.status == proto.StatusBlocked {
		return nil, rewindError(rewindBusy, "Wait for Codex to finish its current turn before rewinding.")
	}
	if err := c.join(ctx, id, true); err != nil {
		return nil, err
	}
	var state struct {
		Thread remoteThreadData `json:"thread"`
	}
	if err := c.wire.call(ctx, "thread/read", map[string]any{"threadId": t.id, "includeTurns": false}, &state); err != nil {
		return nil, err
	}
	if state.Thread.ID != t.id {
		return nil, rewindError(rewindMismatch, "The native Codex conversation changed.")
	}
	if state.Thread.Status.Type == "active" {
		return nil, rewindError(rewindBusy, "Wait for Codex to become idle before rewinding.")
	}
	turns, err := readCodexRewindTurns(ctx, c.wire, t.id)
	if err != nil {
		return nil, err
	}
	target, prompt, err := codexRewindTarget(turns, o)
	if err != nil {
		return nil, err
	}
	// Fail before the native mutation if this node cannot materialize the canonical conversation.
	if err := n.refreshCodexAppHistory(id); err != nil {
		return nil, rewindError(rewindFailed, "Could not read the current Codex history: %v", err)
	}
	f, err := n.file(id)
	if err != nil {
		return nil, rewindError(rewindFailed, "Could not read the current Codex history: %v", err)
	}
	var reverted struct {
		Thread remoteThreadData `json:"thread"`
	}
	err = c.wire.call(ctx, "thread/revert", map[string]any{"threadId": t.id, "beforeTurnId": turns[target].ID}, &reverted)
	if codexMethodMissing(err) {
		err = c.wire.call(ctx, "thread/rollback", map[string]any{"threadId": t.id, "numTurns": len(turns) - target}, &reverted)
	}
	if err != nil {
		return nil, err
	}
	if p := reverted.Thread.Path; p != "" {
		if reverted.Thread.ID != t.id || rolloutID(p) != t.id {
			return nil, rewindError(rewindFailed, "Codex rewound the conversation and returned an unexpected history segment.")
		}
		deadline := time.Now().Add(3 * time.Second)
		for {
			if _, whole := readRolloutMeta(p); whole {
				break
			}
			if time.Now().After(deadline) {
				return nil, rewindError(rewindFailed, "Codex rewound the conversation. Its new history segment is still synchronizing.")
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
		c.mu.Lock()
		current := c.threads[t.id]
		current.path = p
		current.revision++
		c.threads[t.id] = current
		c.mu.Unlock()
	}
	// Read back the native result before publishing success. A concurrent native input must never be hidden locally.
	invalidateCodexPaginated(c.spec.Home)
	remaining, err := readCodexRewindTurns(ctx, c.wire, t.id)
	if err != nil {
		return nil, rewindError(rewindFailed, "Codex rewound the conversation. Could not verify its current history: %v", err)
	}
	if len(remaining) != target {
		return nil, rewindError(rewindFailed, "Codex history changed during rewind. Reload the conversation.")
	}
	for i := range remaining {
		if remaining[i].ID != turns[i].ID {
			return nil, rewindError(rewindFailed, "Codex history changed during rewind. Reload the conversation.")
		}
	}
	if err := n.refreshCodexAppHistory(id); err != nil {
		return nil, rewindError(rewindFailed, "Codex rewound the conversation. Its display is still synchronizing: %v", err)
	}
	f, err = n.file(id)
	if err != nil {
		return nil, rewindError(rewindFailed, "Codex rewound the conversation. Its display is still synchronizing: %v", err)
	}
	if err := n.refresh(id, f); err != nil {
		return nil, rewindError(rewindFailed, "Codex rewound the conversation. Its display is still synchronizing: %v", err)
	}
	c.mu.Lock()
	current := c.threads[t.id]
	current.partial, current.failure, current.failureKind = "", "", ""
	current.buffer = nil
	current.revision++
	c.threads[t.id] = current
	c.mu.Unlock()
	n.emitDelta(id, "")
	n.notifyCodex()
	return map[string]any{"prompt": prompt, "restore": RestoreConversation, "code_restored": false, "input_cleared": true}, nil
}

func codexRewindTarget(turns []codexRewindTurn, o rewindOpts) (int, string, error) {
	type input struct {
		turn, inTurn int
		text         string
	}
	var inputs []input
	for ti, turn := range turns {
		if turn.Status == "inProgress" {
			return 0, "", rewindError(rewindBusy, "Codex has an unfinished turn.")
		}
		n := 0
		for _, item := range turn.Items {
			if item.Type != "userMessage" {
				continue
			}
			var parts []string
			for _, part := range item.Content {
				if part.Type == "text" {
					parts = append(parts, part.Text)
				}
			}
			inputs = append(inputs, input{ti, n, strings.Join(parts, "\n")})
			n++
		}
	}
	squash := func(s string) string { return strings.Join(strings.Fields(s), "") }
	occurrence := 0
	for i := len(inputs) - 1; i >= 0; i-- {
		p := inputs[i]
		if squash(p.text) != squash(o.Prompt) {
			continue
		}
		occurrence++
		if occurrence != o.Occurrence {
			continue
		}
		if o.PromptsAfter != nil && len(inputs)-1-i != *o.PromptsAfter {
			break
		}
		if p.inTurn != 0 {
			return 0, "", rewindError(rewindUnsupported, "Codex can rewind to the first message of a turn. Select that message to rewind this turn.")
		}
		return p.turn, p.text, nil
	}
	return 0, "", rewindError(rewindMismatch, "The selected message moved. Reload the conversation before rewinding.")
}
