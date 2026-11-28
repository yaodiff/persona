package node

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

const runtimeInputPrefix = proto.CommandInputPrefix + "rt_"

type runtimeInputKey struct{ session, task string }
type runtimeInputReport struct {
	proto.CommandInput
	WaitID  string `json:"wait_id"`
	binding string
}

// Only the peer selected for a node-directed start receives the random proof.
type runtimeInputBinding struct {
	runtime, proof string
	confirmed      bool
	seen           time.Time
	// Keep delivery decisions for as long as this binding can authorize the same wait.
	claims map[runtimeInputOccurrence]runtimeInputClaim
}
type runtimeInputOccurrence struct {
	task, wait string
}
type runtimeInputClaim struct {
	inFlight bool
	retryOn  int // ambiguous delivery: only a deliberate retry on the same connection
}
type runtimeInputWait struct {
	source     *runtimeInputBinding
	occurrence runtimeInputOccurrence
	display    proto.CommandInput
	card       proto.Interaction
}

func (b *runtimeRegistry) beginInputStart(h *runtimeConn, session string) *runtimeInputBinding {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h.superseded || b.owner[session] != nil || b.known[session].runtime != "" || b.inputBindings[session] != nil {
		return nil
	}
	if b.inputBindings == nil {
		b.inputBindings = make(map[string]*runtimeInputBinding)
	}
	grant := &runtimeInputBinding{runtime: h.id(), proof: rand.Text(), seen: time.Now()}
	b.inputBindings[session] = grant
	return grant
}
func (b *runtimeRegistry) finishInputStart(session string, grant *runtimeInputBinding) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !grant.confirmed && b.inputBindings[session] == grant {
		delete(b.inputBindings, session)
	}
}

func (h *runtimeConn) reportsInput() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Caps.Input && h.daemon == ""
}

// Reports are private to the runtime wire. They never extend the hub's session row
// or let a runtime choose a card ID, tool name, field flags or answer destination.
func runtimeInputReports(raw json.RawMessage) map[runtimeInputKey]runtimeInputReport {
	var snapshot struct {
		Sessions []struct {
			ID      string          `json:"session_id"`
			Status  string          `json:"status"`
			Deleted json.RawMessage `json:"deleted"`
			Waits   json.RawMessage `json:"waiting_inputs"`
			Binding json.RawMessage `json:"input_binding"`
		} `json:"sessions"`
	}
	if json.Unmarshal(raw, &snapshot) != nil {
		return nil
	}
	seen := map[string]bool{}
	var reports map[runtimeInputKey]runtimeInputReport
	for _, row := range snapshot.Sessions {
		// normalizeRuntimeRows keeps the first row per ID and maps interrupted to unknown.
		if seen[row.ID] {
			continue
		}
		seen[row.ID] = true
		if row.Status == proto.StatusInterrupted || row.Status == proto.StatusOffline || row.Status == "deleted" || row.Status == "archived" || string(row.Deleted) == "true" || len(row.Waits) == 0 {
			continue
		}
		var proof string
		if json.Unmarshal(row.Binding, &proof) != nil || proof == "" {
			continue
		}
		var waits []json.RawMessage
		if json.Unmarshal(row.Waits, &waits) != nil {
			continue
		}
		for _, rawWait := range waits {
			var w runtimeInputReport
			if json.Unmarshal(rawWait, &w) != nil || !w.valid() {
				continue
			}
			w.binding = proof
			w.Masked = w.Masked || proto.CommandPromptMasked(w.Prompt)
			if reports == nil {
				reports = make(map[runtimeInputKey]runtimeInputReport)
			}
			key := runtimeInputKey{row.ID, w.Task}
			if _, duplicate := reports[key]; duplicate {
				reports[key] = runtimeInputReport{}
				continue
			}
			reports[key] = w
		}
	}
	return reports
}

