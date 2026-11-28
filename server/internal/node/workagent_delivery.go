package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// herdr's agent.prompt acknowledges terminal input, not an agy turn. A follow-up is delivered only when agy appends
// that input to the bound conversation. Otherwise the caller gets delivered=unknown and no automatic retype (#14623).
var (
	agySendWait = 10 * time.Second
	agySendPoll = 100 * time.Millisecond
)

const agySendUnconfirmed = "消息已写进 agy 的终端，但没有在 agy 会话记录里看到它，不能确认已送达；先在电脑上看一眼再决定要不要重发。"

// agySendWatch reads complete transcript lines appended after a prompt was typed. A missing initial transcript may
// appear on the first turn, but then its input needs a timestamp at or after the send so an older identical request
// cannot acknowledge it. Replacing or shrinking the transcript makes delivery unknowable rather than confirmed.
type agySendWatch struct {
	path        string
	off         int64
	rest        []byte
	info        os.FileInfo
	want        []rune
	sent        string
	after       time.Time
	requireTime bool
	lost        bool
	seen        bool
}

func (n *Node) watchAgySend(id, pane, text string, typedAt time.Time) *agySendWatch {
	conversation := n.agyConversation(id, pane)
	if conversation == "" {
		return nil
	}
	w := &agySendWatch{
		path:  filepath.Join(n.agyRootFor(conversation), "brain", conversation, agyTranscriptPath),
		want:  echoWords(text),
		sent:  text,
		after: typedAt,
	}
	info, err := os.Stat(w.path)
	if errors.Is(err, os.ErrNotExist) {
		w.requireTime = true
		return w
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	w.info, w.off = info, info.Size()
	return w
}

// took reports whether a complete, newly appended explicit USER_INPUT matches the sent text. Once seen it stays seen.
func (w *agySendWatch) took() bool {
	if w == nil {
		return false
	}
	if w.seen {
		return true
	}
	f, err := os.Open(w.path)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if w.info != nil && !os.SameFile(info, w.info) || info.Size() < w.off {
		w.lost = true
		return false
	}
	w.info = info
	if info.Size() == w.off {
		return false
	}
	buf := make([]byte, info.Size()-w.off)
	got, err := f.ReadAt(buf, w.off)
	if err != nil && !errors.Is(err, io.EOF) {
		return false
	}
	w.off += int64(got)
	lines := bytes.Split(append(w.rest, buf[:got]...), []byte("\n"))
	w.rest = append([]byte(nil), lines[len(lines)-1]...)
	for _, line := range lines[:len(lines)-1] {
		if w.isPrompt(line) {
			w.seen = true
			return true
		}
	}
	return false
}

func (w *agySendWatch) isPrompt(line []byte) bool {
	var step agyStep
	if json.Unmarshal(line, &step) != nil || step.Type != "USER_INPUT" ||
		step.Source != "" && step.Source != "USER_EXPLICIT" {
		return false
	}
	if w.requireTime {
		at, err := time.Parse(time.RFC3339Nano, step.CreatedAt)
		if err != nil || at.Before(w.after.Add(-time.Second)) {
			return false
		}
	}
	if len(w.want) == 0 {
		// echoWords intentionally drops attachment envelopes. An image-only message has no words to compare, but a
		// newly appended explicit input with native media or attached image paths is still the matching user input.
		var media []json.RawMessage
		hasMedia := json.Unmarshal(step.Media, &media) == nil && len(media) != 0
		return strings.TrimSpace(w.sent) != "" && (hasMedia || len(agyAttachedImagePaths(step.Content)) != 0)
	}
	return agySendTextMatches(agyNativeInputText(step.Content, w.sent), w.want)
}

func agySendTextMatches(text string, want []rune) bool {
	if len(want) == 0 {
		return false
	}
	got := echoWords(text)
	k := min(len(want), promptWords)
	return len(got) >= k && string(got[:k]) == string(want[:k])
}

func agyNativeInputText(text, sent string) string {
	text = agyUserText(text)
	if strings.HasPrefix(strings.TrimLeftFunc(sent, unicode.IsSpace), "/") {
		return text
	}
	if i := strings.IndexFunc(text, unicode.IsSpace); i > 0 && strings.HasPrefix(text, "/") {
		return strings.TrimLeftFunc(text[i:], unicode.IsSpace)
	}
	return text
}

func (n *Node) confirmAgySend(ctx context.Context, id, pane, text string, typedAt time.Time, watch *agySendWatch, beforeQueued int) (bool, error) {
	ticker := time.NewTicker(agySendPoll)
	defer ticker.Stop()
	timer := time.NewTimer(agySendWait)
	defer timer.Stop()
	for {
		if watch == nil {
			// A follow-up can be the input that makes agy create and report its first conversation. Bind that report
			// late and scan the new file from its start; only timestamped input at or after this send can count.
			watch = n.watchAgySend(id, pane, text, typedAt)
			if watch != nil {
				watch.off, watch.info, watch.rest = 0, nil, nil
				watch.requireTime = true
			}
		}
		if watch.took() {
			return false, nil
		}
		if watch != nil && watch.lost {
			return false, uncertain(errors.New(agySendUnconfirmed))
		}
		// While agy is busy, it buffers follow-ups in TUI memory and shows `<preview>... (alt+m to send immediately)`
		// instead of appending USER_INPUT to transcript.jsonl immediately (#14871).
		if screen, err := n.readPane(ctx, pane, "visible", 80); err == nil {
			if lines := parseAgyQueuedLines(screen); agyScreenShowsQueued(lines, text, beforeQueued) {
				n.recordAgyQueued(id, pane, text, watch, lines)
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, uncertain(fmt.Errorf("%s: %w", agySendUnconfirmed, ctx.Err()))
		case <-timer.C:
			return false, uncertain(errors.New(agySendUnconfirmed))
		case <-ticker.C:
		}
	}
}
