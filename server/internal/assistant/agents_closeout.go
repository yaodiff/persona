package assistant

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A closeout is what an agent says of its own work as a turn ends (docs/assistant/agent-work.md "Closeout"): done or
// not, what changed, merged and deployed or not, what was verified, what is missing. The node holds a stop once to
// get one (node/closeout_stop.go) when the manager says so here; the triage reads it before it asks a model.

// closeoutCheck answers a node whose agent is about to stop, after a turn that did work, without a closeout: the
// reason to hold the stop with, or none. Only under the triage's main switch and its closeout switch, and never for
// an assistant conversation or a review's reviewer.
func (s *Service) closeoutCheck(ctx context.Context, machine string, p assistantwire.CloseoutCheckRequest) assistantwire.CloseoutCheckResponse {
	var out assistantwire.CloseoutCheckResponse
	if sw := s.triageSwitches(ctx); !sw.Enabled || !sw.Closeout {
		return out
	}
	if p.SessionID == "" || s.isReviewer(machine, p.SessionID) {
		return out
	}
	if c, ok := s.session(ctx, machine, p.SessionID); ok && c.Assistant {
		return out
	}
	language := answerLanguage(s.hello(ctx).Locale)
	out.Reason = renderCloseoutRequired(language, s.closeoutForm(machine, language))
	s.workEvent(machine, p.SessionID, "closeout_asked", nil)
	return out
}

func renderCloseoutRequired(language, form string) string {
	return prompts.RenderAgentCloseoutRequired(prompts.AgentCloseoutRequired{Form: form, Language: language})
}

// closeoutDone is the work state of a turn whose closeout says done: no model is asked. The line is the first thing
// it says it did.
func closeoutDone(c proto.Closeout, artifacts []proto.WorkArtifact, lang i18n.Lang) *proto.WorkState {
	w := &proto.WorkState{Phase: proto.WorkDone, Closeout: &c, Artifacts: workArtifacts(artifacts), By: "rule"}
	if len(c.Did) > 0 {
		w.Line = clip(oneLine(c.Did[0]), 60)
	}
	if w.Line == "" {
		w.Line = i18n.T(lang, "assistant-agents_triage.done")
	}
	return w
}

// closeoutAsksOwner identifies a done report with an outstanding owner decision (#14899).
// Delivery holds reach the owner through delivery.go.
func closeoutAsksOwner(c proto.Closeout) bool {
	return c.Status == proto.CloseoutDone && c.Unblock == unblockUser && (c.Waiting != "" || c.Missing != "") &&
		(c.Delivery == "" || c.Delivery == deliveryNone)
}

// closeoutWaitsOnPerson: the closeout says its turn stopped on a person, the owner or someone else, not on work of its
// own still running (#14899).
func closeoutWaitsOnPerson(c proto.Closeout) bool {
	switch c.Unblock {
	case unblockUser:
		return c.Status != proto.CloseoutDone || closeoutAsksOwner(c)
	case unblockOther:
		return c.Status == proto.CloseoutBlocked || c.Status == proto.CloseoutNotDone
	}
	return false
}

// closeoutWork uses the report's status, responsible party and dependency to settle the turn.
func closeoutWork(c proto.Closeout, artifacts []proto.WorkArtifact, lang i18n.Lang) *proto.WorkState {
	disproved := closeoutDisproved(c)
	if !disproved && c.Status == proto.CloseoutDone && !closeoutAsksOwner(c) {
		return closeoutDone(c, artifacts, lang)
	}
	w := &proto.WorkState{Closeout: &c, Artifacts: workArtifacts(artifacts), By: "rule"}
	if disproved {
		return unfinished(w, lang)
	}
	var wait string
	switch {
	case closeoutWaitsOnPerson(c):
		wait = cmpOr(c.Waiting, c.Missing)
	case c.Status == proto.CloseoutBlocked:
		wait = c.Waiting
	}
	if wait == "" {
		return unfinished(w, lang)
	}
	w.Phase, w.Line = proto.WorkBlocked, clip(wait, 60)
	if c.Unblock == unblockUser {
		w.On = proto.WaitUser
		w.Questions = []proto.Question{{Question: wait, Options: []proto.QuestionOption{
			{Label: answerLabel(lang, answerOK)}, {Label: answerLabel(lang, answerSkip)},
		}}}
	} else {
		w.On, w.Wait = proto.WaitExternal, clip(wait, 80)
	}
	return w
}

