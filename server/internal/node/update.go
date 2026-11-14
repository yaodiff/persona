package node

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/nwverse/persona/server/internal/nodeupdate"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// SetUpdater is used by the resident's Node factory, never by a stdio node.
func (n *Node) SetUpdater(updater *nodeupdate.Manager) { n.updater = updater }

func (n *Node) handleUpdate(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if ok, reason := n.updater.Capability(); !ok {
		return nil, fmt.Errorf("node update unavailable: %s", reason)
	}
	switch method {
	case "node.update.begin":
		var p proto.NodeUpdateBeginParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return n.updater.Begin(p)
	case "node.update.chunk":
		// Bound the base64 payload before decoding an untrusted allocation.
		if len(params) > 2*nodeupdate.MaxChunk+1024 {
			return nil, fmt.Errorf("node chunk exceeds limit")
		}
		var p proto.NodeUpdateChunkParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return n.updater.Chunk(p)
	case "node.update.commit":
		var p proto.NodeUpdateCommitParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return n.updater.Commit(ctx, p.ID, func() error {
			if updateWouldCut(AssistantsBusy(), n.sessionList(ctx, true), n.starts.listed(nil)) {
				return &rpc.Error{Code: proto.CodeNodeUpdateBusy, Message: "node_update_busy: Agent 仍在工作或等待操作，请稍后重试"}
			}
			return nil
		})
	case "node.update.abort":
		var p proto.NodeUpdateAbortParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, n.updater.Abort(p.ID)
	case "node.update.confirm":
		var p proto.NodeUpdateConfirmParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		return map[string]bool{"ok": true}, n.updater.Confirm(p.Version)
	default:
		return nil, fmt.Errorf("unknown node update method %q", method)
	}
}

// updateWouldCut: the restart after a commit would interrupt work (nodeproto.NodeUpdateIdle). It execs in place once
// ResidentReload.Prepare has stopped the idle headless conversations, so only a busy one (assistantsBusy) or a launch
// still starting counts. Terminal Agents, their background tasks and subagents are other processes and go on.
func updateWouldCut(assistantsBusy bool, sessions []proto.Session, starting ...[]proto.Session) bool {
	if assistantsBusy || slices.ContainsFunc(sessions, func(s proto.Session) bool { return s.Starting }) {
		return true
	}
	// A host-visible agent is filtered out of startLog.listed's rows, but its start is still active until readiness
	// and the first prompt finish. Check those records separately so an update cannot cut that launch off (#7362).
	for _, list := range starting {
		if slices.ContainsFunc(list, func(s proto.Session) bool { return s.Starting }) {
			return true
		}
	}
	return false
}

// RPC writes each response in one Write. Observe the successful transport write,
// rather than guessing a delay or restarting from inside a request handler.
type updateResponseWriter struct {
	io.Writer
	responseWritten func(string)
}

func (w *updateResponseWriter) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	if err != nil || !bytes.Contains(b, []byte(`"accepted":true`)) {
		return n, err
	}
	var response struct {
		ID     string                       `json:"id"`
		Result proto.NodeUpdateCommitResult `json:"result"`
		Error  json.RawMessage              `json:"error"`
	}
	if json.Unmarshal(b, &response) == nil && response.ID != "" && len(response.Error) == 0 && response.Result.Accepted {
		w.responseWritten(response.Result.ID)
	}
	return n, err
}
