package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Where a todo was asked for, and telling it there (#6146, docs/assistant/todos.md "Where it came from"). A todo opened from a
// request made in another chat (openFromElsewhere) records that chat and message; when it is done (or its Agent never
// started) or the user accepts it, the outcome is told there, once: a reply to the message that asked. "Don't report
// back" in the topic stops it. That it was told is kept with the todo, so a restart does not tell it again.

// TodoSource is where a todo came from: the channel, chat and message of the request, and whether the report back was
// stopped or made.
type TodoSource = db.TodoSource

const (
	// sourceReportWait is how long a closed todo waits for its outcome (todo_outcome.go reads the Agents' last replies
	// and asks a model; both have their own timeouts, together at most about two and a half minutes) before the report is
	// planned with what there is. The outcome being written plans it at once.
	sourceReportWait = 3 * time.Minute
	// sourceReportEvery is how often the todos are looked at for a report that is due, planned or lost with a timer; they
	// are also looked at when the todos are loaded, sourceReportSweepWait after (the bot may not be connected yet).
	sourceReportSweepWait = 30 * time.Second
	// sourceReportKeep is how long after a todo is done it is still reported (a report the bot could not make for longer
	// is let go), and sourceReportTries how many tries a report has in all: the hub allows a source two replies.
	sourceReportKeep  = 7 * 24 * time.Hour
	sourceReportTries = 2
	// sourceReportMax bounds the report, in runes: one short line.
	sourceReportMax = 600
)

// sourceChannels are the channels a todo can be reported back through. A request from any other is refused when it is
// opened (openFromElsewhere), so that no todo is opened that cannot answer. A new channel adds itself here and in
// sendSourceReport.
var sourceChannels = []string{channel.Feishu}

// setTodoSource records where todo id came from, in the store and in memory.
func (s *Service) setTodoSource(ctx context.Context, id string, src TodoSource) error {
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	if st := s.todoDB(); st != nil {
		if err := st.SetTodoSource(ctx, id, src); err != nil {
			return err
		}
	}
	s.todos.mu.Lock()
	defer s.todos.mu.Unlock()
	t, ok := s.todos.todos[id]
	if !ok {
		return db.ErrTodoNotFound
	}
	t.Source = &src
	return nil
}

// stopSourceReport turns the report back of todo id off ("don't report back"); changed is false when the todo has no
// source or it was off already.
func (s *Service) stopSourceReport(ctx context.Context, id string) (changed bool, err error) {
	changed, err = s.writeSourceStop(ctx, id)
	if changed {
		s.todosChanged(ctx) // after the write lock is let go, as the other writers do
	}
	return changed, err
}

func (s *Service) writeSourceStop(ctx context.Context, id string) (changed bool, err error) {
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	s.todos.mu.RLock()
	t, ok := s.todos.todos[id]
	has := ok && t.Source != nil && !t.Source.NoReport
	s.todos.mu.RUnlock()
	if !ok {
		return false, db.ErrTodoNotFound
	}
	if !has {
		return false, nil
	}
	at := decisionNow()
	if st := s.todoDB(); st != nil {
		if changed, err = st.StopTodoSourceReport(ctx, id, at); err != nil {
			return false, err
		}
	} else {
		changed = true
	}
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[id]; ok && t.Source != nil {
		t.Source.NoReport = true
		if changed && at.After(t.UpdatedAt) {
			t.UpdatedAt = at
		}
	}
	s.todos.mu.Unlock()
	return changed, nil
}

// planSourceReport records what will be told for todo id and when, in the store and in memory; false when it was planned
// already, stopped or told (the caller that wins shows the topic the plan).
func (s *Service) planSourceReport(ctx context.Context, todo Todo, text string, due time.Time, fullReply bool) (bool, error) {
	id := todo.ID
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	s.todos.mu.RLock()
	t, ok := s.todos.todos[id]
	can := ok && t.DoneAt != nil && t.DoneAt.Equal(*todo.DoneAt) &&
		t.Source != nil && !t.Source.NoReport && t.Source.ReportedAt == "" && t.Source.ReportDueAt == ""
	s.todos.mu.RUnlock()
	if !can {
		return false, nil
	}
	if st := s.todoDB(); st != nil {
		planned, err := st.PlanTodoSourceReport(ctx, id, text, due, fullReply, todo.DoneAt)
		if err != nil || !planned {
			return false, err
		}
	}
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[id]; ok && t.Source != nil {
		t.Source.ReportText, t.Source.ReportDueAt = text, due.UTC().Format(time.RFC3339Nano)
		t.Source.ReportFullReply = fullReply
	}
	s.todos.mu.Unlock()
	return true, nil
}

// claimSourceReport takes one try at the report back of todo id, written down before it is sent: it is the guard against
// telling twice, across a restart and across assistants on one store. ok is false for a todo with no source, one the user
// stopped, one told, one not planned and one that used its tries; attempts is which try this is.
func (s *Service) claimSourceReport(ctx context.Context, id string) (src TodoSource, attempts int, ok bool) {
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	s.todos.mu.RLock()
	t, found := s.todos.todos[id]
	if !found || t.Source == nil || t.Source.NoReport || t.Source.ReportedAt != "" || t.Source.ReportDueAt == "" || t.Source.Attempts >= sourceReportTries {
		s.todos.mu.RUnlock()
		return TodoSource{}, 0, false
	}
	src = *t.Source
	attempts = src.Attempts + 1
	s.todos.mu.RUnlock()
	at := s.tposts.clock().UTC()
	if st := s.todoDB(); st != nil {
		// The store says whether it is time and what is to be told: another assistant may have planned it afresh since.
		n, text, fullReply, claimed, err := st.ClaimTodoSourceReport(ctx, id, at, sourceReportTries)
		if err != nil {
			s.logf("todo %s: claim the report back: %v", id, err)
			return TodoSource{}, 0, false
		}
		if !claimed {
			return TodoSource{}, 0, false // told, or stopped, by another assistant on the store
		}
		attempts, src.ReportText, src.ReportFullReply = n, text, fullReply
	}
	src.ReportedAt, src.Attempts = at.Format(time.RFC3339Nano), attempts
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[id]; ok && t.Source != nil {
		t.Source.ReportedAt, t.Source.Attempts = src.ReportedAt, attempts
	}
	s.todos.mu.Unlock()
	return src, attempts, true
}

// dropSourcePlan cancels the report planned for todo id, if it was not told yet: the todo was reopened, so the text the topic
// was shown may no longer be what it comes to. When it is closed again the report is planned afresh: a new text, a new
// notice, a new wait. Called by the writer of the todo, which may hold todoBook.writing: it takes only the memory lock.
func (s *Service) dropSourcePlan(id string) {
	s.todos.mu.RLock()
	t, ok := s.todos.todos[id]
	planned := ok && t.Source != nil && t.Source.ReportDueAt != "" && t.Source.ReportedAt == ""
	s.todos.mu.RUnlock()
	if !planned {
		return
	}
	if st := s.todoDB(); st != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := st.ClearTodoSourcePlan(ctx, id); err != nil {
			s.logf("todo %s: drop the report back's plan: %v", id, err)
			return
		}
	}
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[id]; ok && t.Source != nil && t.Source.ReportedAt == "" {
		t.Source.ReportText, t.Source.ReportDueAt, t.Source.ReportFullReply = "", "", false
	}
	s.todos.mu.Unlock()
}

