package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const a2aConfirmFor = 10 * time.Minute

func a2aDeliveryMethod(method string) bool {
	return method == "session.a2a.send" || method == "session.a2a.wake"
}

func (s *Service) a2aLoop(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-s.a2aWake:
		}
		cfg := s.a2aConfig(ctx)
		if cfg.Machines != nil && len(cfg.Machines) == 0 {
			continue
		}
		st, err := s.a2aStore()
		if err != nil {
			continue
		}
		rows, err := st.A2AClaim(ctx, 12)
		if err != nil {
			s.logf("A2A delivery: %v", err)
			continue
		}
		var workers sync.WaitGroup
		for _, r := range rows {
			workers.Go(func() {
				ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
				defer cancel()
				t, err := decodeA2A(&r, nil)
				if err == nil {
					err = s.a2aProcess(ctx, st, cfg, t)
				}
				if err != nil && !errors.Is(err, errA2AConflict) && ctx.Err() == nil {
					s.logf("A2A %s: %v", r.ID, err)
				}
			})
		}
		workers.Wait()
	}
}

func (s *Service) a2aSignal() {
	select {
	case s.a2aWake <- struct{}{}:
	default:
	}
}

// Idle notifications shorten only the queued request's check lease. The
// delivery worker still uses the revision fence and the node's idle guard.
func (s *Service) a2aRecipientIdle(ctx context.Context, ref a2aRef) {
	st, err := s.a2aStore()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := st.A2AReady(ctx, ref.Machine, ref.Session); err != nil {
		s.logf("A2A idle delivery: %v", err)
	}
	s.a2aSignal()
}

func a2aDeliveryID(task string, seq int) string {
	return fmt.Sprintf("%s%s-%d", proto.A2ADeliveryPrefix, task, seq)
}

func (s *Service) a2aItems(ctx context.Context, ref a2aRef) ([]proto.Item, error) {
	var res struct{ Items []proto.Item }
	err := s.call(ctx, ref.Machine, DeviceSessionItems, map[string]any{"session_id": ref.Session, "turns": 4, "lazy_images": true}, &res)
	return res.Items, err
}

type a2aTurnView struct {
	echo, current, closed, interrupted bool
	reply                              string
}

// a2aTurn binds only the exchange after the exact input. A completed older
// exchange can be recovered after restart if the transcript closed that turn.
func a2aTurn(items []proto.Item, id string) a2aTurnView {
	var out a2aTurnView
	start := -1
	for i, item := range items {
		user := item.Role == "user" && item.Kind != proto.KindToolResult
		if user || item.Wake || item.Injected != nil {
			if user && (item.ClientMsgID == id || prompts.RelayMessageID(item.Text) == id) {
				start = i
			} else if start != -1 {
				return a2aReadTurn(items[start:i], false)
			}
		}
	}
	if start != -1 {
		out = a2aReadTurn(items[start:], true)
	}
	return out
}

func a2aReadTurn(items []proto.Item, current bool) a2aTurnView {
	end, closed := turnEndOf(items)
	out := a2aTurnView{echo: true, current: current, closed: closed}
	for _, item := range items {
		if item.Role == "assistant" && item.Kind == proto.KindText && !item.Narration && item.Text != "" {
			out.reply = a2aCleanText(item.Text)
		}
		if closed && item.ID == end.Key && item.Kind == proto.KindNotice && item.Text == userStoppedNotice {
			out.interrupted = true
		}
	}
	return out
}

