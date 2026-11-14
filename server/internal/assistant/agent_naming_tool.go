package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// agent_autoname is the assistant's narrow path to the user-requested naming in agent_naming_request.go. The caller
// has already passed mcpCall's assistant-only source check; tool arguments cannot make it an assistant or choose a
// credential, URL or arbitrary request. A full machine id and exact session id prevent a prefix from naming another
// session. Like archive-and-close, replacing a hand-written title is confirmed on the phone on every call. There is
// no standing permission: declining or not answering changes nothing and makes no model call. The iOS app calls the
// HTTP endpoint instead; it does not depend on this tool.
func (s *Service) toolAgentAutoname(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a struct {
		Machine   string `json:"machine"`
		SessionID string `json:"session_id"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("agent_autoname arguments: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", errors.New("agent_autoname expects one JSON object")
	}
	a.Machine, a.SessionID = strings.TrimSpace(a.Machine), strings.TrimSpace(a.SessionID)
	am, c, err := s.findAgent(ctx, a.Machine, a.SessionID)
	if err != nil {
		return "", err
	}
	if a.Machine == "" || a.SessionID != c.SessionID {
		return "", errors.New("agent_autoname requires the full machine and session ids from agents_list; nothing was renamed")
	}
	if c.Assistant || s.isMain(ctx, c.SessionID) {
		return "", errors.New("that is a Persona conversation, not a coding agent: agent_autoname does not rename it")
	}
	l := s.lang(ctx)
	now := time.Now().UTC()
	ix := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self, Kind: proto.IxPermission, ToolName: "agent_autoname",
		Summary: i18n.T(l, "assistant-tools.autoname_card", i18n.Args{"agent": agentLabel(c), "machine": am.Name}), ToolInput: args,
		SessionTitle: "Persona", CreatedAt: now, ExpiresAt: now.Add(15 * time.Minute)}
	if conv, ok := s.session(ctx, machine, self); ok {
		ix.Cwd = conv.Cwd
	}
	r, err := s.ask(ctx, ix)
	if errors.Is(err, errUserSpoke) {
		return "The user wrote in the conversation instead of confirming: nothing was renamed.", nil
	}
	if err != nil {
		return "", err
	}
	if r.Allow == nil || !*r.Allow {
		return "The user declined on the phone (or did not answer): nothing was renamed.", nil
	}
	res, err := s.requestAgentName(ctx, am.ID, c.SessionID)
	if err != nil {
		var refusal *agentNameRefusal
		if errors.As(err, &refusal) {
			code := refusal.code
			if code == "" {
				code = "not_found"
			}
			out, _ := json.Marshal(map[string]any{"ok": false, "code": code, "retryable": refusal.retry, "error": i18n.Localize(refusal.err, l)})
			return string(out), nil // the same coded refusal as the HTTP response, without a provider error
		}
		return "", errors.New("agent_autoname failed without changing the title; no error details are available")
	}
	out, _ := json.Marshal(map[string]any{"ok": true, "title": res.Title, "title_source": "auto", "changed": res.Changed})
	return string(out), nil
}
