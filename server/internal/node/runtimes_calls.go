package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/transcript"
)

// claimMethods are the calls that ask the connected runtimes about a session nobody claimed yet: what the apps do
// with a session they show (open it, page it, send to it, stop it).
var claimMethods = map[string]bool{"session.send": true, "session.items": true, "session.item": true, "session.image": true,
	proto.MethodSessionInputPreviews: true, proto.MethodSessionItemsWindow: true,
	"session.visualization": true, "session.fork": true,
	"session.watch": true, "session.info": true, "session.interrupt": true}

// awayReads are the calls the node answers itself for a session of a runtime that is away whose conversation is a
// Claude transcript here (runtimeaway.go): its content, its info and the watch of its transcript.
var awayReads = map[string]bool{"session.items": true, "session.item": true, "session.image": true, "session.visualization": true,
	proto.MethodSessionInputPreviews: true, proto.MethodSessionItemsWindow: true,
	"session.watch": true,
	"session.info":  true}

// runtimeClaimTimeout bounds asking one runtime whether it has a session.
var runtimeClaimTimeout = 5 * time.Second

// runtimeFor is the runtime a hub call about session id goes to: the one listing it, else the one that listed it
// before (an older session it no longer lists), else — for a session this machine has nothing else for (no agent, no
// transcript) — the first connected runtime that answers for it. A session of a runtime that is not connected now is
// refused (errRuntimeAway): it is not a Claude session to resume in a terminal. Only its content is read meanwhile
// when its conversation is a Claude transcript here (runtimeaway.go).
//
// A session none of them has, whose id is one a runtime gives its own sessions ("<runtime id>-…": Paseo's
// paseo-<agent id>), is refused with that runtime's not-found when it says it does not have it (an agent of another
// provider archived or deleted in Paseo, opened from an old link): the node has nothing of it either, and answered with
// its own transcript lookup's failure, a 502 the apps showed as the computer failing (#8131).
func (n *Node) runtimeFor(ctx context.Context, method, id string) (*runtimeConn, error) {
	if id == "" || !strings.HasPrefix(method, "session.") || method == "session.start" {
		return nil, nil
	}
	h, label, known := n.runtimes.belongs(id)
	if h != nil {
		return h, nil
	}
	if known {
		// An omp whose extension left with it (omp exited): a stopped session the node answers from its file and a
		// message resumes (ompcalls.go), not a runtime that is away.
		if n.ompStopped(method, id) {
			return nil, nil
		}
		if awayReads[method] && n.runtimeTranscript(id) != "" {
			return nil, nil // the node reads the transcript (n.file), as it does while the runtime lists the session
		}
		if claimMethods[method] || method == "session.rename" || method == "session.close" || method == "session.rewind" || method == "session.archive" || method == "session.unarchive" ||
			method == "session.queue.edit" || method == "session.queue.send_now" {
			return nil, errRuntimeAway(label)
		}
		return nil, nil // unwatching and the like: nothing to refuse
	}
	if !claimMethods[method] || !runtimeIDRe.MatchString(id) {
		return nil, nil
	}
	conns := n.runtimes.askable(id)
	if len(conns) == 0 {
		// Asked lately: what the runtime whose id it is said then, unless this machine has the session by now.
		if gone := n.runtimes.goneOf(id); gone != nil && !n.hasOwnSession(id) {
			return nil, n.runtimeGoneErr(id, gone)
		}
		return nil, nil
	}
	if n.hasOwnSession(id) {
		return nil, nil
	}
	var gone error
	for _, c := range conns {
		cctx, cancel := context.WithTimeout(ctx, runtimeClaimTimeout)
		var page struct {
			Items []json.RawMessage `json:"items"`
		}
		err := c.peer.Call(cctx, "session.items", map[string]any{"session_id": id, "limit": 1}, &page)
		cancel()
		if hasSession(err) {
			n.runtimes.claim(c, id)
			return c, nil
		}
		if rpcCode(err) == codeNotFound && strings.HasPrefix(id, c.id()+"-") {
			gone = err
		}
	}
	n.runtimes.unclaim(id, gone)
	return nil, n.runtimeGoneErr(id, gone)
}