func (s *Service) a2aProcess(ctx context.Context, st a2aStore, cfg assistantwire.A2AConfig, t *a2aTask) error {
	if t.SourceSession != "" && !s.a2aTaskEndpointAllowed(ctx, cfg, t.source(), t) {
		if !a2aTerminal(t.State) && s.a2aEndpointClosed(ctx, cfg, t.source()) {
			t.fail("The sending session was closed.", false)
			return s.a2aSave(ctx, st, t)
		}
		return nil
	}
	if !s.a2aTaskEndpointAllowed(ctx, cfg, t.target(), t) {
		if !t.TargetClosed && s.a2aEndpointClosed(ctx, cfg, t.target()) {
			t.TargetClosed = true
			if !a2aTerminal(t.State) {
				t.fail("The receiving session was closed.", true)
			}
			return s.a2aSave(ctx, st, t)
		}
		if !t.TargetClosed || !s.a2aMachineAllowed(ctx, cfg, t.TargetMachine) {
			return nil
		}
	}
	if t.State == a2aQueued {
		return nil
	}
	// If A2A subagent launches were disabled while a launch was still queued (never sent to a node),
	// fail it explicitly even before the target computer comes back online. Already-attempted launches
	// keep reconciling normally, and ordinary peer messages or dispatches are unaffected.
	if t.Launch != nil && t.Launch.Pending && !t.Launch.Dispatch && !a2aTerminal(t.State) &&
		len(t.Events) > 0 && t.Events[0].Delivery == a2aQueued && !cfg.LaunchAllowed() {
		t.fail("Starting new A2A subagent sessions is disabled by configuration.", true)
		s.forgetA2ASubagent(t.ID)
		return s.a2aSave(ctx, st, t)
	}
	machine := s.logicalExecutionMachine(ctx, t.TargetMachine, t.TargetSession)
	offline := s.computerState(machine).state == standingComputerOffline
	if !a2aTerminal(t.State) && offline != t.Disconnected {
		t.Disconnected = offline
		if len(t.Events) < a2aMaxEvents/4 {
			e := a2aEvent{ID: randomHex(16), Kind: "connection", Text: "The receiving computer reconnected."}
			if offline {
				e.Text = "The receiving computer disconnected. The task will be checked when it returns."
			}
			t.append(e)
		}
		if err := s.a2aSave(ctx, st, t); err != nil {
			return err
		}
	}
	if !offline && t.Launch != nil && t.Launch.Pending && !a2aTerminal(t.State) {
		return s.a2aLaunch(ctx, st, t)
	}
	for i, e := range t.Events {
		if !a2aActiveDelivery(e) {
			continue
		}
		// Start the bounded delivery wait only when the task is active, not
		// while it is behind another task in the recipient's FIFO.
		if e.Kind == "message" && e.Delivery == a2aQueued && e.WaitingSince.IsZero() {
			t.Events[i].WaitingSince = time.Now().UTC()
			if err := s.a2aSave(ctx, st, t); err != nil {
				return err
			}
		}
		to, from := t.target(), t.source()
		if offline && e.Kind == "message" {
			if changed, err := s.a2aExpireDelivery(ctx, st, t, i); changed || err != nil {
				return err
			}
			continue
		}
		if e.Kind != "message" {
			to, from = from, to
		}
		changed, err := s.a2aDeliver(ctx, st, t, i, to, from)
		if err != nil || changed || e.Kind == "message" {
			return err
		}
	}
	if a2aTerminal(t.State) {
		if !slices.ContainsFunc(t.Events, a2aActiveDelivery) {
			return s.a2aSave(ctx, st, t)
		}
		return nil
	}
	if t.State == a2aInput && t.SourceSession != "" {
		return s.a2aObserveAnswer(ctx, st, t)
	}
	if offline {
		return nil
	}
	return s.a2aObserve(ctx, st, t)
}

func (s *Service) a2aEndpointClosed(ctx context.Context, cfg assistantwire.A2AConfig, ref a2aRef) bool {
	if !s.a2aMachineAllowed(ctx, cfg, ref.Machine) {
		return false
	}
	if _, deleted, _ := s.deletedConversation(ctx, ref.Machine, ref.Session); deleted {
		return true
	}
	if row, ok, _ := s.archivedRow(ctx, ref.Machine, ref.Session); ok && row.Archived {
		return true
	}
	row, ok, err := s.sessionNow(ctx, ref.Machine, ref.Session)
	return err == nil && ok && (row.Archived || row.IsDeleted() || row.HiddenByDirectory() || s.hiddenByDirectory(ref.Machine, row))
}