// workArtifacts keeps the kinds a work state lists (a closeout may also name a branch).
func workArtifacts(in []proto.WorkArtifact) []proto.WorkArtifact {
	var out []proto.WorkArtifact
	for _, a := range in {
		if strings.TrimSpace(a.Ref) != "" {
			out = append(out, a)
		}
	}
	return out
}

// checkCloseout looks at the closeout's pull request itself (deploy_check.go: the pull request's merge, and whether
// the running hub contains it) and keeps what it found on c. Nothing when the closeout names no pull request by its
// link, or nothing could be told.
func (s *Service) checkCloseout(ctx context.Context, c *proto.Closeout, artifacts []proto.WorkArtifact) {
	for _, a := range artifacts {
		if a.Kind != "pr" || ciPRURL.FindString(a.Ref) == "" {
			continue
		}
		c.Checked = closeoutChecked(s.checkPRDeployment(ctx, a.Ref, true))
		return
	}
}

func closeoutChecked(r deployResult) *proto.CloseoutChecked {
	switch r.State {
	case deployIn:
		checked := &proto.CloseoutChecked{Deployed: proto.CloseoutYes, LocalDeployment: r.Receipt.byHead && !strings.EqualFold(r.Receipt.sha, r.Merge)}
		if r.Merge != "" {
			checked.Merged = proto.CloseoutYes
		} else if r.NotMerged {
			checked.Merged = proto.CloseoutNo
		}
		return checked
	case deployOut:
		return &proto.CloseoutChecked{Merged: proto.CloseoutYes, Deployed: proto.CloseoutNo}
	case deployUnmerged:
		return &proto.CloseoutChecked{Merged: proto.CloseoutNo, Deployed: proto.CloseoutNo}
	case deployNotIn:
		checked := &proto.CloseoutChecked{Deployed: proto.CloseoutNo}
		if r.Merge != "" {
			checked.Merged = proto.CloseoutYes
		}
		return checked
	}
	if r.Merge != "" {
		return &proto.CloseoutChecked{Merged: proto.CloseoutYes} // upstream merge was found, deployment could not be checked
	}
	return nil
}

// closeoutDisproved: the closeout says merged or deployed and the hub found it is not.
func closeoutDisproved(c proto.Closeout) bool {
	k := c.Checked
	return k != nil && (c.Merged == proto.CloseoutYes && k.Merged == proto.CloseoutNo ||
		c.Deployed == proto.CloseoutYes && k.Deployed == proto.CloseoutNo)
}

// closeoutDelivery is the delivery a closeout states, for a reply with no delivery line of its own (delivery.go):
// the same claim in the other form. ok false when the closeout says nothing of merging or deploying and is not done.
func closeoutDelivery(c proto.Closeout, artifacts []proto.WorkArtifact) (d TodoDelivery, ok bool) {
	if c.Delivery == deliveryOff && !hasDefaultOffGate(c.Missing+"\n"+c.Waiting) {
		c.Delivery = ""
		if c.Merged == "" && c.Deployed == "" {
			c.Delivery = deliveryMerged
		}
	}
	switch {
	case c.Delivery == deliveryLive || c.Delivery == deliveryPR || c.Delivery == deliveryMerged || c.Delivery == deliveryOff || c.Delivery == deliveryNone:
		d.State = c.Delivery
	case c.Deployed == proto.CloseoutYes:
		d.State = deliveryLive
	case c.Merged == proto.CloseoutYes:
		d.State = deliveryMerged
	case c.Merged == proto.CloseoutNo && hasPR(artifacts):
		d.State = deliveryPR
	case c.Merged == "" && c.Deployed == "" && c.Status == proto.CloseoutDone:
		d.State = deliveryNone
	default:
		return d, false
	}
	if d.State != deliveryLive && d.State != deliveryNone {
		d.Gate = cmpOr(c.Missing, c.Waiting)
		d.Unblock = c.Unblock
		if d.Unblock == "" && (externalDeliveryWait.MatchString(d.Gate) || externalDeliveryWait.MatchString(c.Waiting)) {
			d.Unblock = unblockOther
		}
	}
	return d, true
}

func hasPR(artifacts []proto.WorkArtifact) bool {
	for _, a := range artifacts {
		if a.Kind == "pr" {
			return true
		}
	}
	return false
}