// runtimeGoneErr marks an owning-prefix runtime's not_found error with confirmed_missing=true when this node also sees
// no local terminal pane, presence lock, or active start holding the session. Existing message and data fields are kept
// untouched; any live or uncertain local ownership leaves the plain not_found error so callers stay fail-closed.
func (n *Node) runtimeGoneErr(id string, err error) error {
	if err == nil {
		return nil
	}
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != codeNotFound {
		return err
	}
	if n.runtimeSessionLocallyHeld(id) {
		return err
	}
	out := *re
	fields := map[string]any{}
	if len(re.Data) > 0 && json.Unmarshal(re.Data, &fields) != nil {
		return err
	}
	fields["confirmed_missing"] = true
	if raw, merr := json.Marshal(fields); merr == nil {
		out.Data = raw
	}
	return &out
}

// runtimeSessionLocallyHeld reports whether this node still sees local live/starting ownership or an Antigravity
// terminal pane or presence lock for id, vetoing confirmed_missing even when a runtime answered not_found.
func (n *Node) runtimeSessionLocallyHeld(id string) bool {
	if n.hasOwnSession(id) || n.isAssistantSession(id) || n.runtimeAssistant(id) || n.restarting(id) {
		return true
	}
	if r := n.starts.get(id); r != nil && r.ended.IsZero() {
		return true
	}
	if n.externalStarts.has(id) || n.startsBegun.within(id, n.startWait()) {
		return true
	}
	if st, ok := n.state.get(id); ok && (st.AwaitingFirstMessage || !st.StartedAt.IsZero() && time.Since(st.StartedAt) < n.startWait()) {
		return true
	}
	if conversation, ok := agyConversationID(id); ok {
		if _, _, held := n.agyOpenIn(id); held || n.agyHasRawPaneAttribution(conversation) {
			return true
		}
		switch agyPresenceLockProbe(filepath.Join(n.agyDataRoot(), "presence", conversation+".lock")) {
		case agyLockHeld, agyLockUnknown:
			return true
		}
	}
	return false
}

// agyHasRawPaneAttribution checks local panes, raw host snapshots, verified pane bindings, and fresh hook reports for
// conversation without cross-pane alias suppression, so twin terminal panes running one conversation still veto
// confirmed_missing.
func (n *Node) agyHasRawPaneAttribution(conversation string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range n.agents {
		if a.PaneID == "" {
			continue
		}
		if reportedAgyConversation(a) == conversation || n.agyReports[a.PaneID].conversation == conversation {
			return true
		}
		if b := n.agyBound[a.PaneID]; b.verified && b.conversation == conversation {
			return true
		}
	}
	for _, agents := range n.hostAgents {
		for _, a := range agents {
			if a.PaneID != "" && reportedAgyConversation(a) == conversation {
				return true
			}
		}
	}
	for _, rep := range n.agyReports {
		if rep.conversation == conversation && time.Since(rep.at) <= agyReportGrace {
			return true
		}
	}
	return false
}

// hasSession: a runtime's answer to session.items {limit: 1} says the session is its own. A page does, and so does a
// refusal to read it without starting its agent (proto.CodeNotLoaded: an agent Paseo let go, #8132), which only the
// runtime that has the session gives; the call itself then goes to it, and a watch or a send there starts the agent.
func hasSession(err error) bool {
	return err == nil || rpcCode(err) == proto.CodeNotLoaded
}

// codeNotFound is what a node, or a runtime it asks, answers for a session it does not have (the manager's
// codeSessionNotFound: a card of such a session is withdrawn).
const codeNotFound = "not_found"

