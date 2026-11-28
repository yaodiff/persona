package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// What a T3 Code thread waits on (docs/integrations/engines.md "T3 Code"). A run that asks (Claude Code's
// AskUserQuestion, Codex's request_user_input) or wants an approval keeps running in T3 Code: only the thread's read
// says it waits, with pendingRequestCount, which is how T3 Code's own command line tells. The bridge reads that count
// for live threads, so such a thread shows as Needs you rather than as working.
//
// t3_pending_request lists, reads and answers a thread's questions (never its approvals). T3 Code takes an answer only
// from a caller whose mode ceiling covers the thread's mode, and the session the bridge issues is held to
// approval-required, so a question becomes a card on the phone only in a thread at that mode. A thread in a broader
// mode (full access, T3 Code's default) shows as Needs you with its question in the chat, answered in T3 Code.

var (
	// t3codePendingProbeMost is the most live threads one refresh reads for what they wait on.
	t3codePendingProbeMost = 30
	// t3codeAskRetry is the wait before asking again after a card could not be raised (no hub, a read that failed).
	t3codeAskRetry = 5 * time.Second
)

// t3codeAsk is a card raised, or to raise, for a question a thread waits on.
type t3codeAsk struct {
	threadID  string
	requestID string
	cancel    context.CancelFunc
	retryAt   time.Time
}

// t3codeLive reports whether a thread's status is that of a run going on (one that may wait on the user).
func t3codeLive(status string) bool {
	switch status {
	case "preparing", "queued", "starting", "running", "waiting":
		return true
	}
	return false
}

// t3codeAnswerable reports whether T3 Code takes the bridge's answer to th's questions: only at approval-required,
// the ceiling of the session the bridge issues.
func t3codeAnswerable(th t3codeThreadListed) bool { return th.RuntimeMode == "approval-required" }

// readPending reads how many approvals and questions each live thread waits on into its pending. A thread whose read
// fails keeps pending nil, as does one past t3codePendingProbeMost.
func (b *t3codeBridge) readPending(ctx context.Context, threads []t3codeThreadListed) {
	probed := 0
	for i := range threads {
		if !t3codeLive(threads[i].Status) || probed >= t3codePendingProbeMost {
			continue
		}
		probed++
		var res struct {
			Thread struct {
				PendingRequestCount *int `json:"pendingRequestCount"`
			} `json:"thread"`
		}
		if err := b.mcp.call(ctx, "t3_thread_read", map[string]any{"threadId": threads[i].ThreadID, "limit": 1, "runLimit": 1}, &res); err != nil {
			if ctx.Err() != nil {
				return
			}
			continue
		}
		threads[i].pending = res.Thread.PendingRequestCount
	}
}

