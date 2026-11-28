package extmgr

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The auth relay extension point (docs/operations/auth-relay.md): an authorization link one of the user's Agents stopped at is
// handed to the extension whose auth_relay contribution names its host and path, which completes it with the account
// it is signed in with. Where the link came from is the caller's to check (the triage, assistant/agents_auth_relay.go);
// this only finds the extension and holds what it answers to the schema. The link is never logged.

// authRelayTimeout bounds one extension's try: opening the page, judging it, confirming, seeing it done.
const authRelayTimeout = 60 * time.Second

// AuthRelays says whether an extension would be asked to complete link (one names its host and path).
func (h *Manager) AuthRelays(link string) bool {
	return h.extensions != nil && len(h.extensions.reg.AuthRelayers(link)) > 0
}

// CompleteAuth hands link to the extensions whose auth_relay contributions name it, in turn, until one takes it;
// false when none does (none names it, none runs, each said not_handled).
func (h *Manager) CompleteAuth(ctx context.Context, link string) (wire.AuthRelayResult, bool) {
	if h.extensions == nil || !h.ready(ctx) {
		return wire.AuthRelayResult{}, false
	}
	return h.extensions.completeAuth(ctx, link)
}

func (ps *extensions) completeAuth(ctx context.Context, link string) (wire.AuthRelayResult, bool) {
	for _, r := range ps.reg.AuthRelayers(link) {
		hp := ps.get(r.Extension)
		if hp == nil || hp.proc == nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, authRelayTimeout)
		res, err := buswire.Call(cctx, hp.proc, extwire.AuthRelayCompleteMethod,
			wire.AuthRelayCompleteParams{Contribution: r.Contribution, URL: link})
		cancel()
		if err != nil {
			var re *rpc.Error
			if errors.As(err, &re) && (re.Code == extension.CodeUnknownMethod || re.Code == extension.CodeUnsupported ||
				re.Code == extension.CodeNoContribution) {
				continue // an older version of it: not one it completes
			}
			ps.logf("plugin %s: auth_relay.complete %s: failed", r.Extension, r.Contribution) // never the link, nor its words
			return wire.AuthRelayResult{Status: wire.AuthRelayStatusFailed, Reason: wire.AuthRelayReasonUnavailable}, true
		}
		res = heldAuthRelay(res)
		if res.Status == wire.AuthRelayStatusManual && res.Reason == wire.AuthRelayReasonNotHandled {
			continue
		}
		ps.logf("plugin %s: auth_relay.complete %s: %s %s", r.Extension, r.Contribution, res.Status, res.Reason)
		return res, true
	}
	return wire.AuthRelayResult{}, false
}

// heldAuthRelay holds an extension's answer to the schema: a status it does not know is a failure, a reason it does
// not know is none, the app one short line.
func heldAuthRelay(r wire.AuthRelayResult) wire.AuthRelayResult {
	if !slices.Contains(wire.AuthRelayStatuses, r.Status) {
		return wire.AuthRelayResult{Status: wire.AuthRelayStatusFailed}
	}
	if !slices.Contains(wire.AuthRelayReasons, r.Reason) || r.Status == wire.AuthRelayStatusDone {
		r.Reason = ""
	}
	r.App = clip(strings.Join(strings.Fields(r.App), " "), 79) // with its …, the schema's 80
	return r
}