func (s *Service) a2aExpireDelivery(ctx context.Context, st a2aStore, t *a2aTask, i int) (bool, error) {
	e := &t.Events[i]
	since := e.Attempted
	if e.Delivery == a2aQueued {
		if e.Kind != "message" {
			return false, nil
		}
		since = e.WaitingSince
	}
	if since.IsZero() || time.Since(since) < a2aConfirmFor {
		return false, nil
	}
	if e.Kind == "message" && !a2aTerminal(t.State) {
		reason := "Persona could not confirm delivery of the request."
		if e.Delivery == a2aQueued {
			reason = "The request could not be delivered within ten minutes of becoming active. " + a2aWaitingReason(*e)
		}
		t.fail(reason, true)
	} else {
		e.Delivery = a2aExpired
	}
	keep, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return true, s.a2aSave(keep, st, t)
}

func (t *a2aTask) fail(message string, wake bool) {
	for i := range t.Events {
		switch t.Events[i].Delivery {
		case a2aQueued:
			t.Events[i].Delivery = a2aDiscarded
		case a2aSending, a2aUncertain:
			t.Events[i].Delivery = a2aExpired
		}
	}
	if t.Launch != nil {
		t.Launch.Pending = false
	}
	e := a2aEvent{ID: randomHex(16), Kind: "status", State: a2aFailed, Text: message}
	if wake {
		e = a2aReturn(t, e)
	}
	t.append(e)
}

// A text-only parent can answer the latest question in the turn that received
// it. The event ID makes recovery idempotent across Manager restarts.
func (s *Service) a2aObserveAnswer(ctx context.Context, st a2aStore, t *a2aTask) error {
	idx := len(t.Events) - 1
	for idx >= 0 && t.Events[idx].State != a2aInput {
		idx--
	}
	if idx < 0 || t.Events[idx].Delivery != a2aDelivered {
		return nil
	}
	sess, ok, err := s.sessionNow(ctx, t.SourceMachine, t.SourceSession)
	if err != nil || !ok || !a2aIdle(sess) {
		return nil
	}
	question := t.Events[idx]
	items, err := s.a2aItems(ctx, t.source())
	if err != nil {
		return nil
	}
	turn := a2aTurn(items, a2aDeliveryID(t.ID, question.Seq))
	if !turn.echo || turn.interrupted || !turn.current && !turn.closed {
		return nil
	}
	answer, ok := a2aReplyBody(turn.reply, "A2A "+t.ID+" answer")
	if !ok || s.a2aCanSendMessage(ctx, t.source(), t.target(), answer) != nil {
		return nil
	}
	changed, err := a2aContinue(t, fmt.Sprintf("persona-answer-%d", question.Seq), answer)
	if err != nil || !changed {
		return err
	}
	return s.a2aSave(ctx, st, t)
}

