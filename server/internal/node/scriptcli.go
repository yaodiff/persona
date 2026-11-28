package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// `personad ask` and `personad notify` (internal/scriptcli) reach the phone through the node's socket like the hook
// does (docs/integrations/script-cli.md). ask is an interaction of the same shapes the phone already draws: a question card when it
// has options, an allow / deny permission card when it has not. notify is a hub "notify" request (the path
// POST /api/v1/notify takes). Both are attributed to the session of the pane the caller runs in, when the caller says
// which pane that is (HookPane) and a hook has told the node what runs there.

// What the node says about a script's cards and notifications.
const (
	// scriptAgent / scriptAgentLabel: who asks, as a runtime's cards say it (docs/integrations/runtimes.md): the apps draw "Script is asking you" (in Chinese)
	// and offer no "always allow" on it.
	scriptAgent      = "script"
	scriptAgentLabel = "脚本"
	scriptAskTool    = "personad_ask"
	// notifyTimeout bounds the hub's answer to a notification.
	notifyTimeout = 15 * time.Second
	// The hub's limits of a notification's texts (hub/notify_api.go); longer ones are cut here, not refused.
	notifyTitleMost = 120
	notifyBodyMost  = 1000
)

// scriptSession is the session that runs in the caller's pane and its title; "" when no hook has told the node.
func (n *Node) scriptSession(pane, cwd string) (id, title string) {
	if pane != "" {
		n.mu.Lock()
		id = n.panes[pane]
		n.mu.Unlock()
	}
	if id != "" {
		if p := n.transcriptPath(id); p != "" {
			if st, err := os.Stat(p); err == nil {
				title = n.meta(p, st).title
			}
		}
	}
	if title == "" && cwd != "" {
		title = filepath.Base(cwd)
	}
	if title == "" {
		title = scriptAgentLabel
	}
	return id, title
}

// scriptInteractionID is the same for every attempt of one ask, so one that reconnects after a restart finds its card.
func scriptInteractionID(attempt string) string {
	if attempt == "" {
		return "ix_" + newUUID()[:12]
	}
	sum := sha256.Sum256([]byte("script-ask\x00" + attempt))
	return "ix_" + hex.EncodeToString(sum[:])[:16]
}

// handleAsk puts the question in front of the phone and waits.
func (n *Node) handleAsk(ctx context.Context, req sockRequest) nodeclient.AskReply {
	var a nodeclient.AskRequest
	if err := json.Unmarshal(req.Input, &a); err != nil {
		return nodeclient.AskReply{Status: nodeclient.AskInvalid, Error: "bad request"}
	}
	if err := a.Normalize(); err != nil {
		return nodeclient.AskReply{Status: nodeclient.AskInvalid, Error: err.Error()}
	}
	if n.peer == nil {
		return nodeclient.AskReply{Status: nodeclient.AskUnavailable, Error: "this node is not connected to a hub"}
	}
	sid, title := n.scriptSession(req.Pane, a.Cwd)
	if n.sessionBlocked(sid, a.Cwd) {
		return nodeclient.AskReply{Status: nodeclient.AskUnavailable, Error: "this folder is blocked in Persona"}
	}
	now := time.Now()
	ix := proto.Interaction{
		ID: scriptInteractionID(req.Attempt), SessionID: sid, SessionTitle: title, Cwd: a.Cwd,
		CreatedAt: now.UTC(), ExpiresAt: now.Add(a.Timeout()).UTC(), ToolName: scriptAskTool,
		Resumable: req.Attempt != "", Agent: scriptAgent, AgentLabel: scriptAgentLabel, Format: a.Format,
	}
	if len(a.Options) > 0 {
		ix.Kind = proto.IxQuestion
		q := proto.Question{Question: a.Question, Header: scriptAgentLabel}
		for _, o := range a.Options {
			q.Options = append(q.Options, proto.QuestionOption{Label: o})
		}
		ix.Questions = []proto.Question{q}
	} else {
		ix.Kind, ix.Summary = proto.IxPermission, a.Question
	}
	res, ok, err := n.ask(ctx, ix)
	switch {
	case errors.Is(err, errNodeGone):
		return nodeclient.AskReply{Retry: true}
	case err != nil:
		return nodeclient.AskReply{Status: nodeclient.AskUnavailable, Error: err.Error()}
	case !ok && !time.Now().Before(ix.ExpiresAt.Add(-500*time.Millisecond)):
		// The hub's timer and ours end at the same moment: n.ask cannot tell which one it was.
		return nodeclient.AskReply{Status: nodeclient.AskTimeout}
	case !ok && res.Dismiss:
		return nodeclient.AskReply{Status: nodeclient.AskDismissed}
	case !ok:
		return nodeclient.AskReply{Status: nodeclient.AskUnavailable, Error: "the hub did not take the question"}
	}
	if ix.Kind == proto.IxPermission {
		switch {
		case res.Allow == nil:
			return nodeclient.AskReply{Status: nodeclient.AskDismissed}
		case *res.Allow:
			return nodeclient.AskReply{Status: nodeclient.AskAnswered}
		}
		return nodeclient.AskReply{Status: nodeclient.AskDenied}
	}
	answer := strings.TrimSpace(res.Answers[a.Question])
	if answer == "" && len(res.Answers) == 1 {
		for _, v := range res.Answers {
			answer = strings.TrimSpace(v)
		}
	}
	if answer == "" {
		return nodeclient.AskReply{Status: nodeclient.AskDismissed}
	}
	return nodeclient.AskReply{Status: nodeclient.AskAnswered, Answer: answer}
}