// releaseSourceReport gives a claim back, for a report that did not go out (the try stays counted).
func (s *Service) releaseSourceReport(ctx context.Context, id string) {
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	if st := s.todoDB(); st != nil {
		if err := st.ReleaseTodoSourceReport(ctx, id); err != nil {
			s.logf("todo %s: release the report back: %v", id, err)
			return
		}
	}
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[id]; ok && t.Source != nil {
		t.Source.ReportedAt = ""
	}
	s.todos.mu.Unlock()
}

// reportable says whether todo id has a report back still to make.
func (s *Service) reportable(id string) bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t, ok := s.todos.todos[id]
	return ok && t.Source != nil && !t.Source.NoReport && t.Source.ReportedAt == "" && t.Source.Attempts < sourceReportTries
}

// sourceReportSoon looks at the report of todo id after wait: the todo closed (or was accepted), or its plan is due. Several
// calls for one todo keep the earliest timer, so a ready result can supersede the timer waiting for its outcome.
func (s *Service) sourceReportSoon(id string, wait time.Duration) {
	if !s.reportable(id) {
		return
	}
	b := &s.todos
	when := s.tposts.clock().Add(wait)
	b.reportMu.Lock()
	if scheduled, ok := b.reporting[id]; ok && !when.Before(scheduled) {
		b.reportMu.Unlock()
		return
	}
	if b.reporting == nil {
		b.reporting = map[string]time.Time{}
	}
	b.reporting[id] = when
	b.reportMu.Unlock()
	s.tposts.later(wait, func() {
		b.reportMu.Lock()
		if !b.reporting[id].Equal(when) {
			b.reportMu.Unlock()
			return
		}
		delete(b.reporting, id)
		b.reportMu.Unlock()
		s.sourceReportNow(id)
	})
}