func (s *Service) a2aDeliver(ctx context.Context, st a2aStore, t *a2aTask, i int, to, from a2aRef) (bool, error) {
	e := &t.Events[i]
	id := a2aDeliveryID(t.ID, e.Seq)
	if e.Delivery != a2aQueued {
		if items, err := s.a2aItems(ctx, to); err == nil && a2aTurn(items, id).echo {
			return true, s.a2aReceipt(ctx, st, t.ID, e.Seq, a2aDelivered)
		}
		var status proto.SessionEchoStatus
		if err := s.call(ctx, to.Machine, proto.MethodSessionEchoStatus, map[string]string{"session_id": to.Session, "client_msg_id": id}, &status); err == nil {
			if status.State == proto.EchoSeen {
				return true, s.a2aReceipt(ctx, st, t.ID, e.Seq, a2aDelivered)
			}
		}
		if changed, err := s.a2aExpireDelivery(ctx, st, t, i); changed || err != nil {
			return changed, err
		}
		if e.Delivery == a2aSending && time.Since(e.Attempted) > 15*time.Second {
			return true, s.a2aReceipt(ctx, st, t.ID, e.Seq, a2aUncertain)
		}
		return false, nil
	}
	sess, ok, err := s.sessionNow(ctx, to.Machine, to.Session)
	if err != nil || !ok {
		return s.a2aExpireDelivery(ctx, st, t, i)
	}
	allowSubagent := t.Launch != nil && t.target() == to
	resume := e.Kind != "message" && a2aResumeAllowed(sess, allowSubagent)
	if !resume && (!a2aTaskSendable(sess, t, to) || !a2aIdle(sess)) {
		return s.a2aExpireDelivery(ctx, st, t, i)
	}
	if device, remote := strings.CutPrefix(t.Owner, "device:"); remote && e.Kind == "message" {
		revoked, err := s.hub.CredentialRevoked(ctx, device)
		if err != nil {
			return false, err
		}
		if revoked {
			e.Delivery = a2aDiscarded
			t.append(a2aEvent{ID: randomHex(16), Kind: "status", State: a2aCanceled})
			return true, s.a2aSave(ctx, st, t)
		}
	}
	e.Delivery, e.Attempted = a2aSending, time.Now().UTC()
	if err := s.a2aSave(ctx, st, t); err != nil {
		return false, err
	}
	execMachine := s.logicalExecutionMachine(ctx, to.Machine, to.Session)
	text := a2aPrompt(t, e, from, string(s.lang(ctx)), s.a2aSessionExecutable(ctx, execMachine, sess))
	params := map[string]any{
		"session_id": to.Session, "text": text, "client_msg_id": id, "message_source": a2aMessageSource(from),
	}
	if resume {
		if sess.Capabilities.A2AWakeOnSend {
			params["a2a_wake_on_send"] = true
			return true, s.a2aSendNode(ctx, st, t.ID, e.Seq, to, "session.a2a.send", params)
		}
		taskID, seq := t.ID, e.Seq
		go func() {
			ctx, cancel := context.WithTimeout(s.runCtx(), agentStartTimeout)
			defer cancel()
			if err := s.a2aSendNode(ctx, st, taskID, seq, to, "session.a2a.wake", params); err != nil {
				s.logf("A2A wake %s: %v", taskID, err)
			}
			s.a2aSignal()
		}()
		return true, nil
	}
	return true, s.a2aSendNode(ctx, st, t.ID, e.Seq, to, "session.a2a.send", params)
}

func a2aResumeAllowed(sess proto.Session, allowSubagent bool) bool {
	visible := a2aDiscoverable(sess)
	if allowSubagent {
		visible = a2aEndpointVisible(sess)
	}
	if sess.Status != proto.StatusOffline || !visible || sess.Capabilities == nil {
		return false
	}
	if a2aStopped(sess) {
		return false
	}
	return sess.Capabilities.Send && sess.Capabilities.A2AWakeOnSend ||
		sess.Capabilities.StopCause == proto.StopIdle || sess.Capabilities.StopCause == proto.StopRestart
}

func (s *Service) a2aSendNode(ctx context.Context, st a2aStore, taskID string, seq int, to a2aRef, method string, params map[string]any) error {
	var out struct {
		Queued  bool
		Session proto.Session
	}
	err := s.call(ctx, to.Machine, method, params, &out)
	receipt, reason := a2aDelivered, ""
	switch {
	case err != nil && a2aNoDelivery(err):
		receipt = a2aQueued
		reason = "The node could not accept the request; waiting for a live, idle session."
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == proto.CodeAgentStopped {
			reason = "The receiving session was stopped and cannot accept the request."
		}
	case err != nil:
		receipt = a2aUncertain
	case out.Queued:
		receipt = a2aSending
	case method == "session.a2a.wake" && (out.Session.SessionID != to.Session || out.Session.StartError != ""):
		receipt = a2aUncertain
	}
	keep, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return s.a2aReceipt(keep, st, taskID, seq, receipt, reason)
}

func a2aMessageSource(from a2aRef) *proto.MessageSource {
	fromName := from.Machine + "/" + from.Session
	if from.Session == "" {
		fromName = "A2A client"
	}
	return &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: from.Session, Title: fromName}
}

func a2aPrompt(t *a2aTask, e *a2aEvent, from a2aRef, language, executable string) string {
	source := a2aMessageSource(from)
	state := e.State
	if state == "" {
		state = t.State
	}
	text := prompts.RenderA2AMessage(prompts.A2AMessage{ID: t.ID, Sender: source.Title, State: state,
		Request: e.Kind == "message", Text: e.Text, Language: language, Executable: executable})
	return prompts.RelayMessageText(text, source, a2aDeliveryID(t.ID, e.Seq))
}