// hasOwnSession: this machine has the session itself (a live agent in a terminal, a Claude / Codex and compatible variants record).
func (n *Node) hasOwnSession(id string) bool {
	if _, ok := n.agent(id); ok {
		return true
	}
	if n.transcriptPath(id) != "" {
		return true
	}
	for _, p := range []string{n.codexPath(id), n.traexPath(id)} {
		if _, err := os.Stat(p); p != "" && err == nil {
			return true
		}
	}
	return false
}

// errRuntimeAway: a session of a runtime that is not connected (its app is closed, the plugin is off). Nothing was
// sent; the hub may let the app send it again.
func errRuntimeAway(label string) error {
	return &rpc.Error{Message: fmt.Sprintf("%s 没有连上这台电脑的 Persona（%s 没在运行，或插件没装好），没有发出去", label, label),
		Code: "runtime_away", Data: json.RawMessage(`{"delivered":"none"}`)}
}

// errRuntimeUnsupported: a session.* call the session's runtime has nothing for (no terminal, no TUI modes, a cap it
// did not declare). Like a terminal host lacking a capability (host.ErrUnsupported), the text starts with
// "unsupported": the hub reads that prefix (a terminal stream it cannot open falls back to reading the screen).
func errRuntimeUnsupported(label, what string) error {
	return &rpc.Error{Message: fmt.Sprintf("unsupported: %s 的会话不支持%s", label, what), Code: "unsupported"}
}

// runtimeCallParams are the parameters of a hub call about a runtime's session that the node looks at.
type runtimeCallParams struct {
	MessageSource  *proto.MessageSource `json:"message_source"`
	A2AWakeOnSend  bool                 `json:"a2a_wake_on_send"`
	Force          bool                 `json:"force"`
	SessionID      string               `json:"session_id"`
	Text           string               `json:"text"`
	Images         []proto.Image        `json:"images"`
	SinceSeq       int                  `json:"since_seq"`
	History        string               `json:"history"`
	FoldWork       bool                 `json:"fold_work"`
	BeforeSeq      int                  `json:"before_seq"`
	LazyImages     bool                 `json:"lazy_images"`
	UserInputs     bool                 `json:"user_inputs"`
	MetaOnly       bool                 `json:"meta_only"`
	AfterSeq       *int                 `json:"after_seq"`
	ThroughSeq     int                  `json:"through_seq"`
	Title          *string              `json:"title"`
	ItemID         string               `json:"item_id"`
	PermissionMode string               `json:"permission_mode"`
	Effort         string               `json:"effort"`
	Model          string               `json:"model"`
}