// syncQuestions raises a card for each question an answerable thread waits on and withdraws the cards of questions
// that wait no more (answered in T3 Code, or the run ended). The cards of a live thread whose count could not be read,
// or whose questions could not be listed, stay.
func (b *t3codeBridge) syncQuestions(ctx context.Context, threads []t3codeThreadListed) {
	waiting := map[string]bool{} // ask keys whose questions still wait
	keep := map[string]bool{}    // threads whose cards stay as they are
	for _, th := range threads {
		if th.pending == nil {
			if t3codeLive(th.Status) {
				keep[th.ThreadID] = true
			}
			continue
		}
		if *th.pending == 0 || !t3codeAnswerable(th) {
			continue
		}
		var res struct {
			RequestIDs []string `json:"requestIds"`
		}
		if err := b.mcp.call(ctx, "t3_pending_request", map[string]any{"action": "list", "threadId": th.ThreadID}, &res); err != nil {
			keep[th.ThreadID] = true
			continue
		}
		for _, id := range res.RequestIDs {
			if id = strings.TrimSpace(id); id != "" {
				waiting[th.ThreadID+"/"+id] = true
				b.askQuestion(ctx, th.ThreadID, id)
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, a := range b.asks {
		if waiting[key] || keep[a.threadID] {
			continue
		}
		if a.cancel != nil {
			a.cancel()
		}
		delete(b.asks, key)
	}
}

// t3codeAskID is the card id of a thread's question: the same for the same question, so asking again after the bridge
// or the node restarts keeps the card the phone has.
func t3codeAskID(threadID, requestID string) string {
	sum := sha256.Sum256([]byte(threadID + "/" + requestID))
	return "t3code_" + hex.EncodeToString(sum[:8])
}

// askQuestion raises the card of a question threadID waits on, unless it is up already, and answers the question in
// T3 Code with what the phone answered. A card handed back to the computer leaves the question to T3 Code.
func (b *t3codeBridge) askQuestion(ctx context.Context, threadID, requestID string) {
	key := threadID + "/" + requestID
	b.mu.Lock()
	a := b.asks[key]
	due := a == nil || (!a.retryAt.IsZero() && time.Now().After(a.retryAt))
	if a == nil {
		a = &t3codeAsk{threadID: threadID, requestID: requestID}
		b.asks[key] = a
	}
	if due {
		a.retryAt = time.Time{}
	}
	b.mu.Unlock()
	if !due {
		return
	}
	retry := func() {
		b.mu.Lock()
		if cur := b.asks[key]; cur == a {
			a.retryAt = time.Now().Add(t3codeAskRetry)
		}
		b.mu.Unlock()
	}
	var q struct {
		Questions []t3codeQuestion `json:"questions"`
	}
	if err := b.mcp.call(ctx, "t3_pending_request", map[string]any{"action": "read", "threadId": threadID, "requestId": requestID}, &q); err != nil {
		retry()
		return
	}
	ix := t3codeInteraction(t3codeSessionID(threadID), q.Questions)
	if ix == nil {
		return // nothing a card can ask: left to T3 Code
	}
	ix.ID = t3codeAskID(threadID, requestID)
	askCtx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	if b.asks[key] != a { // withdrawn meanwhile
		b.mu.Unlock()
		cancel()
		return
	}
	a.cancel = cancel
	b.mu.Unlock()
	go func() {
		defer cancel()
		var res proto.Resolution
		err := b.peer.Call(askCtx, "interaction.create", map[string]any{"interaction": ix}, &res)
		if askCtx.Err() != nil {
			if ctx.Err() == nil { // the question waits no more: take the card back
				_ = b.peer.Notify("interaction.cancel", map[string]any{"id": ix.ID})
			}
			return
		}
		if err != nil {
			retry()
			return
		}
		if res.Dismiss || res.Cancel {
			return
		}
		answers := t3codeAnswers(q.Questions, res.Answers)
		if len(answers) == 0 {
			return
		}
		err = b.mcp.call(ctx, "t3_pending_request", map[string]any{"action": "respond", "threadId": threadID,
			"requestId": requestID, "answers": answers}, nil)
		if err != nil {
			b.logf("t3code bridge: answering a question of thread %s: %v", threadID, err)
			return
		}
		b.wakeSoon()
	}()
}

// text is what a question asks: its question, else its header, else its id. A card's answers are keyed by it.
func (q t3codeQuestion) text() string {
	return firstOf(strings.TrimSpace(q.Question), strings.TrimSpace(q.Header), q.ID)
}

// optionValue is what T3 Code takes for the option labelled label: its value, else its label.
func (q t3codeQuestion) optionValue(label string) (string, bool) {
	label = strings.TrimSpace(label)
	for _, o := range q.Options {
		if strings.TrimSpace(o.Label) == label && label != "" {
			return firstOf(o.Value, o.Label), true
		}
	}
	return "", false
}

// t3codeInteraction is the card of a request's questions, nil when there is none to ask.
func t3codeInteraction(sessionID string, questions []t3codeQuestion) *proto.Interaction {
	ix := &proto.Interaction{SessionID: sessionID, Kind: proto.IxQuestion}
	for _, q := range questions {
		if q.text() == "" {
			continue
		}
		pq := proto.Question{Question: q.text(), MultiSelect: q.MultiSelect}
		if h := strings.TrimSpace(q.Header); h != pq.Question {
			pq.Header = h
		}
		for _, o := range q.Options {
			if l := strings.TrimSpace(o.Label); l != "" {
				pq.Options = append(pq.Options, proto.QuestionOption{Label: l, Description: strings.TrimSpace(o.Description)})
			}
		}
		ix.Questions = append(ix.Questions, pq)
	}
	if len(ix.Questions) == 0 {
		return nil
	}
	return ix
}

// t3codeAnswers is a card's answers (question text → option label, a multi-select's labels joined with ", ", or what
// the user typed) as T3 Code takes them: question id → an option's value (a list for a multi-select) or the typed
// text. Typed text is left out for a question that takes only its options, so T3 Code says what is missing.
func t3codeAnswers(questions []t3codeQuestion, answers map[string]string) map[string]any {
	out := map[string]any{}
	for _, q := range questions {
		a, ok := answers[q.text()]
		if a = strings.TrimSpace(a); !ok || a == "" || q.ID == "" {
			continue
		}
		if v, ok := q.optionValue(a); ok {
			if q.MultiSelect {
				out[q.ID] = []string{v}
			} else {
				out[q.ID] = v
			}
			continue
		}
		if q.MultiSelect {
			var values []string
			for _, part := range strings.Split(a, ", ") {
				v, ok := q.optionValue(part)
				if !ok {
					values = nil
					break
				}
				values = append(values, v)
			}
			if len(values) > 0 {
				out[q.ID] = values
				continue
			}
		}
		if q.AllowCustomAnswer == nil || *q.AllowCustomAnswer {
			out[q.ID] = a
		}
	}
	return out
}
