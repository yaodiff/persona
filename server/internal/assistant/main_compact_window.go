package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/nwverse/persona/server/internal/i18n"
)

// assistant.main_compact_window (#7314): the main conversation is compacted at about this many tokens instead of its
// model's whole window. It runs all day, so every turn re-reads its context and a start after the prompt cache expired
// rewrites all of it; on a 1M model it was compacted only at 750K when idle, or near 1M by Claude Code. A direction's
// lead conversation (assistant.leaders, supervise_leader.go) is held to it too (#15318): it runs on the main chat's
// launcher, and the supervision messages of the tasks it hands out wake it so often that it was seldom idle long enough
// for the idle compaction, so leads grew to 750K-970K before anything compacted them. The manager sends the window with
// the starts and messages of those conversations (the main one's next to is_main); the node applies it to the
// conversation it came with, only on a managed launcher whose declared window is larger
// (node/assistant_compaction_policy.go capWindow). Every other conversation and every Agent keeps its launcher's
// window.
const (
	defaultMainCompactWindow = 400_000
	// minMainCompactWindow: the main conversation's fixed part (instructions, tools, the snapshot) is tens of thousands of
	// tokens and Claude Code compacts about 33K short of the window, so a smaller window would compact every few turns.
	minMainCompactWindow = 100_000
	// maxMainCompactWindow only catches a mistyped number: a window at or above the model's changes nothing.
	maxMainCompactWindow = 10_000_000
)

func validMainCompactWindow(w int) bool {
	return w == 0 || w >= minMainCompactWindow && w <= maxMainCompactWindow
}

// mainCompactWindowOf is the window in effect: the saved one, else defaultMainCompactWindow; 0 follows the launcher.
func mainCompactWindowOf(v Settings) int {
	if w := v.MainCompactWindow; w != nil && validMainCompactWindow(*w) {
		return *w
	}
	return defaultMainCompactWindow
}

// putMainCompactWindow puts the window in effect into p, a start or message for the main conversation (its is_main) or
// for a direction's lead conversation (lead: isLeader); another conversation's carries none.
func putMainCompactWindow(cfg Settings, p map[string]any, lead bool) {
	if main, _ := p["is_main"].(bool); main || lead {
		p["main_compact_window"] = mainCompactWindowOf(cfg)
	} else {
		delete(p, "main_compact_window")
	}
}

func (s *Service) setMainCompactWindow(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var n *int // null is refused, not read as 0: that would turn the cap off
	if err := json.Unmarshal(v, &n); err != nil || n == nil || !validMainCompactWindow(*n) {
		return nil, errors.New("value must be 0 (follow the launcher) or a whole number of tokens, 100000–10000000")
	}
	w := *n
	before := mainCompactWindowOf(s.settings(ctx))
	if w == before {
		return &ownChange{summary: "assistant.main_compact_window is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	label := func(l i18n.Lang, w int) string {
		if w == 0 {
			return i18n.T(l, "assistant-settings_tool.main_compact_window_launcher")
		}
		return i18n.T(l, "assistant-settings_tool.main_compact_window_tokens", i18n.Args{"tokens": w})
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_main_compact_window", i18n.Args{"before": label(l, before), "after": label(l, w)})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.MainCompactWindow = &w; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.main_compact_window is %d from the next message of the main conversation, and of each leader conversation, while it is idle.", w), nil
		}}, nil
}