// sourceReportEvery is a var for the tests only.
var sourceReportEvery = 10 * time.Minute

// sourceReportLoop looks at the reports every sourceReportEvery: a timer lost to a restart, a plan that is due, a try that
// failed for a reason that may pass.
func (s *Service) sourceReportLoop(ctx context.Context) {
	t := time.NewTicker(sourceReportEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweepSourceReports()
		}
	}
}

// sweepSourceReports looks at the report of every todo that is done (within sourceReportKeep) and has one still to make:
// one that is planned is looked at when it is due, the others soon. It runs when the todos are loaded and every
// sourceReportEvery.
func (s *Service) sweepSourceReports() {
	now := s.tposts.clock()
	s.todos.mu.RLock()
	waits := map[string]time.Duration{}
	for id, t := range s.todos.todos {
		src := t.Source
		if src == nil || src.NoReport || src.ReportedAt != "" || src.Attempts >= sourceReportTries || t.DoneAt == nil || time.Since(*t.DoneAt) > sourceReportKeep {
			continue
		}
		wait := sourceReportSweepWait
		if due, err := time.Parse(time.RFC3339Nano, src.ReportDueAt); err == nil && due.After(now) {
			wait = due.Sub(now)
		}
		waits[id] = wait
	}
	s.todos.mu.RUnlock()
	ids := make([]string, 0, len(waits))
	for id := range waits {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		s.sourceReportSoon(id, waits[id])
	}
}

// sourceReportNow takes the next step of todo id's report back: plan it (with a preview when the delay is positive), or, when
// the time has come, tell the source what came of it. Nothing for a todo that was reopened since, or done longer ago than
// sourceReportKeep.
func (s *Service) sourceReportNow(id string) {
	s.sourceReportStep(id, false)
}

