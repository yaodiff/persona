package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Matching owns no source actions: it only proposes a reversible task link from this manager's snapshot.
func (s *Service) matchPlannedInbox(ctx context.Context, machine, conversation string, t *PlanTask) error {
	if t.InboxOptOut {
		return nil
	}
	if t.InboxItem != "" || t.Continues != "" {
		return nil
	}
	if err := s.inboxReady(ctx); err != nil {
		return nil
	}
	cfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		return nil
	}
	if t.Brief != nil && t.Brief.Request != nil {
		t.InboxMatchText = t.Brief.Request.Text
	}
	if todo, ok := s.todoNow(t.ReuseTodo); ok && todo.InboxItem != "" {
		return nil
	}
	text := softTaskText(*t)
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	ids, strong := b.softInboxCandidates(text, *t, time.Now(), func(it *InboxItem) bool {
		return inboxVisibilityWithMembersExcludedLocked(b, it, cfg.Visibility)
	})
	// A previously confirmed handoff wins over inference, but never an unrelated item in this conversation.
	var handed string
	for _, id := range ids {
		if h := handoffTo(b.items[id], conversation); h != nil && h.MachineID == machine && h.Op != "" && softStrong(text, b.items[id]) {
			if handed != "" {
				handed = ""
				break
			}
			handed = id
		}
	}
	if handed != "" && strong {
		// Omitted sources remain inferred even when a matching handoff exists.
		// Keep the snapshot so admission cannot overwrite a link acquired meanwhile.
		t.InboxMatch = b.softInboxVersion(ids)
		t.InboxItem, t.InboxHandoff = handed, handoffTo(b.items[handed], conversation).Op
		return b.checkTaskInbox(conversation, *t)
	}
	t.InboxCandidates = ids
	if !strong {
		return nil
	}
	t.InboxItem = ids[0]
	t.InboxMatch = b.softInboxVersion(ids)
	return nil
}

func softTaskText(t PlanTask) string { return t.Title + "\n" + t.Prompt + "\n" + t.InboxMatchText }

func softNormalize(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// Names are whole normalized phrases, never substrings of a different group or person's name.
func softPhrase(text, phrase string) bool {
	text, phrase = softNormalize(text), softNormalize(phrase)
	if phrase == "" {
		return false
	}
	for start := 0; start <= len(text); {
		i := strings.Index(text[start:], phrase)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(phrase)
		before, after := true, true
		if i > 0 {
			r, _ := utf8.DecodeLastRuneInString(text[:i])
			before = !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}
		if end < len(text) {
			r, _ := utf8.DecodeRuneInString(text[end:])
			after = !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}
		if before && after {
			return true
		}
		start = end
	}
	return false
}

var softURL = lazyre.New(`https?://[^\s<>"\x60]+`)

func softURLs(text string) []string {
	var out []string
	for _, u := range softURL.FindAllString(text, -1) {
		// A bare origin is not a discriminating URL.
		rest := u[strings.Index(u, "://")+3:]
		if i := strings.IndexByte(rest, '/'); i >= 0 && len(rest[i:]) > 1 {
			out = append(out, u)
		}
	}
	return out
}
func softSharedURL(a, b string) bool {
	urls := softURLs(a)
	for _, u := range softURLs(b) {
		if slices.Contains(urls, u) {
			return true
		}
	}
	return false
}
func softDistinct(text string) bool {
	text = softNormalize(text)
	chars := map[rune]bool{}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			chars[r] = true
		}
	}
	return utf8.RuneCountInString(text) >= 32 && len(chars) >= 12
}
func softMessages(it *InboxItem) []InboxChainMessage {
	var out []InboxChainMessage
	for _, e := range it.Chain {
		if e.Kind == ChainReceived {
			out = append(out, e.Messages...)
		}
	}
	return out
}
func softStrong(text string, it *InboxItem) bool {
	if softPhrase(text, it.Sender) {
		return true
	}
	for _, m := range softMessages(it) {
		if softSharedURL(text, m.Text) || softDistinct(m.Text) && strings.Contains(softNormalize(text), softNormalize(m.Text)) {
			return true
		}
	}
	// Preview is source text, unlike the generated summary and surrounding Context.
	return softSharedURL(text, it.Preview) || softDistinct(it.Preview) && strings.Contains(softNormalize(text), softNormalize(it.Preview))
}