// runtimeHandle serves a hub call for a session a runtime lists; handled is false for methods that are not about one
// session (the node answers those itself).
func (n *Node) runtimeHandle(ctx context.Context, h *runtimeConn, method string, params json.RawMessage) (res any, err error, handled bool) {
	label := h.label()
	h.mu.Lock()
	caps := h.hello.Caps
	h.mu.Unlock()
	call := func(m string, p any) (json.RawMessage, error) {
		ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
		defer cancel()
		var out json.RawMessage
		err := h.peer.Call(ctx, m, p, &out)
		if err != nil && !isRPCError(err) {
			err = fmt.Errorf("%s: %w", label, err)
		}
		return out, err
	}
	var p runtimeCallParams
	_ = json.Unmarshal(params, &p)
	// A session whose conversation is a Claude transcript here (runtimetranscript.go): its content is the node's.
	if n.runtimeTranscript(p.SessionID) != "" {
		if transcriptCalls[method] {
			return nil, nil, false // node.handle reads the transcript (n.file)
		}
		if res, err, handled := n.runtimeTranscriptCall(ctx, h, method, p, call); handled {
			return res, err, true
		}
	}
	// A session in a terminal pane (a pi in herdr, pi.go): the pane is the engine's — the screen, the keys, closing
	// it, its tab's name — and the rest is the runtime's.
	if a, inPane := n.agent(p.SessionID); inPane && a.PaneID != "" {
		switch method {
		case "session.keys", "session.screen", "session.screen.before", "terminal.watch", "terminal.unwatch", "session.close":
			return nil, nil, false
		case "session.rename":
			if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
				return nil, nil, false
			}
		}
	}
	readItems := func(params any) (json.RawMessage, error) { return call("session.items", params) }
	switch method {
	case proto.MethodSessionInputPreviews, proto.MethodSessionItemsWindow:
		res, err := serveSessionNavigation(ctx, runtimeNavigation{node: n, read: readItems}, method, params)
		return res, err, true
	case "session.items":
		if p.UserInputs {
			index, err := n.runtimeUserInputs(ctx, p.SessionID, p.BeforeSeq, p.MetaOnly, readItems)
			return index, err, true
		}
		out, err := call("session.items", params)
		if err != nil {
			return nil, err, true
		}
		out = n.echoedRaw(p.SessionID, out, "items") // echo.go
		out = n.toolViews.attachRaw(p.SessionID, out, "items", p.BeforeSeq == 0 && p.ThroughSeq == 0)
		out = n.ttft.annotateRaw(p.SessionID, out, "items", p.BeforeSeq == 0 && p.ThroughSeq == 0)
		if !p.FoldWork || p.AfterSeq != nil || p.ThroughSeq > 0 {
			return out, nil, true
		}
		// Pages counted back from the end fold the agent's work, as for transcripts (transcript.FoldWork).
		var page map[string]json.RawMessage
		if json.Unmarshal(out, &page) != nil {
			return out, nil, true
		}
		var items []proto.Item
		if json.Unmarshal(page["items"], &items) != nil {
			return out, nil, true
		}
		if p.BeforeSeq == 0 {
			items = transcript.FoldWorkLatest(items)
		} else {
			items = transcript.FoldWork(items)
		}
		folded, _ := json.Marshal(nonNil(items))
		page["items"] = folded
		return page, nil, true
	case "session.image", "session.item", "session.visualization":
		out, err := call(method, params)
		if method == "session.item" && err == nil {
			out = n.toolViews.attachItemRaw(p.SessionID, out)
		}
		return out, err, true
	case "session.watch":
		attempt := n.runtimes.watch(h, p.SessionID, p.SinceSeq, p.History, rpc.ConnContext(ctx))
		params := map[string]any{"session_id": p.SessionID, "since_seq": p.SinceSeq}
		if p.History != "" {
			params["history"] = p.History
		}
		_, err := call("session.watch", params)
		if err != nil {
			n.runtimes.watchFailed(h, p.SessionID, attempt.token)
		}
		return okResult(err), err, true
	case "session.unwatch":
		n.runtimes.unwatch(p.SessionID)
		_, err := call("session.unwatch", map[string]any{"session_id": p.SessionID})
		return okResult(err), err, true
	case "session.send", proto.MethodSessionSendUnseen:
		if !caps.Send {
			return nil, errRuntimeUnsupported(label, "从手机发消息"), true
		}
		// A session that ran in the terminal engine and has stopped: resumed there, then sent to (pi.go).
		if argv := h.resumeArgv(p.SessionID); len(argv) > 0 && n.runtimeStatus(h, p.SessionID) == proto.StatusOffline {
			res, err := n.resumeInEngine(ctx, h, p.SessionID, argv, params)
			return res, err, true
		}
		var opts sendOpts
		_ = json.Unmarshal(params, &opts)
		var retry *echoRecord
		var previous *sendRecord
		if method == proto.MethodSessionSendUnseen {
			check := n.checkEcho(ctx, p.SessionID, opts.ClientMsgID)
			switch check.state {
			case proto.EchoSeen:
				return map[string]any{"ok": true}, nil, true
			case proto.EchoKnownUnseen:
				retry, previous = &check.record, check.previous
			default:
				return nil, echoUnconfirmed(), true
			}
		}
		var said json.RawMessage // what the runtime answered: it may say the message only queued behind the turn
		syntheticSegment := isSyntheticSegmentMsgID(opts.ClientMsgID)
		err := n.sends.onceReplacing(opts.ClientMsgID, previous, func() error {
			sendFn := func() error {
				if err := n.a2aSendReady(ctx, p.SessionID); err != nil {
					return err
				}
				if h.agent() == agyAgent {
					if err := n.agyIdleCompactBeforeInput(ctx, h, p.SessionID, p.Text, p.Images, false, false); err != nil {
						return err
					}
				}
				input := map[string]any{"session_id": p.SessionID, "text": p.Text, "images": p.Images}
				if !syntheticSegment {
					input["client_msg_id"] = opts.ClientMsgID
				}
				if h.agent() == agyAgent && caps.Model && p.Model != "" {
					input["model"] = p.Model
				}
				if p.PermissionMode != "" {
					input["mode"] = p.PermissionMode
				} else if h.agent() == agyAgent && !n.runtimeAssistant(p.SessionID) {
					if mode := agySendMode(n.runtimePermissionMode(h, p.SessionID), n.agyRuntimeMode(p.SessionID)); mode != "" {
						input["mode"] = mode
					}
				}
				if h.agent() == agyAgent && !n.runtimeAssistant(p.SessionID) && n.state != nil {
					if st, ok := n.state.get(p.SessionID); ok && st.Launcher != "" {
						ch, err := n.agyChannelFor(ctx, st.Launcher)
						if err != nil {
							return err
						}
						if ch != nil {
							input["channel"] = ch
						}
					}
				}
				if p.A2AWakeOnSend {
					input["a2a_wake_on_send"] = true
				}
				sentAt := time.Now()
				n.ttft.arm(p.SessionID, sentAt, "")
				finishAgy := n.runtimes.agyWorkBegin(h, method, p.SessionID)
				out, err := call(method, input)
				finishAgy(err, false)
				said = out
				if err == nil && h.agent() == agyAgent && n.clearAgyStopped(p.SessionID, sentAt) {
					n.publishSessionsSoon()
				}
				if (err != nil && !maybeDelivered(err)) || (err == nil && runtimeAnswerFlag(out, "queued")) {
					n.ttft.disarm(p.SessionID, sentAt)
				}
				return err
			}
			if syntheticSegment && retry == nil {
				err := sendFn()
				if err == nil {
					n.recordSegmentInput(p.SessionID, opts.ClientMsgID)
				}
				return err
			}
			var after echoAfter
			if opts.ClientMsgID != "" {
				after = echoAfter{seq: n.runtimes.lastPushed(p.SessionID), in: echoInRuntime}
				if n.runtimeTranscript(p.SessionID) != "" { // its items are the transcript's: its copy comes after its end
					after = n.echoBase(p.SessionID)
				}
			}
			if retry != nil {
				// As sendUnseen: another send can start a turn while this retry waits for the input lock.
				unlock, err := n.inputTurn(ctx, p.SessionID)
				if err != nil {
					return echoUnconfirmed()
				}
				defer unlock()
				check := n.readEcho(ctx, p.SessionID, opts.ClientMsgID)
				if check.state == proto.EchoSeen {
					return nil
				}
				if check.state != proto.EchoKnownUnseen || !check.record.SentAt.Equal(retry.SentAt) {
					return echoUnconfirmed()
				}
				retry, after = &check.record, check.after
			}
			return n.sendWithEcho(p.SessionID, opts.ClientMsgID, p.Text, len(p.Images), after, p.MessageSource, retry, sendFn)
		})
		if rpcCode(err) == runtimeHandover && ctx.Value(handingOver{}) == nil {
			res, err := n.handOver(ctx, h, p.SessionID, params)
			return res, err, true
		}
		return withFlag(okResult(err), said, "queued"), err, true
	case "session.interrupt":
		if !caps.Interrupt {
			return nil, errRuntimeUnsupported(label, "中断"), true
		}
		// A runtime that waited for the turn to end says so (settled); one that only asked it to stop does not.
		finishAgy := n.runtimes.agyWorkBegin(h, method, p.SessionID)
		out, err := call("session.interrupt", map[string]any{"session_id": p.SessionID})
		finishAgy(err, runtimeAnswerFlag(out, "settled"))
		return withFlag(okResult(err), out, "settled"), err, true
	case "session.close":
		if !caps.Close {
			return nil, errRuntimeUnsupported(label, "从手机关闭"), true
		}
		if p.Force && (n.runtimeAssistant(p.SessionID) || n.runtimeStatus(h, p.SessionID) != proto.StatusBlocked) {
			return nil, refusal("agent_busy", "the Agent is no longer waiting for confirmation", nil), true
		}
		closeParams := map[string]any{"session_id": p.SessionID}
		if p.Force {
			closeParams["force"] = true
		}
		finishAgy := n.runtimes.agyWorkBegin(h, method, p.SessionID)
		_, err := call("session.close", closeParams)
		finishAgy(err, true)
		if err == nil {
			if gp := n.existingGates(); gp != nil {
				gp.releaseSession(p.SessionID)
			}
		}
		return okResult(err), err, true
	case "session.archive", "session.unarchive":
		// Its own archive (archive.go): one that has none keeps the archive Persona's alone.
		if !caps.Archive || strings.Contains(p.SessionID, subagentSep) {
			return archiveUnsupported(label + " has no archive the node can drive"), nil, true
		}
		if _, err := call(method, map[string]any{"session_id": p.SessionID}); err != nil {
			return nil, err, true
		}
		return nativeArchiveResult{Native: nativeArchiveDone, Via: "runtime"}, nil, true
	case "session.seen":
		// Only a runtime with an unread mark of its own is told (caps.seen); for any other there is nothing to clear.
		if !caps.Seen || strings.Contains(p.SessionID, subagentSep) {
			return okResult(nil), nil, true
		}
		_, err := call("session.seen", map[string]any{"session_id": p.SessionID})
		return okResult(err), err, true
	case "session.rename":
		if p.Title == nil || strings.TrimSpace(*p.Title) == "" {
			return okResult(nil), nil, true // a tab name: these sessions have no tab
		}
		if !caps.Rename {
			return nil, errRuntimeUnsupported(label, "改名"), true
		}
		_, err := call("session.rename", map[string]any{"session_id": p.SessionID, "title": strings.TrimSpace(*p.Title)})
		return okResult(err), err, true
	case "session.screen":
		return map[string]any{"text": "", "status": n.runtimeStatus(h, p.SessionID)}, nil, true
	case "session.info":
		// The counts a transcript gives are unknown here, and usage_unknown says so: the apps leave them out rather than
		// show "0 tokens, 0 turns" (#8261). They stay zero for the apps older than that, which decode them as required
		// numbers and read them as "none yet".
		info := map[string]any{"session_id": p.SessionID, "agent": h.agent(), "context_tokens": 0, "output_tokens": 0,
			"compactions": 0, "turns": 0, "usage_unknown": true}
		for _, s := range h.sessionsCopy() {
			if s.SessionID == p.SessionID {
				info["cwd"], info["model"], info["agent"], info["last_activity"] = s.Cwd, s.Model, s.Agent, s.UpdatedAt
				mode := s.PermissionMode
				if h.agent() == agyAgent && !n.runtimeAssistant(p.SessionID) {
					mode = agyShownMode(mode, n.agyRuntimeMode(p.SessionID))
				}
				info["permission_mode"], info["effort"] = mode, s.Effort
				if h.agent() == agyAgent {
					if conversation, ok := strings.CutPrefix(s.SessionID, agyAgent+"-"); ok {
						if workers := n.agySwarmWorkers(conversation); len(workers) > 0 {
							info["swarm_workers"] = workers
						}
					}
					if s.ContextTokens > 0 || s.OutputTokensValue() > 0 {
						info["context_tokens"] = int(s.ContextTokens)
						info["output_tokens"] = int(s.OutputTokensValue())
						delete(info, "usage_unknown")
					}
					n.resolveAgyContextWindow(&s)
					if bd := s.ContextBreakdown(); bd != nil {
						info["context_breakdown"] = bd
					}
				}
			}
		}
		// Not in its list (an older session it still has): what the node knows without the list.
		return info, nil, true
	case "session.keys", "session.screen.before", "terminal.watch":
		return nil, errRuntimeUnsupported(label, "终端"), true
	case "terminal.unwatch":
		return okResult(nil), nil, true
	case "session.modes":
		if !caps.Modes || p.Model != "" && !caps.Model || strings.Contains(p.SessionID, subagentSep) {
			return nil, errRuntimeUnsupported(label, "这个操作"), true
		}
		if err := validModes(p.PermissionMode, p.Effort); err != nil {
			return nil, err, true
		}
		_, err := call("session.modes", map[string]any{"session_id": p.SessionID, "permission_mode": p.PermissionMode, "effort": p.Effort, "model": p.Model})
		if err == nil && h.agent() == agyAgent && p.PermissionMode != "" && n.state != nil {
			n.state.update(p.SessionID, func(st *sessionState) bool {
				if st.AppMode == p.PermissionMode {
					return false
				}
				st.AppMode = p.PermissionMode
				return true
			})
		}
		return okResult(err), err, true
	case "session.queue.edit":
		if !caps.QueueEdit || strings.Contains(p.SessionID, subagentSep) {
			return nil, errRuntimeUnsupported(label, "这个操作"), true
		}
		var edit struct {
			Keep json.RawMessage `json:"keep"`
		}
		if err := json.Unmarshal(params, &edit); err != nil {
			return nil, err, true
		}
		out, err := call(method, map[string]any{"session_id": p.SessionID, "keep": edit.Keep})
		return out, err, true
	case "session.queue.send_now":
		if !caps.QueueSendNow || strings.Contains(p.SessionID, subagentSep) {
			return nil, errRuntimeUnsupported(label, "这个操作"), true
		}
		sentAt := time.Now()
		finishAgy := n.runtimes.agyWorkBegin(h, method, p.SessionID)
		_, err := call(method, map[string]any{"session_id": p.SessionID})
		finishAgy(err, false)
		if err != nil {
			return nil, err, true
		}
		if h.agent() == agyAgent && n.clearAgyStopped(p.SessionID, sentAt) {
			n.publishSessionsSoon()
		}
		return map[string]any{"sent": true}, nil, true
	}
	return nil, nil, false
}