func (s *Service) sourceReportStep(id string, finalOnly bool) {
	s.todos.runMu.Lock()
	defer s.todos.runMu.Unlock()
	if !s.reportable(id) {
		s.forgetSourceWait(id)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	t, ok := s.todoNow(id)
	if !ok || t.Source == nil {
		return
	}
	now := s.tposts.clock()
	if t.DoneAt == nil || time.Since(*t.DoneAt) > sourceReportKeep { // reopened since, or too long ago
		s.forgetSourceWait(id)
		return
	}
	if !slices.Contains(sourceChannels, t.Source.Channel) {
		s.logf("todo %s: no way back to channel %q", id, t.Source.Channel)
		return
	}
	// The bot must be there: with it away nothing is planned or claimed, so a later sweep still can.
	// Read the current setting when planning; a saved plan keeps its announced deadline.
	st, err := s.chat().Status(ctx)
	if err != nil || !st.Ready {
		s.logf("todo %s: not reporting back yet: the bot is not ready (%v)", id, err)
		return
	}
	if t.Source.ReportDueAt == "" {
		s.prepareSourceReport(ctx, t, st, finalOnly)
		return
	}
	if finalOnly {
		return
	}
	due, err := time.Parse(time.RFC3339Nano, t.Source.ReportDueAt)
	if err != nil {
		s.logf("todo %s: the report's time %q: %v", id, t.Source.ReportDueAt, err)
		return
	}
	if due.After(now) {
		s.sourceReportSoon(id, due.Sub(now))
		return
	}
	if t.Thread == nil && t.Source.Anchor == "" {
		// The owner can cancel in the topic or, for a todo with an anchor, in their private chat.
		s.logf("todo %s: not reporting back: it is no longer bound to its topic", id)
		return
	}
	src, attempts, ok := s.claimSourceReport(ctx, id)
	if !ok {
		return
	}
	err = s.sendSourceReport(ctx, src, id)
	if err == nil {
		s.logf("todo %s: reported back to its source", id)
		return
	}
	s.logf("todo %s: report back (try %d): %v", id, attempts, err)
	if !permanentReportError(err) && attempts < sourceReportTries {
		// A failure that may pass: the claim is given back and the next sweep tries again. The same key makes the second
		// try land once at Feishu even if the first had gone out.
		s.releaseSourceReport(context.WithoutCancel(ctx), id)
		return
	}
	// It will not go (the chat refuses, the original is gone) or it has used its tries: the claim stays, so it is not tried
	// again, and the topic is told.
	s.tellOwner(ctx, t, st, "source_report_unreachable", i18n.Args{"error": clip(oneLine(err.Error()), 200)}, "sx:"+id, false)
}

// permanentReportError says whether a failed report can never go: the hub refuses the chat or the message, or Feishu gave a
// definite answer (the original withdrawn or deleted, the chat gone). An error with no answer from Feishu (the network, a
// rate limit, a server failing) may pass.
func permanentReportError(err error) bool {
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == CodeChatDenied {
		return true
	}
	return !botcard.Transient(err)
}

// sourceAnswerPoll is how often a closed todo's answer is read again while it is awaited: often
// while an answer usually lands (sourceReportWait), then less often for background work that can
// run up to channel.HeldMost.
func sourceAnswerPoll(sinceDone time.Duration) time.Duration {
	if sinceDone < sourceReportWait {
		return 5 * time.Second
	}
	return 30 * time.Second
}

// prepareSourceReport previews delayed replies where the owner can cancel with "/noreply", then persists the
// deadline and schedules delivery. A zero delay uses the same plan and send path without a preview.
func (s *Service) prepareSourceReport(ctx context.Context, t Todo, st FeishuStatus, finalOnly bool) {
	th := t.Thread
	if t.Source.Anchor == "" && (th == nil || th.Channel != channel.Feishu || !slices.Contains(st.Chats, th.ChatID)) {
		s.logf("todo %s: not reporting back: no eligible topic for the source report", t.ID)
		return
	}
	var text string
	if st.SourceReportFullReply && t.StartError == "" && !t.Cancelled() {
		reply, wait := s.sourceFinalReply(ctx, t)
		since := time.Since(*t.DoneAt)
		if remaining := wait - since; reply == "" && wait > 0 && remaining > 0 {
			s.sourceReportSoon(t.ID, min(remaining, sourceAnswerPoll(since)))
			return
		}
		s.forgetSourceWait(t.ID)
		text = reply
	}
	fullReply := text != ""
	if !fullReply {
		if finalOnly {
			return // the short outcome may still be recorded
		}
		if st.SourceReportFullReply && t.StartError == "" && !t.Cancelled() {
			text = i18n.T(s.botLang(ctx), "assistant-feishu.source_report_no_final")
		} else {
			text = s.sourceReportText(ctx, t)
		}
	}
	minutes := assistantwire.SourceReportDelayDefault
	if st.SourceReportDelayMinutes != nil {
		minutes = *st.SourceReportDelayMinutes
	}
	delay := time.Duration(minutes) * time.Minute
	if delay > 0 {
		key := "source_report_notice"
		if fullReply {
			key = "source_report_full_notice"
		}
		suffix := ""
		if t.Source.Anchor != "" {
			suffix = "_chat"
		}
		uuid := "sn:" + t.ID + s.tposts.clock().UTC().Format(time.RFC3339Nano)
		args := i18n.Args{"minutes": minutes, "text": text}
		if !s.tellOwner(ctx, t, st, key+suffix, args, uuid, fullReply) {
			return
		}
		if fullReply {
			preview := i18n.T(s.botLang(ctx), "assistant-feishu."+key+suffix, args)
			if _, inline := botcard.InlineAnswer(preview); !inline {
				if !s.tellOwner(ctx, t, st, "source_report_file_notice"+suffix, i18n.Args{"minutes": minutes}, uuid+":file", false) {
					return
				}
			}
		}
	}
	due := s.tposts.clock().Add(delay)
	planned, err := s.planSourceReport(ctx, t, text, due, fullReply)
	if err != nil {
		s.logf("todo %s: plan the report back: %v", t.ID, err)
		return
	}
	if planned {
		s.sourceReportSoon(t.ID, max(0, due.Sub(s.tposts.clock())))
	}
}

// tellOwner writes a line of the language file where todo t is worked on: into the topic it is bound to, as a reply under the
// latest message known there (the bot's lines to a topic, the lark module's thread_posts.go), or, for a todo that has no topic (its
// source has an anchor), into the owner's private chat as a reply under the anchor. key makes the same line, sent again,
// land once. false when it could not be written.
func (s *Service) tellOwner(ctx context.Context, t Todo, st FeishuStatus, key string, args i18n.Args, uuid string, fullReply bool) bool {
	if t.Source != nil && t.Source.Anchor != "" {
		text := i18n.T(s.botLang(ctx), "assistant-feishu."+key, args)
		// no_detour: with the anchor gone the line is lost rather than posted as a message of its own, which could not be
		// replied to with "/noreply" (as in a topic, where nothing is planned then).
		if err := s.chat().Reply(ctx, larkhost.Target{NoDetour: true}, t.Source.Anchor, text, reportKey(uuid, t.ID), fullReply); err != nil {
			s.logf("todo %s: tell the private chat: %v", t.ID, err)
			return false
		}
		return true
	}
	th := t.Thread
	if th == nil || th.Channel != channel.Feishu || !slices.Contains(st.Chats, th.ChatID) {
		return false
	}
	anchor := s.larkModule().TopicAnchor(ctx, th.ChatID, th.ThreadID)
	if anchor == "" {
		s.logf("todo %s: no message of its topic is known to reply under", t.ID)
		return false
	}
	text := i18n.T(s.botLang(ctx), "assistant-feishu."+key, args)
	to := larkhost.Target{ChatID: th.ChatID, InThread: true, NoDetour: true}
	if err := s.chat().Reply(ctx, to, anchor, text, reportKey(uuid, t.ID), fullReply); err != nil {
		s.logf("todo %s: tell its topic: %v", t.ID, err)
		return false
	}
	return true
}

// sendSourceReport writes text under the message that asked, through the channel it came in by. Feishu: a reply to the
// message in its chat (the only place the hub lets the bot write in a group that is not opted in, docs/extensions/bots.md).
func (s *Service) sendSourceReport(ctx context.Context, src TodoSource, todoID string) error {
	switch src.Channel {
	case channel.Feishu:
		return s.chat().Reply(ctx, larkhost.Target{ChatID: src.ChatID, NoDetour: true}, src.MessageID, src.ReportText,
			reportKey(todoID, src.MessageID), src.ReportFullReply)
	}
	return fmt.Errorf("no way back to channel %q", src.Channel)
}

// reportKey is an idempotency key made of a purpose and ids: the same ones always make the same key, so a try repeated
// after an unclear failure lands once.
func reportKey(parts ...string) string {
	sum := sha256.Sum256([]byte("report\x00" + strings.Join(parts, "\x00")))
	return "sr:" + hex.EncodeToString(sum[:])[:32]
}

// sourceReportText is what the todo came to, as one short line for a chat that is not the owner's own: the outcome note
// of the todo (the first line of the "#o" row todo_outcome.go writes: the sentence, never its links), else only that it is
// done, or, when it never started, only that it could not be done. Nothing else leaves the todo: not its decisions, the
// Agents' replies, the judge's reason or the error of a start that failed; by default the owner sees the text before it goes out.
func (s *Service) sourceReportText(ctx context.Context, t Todo) string {
	l := s.botLang(ctx)
	if t.StartError != "" {
		return i18n.T(l, "assistant-feishu.source_report_failed")
	}
	if body := s.sourceReportBody(ctx, t.ID); body != "" {
		return body
	}
	return i18n.T(l, "assistant-feishu.source_report_done")
}

// sourceReportBody is the sentence of the latest outcome note of todo id, cleaned for a chat that is not the owner's
// (cleanReport); "" when there is none, it cannot be read, or nothing is left of it. Whoever wrote the note (the model, or
// the receipt's text when there is none) is read the same way here.
func (s *Service) sourceReportBody(ctx context.Context, id string) string {
	st := s.todoDB()
	if st == nil {
		return ""
	}
	list, err := st.ListTodoDecisions(ctx, id, false)
	if err != nil {
		s.logf("todo %s: decisions for the report back: %v", id, err)
		return ""
	}
	body := ""
	for _, d := range list {
		if d.Source == db.DecisionOutcome && strings.HasSuffix(d.SourceMessage, "#o") {
			line, _, _ := strings.Cut(strings.TrimSpace(d.Choice), "\n") // the sentence; its links follow
			body = line
		}
	}
	return cleanReport(body)
}

var (
	reportMDLink  = lazyre.New(`!?\[[^\]]*\]\([^)]*\)`)
	reportURL     = lazyre.New(`(?i)\b[a-z][a-z0-9+.-]*://\S+|\bwww\.\S+`)
	reportCode    = lazyre.New("(?s)```.*?```|`[^`]*`")
	reportTag     = lazyre.New(`<[^<>]*>`)
	reportPath    = lazyre.New(`(?:[A-Za-z]:\\[^\s]*)|(?:[\w.~@%+=:,-]*(?:/[\w.~@%+=:,-]+)+/?)`)
	reportMention = lazyre.New(`@[^\s，。,;；:：、!！?？)）]+`) // i18n-ignore: full-width punctuation that ends a mention, not text for people
)

// cleanReport is a sentence of an outcome as it may be told to a chat that is not the owner's own: one line without
// links (a web address, a markdown link), code (a backtick span), paths (anything made of words joined by "/"), markup
// (anything in <...>, an <at ...> among it) or an @-mention, clipped to sourceReportMax. "" when nothing is left, or only
// punctuation.
func cleanReport(s string) string {
	for _, re := range []*lazyre.Regexp{reportMDLink, reportCode, reportURL, reportTag, reportPath, reportMention} {
		s = re.ReplaceAllString(s, " ")
	}
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return ""
	}
	return clip(s, sourceReportMax)
}