// a2aSessionExecutable returns the recipient node's JSON-encoded executable when sess calls Persona's tools from a
// terminal the node can verify (a pane or a Codex thread) and has no Persona MCP tools, using task handoff's
// classification (#9846, #13893). Assistant sessions, Persona MCP integrations, and runtimes stay empty.
func (s *Service) a2aSessionExecutable(ctx context.Context, machine string, sess proto.Session) string {
	if sess.Assistant {
		return ""
	}
	entry := s.adoptedTaskEntry(ctx, machine, sess)
	if !entry.Terminal() {
		return ""
	}
	return entry.Executable
}

// a2aLaunchExecutable returns the target node's JSON-encoded executable for the first prompt of an Agent launched by
// A2A or dispatch_start, unless the launch runs on a runtime or its launcher is known to run Claude Code (which Persona
// starts with its MCP tools) (#13893).
func (s *Service) a2aLaunchExecutable(ctx context.Context, dev Device, params map[string]any) string {
	asked, _ := params["host"].(string)
	if agentStartHost(dev, asked) != "" {
		return ""
	}
	launcher, _ := params["launcher"].(string)
	launcher = strings.TrimSpace(launcher)
	if launcher == "" || launcher == "claude" || proto.IsExternalLauncher(launcher) {
		return ""
	}
	if m, ok := s.managedNamed(ctx, launcher); ok {
		if m.AgentType() == "claude" || m.AgentType() == "agy" {
			return ""
		}
		return s.nodeExecutable(ctx, dev.ID)
	}
	if fit, ok := s.a2aLaunchAgent(ctx, dev.ID, launcher); ok {
		if !isTerminalLauncherSource(fit.source, fit.agent) || fit.agent == "claude" {
			return ""
		}
		return s.nodeExecutable(ctx, dev.ID)
	}
	if a2aKnownRuntimeLauncher(launcher) {
		return ""
	}
	return s.nodeExecutable(ctx, dev.ID)
}

func (s *Service) a2aLaunchAgent(ctx context.Context, machine, launcher string) (startAgent, bool) {
	normalize := func(fit startAgent) startAgent {
		if fit.snapshot == "" && fit.agent == "claude" && (proto.IsWorkAgentType(launcher) || proto.IsNoModeAgentType(launcher)) {
			fit.agent = launcher
		}
		return fit
	}
	lctx, cancel := context.WithTimeout(withUnhidden(ctx, launcher), min(agentStartListTimeout, time.Second))
	defer cancel()
	if list, known := s.launchersHere(lctx, machine); known {
		discovered := make([]ConfiguredLauncher, 0, len(list))
		for _, l := range list {
			discovered = append(discovered, ConfiguredLauncher{Launcher: l})
		}
		overrides := s.hello(ctx).Models.Launchers[machine]
		if fit := configuredStartAgentLookup(discovered, overrides)(launcher); fit != (startAgent{}) {
			return normalize(fit), true
		}
	}
	if fit := s.lastSeenAgent(ctx, machine, launcher); fit != (startAgent{}) {
		return normalize(fit), true
	}
	if l, ok := s.configuredLauncher(ctx, machine, launcher); ok && l.Source != proto.ExternalLauncherSource {
		fit := launcherStartAgent(l)
		if fit.agent == "" {
			fit.agent = cmp.Or(l.Agent, "claude")
			if l.Agent == "" && (launcher == "codex" || proto.IsWorkAgentType(launcher) || proto.IsNoModeAgentType(launcher)) {
				fit.agent = launcher
			}
		}
		return fit, true
	}
	return startAgent{}, false
}

func a2aKnownRuntimeLauncher(name string) bool {
	if proto.RuntimeEngine(name) {
		return true
	}
	switch name {
	case "dsh", "grok", "opencode":
		return true
	}
	return false
}