// A group name must end at a source-style delimiter or its own line, not inside a longer name.
func softGroupPhrase(text, name string) bool {
	name = softNormalize(name)
	if name == "" {
		return false
	}
	for _, line := range strings.Split(text, "\n") {
		line = softNormalize(line)
		for start := 0; start < len(line); {
			i := strings.Index(line[start:], name)
			if i < 0 {
				break
			}
			i += start
			tail := strings.TrimSpace(line[i+len(name):])
			// i18n-ignore: delimiters in source group names and user messages, not displayed text.
			if softPhrase(line, name) && (tail == "" || strings.ContainsRune(":,;.!?\"'[]()：，；。！？【】（）", []rune(tail)[0])) {
				return true
			}
			start = i + len(name)
		}
	}
	return false
}

func softPlace(it *InboxItem) string {
	if it.Where != "" {
		return it.Where
	}
	// A rewritten topic title cannot attest the source group name in legacy rows.
	if it.TitleBy != "" {
		return ""
	}
	return it.Title
}

// Equivalence needs a message, not overlapping context. Source-local positions never identify a cross-source message.
func softEquivalent(a, b *InboxItem) bool {
	if softNormalize(softPlace(a)) != softNormalize(softPlace(b)) {
		return false
	}
	if a.Source == b.Source && a.Account == b.Account && a.Last != "" && a.Last == b.Last && a.Lark != nil && b.Lark != nil && a.Lark.FeedID != "" && a.Lark.FeedID == b.Lark.FeedID {
		return true
	}
	if a.Source == b.Source {
		if a.Lark == nil || b.Lark == nil || a.Lark.FeedID == "" || a.Lark.FeedID != b.Lark.FeedID || a.Account != b.Account {
			return false
		}
	}
	for _, x := range softMessages(a) {
		for _, y := range softMessages(b) {
			if x.At == nil || y.At == nil || x.Sender == "" || softNormalize(x.Sender) != softNormalize(y.Sender) {
				continue
			}
			d := x.At.Sub(*y.At)
			if d < -2*time.Minute || d > 2*time.Minute {
				continue
			}
			if softDistinct(x.Text) && softNormalize(x.Text) == softNormalize(y.Text) || softSharedURL(x.Text, y.Text) {
				return true
			}
		}
	}
	return false
}

