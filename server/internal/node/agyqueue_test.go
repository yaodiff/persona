package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestParseAgyQueuedLines(t *testing.T) {
	screen := strings.Join([]string{
		"  Running unit tests...",
		"I need (something to send immediately) in prose",
		"Also run the linter... (alt+m to send immediately)",
		"❯ And check git diff (Alt+M to send immediately)",
		"• Fix foo(bar) (opt+m to send immediately)",
		"  ▸ Check the screenshot... (alt+m to send immediately)",
		"▹ Review the logs... (alt+m to send immediately)",
		"▶ Update the notes... (alt+m to send immediately)",
		"‣ Compare ▸ and ▹... (alt+m to send immediately)",
		"────────────────────────────────────────",
	}, "\n")
	want := []string{
		"Also run the linter...",
		"And check git diff",
		"Fix foo(bar)",
		"Check the screenshot...",
		"Review the logs...",
		"Update the notes...",
		"Compare ▸ and ▹...",
	}
	if got := parseAgyQueuedLines(screen); !slices.Equal(got, want) {
		t.Fatalf("parseAgyQueuedLines() = %q, want %q", got, want)
	}
	if got := parseAgyQueuedLines(idleScreen); len(got) != 0 {
		t.Fatalf("idle screen parsed queued lines %q, want none", got)
	}
}

func TestAgyQueueDeduplicatesMarkedPreview(t *testing.T) {
	const prompt = "Check the screenshot carefully"
	previews := parseAgyQueuedLines("▸ Check the screenshot... (alt+m to send immediately)")
	for _, tc := range []struct {
		name string
		text string
	}{
		{"without attachment", prompt},
		{"with attachment", "<attached_images>\n/tmp/a.png\n</attached_images>\n" + prompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tracked := []*agyQueuedItem{{text: tc.text}}
			if got := combineAgyQueued(tracked, previews); !slices.Equal(got, []string{tc.text}) {
				t.Fatalf("combineAgyQueued() = %q, want [%q]", got, tc.text)
			}
		})
	}
}

func TestAgyPreviewMatchesText(t *testing.T) {
	if !agyPreviewMatchesText("Also run the linter...", "Also run the linter and report back") {
		t.Fatal("expected ellipsis preview to match full prompt")
	}
	if !agyPreviewMatchesText("Check this screenshot…", "<attached_images>\n/tmp/a.png\n</attached_images>\nCheck this screenshot carefully") {
		t.Fatal("expected preview to match prompt with attached_images block")
	}
	if agyPreviewMatchesText("Unrelated follow-up...", "Also run the linter") {
		t.Fatal("unexpected match for unrelated text")
	}
}