// Reserve a launch before handing it to a node. An ambiguous startup stays
// discoverable by its reserved session ID and is never started a second time.
func (s *Service) a2aLaunch(ctx context.Context, st a2aStore, t *a2aTask) error {
	e := &t.Events[0]
	if e.Delivery != a2aQueued {
		if e.Delivery == a2aSending && time.Since(e.Attempted) <= agentStartTimeout {
			return nil
		}
		if sess, ok, _ := s.sessionNow(ctx, t.TargetMachine, t.TargetSession); ok {
			return s.a2aLaunched(ctx, st, t, sess, sess.StartError != "", sess.StartError)
		}
		if time.Since(e.Attempted) > agentStartTimeout+time.Minute {
			return s.a2aLaunched(ctx, st, t, proto.Session{}, true, "") // no answer and no session: unknown
		}
		return nil
	}
	if !t.Launch.Dispatch && !s.a2aConfig(ctx).LaunchAllowed() {
		t.fail("Starting new A2A subagent sessions is disabled by configuration.", true)
		s.forgetA2ASubagent(t.ID)
		return s.a2aSave(ctx, st, t)
	}
	dev, ok := s.machineByName(ctx, t.TargetMachine)
	if !ok || !dev.Online && !dev.Reachable {
		return nil
	}
	dev.ID = cmp.Or(dev.ID, t.TargetMachine)
	e.Delivery, e.Attempted = a2aSending, time.Now().UTC()
	if err := s.a2aSave(ctx, st, t); err != nil {
		return err
	}
	taskID, mid, sid := t.ID, t.TargetMachine, t.TargetSession
	permMode := cmp.Or(t.Launch.PermissionMode, "plan")
	taskSnap := a2aTask{A2ARow: db.A2ARow{ID: t.ID, State: t.State}}
	first := *e
	from := t.source()
	lang := string(s.lang(ctx))
	request := map[string]any{"cwd": t.Launch.CWD, "launcher": t.Launch.Launcher, "session_id": sid,
		"client_msg_id":  a2aDeliveryID(taskID, 1),
		"message_source": a2aMessageSource(from), "permission_mode": permMode}
	t.Launch.Run.lay(request) // a tier's launcher, model, effort and speed; never its permission mode (agent_presets.go)
	go func() {
		ctx, cancel := context.WithTimeout(s.runCtx(), agentStartTimeout)
		defer cancel()
		var out struct{ Session proto.Session }
		err := s.applyModel(ctx, "agent.new", request)
		companion := false
		if err == nil {
			// Where it starts is decided as it goes out, with what the computer runs now (start_host.go): an agy
			// launcher on its companion, and a read-only start a pane cannot hold refused here, not by the node.
			companion, err = s.agentStartedHost(ctx, dev, request)
		}
		definite := err != nil
		if err == nil {
			request["prompt"] = a2aPrompt(&taskSnap, &first, from, lang, s.a2aLaunchExecutable(ctx, dev, request))
			start := func() error {
				return s.call(context.WithValue(ctx, placementTaskKey{}, taskID), mid, DeviceSessionStart, request, &out)
			}
			err = start()
			if companionAway(err, companion) && request["permission_mode"] != "plan" {
				// The companion went away before the start reached it: nothing ran, and a writable start opens in the
				// default host. A read-only one fails with the node's reason: no pane holds it read-only. The first
				// prompt may name the terminal executable, which depends on the host, so it is written again.
				delete(request, "host")
				request["prompt"] = a2aPrompt(&taskSnap, &first, from, lang, s.a2aLaunchExecutable(ctx, dev, request))
				s.logf("A2A launch %s: the Antigravity companion is not connected on %s; starting in its default host", taskID, mid)
				err = start()
			}
			definite = err != nil && !startResultUnknown(err) && !sendUnknown(err)
		}
		keep, stop := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer stop()
		if out.Session.SessionID == "" && !definite {
			_ = s.a2aReceipt(keep, st, taskID, 1, a2aUncertain)
			return
		}
		why := ""
		if definite {
			why = err.Error()
		}
		t, readErr := decodeA2A(st.A2AGet(keep, taskID))
		if readErr == nil && t != nil && t.Launch.Pending && !a2aTerminal(t.State) {
			if err := s.a2aLaunched(keep, st, t, out.Session, definite, why); err != nil {
				s.logf("A2A launch %s: %v", taskID, err)
			}
		}
		s.a2aSignal()
	}()
	return nil
}