// noReplyCommand says whether text is the user's "don't report back": "/noreply" (or the full-width "／noreply", in any
// case) and nothing else. Decided by the text alone, never by a model.
func noReplyCommand(text string) bool {
	t := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(t, "/")
	if !ok {
		rest, ok = strings.CutPrefix(t, "／") // i18n-ignore: the full-width slash
	}
	return ok && strings.EqualFold(rest, "noreply")
}

// chatNoReply answers "/noreply" said in the owner's private chat as a reply to the first message of a to-do that has no
// topic (TodoSource.Anchor), or to anything the bot said under it: the run's answer, the notice before the report. It
// says whether it took the message; one that is no reply to such a message is nothing of its business and goes on as it
// was. Recorded as a decision of the todo, not passed to any Agent, like the topic's.
func (s *Service) chatNoReply(ctx context.Context, ev botcard.MessageEvent) bool {
	msg := ev.Message
	if msg.RootID == "" && msg.ParentID == "" {
		return false
	}
	if s.ensureTodosLoaded(ctx) != nil {
		return false
	}
	t, ok := s.todoOfAnchor(msg.RootID, msg.ParentID)
	if !ok {
		return false
	}
	s.stopReportOf(ctx, msg.MessageID, t, larkhost.Target{}, "chat_question", db.DecisionManual)
	return true
}

