package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type decodedItemsFrame struct {
	Type      string       `json:"type"`
	MachineID string       `json:"machine_id"`
	SessionID string       `json:"session_id"`
	Items     []proto.Item `json:"items"`
	Backlog   bool         `json:"backlog"`
	Partial   bool         `json:"partial"`
	HasMore   *bool        `json:"has_more"`
	MoreAfter bool         `json:"more_after"`
	History   string       `json:"history"`
	FromSeq   *int         `json:"from_seq"`
	ToSeq     *int         `json:"to_seq"`
}

func decodeItemsFrame(t *testing.T, raw []byte) decodedItemsFrame {
	t.Helper()
	var f decodedItemsFrame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal items frame: %v", err)
	}
	return f
}

func TestMarshalItemsFramesSplitsOverBudget(t *testing.T) {
	items := make([]proto.Item, 20)
	for i := range items {
		items[i] = proto.Item{
			ID:   fmt.Sprintf("m#%d", i+1),
			Seq:  100 + i + 1,
			Role: "assistant",
			Kind: proto.KindText,
			Text: strings.Repeat("a", 500),
		}
	}
	hasMore := true
	toSeq := 125 // trailing non-item lines after seq 120
	const budget = 2048

	frames, err := marshalItemsFramesWithBudget(itemsFrameOpts{
		machineID: "m1",
		sessionID: "s1",
		items:     items,
		span:      itemSpan{History: "h1", ToSeq: &toSeq},
		backlog:   true,
		partial:   true,
		moreAfter: true,
		hasMore:   &hasMore,
	}, budget)
	if err != nil {
		t.Fatalf("marshalItemsFramesWithBudget: %v", err)
	}
	if len(frames) < 4 {
		t.Fatalf("expected >=4 split frames, got %d", len(frames))
	}

	var got []proto.Item
	var prevTo int
	for i, raw := range frames {
		if len(raw) > budget {
			t.Fatalf("frame %d size %d exceeds budget %d", i, len(raw), budget)
		}
		f := decodeItemsFrame(t, raw)
		if f.Type != "items" || f.MachineID != "m1" || f.SessionID != "s1" || !f.Backlog || !f.Partial || f.History != "h1" {
			t.Fatalf("frame %d metadata mismatch: %+v", i, f)
		}
		if i == 0 {
			if f.HasMore == nil || !*f.HasMore {
				t.Fatalf("first frame must carry has_more=true, got %v", f.HasMore)
			}
			if f.FromSeq != nil {
				t.Fatalf("first frame of initial backlog must omit from_seq, got %v", *f.FromSeq)
			}
		} else {
			if f.HasMore != nil {
				t.Fatalf("continuation frame %d must omit has_more, got %v", i, *f.HasMore)
			}
			if f.FromSeq == nil || *f.FromSeq != prevTo {
				t.Fatalf("continuation frame %d from_seq=%v, want %d", i, f.FromSeq, prevTo)
			}
		}
		if i == len(frames)-1 {
			if !f.MoreAfter {
				t.Fatalf("last frame must carry more_after=true")
			}
			if f.ToSeq == nil || *f.ToSeq != toSeq {
				t.Fatalf("last frame to_seq=%v, want %d", f.ToSeq, toSeq)
			}
		} else {
			if f.MoreAfter {
				t.Fatalf("non-last frame %d must not set more_after", i)
			}
			if f.ToSeq == nil {
				t.Fatalf("frame %d missing to_seq", i)
			}
			prevTo = *f.ToSeq
		}
		got = append(got, f.Items...)
	}
	if len(got) != len(items) {
		t.Fatalf("got %d items across frames, want %d", len(got), len(items))
	}
	for i := range items {
		if got[i].ID != items[i].ID || got[i].Seq != items[i].Seq {
			t.Fatalf("item %d mismatch: got (%s,%d) want (%s,%d)", i, got[i].ID, got[i].Seq, items[i].ID, items[i].Seq)
		}
	}
}