func (w runtimeInputReport) valid() bool {
	return runtimeIDRe.MatchString(w.Task) && runtimeIDRe.MatchString(w.WaitID) &&
		strings.TrimSpace(w.Prompt) != "" && utf8.RuneCountInString(w.Prompt) <= proto.CommandInputMaxPrompt &&
		proto.CommandInputLineProblem(w.Prompt) == "" && (w.Command == "" || proto.CommandInputLineProblem(w.Command) == "") &&
		len(w.Output) <= proto.CommandInputMaxOutput && !strings.ContainsFunc(w.Output, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' })
}

// Keep the same card on repeated snapshots, including a prompt the owner has
// declined. Replacement, disappearance or reconnection invalidates old answers.
func (b *runtimeRegistry) reconcileInputs(h *runtimeConn, rows []proto.Session, reports map[runtimeInputKey]runtimeInputReport) map[runtimeInputKey]*runtimeInputWait {
	if len(reports) == 0 {
		return nil
	}
	live := make(map[string]*proto.Session, len(rows))
	for i := range rows {
		s := &rows[i]
		if !s.Archived && s.Status != proto.StatusOffline && s.Status != proto.StatusInterrupted {
			live[s.SessionID] = s
		}
	}
	next := make(map[runtimeInputKey]*runtimeInputWait, len(reports))
	now := time.Now()
	for key, w := range reports {
		s, ok := live[key.session]
		if !ok || w.WaitID == "" {
			continue
		}
		source := b.inputBindings[key.session]
		if source == nil || source.runtime != h.hello.Runtime || subtle.ConstantTimeCompare([]byte(source.proof), []byte(w.binding)) != 1 {
			continue
		}
		source.seen = now
		occurrence := runtimeInputOccurrence{task: key.task, wait: w.WaitID}
		if prev := h.inputWaits[key]; prev != nil && prev.source == source && prev.occurrence == occurrence && prev.display == w.CommandInput && prev.card.Cwd == s.Cwd {
			next[key] = prev
			continue
		}
		next[key] = &runtimeInputWait{source: source, occurrence: occurrence, display: w.CommandInput, card: newCommandInputCard(runtimeInputPrefix+rand.Text(), s.SessionID, s.Title, s.Cwd, s.Agent, s.AgentLabel, now, w.CommandInput)}
	}
	return next
}

func (b *runtimeRegistry) inputCards() []proto.Interaction {
	return b.inputCardsAt(time.Now())
}

func (b *runtimeRegistry) inputCardsAt(now time.Time) []proto.Interaction {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for session, source := range b.inputBindings {
		// A quiet session sends no proof until its next wait. Its listed owner
		// keeps the record alive, but only the original proof can authorize a card.
		if h := b.owner[session]; h != nil && h.id() == source.runtime {
			source.seen = now
		}
		if now.Sub(source.seen) > agyInputMaxAge {
			delete(b.inputBindings, session)
		}
	}
	var cards []proto.Interaction
	for _, h := range b.conns {
		if h.superseded {
			continue
		}
		for k, w := range h.inputWaits {
			if b.owner[k.session] != h || !b.authorizesInput(k.session, w) {
				continue
			}
			if claim, ok := w.source.claims[w.occurrence]; ok && !claim.inFlight && claim.retryOn != h.serial {
				continue
			}
			if !now.Before(w.card.ExpiresAt) {
				continue
			}
			cards = append(cards, w.card)
		}
	}
	return cards
}

func (b *runtimeRegistry) authorizesInput(session string, w *runtimeInputWait) bool {
	source := w.source
	return source.confirmed && b.inputBindings[session] == source
}

func changedInputPrompt() error {
	return refusal(screen.PromptChanged, i18n.T(i18n.Default, "command-input.prompt_changed"), nil)
}

// Claim the exact wait under the registry lock. Keep its connection for the call:
// a new owner must never receive the previous runtime's line.
func (b *runtimeRegistry) claimInput(session, id string) (*runtimeConn, *runtimeInputWait, error) {
	if b != nil {
		b.mu.Lock()
		defer b.mu.Unlock()
		if h := b.owner[session]; h != nil && !h.superseded {
			for _, w := range h.inputWaits {
				if w.card.SessionID != session || w.card.ID != id || !b.authorizesInput(session, w) || !time.Now().Before(w.card.ExpiresAt) {
					continue
				}
				if claim, claimed := w.source.claims[w.occurrence]; claimed {
					if claim.inFlight {
						return nil, nil, refusal(codeCommandInputRefused, i18n.T(i18n.Default, "command-input.refused_plain"), nil)
					}
					if claim.retryOn != h.serial {
						return nil, nil, changedInputPrompt()
					}
				}
				if w.source.claims == nil {
					w.source.claims = make(map[runtimeInputOccurrence]runtimeInputClaim)
				}
				w.source.claims[w.occurrence] = runtimeInputClaim{inFlight: true, retryOn: w.source.claims[w.occurrence].retryOn}
				return h, w, nil
			}
		}
	}
	return nil, nil, changedInputPrompt()
}

func (n *Node) runtimeInputAnswer(ctx context.Context, session, id string, r proto.Resolution) error {
	if r.Cancel {
		return fmt.Errorf("a command's prompt cannot be closed without an answer")
	}
	h, w, err := n.runtimes.claimInput(session, id)
	if err != nil {
		return err
	}
	ended := false
	retryOn := 0
	defer func() {
		n.runtimes.mu.Lock()
		// A refusal before writing cannot settle an earlier unknown delivery.
		if !ended && retryOn == 0 {
			retryOn = w.source.claims[w.occurrence].retryOn
		}
		if ended || retryOn != 0 {
			w.source.claims[w.occurrence] = runtimeInputClaim{retryOn: retryOn}
		} else {
			delete(w.source.claims, w.occurrence)
		}
		n.runtimes.mu.Unlock()
	}()
	if n.sessionBlocked(session, w.card.Cwd) {
		return changedInputPrompt()
	}
	if r.Dismiss || r.Allow == nil || !*r.Allow {
		ended = true
		return nil
	}
	line := r.Values[proto.CommandInputField]
	if problem := proto.CommandInputLineProblem(line); problem != "" {
		return refusal(codeCommandInputLine, i18n.T(i18n.Default, "command-input.line_"+problem), map[string]any{"problem": problem})
	}
	ctx, cancel := context.WithTimeout(ctx, agyInputSendWait)
	defer cancel()
	var result struct {
		Typed bool `json:"typed"`
	}
	err = h.peer.Call(ctx, "session.input", map[string]string{"session_id": session, "task": w.occurrence.task, "wait_id": w.occurrence.wait, "line": line}, &result)
	if err == nil && result.Typed {
		ended = true
		return nil
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		// Only forward known codes: even the driver's error message may quote the line.
		switch re.Code {
		case screen.PromptChanged:
			ended = true
			return changedInputPrompt()
		case codeCommandInputRefused:
			return refusal(re.Code, i18n.T(i18n.Default, "command-input.refused_plain"), nil)
		case codeCommandInputUnreachable:
			return refusal(re.Code, i18n.T(i18n.Default, "command-input.unreachable"), nil)
		case codeCommandInputLine:
			return refusal(re.Code, i18n.T(i18n.Default, "command-input.line_control"), nil)
		}
	}
	if errors.Is(err, rpc.ErrNotSent) || errors.Is(err, rpc.ErrNotWritten) || errors.Is(err, rpc.ErrSuspended) {
		return refusal(codeCommandInputUnreachable, i18n.T(i18n.Default, "command-input.unreachable"), nil)
	}
	retryOn = h.serial
	return &rpc.Error{Code: codeCommandInputUnconfirmed, Message: i18n.T(i18n.Default, "command-input.unconfirmed"), Data: json.RawMessage(`{"delivered":"unknown"}`)}
}