// handleDecision carries a local script's non-blocking rich decision to the
// manager. The connected machine is the caller identity; the pane's session
// title is only its display source. These requests are deliberately not spooled:
// a local queued id would not be a manager-durable id the script can poll.
func (n *Node) handleDecision(ctx context.Context, req sockRequest) assistantwire.OutsideDecisionReply {
	if n.peer == nil {
		return assistantwire.OutsideDecisionReply{Code: "unavailable", Error: "this node is not connected to a hub"}
	}
	var reply assistantwire.OutsideDecisionReply
	var err error
	switch req.Kind {
	case assistantwire.MethodDecisionRaise:
		var in nodeclient.DecisionRaiseRequest
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return assistantwire.OutsideDecisionReply{Code: "invalid_decision", Error: "bad request"}
		}
		sid, title := n.scriptSession(req.Pane, in.Cwd)
		if n.sessionBlocked(sid, in.Cwd) {
			return assistantwire.OutsideDecisionReply{Code: "forbidden", Error: "this folder is blocked in Persona"}
		}
		reply, err = buswire.Call(ctx, n.peer, assistantwire.DecisionRaiseMethod, assistantwire.OutsideDecisionRaiseParams{
			Decision: in.Decision, Cwd: in.Cwd, Source: title,
		})
	case assistantwire.MethodDecisionGet, assistantwire.MethodDecisionWithdraw:
		var in assistantwire.OutsideDecisionIDParams
		if err := json.Unmarshal(req.Input, &in); err != nil || in.ID == "" {
			return assistantwire.OutsideDecisionReply{Code: "invalid_decision", Error: "a decision id is required"}
		}
		if req.Kind == assistantwire.MethodDecisionGet {
			reply, err = buswire.Call(ctx, n.peer, assistantwire.DecisionGetMethod, in)
		} else {
			reply, err = buswire.Call(ctx, n.peer, assistantwire.DecisionWithdrawMethod, in)
		}
	default:
		return assistantwire.OutsideDecisionReply{Code: "unavailable", Error: "this node does not support decisions"}
	}
	if err != nil {
		return assistantwire.OutsideDecisionReply{Code: "unavailable", Error: err.Error()}
	}
	return reply
}

// cutRunes cuts s to at most n characters, ending in … when it was cut.
func cutRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// handleNotify has the hub alert the user's devices (hub "notify" request, the path POST /api/v1/notify takes).
func (n *Node) handleNotify(ctx context.Context, req sockRequest) nodeclient.NotifyReply {
	return n.sendNotify(ctx, req, "")
}

// sendNotify is handleNotify; spoolID marks one standby kept (spool.go), for the manager to drop a repeat.
func (n *Node) sendNotify(ctx context.Context, req sockRequest, spoolID string) nodeclient.NotifyReply {
	rep, _ := n.notifyCall(ctx, req, spoolID)
	return rep
}

// notifyCall is sendNotify, with the error of the manager's call (nil when it answered, or when nothing was asked).
func (n *Node) notifyCall(ctx context.Context, req sockRequest, spoolID string) (nodeclient.NotifyReply, error) {
	var nr nodeclient.NotifyRequest
	if err := json.Unmarshal(req.Input, &nr); err != nil {
		return nodeclient.NotifyReply{Error: "bad request"}, nil
	}
	nr.Title, nr.Body = strings.TrimSpace(nr.Title), strings.TrimSpace(nr.Body)
	if nr.Title == "" && nr.Body == "" {
		return nodeclient.NotifyReply{Error: "the notification is empty"}, nil
	}
	if n.peer == nil {
		return nodeclient.NotifyReply{Error: "this node is not connected to a hub"}, nil
	}
	level := "active"
	if nr.Error {
		level = "time-sensitive"
		nr.Title = "⚠️ " + nr.Title
	}
	params := map[string]any{
		"title": cutRunes(strings.TrimSpace(nr.Title), notifyTitleMost), "body": cutRunes(nr.Body, notifyBodyMost),
		"level": level, "silent": nr.Silent,
	}
	if sid, _ := n.scriptSession(req.Pane, ""); sid != "" {
		if n.sessionBlocked(sid, n.sessionCwd(sid)) {
			return nodeclient.NotifyReply{Reason: "this folder is blocked in Persona"}, nil
		}
		params["session_id"] = sid // a tap opens that session; a muted session gets nothing
	}
	if spoolID != "" {
		params["spool_id"] = spoolID
	}
	ctx, cancel := context.WithTimeout(ctx, notifyTimeout)
	defer cancel()
	var res struct {
		Sent   bool   `json:"sent"`
		Reason string `json:"reason"`
	}
	if err := n.peer.Call(ctx, "notify", params, &res); err != nil {
		return nodeclient.NotifyReply{Error: err.Error()}, err
	}
	return nodeclient.NotifyReply{Sent: res.Sent, Reason: res.Reason}, nil
}