func TestAgyBusyQueueLifecycleAndClear(t *testing.T) {
	defer func(d time.Duration) { agyQueuePollEvery = d }(agyQueuePollEvery)
	agyQueuePollEvery = time.Hour

	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	a := agyTestAgent(agyTestPane, home, agyTestID)
	a["agent_status"] = proto.StatusWorking
	seedAgents(t, n, f, a)
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, "")

	const prompt = "Also run the linter at the end of this turn"
	var mu sync.Mutex
	screen := "  Running tests...\n"
	f.handle("pane.read", func(map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"read": map[string]any{"text": screen}}
	})
	f.handle("agent.prompt", func(map[string]any) any {
		mu.Lock()
		screen = "  Running tests...\nAlso run the linter... (alt+m to send immediately)\n"
		mu.Unlock()
		return map[string]any{}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := requestCtx(t, ctx, n, "session.send", map[string]any{"session_id": agyTestRow, "text": prompt, "client_msg_id": "msg-queued-1"})
	if err != nil {
		t.Fatalf("busy agy send failed: %v", err)
	}
	if res["ok"] != true || res["queued"] != true {
		t.Fatalf("busy agy send response = %#v, want ok=true queued=true", res)
	}

	findRow := func() *proto.Session {
		for _, s := range listSessions(t, n) {
			if s.SessionID == agyTestRow {
				row := s
				return &row
			}
		}
		return nil
	}

	row := findRow()
	if row == nil {
		t.Fatal("missing agy row")
	}
	if row.Capabilities == nil || row.Capabilities.QueueSendNow == nil || !*row.Capabilities.QueueSendNow ||
		row.Capabilities.QueueEdit == nil || *row.Capabilities.QueueEdit {
		t.Fatalf("unexpected agy queue capabilities: %+v", row.Capabilities)
	}
	// Full prompt is surfaced and deduplicated against the truncated screen preview.
	if !slices.Equal(row.Queued, []string{prompt}) {
		t.Fatalf("Session.Queued = %q, want [%q]", row.Queued, prompt)
	}

	// Once agy consumes the message and writes USER_INPUT to transcript.jsonl, Session.Queued clears cleanly.
	line := fmt.Sprintf(`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":%q,"content":"<USER_REQUEST>\n%s\n</USER_REQUEST>"}`+"\n", time.Now().UTC().Format(time.RFC3339Nano), prompt)
	if err := os.WriteFile(file, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	screen = "  Running linter...\n"
	mu.Unlock()
	if row = findRow(); len(row.Queued) != 0 {
		t.Fatalf("Session.Queued after USER_INPUT consume = %q, want empty", row.Queued)
	}
	items, _ := agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 1 || items[0].ClientMsgID != "msg-queued-1" {
		t.Fatalf("consumed queued item = %+v, want client_msg_id=%q", items, "msg-queued-1")
	}
}

func TestAgyQueueClearsOnIdleAndSendNow(t *testing.T) {
	defer func(d, w time.Duration) { agyQueuePollEvery, sendNowWait = d, w }(agyQueuePollEvery, sendNowWait)
	agyQueuePollEvery = 20 * time.Millisecond
	sendNowWait = 600 * time.Millisecond

	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	a := agyTestAgent(agyTestPane, home, agyTestID)
	a["agent_status"] = proto.StatusWorking
	seedAgents(t, n, f, a)

	var mu sync.Mutex
	screen := "  Working...\nTyped in terminal... (alt+m to send immediately)\n"
	f.handle("pane.read", func(map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"read": map[string]any{"text": screen}}
	})
	var keys []string
	f.handle("pane.send_keys", func(p map[string]any) any {
		mu.Lock()
		if ks, ok := p["keys"].([]any); ok {
			for _, k := range ks {
				if s, ok := k.(string); ok {
					keys = append(keys, s)
				}
			}
		}
		screen = "  Working...\n"
		mu.Unlock()
		return map[string]any{}
	})

	// Background screen poll picks up a queued message typed directly in the terminal.
	waitFor(t, "terminal-typed agy queued message", func() bool {
		for _, s := range listSessions(t, n) {
			if s.SessionID == agyTestRow && slices.Equal(s.Queued, []string{"Typed in terminal..."}) {
				return true
			}
		}
		return false
	})

	// session.queue.send_now dispatches alt+m and clears Session.Queued.
	if _, err := request(t, n, "session.queue.send_now", map[string]any{"session_id": agyTestRow}); err != nil {
		t.Fatalf("session.queue.send_now on agy: %v", err)
	}
	mu.Lock()
	gotKeys := slices.Clone(keys)
	mu.Unlock()
	if !slices.Equal(gotKeys, []string{"alt+m"}) {
		t.Fatalf("send_now keys = %q, want [alt+m]", gotKeys)
	}
	for _, s := range listSessions(t, n) {
		if s.SessionID == agyTestRow && len(s.Queued) != 0 {
			t.Fatalf("Session.Queued after send_now = %q, want empty", s.Queued)
		}
	}

	// Returning to idle also clears any tracked queued entries cleanly.
	n.recordAgyQueued(agyTestRow, agyTestPane, "Follow up while busy", nil, []string{"Follow up..."})
	a["agent_status"] = proto.StatusIdle
	seedAgents(t, n, f, a)
	for _, s := range listSessions(t, n) {
		if s.SessionID == agyTestRow && len(s.Queued) != 0 {
			t.Fatalf("Session.Queued while idle = %q, want empty", s.Queued)
		}
	}
}

func appendAgyTranscriptStep(path, content string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	line := fmt.Sprintf(`{"step_index":1,"source":"ASSISTANT","type":"PLANNER_RESPONSE","status":"DONE","created_at":%q,"content":%q}`+"\n",
		time.Now().UTC().Format(time.RFC3339Nano), content)
	_, writeErr := f.WriteString(line)
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func listSessionsErr(n *Node) ([]proto.Session, error) {
	res, err := n.handle(context.Background(), "sessions.list", json.RawMessage(`{}`))
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Sessions []proto.Session `json:"sessions"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, err
	}
	return payload.Sessions, nil
}

// Concurrent sessions.list / agyQueuedFor calls on a busy agy pane must not race on shared
// agyQueuedItem.watch initialization or agySendWatch file-offset state (#16129).
func TestAgyQueuedForConcurrentWatchAccess(t *testing.T) {
	t.Run("concurrent_lazy_watch_init_and_took", func(t *testing.T) {
		defer func(d time.Duration) { agyQueuePollEvery = d }(agyQueuePollEvery)
		agyQueuePollEvery = time.Hour

		n, f, home := herdrNode(t)
		t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
		a := agyTestAgent(agyTestPane, home, agyTestID)
		a["agent_status"] = proto.StatusWorking
		seedAgents(t, n, f, a)
		file := agyTestFile(t, n.agyDataRoot(), agyTestID, "")

		const prompt = "Also run the linter at the end of this turn"
		var screenMu sync.Mutex
		screen := "  Running tests...\nAlso run the linter... (alt+m to send immediately)\n"
		f.handle("pane.read", func(map[string]any) any {
			screenMu.Lock()
			defer screenMu.Unlock()
			return map[string]any{"read": map[string]any{"text": screen}}
		})

		n.recordAgyQueued(agyTestRow, agyTestPane, prompt, nil, []string{"Also run the linter..."})

		start := make(chan struct{})
		errCh := make(chan error, 1)
		var wg sync.WaitGroup
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for range 12 {
					_ = n.agyQueuedFor(agyTestRow, agyTestPane)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range 12 {
				if err := appendAgyTranscriptStep(file, fmt.Sprintf("progress step %d", i)); err != nil {
					errCh <- err
					return
				}
			}
		}()
		close(start)
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}

		// Unconsumed follow-up must still be tracked (not falsely marked lost by concurrent offset races).
		if got := n.agyQueuedFor(agyTestRow, agyTestPane); !slices.Equal(got, []string{prompt}) {
			t.Fatalf("agyQueuedFor() after concurrent transcript growth = %q, want [%q]", got, prompt)
		}

		// Appending the matching USER_INPUT consumes and clears the queued prompt cleanly.
		userLine := fmt.Sprintf(`{"step_index":99,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":%q,"content":"<USER_REQUEST>\n%s\n</USER_REQUEST>"}`+"\n",
			time.Now().UTC().Format(time.RFC3339Nano), prompt)
		fp, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fp.WriteString(userLine); err != nil {
			_ = fp.Close()
			t.Fatal(err)
		}
		if err := fp.Close(); err != nil {
			t.Fatal(err)
		}
		screenMu.Lock()
		screen = "  Running linter...\n"
		screenMu.Unlock()
		if got := n.agyQueuedFor(agyTestRow, agyTestPane); len(got) != 0 {
			t.Fatalf("agyQueuedFor() after USER_INPUT consume = %q, want empty", got)
		}
		waitFor(t, "background agy pane poll to finish", func() bool {
			n.agyQueues.mu.Lock()
			defer n.agyQueues.mu.Unlock()
			p := n.agyQueues.byPane[agyTestPane]
			return p == nil || !p.reading
		})
	})

	t.Run("concurrent_list_sessions_with_watch_and_transcript_growth", func(t *testing.T) {
		defer func(d time.Duration) { agyQueuePollEvery = d }(agyQueuePollEvery)
		agyQueuePollEvery = time.Hour

		n, f, home := herdrNode(t)
		t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
		a := agyTestAgent(agyTestPane, home, agyTestID)
		a["agent_status"] = proto.StatusWorking
		seedAgents(t, n, f, a)
		file := agyTestFile(t, n.agyDataRoot(), agyTestID, "")

		const prompt = "Check the diff when this turn finishes"
		f.handle("pane.read", func(map[string]any) any {
			return map[string]any{"read": map[string]any{"text": "  Working...\nCheck the diff... (alt+m to send immediately)\n"}}
		})

		watch := n.watchAgySend(agyTestRow, agyTestPane, prompt, time.Now())
		if watch == nil {
			t.Fatal("watchAgySend returned nil")
		}
		n.recordAgyQueued(agyTestRow, agyTestPane, prompt, watch, []string{"Check the diff..."})

		start := make(chan struct{})
		var wg sync.WaitGroup
		errCh := make(chan error, 7)
		for range 6 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for range 8 {
					sessions, err := listSessionsErr(n)
					if err != nil {
						errCh <- err
						return
					}
					found := false
					for _, s := range sessions {
						if s.SessionID == agyTestRow {
							found = true
							if !slices.Equal(s.Queued, []string{prompt}) {
								errCh <- fmt.Errorf("Session.Queued = %q, want [%q]", s.Queued, prompt)
								return
							}
						}
					}
					if !found {
						errCh <- fmt.Errorf("missing session %q in sessions.list", agyTestRow)
						return
					}
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := range 12 {
				if err := appendAgyTranscriptStep(file, fmt.Sprintf("working step %d", i)); err != nil {
					errCh <- err
					return
				}
			}
		}()
		close(start)
		wg.Wait()
		close(errCh)
		for err := range errCh {
			t.Fatal(err)
		}
	})

	t.Run("concurrent_consume_prunes_screen_preview_once", func(t *testing.T) {
		defer func(d time.Duration) { agyQueuePollEvery = d }(agyQueuePollEvery)
		agyQueuePollEvery = time.Hour

		n := &Node{}
		const pane = "%agy-dup"
		item := &agyQueuedItem{
			text:  "run tests",
			watch: &agySendWatch{seen: true},
			at:    time.Now(),
		}
		p := &agyPaneQueue{
			session:      "initial",
			tracked:      []*agyQueuedItem{item},
			screenQueued: []string{"run tests...", "run tests..."},
			screenAt:     time.Now(),
			reading:      true, // keep background pollAgyPaneQueue off so screenQueued is deterministic
		}
		n.agyQueues.byPane = map[string]*agyPaneQueue{pane: p}

		item.mu.Lock()
		unlock := sync.OnceFunc(item.mu.Unlock)
		var wg sync.WaitGroup
		defer func() {
			unlock()
			wg.Wait()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = n.agyQueuedFor("snap-1", pane)
		}()
		waitFor(t, "first caller to snapshot tracked items", func() bool {
			n.agyQueues.mu.Lock()
			defer n.agyQueues.mu.Unlock()
			return p.session == "snap-1"
		})
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = n.agyQueuedFor("snap-2", pane)
		}()
		waitFor(t, "second caller to snapshot tracked items", func() bool {
			n.agyQueues.mu.Lock()
			defer n.agyQueues.mu.Unlock()
			return p.session == "snap-2"
		})
		unlock()
		wg.Wait()

		n.agyQueues.mu.Lock()
		defer n.agyQueues.mu.Unlock()
		if len(p.tracked) != 0 {
			t.Fatalf("p.tracked = %d, want 0", len(p.tracked))
		}
		if !slices.Equal(p.screenQueued, []string{"run tests..."}) {
			t.Fatalf("p.screenQueued = %q, want one remaining preview [\"run tests...\"]", p.screenQueued)
		}
	})

	t.Run("stale_consume_does_not_prune_recreated_queue", func(t *testing.T) {
		defer func(d time.Duration) { agyQueuePollEvery = d }(agyQueuePollEvery)
		agyQueuePollEvery = time.Hour

		n := &Node{}
		const pane = "%agy-recreate"
		oldItem := &agyQueuedItem{
			text:  "run tests",
			watch: &agySendWatch{seen: true},
			at:    time.Now(),
		}
		oldPane := &agyPaneQueue{
			session:      "initial",
			tracked:      []*agyQueuedItem{oldItem},
			screenQueued: []string{"run tests..."},
			screenAt:     time.Now(),
			reading:      true,
		}
		n.agyQueues.byPane = map[string]*agyPaneQueue{pane: oldPane}

		oldItem.mu.Lock()
		unlockOld := sync.OnceFunc(oldItem.mu.Unlock)
		done := make(chan struct{})
		defer func() {
			unlockOld()
			<-done
		}()
		go func() {
			defer close(done)
			_ = n.agyQueuedFor("snap-old", pane)
		}()
		waitFor(t, "stale caller to snapshot old tracked item", func() bool {
			n.agyQueues.mu.Lock()
			defer n.agyQueues.mu.Unlock()
			return oldPane.session == "snap-old"
		})

		n.clearAgyQueue("snap-old", pane)
		n.recordAgyQueued("new-session", pane, "run tests", &agySendWatch{}, []string{"run tests..."})
		n.agyQueues.mu.Lock()
		n.agyQueues.byPane[pane].reading = true
		n.agyQueues.mu.Unlock()

		unlockOld()
		<-done

		n.agyQueues.mu.Lock()
		defer n.agyQueues.mu.Unlock()
		cur := n.agyQueues.byPane[pane]
		if cur == nil || len(cur.tracked) != 1 || !slices.Equal(cur.screenQueued, []string{"run tests..."}) {
			t.Fatalf("recreated queue was unexpectedly modified by stale consumer: %+v", cur)
		}
	})
}