// todoOfAnchor is the to-do whose first message in the owner's private chat is one of ids ("" is none).
func (s *Service) todoOfAnchor(ids ...string) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		if t.Source == nil || t.Source.Anchor == "" {
			continue
		}
		for _, id := range ids {
			if id != "" && id == t.Source.Anchor {
				return copyTodoLocked(t), true
			}
		}
	}
	return Todo{}, false
}

// stopReportOf is "don't report back" for todo t, said in message messageID: the answer goes to `to`, and the decision
// that records it is the user's, of the kind source, with the question the language file calls questionKey.
func (s *Service) stopReportOf(ctx context.Context, messageID string, t Todo, to larkhost.Target, questionKey, source string) {
	l := s.botLang(ctx)
	say := func(key string, args ...i18n.Args) {
		s.chat().Note(ctx, to, messageID, i18n.T(l, "assistant-feishu."+key, args...))
	}
	switch {
	case t.Source == nil:
		say("source_noreply_none")
		return
	case t.Source.ReportedAt != "":
		say("source_noreply_told")
		return
	}
	changed, err := s.stopSourceReport(ctx, t.ID)
	if err != nil {
		s.logf("todo %s: don't report back: %v", t.ID, err)
		say("source_noreply_failed", i18n.Args{"error": clip(oneLine(err.Error()), 200)})
		return
	}
	if !changed {
		say("source_noreply_already")
		return
	}
	if store := s.todoDB(); store != nil {
		at := decisionNow()
		inserted, err := s.insertDecision(ctx, store, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: t.ID,
			Question: i18n.T(l, "assistant-feishu."+questionKey), Choice: i18n.T(l, "assistant-feishu.source_noreply_choice"),
			Source: source, SourceMessage: "feishu-" + messageID, CreatedAt: at})
		switch {
		case err != nil:
			s.logf("todo %s: record don't report back: %v", t.ID, err)
		case inserted:
			s.decisionsChanged(ctx, t.ID, at)
		}
	}
	say("source_noreply_done")
}