// a2aLaunchCheck refuses a new a2a_send launch on computer to.Machine before anything is asked or recorded when it
// would start read-only where its launcher cannot be held read-only (agentStartedHost, on the start as a2aLaunch will
// send it). A launch a2aStart refuses anyway is not looked at, and neither is one whose launcher Model configuration
// cannot give now: a2aLaunch records why that one did not start. A retry by message_id is not checked again:
// a2aStart answers it.
func (s *Service) a2aLaunchCheck(ctx context.Context, cfg assistantwire.A2AConfig, to a2aRef, contextID string, launch *a2aLaunch) error {
	dev, ok := s.machineByName(ctx, to.Machine)
	if to.Session != "" || contextID != "" || !ok || dev.ID != to.Machine || !s.a2aMachineAllowed(ctx, cfg, to.Machine) ||
		!cfg.LaunchAllowed() {
		return nil // a2aStart refuses it
	}
	probe := map[string]any{"launcher": launch.Launcher, "permission_mode": cmp.Or(strings.TrimSpace(launch.PermissionMode), "plan")}
	launch.Run.lay(probe) // the tier's launcher and model, as the start lays them on its own request
	if s.applyModel(ctx, "agent.new", probe) != nil {
		return nil
	}
	_, err := s.agentStartedHost(ctx, dev, probe)
	return err
}

// a2aNotStarted is the failed status of a launch that did not start: why, when that is known (the node's or the
// manager's refusal, or the error its launcher stopped with), so the calling Agent can start it another way. Only a
// start whose outcome is unknown says that Persona could not confirm it.
func a2aNotStarted(why string) string {
	if why = oneLine(why); why == "" {
		return "Persona could not confirm the Agent started."
	}
	return "The Agent did not start: " + clip(why, 1000)
}

// a2aLaunched records how t's launch ended: session started, or the launch failed. why is what a definite failure
// gives as its reason (a2aNotStarted), "" for a start whose outcome is unknown.
func (s *Service) a2aLaunched(ctx context.Context, st a2aStore, t *a2aTask, session proto.Session, failed bool, why string) error {
	t.Launch.Pending = false
	if failed {
		t.Events[0].Delivery = a2aDiscarded
		t.append(a2aReturn(t, a2aEvent{ID: randomHex(16), Kind: "status", State: a2aFailed, Text: a2aNotStarted(why)}))
	} else {
		t.TargetSession = session.SessionID
		if t.Launch.Branch != "" {
			t.Launch.Worktree = session.Cwd
		}
		// The first prompt may still be queued. Observe its exact message
		// before treating the child as running.
		if t.Events[0].Delivery != a2aDelivered {
			t.Events[0].Delivery = a2aUncertain
		}
	}
	if err := s.a2aSave(ctx, st, t); err != nil {
		return err
	}
	if failed {
		s.forgetA2ASubagent(t.ID)
	} else if t.TargetSession != "" {
		s.recordA2ASubagent(t)
		if s.fleetOn() {
			go s.publishSessions(t.TargetMachine)
		}
	}
	return nil
}

func a2aNoDelivery(err error) bool {
	if errors.Is(err, apierr.ErrOffline) || errors.Is(err, ErrNoDevice) || errors.Is(err, rpc.ErrNotSent) {
		return true
	}
	var re *rpc.Error
	if !errors.As(err, &re) {
		return false
	}
	return nodeDelivered(re) == "none" || re.Code == devicewire.CodeMachineOffline || re.Code == devicewire.CodeNoDevice ||
		re.Code == "a2a_session_busy" || re.Code == proto.CodeAgentStopped
}

func a2aIdle(sess proto.Session) bool {
	return !sess.Starting && len(sess.Queued) == 0 && (sess.Status == proto.StatusIdle || sess.Status == proto.StatusDone)
}

func (s *Service) a2aReceipt(ctx context.Context, st a2aStore, id string, seq int, receipt string, reasons ...string) error {
	for range 5 {
		t, err := decodeA2A(st.A2AGet(ctx, id))
		if err != nil || t == nil {
			return err
		}
		e := &t.Events[seq-1]
		if !a2aActiveDelivery(*e) || e.Delivery == receipt {
			return nil
		}
		e.Delivery = receipt
		if len(reasons) != 0 {
			e.DeliveryReason = reasons[0]
		}
		if receipt == a2aQueued {
			if changed, err := s.a2aExpireDelivery(ctx, st, t, seq-1); changed || err != nil {
				if errors.Is(err, errA2AConflict) {
					continue
				}
				return err
			}
		}
		if receipt == a2aDelivered && e.Kind == "message" && t.State == a2aSubmitted {
			t.append(a2aEvent{ID: randomHex(16), Kind: "status", State: a2aWorking})
		}
		if err := s.a2aSave(ctx, st, t); !errors.Is(err, errA2AConflict) {
			return err
		}
	}
	return errA2AConflict
}