// Under inbox.mu. The complete matching topic must be pairwise equivalent; transitive context overlap is not proof.
func (b *inboxBook) softInboxCandidates(text string, task PlanTask, now time.Time, excluded func(*InboxItem) bool) ([]string, bool) {
	var ids []string
	blocked := false
	for id, it := range b.items {
		at := it.ArrivedAt
		if at.IsZero() {
			at = it.UpdatedAt
		}
		if !it.open() || it.Tier != TierAction || !inboxTaskLinkEligible(it) || excluded(it) || mutedBy(b.mutes, it) != nil || at.IsZero() || now.Sub(at) > 24*time.Hour || at.After(now.Add(time.Minute)) {
			continue
		}
		if it.Kind != KindGroup && it.Kind != KindMention || it.Lark != nil && !it.Lark.IsGroup {
			continue
		}
		if !softGroupPhrase(text, softPlace(it)) {
			continue
		}
		if it.Agent != nil && it.Agent.TodoID != "" && it.Agent.TodoID != task.ReuseTodo && it.Agent.TodoID != task.ReservedTodoID && it.Agent.TodoID != task.TodoID {
			blocked = blocked || softStrong(text, it)
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if blocked {
		return ids, false
	}
	var strong []string
	for _, id := range ids {
		if softStrong(text, b.items[id]) {
			strong = append(strong, id)
		}
	}
	if len(strong) == 0 {
		return ids, false
	}
	// A second topic with the same group is safe only when the input discriminates it. Sender-only matches hit both.
	for i, id := range strong {
		it := b.items[id]
		if it.Agent != nil && it.Agent.TodoID != "" && it.Agent.TodoID != task.ReuseTodo && it.Agent.TodoID != task.ReservedTodoID && it.Agent.TodoID != task.TodoID {
			return ids, false
		}
		for _, other := range strong[:i] {
			if !softEquivalent(it, b.items[other]) {
				return ids, false
			}
		}
	}
	return strong, true
}
func (b *inboxBook) softInboxVersion(ids []string) string {
	h := sha256.New()
	for _, id := range ids {
		it := *b.items[id]
		// Linking changes Agent, not the source evidence. Keep retries idempotent while ownership is checked separately.
		it.Agent = nil
		it.Related = nil
		it.ArrivedOf, it.ArrivedAt = "", time.Time{}
		raw, _ := json.Marshal(it)
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func (b *inboxBook) checkSoftInbox(t PlanTask, now time.Time, excluded func(*InboxItem) bool) error {
	ids, strong := b.softInboxCandidates(softTaskText(t), t, now, excluded)
	if !strong || len(ids) == 0 || !slices.Contains(ids, t.InboxItem) || b.softInboxVersion(ids) != t.InboxMatch {
		return i18n.Err("assistant-tasks.inbox_link_removed")
	}
	return nil
}

// A delayed Start never silently converts a stale soft match into an explicit source or another topic.
func (s *Service) refreshSoftInbox(ctx context.Context, t *PlanTask) {
	if t.InboxMatch == "" {
		return
	}
	cfg, err := s.inboxConfigFresh(ctx)
	b := &s.inbox
	b.mu.Lock()
	if err == nil {
		it := b.items[t.InboxItem]
		if it == nil || inboxVisibilityWithMembersExcludedLocked(b, it, cfg.Visibility) {
			err = fmt.Errorf("excluded")
		} else {
			err = b.checkSoftInbox(*t, time.Now(), func(it *InboxItem) bool { return inboxVisibilityWithMembersExcludedLocked(b, it, cfg.Visibility) })
		}
	}
	b.mu.Unlock()
	if err != nil {
		t.InboxItem, t.InboxMatch, t.InboxMatchText = "", "", ""
		t.InboxNotice = "automatic_link_removed"
	}
}

// Notes use the existing tool result; no new client view or source action is needed.
func (s *Service) inboxPlanNotes(ctx context.Context, tasks []PlanTask) string {
	var out strings.Builder
	for _, task := range tasks {
		if task.InboxNotice != "" {
			out.WriteString("\n" + i18n.T(s.lang(ctx), "assistant-tasks.inbox_link_removed"))
		}
		if task.InboxItem == "" && len(task.InboxCandidates) > 0 {
			out.WriteString("\n" + i18n.T(s.lang(ctx), "assistant-tasks.inbox_candidates", i18n.Args{"items": strings.Join(task.InboxCandidates, ", ")}))
		}
	}
	return out.String()
}

// Preserve both sources and their reply routes, exposing only the existing Related navigation.
// Under inbox.mu; primary already has a write snapshot owned by linkPlannedInbox.
func (b *inboxBook) linkSoftInboxRelated(t PlanTask, primary *InboxItem) []*inboxWrite {
	if t.InboxMatch == "" {
		return nil
	}
	var writes []*inboxWrite
	for _, id := range t.InboxCandidates {
		other := b.items[id]
		if other == nil || other == primary {
			continue
		}
		w := beginWrite(other)
		if relate(primary, other) {
			b.settle(w)
			writes = append(writes, w)
		}
	}
	return writes
}