func TestMarshalItemsFramesSingleOversizedItem(t *testing.T) {
	items := []proto.Item{
		{ID: "i1", Seq: 1, Role: "assistant", Kind: proto.KindText, Text: strings.Repeat("x", 200)},
		{ID: "i2", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: strings.Repeat("y", 4096)},
		{ID: "i3", Seq: 3, Role: "assistant", Kind: proto.KindText, Text: strings.Repeat("z", 200)},
	}
	from, to := 0, 3
	const budget = 1024

	frames, err := marshalItemsFramesWithBudget(itemsFrameOpts{
		machineID: "m1",
		sessionID: "s1",
		items:     items,
		span:      itemSpan{History: "h1", FromSeq: &from, ToSeq: &to},
	}, budget)
	if err != nil {
		t.Fatalf("marshalItemsFramesWithBudget: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames (normal, oversized single, normal), got %d", len(frames))
	}
	if len(frames[0]) > budget || len(frames[2]) > budget {
		t.Fatalf("normal frames exceeded budget: f0=%d f2=%d budget=%d", len(frames[0]), len(frames[2]), budget)
	}
	f0, f1, f2 := decodeItemsFrame(t, frames[0]), decodeItemsFrame(t, frames[1]), decodeItemsFrame(t, frames[2])
	if len(f0.Items) != 1 || f0.Items[0].ID != "i1" || len(f1.Items) != 1 || f1.Items[0].ID != "i2" || len(f2.Items) != 1 || f2.Items[0].ID != "i3" {
		t.Fatalf("unexpected split items: %v / %v / %v", f0.Items, f1.Items, f2.Items)
	}
	if f0.FromSeq == nil || *f0.FromSeq != 0 || f0.ToSeq == nil || *f0.ToSeq != 1 ||
		f1.FromSeq == nil || *f1.FromSeq != 1 || f1.ToSeq == nil || *f1.ToSeq != 2 ||
		f2.FromSeq == nil || *f2.FromSeq != 2 || f2.ToSeq == nil || *f2.ToSeq != 3 {
		t.Fatalf("non-contiguous spans: f0=(%v,%v) f1=(%v,%v) f2=(%v,%v)", f0.FromSeq, f0.ToSeq, f1.FromSeq, f1.ToSeq, f2.FromSeq, f2.ToSeq)
	}
}

func TestMarshalItemsFramesEmptyAndSmallFastPath(t *testing.T) {
	from, to := 8, 10
	frames, err := marshalItemsFrames(itemsFrameOpts{
		machineID: "m1",
		sessionID: "s1",
		items:     nil,
		span:      itemSpan{History: "h1", FromSeq: &from, ToSeq: &to},
	})
	if err != nil {
		t.Fatalf("marshalItemsFrames: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected 1 frame for empty items, got %d", len(frames))
	}
	f := decodeItemsFrame(t, frames[0])
	if len(f.Items) != 0 || f.FromSeq == nil || *f.FromSeq != 8 || f.ToSeq == nil || *f.ToSeq != 10 {
		t.Fatalf("unexpected empty advance frame: %+v", f)
	}
}

func TestSendBacklogAndItemsToSubscribersSplitLargeFrames(t *testing.T) {
	// 80 items * ~16 KiB each ~= 1.28 MiB > 512 KiB budget -> must split into >=3 frames.
	largeItems := make([]proto.Item, 80)
	payload := strings.Repeat("w", 16<<10)
	for i := range largeItems {
		largeItems[i] = proto.Item{
			ID:   fmt.Sprintf("big#%d", i+1),
			Seq:  i + 1,
			Role: "assistant",
			Kind: proto.KindText,
			Text: payload,
		}
	}

	// 1. itemsToSubscribers
	cSub := &client{out: make(chan []byte, 32), subs: map[[2]string]bool{{"m1", "s1"}: true}}
	h := &Hub{clients: map[*client]struct{}{cSub: {}}}
	from, to := 0, len(largeItems)
	h.itemsToSubscribers("m1", "s1", largeItems, itemSpan{History: "h1", FromSeq: &from, ToSeq: &to}, true)
	if len(cSub.out) < 3 {
		t.Fatalf("itemsToSubscribers queued %d frames, want >=3", len(cSub.out))
	}
	seen := 0
	var lastFrame decodedItemsFrame
	for len(cSub.out) > 0 {
		raw := <-cSub.out
		if len(raw) > wsItemsFrameBudgetBytes {
			t.Fatalf("itemsToSubscribers frame size %d exceeds budget %d", len(raw), wsItemsFrameBudgetBytes)
		}
		lastFrame = decodeItemsFrame(t, raw)
		if len(cSub.out) > 0 && lastFrame.MoreAfter {
			t.Fatal("intermediate itemsToSubscribers frame set more_after")
		}
		seen += len(lastFrame.Items)
	}
	if seen != len(largeItems) || !lastFrame.MoreAfter {
		t.Fatalf("itemsToSubscribers delivered %d items (more_after=%v), want %d (true)", seen, lastFrame.MoreAfter, len(largeItems))
	}

	// 2. sendBacklog (since == 0)
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide := rpc.New("h", h2nW)
	nodeSide := rpc.New("n", n2hW)
	nodeSide.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		if method != "session.items" {
			return nil, fmt.Errorf("unexpected method %s", method)
		}
		return map[string]any{
			"items":    largeItems,
			"has_more": true,
			"history":  "h1",
			"to_seq":   len(largeItems),
		}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })

	m := newMachine(&Hub{}, MachineConfig{ID: "m1", Name: "m1"})
	m.peer = hubSide
	cBacklog := &client{out: make(chan []byte, 32), subs: map[[2]string]bool{}}
	(&Hub{}).sendBacklog(context.Background(), cBacklog, m, "s1", 0)
	if len(cBacklog.out) < 3 {
		t.Fatalf("sendBacklog queued %d frames, want >=3", len(cBacklog.out))
	}
	seen = 0
	idx := 0
	for len(cBacklog.out) > 0 {
		raw := <-cBacklog.out
		if len(raw) > wsItemsFrameBudgetBytes {
			t.Fatalf("sendBacklog frame %d size %d exceeds budget %d", idx, len(raw), wsItemsFrameBudgetBytes)
		}
		f := decodeItemsFrame(t, raw)
		if idx == 0 && (f.HasMore == nil || !*f.HasMore) {
			t.Fatal("first sendBacklog frame missing has_more=true")
		}
		if idx > 0 && f.HasMore != nil {
			t.Fatalf("continuation sendBacklog frame %d unexpectedly set has_more", idx)
		}
		seen += len(f.Items)
		idx++
	}
	if seen != len(largeItems) {
		t.Fatalf("sendBacklog delivered %d items, want %d", seen, len(largeItems))
	}
}