// agyRuntimeMode returns the persisted permission mode for a Persona-started non-assistant agy companion session, or ""
// when none was recorded (settings-derived or externally started sessions).
func (n *Node) agyRuntimeMode(id string) string {
	if n.state == nil || id == "" {
		return ""
	}
	st, ok := n.state.get(id)
	if !ok || st.AppMode == "" {
		return ""
	}
	return st.AppMode
}

// agyLiveAutoFallback reports whether a companion row is currently running under a live auto-to-bypass fallback for a
// persisted "auto" session.
func agyLiveAutoFallback(rowMode, savedMode string) bool {
	return savedMode == "auto" && rowMode == proto.ModeBypass
}

// agyShownMode overlays a persisted agy permission mode onto a companion row's reported mode while preserving a live
// auto-to-bypass fallback reported by the companion.
func agyShownMode(rowMode, savedMode string) string {
	if savedMode == "" {
		return rowMode
	}
	if agyLiveAutoFallback(rowMode, savedMode) {
		return rowMode
	}
	return savedMode
}

// agySendMode returns the persisted agy permission mode to replay on session.send, or "" when no mode should be sent
// (unset/external sessions, or a live auto-to-bypass fallback where replaying "auto" would re-trigger the fallback).
func agySendMode(rowMode, savedMode string) string {
	if savedMode == "" || agyLiveAutoFallback(rowMode, savedMode) {
		return ""
	}
	return savedMode
}

