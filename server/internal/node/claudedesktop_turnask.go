package node

import (
	"encoding/json"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The Claude desktop app's end-of-turn question (#16406). A session of the app can end its turn by asking the owner
// one question through the app's own tool (endOfTurnTool, its input `ask` and `blocked`): the app shows it under the
// turn and waits. Claude Code's status file says nothing of it (the turn is over, so the session is idle), and the
// app's index only notes that such a box exists. The transcript has it: the turn's latest tool call is that tool,
// with `blocked: true` when the next step is the owner's. While no prompt of the owner's follows, the session waits
// on them: it is listed as blocked, the question its attention reason, so it reaches Needs you like a dialog does.
const endOfTurnTool = "mcp__ccd_turn__wrap_up"

type endOfTurnAskEntry struct {
	size  int64
	mtime time.Time
	file  *transcript.File
	ask   string
}

var endOfTurnAsks = struct {
	sync.Mutex
	byPath map[string]endOfTurnAskEntry
}{byPath: map[string]endOfTurnAskEntry{}}

// endOfTurnAsk is the question the latest turn of the transcript at p ended on and waits for the owner to answer, ""
// when it ended on none, on one not marked blocked, or the owner has written since.
func endOfTurnAsk(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return ""
	}
	endOfTurnAsks.Lock()
	e, ok := endOfTurnAsks.byPath[p]
	endOfTurnAsks.Unlock()
	if ok && e.size == st.Size() && e.mtime.Equal(st.ModTime()) {
		return e.ask
	}
	f := e.file
	if f == nil {
		f = transcript.NewFile(p)
	}
	ask, err := readEndOfTurnAsk(f)
	if err != nil {
		return "" // a read that failed now is not cached as "none"
	}
	endOfTurnAsks.Lock()
	if len(endOfTurnAsks.byPath) >= 64 {
		clear(endOfTurnAsks.byPath)
	}
	endOfTurnAsks.byPath[p] = endOfTurnAskEntry{size: st.Size(), mtime: st.ModTime(), file: f, ask: ask}
	endOfTurnAsks.Unlock()
	return ask
}

// readEndOfTurnAsk walks back from the end of f to the current turn's latest tool call or prompt, as
// readPendingOutsideAskFile does, and returns that call's question when it is the end-of-turn tool marked blocked.
func readEndOfTurnAsk(f *transcript.File) (string, error) {
	if _, _, err := f.RefreshExportSpan(); err != nil {
		return "", err
	}
	beforeSeq := 0
	for {
		items, hasMore, _ := f.PageTurns(beforeSeq, 2, 2000)
		var last *proto.Item
		sawBoundary := false
		for i := range items {
			it := items[i]
			switch {
			case isOutsideTurnPrompt(it):
				last = nil
				sawBoundary = true
			case it.Role == "assistant" && it.Kind == proto.KindToolUse:
				cp := it
				last = &cp
				sawBoundary = true
			}
		}
		if sawBoundary || !hasMore || len(items) == 0 {
			if last == nil || last.ToolName != endOfTurnTool {
				return "", nil
			}
			var in struct {
				Ask     string `json:"ask"`
				Blocked bool   `json:"blocked"`
			}
			if json.Unmarshal(last.ToolInput, &in) != nil || !in.Blocked {
				return "", nil
			}
			return strings.TrimSpace(in.Ask), nil
		}
		nextBefore := items[0].Seq
		if nextBefore <= 1 || (beforeSeq > 0 && nextBefore >= beforeSeq) {
			return "", nil
		}
		beforeSeq = nextBefore
	}
}
