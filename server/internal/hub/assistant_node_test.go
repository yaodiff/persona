package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// assistantNode is the assistant's machine as runTask sees it: assistant.send with no session starts one (s1, s2…),
// with a session continues it, and with resume_only refuses one it doesn't have (cleaned up, never written).
type assistantNode struct {
	m     *Machine
	mu    sync.Mutex
	known map[string]bool
	// Exact synthetic launcher/model pairs accepted by launchers.model.check; other pairs remain unsupported.
	modelChecks   map[string]string
	made          int // conversations started, for the next id
	sends         []map[string]any
	kept          []string                // the last node.configure keep_sessions
	items         []proto.Item            // session.items answers this (assistant_titles_test.go)
	sentItems     map[string][]proto.Item // user transcript items written by successful supervision assistant.send calls, by session_id
	omitSentItems bool                    // suppress sentItems writes to simulate RPC success without a transcript item
	itemReads     int                     // session.items calls handled
	// assistant.handoff answers handoff; assistant.compact notes the session and answers compactErr
	// (assistant_main_test.go).
	handoff    map[string]any
	compacts   []string
	compactErr error
	sendErr    func(map[string]any) error
}

func newAssistantNode(t *testing.T, h *Hub) *assistantNode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide := rpc.New("h", h2nW)
	nodeSide := rpc.New("n", n2hW)
	n := &assistantNode{known: map[string]bool{}}
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		n.mu.Lock()
		defer n.mu.Unlock()
		switch method {
		case "launchers.model.check":
			if agent := n.modelChecks[fmt.Sprint(p["name"])+"\x00"+fmt.Sprint(p["model"])]; agent != "" {
				return map[string]any{"agent": agent}, nil
			}
		case "assistant.prepare":
			return map[string]any{"cwd": "/home/me/.persona/assistant"}, nil
		case "node.configure":
			n.kept = nil
			keep, _ := p["keep_sessions"].([]any)
			for _, k := range keep {
				n.kept = append(n.kept, k.(string))
			}
			return map[string]any{}, nil
		case "session.items":
			n.itemReads++
			items := n.items
			if sid, _ := p["session_id"].(string); sid != "" && len(n.sentItems[sid]) > 0 {
				items = append(slices.Clone(items), n.sentItems[sid]...)
			}
			return map[string]any{"items": items}, nil
		case "session.queue.edit":
			n.sends = append(n.sends, map[string]any{method: p})
			return proto.QueueEdit{Queue: []string{"Fixture message"}}, nil
		case "session.queue.send_now":
			n.sends = append(n.sends, map[string]any{method: p})
			return map[string]any{"sent": true}, nil
		case "session.rename":
			n.sends = append(n.sends, map[string]any{"rename": p})
			return map[string]any{"ok": true}, nil
		case "assistant.handoff":
			if n.handoff == nil {
				return nil, fmt.Errorf("conversation %v is gone", p["session_id"])
			}
			return n.handoff, nil
		case "assistant.compact":
			n.compacts = append(n.compacts, fmt.Sprint(p["session_id"]))
			if n.compactErr != nil {
				return nil, n.compactErr
			}
			return map[string]any{"ok": true}, nil
		case "session.send":
			n.sends = append(n.sends, p)
			if n.sendErr != nil {
				if err := n.sendErr(p); err != nil {
					return nil, err
				}
			}
			return map[string]any{}, nil
		case "session.interrupt":
			n.sends = append(n.sends, map[string]any{"interrupt": p["session_id"]})
			return map[string]any{"ok": true}, nil
		case "session.close":
			n.sends = append(n.sends, map[string]any{"close": p["session_id"]})
			return map[string]any{"ok": true}, nil
		case assistantwire.DeviceAssistantAnswerAgain:
			n.sends = append(n.sends, map[string]any{"answer_again": p})
			return map[string]any{"ok": true}, nil
		case "assistant.send":
			n.sends = append(n.sends, p)
			id, _ := p["session_id"].(string)
			if id == "" {
				n.made++
				id = fmt.Sprintf("s%d", n.made)
				n.known[id] = true
			} else if p["resume_only"] == true && !n.known[id] {
				return nil, fmt.Errorf("conversation %s is gone", id)
			}
			source, _ := p["message_source"].(map[string]any)
			if !n.omitSentItems && source["kind"] == proto.SourceSupervision {
				if text, _ := p["text"].(string); text != "" {
					if n.sentItems == nil {
						n.sentItems = map[string][]proto.Item{}
					}
					item := proto.Item{Seq: len(n.sentItems[id]) + 1, Role: "user", Kind: proto.KindText, Text: text, TS: time.Now()}
					if cid, _ := p["client_msg_id"].(string); cid != "" {
						item.ClientMsgID = cid
					}
					n.sentItems[id] = append(n.sentItems[id], item)
				}
			}
			return map[string]any{"session_id": id}, nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	n.m = newMachine(h, MachineConfig{ID: "server", Name: "server", Kind: "local"})
	n.m.peer = hubSide
	h.machines = append(h.machines, n.m)
	h.byID["server"] = n.m
	return n
}

func (n *assistantNode) lastSend(t *testing.T) map[string]any {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.sends) == 0 {
		t.Fatal("nothing was sent")
	}
	return n.sends[len(n.sends)-1]
}

func (n *assistantNode) sent() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sends)
}