type stubWSWriter struct {
	mu      sync.Mutex
	written [][]byte
	onWrite func()
}

func (s *stubWSWriter) Write(_ context.Context, _ websocket.MessageType, b []byte) error {
	s.mu.Lock()
	s.written = append(s.written, append([]byte(nil), b...))
	fn := s.onWrite
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
	return nil
}

func (s *stubWSWriter) Ping(_ context.Context) error { return nil }

func TestWriteWSWarnsOversizedFrameWithoutLeakingBody(t *testing.T) {
	var buf bytes.Buffer
	var bufMu sync.Mutex
	logger := log.New(writerFunc(func(p []byte) (int, error) {
		bufMu.Lock()
		defer bufMu.Unlock()
		return buf.Write(p)
	}), "", 0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := &Hub{logger: logger}
	c := &client{
		hub:  h,
		out:  make(chan []byte, 4),
		meta: clientMeta{updates: make(chan heartbeatConfig), done: ctx.Done()},
	}

	const secretBody = "SENSITIVE_PRIVATE_TRANSCRIPT_PAYLOAD_DO_NOT_LOG"
	smallFrame, _ := json.Marshal(map[string]any{
		"type":       "items",
		"machine_id": "m-small",
		"session_id": "s-small",
		"items":      []map[string]any{{"text": secretBody}},
	})
	largeFrame, _ := json.Marshal(map[string]any{
		"type":       "items",
		"machine_id": "m-big",
		"session_id": "s-big",
		"items":      []map[string]any{{"text": secretBody + strings.Repeat("x", wsLargeFrameWarnBytes)}},
	})

	c.out <- smallFrame
	c.out <- largeFrame

	writes := 0
	doneWriting := make(chan struct{})
	w := &stubWSWriter{onWrite: func() {
		writes++
		if writes == 2 {
			close(doneWriting)
		}
	}}

	errCh := make(chan error, 1)
	go func() {
		errCh <- c.writeWS(ctx, w, func(heartbeatConfig) {})
	}()

	<-doneWriting
	cancel()
	<-errCh

	bufMu.Lock()
	logged := buf.String()
	bufMu.Unlock()

	if strings.Contains(logged, "m-small") {
		t.Fatalf("small frame should not trigger oversized warning, got logs: %s", logged)
	}
	if !strings.Contains(logged, `warn: oversized websocket frame type="items" machine_id="m-big" session_id="s-big" bytes=`) {
		t.Fatalf("expected structured oversized frame warning, got logs: %q", logged)
	}
	if strings.Contains(logged, secretBody) {
		t.Fatalf("oversized frame warning leaked message body: %q", logged)
	}
}
