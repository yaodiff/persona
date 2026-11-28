package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/textfmt"
)

func init() {
	handleTypedBusWithDecoder(devicewire.ReplayHeldStartMethod, decodeReplayHeldStart, (*Hub).busReplayHeldStart)
}

type heldStartReplayKey struct{}

// heldStartReplay tracks a replayed held start through startSessionWith so busReplayHeldStart can distinguish
// pre-node refusals from node-level offline, refusal, or mid-flight uncertain outcomes.
type heldStartReplay struct {
	opID       string
	nodeCalled bool
	nodeErr    error
}

func heldStartReplayOf(ctx context.Context) *heldStartReplay {
	r, _ := ctx.Value(heldStartReplayKey{}).(*heldStartReplay)
	return r
}

func (m *Machine) isConnected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.managed {
		return m.connected
	}
	if m.peer == nil {
		return false
	}
	select {
	case <-m.peer.Done():
		return false
	default:
		return true
	}
}

// tryHoldStart asks the machine's manager to hold a new-Agent start while its computer is away (#10674).
// handled is true when tryHoldStart already wrote the HTTP response (202 Accepted or a coded refusal like
// held_start_full); false means the caller should continue the normal online/error path (e.g. machine just came online
// or the manager/store could not hold it).
func (h *Hub) tryHoldStart(ctx context.Context, w http.ResponseWriter, r *http.Request, m *Machine, orig sessionStartRequest, opID string, onlyIfOffline bool) bool {
	owner := h.machineOwner(m.cfg.ID)
	if err := h.waitDevicesFor(ctx, owner); err != nil {
		return false
	}
	rawParams, err := json.Marshal(orig)
	if err != nil {
		return false
	}
	pr := principalOf(r.Context())
	var caller *devicewire.Caller
	if c := devicewire.CallerOf(r.Context()); c.Kind == "device" {
		cp := c
		caller = &cp
	}
	var submission *devicewire.Submission
	if sub, ok := r.Context().Value(trustedSubmissionKey{}).(devicewire.Submission); ok {
		cp := sub
		submission = &cp
	}
	req := devicewire.HoldStartRequest{
		OnlyIfOffline: onlyIfOffline,
		Call: devicewire.HeldStartCall{
			MachineID: m.cfg.ID,
			OpID:      opID,
			Params:    rawParams,
			Meta: devicewire.HeldStartMeta{
				Cwd:         startCwd(m, orig.Cwd),
				Launcher:    orig.Launcher,
				AgentType:   orig.AgentType,
				Model:       strings.TrimSpace(orig.Model),
				Host:        orig.Host,
				TabName:     strings.TrimSpace(orig.TabName),
				TodoID:      orig.TodoID,
				WorkSession: orig.WorkSession,
			},
			Principal: devicewire.HeldStartPrincipal{
				ID:   pr.ID,
				Name: pr.Name,
				Role: pr.Role,
			},
			Caller:     caller,
			Submission: submission,
			Lang:       string(h.requestLang(r)),
		},
	}
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	reply, err := buswire.Call(hctx, h.managerCaller(owner), devicewire.HoldStartMethod, req)
	if err != nil {
		var re *rpc.Error
		if errors.As(err, &re) {
			switch re.Code {
			case devicewire.CodeMachineOnline:
				return false // computer reconnected: proceed to live start
			case devicewire.CodeHeldStartFull:
				callErr(w, err)
				return true
			}
		}
		h.logf("startSession %s on %s: could not hold: %v", opID, m.cfg.ID, err)
		return false
	}
	b, _ := json.Marshal(reply)
	writeRaw(w, http.StatusAccepted, append(b, '\n'))
	return true
}

// decodeReplayHeldStart keeps the role check ahead of decoding and the same error for missing or malformed params.
func decodeReplayHeldStart(c *busConn, raw json.RawMessage) (devicewire.ReplayHeldStartRequest, error) {
	var req devicewire.ReplayHeldStartRequest
	if c.ident.Role != bus.RoleManager {
		return req, &rpc.Error{Code: "forbidden", Message: "only managers can replay held starts"}
	}
	if err := json.Unmarshal(raw, &req); err != nil || req.Call.MachineID == "" || req.Call.OpID == "" {
		return req, &rpc.Error{Code: bus.CodeInvalidParams, Message: "call.machine_id and call.op_id are required"}
	}
	return req, nil
}