func (s *Service) a2aObserve(ctx context.Context, st a2aStore, t *a2aTask) error {
	if t.State != a2aWorking && t.State != a2aAuth {
		return nil
	}
	idx := len(t.Events) - 1
	for idx >= 0 && t.Events[idx].Kind != "message" {
		idx--
	}
	if idx < 0 {
		return nil
	}
	message := t.Events[idx]
	sess, ok, err := s.sessionNow(ctx, t.TargetMachine, t.TargetSession)
	if err != nil || !ok {
		return nil
	}
	items, err := s.a2aItems(ctx, t.target())
	if err != nil {
		return nil
	}
	turn := a2aTurn(items, a2aDeliveryID(t.ID, message.Seq))
	var state, text string
	switch {
	case (!turn.echo || !turn.closed && (!turn.current || turn.reply == "")) &&
		a2aIdle(sess) && time.Since(message.Attempted) > a2aConfirmFor:
		state, text = a2aFailed, "Persona could not find the receiving session's finished task turn."
	case !turn.echo:
		return nil
	case len(t.Events) >= a2aMaxEvents-1:
		state, text = a2aFailed, "This task reached its event limit."
	case turn.interrupted:
		state, text = a2aFailed, "The receiving session was interrupted."
	case turn.closed && !turn.current && turn.reply != "":
		state, text = a2aFinalReply(t.ID, turn.reply)
	case !turn.current:
		return nil
	case sess.Status == proto.StatusBlocked && t.State == a2aWorking:
		state, text = a2aAuth, "The receiving session is waiting for an interaction in Persona."
	case sess.Status == proto.StatusInterrupted:
		state, text = a2aFailed, "The receiving session was interrupted."
	case sess.Status == proto.StatusOffline && a2aStopped(sess):
		state, text = a2aFailed, "The receiving session stopped before finishing the task."
	case a2aIdle(sess) && sess.FailureReason != "":
		state, text = a2aFailed, "The receiving session reported an execution failure."
	case a2aIdle(sess) && turn.reply != "":
		state, text = a2aFinalReply(t.ID, turn.reply)
	case a2aIdle(sess) && turn.closed:
		state, text = a2aFailed, "The receiving session ended its turn without a result."
	case sess.Status == proto.StatusWorking && t.State == a2aAuth:
		state, text = a2aWorking, "The receiving session resumed work."
	default:
		return nil
	}
	t.append(a2aReturn(t, a2aEvent{ID: randomHex(16), Kind: "status", State: state, Text: text}))
	return s.a2aSave(ctx, st, t)
}

// Text-only harnesses can return a question or failure in the exact turn
// that received the task. A marker from any other turn is ordinary text.
func a2aFinalReply(taskID, reply string) (string, string) {
	for _, state := range []string{a2aInput, a2aFailed} {
		if body, ok := a2aReplyBody(reply, "A2A "+taskID+" "+state); ok {
			return state, body
		}
	}
	return a2aComplete, reply
}

func a2aReplyBody(reply, marker string) (string, bool) {
	head, body, hasBody := strings.Cut(reply, "\n")
	body = strings.TrimSpace(body)
	return body, hasBody && head == marker && body != ""
}

func a2aStopped(sess proto.Session) bool {
	if sess.Capabilities == nil {
		return false
	}
	switch sess.Capabilities.StopCause {
	case proto.StopUser, proto.StopExit, proto.StopResumeFailed, proto.StopCompactFailed:
		return true
	}
	return false
}

func a2aWaitingReason(e a2aEvent) string {
	if e.DeliveryReason != "" {
		return e.DeliveryReason
	}
	return "Waiting for the receiving session to become live and idle."
}