func (n *Node) runtimePermissionMode(h *runtimeConn, id string) string {
	if h == nil {
		return ""
	}
	for _, s := range h.sessionsCopy() {
		if s.SessionID == id {
			return s.PermissionMode
		}
	}
	return ""
}

func (n *Node) runtimeStatus(h *runtimeConn, id string) string {
	for _, s := range h.sessionsCopy() {
		if s.SessionID == id {
			return s.Status
		}
	}
	return proto.StatusUnknown
}

// runtimeHandover is the code of a send a runtime hands over instead of taking (it lists the session from disk, and
// has asked the program that runs it to start): handOver delivers it to the runtime that takes the session.
const runtimeHandover = "handover"

// runtimeHandoverWait bounds the wait for the runtime that takes a handed-over session.
var runtimeHandoverWait = 45 * time.Second

// handingOver marks the context of a send handOver forwards: the runtime it reaches cannot hand it over again.
type handingOver struct{}

// handOver delivers a send that from (the session's owner) handed over: to the connection that lists the session as
// running once one does (prefer_running), or to the first other runtime that answers for it (an older session it
// does not list; the claim path of runtimeFor), whichever comes first within runtimeHandoverWait. Nothing is sent
// when none does.
func (n *Node) handOver(ctx context.Context, from *runtimeConn, id string, params json.RawMessage) (any, error) {
	ctx = context.WithValue(ctx, handingOver{}, true)
	forward := func(to *runtimeConn) (any, error) {
		res, err, _ := n.runtimeHandle(ctx, to, "session.send", params)
		return res, err
	}
	deadline := time.Now().Add(runtimeHandoverWait)
	var asked time.Time
	for {
		if r, owner, ok := n.runtimes.rowOf(id); ok && owner != from && r.Status != proto.StatusOffline {
			return forward(owner)
		}
		if time.Since(asked) > 2*time.Second {
			asked = time.Now()
			for _, c := range n.runtimes.ready() {
				if c == from {
					continue
				}
				cctx, cancel := context.WithTimeout(ctx, runtimeClaimTimeout)
				err := c.peer.Call(cctx, "session.items", map[string]any{"session_id": id, "limit": 1}, nil)
				cancel()
				if hasSession(err) {
					n.runtimes.claim(c, id)
					return forward(c)
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, refusal("not_running", fmt.Sprintf("%s 把这个会话交给运行它的程序，%d 秒内没等到它接手，这次没有发送。",
				from.label(), int(runtimeHandoverWait.Seconds())), nil)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// rpcCode is the code of an rpc error, "" for anything else.
func rpcCode(err error) string {
	var e *rpc.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func okResult(err error) any {
	if err != nil {
		return nil
	}
	return map[string]any{"ok": true}
}

// withFlag carries the runtime's boolean flag (session.send's queued, session.interrupt's settled) from its answer
// into the node's ok result, when it is set.
func withFlag(res any, answer json.RawMessage, flag string) any {
	m, ok := res.(map[string]any)
	if ok && runtimeAnswerFlag(answer, flag) {
		m[flag] = true
	}
	return res
}

func runtimeAnswerFlag(answer json.RawMessage, flag string) bool {
	var a map[string]json.RawMessage
	return len(answer) != 0 && json.Unmarshal(answer, &a) == nil && string(a[flag]) == "true"
}

func isRPCError(err error) bool {
	var e *rpc.Error
	return errors.As(err, &e)
}
