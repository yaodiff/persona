package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// ---- Codex cards (docs/integrations/codex.md "Questions and approvals") ----

// A node lists the Codex questions and approvals open on its machine whenever the list changes (codex.interactions,
// node/codexask.go); the manager keeps a card for each — a new one is announced, one no longer listed (answered at
// the computer, the turn interrupted, the agent gone) ends. An answer goes back to the node (codex.answer), which
// types it into the TUI and says whether Codex took it: that error reaches whoever answered.

// codexAnswerTimeout: the node checks the screen, types, and waits for Codex to take the answer (its codexConfirm).
var codexAnswerTimeout = time.Minute

// codexAnnouncedFor: which cards were announced (pushed) lately is remembered this long: a node that reconnects
// lists them again, and the phone should not ring twice for one question.
const codexAnnouncedFor = 24 * time.Hour

// codexAskedAgain: a card the node stopped listing and lists again after this long is Codex asking again (the same
// approval in a later turn has the same id), not a reconnect or a screen read that failed once: it rings the phone
// again (#3766).
var codexAskedAgain = time.Minute

func (s *Service) syncCodexInteractions(machine string, params json.RawMessage) {
	var p struct {
		Interactions []proto.Interaction `json:"interactions"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	listed := map[string]proto.Interaction{}
	lang := s.commandInputLang(p.Interactions)
	for _, ix := range p.Interactions {
		// Display-copy identity is manager-owned at every node-controlled ingress.
		ix.MirrorOf, ix.MirrorMain = "", false
		// A command-input card comes only from this list, in its own shape (fleet_ix_command.go).
		input := proto.IsCommandInputCard(ix)
		if reservedCard(ix) && !input {
			s.logf("[%s] codex card %s: %v", machine, ix.ID, errReservedCard)
			continue
		}
		ix.MachineID = machine
		ix = cleanTitle(ix)
		if input {
			ix = commandInputCard(lang, ix)
		}
		// Node-listed cards do not pass through waitInteractionPrepared, so apply
		// the turn's privacy before either display or direct-chat routing reads it.
		ix.Private = ix.Private || s.justMeNow(ix.MachineID, ix.SessionID)
		// Codex cards do not pass through waitInteractionPrepared. Restore the
		// same pending occurrence's intent after a restart; only a new occurrence
		// uses the current preference. Record it before history and publication.
		if intent, at, found := s.recordedApprovalMirrorIntent(context.Background(), ix); found {
			ix.MirrorMain, ix.CreatedAt = intent, at
		} else if _, ok := s.mainApprovalMirror(context.Background(), ix); ok {
			ix.MirrorMain = true
		}
		listed[ix.ID] = ix
	}
	s.fleet.mu.Lock()
	m := s.ixMapLocked()
	var gone []*pending
	var screens []*pending
	replaced := map[string]bool{}
	for id, pd := range m {
		if !pd.codex || pd.ix.MachineID != machine {
			continue
		}
		if next, ok := listed[id]; ok && sameDurableApprovalOccurrence(pd.ix, next) {
			delete(listed, id) // already this exact occurrence
			continue
		}
		if _, ok := listed[id]; ok {
			replaced[id] = true
		}
		// Either the card is gone or this is a same-ID replacement. Remove the
		// old pointer before the replacement is installed; its finalization below
		// is occurrence-qualified and can never remove the new pending card.
		gone = append(gone, pd)
		delete(m, id)
	}
	var added []proto.Interaction
	for _, ix := range listed {
		if m[ix.ID] != nil {
			continue
		}
		pd := s.codexPending(machine, ix)
		if proto.IsCommandInputCard(ix) {
			pd = s.commandInputPending(machine, ix)
		}
		// A screen card for the same session read the terminal without knowing Codex: an approval or an overlay
		// question card is that dialog read properly. An async question is not on the terminal's keyboard at all.
		for id, old := range m {
			if coversScreen(pd) && old.ix.Kind == proto.IxScreen && old.ix.MachineID == ix.MachineID && old.ix.SessionID == ix.SessionID {
				delete(m, id)
				screens = append(screens, old)
			}
		}
		m[ix.ID] = pd
		added = append(added, pd.ix)
	}
	now := time.Now()
	quiet := map[string]bool{}
	if s.fleet.codexAnnounced == nil {
		s.fleet.codexAnnounced = map[string]time.Time{}
	}
	if s.fleet.codexEnded == nil {
		s.fleet.codexEnded = map[string]time.Time{}
	}
	for id, at := range s.fleet.codexAnnounced {
		if now.Sub(at) > codexAnnouncedFor {
			delete(s.fleet.codexAnnounced, id)
		}
	}
	for id, at := range s.fleet.codexEnded { // on its own: an id no longer announced (handed back) is kept here too
		if now.Sub(at) > codexAnnouncedFor {
			delete(s.fleet.codexEnded, id)
		}
	}
	for _, old := range gone {
		s.fleet.codexEnded[old.ix.ID] = now
	}
	for _, ix := range added {
		_, announced := s.fleet.codexAnnounced[ix.ID]
		ended, wasEnded := s.fleet.codexEnded[ix.ID]
		delete(s.fleet.codexEnded, ix.ID)
		if announced && !replaced[ix.ID] && !(wasEnded && now.Sub(ended) >= codexAskedAgain) {
			quiet[ix.ID] = true
		}
		s.fleet.codexAnnounced[ix.ID] = now
	}
	s.fleet.mu.Unlock()
	for _, old := range screens {
		s.recordEndBy(old.ix.ID, "cancelled", db.EndedByCleared, nil)
		s.finished(old)
	}
	for _, old := range gone {
		by := db.EndedByWithdrawn
		if replaced[old.ix.ID] {
			by = db.EndedByCleared
		}
		if err := s.recordEndOccurrenceStrictBy(old.ix, "cancelled", by, nil); err != nil {
			s.logf("record Codex occurrence outcome: %v", err)
		}
		s.finished(old)
	}
	for _, ix := range added {
		s.record(ix)
		if quiet[ix.ID] {
			s.show(ix)
			go s.larkModule().AskBack(ix, resumeGrace) // listed again by a reconnected node: its Feishu card asks again
		} else {
			s.announce(ix)
		}
	}
}

// codexPending is the manager's side of a Codex card: answering it asks the node. When the node could not type the
// answer (a draft in the box, the TUI elsewhere) the card comes back, still open on the computer; when the node says
// Codex no longer asks it (prompt_changed), the node's next list has what is there now.
func (s *Service) codexPending(machine string, ix proto.Interaction) *pending {
	ix.AlwaysHonored, ix.ToolTitle, ix.ToolNameTitle = nil, "", "" // the manager's word, never a node's (nodeCard)
	ix.NoHandBack = false                                          // Codex TUI cards can always be handed back to the terminal
	p := &pending{ix: ix, codex: true}
	p.local = func(answerCtx context.Context, r proto.Resolution) error {
		if r.Dismiss { // handed back to the computer: the same question asked again later is announced (#3766)
			s.fleet.mu.Lock()
			delete(s.fleet.codexAnnounced, ix.ID)
			delete(s.fleet.codexEnded, ix.ID)
			s.fleet.mu.Unlock()
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(answerCtx), codexAnswerTimeout)
		defer cancel()
		err := s.nodeCall(ctx, machine, "codex.answer", map[string]any{"session_id": ix.SessionID, "id": ix.ID, "created_at": ix.CreatedAt, "resolution": r}, nil)
		var re *rpc.Error
		changed := errors.As(err, &re) && re.Code == screen.PromptChanged
		if (err == nil || changed) && !r.Dismiss {
			// Answered from the phone, or Codex no longer asks it (prompt_changed): either way the card is over and no
			// longer pending, so the node's next list does not count it as gone. It ended now: listed again after
			// codexAskedAgain, it is Codex asking again and rings (#4105).
			ended := time.Now()
			if err == nil && ix.Kind == proto.IxPermission {
				// An approval answered: the node saw its dialog leave the screen before it said done, and lists the
				// same approval only once a screen read shows it again, so that listing is Codex asking again and rings
				// at once (#4585), with no quiet minute.
				ended = ended.Add(-codexAskedAgain)
			}
			s.fleet.mu.Lock()
			current := s.ixMapLocked()[ix.ID]
			if current == nil || sameDurableApprovalOccurrence(current.ix, ix) {
				if s.fleet.codexEnded == nil {
					s.fleet.codexEnded = map[string]time.Time{}
				}
				s.fleet.codexEnded[ix.ID] = ended
			}
			s.fleet.mu.Unlock()
		}
		if err != nil && !r.Dismiss && !changed {
			s.fleet.mu.Lock()
			reopened := false
			if m := s.ixMapLocked(); m[ix.ID] == nil {
				m[ix.ID] = p
				reopened = true
			}
			s.fleet.mu.Unlock()
			if reopened {
				s.show(ix)
			}
		}
		return err
	}
	return p
}