// busReplayHeldStart replays a held new-Agent start through the hub's real startSessionWith validation and launch path
// once its computer reconnects, or completes post-start todo handoff linkage when the node already created the session
// for Call.OpID (#10674).
func (h *Hub) busReplayHeldStart(ctx context.Context, c *busConn, req devicewire.ReplayHeldStartRequest) (devicewire.ReplayHeldStartReply, error) {
	if err := h.ownsMachine(c, req.Call.MachineID); err != nil {
		return devicewire.ReplayHeldStartReply{}, err
	}
	var p sessionStartRequest
	if err := json.Unmarshal(req.Call.Params, &p); err != nil {
		return devicewire.ReplayHeldStartReply{
			Outcome: devicewire.ReplayOutcomeRefused,
			Reason:  "invalid start request",
		}, nil
	}
	p.MachineID = req.Call.MachineID // trust boundary: always the owning manager's machine

	if req.ReconciledSessionID != "" {
		h.finalizeReconciledHeldStart(ctx, req.Call.MachineID, req.ReconciledSessionID, p)
		return devicewire.ReplayHeldStartReply{
			Outcome:   devicewire.ReplayOutcomeStarted,
			SessionID: req.ReconciledSessionID,
		}, nil
	}

	credID := req.Call.Principal.ID
	if req.Call.Caller != nil && req.Call.Caller.CredentialID != "" {
		credID = req.Call.Caller.CredentialID
	}
	if credID != "" && h.isDeviceRevoked(credID) {
		return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeSignedOut}, nil
	}

	lang := i18n.Lang(req.Call.Lang).Or(i18n.Default)
	tracker := &heldStartReplay{opID: req.Call.OpID}
	rctx := context.WithValue(ctx, principalKey{}, principal{
		ID:   req.Call.Principal.ID,
		Name: req.Call.Principal.Name,
		Role: cmp.Or(req.Call.Principal.Role, roleFull),
	})
	if req.Call.Caller != nil {
		rctx = devicewire.WithCaller(rctx, *req.Call.Caller)
	}
	if req.Call.Submission != nil {
		rctx = context.WithValue(rctx, trustedSubmissionKey{}, *req.Call.Submission)
	}
	rctx = i18n.With(rctx, lang)
	rctx = context.WithValue(rctx, heldStartReplayKey{}, tracker)

	synReq, err := http.NewRequestWithContext(rctx, http.MethodPost, "/api/v1/sessions", nil)
	if err != nil {
		return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeRefused, Reason: err.Error()}, nil
	}
	synReq.Header.Set("X-Persona-Op-Id", req.Call.OpID)
	rec := &bufferedResponse{header: http.Header{}, lang: lang}
	h.startSessionWith(rec, synReq, p, false, false) // a held start played again is the owner's own; no "via" mark is kept with it

	body := rec.body.Bytes()
	status := rec.status
	if status == 0 && len(body) > 0 {
		status = http.StatusOK // writeJSON writes directly without calling WriteHeader(200)
	}
	if status >= 200 && status < 300 {
		var res struct {
			Session proto.Session `json:"session"`
		}
		_ = json.Unmarshal(body, &res)
		return devicewire.ReplayHeldStartReply{
			Outcome:   devicewire.ReplayOutcomeStarted,
			SessionID: res.Session.SessionID,
		}, nil
	}
	if !tracker.nodeCalled {
		code := errorCodeOf(body)
		if status == http.StatusServiceUnavailable && (code == "assistant_offline" || code == "node_unreachable" || code == "") {
			return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeNotSent}, nil
		}
		return devicewire.ReplayHeldStartReply{
			Outcome: devicewire.ReplayOutcomeRefused,
			Reason:  heldStartErrorText(body),
		}, nil
	}
	return classifyHeldStartNodeErr(tracker.nodeErr, body), nil
}

func classifyHeldStartNodeErr(err error, body []byte) devicewire.ReplayHeldStartReply {
	if err == nil {
		return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeUnclear}
	}
	if errors.Is(err, errOffline) {
		return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeNotSent}
	}
	if errors.Is(err, rpc.ErrDropped) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) || errors.Is(err, errManagerOffline) {
		return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeUnclear}
	}
	var re *rpc.Error
	if !errors.As(err, &re) {
		return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeUnclear}
	}
	if re.Code == devicewire.CodeNodeClosed || re.Code == devicewire.CodeDeadline {
		return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeUnclear}
	}
	var data struct {
		Delivered string `json:"delivered"`
	}
	_ = json.Unmarshal(re.Data, &data)
	if data.Delivered == sendUnknown {
		return devicewire.ReplayHeldStartReply{Outcome: devicewire.ReplayOutcomeUnclear}
	}
	return devicewire.ReplayHeldStartReply{
		Outcome: devicewire.ReplayOutcomeRefused,
		Reason:  cmp.Or(heldStartErrorText(body), textfmt.Clip(textfmt.OneLine(cmp.Or(re.Message, re.Code)), 300)),
	}
}

func heldStartErrorText(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && strings.TrimSpace(e.Error) != "" {
		return textfmt.Clip(textfmt.OneLine(e.Error), 300)
	}
	return textfmt.Clip(textfmt.OneLine(apierr.Reason(body)), 300)
}

func (h *Hub) finalizeReconciledHeldStart(ctx context.Context, mid, sid string, p sessionStartRequest) {
	m := h.localMachine(mid)
	if m == nil || p.TodoID == "" {
		return
	}
	cwd := startCwd(m, p.Cwd)
	title, agent := "", p.AgentType
	if s, ok := m.session(sid); ok {
		if s.Cwd != "" {
			cwd = s.Cwd
		}
		title, agent = s.Title, cmp.Or(s.Agent, agent)
	}
	if p.ResumeSessionID == "" && strings.TrimSpace(p.Prompt) == "" && len(p.Images) == 0 {
		h.fresh.add(m.cfg.ID, sid, p.TodoID, cwd)
	}
	nameFrom := p.Prompt
	if strings.TrimSpace(p.NameHint) != "" {
		nameFrom = p.NameHint
	}
	if _, err := h.asst().TodoLink(ctx, assistantwire.TodoLinkParams{
		TodoID:    p.TodoID,
		MachineID: m.cfg.ID,
		SessionID: sid,
		Title:     title,
		Cwd:       cwd,
		Agent:     agent,
		Prompt:    nameFrom,
	}); err != nil {
		h.logf("held start reconcile: link todo %s for %s/%s failed: %v", p.TodoID, m.cfg.ID, sid, err)
	}
}
